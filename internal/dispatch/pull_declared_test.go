package dispatch_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// forgeDecl declares a source with two link shapes: issues and pull
// requests over the same groups, repositories of owner/name, everything
// folded to lowercase.
const forgeDecl = `name: forge
base: https://forge.test
credential:
  env: FORGE_TOKEN
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Forge-Signature, encoding: hex }
    delivery: { header: Forge-Delivery }
  bodies: repeat
  normalize: [normalize]
  keys: [event, number, state, merged, conclusion, head_sha, head_repo, train, sender]
link:
  host: forge.test
  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'
  fixed: "https://forge.test/{1}/{2}/issues/{3}"
  thread: "{1}/{2}#{3}"
  fold: lower
  pull:
    issue: '^/([^/]+)/([^/]+)/pull/([0-9]+)'
    fixed: "https://forge.test/{1}/{2}/pull/{3}"
    thread: "{1}/{2}#{3}"
  repos: '[^/]+/[^/]+'
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

const (
	forgeIssue = "https://forge.test/acme/webapp/issues/7"
	forgePull  = "https://forge.test/acme/webapp/pull/7"
)

// forgeHook gives the yard the forge source and a forge hook fg carrying
// says lines, beside the github hook the depot came with.
func forgeHook(t *testing.T, r *ghDepot, lines ...string) {
	t.Helper()
	ctx := context.Background()
	_, err := r.api.PutSource(ctx, "forge.yaml", forgeDecl, "me")
	must(t, err)
	_, err = r.api.PutHook(ctx, store.Hook{Name: "fg", Source: "forge",
		SecretFile: "/nonexistent/forge.secret", Says: says(t, lines...)}, "me")
	must(t, err)
}

// forgeTracker adds a task in stage that tracks the thread at url in forge.
func forgeTracker(t *testing.T, r *ghDepot, id, stage, url string) {
	t.Helper()
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: id, Title: id, Depot: "yard", Stage: stage,
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "forge", URL: url}}}, "me")
	must(t, err)
}

// forgeDeliver records a forge delivery: an envelope of type and action
// about the thread at url, from actor coder, with fields. The event field
// is the type, as the github adapter sets it.
func forgeDeliver(t *testing.T, r *ghDepot, typ, action, url string, fields map[string][]string) {
	t.Helper()
	if fields == nil {
		fields = map[string][]string{}
	}
	fields["event"] = []string{typ}
	env := hooks.Envelope{Source: "forge", Type: typ, Action: action,
		Actor:  hooks.Party{ID: "coder"},
		Thread: hooks.Thread{ID: "acme/webapp#7", Title: "The thread", URL: url},
		Fields: fields}
	_, _, err := r.own.RecordHook(context.Background(), "fg", env.Map(), []byte(`{}`), nil)
	must(t, err)
}

// An event about a declared source's pull request moves the bead that
// tracks it: the link made from the pull shape is the thread the event
// is about.
func TestForgePullRequestMovesTheBeadThatTracksIt(t *testing.T) {
	r := newTrackingDepot(t)
	forgeHook(t, r, "event=pull_request action=labeled label=accepted -> accepted")
	forgeTracker(t, r, "yard-t", "triage", forgePull)
	forgeDeliver(t, r, "pull_request", "labeled", forgePull,
		map[string][]string{"repo": {"acme/webapp"}, "number": {"7"}, "label": {"accepted"}})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s after its pull request was labeled accepted, want backlog", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog")
}

// An event in one form finds the bead linked with the other: an issue
// and its pull request share their groups, so each names the other.
func TestForgeEventFindsTheBeadByTheOtherForm(t *testing.T) {
	for _, c := range []struct{ name, tracked, event, typ string }{
		{"issue linked, pull event", forgeIssue, forgePull, "pull_request"},
		{"pull linked, issue event", forgePull, forgeIssue, "issues"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newTrackingDepot(t)
			forgeHook(t, r,
				"event=pull_request action=labeled label=accepted -> accepted",
				"event=issues action=labeled label=accepted -> accepted")
			forgeTracker(t, r, "yard-t", "triage", c.tracked)
			forgeDeliver(t, r, c.typ, "labeled", c.event,
				map[string][]string{"repo": {"acme/webapp"}, "number": {"7"}, "label": {"accepted"}})
			r.pass(t)
			if st := r.stageOf(t, "yard-t"); st != "backlog" {
				t.Fatalf("yard-t in %s, want backlog", st)
			}
			wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog")
		})
	}
}

// A run's delivery names its pull request by repository and number
// alone: the thread URL is built from the pull shape, and the bead
// tracking it is judged on the run.
func TestForgeRunNamesItsPullRequestByNumber(t *testing.T) {
	r := newTrackingDepot(t)
	forgeHook(t, r, "event=workflow_run action=completed conclusion=success -> accepted")
	ctx := context.Background()
	const sha = "9f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	_, err := r.api.CreateBead(ctx, store.NewBead{ID: "yard-t", Title: "yard-t", Depot: "yard", Stage: "triage",
		Metadata: map[string]any{"pr_head": sha},
		Links:    []store.NewLink{{Kind: store.LinkTracks, Source: "forge", URL: forgePull}}}, "me")
	must(t, err)
	forgeDeliver(t, r, "workflow_run", "completed", "",
		map[string][]string{"repo": {"acme/webapp"}, "number": {"7"}, "head_sha": {sha}, "conclusion": {"success"}})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s after a green run on its pull request's head, want backlog", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog")
}

// An issue opened in the depot's repository makes the bead that tracks
// it there: the depot's origin remote reads as a repository of forge's
// through its repos pattern, with the daemon's own git read, no
// stand-in for it.
func TestForgeEventMakesTheBeadInTheDepotOfItsRepository(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	forgeHook(t, r, contributorSays...)
	ctx := context.Background()
	h, err := r.api.GetHook(ctx, "fg")
	must(t, err)
	m, err := store.ParseMake("event=issues action=opened -> issue:triage")
	must(t, err)
	h.Makes = []store.Make{m}
	_, err = r.api.PutHook(ctx, *h, "me")
	must(t, err)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", "https://forge.test/acme/webapp.git")
	must(t, r.api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main", Path: dir}, "me"))
	if r.d.DepotRepo != nil {
		t.Fatal("DepotRepo is stubbed: this test reads the depot's own remote")
	}
	forgeDeliver(t, r, "issues", "opened", forgeIssue,
		map[string][]string{"repo": {"acme/webapp"}, "number": {"7"}})
	r.pass(t)
	b := r.made(t, forgeIssue)
	if b.Type != "issue" || b.Stage != "triage" || b.Depot == nil || *b.Depot != "yard" {
		t.Fatalf("made %s: type %s, stage %s, depot %v", b.ID, b.Type, b.Stage, b.Depot)
	}
}

// A train bound to a declared source's pull request moves on an event
// about it: the binding reads the pull request's repository and number
// from its URL, and the event is on it.
func TestForgeTrainBoundToItsPullRequestMoves(t *testing.T) {
	r := newGHDepot(t)
	forgeHook(t, r, "event=pull_request_review action=submitted state=changes_requested sender=user -> changes")
	ctx := context.Background()
	_, err := r.api.CreateBead(ctx, store.NewBead{ID: "yard-t", Title: "train yard-t", Type: store.TypeTrain,
		Depot: "yard", Stage: "review",
		Metadata: map[string]any{"pr_url": "https://forge.test/acme/yardr/pull/17",
			"pr_number": "17", "pr_head_repo": "acme/yardr"}}, "me")
	must(t, err)
	forgeDeliver(t, r, "pull_request_review", "submitted", "https://forge.test/acme/yardr/pull/17",
		map[string][]string{"repo": {"acme/yardr"}, "number": {"17"}, "head_repo": {"acme/yardr"},
			"head_sha": {fixtureSHA}, "train": {"yard-t"}, "state": {"changes_requested"}, "sender": {"user"}})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "open" {
		t.Fatalf("yard-t in %s after changes were requested on its pull request, want open", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "changes review -> open")
}

// The job of a bead that tracks a declared source's pull request names
// it, as a github one does. A bead that tracks an issue is started as
// ever.
func TestJobNamesTheForgePullRequestTheBeadTracks(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	_, err := s.PutSource(ctx, "forge.yaml", forgeDecl, "me")
	must(t, err)
	must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "fake", Limit: 9}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "reviewers"}, "me")
	must(t, err)
	bead := func(title, url string) string {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title}, "me")
		must(t, err)
		_, err = s.AddLink(ctx, b.ID, store.NewLink{Kind: store.LinkTracks, Source: "forge", URL: url}, "me")
		must(t, err)
		return b.ID
	}
	tracks := bead("theirs", forgePull)
	want := map[string]*dispatch.Pull{
		tracks:                       {URL: forgePull, Number: 7},
		bead("an issue", forgeIssue): nil,
	}
	must(t, d.Reconcile(ctx))
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) != len(want) {
		t.Fatalf("%d jobs started, want %d", len(f.jobs), len(want))
	}
	for _, j := range f.jobs {
		w := want[j.Bead.ID]
		if (j.Pull == nil) != (w == nil) || (w != nil && *j.Pull != *w) {
			t.Errorf("%s (%s): Pull = %+v, want %+v", j.Bead.ID, j.Bead.Title, j.Pull, w)
		}
	}
}
