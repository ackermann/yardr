package pack_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
)

// A pack of any kind may carry setup notes, its author's words about what
// a setup takes: a file of the pack, whose text the pack keeps. A pack
// without the key carries none.
func TestSetupLoads(t *testing.T) {
	withSetup := func(file, notes string) map[string]string {
		files := smallFiles(strings.Replace(small, "name: small\n", "name: small\nsetup: "+file+"\n", 1))
		files["SETUP.md"] = notes
		return files
	}
	p := load(t, writePack(t, withSetup("SETUP.md", "# Setup\n\nTick Issues and Comments.\n")))
	if p.Setup != "SETUP.md" || p.SetupText != "# Setup\n\nTick Issues and Comments.\n" {
		t.Fatalf("setup = %q, %q", p.Setup, p.SetupText)
	}
	// At the limit, still a pack.
	big := strings.Repeat("n", pack.MaxSetup)
	if p := load(t, writePack(t, withSetup("SETUP.md", big))); p.SetupText != big {
		t.Fatalf("setup at the limit does not load: %d bytes", len(p.SetupText))
	}
	// A pack of kind source carries them too.
	src := sourcePackFiles()
	src["pack.yaml"] = strings.Replace(sourcePackYAML, "kind: source\n", "kind: source\nsetup: SETUP.md\n", 1)
	src["SETUP.md"] = "webhook first\n"
	if p := load(t, writePack(t, src)); p.Setup != "SETUP.md" || p.SetupText != "webhook first\n" {
		t.Fatalf("source pack setup = %q, %q", p.Setup, p.SetupText)
	}
	// Without the key, nothing.
	if p := load(t, writePack(t, smallFiles(small))); p.Setup != "" || p.SetupText != "" {
		t.Fatalf("setup = %q, %q", p.Setup, p.SetupText)
	}
}

func TestSetupRefusals(t *testing.T) {
	cases := []struct {
		name     string
		setup    string            // the setup line's value
		files    map[string]string // added to the pack
		key, msg string
	}{
		{name: "missing notes file", setup: "SETUP.md", key: "setup", msg: "setup notes SETUP.md is missing"},
		{name: "notes outside the pack", setup: "../SETUP.md", key: "setup", msg: "leaves the pack's directory"},
		{name: "absolute notes path", setup: "/SETUP.md", key: "setup", msg: "relative to its directory, not absolute"},
		{name: "empty notes file", setup: "SETUP.md", files: map[string]string{"SETUP.md": " \n"}, key: "setup", msg: "setup notes SETUP.md are empty: leave setup out for a pack without any"},
		{name: "notes over the limit", setup: "SETUP.md", files: map[string]string{"SETUP.md": strings.Repeat("n", pack.MaxSetup+1)}, key: "setup", msg: "at most 65536"},
		{name: "notes are a file, not a list", setup: "[SETUP.md]", key: "setup", msg: "want a string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := strings.Replace(small, "name: small\n", "name: small\nsetup: "+c.setup+"\n", 1)
			files := smallFiles(text)
			for name, content := range c.files {
				files[name] = content
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("Load = %v, want the pack's errors", err)
			}
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want one", errs)
			}
			e := errs[0]
			if e.File != filepath.Join(dir, "pack.yaml") || e.Key != c.key || e.Line != 2 || !strings.Contains(e.Msg, c.msg) {
				t.Fatalf("error = %s\nwant key %s, line 2, message containing %q", e, c.key, c.msg)
			}
		})
	}
}
