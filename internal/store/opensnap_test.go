package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The four questions the dispatcher's pass asks of the open beads
// (ExpireMemories, the trains in in-pr, Dispatchable, Stranded) decode them
// once between them while nothing is written, and again after a write, whose
// change each of them then answers with.
func TestPassQuestionsDecodeOpenBeadsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	const flow = "new -> in-pr | review\nreview -> done\nin-pr -> done\ndone terminal\n"
	repo := gitRepo(t, map[string]string{".yardr/flows/default.flow": flow})
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	open := 0
	create := func(nb store.NewBead) *store.Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		open++
		return b
	}
	task := create(store.NewBead{Title: "a task", Depot: "web"})
	train := create(store.NewBead{Title: "a train", Depot: "web", Type: store.TypeTrain, Stage: "in-pr"})
	lost := create(store.NewBead{Title: "in a stage its flow is to lose", Depot: "web", Stage: "review"})
	closed := create(store.NewBead{Title: "closed", Depot: "web"})
	if _, err := s.CloseBead(ctx, closed.ID, "dropped", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	open--
	past, later := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for title, at := range map[string]*time.Time{"kept": nil, "not yet": &later} {
		if _, err := s.Remember(ctx, store.NewMemory{Title: title, Scope: "all", Expires: at}, "me"); err != nil {
			t.Fatal(err)
		}
		open++
	}

	type answers struct {
		expired           int
		trains, ready     []string
		stranded          []string
		reads, flowsAsked int64
	}
	pass := func() answers {
		t.Helper()
		var a answers
		var err error
		before := s.BeadReads()
		if a.expired, err = s.ExpireMemories(ctx); err != nil {
			t.Fatal(err)
		}
		trains, err := s.ListBeads(ctx, store.BeadFilter{Stage: "in-pr", Type: store.TypeTrain})
		if err != nil {
			t.Fatal(err)
		}
		ready, err := s.Dispatchable(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stranded, err := s.Stranded(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		a.trains, a.ready, a.reads = ids(trains), dispatchableIDs(ready), s.BeadReads()-before
		for _, sb := range stranded {
			a.stranded = append(a.stranded, sb.Bead)
		}
		return a
	}
	check := func(when string, got answers, reads int, trains, ready, stranded []string) {
		t.Helper()
		if got.reads != int64(reads) {
			t.Errorf("%s: the four read %d beads, want %d", when, got.reads, reads)
		}
		if got.expired != 0 || !slices.Equal(got.trains, trains) || !slices.Equal(got.ready, ready) || !slices.Equal(got.stranded, stranded) {
			t.Errorf("%s: expired %d, trains %v, ready %v, stranded %v; want 0, %v, %v, %v",
				when, got.expired, got.trains, got.ready, got.stranded, trains, ready, stranded)
		}
	}
	all := []string{task.ID, train.ID, lost.ID}
	check("first", pass(), open, []string{train.ID}, all, nil)
	check("nothing written since", pass(), 0, []string{train.ID}, all, nil)
	// The repository is not the store: its flow is read on every pass, over
	// the beads as they were decoded.
	commitFiles(t, repo, map[string]string{".yardr/flows/default.flow": strings.Replace(flow, " | review\nreview -> done", "", 1)})
	check("the base lost a stage", pass(), 0, []string{train.ID}, all, []string{lost.ID})

	// A write of something else than a bead is a new state of the store too.
	if err := s.SetSetting(ctx, store.NoteSetting, "1", "me"); err != nil {
		t.Fatal(err)
	}
	check("after a write", pass(), open, []string{train.ID}, all, []string{lost.ID})

	// Each of the four answers with what was written since it last asked.
	if _, err := s.Advance(ctx, train.ID, "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHold(ctx, task.ID, true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, lost.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	open-- // done is terminal: the train closed
	check("after the beads changed", pass(), open, nil, []string{lost.ID}, nil)

	// A memory past its expiry is closed by the pass that finds it, which is
	// a write: the questions after it decode the beads again.
	if _, err := s.Remember(ctx, store.NewMemory{Title: "gone", Scope: "all", Expires: &past}, "me"); err != nil {
		t.Fatal(err)
	}
	got := pass()
	if got.expired != 1 || got.reads != int64(open+1+open) {
		t.Errorf("a pass that expires a memory: expired %d, read %d beads; want 1 and %d (the open ones before it closed, and after)",
			got.expired, got.reads, open+1+open)
	}
	if ms, err := s.Recall(ctx, "", ""); err != nil || len(ms) != 2 {
		t.Errorf("memories after the expiry = %+v %v, want the two that stay", ms, err)
	}
}

// What a listing hands out is the caller's: changing it changes nothing a
// later listing answers, though both come from the same decoded beads.
func TestListedBeadsShareNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "a task", Depot: "web", Labels: []string{"a", "b"},
		Metadata: map[string]any{"model": "m", "nested": map[string]any{"list": []any{"x", map[string]any{"k": "v"}}}}}, "me"); err != nil {
		t.Fatal(err)
	}
	list := func() store.Bead {
		t.Helper()
		bs, err := s.ListBeads(ctx, store.BeadFilter{})
		if err != nil || len(bs) != 1 {
			t.Fatalf("list = %+v %v", bs, err)
		}
		return bs[0]
	}
	text := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	first := list()
	want := text(first)
	*first.Depot = "elsewhere"
	first.Labels[0] = "changed"
	first.Metadata["model"] = "other"
	nested := first.Metadata["nested"].(map[string]any)
	nested["list"].([]any)[0] = "changed"
	nested["list"].([]any)[1].(map[string]any)["k"] = "changed"
	if got := text(list()); got != want {
		t.Errorf("a listing after the first one's bead was changed:\n%s\nwant\n%s", got, want)
	}
	ready, err := s.Dispatchable(ctx)
	if err != nil || len(ready) != 1 {
		t.Fatalf("dispatchable = %+v %v", ready, err)
	}
	*ready[0].Depot = "elsewhere"
	ready[0].Labels[0] = "changed"
	if got := text(list()); got != want {
		t.Errorf("a listing after a dispatchable bead was changed:\n%s\nwant\n%s", got, want)
	}

	// The copy itself, with every field that could be shared set: a field
	// added to Bead fails here until the copy and this bead know of it.
	at := time.Now()
	str := func(v string) *string { return &v }
	b := store.Bead{ID: "web-1", UUID: "u", Title: "t", Body: "b", Type: "task", Stage: "new", Status: "open", Priority: 1,
		Depot: str("web"), Labels: []string{"a"}, Metadata: map[string]any{"k": []any{map[string]any{"k": "v"}}}, Hold: true,
		Group: str("g"), Session: str("s"), CloseReason: str("r"), CreatedAt: at, UpdatedAt: at, ClosedAt: &at,
		CreatedBy: str("me"), Originator: "me", Revision: "r", Train: "web-e", Notify: "s"}
	c := store.CloneBead(&b)
	if !reflect.DeepEqual(b, c) {
		t.Fatalf("clone = %+v, want %+v", c, b)
	}
	bv, cv := reflect.ValueOf(b), reflect.ValueOf(c)
	for i := 0; i < bv.NumField(); i++ {
		name, f := bv.Type().Field(i).Name, bv.Field(i)
		// A row of the open beads has no Peer (withPeer makes one for each
		// bead handed out), so the copy does not copy one.
		if name == "Peer" {
			continue
		}
		if f.IsZero() {
			t.Errorf("Bead.%s is not set in this test", name)
		}
		switch f.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice:
			if f.Pointer() == cv.Field(i).Pointer() {
				t.Errorf("Bead.%s is shared with its clone", name)
			}
		}
	}
	inner := func(b store.Bead) reflect.Value {
		return reflect.ValueOf(b.Metadata["k"].([]any)[0])
	}
	if reflect.ValueOf(b.Metadata["k"]).Pointer() == reflect.ValueOf(c.Metadata["k"]).Pointer() || inner(b).Pointer() == inner(c).Pointer() {
		t.Error("nested metadata is shared with the clone")
	}
}

// Dispatchable says of each ready bead what routing reads, in the order the
// beads are tried, and nothing of what the bead says: the dispatcher asks on
// every pass and reads the bead it starts.
func TestDispatchableCarriesNoBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Kind: store.DepotDir, Path: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	const body, title, secret = "THE-BODY-OF-THE-BEAD", "THE-TITLE-OF-THE-BEAD", "THE-OTHER-METADATA"
	late, err := s.CreateBead(ctx, store.NewBead{Title: title, Body: body, Depot: "web", Priority: 3, Labels: []string{"ui"},
		Metadata: map[string]any{"model": "big", "note": secret}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateBead(ctx, store.NewBead{Title: title, Body: body, Type: "bug", Stage: "triage", Priority: 1}, "me")
	if err != nil {
		t.Fatal(err)
	}
	held, err := s.CreateBead(ctx, store.NewBead{Title: title, Body: body}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHold(ctx, held.ID, true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Dispatchable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	web := "web"
	want := []store.ReadyBead{
		{Bead: first.ID, Stage: "triage", Type: "bug", Priority: 1},
		{Bead: late.ID, Stage: "new", Type: "task", Depot: &web, Labels: []string{"ui"}, Model: "big", Priority: 3},
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, _ := json.Marshal(want)
	if string(raw) != string(wantRaw) {
		t.Fatalf("dispatchable = %s\nwant           %s", raw, wantRaw)
	}
	for _, word := range []string{body, title, secret} {
		if strings.Contains(string(raw), word) {
			t.Errorf("dispatchable carries %s: %s", word, raw)
		}
	}
	// A daemon of a build that still expects beads reads none it could
	// start: no handle, so nothing to claim.
	var old []store.Bead
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	for _, b := range old {
		if b.ID != "" {
			t.Errorf("a build that expects beads reads %s out of the answer", b.ID)
		}
	}
}
