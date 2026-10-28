package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// stageAskFlow is a lifecycle with a templated manual stage: new leads to
// review, which offers done and changes and may stay for another try;
// approved leads to merged. Review's template is the test's to write.
const stageAskFlow = "new -> review\nreview -> review | approved on done | new on changes\napproved -> merged\nmerged terminal\n"

// stageAskYard is a depot governed by stageAskFlow, with the operator jo,
// users ada and mckean, a manual group reviewers working review, and review.md
// holding text.
func stageAskYard(t *testing.T, reviewFile string) (store.API, context.Context) {
	s, _, ctx := stageAskYardOwned(t, reviewFile)
	return s, ctx
}

func stageAskYardOwned(t *testing.T, reviewFile string) (store.API, *store.Store, context.Context) {
	t.Helper()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":      stageAskFlow,
		".yardr/flows/default/review.md": reviewFile,
	})
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "mckean"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return s, own, ctx
}

const stageAskChoice = `Review the diff.

## Ask
kind: choice
question: Where should this bead go next?
context:
- The builder asked for a decision; the question is in their note.
- The branch is ready to review either way.
recommend: 1
why: The question is small and the branch is green.
done: The bead moves to approved with your words as the note.
changes: The bead goes back for changes.
`

// stageAsks are the bead's asks oldest first, with their states.
func stageAsks(t *testing.T, s store.API, id string) []store.ResolvedAsk {
	t.Helper()
	notes, err := s.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return store.ResolveAsks(notes, time.Now())
}

// Entering a templated manual stage puts one ask per user-member, authored
// yard, with the stage, the move per option and the entry frozen.
func TestStageAskEntry(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 2 {
		t.Fatalf("asks = %d, want one per member", len(got))
	}
	for i, r := range got {
		if r.Note.Author != "yard" || r.State != store.AskOpen {
			t.Fatalf("ask %d: author %s state %s", i, r.Note.Author, r.State)
		}
		a := r.Ask
		if a.Stage != "review" || a.Entry == 0 {
			t.Fatalf("ask %d: stage %q entry %d", i, a.Stage, a.Entry)
		}
		if len(a.Move) != 3 || a.Move[0] != "done" || a.Move[1] != "changes" || a.Move[2] != "" {
			t.Fatalf("ask %d: move %q", i, a.Move)
		}
		if len(a.Options) != 3 || a.Options[0].Label != "done" || a.Options[2].Label != store.YouDecide {
			t.Fatalf("ask %d: options %+v", i, a.Options)
		}
		if a.Recommend != 1 || a.Why == "" {
			t.Fatalf("ask %d: recommend %d why %q", i, a.Recommend, a.Why)
		}
	}
	if got[0].Ask.To != "ada" || got[1].Ask.To != "mckean" {
		t.Fatalf("to = %s, %s", got[0].Ask.To, got[1].Ask.To)
	}
	if got[0].Ask.Entry != got[1].Ask.Entry {
		t.Fatalf("entries differ: %d, %d", got[0].Ask.Entry, got[1].Ask.Entry)
	}
	// The entry is the advanced event that entered the bead.
	evs, err := s.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var advanced uint64
	for _, e := range evs {
		if e.Kind == "advanced" {
			if to, _ := e.Data["to"].(string); to == "review" {
				advanced = e.Seq
			}
		}
	}
	if advanced == 0 || got[0].Ask.Entry != advanced {
		t.Fatalf("entry %d, advanced %d", got[0].Ask.Entry, advanced)
	}
	// Caller-sent stage-ask metadata is cleared: asking by hand puts no
	// stage ask and cannot claim fallback authorization.
	seq, err := s.AddAsk(ctx, b.ID, store.Ask{
		Question: "Plain?", To: "ada", Context: []string{"why"},
		Stage: "review", Move: []string{"done"}, Entry: 99, Fallback: true,
	}, "me")
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := s.Notes(ctx, b.ID)
	for _, n := range notes {
		if n.Seq == seq {
			a, _ := store.ParseAsk(n)
			if a.Stage != "" || len(a.Move) != 0 || a.Entry != 0 || a.Fallback {
				t.Fatalf("caller-sent stage ask kept: %+v", a)
			}
		}
	}
}

// Creating a bead directly in a templated stage puts its ask with the
// created event as Entry.
func TestStageAskCreateBeadEntry(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 2 || got[0].Ask.Stage != "review" || got[0].Ask.Entry == 0 {
		t.Fatalf("created stage asks = %+v", got)
	}
	evs, err := s.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var created uint64
	for _, e := range evs {
		if e.Kind == "created" {
			created = e.Seq
		}
	}
	if got[0].Ask.Entry != created || got[1].Ask.Entry != created {
		t.Fatalf("ask entries = %d, %d; created = %d", got[0].Ask.Entry, got[1].Ask.Entry, created)
	}
}

// Store-flow stage files live in the yard's home. Both advancing into one
// and creating a bead directly in it put the stage ask.
func TestStageAskStoreFlow(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "flows", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "flows", "work", "review.md"), []byte(stageAskChoice), 0o644); err != nil {
		t.Fatal(err)
	}
	s := storetest.Open(t, filepath.Join(home, "yard.db"))
	ctx := context.Background()
	dir := gitRepo(t, map[string]string{".yardr/flows/default.flow": stageAskFlow})
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow(stageAskFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "work"
	if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "web", "", "work", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo"); err != nil {
		t.Fatal(err)
	}

	for _, start := range []string{"new", "review"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: start}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if start == "new" {
			b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me")
			if err != nil {
				t.Fatal(err)
			}
		}
		got := stageAsks(t, s, b.ID)
		if len(got) != 1 || got[0].Ask.To != "ada" || got[0].Ask.Stage != "review" || got[0].Note.Author != "yard" {
			t.Fatalf("store-flow ask from %s: %+v", start, got)
		}
	}
}

// A hand move out takes the stage's open and put-aside asks back, with the
// move named; answered and declined asks stay as history.
func TestStageAskHandMove(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	if _, err := s.AddUser(ctx, "grace", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "mckean", "jo", "grace"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 4 {
		t.Fatalf("asks = %d", len(got))
	}
	// You decide: a moving pick would move the bead in its write, and this
	// is the hand move's test.
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 3, Words: "go"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[1].Note.Seq, Decline: true, Words: "not mine"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[2].Note.Seq, Later: true}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Grace's stays open.
	if b, err = s.Advance(ctx, b.ID, "approved", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if len(got) != 4 {
		t.Fatalf("asks after the move = %d", len(got))
	}
	if got[0].State != store.AskAnswered || got[1].State != store.AskDeclined {
		t.Fatalf("states = %s, %s, %s, %s", got[0].State, got[1].State, got[2].State, got[3].State)
	}
	if got[2].State != store.AskTakenBack || got[3].State != store.AskTakenBack {
		t.Fatalf("aside and open were not taken back: %s, %s", got[2].State, got[3].State)
	}
	lines := strings.Join(got[3].Lines(), "\n")
	if !strings.Contains(lines, "taken back by yard: moved to approved by ada") {
		t.Fatalf("take-back reads:\n%s", lines)
	}
}

// Leaving and coming back asks fresh; the old answered ask stays as history
// with its earlier entry.
func TestStageAskReentry(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	first := stageAsks(t, s, b.ID)
	// You decide: a moving pick would move the bead in its write, and this
	// is the re-entry's test.
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 4 {
		t.Fatalf("asks = %d, want the old two and a fresh two", len(got))
	}
	if got[0].State != store.AskAnswered || got[1].State != store.AskTakenBack {
		t.Fatalf("old states = %s, %s", got[0].State, got[1].State)
	}
	if got[2].State != store.AskOpen || got[3].State != store.AskOpen {
		t.Fatalf("fresh states = %s, %s", got[2].State, got[3].State)
	}
	if got[2].Ask.Entry == got[0].Ask.Entry {
		t.Fatalf("fresh entry %d equals the old %d", got[2].Ask.Entry, got[0].Ask.Entry)
	}
}

// When no member got an ask, the ask goes to the operator: an empty list,
// and crew only.
func TestStageAskOperatorFallback(t *testing.T) {
	t.Parallel()
	for _, members := range [][]string{nil, {}} {
		s, ctx := stageAskYard(t, stageAskChoice)
		if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: members}, "jo"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		got := stageAsks(t, s, b.ID)
		if len(got) != 1 || got[0].Ask.To != "jo" || got[0].Note.Author != "yard" {
			t.Fatalf("members %v: asks %+v", members, got)
		}
	}
	// Crew only: a crew member has no decisions queue and is skipped.
	s, ctx := stageAskYard(t, stageAskChoice)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"clerk"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 1 || got[0].Ask.To != "jo" {
		t.Fatalf("crew only: asks %+v", got)
	}
}

// A member naming another yard gets one ask addressed across, queued as the
// yard's own traffic; an unreachable link logs one event and falls back to
// the operator.
func TestStageAskRemote(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":      stageAskFlow,
		".yardr/flows/default/review.md": stageAskChoice,
	})
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	_ = own
	if err := api.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"ask"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"mckean@north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = api.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, api, b.ID)
	if len(got) != 1 || got[0].Ask.To != "mckean@north" || got[0].Ask.Stage != "review" {
		t.Fatalf("remote put: %+v", got)
	}
	out, err := api.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 || out[0].Kind != "ask" {
		t.Fatalf("outbox = %+v, %v", out, err)
	}
	// Unreachable: no such link. One event names the bead, the stage and
	// the member, and the ask falls back to the operator.
	if err := api.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"x@nobody"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b2, err := api.CreateBead(ctx, store.NewBead{Title: "y", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b2, err = api.Advance(ctx, b2.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, api, b2.ID)
	if len(got) != 1 || got[0].Ask.To != "jo" {
		t.Fatalf("unreachable fallback: %+v", got)
	}
	evs, err := api.Events(ctx, b2.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var unreachable []store.Event
	for _, e := range evs {
		if e.Kind == "ask_unreachable" {
			unreachable = append(unreachable, e)
		}
	}
	if len(unreachable) != 1 || unreachable[0].Actor != "yard" {
		t.Fatalf("unreachable events = %+v", unreachable)
	}
	if unreachable[0].Data["stage"] != "review" || unreachable[0].Data["member"] != "x@nobody" {
		t.Fatalf("unreachable data = %+v", unreachable[0].Data)
	}
	// Unreachable: the link carries no ask out.
	if _, err := api.AllowPeer(ctx, "north", store.PeerAllow{NoSend: []string{"ask"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"mckean@north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b3, err := api.CreateBead(ctx, store.NewBead{Title: "z", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b3, err = api.Advance(ctx, b3.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, api, b3.ID)
	if len(got) != 1 || got[0].Ask.To != "jo" {
		t.Fatalf("no-carry fallback: %+v", got)
	}
}

// A local member's stage ask follows that user's configured peer reach, and
// taking the ask back on a hand move settles the copy.
func TestStageAskReachCopy(t *testing.T) {
	t.Parallel()
	s, own, ctx := stageAskYardOwned(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	says := store.Says("v1")
	says.Roster = []store.PeerRosterEntry{{Name: "ada", Kind: store.RosterUser, Mail: true}}
	if err := own.PeerHeard(ctx, "north", yardkey.Public(k), says); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", store.PeerAllow{
		Send: []string{"ask"}, Trust: map[string]string{"ada": "ada"}, Reach: map[string]string{"ada": "ada"},
	}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 1 || got[0].Ask.To != "ada" || got[0].Note.Author != "yard" {
		t.Fatalf("local stage asks = %+v", got)
	}
	out, err := s.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 || out[0].Kind != "ask" || out[0].Re != "" {
		t.Fatalf("reach outbox = %+v, %v", out, err)
	}
	var sent struct {
		To   string    `json:"to"`
		From string    `json:"from"`
		Ask  store.Ask `json:"ask"`
	}
	if err := json.Unmarshal(out[0].Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.To != "ada" || sent.From != "yard" || sent.Ask.To != "ada@north" || sent.Ask.Stage != "review" || sent.Ask.Entry != got[0].Ask.Entry {
		t.Fatalf("reach ask = %+v", sent)
	}
	if _, err := s.Advance(ctx, b.ID, "approved", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if len(got) != 1 || got[0].State != store.AskTakenBack {
		t.Fatalf("ask after hand move = %+v", got)
	}
	out, err = s.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 2 || out[1].Re != out[0].ID {
		t.Fatalf("settled reach outbox = %+v, %v", out, err)
	}
	var settled struct {
		Settled string `json:"settled"`
	}
	if err := json.Unmarshal(out[1].Body, &settled); err != nil || settled.Settled != "taken_back" {
		t.Fatalf("reach settle = %+v, %v", settled, err)
	}
}

// A same-stage transition is a fresh stay: the old stage ask is taken back
// and a fresh ask is put with a new entry.
func TestStageAskSameStage(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	first := stageAsks(t, s, b.ID)
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 4 {
		t.Fatalf("asks = %d", len(got))
	}
	if got[0].State != store.AskTakenBack || got[1].State != store.AskTakenBack {
		t.Fatalf("old states = %s, %s", got[0].State, got[1].State)
	}
	if got[2].State != store.AskOpen || got[2].Ask.Entry == first[0].Ask.Entry {
		t.Fatalf("fresh ask = %+v", got[2].Ask)
	}
	lines := strings.Join(got[0].Lines(), "\n")
	if !strings.Contains(lines, "taken back by yard: moved to review by ada") {
		t.Fatalf("same-stage take-back reads:\n%s", lines)
	}
}

// Close takes back open stage asks with closed as the reason; reopening
// into the templated stage asks fresh.
func TestStageAskCloseReopen(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	first := stageAsks(t, s, b.ID)
	if b, err = s.CloseBead(ctx, b.ID, "stale", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 2 || got[0].State != store.AskTakenBack {
		t.Fatalf("after close: %+v", got)
	}
	lines := strings.Join(got[0].Lines(), "\n")
	if !strings.Contains(lines, "taken back by yard: closed") {
		t.Fatalf("close take-back reads:\n%s", lines)
	}
	if b, err = s.Reopen(ctx, b.ID, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if len(got) != 4 || got[2].State != store.AskOpen || got[2].Ask.Entry == first[0].Ask.Entry {
		t.Fatalf("after reopen: %+v", got)
	}

	// Taking a part out enters its stage just as Reopen does. The fresh ask
	// names the reopened event, before the reason note that Unfold adds.
	other, err := s.CreateBead(ctx, store.NewBead{Title: "other", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if other, err = s.Advance(ctx, other.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	carrier, err := s.FoldBeads(ctx, []string{b.ID, other.ID}, store.FoldOptions{To: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unfold(ctx, carrier.ID, b.ID, "review", "split it out", "me"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	evs, err := s.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var reopened uint64
	for _, e := range evs {
		if e.Kind == "reopened" {
			reopened = max(reopened, e.Seq)
		}
	}
	open := 0
	for _, r := range got {
		if r.State != store.AskOpen {
			continue
		}
		open++
		if r.Ask.Stage != "review" || r.Ask.Entry != reopened {
			t.Errorf("unfold ask stage %q entry %d, want review entry %d", r.Ask.Stage, r.Ask.Entry, reopened)
		}
	}
	if open != 2 {
		t.Errorf("open asks after unfold = %d, want one for each reviewer; asks: %+v", open, got)
	}
}

// A broken template puts no ask and logs one event naming the stage and the
// error; the bead enters as today.
func TestStageAskBrokenTemplate(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, "Review.\n\n## Ask\nkind: choce\n")
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if b.Stage != "review" {
		t.Fatalf("broken template refused entry: %+v", b)
	}
	notes, err := s.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("broken template put notes: %+v", notes)
	}
	evs, err := s.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var bad []store.Event
	for _, e := range evs {
		if e.Kind == "bad_template" {
			bad = append(bad, e)
		}
	}
	if len(bad) != 1 || bad[0].Actor != "yard" || bad[0].Data["stage"] != "review" {
		t.Fatalf("bad_template events = %+v", bad)
	}
	if reason, _ := bad[0].Data["reason"].(string); !strings.Contains(reason, "ask line 4") {
		t.Fatalf("bad_template reason = %q", reason)
	}
}

// NextStages carries the current stay's bad_template reason, where the ask
// would stand: set on a broken entry, empty on a good one, and history
// once a later stay puts its asks.
func TestNextStagesTemplateError(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t, map[string]string{
		".yardr/flows/default.flow":      stageAskFlow,
		".yardr/flows/default/review.md": stageAskChoice,
	})
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "mckean"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextStages(ctx, b.ID); err != nil {
		t.Fatal(err)
	} else if next.TemplateError != "" {
		t.Fatalf("good entry template error = %q", next.TemplateError)
	}
	commitFiles(t, dir, map[string]string{".yardr/flows/default/review.md": "Review.\n\n## Ask\nkind: chek\n"})
	if b, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextStages(ctx, b.ID); err != nil {
		t.Fatal(err)
	} else if next.TemplateError != "" {
		t.Fatalf("outside the stage template error = %q", next.TemplateError)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	next, err := s.NextStages(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.TemplateError, "ask line 4") {
		t.Fatalf("broken entry template error = %q", next.TemplateError)
	}
	// Fixed and re-entered, the old event is history: a fresh ask stands
	// and no error does.
	commitFiles(t, dir, map[string]string{".yardr/flows/default/review.md": stageAskChoice})
	if b, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextStages(ctx, b.ID); err != nil {
		t.Fatal(err)
	} else if next.TemplateError != "" {
		t.Fatalf("fixed re-entry template error = %q", next.TemplateError)
	}
	if got := stageAsks(t, s, b.ID); len(got) != 4 {
		t.Fatalf("asks after fixed re-entry = %d, want the old two and a fresh two", len(got))
	}
}

// Input and check templates put their shape: words with the move as the
// single entry, and Done firing the move with the yard's consequence.
func TestStageAskInputCheck(t *testing.T) {
	t.Parallel()
	input := "Decide.\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The bead waits in decide.\n"
	// Review's done resolves; input's default move is done.
	s, ctx := stageAskYard(t, input)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 2 || len(got[0].Ask.Options) != 0 {
		t.Fatalf("input asks = %+v", got)
	}
	if !slices.Equal(got[0].Ask.Move, []string{"done"}) {
		t.Fatalf("input move = %q", got[0].Ask.Move)
	}
	check := "Check it.\n\n## Ask\nkind: check\nquestion: Is it done?\ncontext:\n- The release waits on this.\n"
	s, ctx = stageAskYard(t, check)
	b, err = s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if len(got) != 2 || len(got[0].Ask.Options) != 2 || got[0].Ask.Options[0].Label != "Done" {
		t.Fatalf("check asks = %+v", got)
	}
	if got[0].Ask.Options[0].Consequence != "the bead leaves review (outcome done)" {
		t.Fatalf("check consequence = %q", got[0].Ask.Options[0].Consequence)
	}
	if !slices.Equal(got[0].Ask.Move, []string{"done", ""}) {
		t.Fatalf("check move = %q", got[0].Ask.Move)
	}
}

// stageMovesFlow is the shape of the design pack's flow: critique's done
// leads to decide, which the flow leaves by bare edges, to approved, back to
// draft or to backlog, none of them an outcome. Critique's changes may go back
// to draft twice.
const stageMovesFlow = "backlog -> draft\n" +
	"draft -> critique on done | backlog\n" +
	"critique -> decide on done | draft on changes max 2 then decide\n" +
	"decide -> approved | draft | backlog\n" +
	"approved -> merged\n" +
	"merged terminal\n"

// stageMovesFile is a stage file whose ask template offers the stage's moves.
const stageMovesFile = `A bead here waits for a person.

## Ask
kind: moves
question: Where should this bead go next?
context:
- The bead waits for a person; the options are the moves of this stage.
`

// movesYard is a depot on stageMovesFlow, with the repository its flow and
// stage files are read from.
type movesYard struct {
	api  store.API
	own  *store.Store
	repo string
	ctx  context.Context
}

// stageMovesYard is a depot on stageMovesFlow with the operator jo and the
// users ada, bob and hal. Each stage in who is worked by a manual group of
// the members it lists, and the stages in files have the stage file given.
func stageMovesYard(t *testing.T, files map[string]string, who map[string][]string) movesYard {
	t.Helper()
	repoFiles := map[string]string{".yardr/flows/default.flow": stageMovesFlow}
	for stage, text := range files {
		repoFiles[".yardr/flows/default/"+stage+".md"] = text
	}
	dir := gitRepo(t, repoFiles)
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"ada", "bob", "hal"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for stage, members := range who {
		g := stage + "-people"
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "manual", Members: members}, "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddRoute(ctx, store.Route{Stage: stage, Group: g}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return movesYard{s, own, dir, ctx}
}

// stage reads where the bead is.
func (y movesYard) stage(t *testing.T, id string) string {
	t.Helper()
	b, err := y.api.GetBead(y.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Stage
}

// toDecide takes the bead to decide as a design goes when its critic is
// satisfied: into critique, and out of it with done.
func (y movesYard) toDecide(t *testing.T, id string) {
	t.Helper()
	if _, err := y.api.Advance(y.ctx, id, "critique", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.Done(y.ctx, id, "done", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
}

// cardOf reads the options of a stage ask as (label, move, consequence), the
// last of them the You decide the yard adds, which fires nothing.
func cardOf(a store.Ask) [][3]string {
	var out [][3]string
	for i, o := range a.Options {
		out = append(out, [3]string{o.Label, a.Move[i], o.Consequence})
	}
	return out
}

// A moves template offers the stage's moves as the flow has them, never a
// list the stage file keeps: critique names outcomes, so it offers them, with
// the flow's own limit; decide has bare edges, so it offers the stages, and
// picking one advances there. The design flow's case: from critique's done into
// decide, the card offers approved, draft and backlog.
func TestStageAskMoves(t *testing.T) {
	t.Parallel()
	y := stageMovesYard(t, map[string]string{"critique": stageMovesFile, "decide": stageMovesFile},
		map[string][]string{"critique": {"ada"}, "decide": {"ada"}})
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "a design", Depot: "web", Stage: "draft"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.Advance(y.ctx, b.ID, "critique", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, y.api, b.ID)
	if len(got) != 1 || got[0].State != store.AskOpen || got[0].Note.Author != "yard" {
		t.Fatalf("asks in critique = %+v, want the yard's one for ada", got)
	}
	card := got[0].Ask
	if card.Stage != "critique" || card.Entry == 0 || !card.Moves || card.To != "ada" || card.Question != "Where should this bead go next?" {
		t.Fatalf("the critique card = %+v", card)
	}
	want := [][3]string{
		{"done", "done", "The bead moves to decide."},
		{"changes", "changes", "The bead goes back to draft, with your words as the note. Round 1 of 2. The flow allows 2 rounds back to draft; after that it goes to decide instead."},
		{store.YouDecide, "", "leave it to yard"},
	}
	if diff := cardOf(card); !slices.Equal(diff, want) {
		t.Fatalf("critique offers\n%q\nwant\n%q", diff, want)
	}

	// Picking done is the outcome's move, and the bead's arrival in decide
	// puts the card of its bare edges.
	if err := y.api.AddAnswer(y.ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada"); err != nil {
		t.Fatal(err)
	}
	if y.stage(t, b.ID) != "decide" {
		t.Fatalf("after done the bead is in %s, want decide", y.stage(t, b.ID))
	}
	got = stageAsks(t, y.api, b.ID)
	if len(got) != 2 || got[0].State != store.AskAnswered || got[1].State != store.AskOpen {
		t.Fatalf("asks after done = %+v, want critique's answered and decide's open", got)
	}
	card = got[1].Ask
	if card.Stage != "decide" || !card.Moves || card.To != "ada" || card.Entry <= got[0].Ask.Entry {
		t.Fatalf("the decide card = %+v", card)
	}
	want = [][3]string{
		{"approved", "->approved", "The bead moves to approved."},
		{"draft", "->draft", "The bead goes back to draft, with your words as the note."},
		{"backlog", "->backlog", "The bead goes back to backlog, with your words as the note."},
		{store.YouDecide, "", "leave it to yard"},
	}
	if diff := cardOf(card); !slices.Equal(diff, want) {
		t.Fatalf("decide offers\n%q\nwant\n%q", diff, want)
	}

	// A later arrival can use the limit's redirect. Its card names where the
	// outcome goes now, not only the edge it used before the limit was spent.
	limited, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "a limited design", Depot: "web", Stage: "draft"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		if _, err := y.api.Advance(y.ctx, limited.ID, "critique", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		tasks := stageAsks(t, y.api, limited.ID)
		if len(tasks) != round {
			t.Fatalf("round %d has %d stage asks, want %d", round, len(tasks), round)
		}
		if round == 3 {
			consequence := tasks[len(tasks)-1].Ask.Options[1].Consequence
			if consequence != "The bead moves to decide. The 2 rounds back to draft are used." {
				t.Fatalf("the exhausted outcome says %q, want its current move to decide", consequence)
			}
		}
		if err := y.api.AddAnswer(y.ctx, limited.ID, store.Answer{Ask: tasks[len(tasks)-1].Note.Seq, Option: 2}, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	if got := y.stage(t, limited.ID); got != "decide" {
		t.Fatalf("after the exhausted changes outcome the bead is in %s, want decide", got)
	}
}

// A card of moves is one per arrival, for the stage's people, and moves the
// bead only on a member's word: the pick of draft sends it back, as a person
// at a terminal would with yardr bead advance, and a second arrival is a
// second card, with none doubled.
func TestStageAskMovesArrivals(t *testing.T) {
	t.Parallel()
	y := stageMovesYard(t, map[string]string{"decide": stageMovesFile}, map[string][]string{"decide": {"ada"}})
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "a design", Depot: "web", Stage: "draft"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	open := func() []store.ResolvedAsk {
		t.Helper()
		var out []store.ResolvedAsk
		for _, r := range stageAsks(t, y.api, b.ID) {
			if r.State == store.AskOpen {
				out = append(out, r)
			}
		}
		return out
	}
	y.toDecide(t, b.ID)
	first := open()
	if len(first) != 1 || first[0].Ask.To != "ada" {
		t.Fatalf("after the first arrival: open asks %+v, want one, for ada", first)
	}

	// Someone outside the group moves nothing: the answer is refused whole.
	before := stageAnswerNotes(t, y.api, b.ID)
	if err := y.api.AddAnswer(y.ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 2}, "bob"); err == nil ||
		!strings.Contains(err.Error(), "only members of group decide-people may move it out of stage decide") {
		t.Fatalf("draft by someone outside the group: %v", err)
	}
	if after := stageAnswerNotes(t, y.api, b.ID); len(after) != len(before) || y.stage(t, b.ID) != "decide" || len(open()) != 1 {
		t.Fatalf("a refused answer left notes %d (was %d), the bead in %s, %d open asks", len(after), len(before), y.stage(t, b.ID), len(open()))
	}

	// A member's draft sends it back, in the answer's own write: the advance
	// reports no outcome, and the card is decided, no longer standing.
	if err := y.api.AddAnswer(y.ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 2, Words: "not there yet"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if y.stage(t, b.ID) != "draft" {
		t.Fatalf("after the pick of draft the bead is in %s", y.stage(t, b.ID))
	}
	if outcome, ok := stageAnswerAdvanced(t, y.api, b.ID, "draft"); !ok || outcome != "" {
		t.Fatalf("advanced into draft: outcome %q, found %v; want a move that reports none", outcome, ok)
	}
	if got := stageAsks(t, y.api, b.ID); len(got) != 1 || got[0].State != store.AskAnswered || len(open()) != 0 {
		t.Fatalf("after the pick: asks %+v, open %d; want the one, answered", got, len(open()))
	}
	if lines := strings.Join(stageAsks(t, y.api, b.ID)[0].Lines(), "\n"); !strings.Contains(lines, "ada picked 2 (draft): not there yet") {
		t.Fatalf("the card reads:\n%s", lines)
	}

	// Back in decide it is a second arrival, and a second card: the first
	// stays decided, and the one standing is the new one.
	y.toDecide(t, b.ID)
	second := open()
	all := stageAsks(t, y.api, b.ID)
	if len(all) != 2 || len(second) != 1 || second[0].Note.Seq == first[0].Note.Seq ||
		second[0].Ask.Entry <= first[0].Ask.Entry || all[0].State != store.AskAnswered {
		t.Fatalf("after the second arrival: asks %+v, open %+v; want a second card, the first still answered", all, second)
	}
	// The old card takes no pick once the bead has been away.
	if err := y.api.AddAnswer(y.ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 1}, "ada"); err == nil {
		t.Fatalf("a pick on the first arrival's card was taken in the second stay")
	}
}

// The yard's card of moves is for a bead nobody has asked about: where a
// hand ask stands, whether it is open or put aside, no card is put beside it,
// and one that stands gives way to an ask put later, which no other stage
// ask does. A withdrawn card is not put back when the ask is answered: the
// move is the asker's, as it was.
func TestStageAskMovesYieldToAHandAsk(t *testing.T) {
	t.Parallel()
	y := stageMovesYard(t, map[string]string{"decide": stageMovesFile}, map[string][]string{"decide": {"ada"}})
	ask := store.Ask{
		Question: "Hidden or deleted?", To: "ada", Context: []string{"The reviewer could not tell."},
		Options: []store.AskOption{{Label: "Hidden", Consequence: "the docs say hidden"}, {Label: "Deleted", Consequence: "the docs say deleted"}},
	}
	states := func(id string) []string {
		t.Helper()
		var out []string
		for _, r := range stageAsks(t, y.api, id) {
			out = append(out, r.Ask.Question+": "+r.State.String())
		}
		return out
	}
	newBead := func(title string) string {
		t.Helper()
		b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: title, Depot: "web", Stage: "draft"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}

	// An ask open when the bead arrives: no card, and none when it is answered.
	id := newBead("asked first")
	seq, err := y.api.AddAsk(y.ctx, id, ask, "jo")
	if err != nil {
		t.Fatal(err)
	}
	y.toDecide(t, id)
	if got, want := states(id), []string{"Hidden or deleted?: open"}; !slices.Equal(got, want) {
		t.Fatalf("an ask open on arrival: %q, want only %q", got, want)
	}
	if err := y.api.AddAnswer(y.ctx, id, store.Answer{Ask: seq, Option: 1}, "ada"); err != nil {
		t.Fatal(err)
	}
	if got, want := states(id), []string{"Hidden or deleted?: answered"}; !slices.Equal(got, want) {
		t.Fatalf("the ask answered: %q, want %q: the card is not put back", got, want)
	}

	// An ask put aside still stands, and returns: no card beside it either.
	id = newBead("put aside")
	seq, err = y.api.AddAsk(y.ctx, id, ask, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.api.AddAnswer(y.ctx, id, store.Answer{Ask: seq, Later: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	y.toDecide(t, id)
	if got, want := states(id), []string{"Hidden or deleted?: put aside"}; !slices.Equal(got, want) {
		t.Fatalf("an ask put aside on arrival: %q, want only %q", got, want)
	}

	// An ask put after the card takes the card back, naming the ask.
	id = newBead("asked after")
	y.toDecide(t, id)
	if got := states(id); len(got) != 1 || !strings.HasSuffix(got[0], ": open") {
		t.Fatalf("on arrival: %q, want the card", got)
	}
	seq, err = y.api.AddAsk(y.ctx, id, ask, "jo")
	if err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, y.api, id)
	if len(got) != 2 || got[0].State != store.AskTakenBack || got[1].Ask.Question != "Hidden or deleted?" || got[1].State != store.AskOpen {
		t.Fatalf("after the ask: %+v, want the card taken back and the ask open", got)
	}
	if lines := strings.Join(got[0].Lines(), "\n"); !strings.Contains(lines, fmt.Sprintf("taken back by yard: ask %d is on the bead now", seq)) {
		t.Fatalf("the card reads:\n%s", lines)
	}

	// A decision made straight into decide with its question (yardr bead ask
	// --new --stage decide) is the same: the bead is made in the stage, which
	// puts its card, and the question comes in the same write and takes it
	// back, leaving the question the one standing.
	made, err := y.api.AskNew(y.ctx, store.NewBead{Title: "Hidden or deleted?", Depot: "web", Stage: "decide"}, ask, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := states(made.Bead.ID), []string{"Where should this bead go next?: taken back", "Hidden or deleted?: open"}; !slices.Equal(got, want) {
		t.Fatalf("a question asked into decide: %q, want %q", got, want)
	}

	// Another stage's card is the stage file's own making and stays: only the
	// yard's card of moves gives way. Review's choice card stands beside a
	// hand ask.
	s, ctx := stageAskYard(t, stageAskChoice)
	r, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, r.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAsk(ctx, r.ID, ask, "jo"); err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, a := range stageAsks(t, s, r.ID) {
		if a.State == store.AskOpen && a.Ask.Stage == "review" {
			open++
		}
	}
	if open != 2 {
		t.Fatalf("review's choice cards open beside a hand ask = %d, want both members'", open)
	}
}
