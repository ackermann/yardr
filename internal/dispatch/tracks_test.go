package dispatch_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// contributorFlow is a flow for what comes from outside: a maintainer looks
// in triage, and the source says what became of it.
const contributorFlow = `
triage   -> backlog on accepted | closed on dropped
backlog  -> new
new      -> review on done
review   -> merged on merged | closed on dropped
merged terminal
closed terminal
`

// contributorSays is a table for it, with the starter's line for a merged
// pull request, which a train takes too.
var contributorSays = []string{
	"event=issues action=labeled label=accepted -> accepted",
	"event=issues action=closed -> dropped",
	"event=pull_request action=closed merged=true -> merged: PR merged",
}

const (
	issue23 = "https://github.com/acme/yardr/issues/23"
)

// newTrackingDepot is newGHDepot whose other beads follow contributorFlow,
// with the yard among those who hold its stages, and whose hook carries
// lines.
func newTrackingDepot(t *testing.T, lines ...string) *ghDepot {
	t.Helper()
	r := newGHDepot(t)
	ctx := context.Background()
	flow, err := store.ParseFlow(contributorFlow)
	must(t, err)
	flow.Name = "yard"
	_, err = r.api.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, r.api.SetFlowPointer(ctx, "yard", "", "yard", "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "maintainers", Runner: "manual", Limit: 50, Members: []string{"mckean", "yard"}}, "me"))
	for _, stage := range []string{"triage", "backlog", "new", "review"} {
		_, err = r.api.AddRoute(ctx, store.Route{Stage: stage, Group: "maintainers"}, "me")
		must(t, err)
	}
	_, err = r.api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/nonexistent/github.secret", Says: says(t, lines...)}, "me")
	must(t, err)
	return r
}

// tracker adds a task in stage that tracks the thread at url.
func (r *ghDepot) tracker(t *testing.T, id, stage, url string) {
	t.Helper()
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: id, Title: id, Depot: "yard", Stage: stage,
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: url}}}, "me")
	must(t, err)
}

// deliverAs records fixture name as GitHub delivered it to hook, with its
// text edited first.
func (r *ghDepot) deliverAs(t *testing.T, hook, event, name string, edit func(string) string) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	s := string(body)
	if edit != nil {
		s = edit(s)
	}
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	env, err := hooks.GitHub{}.Normalize(h, []byte(s))
	must(t, err)
	_, _, err = r.own.RecordHook(context.Background(), hook, env.Map(), []byte(s), nil)
	must(t, err)
}

// An event about an issue moves the bead that tracks it, of any type, as an
// event about a train's pull request moves the train: the line's outcome
// through the bead's flow, a hook_rule event on the bead, and the event
// marked seen on it so that a restarted daemon does nothing twice.
func TestEventMovesTheBeadThatTracksItsThread(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	r.tracker(t, "yard-t", "triage", issue23)
	r.deliverAs(t, "gh", "issues", "issues_labeled", nil)
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s after its issue was labeled accepted, want backlog", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog")
	if note := r.lastNote(t, "yard-t"); !strings.Contains(note, issue23) || !strings.Contains(note, "github issues labeled, by mckean") {
		t.Fatalf("note of the move: %q", note)
	}
	if r.bead(t, "yard-t").Metadata["hook_seq"] == nil {
		t.Fatal("the event is not marked seen on yard-t")
	}

	// A daemon that starts again reads the log from the start, and the
	// bead, now in a stage with no edge for accepted, is asked nothing.
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI}
	must(t, fresh.Reconcile(context.Background()))
	wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog")

	// An event the table has no line for moves and logs nothing; one with a
	// line the stage has no edge for is refused, and says so.
	r.deliverAs(t, "gh", "issues", "issues_edited", nil)
	r.deliverAs(t, "gh", "issues", "issues_closed", nil)
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s, want backlog", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "accepted triage -> backlog", "dropped refused: ")

	// A held bead is a human's.
	r.tracker(t, "yard-h", "triage", "https://github.com/acme/yardr/issues/24")
	_, err := r.api.SetHold(context.Background(), "yard-h", true, store.Expect{}, "me")
	must(t, err)
	r.deliverAs(t, "gh", "issues", "issues_labeled", func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "issues/23", "issues/24"), `"number": 23`, `"number": 24`)
	})
	r.pass(t)
	wantRules(t, r.rules(t, "yard-h"), "accepted ignored: bead is held")
}

// A bead with only the old dispatch cursor is judged again: old metadata is
// ignored after the cursor was renamed to hook_seq.
func TestBeadWithOldDispatchSeqIsJudgedAgain(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	r.tracker(t, "yard-t", "triage", issue23)
	r.deliverAs(t, "gh", "issues", "issues_labeled", nil)
	ctx := context.Background()
	evs, err := r.api.Events(ctx, "", 500)
	must(t, err)
	seq := uint64(0)
	for _, e := range evs {
		if e.Kind == "hook" {
			seq = e.Seq
		}
	}
	if seq == 0 {
		t.Fatal("no hook event logged for the delivery")
	}
	// Simulate the metadata a build before the rename left behind.
	if _, err := r.api.UpdateBead(ctx, "yard-t", store.BeadPatch{Metadata: map[string]any{"github_seq": seq}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s, want backlog", st)
	}
	if rules := r.rules(t, "yard-t"); len(rules) == 0 {
		t.Fatal("no hook_rule event recorded after judging the queued delivery")
	}
	if b := r.bead(t, "yard-t"); b.Metadata["hook_seq"] == nil {
		t.Fatalf("hook_seq was not written: %v", b.Metadata)
	}
}

// What the yard did in the source comes back as an event like any other,
// and says nothing: a label a crew member set, under the identity it has
// there (a GitHub App's <slug>[bot]), never moves the yard.
func TestTheYardsOwnActionSaysNothing(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	ctx := context.Background()
	_, err := r.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake"}, "me")
	must(t, err)
	must(t, r.api.SetIdentity(ctx, "yardmaster", "github", store.Identity{Key: "Yardr[bot]"}, "me"))
	// An identity in another source is no one in this one.
	_, err = r.api.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "fake"}, "me")
	must(t, err)
	must(t, r.api.SetIdentity(ctx, "scribe", "linear", store.Identity{Key: "mckean"}, "me"))

	r.tracker(t, "yard-t", "triage", issue23)
	// This event has no matching says line, but the yard's own action is
	// still recorded as ignored rather than silently skipped.
	r.deliverAs(t, "gh", "issues", "issues_edited", func(s string) string {
		return strings.ReplaceAll(s, "jane-doe", "yardr[bot]")
	})
	r.pass(t)
	wantRules(t, r.rules(t, "yard-t"), "issues ignored: the yard's own action")

	r.deliverAs(t, "gh", "issues", "issues_labeled", func(s string) string {
		return strings.Replace(s, `"login": "mckean",
  "type": "User"`, `"login": "yardr[bot]",
  "type": "Bot"`, 1)
	})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "triage" {
		t.Fatalf("yard-t in %s after the yard labeled its issue, want it left in triage", st)
	}
	wantRules(t, r.rules(t, "yard-t"), "issues ignored: the yard's own action", "accepted ignored: the yard's own action")

	// The same label from a person moves it.
	r.deliverAs(t, "gh", "issues", "issues_labeled", func(s string) string { return strings.Replace(s, "Seen on startup.", "Seen.", 1) })
	r.pass(t)
	wantRules(t, r.rules(t, "yard-t"), "issues ignored: the yard's own action", "accepted ignored: the yard's own action", "accepted triage -> backlog")
}

// A pull request no bead tracks is its train's, by its branch, as it always
// was. Once a bead tracks the pull request the link wins: the event is put
// to the table for that bead, and the train of the branch hears nothing. A
// closed bead keeps its link and tracks nothing any more, so the train has
// its pull request back.
func TestTrackingBeadWinsOverTheTrainOfTheBranch(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	r.train(t, "yard-a", "in-pr")
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "merged" {
		t.Fatalf("yard-a in %s after its PR merged, want merged", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "merged in-pr -> merged")

	r.train(t, "yard-b", "in-pr")
	// A PR's issue URL and pull URL name the same GitHub thread; the event
	// uses /pull/17 while this bead was linked with /issues/17.
	r.tracker(t, "yard-p", "review", "https://github.com/acme/yardr/issues/17")
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-b", "")
	r.pass(t)
	if a, b := r.stageOf(t, "yard-p"), r.stageOf(t, "yard-b"); a != "merged" || b != "in-pr" {
		t.Fatalf("yard-p in %s and yard-b in %s, want the tracking bead merged and the train left in in-pr", a, b)
	}
	wantRules(t, r.rules(t, "yard-p"), "merged review -> merged")
	wantRules(t, r.rules(t, "yard-b"))
	if b := r.bead(t, "yard-b"); b.Metadata["hook_seq"] != nil || b.Metadata["pr_head"] != nil {
		t.Fatalf("the train was judged on an event of a tracked pull request: %v", b.Metadata)
	}

	// yard-p is closed now: the next event is the train's again.
	r.deliverEdited(t, "pull_request", "pull_request_closed_merged", "yard-b", "", func(s string) string {
		return strings.Replace(s, "Lands the train.", "Lands it.", 1)
	})
	r.pass(t)
	if st := r.stageOf(t, "yard-b"); st != "merged" {
		t.Fatalf("yard-b in %s once the tracking bead was closed, want merged", st)
	}
}

// A hook that only sets a thread's status has no table, and its deliveries
// about a tracked thread are not logged on the bead one by one.
func TestHookWithoutTableIsSilentOfATrackedBead(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	_, err := r.api.PutHook(context.Background(), store.Hook{Name: "quiet", Source: "github", SecretFile: "/nonexistent/github.secret"}, "me")
	must(t, err)
	r.tracker(t, "yard-t", "triage", issue23)
	r.deliverAs(t, "quiet", "issues", "issues_labeled", nil)
	r.pass(t)
	wantRules(t, r.rules(t, "yard-t"))
	if b := r.bead(t, "yard-t"); b.Stage != "triage" || b.Metadata["hook_seq"] != nil {
		t.Fatalf("yard-t in %s, metadata %v", b.Stage, b.Metadata)
	}
}

// association is a key a line may filter on: whom the table trusts.
func TestSaysFiltersOnAssociation(t *testing.T) {
	r := newTrackingDepot(t, "event=issues action=labeled association=member association=owner -> accepted")
	r.tracker(t, "yard-t", "triage", issue23)
	r.deliverAs(t, "gh", "issues", "issues_labeled", nil) // a first-time contributor's
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "triage" {
		t.Fatalf("yard-t in %s after a stranger's issue was labeled, want triage", st)
	}
	r.deliverAs(t, "gh", "issues", "issues_labeled", func(s string) string {
		return strings.Replace(s, "FIRST_TIME_CONTRIBUTOR", "MEMBER", 1)
	})
	r.pass(t)
	if st := r.stageOf(t, "yard-t"); st != "backlog" {
		t.Fatalf("yard-t in %s after a member's issue was labeled, want backlog", st)
	}
}
