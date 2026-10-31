package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// startingYard is deviceYard with the depot yard, the builders group and a
// bead in new, for what a session calls before its start is recorded.
func startingYard(t *testing.T) (store.API, *store.Bead) {
	t.Helper()
	api, _ := deviceYard(t)
	ctx := context.Background()
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, api, "yard", "", outcomeFlow)
	if err := api.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return api, b
}

// startingCall is ctx calling from the test process itself, for whose pane
// the store has no record, as the session id: what an exec session's first
// call looks like, before its handle is recorded. Over the socket the
// session travels in the call's header, set on the client, since the
// context does not cross it.
func startingCall(t *testing.T, api store.API, ctx context.Context, id string) context.Context {
	t.Helper()
	if c, ok := api.(*rpc.Client); ok {
		c.Session = id
	}
	return store.WithSession(store.WithCaller(ctx, os.Getpid()), id)
}

// An exec session's first call, before the dispatcher has recorded its
// handle, names a session with no pane on record: adding a device is
// refused as its session's, whatever actor it names, rather than passing
// on the name alone (yardr-gbax). Once the handle is recorded the same
// call is the session's as before.
func TestDeviceAddRefusedWhileItsSessionIsStarting(t *testing.T) {
	t.Parallel()
	api, b := startingYard(t)
	ctx := context.Background()
	if _, err := api.AddDevice(ctx, "ada", "tablet", "jo"); err != nil {
		t.Fatalf("the operator, from no agent's pane: %v", err)
	}
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	call := startingCall(t, api, ctx, sess.ID)
	want := "the session " + sess.ID + " is starting and not on record yet"
	for _, actor := range []string{"jo", "ada", sess.ID} {
		if _, err := api.AddDevice(call, "ada", "phone", actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AddDevice from a starting session as %s = %v", actor, err)
		}
	}
	// A session the store never started is refused the same way.
	unknown := startingCall(t, api, ctx, "s-unknown")
	if _, err := api.AddDevice(unknown, "ada", "phone", "jo"); err == nil || !strings.Contains(err.Error(), "the session s-unknown is starting and not on record yet") {
		t.Errorf("AddDevice naming an unknown session as jo = %v", err)
	}
	// The handle is recorded: the same call is the session's, refused for
	// an operator-only action as before.
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := api.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddDevice(call, "ada", "phone", "jo"); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
		t.Errorf("AddDevice from the running session's pane as jo = %v", err)
	}
	if list, _ := api.Devices(ctx, ""); len(list) != 1 || list[0].Name != "tablet" {
		t.Fatalf("devices = %+v, want the operator's tablet alone", list)
	}
}

// A manual session is a person's claim, not an agent's: naming one still
// starting refuses nothing.
func TestDeviceAddAllowedWhileAManualSessionIsStarting(t *testing.T) {
	t.Parallel()
	api, _ := startingYard(t)
	ctx := context.Background()
	if err := api.PutGroup(ctx, store.Group{Name: "humans", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "m", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := api.Claim(ctx, b.ID, "humans", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := api.GetSession(ctx, sess.ID); err != nil || got.State != "starting" {
		t.Fatalf("the session = %+v, %v: want it still starting", got, err)
	}
	if _, err := api.AddDevice(startingCall(t, api, ctx, sess.ID), "ada", "phone", "jo"); err != nil {
		t.Fatalf("the operator, naming their starting manual session: %v", err)
	}
}

// The same of a run ask: written by nobody, and approved by nobody, from a
// session still starting, whatever actor the call names.
func TestRunAskIsNotWrittenOrApprovedWhileItsSessionIsStarting(t *testing.T) {
	t.Parallel()
	api, b := startingYard(t)
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddPeer(ctx, store.Peer{Name: "airy", Key: store.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	seq, err := api.AddAsk(ctx, b.ID, mailLimitAsk(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	call := startingCall(t, api, ctx, sess.ID)
	want := "the session " + sess.ID + " is starting and not on record yet"
	if _, err := api.AddAsk(call, b.ID, mailLimitAsk(), "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("AddAsk from a starting session as jo = %v", err)
	}
	if _, err := api.ApproveAsk(call, b.ID, seq, "jo", false, nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ApproveAsk from a starting session as jo = %v", err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := api.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	whose := "this is the session " + sess.ID + " of a bead's agent"
	if _, err := api.AddAsk(call, b.ID, mailLimitAsk(), "jo"); err == nil || !strings.Contains(err.Error(), whose) {
		t.Errorf("AddAsk from the running session's pane as jo = %v", err)
	}
	if _, err := api.ApproveAsk(call, b.ID, seq, "jo", false, nil); err == nil || !strings.Contains(err.Error(), whose) {
		t.Errorf("ApproveAsk from the running session's pane as jo = %v", err)
	}
	if notes, err := api.Notes(ctx, b.ID); err != nil || len(notes) != 1 {
		t.Fatalf("the bead has %d notes, %v: want the one ask", len(notes), err)
	}
	if p, err := api.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != store.PeerMailLimit {
		t.Fatalf("the link = %+v, %v: a refused approval changed it", p, err)
	}
}

// The same of a declared source: neither put nor keyed from a session
// still starting, whatever actor the call names.
func TestDeclaredSourceRefusedWhileItsSessionIsStarting(t *testing.T) {
	t.Parallel()
	api, b := startingYard(t)
	ctx := context.Background()
	if _, err := api.PutSource(ctx, "p.yaml", sourceFile, "jo"); err != nil {
		t.Fatalf("the operator, from no agent's pane: %v", err)
	}
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	call := startingCall(t, api, ctx, sess.ID)
	want := "the session " + sess.ID + " is starting and not on record yet"
	if _, err := api.PutSource(call, "q.yaml", limitsFile, "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("PutSource from a starting session as jo = %v", err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := api.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutSource(call, "q.yaml", limitsFile, "jo"); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
		t.Errorf("PutSource from the running session's pane as jo = %v", err)
	}
	if all, _ := api.Sources(ctx); len(all) != 1 {
		t.Fatalf("sources = %+v, want the operator's one alone", all)
	}
}
