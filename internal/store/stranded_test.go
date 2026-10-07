package store_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Stranded reads the open beads through the index and answers what a walk
// of every bead answers (StrandedWalk, the function as it was), under each
// rule for which flow governs a bead: a pin on a repository flow and on a
// store flow, the repository's flow for a type and for the depot, a depot's
// pointer shadowing the repository's flow for a type, a type's pointer
// shadowing the depot's, a bead of no depot, a memory, and a depot whose
// checkout git cannot read. Each kind has a closed twin in the same stage,
// which the walk decodes and the index never does.
func TestStrandedReadsOpenBeadsOnly(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	files := map[string]string{
		".yardr/flows/default.flow":       repoDefault,
		".yardr/flows/type/decision.flow": repoDecision,
		".yardr/flows/alt.flow":           repoAlt,
	}
	web, app, gone := gitRepo(t, files), gitRepo(t, files), gitRepo(t, files)
	for _, d := range []store.Depot{
		{Name: "web", Path: web, Base: "main"},
		{Name: "app", Path: app, Base: "main"},
		{Name: "gone", Path: gone, Base: "main"},
	} {
		if err := s.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	putNamed(t, s, "tri", "triage -> new\nnew -> done\ndone terminal\n")
	// app follows the repository's alt by a pointer, which shadows the
	// repository's flow for decisions; its bugs follow tri, which shadows
	// that pointer.
	if err := s.SetFlowPointer(ctx, "app", "", "alt", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFlowPointer(ctx, "app", "bug", "tri", "me"); err != nil {
		t.Fatal(err)
	}

	open, total := 0, 0
	// both creates the bead twice and closes the second.
	both := func(nb store.NewBead) string {
		t.Helper()
		b, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		twin, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CloseBead(ctx, twin.ID, "dropped", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		open, total = open+1, total+2
		return b.ID
	}
	pin := func(name string) map[string]any { return map[string]any{"flow": name} }
	review := both(store.NewBead{Title: "the depot's flow", Depot: "web", Stage: "review"})
	both(store.NewBead{Title: "the depot's flow, a stage it keeps", Depot: "web"})
	ask := both(store.NewBead{Title: "the type's flow", Depot: "web", Type: "decision", Stage: "ask"})
	pinRepo := both(store.NewBead{Title: "pins a repository flow", Depot: "web", Metadata: pin("alt")})
	both(store.NewBead{Title: "pins a store flow", Depot: "web", Stage: "triage", Metadata: pin("tri")})
	shadowed := both(store.NewBead{Title: "the type's flow, shadowed by the depot's pointer", Depot: "app", Type: "decision"})
	pointed := both(store.NewBead{Title: "the depot's pointer", Depot: "app"})
	both(store.NewBead{Title: "the type's pointer", Depot: "app", Type: "bug", Stage: "triage"})
	pinApp := both(store.NewBead{Title: "a pin over both pointers", Depot: "app", Type: "bug", Stage: "review", Metadata: pin("default")})
	both(store.NewBead{Title: "no depot, a pin", Stage: "triage", Metadata: pin("tri")})
	both(store.NewBead{Title: "no depot, no flow", Stage: "anywhere"})
	both(store.NewBead{Title: "a checkout git cannot read", Depot: "gone", Stage: "review"})
	if _, err := s.Remember(ctx, store.NewMemory{Title: "a memory", Scope: "all"}, "me"); err != nil {
		t.Fatal(err)
	}
	open, total = open+1, total+1
	// Its beads are bad_flow's to report from here on, stranded or not.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	same := func(when string) []store.StrandedBead {
		t.Helper()
		var all []store.StrandedBead
		if err := s.SetSetting(ctx, store.NoteSetting, when[:5], "me"); err != nil {
			t.Fatal(err)
		}
		for i, depot := range []string{"", "web", "app", "gone", "elsewhere"} {
			before := s.BeadReads()
			got, err := s.Stranded(ctx, depot)
			if err != nil {
				t.Fatal(err)
			}
			// Once each: a bead whose answer needs its depot's repository
			// is answered after that is read, as it was decoded. And once
			// for the store as it is: nothing is written between the
			// depots, so only the first of them decodes anything.
			once := int64(0)
			if i == 0 {
				once = int64(open)
			}
			if reads := s.BeadReads() - before; reads != once {
				t.Errorf("%s: Stranded(%q) read %d beads, want %d (the open ones, once since the store changed)", when, depot, reads, once)
			}
			before = s.BeadReads()
			want, err := s.StrandedWalk(ctx, depot)
			if err != nil {
				t.Fatal(err)
			}
			if reads := s.BeadReads() - before; reads != int64(total) {
				t.Fatalf("%s: the walk read %d beads, want %d (every bead, once)", when, reads, total)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: Stranded(%q) = %+v; the walk says %+v", when, depot, got, want)
			}
			if depot == "" {
				all = got
			}
		}
		return all
	}
	if got := same("before the flows changed"); len(got) != 0 {
		t.Fatalf("stranded before the flows changed = %+v", got)
	}

	// The base of both repositories drops review from default, ask from the
	// decisions' flow and new from alt.
	changed := map[string]string{
		".yardr/flows/default.flow":       "new -> done\ndone terminal\n",
		".yardr/flows/type/decision.flow": "question -> answered\nanswered terminal\n",
		".yardr/flows/alt.flow":           "start -> alt\nalt terminal\n",
	}
	webSHA, appSHA := commitFiles(t, web, changed), commitFiles(t, app, changed)
	want := []store.StrandedBead{
		{Bead: review, Stage: "review", Flow: "default", Source: store.FlowSource{Kind: "repo", Commit: webSHA, Path: ".yardr/flows/default.flow"}},
		{Bead: ask, Stage: "ask", Flow: "type/decision", Source: store.FlowSource{Kind: "repo", Type: "decision", Commit: webSHA, Path: ".yardr/flows/type/decision.flow"}},
		{Bead: pinRepo, Stage: "new", Flow: "alt", Source: store.FlowSource{Kind: "pin", Commit: webSHA, Path: ".yardr/flows/alt.flow"}},
		{Bead: shadowed, Stage: "new", Flow: "alt", Source: store.FlowSource{Kind: "store", Commit: appSHA, Path: ".yardr/flows/alt.flow"}},
		{Bead: pointed, Stage: "new", Flow: "alt", Source: store.FlowSource{Kind: "store", Commit: appSHA, Path: ".yardr/flows/alt.flow"}},
		{Bead: pinApp, Stage: "review", Flow: "default", Source: store.FlowSource{Kind: "pin", Commit: appSHA, Path: ".yardr/flows/default.flow"}},
	}
	slices.SortFunc(want, func(a, b store.StrandedBead) int { return strings.Compare(a.Bead, b.Bead) })
	if got := same("after the flows changed"); !slices.Equal(got, want) {
		t.Fatalf("Stranded = %+v\nwant       %+v", got, want)
	}
}
