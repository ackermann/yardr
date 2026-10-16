package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
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
// by-hand declaration, named from pack.yaml as the page's pack names it.
func pushoverPack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"pack.yaml":             "name: notify\nsources:\n  pushover:\n    declaration: sources/pushover.yaml\n",
		"sources/pushover.yaml": docYAML(t, "## By hand"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The by-hand file docs/pushover.md keeps is the one the tests of yardr
// source and of the push send through, to the byte, and the one the
// pushover pack brings.
func TestThePushoverPageHasTheTestedDeclaration(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile("testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := docYAML(t, "## By hand"); got != string(want) {
		t.Fatalf("docs/pushover.md and testdata/pushover.yaml differ:\n%s", got)
	}
	packDecl, err := fs.ReadFile(packs.FS, "pushover/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(packDecl) != string(want) {
		t.Fatalf("packs/pushover/pushover.yaml and testdata/pushover.yaml differ:\n%s", packDecl)
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
		"  missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source key pushover gives PUSHOVER_TOKEN, or yardr source set pushover env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret pushover --context \"<where to get it>\"\n",
		"  missing  notify       it can tell a user outside the yard and no user names it. The import names none: yardr user key <user> pushover gives their PUSHOVER_USER, then yardr user set <user> notify=pushover\n",
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
		"next, for source pushover: no env_file is set for it, and a pack neither carries nor names one: yardr source key pushover gives PUSHOVER_TOKEN",
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

// widgetPack is a pack that declares the source widget, whose credential
// may be minted, against base.
func widgetPack(t *testing.T, base string) (dir, decl string) {
	t.Helper()
	dir = t.TempDir()
	decl = `name: widget
base: BASE
credential:
  env: WIDGET_TOKEN
  mint: [WIDGET_APP_ID, WIDGET_APP_KEY]
  header: "Authorization: Bearer {credential}"
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	decl = strings.Replace(decl, "BASE", base, 1)
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"pack.yaml":           "name: widgets\nsources:\n  widget:\n    declaration: sources/widget.yaml\n",
		"sources/widget.yaml": decl,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, decl
}

// A pack that declares a mint asks for the env_file and then the
// mint_command, which no pack brings; a second apply keeps the operator's.
func TestPackApplyKeepsMintCommand(t *testing.T) {
	a := openApp(t)
	ctx := context.Background()
	dir, decl := widgetPack(t, "https://widget.example.com")

	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "  missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source key widget gives WIDGET_APP_ID and WIDGET_APP_KEY, or WIDGET_TOKEN, or yardr source set widget env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret widget --context \"<where to get it>\"\n"; !strings.Contains(out, want) {
		t.Fatalf("pack check lacks %q:\n%s", want, out)
	}
	if _, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes"); err != nil {
		t.Fatal(err)
	}
	env := envFile(t, "WIDGET_APP_ID=app-1\nWIDGET_APP_KEY=keys/app.pem\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "widget", "env_file="+env); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "  missing  mint command WIDGET_APP_ID and WIDGET_APP_KEY are set in " + env + ", and a pack cannot bring one: no mint_command is set: yardr source set widget mint_command=<program> [args...]\n"; !strings.Contains(out, want) {
		t.Fatalf("pack check lacks %q:\n%s", want, out)
	}
	mint := filepath.Join(dir, "mint")
	if err := os.WriteFile(mint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "set", "widget", "mint_command="+mint); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "check", dir, "--depot", "web")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := "  ok       env file     WIDGET_APP_ID and WIDGET_APP_KEY are set in " + env + ", minted with " + mint + "\n"; !strings.Contains(out, want) {
		t.Fatalf("pack check lacks %q:\n%s", want, out)
	}

	// A second apply, of the pack changed, keeps the operator's command.
	changed := strings.Replace(decl, "base: https://widget.example.com", "base: https://widget.example.com/v2", 1)
	if err := os.WriteFile(filepath.Join(dir, "sources/widget.yaml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes"); err != nil {
		t.Fatal(err)
	}
	src, err := a.st.GetSource(ctx, "widget")
	if err != nil || src.MintCommand != mint || src.EnvFile != env {
		t.Fatalf("after the second apply: %+v %v", src, err)
	}
}

// Doctor says of a mint without its command that no call can be made, and
// --try mints for the check call.
func TestDoctorSourcesMint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-doctor-mint" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{"id":"app-1"}`)
	}))
	defer srv.Close()
	a := openApp(t)
	ctx := context.Background()
	dir, _ := widgetPack(t, srv.URL)
	if _, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes"); err != nil {
		t.Fatal(err)
	}
	env := envFile(t, "WIDGET_APP_ID=app-1\nWIDGET_APP_KEY=keys/app.pem\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "widget", "env_file="+env); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: a.cfg.Home}
	part := func(try bool) optionalPart {
		t.Helper()
		p, err := sourcesPart(ctx, cfg, a.st, try)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range p.Notes {
			if strings.Contains(n, "tok-doctor-mint") {
				t.Fatalf("doctor prints the token: %s", n)
			}
		}
		return p
	}
	has := func(p optionalPart, want string) bool {
		return slices.ContainsFunc(p.Notes, func(n string) bool { return strings.Contains(n, want) })
	}
	if p := part(false); !has(p, "source widget: no call of it can be made: no mint_command is set: ") {
		t.Fatalf("without the command: %+v", p)
	}
	mint := filepath.Join(dir, "mint")
	scripttest.Write(t, mint, "#!/bin/sh\nprintf '{\"token\":\"tok-doctor-mint\",\"expires_at\":\"2030-01-01T00:00:00Z\"}'\n")
	if _, err := run(t, a.sourceCmd(), "set", "widget", "mint_command="+mint); err != nil {
		t.Fatal(err)
	}
	if p := part(false); has(p, "source widget: no call of it can be made") {
		t.Fatalf("with the command: %+v", p)
	}
	if p := part(true); !has(p, "tried: source widget: its check call ping (GET /me) was answered 200 in ") {
		t.Fatalf("--try: %+v", p)
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
