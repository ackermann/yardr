package pack_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
)

// presetDecl is a declaration with a read call and an update call, as a
// preset names them: read for the reads, a call by its name.
const presetDecl = `name: acme
about: Acme issues.
base: https://acme.test
credential: { env: ACME_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
  note: { kind: update, request: { method: POST, path: /note } }
`

func presetPackFiles() map[string]string {
	return map[string]string{
		"pack.yaml": `name: acme-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
presets:
  zz-all:
    about: Everything the pack lists
    grants:
      acme: [read, note]
  aa-read:
    grants:
      acme: [read, ping]
`,
		"acme.yaml": presetDecl,
	}
}

// A pack of kind source may name its suggested grants: each preset an
// optional one line of about and, per source the pack declares, read
// and calls by name. What the file lists in which order is kept: pack
// show prints them numbered as they stand.
func TestSourcePackPresetsLoad(t *testing.T) {
	p := load(t, writePack(t, presetPackFiles()))
	want := []pack.Preset{
		{Name: "zz-all", About: "Everything the pack lists",
			Grants: []pack.CallsAsked{{Source: "acme", Calls: []string{"read", "note"}}}},
		{Name: "aa-read",
			Grants: []pack.CallsAsked{{Source: "acme", Calls: []string{"read", "ping"}}}},
	}
	if !reflect.DeepEqual(p.Presets, want) {
		t.Fatalf("presets = %+v, want %+v", p.Presets, want)
	}
}

func TestSourcePackPresetRefusals(t *testing.T) {
	base := `name: acme-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
`
	cases := []struct {
		name     string
		yaml     string // the pack's pack.yaml; "" appends presets: to base
		presets  string // the presets mapping, appended after "presets:"
		key, msg string
		line     int
	}{
		{name: "a source the pack does not declare",
			presets: "  read:\n    grants:\n      other: [read]\n",
			key:     "presets.read.grants.other", line: 9, msg: "the pack declares no source other (it declares: acme)"},
		{name: "a call the declaration does not name",
			presets: "  read:\n    grants:\n      acme: [read, missing]\n",
			key:     "presets.read.grants.acme", line: 9, msg: "acme.yaml declares no call missing (it has: ping, note)"},
		{name: "an empty preset",
			presets: "  read:\n    about: Read-only words\n    grants: {}\n",
			key:     "presets.read.grants", line: 9, msg: "empty: name a source the pack declares and its calls"},
		{name: "a preset without grants",
			presets: "  read:\n    about: Read-only words\n",
			key:     "presets.read.grants", line: 7, msg: "missing: the calls the preset grants"},
		{name: "a source with no calls",
			presets: "  read:\n    grants:\n      acme: []\n",
			key:     "presets.read.grants.acme", line: 9, msg: "empty: name read or a call"},
		{name: "presets on a pack for a depot",
			yaml: `name: acme-pack
kind: git
presets:
  read:
    grants:
      acme: [read]
`,
			key: "presets", line: 3, msg: "a pack of kind git carries no presets"},
		{name: "presets on a pack without a kind",
			yaml: `name: acme-pack
presets:
  read:
    grants:
      acme: [read]
`,
			key: "presets", line: 2, msg: "a pack without a kind carries no presets"},
		{name: "an unknown preset key",
			presets: "  read:\n    about: Read-only words\n    grant: {}\n",
			key:     "presets.read.grant", line: 9, msg: "unknown key (known here: about, grants)"},
		{name: "an about of two lines",
			presets: "  read:\n    about: \"Read-only words\\non two lines\"\n    grants:\n      acme: [read]\n",
			key:     "presets.read.about", line: 8, msg: "about is one line"},
		{name: "a call listed twice",
			presets: "  read:\n    grants:\n      acme: [read, read]\n",
			key:     "presets.read.grants.acme", line: 9, msg: "read is listed twice"},
		{name: "a preset that is no mapping",
			presets: "  read: [read]\n",
			key:     "presets.read", line: 7, msg: "want a mapping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"acme.yaml": presetDecl}
			if c.yaml != "" {
				files["pack.yaml"] = c.yaml
			} else {
				files["pack.yaml"] = base + "presets:\n" + c.presets
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want the pack's errors", err)
			}
			want := filepath.Join(dir, "pack.yaml")
			if !slices.ContainsFunc(errs, func(e pack.Error) bool {
				return e.File == want && e.Key == c.key && e.Line == c.line && strings.Contains(e.Msg, c.msg)
			}) {
				t.Fatalf("no error at pack.yaml:%d %s with %q:\n%v", c.line, c.key, c.msg, errs)
			}
		})
	}
}
