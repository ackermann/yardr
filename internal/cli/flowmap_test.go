package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// prime draws the flow governing the bead before its stage file: every
// stage with who works it and its one line, the bead's stage marked, and
// the path it took once it has moved. Only the stage the bead is in has its
// file printed, without its `summary:` line; --json carries the same map.
// A store flow's lines come from the stage files in the yard's home.
func TestPrimeShowsTheFlowMap(t *testing.T) {
	a, repo, home, git, write := stageYard(t)
	ctx := context.Background()
	write(repo, map[string]string{
		".yardr/flows/default/new.md":    "summary: A builder implements the bead.\n\nBuild it on your branch. Then report.\n",
		".yardr/flows/default/review.md": "The diff is read. All of it.\n\nSECRET-OF-REVIEW stays in review.\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "stage files")
	write(home, map[string]string{"flows/code/review.md": "Review for every depot. In full.\n"})
	for name, runner := range map[string]string{"builders": "herdr", "humans": "manual"} {
		if err := a.st.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: map[string]any{}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for stage, group := range map[string]string{"new": "builders", "review": "humans"} {
		if _, err := a.st.AddRoute(ctx, store.Route{Stage: stage, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	prime := func(id string, asJSON bool) string {
		t.Helper()
		a.json = asJSON
		defer func() { a.json = false }()
		out, err := run(t, a.primeCmd(), "--bead", id)
		if err != nil {
			t.Fatalf("prime %s: %v", id, err)
		}
		return out
	}
	holds := func(out, want string) {
		t.Helper()
		if !strings.Contains(out, want) {
			t.Errorf("prime:\n%s\nwant it to hold:\n%s", out, want)
		}
	}
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "task", Depot: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}

	out := prime(b.ID, false)
	holds(out, `
FLOW default · 3 stages (context: your stage's file below says what you must do)
> new      agents   A builder implements the bead.   <- you are here
  review   people   The diff is read.
  merged   end

STAGE new (.yardr/flows/default/new.md):
  Build it on your branch. Then report.

OUTCOMES:`)
	for _, not := range []string{"PATH", "summary:", "SECRET-OF-REVIEW", "All of it", "humans", "builders"} {
		if strings.Contains(out, not) {
			t.Errorf("prime holds %q:\n%s", not, out)
		}
	}

	if _, err := a.st.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	holds(prime(b.ID, false), `
  new      agents   A builder implements the bead.
> review   people   The diff is read.   <- you are here
  merged   end
PATH  new -> review

STAGE review (.yardr/flows/default/review.md):
  The diff is read. All of it.`)

	if _, err := a.st.Done(ctx, b.ID, "changes", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	holds(prime(b.ID, false), `
> new      agents   A builder implements the bead.   <- you are here
  review   people   The diff is read.
  merged   end
PATH  new -> review -> new (changes)

STAGE new `)

	var v struct {
		Flow *store.FlowMap `json:"flow"`
		Next struct {
			StageFile store.StageFile `json:"stage_file"`
		} `json:"next"`
	}
	if err := json.Unmarshal([]byte(prime(b.ID, true)), &v); err != nil {
		t.Fatal(err)
	}
	want := &store.FlowMap{Name: "default",
		Stages: []store.FlowMapStage{
			{Name: "new", Who: "agents", Summary: "A builder implements the bead.", Current: true},
			{Name: "review", Who: "people", Summary: "The diff is read."},
			{Name: "merged", Who: "end"},
		},
		Path: []store.PathStep{{Stage: "new"}, {Stage: "review"}, {Stage: "new", Back: "changes"}}}
	if !reflect.DeepEqual(v.Flow, want) {
		t.Errorf("prime --json: flow = %+v\nwant %+v", v.Flow, want)
	}
	// The file itself is whole in --json.
	if !strings.HasPrefix(v.Next.StageFile.Text, "summary: ") {
		t.Errorf("prime --json: next.stage_file.text = %q", v.Next.StageFile.Text)
	}

	// A store flow: the lines are read from the home. Its first stage has no
	// file, so nothing below says what to do there.
	api, err := a.st.CreateBead(ctx, store.NewBead{Title: "api", Depot: "api", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	holds(prime(api.ID, false), `
FLOW code · 3 stages (context: it does not say what you must do)
> new       agents   <- you are here
  review    people   Review for every depot.
  shipped   end

OUTCOMES:`)

	// Nothing governs a bead without a depot: no map.
	free, err := a.st.CreateBead(ctx, store.NewBead{Title: "free"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if out := prime(free.ID, false) + prime(free.ID, true); strings.Contains(out, "FLOW") || strings.Contains(out, `"flow"`) {
		t.Errorf("prime of a bead no flow governs draws one:\n%s", out)
	}
}

// A path of more than eight stages keeps its first two and its last five.
func TestFlowMapTextLongPath(t *testing.T) {
	m := &store.FlowMap{Name: "f", Stages: []store.FlowMapStage{{Name: "only", Who: "nobody", Current: true}}}
	for i, s := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		m.Path = append(m.Path, store.PathStep{Stage: s})
		if i == 3 {
			m.Path[i].Back = "changes"
		}
	}
	whole := "PATH  a -> b -> c -> d (changes) -> e -> f -> g -> h\n"
	if got, want := m.Text(false), "FLOW f · 1 stage (context: it does not say what you must do)\n> only   nobody   <- you are here\n"+whole; got != want {
		t.Errorf("eight stages:\n%s\nwant:\n%s", got, want)
	}
	m.Path = append(m.Path, store.PathStep{Stage: "i", Back: store.BackByHand})
	if got, want := m.Text(false), "PATH  a -> b -> … -> e -> f -> g -> h -> i (by hand)\n"; !strings.HasSuffix(got, "\n"+want) {
		t.Errorf("nine stages:\n%s\nwant it to end:\n%s", got, want)
	}
	if len(m.Path) != 9 {
		t.Errorf("drawing the map changed its path: %+v", m.Path)
	}
}

// briefRunner writes the brief of every job it is started with, as the
// herdr runner does, and starts nothing.
type briefRunner struct {
	home   string
	st     store.API
	briefs map[string]string // by bead, the last one
	err    error
}

func (r *briefRunner) Start(_ context.Context, j dispatch.Job) (map[string]any, error) {
	b, err := runner.Brief(j, "a", "", r.home, r.st)
	if err != nil {
		r.err = err
	}
	r.briefs[j.Bead.ID] = b
	return map[string]any{}, nil
}
func (*briefRunner) Alive(context.Context, map[string]any) (bool, error) { return true, nil }
func (*briefRunner) Stop(context.Context, map[string]any) error          { return nil }

// The brief a session starts from holds the map prime draws, directly
// before its stage section: for a bead in its first stage, for one sent
// back (the path says by which outcome) and for a store flow, whose lines
// come from the home. The text is prime's for the same bead. A bead no
// flow governs gets the brief it always got.
func TestBriefCarriesPrimesFlowMap(t *testing.T) {
	a, repo, home, git, write := stageYard(t)
	ctx := context.Background()
	write(repo, map[string]string{
		".yardr/flows/default/new.md":    "summary: A builder implements the bead.\n\nBuild it on your branch. Then report.\n",
		".yardr/flows/default/review.md": "The diff is read. All of it.\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "stage files")
	write(home, map[string]string{"flows/code/review.md": "Review for every depot. In full.\n"})
	if err := a.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "brief", Limit: 5, Config: map[string]any{}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me"); err != nil {
		t.Fatal(err)
	}
	r := &briefRunner{home: home, st: a.st, briefs: map[string]string{}}
	d := &dispatch.Dispatcher{Store: a.st, Runners: map[string]dispatch.Runner{"brief": r}, Backoff: time.Nanosecond}
	// primes is the FLOW block of prime for the bead, up to the blank line
	// that ends it.
	primes := func(id string) string {
		t.Helper()
		out, err := run(t, a.primeCmd(), "--bead", id)
		if err != nil {
			t.Fatalf("prime %s: %v", id, err)
		}
		i := strings.Index(out, "\nFLOW ")
		if i < 0 {
			t.Fatalf("prime %s draws no map:\n%s", id, out)
		}
		block, _, _ := strings.Cut(out[i+1:], "\n\n")
		return block + "\n"
	}
	brief := func(id string) string {
		t.Helper()
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.briefs[id]
	}
	holds := func(brief, want string) {
		t.Helper()
		if !strings.Contains(brief, want) {
			t.Errorf("brief:\n%s\nwant it to hold:\n%s", brief, want)
		}
	}

	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "task", Depot: "web"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	api, err := a.st.CreateBead(ctx, store.NewBead{Title: "api", Depot: "api", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	free, err := a.st.CreateBead(ctx, store.NewBead{Title: "free", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}

	first := brief(b.ID)
	holds(first, "\n## Flow `default`\n\n```text\n"+
		"FLOW default · 3 stages (context: your stage's file below says what you must do)\n"+
		"> new      agents   A builder implements the bead.   <- you are here\n"+
		"  review   nobody   The diff is read.\n"+
		"  merged   end\n"+
		"```\n\n## Stage `new`\n\nBuild it on your branch. Then report.\n\n## Outcomes\n")
	holds(first, "```text\n"+primes(b.ID)+"```\n")
	if strings.Contains(first, "PATH") {
		t.Errorf("the brief of a bead that never moved has a path:\n%s", first)
	}

	// A store flow, in a stage without a file: the heading says so.
	holds(brief(api.ID), "\n## Flow `code`\n\n```text\n"+
		"FLOW code · 3 stages (context: it does not say what you must do)\n"+
		"> new       agents   <- you are here\n"+
		"  review    nobody   Review for every depot.\n"+
		"  shipped   end\n"+
		"```\n\n## Outcomes\n")
	holds(brief(api.ID), "```text\n"+primes(api.ID)+"```\n")

	// Sent back from review: the new session's brief says so.
	if _, err := a.st.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.Done(ctx, b.ID, "changes", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	delete(r.briefs, b.ID)
	back := brief(b.ID)
	holds(back, "  merged   end\nPATH  new -> review -> new (changes)\n```\n\n## Stage `new`\n")
	holds(back, "```text\n"+primes(b.ID)+"```\n")

	// No flow, no map: the brief is the one it was.
	none := brief(free.ID)
	if none == "" || strings.Contains(none, "## Flow") || strings.Contains(none, "FLOW") || strings.Contains(none, "```") {
		t.Errorf("the brief of a bead no flow governs:\n%s", none)
	}
}
