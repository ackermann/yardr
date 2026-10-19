package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// etaYard is a yard with clocked cells: ten beads that passed through new
// in sub-minute stays under builders. Durations are what the test run
// takes, so assertions on spans only hold below a minute per stay.
func etaYard(t *testing.T, flowText, seedTo string, routes ...store.Route) (store.API, *store.Bead) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow(flowText)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if _, err := st.AddRoute(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		b, err := st.CreateBead(ctx, store.NewBead{Title: fmt.Sprintf("seed %d", i), Stage: "new",
			Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Claim(ctx, b.ID, "builders", "new", "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Advance(ctx, b.ID, seedTo, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the bead", Stage: "new",
		Labels: []string{"difficulty:2"}, Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return st, b
}

func TestBeadShowPrintsEstimate(t *testing.T) {
	t.Parallel()
	st, b := etaYard(t, "new -> review on done\nreview terminal\n", "review",
		store.Route{Stage: "new", Group: "builders"}, store.Route{Stage: "review", Group: "builders"})
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The seeds closed on entering review, so nothing stands ahead: the
	// line is the new stage's sub-minute median alone.
	if !strings.Contains(out, "\n  eta usual under a minute (new ~under a minute)\n") {
		t.Fatalf("no eta line:\n%s", out)
	}
}

func TestBeadShowEstimateJSON(t *testing.T) {
	t.Parallel()
	st, b := etaYard(t, "new -> review on done\nreview terminal\n", "review",
		store.Route{Stage: "new", Group: "builders"}, store.Route{Stage: "review", Group: "builders"})
	j := &app{st: st, actor: "me", json: true}
	out, err := run(t, j.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	etaObj, ok := got["eta"].(map[string]any)
	if !ok {
		t.Fatalf("no eta object:\n%s", out)
	}
	if etaObj["low"] != "under a minute" || etaObj["high"] != "under a minute" {
		t.Fatalf("range: %v", etaObj)
	}
	stages, ok := etaObj["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("stages: %v", etaObj)
	}
	stage := stages[0].(map[string]any)
	if stage["stage"] != "new" || stage["group"] != "builders" || stage["median"] != "under a minute" {
		t.Fatalf("stage: %v", stage)
	}
	if etaObj["samples"] != 10.0 {
		t.Fatalf("samples: %v", etaObj)
	}
	line, _ := etaObj["line"].(string)
	if !strings.HasPrefix(line, "eta usual ") {
		t.Fatalf("line: %v", etaObj)
	}
	if _, ok := got["queue"]; ok {
		t.Fatalf("zero queue present:\n%s", out)
	}
}

func TestBeadShowNamesManualFuture(t *testing.T) {
	t.Parallel()
	st, b := etaYard(t, "new -> approved on done\n", "approved",
		store.Route{Stage: "new", Group: "builders"}, store.Route{Stage: "approved", Group: "people"})
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n  eta usual under a minute (new ~under a minute; approved after your decision)\n"
	if !strings.Contains(out, want) {
		t.Fatalf("no manual stage in line:\n%s", out)
	}
}

func TestBeadShowOmitsEstimate(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the bead", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n  eta usual ") || strings.Contains(out, "\n  late: ") {
		t.Fatalf("estimated with no history:\n%s", out)
	}
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["eta"]; ok {
		t.Fatalf("eta present with no history:\n%s", out)
	}
	if _, ok := got["late"]; ok {
		t.Fatalf("late present with no history:\n%s", out)
	}
	// Closed beads say nothing either.
	if _, err := st.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n  eta usual ") || strings.Contains(out, "\n  late: ") {
		t.Fatalf("estimated a closed bead:\n%s", out)
	}
}

func TestPrimeBeadShowsEstimate(t *testing.T) {
	t.Parallel()
	st, b := etaYard(t, "new -> review on done\nreview terminal\n", "review",
		store.Route{Stage: "new", Group: "builders"}, store.Route{Stage: "review", Group: "builders"})
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\n  eta usual under a minute (new ~under a minute)\n") {
		t.Fatalf("no eta line:\n%s", out)
	}
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["eta"].(map[string]any); !ok {
		t.Fatalf("no eta object:\n%s", out)
	}
}
