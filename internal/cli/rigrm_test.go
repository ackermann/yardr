package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// rig rm is refused, naming the bead or session, while the rig is in use;
// once nothing needs it, the rig leaves rig list, route list and flow list
// together with its routes and flow pointers.
func TestRigRm(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutRig(ctx, store.Rig{Name: "web"}, "me"))
	must(st.PutRig(ctx, store.Rig{Name: "keep"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}))
	web, keep := "web", "keep"
	route, err := st.AddRoute(ctx, store.Route{Stage: "new", Rig: &web, Group: "builders"})
	must(err)
	kept, err := st.AddRoute(ctx, store.Route{Stage: "new", Rig: &keep, Group: "builders"})
	must(err)
	f, err := store.ParseFlow("new -> review\nreview -> done\ndone terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "web", "", "code", "me"))
	must(st.SetFlowPointer(ctx, "web", "bug", "code", "me"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Rig: "web"}, "me")
	must(err)
	a := &app{st: st, actor: "me"}

	// Open bead: refused and named; the rig and what hangs off it stay.
	_, err = run(t, a.rigCmd(), "rm", "web")
	if err == nil || !strings.Contains(err.Error(), "rig web is in use by bead "+b.ID) {
		t.Fatalf("rm with an open bead: %v", err)
	}
	if exitCode(err) == 0 {
		t.Fatal("refusal exited 0")
	}
	sess, err := st.Claim(ctx, b.ID, "builders", "new", "yard")
	must(err)
	_, err = run(t, a.rigCmd(), "rm", "web")
	if err == nil || !strings.Contains(err.Error(), "session "+sess.ID+" (bead "+b.ID+")") {
		t.Fatalf("rm with a live session: %v", err)
	}
	out, err := run(t, a.rigCmd(), "list")
	must(err)
	if !strings.Contains(out, "web") || !strings.Contains(out, "code") {
		t.Fatalf("refused rm changed rig list:\n%s", out)
	}
	if ps, _ := st.FlowPointers(ctx); len(ps) != 2 {
		t.Fatalf("refused rm changed the flow pointers: %+v", ps)
	}
	if rs, _ := st.Routes(ctx); len(rs) != 2 {
		t.Fatalf("refused rm changed the routes: %+v", rs)
	}

	// Closed, with its session over: the rig goes, and the closed bead keeps
	// its rig as history.
	must(st.EndSession(ctx, sess.ID, "aborted", "test"))
	_, err = st.CloseBead(ctx, b.ID, "dropped", store.Expect{}, "me")
	must(err)
	out, err = run(t, a.rigCmd(), "rm", "web")
	must(err)
	for _, want := range []string{"removed rig web; 1 route(s) and 2 flow pointer(s) went with it",
		"routes: " + route.ID, "flow pointers: rig web, rig web type bug", "nothing on disk was touched"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, err = run(t, a.rigCmd(), "list")
	if err != nil || strings.Contains(out, "web") || !strings.Contains(out, "keep") {
		t.Fatalf("rig list after rm: %v\n%s", err, out)
	}
	out, err = run(t, a.routeCmd(), "list")
	if err != nil || strings.Contains(out, route.ID) || !strings.Contains(out, kept.ID) {
		t.Fatalf("route list after rm: %v\n%s", err, out)
	}
	out, err = run(t, a.flowCmd(), "list")
	if err != nil || strings.Contains(out, "rig web") || !strings.Contains(out, "code") {
		t.Fatalf("flow list after rm: %v\n%s", err, out)
	}
	if got, err := st.GetBead(ctx, b.ID); err != nil || got.Rig == nil || *got.Rig != "web" {
		t.Fatalf("closed bead after rm: %+v, %v", got, err)
	}

	// Unknown rig: a clear error, exit 3 like the other lookups.
	_, err = run(t, a.rigCmd(), "rm", "web")
	if exitCode(err) != 3 || !strings.Contains(err.Error(), "rig web: not found") {
		t.Fatalf("rm of a removed rig: %v (exit %d)", err, exitCode(err))
	}

	// --json reports what went with the rig.
	must(st.PutRig(ctx, store.Rig{Name: "web"}, "me"))
	must(st.SetFlowPointer(ctx, "web", "", "code", "me"))
	a.json = true
	out, err = run(t, a.rigCmd(), "rm", "web")
	must(err)
	var got store.RigRemoval
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if got.Rig != "web" || len(got.Routes) != 0 || len(got.FlowPointers) != 1 || got.FlowPointers[0].Flow != "code" {
		t.Fatalf("--json = %+v", got)
	}
}
