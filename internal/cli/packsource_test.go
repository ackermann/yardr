package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// docYAML is the first yaml block of docs/pushover.md under the heading
// that starts with under: what a reader of the page copies.
func docYAML(t *testing.T, under string) string {
	t.Helper()
	text, err := os.ReadFile("../../docs/pushover.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(text), "\n"+under)
	if !ok {
		t.Fatalf("docs/pushover.md has no heading %q", under)
	}
	_, rest, ok = strings.Cut(rest, "```yaml\n")
	block, _, closed := strings.Cut(rest, "```\n")
	if !ok || !closed {
		t.Fatalf("docs/pushover.md has no yaml block under %q", under)
	}
	return block
}

// pushoverPack is a pack made of what docs/pushover.md gives: its
// declaration, and the lines of pack.yaml that name it.
func pushoverPack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"pack.yaml":             "name: notify\n" + docYAML(t, "## In a pack"),
		"sources/pushover.yaml": docYAML(t, "## 3. The declaration"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The file docs/pushover.md tells a reader to save is the one the tests of
// yardr source and of the push send through, to the byte.
func TestThePushoverPageHasTheTestedDeclaration(t *testing.T) {
	want, err := os.ReadFile("testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := docYAML(t, "## 3. The declaration"); got != string(want) {
		t.Fatalf("docs/pushover.md and testdata/pushover.yaml differ:\n%s", got)
	}
}

// A pack with the page's declaration is shown by pack check with what the
// yard must still be told, and imported by the operator: yardr source show
// then prints the source, and the next plan says what is there.
func TestPackApplyAddsTheDeclaredSource(t *testing.T) {
	a := openApp(t)
	ctx := context.Background()
	dir := pushoverPack(t)

	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\ndeclared sources\n  create           pushover\n",
		"\ndeclared source pushover, from sources/pushover.yaml\n  its calls go to https://api.pushover.net/1: message (update)\n  would be added to the yard's sources\n",
		"  missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source set pushover env_file=<a file of mode 0600 that sets PUSHOVER_TOKEN>\n",
		"  missing  notify       it can tell a user outside the yard and no user names it. The import names none: yardr user set <user> notify=pushover env_file=<a file of mode 0600 that sets PUSHOVER_USER>\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if _, err := a.st.GetSource(ctx, "pushover"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pack check wrote the source: %v", err)
	}

	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)\n",
		"next, for source pushover: no env_file is set for it, and a pack neither carries nor names one: yardr source set pushover env_file=",
		"next, for source pushover: it can tell a user outside the yard and no user names it.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	out, err = run(t, a.sourceCmd(), "show", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pushover  https://api.pushover.net/1\n",
		"  call message  update  POST /messages.json  One push to one person\n",
		"  notify: the call message tells a user, at the PUSHOVER_USER of their env_file (yardr user set <name> notify=pushover)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("source show lacks %q:\n%s", want, out)
		}
	}

	// With the credential and a user set, the plan says so and names no value.
	if _, err := run(t, a.sourceCmd(), "set", "pushover", "env_file="+envFile(t, "PUSHOVER_TOKEN=tok-not-to-be-seen\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.userCmd(), "set", "me", "notify=pushover", "env_file="+envFile(t, "PUSHOVER_USER=u9f2k4xq71ab03cd55ef8gh2ij6kl9\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  unchanged        pushover\n", "  the yard has it\n", "  ok       env file     PUSHOVER_TOKEN is set in ", "  ok       notify       tells me\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the second pack check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tok-not-to-be-seen") || strings.Contains(out, "u9f2k4xq71ab03cd55ef8gh2ij6kl9") {
		t.Fatalf("a plan prints a secret:\n%s", out)
	}
}

// A source no call of which is granted, whose key a crew member holds,
// is shown with no env_file to set: the plan says nothing of it, and the
// result names nothing next for it. What still lacks is still said.
func TestPackOmitsTheEnvFileACrewMemberHolds(t *testing.T) {
	a := openApp(t)
	ctx := context.Background()
	boss, err := a.st.GetCrew(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	env, _ := boss.Config["env_file"].(string)
	if err := os.WriteFile(env, []byte("PUSHOVER_TOKEN=tok-of-boss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := pushoverPack(t)

	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "no env_file is set for it") || strings.Contains(out, "source set pushover env_file") {
		t.Fatalf("the plan names an env_file nobody needs:\n%s", out)
	}
	if !strings.Contains(out, "  missing  notify       it can tell a user outside the yard and no user names it.") {
		t.Fatalf("the plan lost what still lacks:\n%s", out)
	}
	if strings.Contains(out, "tok-of-boss") {
		t.Fatalf("a plan prints a secret:\n%s", out)
	}

	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "next, for source pushover: no env_file is set for it") {
		t.Fatalf("the result names an env_file nobody needs:\n%s", out)
	}
	if !strings.Contains(out, "next, for source pushover: it can tell a user outside the yard and no user names it.") {
		t.Fatalf("the result lost what still lacks:\n%s", out)
	}
}

// Adding a source is the operator's. Run by anyone else the import says so
// in the plan and the result, adds none, and does the rest.
func TestPackApplyByAnotherSessionAddsNoSource(t *testing.T) {
	a := openApp(t)
	ctx := context.Background()
	dir := pushoverPack(t)
	a.actor = "web-builder-1"
	const refusal = "adding a source is the operator's (me), and this is web-builder-1"

	out, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\ndeclared source pushover, from sources/pushover.yaml\n  its calls go to https://api.pushover.net/1: message (update)\n  not added: " + refusal + "\n",
		"the source pushover was not added: " + refusal + ". The rest of the import does not depend on it\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "next, for source pushover") {
		t.Errorf("a source that was not added has nothing to set next:\n%s", out)
	}
	if _, err := a.st.GetSource(ctx, "pushover"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the source was added: %v", err)
	}
	recs, _ := a.st.Packs(ctx)
	if slices.ContainsFunc(recs, func(r store.PackImport) bool {
		return slices.ContainsFunc(r.Objects, func(po store.PackObject) bool { return po.Kind == "source" })
	}) {
		t.Fatalf("records = %+v", recs)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes")
	var got struct {
		Refused map[string]string `json:"sources_refused"`
		Result  struct {
			Declared []string `json:"declared"`
		} `json:"result"`
	}
	if jerr := json.Unmarshal([]byte(out), &got); err != nil || jerr != nil || got.Refused["pushover"] != refusal || len(got.Result.Declared) != 0 {
		t.Fatalf("--json: %v, %v, %+v\n%s", err, jerr, got, out)
	}
	a.json = false

	a.actor = "me"
	if out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes"); err != nil || !strings.Contains(out, "source pushover is declared") {
		t.Fatalf("as the operator: %v\n%s", err, out)
	}
}
