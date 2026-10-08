package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	api, _ := operatorYard(t)
	ctx := context.Background()
	key := peerKey(t)
	grant := store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{store.OpenThreads: "linear", store.MailPeers: "north"}}
	writes := map[string]func(actor string) error{
		"add a user":    func(a string) error { _, err := api.AddUser(ctx, "grace", a); return err },
		"remove a user": func(a string) error { return api.DeleteUser(ctx, "ada", a) },
		"add a peer": func(a string) error {
			_, err := api.AddPeer(ctx, store.Peer{Name: "south", Key: key}, a)
			return err
		},
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
	// Its pane is its own, not another crew member's.
	if _, err := own.Replying(pane, "clerk", 7, "", "hello", "yard-10-new"); err == nil ||
		!strings.Contains(err.Error(), "only clerk's own session or the operator (jo) may speak as clerk; this is yard-10-new") {
		t.Errorf("replying as clerk from yardmaster's pane = %v", err)
	}
}
