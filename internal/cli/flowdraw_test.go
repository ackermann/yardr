package cli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// The three ways to a drawing give the same file for the same flow: the
// starter pack by name and by directory (flow draw), a depot that follows
// the pack's flow with the pack's groups routed to its stages (flow show
// --svg), and the file go generate keeps under docs/flows.
func TestFlowDrawThreeWaysOneDrawing(t *testing.T) {
	want, err := os.ReadFile("../../docs/flows/starter.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"starter", "../../packs/starter"} {
		out, err := run(t, (&app{}).flowCmd(), "draw", arg)
		if err != nil || out != string(want) {
			t.Errorf("flow draw %s: err %v, the drawing differs from docs/flows/starter.svg: %v", arg, err, out != string(want))
		}
	}

	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := pack.LoadDir("../../packs/starter")
	must(err)
	must(st.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	_, err = st.PutFlow(ctx, p.Flows[0].StoreFlow("default"), "me")
	must(err)
	must(st.SetFlowPointer(ctx, "web", "", "default", "me"))
	depot := "web"
	for _, g := range p.Groups {
		sg := store.Group{Name: g.Name, Runner: "herdr", Limit: g.Limit}
		switch {
		case g.People:
			sg.Runner = "manual"
		case g.Run != "":
			sg.Runner, sg.Config = "exec", map[string]any{"cmd": g.Run}
		}
		must(st.PutGroup(ctx, sg, "me"))
	}
	for _, s := range p.Flows[0].Stages {
		if s.Group != "" {
			_, err := st.AddRoute(ctx, store.Route{Stage: s.Name, Depot: &depot, Group: s.Group}, "me")
			must(err)
		}
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.flowCmd(), "show", "web", "--svg")
	if err != nil || out != string(want) {
		t.Errorf("flow show web --svg: err %v, the drawing differs from docs/flows/starter.svg:\n%s", err, out)
	}
	// The named flow alone has no route, so no building: what its flow file
	// draws.
	file := filepath.Join(t.TempDir(), "default.flow")
	f := p.Flows[0].StoreFlow("default")
	must(os.WriteFile(file, []byte(f.String()), 0o644))
	fromFile, err := run(t, (&app{}).flowCmd(), "draw", file)
	must(err)
	named, err := run(t, a.flowCmd(), "show", "default", "--svg")
	if err != nil || named != fromFile || strings.Contains(named, `class="group`) || !strings.Contains(named, `data-stage="decide"`) {
		t.Errorf("flow show default --svg: err %v, same as the flow file's drawing: %v", err, named == fromFile)
	}
}

// --flow picks among a depot's or a pack's flows, by name or by bead type,
// and a name that is none of them is refused with the ones there are.
func TestFlowDrawPicksAFlow(t *testing.T) {
	out, err := run(t, (&app{}).flowCmd(), "draw", "../../packs/dir", "--flow", "train")
	if err != nil || !strings.Contains(out, "<title>Flow train</title>") {
		t.Errorf("flow draw packs/dir --flow train: %v\n%.200s", err, out)
	}
	if _, err := run(t, (&app{}).flowCmd(), "draw", "../../packs/dir", "--flow", "nope"); err == nil || !strings.Contains(err.Error(), `pack dir has no flow "nope": it has default, train`) {
		t.Errorf("err = %v", err)
	}
	if _, err := run(t, (&app{}).flowCmd(), "draw", "nope"); err == nil || !strings.Contains(err.Error(), `"nope" is no flow file and no pack yardr embeds`) {
		t.Errorf("err = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.flow")
	if err := os.WriteFile(bad, []byte("merged terminal\nmerged -> new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, (&app{}).flowCmd(), "draw", bad); err == nil || !strings.Contains(err.Error(), "terminal stage merged cannot have transitions") {
		t.Errorf("err = %v", err)
	}

	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.flowCmd(), "show", "web", "--svg"); err == nil || !strings.Contains(err.Error(), "depot web has no flow") {
		t.Errorf("err = %v", err)
	}
	for name, text := range map[string]string{"code": "new -> review\nreview -> merged | new\nmerged terminal", "trains": "open -> done\ndone terminal"} {
		f, err := store.ReadFlow(name, text)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "train", "trains", "me"); err != nil {
		t.Fatal(err)
	}
	for arg, title := range map[string]string{"": "code", "trains": "trains", "train": "trains"} {
		args := []string{"show", "web", "--svg"}
		if arg != "" {
			args = append(args, "--flow", arg)
		}
		if out, err := run(t, a.flowCmd(), args...); err != nil || !strings.Contains(out, "<title>Flow "+title+"</title>") {
			t.Errorf("flow %v: %v\n%.200s", args, err, out)
		}
	}
	if _, err := run(t, a.flowCmd(), "show", "web", "--svg", "--flow", "nope"); err == nil || !strings.Contains(err.Error(), `depot web has no flow "nope": it has code, trains`) {
		t.Errorf("err = %v", err)
	}
	if _, err := run(t, a.flowCmd(), "show", "web", "--flow", "code"); err == nil || !strings.Contains(err.Error(), "--flow picks the flow --svg draws") {
		t.Errorf("err = %v", err)
	}
}

// --mini draws the line plan instead of the signalling diagram: one thin
// track, each stage a section of it, the signals unlit outlines. Without
// --svg it is refused, as --flow is.
func TestFlowDrawMini(t *testing.T) {
	fromPack, err := run(t, (&app{}).flowCmd(), "draw", "starter", "--mini")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" class="flowdraw"`,
		`<title>Flow default</title>`,
		`<circle class="lamp unlit"`,
		`class="ln branch"`,
	} {
		if !strings.Contains(fromPack, want) {
			t.Errorf("flow draw starter --mini lacks %s", want)
		}
	}
	for _, not := range []string{`class="platform"`, `class="group`, `class="lamp"`, ">question<"} {
		if strings.Contains(fromPack, not) {
			t.Errorf("flow draw starter --mini has %s", not)
		}
	}

	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ReadFlow("code", "new -> review\nreview -> merged | new\nmerged terminal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "web", "", "code", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	fromDepot, err := run(t, a.flowCmd(), "show", "web", "--svg", "--mini")
	if err != nil || !strings.Contains(fromDepot, "<title>Flow code</title>") || strings.Contains(fromDepot, `class="platform"`) {
		t.Errorf("flow show web --svg --mini: err %v\n%.200s", err, fromDepot)
	}
	// The same flow is the same file whichever way it was asked for.
	file := filepath.Join(t.TempDir(), "code.flow")
	if err := os.WriteFile(file, []byte(f.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fromFile, err := run(t, (&app{}).flowCmd(), "draw", file, "--mini")
	if err != nil || fromFile != fromDepot {
		t.Errorf("flow draw code.flow --mini: err %v, same as the depot's: %v", err, fromFile == fromDepot)
	}
	if _, err := run(t, a.flowCmd(), "show", "web", "--mini"); err == nil || !strings.Contains(err.Error(), "--mini shapes the drawing --svg draws") {
		t.Errorf("err = %v", err)
	}
}

// The help of flow draw names every pack the binary carries, so a pack
// added to it is added to the help with it.
func TestFlowDrawHelpNamesEveryEmbeddedPack(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(packs.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := (&app{}).flowCmd().Find([]string{"draw"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.Contains(sub.Long, e.Name()) {
			t.Errorf("yardr flow draw --help does not name the embedded pack %s", e.Name())
		}
	}
}

// flow draw reads files alone: on a home with no yard it draws a flow file
// and a pack, by directory or by name, and neither makes a store nor starts
// a server.
func TestFlowDrawWithoutAYard(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	want, err := os.ReadFile("../../docs/flows/starter.svg")
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"starter", "../../packs/starter"} {
		errs := captureStderr(t)
		out, code := executeOut(&app{stderr: errs}, "flow", "draw", arg)
		if code != 0 {
			t.Errorf("flow draw %s: exit %d\n%s", arg, code, errs.String())
		} else if out != string(want) {
			t.Errorf("flow draw %s: not the starter pack's drawing", arg)
		}
	}
	out, code := executeOut(&app{}, "flow", "draw", "../../.yardr/flows/default.flow")
	if code != 0 || !strings.Contains(out, "<title>Flow default</title>") {
		t.Errorf("flow draw of a flow file: exit %d\n%.200s", code, out)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, made := range []string{cfg.DBPath(), cfg.PIDFile(), cfg.Socket()} {
		if _, err := os.Stat(made); err == nil {
			t.Errorf("%s exists: flow draw reached for a store", made)
		}
	}
}
