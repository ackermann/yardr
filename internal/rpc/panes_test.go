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
	if got, err := st.GetBead(ctx, b.ID); err != nil || store.PinnedFlow(got) != "" {
		t.Errorf("bead after the refused pin = %+v, %v: want no pin", got, err)
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
