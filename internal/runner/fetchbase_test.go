package runner

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fetchYard is a depot repository cloned from a bare origin, with a store
// holding the depot and one bead, for the local-base fetch tests.
type fetchYard struct {
	src    string // origin's content, to move the remote with
	origin string // the bare remote
	repo   string // the depot's clone, main tracking origin/main
	store  store.API
}

func newFetchYard(t *testing.T) *fetchYard {
	t.Helper()
	ctx := context.Background()
	y := &fetchYard{src: t.TempDir(), origin: filepath.Join(t.TempDir(), "origin.git"), repo: filepath.Join(t.TempDir(), "repo")}
	gitT(t, y.src, "init", "-q", "-b", "main")
	gitT(t, y.src, "commit", "-q", "--allow-empty", "-m", "A")
	gitT(t, y.src, "init", "-q", "--bare", y.origin)
	gitT(t, y.src, "push", "-q", y.origin, "main")
	gitT(t, y.src, "clone", "-q", y.origin, y.repo)
	y.store = storetest.New(t)
	mustT(t, y.store.PutDepot(ctx, store.Depot{Name: "r", Path: y.repo, Base: "main"}, "me"))
	_, err := y.store.CreateBead(ctx, store.NewBead{Title: "work", ID: "r-1", Stage: "new", Depot: "r"}, "me")
	mustT(t, err)
	return y
}

// moveOrigin commits on the remote's main, past what the depot has.
func (y *fetchYard) moveOrigin(t *testing.T) string {
	t.Helper()
	gitT(t, y.src, "commit", "-q", "--allow-empty", "-m", "remote")
	gitT(t, y.src, "push", "-q", y.origin, "main")
	return gitOutT(t, y.src, "rev-parse", "main")
}

func (y *fetchYard) job() dispatch.Job {
	return dispatch.Job{Bead: store.Bead{ID: "r-1", Stage: "new"},
		Depot: &store.Depot{Name: "r", Path: y.repo, Base: "main"}}
}

func (y *fetchYard) diverged(t *testing.T) []store.Event {
	t.Helper()
	evs, err := y.store.Events(context.Background(), "r-1", 0)
	mustT(t, err)
	return slices.DeleteFunc(evs, func(e store.Event) bool { return e.Kind != BaseDiverged })
}

// A local base behind its upstream is fast-forwarded before the bead
// branches, without a checkout.
func TestFetchBaseFastForwardsLocalBase(t *testing.T) {
	y := newFetchYard(t)
	remote := y.moveOrigin(t)
	// main checked out nowhere: the clone's checkout is detached.
	gitT(t, y.repo, "checkout", "-q", "--detach")

	(&Herdr{Store: y.store}).fetchBase(context.Background(), y.job())

	if got := gitOutT(t, y.repo, "rev-parse", "main"); got != remote {
		t.Fatalf("main at %s, want the upstream's %s", got, remote)
	}
	if evs := y.diverged(t); len(evs) != 0 {
		t.Fatalf("base_diverged events = %+v, want none", evs)
	}
}

// A local base that diverged from its upstream is left where it is, with a
// base_diverged event on the bead, of the class attention; the bead still
// starts from it.
func TestFetchBaseDivergedLogsEvent(t *testing.T) {
	y := newFetchYard(t)
	gitT(t, y.repo, "commit", "-q", "--allow-empty", "-m", "local")
	local := gitOutT(t, y.repo, "rev-parse", "main")
	remote := y.moveOrigin(t)
	gitT(t, y.repo, "checkout", "-q", "--detach")

	(&Herdr{Store: y.store}).fetchBase(context.Background(), y.job())

	if got := gitOutT(t, y.repo, "rev-parse", "main"); got != local {
		t.Fatalf("main moved to %s, want it left at %s", got, local)
	}
	evs := y.diverged(t)
	if len(evs) != 1 {
		t.Fatalf("base_diverged events = %+v, want one", evs)
	}
	e := evs[0]
	if e.Bead == nil || *e.Bead != "r-1" || e.Data["depot"] != "r" || e.Data["base"] != "main" ||
		e.Data["upstream"] != "origin/main" || e.Data["local"] != local || e.Data["remote"] != remote {
		t.Fatalf("base_diverged event = %+v, want it on r-1 naming r's main and origin/main", e)
	}
	if !slices.Contains(store.AttentionKinds, BaseDiverged) {
		t.Fatalf("%s is not of the class attention: %v", BaseDiverged, store.AttentionKinds)
	}
}

// A local base checked out in a worktree is left as it is, even behind its
// upstream: pulling into a working copy a person may use is the operator's.
// No event either: nothing went wrong.
func TestFetchBaseCheckedOutLeavesBase(t *testing.T) {
	y := newFetchYard(t)
	before := gitOutT(t, y.repo, "rev-parse", "main")
	y.moveOrigin(t) // main stays checked out in the clone

	(&Herdr{Store: y.store}).fetchBase(context.Background(), y.job())

	if got := gitOutT(t, y.repo, "rev-parse", "main"); got != before {
		t.Fatalf("the checked-out main moved to %s, want it left at %s", got, before)
	}
	if evs := y.diverged(t); len(evs) != 0 {
		t.Fatalf("base_diverged events = %+v, want none", evs)
	}
}

// A local base ahead of its upstream holds commits that are simply not
// pushed yet: it is left, with no event.
func TestFetchBaseAheadOfUpstreamLeavesBase(t *testing.T) {
	y := newFetchYard(t)
	gitT(t, y.repo, "commit", "-q", "--allow-empty", "-m", "local")
	local := gitOutT(t, y.repo, "rev-parse", "main")
	gitT(t, y.repo, "checkout", "-q", "--detach")

	(&Herdr{Store: y.store}).fetchBase(context.Background(), y.job())

	if got := gitOutT(t, y.repo, "rev-parse", "main"); got != local {
		t.Fatalf("main moved to %s, want it left at %s", got, local)
	}
	if evs := y.diverged(t); len(evs) != 0 {
		t.Fatalf("base_diverged events = %+v, want none", evs)
	}
}

// A base with no upstream, and a fetch that fails, leave the base as it is,
// with no event: best effort, as for a base that names a remote.
func TestFetchBaseBestEffort(t *testing.T) {
	t.Run("no upstream", func(t *testing.T) {
		repo := t.TempDir()
		gitT(t, repo, "init", "-q", "-b", "main")
		gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "A")
		st := storetest.New(t)
		job := dispatch.Job{Bead: store.Bead{ID: "r-1"},
			Depot: &store.Depot{Name: "r", Path: repo, Base: "main"}}
		(&Herdr{Store: st}).fetchBase(context.Background(), job)
		evs, err := st.EventsOfKind(context.Background(), BaseDiverged, 0)
		mustT(t, err)
		if len(evs) != 0 {
			t.Fatalf("events = %+v, want none", evs)
		}
	})
	t.Run("failed fetch", func(t *testing.T) {
		y := newFetchYard(t)
		before := gitOutT(t, y.repo, "rev-parse", "main")
		y.moveOrigin(t)
		gitT(t, y.repo, "checkout", "-q", "--detach")
		gitT(t, y.repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

		(&Herdr{Store: y.store}).fetchBase(context.Background(), y.job())

		if got := gitOutT(t, y.repo, "rev-parse", "main"); got != before {
			t.Fatalf("main moved to %s, want it left at %s", got, before)
		}
		if evs := y.diverged(t); len(evs) != 0 {
			t.Fatalf("base_diverged events = %+v, want none", evs)
		}
	})
}
