package version

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fake is a "binary" whose contents are its revision.
func fake(t *testing.T, running string) (*Binary, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yardr")
	install(t, path, running)
	b, err := Watch(path, running)
	if err != nil {
		t.Fatal(err)
	}
	b.ReadRevision = func(p string) (string, error) {
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			return "", errors.New("not a binary")
		}
		return string(data), nil
	}
	b.Exec = func(string, []string, []string) error { return nil }
	return b, path
}

// install replaces the file the way go install does: a new inode renamed
// over the old path.
func install(t *testing.T, path, rev string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(rev), 0o755); err != nil {
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

func TestModifiedBuildAlwaysCounts(t *testing.T) {
	b, path := fake(t, "aaaa-modified")
	install(t, path, "aaaa-modified")
	if _, ok := b.Replacement(); !ok {
		t.Fatal("two -modified builds cannot be told apart; a change must count")
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
