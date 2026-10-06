package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// The yardmaster's role was mayor.md until the resident was renamed. For
// this release a prompt that names the file by one of the two names reads
// the other when no file has the name asked for: a yard set up before the
// rename has only mayor.md, and a rig that renamed its file is still named
// by a prompt stored before. A file that is there under the name asked for
// always wins, and no other role is looked up under another name.
func TestYardmasterRoleIsReadUnderItsOldName(t *testing.T) {
	write := func(t *testing.T, path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old, renamed, both := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(old, "roles", "mayor.md"), "old name")
	write(t, filepath.Join(renamed, "roles", "yardmaster.md"), "new name")
	write(t, filepath.Join(both, "roles", "mayor.md"), "old name")
	write(t, filepath.Join(both, "roles", "yardmaster.md"), "new name")
	for _, tc := range []struct {
		name, prompt, home, want string
	}{
		{"a role, in a home from before", "roles/yardmaster.md", old, "old name"},
		{"a bare name, in a home from before", "yardmaster.md", old, "old name"},
		{"a path, in a home from before", filepath.Join(old, "roles", "yardmaster.md"), "", "old name"},
		{"a role stored before, the file renamed", "roles/mayor.md", renamed, "new name"},
		{"a path stored before, the file renamed", filepath.Join(renamed, "roles", "mayor.md"), "", "new name"},
		{"both there: the new name", "roles/yardmaster.md", both, "new name"},
		{"both there: the old name", filepath.Join(both, "roles", "mayor.md"), "", "old name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := readPrompt(tc.prompt, nil, tc.home)
			if err != nil || string(text) != tc.want {
				t.Fatalf("readPrompt(%s) = %q %v, want %q", tc.prompt, text, err, tc.want)
			}
		})
	}
	if _, err := readPrompt("roles/builder.md", nil, old); err == nil {
		t.Error("a role the home lacks was read")
	}
	if _, err := readPrompt(filepath.Join(t.TempDir(), "mayor.md"), nil, ""); err == nil {
		t.Error("a path with neither file beside it was read")
	}
}
