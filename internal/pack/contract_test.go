package pack_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// api1 is the frozen fixture of pack API 1: two packs that use every
// stable seam, held still so a change that breaks the contract fails here
// and not at a customer (docs/pack-api.md). A new API number adds api2/
// beside api1/ and leaves api1/ as it is.
const api1 = "testdata/api1"

// The digests of the fixture packs, pinned: an edit to the fixture shows
// as a changed constant in review, where a golden alone would show as a
// rewritten file.
const (
	api1LineDigest   = "bdc31685332e50662df6af343d1f488ddd643912c5b7a92285a3ff9176f746b6"
	api1SourceDigest = "f350b4fd774adab6d0454d3127b0eb9532892046686578e9bb28ad13b2a45391"
)

// contractHome is a short home of the test's: the socket the script test
// serves the yard on hangs off it, and a unix socket's path must fit in
// about a hundred bytes, which t.TempDir under a test's name may not
// leave.
func contractHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "api1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

// contractSetup is a yard with both fixture packs applied: the source
// pack first, so the yard has the acme source, then a bare hook and the
// crew member with its identity, then the line pack, so the apply sets
// the hook's tables. It returns the API, the owning store and the home.
func contractSetup(t *testing.T) (store.API, *store.Store, string) {
	t.Helper()
	api, own, home := contractYard(t)
	source := load(t, api1+"/api1-source")
	apply(t, api, build(t, api, source, pack.Options{Home: home}), nil)
	if _, err := api.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: "/nonexistent/acme.secret"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "keeper", Runner: "fake"}, "me"); err != nil {
		t.Fatal(err)
	}
	// Another id than the stub envelopes' actor (u-1): what the yard
	// did itself says and makes nothing.
	if err := api.SetIdentity(ctx, "keeper", "acme", store.Identity{ID: "u-keeper", Key: "keeper"}, "me"); err != nil {
		t.Fatal(err)
	}
	line := load(t, api1+"/api1-line")
	apply(t, api, build(t, api, line, pack.Options{Depot: "web", Home: home}), nil)
	return api, own, home
}

// golden compares got with the golden file name under the fixture, and
// fails naming both. YARDR_UPDATE_GOLDEN=1 writes the file again
// instead, as docs/api.md's is.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(api1, "goldens", name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	if os.Getenv("YARDR_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("golden %s is not what the yard answers; write it again with\n"+
		"  YARDR_UPDATE_GOLDEN=1 go test ./internal/pack -run TestPackAPI1\ngot:\n%s\nwant:\n%s", name, got, want)
}

// planJSON is the plan as JSON, with the test's home redacted: every
// installed path hangs off it, and it is a temp dir.
func planJSON(t *testing.T, p *pack.Plan, home string) string {
	t.Helper()
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(api1)
	if err != nil {
		t.Fatal(err)
	}
	out = []byte(strings.ReplaceAll(string(out), fixture, "<fixture>"))
	out = []byte(strings.ReplaceAll(string(out), home, "<home>"))
	return string(out) + "\n"
}

// The fixture packs check and apply: each loads as API 1 with its setup
// and programs, the source pack carries its two presets, each plan
// matches its golden, each digest matches its constant, and applying
// each again changes nothing.
func TestPackAPI1Plans(t *testing.T) {
	t.Parallel()
	line := load(t, api1+"/api1-line")
	source := load(t, api1+"/api1-source")
	if line.API != 1 || source.API != 1 {
		t.Fatalf("api = %d and %d, want 1 and 1", line.API, source.API)
	}
	if line.Setup != "SETUP.md" || line.SetupText == "" || source.Setup != "SETUP.md" || source.SetupText == "" {
		t.Fatalf("setup = %q and %q", line.Setup, source.Setup)
	}
	if len(source.Presets) != 2 || source.Presets[0].Name != "read" || source.Presets[1].Name != "write" {
		t.Fatalf("presets = %+v, want read and write in file order", source.Presets)
	}

	st, _, home := contractYard(t)
	splan := build(t, st, source, pack.Options{Home: home})
	if splan.Digest != api1SourceDigest {
		t.Errorf("source digest = %s, want pinned %s", splan.Digest, api1SourceDigest)
	}
	golden(t, "api1-source.plan.json", planJSON(t, splan, home))
	apply(t, st, splan, nil)
	sagain := build(t, st, load(t, api1+"/api1-source"), pack.Options{Home: home})
	for _, o := range sagain.Objects {
		if o.Action != pack.Unchanged {
			t.Errorf("source second plan: %s %s %s", o.Action, o.Kind, o.Name)
		}
	}
	if len(sagain.Questions) != 0 {
		t.Errorf("source second plan asks %d questions", len(sagain.Questions))
	}

	if _, err := st.PutHook(ctx, store.Hook{Name: "acme", Source: "acme", SecretFile: "/nonexistent/acme.secret"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "keeper", Runner: "fake"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIdentity(ctx, "keeper", "acme", store.Identity{ID: "u-keeper", Key: "keeper"}, "me"); err != nil {
		t.Fatal(err)
	}
	lplan := build(t, st, line, pack.Options{Depot: "web", Home: home})
	if lplan.Digest != api1LineDigest {
		t.Errorf("line digest = %s, want pinned %s", lplan.Digest, api1LineDigest)
	}
	golden(t, "api1-line.plan.json", planJSON(t, lplan, home))
	lres := apply(t, st, lplan, nil)
	if len(lres.Mapped) != 1 {
		t.Fatalf("line apply mapped %d hooks, want the acme one", len(lres.Mapped))
	}
	lagain := build(t, st, load(t, api1+"/api1-line"), pack.Options{Depot: "web", Home: home})
	for _, o := range lagain.Objects {
		if o.Action != pack.Unchanged {
			t.Errorf("line second plan: %s %s %s", o.Action, o.Kind, o.Name)
		}
	}
	if len(lagain.Questions) != 0 {
		t.Errorf("line second plan asks %d questions", len(lagain.Questions))
	}
}

// The fixture source's stubs answer in shape: each installed copy is run
// with one JSON object on stdin and answers one on stdout, and the
// answers match the golden. The normalize cases are the bodies the makes
// and says test delivers. The bounds a command runs under are the
// declaration's own constants, held beside.
func TestPackAPI1Stubs(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the fixture's stubs are written in it")
	}
	// Give CommandEnv the proxy and CA variables the page promises to pass
	// through, regardless of the machine running the test.
	for _, name := range []string{
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	} {
		t.Setenv(name, "api1-test-value")
	}
	_, _, home := contractSetup(t)
	decl, err := source.Parse(filepath.Join(home, "sources", "acme.yaml"), []byte(mustRead(t, filepath.Join(api1, "api1-source", "acme.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	if source.Timeout != 30*time.Second {
		t.Errorf("source.Timeout = %s, want 30s", source.Timeout)
	}
	if source.MaxCommandOutput != 64<<10 {
		t.Errorf("source.MaxCommandOutput = %d, want 64 KB", source.MaxCommandOutput)
	}
	var names []string
	for _, kv := range decl.CommandEnv("dummy") {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	// PATH, HOME, USER and LOGNAME, the proxy and CA variables, the
	// credential, and the base: nothing else of the yard's environment
	// reaches a command.
	wantEnv := []string{
		"PATH", "HOME", "USER", "LOGNAME",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"ACME_TOKEN", "YARDR_SOURCE_BASE",
	}
	if !slices.Equal(names, wantEnv) {
		t.Errorf("command env names = %v, want %v", names, wantEnv)
	}
	var b strings.Builder
	run := func(prog string, args []string, in string) {
		t.Helper()
		b.WriteString("== " + strings.TrimSpace(prog+" "+strings.Join(args, " ")) + " " + in + " ==\n")
		cmd := exec.Command(filepath.Join(home, "sources", "acme", "acme", prog), args...)
		cmd.Stdin = strings.NewReader(in)
		cmd.Env = decl.CommandEnv("dummy")
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Fatalf("%s %v: %v: %s", prog, args, err, exit.Stderr)
			}
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("%s %v printed no JSON object: %v: %q", prog, args, err, out)
		}
		b.Write(out)
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	run("normalize", nil, `{"headers":{},"body":{"action":"opened"}}`)
	run("normalize", nil, `{"headers":{},"body":{"action":"closed"}}`)
	run("thread", []string{"reply"}, `{"thread":"ACME-1","parent":"","body":"hi","key":"k-1"}`)
	run("thread", []string{"posted"}, `{"thread":"ACME-1","key":"k-1"}`)
	run("thread", []string{"read"}, `{"thread":"ACME-1"}`)
	run("thread", []string{"open"}, `{"in":"acme","title":"T","body":"B","key":"k-2"}`)
	run("thread", []string{"opened"}, `{"in":"acme","key":"k-2"}`)
	run("thread", []string{"state"}, `{"thread":"ACME-1","state":"started"}`)
	run("pr", nil, `{"repo":"acme/web","number":"7","url":""}`)
	run("ci", nil, `{"repo":"acme/web","number":"7","sha":"abc123"}`)
	golden(t, "stubs.out", b.String())
}

// One delivery through crew hears: a bead entering the stage the
// fixture crew member hears lands in its inbox, through the
// subscription the import set, and the entry matches the golden.
func TestPackAPI1Hears(t *testing.T) {
	t.Parallel()
	st, _, _ := contractSetup(t)
	page, err := st.Inbox(ctx, "keeper", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total > 0 {
		if err := st.AckInbox(ctx, "keeper", page.Last); err != nil {
			t.Fatal(err)
		}
	}
	depot := "web"
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "api1-decide", Title: "a bead for decide", Depot: depot, Stage: "decide"}, "me"); err != nil {
		t.Fatal(err)
	}
	page, err = st.Inbox(ctx, "keeper", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("keeper's inbox holds %d events, want the bead's", page.Total)
	}
	if got := store.Reason(page.Events[0], "keeper"); got != store.ReasonAll {
		t.Fatalf("the bead reached keeper as %q, want %q", got, store.ReasonAll)
	}
	golden(t, "hears-inbox.json", eventJSON(t, page.Events[0]))
}

// eventJSON is the event as JSON, with its numbers and time redacted:
// what the log carries, not when.
func eventJSON(t *testing.T, e store.Event) string {
	t.Helper()
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	v["id"], v["seq"], v["at"] = "<id>", "<seq>", "<time>"
	if data, ok := v["data"].(map[string]any); ok {
		if _, ok := data["seq"]; ok {
			data["seq"] = "<seq>"
		}
	}
	return canonicalJSON(t, v)
}

// canonicalJSON is v as indented JSON, without HTML escaping: goldens
// are read by reviewers.
func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// contractRunner starts nothing: the keeper's crew sessions in the makes
// and says test, which the dispatcher keeps running as ever.
type contractRunner struct{}

func (contractRunner) Start(context.Context, dispatch.Job) (map[string]any, error) {
	return map[string]any{"contract": true}, nil
}

func (contractRunner) Alive(context.Context, map[string]any) (bool, error) { return true, nil }
func (contractRunner) Stop(context.Context, map[string]any) error          { return nil }

func (contractRunner) StartCrew(context.Context, store.CrewMember, string) (map[string]any, error) {
	return map[string]any{"contract": true}, nil
}

// One envelope through the fixture's makes line makes the bead that
// tracks its thread, and one through its says line moves that bead: a
// script reads its event from the made bead, so its title, body and
// link match the golden, as does the move and its rule event.
func TestPackAPI1MakesAndSays(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the fixture's normalize is written in it")
	}
	st, own, home := contractSetup(t)
	decl, err := source.Parse(filepath.Join(home, "sources", "acme.yaml"), []byte(mustRead(t, filepath.Join(api1, "api1-source", "acme.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	dec := hooks.Declared{D: decl, Home: home}
	deliver := func(body string) {
		t.Helper()
		env, err := dec.Normalize(http.Header{}, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, dup, err := own.RecordHook(ctx, "acme", env.Map(), []byte(body), nil); err != nil || dup {
			t.Fatalf("record %s: duplicate=%v err=%v", body, dup, err)
		}
	}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"fake": contractRunner{}}}
	const thread = "https://acme.example.com/acme/issue/ACME-1"

	deliver(`{"action":"opened"}`)
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	links, err := st.IssueLinks(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Kind != store.LinkTracks {
		t.Fatalf("links to %s: %+v, want the one tracks link", thread, links)
	}
	made, err := st.GetBead(ctx, links[0].Bead)
	if err != nil {
		t.Fatal(err)
	}
	if made.Type != "task" || made.Stage != "new" || made.Depot == nil || *made.Depot != "web" {
		t.Fatalf("made %s: type %s stage %s depot %v", made.ID, made.Type, made.Stage, made.Depot)
	}
	golden(t, "made-bead.json", canonicalJSON(t, map[string]any{
		"title": made.Title, "body": made.Body,
		"link": map[string]any{"kind": links[0].Kind, "source": links[0].Source, "url": links[0].URL},
	}))

	// One bead for the thread: another delivery about it makes none
	// while the bead is open.
	deliver(`{"action":"opened","retry":true}`)
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if links, err := st.IssueLinks(ctx, thread); err != nil || len(links) != 1 {
		t.Fatalf("links to %s after a second delivery: %+v, %v", thread, links, err)
	}

	deliver(`{"action":"closed"}`)
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	moved, err := st.GetBead(ctx, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Stage != "review" {
		t.Fatalf("%s in %s after its issue closed, want review", moved.ID, moved.Stage)
	}
	evs, err := st.Events(ctx, made.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var rules strings.Builder
	for _, e := range evs {
		if e.Kind == "hook_rule" {
			rules.WriteString(eventJSON(t, e))
		}
	}
	golden(t, "says-move.txt", "stage: "+moved.Stage+"\n"+strings.ReplaceAll(rules.String(), made.ID, "<bead>"))
}

// The fixture's exec script runs through runner.Exec against a claimed
// bead, with YARDR_BIN a freshly built yardr and the yard served on the
// home's socket: it writes its environment and the result of each stable
// command but `call` to contract.out, which the test redacts to the
// stable seams and compares with the golden. A call needs a live
// credentialed source, which no fixture has; the stable call fields are
// the B3 test's to hold.
func TestPackAPI1Script(t *testing.T) {
	// Serial: it unsets the process's YARDR_* while the script runs, so
	// nothing of this session's reaches the script as the yard's.
	st, own, home := contractSetup(t)
	depot := "web"
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "api1-standin", Title: "the fixture script's bead",
		Type: "task", Depot: depot, Stage: "landed"}, "me"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Claim(ctx, "api1-standin", "web-assembly", "landed", "me")
	if err != nil {
		t.Fatal(err)
	}
	bead, err := st.GetBead(ctx, "api1-standin")
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == "web-assembly" })
	if i < 0 {
		t.Fatal("no group web-assembly")
	}
	depots, err := st.Depots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j := slices.IndexFunc(depots, func(d store.Depot) bool { return d.Name == "web" })
	if j < 0 {
		t.Fatal("no depot web")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "yardr")
	build := exec.Command("go", "build", "-o", bin, "yardr.dev/yardr")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	ln, err := net.Listen("unix", filepath.Join(home, "yard.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rpc.Handler(own, nil), ConnContext: rpc.ConnContext}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	saved := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		// Besides the golden knob itself, which the script's dump is
		// redacted of as any other unlisted variable.
		if strings.HasPrefix(k, "YARDR_") && k != "YARDR_UPDATE_GOLDEN" {
			saved[k] = v
			os.Unsetenv(k)
		}
	}
	t.Cleanup(func() {
		for k, v := range saved {
			os.Setenv(k, v)
		}
	})

	e := &runner.Exec{Home: home, Bin: bin}
	h, err := e.Start(ctx, dispatch.Job{Bead: *bead, Session: *sess, Group: groups[i], Depot: &depots[j]})
	if err != nil {
		t.Fatal(err)
	}
	// The leader exits when contract.out is whole; the deadline is only
	// a fuse against a hang.
	deadline := time.Now().Add(60 * time.Second)
	for {
		alive, err := e.Alive(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			e.Stop(ctx, h)
			t.Fatal("the fixture script never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(filepath.Join(home, "contract.out"))
	if err != nil {
		contractLog(t, h)
		t.Fatal(err)
	}
	red := redactContract(t, string(raw), home, bin)
	if want, err := os.ReadFile(filepath.Join(api1, "goldens", "script.out")); err != nil || red != string(want) {
		contractLog(t, h)
	}
	golden(t, "script.out", red)

	second, err := st.GetBead(ctx, "api1-second")
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "closed" || second.CloseReason == nil || *second.CloseReason != "the fixture is done with it" {
		t.Fatalf("api1-second: status %s reason %v", second.Status, second.CloseReason)
	}
	page, err := st.Inbox(ctx, "keeper", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(page.Events, func(e store.Event) bool {
		text, _ := e.Data["text"].(string)
		return e.Kind == "mail" && strings.Contains(text, "the fixture says hello")
	}) {
		t.Fatalf("keeper's inbox holds no mail of the fixture")
	}
}

// stableEnv are the YARDR_* variables docs/pack-api.md calls stable: the
// script's dump is redacted to them, so an internal variable may come
// and go but a stable one dropped fails.
var stableEnv = []string{
	"YARDR_BEAD", "YARDR_SESSION", "YARDR_STAGE", "YARDR_GROUP", "YARDR_ACTOR",
	"YARDR_HOME", "YARDR_BIN", "YARDR_TYPE", "YARDR_TRAIN",
	"YARDR_DEPOT", "YARDR_DEPOT_PATH", "YARDR_DEPOT_KIND",
	"YARDR_BRANCH", "YARDR_BASE", "YARDR_WORKTREE", "YARDR_TEST",
	"YARDR_FIXTURE",
}

// stableBead are the fields of a bead docs/pack-api.md calls stable.
var stableBead = []string{
	"id", "title", "body", "type", "stage", "status", "priority", "depot", "labels", "metadata",
}

// redactContract is contract.out redacted to the stable seams: the
// stable variables with their paths and numbers redacted, the stable
// JSON fields of each --json answer, the exit of every command, and the
// value settings get prints, which is stable as text. Prose answers and
// internal fields are dropped: the contract does not hold them.
func redactContract(t *testing.T, out, home, bin string) string {
	t.Helper()
	var red strings.Builder
	sections := strings.Split(out, "== ")
	for _, s := range sections {
		head, body, _ := strings.Cut(s, " ==\n")
		head = strings.TrimSpace(head)
		if head == "" {
			continue
		}
		red.WriteString("== " + strings.ReplaceAll(head, bin, "<bin>") + " ==\n")
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		if head == "env" {
			for _, line := range lines {
				k, v, _ := strings.Cut(line, "=")
				switch {
				case k == "TRACEPARENT":
					red.WriteString("TRACEPARENT=<trace>\n")
				case slices.Contains(stableEnv, k):
					v = strings.ReplaceAll(strings.ReplaceAll(v, home, "<home>"), bin, "<bin>")
					if k == "YARDR_SESSION" {
						v = "<session>"
					}
					red.WriteString(k + "=" + v + "\n")
				}
			}
			continue
		}
		exit := ""
		var doc []string
		for _, line := range lines {
			if v, ok := strings.CutPrefix(line, "exit="); ok {
				exit = v
			} else {
				doc = append(doc, line)
			}
		}
		redact := func(v any) {
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var w any
			dec := json.NewDecoder(strings.NewReader(string(out)))
			dec.UseNumber()
			if err := dec.Decode(&w); err != nil {
				t.Fatal(err)
			}
			red.WriteString(canonicalJSON(t, w))
		}
		keep := func(v map[string]any, keys ...string) map[string]any {
			out := map[string]any{}
			for _, k := range keys {
				if val, ok := v[k]; ok {
					out[k] = val
				}
			}
			return out
		}
		which := func() string {
			switch {
			case strings.Contains(head, "prime --bead"):
				return "prime"
			case strings.Contains(head, "no-such-bead"):
				return "error"
			case strings.Contains(head, "bead show"), strings.Contains(head, "bead create"):
				return "bead"
			case strings.Contains(head, "link list"):
				return "links"
			case strings.Contains(head, "settings get operator --json"):
				return "settings"
			case strings.Contains(head, "settings get operator"):
				return "setting"
			default:
				return "text"
			}
		}()
		switch which {
		case "text":
			// Prose is internal; the exit below is the contract.
		case "setting":
			red.WriteString(strings.Join(doc, "\n") + "\n")
		case "error":
			var v map[string]any
			if err := json.Unmarshal([]byte(strings.Join(doc, "\n")), &v); err != nil {
				t.Fatalf("%s printed no JSON: %v", head, err)
			}
			if _, ok := v["error"]; !ok {
				t.Fatalf("%s printed no error object: %v", head, v)
			}
			redact(map[string]any{"error": "<error>"})
		case "links":
			var v []map[string]any
			if err := json.Unmarshal([]byte(strings.Join(doc, "\n")), &v); err != nil {
				t.Fatalf("%s printed no JSON array: %v", head, err)
			}
			out := []map[string]any{}
			for _, l := range v {
				out = append(out, keep(l, "kind", "source", "url"))
			}
			redact(out)
		case "settings":
			var v map[string]any
			if err := json.Unmarshal([]byte(strings.Join(doc, "\n")), &v); err != nil {
				t.Fatalf("%s printed no JSON: %v", head, err)
			}
			redact(keep(v, "key", "value"))
		case "bead":
			var v map[string]any
			if err := json.Unmarshal([]byte(strings.Join(doc, "\n")), &v); err != nil {
				t.Fatalf("%s printed no JSON: %v", head, err)
			}
			redact(keep(v, stableBead...))
		case "prime":
			var v map[string]any
			if err := json.Unmarshal([]byte(strings.Join(doc, "\n")), &v); err != nil {
				t.Fatalf("%s printed no JSON: %v", head, err)
			}
			bead, _ := v["bead"].(map[string]any)
			next, _ := v["next"].(map[string]any)
			outcomes := []any{}
			if list, _ := next["outcomes"].([]any); list != nil {
				for _, o := range list {
					if m, ok := o.(map[string]any); ok {
						outcomes = append(outcomes, keep(m, "name", "goes"))
					}
				}
			}
			redact(map[string]any{
				"bead": keep(bead, stableBead...),
				"next": map[string]any{
					"stage":    next["stage"],
					"stages":   next["stages"],
					"outcomes": outcomes,
				},
			})
		}
		red.WriteString("exit=" + exit + "\n")
	}
	return red.String()
}

// contractLog prints the session's log for a script run that failed.
func contractLog(t *testing.T, h map[string]any) {
	t.Helper()
	path, _ := h["log"].(string)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Logf("session log %s: %v", path, err)
		return
	}
	t.Logf("session log %s:\n%s", path, b)
}

func contractYard(t *testing.T) (store.API, *store.Store, string) {
	t.Helper()
	home := contractHome(t)
	api, own := storetest.NewOwnedIn(t, home)
	if err := api.PutDepot(ctx, store.Depot{Name: "web", Base: "main", Test: "sh .yardr/test"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := api.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	return api, own, home
}
