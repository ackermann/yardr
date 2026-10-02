package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// With herdr missing, every Start fails as a runtime outage: no bead is
// held or charged a failure, and each group gets one runtime_down event.
func TestHerdrMissingHoldsNoBead(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	// A PATH with git and nothing else: no herdr anywhere.
	gitBin, err := exec.LookPath("git")
	mustT(t, err)
	path := t.TempDir()
	mustT(t, os.Symlink(gitBin, filepath.Join(path, "git")))
	t.Setenv("PATH", path)
	h := &Herdr{Bin: "herdr", WorktreeRoot: t.TempDir()}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"herdr": h},
		Backoff: time.Nanosecond, RuntimeBackoff: time.Nanosecond, MaxAttempts: 1}
	mustT(t, st.PutRig(ctx, store.Rig{Name: "r", Path: repo, Base: "main"}, "me"))
	for _, g := range []string{"builders", "reviewers"} {
		mustT(t, st.PutGroup(ctx, store.Group{Name: g, Runner: "herdr", Limit: 2}))
	}
	_, _ = st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	_, _ = st.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"})
	rig := "r"
	var beads []*store.Bead
	for i, stage := range []string{"new", "new", "review"} {
		b, err := st.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("b", i), Stage: stage, Rig: rig}, "me")
		mustT(t, err)
		beads = append(beads, b)
	}
	for range 4 {
		mustT(t, d.Reconcile(ctx))
		time.Sleep(time.Millisecond)
	}
	for _, b := range beads {
		got, _ := st.GetBead(ctx, b.ID)
		if got.Hold || got.Group != nil {
			t.Fatalf("bead %s held or kept by the outage: %+v", b.ID, got)
		}
	}
	evs, _ := st.EventsSince(ctx, 0, 0)
	down := map[string]int{}
	for _, e := range evs {
		switch e.Kind {
		case "runtime_down":
			down[fmt.Sprint(e.Data["group"])]++
			if !strings.Contains(fmt.Sprint(e.Data["reason"]), "herdr") {
				t.Errorf("runtime_down reason does not name herdr: %v", e.Data)
			}
		case "session_failed", "gave_up", "held":
			t.Fatalf("outage counted against a bead: %s %v", e.Kind, e.Data)
		}
	}
	if down["builders"] != 1 || down["reviewers"] != 1 || len(down) != 2 {
		t.Fatalf("runtime_down per group = %v, want one each", down)
	}
}

// A fetch of the base that hangs (a dead remote, a credential prompt) is cut
// off at the deadline, and git is told never to prompt.
func TestFetchBaseHasADeadline(t *testing.T) {
	bin := t.TempDir()
	envOut := filepath.Join(t.TempDir(), "env")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$GIT_TERMINAL_PROMPT\" > " + envOut + "\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &Herdr{FetchTimeout: 200 * time.Millisecond}
	start := time.Now()
	h.fetchBase(context.Background(), &store.Rig{Path: t.TempDir(), Base: "origin/main"})
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("fetch returned after %s; the deadline did not hold", took)
	}
	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("fake git never ran: %v", err)
	}
	if strings.TrimSpace(string(got)) != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
	if FetchTimeout != 60*time.Second {
		t.Fatalf("FetchTimeout = %s", FetchTimeout)
	}
}

// Errors herdr itself reports (an error object) stay the bead's; only a
// herdr that cannot be run or reached is a runtime outage.
func TestHerdrErrorsClassified(t *testing.T) {
	ctx := context.Background()
	h := &Herdr{Bin: fakeHerdrBin(t, `case "$3" in
  known) echo '{"error":{"code":"agent_exists","message":"taken"}}' >&2; exit 1 ;;
  *)     echo 'connection refused' >&2; exit 1 ;;
esac`)}
	if _, err := h.run(ctx, "agent", "start", "known"); err == nil || dispatch.IsRuntime(err) {
		t.Fatalf("herdr error object classified as runtime: %v", err)
	}
	if _, err := h.run(ctx, "agent", "start", "x"); !dispatch.IsRuntime(err) {
		t.Fatalf("unreachable herdr not a runtime error: %v", err)
	}
	missing := &Herdr{Bin: filepath.Join(t.TempDir(), "herdr")}
	if _, err := missing.run(ctx, "agent", "list"); !dispatch.IsRuntime(err) {
		t.Fatalf("missing herdr not a runtime error: %v", err)
	}
}
