package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

func (a *app) depotInitCmd() *cobra.Command {
	var packArg string
	var imp importFlags
	cmd := &cobra.Command{
		Use:   "init <depot> [--pack <pack>]",
		Short: "Set a depot up from a pack (the yard's default unless one is named): its groups, roles, flow and routes in the yard, and a guess at its test command",
		Long: `Set a depot up from a pack: the yard's default pack, or the one --pack
names. It does two things.

It imports the pack as yardr pack apply <pack> --depot <depot> does: the
pack's roles, scripts and stage files are installed under the yard's home,
and the depot's groups, its routes and its pointer to the pack's flow go into
the store. The plan is printed first, and what the import asks is answered
in the terminal or by the same flags: --group, --bind, --members, --crew,
--flow, --open-threads, --calls, --source-env, --notify, --depot-roles. A
role the depot has under .yardr/roles/ is named in the plan as shadowed.

And it sets the depot's test command (yardr depot show <depot>) when a git
depot has none and the checkout says how it is tested: a guess, to be read
before it is relied on. A test script of the repository's own is named by
its path (.yardr/test, scripts/test); else go.mod gives go vet and go test,
a package.json with a test script npm, pnpm or yarn test, Cargo.toml cargo
test, a Makefile with a test target make test. A repository that says none
of that gets no test command. yardr never runs it: a bead's prime prints it
and a script group gets it as YARDR_TEST (docs/configuration.md, "Tests").

Nothing is written into the depot: no roles, no flow, no stage files, no
script. Those are the pack's, and they are changed in the pack and brought
to the yard by yardr pack apply. A depot that keeps a flow of its own commits
it under .yardr/flows/ on its base and says so with yardr depot source <depot>
flow depot; one that keeps a role of its own, with yardr depot source
<depot> role <group> depot (docs/configuration.md). Nothing is set, the test
command included, before a yes in the terminal or --yes.

Without --pack the pack is the yard's default_pack setting (yardr settings
set default_pack <name|directory>). Unset, it is one of the two packs yardr
carries, by the depot's kind: starter for a git depot, dir for a directory
depot. <pack> is named as for yardr pack apply: a directory with a path
separator in it, else the name of a pack the yard has imported or yardr
carries. Applying the pack again later is yardr pack apply.

A dir depot gets no guess: nothing in a folder of documents says how it is
tested. Of the two packs yardr carries each is for one kind of depot: starter is refused on a
dir depot, whose beads have no base branch to land on, and dir on a git
depot. See docs/packs.md and docs/dir-depots.md.`,
		Example: `  yardr depot init webapp
  yardr depot init webapp --yes --crew yardmaster=boss \
    --bind builders=claude:opus --bind reviewers=codex --members backlog= --members decisions=jo
  yardr depot init invoices --yes --bind builders=claude --bind reviewers=claude \
    --members backlog= --members decisions=jo          # a dir depot
  yardr depot init notes --pack ~/packs/research
  yardr depot init signalbox --pack contributors --yes --group builders=yardr-builders \
    --group reviewers=yardr-reviewers --group maintainers=decisions   # groups the yard has`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			depot, err := st.GetDepot(c.Context(), args[0])
			if err != nil {
				return err
			}
			if packArg == "" {
				set := false
				if packArg, set, err = defaultPack(c.Context(), st, *depot); err != nil {
					return err
				}
				if set {
					// So a pack that is not found or does not fit is known
					// for the setting's, which the command line does not show.
					fmt.Fprintf(c.ErrOrStderr(), "pack %s: the yard's %s setting\n", packArg, defaultPackSetting)
				}
			}
			return a.depotInitPack(c.OutOrStdout(), c.Context(), *depot, packArg, &imp)
		},
	}
	cmd.Flags().StringVar(&packArg, "pack", "", "the pack to set the depot up from: a directory, a pack's URL, or the name of an imported or embedded pack (default: the yard's default_pack setting, else starter, and dir for a dir depot)")
	imp.registerShape(cmd)
	imp.registerAnswers(cmd)
	return cmd
}

// defaultPack is the pack depot is set up from when depot init names none:
// the yard's default_pack setting (set is then true), else of the two packs
// yardr carries the one written for the depot's kind.
func defaultPack(ctx context.Context, st store.API, depot store.Depot) (arg string, set bool, err error) {
	all, err := st.Settings(ctx)
	if err != nil {
		return "", false, err
	}
	if v := all[defaultPackSetting]; v != "" {
		return v, true, nil
	}
	if depot.IsDir() {
		return packs.Dir, false, nil
	}
	return packs.Starter, false, nil
}

// testResult is what depot init did about the depot's test command.
type testResult struct {
	Depot string `json:"depot"`
	Kind  string `json:"kind"`
	Path  string `json:"path"` // the checkout, or a dir depot's directory
	Base  string `json:"base,omitempty"`
	Test  string `json:"test"` // the depot's test command now; "" for none
	Set   bool   `json:"set"`  // false: the depot had one, nothing said how it is tested, or it is a dir depot
}

// depotInitPack sets depot up from the pack arg names: the guess at its
// test command into the store when a git depot has none, then the import
// pack apply makes. Nothing goes into the checkout: the roles, the flow and
// its stage files are the pack's, under the yard's home. A dir depot gets
// no guess: what would test a folder of documents is not told by its files.
func (a *app) depotInitPack(w io.Writer, ctx context.Context, depot store.Depot, arg string, f *importFlags) error {
	res := &testResult{Depot: depot.Name, Kind: depot.Kind, Path: depot.Path, Base: depot.Base, Test: depot.Test}
	var guess, also string
	if !depot.IsDir() && depot.Test == "" {
		if guess = guessTest(depot.Path); guess != "" {
			also = "the depot's test command, " + guess
		}
	}
	f.opts.Depot = depot.Name
	imp, err := a.planImport(w, ctx, "depot init", arg, f, also, func(p *pack.Pack) error { return packFits(p, depot) })
	if imp == nil || err != nil {
		return err
	}
	// Before the import and after the yes: nothing is set unasked. The
	// depot is read again, so what changed of it while the plan was asked
	// about is kept, and a test command set meanwhile is too.
	if guess != "" {
		st, err := a.store(ctx)
		if err != nil {
			return err
		}
		now, err := st.GetDepot(ctx, depot.Name)
		if err != nil {
			return err
		}
		if res.Test = now.Test; now.Test == "" {
			now.Test = guess
			if err := st.PutDepot(ctx, *now, a.who()); err != nil {
				return err
			}
			res.Test, res.Set = guess, true
		}
	}
	err = imp.apply(w, ctx, a, map[string]any{"test": res}, res.print)
	if err != nil && res.Set {
		// A failed import may have printed nothing, and the command is set.
		err = fmt.Errorf("%w (depot %s's test command was set to %s before the import failed, and is kept)", err, depot.Name, res.Test)
	}
	return err
}

// packFits refuses a pack for a depot of the other kind than the one it
// declares (kind in pack.yaml). A git pack's flow lands a bead on a base
// branch through an assembly, which a directory has not; a dir pack's roles
// commit nothing and its flow ends at review, so in a repository nothing a
// bead built would ever land. A pack that declares no kind fits either: it
// is the operator's to judge. The pack's name decides nothing here, only
// what the refusal can offer: for one of yardr's two, the other.
func packFits(p *pack.Pack, depot store.Depot) error {
	kind := store.DepotGit
	if depot.IsDir() {
		kind = store.DepotDir
	}
	if p.Kind == "" || p.Kind == kind {
		return nil
	}
	if p.Kind == pack.KindSource {
		return fmt.Errorf("depot %s is set up from a pack for a depot, and pack %s is of kind source: it is imported into the yard, not into a depot (yardr pack apply %s)", depot.Name, p.Name, p.Name)
	}
	// The explanation is of yardr's own pack where the refused one is that,
	// and of the kind where it is the operator's, whose flow yardr has not
	// read for what it does.
	depotIs, packIs, fits, other := "a git depot", "a directory depot", packs.Starter, packs.Dir
	its, such := "its roles commit nothing and its flow closes a bead at review, so nothing built on a branch would land",
		"a pack for a directory commits and lands nothing, so nothing built on a branch would land"
	if depot.IsDir() {
		depotIs, packIs, fits, other = "a directory depot", "a git depot", packs.Dir, packs.Starter
		its, such = "its flow lands a bead on a base branch through an assembly, and nothing merges in a directory",
			"a pack for a repository lands a bead on a base branch, and nothing merges in a directory"
	}
	if p.Name == other {
		return fmt.Errorf("depot %s is %s, and pack %s is for %s: %s. yardr's pack for %s is %s: yardr depot init %s --pack %s",
			depot.Name, depotIs, p.Name, packIs, its, depotIs, fits, depot.Name, fits)
	}
	return fmt.Errorf("depot %s is %s, and pack %s declares kind: %s in its %s: %s. A pack with no kind fits either",
		depot.Name, depotIs, p.Name, p.Kind, pack.File, such)
}

// testCommand is the command line that sets, or changes, the test command
// of the depot in r. depot add replaces the depot's record, so it names the
// path and the base the depot has.
func testCommand(r *testResult, test string) string {
	cmd := fmt.Sprintf("yardr depot add %s %s", r.Depot, shellQuote(r.Path))
	if r.Kind == store.DepotDir {
		cmd += " --kind dir"
	} else if r.Base != "" {
		cmd += " --base " + shellQuote(r.Base)
	}
	return cmd + " --test " + shellQuote(test)
}

func (r *testResult) print(w io.Writer) {
	fmt.Fprintln(w)
	switch {
	case r.Set:
		fmt.Fprintf(w, "test: depot %s is tested with %s (a guess from %s: check it with yardr depot show %s)\n", r.Depot, r.Test, r.Path, r.Depot)
		fmt.Fprintf(w, "To change it:\n  %s\n", testCommand(r, "<command>"))
	case r.Test != "":
		fmt.Fprintf(w, "test: depot %s already has a test command, kept: %s\n", r.Depot, r.Test)
	case r.Kind == store.DepotDir:
		fmt.Fprintf(w, "test: none set. Depot %s is a directory depot, and nothing in a directory says how it is tested. To set one:\n  %s\n", r.Depot, testCommand(r, "<command>"))
	default:
		fmt.Fprintf(w, "test: none set. Nothing in %s says how depot %s is tested, so no bead's brief names a test command and a script group gets no YARDR_TEST. To set one:\n  %s\n", r.Path, r.Depot, testCommand(r, "<command>"))
	}
}

// yardOperator is the yard's operator setting, or who it defaults to.
func yardOperator(ctx context.Context, st store.API) string {
	all, err := st.Settings(ctx)
	if err != nil {
		return defaultOperator()
	}
	return or(all["operator"], defaultOperator())
}

// yardmasterOf is the yardmaster crew member the roles tell agents to mail: the
// yard's yardmaster setting, else the one crew member of a yard from before the
// setting, else init's default name.
func yardmasterOf(ctx context.Context, st store.API) string {
	if all, err := st.Settings(ctx); err == nil && all[store.YardmasterSetting] != "" {
		return all[store.YardmasterSetting]
	}
	if rs, err := st.Crew(ctx); err == nil && len(rs) == 1 {
		return rs[0].Name
	}
	return "yardmaster"
}

// guessTest is the command that tests the repository checked out at dir,
// as its files say, or "" when they do not. A test script of the
// repository's own is named by its path: whoever runs the command can then
// run the base's copy of it, which a bead cannot have changed.
func guessTest(dir string) string {
	has := func(name string) bool {
		info, err := os.Stat(filepath.Join(dir, name))
		return err == nil && !info.IsDir()
	}
	for _, script := range []string{".yardr/test", "scripts/test"} {
		if has(script) {
			return script
		}
	}
	switch {
	case has("go.mod"):
		return "go vet ./... && go test ./..."
	case packageTest(filepath.Join(dir, "package.json")):
		switch {
		case has("pnpm-lock.yaml"):
			return "pnpm test"
		case has("yarn.lock"):
			return "yarn test"
		}
		return "npm test"
	case has("Cargo.toml"):
		return "cargo test"
	case makeTest(dir):
		return "make test"
	}
	return ""
}

// makeTest reports whether the Makefile in dir has a target called test.
func makeTest(dir string) bool {
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if target, _, ok := strings.Cut(line, ":"); ok && slices.Contains(strings.Fields(target), "test") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, ".PHONY") {
				return true
			}
		}
		return false
	}
	return false
}

// packageTest reports whether a package.json has a test script of its own,
// not npm init's placeholder that only fails.
func packageTest(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var p struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &p) != nil {
		return false
	}
	t := strings.TrimSpace(p.Scripts["test"])
	return t != "" && !strings.Contains(t, "no test specified")
}

// writeNew creates path with body, and reports false, writing nothing, when
// it is already there.
func writeNew(path, body string, mode fs.FileMode) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	_, err = f.WriteString(body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}
