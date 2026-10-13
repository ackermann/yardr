// Package release tests contrib/release/build, the script that makes
// yardr's release archives (and that .github/workflows/release.yml runs).
package release

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// build runs the script for this machine's target only, with nothing of a
// yardr or git hook running the tests leaking into it.
func build(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return buildWith(t, nil, args...)
}

// buildWith is build with more environment, which wins over the rest.
func buildWith(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("./build", args...)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	// The tree under test is a bead's worktree, which may be dirty.
	cmd.Env = append(cmd.Env, "YARDR_RELEASE_TARGETS="+runtime.GOOS+"/"+runtime.GOARCH, "YARDR_RELEASE_ALLOW_DIRTY=1")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBuildStampsTheVersionAndPacksTheArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds yardr")
	}
	dist := t.TempDir()
	if out, err := build(t, "v9.8.7-rc.1", dist); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	archive := "yardr_9.8.7-rc.1_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"

	members(t, filepath.Join(dist, archive))

	unpacked := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", filepath.Join(dist, archive), "-C", unpacked).CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(unpacked, "README.md")); err != nil {
		t.Errorf("the archive has no README: %v", err)
	}
	out, err := exec.Command(filepath.Join(unpacked, "yardr"), "version").CombinedOutput()
	if err != nil {
		t.Fatalf("yardr version: %v\n%s", err, out)
	}
	// The tag, then the revision when the build came from a checkout.
	got := strings.TrimSpace(string(out))
	if got != "v9.8.7-rc.1" && !(strings.HasPrefix(got, "v9.8.7-rc.1 (") && strings.HasSuffix(got, ")")) {
		t.Errorf("yardr version = %q; want v9.8.7-rc.1 and its revision", got)
	}

	sums, err := os.ReadFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(sums))
	if len(fields) != 2 || len(fields[0]) != 64 || fields[1] != archive {
		t.Fatalf("checksums.txt = %q; want one sha256 line for %s", sums, archive)
	}
	check := exec.Command("shasum", "-a", "256", "-c", "checksums.txt")
	if _, err := exec.LookPath("sha256sum"); err == nil {
		check = exec.Command("sha256sum", "-c", "checksums.txt")
	}
	check.Dir = dist
	if out, err := check.CombinedOutput(); err != nil {
		t.Errorf("checksums.txt does not verify: %v\n%s", err, out)
	}
}

// members checks that the archive holds the files the script lists and
// nothing of the machine that packed it: no AppleDouble `._*` entry and no
// extended attribute in a pax header (bsdtar on a Mac writes both for a file
// carrying com.apple.provenance, and GNU tar warns of the header on unpack),
// and no builder's user or group.
func members(t *testing.T, archive string) {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	// The script's list.
	want := map[string]bool{"yardr": true, "README.md": true, "LICENSE": true, "NOTICE": true}
	got := map[string]bool{}
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !want[h.Name] || got[h.Name] {
			t.Errorf("the archive holds %q; want each of %v once and nothing else", h.Name, want)
		}
		got[h.Name] = true
		for k := range h.PAXRecords {
			if strings.Contains(strings.ToLower(k), "xattr") {
				t.Errorf("%s carries the pax header %s", h.Name, k)
			}
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s is owned by %d/%d %q/%q; want 0/0 and no names", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("the archive has no %s", name)
		}
	}
}

func TestBuildRefusesAVersionThatIsNoTag(t *testing.T) {
	for _, v := range []string{"", "1.2.3", "v1.2", "vnext", "v1.2.3 x"} {
		dist := filepath.Join(t.TempDir(), "dist")
		out, err := build(t, v, dist)
		if err == nil || !strings.Contains(out, "usage:") {
			t.Errorf("build %q: err %v, output %q; want the usage", v, err, out)
		}
		if _, err := os.Stat(dist); err == nil {
			t.Errorf("build %q created %s", v, dist)
		}
	}
}

// standIns puts a codesign, an xcrun and a zip on a PATH of their own, as no
// test has a Developer ID or may call Apple, and returns the environment
// that leads to them and the file each writes its call to. uname is one too,
// so that the Mac's path runs wherever the tests do. tar is the real one,
// noting only when an archive is packed.
//
//	STANDIN_UNAME    what uname -s prints (Darwin)
//	STANDIN_STATUS   what the notary service answers (Accepted)
func standIns(t *testing.T) (env []string, calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"uname": `echo "${STANDIN_UNAME:-Darwin}"`,
		// A signed binary is one the archive can be asked about: the mark is
		// in it only if it was packed after this ran.
		"codesign": `echo "codesign $*" >>"$CALLS"
for last; do :; done
printf 'signed by the stand-in' >>"$last"`,
		"zip": `echo "zip $*" >>"$CALLS"
cp "$4" "$3"`,
		"xcrun": `echo "xcrun $*" >>"$CALLS"
case "$2" in
  submit)
    grep -q 'signed by the stand-in' "$3" || { echo "the submission is not of the signed binary" >&2; exit 1; }
    echo '{"path":"'"$3"'","message":"Processing complete","status":"'"${STANDIN_STATUS:-Accepted}"'","id":"2efe2717-52ef-43a5-96dc-0797e4ca1041"}'
    exit "${STANDIN_SUBMIT_EXIT:-0}"
    ;;
  log)
    echo '{"issues":[{"message":"The signature of the binary is invalid."}]}'
    ;;
esac`,
		"tar": `case " $* " in *" -czf "*) echo "tar $2" >>"$CALLS" ;; esac
exec ` + realTar + ` "$@"`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"CALLS=" + calls,
		"YARDR_RELEASE_SIGN_IDENTITY=Developer ID Application: Stand In (ABCDE12345)",
		"YARDR_RELEASE_NOTARY_PROFILE=stand-in-profile",
	}, calls
}

// binary returns the yardr an archive holds.
func binary(t *testing.T, archive string) string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("%s: no yardr in it: %v", archive, err)
		}
		if h.Name == "yardr" {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
}

func TestBuildSignsAndNotarizesTheDarwinBinaryBeforeItPacksIt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds yardr")
	}
	env, calls := standIns(t)
	dist := t.TempDir()
	out, err := buildWith(t, append(env, "YARDR_RELEASE_TARGETS=darwin/arm64 linux/amd64"), "v9.8.7", dist)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if strings.Contains(out, "not signed") {
		t.Errorf("a signing build says the binaries are not signed:\n%s", out)
	}

	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		// The program and, of the two that have one, the subcommand.
		switch f[0] {
		case "xcrun":
			got = append(got, strings.Join(f[:3], " "))
		case "tar":
			got = append(got, f[0]+" "+filepath.Base(f[1]))
		default:
			got = append(got, f[0])
		}
	}
	// darwin: sign, submit, and only then pack. linux: packed as ever.
	want := []string{"codesign", "zip", "xcrun notarytool submit", "tar yardr_9.8.7_darwin_arm64.tar.gz", "tar yardr_9.8.7_linux_amd64.tar.gz"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the build called\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, arg := range []string{
		"--sign Developer ID Application: Stand In (ABCDE12345) ",
		"--options runtime", "--timestamp", "--identifier dev.yardr.yardr", "--force",
		"--keychain-profile stand-in-profile", "--wait",
	} {
		if !strings.Contains(string(b), arg) {
			t.Errorf("no %q in the calls:\n%s", arg, b)
		}
	}

	darwin := filepath.Join(dist, "yardr_9.8.7_darwin_arm64.tar.gz")
	members(t, darwin)
	if !strings.HasSuffix(binary(t, darwin), "signed by the stand-in") {
		t.Error("the darwin archive holds a binary that is not the signed one")
	}
	if strings.Contains(binary(t, filepath.Join(dist, "yardr_9.8.7_linux_amd64.tar.gz")), "signed by the stand-in") {
		t.Error("the linux binary was signed")
	}
}

func TestBuildFailsWithTheNotaryLogWhenAppleRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds yardr")
	}
	env, calls := standIns(t)
	dist := t.TempDir()
	out, err := buildWith(t, append(env, "YARDR_RELEASE_TARGETS=darwin/arm64", "STANDIN_STATUS=Invalid", "STANDIN_SUBMIT_EXIT=1"), "v9.8.7", dist)
	if err == nil {
		t.Fatalf("the build passed:\n%s", out)
	}
	for _, want := range []string{`status "Invalid"`, "2efe2717-52ef-43a5-96dc-0797e4ca1041", "The signature of the binary is invalid."} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in the output:\n%s", want, out)
		}
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "xcrun notarytool log 2efe2717-52ef-43a5-96dc-0797e4ca1041 --keychain-profile stand-in-profile") {
		t.Errorf("the log of the submission was not asked for:\n%s", b)
	}
	if left, _ := os.ReadDir(dist); len(left) != 0 {
		t.Errorf("a refused build left %d files in %s", len(left), dist)
	}
}

// Half a signing setup, or a whole one where there is no codesign, is an
// error before anything is built: nothing is called and no out-dir made.
func TestBuildRefusesSigningItCannotDo(t *testing.T) {
	for name, tc := range map[string]struct {
		env  []string
		want string
	}{
		"identity alone": {[]string{"YARDR_RELEASE_NOTARY_PROFILE="}, "YARDR_RELEASE_NOTARY_PROFILE is not"},
		"profile alone":  {[]string{"YARDR_RELEASE_SIGN_IDENTITY="}, "YARDR_RELEASE_SIGN_IDENTITY is not"},
		"not macOS":      {[]string{"STANDIN_UNAME=Linux"}, "this is Linux"},
	} {
		t.Run(name, func(t *testing.T) {
			env, calls := standIns(t)
			dist := filepath.Join(t.TempDir(), "dist")
			out, err := buildWith(t, append(env, append(tc.env, "YARDR_RELEASE_TARGETS=darwin/arm64")...), "v9.8.7", dist)
			if err == nil || !strings.Contains(out, tc.want) {
				t.Errorf("err %v, output %q; want an error saying %q", err, out, tc.want)
			}
			if _, err := os.Stat(dist); err == nil {
				t.Errorf("the refused build created %s", dist)
			}
			if b, err := os.ReadFile(calls); err == nil {
				t.Errorf("the refused build called:\n%s", b)
			}
		})
	}
}

func TestBuildSaysThatUnsignedDarwinBinariesAreNotSigned(t *testing.T) {
	if testing.Short() {
		t.Skip("builds yardr")
	}
	env, calls := standIns(t)
	// The stand-ins and where they write, without the two variables.
	env = env[:2]
	out, err := buildWith(t, append(env, "YARDR_RELEASE_TARGETS=darwin/arm64 linux/amd64"), "v9.8.7", t.TempDir())
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if n := strings.Count(out, "the darwin binaries are not signed"); n != 1 {
		t.Errorf("the build says %d times that the darwin binaries are not signed; want once:\n%s", n, out)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "\n"); got != 2 || strings.Count(string(b), "tar ") != 2 {
		t.Errorf("an unsigned build called more than tar, twice:\n%s", b)
	}
}
