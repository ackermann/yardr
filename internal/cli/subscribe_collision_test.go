package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A manual group and a crew member may share a name. Unsubscribing such a
// name removes the matching subscriptions from whichever of the two has
// them; when both do it refuses naming both, and --crew or --group says
// which (yardr-f0ez).
func TestUnsubscribeSharedName(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	// Commands run as the operator here: the shared names' subscriptions
	// are the operator's to change, and the tests are the shared-name
	// mechanics, not the gates.
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	sub := store.Subscription{Source: "acme", Scope: store.ScopeAll}
	if err := st.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "reviewers"}, "me"); err != nil {
		t.Fatal(err)
	}
	// Only the group subscribes: the group's subscription goes.
	if out, err := run(t, a.unsubscribeCmd(), "reviewers", "acme"); err != nil ||
		!strings.Contains(out, "reviewers: 1 subscription(s) removed") {
		t.Fatalf("unsubscribe the group's only: %q %v", out, err)
	}
	if g, _ := st.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions after unsubscribe = %v", g.Subscriptions)
	}
	// Both subscribe: refused, naming both.
	if err := st.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscriptionKind(ctx, "reviewers", "group", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.unsubscribeCmd(), "reviewers", "acme"); err == nil ||
		!strings.Contains(err.Error(), "reviewers is a crew member and a manual group") {
		t.Fatalf("unsubscribe both = %q, %v; want a refusal naming both", out, err)
	}
	// The flags take one alone.
	if out, err := run(t, a.unsubscribeCmd(), "reviewers", "acme", "--group"); err != nil ||
		!strings.Contains(out, "1 subscription(s) removed") {
		t.Fatalf("unsubscribe --group: %q %v", out, err)
	}
	if g, _ := st.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions after --group = %v", g.Subscriptions)
	}
	if r, _ := st.GetCrew(ctx, "reviewers"); len(r.Subscriptions) != 1 {
		t.Fatalf("crew's subscriptions after --group = %v", r.Subscriptions)
	}
	if out, err := run(t, a.unsubscribeCmd(), "reviewers", "acme", "--crew"); err != nil ||
		!strings.Contains(out, "1 subscription(s) removed") {
		t.Fatalf("unsubscribe --crew: %q %v", out, err)
	}
	if _, err := run(t, a.unsubscribeCmd(), "reviewers", "acme", "--crew", "--group"); err == nil ||
		!strings.Contains(err.Error(), "one of --crew or --group") {
		t.Fatalf("unsubscribe with both flags = %v, want a refusal", err)
	}
}

// Subscribing a name a crew member and a manual group share subscribes the
// crew member; --group subscribes the group.
func TestSubscribeSharedNameFlags(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	// Commands run as the operator here: the shared names' subscriptions
	// are the operator's to change, and the tests are the shared-name
	// mechanics, not the gates.
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "reviewers"}, "me"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.subscribeCmd(), "reviewers", "yardr", "--done"); err != nil ||
		!strings.Contains(out, "reviewers subscribed: yardr all state=done") {
		t.Fatalf("subscribe a shared name: %q %v", out, err)
	}
	if r, _ := st.GetCrew(ctx, "reviewers"); len(r.Subscriptions) != 1 {
		t.Fatalf("crew's subscriptions = %v, want the one", r.Subscriptions)
	}
	if g, _ := st.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions = %v, want none", g.Subscriptions)
	}
	if out, err := run(t, a.subscribeCmd(), "reviewers", "yardr", "--done", "--group"); err != nil ||
		!strings.Contains(out, "reviewers subscribed: yardr all state=done") {
		t.Fatalf("subscribe --group: %q %v", out, err)
	}
	if g, _ := st.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 1 {
		t.Fatalf("group's subscriptions after --group = %v", g.Subscriptions)
	}
	if out, err := run(t, a.subscribeCmd(), "reviewers", "yardr", "--done", "--crew"); err != nil ||
		!strings.Contains(out, "already subscribed") {
		t.Fatalf("subscribe --crew again: %q %v", out, err)
	}
	if _, err := run(t, a.subscribeCmd(), "reviewers", "yardr", "--crew", "--group"); err == nil ||
		!strings.Contains(err.Error(), "one of --crew or --group") {
		t.Fatalf("subscribe with both flags = %v, want a refusal", err)
	}
}

// Source rm prints cleanup commands that work under a shared name: one
// with each flag when a crew member and a manual group both subscribe.
func TestSourceRmSharedNameCommands(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	// Commands run as the operator here: the shared names' subscriptions
	// are the operator's to change, and the tests are the shared-name
	// mechanics, not the gates.
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "acme.yaml", hookDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	sub := store.Subscription{Source: "acme", Scope: store.ScopeAll}
	if err := st.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "reviewers"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "reviewers", sub, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "rm", "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"yardr unsubscribe reviewers acme --crew",
		"yardr unsubscribe reviewers acme --group",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("source rm printed no %q:\n%s", want, out)
		}
	}
	for _, flag := range []string{"--crew", "--group"} {
		if _, err := run(t, a.unsubscribeCmd(), "reviewers", "acme", flag); err != nil {
			t.Fatalf("the printed %s command: %v", flag, err)
		}
	}
	if r, _ := st.GetCrew(ctx, "reviewers"); len(r.Subscriptions) != 0 {
		t.Fatalf("crew's subscriptions after the cleanup = %v", r.Subscriptions)
	}
	if g, _ := st.GetGroup(ctx, "reviewers"); len(g.Subscriptions) != 0 {
		t.Fatalf("group's subscriptions after the cleanup = %v", g.Subscriptions)
	}
}
