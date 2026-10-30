package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// answerSection is the home page's answer part: the answer line and the
// needs rows, without the counts and the feed after it.
func answerSection(t *testing.T, text string) string {
	t.Helper()
	_, s, _ := strings.Cut(squash(text), `<p class="answer">`)
	s, _, _ = strings.Cut(s, `</section>`)
	if s == "" {
		t.Fatal("the page has no answer line")
	}
	return s
}

// shortID is a bead id as the needs rows show it.
func shortID(id string) string { return web.Waiting{Bead: id}.ShortID() }

// sameReason reports whether two reasons say the same in the JSON: kind,
// text and note. The struct itself is not comparable, since an ask carries
// its options.
func sameReason(a, b web.Reason) bool {
	return a.Kind == b.Kind && a.Text == b.Text && a.Note == b.Note
}

// The top of / answers whether anything needs the person: nothing, one need,
// or needs with the faults counted apart. One waiting bead counts once.
func TestHomeAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	setup := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t)
		must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
		must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual"}, "me"))
		_, err := f.st.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"}, "me")
		must(t, err)
		return f
	}
	held := func(t *testing.T, f *fixture, title string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Stage: "new", Depot: "home"}, "me")
		must(t, err)
		_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "me")
		must(t, err)
		return b
	}
	parked := func(t *testing.T, f *fixture, title string) *store.Bead {
		t.Helper()
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: title, Stage: "decide", Depot: "home"}, "me")
		must(t, err)
		return b
	}

	t.Run("nothing waits", func(t *testing.T) {
		f := setup(t)
		_, err := f.st.CreateBead(ctx, store.NewBead{Title: "at work", Stage: "new", Depot: "home"}, "me")
		must(t, err)
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "Nothing needs you.</p>") {
			t.Errorf("the answer is %q, want nothing-needs-you", s)
		}
		if !strings.Contains(s, `<p class="faint">All clear.</p>`) {
			t.Error("with nothing waiting the page does not say the yard is clear")
		}
		for _, not := range []string{"<li>", "All waiting"} {
			if strings.Contains(s, not) {
				t.Errorf("the answer names %q, and nothing waits", not)
			}
		}
		if h := homeJSON(t, f, "/"); len(h.Attention) != 0 {
			t.Errorf("attention %+v, want none", h.Attention)
		}
	})

	t.Run("one parked", func(t *testing.T) {
		f := setup(t)
		b := parked(t, f, "which colour")
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "1 needs you.</p>") {
			t.Errorf("the answer is %q, want one need and no fault", s)
		}
		want := `<a href="/beads/` + b.ID + `"><span class="stage">decide</span><span class="separator" aria-hidden="true">·</span>` +
			`<span class="entry-title">which colour</span><span class="separator" aria-hidden="true">·</span>` +
			`<span class="label">` + shortID(b.ID) + `</span><span class="faint">`
		if !strings.Contains(s, want) {
			t.Errorf("the row lacks %q", want)
		}
		for _, not := range []string{"dot", "held", "All waiting"} {
			if strings.Contains(s, not) {
				t.Errorf("the parked row has %q", not)
			}
		}
	})

	t.Run("one fault", func(t *testing.T) {
		f := setup(t)
		b := held(t, f, "a door")
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "1 needs you. 1 fault.</p>") {
			t.Errorf("the answer is %q, want one need and one fault", s)
		}
		want := `<a href="/beads/` + b.ID + `"><span class="dot held"></span><span class="held">held</span><span class="separator" aria-hidden="true">·</span>` +
			`<span class="entry-title">a door</span><span class="separator" aria-hidden="true">·</span>` +
			`<span class="label">` + shortID(b.ID) + `</span><span class="faint">`
		if !strings.Contains(s, want) {
			t.Errorf("the row lacks %q", want)
		}
	})

	t.Run("two wait, one fault", func(t *testing.T) {
		f := setup(t)
		parked(t, f, "which colour")
		hb := held(t, f, "a door")
		pb := f.byTitle()["which colour"]
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "2 need you. 1 fault.</p>") {
			t.Errorf("the answer is %q, want two needs and one fault", s)
		}
		// Faults first: the held row stands before the parked one.
		if !strings.Contains(s, hb.ID) || strings.Index(s, hb.ID) > strings.Index(s, pb.ID) {
			t.Error("the held row does not stand before the parked one")
		}
	})

	t.Run("faults plural", func(t *testing.T) {
		f := setup(t)
		held(t, f, "a door")
		held(t, f, "a window")
		parked(t, f, "which colour")
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "3 need you. 2 faults.</p>") {
			t.Errorf("the answer is %q, want three needs and two faults", s)
		}
	})
}

// The answer line in every count, without a yard.
func TestAnswer(t *testing.T) {
	t.Parallel()
	fault := web.Waiting{Bead: "a", Why: []web.Reason{{Kind: "held", Text: "held by me"}}}
	parked := web.Waiting{Bead: "b", Stage: "decide", Why: []web.Reason{{Kind: "manual", Text: "waits"}}}
	stranded := web.Waiting{Bead: "c", Why: []web.Reason{{Kind: "stranded", Text: "stranded"}}}
	for _, c := range []struct {
		name string
		attn []web.Waiting
		want string
	}{
		{"none", nil, "Nothing needs you."},
		{"one parked", []web.Waiting{parked}, "1 needs you."},
		{"one stranded", []web.Waiting{stranded}, "1 needs you."},
		{"one fault", []web.Waiting{fault}, "1 needs you. 1 fault."},
		{"two, one fault", []web.Waiting{fault, parked}, "2 need you. 1 fault."},
		{"three, two faults", []web.Waiting{fault, fault, parked}, "3 need you. 2 faults."},
		{"two parked", []web.Waiting{parked, parked}, "2 need you."},
	} {
		if got := (web.Home{Attention: c.attn}).Answer(); got != c.want {
			t.Errorf("%s: answer %q, want %q", c.name, got, c.want)
		}
	}
}

// The page names five waiting beads; past five the rest are one tap away on
// /line. The JSON names them all either way.
func TestHomeNeedsOverflow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	yard := func(t *testing.T, n int) *fixture {
		t.Helper()
		f := newFixture(t)
		must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
		var beads []*store.Bead
		for range n {
			b, err := f.st.CreateBead(ctx, store.NewBead{Title: "wait", Stage: "new", Depot: "home"}, "me")
			must(t, err)
			beads = append(beads, b)
		}
		// Held in order, so the last held changed last and stands first.
		for _, b := range beads {
			_, err := f.st.SetHold(ctx, b.ID, true, store.Expect{}, "me")
			must(t, err)
		}
		return f
	}

	t.Run("five fit", func(t *testing.T) {
		f := yard(t, 5)
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "5 need you. 5 faults.</p>") {
			t.Errorf("the answer is %q", s)
		}
		if n := strings.Count(s, `<li><a href="/beads/`); n != 5 {
			t.Errorf("the page names %d waits, want five", n)
		}
		if strings.Contains(s, "All waiting") {
			t.Error("five waits fit, and the page still overflows")
		}
	})

	t.Run("past five", func(t *testing.T) {
		f := yard(t, 7)
		s := answerSection(t, f.get("/").body)
		if !strings.HasPrefix(s, "7 need you. 7 faults.</p>") {
			t.Errorf("the answer is %q", s)
		}
		// The five named are the five that changed last, newest first;
		// the two first held are behind the overflow row.
		h := homeJSON(t, f, "/")
		if len(h.Attention) != 7 {
			t.Fatalf("attention names %d waits, want all seven", len(h.Attention))
		}
		var ids []string
		for _, w := range h.Attention {
			ids = append(ids, w.Bead)
		}
		rows := strings.Split(s, `<li><a href="/beads/`)
		if len(rows) != 6 {
			t.Fatalf("the answer holds %d bead rows, want five", len(rows)-1)
		}
		for i := range 5 {
			if id, _, _ := strings.Cut(rows[i+1], `"`); id != ids[i] {
				t.Errorf("row %d names %s, want %s, the line's order", i, id, ids[i])
			}
		}
		for _, id := range ids[5:] {
			if strings.Contains(s, id) {
				t.Errorf("the page names %s, past the five", id)
			}
		}
		if want := `<li><a class="label" href="/line">All waiting 7 ›</a></li>`; !strings.Contains(s, want) {
			t.Errorf("the page lacks %q", want)
		}
		// The JSON is unchanged: attention names every wait, not the five
		// the page shows.
		if l := lineJSON(t, f); len(l.Waits) != 7 {
			t.Errorf("/line waits %d, want all seven", len(l.Waits))
		}
	})
}

// A held row whose bead's events hold a gave_up for that hold names the
// give-up, on / and on /line alike, and still counts once, as a fault.
func TestHomeNeedsGaveUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the github pack", Stage: "review", Depot: "home"}, "me")
	must(t, err)
	_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "yard")
	must(t, err)
	must(t, f.st.LogDispatch(ctx, b.ID, "gave_up", "yard", map[string]any{"stage": "review", "failures": 3}))

	want := web.Reason{Kind: "held", Text: "gave up after 3 failures in review"}
	if got := waiting(lineJSON(t, f)); len(got) != 1 || len(got[0].Why) != 1 || !sameReason(got[0].Why[0], want) {
		t.Errorf("/line waits %+v, want the give-up named", got)
	}
	if h := homeJSON(t, f, "/"); len(h.Attention) != 1 || len(h.Attention[0].Why) != 1 || !sameReason(h.Attention[0].Why[0], want) {
		t.Errorf("attention %+v, want the give-up named", h.Attention)
	}
	// No new field: the reason's JSON is kind and text, as before.
	var l struct {
		Waits []struct {
			Why []map[string]any `json:"why"`
		} `json:"waits"`
	}
	r := f.get("/line", "Accept", "application/json")
	if err := json.Unmarshal([]byte(r.body), &l); err != nil {
		t.Fatal(err)
	}
	for k := range l.Waits[0].Why[0] {
		if k != "kind" && k != "text" {
			t.Errorf("the reason's JSON has %q", k)
		}
	}

	text := squash(f.get("/line").body)
	s := answerSection(t, f.get("/").body)
	if !strings.HasPrefix(s, "1 needs you. 1 fault.</p>") {
		t.Errorf("the answer is %q: the bead counts once, as a fault", s)
	}
	if want := `<span class="dot held"></span><span class="held">gave up</span><span class="separator" aria-hidden="true">·</span>` +
		`<span class="entry-title">the github pack</span>`; !strings.Contains(s, want) {
		t.Errorf("the row lacks %q", want)
	}
	if want := `<p class="why held">gave up after 3 failures in review</p>`; !strings.Contains(text, want) {
		t.Errorf("the column lacks %q", want)
	}

	// A hold lifted and given again, with no new gave_up, is plain again.
	_, err = f.st.SetHold(ctx, b.ID, false, store.Expect{}, "me")
	must(t, err)
	_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "me")
	must(t, err)
	if got := waiting(lineJSON(t, f)); len(got) != 1 || len(got[0].Why) != 1 ||
		!sameReason(got[0].Why[0], web.Reason{Kind: "held", Text: "held by me"}) {
		t.Errorf("held again %+v, want a plain hold", got)
	}
	if s := answerSection(t, f.get("/").body); !strings.Contains(s, `<span class="held">held</span>`) {
		t.Error("the row no longer says held")
	}
}

// A give-up said with what its event has: the dispatcher's has the count
// and the stage, a hand-logged one may have less.
func TestHomeNeedsGaveUpText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "home", Base: "origin/main"}, "me"))
	cases := []struct {
		title string
		data  map[string]any
		want  string
	}{
		{"full", map[string]any{"stage": "new", "failures": 3}, "gave up after 3 failures in new"},
		{"one", map[string]any{"stage": "new", "failures": 1}, "gave up after 1 failure in new"},
		{"no count", map[string]any{"stage": "new"}, "gave up in new"},
		{"no stage", map[string]any{"failures": 2}, "gave up after 2 failures"},
		{"bare", nil, "gave up"},
	}
	for _, c := range cases {
		b, err := f.st.CreateBead(ctx, store.NewBead{Title: c.title, Stage: "new", Depot: "home"}, "me")
		must(t, err)
		_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "yard")
		must(t, err)
		must(t, f.st.LogDispatch(ctx, b.ID, "gave_up", "yard", c.data))
	}
	got := map[string]string{}
	for _, w := range waiting(lineJSON(t, f)) {
		got[w.Title] = w.Why[0].Text
	}
	for _, c := range cases {
		if got[c.title] != c.want {
			t.Errorf("%s says %q, want %q", c.title, got[c.title], c.want)
		}
	}
}

// A row's first words: what happened for a bead something stops, the stage
// for one that only waits its turn.
func TestWaitingLead(t *testing.T) {
	t.Parallel()
	held := web.Reason{Kind: "held", Text: "held by me"}
	gaveUp := web.Reason{Kind: "held", Text: "gave up after 3 failures in new", GaveUp: true}
	blocked := web.Reason{Kind: "blocked", Text: "its session waits on a prompt: x"}
	stranded := web.Reason{Kind: "stranded", Text: "stranded in limbo"}
	manual := web.Reason{Kind: "manual", Text: "waits for decisions in decide"}
	for _, c := range []struct {
		name  string
		why   []web.Reason
		lead  string
		fault bool
	}{
		{"held", []web.Reason{held}, "held", true},
		{"gave up", []web.Reason{gaveUp}, "gave up", true},
		{"blocked", []web.Reason{blocked}, "blocked", true},
		{"stranded", []web.Reason{stranded}, "stranded", false},
		{"manual", []web.Reason{manual}, "decide", false},
		{"held and manual", []web.Reason{held, manual}, "held", true},
		{"manual and held", []web.Reason{manual, held}, "held", true},
		{"stranded and manual", []web.Reason{stranded, manual}, "stranded", false},
		{"none", nil, "decide", false},
	} {
		w := web.Waiting{Bead: "home-x", Stage: "decide", Why: c.why}
		if got := w.Lead(); got != c.lead {
			t.Errorf("%s: lead %q, want %q", c.name, got, c.lead)
		}
		if got := w.Fault(); got != c.fault {
			t.Errorf("%s: fault %v, want %v", c.name, got, c.fault)
		}
	}
	if got := (web.Waiting{Bead: "home-x1y2"}).ShortID(); got != "x1y2" {
		t.Errorf("short id %q, want x1y2", got)
	}
	if got := (web.Waiting{Bead: "nobreak"}).ShortID(); got != "nobreak" {
		t.Errorf("short id %q, want the whole id", got)
	}
}

// A weighty mapping card's home row shows the weight first and the decline
// path with the terminal command instead of the pick buttons, as its bead's
// page does. The options still read as a list, and the words stay with
// Later and Decline, riding them as the reason.
func TestHomeNeedsWeightyMapping(t *testing.T) {
	t.Parallel()
	f, b, seq := mappingActYard(t)
	ctx := context.Background()
	ask := strconv.FormatUint(seq, 10)
	s := answerSection(t, f.get("/").body)
	for _, want := range []string{
		`<p class="weighty">WEIGHTY · as-user: lets calls or words count as a person&#39;s: a pick says which user U777 in talk is, and a delivery from them is then heard as that user&#39;s</p>`,
		`This card is weighty and this window has no gate. Approve it in footplate, or in a terminal on the yard's machine:`,
		`Approve it with <code>yardr bead answer ` + b.ID + ` ` + ask + ` &lt;n&gt;</code>`,
		`<li><span>1. ada</span>`,
		`<li><span>2. mckean</span>`,
		`<input type="text" name="words"`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the row lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, `name="option"`) {
		t.Errorf("the weighty row offers its picks:\n%s", s)
	}
	// A writing pick posted straight at the handler is still the yard's
	// to refuse, and the ask stays open; decline with words still posts.
	rev := revOf(t, f, b.ID)
	r := f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "option": {"1"}}, "Origin", f.ts.URL)
	if r.status != 200 || !strings.Contains(r.body, "is weighty (as-user)") || !strings.Contains(r.body, "approve it where its weight is shown") {
		t.Fatalf("status %d, want the redrawn page with the yard's refusal:\n%s", r.status, r.body)
	}
	r = f.postForm("/beads/"+b.ID, url.Values{"rev": {rev}, "ask": {ask}, "words": {"not now"}, "action": {"decline"}}, "Origin", f.ts.URL)
	if r.status != http.StatusSeeOther {
		t.Fatalf("decline: status %d", r.status)
	}
	v, err := f.st.Bead(ctx, b.ID)
	must(t, err)
	if v.Asks[0].State != "declined" || v.Asks[0].Words != "not now" {
		t.Errorf("the ask is %s with %q, want it declined with the words", v.Asks[0].State, v.Asks[0].Words)
	}
}

// A weighty secret card's home row shows the weight with what the ask is
// for, the decline path with the terminal command as its bead's page does,
// and Later and Decline: a secret row offers no giving.
func TestHomeNeedsWeightySecret(t *testing.T) {
	t.Parallel()
	f, b, seq := secretActYard(t)
	s := answerSection(t, f.get("/").body)
	for _, want := range []string{
		`<p class="weighty">WEIGHTY · credential: gives a credential or an address to the yard: CRM_TOKEN, for the source crm (https://crm.example.com)</p>`,
		`a secret: CRM_TOKEN, for the source crm (https://crm.example.com)`,
		`This card is weighty and this window has no gate. Approve it in footplate, or in a terminal on the yard's machine:`,
		`Give it with <code>yardr bead answer ` + b.ID + ` ` + strconv.FormatUint(seq, 10) + ` --secret</code>`,
		`<button type="submit" name="action" value="later">Later</button>`,
		`<button type="submit" name="action" value="decline" title="Say no to all options. The asker finds another way.">Decline</button>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the row lacks %q:\n%s", want, s)
		}
	}
	for _, foreign := range []string{`name="option"`, `type="password"`, `value="give"`} {
		if strings.Contains(s, foreign) {
			t.Errorf("the secret row shows %q:\n%s", foreign, s)
		}
	}
}
