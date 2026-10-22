package pack

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The digest cannot be had by moving a boundary: a file renamed, or bytes
// moved from the end of one file to the start of the next, is another
// digest, and a name with a NUL in it, which would move the end of a path,
// is no name. The pack has two named files, a crew member's role and a
// group's agent, and pack.yaml.
func TestDigestRecordsKeepTheirBoundaries(t *testing.T) {
	digest := func(role, agent string, files map[string]string) (string, error) {
		fsys := fstest.MapFS{"pack.yaml": {Data: []byte("name: p\n")}}
		for name, text := range files {
			fsys[name] = &fstest.MapFile{Data: []byte(text)}
		}
		p := &Pack{Dir: "d", reads: newReads(fsys),
			Groups: []Group{{Name: "g", Agent: agent}}, Crew: []CrewMember{{Name: "c", Role: role}}}
		return p.digest()
	}
	base, err := digest("a", "b", map[string]string{"a": "xy", "b": "z"})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		role, agent string
		files       map[string]string
	}{
		"a file renamed":    {"a2", "b", map[string]string{"a2": "xy", "b": "z"}},
		"a byte moved on":   {"a", "b", map[string]string{"a": "x", "b": "yz"}},
		"a byte moved back": {"a", "b", map[string]string{"a": "xyz", "b": ""}},
	} {
		if got, err := digest(c.role, c.agent, c.files); err != nil || got == base {
			t.Errorf("%s: digest %q, %v, want one other than %q", name, got, err, base)
		}
	}
	if again, err := digest("a", "b", map[string]string{"a": "xy", "b": "z"}); err != nil || again != base {
		t.Errorf("the same files again: %q, %v, want %q", again, err, base)
	}
	if got, err := digest("a\x00b", "b", map[string]string{"b": "z"}); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Errorf("a name with a NUL: %q, %v, want an error saying so", got, err)
	}
}

// The digest names every file the pack's reader and planner read: a file
// they take through reads and the digest leaves out is one a server could
// change under a pin. The packs are the ones the repository ships that
// hold every kind of file (roles, scripts, stage files, setup notes, and in
// the template a declaration with its command programs).
func TestDigestNamesEveryFileRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	for _, d := range []store.Depot{{Name: "web", Base: "main"}, {Name: "notes", Kind: store.DepotDir, Path: t.TempDir()}} {
		if err := st.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"starter", "dir", "website", "template"} {
		t.Run(name, func(t *testing.T) {
			p, err := LoadDir(filepath.Join("..", "..", "packs", name))
			if err != nil {
				t.Fatal(err)
			}
			o := Options{Home: t.TempDir()}
			switch {
			case p.Kind == KindSource:
			case p.Kind == store.DepotDir:
				o.Depot = "notes"
			default:
				o.Depot = "web"
			}
			if _, err := Build(ctx, st, p, o); err != nil {
				t.Fatal(err)
			}
			named := p.named()
			if len(p.reads.files) < 2 {
				t.Fatalf("the pack read %d files: the test sees nothing", len(p.reads.files))
			}
			for read := range p.reads.files {
				if !slices.Contains(named, read) {
					t.Errorf("%s was read and the digest does not name it (named: %v)", read, named)
				}
			}
		})
	}
}
