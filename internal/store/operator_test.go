package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// operatorYard is a yard whose operator is jo, with the user ada, the crew
// members yardmaster and clerk, and the peer north.
func operatorYard(t *testing.T) (store.API, *store.Store) {
	t.Helper()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"yardmaster", "clerk"} {
		if _, err := api.PutCrew(ctx, store.CrewMember{Name: r, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.AddPeer(ctx, store.Peer{Name: "north", Key: peerKey(t), URL: "https://north.example"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return api, own
}

func peerKey(t *testing.T) string {
	t.Helper()
	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	return yardkey.Public(key)
}

// What only the operator may do is refused by the store itself, in one
// text, to a crew member, another user and a bead's agent alike: a client
// that asks nothing first (a pack's import, the web, yardr chat) is held to
// it as yardr's own commands are (yardr-1379).
func TestOnlyTheOperatorMay(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	key := peerKey(t)
	grant := store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{store.OpenThreads: "linear", store.MailPeers: "north"}}
	if _, err := api.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/h1.secret"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// A live process that holds no daemon lock: the daemon-or-operator
	// write refuses it as a non-operator, in both modes (over the socket
	// the test process itself holds the lock, so it goes through own,
	// where the caller stands as named).
	other := store.WithCaller(ctx, os.Getppid())
	writes := map[string]func(actor string) error{
		"add a user":    func(a string) error { _, err := api.AddUser(ctx, "grace", a); return err },
		"remove a user": func(a string) error { return api.DeleteUser(ctx, "ada", a) },
		"add a peer": func(a string) error {
			_, err := api.AddPeer(ctx, store.Peer{Name: "south", Key: key}, a)
			return err
		},
		"remove a peer":                 func(a string) error { return api.DeletePeer(ctx, "north", a) },
		"rename a peer":                 func(a string) error { _, err := api.RenamePeer(ctx, "north", "up", a); return err },
		"change where a peer is called": func(a string) error { _, err := api.SetPeerURL(ctx, "north", "", a); return err },
		"allow a peer's messages": func(a string) error {
			_, err := api.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{store.PeerMail}}, a)
			return err
		},
		"expose someone of the yard to a peer": func(a string) error {
			_, err := api.AllowPeer(ctx, "north", store.PeerAllow{Expose: []string{"clerk"}}, a)
			return err
		},
		// The source is none the yard declares: the operator gets past the
		// check that is on trial to the next one (the loop below asks only
		// that it is not the operator's refusal).
		"say who a peer's crew member is in a source": func(a string) error {
			_, err := api.SetPeerIdentity(ctx, "north", store.PeerIdentity{Source: "nowhere", ID: "u-1", Crew: "mayor"}, a)
			return err
		},
		"take back who a peer's crew member is in a source": func(a string) error {
			_, err := api.RemovePeerIdentity(ctx, "north", "mayor", "nowhere", a)
			return err
		},
		"pair this yard with another": func(a string) error { _, err := api.PairOpen(ctx, store.PairOffer{}, a); return err },
		"let clerk open threads in linear": func(a string) error {
			_, err := api.PutCrew(ctx, grant, a)
			return err
		},
		"let yardmaster open threads in github": func(a string) error {
			_, err := api.AllowOpening(ctx, "yardmaster", []string{"github"}, "pack", a)
			return err
		},
		"remove a depot": func(a string) error { _, err := api.DeleteDepot(ctx, "no-such", a); return err },
		"change a depot's path": func(a string) error {
			return api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/other", Base: "origin/main"}, a)
		},
		"change a depot's test command": func(a string) error {
			return api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main", Test: "make test"}, a)
		},
		"change a depot's base": func(a string) error {
			return api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/other"}, a)
		},
		"remove a hook": func(a string) error { return api.DeleteHook(ctx, "no-such", a) },
		"move links from one source to another": func(a string) error {
			_, err := api.MoveLinkSource(ctx, "acme", "acme2", a)
			return err
		},
		"move a source's records": func(a string) error {
			_, err := api.MoveSourceRecords(ctx, "acme", "acme2", a)
			return err
		},
		"add a hook": func(a string) error {
			_, err := api.PutHook(ctx, store.Hook{Name: "h2", Source: "linear", SecretFile: "/tmp/h2.secret"}, a)
			return err
		},
		"change a hook's source or secret file": func(a string) error {
			_, err := api.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/other.secret"}, a)
			return err
		},
		"prune the store": func(a string) error { _, err := own.Prune(other, store.Retention{}, a); return err },
	}
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for does, write := range writes {
		for _, actor := range []string{"yardmaster", "ada", "yard-10-new", ""} {
			want := "only the operator (jo) may " + does + "; this is " + map[bool]string{true: "nobody", false: actor}[actor == ""]
			if err := write(actor); err == nil || err.Error() != want {
				t.Errorf("%s as %q = %v, want %q", does, actor, err, want)
			}
		}
	}
	if now, _ := api.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	// Mail to another yard is a crew member's that was allowed the peer,
	// or a user's, the operator among them.
	if _, err := api.MailPeer(ctx, "scout", "north", "hello", "yard-10-new"); err == nil ||
		!strings.Contains(err.Error(), "or a user of the yard (yardr user list) may mail another yard; this is yard-10-new") {
		t.Errorf("mail to a peer as a bead's agent = %v", err)
	}
	if _, err := api.MailPeer(ctx, "scout", "north", "hello", "jo"); err == nil || strings.Contains(err.Error(), "only") {
		t.Errorf("mail to a peer as the operator, over a link that carries none = %v", err)
	}

	// What keeps or narrows is anyone's, as the rest of a definition is.
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"model": "opus"}}, "yardmaster"); err != nil {
		t.Errorf("a definition that grants nothing, as yardmaster: %v", err)
	}
	if _, err := api.AllowPeer(ctx, "north", store.PeerAllow{NoSend: []string{store.PeerMail}}, "yardmaster"); err != nil {
		t.Errorf("stopping a kind, as yardmaster: %v", err)
	}
	if _, err := api.AllowPeer(ctx, "north", store.PeerAllow{NoExpose: []string{"clerk"}}, "yardmaster"); err != nil {
		t.Errorf("exposing one no longer, as yardmaster: %v", err)
	}
	if _, err := api.SetPeerIntake(ctx, "north", nil, "yardmaster"); err != nil {
		t.Errorf("taking none of a peer's beads, as yardmaster: %v", err)
	}
	if err := api.SetCrewEnabled(ctx, "clerk", false, "", "yardmaster"); err != nil {
		t.Errorf("disabling a crew member, as yardmaster: %v", err)
	}
	// The operator.
	if _, err := api.PutCrew(ctx, grant, "jo"); err != nil {
		t.Fatalf("the grant, as the operator: %v", err)
	}
	if _, err := api.PutCrew(ctx, grant, "yardmaster"); err != nil {
		t.Errorf("a definition that keeps what was granted, as yardmaster: %v", err)
	}
	for does, write := range writes {
		if err := write("jo"); err != nil && strings.Contains(err.Error(), "only the operator") {
			t.Errorf("%s as the operator = %v", does, err)
		}
	}
}

// Several users may be operators: the role, not the operator setting, is
// what every operator gate reads. An operator alone gives or takes it, and
// the yard keeps its operator setting's user and its last operator one.
func TestSeveralOperators(t *testing.T) {
	t.Parallel()
	api, _ := operatorYard(t)
	ctx := context.Background()
	role := func(name, r, actor string) error {
		_, err := api.SetUser(ctx, name, map[string]string{store.UserRole: r}, actor)
		return err
	}
	if _, err := api.AddUser(ctx, "bob", "jo"); err != nil {
		t.Fatal(err)
	}
	// Nobody gives themselves the role, and a role is one of two.
	if err := role("ada", store.RoleOperator, "ada"); err == nil || err.Error() != "only the operator (jo) may change a user's role; this is ada" {
		t.Fatalf("ada's own role = %v", err)
	}
	if err := role("ada", "boss", "jo"); err == nil || !strings.Contains(err.Error(), `role="boss": a user's role is operator or member`) {
		t.Fatalf("a role that is none = %v", err)
	}
	if err := role("ada", store.RoleOperator, "jo"); err != nil {
		t.Fatal(err)
	}
	// ada now does what an operator does; bob, a member, is refused in
	// words naming both operators.
	if _, err := api.AddUser(ctx, "grace", "ada"); err != nil {
		t.Fatalf("a user add by the operator ada: %v", err)
	}
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/other", Base: "origin/main"}, "ada"); err != nil {
		t.Fatalf("a depot's path changed by the operator ada: %v", err)
	}
	if _, err := api.AddUser(ctx, "kim", "bob"); err == nil || err.Error() != "only an operator (ada, jo) may add a user; this is bob" {
		t.Fatalf("a user add by the member bob = %v", err)
	}
	if err := role("bob", store.RoleOperator, "bob"); err == nil || err.Error() != "only an operator (ada, jo) may change a user's role; this is bob" {
		t.Fatalf("bob's own role = %v", err)
	}
	// The user the operator setting names keeps the role and the record:
	// asks with no --to go to them.
	if err := role("jo", store.RoleMember, "ada"); err == nil || !strings.Contains(err.Error(), "user jo is the yard's operator and is not made a member; name another operator first") {
		t.Fatalf("the setting's operator made a member = %v", err)
	}
	if err := api.DeleteUser(ctx, "jo", "ada"); err == nil || !strings.Contains(err.Error(), "user jo is the yard's operator and is not removed") {
		t.Fatalf("the setting's operator removed = %v", err)
	}
	// Naming bob in the setting makes him an operator, and jo can go.
	if err := api.SetSetting(ctx, store.OperatorSetting, "bob", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := role("jo", store.RoleMember, "ada"); err != nil {
		t.Fatalf("jo made a member once the setting names bob: %v", err)
	}
	us, err := api.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Operators(us); !slices.Equal(got, []string{"ada", "bob"}) {
		t.Fatalf("operators = %v, want ada and bob", got)
	}
	// With the setting cleared, the last operator stays one.
	if err := role("ada", store.RoleMember, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := api.SetSetting(ctx, store.OperatorSetting, "", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := role("bob", store.RoleMember, "bob"); err == nil || !strings.Contains(err.Error(), "user bob is the yard's last operator and is not made a member; make another user an operator first (yardr user set <user> role=operator)") {
		t.Fatalf("the last operator made a member = %v", err)
	}
	if err := api.DeleteUser(ctx, "bob", "bob"); err == nil || !strings.Contains(err.Error(), "user bob is the yard's last operator and is not removed") {
		t.Fatalf("the last operator removed = %v", err)
	}
	if _, err := api.AddUser(ctx, "lee", "jo"); err == nil || err.Error() != "only the operator (bob) may add a user; this is jo" {
		t.Fatalf("a user add by jo, a member now = %v", err)
	}
}

// Removing a route is the operator's or the yardmaster's: the crew
// member the yardmaster setting names removes one as the operator does,
// so changing a route (rm plus add) leaves no duplicates behind. Anyone
// else is refused in words naming both, and the pane's actor is who is
// checked, whatever --actor says (yardr-g7kq).
func TestRouteRemoveIsTheOperatorsAndYardmasters(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "brakeman", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Each route asks for a label of its own: two that take the same
	// beads are refused as a tie, and removal is what is on trial here.
	n := 0
	add := func(group string) string {
		t.Helper()
		n++
		label := fmt.Sprint("l", n)
		r, err := api.AddRoute(ctx, store.Route{Stage: "new", Label: &label, Group: group}, "jo")
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	ids := func() []string {
		t.Helper()
		rs, err := api.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.ID
		}
		return out
	}
	const refuse = "only the operator (jo) or the yardmaster (yardmaster) may remove a route"
	// The yardmaster removes, as named: the yardmaster setting is unset,
	// so the default names them.
	if err := api.DeleteRoute(ctx, add("builders"), "yardmaster"); err != nil {
		t.Fatalf("DeleteRoute as yardmaster: %v", err)
	}
	if actor := lastKindActor(t, api, ctx, "route_removed"); actor != "yardmaster" {
		t.Fatalf("route_removed actor = %q, want yardmaster", actor)
	}
	// Another crew member is refused, the route stays, and the refusal
	// keeps the alone mark: no run ask proposes it.
	kept := add("builders")
	if err := api.DeleteRoute(ctx, kept, "brakeman"); err == nil || err.Error() != refuse {
		t.Fatalf("DeleteRoute as brakeman = %v, want %q", err, refuse)
	}
	if err := own.DeleteRoute(ctx, "no-such", "brakeman"); err == nil {
		t.Fatalf("DeleteRoute as brakeman through own = nil")
	} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
		t.Fatalf("DeleteRoute as brakeman = %v (mark %v, %v), want an operator-only refusal marked alone", err, ask, ok)
	}
	if !slices.Contains(ids(), kept) {
		t.Fatalf("the refused removal removed route %s", kept)
	}
	// When the setting names another crew member, that one removes and
	// the yardmaster is refused.
	if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
		t.Fatal(err)
	}
	const renamed = "only the operator (jo) or the yardmaster (clerk) may remove a route"
	if err := api.DeleteRoute(ctx, kept, "yardmaster"); err == nil || err.Error() != renamed {
		t.Fatalf("DeleteRoute as yardmaster while the setting names clerk = %v, want %q", err, renamed)
	}
	if err := api.DeleteRoute(ctx, kept, "clerk"); err != nil {
		t.Fatalf("DeleteRoute as the named clerk: %v", err)
	}
	if actor := lastKindActor(t, api, ctx, "route_removed"); actor != "clerk" {
		t.Fatalf("route_removed actor = %q, want clerk", actor)
	}
	// The setting names a crew member, so naming a user does not make
	// that user a route remover.
	if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	nonCrew := add("builders")
	const nonCrewRefuse = "only the operator (jo) or the yardmaster (ada) may remove a route"
	if err := api.DeleteRoute(ctx, nonCrew, "ada"); err == nil || err.Error() != nonCrewRefuse {
		t.Fatalf("DeleteRoute as a user named by the setting = %v, want %q", err, nonCrewRefuse)
	}
	if !slices.Contains(ids(), nonCrew) {
		t.Fatalf("the refused removal removed route %s", nonCrew)
	}
	// The operator still removes.
	if err := api.DeleteRoute(ctx, add("builders"), "jo"); err != nil {
		t.Fatalf("DeleteRoute as the operator: %v", err)
	}
	if actor := lastKindActor(t, api, ctx, "route_removed"); actor != "jo" {
		t.Fatalf("route_removed actor = %q, want jo", actor)
	}
	// From the yardmaster's own pane the removal is the pane's, whatever
	// actor says. The setting is yardmaster again, the route is added,
	// and the pane goes on record last: over the socket every call
	// carries this process as its caller.
	if err := api.SetSetting(ctx, store.YardmasterSetting, "yardmaster", "jo"); err != nil {
		t.Fatal(err)
	}
	paned := add("builders")
	me := os.Getpid()
	handle := map[string]any{"shell_pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["shell_start"] = ps.Start
	}
	if err := api.CrewStarted(ctx, "yardmaster", handle); err != nil {
		t.Fatal(err)
	}
	if err := own.DeleteRoute(store.WithCaller(ctx, me), paned, "jo"); err != nil {
		t.Fatalf("DeleteRoute from the yardmaster's pane as the operator: %v", err)
	}
	if actor := lastKindActor(t, api, ctx, "route_removed"); actor != "yardmaster" {
		t.Fatalf("route_removed actor from the pane = %q, want the pane's yardmaster", actor)
	}
	// A builder session's pane cannot remove by naming the yardmaster: it
	// is checked as its session's actor.
	s, ctx, _, _, _ := paneMoveYard(t)
	r, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "op")
	if err != nil {
		t.Fatal(err)
	}
	const paneRefuse = "only the operator (op) or the yardmaster (yardmaster) may remove a route"
	if err := s.DeleteRoute(store.WithCaller(ctx, os.Getpid()), r.ID, "yardmaster"); err == nil || err.Error() != paneRefuse {
		t.Fatalf("DeleteRoute from a session's pane as yardmaster = %v, want %q", err, paneRefuse)
	}
	if rs, err := s.Routes(ctx); err != nil {
		t.Fatal(err)
	} else if !slices.ContainsFunc(rs, func(x store.Route) bool { return x.ID == r.ID }) {
		t.Fatalf("the refused removal removed route %s", r.ID)
	}
}

// Shaping the yard is the operator's or the yardmaster's: restarting,
// enabling and removing a crew member, and removing a group or a flow.
// The crew member the yardmaster setting names does each as the
// operator does; another crew member, and a user the setting names who
// is no crew member, is refused in words naming both. The yardmaster
// cannot remove itself, and the daemon still restarts crew.
func TestYardShapingIsTheOperatorsAndYardmasters(t *testing.T) {
	t.Parallel()
	// A yard whose operator is jo, with the user ada and the crew
	// members yardmaster, clerk and brakeman.
	shapeYard := func(t *testing.T) (store.API, *store.Store, context.Context) {
		t.Helper()
		api, own := operatorYard(t)
		ctx := context.Background()
		if _, err := api.PutCrew(ctx, store.CrewMember{Name: "brakeman", Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
		return api, own, ctx
	}
	refuse := func(ym, does string) string {
		return "only the operator (jo) or the yardmaster (" + ym + ") may " + does
	}
	// alone asks the refusal's run-ask mark of the store behind the
	// API, which the socket does not carry.
	alone := func(t *testing.T, err error, what string) {
		t.Helper()
		if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
			t.Fatalf("%s = %v (mark %v, %v), want an operator-only refusal marked alone", what, err, ask, ok)
		}
	}

	t.Run("restart", func(t *testing.T) {
		t.Parallel()
		api, own, ctx := shapeYard(t)
		const does = "restart a crew member"
		// A live process that holds no daemon lock, so the daemon leg
		// fails and the operator-or-yardmaster leg is what is tried.
		other := store.WithCaller(ctx, os.Getppid())
		requested := func() string {
			t.Helper()
			r, err := api.GetCrew(ctx, "clerk")
			if err != nil {
				t.Fatal(err)
			}
			return r.CrewSession.RestartRequested
		}
		if err := own.RequestRestart(other, "clerk", "x", "", "jo"); err != nil {
			t.Fatalf("restart as the operator: %v", err)
		}
		if got := requested(); got != "x" {
			t.Fatalf("restart requested = %q, want x", got)
		}
		if err := own.RequestRestart(other, "clerk", "y", "", "yardmaster"); err != nil {
			t.Fatalf("restart as yardmaster: %v", err)
		}
		if err := own.RequestRestart(other, "clerk", "y", "", "brakeman"); err == nil || err.Error() != refuse("yardmaster", does) {
			t.Fatalf("restart as brakeman = %v, want %q", err, refuse("yardmaster", does))
		}
		alone(t, own.RequestRestart(other, "clerk", "y", "", "brakeman"), "restart as brakeman")
		if got := requested(); got != "y" {
			t.Fatalf("restart requested = %q after the refusal, want y", got)
		}
		if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := own.RequestRestart(other, "clerk", "z", "", "clerk"); err != nil {
			t.Fatalf("restart as the named clerk: %v", err)
		}
		if err := own.RequestRestart(other, "clerk", "z", "", "yardmaster"); err == nil || err.Error() != refuse("clerk", does) {
			t.Fatalf("restart as yardmaster while the setting names clerk = %v, want %q", err, refuse("clerk", does))
		}
		if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := own.RequestRestart(other, "clerk", "z", "", "ada"); err == nil || err.Error() != refuse("ada", does) {
			t.Fatalf("restart as a user named by the setting = %v, want %q", err, refuse("ada", does))
		}
		if err := own.RequestRestart(ctx, "clerk", "rotation", "", "yard"); err != nil {
			t.Fatalf("the daemon's restart: %v", err)
		}
	})

	t.Run("enable", func(t *testing.T) {
		t.Parallel()
		api, own, ctx := shapeYard(t)
		const does = "enable a crew member"
		enabled := func() bool {
			t.Helper()
			r, err := api.GetCrew(ctx, "clerk")
			if err != nil {
				t.Fatal(err)
			}
			return r.Enabled
		}
		off := func() {
			t.Helper()
			if err := api.SetCrewEnabled(ctx, "clerk", false, "", "jo"); err != nil {
				t.Fatal(err)
			}
		}
		off()
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "jo"); err != nil {
			t.Fatalf("enable as the operator: %v", err)
		}
		if !enabled() {
			t.Fatal("clerk is not enabled after the operator's enable")
		}
		off()
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "yardmaster"); err != nil {
			t.Fatalf("enable as yardmaster: %v", err)
		}
		off()
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "brakeman"); err == nil || err.Error() != refuse("yardmaster", does) {
			t.Fatalf("enable as brakeman = %v, want %q", err, refuse("yardmaster", does))
		}
		alone(t, own.SetCrewEnabled(ctx, "clerk", true, "", "brakeman"), "enable as brakeman")
		if enabled() {
			t.Fatal("clerk is enabled after the refused enable")
		}
		if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "clerk"); err != nil {
			t.Fatalf("enable as the named clerk: %v", err)
		}
		off()
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "yardmaster"); err == nil || err.Error() != refuse("clerk", does) {
			t.Fatalf("enable as yardmaster while the setting names clerk = %v, want %q", err, refuse("clerk", does))
		}
		if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := api.SetCrewEnabled(ctx, "clerk", true, "", "ada"); err == nil || err.Error() != refuse("ada", does) {
			t.Fatalf("enable as a user named by the setting = %v, want %q", err, refuse("ada", does))
		}
		if enabled() {
			t.Fatal("clerk is enabled after the refused enable")
		}
	})

	t.Run("remove crew", func(t *testing.T) {
		t.Parallel()
		api, own, ctx := shapeYard(t)
		const does = "remove a crew member"
		add := func(name string) {
			t.Helper()
			if _, err := api.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		gone := func(name string) {
			t.Helper()
			if _, err := api.GetCrew(ctx, name); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetCrew %s after rm = %v, want not found", name, err)
			}
		}
		kept := func(name string) {
			t.Helper()
			if _, err := api.GetCrew(ctx, name); err != nil {
				t.Fatalf("the refused removal removed crew member %s: %v", name, err)
			}
		}
		add("gone-op")
		if err := api.DeleteCrew(ctx, "gone-op", "", "jo"); err != nil {
			t.Fatalf("remove as the operator: %v", err)
		}
		gone("gone-op")
		add("gone-ym")
		if err := api.DeleteCrew(ctx, "gone-ym", "", "yardmaster"); err != nil {
			t.Fatalf("remove as yardmaster: %v", err)
		}
		gone("gone-ym")
		add("kept")
		if err := api.DeleteCrew(ctx, "kept", "", "brakeman"); err == nil || err.Error() != refuse("yardmaster", does) {
			t.Fatalf("remove as brakeman = %v, want %q", err, refuse("yardmaster", does))
		}
		alone(t, own.DeleteCrew(ctx, "kept", "", "brakeman"), "remove as brakeman")
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
			t.Fatal(err)
		}
		add("gone-clerk")
		if err := api.DeleteCrew(ctx, "gone-clerk", "", "clerk"); err != nil {
			t.Fatalf("remove as the named clerk: %v", err)
		}
		if err := api.DeleteCrew(ctx, "kept", "", "yardmaster"); err == nil || err.Error() != refuse("clerk", does) {
			t.Fatalf("remove as yardmaster while the setting names clerk = %v, want %q", err, refuse("clerk", does))
		}
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := api.DeleteCrew(ctx, "kept", "", "ada"); err == nil || err.Error() != refuse("ada", does) {
			t.Fatalf("remove as a user named by the setting = %v, want %q", err, refuse("ada", does))
		}
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "yardmaster", "jo"); err != nil {
			t.Fatal(err)
		}
		const self = "the yardmaster cannot remove itself; the operator can"
		if err := api.DeleteCrew(ctx, "yardmaster", "", "yardmaster"); err == nil || err.Error() != self {
			t.Fatalf("remove itself as yardmaster = %v, want %q", err, self)
		}
		kept("yardmaster")
		if err := api.DeleteCrew(ctx, "yardmaster", "", "jo"); err != nil {
			t.Fatalf("remove the yardmaster as the operator: %v", err)
		}
		gone("yardmaster")
	})

	t.Run("remove group", func(t *testing.T) {
		t.Parallel()
		api, own, ctx := shapeYard(t)
		const does = "remove a group"
		add := func(name string) {
			t.Helper()
			if err := api.PutGroup(ctx, store.Group{Name: name, Runner: "herdr"}, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		gone := func(name string) {
			t.Helper()
			if _, err := api.GetGroup(ctx, name); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetGroup %s after rm = %v, want not found", name, err)
			}
		}
		kept := func(name string) {
			t.Helper()
			if _, err := api.GetGroup(ctx, name); err != nil {
				t.Fatalf("the refused removal removed group %s: %v", name, err)
			}
		}
		add("gone-op")
		if _, err := api.DeleteGroup(ctx, "gone-op", "jo"); err != nil {
			t.Fatalf("remove as the operator: %v", err)
		}
		gone("gone-op")
		add("gone-ym")
		if _, err := api.DeleteGroup(ctx, "gone-ym", "yardmaster"); err != nil {
			t.Fatalf("remove as yardmaster: %v", err)
		}
		gone("gone-ym")
		add("kept")
		if _, err := api.DeleteGroup(ctx, "kept", "brakeman"); err == nil || err.Error() != refuse("yardmaster", does) {
			t.Fatalf("remove as brakeman = %v, want %q", err, refuse("yardmaster", does))
		}
		if _, err := own.DeleteGroup(ctx, "kept", "brakeman"); err == nil {
			t.Fatalf("remove as brakeman through own = nil")
		} else {
			alone(t, err, "remove as brakeman")
		}
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
			t.Fatal(err)
		}
		add("gone-clerk")
		if _, err := api.DeleteGroup(ctx, "gone-clerk", "clerk"); err != nil {
			t.Fatalf("remove as the named clerk: %v", err)
		}
		if _, err := api.DeleteGroup(ctx, "kept", "yardmaster"); err == nil || err.Error() != refuse("clerk", does) {
			t.Fatalf("remove as yardmaster while the setting names clerk = %v, want %q", err, refuse("clerk", does))
		}
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := api.DeleteGroup(ctx, "kept", "ada"); err == nil || err.Error() != refuse("ada", does) {
			t.Fatalf("remove as a user named by the setting = %v, want %q", err, refuse("ada", does))
		}
		kept("kept")
	})

	t.Run("remove flow", func(t *testing.T) {
		t.Parallel()
		api, own, ctx := shapeYard(t)
		const does = "remove a flow"
		add := func(name string) {
			t.Helper()
			f, err := store.ParseFlow("new -> review\nreview terminal\n")
			if err != nil {
				t.Fatal(err)
			}
			f.Name = name
			if _, err := api.PutFlow(ctx, *f, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		gone := func(name string) {
			t.Helper()
			if _, err := api.GetFlow(ctx, name); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("GetFlow %s after rm = %v, want not found", name, err)
			}
		}
		kept := func(name string) {
			t.Helper()
			if _, err := api.GetFlow(ctx, name); err != nil {
				t.Fatalf("the refused removal removed flow %s: %v", name, err)
			}
		}
		add("gone-op")
		if err := api.DeleteFlow(ctx, "gone-op", "jo"); err != nil {
			t.Fatalf("remove as the operator: %v", err)
		}
		gone("gone-op")
		add("gone-ym")
		if err := api.DeleteFlow(ctx, "gone-ym", "yardmaster"); err != nil {
			t.Fatalf("remove as yardmaster: %v", err)
		}
		gone("gone-ym")
		add("kept")
		if err := api.DeleteFlow(ctx, "kept", "brakeman"); err == nil || err.Error() != refuse("yardmaster", does) {
			t.Fatalf("remove as brakeman = %v, want %q", err, refuse("yardmaster", does))
		}
		alone(t, own.DeleteFlow(ctx, "kept", "brakeman"), "remove as brakeman")
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "clerk", "jo"); err != nil {
			t.Fatal(err)
		}
		add("gone-clerk")
		if err := api.DeleteFlow(ctx, "gone-clerk", "clerk"); err != nil {
			t.Fatalf("remove as the named clerk: %v", err)
		}
		if err := api.DeleteFlow(ctx, "kept", "yardmaster"); err == nil || err.Error() != refuse("clerk", does) {
			t.Fatalf("remove as yardmaster while the setting names clerk = %v, want %q", err, refuse("clerk", does))
		}
		kept("kept")
		if err := api.SetSetting(ctx, store.YardmasterSetting, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := api.DeleteFlow(ctx, "kept", "ada"); err == nil || err.Error() != refuse("ada", does) {
			t.Fatalf("remove as a user named by the setting = %v, want %q", err, refuse("ada", does))
		}
		kept("kept")
	})
}

// Removing a peer is the operator's alone, as adding one is: a crew
// member is refused with the alone mark and the link stays, and the
// operator removes.
func TestPeerRemoveIsTheOperatorsAlone(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	// The mark is the store's own: the socket carries a refusal's words
	// but not its mark, as for every operator-only check, so it is asked
	// of the store behind the API.
	err := own.DeletePeer(ctx, "north", "yardmaster")
	if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
		t.Fatalf("DeletePeer as yardmaster = %v (mark %v, %v), want an operator-only refusal marked alone", err, ask, ok)
	}
	const want = "only the operator (jo) may remove a peer; this is yardmaster"
	if err := api.DeletePeer(ctx, "north", "yardmaster"); err == nil || err.Error() != want {
		t.Fatalf("DeletePeer as yardmaster over the API = %v, want %q", err, want)
	}
	if _, err := api.GetPeer(ctx, "north"); err != nil {
		t.Fatalf("the refused removal removed: %v", err)
	}
	if err := api.DeletePeer(ctx, "north", "jo"); err != nil {
		t.Fatalf("DeletePeer as the operator: %v", err)
	}
	if _, err := api.GetPeer(ctx, "north"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPeer after rm = %v", err)
	}
}

// Adding a hook, or changing its source, secret file or identity-hearing
// policy, is the operator's alone; what one says, makes, maps or acts as
// is anyone's to change, recorded as the pane's actor from a pane, and a
// hook that acts as a crew member still speaks as it.
func TestPutHookCreateAndSourceChangeAreTheOperators(t *testing.T) {
	t.Parallel()
	hookYard := func(t *testing.T) (store.API, *store.Store, context.Context) {
		t.Helper()
		api, own := operatorYard(t)
		ctx := context.Background()
		if err := declareSlackSource(ctx, api); err != nil {
			t.Fatal(err)
		}
		if _, err := api.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/h1.secret"}, "jo"); err != nil {
			t.Fatal(err)
		}
		return api, own, ctx
	}
	api, own, ctx := hookYard(t)
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutHook(ctx, store.Hook{Name: "h2", Source: "linear", SecretFile: "/tmp/h2.secret"}, "yardmaster"); err == nil ||
		err.Error() != "only the operator (jo) may add a hook; this is yardmaster" {
		t.Errorf("adding a hook as yardmaster = %v", err)
	}
	for _, h := range []store.Hook{
		{Name: "h1", Source: "linear", SecretFile: "/tmp/other.secret"},
		{Name: "h1", Source: "github", SecretFile: "/tmp/h1.secret"},
	} {
		if _, err := api.PutHook(ctx, h, "yardmaster"); err == nil ||
			err.Error() != "only the operator (jo) may change a hook's source or secret file; this is yardmaster" {
			t.Errorf("changing %v as yardmaster = %v", h, err)
		}
	}
	// The marks are the store's own: the socket carries a refusal's words
	// but not its mark, as for every operator-only check, so they are
	// asked of the store behind the API.
	if _, err := own.PutHook(ctx, store.Hook{Name: "h3", Source: "linear", SecretFile: "/tmp/h3.secret"}, "yardmaster"); err == nil {
		t.Errorf("adding a hook as yardmaster through own = nil")
	} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
		t.Errorf("adding a hook as yardmaster = %v (mark %v, %v), want an operator-only refusal marked alone", err, ask, ok)
	}
	if _, err := own.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/other.secret"}, "yardmaster"); err == nil {
		t.Errorf("changing a hook's secret file as yardmaster through own = nil")
	} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
		t.Errorf("changing a hook's secret file as yardmaster = %v (mark %v, %v), want an operator-only refusal marked alone", err, ask, ok)
	}
	if now, _ := api.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	// The unknown sender policy decides whether unrecognized people may
	// reach the yard, so only the operator may change it on an existing
	// identity-hearing hook.
	if _, err := api.PutHook(ctx, store.Hook{Name: "slack", Source: "slack", SecretFile: "/tmp/slack.secret"}, "jo"); err != nil {
		t.Fatal(err)
	}
	head, err = api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutHook(ctx, store.Hook{Name: "slack", Source: "slack", SecretFile: "/tmp/slack.secret", Unknown: store.HookUnknownAllow}, "yardmaster"); err == nil ||
		err.Error() != "only the operator (jo) may change a hook's unmapped sender policy; this is yardmaster" {
		t.Errorf("changing the unmapped sender policy as yardmaster = %v", err)
	}
	if _, err := own.PutHook(ctx, store.Hook{Name: "slack", Source: "slack", SecretFile: "/tmp/slack.secret", Unknown: store.HookUnknownAllow}, "yardmaster"); err == nil {
		t.Errorf("changing the unmapped sender policy as yardmaster through own = nil")
	} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
		t.Errorf("changing the unmapped sender policy as yardmaster = %v (mark %v, %v), want an operator-only refusal marked alone", err, ask, ok)
	}
	if now, _ := api.Head(ctx); now != head {
		t.Fatalf("the policy refusals logged %d events", now-head)
	}
	if _, err := api.PutHook(ctx, store.Hook{Name: "slack", Source: "slack", SecretFile: "/tmp/slack.secret", Unknown: store.HookUnknownAllow}, "jo"); err != nil {
		t.Errorf("changing the unmapped sender policy as the operator = %v", err)
	}
	if h, _ := api.GetHook(ctx, "slack"); h.Unknown != store.HookUnknownAllow {
		t.Errorf("the operator's policy change stored %q, want allow", h.Unknown)
	}
	if h, _ := api.GetHook(ctx, "h1"); h.Source != "linear" || h.SecretFile != "/tmp/h1.secret" {
		t.Fatalf("the refused changes changed: %+v", h)
	}
	// What the hook says is the invoker's to change, as a pack's map
	// update does. A fresh yard: no pane is on record, so the actor
	// stands as named.
	api, _, ctx = hookYard(t)
	h, err := api.GetHook(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	h.Says = []store.Say{{Where: map[string]string{"stage": "review"}, Outcome: "merged"}}
	if _, err := api.PutHook(ctx, *h, "yardmaster"); err != nil {
		t.Fatalf("saying more as yardmaster: %v", err)
	}
	// Whom it acts as is the operator's to say; with its map, under
	// today's rule, the crew member speaks as itself.
	if err := api.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u-yardmaster"}, "jo"); err != nil {
		t.Fatal(err)
	}
	h, err = api.GetHook(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	h.As, h.Map = "yardmaster", map[string]string{"task:new": "started"}
	if _, err := api.PutHook(ctx, *h, "yardmaster"); err != nil {
		t.Fatalf("mapping as itself, as yardmaster: %v", err)
	}
	if h, _ := api.GetHook(ctx, "h1"); h.As != "yardmaster" || h.Map["task:new"] != "started" || len(h.Says) != 1 {
		t.Fatalf("the updates did not land: %+v", h)
	}
	// Acting as another crew member is still refused as before.
	h.As = "clerk"
	if _, err := api.PutHook(ctx, *h, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "may speak as clerk; this is yardmaster") {
		t.Fatalf("mapping as clerk, as yardmaster = %v", err)
	}
	// From the pane the update is the pane's actor's, whatever actor
	// says. A fresh yard again: the pane goes on record after the
	// setup, since over the socket every call carries this process as
	// its caller.
	api, _, ctx = hookYard(t)
	me := os.Getpid()
	handle := map[string]any{"shell_pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["shell_start"] = ps.Start
	}
	if err := api.CrewStarted(ctx, "clerk", handle); err != nil {
		t.Fatal(err)
	}
	h, err = api.GetHook(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	h.Says = []store.Say{{Where: map[string]string{"stage": "new"}, Outcome: "opened"}}
	if _, err := api.PutHook(store.WithCaller(ctx, me), *h, "jo"); err != nil {
		t.Fatalf("saying more from clerk's pane as the operator: %v", err)
	}
	evs, err := api.EventsOfKind(ctx, "hook_added", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Actor != "clerk" {
		t.Fatalf("hook_added actors = %+v, want the last by clerk", evs)
	}
}

func declareSlackSource(ctx context.Context, api store.API) error {
	b, err := os.ReadFile("../../packs/slack/slack.yaml")
	if err != nil {
		return err
	}
	_, err = api.PutSource(ctx, "slack.yaml", string(b), "jo")
	return err
}

// Changing an existing depot's path is the operator's alone; its test
// command or base is the operator's, and a run ask may propose it.
// Adding a depot, and re-adding one with all three unchanged, are
// anyone's, recorded as the pane's actor from a pane.
func TestPutDepotPathTestBaseChangesAreTheOperators(t *testing.T) {
	t.Parallel()
	depotYard := func(t *testing.T) (store.API, *store.Store, context.Context) {
		t.Helper()
		api, own := operatorYard(t)
		ctx := context.Background()
		if err := api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "jo"); err != nil {
			t.Fatal(err)
		}
		return api, own, ctx
	}
	changes := map[string]store.Depot{
		"change a depot's path":         {Name: "yard", Path: "/srv/other", Base: "origin/main"},
		"change a depot's test command": {Name: "yard", Path: "/srv/yard", Base: "origin/main", Test: "make test"},
		"change a depot's base":         {Name: "yard", Path: "/srv/yard", Base: "origin/other"},
	}
	// The test command and the base may be proposed on a run ask; the
	// path stays the operator's alone.
	marks := map[string]store.AskClass{
		"change a depot's path":         store.Alone,
		"change a depot's test command": store.Askable,
		"change a depot's base":         store.Askable,
	}
	markWord := map[store.AskClass]string{store.Alone: "alone", store.Askable: "askable"}
	api, own, ctx := depotYard(t)
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for does, r := range changes {
		if err := api.PutDepot(ctx, r, "yardmaster"); err == nil ||
			err.Error() != "only the operator (jo) may "+does+"; this is yardmaster" {
			t.Errorf("%s as yardmaster = %v", does, err)
		}
		// The mark is the store's own: the socket carries a refusal's
		// words but not its mark, as for every operator-only check, so
		// it is asked of the store behind the API.
		if err := own.PutDepot(ctx, r, "yardmaster"); err == nil {
			t.Errorf("%s as yardmaster through own = nil", does)
		} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != marks[does] {
			t.Errorf("%s as yardmaster = %v (mark %v, %v), want an operator-only refusal marked %s", does, err, ask, ok, markWord[marks[does]])
		}
	}
	if now, _ := api.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	if d, _ := api.GetDepot(ctx, "yard"); d.Path != "/srv/yard" || d.Test != "" || d.Base != "origin/main" {
		t.Fatalf("a refused change changed: %+v", d)
	}
	// Adding a depot stays anyone's, as does re-adding one with path,
	// test command and base unchanged. A fresh yard: no pane is on
	// record, so the actor stands as named.
	api, _, ctx = depotYard(t)
	if err := api.PutDepot(ctx, store.Depot{Name: "web", Path: "/srv/web", Base: "origin/main"}, "yardmaster"); err != nil {
		t.Fatalf("adding a depot as yardmaster: %v", err)
	}
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "yardmaster"); err != nil {
		t.Fatalf("re-adding a depot unchanged as yardmaster: %v", err)
	}
	// The operator's own changes pass.
	for does, r := range changes {
		if err := api.PutDepot(ctx, r, "jo"); err != nil {
			t.Fatalf("%s as the operator: %v", does, err)
		}
	}
	// From a crew member's pane every change is refused even as the
	// operator, and an allowed write is the pane's actor's. A fresh yard
	// again: the pane goes on record after the setup, since over the
	// socket every call carries this process as its caller.
	api, _, ctx = depotYard(t)
	me := os.Getpid()
	handle := map[string]any{"shell_pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["shell_start"] = ps.Start
	}
	if err := api.CrewStarted(ctx, "clerk", handle); err != nil {
		t.Fatal(err)
	}
	for does, r := range changes {
		if err := api.PutDepot(store.WithCaller(ctx, me), r, "jo"); err == nil ||
			err.Error() != "only the operator (jo) may "+does+"; this is clerk" {
			t.Fatalf("%s from clerk's pane as the operator = %v", does, err)
		}
	}
	if err := api.PutDepot(store.WithCaller(ctx, me), store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatalf("an unchanged re-put from clerk's pane as the operator: %v", err)
	}
	evs, err := api.EventsOfKind(ctx, "depot_updated", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Actor != "clerk" {
		t.Fatalf("depot_updated actors = %+v, want the last by clerk", evs)
	}
	// A builder session's pane cannot change by naming the operator: it
	// is checked as its session's actor.
	s := storetest.New(t)
	ctx = context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Path: "/srv/yard", Base: "origin/main"}, "op"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "op")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	actor := b.ID + "-new"
	if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, os.Getpid())
	for does, r := range changes {
		if err := s.PutDepot(pane, r, "op"); err == nil ||
			err.Error() != "only the operator (op) may "+does+"; this is "+actor {
			t.Fatalf("%s from a session's pane as the operator = %v", does, err)
		}
	}
	if d, _ := s.GetDepot(ctx, "yard"); d.Path != "/srv/yard" || d.Test != "" || d.Base != "origin/main" {
		t.Fatalf("a refused change from the pane changed: %+v", d)
	}
}

// Changing an existing depot's kind, with path, test command and base
// unchanged, is the operator's alone, as changing any of those is: a depot
// that worked as a directory must not start working as a repository from a
// non-operator's re-put, or a repository with an empty base as a directory.
func TestPutDepotKindChangeIsTheOperators(t *testing.T) {
	t.Parallel()
	depotYard := func(t *testing.T) (store.API, *store.Store, context.Context) {
		t.Helper()
		api, own := operatorYard(t)
		ctx := context.Background()
		if err := api.PutDepot(ctx, store.Depot{Name: "docs", Path: "/srv/docs", Kind: store.DepotDir}, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := api.PutDepot(ctx, store.Depot{Name: "notes", Path: "/srv/notes"}, "jo"); err != nil {
			t.Fatal(err)
		}
		return api, own, ctx
	}
	const does = "change a depot's kind"
	flips := map[string]store.Depot{
		"docs":  {Name: "docs", Path: "/srv/docs", Kind: store.DepotGit},
		"notes": {Name: "notes", Path: "/srv/notes", Kind: store.DepotDir},
	}
	api, own, ctx := depotYard(t)
	// Re-putting a directory depot with its kind unchanged stays anyone's.
	if err := api.PutDepot(ctx, store.Depot{Name: "docs", Path: "/srv/docs", Kind: store.DepotDir}, "yardmaster"); err != nil {
		t.Fatalf("re-adding an unchanged directory depot as yardmaster: %v", err)
	}
	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range flips {
		if err := api.PutDepot(ctx, r, "yardmaster"); err == nil ||
			err.Error() != "only the operator (jo) may "+does+"; this is yardmaster" {
			t.Errorf("%s to %s as yardmaster = %v", name, r.Kind, err)
		}
		if err := own.PutDepot(ctx, r, "yardmaster"); err == nil {
			t.Errorf("%s to %s as yardmaster through own = nil", name, r.Kind)
		} else if ask, ok := store.IsOperatorOnly(err); !ok || ask != store.Alone {
			t.Errorf("%s to %s as yardmaster = %v (mark %v, %v), want an operator-only refusal marked alone", name, r.Kind, err, ask, ok)
		}
	}
	if now, _ := api.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	if d, _ := api.GetDepot(ctx, "docs"); d.Kind != store.DepotDir {
		t.Fatalf("a refused change changed: %+v", d)
	}
	if d, _ := api.GetDepot(ctx, "notes"); d.Kind != store.DepotGit {
		t.Fatalf("a refused change changed: %+v", d)
	}
	// The operator's own flips pass.
	for name, r := range flips {
		if err := api.PutDepot(ctx, r, "jo"); err != nil {
			t.Fatalf("%s to %s as the operator: %v", name, r.Kind, err)
		}
	}
	// From a crew member's pane the flip is refused even as the
	// operator. A fresh yard again: the pane goes on record after the
	// setup, since over the socket every call carries this process as
	// its caller.
	api, _, ctx = depotYard(t)
	me := os.Getpid()
	handle := map[string]any{"shell_pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["shell_start"] = ps.Start
	}
	if err := api.CrewStarted(ctx, "clerk", handle); err != nil {
		t.Fatal(err)
	}
	for name, r := range flips {
		if err := api.PutDepot(store.WithCaller(ctx, me), r, "jo"); err == nil ||
			err.Error() != "only the operator (jo) may "+does+"; this is clerk" {
			t.Fatalf("%s to %s from clerk's pane as the operator = %v", name, r.Kind, err)
		}
	}
	// A bead session's pane cannot change kind by naming the operator either.
	s := storetest.New(t)
	ctx = context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "notes", Path: "/srv/notes"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "notes", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "notes", Depot: "notes", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Claim(ctx, b.ID, "builders", "new", "notes")
	if err != nil {
		t.Fatal(err)
	}
	actor := b.ID + "-new"
	if err := s.MarkRunning(ctx, session.ID, paneShell(t, actor)); err != nil {
		t.Fatal(err)
	}
	flip := store.Depot{Name: "notes", Path: "/srv/notes", Kind: store.DepotDir}
	if err := s.PutDepot(store.WithCaller(ctx, os.Getpid()), flip, "jo"); err == nil ||
		err.Error() != "only the operator (jo) may "+does+"; this is "+actor {
		t.Fatalf("%s to %s from a bead session pane as the operator = %v", "notes", flip.Kind, err)
	}
}

// A yard that names no operator has nobody to hold the rule to: it takes
// the change from anyone who is no agent of its own, as a declared source
// is taken (maySource).
func TestOperatorOnlyWithoutAnOperator(t *testing.T) {
	t.Parallel()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddUser(ctx, "grace", "yardmaster"); err == nil || err.Error() != "only the operator may add a user; this is the crew member yardmaster" {
		t.Fatalf("a crew member, with no operator = %v", err)
	}
	if _, err := api.AddUser(ctx, "grace", "me"); err != nil {
		t.Fatalf("anyone else, with no operator: %v", err)
	}
	// From the yardmaster's pane, whatever actor the command names.
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	if _, err := own.AddUser(store.WithCaller(ctx, me), "ada", "me"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("from the yardmaster's pane, with no operator = %v", err)
	}
}

// Two live crew records may name one shell pid: one's shell is gone and
// the kernel gave its pid to the other's shell, before the daemon
// noticed. The stale record must not hide the live one: a call from the
// pane is the crew member whose shell still runs there.
func TestPaneCrewStaleRecordDoesNotHideLiveOne(t *testing.T) {
	t.Parallel()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, name := range []string{"yardmaster", "warden"} {
		if _, err := api.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start + 1}); err != nil {
		t.Fatal(err)
	}
	if err := api.CrewStarted(ctx, "warden", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	if _, err := own.AddUser(store.WithCaller(ctx, me), "ada", "me"); err == nil || !strings.Contains(err.Error(), "this is the crew member warden") {
		t.Fatalf("from a pid one stale and one live record name = %v", err)
	}
	// A shell whose start time matches wins over one recorded without,
	// even one the bucket yields first.
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := api.CrewStarted(ctx, "clerk", map[string]any{"shell_pid": me}); err != nil {
		t.Fatal(err)
	}
	if _, err := own.AddUser(store.WithCaller(ctx, me), "ada", "me"); err == nil || !strings.Contains(err.Error(), "this is the crew member warden") {
		t.Fatalf("from a pid one live, one stale and one unrecorded record name = %v", err)
	}
}

// An admin write from a session's or a crew member's pane is checked as
// the pane's actor, whatever --actor says: a pane can never pass one as
// the operator. From no pane the actor stands as given (yardr-6g56).
func TestPaneAdminWriteIsThePanesActor(t *testing.T) {
	t.Parallel()
	t.Run("session", func(t *testing.T) {
		s, ctx, _, _, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.AddUser(pane, "grace", "op"); err == nil ||
			err.Error() != "only the operator (op) may add a user; this is "+actor {
			t.Fatalf("AddUser from the pane as the operator = %v", err)
		}
		// From no pane, as today: its own yard, with no pane on record.
		plain, plainCtx, _, _, _ := moveYardClaim(t)
		if _, err := plain.AddUser(plainCtx, "grace", "op"); err != nil {
			t.Fatalf("AddUser from no pane as the operator: %v", err)
		}
	})
	t.Run("crew", func(t *testing.T) {
		api, own := operatorYard(t)
		ctx := context.Background()
		if _, err := api.AddUser(ctx, "grace", "jo"); err != nil {
			t.Fatalf("AddUser from no pane as the operator: %v", err)
		}
		me := os.Getpid()
		ps, err := peer.Describe(me)
		if err != nil {
			t.Skipf("this process cannot be described: %v", err)
		}
		if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, me)
		if _, err := own.AddUser(pane, "heidi", "jo"); err == nil ||
			err.Error() != "only the operator (jo) may add a user; this is yardmaster" {
			t.Fatalf("AddUser from the pane as the operator = %v", err)
		}
	})
}

// An operator write made from a pane whose recorded actor is the operator
// still records that actor, whatever the caller supplies as --actor. The
// gate must use the pane actor both to decide access and to write the event.
func TestOperatorWritesFromPaneRecordPanesActor(t *testing.T) {
	t.Parallel()
	t.Run("operator-only", func(t *testing.T) {
		s, ctx, _, sess, _ := moveYardClaim(t)
		route, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, paneShell(t, "op")); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.DeleteRoute(pane, route.ID, "spoofed"); err != nil {
			t.Fatalf("DeleteRoute from the operator's pane: %v", err)
		}
		if actor := lastKindActor(t, s, ctx, "route_removed"); actor != "op" {
			t.Fatalf("route_removed actor = %q, want pane actor op", actor)
		}
	})

	t.Run("daemon-or-operator", func(t *testing.T) {
		s, ctx, _, sess, _ := moveYardClaim(t)
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, paneShell(t, "op")); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.RequestRestart(pane, "clerk", "because", "", "spoofed"); err != nil {
			t.Fatalf("RequestRestart from the operator's pane: %v", err)
		}
		clerk, err := s.GetCrew(ctx, "clerk")
		if err != nil {
			t.Fatal(err)
		}
		if actor := lastEventActor(t, s, ctx, clerk.Journal, "crew_restart_requested"); actor != "op" {
			t.Fatalf("crew_restart_requested actor = %q, want pane actor op", actor)
		}
	})
}

// A timer set from a session's pane is the pane's actor's, whatever
// --actor says: a pane can never set one as the operator. From no pane
// the actor stands as given (yardr-6g56).
func TestPaneTimerIsThePanesActor(t *testing.T) {
	t.Parallel()
	nt := store.NewTimer{Reason: "stretch", Due: time.Now().Add(time.Hour)}
	s, ctx, _, _, actor := paneMoveYard(t)
	pane := store.WithCaller(ctx, os.Getpid())
	if _, err := s.AddTimer(pane, nt, "op"); err == nil ||
		!strings.Contains(err.Error(), actor) ||
		!strings.Contains(err.Error(), "neither a crew member nor a user") {
		t.Fatalf("AddTimer from the pane as the operator = %v, want a refusal as %s", err, actor)
	}
	// From no pane, as today: its own yard, with no pane on record.
	plain, plainCtx, _, _, _ := moveYardClaim(t)
	if _, err := plain.AddTimer(plainCtx, nt, "op"); err != nil {
		t.Fatalf("AddTimer from no pane as the operator: %v", err)
	}
}

// Speaking as a crew member is its own or the operator's: its own by the
// actor its session names, or by the pane yardr started it in when a
// command there names another.
func TestSpeakingAsACrewMember(t *testing.T) {
	t.Parallel()
	api, own := operatorYard(t)
	ctx := context.Background()
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{store.OpenThreads: "linear"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	speak := map[string]func(ctx context.Context, s store.API, actor string) error{
		"reply": func(ctx context.Context, s store.API, a string) error {
			_, err := s.Replying(ctx, "yardmaster", 7, "", "hello "+a, a)
			return err
		},
		"open": func(ctx context.Context, s store.API, a string) error {
			_, err := s.Opening(ctx, "yardmaster", "linear", "YRD", "Title "+a, "body", a)
			return err
		},
		"state": func(ctx context.Context, s store.API, a string) error {
			_, err := s.SettingState(ctx, "yardmaster", "linear", "T", a)
			return err
		},
	}
	refused := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster; this is ")
	}
	for does, fn := range speak {
		for _, actor := range []string{"clerk", "ada", "yard-10-new"} {
			if err := fn(ctx, api, actor); !refused(err) || !strings.HasSuffix(err.Error(), "this is "+actor) {
				t.Errorf("%s as %s = %v", does, actor, err)
			}
		}
		for _, actor := range []string{"yardmaster", "jo"} {
			if err := fn(ctx, api, actor); refused(err) {
				t.Errorf("%s as %s = %v", does, actor, err)
			}
		}
	}
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	for does, fn := range speak {
		if err := fn(pane, own, "yard-10-new"); refused(err) {
			t.Errorf("%s from yardmaster's own pane, naming another = %v", does, err)
		}
	}
	// Its pane is its own, not another crew member's: the call is
	// checked as yardmaster, whatever --actor says (yardr-6g56).
	if _, err := own.Replying(pane, "clerk", 7, "", "hello", "yard-10-new"); err == nil ||
		!strings.Contains(err.Error(), "only clerk's own session or the operator (jo) may speak as clerk; this is yardmaster") {
		t.Errorf("replying as clerk from yardmaster's pane = %v", err)
	}
}

// A thread's completions and follows speak as the crew member itself:
// Replied, Opened, StateSet, FollowThread and UnfollowThread refuse
// anyone else (maySpeakAs), as the pre-calls Replying, Opening and
// SettingState do, so a completion faked from another crew member's
// pane is refused in their words. The crew member's own pane and the
// operator pass, and a refusal logs no event.
func TestCompletionsAndFollowsSpeakAsTheCrewMember(t *testing.T) {
	t.Parallel()
	speakYard := func(t *testing.T) (store.API, context.Context) {
		t.Helper()
		api := storetest.New(t)
		ctx := context.Background()
		if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
			t.Fatal(err)
		}
		cfg := map[string]any{store.OpenThreads: "linear", store.SetState: "linear"}
		if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: cfg}, "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := api.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
		return api, ctx
	}
	// ready records, as the crew member itself, the reply and the thread
	// the completions complete and the participation StateSet and
	// UnfollowThread need.
	ready := func(t *testing.T, s store.API, ctx context.Context) (reply, opened string) {
		t.Helper()
		rp, err := s.Replying(ctx, "yardmaster", 7, "", "hello", "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		op, err := s.Opening(ctx, "yardmaster", "linear", "YRD", "Title", "body", "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range []string{"T-state", "T-unfollow"} {
			if err := s.FollowThread(ctx, "yardmaster", "linear", thread, "yardmaster"); err != nil {
				t.Fatal(err)
			}
		}
		return rp.Key, op.Key
	}
	complete := func(s store.API, reply, opened string) map[string]func(context.Context, string) error {
		replied := store.Reply{Source: "linear", Thread: "T-reply", Key: reply, Seq: 7, Message: "m1", URL: "https://linear.app/acme/comment/m1"}
		openedRec := store.Reply{Source: "linear", Thread: "T-open", Key: opened, In: "YRD"}
		set := store.ThreadState{Source: "linear", Thread: "T-state", State: "started", Status: "In Progress"}
		return map[string]func(context.Context, string) error{
			"replied": func(ctx context.Context, a string) error { return s.Replied(ctx, "yardmaster", replied, a) },
			"opened":  func(ctx context.Context, a string) error { return s.Opened(ctx, "yardmaster", openedRec, a) },
			"state":   func(ctx context.Context, a string) error { return s.StateSet(ctx, "yardmaster", set, a) },
			"follow": func(ctx context.Context, a string) error {
				return s.FollowThread(ctx, "yardmaster", "linear", "T-follow", a)
			},
			"unfollow": func(ctx context.Context, a string) error {
				return s.UnfollowThread(ctx, "yardmaster", "linear", "T-unfollow", a)
			},
		}
	}
	// The operator, from a terminal with no pane on record: every
	// completion lands, three of them with an event.
	s, ctx := speakYard(t)
	reply, opened := ready(t, s, ctx)
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for does, fn := range complete(s, reply, opened) {
		if err := fn(ctx, "jo"); err != nil {
			t.Errorf("%s as the operator: %v", does, err)
		}
	}
	if now, _ := s.Head(ctx); now-head != 3 {
		t.Errorf("the operator's completions logged %d events, want 3", now-head)
	}
	if got, want := threads(t, s, "yardmaster"), []string{"linear/T-follow followed", "linear/T-open opened", "linear/T-reply replied", "linear/T-state followed"}; !slices.Equal(got, want) {
		t.Errorf("threads after the operator's completions = %v, want %v", got, want)
	}
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	// Another crew member's pane, naming the crew member: every
	// completion is refused in maySpeakAs's words, and nothing is
	// written. The pane goes on record after the setup: over the socket
	// every call carries this process as its caller.
	s, ctx = speakYard(t)
	reply, opened = ready(t, s, ctx)
	if err := s.CrewStarted(ctx, "clerk", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	head, err = s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const want = "only yardmaster's own session or the operator (jo) may speak as yardmaster; this is clerk"
	for does, fn := range complete(s, reply, opened) {
		if err := fn(pane, "yardmaster"); err == nil || err.Error() != want {
			t.Errorf("%s faked from clerk's pane = %v, want %q", does, err, want)
		}
	}
	if now, _ := s.Head(ctx); now != head {
		t.Errorf("the refusals logged %d events", now-head)
	}
	if got := threads(t, s, "yardmaster"); !slices.Equal(got, []string{"linear/T-state followed", "linear/T-unfollow followed"}) {
		t.Errorf("threads after the refusals = %v", got)
	}
	// The crew member's own pane, naming another: every completion
	// passes, as the pre-calls do.
	s, ctx = speakYard(t)
	reply, opened = ready(t, s, ctx)
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane = store.WithCaller(ctx, me)
	head, err = s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for does, fn := range complete(s, reply, opened) {
		if err := fn(pane, "yard-10-new"); err != nil {
			t.Errorf("%s from yardmaster's own pane, naming another: %v", does, err)
		}
	}
	if now, _ := s.Head(ctx); now-head != 3 {
		t.Errorf("the pane's completions logged %d events, want 3", now-head)
	}
}
