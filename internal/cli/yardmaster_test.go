package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/roles"
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
	if out, err := run(t, a.depotCmd(), "init", "notes", "--mayor", "lead", "--with-mayor"); err != nil {
		t.Fatalf("depot init --mayor --with-mayor: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".yardr", "roles", "yardmaster.md")); err != nil {
		t.Errorf("--with-mayor wrote no yardmaster.md: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "builder.md")); !strings.Contains(string(b), "yardr mail lead ") {
		t.Errorf("--mayor lead is not whom the builder mails:\n%s", b)
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

// A yard set up before the rename has its yardmaster's role in mayor.md,
// and its crew member's prompt is that path. Init again keeps the role there:
// refreshed in place when it was never edited, with no yardmaster.md beside
// it that nothing would read. The crew member keeps its name, which the
// operator gave it.
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
		"mayor.md refreshed to this build's", "the yardmaster's prompt is mayor.md."} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q:\n%s", want, out)
		}
	}
	rs, err := st.Crew(ctx)
	if err != nil || len(rs) != 1 || rs[0].Name != "mayor" || rs[0].Config["prompt"] != prompt {
		t.Fatalf("crew members = %+v %v", rs, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "yardmaster.md")); err == nil {
		t.Error("yardmaster.md was written beside the mayor.md the crew member reads")
	}
	tmpl, _ := roles.FS.ReadFile("yardmaster.md")
	if b, _ := os.ReadFile(prompt); !bytes.Equal(b, fillRole(tmpl, "Jo", "mayor")) {
		t.Errorf("mayor.md is not this build's yardmaster role:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "builder.md")); !strings.Contains(string(b), "yardr mail mayor ") {
		t.Errorf("the builder is not told to mail the crew member mayor:\n%s", b)
	}
}
