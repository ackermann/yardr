package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// askYard is a store with one bead and the clock in the test's hand.
type askYard struct {
	*Store
	ctx  context.Context
	bead string
	now  time.Time
}

func newAskYard(t *testing.T) *askYard {
	t.Helper()
	y := &askYard{ctx: context.Background(), now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	b, err := s.CreateBead(y.ctx, NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	y.bead = b.ID
	return y
}

func (y *askYard) ask(t *testing.T, ask Ask, actor string) uint64 {
	t.Helper()
	seq, err := y.AddAsk(y.ctx, y.bead, ask, actor)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func (y *askYard) answer(t *testing.T, ans Answer, actor string) {
	t.Helper()
	if err := y.AddAnswer(y.ctx, y.bead, ans, actor); err != nil {
		t.Fatal(err)
	}
}

func (y *askYard) resolved(t *testing.T) []ResolvedAsk {
	t.Helper()
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	return ResolveAsks(notes, y.now)
}

func linksAsk() Ask {
	return Ask{
		Question: "Move this yard's links to the declared source now?",
		To:       "mckean",
		Context: []string{
			"The links file declares the new source; 64 links still point at the old one.",
			"The move was tested on a copy just now and passed; the old source goes away Friday.",
		},
		Options: []AskOption{
			{Label: "Move them now", Consequence: "all 64 links point at the new source tonight"},
			{Label: "After Friday", Consequence: "keep the old source until the release is out"},
		},
		Recommend: 1,
		Why:       "the test passed; waiting only risks Friday's rush",
	}
}

// An ask is stored with yardr's own option last, and refused without
// context, without a consequence, or naming You decide itself.
func TestAddAsk(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")

	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Kind != NoteAsk || notes[0].Seq != seq || notes[0].Author != "yardmaster" {
		t.Fatalf("notes = %+v", notes)
	}
	got, err := ParseAsk(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []AskOption{
		{Label: "Move them now", Consequence: "all 64 links point at the new source tonight"},
		{Label: "After Friday", Consequence: "keep the old source until the release is out"},
		{Label: "You decide", Consequence: "leave it to yardmaster"},
	}
	if len(got.Options) != 3 {
		t.Fatalf("options = %+v", got.Options)
	}
	for i, o := range want {
		if got.Options[i] != o {
			t.Fatalf("options = %+v, want %+v", got.Options, want)
		}
	}
	if got.Recommend != 1 || got.Why == "" || got.To != "mckean" || len(got.Context) != 2 {
		t.Fatalf("ask = %+v", got)
	}

	bad := []struct {
		name string
		ask  Ask
		want string
	}{
		{"no question", Ask{To: "mckean", Context: []string{"why"}}, "a question"},
		{"no to", Ask{Question: "q?", Context: []string{"why"}}, "--to"},
		{"no context", Ask{Question: "q?", To: "mckean"}, "--context"},
		{"blank context", Ask{Question: "q?", To: "mckean", Context: []string{"  "}}, "--context"},
		{"no consequence", Ask{Question: "q?", To: "mckean", Context: []string{"why"},
			Options: []AskOption{{Label: "Go"}}}, "consequence"},
		{"no label", Ask{Question: "q?", To: "mckean", Context: []string{"why"},
			Options: []AskOption{{Consequence: "it goes"}}}, "a label"},
		{"writer you decide", Ask{Question: "q?", To: "mckean", Context: []string{"why"},
			Options: []AskOption{{Label: "you decide", Consequence: "x"}}}, "leave it out"},
		{"recommend without why", func() Ask { a := linksAsk(); a.Why = ""; return a }(), "--why"},
		{"why without recommend", func() Ask { a := linksAsk(); a.Recommend = 0; return a }(), "--recommend"},
		{"recommend past the end", func() Ask { a := linksAsk(); a.Recommend = 3; return a }(), "1..2"},
		{"recommend with no options", Ask{Question: "q?", To: "mckean", Context: []string{"why"}, Recommend: 1, Why: "x"}, "no options"},
	}
	for _, c := range bad {
		if _, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 1 {
		t.Fatalf("a refused ask left notes: %+v", notes)
	}
}

// About names other beads by handle, whatever form was typed; unknown and
// twice-named ones refuse the ask and store nothing.
func TestAddAskAbout(t *testing.T) {
	y := newAskYard(t)
	other, err := y.CreateBead(y.ctx, NewBead{Title: "the other"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	a := linksAsk()
	a.About = []string{other.UUID, " " + y.bead}
	seq := y.ask(t, a, "yardmaster")
	notes, _ := y.Notes(y.ctx, y.bead)
	got, err := ParseAsk(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got.About) != 2 || got.About[0] != other.ID || got.About[1] != y.bead {
		t.Fatalf("about = %v", got.About)
	}
	_ = seq

	a.About = []string{"yard-nope"}
	if _, err := y.AddAsk(y.ctx, y.bead, a, "yardmaster"); err == nil || !strings.Contains(err.Error(), "about:") {
		t.Fatalf("about an unknown bead: err = %v", err)
	}
	a.About = []string{other.ID, other.UUID}
	if _, err := y.AddAsk(y.ctx, y.bead, a, "yardmaster"); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("about a bead twice: err = %v", err)
	}
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 1 {
		t.Fatalf("a refused ask left notes: %+v", notes)
	}
}

// A pick records who picked which option with the words; a newer pick
// supersedes for display and every pick stays as a note.
func TestAnswerPick(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: seq, Option: 2, Words: "after the release"}, "mckean")

	r := y.resolved(t)
	if len(r) != 1 || r[0].State != AskAnswered || r[0].Current == nil {
		t.Fatalf("resolved = %+v", r)
	}
	if got := r[0].Current.Answer; got.Option != 2 || got.Words != "after the release" {
		t.Fatalf("answer = %+v", got)
	}
	if r[0].Current.Note.Author != "mckean" {
		t.Fatalf("answer author = %s", r[0].Current.Note.Author)
	}

	y.now = y.now.Add(time.Hour)
	y.answer(t, Answer{Ask: seq, Option: 1}, "mckean")
	r = y.resolved(t)
	if len(r[0].Answers) != 2 {
		t.Fatalf("answers = %+v", r[0].Answers)
	}
	if got := r[0].Current.Answer; got.Option != 1 || got.Words != "" {
		t.Fatalf("the newer pick did not supersede: %+v", got)
	}

	// Out of range, and on another bead's ask, are refused.
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Option: 4}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "options 1..3") {
		t.Fatalf("pick past the end: err = %v", err)
	}
	other, err := y.CreateBead(y.ctx, NewBead{Title: "other"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.AddAnswer(y.ctx, other.ID, Answer{Ask: seq, Option: 1}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "has no ask") {
		t.Fatalf("another bead's ask: err = %v", err)
	}
	// A note that is no ask is refused as such.
	if err := y.AddNote(y.ctx, y.bead, "plain", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	notes, _ := y.Notes(y.ctx, y.bead)
	var plain uint64
	for _, n := range notes {
		if n.Kind == NoteNote {
			plain = n.Seq
		}
	}
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: plain, Option: 1}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "is not an ask") {
		t.Fatalf("a plain note as the ask: err = %v", err)
	}
}

// An ask with no options wants words: --words closes it, a pick is
// refused, and words alone on an ask with options go as a plain note.
func TestAnswerWords(t *testing.T) {
	y := newAskYard(t)
	words := linksAsk()
	words.Options, words.Recommend, words.Why = nil, 0, ""
	wseq := y.ask(t, words, "yardmaster")
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 1 {
		t.Fatal("the words ask was not stored")
	}
	got, _ := ParseAsk(mustNote(t, y, wseq))
	if len(got.Options) != 0 {
		t.Fatalf("a words ask gained options: %+v", got.Options)
	}

	y.answer(t, Answer{Ask: wseq, Words: "a warm greeting"}, "mckean")
	if r := y.resolved(t); r[0].State != AskAnswered {
		t.Fatalf("state = %v", r[0].State)
	}
	// Answered with words, it takes no further answer.
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: wseq, Words: "more"}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "answered already") {
		t.Fatalf("words after words: err = %v", err)
	}

	oseq := y.ask(t, linksAsk(), "yardmaster")
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: oseq, Words: "just words"}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "words alone go as yardr bead note") ||
		!strings.Contains(err.Error(), "leave the ask open") {
		t.Fatalf("words alone on an ask with options: err = %v", err)
	}
	open := linksAsk()
	open.Options, open.Recommend, open.Why = nil, 0, ""
	oseq2 := y.ask(t, open, "yardmaster")
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: oseq2, Option: 1}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "wants words") {
		t.Fatalf("a pick on a words ask: err = %v", err)
	}
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: oseq2}, "mckean"); err == nil {
		t.Fatalf("an empty answer went through")
	}
}

func mustNote(t *testing.T, y *askYard, seq uint64) Note {
	t.Helper()
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if n.Seq == seq {
			return n
		}
	}
	t.Fatalf("no note %d", seq)
	return Note{}
}

// Later puts the ask aside for 24 hours or until the asker's next note,
// whichever is first; answering early is a pick like any other.
func TestAnswerLater(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")

	r := y.resolved(t)
	if r[0].State != AskAside || r[0].Current == nil || !r[0].Current.Answer.Later {
		t.Fatalf("resolved = %+v", r)
	}
	if want := y.now.Add(LaterFor); !r[0].ReturnAt.Equal(want) {
		t.Fatalf("return at %v, want %v", r[0].ReturnAt, want)
	}

	// A note from anyone but the asker leaves it aside.
	y.now = y.now.Add(time.Hour)
	if err := y.AddNote(y.ctx, y.bead, "a comment", "mckean"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t); r[0].State != AskAside {
		t.Fatalf("another's note brought it back: %+v", r[0])
	}
	// The asker's next note brings it back.
	if err := y.AddNote(y.ctx, y.bead, "the test log", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if r := y.resolved(t); r[0].State != AskOpen {
		t.Fatalf("the asker's note did not bring it back: %+v", r[0])
	}

	// Put aside again, it comes back after 24 hours with no note.
	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")
	y.now = y.now.Add(LaterFor - time.Minute)
	if r := y.resolved(t); r[0].State != AskAside {
		t.Fatalf("back before its 24 hours: %+v", r[0])
	}
	y.now = y.now.Add(2 * time.Minute)
	if r := y.resolved(t); r[0].State != AskOpen {
		t.Fatalf("not back after 24 hours: %+v", r[0])
	}

	// Answered early, while aside: the pick stands.
	y.now = y.now.Add(-time.Hour)
	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")
	y.answer(t, Answer{Ask: seq, Option: 1}, "mckean")
	if r := y.resolved(t); r[0].State != AskAnswered {
		t.Fatalf("the early pick did not stand: %+v", r[0])
	}
	// Answered, it takes only a newer pick.
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Later: true}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "newer pick") {
		t.Fatalf("later after a pick: err = %v", err)
	}
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Decline: true, Words: "no"}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "newer pick") {
		t.Fatalf("decline after a pick: err = %v", err)
	}
}

// Decline ends the ask with no option picked, in the person's own words,
// and nothing answers it after.
func TestAnswerDecline(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Decline: true}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "--decline") {
		t.Fatalf("decline without words: err = %v", err)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "the release owns Friday"}, "mckean")

	r := y.resolved(t)
	if r[0].State != AskDeclined {
		t.Fatalf("resolved = %+v", r)
	}
	lines := strings.Join(r[0].Lines(), "\n")
	if !strings.Contains(lines, "mckean declined to decide this: the release owns Friday") {
		t.Fatalf("decline reads:\n%s", lines)
	}
	for _, ans := range []Answer{{Ask: seq, Option: 1}, {Ask: seq, Later: true}, {Ask: seq, Decline: true, Words: "again"}} {
		if err := y.AddAnswer(y.ctx, y.bead, ans, "mckean"); err == nil || !strings.Contains(err.Error(), "declined") {
			t.Fatalf("an answer after decline went through: %+v, err = %v", ans, err)
		}
	}
}

// The rendering is numbered text with the recommendation marked and the
// state, never the raw JSON.
func TestAskLines(t *testing.T) {
	y := newAskYard(t)
	seq := y.ask(t, linksAsk(), "yardmaster")
	y.now = y.now.Add(2 * time.Hour)
	y.answer(t, Answer{Ask: seq, Option: 1, Words: "go"}, "mckean")

	r := y.resolved(t)
	text := strings.Join(r[0].Lines(), "\n")
	for _, want := range []string{
		"ask 1 · answered",
		"to mckean",
		"asked by yardmaster",
		"2h ago",
		"Move this yard's links to the declared source now?",
		"The links file declares the new source",
		"1. Move them now - all 64 links point at the new source tonight (recommended: the test passed; waiting only risks Friday's rush)",
		"2. After Friday - keep the old source until the release is out",
		"3. You decide - leave it to yardmaster",
		"mckean picked 1 (Move them now): go",
		"answer with: yardr bead answer ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendering has no %q:\n%s", want, text)
		}
	}
	for _, raw := range []string{`"question"`, `"consequence"`, `"recommend"`} {
		if strings.Contains(text, raw) {
			t.Errorf("the rendering shows raw JSON %q:\n%s", raw, text)
		}
	}

	// Open, aside and declined read as such, with the return time.
	y2 := newAskYard(t)
	o := y2.ask(t, linksAsk(), "yardmaster")
	y2.now = y2.now.Add(time.Hour)
	y2.answer(t, Answer{Ask: o, Later: true}, "mckean")
	text = strings.Join(y2.resolved(t)[0].Lines(), "\n")
	if !strings.Contains(text, "put aside until "+y2.now.Add(LaterFor).Local().Format("2006-01-02 15:04")) ||
		!strings.Contains(text, "back sooner if yardmaster writes") {
		t.Fatalf("aside reads:\n%s", text)
	}
	if !strings.Contains(text, "answer with: yardr bead answer") {
		t.Fatalf("a put-aside ask lost its hint:\n%s", text)
	}

	// Words wanted, and a corrupt ask that says so without its text.
	y3 := newAskYard(t)
	w := linksAsk()
	w.Options, w.Recommend, w.Why = nil, 0, ""
	wseq := y3.ask(t, w, "yardmaster")
	text = strings.Join(y3.resolved(t)[0].Lines(), "\n")
	if !strings.Contains(text, "words wanted") || !strings.Contains(text, "--words \"<text>\"") {
		t.Fatalf("a words ask reads:\n%s", text)
	}
	_ = wseq
	broken := ResolvedAsk{Note: Note{Seq: 9, Kind: NoteAsk, Text: "{oops"}, AskErr: errBrokenTest}
	if text := strings.Join(broken.Lines(), "\n"); !strings.Contains(text, "ask 9") || strings.Contains(text, "{oops") {
		t.Fatalf("a corrupt ask reads:\n%s", text)
	}
}

var errBrokenTest = errors.New("unexpected end of JSON input")

// A topic is a bead label with the prefix topic:, and it routes like any
// label: only a route that names it exactly matches it.
func TestTopicLabelsRouteExactly(t *testing.T) {
	bead := Bead{ID: "yard-1", Stage: "review", Type: "task", Labels: []string{"topic:linear-out", "muse"}}
	plain := Route{ID: "b", Stage: "review", Group: "reviewers", Priority: 1}
	label := "muse"
	plain.Label = &label
	topic := Route{ID: "a", Stage: "review", Group: "linear", Priority: 0}
	topicLabel := "topic:linear-out"
	topic.Label = &topicLabel
	routes := []Route{topic, plain}
	SortRoutes(routes)
	if got := MatchRoute(routes, bead); got == nil || got.ID != "b" {
		t.Fatalf("the plain label route lost to the topic one: %+v", got)
	}
	bead.Labels = []string{"topic:linear-out"}
	if got := MatchRoute(routes, bead); got == nil || got.ID != "a" {
		t.Fatalf("the topic route matches only its own label: %+v", got)
	}
	bead.Labels = []string{"topic:muse"}
	if got := MatchRoute(routes, bead); got != nil {
		t.Fatalf("topic:muse matched a route for muse: %+v", got)
	}
}
