package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// crmFile is a source no user can be told through: it has no notify key.
const crmFile = `name: crm
base: https://crm.example.com
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// A user names the source that tells them and the file their address is
// in. The user sets it, or the operator; a source without a notify key is
// refused with the ones that have one; the event has the names and the
// path.
func TestUserSet(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "crm.yaml", crmFile, "jo"); err != nil {
		t.Fatal(err)
	}
	_, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "crm"}, "ada")
	if err == nil || !strings.Contains(err.Error(), "user ada: the source crm has no notify key") || !strings.Contains(err.Error(), "No source of this yard can yet") {
		t.Fatalf("a source without a notify key, in a yard without one that has: %v", err)
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	_, err = st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "crm"}, "ada")
	if err == nil || !strings.Contains(err.Error(), "the source crm has no notify key, so its file names no call that tells a person. A user can be told through: pushover") {
		t.Fatalf("a source without a notify key: %v", err)
	}
	_, err = st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "linear"}, "ada")
	if err == nil || !strings.Contains(err.Error(), "linear is no source a user can be told through. A user can be told through: pushover") {
		t.Fatalf("a source that is not declared: %v", err)
	}
	if u, _ := st.GetUser(ctx, "ada"); u.Notify != "" {
		t.Fatalf("a refused notify was kept: %+v", u)
	}

	u, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/.yardr/users/ada.env"}, "ada")
	if err != nil || u.Notify != "pushover" || u.EnvFile != "/home/ada/.yardr/users/ada.env" {
		t.Fatalf("the user, for themselves: %+v, %v", u, err)
	}
	// Reading the inbox rewrites the record, and keeps them.
	if err := st.AckUserInbox(ctx, "ada", 1<<40); err != nil {
		t.Fatal(err)
	}
	if u, _ = st.GetUser(ctx, "ada"); u.Notify != "pushover" || u.EnvFile == "" {
		t.Fatalf("after an acknowledgement: %+v", u)
	}
	if u, err = st.SetUser(ctx, "ada", map[string]string{store.UserNotify: ""}, "jo"); err != nil || u.Notify != "" || u.EnvFile == "" {
		t.Fatalf("the operator, taking the source away: %+v, %v", u, err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{"push": "mail"}, "ada"); err == nil || !strings.Contains(err.Error(), "push is no setting of a user") {
		t.Fatalf("a setting a user does not have: %v", err)
	}
	if _, err := st.SetUser(ctx, "ada", nil, "ada"); err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("nothing to set: %v", err)
	}
	if _, err := st.SetUser(ctx, "bob", map[string]string{store.UserNotify: "pushover"}, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a user that is not there: %v", err)
	}

	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var set []map[string]any
	for _, e := range evs {
		if e.Kind == "user_set" {
			set = append(set, e.Data)
		}
	}
	raw, _ := json.Marshal(set)
	if string(raw) != `[{"env_file":"/home/ada/.yardr/users/ada.env","notify":"pushover","user":"ada"},{"notify":"","user":"ada"}]` {
		t.Fatalf("user_set events = %s", raw)
	}
}

// Where a person is told is theirs to set, or the operator's: anyone else
// is refused by name, and an agent by its pane, whatever name the command
// gives. Pointing a person's notify elsewhere sends their inbox out of the
// yard.
func TestUserSetIsTheUsersAndTheOperators(t *testing.T) {
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if _, err := api.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutSource(ctx, "p.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/tmp/mine.env"}
	const why = "where ada is told outside the yard is set by ada, or by the operator (jo): what lands in their inbox goes there; "
	for actor, want := range map[string]string{
		"yardmaster":     why + "yardmaster is a crew member",
		"bob":            why + "this is bob",
		"yardr-1my8-new": why + "this is yardr-1my8-new",
		"":               why + "this is nobody",
	} {
		if _, err := api.SetUser(ctx, "ada", set, actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SetUser as %q = %v", actor, err)
		}
	}

	// From the yardmaster's pane, naming the user and then the operator.
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	for _, actor := range []string{"ada", "jo"} {
		if _, err := own.SetUser(pane, "ada", set, actor); err == nil || !strings.Contains(err.Error(), why+"this is the crew member yardmaster") {
			t.Fatalf("SetUser from the yardmaster's pane as %s = %v", actor, err)
		}
		// Taking it away is pointing it too: refused the same.
		if _, err := own.SetUser(pane, "ada", map[string]string{store.UserNotify: ""}, actor); err == nil {
			t.Fatalf("SetUser notify= from the yardmaster's pane as %s was taken", actor)
		}
	}
	if u, _ := api.GetUser(ctx, "ada"); u.Notify != "" || u.EnvFile != "" {
		t.Fatalf("a refused change was made: %+v", u)
	}
	for _, actor := range []string{"ada", "jo"} {
		if _, err := own.SetUser(ctx, "ada", set, actor); err != nil {
			t.Fatalf("%s, from no agent's pane: %v", actor, err)
		}
	}
}

// A bead's agent is refused from its pane, whatever --actor says.
func TestUserSetRefusedFromASessionsPane(t *testing.T) {
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSource(ctx, "p.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	for _, actor := range []string{"ada", "jo", sess.ID} {
		for _, set := range []map[string]string{{store.UserNotify: "pushover"}, {store.UserEnvFile: "/tmp/mine.env"}} {
			if _, err := s.SetUser(pane, "ada", set, actor); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
				t.Fatalf("SetUser %v from a session's pane as %s = %v", set, actor, err)
			}
		}
	}
	if u, _ := s.GetUser(ctx, "ada"); u.Notify != "" || u.EnvFile != "" {
		t.Fatalf("a refused change was made: %+v", u)
	}
}

// A source yardr has in Go that can tell a person is taken as a declared
// one with a notify key is, and named with them.
func TestUserSetTakesAnAdapterThatNotifies(t *testing.T) {
	store.NotifyAdapters(func() []string { return []string{"linear"} })
	t.Cleanup(func() { store.NotifyAdapters(func() []string { return nil }) })
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "p.yaml", sourceFile, "ada"); err != nil {
		t.Fatal(err)
	}
	if u, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "linear"}, "ada"); err != nil || u.Notify != "linear" {
		t.Fatalf("an adapter that notifies: %+v, %v", u, err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "github"}, "ada"); err == nil || !strings.Contains(err.Error(), "A user can be told through: linear, pushover") {
		t.Fatalf("an adapter that does not: %v", err)
	}
}
