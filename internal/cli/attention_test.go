package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A crew member added without --on hears the class by its name, and bare
// yardr subscribe shows that as one row, not one for each kind of it. No
// kind of the class reaches no one while someone hears the class; when
// nobody does any more, each kind nobody has is listed again (yardr-qcm2).
func TestSubscribeMatrixShowsTheClassAsOneRow(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	// init made the yardmaster, with the default --on.
	mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "kind=claude")
	for _, name := range []string{"yardmaster", "clerk"} {
		r, err := st.GetCrew(ctx, name)
		if err != nil || !slices.Equal(r.On, []string{store.KindAttention}) {
			t.Fatalf("%s hears %v, %v; want the class by its name", name, r, err)
		}
	}
	if show := mustAs(t, st, "jo", "crew", "show", "clerk"); !strings.Contains(show, " · on attention\n") {
		t.Errorf("crew show does not name the class:\n%s", show)
	}
	matrix := func() (string, subMatrix) {
		t.Helper()
		var m subMatrix
		if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "subscribe", "--json")), &m); err != nil {
			t.Fatal(err)
		}
		return mustAs(t, st, "jo", "subscribe"), m
	}
	text, m := matrix()
	// The class is one row; the operator's failure subscription is the
	// yard's other row.
	if len(m.Rows) != 2 || m.Rows[0].Subscription.String() != "yardr all kind=attention" || m.Rows[0].Kind != store.KindAttention ||
		!slices.Equal(m.Rows[0].Reaches, []string{"clerk", "yardmaster"}) ||
		m.Rows[1].Subscription.String() != "yardr all kind=install_failed kind=rolled_back" ||
		!slices.Equal(m.Rows[1].Reaches, []string{"jo"}) || len(m.ReachesNoOne) != 0 {
		t.Fatalf("matrix = %+v, want the class as one row and the operator's subscription, and no kind that reaches no one:\n%s", m, text)
	}
	if strings.Contains(text, "reaches no one") || strings.Contains(text, "kind=held") {
		t.Errorf("the class is not one row:\n%s", text)
	}

	// With a depot beside it the class is one depot's: a row of its own,
	// which does not stand for hearing the kinds yard-wide.
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "subscribe", "clerk", "yardr", "--where", "depot=web", "--where", "kind=attention")
	mustAs(t, st, "jo", "crew", "set", "yardmaster", "--on", "gave_up")
	mustAs(t, st, "jo", "crew", "set", "clerk", "--on", "held")
	text, m = matrix()
	var want []string
	for _, k := range store.AttentionKinds {
		// The operator's default subscription reaches the failure kinds.
		if k != "gave_up" && k != "held" && k != store.InstallFailed && k != store.RolledBack {
			want = append(want, k)
		}
	}
	// nudge_held is of the class: an input held by a harness's dialog needs someone (yardr-vsen).
	if !slices.Contains(want, "nudge_held") {
		t.Errorf("nudge_held is not of the class attention: %v", store.AttentionKinds)
	}
	if !slices.Equal(m.ReachesNoOne, want) || !strings.Contains(text, "): "+strings.Join(want, ", ")+"\n") ||
		!strings.Contains(text, "yardr all depot=web kind=attention ") {
		t.Errorf("reaches no one = %v, want %v, in:\n%s", m.ReachesNoOne, want, text)
	}

	// A subscription to the class and nothing else reaches someone with
	// every kind of it, as --on does.
	mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "kind=attention")
	if text, m = matrix(); len(m.ReachesNoOne) != 0 {
		t.Errorf("reaches no one = %v with a user subscribed to the class:\n%s", m.ReachesNoOne, text)
	}
}

// doctor names the crew member whose --on is a copy of a default list of
// before the class, with the one command that moves it to the class and
// what it would gain. It rewrites none: a list someone shortened on purpose
// looks the same. A crew member on the class, and one given a few kinds,
// get no note (yardr-qcm2).
func TestDoctorNotesACopiedDefaultOn(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	for name, on := range map[string][]string{
		"byname": {store.KindAttention},
		"few":    {"held", "gave_up"},
		// The first default, a kind that joined later, and one of its own.
		"early": append(slices.Clone(firstDefaultOn), "runtime_down", "peer_bead_arrived"),
		"today": slices.Clone(store.AttentionKinds),
	} {
		if _, err := st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", On: on}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := st.Crew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err := readState(ctx, &config.Config{Home: t.TempDir()}, st, rs)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CopiedOn) != 2 {
		t.Fatalf("notes %q, want one for early and one for today", state.CopiedOn)
	}
	early, today := state.CopiedOn[0], state.CopiedOn[1]
	for _, want := range []string{"crew member early has a copy of an earlier default --on",
		"It does not hear stranded, session_idle, session_aged, session_stalled, session_harness_error, session_harness_dialog, session_model_mismatch, session_prompt_gave_up, move_refused, make_refused, held, waited, nudge_held, backup_failed, note_untold, install_failed, rolled_back, hook_quiet, quiet_failed, base_diverged. ",
		" crew set early --on attention,peer_bead_arrived"} {
		if !strings.Contains(early, want) {
			t.Errorf("the note for early lacks %q:\n%s", want, early)
		}
	}
	if !strings.Contains(today, "It hears every kind of the class today. ") || !strings.HasSuffix(today, " crew set today --on attention") {
		t.Errorf("the note for today:\n%s", today)
	}
	var lines []string
	for _, l := range state.lines(t.TempDir()) {
		lines = append(lines, l.text)
	}
	if text := strings.Join(lines, "\n"); !strings.Contains(text, early) || !strings.Contains(text, today) {
		t.Errorf("doctor does not print the notes:\n%s", text)
	}

	// The command the note gives is taken, and the note is gone.
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "early", Runner: "herdr", On: []string{store.KindAttention, "peer_bead_arrived"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if rs, err = st.Crew(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err = readState(ctx, &config.Config{Home: t.TempDir()}, st, rs); err != nil || len(state.CopiedOn) != 1 {
		t.Fatalf("notes %q, %v; want the one for today", state.CopiedOn, err)
	}
}
