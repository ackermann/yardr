package web_test

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

func TestUnified(t *testing.T) {
	t.Parallel()
	seq := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, "l%d\n", i)
		}
		return b.String()
	}
	for _, c := range []struct{ name, old, new, want string }{
		{"the same", "a\nb\n", "a\nb\n", ""},
		{"a final newline is no line", "a\nb", "a\nb\n", ""},
		{"from nothing", "", "a\nb\n", "--- was\n+++ is\n@@ -0,0 +1,2 @@\n+a\n+b\n"},
		{"to nothing", "a\n", "", "--- was\n+++ is\n@@ -1,1 +0,0 @@\n-a\n"},
		{"one line of three", "a\nb\nc\n", "a\nB\nc\n", "--- was\n+++ is\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		// Three lines around a change, and two changes far apart are two hunks.
		{"two hunks", seq(1, 20), strings.Replace(strings.Replace(seq(1, 20), "l2\n", "", 1), "l18\n", "l18\nnew\n", 1),
			"--- was\n+++ is\n@@ -1,5 +1,4 @@\n l1\n-l2\n l3\n l4\n l5\n@@ -16,5 +15,6 @@\n l16\n l17\n l18\n+new\n l19\n l20\n"},
		// Six lines between two changes: one hunk holds both.
		{"one hunk", seq(1, 10), strings.Replace(strings.Replace(seq(1, 10), "l2\n", "", 1), "l9\n", "", 1),
			"--- was\n+++ is\n@@ -1,10 +1,8 @@\n l1\n-l2\n l3\n l4\n l5\n l6\n l7\n l8\n-l9\n l10\n"},
	} {
		if got := web.Unified(c.old, c.new); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

func TestUpdatedWording(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		data map[string]any
		want string
	}{
		{map[string]any{"title": "the door", "priority": 1.0, "body": true, "depot": "line",
			"was": map[string]any{"title": "a door", "priority": 2.0, "body": "x", "depot": ""}},
			`title "a door" → "the door" · priority 2 → 1 · depot none → line · body`},
		// Set to what it was: nothing changed there.
		{map[string]any{"title": "a door", "type": "bug", "was": map[string]any{"title": "a door", "type": "task"}}, "type task → bug"},
		{map[string]any{"labels_added": []any{"wood"}, "labels_removed": []string{"iron"}, "was": map[string]any{"labels": []any{"iron"}}},
			"labels +wood -iron"},
		{map[string]any{"metadata": map[string]any{"a": "1", "b": "2", "c": "3"},
			"was": map[string]any{"metadata": map[string]any{"a": nil, "b": "0", "c": "3"}}}, "metadata a=1 · metadata b 0 → 2"},
		// An update from before events kept what a field was.
		{map[string]any{"title": "the door", "body": true}, `title "the door" · body`},
		{nil, ""},
	} {
		if got := web.Updated(c.data); got != c.want {
			t.Errorf("%v: %q, want %q", c.data, got, c.want)
		}
	}
}

// edited is a bead with a history: made, its body written, then title,
// priority and body changed, noted and moved.
func edited(t *testing.T, st store.API) *store.Bead {
	t.Helper()
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\n", Stage: "new", Priority: 2}, "me")
	must(t, err)
	body := "Hang it.\n- on two hinges\n"
	_, err = st.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "jo")
	must(t, err)
	title, pri, body2 := "the <door>", 1, "Hang it.\n- on three hinges\n"
	_, err = st.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title, Priority: &pri, Body: &body2}, store.Expect{}, "ann")
	must(t, err)
	must(t, st.AddNote(ctx, b.ID, "hung", "ann"))
	b, err = st.Advance(ctx, b.ID, "review", store.Expect{}, "ann")
	must(t, err)
	return b
}

// written is s as a page writes text: html/template escapes a plus too.
func written(s string) string {
	return strings.ReplaceAll(template.HTMLEscapeString(s), "+", "&#43;")
}

const (
	firstDiff  = "--- was\n+++ is\n@@ -1,1 +1,2 @@\n Hang it.\n+- on two hinges\n"
	secondDiff = "--- was\n+++ is\n@@ -1,2 +1,2 @@\n Hang it.\n-- on two hinges\n+- on three hinges\n"
)

// The bead's page carries its history, the oldest event first: each as the
// feed words it, and under an update of the body, the diff.
func TestBeadHistoryJSON(t *testing.T) {
	t.Parallel()
	// The old values a diff is said from are the operator's to read, so
	// the page is read as the operator's.
	f, _ := talkFixture(t, "ada")
	must(t, f.st.SetSetting(context.Background(), store.OperatorSetting, "ada", "ada"))
	b := edited(t, f.st)
	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	type line struct{ kind, actor, text, note, diff string }
	var lines []line
	for i, c := range got.History {
		if c.Bead != b.ID || c.Seq == 0 || c.At.IsZero() || i > 0 && c.Seq <= got.History[i-1].Seq {
			t.Errorf("change %d: %+v", i, c)
		}
		lines = append(lines, line{c.Kind, c.Actor, c.Text, c.Note, c.Diff})
	}
	want := []line{
		{"created", "me", "created", "", ""},
		{"updated", "jo", "updated", "body", firstDiff},
		{"updated", "ann", "updated", `title "a door" → "the <door>" · priority 2 → 1 · body`, secondDiff},
		{"noted", "ann", "noted", "hung", ""},
		{"advanced", "ann", "moved from new to review", "hung", ""},
	}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Errorf("history:\n%q\nwant\n%q", lines, want)
	}
}

// The page shows the history, written as text, and shows a later update
// when it is read again, which it is at the stream's next event.
func TestBeadHistoryHTML(t *testing.T) {
	t.Parallel()
	// As the JSON is: the old values a diff is said from are the
	// operator's to read.
	f, _ := talkFixture(t, "ada")
	ctx := context.Background()
	must(t, f.st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"))
	b := edited(t, f.st)
	head, err := f.st.Head(ctx)
	must(t, err)
	r := f.get("/beads/" + b.ID)
	for _, want := range []string{
		`<section class="item" id="history">`,
		`<p class="entry-note">` + written(`title "a door" → "the <door>" · priority 2 → 1 · body`) + `</p>`,
		`<pre class="text file">` + written(firstDiff) + `</pre>`,
		`<pre class="text file">` + written(secondDiff) + `</pre>`,
		`<p>moved from new to review</p>`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if strings.Contains(r.body, "the <door>") {
		t.Error("a title in the history is not escaped")
	}
	if first, second := strings.Index(r.body, written("+- on two hinges")), strings.Index(r.body, written("+- on three hinges")); first < 0 || second < first {
		t.Error("the history is not oldest first")
	}

	// Live: the update is an event of the stream the page listens to, and
	// the page it then reads has it.
	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=seq", head))
	body := "Hang it.\n- on three hinges\n- oiled\n"
	_, err = f.st.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "jo")
	must(t, err)
	if fr := s.next(t); fr.id != head+1 {
		t.Fatalf("the update's frame: %+v", fr)
	}
	if r := f.get("/beads/" + b.ID); !strings.Contains(r.body, written("+- oiled\n")+"</pre>") {
		t.Error("the page read again lacks the new update")
	}
}

// bare is a store whose first update of a bead's body is as events were
// before they kept what a field was.
type bare struct{ store.API }

func (s bare) Events(ctx context.Context, bead string, limit int) ([]store.Event, error) {
	evs, err := s.API.Events(ctx, bead, limit)
	for _, e := range evs {
		if e.Kind == "updated" {
			delete(e.Data, "was")
			break
		}
	}
	return evs, err
}

// A bead's history is told as its viewer is told it: the old values an
// update's words and its diff are said from are the operator's, so a
// member's history, and nobody's, stands without them.
func TestHistoryTellsEachViewerTheirOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"))
	must(t, func() error { _, err := f.st.AddUser(ctx, "sam", "ada"); return err }())
	b := edited(t, f.st)
	op, err := web.History(ctx, f.st, b.ID, "ada")
	must(t, err)
	if len(op) != 5 || op[2].Diff != secondDiff || !strings.Contains(op[2].Note, "→") {
		t.Errorf("the operator's history: %+v", op)
	}
	for _, viewer := range []string{"sam", ""} {
		h, err := web.History(ctx, f.st, b.ID, viewer)
		must(t, err)
		if len(h) != 5 {
			t.Fatalf("%q's history: %+v", viewer, h)
		}
		for _, c := range h {
			if c.Diff != "" {
				t.Errorf("%q's history diffs: %+v", viewer, c)
			}
		}
		if strings.Contains(h[2].Note, "→") {
			t.Errorf("%q's update names old values: %q", viewer, h[2].Note)
		}
	}
}

// An update from before events kept the old body has no diff, and takes
// none from the updates after it; a bead nobody changed has its making; an
// unknown bead is an error.
func TestHistoryOfOldEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	// The old values a diff is said from are the operator's to read, so
	// the history is read as the operator's.
	must(t, f.st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"))
	b := edited(t, f.st)
	h, err := web.History(ctx, bare{f.st}, b.ID, "ada")
	must(t, err)
	if len(h) != 5 || h[1].Diff != "" || h[1].Note != "body" || h[2].Diff != secondDiff {
		t.Errorf("history: %+v", h)
	}
	plain := bead(t, f.st, "plain")
	if h, err := web.History(ctx, f.st, plain.ID, "ada"); err != nil || len(h) != 1 || h[0].Text != "created" {
		t.Errorf("a new bead's history: %+v, %v", h, err)
	}
	if _, err := web.History(ctx, f.st, "none-zzzz", "ada"); err == nil {
		t.Error("an unknown bead has a history")
	}
}
