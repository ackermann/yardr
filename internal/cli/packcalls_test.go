package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// The stand-in's two tokens, which no output may hold.
const deskRead, deskWrite = "tok-11aa-not-to-be-seen", "tok-22bb-not-to-be-seen"

// deskYAML is a pack that brings the customer system and asks for its
// calls: every read call for the builders, the note for the script group
// that writes it, one read call for the yardmaster.
const deskYAML = `name: desk
flows:
  default:
    stages:
      new:    { group: builders, outcomes: { done: review } }
      review: { group: support, outcomes: { done: merged } }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md, calls: { crm: [read] } }
  support:  { run: scripts/note, calls: { crm: [note] } }
crew:
  yardmaster:
    role: roles/yardmaster.md
    calls: { crm: [customer, note] }
sources:
  crm:
    declaration: sources/crm.yaml
`

// deskFlags answer what an import of the desk pack asks besides the calls.
var deskFlags = []string{"--crew", "yardmaster=boss", "--bind", "builders=claude"}

// deskPack is the desk pack in a directory, with the isolated yard's
// declaration of the customer system (contrib/isolated-yard/sources) aimed
// at a stand-in; asked says how many requests the stand-in got.
func deskPack(t *testing.T) (dir string, asked func() int) {
	t.Helper()
	crm := (&standin.Server{CRMToken: deskRead, CRMUpdateToken: deskWrite}).Handler()
	var got atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		crm.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	asked = func() int { return int(got.Load()) }
	decl, err := os.ReadFile("../../contrib/isolated-yard/sources/crm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	for name, text := range map[string]string{
		"pack.yaml":           deskYAML,
		"roles/builder.md":    "build\n",
		"roles/yardmaster.md": "yardmaster\n",
		"scripts/note":        "#!/bin/sh\n",
		"sources/crm.yaml":    strings.Replace(string(decl), "base: BASE/crm", "base: "+srv.URL+"/crm", 1),
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, asked
}

// deskYard is openApp after a first import of the desk pack and with the
// file the source's credential is read from set: a yard an import asks
// about the calls in.
func deskYard(t *testing.T, dir string) *app {
	t.Helper()
	a := openApp(t)
	requireNonInteractive(t)
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web", "--yes"}, deskFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+envFile(t, "CRM_TOKEN="+deskRead+"\nCRM_UPDATE_TOKEN="+deskWrite+"\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	return a
}

// mayCall is what the yard's grants of the crm are, a line per grantee.
func mayCall(t *testing.T, a *app) string {
	t.Helper()
	grants, err := a.st.SourceGrants(context.Background(), "crm")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range grants {
		out = append(out, g.Grantee.String()+": "+strings.Join(g.Calls, ","))
	}
	slices.Sort(out)
	return strings.Join(out, "; ")
}

func callRecord(t *testing.T, a *app) map[string]map[string]bool {
	t.Helper()
	recs, err := a.st.Packs(context.Background())
	if err != nil || len(recs) != 1 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	return recs[0].Calls
}

// A pack with the customer system, imported into a yard that has not the
// source: the import adds the source and asks nothing about its calls,
// since nobody can make one before the operator set the credential's file.
// The plan and the result say so, with the command.
func TestPackApplyAsksNothingAboutCallsOfASourceWithoutItsCredential(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	requireNonInteractive(t)
	dir, _ := deskPack(t)
	out, err := run(t, a.packCmd(), append([]string{"check", dir, "--depot", "web"}, deskFlags[:2]...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\ncalls of source crm, for group web-builders\n  the pack asks for read\n  lacks     read: nothing is asked or granted\n" +
			"    missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source key crm gives CRM_TOKEN and CRM_UPDATE_TOKEN, or yardr source set crm env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret crm --context \"<where to get it>\", then apply the pack again\n",
		"\ncalls of source crm, for group web-support\n  the pack asks for note (update)\n  lacks     note (update): nothing is asked or granted\n",
		"\ncalls of source crm, for crew member boss\n  the pack asks for customer, note (update)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the operator: let") {
		t.Errorf("the plan asks about a call nobody can make:\n%s", out)
	}
	// A flag for what is not asked grants nothing, and says why.
	out, err = run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web", "--yes", "--calls", "builders=crm:read"}, deskFlags...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource crm is declared: 5 calls under ",
		"--calls builders=crm:read grants nothing: no env_file is set for it",
		"nothing was asked about group web-builders calling read of the source crm: no env_file is set for it, and a pack neither carries nor names one: yardr source key crm gives CRM_TOKEN and CRM_UPDATE_TOKEN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if mayCall(t, a) != "" || callRecord(t, a) != nil {
		t.Fatalf("granted %q, recorded %v", mayCall(t, a), callRecord(t, a))
	}
}

// In a terminal the operator is asked once per group or crew member and
// source, after the import's other questions and with no as the default.
// A yes is a grant, which yardr source show then prints; a no is recorded.
// The second apply asks nothing and changes nothing.
func TestPackApplyAsksTheOperatorAboutCalls(t *testing.T) {
	dir, _ := deskPack(t)
	a := deskYard(t, dir)
	ctx := context.Background()
	setInteractive(t, true)
	var asked []string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			c, ok := f.(*huh.Confirm)
			if !ok || f.GetKey() != "calls" {
				continue
			}
			title := strings.TrimSpace(strings.SplitN(c.View(), "\n", 2)[0])
			asked = append(asked, title)
			if c.GetValue() != false {
				t.Errorf("%q defaults to yes", title)
			}
			// Yes to the builders' read, no to the two that write a note.
			if strings.Contains(title, "web-builders") {
				c.WithKeyMap(huh.NewDefaultKeyMap())
				c.Focus()
				c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
			}
		}
		return nil
	})
	args := append([]string{"apply", dir, "--depot", "web"}, deskFlags...)

	out, err := run(t, a.packCmd(), append([]string{"check", dir, "--depot", "web"}, deskFlags[:2]...)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\ncalls of source crm, for group web-builders\n  the pack asks for read\n  asks      read: the operator is asked, and the answer without one is no\n",
		"\nan import would ask\n",
		"  the operator: let group web-builders call read of the source crm (--calls builders=crm:read, or :none)? [no]\n",
		"  the operator: let group web-support call note (update) of the source crm (--calls support=crm:note, or :none)? [no]\n",
		"  the operator: let crew member boss call customer, note (update) of the source crm (--calls crew:yardmaster=crm:customer,note, or :none)? [no]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if mayCall(t, a) != "" {
		t.Fatal("pack check granted something")
	}

	head, _ := a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{"Let group web-builders call read of the source crm?", "Let group web-support call note (update) of the source crm?",
		"Let crew member boss call customer, note (update) of the source crm?"}
	if !slices.Equal(asked, want) {
		t.Fatalf("asked %q, want %q", asked, want)
	}
	for _, want := range []string{
		"\ngroup web-builders may now call read of the source crm (yardr source show crm)\n",
		"group web-support was not let call note (update) of the source crm: the no is recorded and not asked again (--calls support=crm:note grants it after all)\n",
		"crew member boss was not let call customer, note (update) of the source crm: the no is recorded and not asked again (--calls crew:yardmaster=crm:customer,note grants it after all)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if got := mayCall(t, a); got != "group web-builders: read" {
		t.Fatalf("grants = %q", got)
	}
	evs, _ := a.st.EventsSince(ctx, head, 0)
	if !slices.ContainsFunc(evs, func(e store.Event) bool {
		return e.Kind == "source_granted" && e.Actor == "me" && e.Data["group"] == "web-builders" && e.Data["what"] == "read"
	}) {
		t.Fatalf("the grant is not logged: %+v", evs)
	}
	if show, err := run(t, a.sourceCmd(), "show", "crm"); err != nil || !strings.Contains(show, "  call customer  read  GET /customers/{id}  Look a customer up by id\n") ||
		!strings.Contains(show, "    may be called by group web-builders (through read)\n") || strings.Contains(show, "web-support") {
		t.Fatalf("source show: %v\n%s", err, show)
	}
	rec := callRecord(t, a)
	if !rec["group web-builders"]["crm:read"] || rec["group web-support"]["crm:note"] || len(rec["group web-support"]) != 1 || len(rec["crew member boss"]) != 2 {
		t.Fatalf("record = %+v", rec)
	}
	if list, err := run(t, a.packCmd(), "list"); err != nil || !strings.Contains(list, "  may call   crew member boss: crm:customer no, crm:note no\n") ||
		!strings.Contains(list, "  may call   group web-builders: crm:read yes\n") {
		t.Fatalf("pack list: %v\n%s", err, list)
	}

	// The second apply: nothing asked, nothing changed.
	asked, head = nil, 0
	head, _ = a.st.Head(ctx)
	if out, err = run(t, a.packCmd(), args...); err != nil || len(asked) != 0 || !strings.Contains(out, "pack desk is already applied to depot web: nothing changed") {
		t.Fatalf("the second apply: %v, asked %q\n%s", err, asked, out)
	}
	for _, want := range []string{
		"  has       read: it may already, and nothing is asked\n",
		"  declined  note (update): an earlier import was told no (--calls support=crm:<call> grants it)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the second plan lacks %q:\n%s", want, out)
		}
	}
	if evs, _ := a.st.EventsSince(ctx, head, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("the second apply logged %+v", evs)
	}
	for _, secret := range []string{deskRead, deskWrite} {
		if strings.Contains(out, secret) {
			t.Fatalf("a plan prints a credential:\n%s", out)
		}
	}
}

// Without a terminal, or with --yes, nothing is granted unless --calls
// says so: the import goes through and names the flag. The flag grants what
// it names and says no to what it leaves out of that group's or crew
// member's calls of the source; :none says no to all of them.
func TestPackApplyCallsFlag(t *testing.T) {
	t.Parallel()
	dir, _ := deskPack(t)
	args := append([]string{"apply", dir, "--depot", "web", "--yes"}, deskFlags...)

	t.Run("no flag", func(t *testing.T) {
		a := deskYard(t, dir)
		out, err := run(t, a.packCmd(), args...)
		if err != nil || !strings.Contains(out, "group web-builders was not let call read of the source crm: nothing is asked here, and the answer without a flag is no. --calls builders=crm:read grants it, --calls builders=crm:none says no for good") {
			t.Fatalf("%v\n%s", err, out)
		}
		if mayCall(t, a) != "" || callRecord(t, a) != nil {
			t.Fatalf("granted %q, recorded %v", mayCall(t, a), callRecord(t, a))
		}
	})

	t.Run("the flag", func(t *testing.T) {
		a := deskYard(t, dir)
		out, err := run(t, a.packCmd(), append(args, "--calls", "builders=crm:read", "--calls", "support=crm:none", "--calls", "crew:yardmaster=crm:customer")...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, want := range []string{
			"  grants    read: --calls says so\n",
			"  grants    customer: --calls says so\n  declines  note (update): --calls leaves it out\n",
			"\ngroup web-builders may now call read of the source crm (yardr source show crm)\n",
			"crew member boss may now call customer of the source crm (yardr source show crm)\n",
			"crew member boss was not let call note (update) of the source crm: the no is recorded and not asked again (--calls crew:yardmaster=crm:note grants it after all)\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("pack apply lacks %q:\n%s", want, out)
			}
		}
		if got := mayCall(t, a); got != "crew member boss: customer; group web-builders: read" {
			t.Fatalf("grants = %q", got)
		}
		// Again, with no flag: nothing is asked for and nothing changes.
		if out, err = run(t, a.packCmd(), args...); err != nil || !strings.Contains(out, "nothing changed") || strings.Contains(out, "nothing is asked here") {
			t.Fatalf("again: %v\n%s", err, out)
		}
		// A no is answered after all.
		if out, err = run(t, a.packCmd(), append(args, "--calls", "support=crm:note")...); err != nil || !strings.Contains(out, "group web-support may now call note (update) of the source crm") {
			t.Fatalf("after a no: %v\n%s", err, out)
		}
		if got := mayCall(t, a); got != "crew member boss: customer; group web-builders: read; group web-support: note" {
			t.Fatalf("grants = %q", got)
		}
	})

	t.Run("a flag that fits nothing", func(t *testing.T) {
		a := deskYard(t, dir)
		for flag, want := range map[string]string{
			"builders":                         "want <group>=<source>:<call>[,<call>]",
			"builders=crm":                     "want <group>=<source>:<call>[,<call>]",
			"nobody=crm:read":                  "the pack has no group nobody (it has: builders, support)",
			"crew:nobody=crm:read":             "the pack has no crew member nobody (it has: yardmaster)",
			"builders=erp:read":                "the pack asks for no call of the source erp for its group builders",
			"builders=crm:note":                `the pack does not ask that builders may call "note" of crm (it asks for: read)`,
			"yardmaster=crm:customer":          "the pack has no group yardmaster",
			"crew:yardmaster=crm:customer,big": `the pack does not ask that crew:yardmaster may call "big" of crm`,
		} {
			if _, err := run(t, a.packCmd(), append(args, "--calls", flag)...); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("--calls %s: %v, want %q", flag, err, want)
			}
		}
		if _, err := run(t, a.packCmd(), append(args, "--calls", "builders=crm:read", "--calls", "builders=crm:none")...); err == nil || !strings.Contains(err.Error(), "answered twice") {
			t.Errorf("twice: %v", err)
		}
		if mayCall(t, a) != "" {
			t.Fatalf("a refused command granted %q", mayCall(t, a))
		}
	})
}

// Only the operator answers: run by anyone else the import does the rest,
// grants nothing and records nothing, by flag or in a terminal — and then
// fails loud folding the pack's source, which is the operator's to do.
func TestPackApplyOnlyTheOperatorGrantsCalls(t *testing.T) {
	dir, _ := deskPack(t)
	a := deskYard(t, dir)
	a.actor = "boss"
	args := append([]string{"apply", dir, "--depot", "web", "--yes"}, deskFlags...)
	const fold = "source crm is declared, but its links were not folded in: " +
		"only the operator (me) may move links from one source to another; this is boss"
	for _, flag := range []string{"builders=crm:read", "builders=crm:none"} {
		out, err := run(t, a.packCmd(), append(args, "--calls", flag)...)
		if err == nil || !strings.Contains(err.Error(), fold) || strings.Contains(out, "may now call") {
			t.Fatalf("--calls %s: %v\n%s", flag, err, out)
		}
		if mayCall(t, a) != "" || callRecord(t, a) != nil {
			t.Fatalf("--calls %s: granted %q, recorded %v", flag, mayCall(t, a), callRecord(t, a))
		}
	}
	setInteractive(t, true)
	asked := 0
	stubAsk(t, func(fields ...huh.Field) error { asked += len(fields); return nil })
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web"}, deskFlags...)...); err == nil ||
		!strings.Contains(err.Error(), fold) || asked != 0 {
		t.Fatalf("in a terminal: %v, %d asked\n%s", err, asked, out)
	}
}

// A pack whose declaration adds a read call to the one the yard has says in
// its plan who gets the call through a grant of read given before.
func TestPackPlanShowsWhoReadNowReaches(t *testing.T) {
	t.Parallel()
	dir, _ := deskPack(t)
	a := deskYard(t, dir)
	if _, err := a.st.AddUser(context.Background(), "ada", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.GrantSource(context.Background(), "crm", "read", store.Grantee{Group: "web-builders"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web", "--yes", "--calls", "builders=crm:read"}, deskFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file := filepath.Join(dir, "sources", "crm.yaml")
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(text, "  orders: { kind: read, request: { method: GET, path: /orders } }\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), append([]string{"check", dir, "--depot", "web"}, deskFlags[:2]...)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  change           crm\n                     call orders: (none) -> (read) GET /orders\n",
		"  through their grant of read, group web-builders, group web-builders as ada would then call: orders (nobody is asked: read covers the read calls a later file adds; yardr source revoke crm read takes it back)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
}

// doctor's line on the declared sources, and its notes: a credential file
// that is not set, missing or open to others; a source nobody is granted;
// a source whose last answered call the service refused. It sends nothing
// unless --try, which makes each source's check call.
func TestDoctorSources(t *testing.T) {
	t.Parallel()
	dir, requests := deskPack(t)
	a := openApp(t)
	if err := a.st.DeleteSource(context.Background(), "linear", "me"); err != nil {
		t.Fatal(err)
	}
	requireNonInteractive(t)
	ctx := context.Background()
	cfg := &config.Config{Home: a.cfg.Home}
	part := func(try bool) optionalPart {
		t.Helper()
		p, err := sourcesPart(ctx, cfg, a.st, try)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range p.Notes {
			if strings.Contains(n, deskRead) || strings.Contains(n, deskWrite) {
				t.Fatalf("doctor prints a credential: %s", n)
			}
		}
		return p
	}
	has := func(p optionalPart, want string) bool {
		return slices.ContainsFunc(p.Notes, func(n string) bool { return strings.Contains(n, want) })
	}
	yardr := yardrAt(cfg.Home)

	if p := part(false); p.Configured || p.Key != "sources" || !strings.Contains(p.Detail, "no service outside the yard is declared") || len(p.Notes) != 0 {
		t.Fatalf("without a source: %+v", p)
	}
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web", "--yes"}, deskFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	p := part(false)
	if !p.Configured || p.Detail != "crm (5 calls)" || len(p.Notes) != 2 ||
		!has(p, "source crm: no call of it can be made: CRM_TOKEN is not set up: "+yardr+" source key crm gives CRM_TOKEN and CRM_UPDATE_TOKEN, or "+yardr+" source set crm env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: "+yardr+" bead ask <bead> \"<question>\" --secret crm --context \"<where to get it>\"") ||
		!has(p, "source crm: nobody is granted a call of it, so only the operator makes one ("+yardr+" source grant crm read --group <group>)") {
		t.Fatalf("a source without its credential: %+v", p)
	}
	if p = part(true); !has(p, "not tried: source crm has no credential to make its check call with") {
		t.Fatalf("--try without a credential: %+v", p)
	}

	// A file that is gone, and one that others may read.
	good := envFile(t, "CRM_TOKEN="+deskRead+"\nCRM_UPDATE_TOKEN="+deskWrite+"\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+good); err != nil {
		t.Fatal(err)
	}
	if p = part(false); len(p.Notes) != 1 || !has(p, "nobody is granted") {
		t.Fatalf("with the credential: %+v", p)
	}
	if err := os.Chmod(good, 0o644); err != nil {
		t.Fatal(err)
	}
	if p = part(false); !has(p, "source crm: no call of it can be made: ") || !has(p, good+" is open to others (mode 0644)") {
		t.Fatalf("a file open to others: %+v", p)
	}
	if err := os.Rename(good, good+".away"); err != nil {
		t.Fatal(err)
	}
	if p = part(false); !has(p, "source crm: no call of it can be made: ") || !has(p, "no such file") {
		t.Fatalf("a file that is gone: %+v", p)
	}
	if err := os.Rename(good+".away", good); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(good, 0o600); err != nil {
		t.Fatal(err)
	}

	// Granted: nothing to note, and nothing was sent so far.
	if _, err := run(t, a.sourceCmd(), "grant", "crm", "read", "--group", "web-builders"); err != nil {
		t.Fatal(err)
	}
	if p = part(false); len(p.Notes) != 0 {
		t.Fatalf("a source that is set up: %+v", p)
	}
	if got := requests(); got != 0 {
		t.Fatalf("doctor without --try sent %d requests", got)
	}
	if p = part(true); len(p.Notes) != 1 || !has(p, "tried: source crm: its check call ping (GET /me) was answered 200 in ") {
		t.Fatalf("--try: %+v", p)
	}
	if got := requests(); got != 1 {
		t.Fatalf("doctor --try sent %d requests, want the check call alone", got)
	}

	// A credential the service no longer takes: the last call says so, from
	// its event, and --try says it of the check call.
	bad := envFile(t, "CRM_TOKEN=rotated-away\nCRM_UPDATE_TOKEN=rotated-away\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+bad); err != nil {
		t.Fatal(err)
	}
	if p = part(false); len(p.Notes) != 0 {
		t.Fatalf("before a call was refused: %+v", p)
	}
	if _, err := run(t, a.callCmd(), "crm", "customer", "id=1"); err == nil {
		t.Fatal("a call with a credential the service does not take was answered")
	}
	if p = part(false); len(p.Notes) != 1 || !has(p, "source crm: its last call that was answered (customer, ") ||
		!has(p, ") was answered 401: the service did not take what CRM_TOKEN is set to. "+yardr+" source check crm asks it again") {
		t.Fatalf("after a 401: %+v", p)
	}
	if p = part(true); !has(p, "tried: source crm: its check call ping (GET /me) was answered 401: the service does not take what CRM_TOKEN is set to") {
		t.Fatalf("--try with a bad credential: %+v", p)
	}
	// Set right again: a check call that is answered now says more than the
	// call before it, and the next call clears the note for good.
	if _, err := run(t, a.sourceCmd(), "set", "crm", "env_file="+good); err != nil {
		t.Fatal(err)
	}
	if p = part(true); len(p.Notes) != 1 || !has(p, "was answered 200") {
		t.Fatalf("--try after the credential was set right: %+v", p)
	}
	if _, err := run(t, a.callCmd(), "crm", "customer", "id=1"); err != nil {
		t.Fatal(err)
	}
	if p = part(false); len(p.Notes) != 0 {
		t.Fatalf("after a call that was answered: %+v", p)
	}

	// A file that sets the read credential alone: a read call can still be
	// made, so no call of it is "no call", and --try makes the check call.
	// (source set would refuse the file, so the one that is set is rewritten.)
	if err := os.WriteFile(good, []byte("CRM_TOKEN="+deskRead+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p = part(false); len(p.Notes) != 1 ||
		!has(p, "source crm: an update call of it cannot be made: env_file "+good+" does not set CRM_UPDATE_TOKEN") {
		t.Fatalf("a file without the update credential: %+v", p)
	}
	if p = part(true); len(p.Notes) != 2 || !has(p, "an update call of it cannot be made") ||
		!has(p, "tried: source crm: its check call ping (GET /me) was answered 200 in ") {
		t.Fatalf("--try with the read credential alone: %+v", p)
	}
}

// Doctor says nothing of the env_file of a source no call of which is
// granted when a crew member holds its key: only its check call wants the
// source's own file. The grants note stays.
func TestDoctorOmitsTheEnvFileACrewMemberHolds(t *testing.T) {
	t.Parallel()
	dir, _ := deskPack(t)
	a := openApp(t)
	if err := a.st.DeleteSource(context.Background(), "linear", "me"); err != nil {
		t.Fatal(err)
	}
	requireNonInteractive(t)
	ctx := context.Background()
	if out, err := run(t, a.packCmd(), append([]string{"apply", dir, "--depot", "web", "--yes"}, deskFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	boss, err := a.st.GetCrew(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	env, _ := boss.Config["env_file"].(string)
	if err := os.WriteFile(env, []byte("CRM_TOKEN=tok-of-boss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := sourcesPart(ctx, &config.Config{Home: a.cfg.Home}, a.st, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Notes {
		if strings.Contains(n, "no call of it can be made") {
			t.Fatalf("doctor names an env_file nobody needs: %+v", p.Notes)
		}
		if strings.Contains(n, "tok-of-boss") {
			t.Fatalf("doctor prints a credential: %s", n)
		}
	}
	if !slices.ContainsFunc(p.Notes, func(n string) bool { return strings.Contains(n, "nobody is granted a call of it") }) {
		t.Fatalf("doctor lost the grants note: %+v", p.Notes)
	}
	decl, err := os.ReadFile("testdata/pushover.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.PutSource(ctx, "pushover.yaml", string(decl), a.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddUser(ctx, "ada", a.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("CRM_TOKEN=tok-of-boss\nPUSHOVER_TOKEN=tok-of-boss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err = sourcesPart(ctx, &config.Config{Home: a.cfg.Home}, a.st, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Notes, func(n string) bool { return strings.Contains(n, "source pushover: no call of it can be made") }) {
		t.Fatalf("doctor hid the source credential needed for user notifications: %+v", p.Notes)
	}
}
