package rpc_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// paneYard is a yard served on a socket of its own, its store for the
// in-process writes a test sets it up with (no caller: the daemon's too),
// and a client whose calls come from this test process.
func paneYard(t *testing.T) (*store.Store, *rpc.Client, string) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	// Unix socket paths are limited to ~108 bytes; t.TempDir is too long.
	dir, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rpc.Handler(st, nil), ConnContext: rpc.ConnContext}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return st, rpc.NewClient(sock), home
}

// thisPane is the handle that makes this test process's own shell the
// pane a session or a crew member runs in.
func thisPane(t *testing.T, agent string) map[string]any {
	t.Helper()
	me, err := peer.Describe(os.Getpid())
	if err != nil {
		t.Skipf("the OS does not describe this process: %v", err)
	}
	return map[string]any{"agent": agent, "shell_pid": os.Getpid(), "shell_start": me.Start}
}

var trains = store.Flow{Name: "trains", Stages: []string{"open", "landed"},
	Transitions: map[string][]string{"open": {"landed"}}, Terminal: []string{"landed"}}

// From a bead session's pane the socket refuses what defines how the yard
// works (groups, flows), the daemon's session writes, a backup, and a pin
// of the bead's own flow, before the store's own checks are asked: a
// bead's agent cannot give an exec group a command, or route its bead
// past review. What an agent does with its bead is still served.
func TestSocketRefusesABeadsPaneWhatIsNotAnAgents(t *testing.T) {
	st, c, home := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3}, "jo"))
	_, err := st.PutFlow(ctx, trains, "jo")
	must(err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "one"}, "jo")
	must(err)
	sess, err := st.Claim(ctx, b.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, sess.ID, thisPane(t, "yard-one-new")))
	if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != store.CallerBead {
		t.Fatalf("this process is %v, %v: want a bead's pane", kind, err)
	}

	backups := filepath.Join(home, "backups")
	must(os.MkdirAll(backups, 0o700))
	refused := []struct {
		method string
		call   func() error
	}{
		{"PutGroup", func() error {
			return c.PutGroup(ctx, store.Group{Name: "sh", Runner: "exec", Config: map[string]any{"cmd": "touch /tmp/owned"}}, "jo")
		}},
		{"SetGroup", func() error {
			_, err := c.SetGroup(ctx, "builders", store.GroupEdit{Set: map[string]any{"args": []string{"--yolo"}}}, "jo")
			return err
		}},
		{"PutFlow", func() error {
			_, err := c.PutFlow(ctx, store.Flow{Name: "skip", Stages: []string{"new", "approved"},
				Transitions: map[string][]string{"new": {"approved"}}, Terminal: []string{"approved"}}, "jo")
			return err
		}},
		{"SetFlowPointer", func() error { return c.SetFlowPointer(ctx, "", "", "trains", "jo") }},
		{"Backup", func() error {
			_, err := c.Backup(ctx, filepath.Join(backups, "yard.db"))
			return err
		}},
		{"EndSession", func() error { return c.EndSession(ctx, sess.ID, "aborted", "an agent ends itself") }},
		{"MarkStopped", func() error { return c.MarkStopped(ctx, sess.ID) }},
		{"FoldBeads", func() error {
			_, err := c.FoldBeads(ctx, []string{b.ID}, store.FoldOptions{To: "backlog"}, "jo")
			return err
		}},
	}
	for _, r := range refused {
		if err := r.call(); err == nil || !strings.HasPrefix(err.Error(), r.method+" is ") {
			t.Errorf("%s from a bead's pane = %v, want it refused by its class", r.method, err)
		}
	}
	// The pin is refused by the store: UpdateBead is an agent's call.
	_, err = c.UpdateBead(ctx, b.ID, store.BeadPatch{Metadata: map[string]any{"flow": "trains"}}, store.Expect{}, "jo")
	if err == nil || !strings.Contains(err.Error(), "may not pin a bead's flow") {
		t.Errorf("UpdateBead pinning metadata.flow from a bead's pane = %v, want it refused", err)
	}

	// None of it reached the store.
	if _, err := st.GetGroup(ctx, "sh"); err == nil {
		t.Error("the refused PutGroup stored a group")
	}
	if _, err := st.GetFlow(ctx, "skip"); err == nil {
		t.Error("the refused PutFlow stored a flow")
	}
	if got, err := st.GetSession(ctx, sess.ID); err != nil || got.State != "running" || got.Stopped {
		t.Errorf("session after refused writes = %+v, %v: want it running", got, err)
	}
	if got, err := st.GetBead(ctx, b.ID); err != nil || store.PinnedFlow(got) != "" || got.Status != "open" {
		t.Errorf("bead after the refused pin and fold = %+v, %v: want no pin, and open", got, err)
	}
	if _, err := os.Stat(filepath.Join(backups, "yard.db")); !os.IsNotExist(err) {
		t.Errorf("the refused backup wrote a file: %v", err)
	}

	// The work itself is served.
	must(c.AddNote(ctx, b.ID, "a note from the agent", "jo"))
	if _, err := c.UpdateBead(ctx, b.ID, store.BeadPatch{Metadata: map[string]any{"pr": "12"}}, store.Expect{}, "jo"); err != nil {
		t.Errorf("UpdateBead of other metadata from a bead's pane: %v", err)
	}
	if _, err := c.Groups(ctx); err != nil {
		t.Errorf("Groups from a bead's pane: %v", err)
	}
}

// A crew member's pane keeps what the yardmaster does today: it changes
// groups and flows. The operator's and the daemon's calls stay theirs.
func TestSocketServesACrewPaneTheYardsRouting(t *testing.T) {
	st, c, _ := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo")
	must(err)
	must(st.CrewStartedFor(ctx, "yardmaster", "", thisPane(t, "yard-yardmaster")))
	if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != store.CallerCrew {
		t.Fatalf("this process is %v, %v: want a crew member's pane", kind, err)
	}

	if err := c.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "yardmaster"); err != nil {
		t.Errorf("PutGroup from a crew pane: %v", err)
	}
	if _, err := c.PutFlow(ctx, trains, "yardmaster"); err != nil {
		t.Errorf("PutFlow from a crew pane: %v", err)
	}
	if _, err := st.GetGroup(ctx, "builders"); err != nil {
		t.Errorf("the crew pane's group: %v", err)
	}
	if _, err := st.GetFlow(ctx, "trains"); err != nil {
		t.Errorf("the crew pane's flow: %v", err)
	}
	if err := c.DeletePeer(ctx, "far", "jo"); err == nil || !strings.HasPrefix(err.Error(), "DeletePeer is the operator's alone") {
		t.Errorf("DeletePeer from a crew pane = %v, want it the operator's alone", err)
	}
	if err := c.MarkStopped(ctx, "nope"); err == nil || err.Error() != "MarkStopped is the daemon's alone" {
		t.Errorf("MarkStopped from a crew pane = %v, want it the daemon's alone", err)
	}
}

// A runner can make a call before the daemon records its pane handle. Its
// session id still makes the socket refuse admin calls, and the store must
// also refuse a flow pin for that starting session.
func TestSocketRefusesAStartingBeadSessionFlowPin(t *testing.T) {
	st, c, _ := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 1}, "jo"))
	if _, err := st.PutFlow(ctx, trains, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "starting"}, "jo")
	must(err)
	sess, err := st.Claim(ctx, b.ID, "builders", "new", "yard")
	must(err)
	c.Session = sess.ID

	_, err = c.UpdateBead(ctx, b.ID, store.BeadPatch{Metadata: map[string]any{"flow": "trains"}}, store.Expect{Session: sess.ID}, "jo")
	if err == nil || !strings.Contains(err.Error(), "may not pin a bead's flow") {
		t.Fatalf("UpdateBead pin from a starting session = %v, want it refused", err)
	}
	got, err := st.GetBead(ctx, b.ID)
	if err != nil || store.PinnedFlow(got) != "" {
		t.Fatalf("bead after the refused pin = %+v, %v: want no pin", got, err)
	}
}

// An exec session's command is the operator's own, run as its group (the
// assembly that lands a bead and installs the build): it is served what a
// person's terminal is, not held to an agent's calls.
func TestSocketServesAnExecSessionAsTheOperatorsCommand(t *testing.T) {
	st, c, home := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutGroup(ctx, store.Group{Name: "assembly", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "true"}}, "me"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "one"}, "me")
	must(err)
	sess, err := st.Claim(ctx, b.ID, "assembly", b.Stage, "yard")
	must(err)
	me, err := peer.Describe(os.Getpid())
	if err != nil {
		t.Skipf("the OS does not describe this process: %v", err)
	}
	must(st.MarkRunning(ctx, sess.ID, map[string]any{"pid": os.Getpid(), "start": me.Start}))
	if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != store.CallerScript {
		t.Fatalf("this process is %v, %v: want an exec session's command", kind, err)
	}

	backups := filepath.Join(home, "backups")
	must(os.MkdirAll(backups, 0o700))
	if _, err := c.Backup(ctx, filepath.Join(backups, "before-install.db")); err != nil {
		t.Errorf("Backup from an exec session: %v", err)
	}
	if err := c.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Errorf("PutGroup from an exec session: %v", err)
	}
	if err := c.MarkStopped(ctx, sess.ID); err == nil || err.Error() != "MarkStopped is the daemon's alone" {
		t.Errorf("MarkStopped from an exec session = %v, want it the daemon's alone", err)
	}

	// The operator's own program may take away what a bead's agent may not,
	// a hold and a blocks edge, as a person at a terminal may.
	held, err := st.CreateBead(ctx, store.NewBead{Title: "held"}, "me")
	must(err)
	next, err := st.CreateBead(ctx, store.NewBead{Title: "next"}, "me")
	must(err)
	_, err = st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(err)
	must(st.AddDep(ctx, held.ID, next.ID, store.DepBlocks, "", "", "me"))
	if _, err := c.SetHold(ctx, held.ID, false, store.Expect{}, "me"); err != nil {
		t.Errorf("SetHold(false) from an exec session: %v", err)
	}
	if err := c.RemoveDep(ctx, held.ID, next.ID, store.DepBlocks, "", "", "me"); err != nil {
		t.Errorf("RemoveDep of a blocks edge from an exec session: %v", err)
	}
	if isHeld(t, st, held) || edgesOf(t, st, next) != 0 {
		t.Error("the hold or the edge stands after an exec session took it away")
	}
}

// holdBeads are the beads the tests below work on, made by holdYard.
type holdBeads struct {
	mine, other, held, blocker, target, train, child *store.Bead
}

// holdYard is a paneYard with an operator, a builders group and holdBeads:
// one is held, one blocks another, and a train has a child. The yard makes
// the hold and the edges itself, as an earlier stage leaves them.
func holdYard(t *testing.T) (*store.Store, *rpc.Client, holdBeads) {
	t.Helper()
	st, c, _ := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3}, "jo"))
	bead := func(title, typ string) *store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title, Type: typ}, "jo")
		must(err)
		return b
	}
	y := holdBeads{mine: bead("mine", ""), other: bead("other", ""), held: bead("held", ""),
		blocker: bead("blocker", ""), target: bead("target", ""),
		train: bead("train", store.TypeTrain), child: bead("child", "")}
	_, err := st.SetHold(ctx, y.held.ID, true, store.Expect{}, "jo")
	must(err)
	must(st.AddDep(ctx, y.blocker.ID, y.target.ID, store.DepBlocks, "", "", "jo"))
	must(st.AddDep(ctx, y.train.ID, y.child.ID, store.DepParent, "", "", "jo"))
	return st, c, y
}

// isHeld is whether the store has the bead on hold.
func isHeld(t *testing.T, st *store.Store, b *store.Bead) bool {
	t.Helper()
	got, err := st.GetBead(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Hold
}

// edgesOf is how many edges touch the bead, in either direction.
func edgesOf(t *testing.T, st *store.Store, b *store.Bead) int {
	t.Helper()
	ds, err := st.Deps(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds)
}

// A bead's agent may add holds and edges and may not take them away. SetHold
// and RemoveDep are agent calls, so the socket's classes cannot say so and
// the store does. From a bead's pane a hold and an edge are served. Lifting a
// hold, removing a blocks edge and taking a child off its train are refused
// for any beads. Edges of other kinds are still the agent's to remove, and
// Release leaves a hold where it is.
func TestSocketRefusesABeadsPaneTakingAHoldOrABlocksEdgeAway(t *testing.T) {
	st, c, y := holdYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	sess, err := st.Claim(ctx, y.mine.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, sess.ID, thisPane(t, "yard-mine-new")))
	if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != store.CallerBead {
		t.Fatalf("this process is %v, %v: want a bead's pane", kind, err)
	}

	// Adding is served: a hold on another bead and on its own, and edges.
	if _, err := c.SetHold(ctx, y.other.ID, true, store.Expect{}, "jo"); err != nil {
		t.Errorf("SetHold(true) of another bead from a bead's pane: %v", err)
	}
	if _, err := c.SetHold(ctx, y.mine.ID, true, store.Expect{Session: sess.ID}, "jo"); err != nil {
		t.Errorf("SetHold(true) of its own bead from a bead's pane: %v", err)
	}
	if err := c.AddDep(ctx, y.other.ID, y.target.ID, store.DepBlocks, "", "", "jo"); err != nil {
		t.Errorf("AddDep of a blocks edge from a bead's pane: %v", err)
	}
	if err := c.AddDep(ctx, y.blocker.ID, y.target.ID, store.DepBlocks, "approved", "", "jo"); err != nil {
		t.Errorf("AddDep of a staged blocks edge from a bead's pane: %v", err)
	}

	// Release ends a session and holds nothing, so it cannot lift a hold. It
	// runs before any unhold, so the hold it leaves is its own doing.
	if _, err := c.Release(ctx, y.held.ID, "again", store.Expect{}, "jo"); err != nil {
		t.Errorf("Release of a held bead from a bead's pane: %v", err)
	}
	if !isHeld(t, st, y.held) {
		t.Error("Release from a bead's pane lifted a hold")
	}

	// Lifting a hold is refused: one the yard put on, one this agent put on
	// its own bead, one on another's, and a bead that is not held at all.
	for _, b := range []struct {
		bead *store.Bead
		held bool
	}{{y.held, true}, {y.mine, true}, {y.other, true}, {y.child, false}} {
		_, err := c.SetHold(ctx, b.bead.ID, false, store.Expect{}, "jo")
		if err == nil || !strings.Contains(err.Error(), "may hold a bead but not lift a hold") {
			t.Errorf("SetHold(%s, false) from a bead's pane = %v, want it refused", b.bead.ID, err)
		}
		if got := isHeld(t, st, b.bead); got != b.held {
			t.Errorf("%s held = %v after the refused unhold, want %v", b.bead.ID, got, b.held)
		}
	}

	// Removing a blocks edge is refused: plain, staged, between other beads,
	// and one that is not there. So is taking a child off its train, which
	// takes its blocks edge too.
	target, train := edgesOf(t, st, y.target), edgesOf(t, st, y.train)
	for _, r := range []struct {
		what, kind, at string
		from, to       *store.Bead
		not            string
	}{
		{"a blocks edge", store.DepBlocks, "", y.blocker, y.target, "a blocks edge"},
		{"a staged blocks edge", store.DepBlocks, "approved", y.blocker, y.target, "a blocks edge"},
		{"a blocks edge between other beads", store.DepBlocks, "", y.other, y.target, "a blocks edge"},
		{"a blocks edge that is not there", store.DepBlocks, "", y.child, y.mine, "a blocks edge"},
		{"a child from its train", store.DepParent, "", y.train, y.child, "a child from its train"},
	} {
		err := c.RemoveDep(ctx, r.from.ID, r.to.ID, r.kind, r.at, "", "jo")
		if err == nil || !strings.Contains(err.Error(), "may add edges but not remove "+r.not) {
			t.Errorf("RemoveDep of %s from a bead's pane = %v, want it refused", r.what, err)
		}
	}
	if got := edgesOf(t, st, y.target); got != target {
		t.Errorf("the target has %d edges after the refused removals, had %d", got, target)
	}
	if got := edgesOf(t, st, y.train); got != train || got != 2 {
		t.Errorf("the train has %d edges after the refused removal, had %d: want the parent edge and its child's blocks edge", got, train)
	}

	// The other kinds decide no readiness and stay the agent's to remove.
	for _, kind := range []string{"related", store.DepDiscoveredFrom} {
		if err := c.AddDep(ctx, y.blocker.ID, y.other.ID, kind, "", "", "jo"); err != nil {
			t.Errorf("AddDep of a %s edge from a bead's pane: %v", kind, err)
		}
		if err := c.RemoveDep(ctx, y.blocker.ID, y.other.ID, kind, "", "", "jo"); err != nil {
			t.Errorf("RemoveDep of a %s edge from a bead's pane: %v", kind, err)
		}
	}
}

// A person and a crew member's pane lift holds and remove edges. This is how
// the yardmaster removes the edge an agent noted as wrong and unholds what an
// agent held.
func TestSocketServesAPersonAndACrewPaneTakingAHoldOrAnEdgeAway(t *testing.T) {
	for _, who := range []string{"a person", "a crew member's pane"} {
		t.Run(who, func(t *testing.T) {
			st, c, y := holdYard(t)
			ctx := context.Background()
			want := store.CallerPerson
			if who != "a person" {
				if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
					t.Fatal(err)
				}
				if err := st.CrewStartedFor(ctx, "yardmaster", "", thisPane(t, "yard-yardmaster")); err != nil {
					t.Fatal(err)
				}
				want = store.CallerCrew
			}
			if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != want {
				t.Fatalf("this process is %v, %v: want %v", kind, err, want)
			}

			if _, err := c.SetHold(ctx, y.held.ID, false, store.Expect{}, "jo"); err != nil {
				t.Errorf("SetHold(false) from %s: %v", who, err)
			}
			if isHeld(t, st, y.held) {
				t.Errorf("the hold stands after SetHold(false) from %s", who)
			}
			if err := c.RemoveDep(ctx, y.blocker.ID, y.target.ID, store.DepBlocks, "", "", "jo"); err != nil {
				t.Errorf("RemoveDep of a blocks edge from %s: %v", who, err)
			}
			if got := edgesOf(t, st, y.target); got != 0 {
				t.Errorf("the target has %d edges after RemoveDep from %s, want none", got, who)
			}
			if err := c.RemoveDep(ctx, y.train.ID, y.child.ID, store.DepParent, "", "", "jo"); err != nil {
				t.Errorf("RemoveDep of a child from its train from %s: %v", who, err)
			}
			if got := edgesOf(t, st, y.train); got != 0 {
				t.Errorf("the train has %d edges after its child was removed by %s, want none", got, who)
			}
		})
	}
}

// A runner can call before its pane is recorded, naming its session. The
// socket classes that call as a bead's agent, so the store must refuse the
// same unholds and edge removals it refuses a pane.
func TestSocketRefusesAStartingBeadSessionTakingAHoldOrABlocksEdgeAway(t *testing.T) {
	st, c, y := holdYard(t)
	ctx := context.Background()
	sess, err := st.Claim(ctx, y.mine.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	c.Session = sess.ID

	if _, err := c.SetHold(ctx, y.held.ID, false, store.Expect{}, "jo"); err == nil || !strings.Contains(err.Error(), "may hold a bead but not lift a hold") {
		t.Errorf("SetHold(false) from a starting session = %v, want it refused", err)
	}
	if !isHeld(t, st, y.held) {
		t.Error("a starting session lifted a hold")
	}
	if err := c.RemoveDep(ctx, y.blocker.ID, y.target.ID, store.DepBlocks, "", "", "jo"); err == nil || !strings.Contains(err.Error(), "may add edges but not remove a blocks edge") {
		t.Errorf("RemoveDep of a blocks edge from a starting session = %v, want it refused", err)
	}
	if err := c.RemoveDep(ctx, y.train.ID, y.child.ID, store.DepParent, "", "", "jo"); err == nil || !strings.Contains(err.Error(), "may add edges but not remove a child from its train") {
		t.Errorf("RemoveDep of a child from its train from a starting session = %v, want it refused", err)
	}
	if got, want := edgesOf(t, st, y.target)+edgesOf(t, st, y.train), 1+2; got != want {
		t.Errorf("%d edges stand after the refused removals, want %d", got, want)
	}
}
