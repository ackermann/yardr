package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// replies is a store whose AnswerAsk and ApproveAsk are the test's: it keeps
// what each was called with, and answers with fail. It records the answer
// and changes nothing of the yard, so that the ask stays open for the next.
type replies struct {
	store.API
	answered []answerCall
	approved []approveCall
	fail     error
}

type answerCall struct {
	bead    string
	ask     uint64
	option  int
	words   string
	actor   string
	weighed bool
}

type approveCall struct {
	bead    string
	ask     uint64
	actor   string
	weighed bool
	values  map[string]string
}

func (r *replies) AnswerAsk(_ context.Context, id string, ask uint64, option int, words, actor string, weighed bool) error {
	r.answered = append(r.answered, answerCall{id, ask, option, words, actor, weighed})
	return r.fail
}

func (r *replies) ApproveAsk(_ context.Context, id string, ask uint64, actor string, weighed bool, values map[string]string) (*store.AskApproved, error) {
	r.approved = append(r.approved, approveCall{id, ask, actor, weighed, values})
	if r.fail != nil {
		return nil, r.fail
	}
	return &store.AskApproved{Bead: id, Ask: ask, Argv: []string{"user", "add", "grace"}}, nil
}

func ctrlR() tea.Msg { return tea.KeyMsg{Type: tea.KeyCtrlR} }

// press gives the model a key and leaves what it asks for unrun: a field
// that takes the focus asks for its cursor to blink, which is a timer the
// test does not wait for. What enter sends is run, by v.key.
func (v *view) press(msg tea.Msg) {
	v.t.Helper()
	v.m.Update(msg)
}

// wordsAsk is chief asking ada a question that has no options on a bead of
// its own.
func wordsAsk(t *testing.T, st store.API) (string, uint64) {
	t.Helper()
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the day"}, "yardmaster")
	must(t, err)
	seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Which day do the links move?", To: me,
		Context: []string{"The old source stays up until they do."}}, "chief")
	must(t, err)
	return b.ID, seq
}

// runAsk is chief asking ada to approve adding grace, a card that weighs
// (trust), on the bead secretYard made.
func runAsk(t *testing.T, st store.API, bead string) uint64 {
	t.Helper()
	seq, err := st.AddAsk(context.Background(), bead, store.Ask{Question: "Add grace?", To: me,
		Context: []string{"She joins the yard."}, Run: &store.AskRun{Argv: []string{"user add grace"}}}, "chief")
	must(t, err)
	return seq
}

// An open option ask is answered from the field with a pick, and with a pick
// and words, and the yard is called with what yardr bead answer would send:
// AnswerAsk with the ask, the pick, the words, the user, and no word of
// weight. The same through a store that records: the card reads answered.
func TestAnswerAPick(t *testing.T) {
	st := yard(t)
	id, seq := askBead(t, st, "chief")
	r := &replies{API: st}
	v := withAsks(t, r, "chief", 120, 40)
	v.fits(120, 40)

	// One ask waits, so the field goes to it. It replaces the line for mail,
	// and says what it takes, what enter does and which keys there are.
	v.press(ctrlR())
	v.shows(fmt.Sprintf("ask %d: pick 1-3 [words]", seq), "enter answers the ask · esc leaves, and nothing is sent",
		"enter answer the ask  esc leave, nothing is sent  ^c quit")
	v.hides("Write to chief", "send  ^j new line")
	v.fits(120, 40)

	// A pick that is no number is no answer: nothing is sent, the field
	// stays, and words alone on an option ask are talk for the conversation.
	v.press(runes("go ahead"))
	v.key(tea.KeyEnter)
	v.shows(`"go" is no option number: type 1 to 3, then words if you like`, fmt.Sprintf("ask %d: pick 1-3 [words]", seq))
	if v.m.answering == nil || len(r.answered) != 0 {
		t.Fatalf("after a pick that is no number the field is %v and the yard was called %d times", v.m.answering, len(r.answered))
	}
	// Nothing typed stays too.
	v.m.answering.in.Reset()
	v.key(tea.KeyEnter)
	v.shows("nothing typed: esc leaves, and nothing is sent")

	v.press(runes("2"))
	v.key(tea.KeyEnter)
	want := []answerCall{{id, seq, 2, "", me, false}}
	if !reflect.DeepEqual(r.answered, want) || v.m.answering != nil {
		t.Fatalf("the yard was called %+v (want %+v), the field is %v", r.answered, want, v.m.answering)
	}
	v.shows(fmt.Sprintf("ada picked 2 on ask %d", seq))
	v.hides("nothing was sent")

	// A pick and words: what follows the number is the words, as the
	// command's fourth argument is, and pasted into the field it stays in
	// the field and not in the line behind it.
	v.press(ctrlR())
	v.press(pasted("1  after the release  "))
	if got := v.m.input.Value(); got != "" {
		t.Fatalf("a paste into the field reached the line for mail: %q", got)
	}
	v.shows("ask 1: pick 1-3 [words]  1  after the release")
	v.key(tea.KeyEnter)
	want = append(want, answerCall{id, seq, 1, "after the release", me, false})
	if !reflect.DeepEqual(r.answered, want) {
		t.Fatalf("the yard was called %+v, want %+v", r.answered, want)
	}
	if len(r.approved) != 0 {
		t.Fatalf("the yard was asked to approve: %+v", r.approved)
	}

	// Esc leaves with nothing sent, whatever was typed.
	v.press(ctrlR())
	v.press(runes("3"))
	v.key(tea.KeyEsc)
	v.shows("nothing was sent")
	if v.m.answering != nil || len(r.answered) != 2 {
		t.Fatalf("after esc the field is %v and the yard was called %d times", v.m.answering, len(r.answered))
	}

	// Through the yard itself, the card reads what was picked.
	v = withAsks(t, st, "chief", 120, 40)
	v.press(ctrlR())
	v.press(runes("2 not before Friday"))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d · answered", seq), "ada picked 2 (After Friday): not before Friday")
	v.hides("· open", AnswerKey)
	// An answered ask is not offered again: a newer pick is the terminal's.
	v.press(ctrlR())
	v.shows("nothing to answer: chief asks you nothing that waits")
}

// A question that wants words is answered with the words typed in the field,
// whatever they look like: "1" is a word here, since the field was opened
// for words.
func TestAnswerWithWords(t *testing.T) {
	st := yard(t)
	id, seq := wordsAsk(t, st)
	r := &replies{API: st}
	v := withAsks(t, r, "chief", 200, 40)
	v.shows("ctrl+r answers it here · or in a terminal: yardr bead answer " + id + fmt.Sprintf(" %d --words \"<text>\"", seq))

	v.press(ctrlR())
	v.shows(fmt.Sprintf("ask %d: words", seq))
	v.press(runes("1 and then the 2nd, on a Friday"))
	v.key(tea.KeyEnter)
	want := []answerCall{{id, seq, 0, "1 and then the 2nd, on a Friday", me, false}}
	if !reflect.DeepEqual(r.answered, want) {
		t.Fatalf("the yard was called %+v, want %+v", r.answered, want)
	}
	v.shows(fmt.Sprintf("ada answered ask %d", seq))

	v = withAsks(t, st, "chief", 120, 40)
	v.press(ctrlR())
	v.press(runes("Friday"))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d · answered", seq), "ada answered: Friday")
}

// A run ask wants its number typed back, whether the card weighs or not, and
// the yard hears of the approval only then: another number, and nothing, send
// nothing, and the yard is not called. The number typed back is the word that
// its person saw the weight (weighed).
func TestAnswerARunAsk(t *testing.T) {
	st, bead := secretYard(t)
	seq := runAsk(t, st, bead)
	r := &replies{API: st}
	v := withAsks(t, r, "chief", 200, 40)
	v.shows("ctrl+r approves it here · or in a terminal: yardr bead answer " + bead + fmt.Sprintf(" %d --approve", seq))
	v.hides("approve with:")

	number := fmt.Sprint(seq)
	v.press(ctrlR())
	v.shows(fmt.Sprintf("type %d to approve it, the card is weighty", seq), "a weighty card: its number shows as you type it",
		"enter approves it · esc leaves, and nothing is sent", "enter approve it  esc leave, nothing is sent  ^c quit")
	v.key(tea.KeyEnter)
	v.shows("nothing typed: esc leaves, and nothing is sent")
	v.press(runes("9" + number))
	v.key(tea.KeyEnter)
	v.shows(number + " was not typed back: nothing was sent")
	// A pick on a run ask is not the way either: it would be the yard's
	// refusal, and the view does not send it.
	if v.m.answering != nil || len(r.approved) != 0 || len(r.answered) != 0 {
		t.Fatalf("after another number the field is %v and the yard was called %d and %d times", v.m.answering, len(r.approved), len(r.answered))
	}

	v.press(ctrlR())
	v.press(runes(number))
	v.shows(fmt.Sprintf("type %d to approve it, the card is weighty  %d", seq, seq))
	v.key(tea.KeyEnter)
	want := []approveCall{{bead, seq, me, true, nil}}
	if !reflect.DeepEqual(r.approved, want) || len(r.answered) != 0 {
		t.Fatalf("the yard was called %+v and %+v, want %+v", r.approved, r.answered, want)
	}
	v.shows("ada approved ask "+number+", and the yard ran it: ", "user add grace")

	// A card with no weight on it has no weight to say, and still wants the
	// number: with nothing to type, it is the deliberate act.
	v = open(t, r, "chief", 120, 40, func(o *Options) {
		o.Asks = func(ctx context.Context) ([]store.ResolvedAsk, error) {
			rs, err := AsksFor(ctx, st, me)
			for i := range rs {
				rs[i].Ask.Weight = nil
			}
			return rs, err
		}
	})
	v.do(v.m.loadAsks())
	v.press(ctrlR())
	v.shows(fmt.Sprintf("type %d to approve it  ", seq))
	v.hides("the card is weighty", "a weighty card")
	v.key(tea.KeyEsc)
	if len(r.approved) != 1 {
		t.Fatalf("the yard was called %d times", len(r.approved))
	}
	v.press(ctrlR())
	v.press(runes(number))
	v.key(tea.KeyEnter)
	if want := append(want, approveCall{bead, seq, me, true, nil}); !reflect.DeepEqual(r.approved, want) {
		t.Fatalf("the yard was called %+v, want %+v", r.approved, want)
	}

	// Through the yard itself, which runs the command as ada: the card reads
	// that it ran.
	v = withAsks(t, st, "chief", 120, 40)
	v.press(ctrlR())
	v.press(runes(number))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d · approved", seq), "ada approved, ran: ")
	if _, err := st.GetUser(context.Background(), "grace"); err != nil {
		t.Fatalf("the yard ran nothing: %v", err)
	}
}

// With several asks that wait the field first asks which, by the number the
// cards print. A number that is none of them keeps the field, esc answers
// nothing and calls nothing, and the ask chosen is the one answered.
func TestAnswerWhichAsk(t *testing.T) {
	st := yard(t)
	_, seq1 := askBead(t, st, "chief")
	second, seq2 := askBead(t, st, "chief")
	r := &replies{API: st}
	v := withAsks(t, r, "chief", 120, 40)

	v.press(ctrlR())
	which := fmt.Sprintf("answer which ask: %d, %d", seq1, seq2)
	v.shows(which, "enter goes to that ask · esc leaves, and nothing is sent", "enter go to that ask  esc leave, nothing is sent  ^c quit")
	v.key(tea.KeyEnter)
	v.shows("nothing typed: esc leaves, and nothing is sent")
	v.press(runes("7"))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf(`"7" is none of the asks to answer: %d, %d`, seq1, seq2), which)
	v.key(tea.KeyEsc)
	v.shows("nothing was sent")
	if v.m.answering != nil || len(r.answered) != 0 || len(r.approved) != 0 {
		t.Fatalf("after esc the field is %v and the yard was called %d and %d times", v.m.answering, len(r.answered), len(r.approved))
	}

	// The second is chosen by its number and answered by its bead.
	v.press(ctrlR())
	v.press(runes(fmt.Sprint(seq2)))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d: pick 1-3 [words]", seq2))
	v.hides(which)
	v.press(runes("3"))
	v.key(tea.KeyEnter)
	want := []answerCall{{second, seq2, 3, "", me, false}}
	if !reflect.DeepEqual(r.answered, want) {
		t.Fatalf("the yard was called %+v, want %+v", r.answered, want)
	}

	// With many asks the label names the first few and stops: what is typed
	// after it must have room in a narrow terminal, and the cards above
	// print every number.
	for range 6 {
		askBead(t, st, "chief")
	}
	v = withAsks(t, r, "chief", 80, 40)
	v.press(ctrlR())
	v.shows(fmt.Sprintf("answer which ask: %d, %d, ", seq1, seq2), ", …")
	v.press(runes("12"))
	v.fits(80, 40)
	v.shows(", …  12")
}

// The yard's refusal is shown in the yard's words and the field is closed: a
// pick the card has no option for, an ask declined from a terminal while the
// field was open, and whatever else a store says. The view adds no check of
// its own to them.
func TestAnswerRefusedIsShown(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	id, seq := askBead(t, st, "chief")
	v := withAsks(t, st, "chief", 120, 40)

	v.press(ctrlR())
	v.press(runes("9"))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d has options 1..3", seq))
	if v.m.answering != nil {
		t.Fatal("the field is open after the yard answered")
	}
	v.shows("· open")

	v.press(ctrlR())
	v.press(runes("1"))
	must(t, st.AddAnswer(ctx, id, store.Answer{Ask: seq, Decline: true, Words: "not today"}, me))
	v.key(tea.KeyEnter)
	v.shows(fmt.Sprintf("ask %d was declined: ada declined to decide this: not today", seq))
	v.hides("· open")

	r := &replies{API: st, fail: errors.New("stopped here")}
	id2, seq2 := askBead(t, st, "chief")
	v = withAsks(t, r, "chief", 120, 40)
	v.press(ctrlR())
	v.press(runes("1"))
	v.key(tea.KeyEnter)
	v.shows("stopped here")
	if want := []answerCall{{id2, seq2, 1, "", me, false}}; !reflect.DeepEqual(r.answered, want) {
		t.Fatalf("the yard was called %+v, want %+v", r.answered, want)
	}
	// The next key that does something takes the line away.
	v.typed("x")
	v.hides("stopped here")
}

// ctrl+r says why where there is nothing for it to answer, and then the line
// is the line for mail: what is typed next is mail. An ask put aside waits to
// be answered, so it is answered; what the field leaves to others says where
// they answer it.
func TestNothingToAnswer(t *testing.T) {
	st, bead := secretYard(t)
	askBead(t, st, "recorder")
	v := withAsks(t, st, "chief", 120, 40)
	v.press(ctrlR())
	v.shows("nothing to answer: chief asks you nothing that waits")
	v.typed("hello")
	if got := v.m.input.Value(); got != "hello" || v.m.answering != nil {
		t.Fatalf("the line holds %q, the field is %v", got, v.m.answering)
	}
	v.m.input.Reset()

	// A secret is typed with ctrl+o, and a run card that asks for values in
	// the clear is approved with --value in a terminal (the yard writes that
	// part of a card, so the view reads it from the ask as it comes).
	secretAsk(t, st, bead, "crm")
	runAsk(t, st, bead)
	v = open(t, st, "chief", 120, 40, func(o *Options) {
		o.Asks = func(ctx context.Context) ([]store.ResolvedAsk, error) {
			rs, err := AsksFor(ctx, st, me)
			for i := range rs {
				if rs[i].Ask.Run != nil {
					rs[i].Ask.Run.Ask = []string{"project_id"}
				}
			}
			return rs, err
		}
	})
	v.do(v.m.loadAsks())
	v.showsUnwrapped("approve with: yardr bead answer "+bead, "ctrl+o types it here")
	v.hides("ctrl+r")
	v.press(ctrlR())
	v.shows("nothing to answer here: what chief waits on is answered as its card says (ctrl+o gives a secret)")
	if v.m.answering != nil {
		t.Fatal("a field for asks the field leaves")
	}

	// The yard's row has no conversation, and a view that was given no way to
	// answer names the command.
	v.key(tea.KeyShiftTab)
	if !v.m.OnYard() {
		t.Fatalf("selected %q", v.m.Selected())
	}
	v.press(ctrlR())
	v.shows("nothing to answer here: a question is answered in a crew member's conversation")
	v = open(t, st, "recorder", 120, 40, func(o *Options) {
		o.Answers = nil
		o.Asks = func(ctx context.Context) ([]store.ResolvedAsk, error) { return AsksFor(ctx, st, me) }
	})
	v.do(v.m.loadAsks())
	v.press(ctrlR())
	v.shows("a question is answered in a terminal here: yardr bead answer <bead> <ask> <n>, as its card says")
	if v.m.answering != nil {
		t.Fatal("a field in a view that cannot answer")
	}

	// Put aside, the ask still waits and is offered; its card says so.
	st = yard(t)
	put, seq := askBead(t, st, "recorder")
	must(t, st.AddAnswer(context.Background(), put, store.Answer{Ask: seq, Later: true}, me))
	v = withAsks(t, st, "recorder", 120, 40)
	v.shows("put aside until")
	v.showsUnwrapped("ctrl+r answers it here")
	v.press(ctrlR())
	v.shows(fmt.Sprintf("ask %d: pick 1-3 [words]", seq))
}

// A bare number typed in the line is mail and nothing else: a mail can be
// "1", and an answer is the field's, a deliberate act. And the key that opens
// the field is the line's no more than the view's: no binding of the textarea
// holds it, and none of the view's other keys is it.
func TestNumberInTheLineIsMail(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	askBead(t, st, "chief")
	r := &replies{API: st}
	v := withAsks(t, r, "chief", 120, 40)
	v.typed("1")
	v.key(tea.KeyEnter)
	if len(r.answered) != 0 || len(r.approved) != 0 {
		t.Fatalf("a number in the line answered an ask: %+v %+v", r.answered, r.approved)
	}
	letters, _, err := st.Conversation(ctx, me, "chief", 0, 0, 0)
	must(t, err)
	if len(letters) != 1 || letters[0].Text != "1" || letters[0].From != me {
		t.Fatalf("the line sent %+v, want the mail \"1\"", letters)
	}

	var maps []key.Binding
	for _, km := range []any{v.m.input.KeyMap, textinput.DefaultKeyMap} {
		fields := reflect.ValueOf(km)
		for i := range fields.NumField() {
			if b, ok := fields.Field(i).Interface().(key.Binding); ok {
				maps = append(maps, b)
			}
		}
	}
	if len(maps) < 20 {
		t.Fatalf("found %d bindings in the line's keymaps, the walk is wrong", len(maps))
	}
	for _, b := range maps {
		if slices.Contains(b.Keys(), AnswerKey) {
			t.Errorf("%s is bound in the line to type in: %v", AnswerKey, b.Keys())
		}
	}
	for _, other := range []string{SecretKey, PaneKey, "ctrl+c", "ctrl+j", "tab", "shift+tab", "enter", "pgup", "pgdown", "esc"} {
		if AnswerKey == other {
			t.Errorf("%s is another key of the view", AnswerKey)
		}
	}
}

// What the view asks of the yard to answer is what docs/api.md promises a
// client that is not yardr, or says is an operator's alone: AnswerAsk is a
// call for any client, ApproveAsk the operator's.
func TestAnswerStoreIsTheDocumentedAPI(t *testing.T) {
	st := reflect.TypeOf((*AnswerStore)(nil)).Elem()
	for i := range st.NumMethod() {
		if name := st.Method(i).Name; rpc.Calls[name] == nil && rpc.OperatorCalls[name] == nil {
			t.Errorf("chat.AnswerStore has %s, which docs/api.md names in neither list (rpc.Calls, rpc.OperatorCalls)", name)
		}
	}
}
