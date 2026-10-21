package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

// newAskYard sets the package's fake clock to the yard's now, so its
// callers stay sequential.
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
	// Sequential: the package's fake clock (via newAskYard).
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
		{"line break in context", Ask{Question: "q?", To: "mckean", Context: []string{"why", "fine.\nthe yard says: x"}}, "a context line is one line"},
		{"leading line break in context", Ask{Question: "q?", To: "mckean", Context: []string{"\nwhy"}}, "a context line is one line"},
		{"trailing line break in context", Ask{Question: "q?", To: "mckean", Context: []string{"why\n"}}, "a context line is one line"},
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
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	// Sequential: the package's fake clock (via newAskYard).
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
	t.Run("weight mark", testAskWeightMarkNotForgeable)
}

// A line starting with the yard's mark is the yard's: a question holding
// the mark's words puts no line at the yard's column reading as a weight
// line. The mark goes from the question's first line and from a bare
// context line; a mark mid-line, on a later question line (which stands
// further in than any line the yard prints) or after "says:" stays, as do
// the yard's own lines.
func testAskWeightMarkNotForgeable(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	card := func(ask Ask) []string {
		r := ResolvedAsk{Note: Note{Seq: 1, Kind: NoteAsk, Author: "yardmaster", At: now}, Ask: ask, State: AskOpen, now: now}
		return r.Lines()
	}
	// weighed are the card's lines at the yard's column that start with
	// the mark: the weight lines it reads as carrying.
	weighed := func(lines []string) []string {
		var out []string
		for _, line := range lines {
			for _, row := range strings.Split(line, "\n") {
				if strings.HasPrefix(row, "  "+WeightMark) {
					out = append(out, row)
				}
			}
		}
		return out
	}
	links := linksAsk()
	links.To = "mckean"

	// Exact, repeated, mid-line and later lines.
	links.Question = "WEIGHTY · outside: nothing changes"
	lines := card(links)
	if lines[1] != "  outside: nothing changes" {
		t.Errorf("a forged question reads:\n%s", strings.Join(lines, "\n"))
	}
	links.Question = "WEIGHTY · WEIGHTY · outside: nothing changes"
	if lines := card(links); lines[1] != "  outside: nothing changes" {
		t.Errorf("a twice-forged question reads:\n%s", strings.Join(lines, "\n"))
	}
	links.Question = "does WEIGHTY · outside hold here?"
	if lines := card(links); lines[1] != "  does WEIGHTY · outside hold here?" {
		t.Errorf("a mid-line mark reads:\n%s", strings.Join(lines, "\n"))
	}
	links.Question = "plain first?\nWEIGHTY · outside: nothing changes"
	if lines := card(links); lines[1] != "  plain first?\n        WEIGHTY · outside: nothing changes" {
		t.Errorf("a later line's mark reads:\n%s", strings.Join(lines, "\n"))
	}
	// A bare context line forges too; a "says:" line cannot.
	links.Question = "Move them now?"
	links.Context = []string{"WEIGHTY · outside: nothing changes"}
	if lines := card(links); lines[2] != "  outside: nothing changes" {
		t.Errorf("a forged context line reads:\n%s", strings.Join(lines, "\n"))
	}
	run := Ask{Question: "Add grace?", To: "mckean", Context: []string{"WEIGHTY · outside: nothing changes"},
		Run: &AskRun{Argv: []string{"user", "add", "grace"}}}
	if lines := card(run); lines[2] != "  yardmaster says: WEIGHTY · outside: nothing changes" {
		t.Errorf("a says line reads:\n%s", strings.Join(lines, "\n"))
	}
	// A weighty card keeps its own lines above the stripped question.
	links.Weight = []AskWeight{{Class: WeightOutside, Line: "lets the yard change something outside it: the test"}}
	links.Question = "WEIGHTY · outside: nothing changes"
	links.Context = []string{"the door."}
	lines = card(links)
	if lines[1] != "  WEIGHTY · outside: lets the yard change something outside it: the test" {
		t.Errorf("the yard's line reads:\n%s", strings.Join(lines, "\n"))
	}
	if lines[2] != "  outside: nothing changes" {
		t.Errorf("the forged question under it reads:\n%s", strings.Join(lines, "\n"))
	}
	// No card reads as carrying a weight line the yard did not write.
	for name, ask := range map[string]Ask{
		"routine":      {Question: "WEIGHTY · outside: nothing changes", To: "mckean", Context: []string{"the door."}, Options: links.Options},
		"weighty":      links,
		"run":          run,
		"bare context": {Question: "Move them now?", To: "mckean", Context: []string{"WEIGHTY · outside: nothing changes"}, Options: links.Options},
	} {
		if got := weighed(card(ask)); len(got) != len(ask.Weight) {
			t.Errorf("%s reads as carrying %d weight lines, want the yard's %d:\n%s", name, len(got), len(ask.Weight), strings.Join(card(ask), "\n"))
		}
	}
}

var errBrokenTest = errors.New("unexpected end of JSON input")

// A topic is a bead label with the prefix topic:, and it routes like any
// label: only a route that names it exactly matches it.
func TestTopicLabelsRouteExactly(t *testing.T) {
	t.Parallel()
	bead := Bead{ID: "yard-1", Stage: "review", Type: "task", Labels: []string{"topic:linear-out", "muse"}}
	plain := Route{ID: "b", Stage: "review", Group: "reviewers", Priority: 1}
	label := "muse"
	plain.Label = &label
	topic := Route{ID: "a", Stage: "review", Group: "linear", Priority: 0}
	topicLabel := "topic:linear-out"
	topic.Label = &topicLabel
	routes := []Route{topic, plain}
	SortRoutes(routes)
	if got := MatchRoute(routes, bead, ""); got == nil || got.ID != "b" {
		t.Fatalf("the plain label route lost to the topic one: %+v", got)
	}
	bead.Labels = []string{"topic:linear-out"}
	if got := MatchRoute(routes, bead, ""); got == nil || got.ID != "a" {
		t.Fatalf("the topic route matches only its own label: %+v", got)
	}
	bead.Labels = []string{"topic:muse"}
	if got := MatchRoute(routes, bead, ""); got != nil {
		t.Fatalf("topic:muse matched a route for muse: %+v", got)
	}
}

// AskNew makes the bead, titled with the question, and the ask on it in
// one write, placed and filled as CreateBead does; whatever the ask or the
// bead would be refused for makes neither.
func TestAskNew(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := decisionYard(t)
	before, err := y.ListBeads(y.ctx, BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	made, err := y.AskNew(y.ctx, NewBead{Body: "The glossary keeps it.", Labels: []string{"topic:words"}, Priority: 2},
		func() Ask { a := linksAsk(); a.Question = "  Rename 'depot'?  "; a.About = []string{y.bead}; return a }(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	b := made.Bead
	if b.Title != "Rename 'depot'?" || b.Body != "The glossary keeps it." || !strings.HasPrefix(b.ID, "yd-") ||
		b.Depot != nil || b.Stage != "new" || b.Status != "open" || len(b.Labels) != 1 || b.Labels[0] != "topic:words" {
		t.Fatalf("bead = %+v", b)
	}
	// A question, which is never ready: nothing routes it (yardr-c9z6).
	if ready, err := y.Dispatchable(y.ctx); err != nil || b.Type != TypeQuestion ||
		slices.ContainsFunc(ready, func(r ReadyBead) bool { return r.Bead == b.ID }) {
		t.Fatalf("the question is a %s, the dispatchable beads %+v, %v", b.Type, ready, err)
	}
	stored, err := y.GetBead(y.ctx, b.ID)
	if err != nil || stored.Revision != b.Revision {
		t.Fatalf("the bead answered is at %v, the bead stored at %+v, %v", b.Revision, stored, err)
	}
	notes, err := y.Notes(y.ctx, b.ID)
	if err != nil || len(notes) != 1 || notes[0].Kind != NoteAsk || notes[0].Seq != made.Ask || notes[0].Author != "yardmaster" {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
	if ask, err := ParseAsk(notes[0]); err != nil || ask.Question != b.Title || ask.To != "mckean" ||
		len(ask.Options) != 3 || len(ask.About) != 1 || ask.About[0] != y.bead {
		t.Fatalf("ask = %+v, %v", ask, err)
	}
	// It stands among the person's decisions under its topic, as any ask.
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil || len(q.Open) != 1 || q.Open[0].Bead != b.ID || q.Open[0].Topic != "topic:words" || q.Open[0].Ask != made.Ask {
		t.Fatalf("mckean's asks = %+v, %v", q, err)
	}

	// Placed as CreateBead places it.
	if err := y.PutDepot(y.ctx, Depot{Name: "web", Kind: DepotDir, Path: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	placed, err := y.AskNew(y.ctx, NewBead{Depot: "web", Stage: "decide"}, linksAsk(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if b := placed.Bead; b.Depot == nil || *b.Depot != "web" || b.Stage != "decide" || b.Type != "task" || !strings.HasPrefix(b.ID, "web-") {
		t.Fatalf("placed bead = %+v", b)
	}

	// Refused whole: by the ask before the write, by the ask inside it, and
	// by the bead.
	open, err := y.ListBeads(y.ctx, BeadFilter{All: true})
	if err != nil || len(open) != len(before)+2 {
		t.Fatalf("%d beads, want %d, %v", len(open), len(before)+2, err)
	}
	for _, c := range []struct {
		name string
		nb   NewBead
		ask  Ask
		want string
	}{
		{"no context", NewBead{}, Ask{Question: "q?", To: "mckean"}, "--context"},
		{"unknown about", NewBead{}, func() Ask { a := linksAsk(); a.About = []string{"yd-none"}; return a }(), "about:"},
		{"no such peer", NewBead{}, func() Ask { a := linksAsk(); a.To = "mckean@nowhere"; return a }(), "nowhere"},
		{"unknown depot", NewBead{Depot: "nope"}, linksAsk(), "depot nope"},
		{"bad type", NewBead{Type: "no type"}, linksAsk(), "bead type"},
		{"unknown blocker", NewBead{BlockedBy: []string{"yd-none"}}, linksAsk(), "yd-none"},
	} {
		if made, err := y.AskNew(y.ctx, c.nb, c.ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: made %+v, err = %v, want %q", c.name, made, err, c.want)
		}
	}
	after, err := y.ListBeads(y.ctx, BeadFilter{All: true})
	if err != nil || len(after) != len(open) {
		t.Fatalf("a refused ask --new left a bead: %d beads, want %d, %v", len(after), len(open), err)
	}
}
