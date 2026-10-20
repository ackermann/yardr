package version

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fake is a "binary" whose contents are its revision; each install is
// stamped a second after the last, so mtime orders them.
func fake(t *testing.T, running string) (*Binary, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yardr")
	install(t, path, running)
	b, err := Watch(path, running)
	if err != nil {
		t.Fatal(err)
	}
	b.ReadBuild = func(p string) (Build, error) {
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			return Build{}, errors.New("not a binary")
		}
		return Build{Revision: string(data)}, nil
	}
	b.Exec = func(string, []string, []string) error { return nil }
	return b, path
}

var installed = time.Now()

// install replaces the file the way go install does: a new inode renamed
// over the old path.
func install(t *testing.T, path, rev string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(rev), 0o755); err != nil {
		t.Fatal(err)
	}
	installed = installed.Add(time.Second)
	if err := os.Chtimes(tmp, installed, installed); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestReplacement(t *testing.T) {
	b, path := fake(t, "aaaa")
	if _, ok := b.Replacement(); ok {
		t.Fatal("unchanged binary offered as a replacement")
	}
	install(t, path, "aaaa")
	if _, ok := b.Replacement(); ok {
		t.Fatal("a reinstall of the same clean build is not a replacement")
	}
	install(t, path, "bbbb")
	if rev, ok := b.Replacement(); !ok || rev != "bbbb" {
		t.Fatalf("Replacement = %q, %v; want bbbb", rev, ok)
	}
	install(t, path, "")
	if _, ok := b.Replacement(); ok {
		t.Fatal("a binary that does not parse (still being written) was offered")
	}
	os.Remove(path)
	if _, ok := b.Replacement(); ok {
		t.Fatal("a missing binary was offered")
	}
}

func TestSameCommitDirtyOrNotIsNeverAReplacement(t *testing.T) {
	// A go install from a bead worktree on the running commit must not
	// deploy its unreviewed changes (yard-r4z9).
	for _, c := range []struct{ running, installed string }{
		{"aaaa-modified", "aaaa-modified"},
		{"aaaa", "aaaa-modified"},
		{"aaaa-modified", "aaaa"},
	} {
		b, path := fake(t, c.running)
		var skipped []string
		b.Skipped = func(rev, reason string) { skipped = append(skipped, rev+": "+reason) }
		install(t, path, c.installed)
		if _, ok := b.Replacement(); ok {
			t.Errorf("running %s, installed %s: offered", c.running, c.installed)
		}
		b.Replacement()
		if len(skipped) != 1 || !strings.Contains(skipped[0], "same revision aaaa") {
			t.Errorf("running %s, installed %s: skipped %q; want one same-revision report", c.running, c.installed, skipped)
		}
	}
}

func TestOlderInstallIsSkippedOnce(t *testing.T) {
	b, path := fake(t, "aaaa")
	var skipped []string
	b.Skipped = func(rev, reason string) { skipped = append(skipped, rev+": "+reason) }
	install(t, path, "bbbb")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, ok := b.Replacement(); ok {
			t.Fatal("an older build was offered")
		}
	}
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "bbbb: built ") {
		t.Fatalf("skipped = %q; want one report naming the build time", skipped)
	}
	install(t, path, "cccc")
	if rev, ok := b.Replacement(); !ok || rev != "cccc" {
		t.Fatalf("a newer install after a skipped one: %q, %v", rev, ok)
	}
}

// info is a fake buildinfo for a build of commit rev at t.
func info(rev string, t time.Time, modified bool) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Path: "yardr.dev/yardr", Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: rev},
		{Key: "vcs.time", Value: t.Format(time.RFC3339)},
		{Key: "vcs.modified", Value: map[bool]string{true: "true", false: "false"}[modified]},
	}}
}

func TestOnlyANewerBuildInfoIsNewer(t *testing.T) {
	t0 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	running := buildOf(info("1111111111111111111111111111111111111111", t0, false))
	if running.Revision != "111111111111" || !running.Time.Equal(t0) {
		t.Fatalf("buildOf = %+v", running)
	}
	for _, c := range []struct {
		name  string
		cand  *debug.BuildInfo
		newer bool
	}{
		{"older", info("0000000000000000000000000000000000000000", t0.Add(-time.Hour), false), false},
		{"same", info("1111111111111111111111111111111111111111", t0, false), false},
		{"same, dirty", info("1111111111111111111111111111111111111111", t0, true), false},
		{"same time, other commit", info("2222222222222222222222222222222222222222", t0, false), false},
		{"newer", info("3333333333333333333333333333333333333333", t0.Add(time.Hour), false), true},
		{"newer, dirty", info("3333333333333333333333333333333333333333", t0.Add(time.Hour), true), true},
	} {
		reason := Newer(running, buildOf(c.cand))
		if (reason == "") != c.newer {
			t.Errorf("%s: Newer = %q; want newer=%v", c.name, reason, c.newer)
		}
	}
}

func TestModuleVersionInstallsAreCompared(t *testing.T) {
	// go install yardr.dev/yardr/cmd/yardr@<version> stamps no vcs settings.
	mod := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "yardr.dev/yardr", Version: v}}
	}
	t0 := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	running := buildOf(info("abcdefabcdef0000000000000000000000000000", t0, false))

	same := buildOf(mod("v0.0.0-20261002120000-abcdefabcdef"))
	if same.Revision != "abcdefabcdef" || !same.Time.Equal(t0) {
		t.Fatalf("pseudo-version read as %+v", same)
	}
	if Newer(running, same) == "" {
		t.Error("an @pseudo-version install of the running commit is newer")
	}
	if r := Newer(running, buildOf(mod("v1.2.4-0.20261002130000-123456123456"))); r != "" {
		t.Errorf("a later pseudo-version is not newer: %s", r)
	}
	if Newer(running, buildOf(mod("v0.0.0-20261002110000-123456123456"))) == "" {
		t.Error("an earlier pseudo-version is newer")
	}

	v120 := buildOf(mod("v1.2.0"))
	v130 := buildOf(mod("v1.3.0"))
	if v120.Revision != "v1.2.0" {
		t.Fatalf("release read as %+v", v120)
	}
	// Releases are ordered by version, whatever their files' times.
	v120.Time, v130.Time = t0.Add(time.Hour), t0
	if r := Newer(v120, v130); r != "" {
		t.Errorf("v1.3.0 not newer than v1.2.0: %s", r)
	}
	if Newer(v130, v120) == "" || Newer(v130, buildOf(mod("v1.3.0-rc.1"))) == "" {
		t.Error("an older release is newer")
	}
	if Newer(buildOf(mod("v1.3.0-rc.9")), buildOf(mod("v1.3.0-rc.10"))) != "" {
		t.Error("rc.10 should be newer than rc.9")
	}
	if buildOf(mod("(devel)")) != (Build{}) {
		t.Error("(devel) read as a version")
	}
}

func TestFailedReplaceIsNotRetriedUntilTheFileChanges(t *testing.T) {
	b, path := fake(t, "aaaa")
	var calls int
	var gotPath string
	var gotEnv []string
	b.Exec = func(p string, _, env []string) error {
		calls++
		gotPath, gotEnv = p, env
		return errors.New("exec format error")
	}
	install(t, path, "bbbb")
	if _, ok := b.Replacement(); !ok {
		t.Fatal("not offered")
	}
	t.Setenv("YARDR_REPLACED_FROM", "stale")
	if err := b.Replace("YARDR_REPLACED_FROM=aaaa"); err == nil {
		t.Fatal("exec error swallowed")
	}
	if calls != 1 || gotPath != path {
		t.Fatalf("exec calls=%d path=%s", calls, gotPath)
	}
	var from []string
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "YARDR_REPLACED_FROM=") {
			from = append(from, kv)
		}
	}
	if !slices.Equal(from, []string{"YARDR_REPLACED_FROM=aaaa"}) {
		t.Fatalf("env has %v; the override must replace the inherited value", from)
	}
	if _, ok := b.Replacement(); ok {
		t.Fatal("a binary that failed to exec was offered again")
	}
	install(t, path, "cccc")
	if rev, ok := b.Replacement(); !ok || rev != "cccc" {
		t.Fatalf("a newer install after a failure: %q, %v", rev, ok)
	}
}

func TestOfFileReadsThisTestBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OfFile(exe); err != nil {
		t.Fatalf("OfFile(test binary): %v", err)
	}
	if _, err := OfFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file read")
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct{ tag, rev, want string }{
		{"", "", "unknown"},
		{"", "abcdefabcdef", "abcdefabcdef"},
		{"", "abcdefabcdef-modified", "abcdefabcdef-modified"},
		{"v1.2.3", "abcdefabcdef", "v1.2.3 (abcdefabcdef)"},
		// A release built outside a checkout carries no revision.
		{"v1.2.3", "", "v1.2.3"},
	} {
		if got := describe(c.tag, c.rev); got != c.want {
			t.Errorf("describe(%q, %q) = %q; want %q", c.tag, c.rev, got, c.want)
		}
	}
}

// Replace leaves every descriptor above 2 close-on-exec for the new image:
// a file this process holds open without the flag, as the C library holds
// its resolver socket, is flagged, and 0, 1 and 2 are untouched. Without
// this each replace adds the next image's own resolver socket to the
// inherited ones, one per install (yardr-b4ep).
func TestReplaceClosesInheritedDescriptorsOnExec(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Clear close-on-exec, as libc leaves its own descriptors.
	fd := int(f.Fd())
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); errno != 0 {
		t.Fatal(errno)
	}
	stdio := make([]uintptr, 3)
	for i := range stdio {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(i), syscall.F_GETFD, 0)
		if errno != 0 {
			t.Skipf("cannot read the flags of fd %d: %v", i, errno)
		}
		stdio[i] = flags
	}
	b, _ := fake(t, "aaaa")
	if err := b.Replace(); err != nil {
		t.Fatal(err)
	}
	if flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); errno != 0 {
		t.Fatal(errno)
	} else if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("fd %d is inherited across exec after Replace", fd)
	}
	for i, before := range stdio {
		if after, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(i), syscall.F_GETFD, 0); errno != 0 {
			t.Fatal(errno)
		} else if after != before {
			t.Errorf("fd %d flags changed across Replace: %#x, was %#x", i, after, before)
		}
	}
}
