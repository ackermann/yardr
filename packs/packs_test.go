package packs_test

import (
	"io/fs"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/packs"
)

// Every embedded pack is a valid pack under the name of its directory, which
// is the name it is found by, and the starter is among them. A file the
// embed left out (a role, a script, a stage file) fails here: the pack names
// it and Load looks for it.
func TestEmbeddedPacksAreValid(t *testing.T) {
	entries, err := fs.ReadDir(packs.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		sub, err := fs.Sub(packs.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		p, err := pack.Load(sub, packs.Embedded)
		if err != nil {
			t.Errorf("pack %s: %v", e.Name(), err)
			continue
		}
		if p.Name != e.Name() {
			t.Errorf("the pack embedded as %s calls itself %s", e.Name(), p.Name)
		}
	}
	if len(names) != 1 || names[0] != "starter" {
		t.Errorf("embedded packs = %v, want starter", names)
	}
}
