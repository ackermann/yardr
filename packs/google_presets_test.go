package packs_test

import (
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// Each google pack suggests three grants: read for the reads, a middle
// preset for the reads and some updates, and everything for all the pack
// lists. The test holds each preset's calls to the declaration's: read
// grants read alone, everything grants read and every update call the
// declaration names, and the middle preset stands between the two.
func TestGooglePresetsMatchDeclarations(t *testing.T) {
	middle := map[string]string{
		"google-mail":     "drafts",
		"google-calendar": "create",
		"google-docs":     "append",
		"google-drive":    "make",
	}
	middleExcludes := map[string][]string{
		"google-drive": {"copy"},
	}
	for _, name := range []string{"google-mail", "google-calendar", "google-docs", "google-drive"} {
		t.Run(name, func(t *testing.T) {
			sub, err := fs.Sub(packs.FS, name)
			if err != nil {
				t.Fatal(err)
			}
			p, err := pack.Load(sub, packs.Embedded)
			if err != nil {
				t.Fatal(err)
			}
			// The file order is the ladder: pack show numbers them as
			// they stand.
			var names []string
			byName := map[string]pack.Preset{}
			for _, pr := range p.Presets {
				names = append(names, pr.Name)
				byName[pr.Name] = pr
				if pr.About == "" || strings.Contains(pr.About, "\n") {
					t.Errorf("preset %s: about %q, want one line saying what it grants", pr.Name, pr.About)
				}
			}
			if want := []string{"read", middle[name], "everything"}; !reflect.DeepEqual(names, want) {
				t.Fatalf("presets = %v, want %v", names, want)
			}
			decls := map[string]*source.Declaration{}
			for _, d := range p.Declared {
				decls[d.Name] = d.Declaration
			}
			words := func(pr pack.Preset, source string) []string {
				for _, g := range pr.Grants {
					if g.Source == source {
						return g.Calls
					}
				}
				return nil
			}
			var between, narrower bool
			for _, d := range p.Declared {
				var updates []string
				for _, c := range d.Declaration.Calls {
					if c.Kind == source.Update {
						updates = append(updates, c.Name)
					}
				}
				if got := words(byName["read"], d.Name); !reflect.DeepEqual(got, []string{store.GrantRead}) {
					t.Errorf("preset read of %s grants %v, want [%s] alone", d.Name, got, store.GrantRead)
				}
				want := append([]string{store.GrantRead}, updates...)
				all := words(byName["everything"], d.Name)
				if !reflect.DeepEqual(all, want) {
					t.Errorf("preset everything of %s grants %v, want %v: every update call the declaration names", d.Name, all, want)
				}
				mid := words(byName[middle[name]], d.Name)
				if mid == nil {
					t.Errorf("preset %s grants nothing of %s", middle[name], d.Name)
					continue
				}
				for _, excluded := range middleExcludes[name] {
					if slices.Contains(mid, excluded) {
						t.Errorf("preset %s of %s grants %s, which is reserved for everything", middle[name], d.Name, excluded)
					}
				}
				if !slices.Contains(mid, store.GrantRead) {
					t.Errorf("preset %s of %s grants %v, without %s", middle[name], d.Name, mid, store.GrantRead)
				}
				for _, w := range mid {
					switch {
					case w == store.GrantRead:
					case slices.Contains(all, w):
						between = true
					default:
						t.Errorf("preset %s of %s grants %s, which everything does not", middle[name], d.Name, w)
					}
				}
				for _, w := range all {
					if !slices.Contains(mid, w) {
						narrower = true
					}
				}
			}
			if !between {
				t.Errorf("preset %s grants no update call: it is read again", middle[name])
			}
			if !narrower {
				t.Errorf("preset %s grants all everything grants: it is everything again", middle[name])
			}
		})
	}
}
