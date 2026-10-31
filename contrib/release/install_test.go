// Tests for install.sh, the one way yardr is installed. The script runs the
// way a user runs it, under a HOME and a TMPDIR of the test's own and a PATH
// that holds the tools it is known to use and nothing else, so a tool it
// starts to need shows as a failure here and the machine's own minisign, Go
// or yardr cannot change an answer. curl is a stand-in that serves
// https://yardr.dev/<path> from a fixture release and refuses every other
// host, so "from yardr.dev only" is asserted from what was asked of it; no
// test reaches the network, and the script has no knob for a mirror or a key.
// What the stand-ins were asked is in one log, in order.
package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const installTag = "v9.8.7"

var installTargets = []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64"}

func installArchive(target string) string {
	return "yardr_" + strings.TrimPrefix(installTag, "v") + "_" + target + ".tar.gz"
}

// fixtureYardr is the yardr an archive holds: a script that says which
// archive it came from.
func fixtureYardr(target string) string {
	return "#!/bin/sh\necho \"" + installTag + " (fixture " + target + ")\"\n"
}

// packArchive is a release archive of these members: yardr and server-yard
// executable, the rest not.
func packArchive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		mode := int64(0o644)
		if name == "yardr" || name == "server-yard" {
			mode = 0o755
		}
		body := members[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body))}); err != nil {
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
	return buf.Bytes()
}

// fixtureSite is a release laid out as yardr.dev serves one: latest.json, and
// under releases/<tag>/ the four archives, checksums.txt and its signature.
type fixtureSite struct {
	t   *testing.T
	dir string // what https://yardr.dev/ serves
}

func (s *fixtureSite) tagDir() string { return filepath.Join(s.dir, "releases", installTag) }

func (s *fixtureSite) write(rel, body string) {
	s.t.Helper()
	path := filepath.Join(s.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fixtureSite) read(rel string) string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(rel)))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

// edit changes a file of the site.
func (s *fixtureSite) edit(rel string, f func(string) string) {
	s.t.Helper()
	s.write(rel, f(s.read(rel)))
}

// newFixtureSite makes a whole release: archives holding the five members a
// real one does, checksums.txt over them and latest.json in the shape the site
// writes, signed by nothing (a placeholder signature the stand-ins and the
// signature tests replace).
func newFixtureSite(t *testing.T) *fixtureSite {
	t.Helper()
	s := &fixtureSite{t: t, dir: t.TempDir()}
	for _, target := range installTargets {
		s.archive(target, map[string]string{
			"yardr": fixtureYardr(target), "server-yard": "#!/bin/sh\n",
			"README.md": "readme\n", "LICENSE": "license\n", "NOTICE": "notice\n",
		})
	}
	s.publish()
	return s
}

// archive replaces one target's archive; publish records it afterwards.
func (s *fixtureSite) archive(target string, members map[string]string) {
	s.t.Helper()
	s.write("releases/"+installTag+"/"+installArchive(target), string(packArchive(s.t, members)))
}

// publish writes checksums.txt and latest.json from the archives as they are.
func (s *fixtureSite) publish() {
	s.t.Helper()
	var sums, targets strings.Builder
	for i, target := range installTargets {
		body := s.read("releases/" + installTag + "/" + installArchive(target))
		sum := sha256.Sum256([]byte(body))
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), installArchive(target))
		if i > 0 {
			targets.WriteString(",\n")
		}
		fmt.Fprintf(&targets, "    %q: {\n      \"path\": \"/releases/%s/%s\",\n      \"size\": %d,\n      \"sha256\": %q\n    }",
			target, installTag, installArchive(target), len(body), hex.EncodeToString(sum[:]))
	}
	s.write("releases/"+installTag+"/checksums.txt", sums.String())
	s.write("releases/"+installTag+"/checksums.txt.minisig", "untrusted comment: fixture\nsignature\n")
	s.write("releases/latest.json", fmt.Sprintf(`{
  "version": %q,
  "date": "2026-10-25",
  "checksums": "/releases/%[2]s/checksums.txt",
  "signature": "/releases/%[2]s/checksums.txt.minisig",
  "targets": {
%[3]s
  }
}
`, installTag, installTag, targets.String()))
}

const standInCurl = `#!/bin/sh
# The last argument is the URL. Without --fail a missing file is a page with a
# 200, as curl keeps it; with it, an error status.
for url; do :; done
out=
fail=
prev=
writeout=
for arg; do
  case $arg in --fail | -f) fail=1 ;; esac
  [ "$prev" != --output ] || out=$arg
  [ "$prev" != --write-out ] || writeout=$arg
  prev=$arg
done
printf 'curl %s\n' "$*" >>"$CALLS"
case $url in
https://yardr.dev/*) ;;
*)
  echo "curl: stand-in: $url is not on yardr.dev" >&2
  exit 7
  ;;
esac
file=$FIXTURE/${url#https://yardr.dev/}
if [ ! -f "$file" ]; then
  if [ -n "$fail" ]; then
    echo "curl: (22) The requested URL returned error: 404" >&2
    exit 22
  fi
  echo "<html>404</html>" >"$out"
  exit 0
fi
status=200
[ ! -f "$file.status" ] || status=$(cat "$file.status")
case $status in
4?? | 5??)
  if [ -n "$fail" ]; then
    echo "curl: (22) The requested URL returned error: $status" >&2
    exit 22
  fi
  ;;
esac
cat "$file" >"$out"
if [ "$writeout" = '%{http_code}' ]; then
  printf '%s' "$status"
fi
`

const standInUname = `#!/bin/sh
case "$1" in
-m) echo "$STANDIN_ARCH" ;;
*) echo "$STANDIN_OS" ;;
esac
`

// standInSysctl knows the one key the script asks, as an Apple silicon Mac
// answers it, and fails for it as an Intel Mac does.
const standInSysctl = `#!/bin/sh
if [ "$1 $2" = "-n hw.optional.arm64" ] && [ -n "${STANDIN_ARM64:-}" ]; then
  echo "$STANDIN_ARM64"
  exit 0
fi
echo "sysctl: unknown oid" >&2
exit 1
`

// standInMinisign verifies as its exit status says.
const standInMinisign = `#!/bin/sh
printf 'minisign %s\n' "$*" >>"$CALLS"
[ -z "${STANDIN_MINISIGN_ERR:-}" ] || echo "$STANDIN_MINISIGN_ERR" >&2
exit "${STANDIN_MINISIGN_RC:-0}"
`

// installTools are the programs the script is known to run, and so the whole
// PATH it gets besides the stand-ins. gzip is for a GNU tar, which runs it.
var installTools = []string{"awk", "cat", "chmod", "grep", "gzip", "mkdir", "mktemp", "mv", "rm", "tar", "sha256sum", "shasum"}

// installWorld is one run's world: a HOME and a TMPDIR that start empty, the
// PATH, and the environment the stand-ins read.
type installWorld struct {
	t      *testing.T
	site   *fixtureSite
	shell  string
	script string
	home   string
	tmp    string
	bin    string
	cwd    string
	calls  string
	env    []string
}

func newInstallWorld(t *testing.T, site *fixtureSite) *installWorld {
	t.Helper()
	script, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	w := &installWorld{
		t: t, site: site, shell: shell, script: script,
		home: filepath.Join(root, "home"), tmp: filepath.Join(root, "tmp"), bin: filepath.Join(root, "bin"),
		cwd: filepath.Join(root, "cwd"), calls: filepath.Join(root, "calls"),
	}
	for _, dir := range []string{w.home, w.tmp, w.bin, w.cwd} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var sums int
	for _, name := range installTools {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if strings.HasPrefix(name, "sha") {
			sums++
		}
		if err := os.Symlink(p, filepath.Join(w.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	if sums == 0 {
		t.Fatal("neither sha256sum nor shasum is on PATH")
	}
	for name, body := range map[string]string{"curl": standInCurl, "uname": standInUname, "sysctl": standInSysctl} {
		w.standIn(name, body)
	}
	w.env = []string{
		"HOME=" + w.home, "TMPDIR=" + w.tmp, "PATH=" + w.bin,
		"FIXTURE=" + site.dir, "CALLS=" + w.calls,
		"STANDIN_OS=Linux", "STANDIN_ARCH=x86_64",
	}
	return w
}

func (w *installWorld) standIn(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.bin, name), []byte(body), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

// setenv sets a variable for the next runs; the last setting wins.
func (w *installWorld) setenv(kv ...string) { w.env = append(w.env, kv...) }

// minisign puts the stand-in on PATH.
func (w *installWorld) minisign() { w.standIn("minisign", standInMinisign) }

// realMinisign puts the machine's own on PATH, and skips the test without one.
func (w *installWorld) realMinisign() {
	w.t.Helper()
	if err := os.Symlink(mustLookPath(w.t, "minisign"), filepath.Join(w.bin, "minisign")); err != nil {
		w.t.Fatal(err)
	}
}

// pathAdd puts a directory last on PATH.
func (w *installWorld) pathAdd(dir string) {
	w.setenv("PATH=" + w.bin + string(os.PathListSeparator) + dir)
}

func (w *installWorld) localBin() string { return filepath.Join(w.home, ".local", "bin") }

// run runs the script as a user does, by path, and returns what it printed.
func (w *installWorld) run(args ...string) (string, error) {
	w.t.Helper()
	cmd := exec.Command(w.shell, append([]string{w.script}, args...)...)
	cmd.Dir = w.cwd
	cmd.Env = w.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ok runs it and fails the test when it fails.
func (w *installWorld) ok(args ...string) string {
	w.t.Helper()
	out, err := w.run(args...)
	if err != nil {
		w.t.Fatalf("install.sh %v: %v\n%s", args, err, out)
	}
	return out
}

// refused runs it and fails the test unless it exits with a status of 1 and
// says want, and unless it left HOME and TMPDIR as it found them.
func (w *installWorld) refused(want string, args ...string) string {
	w.t.Helper()
	before := installTree(w.t, w.home)
	out, err := w.run(args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		w.t.Fatalf("install.sh %v: %v; want exit status 1:\n%s", args, err, out)
	}
	if !strings.Contains(out, want) {
		w.t.Errorf("install.sh %v said\n%s\nwant %q in it", args, out, want)
	}
	if got := installTree(w.t, w.home); strings.Join(got, "\n") != strings.Join(before, "\n") {
		w.t.Errorf("a refused install changed HOME: %q, was %q", got, before)
	}
	if got := installTree(w.t, w.tmp); len(got) != 0 {
		w.t.Errorf("a refused install left %q in TMPDIR", got)
	}
	return out
}

// log is what the stand-ins were asked, a line each, in order.
func (w *installWorld) log() []string {
	w.t.Helper()
	b, err := os.ReadFile(w.calls)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// urls are the addresses curl was asked for.
func (w *installWorld) urls() []string {
	var urls []string
	for _, line := range w.log() {
		if strings.HasPrefix(line, "curl ") {
			urls = append(urls, line[strings.LastIndex(line, " ")+1:])
		}
	}
	return urls
}

// installTree lists what is under dir, relative to it.
func installTree(t *testing.T, dir string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		got = append(got, rel)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// installed runs the yardr the script installed and returns what it says.
func (w *installWorld) installed() string {
	w.t.Helper()
	out, err := exec.Command(filepath.Join(w.localBin(), "yardr"), "--version").CombinedOutput()
	if err != nil {
		w.t.Fatalf("the installed yardr: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// shells are the shells to run the script's main road under: the system's sh
// (bash in POSIX mode on a Mac, dash on Debian) and dash where it is another
// program, since it is the stricter.
func shells(t *testing.T) []string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	list := []string{sh}
	if dash, err := exec.LookPath("dash"); err == nil {
		a, _ := filepath.EvalSymlinks(sh)
		b, _ := filepath.EvalSymlinks(dash)
		if a != b {
			list = append(list, dash)
		}
	}
	return list
}

func TestInstallPutsTheReleaseInLocalBin(t *testing.T) {
	t.Parallel()
	for _, shell := range shells(t) {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			t.Parallel()
			w := newInstallWorld(t, newFixtureSite(t))
			w.shell = shell
			w.minisign()
			// Both profiles of a user: the script writes neither.
			profiles := map[string]string{".profile": "# mine\n", ".zshrc": "export A=1\n"}
			for name, body := range profiles {
				if err := os.WriteFile(filepath.Join(w.home, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// As `curl ... | sh` runs it: from the shell's stdin.
			src, err := os.Open(w.script)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			cmd := exec.Command(shell, "-s", "--")
			cmd.Dir, cmd.Env, cmd.Stdin = w.cwd, w.env, src
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			if err != nil {
				t.Fatalf("install.sh: %v\n%s", err, out)
			}

			// Only yardr was unpacked, to the one place, ready to run.
			got := installTree(t, w.home)
			want := []string{".local", ".local/bin", ".local/bin/yardr", ".profile", ".zshrc"}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("HOME holds %q; want %q", got, want)
			}
			for name, body := range profiles {
				if b, _ := os.ReadFile(filepath.Join(w.home, name)); string(b) != body {
					t.Errorf("%s was changed to %q", name, b)
				}
			}
			if fi, err := os.Stat(filepath.Join(w.localBin(), "yardr")); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("yardr is %v, %v; want an executable file, mode 755", fi, err)
			}
			if v := w.installed(); v != installTag+" (fixture linux_amd64)" {
				t.Errorf("the installed yardr says %q; want this system's archive's", v)
			}
			if left := installTree(t, w.tmp); len(left) != 0 {
				t.Errorf("TMPDIR holds %q afterwards", left)
			}
			for _, s := range []string{
				"installing yardr " + installTag + " for linux_amd64",
				"the signature on checksums.txt verifies against yardr's release key",
				"yardr_9.8.7_linux_amd64.tar.gz matches its sha256 in checksums.txt",
				"installed ~/.local/bin/yardr: " + installTag + " (fixture linux_amd64)",
				// Not on the PATH of the run: the one line to add, said and not written.
				`export PATH="$HOME/.local/bin:$PATH"`,
			} {
				if !strings.Contains(out, s) {
					t.Errorf("the output lacks %q:\n%s", s, out)
				}
			}
			if strings.Contains(out, "only the checksum") {
				t.Errorf("a checked signature is called unchecked:\n%s", out)
			}

			// Four things asked of yardr.dev and nothing else, over https,
			// the signature checked before the archive is fetched.
			base := "https://yardr.dev/releases/"
			steps := []struct{ start, end string }{
				{"curl ", base + "latest.json"},
				{"curl ", base + installTag + "/checksums.txt"},
				{"curl ", base + installTag + "/checksums.txt.minisig"},
				{"minisign -V -P " + releaseKey, "checksums.txt"},
				{"curl ", base + installTag + "/yardr_9.8.7_linux_amd64.tar.gz"},
			}
			log := w.log()
			if len(log) != len(steps) {
				t.Fatalf("the stand-ins were asked\n%s\nwant %d calls", strings.Join(log, "\n"), len(steps))
			}
			for i, line := range log {
				if !strings.HasPrefix(line, steps[i].start) || !strings.HasSuffix(line, steps[i].end) {
					t.Errorf("call %d is %q; want it to start %q and end %q", i+1, line, steps[i].start, steps[i].end)
				}
				if strings.HasPrefix(line, "curl ") && (!strings.Contains(line, " --fail ") || !strings.Contains(line, "--proto =https") || !strings.Contains(line, "--write-out %{http_code}")) {
					t.Errorf("call %d, %q, does not fail on an error status or hold itself to https", i+1, line)
				}
				if strings.HasPrefix(line, "curl ") && (!strings.HasPrefix(line, "curl --disable ") || strings.Contains(" "+line+" ", " --location ") || strings.Contains(" "+line+" ", " -L ") || strings.Contains(line, "--proto-redir")) {
					t.Errorf("call %d, %q, reads curl config or follows redirects", i+1, line)
				}
			}
			if !strings.Contains(log[3], "-m "+w.tmp) || !strings.Contains(log[3], "checksums.txt") {
				t.Errorf("minisign was not asked about the downloaded checksums.txt: %q", log[3])
			}
		})
	}
}

func TestInstallSaysThatOnlyTheChecksumWasCheckedWithoutMinisign(t *testing.T) {
	t.Parallel()
	w := newInstallWorld(t, newFixtureSite(t))
	out := w.ok()
	for _, s := range []string{
		"minisign is not on PATH, so only the checksum was checked",
		"not who made it",
		"https://jedisct1.github.io/minisign/",
		"brew install minisign on macOS, apt install minisign on Debian",
		"installed ~/.local/bin/yardr",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("the output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "verifies against yardr's release key") {
		t.Errorf("a signature that was not checked is called checked:\n%s", out)
	}
	// All three are still fetched: the signature is for whoever has minisign next time.
	if got := len(w.urls()); got != 4 {
		t.Errorf("curl was asked for %d files: %q", got, w.urls())
	}
}

func TestInstallRefusesAWrongChecksum(t *testing.T) {
	t.Parallel()
	for name, withMinisign := range map[string]bool{"checksum alone": false, "beside a good signature": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := newFixtureSite(t)
			// The archive is changed after checksums.txt was made over it.
			site.write("releases/"+installTag+"/"+installArchive("linux_amd64"), "not the archive checksums.txt is of\n")
			w := newInstallWorld(t, site)
			if withMinisign {
				w.minisign()
			}
			out := w.refused("yardr_9.8.7_linux_amd64.tar.gz does not match its sha256 in checksums.txt")
			if !strings.Contains(out, "nothing was installed") {
				t.Errorf("the refusal does not say that nothing was installed:\n%s", out)
			}
		})
	}
}

// A refusal leaves the yardr that was there as it was, down to the file.
func TestInstallRefusalsLeaveTheInstalledYardrAlone(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*installWorld){
		"a wrong checksum": func(w *installWorld) {
			w.site.write("releases/"+installTag+"/"+installArchive("linux_amd64"), "tampered\n")
		},
		"a bad signature": func(w *installWorld) {
			w.minisign()
			w.setenv("STANDIN_MINISIGN_RC=1")
		},
		"a yardr that does not run": func(w *installWorld) {
			w.site.archive("linux_amd64", map[string]string{"yardr": "#!/bin/sh\necho broken >&2\nexit 3\n"})
			w.site.publish()
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newInstallWorld(t, newFixtureSite(t))
			if err := os.MkdirAll(w.localBin(), 0o755); err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(w.localBin(), "yardr")
			if err := os.WriteFile(old, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(old)
			if err != nil {
				t.Fatal(err)
			}
			change(w)
			w.refused("nothing was installed")
			after, err := os.Stat(old)
			if err != nil || !os.SameFile(before, after) {
				t.Errorf("the old yardr is gone or was replaced: %v", err)
			}
			if b, _ := os.ReadFile(old); string(b) != "#!/bin/sh\necho old\n" {
				t.Errorf("the old yardr now holds %q", b)
			}
			if got := installTree(t, w.localBin()); len(got) != 1 {
				t.Errorf("~/.local/bin holds %q; want the old yardr alone", got)
			}
		})
	}
}

func TestInstallRefusesABadSignature(t *testing.T) {
	t.Parallel()
	site := newFixtureSite(t)
	w := newInstallWorld(t, site)
	w.minisign()
	w.setenv("STANDIN_MINISIGN_RC=1", "STANDIN_MINISIGN_ERR=Signature verification failed")
	out := w.refused("the signature on checksums.txt does not verify against yardr's release key (id 0ECCFE6E81F150B3)")
	// minisign's own words reach the user, and the archive was never fetched.
	if !strings.Contains(out, "Signature verification failed") {
		t.Errorf("minisign's words are lost:\n%s", out)
	}
	for _, u := range w.urls() {
		if strings.HasSuffix(u, ".tar.gz") {
			t.Errorf("the archive was fetched before the signature was checked: %s", u)
		}
	}
}

// signSite signs the site's checksums.txt with the key in secret.
func signSite(t *testing.T, site *fixtureSite, secret string) {
	t.Helper()
	cmd := exec.Command("minisign", "-S", "-s", secret, "-m", "checksums.txt", "-x", "checksums.txt.minisig")
	cmd.Dir = site.tagDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("minisign -S: %v\n%s", err, out)
	}
}

// publicKey is the key a minisign public key file holds, as -P takes it.
func publicKey(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// scriptWithKey is a copy of install.sh that trusts another release key, as
// no signature a test makes is by yardr's: the one place the script names the
// key is replaced in the copy, so the script itself needs no way to be told
// another.
func scriptWithKey(t *testing.T, key string) string {
	t.Helper()
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), releaseKey); n != 1 {
		t.Fatalf("install.sh names the release key %d times; want once", n)
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(strings.Replace(string(src), releaseKey, key, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// With the real minisign: the flags the script gives it are right, a
// signature by the key it trusts passes, and one by another key, or by none, is
// refused (and so is the fixture signed by a key of the test's, under the
// script as it is, which trusts yardr's).
func TestInstallChecksTheSignatureWithMinisign(t *testing.T) {
	t.Parallel()
	secret, public := minisignKey(t)
	key := publicKey(t, public)
	_, otherPublic := minisignKey(t)

	t.Run("a signature by the key it trusts", func(t *testing.T) {
		t.Parallel()
		site := newFixtureSite(t)
		signSite(t, site, secret)
		w := newInstallWorld(t, site)
		w.script = scriptWithKey(t, key)
		w.realMinisign()
		out := w.ok()
		if !strings.Contains(out, "the signature on checksums.txt verifies against yardr's release key") {
			t.Errorf("a good signature is not reported:\n%s", out)
		}
		if w.installed() != installTag+" (fixture linux_amd64)" {
			t.Error("the verified release was not installed")
		}
	})
	for name, trust := range map[string]string{"a signature by another key": otherPublic, "the key the script has": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := newFixtureSite(t)
			signSite(t, site, secret)
			w := newInstallWorld(t, site)
			if trust != "" {
				w.script = scriptWithKey(t, publicKey(t, trust))
			}
			w.realMinisign()
			w.refused("the signature on checksums.txt does not verify against yardr's release key")
		})
	}
	t.Run("changed checksums", func(t *testing.T) {
		t.Parallel()
		site := newFixtureSite(t)
		signSite(t, site, secret)
		// The signature is of the checksums as they were.
		site.edit("releases/"+installTag+"/checksums.txt", func(s string) string { return "0" + s[1:] })
		w := newInstallWorld(t, site)
		w.script = scriptWithKey(t, key)
		w.realMinisign()
		w.refused("does not verify against yardr's release key")
	})
}

func mustLookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s on PATH", name)
	}
	return p
}

// What the site's latest.json may say is the site's, and the script holds it
// to what it can use before anything is fetched on its word.
func TestInstallRefusesWhatLatestJSONGetsWrong(t *testing.T) {
	t.Parallel()
	tag := "releases/" + installTag + "/"
	for _, tc := range []struct {
		name string
		edit func(*fixtureSite)
		want string
	}{
		{"no archive for this system", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string { return strings.ReplaceAll(j, `"linux_amd64"`, `"linux_s390x"`) })
		}, "latest.json lists no yardr v9.8.7 for linux_amd64"},
		{"no version", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string { return strings.Replace(j, `"version"`, `"edition"`, 1) })
		}, "latest.json names no usable version"},
		{"a version that is no version", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, `"v9.8.7"`, `"v1.2.3\u001b[31m"`, 1)
			})
		}, "latest.json names no usable version"},
		{"a page instead of json", func(s *fixtureSite) { s.write("releases/latest.json", "<html>Sign in to the network</html>\n") },
			"latest.json names no usable version"},
		{"a redirect response", func(s *fixtureSite) {
			s.write("releases/latest.json.status", "302")
		}, "downloading https://yardr.dev/releases/latest.json returned HTTP 302"},
		{"an archive on another host", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, "/"+tag+installArchive("linux_amd64"), "https://evil.example/yardr.tar.gz", 1)
			})
		}, "latest.json's linux_amd64 archive is not a plain path under /releases/"},
		{"an archive that makes another host of the address", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, "/"+tag+installArchive("linux_amd64"), "/releases/@evil.example/yardr.tar.gz", 1)
			})
		}, "latest.json's linux_amd64 archive is not a plain path under /releases/"},
		{"an archive outside /releases/", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, "/"+tag+installArchive("linux_amd64"), "//evil.example/yardr.tar.gz", 1)
			})
		}, "latest.json's linux_amd64 archive is not a plain path under /releases/"},
		{"an archive that climbs out", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, "/"+tag+installArchive("linux_amd64"), "/releases/../private/yardr.tar.gz", 1)
			})
		}, "latest.json's linux_amd64 archive is not a plain path under /releases/"},
		{"checksums elsewhere", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, `"/`+tag+`checksums.txt"`, `"https://evil.example/checksums.txt"`, 1)
			})
		}, "latest.json's checksums is not a plain path under /releases/"},
		{"a signature elsewhere", func(s *fixtureSite) {
			s.edit("releases/latest.json", func(j string) string {
				return strings.Replace(j, `"/`+tag+`checksums.txt.minisig"`, `"/releases/x y"`, 1)
			})
		}, "latest.json's signature is not a plain path under /releases/"},
		{"no checksum for the archive", func(s *fixtureSite) {
			s.edit(tag+"checksums.txt", func(c string) string { return strings.ReplaceAll(c, "linux_amd64", "linux_amd65") })
		}, "checksums.txt has no sha256 for yardr_9.8.7_linux_amd64.tar.gz"},
		{"a checksum of 63 digits", func(s *fixtureSite) {
			s.edit(tag+"checksums.txt", func(c string) string {
				return regexp.MustCompile(`(?m)^.(.{63}  yardr_9\.8\.7_linux_amd64\.tar\.gz)$`).ReplaceAllString(c, "$1")
			})
		}, "checksums.txt has no sha256 for yardr_9.8.7_linux_amd64.tar.gz"},
		{"a checksum that is no hex", func(s *fixtureSite) {
			s.edit(tag+"checksums.txt", func(c string) string {
				return regexp.MustCompile(`(?m)^.(.{63}  yardr_9\.8\.7_linux_amd64\.tar\.gz)$`).ReplaceAllString(c, "Z$1")
			})
		}, "checksums.txt has no sha256 for yardr_9.8.7_linux_amd64.tar.gz"},
		{"a signature that is not served", func(s *fixtureSite) {
			if err := os.Remove(filepath.Join(s.tagDir(), "checksums.txt.minisig")); err != nil {
				s.t.Fatal(err)
			}
		}, "downloading https://yardr.dev/releases/v9.8.7/checksums.txt.minisig failed"},
		{"an archive that is not served", func(s *fixtureSite) {
			if err := os.Remove(filepath.Join(s.tagDir(), installArchive("linux_amd64"))); err != nil {
				s.t.Fatal(err)
			}
		}, "downloading https://yardr.dev/releases/v9.8.7/yardr_9.8.7_linux_amd64.tar.gz failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			site := newFixtureSite(t)
			tc.edit(site)
			w := newInstallWorld(t, site)
			out := w.refused(tc.want)
			if !strings.Contains(out, "nothing was installed") {
				t.Errorf("the refusal does not say that nothing was installed:\n%s", out)
			}
			// Whatever the site said, no address but yardr.dev's was tried
			// (the stand-in fails any other, and logs it).
			for _, u := range w.urls() {
				if !strings.HasPrefix(u, "https://yardr.dev/releases/") {
					t.Errorf("curl was asked for %s", u)
				}
			}
			if strings.Contains(out, "evil.example") || strings.Contains(out, "\x1b") {
				t.Errorf("the refusal repeats what the site said:\n%q", out)
			}
		})
	}
}

// latest.json is read for its keys, not its layout.
func TestInstallReadsLatestJSONWhateverItsLayout(t *testing.T) {
	t.Parallel()
	for name, layout := range map[string]func(string) string{
		"as the site writes it": func(j string) string { return j },
		"on one line": func(j string) string {
			return regexp.MustCompile(`\s*\n\s*`).ReplaceAllString(j, " ")
		},
		"tabs and carriage returns": func(j string) string {
			return strings.ReplaceAll(strings.ReplaceAll(j, "\n", "\r\n"), "  ", "\t")
		},
		"the targets first": func(j string) string {
			// version and the paths move after the targets, as a re-ordering writer would.
			i := strings.Index(j, `  "targets"`)
			end := strings.LastIndex(j, "}")
			head := strings.TrimSuffix(j[:i], ",\n")
			head = strings.TrimPrefix(head, "{\n")
			return "{\n" + j[i:end] + ",\n" + head + "\n}\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			site := newFixtureSite(t)
			site.edit("releases/latest.json", layout)
			w := newInstallWorld(t, site)
			w.ok()
			if got := w.installed(); got != installTag+" (fixture linux_amd64)" {
				t.Errorf("installed %q", got)
			}
		})
	}
}

// The archive is this system's, as uname says, and a Mac's shell started under
// Rosetta is not taken at its word.
func TestInstallChoosesThisSystemsArchive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		os, arch, arm64 string // uname -s, uname -m, what sysctl says of hw.optional.arm64
		want            string // the archive's target, or "" for a system with no release
	}{
		{"Linux", "x86_64", "", "linux_amd64"},
		{"Linux", "amd64", "", "linux_amd64"},
		{"Linux", "aarch64", "", "linux_arm64"},
		{"Linux", "arm64", "", "linux_arm64"},
		{"Darwin", "arm64", "1", "darwin_arm64"},
		{"Darwin", "x86_64", "", "darwin_amd64"},
		{"Darwin", "x86_64", "1", "darwin_arm64"},
		{"FreeBSD", "amd64", "", ""},
		{"Linux", "riscv64", "", ""},
		{"Linux", "armv7l", "", ""},
		{"Linux", "i686", "", ""},
	} {
		t.Run(tc.os+" "+tc.arch+" "+tc.arm64, func(t *testing.T) {
			t.Parallel()
			w := newInstallWorld(t, newFixtureSite(t))
			w.setenv("STANDIN_OS="+tc.os, "STANDIN_ARCH="+tc.arch, "STANDIN_ARM64="+tc.arm64)
			if tc.want == "" {
				w.refused("there is no yardr release for ")
				if urls := w.urls(); len(urls) != 0 {
					t.Errorf("a system with no release fetched %q", urls)
				}
				return
			}
			w.ok()
			if got := w.installed(); got != installTag+" (fixture "+tc.want+")" {
				t.Errorf("%s %s installed %q; want the %s archive", tc.os, tc.arch, got, tc.want)
			}
			if urls := w.urls(); !strings.HasSuffix(urls[len(urls)-1], "yardr_9.8.7_"+tc.want+".tar.gz") {
				t.Errorf("the archive fetched was %s", urls[len(urls)-1])
			}
		})
	}
}

// The line to add is for a PATH without ~/.local/bin, and a yardr found before
// the one installed is said to be the one that runs.
func TestInstallSaysWhatThePathDoes(t *testing.T) {
	t.Parallel()
	const line = `export PATH="$HOME/.local/bin:$PATH"`
	other := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "yardr"), []byte("#!/bin/sh\necho other\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for _, tc := range []struct {
		name string
		path func(t *testing.T, w *installWorld) // sets the PATH after the tools
		want []string
		lack []string
	}{
		{"~/.local/bin is not on PATH", func(*testing.T, *installWorld) {},
			[]string{"is not on your PATH", line}, []string{"note:"}},
		{"~/.local/bin is on PATH", func(_ *testing.T, w *installWorld) { w.pathAdd(w.localBin()) },
			nil, []string{"is not on your PATH", line, "note:"}},
		{"another yardr, with ~/.local/bin off PATH", func(t *testing.T, w *installWorld) { w.pathAdd(other(t)) },
			[]string{line, "until then yardr runs "}, nil},
		{"another yardr before ~/.local/bin", func(t *testing.T, w *installWorld) {
			w.setenv("PATH=" + w.bin + string(os.PathListSeparator) + other(t) + string(os.PathListSeparator) + w.localBin())
		}, []string{"note: yardr on your PATH is ", "comes before ~/.local/bin"}, []string{"is not on your PATH", line}},
		{"another yardr after ~/.local/bin", func(t *testing.T, w *installWorld) {
			w.setenv("PATH=" + w.bin + string(os.PathListSeparator) + w.localBin() + string(os.PathListSeparator) + other(t))
		}, nil, []string{"is not on your PATH", line, "note:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newInstallWorld(t, newFixtureSite(t))
			w.minisign()
			tc.path(t, w)
			out := w.ok()
			for _, s := range tc.want {
				if !strings.Contains(out, s) {
					t.Errorf("the output lacks %q:\n%s", s, out)
				}
			}
			for _, s := range tc.lack {
				if strings.Contains(out, s) {
					t.Errorf("the output holds %q:\n%s", s, out)
				}
			}
		})
	}
}

// The new yardr is a new file renamed over the old, so that a yardr that is
// running keeps the file it runs from, and it is run before it replaces
// anything.
func TestInstallRenamesTheNewYardrOverTheOld(t *testing.T) {
	t.Parallel()
	w := newInstallWorld(t, newFixtureSite(t))
	if err := os.MkdirAll(w.localBin(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(w.localBin(), "yardr")
	if err := os.WriteFile(old, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	// Open, as a running program's file is.
	running, err := os.Open(old)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	w.ok()
	after, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the new yardr was written into the old file, not renamed over it")
	}
	buf := make([]byte, 64)
	n, _ := running.Read(buf)
	if !strings.Contains(string(buf[:n]), "echo old") {
		t.Errorf("the file the old yardr runs from changed under it: %q", buf[:n])
	}
	if got := w.installed(); got != installTag+" (fixture linux_amd64)" {
		t.Errorf("installed %q", got)
	}
	if got := installTree(t, w.localBin()); len(got) != 1 {
		t.Errorf("~/.local/bin holds %q; want yardr alone", got)
	}
}

func TestInstallRefusesWhereYardrIsNotAFile(t *testing.T) {
	t.Parallel()
	w := newInstallWorld(t, newFixtureSite(t))
	if err := os.MkdirAll(filepath.Join(w.localBin(), "yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.refused(filepath.Join(w.localBin(), "yardr") + " is a directory")
}

func TestInstallRefusesAnArchiveWithNoYardr(t *testing.T) {
	t.Parallel()
	site := newFixtureSite(t)
	site.archive("linux_amd64", map[string]string{"README.md": "readme\n", "server-yard": "#!/bin/sh\n"})
	site.publish()
	w := newInstallWorld(t, site)
	// Everything checks, so ~/.local/bin was made; nothing of the archive is in it.
	out, err := w.run()
	if err == nil || !strings.Contains(out, "holds no yardr to unpack; nothing was installed") {
		t.Fatalf("err %v; want the refusal:\n%s", err, out)
	}
	for _, p := range installTree(t, w.home) {
		if p != ".local" && p != filepath.Join(".local", "bin") {
			t.Errorf("a refused install left %s", p)
		}
	}
	if left := installTree(t, w.tmp); len(left) != 0 {
		t.Errorf("TMPDIR holds %q afterwards", left)
	}
}

func TestInstallUsage(t *testing.T) {
	t.Parallel()
	w := newInstallWorld(t, newFixtureSite(t))
	for _, flag := range []string{"-h", "--help"} {
		if out := w.ok(flag); !strings.Contains(out, "usage: install.sh [--from-source]") {
			t.Errorf("%s printed\n%s", flag, out)
		}
	}
	for _, args := range [][]string{{"--prefix"}, {"--from-source", "--bin"}, {"v1.2.3"}} {
		out, err := w.run(args...)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(out, "usage: install.sh [--from-source]") {
			t.Errorf("install.sh %v: %v\n%s\nwant usage and exit status 2", args, err, out)
		}
	}
	if got := w.log(); len(got) != 0 || len(installTree(t, w.home)) != 0 {
		t.Errorf("a refused command line fetched %q or wrote %q", got, installTree(t, w.home))
	}
}

// The script runs from a checkout and builds it. A Go environment of the
// test's own: HOME is the install's, which would otherwise be where Go looks
// for its caches, and nothing may be fetched.
func goEnv(t *testing.T, home, tmp string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "YARDR_"), strings.HasPrefix(k, "GIT_"), k == "HOME", k == "TMPDIR":
		default:
			env = append(env, kv)
		}
	}
	for _, name := range []string{"GOCACHE", "GOMODCACHE"} {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", name, err)
		}
		env = append(env, name+"="+strings.TrimSpace(string(out)))
	}
	return append(env, "HOME="+home, "TMPDIR="+tmp, "GOPROXY=off")
}

func TestInstallFromSourceBuildsTheCheckout(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds yardr")
	}
	out, err := exec.Command("git", "-C", "../..", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("not a git checkout, so the build has no revision to say")
	}
	rev := strings.TrimSpace(string(out))[:12]
	script, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	home, tmp := t.TempDir(), t.TempDir()
	// Run from another directory: the checkout is where the script sits.
	cmd := exec.Command("sh", script, "--from-source")
	cmd.Dir = t.TempDir()
	cmd.Env = goEnv(t, home, tmp)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install.sh --from-source: %v\n%s", err, raw)
	} else if !strings.Contains(string(raw), "installed ~/.local/bin/yardr: "+rev) {
		t.Errorf("the output does not say the revision %s was installed:\n%s", rev, raw)
	}
	bin := filepath.Join(home, ".local", "bin")
	entries, err := os.ReadDir(bin)
	if err != nil || len(entries) != 1 || entries[0].Name() != "yardr" {
		t.Fatalf("~/.local/bin holds %v, %v; want yardr alone", entries, err)
	}
	// What it says is its revision, as a go build leaves it: no tag, and
	// -modified where the tree is not the commit.
	got, err := exec.Command(filepath.Join(bin, "yardr"), "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("yardr --version: %v\n%s", err, got)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(got)), rev) {
		t.Errorf("yardr --version says %q; want the revision %s", got, rev)
	}
}

func TestInstallFromSourceNeedsAGoAndACheckout(t *testing.T) {
	t.Parallel()
	tree, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(tree)
	if err != nil {
		t.Fatal(err)
	}
	// The script in a directory of its own, with the go.mod it names or none.
	copyTo := func(t *testing.T, gomod string) string {
		dir := t.TempDir()
		if gomod != "" {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(dir, "install.sh")
		if err := os.WriteFile(path, src, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name   string
		script func(t *testing.T) string
		want   string
	}{
		{"no go.mod at all", func(t *testing.T) string { return copyTo(t, "") }, "is none (it has no go.mod for yardr.dev/yardr)"},
		{"the go.mod of another module", func(t *testing.T) string { return copyTo(t, "module example.com/other\n\ngo 1.22\n") },
			"is none (it has no go.mod for yardr.dev/yardr)"},
		{"no Go on PATH", func(t *testing.T) string { return copyTo(t, "module yardr.dev/yardr\n\ngo 1.22\n") },
			"--from-source needs Go on PATH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newInstallWorld(t, newFixtureSite(t))
			w.script = tc.script(t)
			w.refused(tc.want, "--from-source")
			if got := w.log(); len(got) != 0 {
				t.Errorf("a source install fetched %q", got)
			}
		})
	}
}

// unquoted is the code of a script's line with its quoted text and its comment
// taken out, each quoted string standing as "". quote is what the previous line
// left open (' or ", or 0): a string may run over lines, as the awk program of
// install.sh does. ansiC says the line opens a $'...' string.
func unquoted(line string, quote byte) (code string, left byte, ansiC bool) {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			b.WriteByte(c)
			if i+1 < len(line) {
				i++
				b.WriteByte(line[i])
			}
		case c == '\'' || c == '"':
			if c == '\'' && i > 0 && line[i-1] == '$' {
				ansiC = true
			}
			quote = c
			b.WriteString(`""`)
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return b.String(), quote, ansiC
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), quote, ansiC
}

// bashisms are the lines of a shell script that use what POSIX sh lacks. It is
// a net for what `sh -n` lets through where sh is bash (a Mac's), not a
// parser: quoted text and here-document bodies are not read, and comments are
// dropped. dash -n and shellcheck (CI runs it over this script) are the
// stricter checks.
func bashisms(src string) []string {
	checks := []struct {
		why string
		re  *regexp.Regexp
	}{
		{"[[ ]]", regexp.MustCompile(`\[\[|\]\]`)},
		{"== in a test", regexp.MustCompile(`==`)},
		{"function keyword", regexp.MustCompile(`(^|[;&|]|\s)function\s+\w`)},
		{"local, declare, typeset", regexp.MustCompile(`(^|[;&|(]|\s)(local|declare|typeset)\s`)},
		{"source", regexp.MustCompile(`(^|[;&|(])\s*source\s`)},
		{"echo with a flag", regexp.MustCompile(`\becho\s+-[neE]+\b`)},
		{"here-string or process substitution", regexp.MustCompile(`<<<|<\(|>\(|&>`)},
		{"${var//x}, ${var^^}, ${var,,}", regexp.MustCompile(`\$\{[^}]*(//|\^\^|,,)`)},
		{"${var:offset}, ${!var}, ${arr[i]}", regexp.MustCompile(`\$\{(!|\w+:[0-9]|\w+\[)`)},
		{"arithmetic command or let", regexp.MustCompile(`(^|[^$])\(\(|(^|[;&|]|\s)let\s`)},
		{"arrays and +=", regexp.MustCompile(`\w=\(|\+=`)},
		{"bash variables", regexp.MustCompile(`\$(RANDOM|BASH\w*|OSTYPE|HOSTNAME|PIPESTATUS|FUNCNAME|SECONDS)\b`)},
		{"read, printf or set with a bash option", regexp.MustCompile(`\bread\s+-[pasd]|\bprintf\s+-v\b|\bset\s+-o\s+pipefail`)},
		{"trap on a bash signal", regexp.MustCompile(`\btrap\b.*\b(ERR|DEBUG|RETURN)\b`)},
		{"-nt, -ot, -ef", regexp.MustCompile(`\s-(nt|ot|ef)\s`)},
		{"select, coproc, mapfile, shopt, pushd", regexp.MustCompile(`(^|[;&|]|\s)(select|coproc|mapfile|readarray|shopt|pushd|popd)\s`)},
	}
	hereDoc := regexp.MustCompile(`<<-?\s*'?(\w+)'?`)
	var found []string
	var quote byte
	inHere := ""
	for n, line := range strings.Split(src, "\n") {
		if inHere != "" {
			if strings.TrimSpace(line) == inHere {
				inHere = ""
			}
			continue
		}
		if quote == 0 {
			if m := hereDoc.FindStringSubmatch(line); m != nil {
				inHere = m[1]
			}
		}
		code, left, ansiC := unquoted(line, quote)
		quote = left
		if ansiC {
			found = append(found, fmt.Sprintf("line %d: ANSI-C quoting: %s", n+1, strings.TrimSpace(line)))
		}
		for _, c := range checks {
			if c.re.MatchString(code) {
				found = append(found, fmt.Sprintf("line %d: %s: %s", n+1, c.why, strings.TrimSpace(line)))
			}
		}
	}
	return found
}

func TestInstallIsPOSIXSh(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(src, []byte("#!/bin/sh\n")) {
		t.Error("install.sh does not start with #!/bin/sh")
	}
	fi, err := os.Stat("../../install.sh")
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("install.sh is not executable: %v, %v", fi, err)
	}
	for _, shell := range shells(t) {
		if out, err := exec.Command(shell, "-n", "../../install.sh").CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", shell, err, out)
		}
	}
	if found := bashisms(string(src)); len(found) != 0 {
		t.Errorf("install.sh uses what POSIX sh lacks:\n%s", strings.Join(found, "\n"))
	}
	// Read before it runs: the last thing in the file is the call of main, so a
	// download cut short can run nothing of it.
	lines := strings.Split(strings.TrimSpace(string(src)), "\n")
	if last := lines[len(lines)-1]; last != `main "$@"` {
		t.Errorf("install.sh ends with %q; want main \"$@\"", last)
	}
}

// The net above has to catch what it is for, or it is a test that cannot fail.
func TestBashismScanFindsBashisms(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		`if [[ -n $x ]]; then`, `[ "$a" == "$b" ]`, `function f {`, `local x=1`, `source ./lib`, `echo -e "a\tb"`,
		`x=$'a\n'`, `cat <<< "$x"`, `diff <(a) <(b)`, `y=${x//a/b}`, `y=${x:1:2}`, `arr=(a b)`, `x+=1`, `(( i++ ))`,
		`echo $RANDOM`, `read -p "ok? " a`, `set -o pipefail`, `trap 'x' ERR`, `[ a -nt b ]`, `declare -a x`, `let i=1`,
	} {
		if len(bashisms(bad)) == 0 {
			t.Errorf("%q is not found to be a bashism", bad)
		}
	}
	for _, good := range []string{
		`if [ -n "$x" ]; then`, `[ "$a" = "$b" ]`, `f() {`, `echo "a [[ b ]] == c"`, `echo --from-source`, `x=${1:-}`,
		`n=${#want}`, `y=${x#--}`, `i=$((i + 1))`, `# local x`, `cat <<'EOF'` + "\nsource it\nEOF", `printf '%s\n' "$x"`,
	} {
		if found := bashisms(good); len(found) != 0 {
			t.Errorf("%q is called a bashism: %v", good, found)
		}
	}
}

// The release key is named by the script, by the stage that checks a release
// against it and by the page that tells a reader how to check one by hand, and
// they must be the one key.
func TestInstallNamesTheReleaseKeyTheDocsName(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"../../install.sh", "../../docs/install.md", "stage"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]bool{}
		for _, k := range regexp.MustCompile(`RW[A-Za-z0-9+/]{54}`).FindAllString(string(b), -1) {
			keys[k] = true
		}
		if len(keys) != 1 || !keys[releaseKey] {
			t.Errorf("%s names the keys %v; want only %s", file, keys, releaseKey)
		}
	}
}
