package dispatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const (
	issueA  = "https://linear.app/acme/issue/YRD-1"
	issueB  = "https://linear.app/acme/issue/YRD-2"
	issueGH = "https://github.com/acme/webapp/issues/7"

	// The starter flow's shape for tasks.
	issueFlow = `
backlog  -> new
new      -> review on done | backlog
review   -> merged on done | new on changes
merged terminal
`
)

// issueYard is a yard with a stand-in for Linear and GitHub, crew member
// yardmaster whose env_file holds the credentials the stand-in takes, and a
// hook per source that maps as the yardmaster.
type issueYard struct {
	api   store.API
	d     *dispatch.Dispatcher
	src   *standin.Server
	down  atomic.Bool  // the stand-in answers 503
	asked atomic.Int64 // requests that reached the stand-in or its outage
	now   time.Time
	env   string // the yardmaster's env_file
	home  string // the yard's home, which a declared source's commands run from
	base  string // the stand-in's address, which a declared pack is pointed at
}

func newIssueYard(t *testing.T) *issueYard {
	t.Helper()
	api := storetest.New(t)
	ctx := context.Background()
	y := &issueYard{api: api, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		src: &standin.Server{LinearKey: "lin_api_yardmaster", GitHubToken: "gh-yardmaster"},
		env: filepath.Join(t.TempDir(), "yardmaster.env")}
	inner := y.src.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		y.asked.Add(1)
		if y.down.Load() {
			http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	y.base = srv.URL
	y.src.Add(standin.Issue{Source: "linear", ID: "uuid-1", Key: "YRD-1", Title: "Web view", Status: "Todo"})
	y.src.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Backlog"})
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: "open"})

	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_yardmaster\nGITHUB_TOKEN=gh-yardmaster\n"), 0o600))
	must(t, api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"))
	flow, err := store.ParseFlow(issueFlow)
	must(t, err)
	flow.Name = "yard"
	_, err = api.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, api.SetFlowPointer(ctx, "yard", "", "yard", "me"))
	y.yardmaster(t, "linear,github")
	for _, source := range []string{"linear", "github"} {
		must(t, api.SetIdentity(ctx, "yardmaster", source, store.Identity{ID: "u-yardmaster"}, "me"))
	}
	_, err = api.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s", As: "yardmaster",
		Map: map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done"}}, "me")
	must(t, err)
	_, err = api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/s", As: "yardmaster",
		Map: map[string]string{"task:new": "started", "task:merged": "done"}}, "me")
	must(t, err)
	y.d = y.daemon()
	return y
}

// daemon is a fresh dispatcher on the yard's store: what a restart leaves.
func (y *issueYard) daemon() *dispatch.Dispatcher {
	d := &dispatch.Dispatcher{Store: y.api, Home: y.home, Runners: map[string]dispatch.Runner{"fake": &fakeRunner{dead: map[string]bool{}}}}
	dispatch.SetClock(d, func() time.Time { return y.now })
	return d
}

// declareLinearPack declares the pack's linear source against the
// stand-in, with its commands under home (a directory of the test's
// when ""). Moves of Linear issues run the pack's thread.state.
func (y *issueYard) declareLinearPack(t *testing.T, home string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's thread commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread commands call Linear with it")
	}
	if home == "" {
		home = t.TempDir()
	}
	// The pack as it is, pointed at the stand-in.
	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	must(t, err)
	text = []byte(strings.Replace(string(text), "base: https://api.linear.app", "base: "+y.base, 1))
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	must(t, err)
	must(t, d.CopyCommandFiles(dir, home))
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	y.home, y.d.Home = home, home
	_, err = y.api.PutSource(context.Background(), "linear.yaml", string(text), "me")
	must(t, err)
}

// declareGitHubPack declares the pack's github source against the
// stand-in, with its commands under home (a directory of the test's
// when ""). Moves of GitHub issues run the pack's thread.state.
func (y *issueYard) declareGitHubPack(t *testing.T, home string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's thread commands are written in it")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread commands call GitHub with it")
	}
	if home == "" {
		home = t.TempDir()
	}
	// The pack as it is, pointed at the stand-in.
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	must(t, err)
	text = []byte(strings.Replace(string(text), "base: https://api.github.com", "base: "+y.base, 1))
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	must(t, err)
	must(t, d.CopyCommandFiles(dir, home))
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	y.home, y.d.Home = home, home
	_, err = y.api.PutSource(context.Background(), "github.yaml", string(text), "me")
	must(t, err)
}

// declarations are the yard's declared sources, parsed.
func (y *issueYard) declarations(t *testing.T) []*source.Declaration {
	t.Helper()
	srcs, err := y.api.Sources(context.Background())
	must(t, err)
	var out []*source.Declaration
	for _, s := range srcs {
		d, err := source.Parse(s.Name+".yaml", []byte(s.Text))
		must(t, err)
		out = append(out, d)
	}
	return out
}

// yardmaster defines the yardmaster with set_state for sources ("": none).
func (y *issueYard) yardmaster(t *testing.T, sources string) {
	t.Helper()
	def := store.CrewMember{Name: "yardmaster", Runner: "fake", Config: map[string]any{"env_file": y.env}}
	if sources != "" {
		def.Config[store.SetState] = sources
	}
	_, err := y.api.PutCrew(context.Background(), def, "me")
	must(t, err)
}

// bead creates a task in backlog tracking the issues, linked by URL alone.
func (y *issueYard) bead(t *testing.T, id string, issues ...string) {
	t.Helper()
	nb := store.NewBead{ID: id, Title: id, Depot: "yard", Stage: "backlog"}
	for _, u := range issues {
		source, fixed, err := hooks.Issue(u, y.declarations(t)...)
		must(t, err)
		nb.Links = append(nb.Links, store.NewLink{Kind: store.LinkTracks, Source: source, URL: fixed})
	}
	_, err := y.api.CreateBead(context.Background(), nb, "me")
	must(t, err)
}

// to moves the bead to stage and lets the daemon make a pass.
func (y *issueYard) to(t *testing.T, id, stage string) {
	t.Helper()
	_, err := y.api.Advance(context.Background(), id, stage, store.Expect{}, "me")
	must(t, err)
	y.pass(t)
}

func (y *issueYard) pass(t *testing.T) {
	t.Helper()
	must(t, y.d.Reconcile(context.Background()))
}

// said is what the hooks logged on the bead, oldest first: "state: status
// (was was)" for a thread_state and "state skipped: reason" for a skip.
func (y *issueYard) said(t *testing.T, id string) []string {
	t.Helper()
	evs, err := y.api.Events(context.Background(), id, 100)
	must(t, err)
	out := []string{}
	for _, e := range evs {
		switch e.Kind {
		case "thread_state":
			out = append(out, fmt.Sprintf("%v: %v (was %v)", e.Data["state"], e.Data["status"], e.Data["was"]))
		case "thread_state_skipped":
			out = append(out, fmt.Sprintf("%v skipped: %v", e.Data["state"], e.Data["reason"]))
		}
	}
	return out
}

// skipped is the last thread_state_skipped on the bead.
func (y *issueYard) skipped(t *testing.T, id string) store.Event {
	t.Helper()
	evs, err := y.api.Events(context.Background(), id, 100)
	must(t, err)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "thread_state_skipped" {
			return evs[i]
		}
	}
	t.Fatalf("no thread_state_skipped on %s", id)
	return store.Event{}
}

func (y *issueYard) pending(t *testing.T) []store.IssueMove {
	t.Helper()
	ms, err := y.api.IssueMoves(context.Background())
	must(t, err)
	return ms
}

func want[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: %v, want %v", what, got, want)
	}
}

// The bead goes new, review, merged and the issue it tracks, linked by its
// URL alone, goes started, review, done, as the yardmaster.
func TestIssueFollowsItsBead(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.bead(t, "yard-a", issueA+"/web-view")
	y.pass(t)
	want(t, "calls in backlog", len(y.src.Calls()), 0)
	for _, c := range []struct{ stage, status string }{{"new", "In Progress"}, {"review", "In Review"}, {"merged", "Done"}} {
		y.to(t, "yard-a", c.stage)
		want(t, "the issue after "+c.stage, y.src.Status("linear", "YRD-1"), c.status)
	}
	want(t, "events", y.said(t, "yard-a"), []string{"started: In Progress (was Todo)", "review: In Review (was In Progress)", "done: Done (was In Review)"})
	want(t, "calls", y.src.Calls(), []string{"linear read YRD-1", "linear set YRD-1 In Progress",
		"linear read YRD-1", "linear set YRD-1 In Review", "linear read YRD-1", "linear set YRD-1 Done"})
	want(t, "moves left", len(y.pending(t)), 0)

	evs, err := y.api.Events(context.Background(), "yard-a", 100)
	must(t, err)
	var adv, state store.Event
	for _, e := range evs {
		if e.Kind == "advanced" && e.Data["to"] == "review" {
			adv = e
		}
		if e.Kind == "thread_state" && e.Data["state"] == "review" {
			state = e
		}
	}
	// The hook is the cause; the thread is the move's, the issue's key.
	if state.Actor != "hook:linear" || state.Data["hook"] != "linear" || state.Data["crew"] != "yardmaster" || state.Data["stage"] != "review" ||
		state.Data["source"] != "linear" || state.Data["url"] != issueA || state.Data["thread"] != "YRD-1" ||
		fmt.Sprint(state.Data["cause"]) != fmt.Sprint(adv.Seq) || state.Seq < adv.Seq {
		t.Fatalf("thread_state %+v for advanced %+v", state, adv)
	}
	// Nothing more on a later pass.
	y.pass(t)
	want(t, "sets", len(y.src.Sets()), 3)
}

// An issue is only moved forward: a bead sent back leaves it, and so does
// an issue a person moved ahead, or out of the way, by hand.
func TestIssueOnlyMovesForward(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.bead(t, "yard-a", issueA)
	y.to(t, "yard-a", "new")
	y.to(t, "yard-a", "review")
	_, err := y.api.Done(context.Background(), "yard-a", "changes", "", store.Expect{}, "me")
	must(t, err)
	y.pass(t)
	want(t, "the issue of a bead sent back", y.src.Status("linear", "YRD-1"), "In Review")
	e := y.skipped(t, "yard-a")
	if e.Actor != "hook:linear" || e.Data["reason"] != store.SkipPast || e.Data["status"] != "In Review" || e.Data["state"] != "started" ||
		e.Data["stage"] != "new" || e.Data["crew"] != "yardmaster" || e.Data["detail"] != "the issue is In Review, which is at or past started" {
		t.Fatalf("skipped %+v", e)
	}
	// Review again: it is there.
	y.to(t, "yard-a", "review")
	want(t, "events", y.said(t, "yard-a"), []string{"started: In Progress (was Todo)", "review: In Review (was In Progress)",
		"started skipped: already_past", "review skipped: already_past"})
	want(t, "sets", len(y.src.Sets()), 2)

	// Moved to done by hand while its bead is still in new.
	y.bead(t, "yard-b", issueB)
	y.to(t, "yard-b", "new")
	y.src.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Done"})
	y.to(t, "yard-b", "review")
	want(t, "the issue moved by hand", y.src.Status("linear", "YRD-2"), "Done")
	// Cancelled by hand: past everything.
	y.src.Add(standin.Issue{Source: "linear", ID: "uuid-2", Key: "YRD-2", Title: "Links", Status: "Canceled"})
	y.to(t, "yard-b", "merged")
	want(t, "the cancelled issue", y.src.Status("linear", "YRD-2"), "Canceled")
	want(t, "events", y.said(t, "yard-b"), []string{"started: In Progress (was Backlog)", "review skipped: already_past", "done skipped: already_past"})
	want(t, "sets", len(y.src.Sets()), 3)
}

// Without set_state for the source nothing is sent, and the event names
// the setting. Once the operator allows it, the next stage moves the issue.
func TestIssueNeedsSetState(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.yardmaster(t, "github")
	y.bead(t, "yard-a", issueA)
	y.to(t, "yard-a", "new")
	want(t, "requests", y.asked.Load(), int64(0))
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "Todo")
	e := y.skipped(t, "yard-a")
	detail, _ := e.Data["detail"].(string)
	if e.Data["reason"] != store.SkipNoGrant || !strings.Contains(detail, "crew member yardmaster may not set the status of threads in linear") ||
		!strings.Contains(detail, "yardr crew set yardmaster set_state=github,linear") {
		t.Fatalf("skipped %+v", e)
	}
	want(t, "moves left", len(y.pending(t)), 0)

	y.yardmaster(t, "github,linear")
	y.to(t, "yard-a", "review")
	want(t, "the issue once allowed", y.src.Status("linear", "YRD-1"), "In Review")
	want(t, "events", y.said(t, "yard-a"), []string{"started skipped: no_grant", "review: In Review (was Todo)"})
}

// The source down for a while, then up: the move is tried again after a
// wait that grows, by whichever daemon runs then, and the issue is changed
// once.
func TestIssueMoveWaitsOutAnOutage(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.bead(t, "yard-a", issueA)
	y.bead(t, "yard-b", issueB)
	y.down.Store(true)
	y.to(t, "yard-a", "new")
	y.to(t, "yard-b", "new")
	// One call failed; the second move of the source was not tried in its
	// wake, and a pass before the wait is over asks nothing.
	y.pass(t)
	want(t, "requests while down", y.asked.Load(), int64(1))
	ms := y.pending(t)
	if len(ms) != 2 || ms[0].Attempts != 1 || !strings.Contains(ms[0].Error, "503") || !ms[0].RetryAt.Equal(y.now.Add(30*time.Second)) || ms[1].Attempts != 0 {
		t.Fatalf("moves %+v", ms)
	}
	want(t, "events while down", y.said(t, "yard-a"), []string{})

	y.now = y.now.Add(31 * time.Second)
	y.pass(t)
	ms = y.pending(t)
	if ms[0].Attempts != 2 || !ms[0].RetryAt.Equal(y.now.Add(time.Minute)) {
		t.Fatalf("second try: %+v", ms[0])
	}

	// Up again, and the daemon restarted meanwhile.
	y.down.Store(false)
	y.d = y.daemon()
	y.pass(t)
	want(t, "yard-b's issue, never tried before", y.src.Status("linear", "YRD-2"), "In Progress")
	want(t, "yard-a's issue before its wait is over", y.src.Status("linear", "YRD-1"), "Todo")
	y.now = y.now.Add(61 * time.Second)
	y.pass(t)
	y.pass(t)
	want(t, "yard-a's issue", y.src.Status("linear", "YRD-1"), "In Progress")
	want(t, "sets", y.src.Sets(), []string{"linear set YRD-2 In Progress", "linear set YRD-1 In Progress"})
	want(t, "events", y.said(t, "yard-a"), []string{"started: In Progress (was Todo)"})
	want(t, "moves left", len(y.pending(t)), 0)
}

// A source that keeps failing is given up after IssueTries tries, and the
// event says so.
func TestIssueMoveGivesUp(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.d.IssueTries = 3
	y.bead(t, "yard-a", issueA)
	y.down.Store(true)
	y.to(t, "yard-a", "new")
	for i := 0; i < 5; i++ {
		y.now = y.now.Add(2 * time.Hour)
		y.pass(t)
	}
	want(t, "requests", y.asked.Load(), int64(3))
	e := y.skipped(t, "yard-a")
	detail, _ := e.Data["detail"].(string)
	if e.Data["reason"] != store.SkipFailed || !strings.HasPrefix(detail, "gave up after 3 tries: thread.state of source linear: ") || !strings.Contains(detail, "503") ||
		fmt.Sprint(e.Data["attempts"]) != "3" {
		t.Fatalf("skipped %+v", e)
	}
	want(t, "moves left", len(y.pending(t)), 0)
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "Todo")
}

// A stage mapped to a label adds it to the issue the bead tracks, and to a
// pull request, which no state is asked of; a thread that has the label is
// skipped with no write, and nothing is closed or removed.
func TestIssueMoveAddsALabel(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareGitHubPack(t, "")
	ctx := context.Background()
	const pull = "https://github.com/acme/webapp/pull/8"
	_, err := y.api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/s", As: "yardmaster",
		Map: map[string]string{"task:new": "label:in-progress", "task:review": "label:in-review", "task:merged": "done"}}, "me")
	must(t, err)
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#8", Title: "A change", Status: "open", Pull: true, Labels: []string{"In-Progress"}})
	y.bead(t, "yard-a", issueGH)
	y.bead(t, "yard-b", pull)

	y.to(t, "yard-a", "new")
	want(t, "the issue's labels", fmt.Sprint(y.src.Labels("github", "acme/webapp#7")), "[in-progress]")
	want(t, "said", strings.Join(y.said(t, "yard-a"), "; "), "label:in-progress: labeled in-progress (was <nil>)")
	// The pull request has the label, as someone spelled it.
	y.to(t, "yard-b", "new")
	e := y.skipped(t, "yard-b")
	if e.Data["reason"] != store.SkipPast || e.Data["detail"] != "the issue is labeled In-Progress already" || e.Data["status"] != "labeled In-Progress" {
		t.Fatalf("skipped %+v", e)
	}
	y.to(t, "yard-b", "review")
	want(t, "the pull request's labels", fmt.Sprint(y.src.Labels("github", "acme/webapp#8")), "[In-Progress in-review]")
	want(t, "the pull request", y.src.Status("github", "acme/webapp#8"), "open")
	// A bead sent back adds what its stage says and takes nothing off.
	y.to(t, "yard-a", "review")
	y.to(t, "yard-a", "new")
	want(t, "the issue's labels", fmt.Sprint(y.src.Labels("github", "acme/webapp#7")), "[in-progress in-review]")
	want(t, "sets", strings.Join(y.src.Sets(), "; "),
		"github label acme/webapp#7 in-progress; github label acme/webapp#8 in-review; github label acme/webapp#7 in-review")
	want(t, "moves left", len(y.pending(t)), 0)
}

// A word the source has no status for is skipped at once, with nothing
// sent; a state asked of a pull request fails in the script, which reads
// as the source failing, so the move waits and is tried again.
func TestIssueMoveRefusedForGood(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareGitHubPack(t, "")
	ctx := context.Background()
	// The CLI refuses review for GitHub; a map written past it is still
	// not acted on.
	_, err := y.api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/s", As: "yardmaster",
		Map: map[string]string{"task:review": "review", "task:merged": "done"}}, "me")
	must(t, err)
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "A change", Status: "open", Pull: true})
	y.bead(t, "yard-a", issueGH)
	y.to(t, "yard-a", "new")
	y.to(t, "yard-a", "review")
	e := y.skipped(t, "yard-a")
	if detail, _ := e.Data["detail"].(string); e.Data["reason"] != store.SkipFailed || !strings.Contains(detail, `"review" is no state of github`) || e.Data["attempts"] != nil {
		t.Fatalf("skipped %+v", e)
	}
	want(t, "requests for review", y.asked.Load(), int64(0))
	// A state asked of a pull request fails in the script, which the
	// daemon cannot tell from the source failing: the move waits and is
	// tried again, with nothing set.
	y.to(t, "yard-a", "merged")
	ms := y.pending(t)
	if len(ms) != 1 || ms[0].Attempts != 1 || !strings.Contains(ms[0].Error, "is a pull request") {
		t.Fatalf("moves %+v", ms)
	}
	want(t, "sets", y.src.Sets(), nil)
}

// A hook that acts as a crew member whose env_file names a GitHub App moves
// the issue as the App: the daemon runs the source's mint_command, which
// signs as the App, asks for the installation of the issue's repository
// and answers a token narrowed to it, with which the pack's thread.state
// closes the issue. The token is minted once per thread and used again by
// its next move. Where the App is not installed the move is tried again
// and says what is missing. Neither the token nor the key is in an event
// or in the daemon's log.
func TestIssueMoveAsAGitHubApp(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl is not installed, and the mint signs the App's JWT with it")
	}
	y := newIssueYard(t)
	y.declareGitHubPack(t, "")
	mint, err := filepath.Abs(filepath.Join("..", "..", "contrib", "github-mint", "github-mint"))
	must(t, err)
	_, err = y.api.SetSource(context.Background(), "github", store.SourceMintCommand, mint, "me")
	must(t, err)
	key := filepath.Join(t.TempDir(), "yardr.pem")
	app, err := standin.NewApp("4242", "yardr", key, "acme/webapp")
	must(t, err)
	y.src.App = app
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#8", Title: "Timeouts", Status: "open"})
	y.src.Add(standin.Issue{Source: "github", Key: "other/repo#1", Title: "Elsewhere", Status: "open"})
	// GITHUB_TOKEN is there too: the App is who acts.
	must(t, os.WriteFile(y.env, []byte("GITHUB_TOKEN=gh-yardmaster\nGITHUB_APP_ID=4242\nGITHUB_APP_PRIVATE_KEY_FILE="+key+"\n"), 0o600))
	var logged bytes.Buffer
	y.d.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))

	y.bead(t, "yard-a", "https://github.com/acme/webapp/issues/7")
	y.bead(t, "yard-b", "https://github.com/acme/webapp/issues/8")
	y.bead(t, "yard-c", "https://github.com/other/repo/issues/1")
	for _, id := range []string{"yard-a", "yard-b", "yard-c"} {
		y.to(t, id, "new")
		y.to(t, id, "review")
		y.to(t, id, "merged")
	}
	want(t, "calls", y.src.Calls(), []string{
		"github app installation acme/webapp", "github app token acme/webapp",
		// Entering new reads the issue and leaves it: open is started.
		"github read acme/webapp#7", "github read acme/webapp#7", "github set acme/webapp#7 closed",
		"github app installation acme/webapp", "github app token acme/webapp",
		"github read acme/webapp#8", "github read acme/webapp#8", "github set acme/webapp#8 closed",
		"github app installation other/repo"})
	ms := y.pending(t)
	if len(ms) == 0 || ms[0].Bead != "yard-c" || !strings.Contains(ms[0].Error, "no installation of the App on other/repo") || !strings.Contains(ms[0].Error, "install the App there") {
		t.Fatalf("moves %+v", ms)
	}

	// A key file open to others is refused as the env_file would be.
	must(t, os.Chmod(key, 0o644))
	y.src.Add(standin.Issue{Source: "github", Key: "acme/webapp#9", Title: "Leaks", Status: "open"})
	y.bead(t, "yard-d", "https://github.com/acme/webapp/issues/9")
	y.to(t, "yard-d", "new")
	y.to(t, "yard-d", "review")
	y.d = y.daemon() // a new daemon mints again for a thread it has no token for
	y.d.Log = slog.New(slog.NewTextHandler(&logged, nil))
	y.to(t, "yard-d", "merged")
	found := false
	for _, m := range y.pending(t) {
		found = found || strings.Contains(m.Error, key) && strings.Contains(m.Error, "open to others")
	}
	if !found {
		t.Fatalf("moves %+v", y.pending(t))
	}

	evs, err := y.api.EventsSince(context.Background(), 0, 0)
	must(t, err)
	raw, err := json.Marshal(evs)
	must(t, err)
	pem, err := os.ReadFile(key)
	must(t, err)
	secrets := y.src.Tokens()
	for _, line := range strings.Split(string(pem), "\n") {
		if len(line) > 20 && !strings.HasPrefix(line, "-----") {
			secrets = append(secrets, line)
		}
	}
	if len(secrets) < 2 {
		t.Fatalf("secrets to look for: %d", len(secrets))
	}
	for _, s := range secrets {
		if strings.Contains(string(raw), s) || strings.Contains(logged.String(), s) {
			t.Fatalf("a secret shows in the events or the log:\n%s", logged.String())
		}
	}
}

// The credential is the crew member's own, from its env_file, and nothing
// else: without it the move waits, with nothing sent, and is made once the
// file has it.
func TestIssueMoveUsesTheCrewCredential(t *testing.T) {
	// Serial: it changes process environment, which parallel tests cannot share.
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	t.Setenv("LINEAR_API_KEY", "lin_api_yardmaster") // the daemon's own environment is not the crew member's
	must(t, os.WriteFile(y.env, []byte("GITHUB_TOKEN=gh-yardmaster\n"), 0o600))
	y.bead(t, "yard-a", issueA)
	y.to(t, "yard-a", "new")
	want(t, "requests", y.asked.Load(), int64(0))
	ms := y.pending(t)
	if len(ms) != 1 || ms[0].Attempts != 1 || !strings.Contains(ms[0].Error, "does not set LINEAR_API_KEY") {
		t.Fatalf("moves %+v", ms)
	}
	// A credential the source does not take is the source failing.
	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_other\n"), 0o600))
	y.now = y.now.Add(time.Minute)
	y.pass(t)
	want(t, "calls", y.src.Calls(), []string{"linear refused"})
	if ms = y.pending(t); len(ms) != 1 || ms[0].Attempts != 2 || !strings.Contains(ms[0].Error, "Authentication required") || strings.Contains(ms[0].Error, "lin_api") {
		t.Fatalf("moves %+v", ms)
	}
	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_yardmaster\n"), 0o600))
	y.now = y.now.Add(time.Hour)
	y.pass(t)
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "In Progress")

	// No env_file at all.
	_, err := y.api.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "fake",
		Config: map[string]any{store.SetState: "linear"}}, "me")
	must(t, err)
	y.to(t, "yard-a", "review")
	if ms = y.pending(t); len(ms) != 1 || !strings.Contains(ms[0].Error, "crew member yardmaster has no env_file") {
		t.Fatalf("moves %+v", ms)
	}
	// The hook removed: what it had still to do is not done.
	must(t, y.api.DeleteHook(context.Background(), "linear", "me"))
	y.yardmaster(t, "linear")
	y.now = y.now.Add(time.Hour)
	y.pass(t)
	want(t, "the issue after the hook was removed", y.src.Status("linear", "YRD-1"), "In Progress")
}

// A bead tracking a Linear and a GitHub issue, each with a hook and map:
// both move.
func TestIssuesOfTwoSources(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	y.declareLinearPack(t, "")
	y.declareGitHubPack(t, y.home)
	y.bead(t, "yard-a", issueA, issueGH)
	y.to(t, "yard-a", "new")
	want(t, "linear after new", y.src.Status("linear", "YRD-1"), "In Progress")
	want(t, "github after new", y.src.Status("github", "acme/webapp#7"), "open")
	y.to(t, "yard-a", "review")
	y.to(t, "yard-a", "merged")
	want(t, "linear", y.src.Status("linear", "YRD-1"), "Done")
	want(t, "github", y.src.Status("github", "acme/webapp#7"), "closed")
	want(t, "sets", y.src.Sets(), []string{"linear set YRD-1 In Progress", "linear set YRD-1 In Review",
		"github set acme/webapp#7 closed", "linear set YRD-1 Done"})
	// An open GitHub issue stands at started already.
	want(t, "events", y.said(t, "yard-a"), []string{"started skipped: already_past", "started: In Progress (was Todo)",
		"review: In Review (was In Progress)", "done: closed (was open)", "done: Done (was In Review)"})
}

// A crew member whose credential comes from a provider (env_command) has its
// issue moved with what the provider prints, asked again for each move; with
// an env_file too, the provider's value wins. A provider that fails makes
// the move wait with nothing sent, saying which command and how it ended,
// and what it printed is in no event or log.
func TestIssueMoveUsesTheCrewEnvCommand(t *testing.T) {
	t.Parallel()
	y := newIssueYard(t)
	var logged bytes.Buffer
	y.d.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	cmd, runs := filepath.Join(dir, "provider"), filepath.Join(dir, "runs")
	provider := func(body string) {
		t.Helper()
		scripttest.Write(t, cmd, "#!/bin/sh\necho \"$YARDR_CREW in $YARDR_HOME\" >> "+runs+"\n"+body+"\n")
	}
	yardmaster := func(cfg map[string]any) {
		t.Helper()
		cfg[store.SetState] = "linear,github"
		_, err := y.api.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "fake", Config: cfg}, "me")
		must(t, err)
	}
	y.d.Home = dir
	y.declareLinearPack(t, dir)
	y.declareGitHubPack(t, dir)

	// The provider alone.
	provider(`echo "LINEAR_API_KEY=lin_api_yardmaster"`)
	yardmaster(map[string]any{"env_command": cmd})
	y.bead(t, "yard-a", issueA)
	y.to(t, "yard-a", "new")
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "In Progress")
	raw, err := os.ReadFile(runs)
	must(t, err)
	want(t, "the provider's runs", string(raw), "yardmaster in "+dir+"\n")

	// With an env_file whose key the source refuses: the provider's wins,
	// and the file's other key is still the crew member's.
	must(t, os.WriteFile(y.env, []byte("LINEAR_API_KEY=lin_api_other\nGITHUB_TOKEN=gh-yardmaster\n"), 0o600))
	yardmaster(map[string]any{"env_file": y.env, "env_command": cmd})
	y.to(t, "yard-a", "review")
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "In Review")
	y.bead(t, "yard-g", issueGH)
	y.to(t, "yard-g", "new")
	want(t, "moves left", len(y.pending(t)), 0)

	// The provider fails: the move waits, the source is not asked.
	provider(`echo "LINEAR_API_KEY=lin_api_yardmaster"; echo "vault is locked" >&2; exit 3`)
	asked := y.asked.Load()
	y.to(t, "yard-a", "merged")
	ms := y.pending(t)
	if len(ms) != 1 || ms[0].Attempts != 1 || y.asked.Load() != asked {
		t.Fatalf("moves %+v, asked %d more", ms, y.asked.Load()-asked)
	}
	for _, w := range []string{"env_command " + cmd, "exit status 3", "vault is locked"} {
		if !strings.Contains(ms[0].Error, w) {
			t.Errorf("the move's error %q lacks %q", ms[0].Error, w)
		}
	}
	// It does not set the name: said as such, with where it was looked for.
	provider(`echo "OTHER=lin_api_yardmaster"`)
	yardmaster(map[string]any{"env_command": cmd})
	y.now = y.now.Add(time.Minute)
	y.pass(t)
	if ms = y.pending(t); len(ms) != 1 || !strings.Contains(ms[0].Error, "crew member yardmaster's env_command "+cmd+" does not set LINEAR_API_KEY") {
		t.Fatalf("moves %+v", ms)
	}
	// It answers again: the move is made.
	provider(`echo "LINEAR_API_KEY=lin_api_yardmaster"`)
	y.now = y.now.Add(time.Hour)
	y.pass(t)
	want(t, "the issue", y.src.Status("linear", "YRD-1"), "Done")

	evs, err := y.api.EventsSince(context.Background(), 0, 0)
	must(t, err)
	all, err := json.Marshal(evs)
	must(t, err)
	for _, s := range []string{"lin_api_yardmaster", "lin_api_other", "gh-yardmaster"} {
		if strings.Contains(string(all), s) || strings.Contains(logged.String(), s) {
			t.Fatalf("%s shows in the events or the log:\n%s", s, logged.String())
		}
	}
}
