package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/blake2b"

	"yardr.dev/yardr/internal/version"
)

// The release road of yardr upgrade: for a yardr with no checkout and no
// Go, the latest release is fetched from yardr.dev, proved to be its
// makers', and written over the binary this yardr runs from, through
// the same gate as a build from a checkout. The verification follows
// footplate's fetch (its src/fetch.rs): the signature is checked before
// the archive is downloaded, latest.json is discovery only, and nothing
// is trusted because it says so.

// upgradeReleaseSite is the one host a release is fetched from. Every
// address the fetch asks for is this host plus a path under /releases/
// that the checks below have vetted, so a latest.json that names
// another host, or a path outside /releases/, fetches nothing.
const upgradeReleaseSite = "https://yardr.dev"

// upgradeReleaseKey is the public half of the minisign key yardr's
// releases are signed with (id 0ECCFE6E81F150B3), as the second line
// of its .pub file has it and docs/install.md prints it. It is in the
// binary because the secret half stays with whoever cuts the releases:
// a new key is a new yardr, as it is a new footplate.
const upgradeReleaseKey = "RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB"

// upgradeReleaseBase is upgradeReleaseSite as a test reaches it: a
// fixture release stands at another address, with a key of its own.
// Production runs never change it, so the one-host rule holds there.
var upgradeReleaseBase = upgradeReleaseSite

// upgradeReleasePubkey is the key the fetch verifies with: the
// embedded one, or a fixture's in a test.
var upgradeReleasePubkey = upgradeReleaseKey

// upgradeReleaseHTTP fetches a release. A redirect is a refusal, as in
// footplate: the site serves each file directly, and following a
// redirect would leave the one named host. The bound is for a host
// that hangs, sized for the archive on a slow link.
var upgradeReleaseHTTP = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// upgradeCurrentTag is the release this build was made for, "" for a
// build from a checkout; tests name one.
var upgradeCurrentTag = version.Tag

// What is read of latest.json, of the checksums and of the signature,
// of an archive, and of what an archive unpacks to. An archive of
// yardr 0.10 is 9 MB and unpacks to 21, as footplate's fetch says.
const (
	releaseLatestMost    = 64 * 1024
	releaseChecksumsMost = 64 * 1024
	releaseSignatureMost = 4 * 1024
	releaseArchiveMost   = 64 * 1024 * 1024
	releaseUnpackedMost  = 256 * 1024 * 1024
)

// errReleaseCurrent says this yardr already runs the latest release:
// no failure, and nothing is fetched past latest.json.
var errReleaseCurrent = errors.New("already on the latest release")

// releaseCurrentVersion is the version an errReleaseCurrent names.
func releaseCurrentVersion(err error) string {
	s := err.Error()
	i := strings.LastIndex(s, "(")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return s
	}
	return s[i+1 : len(s)-1]
}

// fetchedRelease is a release proved to be its makers', unpacked: the
// tag it is, the archive it came from, and the yardr binary it held.
type fetchedRelease struct {
	version string
	archive string
	binary  []byte
}

// releaseLatest is latest.json as sync-release writes it, of which
// only what leads to the files is read. Its per-target size and sha256
// are for a person: never this fetch's trust, which is the signed
// checksums alone.
type releaseLatest struct {
	Version   string                   `json:"version"`
	Checksums string                   `json:"checksums"`
	Targets   map[string]releaseTarget `json:"targets"`
}

type releaseTarget struct {
	Path string `json:"path"`
}

// upgradeFetchRelease reads the latest release from yardr.dev and
// proves it, in footplate's order. It writes nothing: a caller that
// installs the result does so only once this has answered, so a
// release that fails anywhere here leaves the old binary whole and
// the store untouched.
func upgradeFetchRelease(ctx context.Context) (*fetchedRelease, error) {
	// Before anything is asked of the site: without the key nothing it
	// answers could be kept.
	pub, keyID, err := parseMinisignKey(upgradeReleasePubkey)
	if err != nil {
		return nil, fmt.Errorf("this yardr was built without the key yardr's releases are signed with, so it cannot tell whether a release is the real one, and fetches none")
	}
	target, err := releaseTargetName()
	if err != nil {
		return nil, err
	}
	latestBytes, err := releaseGet(ctx, "/releases/latest.json", releaseLatestMost)
	if err != nil {
		return nil, err
	}
	var latest releaseLatest
	if err := json.Unmarshal(latestBytes, &latest); err != nil {
		return nil, fmt.Errorf("yardr.dev's /releases/latest.json cannot be read: %v; yardr was not fetched", err)
	}
	ver, ok := parseReleaseVersion(latest.Version)
	if !ok {
		return nil, fmt.Errorf("yardr.dev's /releases/latest.json names %q, which is no version of a release; yardr was not fetched", latest.Version)
	}
	if cur, ok := parseReleaseVersion(upgradeCurrentTag()); ok {
		switch cmp := compareReleaseVersion(ver, cur); {
		case cmp == 0:
			return nil, fmt.Errorf("%w (%s)", errReleaseCurrent, latest.Version)
		case cmp < 0:
			return nil, fmt.Errorf("the latest release is %s, older than this build (%s): not installing it, and the old binary is left as it is", latest.Version, upgradeCurrentTag())
		}
	}
	info, ok := latest.Targets[target]
	if !ok {
		return nil, fmt.Errorf("there is no yardr for this system (%s) in release %s: its releases are for macOS and Linux, on arm64 and amd64; yardr was not fetched", target, latest.Version)
	}
	// The name is made here from the version, and not taken from the
	// path: it is what the signed checksums are asked for, so that the
	// version latest.json claims is the version that was signed.
	archive := "yardr_" + strings.TrimPrefix(latest.Version, "v") + "_" + target + ".tar.gz"
	if name := releaseFileUnder(info.Path); name != archive {
		return nil, fmt.Errorf("yardr.dev's /releases/latest.json names %q for %s, and the archive of %s is %s under /releases/; yardr was not fetched", info.Path, target, latest.Version, archive)
	}
	if releaseFileUnder(latest.Checksums) == "" {
		return nil, fmt.Errorf("yardr.dev's /releases/latest.json names %q for the checksums, which is no file under /releases/; yardr was not fetched", latest.Checksums)
	}

	// The signature before the archive: a release that is not signed
	// costs no download.
	checksums, err := releaseGet(ctx, latest.Checksums, releaseChecksumsMost)
	if err != nil {
		return nil, err
	}
	sigPath := latest.Checksums + ".minisig"
	sig, err := releaseGet(ctx, sigPath, releaseSignatureMost)
	if err != nil {
		var answered releaseAnswered
		if errors.As(err, &answered) && answered.status == http.StatusNotFound {
			return nil, fmt.Errorf("the release of yardr at %s is not signed, so nothing shows who made it; yardr was not fetched", upgradeReleaseBase)
		}
		return nil, err
	}
	if err := verifyMinisign(pub, keyID, checksums, sig); err != nil {
		return nil, err
	}
	want, err := releaseListedSum(checksums, archive)
	if err != nil {
		return nil, err
	}

	archiveBytes, err := releaseGet(ctx, info.Path, releaseArchiveMost)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(archiveBytes); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("%s is not the file yardr's makers signed: its checksum differs; it is not kept, and the old binary is left as it is", archive)
	}
	binary, err := releaseMember(archiveBytes)
	if err != nil {
		return nil, fmt.Errorf("%s is not what a release holds: %s; nothing of it is kept, and the old binary is left as it is", archive, err)
	}
	return &fetchedRelease{version: latest.Version, archive: archive, binary: binary}, nil
}

// releaseTargetName is this system as a release names it, Go's GOOS
// and GOARCH joined as the archives join them. Windows is left out of
// this road, as the bead has it.
func releaseTargetName() (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("there is no yardr for this system (%s/%s): its releases are for macOS and Linux, on arm64 and amd64; yardr was not fetched", goos, goarch)
	}
	switch goarch {
	case "arm64", "amd64":
	default:
		return "", fmt.Errorf("there is no yardr for this system (%s/%s): its releases are for macOS and Linux, on arm64 and amd64; yardr was not fetched", goos, goarch)
	}
	return goos + "_" + goarch, nil
}

// releaseVersion is a release's tag as three numbers.
type releaseVersion struct{ major, minor, patch int }

// parseReleaseVersion reads a tag v1.2.3 and nothing more: where a
// pre-release would stand against this build is not guessed at.
func parseReleaseVersion(s string) (releaseVersion, bool) {
	var v releaseVersion
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return v, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := []*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return v, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		*nums[i] = n
	}
	return v, true
}

func compareReleaseVersion(a, b releaseVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// releaseAnswered is the site answering with any status but 200: the
// file is not served, directly or at all.
type releaseAnswered struct {
	path   string
	status int
}

func (e releaseAnswered) Error() string {
	return fmt.Sprintf("%s did not serve %s directly (status %d), so yardr was not fetched", upgradeReleaseBase, e.path, e.status)
}

// releaseGet reads the file the site has at path, of most bytes at the
// most. path is the site's word, vetted by releaseFileUnder where it
// came from latest.json, and the constant /releases/latest.json where
// it did not: both stay under /releases/ on the one named host.
func releaseGet(ctx context.Context, path string, most int64) ([]byte, error) {
	if releaseFileUnder(path) == "" {
		return nil, fmt.Errorf("%q is no file under /releases/ on %s; yardr was not fetched", path, upgradeReleaseBase)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(upgradeReleaseBase, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := upgradeReleaseHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s does not answer, so yardr was not fetched (%v); this needs the network once, and the old binary is left as it is", upgradeReleaseBase, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body of a refusal is not read as the file it is not.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, releaseAnswered{path: path, status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, most+1))
	if err != nil {
		return nil, fmt.Errorf("%s's answer for %s could not be read, so yardr was not fetched: %w", upgradeReleaseBase, path, err)
	}
	if int64(len(b)) > most {
		return nil, fmt.Errorf("%s's %s is larger than %d bytes, so yardr was not fetched", upgradeReleaseBase, path, most)
	}
	return b, nil
}

// releaseFileUnder is the file's name, when path is a file under
// /releases/ written in letters, digits, ., _, - and / alone, with no
// empty or dotted part. The path goes into the first line of a
// request as it is, so nothing else is let through.
func releaseFileUnder(path string) string {
	rest, ok := strings.CutPrefix(path, "/releases/")
	if !ok {
		return ""
	}
	for _, b := range []byte(rest) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-' || b == '/') {
			return ""
		}
	}
	parts := strings.Split(rest, "/")
	for _, p := range parts {
		if p == "" || strings.HasPrefix(p, ".") {
			return ""
		}
	}
	return parts[len(parts)-1]
}

// parseMinisignKey reads a minisign public key's base64: "Ed", the
// key's id (8 bytes) and the key (32).
func parseMinisignKey(s string) (ed25519.PublicKey, []byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != 42 || string(b[:2]) != "Ed" {
		return nil, nil, fmt.Errorf("no minisign key")
	}
	return ed25519.PublicKey(b[10:]), b[2:10], nil
}

// verifyMinisign reports whether sigFile, a checksums.txt.minisig,
// is the key's signature over checksums. The file is four lines: an
// untrusted comment, the signature, a trusted comment, and a second
// signature over the first and that comment. The signature is the
// prehashed kind minisign writes now ("ED", over the file's
// BLAKE2b-512); the old kind ("Ed", over the file itself) is a bad
// signature here, as no release has one. Verification is in this
// process, with the standard Ed25519: no minisign program is run.
func verifyMinisign(pub ed25519.PublicKey, keyID, checksums, sigFile []byte) error {
	bad := func(why string) error {
		return fmt.Errorf("the signature of yardr's release is not that of yardr's makers (%s); what was fetched is not kept, and the old binary is left as it is", why)
	}
	lines := strings.Split(string(sigFile), "\n")
	if len(lines) < 4 {
		return bad("it is no minisign signature")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(sig) != 74 {
		return bad("it is no minisign signature")
	}
	if string(sig[:2]) != "ED" {
		return bad("it is no prehashed minisign signature")
	}
	if !bytes.Equal(sig[2:10], keyID) {
		return bad("it was not made with the release key")
	}
	comment, ok := strings.CutPrefix(strings.TrimRight(lines[2], "\r"), "trusted comment: ")
	if !ok {
		return bad("it is no minisign signature")
	}
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return bad("it is no minisign signature")
	}
	hash := blake2b.Sum512(checksums)
	if !ed25519.Verify(pub, hash[:], sig[10:]) {
		return bad("it does not verify the checksums")
	}
	signed := make([]byte, 0, len(sig[10:])+len(comment))
	signed = append(signed, sig[10:]...)
	signed = append(signed, comment...)
	if !ed25519.Verify(pub, signed, global) {
		return bad("its trusted comment does not verify")
	}
	return nil
}

// releaseListedSum is the SHA-256 the signed checksums have for the
// file name, in small hex. The checksums are lines of sha256sum: the
// sum, then the name, which a star may mark as read in binary.
func releaseListedSum(checksums []byte, name string) (string, error) {
	text := string(checksums)
	var found string
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("the release's checksums name %s twice, so yardr was not fetched", name)
		}
		found = fields[0]
	}
	if found == "" {
		return "", fmt.Errorf("the checksums yardr's makers signed do not name %s; yardr was not fetched", name)
	}
	if len(found) != 64 {
		return "", fmt.Errorf("%s has no SHA-256 in the release's checksums, so yardr was not fetched", name)
	}
	for _, c := range found {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", fmt.Errorf("%s has no SHA-256 in the release's checksums, so yardr was not fetched", name)
		}
	}
	return strings.ToLower(found), nil
}

// releaseBeside is what a release's archive holds beside the binary:
// the README, the licence and the server-yard script. None of it is
// kept by the upgrade. Anything else in the archive is refused.
var releaseBeside = []string{"server-yard", "README.md", "LICENSE", "NOTICE"}

// releaseMember takes the yardr member out of a gzipped tar archive,
// in memory: the archive may hold the regular files of releaseBeside
// too, and nothing else — no other name, no directory, no link, no
// name twice. What it unpacks to is read to releaseUnpackedMost.
func releaseMember(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("it is no gzipped archive (%v)", err)
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var found []byte
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("it is no tar archive (%v)", err)
		}
		if hdr.Name != "yardr" && !slices.Contains(releaseBeside, hdr.Name) {
			return nil, fmt.Errorf("it holds %q, which is not expected in it", hdr.Name)
		}
		// The reader reports a regular file written with the old NUL type
		// flag as TypeReg already, so TypeReg is the one type it can be.
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("its %s is no regular file", hdr.Name)
		}
		if seen[hdr.Name] {
			return nil, fmt.Errorf("it holds %s twice", hdr.Name)
		}
		seen[hdr.Name] = true
		b, err := io.ReadAll(io.LimitReader(tr, releaseUnpackedMost-total+1))
		if err != nil {
			return nil, fmt.Errorf("its %s could not be read (%v)", hdr.Name, err)
		}
		total += int64(len(b))
		if total > releaseUnpackedMost {
			return nil, fmt.Errorf("it unpacks to more than %d MB", releaseUnpackedMost/(1024*1024))
		}
		if hdr.Name == "yardr" {
			found = b
		}
	}
	if len(found) == 0 {
		if !seen["yardr"] {
			return nil, errors.New("it holds no yardr")
		}
		return nil, errors.New("its yardr is empty")
	}
	return found, nil
}

// upgradeInstallRelease writes the fetched binary over target as the
// source road writes its build: to a temp file beside it, with the
// mode of the file it replaces, renamed over it, so the server's and
// the daemon's watch of the path sees one complete new file and never
// one still being written. The temp is removed on every path out: a
// failure leaves target as it was and nothing beside it.
func upgradeInstallRelease(target string, binary []byte) error {
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".build-*")
	if err != nil {
		return fmt.Errorf("the new build cannot be written beside %s: %w", target, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(tmp.Name(), binary, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}
