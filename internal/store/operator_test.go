package store_test

import (
	"context"
	"errors"
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
	api, own := operatorYard(t)
	ctx := context.Background()
	key := peerKey(t)
	grant := store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{store.OpenThreads: "linear", store.MailPeers: "north"}}
	if _, err := api.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/h1.secret"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// A live process that holds no daemon lock: the daemon-or-operator
	// writes refuse it as a non-operator, in both modes (over the socket
	// the test process itself holds the lock, so those two go through
	// own, where the caller stands as named).
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
		"pair this yard with another": func(a string) error { _, err := api.PairOpen(ctx, store.PairOffer{}, a); return err },
		"let clerk open threads in linear": func(a string) error {
			_, err := api.PutCrew(ctx, grant, a)
			return err
		},
		"let yardmaster open threads in github": func(a string) error {
			_, err := api.AllowOpening(ctx, "yardmaster", []string{"github"}, "pack", a)
			return err
		},
		"remove a group": func(a string) error { _, err := api.DeleteGroup(ctx, "builders", a); return err },
		"remove a route": func(a string) error { return api.DeleteRoute(ctx, "no-such", a) },
		"remove a flow":  func(a string) error { return api.DeleteFlow(ctx, "no-such", a) },
		"remove a depot": func(a string) error { _, err := api.DeleteDepot(ctx, "no-such", a); return err },
		"remove a hook":  func(a string) error { return api.DeleteHook(ctx, "no-such", a) },
		"move links from one source to another": func(a string) error {
			_, err := api.MoveLinkSource(ctx, "acme", "acme2", a)
			return err
		},
		"move a source's records": func(a string) error {
			_, err := api.MoveSourceRecords(ctx, "acme", "acme2", a)
			return err
		},
		"remove a crew member": func(a string) error { return api.DeleteCrew(ctx, "clerk", "", a) },
		"add a hook": func(a string) error {
			_, err := api.PutHook(ctx, store.Hook{Name: "h2", Source: "linear", SecretFile: "/tmp/h2.secret"}, a)
			return err
		},
		"change a hook's source or secret file": func(a string) error {
			_, err := api.PutHook(ctx, store.Hook{Name: "h1", Source: "linear", SecretFile: "/tmp/other.secret"}, a)
			return err
		},
		"enable a crew member": func(a string) error { return api.SetCrewEnabled(ctx, "clerk", true, "", a) },
		"restart a crew member": func(a string) error {
			return own.RequestRestart(other, "yardmaster", "because", "", a)
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

// Removing a peer is the operator's alone, as adding one is: a crew
// member is refused with the alone mark and the link stays, and the
// operator removes.
func TestPeerRemoveIsTheOperatorsAlone(t *testing.T) {
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

// Adding a hook, or changing one's source or secret file, is the
// operator's alone; what one says, makes, maps or acts as is anyone's to
// change, recorded as the pane's actor from a pane, and a hook that acts
// as a crew member still speaks as it.
func TestPutHookCreateAndSourceChangeAreTheOperators(t *testing.T) {
	hookYard := func(t *testing.T) (store.API, *store.Store, context.Context) {
		t.Helper()
		api, own := operatorYard(t)
		ctx := context.Background()
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
	// So is whom it acts as, with its map, under today's rule: the crew
	// member speaks as itself.
	if err := api.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u-yardmaster"}, "yardmaster"); err != nil {
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

// A yard that names no operator has nobody to hold the rule to: it takes
// the change from anyone who is no agent of its own, as a declared source
// is taken (maySource).
func TestOperatorOnlyWithoutAnOperator(t *testing.T) {
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

// A timer set from a session's pane is the pane's actor's, whatever
// --actor says: a pane can never set one as the operator. From no pane
// the actor stands as given (yardr-6g56).
func TestPaneTimerIsThePanesActor(t *testing.T) {
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
	// completion lands its event.
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
	if now, _ := s.Head(ctx); now-head != 5 {
		t.Errorf("the operator's completions logged %d events, want 5", now-head)
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
	if now, _ := s.Head(ctx); now-head != 5 {
		t.Errorf("the pane's completions logged %d events, want 5", now-head)
	}
}
