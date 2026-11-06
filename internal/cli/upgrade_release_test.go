package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// releaseTestKey is a minisign key pair of the test's own, in the
// shapes the fetch reads: the private half, the public half as the
// second line of a .pub file has it, and the key's id.
type releaseTestKey struct {
	priv ed25519.PrivateKey
	pub  string
	id   []byte
}

func newReleaseTestKey(t *testing.T) releaseTestKey {
	t.Helper()
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 8)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte("Ed"), id...)
	raw = append(raw, pubKey...)
	return releaseTestKey{priv: priv, pub: base64.StdEncoding.EncodeToString(raw), id: id}
}

// signChecksums is checksums.txt.minisig as minisign writes it, from
// the test's key: the prehashed signature, then the signature over it
// and the trusted comment.
func (k releaseTestKey) signChecksums(t *testing.T, checksums []byte) []byte {
	t.Helper()
	hash := blake2b.Sum512(checksums)
	sig := ed25519.Sign(k.priv, hash[:])
	const comment = "timestamp:1790000000"
	global := ed25519.Sign(k.priv, append(append([]byte{}, sig...), comment...))
	first := append([]byte("ED"), k.id...)
	first = append(first, sig...)
	return []byte(fmt.Sprintf("untrusted comment: signature from minisign secret key\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(first), comment, base64.StdEncoding.EncodeToString(global)))
}

// releaseArchiveBytes is a release archive holding yardr's bytes and
// the files a release holds beside it, as contrib/release/build packs.
func releaseArchiveBytes(t *testing.T, yardr []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{
		"yardr":       yardr,
		"server-yard": []byte("#!/bin/sh\necho server-yard\n"),
		"README.md":   []byte("# yardr\n"),
		"LICENSE":     []byte("FSL-1.1-ALv2\n"),
		"NOTICE":      []byte("notice\n"),
	} {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}
		if name != "yardr" && name != "server-yard" {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseOneMemberBytes is a release archive of the one member hdr with
// body. With oldNUL the member's type flag is the NUL one that tar wrote
// for a regular file before there was TypeReg: archive/tar's writer
// promotes it, so the header is patched after and its checksum made again.
func releaseOneMemberBytes(t *testing.T, hdr tar.Header, body []byte, oldNUL bool) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	hdr.Size = int64(len(body))
	if err := tw.WriteHeader(&hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b := raw.Bytes()
	if oldNUL {
		const typeAt, checksumAt = 156, 148 // in the header block: the type flag and the 8-byte checksum
		if b[typeAt] != tar.TypeReg {
			t.Fatalf("the header's type flag is %q, want TypeReg to patch", b[typeAt])
		}
		b[typeAt] = 0
		sum := 0
		for i, c := range b[:512] {
			if i >= checksumAt && i < checksumAt+8 { // the checksum counts its own field as spaces
				c = ' '
			}
			sum += int(c)
		}
		copy(b[checksumAt:checksumAt+8], fmt.Sprintf("%06o\x00 ", sum))
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseBuilt is one tiny Go binary's bytes, built once for the
// install and rollback tests. The tests used to read os.Executable, the
// race test binary itself at tens of megabytes, and compressing,
// serving, fetching and hashing it cost them some thirty seconds of the
// package's race budget. Any bytes would prove the install and the
// rollback, but the flow reads Go build info from what it installed, so
// the bytes are a real binary: a program of its own, built the way
// TestUpgradeInstall builds one. It is built before the scene stands the
// release road's stubs up, so the "no go" the road needs does not hide
// the go this needs.
var releaseBuilt = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "yardr-release")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, body := range map[string]string{
		"go.mod":  "module example.com/releaseyardr\n\ngo 1.21\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bin := filepath.Join(dir, "yardr")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build: %v\n%s", err, out)
	}
	return os.ReadFile(bin)
})

// releaseBuiltBin is the yardr a fixture release carries.
func releaseBuiltBin(t *testing.T) []byte {
	t.Helper()
	b, err := releaseBuilt()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// releaseSite is a yardr.dev of the test's own: it serves one release,
// signed with the test's key, and counts what was asked for. Spoiling
// a field serves the release wrong in that one way.
type releaseSite struct {
	version  string
	key      releaseTestKey
	archive  []byte // the archive checksums.txt names
	serve    []byte // the archive the site hands out; archive when unset
	signWith *releaseTestKey
	noSig    bool

	mu   sync.Mutex
	asks map[string]int
	srv  *httptest.Server
}

func (s *releaseSite) asked(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asks[path]
}

func newReleaseSite(t *testing.T, s *releaseSite) *releaseSite {
	t.Helper()
	if s.version == "" {
		s.version = "v1.2.3"
	}
	target := runtime.GOOS + "_" + runtime.GOARCH
	name := "yardr_" + strings.TrimPrefix(s.version, "v") + "_" + target + ".tar.gz"
	sum := sha256.Sum256(s.archive)
	// The release's checksums.txt also names its install.sh, which is no
	// archive: the upgrade looks its archive up among lines of other files.
	installer := sha256.Sum256([]byte("#!/bin/sh\n"))
	checksums := []byte(fmt.Sprintf("%s  %s\n%s  install.sh\n", hex.EncodeToString(sum[:]), name, hex.EncodeToString(installer[:])))
	signer := s.key
	if s.signWith != nil {
		signer = *s.signWith
	}
	sig := signer.signChecksums(t, checksums)
	served := s.serve
	if served == nil {
		served = s.archive
	}
	dir := "/releases/" + s.version
	latest, err := json.Marshal(map[string]any{
		"version":   s.version,
		"date":      "2026-10-31",
		"checksums": dir + "/checksums.txt",
		"signature": dir + "/checksums.txt.minisig",
		"targets": map[string]any{
			target: map[string]any{"path": dir + "/" + name, "size": len(s.archive), "sha256": hex.EncodeToString(sum[:])},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.asks = map[string]int{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.asks[r.URL.Path]++
		s.mu.Unlock()
		switch r.URL.Path {
		case "/releases/latest.json":
			_, _ = w.Write(latest)
		case dir + "/checksums.txt":
			_, _ = w.Write(checksums)
		case dir + "/checksums.txt.minisig":
			if s.noSig {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(sig)
		case dir + "/" + name:
			_, _ = w.Write(served)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// stubNoRelease points the release road at a site that does not
// answer, for a test of what a plain upgrade does with no depot
// named: the fetch fails, and nothing is installed.
func stubNoRelease(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	stub(t, &upgradeReleaseBase, url)
	stub(t, &upgradeReleaseHTTP, &http.Client{Timeout: 5 * time.Second})
	stub(t, &upgradeCurrentTag, func() string { return "" })
}

// releaseScene is a yard that takes the release road: a yard of the
// test's own, a binary it runs from holding the old build, and every
// outward step of the gate stood in for, as newUpgradeScene stands in
// for the source road. No depot is registered and lookPath finds no
// go: the road needs neither.
type releaseScene struct {
	a      *app
	home   string
	st     store.API
	target string
	old    []byte
	tag    string
	gate   func() error
}

func newReleaseScene(t *testing.T, site *releaseSite) *releaseScene {
	t.Helper()
	ctx := context.Background()
	home, st, mk := upgradeYard(t)
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.BackupDirSetting, filepath.Join(home, "backups"), "jo"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realTempDir(t), "yardr")
	old := []byte("old build")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	sc := &releaseScene{a: mk(), home: home, st: st, target: target, old: old}
	stub(t, &upgradeExecutable, func() (string, error) { return target, nil })
	stub(t, &lookPath, func(string) (string, error) { return "", errors.New("no go") })
	stub(t, &goos, runtime.GOOS)
	stub(t, &goarch, runtime.GOARCH)
	stub(t, &upgradeReleaseBase, site.srv.URL)
	stub(t, &upgradeReleasePubkey, site.key.pub)
	stub(t, &upgradeCurrentTag, func() string { return sc.tag })
	stub(t, &upgradeReleaseHTTP, site.srv.Client())
	stub(t, &upgradeCodesign, func(context.Context, string, string) (string, error) {
		t.Error("upgradeCodesign called with no identity set")
		return "", nil
	})
	stub(t, &serviceDir, func() (string, error) { return t.TempDir(), nil })
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return true })
	stub(t, &upgradeServerStart, func(context.Context, *config.Config) error { return nil })
	stub(t, &upgradeServerStop, func(*config.Config) error { return nil })
	stub(t, &upgradeStopDaemon, func(*config.Config, time.Duration) (int, error) { return 0, nil })
	stub(t, &upgradeGoBuild, func(context.Context, string, string) (string, error) {
		t.Error("upgradeGoBuild called on the release road")
		return "", errors.New("no build on the release road")
	})
	stub(t, &upgradeRunInstalled, func(_ context.Context, _, _ string, _ ...string) (string, error) {
		if sc.gate == nil {
			return "", nil
		}
		if err := sc.gate(); err != nil {
			return "server    fail: " + err.Error(), err
		}
		return "", nil
	})
	return sc
}

// upgrade runs plain yardr upgrade on the scene's yard: no --from, no
// setting, so the release road answers.
func (sc *releaseScene) upgrade(t *testing.T, flags ...string) (string, error) {
	t.Helper()
	return run(t, sc.a.rootCmd(), append([]string{"--actor", "jo", "upgrade"}, flags...)...)
}

// whole says the old binary still stands as it was: the same file,
// with the old bytes, alone in its directory but for yardr.prev where
// a run got as far as keeping it.
func (sc *releaseScene) whole(t *testing.T, before os.FileInfo) {
	t.Helper()
	after, err := os.Stat(sc.target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatalf("the old binary was replaced")
	}
	b, err := os.ReadFile(sc.target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(sc.old) {
		t.Fatalf("the old binary holds %q, want the old build whole", b)
	}
}

// The release road: a plain upgrade with no depot named fetches the
// latest release, proves it and installs its yardr through the gate,
// with no go on PATH and no checkout. The installed bytes are the
// fixture binary's, the previous binary is kept, and no temp stands
// beside the binary.
func TestUpgradeReleaseInstalls(t *testing.T) {
	binary := releaseBuiltBin(t)
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, binary)})
	sc := newReleaseScene(t, site)
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	for _, want := range []string{
		fmt.Sprintf("%-9s  %s (%s)\n", "release", site.version, "yardr_"+strings.TrimPrefix(site.version, "v")+"_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz"),
		fmt.Sprintf("%-9s  %s at ", "install", sc.target),
		"scripts    current\n",
		"health     ok in",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the output lacks %q:\n%s", want, out)
		}
	}
	if os.SameFile(before, mustStat(t, sc.target)) {
		t.Fatalf("the binary was written in place, not renamed over")
	}
	got, err := os.ReadFile(sc.target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("the binary is not the release's yardr")
	}
	prev, err := os.ReadFile(filepath.Join(filepath.Dir(sc.target), "yardr.prev"))
	if err != nil || string(prev) != string(sc.old) {
		t.Fatalf("yardr.prev = %q, %v; want the old build kept", prev, err)
	}
	for _, name := range dirNames(filepath.Dir(sc.target)) {
		if strings.HasPrefix(name, ".") {
			t.Fatalf("%s stands beside the binary", name)
		}
	}
	if _, err := os.Stat(config.At(sc.home).UpgradeGate()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gate file stands after a passed gate: %v", err)
	}
}

// A tampered archive — bytes other than the ones the signed checksums
// name — is refused at the checksum, before any write: the old binary
// stands whole, with no previous binary kept and no gate file, since
// the run never reached the install.
func TestUpgradeReleaseTamperedArchive(t *testing.T) {
	key := newReleaseTestKey(t)
	good := releaseArchiveBytes(t, []byte("the makers' yardr"))
	served := releaseArchiveBytes(t, []byte("someone else's yardr"))
	site := newReleaseSite(t, &releaseSite{key: key, archive: good, serve: served})
	sc := newReleaseScene(t, site)
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err == nil || !strings.Contains(err.Error(), "its checksum differs") {
		t.Fatalf("upgrade = %v, want the checksum named\n%s", err, out)
	}
	sc.whole(t, before)
	if _, err := os.Stat(filepath.Join(filepath.Dir(sc.target), "yardr.prev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a previous binary was kept by a run that installed nothing: %v", err)
	}
	if _, err := os.Stat(config.At(sc.home).UpgradeGate()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gate file stands: %v", err)
	}
}

// A release is the operator's to install, as a named depot is: anyone
// else is refused before the site is asked for anything, and the old
// binary stands.
func TestUpgradeReleaseOperatorOnly(t *testing.T) {
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr"))})
	sc := newReleaseScene(t, site)
	before := mustStat(t, sc.target)

	out, err := run(t, sc.a.rootCmd(), "--actor", "hal", "upgrade")

	if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may install the latest signed release; this is hal") {
		t.Fatalf("upgrade = %v, want the operator named\n%s", err, out)
	}
	if len(site.asks) != 0 {
		t.Fatalf("asked the site for %v before the refusal", site.asks)
	}
	sc.whole(t, before)
}

// A signature the release key did not make — here, one made with
// another key — is refused before the archive is asked for: the old
// binary stands whole.
func TestUpgradeReleaseBadSignature(t *testing.T) {
	key := newReleaseTestKey(t)
	other := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr")), signWith: &other})
	sc := newReleaseScene(t, site)
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err == nil || !strings.Contains(err.Error(), "not that of yardr's makers") {
		t.Fatalf("upgrade = %v, want the signature named\n%s", err, out)
	}
	target := runtime.GOOS + "_" + runtime.GOARCH
	archivePath := "/releases/" + site.version + "/yardr_" + strings.TrimPrefix(site.version, "v") + "_" + target + ".tar.gz"
	if n := site.asked(archivePath); n != 0 {
		t.Fatalf("the archive was fetched %d times behind a bad signature", n)
	}
	sc.whole(t, before)
}

// An unsigned release is refused before the archive is asked for.
func TestUpgradeReleaseUnsigned(t *testing.T) {
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr")), noSig: true})
	sc := newReleaseScene(t, site)
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err == nil || !strings.Contains(err.Error(), "is not signed") {
		t.Fatalf("upgrade = %v, want the missing signature named\n%s", err, out)
	}
	sc.whole(t, before)
}

// Already on the latest release: the run says so, fetches nothing
// past latest.json, installs nothing and leaves the old binary whole.
func TestUpgradeReleaseAlreadyLatest(t *testing.T) {
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr"))})
	sc := newReleaseScene(t, site)
	sc.tag = site.version
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	if want := fmt.Sprintf("%-9s  already on %s, the latest release\n", "release", site.version); !strings.Contains(out, want) {
		t.Fatalf("the output lacks %q:\n%s", want, out)
	}
	if n := len(site.asks); n != 1 || site.asked("/releases/latest.json") != 1 {
		t.Fatalf("asked for %v, want latest.json alone", site.asks)
	}
	sc.whole(t, before)
	if _, err := os.Stat(filepath.Join(filepath.Dir(sc.target), "yardr.prev")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a previous binary was kept: %v", err)
	}
}

// A latest release older than this build is no upgrade: refused, old
// binary whole.
func TestUpgradeReleaseOlderIsNotInstalled(t *testing.T) {
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr"))})
	sc := newReleaseScene(t, site)
	sc.tag = "v1.2.4"
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err == nil || !strings.Contains(err.Error(), "older than this build") {
		t.Fatalf("upgrade = %v, want the older release refused\n%s", err, out)
	}
	sc.whole(t, before)
}

// No network: the site does not answer, the run fails saying so, and
// the old binary stands whole.
func TestUpgradeReleaseNoNetwork(t *testing.T) {
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, []byte("yardr"))})
	sc := newReleaseScene(t, site)
	// The site the scene points at goes away before the run.
	site.srv.Close()
	stub(t, &upgradeReleaseHTTP, &http.Client{Timeout: 5 * time.Second})
	before := mustStat(t, sc.target)

	out, err := sc.upgrade(t)

	if err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("upgrade = %v, want the unreachable site named\n%s", err, out)
	}
	sc.whole(t, before)
}

// A gate that fails on the release road rolls back as on the source
// road: the previous binary is put back and the run says it rolled
// back. The release it came from makes no difference here, which is
// the gate's one shape for both roads.
func TestUpgradeReleaseGateFailureRollsBack(t *testing.T) {
	binary := releaseBuiltBin(t)
	key := newReleaseTestKey(t)
	site := newReleaseSite(t, &releaseSite{key: key, archive: releaseArchiveBytes(t, binary)})
	sc := newReleaseScene(t, site)
	sc.gate = func() error { return errors.New("the new build does not answer") }

	out, err := sc.upgrade(t, "--timeout", "50ms")

	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("upgrade = %v, want a rollback\n%s", err, out)
	}
	got, err := os.ReadFile(sc.target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sc.old) {
		t.Fatalf("the binary holds %q after the rollback, want the old build back", got)
	}
	if !strings.Contains(out, "scripts    current\n") {
		t.Fatalf("no scripts line:\n%s", out)
	}
}

// The verifier against a signature the real minisign made: a key pair
// and a signature from `minisign -G -W` and `minisign -S` on this
// machine, held here as constants, so the in-process check is held to
// minisign's own bytes rather than to this package's signer. The same
// signature over other checksums fails.
func TestMinisignVerifyKnownAnswer(t *testing.T) {
	t.Parallel()
	const pub = "RWSt4SceCdbv58WJTAIlxdnrBnvIL9/nofAQXHMW3GnjKhGp3AdSYZfe"
	checksums := []byte("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  yardr_1.2.3_darwin_arm64.tar.gz\n")
	sig := []byte("untrusted comment: signature from minisign secret key\n" +
		"RUSt4SceCdbv53bP36nhnbPqGL/WogwTKo7GJuWSUblswTMGqItMjz+VqQVmqKMwaZIgz/f8Ou0fTldjqlNoCRR9p8cuSAweQgk=\n" +
		"trusted comment: timestamp:2026-10-31\n" +
		"SuCOEt+eggxvhZNPLC4bMsyEAnLNOtCqsjah+h1ZVGHD3sbsWYC6FZbtkQxQkG+BvqZXBSof7PCgmcZHdXssCQ==\n")
	key, id, err := parseMinisignKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	// The key built into the binary parses as one too, and the fetch
	// starts from the one named host.
	if _, _, err := parseMinisignKey(upgradeReleaseKey); err != nil {
		t.Fatalf("the built-in release key does not parse: %v", err)
	}
	if upgradeReleaseBase != upgradeReleaseSite || upgradeReleaseSite != "https://yardr.dev" {
		t.Fatalf("the release fetch starts from %q, want https://yardr.dev alone", upgradeReleaseBase)
	}
	if err := verifyMinisign(key, id, checksums, sig); err != nil {
		t.Fatalf("minisign's own signature does not verify: %v", err)
	}
	tampered := bytes.Replace(checksums, []byte("e3b0"), []byte("e3b1"), 1)
	if err := verifyMinisign(key, id, tampered, sig); err == nil || !strings.Contains(err.Error(), "not that of yardr's makers") {
		t.Fatalf("verify of tampered checksums = %v, want a bad signature", err)
	}
	// The old, non-prehashed kind is refused even though its "Ed"
	// prefix parses as a key's.
	legacy := bytes.Replace(sig, []byte("RUSt"), []byte("RWSt"), 1)
	if err := verifyMinisign(key, id, checksums, legacy); err == nil {
		t.Fatalf("a legacy signature verified")
	}
}

// The archive and checksums readers refuse what a release does not
// hold, apart from any fetch.
func TestReleaseMemberAndListedSum(t *testing.T) {
	t.Parallel()
	good := releaseArchiveBytes(t, []byte("yardr bytes"))
	bin, err := releaseMember(good)
	if err != nil || string(bin) != "yardr bytes" {
		t.Fatalf("member = %q, %v", bin, err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"yardr": "x", "evil.sh": "echo hi\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseMember(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "evil.sh") {
		t.Fatalf("member with an extra file = %v, want it named refused", err)
	}
	// A member is a regular file or the archive is refused. The old NUL
	// type flag of a regular file reads as TypeReg, so it is still taken,
	// and a link or a directory under the binary's name is not.
	bin, err = releaseMember(releaseOneMemberBytes(t, tar.Header{Name: "yardr", Mode: 0o755}, []byte("legacy bytes"), true))
	if err != nil || string(bin) != "legacy bytes" {
		t.Fatalf("member with the old NUL type flag = %q, %v, want it taken", bin, err)
	}
	for _, hdr := range []tar.Header{
		{Name: "yardr", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"},
		{Name: "yardr", Typeflag: tar.TypeDir, Mode: 0o755},
	} {
		if _, err := releaseMember(releaseOneMemberBytes(t, hdr, nil, false)); err == nil || !strings.Contains(err.Error(), "its yardr is no regular file") {
			t.Fatalf("member of type %q = %v, want it refused as no regular file", hdr.Typeflag, err)
		}
	}
	sum := sha256.Sum256(good)
	sums := []byte(fmt.Sprintf("%s  other.tar.gz\n%s *yardr_1.2.3_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])))
	got, err := releaseListedSum(sums, "yardr_1.2.3_linux_amd64.tar.gz")
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("listed sum = %q, %v", got, err)
	}
	if _, err := releaseListedSum(sums, "missing.tar.gz"); err == nil || !strings.Contains(err.Error(), "do not name") {
		t.Fatalf("listed sum for a missing name = %v", err)
	}
	if !slices.Contains(releaseBeside, "server-yard") {
		t.Fatalf("releaseBeside lost server-yard")
	}
}
