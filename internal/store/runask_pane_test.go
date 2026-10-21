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

// paneYard is a yard whose operator is jo, with the crew member yardmaster,
// the peer airy and a bead the builders work, for what is asked from an
// agent's pane.
func paneYard(t *testing.T) (*store.Store, *store.Bead) {
	t.Helper()
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPeer(ctx, store.Peer{Name: "airy", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

func mailLimitAsk() store.Ask {
	return store.Ask{Question: "Take more mail from airy?", To: "jo",
		Context: []string{"airy's yard was refused 4 mails in the last hour."},
		Run:     &store.AskRun{Argv: []string{"peer allow airy --mail-limit 300"}}}
}

// A bead's agent writes no run ask, whatever actor it names: the store
// knows it by its pane. And it approves none.
func TestRunAskIsNotWrittenOrApprovedFromASessionsPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	seq, err := s.AddAsk(ctx, b.ID, mailLimitAsk(), "yardmaster")
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
	for _, actor := range []string{"jo", "yardmaster", sess.ID} {
		_, err := s.AddAsk(pane, b.ID, mailLimitAsk(), actor)
		if want := "a command for the operator to approve is proposed by a crew member or a user of the yard; this is the session " + sess.ID + " of a bead's agent"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AddAsk from a session's pane as %s = %v", actor, err)
		}
		_, err = s.ApproveAsk(pane, b.ID, seq, actor, false)
		if want := "approving a command is the operator's (jo): the yard runs it as them; this is the session " + sess.ID + " of a bead's agent"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from a session's pane as %s = %v", actor, err)
		}
	}
	if notes, err := s.Notes(ctx, b.ID); err != nil || len(notes) != 1 {
		t.Fatalf("the bead has %d notes, %v: want the one ask", len(notes), err)
	}
	if p, err := s.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != store.PeerMailLimit {
		t.Fatalf("the link = %+v, %v: a refused approval changed it", p, err)
	}
	// Any other ask is the agent's to write, as before.
	if _, err := s.AddAsk(pane, b.ID, store.Ask{Question: "Which?", To: "jo", Context: []string{"c"}}, sess.ID); err != nil {
		t.Fatalf("a plain ask from the session's pane: %v", err)
	}
}

// A crew member proposes a command from its pane, and approves none from
// it, also when the call names the operator: an agent cannot approve its
// own ask. Outside the pane the operator approves.
func TestRunAskIsNotApprovedFromACrewMembersPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	seq, err := s.AddAsk(pane, b.ID, mailLimitAsk(), "yardmaster")
	if err != nil {
		t.Fatalf("the yardmaster's ask from its pane: %v", err)
	}
	const want = "approving a command is the operator's (jo): the yard runs it as them; this is the crew member yardmaster"
	for _, actor := range []string{"jo", "yardmaster"} {
		if _, err := s.ApproveAsk(pane, b.ID, seq, actor, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from the yardmaster's pane as %s = %v", actor, err)
		}
	}
	if p, err := s.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != store.PeerMailLimit {
		t.Fatalf("the link = %+v, %v: a refused approval changed it", p, err)
	}
	if _, err := s.ApproveAsk(ctx, b.ID, seq, "jo", true); err != nil {
		t.Fatalf("the operator, from no agent's pane: %v", err)
	}
	if p, err := s.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != 300 {
		t.Fatalf("the link after the approval = %+v, %v", p, err)
	}
}

// A user's own subscription is approved from no agent's pane either,
// whatever actor the call names: the check a subscription has goes by the
// name alone, and the approval asks the pane first.
func TestRunAskSubscribeIsNotApprovedFromACrewMembersPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	seq, err := s.AddAsk(pane, b.ID, store.Ask{Question: "Hear of what lands?", To: "jo",
		Context: []string{"You asked to be told when a bead is done."},
		Run:     &store.AskRun{Argv: []string{"subscribe jo yardr --done"}}}, "yardmaster")
	if err != nil {
		t.Fatalf("the yardmaster's ask from its pane: %v", err)
	}
	const want = "approving this command is jo's, whom it was asked of: the yard runs it as them; this is the crew member yardmaster"
	for _, actor := range []string{"jo", "yardmaster"} {
		if _, err := s.ApproveAsk(pane, b.ID, seq, actor, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from the yardmaster's pane as %s = %v", actor, err)
		}
	}
	if u, err := s.GetUser(ctx, "jo"); err != nil || len(u.Subscriptions) != 0 {
		t.Fatalf("jo = %+v, %v: a refused approval subscribed them", u, err)
	}
	if _, err := s.ApproveAsk(ctx, b.ID, seq, "jo", false); err != nil {
		t.Fatalf("jo, from no agent's pane: %v", err)
	}
	if u, err := s.GetUser(ctx, "jo"); err != nil || len(u.Subscriptions) != 1 {
		t.Fatalf("jo after the approval = %+v, %v", u, err)
	}
}

// Which parts a user is told of is approved from no agent's pane either,
// whatever actor the call names: the asking crew member cannot approve its
// own ask as the user (yardr-6k0b).
func TestRunAskUserTellIsNotApprovedFromACrewMembersPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	seq, err := s.AddAsk(pane, b.ID, store.Ask{Question: "Leave mail off the phone?", To: "jo",
		Context: []string{"You said an evening's mail is too much there."},
		Run:     &store.AskRun{Argv: []string{"user set jo tell=notifications,asks"}}}, "yardmaster")
	if err != nil {
		t.Fatalf("the yardmaster's ask from its pane: %v", err)
	}
	const want = "approving this command is jo's, whom it was asked of: the yard runs it as them; this is the crew member yardmaster"
	for _, actor := range []string{"jo", "yardmaster"} {
		if _, err := s.ApproveAsk(pane, b.ID, seq, actor, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from the yardmaster's pane as %s = %v", actor, err)
		}
	}
	if u, err := s.GetUser(ctx, "jo"); err != nil || len(u.Tell) != 0 {
		t.Fatalf("jo = %+v, %v: a refused approval set their tell", u, err)
	}
	if _, err := s.ApproveAsk(ctx, b.ID, seq, "jo", false); err != nil {
		t.Fatalf("jo, from no agent's pane: %v", err)
	}
	if u, err := s.GetUser(ctx, "jo"); err != nil || strings.Join(u.Tell, ",") != "notifications,asks" {
		t.Fatalf("jo after the approval = %+v, %v", u, err)
	}
}

// A crew member's agent takes its first prompt before the daemon has
// recorded its start, and approves nothing in those seconds either: the
// pane is on record before the agent is started in it (yardr-6k0b, where
// the yardmaster's first two commands were an ask and its approval as jo).
// A start that failed leaves the pane the crew member's while its shell
// still runs: the daemon may not have been able to close it.
func TestRunAskIsNotApprovedFromAStartingCrewMembersPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := s.CrewStarting(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetCrew(ctx, "yardmaster"); err != nil || r.Live() {
		t.Fatalf("the yardmaster = %+v, %v: its start is not recorded yet", r, err)
	}
	pane := store.WithCaller(ctx, me)
	seq, err := s.AddAsk(pane, b.ID, mailLimitAsk(), "yardmaster")
	if err != nil {
		t.Fatalf("the yardmaster's ask from its pane: %v", err)
	}
	const want = "approving a command is the operator's (jo): the yard runs it as them; this is the crew member yardmaster"
	for _, actor := range []string{"jo", "yardmaster"} {
		if _, err := s.ApproveAsk(pane, b.ID, seq, actor, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from the starting yardmaster's pane as %s = %v", actor, err)
		}
	}
	if p, err := s.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != store.PeerMailLimit {
		t.Fatalf("the link = %+v, %v: a refused approval changed it", p, err)
	}
	// The start is recorded: the pane is the running session's.
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetCrew(ctx, "yardmaster"); err != nil || r.Starting != nil {
		t.Fatalf("the yardmaster = %+v, %v: a recorded start keeps no starting pane", r, err)
	}
	if _, err := s.ApproveAsk(pane, b.ID, seq, "jo", false); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ApproveAsk from the started yardmaster's pane as jo = %v", err)
	}
	// A start that failed after its agent ran, in a pane the daemon could
	// not close: the shell still runs, and the pane is still the crew
	// member's.
	if err := s.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStarting(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStartFailed(ctx, "yardmaster", "herdr agent start: boom", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveAsk(pane, b.ID, seq, "jo", false); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ApproveAsk as jo from the pane a failed start left open = %v", err)
	}
	// A shell without a start time cannot be told from a later process
	// with its pid: a failed start keeps no such pane.
	if err := s.CrewStarting(ctx, "yardmaster", map[string]any{"shell_pid": me}); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStartFailed(ctx, "yardmaster", "herdr agent start: boom", 0); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetCrew(ctx, "yardmaster"); err != nil || r.Starting != nil {
		t.Fatalf("the yardmaster = %+v, %v: a failed start kept a pane without a start time", r, err)
	}
	// One whose pane was closed: a later process that has the shell's pid
	// is nobody's pane.
	if err := s.CrewStarting(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start + 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStartFailed(ctx, "yardmaster", "herdr agent start: boom", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveAsk(pane, b.ID, seq, "jo", true); err != nil {
		t.Fatalf("the operator, after the start failed and its pane was closed: %v", err)
	}
}

// The same of a bead's session: its pane is on its handle while it is
// still starting, before the dispatcher has marked it running, and the
// agent in it approves nothing as the operator.
func TestRunAskIsNotApprovedFromAStartingSessionsPane(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t)
	ctx := context.Background()
	seq, err := s.AddAsk(ctx, b.ID, mailLimitAsk(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	pane := store.WithCaller(ctx, me)
	handle := map[string]any{"agent": "yard-a-new", "shell_pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["shell_start"] = ps.Start
	}
	if err := s.SetHandle(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(ctx, sess.ID); err != nil || got.State != "starting" {
		t.Fatalf("the session = %+v, %v: want it still starting", got, err)
	}
	for _, actor := range []string{"jo", sess.ID} {
		_, err := s.ApproveAsk(pane, b.ID, seq, actor, false)
		if want := "approving a command is the operator's (jo): the yard runs it as them; this is the session " + sess.ID + " of a bead's agent"; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ApproveAsk from a starting session's pane as %s = %v", actor, err)
		}
	}
	if p, err := s.GetPeer(ctx, "airy"); err != nil || p.MailsAnHour() != store.PeerMailLimit {
		t.Fatalf("the link = %+v, %v: a refused approval changed it", p, err)
	}
}
