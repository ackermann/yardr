package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// One subscription says "these kinds, in this depot": a key given more than
// once holds each value, any of which matches, and the keys stay ANDed. So
// the 16 default kinds and a depot are one subscription, which hears that
// depot's session_blocked and not another's, and bare yardr subscribe shows
// it as one row (yardr-03fj).
func TestSubscribeSeveralValuesForOneKey(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "kind=claude", "--on", "held")
	beads := map[string]string{}
	for _, depot := range []string{"web", "api"} {
		if err := st.PutDepot(ctx, store.Depot{Name: depot, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		b, err := st.CreateBead(ctx, store.NewBead{Title: "work", Depot: depot}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads[depot] = b.ID
	}
	args := []string{"subscribe", "clerk", "yardr", "--where", "depot=web"}
	for _, k := range defaultOn {
		args = append(args, "--where", "kind="+k)
	}
	mustAs(t, st, "jo", args...)
	r, err := st.GetCrew(ctx, "clerk")
	if err != nil || len(r.Subscriptions) != 1 {
		t.Fatalf("clerk's subscriptions = %v, %v; want one", r.Subscriptions, err)
	}
	// The same filter again, its values in another order, is the one kept.
	again := []string{"subscribe", "clerk", "yardr"}
	for i := len(defaultOn) - 1; i >= 0; i-- {
		again = append(again, "--where", "kind="+defaultOn[i])
	}
	if out := mustAs(t, st, "jo", append(again, "--where", "depot=web")...); !strings.Contains(out, "already subscribed") {
		t.Errorf("the same values in another order: %s", out)
	}

	for _, depot := range []string{"web", "api"} {
		for _, kind := range []string{"session_blocked", "session_unblocked"} {
			if err := st.LogDispatch(ctx, beads[depot], kind, "yard", map[string]any{"session": "s1"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	heard := func(bead string) string {
		t.Helper()
		evs, err := st.Events(ctx, bead, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			if why := store.Reason(e, "clerk"); why != "" {
				out = append(out, e.Kind+":"+why)
			}
		}
		return strings.Join(out, ",")
	}
	// session_unblocked is none of the kinds, so the depot alone is not enough.
	if got := heard(beads["web"]); got != "session_blocked:all" {
		t.Errorf("clerk heard of its depot %q, want session_blocked:all", got)
	}
	if got := heard(beads["api"]); got != "" {
		t.Errorf("clerk heard of another depot %q, want nothing", got)
	}

	var m subMatrix
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "subscribe", "--json")), &m); err != nil {
		t.Fatal(err)
	}
	var rows []subRow
	for _, row := range m.Rows {
		if row.Subscription.Where["depot"] == "web" {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 || rows[0].Kind != "" || strings.Join(rows[0].Reaches, ",") != "clerk" {
		t.Fatalf("rows of the subscription = %+v, want one that reaches clerk", rows)
	}
	text := mustAs(t, st, "jo", "subscribe")
	if n := strings.Count(text, "depot=web"); n != 1 || !strings.Contains(text, "yardr all depot=web kind=crew_gave_up kind=gave_up kind=held ") {
		t.Errorf("depot=web is in %d rows, want one with each kind:\n%s", n, text)
	}

	if out := mustAs(t, st, "jo", append([]string{"unsubscribe"}, args[1:]...)...); !strings.Contains(out, "1 subscription(s) removed") {
		t.Errorf("unsubscribe with the same filter: %s", out)
	}
}

// A value is all that follows the "=": a comma or a bar in it separates
// nothing, so a label that has one is still one value. A bead takes one
// value, since its subscription ends when it closes.
func TestSubscribeWhereValueIsWhole(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "subscribe", "yardmaster", "yardr", "--where", "label=ui,api", "--where", "label=a|b", "--where", "label=line1\nline2")
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil || len(r.Subscriptions) != 1 {
		t.Fatalf("subscriptions = %v, %v", r, err)
	}
	if got := store.WhereValues(r.Subscriptions[0].Where["label"]); strings.Join(got, " & ") != "a|b & line1\nline2 & ui,api" {
		t.Errorf("label values = %q, want a|b, line1\\nline2 and ui,api", got)
	}
	if got := r.Subscriptions[0].String(); strings.Contains(got, "\n") || !strings.Contains(got, `label=line1\nline2`) {
		t.Errorf("subscription string = %q, want one row with the line break escaped", got)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		b, err := st.CreateBead(ctx, store.NewBead{Title: "work", Depot: "web"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	refusedAs(t, st, "jo", "2 beads in one subscription", "subscribe", "yardmaster", "yardr", "--where", "bead="+ids[0], "--where", "bead="+ids[1])
}

// A kind somebody has among the several of a subscription that filters on
// kinds alone reaches someone, so it is not listed as reaching no one.
func TestSubscribeMatrixReachesThroughSeveralKinds(t *testing.T) {
	crew := []store.CrewMember{{Name: "clerk", Subscriptions: []store.Subscription{
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"kind": store.JoinWhere([]string{"stranded", "unrouted"})}},
		{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"kind": store.JoinWhere([]string{"held", "gave_up"}), "depot": "web"}},
	}}}
	m := subscriptionMatrix(crew, nil, nil)
	if len(m.Rows) != 2 || m.Rows[0].Kind != "" || m.Rows[1].Kind != "" {
		t.Fatalf("rows = %+v, want two, neither a --on kind's", m.Rows)
	}
	no := strings.Join(m.ReachesNoOne, ",")
	if strings.Contains(no, "stranded") || strings.Contains(no, "unrouted") {
		t.Errorf("reaches no one = %s, with kinds clerk subscribes to", no)
	}
	// Narrowed to a depot, a kind still reaches no one elsewhere.
	if !strings.Contains(no, "held") || !strings.Contains(no, "gave_up") {
		t.Errorf("reaches no one = %s, without the kinds clerk has for one depot only", no)
	}
}
