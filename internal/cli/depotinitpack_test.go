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

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// packYard is a yard with a git depot "web", a Go repository with nothing of
// yardr's in it and no test command. files are committed with go.mod.
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
	if _, err := a.st.PutSource(context.Background(), "linear.yaml", linearPackDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutDepot(context.Background(), store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	// Imports run as the operator here, as an operator's import does.
	if err := a.st.SetSetting(context.Background(), store.OperatorSetting, "me", "me"); err != nil {
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
// pack apply makes, and the depot's test command as the repository says it,
// with nothing written into the checkout. depot sources names the pack for
// the flow and every role. A second run changes nothing and keeps the test
// command, whoever set it.
func TestDepotInitPack(t *testing.T) {
	t.Parallel()
	a, repo := packYard(t, nil)
	requireNonInteractive(t)
	before := snapshot(t, repo)
	args := append([]string{"init", "web", "--pack", "starter", "--yes"}, starterFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	const guess = "go vet ./... && go test ./..."
	for _, want := range []string{"pack starter (", "  create           web-builders\n", "\napplied pack starter to depot web: 40 created, 0 changed, 0 unchanged\n",
		"\ntest: depot web is tested with " + guess + " (a guess from " + repo + ": check it with yardr depot show web)\n",
		"To change it:\n  yardr depot add web " + repo + " --base main --test '<command>'\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "\ntest: ") < strings.Index(out, "\napplied pack") {
		t.Errorf("the test command is reported before the import:\n%s", out)
	}
	// The checkout holds nothing of yardr's, and the depot is as it was but
	// for its test command, which depot show prints.
	if after := snapshot(t, repo); !maps.Equal(before, after) {
		t.Errorf("the checkout changed: %v", slices.Sorted(maps.Keys(after)))
	}
	if d, err := a.st.GetDepot(context.Background(), "web"); err != nil || *d != (store.Depot{Name: "web", Path: repo, Kind: store.DepotGit, Base: "main", Test: guess}) {
		t.Fatalf("depot = %+v, %v", d, err)
	}
	if out, err := run(t, a.depotCmd(), "show", "web"); err != nil || !strings.Contains(out, "  test:  "+guess+"\n") {
		t.Fatalf("depot show: %v\n%s", err, out)
	}
	// The yard is what pack apply makes it.
	ctx := context.Background()
	roles := config.At(a.cfg.Home).PackRoles("starter")
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
		"stage default:backlog pack starter ("+filepath.Join(stages, "backlog.md")+")",
		"stage default:new pack starter ("+filepath.Join(stages, "new.md")+")",
		"stage default:review pack starter ("+filepath.Join(stages, "review.md")+")",
		"stage default:review-security pack starter ("+filepath.Join(stages, "review-security.md")+")",
		"stage default:approved pack starter ("+filepath.Join(stages, "approved.md")+")",
		"stage default:decide pack starter ("+filepath.Join(stages, "decide.md")+")",
		"flow type/train pack starter (store starter.train)",
		"stage train:backlog none", "stage train:open none", "stage train:review none", "stage train:review-security none", "stage train:approved none",
		"stage train:in-pr none", "stage train:land none", "stage train:merge none", "stage train:decide none",
		"role web-builders pack starter ("+filepath.Join(roles, "builder.md")+")",
		"role web-reviewers pack starter ("+filepath.Join(roles, "reviewer.md")+")",
		"role web-security-reviewers pack starter ("+filepath.Join(roles, "review-security.md")+")",
		"role boss pack starter ("+filepath.Join(roles, "yardmaster.md")+")",
		"rules web none",
	)

	// Again, with no flag but the pack: the record answers, nothing is to
	// confirm, and the test command the operator set since is kept.
	if err := a.st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main", Test: "make test"}, "me"); err != nil {
		t.Fatal(err)
	}
	yard := yardOf(t, a)
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter")
	if err != nil || !strings.Contains(out, "pack starter is already applied to depot web: nothing changed\n") ||
		!strings.Contains(out, "\ntest: depot web already has a test command, kept: make test\n") || strings.Contains(out, "To change it") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if d, _ := a.st.GetDepot(ctx, "web"); d == nil || d.Test != "make test" || yardOf(t, a) != yard {
		t.Errorf("the second run changed the test command (%+v) or the yard", d)
	}

	a.json = true
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes")
	var got struct {
		Result struct{ Unchanged int }
		Test   testResult
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Result.Unchanged != 40 ||
		got.Test != (testResult{Depot: "web", Kind: store.DepotGit, Path: repo, Base: "main", Test: "make test"}) {
		t.Fatalf("json: %v, %+v\n%s", err, got, out)
	}
}

// A depot with roles of its own is shown them as shadowed before anything
// is written, and nothing is: not without --yes where nothing can be
// asked, and not after a no in a terminal. That holds for the test command too.
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
		if d, err := a.st.GetDepot(context.Background(), "web"); err != nil || d.Test != "" {
			t.Errorf("%s: the depot has a test command: %+v, %v", when, d, err)
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

	// A missing answer names every missing flag, one to a line, before
	// anything else is printed: no plan. The flags are this command's.
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes", "--crew", "yardmaster=boss")
	want := "--bind builders=<kind>[:<model>]\n" +
		"--bind reviewers=<kind>[:<model>]\n" +
		"--members backlog=<users>\n" +
		"--members decisions=<users>\n" +
		"--bind crew:yardmaster=<kind>[:<model>]\n"
	if !errors.Is(err, errCheckFailed) || out != want {
		t.Fatalf("without the answers: %v, got:\n%s\nwant:\n%s", err, out, want)
	}
	unchanged("without the answers")

	// With two answers open, only the two missing flags.
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes",
		"--crew", "yardmaster=boss", "--bind", "builders=claude:opus",
		"--bind", "crew:yardmaster=claude", "--members", "decisions=ann,boss")
	want = "--bind reviewers=<kind>[:<model>]\n--members backlog=<users>\n"
	if !errors.Is(err, errCheckFailed) || out != want {
		t.Fatalf("with two answers open: %v, got:\n%s\nwant:\n%s", err, out, want)
	}
	unchanged("with two answers open")

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
	// What is confirmed names the test command.
	if !strings.Contains(confirm, "the depot's test command, go vet ./... && go test ./...") {
		t.Errorf("the confirmation does not name the test command:\n%s", confirm)
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

// The test command is depot init's own write and is confirmed like the
// import's: over a pack that is already applied it is still not set without
// --yes. A repository that does not say how it is tested gets none, and is
// told how to set one.
func TestDepotInitPackTestAlone(t *testing.T) {
	t.Parallel()
	a, _ := packYard(t, nil)
	requireNonInteractive(t)
	ctx := context.Background()
	if out, err := run(t, a.packCmd(), append([]string{"apply", "starter", "--depot", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	test := func() string {
		t.Helper()
		d, err := a.st.GetDepot(ctx, "web")
		if err != nil {
			t.Fatal(err)
		}
		return d.Test
	}
	out, err := run(t, a.depotCmd(), "init", "web", "--pack", "starter")
	if err == nil || !strings.Contains(err.Error(), "give --yes") || test() != "" {
		t.Fatalf("without --yes: %v, test %q\n%s", err, test(), out)
	}
	out, err = run(t, a.depotCmd(), "init", "web", "--pack", "starter", "--yes")
	if err != nil || test() != "go vet ./... && go test ./..." || !strings.Contains(out, "nothing changed\n") || !strings.Contains(out, "\ntest: depot web is tested with go vet ./... && go test ./... (a guess from ") {
		t.Fatalf("with --yes: %v, test %q\n%s", err, test(), out)
	}
	if evs, err := a.st.Events(ctx, "", 0); err != nil || !slices.ContainsFunc(evs, func(e store.Event) bool {
		return e.Kind == "depot_updated" && e.Data["test"] == "go vet ./... && go test ./..."
	}) {
		t.Errorf("no depot_updated event carries the test command: %v", err)
	}

	// Nothing to guess from: no question, no test command, and how to set one.
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "-b", "main")
	gitIn(t, bare, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "a repository")
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: bare, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.depotCmd(), append([]string{"init", "notes", "--pack", "starter", "--yes"}, starterFlags...)...)
	if d, _ := a.st.GetDepot(ctx, "notes"); err != nil || d.Test != "" ||
		!strings.Contains(out, "\ntest: none set. Nothing in "+bare+" says how depot notes is tested, so no bead's brief names a test command and a script group gets no YARDR_TEST. To set one:\n  yardr depot add notes "+bare+" --base main --test '<command>'\n") {
		t.Fatalf("a repository that says nothing: %v, %+v\n%s", err, d, out)
	}
}

// What depot init refuses writes nothing: a pack that is not there, and an
// import nobody said yes to, whatever flags it was given, and a flag it had
// for the role files it once wrote into a depot. (A pack for the other kind
// of depot: TestDepotInitPackIsForTheDepotsKind.)
func TestDepotInitPackRefusals(t *testing.T) {
	t.Parallel()
	a, repo := packYard(t, nil)
	requireNonInteractive(t)
	dir := t.TempDir()
	if err := a.st.PutDepot(context.Background(), store.Depot{Name: "notes", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	yard := yardOf(t, a)
	// "check failed" is the missing answers, printed as the flags to add.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"init", "web"}, "check failed"},
		{[]string{"init", "web", "--bind", "builders=claude"}, "check failed"},
		{[]string{"init", "web", "--yes", "--operator", "Jo"}, "unknown flag: --operator"},
		{[]string{"init", "notes"}, "check failed"},
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

// From pack starter on a yard with no source, depot init says each source
// the pack could use once, as optional, with the command that sets it up;
// no step calls one "missing".
func TestDepotInitSaysUnusedSourcesOnceAsOptional(t *testing.T) {
	t.Parallel()
	a, _, _, _, _ := stageYard(t)
	// The import runs as the operator, as an operator's import does.
	if err := a.st.SetSetting(context.Background(), store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	requireNonInteractive(t)
	args := append([]string{"init", "web", "--pack", "starter", "--yes"}, starterFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "missing") {
		t.Errorf("a source is called missing:\n%s", out)
	}
	for _, want := range []string{
		"  optional linear       the yard has no source linear (yardr pack apply linear brings it)\n",
		"  optional github       the yard has no source github (yardr pack apply github brings it)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// One line per source: the pack's "asks for" line names what it wants,
	// and the optional line is the only other to name the source.
	for _, source := range []string{"linear", "github"} {
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, source) && !strings.Contains(line, "the pack asks for") {
				lines = append(lines, line)
			}
		}
		if len(lines) != 1 || !strings.Contains(lines[0], "optional") {
			t.Errorf("%s is said in %d lines, want one optional line: %q\n%s", source, len(lines), lines, out)
		}
	}
	if !strings.Contains(out, "  the pack asks for linear\n") {
		t.Errorf("the pack's ask is gone:\n%s", out)
	}
}

// A need the import cannot do without still reads as today: with linear
// declared but no hook, the hook, identity and credential are "missing",
// not "optional".
func TestDepotInitKeepsMissingForASourceTheYardHas(t *testing.T) {
	t.Parallel()
	a, _ := packYard(t, nil)
	requireNonInteractive(t)
	args := append([]string{"init", "web", "--pack", "starter", "--yes"}, starterFlags...)
	out, err := run(t, a.depotCmd(), args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"source linear, for crew member boss\n  missing  hook",
		"  missing  identity     crew member boss has no identity in linear",
		"  missing  credential   ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "optional  linear") {
		t.Errorf("a source the yard has is called optional:\n%s", out)
	}
}
