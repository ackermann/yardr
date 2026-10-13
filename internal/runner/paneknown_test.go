package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// paneStore is a store that notes what herdr had been asked when a pane
// was put on record, and can refuse the record.
type paneStore struct {
	store.API
	t      *testing.T
	calls  string // the fake herdr's log
	refuse error

	id          string
	handle      map[string]any
	before      string // herdr's calls when the pane record was made
	firstStart  map[string]any
	startBefore string // herdr's calls when the start was first recorded
}

func (p *paneStore) record(id string, handle map[string]any) error {
	p.id, p.handle, p.before = id, handle, readCalls(p.t, p.calls)
	return p.refuse
}

func (p *paneStore) SetHandle(_ context.Context, id string, handle map[string]any) error {
	return p.record(id, handle)
}

func (p *paneStore) CrewStarting(ctx context.Context, name string, handle map[string]any) error {
	if _, pane := handle["pane"]; !pane && p.firstStart == nil {
		p.firstStart = handle
		p.startBefore = readCalls(p.t, p.calls)
		return p.API.CrewStarting(ctx, name, handle)
	}
	if err := p.record(name, handle); err != nil {
		return err
	}
	return p.API.CrewStarting(ctx, name, handle)
}

// shellHerdr is envHerdr whose panes tell their shell: this process.
func shellHerdr(t *testing.T, wt string) (*Herdr, *paneStore) {
	h, calls, _ := envHerdr(t, wt)
	h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p1","shell_pid":`+fmt.Sprint(os.Getpid())+`}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
	st := &paneStore{API: storetest.New(t), t: t, calls: calls}
	h.Store = st
	return h, st
}

// The store holds a crew member's pane before the agent is started in it:
// the agent takes its first prompt seconds before the daemon records the
// start, and in them a command of its own that named the operator passed
// as the operator's (yardr-6k0b). A store that does not take the pane
// starts no agent.
func TestStartCrewRecordsItsPaneBeforeTheAgentStarts(t *testing.T) {
	ctx := context.Background()
	h, st := shellHerdr(t, "")
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.StartCrew(ctx, store.CrewMember{Name: "yardmaster", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if st.firstStart == nil || st.firstStart[store.HandleHerdrSession] != "" || st.startBefore != "" {
		t.Fatalf("the incomplete crew start was not recorded before herdr opened a workspace: handle %v, prior calls %q", st.firstStart, st.startBefore)
	}
	if st.id != "yardmaster" || st.handle["shell_pid"] != os.Getpid() || st.handle["shell_start"] == nil {
		t.Fatalf("recorded for %q: %v, want the yardmaster's shell %d and its start", st.id, st.handle, os.Getpid())
	}
	if strings.Contains(st.before, "agent start") || !strings.Contains(readCalls(t, st.calls), "agent start") {
		t.Fatalf("the pane was recorded after the agent started; herdr's calls before the record:\n%s", st.before)
	}
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil || r.Live() || fmt.Sprint(r.Starting["shell_pid"]) != fmt.Sprint(os.Getpid()) {
		t.Fatalf("the yardmaster = %+v, %v: want its starting pane on record", r, err)
	}

	h, st = shellHerdr(t, "")
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	st.refuse = errors.New("the store is away")
	handle, err := h.StartCrew(ctx, store.CrewMember{Name: "yardmaster", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "the store is away") || strings.Contains(readCalls(t, st.calls), "agent start") {
		t.Fatalf("a pane the store did not take: %v, calls:\n%s", err, readCalls(t, st.calls))
	}
	// What was opened goes back, for the dispatcher to close.
	if handle["workspace"] == nil {
		t.Fatalf("the handle of the failed start = %v, want its workspace", handle)
	}
}

// Without the shell PID, the store cannot distinguish the agent's pane
// from an operator calling from outside it. Do not start that agent with
// the actor-name checks as its only guard.
func TestStartDoesNotRunAgentWithoutARecordedPane(t *testing.T) {
	ctx := context.Background()
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	for name, depot := range map[string]*store.Depot{
		"git": {Name: "yard", Path: t.TempDir(), Base: "main"},
		"dir": {Name: "notes", Path: t.TempDir(), Kind: store.DepotDir},
	} {
		h, st := shellHerdr(t, wt)
		h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+st.calls+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p1"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
		job := dispatch.Job{
			Bead:    store.Bead{ID: "yard-13", Stage: "new"},
			Group:   store.Group{Name: "yard-builders"},
			Depot:   depot,
			Session: store.Session{ID: "s1"},
		}
		_, err := h.Start(ctx, job)
		calls := readCalls(t, st.calls)
		if err == nil || !strings.Contains(err.Error(), "did not report the pane shell") || strings.Contains(calls, "agent start") {
			t.Errorf("%s depot: start without a recorded pane = %v, calls:\n%s", name, err, calls)
		}
	}

	h, st := shellHerdr(t, "")
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+st.calls+`
case "$1 $2" in
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p1"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
	_, err := h.StartCrew(ctx, store.CrewMember{Name: "yardmaster", Dir: t.TempDir()})
	calls := readCalls(t, st.calls)
	if err == nil || !strings.Contains(err.Error(), "did not report the pane shell") || strings.Contains(calls, "agent start") {
		t.Errorf("crew start without a recorded pane = %v, calls:\n%s", err, calls)
	}
}

// The same of a bead's session, in a worktree and in a dir depot: its
// handle is the store's before its agent starts.
func TestHerdrStartRecordsItsPaneBeforeTheAgentStarts(t *testing.T) {
	ctx := context.Background()
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	for name, depot := range map[string]*store.Depot{
		"git": {Name: "yard", Path: t.TempDir(), Base: "main"},
		"dir": {Name: "notes", Path: t.TempDir(), Kind: store.DepotDir},
	} {
		job := dispatch.Job{
			Bead:    store.Bead{ID: "yard-13", Stage: "new"},
			Group:   store.Group{Name: "yard-builders"},
			Depot:   depot,
			Session: store.Session{ID: "s1"},
		}
		h, st := shellHerdr(t, wt)
		if _, err := h.Start(ctx, job); err != nil {
			t.Fatalf("%s depot: %v", name, err)
		}
		if st.id != "s1" || st.handle["shell_pid"] != os.Getpid() || st.handle["pane"] == nil {
			t.Fatalf("%s depot: recorded for %q: %v, want the session's pane and shell %d", name, st.id, st.handle, os.Getpid())
		}
		if strings.Contains(st.before, "agent start") || !strings.Contains(readCalls(t, st.calls), "agent start") {
			t.Fatalf("%s depot: the pane was recorded after the agent started; herdr's calls before the record:\n%s", name, st.before)
		}

		h, st = shellHerdr(t, wt)
		st.refuse = errors.New("the store is away")
		handle, err := h.Start(ctx, job)
		if err == nil || !strings.Contains(err.Error(), "the store is away") || strings.Contains(readCalls(t, st.calls), "agent start") {
			t.Fatalf("%s depot: a pane the store did not take: %v, calls:\n%s", name, err, readCalls(t, st.calls))
		}
		if handle["tab"] == nil {
			t.Fatalf("%s depot: the handle of the failed start = %v, want its tab", name, handle)
		}
	}
}
