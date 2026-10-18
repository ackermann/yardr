package pack_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"yardr.dev/yardr/internal/pack"
)

// presetFS is packs by directory: a kind-source pack declaring acme with
// read and all presets, one declaring beta with a preset for beta alone,
// and a depot pack naming acme without declaring it.
func presetFS() fstest.MapFS {
	decl := `name: ACME
base: https://acme.test
credential: { env: ACME_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
  note: { kind: update, request: { method: POST, path: /note } }
`
	return fstest.MapFS{
		"acme-pack/pack.yaml": &fstest.MapFile{Data: []byte(`name: acme-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
presets:
  read:
    grants:
      acme: [read]
  all:
    grants:
      acme: [read, note]
`)},
		"acme-pack/acme.yaml": &fstest.MapFile{Data: []byte(strings.Replace(decl, "ACME", "acme", 1))},
		"beta-pack/pack.yaml": &fstest.MapFile{Data: []byte(`name: beta-pack
kind: source
sources:
  beta:
    declaration: beta.yaml
  acme-too:
    declaration: acme-too.yaml
presets:
  beta-only:
    grants:
      beta: [read]
`)},
		"beta-pack/beta.yaml":     &fstest.MapFile{Data: []byte(strings.Replace(decl, "ACME", "beta", 1))},
		"beta-pack/acme-too.yaml": &fstest.MapFile{Data: []byte(strings.Replace(decl, "ACME", "acme-too", 1))},
		"team-pack/pack.yaml": &fstest.MapFile{Data: []byte(`name: team-pack
kind: git
flows:
  work:
    stages:
      new:
        group: builders
      done:
        end: true
groups:
  builders:
    agent: roles/builder.md
sources:
  acme:
    as: yardmaster
    hears:
      task: { new: started }
crew:
  yardmaster:
    role: roles/yardmaster.md
`)},
		"team-pack/roles/builder.md":    &fstest.MapFile{Data: []byte("build\n")},
		"team-pack/roles/yardmaster.md": &fstest.MapFile{Data: []byte("run\n")},
	}
}

// A preset is resolved against the packs: the one pack of kind source
// declaring the source, and its preset of that name with the calls it
// grants of the source, in file order. A depot pack naming the source
// without declaring it is no part of it.
func TestPresetOfSource(t *testing.T) {
	fsys := presetFS()
	name, calls, err := pack.PresetOfSource(fsys, "acme", "all")
	if err != nil || name != "acme-pack" || strings.Join(calls, ",") != "read,note" {
		t.Fatalf("acme all = %q %v, %v", name, calls, err)
	}
	if name, calls, err := pack.PresetOfSource(fsys, "beta", "beta-only"); err != nil || name != "beta-pack" || strings.Join(calls, ",") != "read" {
		t.Fatalf("beta beta-only = %q %v, %v", name, calls, err)
	}
}

// An unknown preset, a source no pack declares, two packs declaring one
// source, and a preset granting nothing of the source are each refused
// with the terminal grant by name; the unknown preset names pack show.
func TestPresetOfSourceRefusals(t *testing.T) {
	fsys := presetFS()
	fsys["second-pack/pack.yaml"] = &fstest.MapFile{Data: []byte(`name: second-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
`)}
	fsys["second-pack/acme.yaml"] = fsys["acme-pack/acme.yaml"]
	for _, c := range []struct {
		source, preset string
		fsys           fstest.MapFS
		want           []string
	}{
		{"acme", "nosuch", presetFS(), []string{"pack acme-pack has no preset nosuch", "yardr pack show acme-pack", "in a terminal", "yardr source grant acme read|<call>"}},
		{"crm", "read", presetFS(), []string{"source crm: no embedded pack declares it", "in a terminal", "yardr source grant crm read|<call>"}},
		{"acme", "read", fsys, []string{"declared by packs acme-pack, second-pack", "in a terminal", "yardr source grant acme read|<call>"}},
		{"acme-too", "beta-only", presetFS(), []string{"the preset beta-only of pack beta-pack grants nothing of the source acme-too", "in a terminal"}},
	} {
		if _, _, err := pack.PresetOfSource(c.fsys, c.source, c.preset); err == nil {
			t.Fatalf("%s %s resolved", c.source, c.preset)
		} else {
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s %s error = %q, want %q", c.source, c.preset, err, want)
				}
			}
		}
	}
}

// A pack of another kind declaring the source carries no presets: the
// grant is by name in a terminal.
func TestPresetOfSourceOtherKind(t *testing.T) {
	fsys := fstest.MapFS{
		"acme-pack/pack.yaml": &fstest.MapFile{Data: []byte(`name: acme-pack
kind: git
sources:
  acme:
    declaration: acme.yaml
`)},
		"acme-pack/acme.yaml": &fstest.MapFile{Data: []byte("name: acme\nbase: https://acme.test\ncredential: { env: ACME_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n")},
	}
	if _, _, err := pack.PresetOfSource(fsys, "acme", "read"); err == nil ||
		!strings.Contains(err.Error(), "only a pack of kind source carries presets") ||
		!strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("a depot pack declaring the source = %v", err)
	}
}
