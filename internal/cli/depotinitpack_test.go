package cli

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
)

// packYard is a yard with a git depot "web", a Go repository with nothing of
// yardr's in it and no gate. files are committed with go.mod.
func packYard(t *testing.T, files map[string]string) (a *app, repo string) {
	t.Helper()
	a, _, _, _, _ = stageYard(t)
	repo = t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(repo, "go.mod"), "module x\n")
	for name, text := range files {
		writeFile(t, filepath.Join(repo, name), text)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "a repository")
	if err := a.st.PutDepot(context.Background(), store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return a, repo
}

// yardOf is what an import may change, as text: the yard's groups, routes,
// crew and pack records, and the files under its home.
func yardOf(t *testing.T, a *app) string {
	t.Helper()
	ctx := context.Background()
	groups, _ := a.st.Groups(ctx)
	routes, _ := a.st.Routes(ctx)
	crew, _ := a.st.Crew(ctx)
	recs, _ := a.st.Packs(ctx)
	b, err := json.Marshal([]any{groups, routes, crew, recs, slices.Sorted(maps.Keys(snapshot(t, a.cfg.Home)))})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// depot init --pack sets a depot up from the pack: the plan, the import
// pack apply makes, and in the checkout the gate alone, with the commit
// that puts it on the base. depot sources names the pack for the flow and
// every role. A second run changes nothing and keeps the gate.
func TestDepotInitPack(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	before := snapshot(t, repo)
	args := append([]string{"init", "web", "--pack", "starter", "--yes"}, starterFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gate := filepath.Join(repo, ".yardr", "check")
	for _, want := range []string{"pack starter (", "  create           web-builders\n", "\napplied pack starter to depot web: 25 created, 0 changed, 0 unchanged\n",
		"\ngate: wrote .yardr/check in " + repo + ": go vet ./... && go test ./... (a guess from the repo: check it)\n",
		"Nothing is committed. yardr reads the gate from main:\n  git -C " + repo + " add .yardr/check && git -C " + repo + " commit -m 'yardr: gate' -- .yardr/check\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "\ngate: ") < strings.Index(out, "\napplied pack") {
		t.Errorf("the gate is reported before the import:\n%s", out)
	}
	// The checkout holds the gate and nothing else of yardr's.
	after := snapshot(t, repo)
	if info, err := os.Stat(gate); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("%s: %v, %v", gate, err, info)
	}
	if !strings.HasSuffix(after[gate], "\ngo vet ./... && go test ./...\n") {
		t.Errorf("the gate is %q", after[gate])
	}
	delete(after, gate)
	if !maps.Equal(before, after) {
		t.Errorf("the checkout has more than the gate: %v", slices.Sorted(maps.Keys(after)))
	}
	// The yard is what pack apply makes it.
	ctx := context.Background()
	roles := filepath.Join(a.cfg.Home, "packs", "starter", "roles")
	if g, err := a.st.GetGroup(ctx, "web-builders"); err != nil || g.Config["prompt"] != filepath.Join(roles, "builder.md") || g.Config["model"] != "opus" {
		t.Fatalf("web-builders = %+v, %v", g, err)
	}
	if recs, _ := a.st.Packs(ctx); len(recs) != 1 || recs[0].Pack != "starter" || recs[0].Depot != "web" {
		t.Fatalf("records = %+v", recs)
	}
	out, err = run(t, a.depotSourcesCmd(), "web")
	if err != nil {
		t.Fatal(err)
	}
	stages := filepath.Join(a.cfg.Home, "flows", "starter.default")
	wantLines(t, out,
		"flow default pack starter (store starter.default)",
		"stage backlog pack starter ("+filepath.Join(stages, "backlog.md")+")",
		"stage new pack starter ("+filepath.Join(stages, "new.md")+")",
		"stage review pack starter ("+filepath.Join(stages, "review.md")+")",
		"stage approved pack starter ("+filepath.Join(stages, "approved.md")+")",
		"stage decide pack starter ("+filepath.Join(stages, "decide.md")+")",
		"role web-builders pack starter ("+filepath.Join(roles, "builder.md")+")",
		"role web-reviewers pack starter ("+filepath.Join(roles, "reviewer.md")+")",
		"role boss pack starter ("+filepath.Join(roles, "yardmaster.md")+")",
	)

	// Again, with no flag but the pack: the record answers, nothing is to
	// confirm, and the gate that is there is kept as it is.
	writeFile(t, gate, "#!/bin/sh\nmake test\n")
	yard := yardOf(t, a)
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter")
	if err != nil || !strings.Contains(out, "pack starter is already applied to depot web: nothing changed\n") ||
		!strings.Contains(out, "\ngate: .yardr/check in "+repo+" is already there, kept\n") || strings.Contains(out, "Nothing is committed") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(gate); string(b) != "#!/bin/sh\nmake test\n" || yardOf(t, a) != yard {
		t.Errorf("the second run changed the gate (%q) or the yard", b)
	}

	a.json = true
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes")
	var got struct {
		Result struct{ Unchanged int }
		Gate   gateResult
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Result.Unchanged != 25 || got.Gate.Written || got.Gate.File != ".yardr/check" || got.Gate.Path != repo {
		t.Fatalf("json: %v, %+v\n%s", err, got, out)
	}
}

// A depot with roles of its own is shown them as shadowed before anything
// is written, and nothing is: not without --yes where nothing can be
// asked, and not after a no in a terminal. That holds for the gate too.
func TestDepotInitPackWritesNothingUnasked(t *testing.T) {
	a, repo := packYard(t, map[string]string{".yardr/roles/builder.md": "the depot's builder\n", ".yardr/roles/yardmaster.md": "the depot's yardmaster\n"})
	before, yard := snapshot(t, repo), yardOf(t, a)
	unchanged := func(when string) {
		t.Helper()
		if !maps.Equal(before, snapshot(t, repo)) {
			t.Errorf("%s: the checkout changed: %v", when, slices.Sorted(maps.Keys(snapshot(t, repo))))
		}
		if got := yardOf(t, a); got != yard {
			t.Errorf("%s: the yard changed:\n%s\nwas:\n%s", when, got, yard)
		}
	}
	shadows := []string{"  create           web-builders\n                     shadows .yardr/roles/builder.md\n",
		"  create           boss\n                     shadows .yardr/roles/yardmaster.md\n"}
	args := append([]string{"init", "web", "--pack", "starter"}, starterFlags...)

	setInteractive(t, false)
	out, err := run(t, a.depotCmd(), args...)
	if err == nil || !strings.Contains(err.Error(), "depot init changes the yard: give --yes") {
		t.Fatalf("without --yes: %v\n%s", err, out)
	}
	for _, want := range shadows {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}
	unchanged("without --yes")

	// A missing answer names the flag, as a flag of this command.
	_, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes", "--crew", "yardmaster=boss")
	if err == nil || !strings.Contains(err.Error(), "depot init cannot ask without a terminal") || !strings.Contains(err.Error(), "--bind builders=<kind>[:<model>] for the new group web-builders") {
		t.Fatalf("without the answers: %v", err)
	}
	unchanged("without the answers")

	setInteractive(t, true)
	var confirm string
	stubAsk(t, func(fields ...huh.Field) error {
		for _, f := range fields {
			if c, ok := f.(*huh.Confirm); ok {
				confirm = c.View()
				return huh.ErrUserAborted
			}
		}
		return nil
	})
	out, err = run(t, a.depotCmd(), args...)
	if err != nil || !strings.Contains(out, "cancelled; nothing was changed\n") {
		t.Fatalf("leaving the confirmation: %v\n%s", err, out)
	}
	if i, j := strings.Index(out, shadows[0]), strings.Index(out, "cancelled"); i < 0 || j < i {
		t.Errorf("the shadowed role is not shown before the question:\n%s", out)
	}
	// What is confirmed names the gate.
	if !strings.Contains(confirm, ".yardr/check") {
		t.Errorf("the confirmation does not name the gate:\n%s", confirm)
	}
	unchanged("after leaving the confirmation")

	// --depot-roles keeps the depot's own, as for pack apply.
	setInteractive(t, false)
	if out, err = run(t, a.depotCmd(), append(args, "--yes", "--depot-roles")...); err != nil || !strings.Contains(out, "kept: prompt roles/builder.md: the depot has its own .yardr/roles/builder.md\n") {
		t.Fatalf("--depot-roles: %v\n%s", err, out)
	}
	if g, _ := a.st.GetGroup(context.Background(), "web-builders"); g == nil || g.Config["prompt"] != "roles/builder.md" {
		t.Errorf("web-builders = %+v", g)
	}
}

// The gate is depot init's own write and is confirmed like the import's:
// over a pack that is already applied it is still not written without
// --yes. A depot whose gate is a --check in the store gets no file.
func TestDepotInitPackGateAlone(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	ctx := context.Background()
	if out, err := run(t, a.packCmd(), append([]string{"apply", "starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gate := filepath.Join(repo, ".yardr", "check")
	out, err := run(t, a.depotCmd(), "init", "web", "--pack", "starter")
	if _, serr := os.Stat(gate); err == nil || !strings.Contains(err.Error(), "give --yes") || !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("without --yes: %v, stat %v\n%s", err, serr, out)
	}
	if err := a.st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main", Check: "make test"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter")
	if _, serr := os.Stat(gate); err != nil || !errors.Is(serr, os.ErrNotExist) || !strings.Contains(out, "gate: depot web has a --check in the store (make test), so no .yardr/check was written\n") {
		t.Fatalf("with a --check: %v, stat %v\n%s", err, serr, out)
	}
	if err := a.st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes")
	if _, serr := os.Stat(gate); err != nil || serr != nil || !strings.Contains(out, "nothing changed\n") || !strings.Contains(out, "\ngate: wrote .yardr/check in ") {
		t.Fatalf("with --yes: %v, stat %v\n%s", err, serr, out)
	}
}

// What depot init --pack refuses, each saying why and writing nothing: a
// dir depot, a flag for the files it does not write, and without --pack a
// flag that answers an import.
func TestDepotInitPackRefusals(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	dir := t.TempDir()
	if err := a.st.PutDepot(context.Background(), store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	yard := yardOf(t, a)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{append([]string{"init", "notes", "--pack", "starter", "--yes"}, starterFlags...), "depot notes is a directory depot, and depot init --pack is for a git depot: a pack's flow lands a bead on a base branch"},
		{[]string{"init", "web", "--pack", "starter", "--operator", "Jo"}, "--operator does not go with --pack: a pack's roles name the yard's operator setting"},
		{[]string{"init", "web", "--pack", "starter", "--yardmaster", "boss"}, "--yardmaster does not go with --pack: --crew yardmaster=<name>"},
		{[]string{"init", "web", "--pack", "starter", "--mayor", "boss"}, "--yardmaster does not go with --pack"},
		{[]string{"init", "web", "--pack", "starter", "--with-yardmaster"}, "--with-yardmaster does not go with --pack"},
		{[]string{"init", "web", "--bind", "builders=claude"}, "--bind answers a pack's import: it needs --pack <pack>"},
		{[]string{"init", "web", "--yes"}, "--yes answers a pack's import: it needs --pack <pack>"},
		{[]string{"init", "web", "--pack", "nosuch", "--yes"}, `no pack named "nosuch"`},
	} {
		out, err := run(t, a.depotCmd(), tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q\n%s", tc.args, err, tc.want, out)
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
}
