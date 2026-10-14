package store_test

import (
	"context"
	"encoding/json"
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
}

// A broken template puts no ask and logs one event naming the stage and the
// error; the bead enters as today.
func TestStageAskBrokenTemplate(t *testing.T) {
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

// Input and check templates put their shape: words with the move as the
// single entry, and Done firing the move with the yard's consequence.
func TestStageAskInputCheck(t *testing.T) {
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
