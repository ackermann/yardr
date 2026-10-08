package pack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
)

// InstallRole writes one crew member's role where an import of the pack
// installs it, naming the operator and the yard's crew member that takes
// it. A file already there is kept unless it may be replaced, and a pack
// with no such crew member has nothing to install.
func TestInstallRole(t *testing.T) {
	p, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	want := filepath.Join(config.At(home).PackRoles("starter"), "yardmaster.md")
	path, wrote, err := pack.InstallRole(p, home, "yardmaster", "boss", "Jo", false)
	if err != nil || path != want || !wrote {
		t.Fatalf("InstallRole = %q, %v, %v", path, wrote, err)
	}
	text, err := os.ReadFile(want)
	if err != nil || strings.Contains(string(text), "{{") || !strings.Contains(string(text), "bring to Jo") {
		t.Fatalf("installed: %v\n%s", err, text)
	}
	if info, _ := os.Stat(want); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", info.Mode())
	}
	// As it is: nothing to write, whether or not it may be replaced.
	for _, replace := range []bool{false, true} {
		if path, wrote, err := pack.InstallRole(p, home, "yardmaster", "boss", "Jo", replace); err != nil || path != want || wrote {
			t.Errorf("again (replace %v) = %q, %v, %v", replace, path, wrote, err)
		}
	}
	// Told another operator: kept, and written only where it may be replaced.
	if path, wrote, err := pack.InstallRole(p, home, "yardmaster", "boss", "Kim", false); err != nil || path != want || wrote {
		t.Errorf("another operator, kept = %q, %v, %v", path, wrote, err)
	}
	if b, _ := os.ReadFile(want); string(b) != string(text) {
		t.Error("a file that was to be kept was rewritten")
	}
	if _, wrote, err := pack.InstallRole(p, home, "yardmaster", "boss", "Kim", true); err != nil || !wrote {
		t.Errorf("another operator, replaced: %v, %v", wrote, err)
	}
	if b, _ := os.ReadFile(want); !strings.Contains(string(b), "bring to Kim") {
		t.Errorf("the replaced file does not name Kim:\n%s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(want), ".*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
	if path, wrote, err := pack.InstallRole(p, home, "scribe", "scribe", "Jo", true); path != "" || wrote || err != nil {
		t.Errorf("a crew member the pack lacks = %q, %v, %v", path, wrote, err)
	}
}
