package store_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// EstimateMany over a yard's beads reads each depot's checkout once, not
// once per bead: every bead's flow resolves from one shared reading. Thirty
// beads cost thirty git invocations before, one after.
func TestStoreEstimateManyReadsRepoOnce(t *testing.T) {
	// Sequential: process environment.
	ctx := context.Background()
	repo := gitRepo(t, map[string]string{
		".yardr/flows/default.flow": "backlog -> new\nnew -> review\nreview -> merged\nmerged terminal\n",
	})
	st := storetest.New(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "perf", Path: repo, Base: "main"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"))
	stages := []string{"backlog", "new", "review"}
	for _, stage := range stages {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Group: "builders"}, "me")
		must(err)
	}
	var ids []string
	for i := range 30 {
		b, err := st.CreateBead(ctx, store.NewBead{
			Title: fmt.Sprintf("bead %d", i), Type: "task",
			Stage: stages[i%len(stages)], Depot: "perf",
		}, "me")
		must(err)
		ids = append(ids, b.ID)
	}
	// Count git invocations from here: the seeding above warmed the
	// depot's cached flows, so one rev-parse confirms them.
	real, err := exec.LookPath("git")
	must(err)
	counts := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	scripttest.Write(t, filepath.Join(bin, "git"), "#!/bin/sh\necho x >> \"$YARDR_TEST_GITCOUNT\"\nexec \"$YARDR_TEST_GIT\" \"$@\"\n")
	t.Setenv("YARDR_TEST_GIT", real)
	t.Setenv("YARDR_TEST_GITCOUNT", counts)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ests, err := eta.EstimateManyViaStore(ctx, st, ids, time.Now())
	must(err)
	if len(ests) != len(ids) {
		t.Fatalf("EstimateMany answered %d of %d beads", len(ests), len(ids))
	}
	raw, err := os.ReadFile(counts)
	if err != nil {
		// No invocation at all: the flows were answered without git.
		return
	}
	if n := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); n > 2 {
		t.Fatalf("EstimateMany over %d beads shelled to git %d times, want at most 2", len(ids), n)
	}
}
