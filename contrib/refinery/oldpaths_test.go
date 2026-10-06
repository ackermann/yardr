package refinery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A group's cmd that names a script under contrib/refinery, as it was set up
// before the directory became contrib/assembly, runs the script of that
// name in contrib/assembly with its arguments and environment, and the two
// that were named for epics run what they are called now. Every script of
// contrib/assembly has its old path.
func TestOldPathsRunTheAssemblyScripts(t *testing.T) {
	old := map[string]string{"merge-local": "merge-local", "integrate-train": "integrate-train", "pr-train": "pr-train",
		"pr-merge": "pr-merge", "integrate-epic": "integrate-train", "pr-epic": "pr-train"}
	dir := t.TempDir()
	for _, d := range []string{"refinery", "assembly"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, runs := range old {
		info, err := os.Stat(name)
		if err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("%s: %v, mode %v: want an executable file", name, err, info.Mode())
		}
		if _, err := os.Stat(filepath.Join("..", "assembly", runs)); err != nil {
			t.Fatalf("%s runs contrib/assembly/%s: %v", name, runs, err)
		}
		text, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "refinery", name), text, 0o755); err != nil {
			t.Fatal(err)
		}
		// In place of the real script: one that says who it is and what it got.
		stub := "#!/bin/sh\necho \"" + runs + " $YARDR_BEAD $*\"\n"
		if err := os.WriteFile(filepath.Join(dir, "assembly", runs), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join(dir, "refinery", name), "a", "b c")
		cmd.Env = append(os.Environ(), "YARDR_BEAD=r-1")
		// From elsewhere: the script finds its neighbour by its own path.
		cmd.Dir = t.TempDir()
		out, err := cmd.CombinedOutput()
		if want := runs + " r-1 a b c"; err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%s printed %q (%v), want %q", name, out, err, want)
		}
	}
	scripts, err := os.ReadDir(filepath.Join("..", "assembly"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range scripts {
		if info, err := e.Info(); err != nil || e.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if _, ok := old[e.Name()]; !ok {
			t.Errorf("contrib/assembly/%s has no old path in contrib/refinery", e.Name())
		}
	}
}
