package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

const stageAnswerInput = `Decide the words.

## Ask
kind: input
question: What was decided?
context:
- The bead waits in review.
`

const stageAnswerInputNone = `Decide the words.

## Ask
kind: input
question: What was decided?
context:
- The bead waits in review.
move: none
`

const stageAnswerCheck = `Check it.

## Ask
kind: check
question: Is it done?
context:
- The release waits on this.
`

// stageAnswerAdvanced is the bead's advanced event into to, with its outcome.
func stageAnswerAdvanced(t *testing.T, s store.API, id, to string) (string, bool) {
	t.Helper()
	evs, err := s.Events(context.Background(), id, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "advanced" {
			if got, _ := e.Data["to"].(string); got == to {
				outcome, _ := e.Data["outcome"].(string)
				return outcome, true
			}
		}
	}
	return "", false
}

func stageAnswerNotes(t *testing.T, s store.API, id string) []store.Note {
	t.Helper()
	notes, err := s.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

// A choice pick moves the bead in the answer's write, with the outcome on
// the advanced event and no separate note; the other member's ask is taken
// back with the move named.
func TestStageAnswerChoiceMoves(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	before := stageAnswerNotes(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1, Words: "ship it"}, "ada"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetBead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Stage != "approved" {
		t.Fatalf("after the pick the bead is in %s, want approved", cur.Stage)
	}
	outcome, ok := stageAnswerAdvanced(t, s, b.ID, "approved")
	if !ok || outcome != "done" {
		t.Fatalf("advanced into approved: outcome %q, found %v", outcome, ok)
	}
	after := stageAnswerNotes(t, s, b.ID)
	if len(after) != len(before)+2 {
		t.Fatalf("notes = %d, want %d: the answer and the take-back, no separate note", len(after), len(before)+2)
	}
	if after[len(after)-2].Kind != store.NoteAnswer || after[len(after)-1].Kind != store.NoteAnswer {
		t.Fatalf("added notes are not the answer and the take-back: %+v", after[len(after)-2:])
	}
	got = stageAsks(t, s, b.ID)
	if got[0].State != store.AskAnswered || got[1].State != store.AskTakenBack {
		t.Fatalf("states = %s, %s", got[0].State, got[1].State)
	}
	lines := strings.Join(got[1].Lines(), "\n")
	if !strings.Contains(lines, "taken back by yard: moved to approved by ada") {
		t.Fatalf("loser's ask reads:\n%s", lines)
	}
	if lines := strings.Join(got[0].Lines(), "\n"); !strings.Contains(lines, "ada picked 1 (done): ship it") {
		t.Fatalf("winner's ask reads:\n%s", lines)
	}
}

// Words on an input ask move with the ask's move; the words stay on the
// answer note.
func TestStageAnswerInputMoves(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAnswerInput)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Words: "merge on green"}, "ada"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetBead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Stage != "approved" {
		t.Fatalf("after the words the bead is in %s, want approved", cur.Stage)
	}
	outcome, ok := stageAnswerAdvanced(t, s, b.ID, "approved")
	if !ok || outcome != "done" {
		t.Fatalf("advanced into approved: outcome %q, found %v", outcome, ok)
	}
	got = stageAsks(t, s, b.ID)
	if got[0].State != store.AskAnswered || got[1].State != store.AskTakenBack {
		t.Fatalf("states = %s, %s", got[0].State, got[1].State)
	}
	if lines := strings.Join(got[0].Lines(), "\n"); !strings.Contains(lines, "ada answered: merge on green") {
		t.Fatalf("winner's ask reads:\n%s", lines)
	}
}

// Picking You decide records without moving; the person re-picks from the
// bead while it is still in the stage.
func TestStageAnswerYouDecide(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.GetBead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Stage != "review" {
		t.Fatalf("You decide moved the bead to %s", cur.Stage)
	}
	if got = stageAsks(t, s, b.ID); got[0].State != store.AskAnswered || got[1].State != store.AskOpen {
		t.Fatalf("states = %s, %s", got[0].State, got[1].State)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada"); err != nil {
		t.Fatalf("re-pick: %v", err)
	}
	if cur, err = s.GetBead(ctx, b.ID); err != nil || cur.Stage != "approved" {
		t.Fatalf("after the re-pick: %+v, %v", cur, err)
	}
}

// Later puts the stage ask aside and Decline ends it; the bead waits as
// today in both cases.
func TestStageAnswerLaterDecline(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Later: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[1].Note.Seq, Decline: true, Words: "not mine"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if got[0].State != store.AskAside || got[1].State != store.AskDeclined {
		t.Fatalf("states = %s, %s", got[0].State, got[1].State)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "review" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// A pick, words, Later and Decline from anyone but the stage's deciders are
// each refused whole, with the membership reason: no answer is stored and
// the bead does not move.
func TestStageAnswerNonMember(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	if _, err := s.AddUser(ctx, "grace", "jo"); err != nil {
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
	before := stageAnswerNotes(t, s, b.ID)
	for name, ans := range map[string]store.Answer{
		"pick":    {Ask: got[0].Note.Seq, Option: 1},
		"later":   {Ask: got[0].Note.Seq, Later: true},
		"decline": {Ask: got[0].Note.Seq, Decline: true, Words: "no"},
	} {
		if err := s.AddAnswer(ctx, b.ID, ans, "grace"); err == nil ||
			!strings.Contains(err.Error(), "only members of group reviewers may move it out of stage review") {
			t.Fatalf("%s by a non-member: %v", name, err)
		}
	}
	words, ctx2 := stageAskYard(t, stageAnswerInput)
	bw, err := words.CreateBead(ctx2, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if bw, err = words.Advance(ctx2, bw.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wgot := stageAsks(t, words, bw.ID)
	if _, err := words.AddUser(ctx2, "grace", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := words.AddAnswer(ctx2, bw.ID, store.Answer{Ask: wgot[0].Note.Seq, Words: "mine"}, "grace"); err == nil ||
		!strings.Contains(err.Error(), "only members of group reviewers may move it out of stage review") {
		t.Fatalf("words by a non-member: %v", err)
	}
	if after := stageAnswerNotes(t, s, b.ID); len(after) != len(before) {
		t.Fatalf("a refused answer left notes: %d, want %d", len(after), len(before))
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "review" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// A group with no members refuses nobody on the answer path either, as on
// the hand path.
func TestStageAnswerOpenGroup(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	if _, err := s.AddUser(ctx, "grace", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "jo"); err != nil {
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
		t.Fatalf("fallback asks = %+v", got)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "grace"); err != nil {
		t.Fatalf("a stranger's pick on an open group: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "approved" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// Where the yard put the ask to the operator as fallback, the operator's
// answer is taken.
func TestStageAnswerFallback(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"x@nobody"}}, "jo"); err != nil {
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
		t.Fatalf("fallback asks = %+v", got)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 2}, "jo"); err != nil {
		t.Fatalf("the operator's pick on the fallback ask: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "new" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
	if outcome, ok := stageAnswerAdvanced(t, s, b.ID, "new"); !ok || outcome != "changes" {
		t.Fatalf("advanced into new: outcome %q, found %v", outcome, ok)
	}
}

// The operator addressed as fallback remains the recipient's authorized
// answerer if the operator setting changes while the ask is open.
func TestStageAnswerFallbackAfterOperatorChange(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"x@nobody"}}, "jo"); err != nil {
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
	if len(got) != 1 || got[0].Ask.To != "jo" || !got[0].Ask.Fallback {
		t.Fatalf("fallback asks = %+v", got)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 2}, "jo"); err != nil {
		t.Fatalf("the fallback recipient's pick after the operator changed: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "new" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// Two members answering in turn leave one move and one take-back: the
// loser's pick is refused as asked no more, naming the move.
func TestStageAnswerFirstWins(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada"); err != nil {
		t.Fatal(err)
	}
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[1].Note.Seq, Option: 1}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "is asked no more") ||
		!strings.Contains(err.Error(), "taken back by yard: moved to approved by ada") {
		t.Fatalf("the loser's pick: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "approved" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// A stage ask whose current answer is a pick that moved takes no further
// pick; the refusal names the outcome.
func TestStageAnswerRepickAfterMove(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada"); err != nil {
		t.Fatal(err)
	}
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 2}, "ada")
	if err == nil || !strings.Contains(err.Error(), "already moved bead "+b.ID+" with the outcome done") {
		t.Fatalf("re-pick after a moving pick: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "approved" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// A pick while the bead stands in another stage than the ask's is refused,
// naming both: the loser's You decide stays answered, and a re-pick there
// names the stages.
func TestStageAnswerOtherStage(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[1].Note.Seq, Option: 1}, "mckean"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if got[0].State != store.AskAnswered {
		t.Fatalf("the loser's You decide = %s", got[0].State)
	}
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada")
	if err == nil || !strings.Contains(err.Error(), "was put for the stage review") ||
		!strings.Contains(err.Error(), "is in approved") {
		t.Fatalf("re-pick from another stage: %v", err)
	}
}

// A pick on an ask put for an earlier stay is refused, naming the fresh
// ask where one stands.
func TestStageAnswerReentry(t *testing.T) {
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
		t.Fatalf("asks = %d", len(got))
	}
	fresh := got[2].Note.Seq
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 1}, "ada")
	if err == nil || !strings.Contains(err.Error(), "was put for an earlier stay in review") ||
		!strings.Contains(err.Error(), fmt.Sprintf("ask %d was put for this one", fresh)) {
		t.Fatalf("re-pick after re-entry: %v", err)
	}
}

// stageAnswerStoreYard is stageAskYard on a store flow, with the yard's home
// returned so the test can rewrite the template between moves.
func stageAnswerStoreYard(t *testing.T, reviewFile string) (store.API, string, context.Context) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "flows", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageAnswerWriteTemplate(t, home, reviewFile)
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
	return s, home, ctx
}

func stageAnswerWriteTemplate(t *testing.T, home, reviewFile string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "flows", "work", "review.md"), []byte(reviewFile), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A re-pick after a re-entry that put no fresh ask, because the template
// was removed, says so: the bead is moved by hand.
func TestStageAnswerTemplateRemoved(t *testing.T) {
	t.Parallel()
	s, home, ctx := stageAnswerStoreYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	first := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	stageAnswerWriteTemplate(t, home, "Review the diff.\n")
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := stageAsks(t, s, b.ID); len(got) != 2 {
		t.Fatalf("asks after re-entry = %d, want the old two only", len(got))
	}
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 1}, "ada")
	if err == nil || !strings.Contains(err.Error(), "was put for an earlier stay in review") ||
		!strings.Contains(err.Error(), "the stage has no ask template any more") ||
		!strings.Contains(err.Error(), "move the bead by hand") {
		t.Fatalf("re-pick with the template removed: %v", err)
	}
}

// A re-pick after a re-entry that put no fresh ask, because the template is
// broken, says so: the bead is moved by hand.
func TestStageAnswerTemplateBroken(t *testing.T) {
	t.Parallel()
	s, home, ctx := stageAnswerStoreYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	first := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "ada"); err != nil {
		t.Fatal(err)
	}
	stageAnswerWriteTemplate(t, home, "Review.\n\n## Ask\nkind: choce\n")
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := stageAsks(t, s, b.ID); len(got) != 2 {
		t.Fatalf("asks after re-entry = %d, want the old two only", len(got))
	}
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: first[0].Note.Seq, Option: 1}, "ada")
	if err == nil || !strings.Contains(err.Error(), "was put for an earlier stay in review") ||
		!strings.Contains(err.Error(), "its ask template is broken") ||
		!strings.Contains(err.Error(), "move the bead by hand") {
		t.Fatalf("re-pick with the template broken: %v", err)
	}
	evs, err := s.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, e := range evs {
		if e.Kind == "bad_template" {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("bad_template events = %d", bad)
	}
}

// stageAnswerPeer links north carrying ask both ways, heard with ada on its
// roster, and answers north's key.
func stageAnswerPeer(t *testing.T, s store.API, own *store.Store, ctx context.Context, allow store.PeerAllow) string {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	key := yardkey.Public(k)
	if _, err := s.AddPeer(ctx, store.Peer{Name: "north", Key: key}, "jo"); err != nil {
		t.Fatal(err)
	}
	says := store.Says("v1")
	says.Roster = []store.PeerRosterEntry{{Name: "ada", Kind: store.RosterUser, Mail: true}}
	if err := own.PeerHeard(ctx, "north", key, says); err != nil {
		t.Fatal(err)
	}
	allow.Send = append(allow.Send, "ask")
	allow.Receive = append(allow.Receive, "ask")
	if _, err := s.AllowPeer(ctx, "north", allow, "jo"); err != nil {
		t.Fatal(err)
	}
	return key
}

// stageAnswerFromLink is what became of the answer north's name sent home
// for the ask seq on bead, under the message id.
func stageAnswerFromLink(t *testing.T, own *store.Store, ctx context.Context, key, id, re, from, bead string, seq uint64, ans map[string]any) store.PeerTaken {
	t.Helper()
	ans["v"], ans["from"], ans["bead"], ans["seq"] = 1, from, bead, seq
	body, err := json.Marshal(ans)
	if err != nil {
		t.Fatal(err)
	}
	got, err := own.PeerReceive(ctx, "north", key, []store.PeerMessage{{
		ID: id, Kind: store.PeerAsk, Re: re, Body: body,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

// An answer arriving from a linked yard moves the bead the same way: a
// remote member's pick lands by name@peer and fires its outcome.
func TestStageAnswerFromLink(t *testing.T) {
	t.Parallel()
	s, own, ctx := stageAskYardOwned(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada", "mckean@north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	key := stageAnswerPeer(t, s, own, ctx, store.PeerAllow{})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 2 || got[1].Ask.To != "mckean@north" {
		t.Fatalf("asks = %+v", got)
	}
	out, err := s.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 {
		t.Fatalf("outbox = %+v, %v", out, err)
	}
	if taken := stageAnswerFromLink(t, own, ctx, key, "ans1", out[0].ID, "mckean", b.ID, got[1].Note.Seq,
		map[string]any{"option": 1, "words": "from afar"}); taken.Refused != "" {
		t.Fatalf("the remote pick = %+v", taken)
	}
	cur, err := s.GetBead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Stage != "approved" {
		t.Fatalf("after the remote pick the bead is in %s", cur.Stage)
	}
	outcome, ok := stageAnswerAdvanced(t, s, b.ID, "approved")
	if !ok || outcome != "done" {
		t.Fatalf("advanced into approved: outcome %q, found %v", outcome, ok)
	}
	got = stageAsks(t, s, b.ID)
	if got[1].State != store.AskAnswered || got[1].Current.Note.Author != "mckean@north" ||
		got[0].State != store.AskTakenBack {
		t.Fatalf("asks = %+v", got)
	}
	if lines := strings.Join(got[0].Lines(), "\n"); !strings.Contains(lines, "moved to approved by mckean@north") {
		t.Fatalf("loser's ask reads:\n%s", lines)
	}
}

// An answer arriving over reach counts as the member's word where the link
// trusts the name as them: it is taken and moves.
func TestStageAnswerTrustedMember(t *testing.T) {
	t.Parallel()
	s, own, ctx := stageAskYardOwned(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	key := stageAnswerPeer(t, s, own, ctx, store.PeerAllow{
		Trust: map[string]string{"ada": "ada"}, Reach: map[string]string{"ada": "ada"},
	})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if len(got) != 1 || got[0].Ask.To != "ada" {
		t.Fatalf("asks = %+v", got)
	}
	out, err := s.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 {
		t.Fatalf("outbox = %+v, %v", out, err)
	}
	if taken := stageAnswerFromLink(t, own, ctx, key, "ans1", out[0].ID, "ada", b.ID, got[0].Note.Seq,
		map[string]any{"option": 2}); taken.Refused != "" {
		t.Fatalf("the trusted answer = %+v", taken)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "new" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
	if outcome, ok := stageAnswerAdvanced(t, s, b.ID, "new"); !ok || outcome != "changes" {
		t.Fatalf("advanced into new: outcome %q, found %v", outcome, ok)
	}
}

// An answer arriving over a link from none of the stage's deciders is
// refused whole with the membership reason: nothing is stored.
func TestStageAnswerLinkNonMember(t *testing.T) {
	t.Parallel()
	s, own, ctx := stageAskYardOwned(t, stageAskChoice)
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"mckean@north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	key := stageAnswerPeer(t, s, own, ctx, store.PeerAllow{})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	out, err := s.PeerOutbox(ctx, "north", 0)
	if err != nil || len(out) != 1 {
		t.Fatalf("outbox = %+v, %v", out, err)
	}
	// The member leaves the group; the ask still names them.
	if err := s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual", Members: []string{"ada"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	before := stageAnswerNotes(t, s, b.ID)
	taken := stageAnswerFromLink(t, own, ctx, key, "ans1", out[0].ID, "mckean", b.ID, got[0].Note.Seq,
		map[string]any{"option": 1})
	if taken.Refused != store.RefusedNotAsk {
		t.Fatalf("a past member's answer = %+v, want refused as not_ask", taken)
	}
	evs, err := s.Events(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	refused := false
	for _, e := range evs {
		if e.Kind == "peer_message_refused" && e.Data["reason"] == "not_ask" {
			if detail, _ := e.Data["detail"].(string); strings.Contains(detail, "only members of group reviewers") {
				refused = true
			}
		}
	}
	if !refused {
		t.Fatal("no refusal event carries the membership reason")
	}
	if after := stageAnswerNotes(t, s, b.ID); len(after) != len(before) {
		t.Fatalf("a refused answer left notes: %d, want %d", len(after), len(before))
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "review" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
}

// The answer hook never moves a closed bead: a re-pick there is refused,
// and the bead stays closed.
func TestStageAnswerClosed(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAskChoice)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 3}, "ada"); err != nil {
		t.Fatal(err)
	}
	if b, err = s.CloseBead(ctx, b.ID, "stale", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	before := stageAnswerNotes(t, s, b.ID)
	err = s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "ada")
	if err == nil || !strings.Contains(err.Error(), "is closed: a closed bead moves no more") {
		t.Fatalf("re-pick on a closed bead: %v", err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Status != "closed" || cur.Stage != "review" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
	if after := stageAnswerNotes(t, s, b.ID); len(after) != len(before) {
		t.Fatalf("a refused answer left notes: %d, want %d", len(after), len(before))
	}
}

// Words with move none record without moving, for a stage with no outcomes
// to fire.
func TestStageAnswerInputNone(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAnswerInputNone)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Words: "decided: merge"}, "ada"); err != nil {
		t.Fatal(err)
	}
	got = stageAsks(t, s, b.ID)
	if got[0].State != store.AskAnswered || got[1].State != store.AskOpen {
		t.Fatalf("states = %s, %s", got[0].State, got[1].State)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "review" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
	if lines := strings.Join(got[0].Lines(), "\n"); !strings.Contains(lines, "ada answered: decided: merge") {
		t.Fatalf("ask reads:\n%s", lines)
	}
}

// Done on a check ask moves with the check's move.
func TestStageAnswerCheckDone(t *testing.T) {
	t.Parallel()
	s, ctx := stageAskYard(t, stageAnswerCheck)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := stageAsks(t, s, b.ID)
	if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: got[0].Note.Seq, Option: 1}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != "approved" {
		t.Fatalf("bead = %+v, %v", cur, err)
	}
	if outcome, ok := stageAnswerAdvanced(t, s, b.ID, "approved"); !ok || outcome != "done" {
		t.Fatalf("advanced into approved: outcome %q, found %v", outcome, ok)
	}
}
