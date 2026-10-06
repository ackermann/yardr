package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The yardmaster was the mayor until this release. What a script typed then
// still works and is not listed: --mayor of init and depot init, --with-mayor,
// the setting mayor. yardr answers with the new word.
func TestTheMayorsFlagsAndSettingStillWork(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo", "--mayor", "boss"); err != nil {
		t.Fatalf("init --mayor: %v\n%s", err, out)
	}
	if _, err := st.GetCrew(ctx, "boss"); err != nil {
		t.Fatalf("the crew member --mayor named: %v", err)
	}
	if got, _ := st.Settings(ctx); got["yardmaster"] != "boss" || got["mayor"] != "" {
		t.Fatalf("settings = %v, want yardmaster boss and no mayor", got)
	}
	// Given under its old name it counts as given: init again changes it.
	out, err := run(t, a.rootCmd(), "init", "--mayor", "chief")
	if err != nil || strings.Contains(out, "yardmaster boss, as set before") {
		t.Fatalf("init --mayor again: %v\n%s", err, out)
	}
	if got, _ := st.Settings(ctx); got["yardmaster"] != "chief" {
		t.Fatalf("settings after init --mayor chief = %v", got)
	}

	if out, err = run(t, a.settingsCmd(), "set", "mayor", "boss"); err != nil || out != "yardmaster = boss\n" {
		t.Fatalf("settings set mayor boss = %q %v", out, err)
	}
	if out, err = run(t, a.settingsCmd(), "get", "mayor"); err != nil || out != "boss\n" {
		t.Fatalf("settings get mayor = %q %v", out, err)
	}
	if out, err = run(t, a.settingsCmd(), "get"); err != nil || strings.Contains(out, "mayor") {
		t.Fatalf("settings get lists the old key: %v\n%s", err, out)
	}

	dir := t.TempDir()
	if err := st.PutDepot(ctx, store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	// depot init takes them still and does nothing with either, as under
	// their new names: the roles are a pack's now.
	errs := captureStderr(t)
	if out, err := run(t, a.depotCmd(), append([]string{"init", "notes", "--mayor", "lead", "--with-mayor", "--yes"}, dirFlags...)...); err != nil {
		t.Fatalf("depot init --mayor --with-mayor: %v\n%s", err, out)
	}
	if got := errs.String(); !strings.Contains(got, "--yardmaster is ignored: ") || !strings.Contains(got, "--with-yardmaster is ignored: ") {
		t.Errorf("depot init --mayor --with-mayor does not say they are ignored:\n%s", got)
	}
	if files := snapshot(t, dir); len(files) != 0 {
		t.Errorf("depot init wrote into the directory: %v", files)
	}

	for _, args := range [][]string{{"init", "--help"}, {"depot", "init", "--help"}} {
		out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), args...)
		if err != nil || !strings.Contains(out, "yardmaster") || strings.Contains(strings.ToLower(out), "mayor") {
			t.Errorf("yardr %s names the mayor, or not the yardmaster: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// A role template a yard keeps of its own may still say {{mayor}}.
func TestFillRoleFillsTheOldPlaceholder(t *testing.T) {
	got := fillRole([]byte("{{operator}}: mail {{mayor}}, or {{yardmaster}}"), "Jo", "boss")
	if string(got) != "Jo: mail boss, or boss" {
		t.Fatalf("filled = %q", got)
	}
}

// A yard set up before the rename, and before a yard had a default pack,
// has its yardmaster's role in <home>/roles/mayor.md, and its crew member's
// prompt is that path. Init again leaves all of it as it is: the role is
// not refreshed, though never edited, no yardmaster.md is written beside it
// and nothing of a pack is installed. The crew member keeps its name and
// its prompt.
func TestInitKeepsAYardsMayor(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	// The yard as the build before made it and the store's upgrade left it.
	dir := filepath.Join(home, "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("# Mayor\n\nThe build before wrote this for Jo.\n")
	manifest, _ := json.Marshal(map[string]string{"mayor.md": digest(old)})
	for name, body := range map[string][]byte{"mayor.md": old, shippedManifest: manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prompt := filepath.Join(dir, "mayor.md")
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "mayor", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "prompt": prompt}}, "me"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"default_kind": "claude", "operator": "Jo", "yardmaster": "mayor"} {
		if err := st.SetSetting(ctx, k, v, "me"); err != nil {
			t.Fatal(err)
		}
	}

	out, err := run(t, a.rootCmd(), "init")
	if err != nil {
		t.Fatalf("init again: %v\n%s", err, out)
	}
	for _, want := range []string{"yardmaster mayor, as set before", "  yardmaster mayor, already a crew member: kept as it was",
		"  roles      " + dir + ", from an earlier yardr, is read as before", "init no longer writes or refreshes it"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q:\n%s", want, out)
		}
	}
	rs, err := st.Crew(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "mayor" || rs[0].Config["prompt"] != prompt {
		t.Fatalf("crew members = %+v %v", rs, err)
	}
	if b, _ := os.ReadFile(prompt); !bytes.Equal(b, old) {
		t.Errorf("mayor.md was rewritten:\n%s", b)
	}
	var files []string
	for p := range snapshot(t, home) {
		files = append(files, strings.TrimPrefix(p, home+"/"))
	}
	if slices.Sort(files); !slices.Equal(files, []string{"roles/" + shippedManifest, "roles/mayor.md"}) {
		t.Errorf("init again wrote into the home: %v", files)
	}
}
