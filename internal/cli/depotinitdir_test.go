package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// dirYard is a yard whose operator is Jo, with two dir depots that hold
// nothing: "old" and "invoices".
func dirYard(t *testing.T) (a *app, old, invoices store.Depot) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	old = store.Depot{Name: "old", Path: t.TempDir(), Kind: store.DepotDir}
	invoices = store.Depot{Name: "invoices", Path: t.TempDir(), Kind: store.DepotDir}
	for _, d := range []store.Depot{old, invoices} {
		if err := st.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSetting(ctx, "operator", "Jo", "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}, old, invoices
}

var dirFlags = []string{"--bind", "builders=claude:opus", "--bind", "reviewers=codex"}

// rules are a flow file's lines that say something: its rules, without the
// comments above them.
func rules(flow string) string {
	var out []string
	for _, l := range strings.Split(flow, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// A dir depot set up from the dir pack is worked as one depot init seeded:
// the same two flows govern its tasks and its trains, every stage has the
// same stage text, and the routes are the ones depot init tells the
// operator to add (new to builders, review to reviewers, a train's open to
// the reviewers). Where they differ is where it is kept: the pack's under
// the yard's home and in the store, depot init's in the directory, which
// --pack leaves as it was, with no gate.
func TestDirPackIsWhatDepotInitGivesADirDepot(t *testing.T) {
	a, old, invoices := dirYard(t)
	setInteractive(t, false)
	ctx := context.Background()
	if _, err := depotInit(old, "Jo", "yardmaster", false); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"init", "invoices", "--pack", "dir", "--yes"}, dirFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"pack dir (", "  create           invoices-builders\n", "\napplied pack dir to depot invoices: ",
		"\ngate: none written. Depot invoices is a directory depot: its gate is .yardr/check in " + invoices.Path + " when it has one, and without one yardr depot check passes with \"no gate\"\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "git -C") || strings.Contains(out, "Nothing is committed") || strings.Contains(out, "base") {
		t.Errorf("a dir depot has no base and nothing to commit:\n%s", out)
	}
	if left := snapshot(t, invoices.Path); len(left) != 0 {
		t.Errorf("depot init --pack wrote into the directory: %v", left)
	}

	// The flows: depot init's rules, for a task and for a train, each
	// governing from where its depot keeps it.
	for typ, f := range map[string]struct{ file, text, flow string }{
		"task":          {"default", dirFlow, "dir.default"},
		store.TypeTrain: {"type/train", dirTrainFlow, "dir.train"},
	} {
		var got [2]*store.NextStages
		for i, depot := range []string{"old", "invoices"} {
			b, err := a.st.CreateBead(ctx, store.NewBead{Title: typ, Depot: depot, Type: typ, Stage: "backlog"}, "me")
			if err != nil {
				t.Fatal(err)
			}
			if got[i], err = a.st.NextStages(ctx, b.ID); err != nil || got[i].Flow == nil {
				t.Fatalf("%s in %s: %+v, %v", typ, depot, got[i], err)
			}
		}
		if got[0].Flow.Name != f.file || got[1].Flow.Name != f.flow || got[1].Source.Kind != "store" {
			t.Errorf("%s: governed by %s (%s) and %s (%s)", typ, got[0].Flow.Name, got[0].Source, got[1].Flow.Name, got[1].Source)
		}
		// A flow read from a file lists its stages as the rules first name
		// them, the pack's as it declares them: the same rules in another
		// order.
		sorted := func(f *store.Flow) string {
			lines := strings.Split(f.String(), "\n")
			slices.Sort(lines)
			return strings.Join(lines, "\n")
		}
		if got[1].Flow.String() != rules(f.text) || sorted(got[1].Flow) != sorted(got[0].Flow) {
			t.Errorf("%s: the pack's flow is\n%s\ndepot init's\n%s", typ, got[1].Flow, got[0].Flow)
		}
		if got[0].Flow.Stages[0] != got[1].Flow.Stages[0] {
			t.Errorf("%s starts in %s by the pack, in %s by depot init", typ, got[1].Flow.Stages[0], got[0].Flow.Stages[0])
		}
		// The stage texts, for every stage a bead is worked or waits in.
		n := 0
		for _, stage := range got[0].Flow.Stages {
			if got[0].Flow.IsTerminal(stage) {
				continue
			}
			n++
			want, err := os.ReadFile(filepath.Join(old.Path, ".yardr", "flows", filepath.FromSlash(f.file), stage+".md"))
			if err != nil {
				t.Fatal(err)
			}
			sf, err := store.HomeStageFile(a.cfg.Home, f.flow, stage)
			if err != nil || sf == nil || sf.Text != string(want) {
				t.Errorf("%s stage %s: the pack's stage file is %+v, %v; depot init's:\n%s", typ, stage, sf, err, want)
			}
		}
		if n != 4 {
			t.Errorf("%s: %d stage texts compared, want 4", typ, n)
		}
	}
	if strings.Contains(string(mustRead(t, filepath.Join(a.cfg.Home, "flows", "dir.default", "decide.md"))), "{{") {
		t.Error("a stage file was installed with a placeholder in it")
	}

	// The groups and routes: what depot init prints for the operator to add.
	groups, err := a.st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.Name+" "+g.Runner)
		if want := filepath.Join(a.cfg.Home, "packs", "dir", "roles", strings.TrimPrefix(strings.TrimSuffix(g.Name, "s"), "invoices-")+".md"); g.Config["prompt"] != want {
			t.Errorf("group %s reads %v, want %s", g.Name, g.Config["prompt"], want)
		}
	}
	if want := []string{"invoices-builders herdr", "invoices-reviewers herdr"}; !slices.Equal(names, want) {
		t.Errorf("groups = %v, want %v: no landing group, none of people", names, want)
	}
	routes, err := a.st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range routes {
		if r.Depot == nil || r.Label != nil {
			t.Errorf("route %+v is not one of a depot's stage", r)
			continue
		}
		typ := ""
		if r.Type != nil {
			typ = " " + *r.Type
		}
		got = append(got, *r.Depot+" "+r.Stage+typ+" -> "+r.Group)
	}
	slices.Sort(got)
	if want := []string{"invoices new -> invoices-builders", "invoices open train -> invoices-reviewers",
		"invoices review -> invoices-reviewers", "invoices review train -> invoices-reviewers"}; !slices.Equal(got, want) {
		t.Errorf("routes = %v\nwant %v", got, want)
	}
	if crew, _ := a.st.Crew(ctx); len(crew) != 0 {
		t.Errorf("the dir pack made crew: %+v", crew)
	}
	// The roles are the pack's, written for a directory, and name nobody
	// the pack does not have.
	for _, role := range []string{"builder.md", "reviewer.md"} {
		body := string(mustRead(t, filepath.Join(a.cfg.Home, "packs", "dir", "roles", role)))
		if !strings.Contains(strings.Join(strings.Fields(body), " "), "a plain directory, with no branches") || !strings.Contains(body, "Jo") || strings.Contains(body, "{{") {
			t.Errorf("%s as installed:\n%s", role, body)
		}
	}

	out, err = run(t, a.depotSourcesCmd(), "invoices")
	if err != nil {
		t.Fatal(err)
	}
	home := a.cfg.Home
	wantLines(t, out,
		"flow default pack dir (store dir.default)",
		"stage default:backlog pack dir ("+filepath.Join(home, "flows", "dir.default", "backlog.md")+")",
		"stage default:new pack dir ("+filepath.Join(home, "flows", "dir.default", "new.md")+")",
		"stage default:review pack dir ("+filepath.Join(home, "flows", "dir.default", "review.md")+")",
		"stage default:decide pack dir ("+filepath.Join(home, "flows", "dir.default", "decide.md")+")",
		"flow type/train pack dir (store dir.train)",
		"stage train:backlog pack dir ("+filepath.Join(home, "flows", "dir.train", "backlog.md")+")",
		"stage train:open pack dir ("+filepath.Join(home, "flows", "dir.train", "open.md")+")",
		"stage train:review pack dir ("+filepath.Join(home, "flows", "dir.train", "review.md")+")",
		"stage train:decide pack dir ("+filepath.Join(home, "flows", "dir.train", "decide.md")+")",
		"role invoices-builders pack dir ("+filepath.Join(home, "packs", "dir", "roles", "builder.md")+")",
		"role invoices-reviewers pack dir ("+filepath.Join(home, "packs", "dir", "roles", "reviewer.md")+")",
	)

	// Again: nothing changes, and there is still no gate to speak of.
	yard := yardOf(t, a)
	out, err = run(t, a.depotCmd(), "init", "invoices", "--pack", "dir")
	if err != nil || !strings.Contains(out, "pack dir is already applied to depot invoices: nothing changed\n") || yardOf(t, a) != yard {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	// A gate the depot has is its own: it is named, and left alone.
	writeFile(t, filepath.Join(invoices.Path, ".yardr", "check"), "#!/bin/sh\ntrue\n")
	a.json = true
	out, err = run(t, a.depotCmd(), "init", "invoices", "--pack", "dir", "--yes")
	a.json = false
	var res struct{ Gate gateResult }
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Gate.Written || res.Gate.Kind != store.DepotDir || res.Gate.Path != invoices.Path {
		t.Fatalf("json: %v, %+v\n%s", err, res, out)
	}
	if b := mustRead(t, filepath.Join(invoices.Path, ".yardr", "check")); string(b) != "#!/bin/sh\ntrue\n" {
		t.Errorf("the depot's gate was rewritten: %q", b)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A bead and a train walk the dir pack's flows in a depot set up from it,
// as they walk depot init's (TestDirDepotFlowsWalk): built, reviewed, sent
// back, asked about, and done, which closes them. No stage lands anything.
func TestDirPackFlowsWalk(t *testing.T) {
	a, _, _ := dirYard(t)
	setInteractive(t, false)
	ctx := context.Background()
	if out, err := run(t, a.depotCmd(), append([]string{"init", "invoices", "--pack", "dir", "--yes"}, dirFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	walk := func(typ string, steps [][2]string) *store.Bead {
		t.Helper()
		b, err := a.st.CreateBead(ctx, store.NewBead{Title: typ, Depot: "invoices", Type: typ}, "me")
		if err != nil || b.Stage != "backlog" {
			t.Fatalf("%s: %+v, %v", typ, b, err)
		}
		for _, s := range steps {
			move, want := s[0], s[1]
			if to, ok := strings.CutPrefix(move, "->"); ok {
				b, err = a.st.Advance(ctx, b.ID, to, store.Expect{}, "me")
			} else {
				b, err = a.st.Done(ctx, b.ID, move, "", store.Expect{}, "me")
			}
			if err != nil || b.Stage != want {
				t.Fatalf("%s: %s from %v: %v; want %s", typ, move, b, err, want)
			}
		}
		return b
	}
	if task := walk("task", [][2]string{
		{"->new", "new"}, {"done", "review"}, {"changes", "new"}, {"done", "review"},
		{"question", "decide"}, {"->new", "new"}, {"done", "review"}, {"done", "done"},
	}); task.Status != "closed" {
		t.Fatalf("a done task is %s", task.Status)
	}
	if train := walk(store.TypeTrain, [][2]string{
		{"->open", "open"}, {"done", "review"}, {"changes", "open"}, {"done", "review"},
		{"question", "decide"}, {"->open", "open"}, {"done", "review"}, {"done", "done"},
	}); train.Status != "closed" {
		t.Fatalf("a done train is %s", train.Status)
	}
	b, err := a.st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "invoices"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.Advance(ctx, b.ID, "approved", store.Expect{}, "me"); err == nil {
		t.Fatal("a dir depot's task reached approved")
	}
}

// Each pack yardr carries is for one kind of depot, and depot init refuses
// the other's, naming the one that fits and writing nothing: the starter's
// flow lands on a base a directory lacks, and the dir pack's roles commit
// nothing and its flow lands nothing, which is no way to work a repository.
// A pack of the operator's own is theirs to judge.
func TestDepotInitPackIsForTheDepotsKind(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	ctx := context.Background()
	dir := t.TempDir()
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	yard := yardOf(t, a)
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{append([]string{"init", "notes", "--pack", "starter", "--yes"}, starterFlags...),
			[]string{"depot notes is a directory depot, and pack starter is for a git depot", "yardr depot init notes --pack dir"}},
		// By what the pack is, not by how it was named.
		{append([]string{"init", "notes", "--pack", filepath.Join("..", "..", "packs", "starter"), "--yes"}, starterFlags...),
			[]string{"pack starter is for a git depot", "--pack dir"}},
		{append([]string{"init", "web", "--pack", "dir", "--yes"}, dirFlags...),
			[]string{"depot web is a git depot, and pack dir is for a directory depot", "yardr depot init web --pack starter"}},
	} {
		out, err := run(t, a.depotCmd(), tc.args...)
		for _, want := range tc.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%v: err = %v, want %q\n%s", tc.args, err, want, out)
			}
		}
		if strings.Contains(out, "create") {
			t.Errorf("%v: a plan was printed before the refusal:\n%s", tc.args, out)
		}
	}
	if got := yardOf(t, a); got != yard {
		t.Errorf("a refused depot init changed the yard:\n%s", got)
	}
	for _, d := range []string{repo, dir} {
		if _, err := os.Stat(filepath.Join(d, ".yardr")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused depot init wrote into %s: %v", d, err)
		}
	}
	// A pack of another name is not yardr's to place: the operator's own
	// pack for a directory goes into one.
	own := t.TempDir()
	sub, err := fs.Sub(packs.FS, packs.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(own, sub); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(own, "pack.yaml")
	writeFile(t, manifest, strings.Replace(string(mustRead(t, manifest)), "name: dir\n", "name: ledgers\n", 1))
	if out, err := run(t, a.depotCmd(), append([]string{"init", "notes", "--pack", own, "--yes"}, dirFlags...)...); err != nil ||
		!strings.Contains(out, "applied pack ledgers to depot notes") {
		t.Fatalf("a pack of the operator's own: %v\n%s", err, out)
	}
}
