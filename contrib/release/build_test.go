// Package release tests contrib/release/build, the script that makes
// yardr's release archives (and that .github/workflows/release.yml runs).
package release

import (
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
