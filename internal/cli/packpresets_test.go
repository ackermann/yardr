package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
)

// presetShowYAML are two presets for kindPack's acme source: read, and
// everything without an about.
const presetShowYAML = `presets:
  read:
    about: Read-only words
    grants:
      acme: [read]
  everything:
    grants:
      acme: [read, ping]
`

func addPresets(t *testing.T, dir string) {
	t.Helper()
	file := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(text, presetShowYAML...), 0o644); err != nil {
		t.Fatal(err)
	}
}

const presetShowText = `presets of pack acme-pack (the pack's suggested grants, granted by the operator):
  1. read: Read-only words
     acme: read
  2. everything
     acme: read, ping
`

// pack show prints a pack's presets after its setup notes, numbered in
// file order, each with the words it grants of each source; --json
// carries them as data.
func TestPackShowPrintsPresets(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	dir := setupPack(t)
	addPresets(t, dir)
	out, err := run(t, a.packCmd(), "show", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := setupMark + "\n" + setupNotes + "\n" + presetShowText; out != want {
		t.Fatalf("show prints:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "show", dir)
	if err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	var got struct {
		Pack    string        `json:"pack"`
		Presets []pack.Preset `json:"presets"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	want := []pack.Preset{
		{Name: "read", About: "Read-only words",
			Grants: []pack.CallsAsked{{Source: "acme", Calls: []string{"read"}}}},
		{Name: "everything",
			Grants: []pack.CallsAsked{{Source: "acme", Calls: []string{"read", "ping"}}}},
	}
	if got.Pack != "acme-pack" || len(got.Presets) != 2 ||
		got.Presets[0].Name != want[0].Name || got.Presets[0].About != want[0].About ||
		got.Presets[1].Name != want[1].Name || len(got.Presets[1].Grants) != 1 ||
		strings.Join(got.Presets[1].Grants[0].Calls, ",") != "read,ping" {
		t.Fatalf("json presets = %+v, want %+v", got.Presets, want)
	}
}

// A pack with presets but no setup notes says it has none, then prints
// the presets.
func TestPackShowPresetsWithoutNotes(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	dir := kindPack(t)
	addPresets(t, dir)
	out, err := run(t, a.packCmd(), "show", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "pack acme-pack carries no setup notes\n\n" + presetShowText; out != want {
		t.Fatalf("show prints:\n%s", out)
	}
}

// pack check reports a preset's mistakes with file, line and key, as any
// mistake of the pack.
func TestPackCheckReportsPresetMistakes(t *testing.T) {
	t.Parallel()
	dir := kindPack(t)
	file := filepath.Join(dir, "pack.yaml")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	bad := "presets:\n  read:\n    grants:\n      other: [read]\n      acme: [read, missing]\n"
	if err := os.WriteFile(file, append(text, bad...), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, packApp(t).packCmd(), "check", dir)
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCode(err))
	}
	for _, want := range []string{
		file + ":9: presets.read.grants.other: the pack declares no source other (it declares: acme)",
		file + ":10: presets.read.grants.acme: acme.yaml declares no call missing (it has: ping)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}
