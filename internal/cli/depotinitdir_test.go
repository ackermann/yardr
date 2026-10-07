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

// dirYard is a yard whose operator is Jo, with a dir depot that holds
// nothing, "invoices".
func dirYard(t *testing.T) (a *app, invoices store.Depot) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	invoices = store.Depot{Name: "invoices", Path: t.TempDir(), Kind: store.DepotDir}
	if err := st.PutDepot(ctx, invoices, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "operator", "Jo", "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}, invoices
}

var dirFlags = []string{"--bind", "builders=claude:opus", "--bind", "reviewers=codex", "--members", "backlog=", "--members", "decisions=me"}

// A dir depot is set up from the dir pack: two flows in the store govern
// its tasks and its trains, every stage a bead waits in has its stage file
// under the yard's home, and the routes hand new to the builders, review
// and a train's open to the reviewers, and backlog and decide to the yard's
// two people groups, so no bead there is unrouted (yardr-bv0x). Nothing is
// written into the directory, and it gets no gate.
func TestDepotInitSetsADirDepotUpFromTheDirPack(t *testing.T) {
	a, invoices := dirYard(t)
	setInteractive(t, false)
	ctx := context.Background()
	args := append([]string{"init", "invoices", "--pack", "dir", "--yes"}, dirFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"pack dir (", "  create           invoices-builders\n", "  create           decisions\n", "\napplied pack dir to depot invoices: ",
		"\ngate: none written. Depot invoices is a directory depot: its gate is .yardr/check in " + invoices.Path + " when it has one, and without one yardr depot check passes with \"no gate\"\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "names no group") {
		t.Errorf("the dir pack routes a stage to nobody:\n%s", out)
	}
	if strings.Contains(out, "git -C") || strings.Contains(out, "Nothing is committed") || strings.Contains(out, "base") {
		t.Errorf("a dir depot has no base and nothing to commit:\n%s", out)
	}
	if left := snapshot(t, invoices.Path); len(left) != 0 {
		t.Errorf("depot init --pack wrote into the directory: %v", left)
	}

	// The flows: one for a task and one for a train, both from the store,
	// with a stage file for every stage a bead is worked or waits in.
	for typ, flow := range map[string]string{"task": "dir.default", store.TypeTrain: "dir.train"} {
		b, err := a.st.CreateBead(ctx, store.NewBead{Title: typ, Depot: "invoices", Type: typ, Stage: "backlog"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		next, err := a.st.NextStages(ctx, b.ID)
		if err != nil || next.Flow == nil || next.Flow.Name != flow || next.Source.Kind != "store" {
			t.Fatalf("%s: governed by %+v, %v; want %s from the store", typ, next, err, flow)
		}
		n := 0
		for _, stage := range next.Flow.Stages {
			if next.Flow.IsTerminal(stage) {
				continue
			}
			n++
			if sf, err := store.HomeStageFile(a.cfg.Home, flow, stage); err != nil || sf == nil || strings.TrimSpace(sf.Text) == "" {
				t.Errorf("%s stage %s: the pack's stage file is %+v, %v", typ, stage, sf, err)
			}
		}
		if n != 4 {
			t.Errorf("%s: %d stages with a stage file, want 4", typ, n)
		}
	}
	if strings.Contains(string(mustRead(t, filepath.Join(a.cfg.Home, "flows", "dir.default", "decide.md"))), "{{") {
		t.Error("a stage file was installed with a placeholder in it")
	}

	// The groups and routes.
	groups, err := a.st.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.Name+" "+g.Runner)
		if g.Runner == "manual" {
			continue
		}
		if want := filepath.Join(a.cfg.Home, "packs", "dir", "roles", strings.TrimPrefix(strings.TrimSuffix(g.Name, "s"), "invoices-")+".md"); g.Config["prompt"] != want {
			t.Errorf("group %s reads %v, want %s", g.Name, g.Config["prompt"], want)
		}
	}
	if want := []string{"backlog manual", "decisions manual", "invoices-builders herdr", "invoices-reviewers herdr"}; !slices.Equal(names, want) {
		t.Errorf("groups = %v, want %v: no landing group, and the yard's two of people under their own names", names, want)
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
	// Every stage a bead or a train waits in has a group: none is unrouted.
	if want := []string{"invoices backlog -> backlog", "invoices backlog train -> backlog",
		"invoices decide -> decisions", "invoices decide train -> decisions",
		"invoices new -> invoices-builders", "invoices open train -> invoices-reviewers",
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

// A bead and a train walk the dir pack's flows in a depot set up from it:
// built, reviewed, sent back, asked about, and done, which closes them. No
// stage lands anything.
func TestDirPackFlowsWalk(t *testing.T) {
	a, _ := dirYard(t)
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

// The dir pack's two people groups are the yard's, the starter pack's: a
// yard that has them is asked nothing about them and keeps them as they
// are, members included, and a bead that waits for a person in the dir
// depot is in a person's group. Its backlog and decide used to name no
// group, so the plan noted four stages routed to nobody and every bead
// waiting there was reported as unrouted (yardr-bv0x).
func TestDirPackSharesTheYardsPeopleGroups(t *testing.T) {
	a, _ := dirYard(t)
	setInteractive(t, false)
	ctx := context.Background()
	for _, g := range []store.Group{
		{Name: "backlog", Runner: "manual", Limit: 4},
		{Name: "decisions", Runner: "manual", Members: []string{"ann", "me"}},
	} {
		if err := a.st.PutGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	people := func() string {
		t.Helper()
		var out []string
		for _, name := range []string{"backlog", "decisions"} {
			g, err := a.st.GetGroup(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(g)
			out = append(out, string(b))
		}
		return strings.Join(out, "\n")
	}
	before := people()
	out, err := run(t, a.packCmd(), "check", "dir", "--depot", "invoices")
	if err != nil || strings.Contains(out, "names no group") {
		t.Fatalf("pack check dir: %v\n%s", err, out)
	}
	// No --members: the groups are there, so there is nothing to ask.
	out, err = run(t, a.depotCmd(), "init", "invoices", "--pack", "dir", "--yes", "--bind", "builders=claude", "--bind", "reviewers=claude")
	if err != nil || strings.Contains(out, "names no group") {
		t.Fatalf("depot init --pack dir: %v\n%s", err, out)
	}
	if got := people(); got != before {
		t.Errorf("the import changed the yard's people groups:\n%s\nwere\n%s", got, before)
	}
	for typ, work := range map[string]string{"task": "new", store.TypeTrain: "open"} {
		b, err := a.st.CreateBead(ctx, store.NewBead{Title: "which " + typ, Depot: "invoices", Type: typ}, "me")
		if err != nil || b.Stage != "backlog" {
			t.Fatalf("%s: %+v, %v", typ, b, err)
		}
		if out, err = run(t, a.reviewCmd(), "-g", "backlog"); err != nil || !strings.Contains(out, b.ID) {
			t.Errorf("a %s in backlog is not in review -g backlog: %v\n%s", typ, err, out)
		}
		if _, err = a.st.Advance(ctx, b.ID, work, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err = a.st.Done(ctx, b.ID, "done", "", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		if b, err = a.st.Done(ctx, b.ID, "question", "", store.Expect{}, "me"); err != nil || b.Stage != "decide" {
			t.Fatalf("%s: %+v, %v", typ, b, err)
		}
		if out, err = run(t, a.reviewCmd(), "-g", "decisions"); err != nil || !strings.Contains(out, b.ID) {
			t.Errorf("a %s in decide is not in review -g decisions: %v\n%s", typ, err, out)
		}
		if out, err = run(t, a.reviewCmd(), "-g", "backlog"); err != nil || strings.Contains(out, b.ID) {
			t.Errorf("a %s in decide is in review -g backlog: %v\n%s", typ, err, out)
		}
	}
}

// Each pack yardr carries is for one kind of depot, and depot init refuses
// the other's, naming the one that fits and writing nothing: the starter's
// flow lands on a base a directory lacks, and the dir pack's roles commit
// nothing and its flow lands nothing, which is no way to work a repository.
// The pack's kind decides, which a pack of the operator's own may say too.
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
	// A pack of another name is placed by its kind as well: the operator's
	// own pack for a directory goes into one.
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

// A pack of the operator's own is placed by the kind it declares and by
// nothing else: one for the other kind of depot is refused with nothing
// written, saying what the pack declares, and one that declares none goes
// into a depot of either kind (yardr-vzb0).
func TestDepotInitGoesByThePacksDeclaredKind(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	ctx := context.Background()
	dir := t.TempDir()
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	// The dir pack under another name, saying kind as the case has it: its
	// groups are people and two agents, which either kind of depot takes.
	own := func(name, kind string) string {
		t.Helper()
		d := t.TempDir()
		sub, err := fs.Sub(packs.FS, packs.Dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(d, sub); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(d, "pack.yaml")
		text := string(mustRead(t, manifest))
		for old, new := range map[string]string{"name: dir\n": "name: " + name + "\n", "kind: dir\n": kind} {
			if !strings.Contains(text, old) {
				t.Fatalf("the dir pack has no %q", old)
			}
			text = strings.Replace(text, old, new, 1)
		}
		writeFile(t, manifest, text)
		return d
	}
	yard := yardOf(t, a)
	for _, tc := range []struct {
		depot, pack string
		want        []string
	}{
		{"web", own("ledgers", "kind: dir\n"), []string{"depot web is a git depot, and pack ledgers declares kind: dir in its pack.yaml", "nothing built on a branch would land"}},
		{"notes", own("ships", "kind: git\n"), []string{"depot notes is a directory depot, and pack ships declares kind: git in its pack.yaml", "nothing merges in a directory"}},
	} {
		out, err := run(t, a.depotCmd(), append([]string{"init", tc.depot, "--pack", tc.pack, "--yes"}, dirFlags...)...)
		for _, want := range tc.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: err = %v, want %q\n%s", tc.depot, err, want, out)
			}
		}
		// yardr's pack for the depot is no answer to a pack of one's own.
		if err != nil && strings.Contains(err.Error(), "--pack") {
			t.Errorf("%s: the refusal offers a pack of yardr's: %v", tc.depot, err)
		}
		if strings.Contains(out, "create") {
			t.Errorf("%s: a plan was printed before the refusal:\n%s", tc.depot, out)
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
	for _, tc := range []struct{ depot, name string }{{"web", "anyweb"}, {"notes", "anynotes"}} {
		out, err := run(t, a.depotCmd(), append([]string{"init", tc.depot, "--pack", own(tc.name, ""), "--yes"}, dirFlags...)...)
		if err != nil || !strings.Contains(out, "applied pack "+tc.name+" to depot "+tc.depot) {
			t.Fatalf("a pack of no kind into depot %s: %v\n%s", tc.depot, err, out)
		}
		if !strings.Contains(out, "pack "+tc.name+" (") || !strings.Contains(out, "), of no kind, into depot "+tc.depot+"\n") {
			t.Errorf("the plan's first line does not say the pack has no kind:\n%s", out)
		}
	}
}
