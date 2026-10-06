package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
	"yardr.dev/yardr/roles"
)

// starterFlow is the flow depot init seeds: the stages the yardmaster's setup
// routes (new, review, approved), with backlog before them and decide for
// what a person must answer. A merge-local assembly whose rebase conflicts
// reports failed in a flow without a merge stage, so approved needs no more.
const starterFlow = `# The depot's flow, written by yardr depot init. yardr reads it from the base
# branch; see docs/flows.md.
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes | decide on question
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
`

// dirFlow is the flow depot init seeds in a dir depot: nothing merges there,
// so a reviewed bead is done, and done closes it.
const dirFlow = `# The depot's flow, written by yardr depot init for a directory depot. yardr reads
# it from the directory as it is; see docs/dir-depots.md.
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> done on done | new on changes | decide on question
decide -> backlog | new
done terminal
`

// dirTrainFlow governs a dir depot's trains. They only group their children,
// which work in the same directory: there is no train branch to integrate,
// so once the children are closed the train is reviewed as a whole. Its open
// stage goes to the depot's reviewers by a route the operator adds.
const dirTrainFlow = `# The flow of the depot's trains, written by yardr depot init for a directory depot.
# A train waits in open while its children are open (each child blocks it);
# then a route for open (--type train) hands it to the depot's reviewers, who
# look at what the children produced as a whole. See docs/dir-depots.md.
backlog -> open
open -> review on done | backlog
review -> done on done | open on changes | decide on question
decide -> open | backlog
done terminal
`

// starterCommit is the message of the commit that put the files depot init
// wrote into a depot on the base (depotInit).
const starterCommit = "yardr: starter roles, gate, flow and stage files"

// commitCommand is the command line that commits depot init's files in the
// checkout at path, and nothing else staged there.
func commitCommand(path string) string {
	return fmt.Sprintf("git -C %s add .yardr && git -C %s commit -m %s -- .yardr", shellQuote(path), shellQuote(path), shellQuote(starterCommit))
}

// depotInitResult is what depot init wrote into a depot's checkout.
type depotInitResult struct {
	Depot      string   `json:"depot"`
	Path       string   `json:"path"`
	Kind       string   `json:"kind"`
	Base       string   `json:"base"`
	Operator   string   `json:"operator"`
	Yardmaster string   `json:"yardmaster"` // the crew member the roles tell agents to mail
	Gate       string   `json:"gate"`       // the guessed check command
	Written    []string `json:"written"`    // relative to Path
	Kept       []string `json:"kept"`       // already there, left as they were
}

func (a *app) depotInitCmd() *cobra.Command {
	var operator, yardmaster string
	var withYardmaster bool
	var packArg string
	var imp importFlags
	cmd := &cobra.Command{
		Use:   "init <depot> [--pack <pack>]",
		Short: "Set a depot up from a pack (the yard's default unless one is named): its groups, roles, flow and routes in the yard, and the gate in its repo",
		Long: `Set a depot up from a pack: the yard's default pack, or the one --pack
names. It does two things.

It imports the pack as yardr pack apply <pack> --depot <depot> does: the
pack's roles, scripts and stage files are installed under the yard's home,
and the depot's groups, its routes and its pointer to the pack's flow go into
the store. The plan is printed first, and what the import asks is answered
in the terminal or by the same flags: --bind, --members, --crew, --flow,
--open-threads, --depot-roles. A role the depot has under .yardr/roles/ is
named in the plan as shadowed.

And it writes the gate, ` + rigcheck.RepoPath + `, into the checkout when the depot
has none (no file there, no --check in the store): a guess from the repo,
to be read before it is relied on, and committed on the base, where yardr
reads it. go.mod runs go vet and go test, a package.json with a test script
runs it, anything else passes with a TODO.

Nothing else is written into the depot: no roles, no flow, no stage files.
Those are the pack's, and they are changed in the pack and brought to the
yard by yardr pack apply. A depot that keeps a flow of its own commits it
under .yardr/flows/ on its base and says so with yardr depot source <depot>
flow depot; one that keeps a role of its own, with yardr depot source
<depot> role <group> depot (docs/configuration.md). Nothing is written, gate
included, before a yes in the terminal or --yes.

Without --pack the pack is the yard's default_pack setting (yardr settings
set default_pack <name|directory>). Unset, it is one of the two packs yardr
carries, by the depot's kind: starter for a git depot, dir for a directory
depot. <pack> is named as for yardr pack apply: a directory with a path
separator in it, else the name of a pack the yard has imported or yardr
carries. Applying the pack again later is yardr pack apply.

A dir depot gets no gate: nothing is written into the directory. Of the two
packs yardr carries each is for one kind of depot: starter is refused on a
dir depot, whose beads have no base branch to land on, and dir on a git
depot. See docs/packs.md and docs/dir-depots.md.

--operator, --yardmaster and --with-yardmaster are from when depot init
wrote role files into the depot. They are accepted and ignored, each with a
line saying what says the same now: the operator the roles name is the
yard's operator setting, the yardmaster is the crew member --crew
yardmaster=<name> gives, and a dir depot's roles are its pack's.`,
		Example: `  yardr depot init webapp
  yardr depot init webapp --yes --crew yardmaster=boss \
    --bind builders=claude:opus --bind reviewers=codex --members backlog= --members decisions=jo
  yardr depot init invoices --yes --bind builders=claude --bind reviewers=claude \
    --members backlog= --members decisions=jo          # a dir depot
  yardr depot init notes --pack ~/packs/research`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			for _, f := range seedFlags {
				if c.Flags().Changed(f[0]) {
					// To stderr: --json's output stays the import alone.
					fmt.Fprintf(stderr, "--%s is ignored: %s\n", f[0], f[1])
				}
			}
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
					fmt.Fprintf(stderr, "pack %s: the yard's %s setting\n", packArg, defaultPackSetting)
				}
			}
			return a.depotInitPack(c.Context(), *depot, packArg, &imp)
		},
	}
	cmd.Flags().StringVar(&operator, "operator", "", "ignored: "+seedFlags[0][1])
	cmd.Flags().StringVar(&yardmaster, "yardmaster", "", "ignored: "+seedFlags[1][1])
	cmd.Flags().BoolVar(&withYardmaster, "with-yardmaster", false, "ignored: "+seedFlags[2][1])
	oldFlag(cmd, "mayor", "yardmaster")
	oldFlag(cmd, "with-mayor", "with-yardmaster")
	cmd.Flags().StringVar(&packArg, "pack", "", "the pack to set the depot up from: a directory, or the name of an imported or embedded pack (default: the yard's default_pack setting, else starter, and dir for a dir depot)")
	imp.registerShape(cmd)
	imp.registerAnswers(cmd)
	return cmd
}

// seedFlags are the flags depot init had for the role files it wrote into a
// depot, each with what says the same now that the roles are a pack's. They
// are still taken, so a script written for them runs, and do nothing.
var seedFlags = [][2]string{
	{"operator", "a pack's roles name the yard's operator setting (yardr settings set operator <name>)"},
	{"yardmaster", "--crew yardmaster=<name> says which crew member takes the pack's yardmaster"},
	{"with-yardmaster", "a dir depot's roles are its pack's, installed under the yard's home, and none is written into the directory"},
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

// gateCommit is the message of the commit that puts the gate depot init
// wrote on the base.
const gateCommit = "yardr: gate"

// gateResult is what depot init did about the depot's gate.
type gateResult struct {
	Depot   string `json:"depot"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`    // the checkout, or a dir depot's directory
	File    string `json:"file"`    // relative to Path
	Written bool   `json:"written"` // false: the depot had a gate, or is a dir depot
	Command string `json:"command"` // the guess; "" for the TODO, or when nothing was written
	Base    string `json:"base"`
	Store   string `json:"store,omitempty"` // the depot's --check, which is why none was written
}

// depotInitPack sets depot up from the pack arg names: the gate guess into
// the checkout when a git depot has no gate, then the import pack apply
// makes. Nothing else goes into the checkout: the roles, the flow and its
// stage files are the pack's, under the yard's home. A dir depot gets no
// gate: nothing lands there for one to hold back, and what would check a folder of documents is no guess from
// its files.
func (a *app) depotInitPack(ctx context.Context, depot store.Depot, arg string, f *importFlags) error {
	gate := &gateResult{Depot: depot.Name, Kind: depot.Kind, Path: depot.Path, File: rigcheck.RepoPath, Base: depot.Base, Store: depot.Check}
	file := filepath.Join(depot.Path, rigcheck.RepoPath)
	var also string
	if !depot.IsDir() {
		if depot.Path == "" {
			return fmt.Errorf("depot %s has no path: depot init writes %s into its checkout", depot.Name, rigcheck.RepoPath)
		}
		if depot.Check == "" {
			if _, err := os.Lstat(file); errors.Is(err, fs.ErrNotExist) {
				also = file + ", the depot's gate"
			} else if err != nil {
				return err
			}
		}
	}
	f.opts.Depot = depot.Name
	imp, err := a.planImport(ctx, "depot init", arg, f, also, func(p *pack.Pack) error { return packFits(p, depot) })
	if imp == nil || err != nil {
		return err
	}
	// Before the import and after the yes: a gate that cannot be written
	// leaves the yard as it was, and nothing is written unasked.
	if also != "" {
		cmd, script := guessGate(depot)
		if gate.Written, err = writeNew(file, script, 0o755); err != nil {
			return err
		}
		if gate.Written {
			gate.Command = cmd
		}
	}
	err = imp.apply(ctx, a, map[string]any{"gate": gate}, gate.print)
	if err != nil && gate.Written {
		// A failed import may have printed nothing, and the file is there.
		err = fmt.Errorf("%w (%s was written before the import failed, and is kept)", err, file)
	}
	return err
}

// packFits refuses one of the two packs yardr carries for a depot of the
// kind the other is written for, naming the one that fits. The starter's
// flow lands a bead on a base branch through an assembly, which a directory
// has not; the dir pack's roles commit nothing and its flow ends at review,
// so in a repository nothing a bead built would ever land. A pack of
// another name is the operator's own and theirs to judge: nothing in a
// pack says which kind of depot it is for.
func packFits(p *pack.Pack, depot store.Depot) error {
	switch {
	case depot.IsDir() && p.Name == packs.Starter:
		return fmt.Errorf("depot %s is a directory depot, and pack %s is for a git depot: its flow lands a bead on a base branch through an assembly, and nothing merges in a directory. yardr's pack for a directory depot is %s: yardr depot init %s --pack %s",
			depot.Name, p.Name, packs.Dir, depot.Name, packs.Dir)
	case !depot.IsDir() && p.Name == packs.Dir:
		return fmt.Errorf("depot %s is a git depot, and pack %s is for a directory depot: its roles commit nothing and its flow closes a bead at review, so nothing built on a branch would land. yardr's pack for a git depot is %s: yardr depot init %s --pack %s",
			depot.Name, p.Name, packs.Starter, depot.Name, packs.Starter)
	}
	return nil
}

// gateCommand is the command line that commits the gate depot init
// wrote in the checkout at path. The one file by name: a depot may hold
// other things under .yardr/ that are not this command's to commit.
func gateCommand(path string) string {
	return fmt.Sprintf("git -C %s add %s && git -C %s commit -m %s -- %s",
		shellQuote(path), rigcheck.RepoPath, shellQuote(path), shellQuote(gateCommit), rigcheck.RepoPath)
}

func (g *gateResult) print() {
	fmt.Println()
	switch {
	case g.Store != "":
		fmt.Printf("gate: depot %s has a --check in the store (%s), so no %s was written\n", g.Depot, g.Store, g.File)
		return
	case g.Kind == store.DepotDir:
		// Whether it has one or not, it is the directory's own, read as it
		// is: there is nothing to guess it from and no base to commit it on.
		fmt.Printf("gate: none written. Depot %s is a directory depot: its gate is %s in %s when it has one, and without one yardr depot check passes with \"no gate\"\n", g.Depot, g.File, g.Path)
		return
	case !g.Written:
		fmt.Printf("gate: %s in %s is already there, kept\n", g.File, g.Path)
		return
	case g.Command != "":
		fmt.Printf("gate: wrote %s in %s: %s (a guess from the repo: check it)\n", g.File, g.Path, g.Command)
	default:
		fmt.Printf("gate: wrote %s in %s: it passes everything until its TODO is filled in\n", g.File, g.Path)
	}
	fmt.Printf("Nothing is committed. yardr reads the gate from %s:\n  %s\n", g.Base, gateCommand(g.Path))
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

// depotInit writes the starter files into depot's checkout, keeping any already
// there. No command calls it since depot init sets a depot up from a pack
// (yardr-1b2r): it stays, with what it writes, for the tests that build a
// depot set up the earlier way, until the copies it reads are deleted. A dir depot gets no merger, the yardmaster's role only when asked
// (withYardmaster), no check and the dir flows.
func depotInit(depot store.Depot, operator, yardmaster string, withYardmaster bool) (*depotInitResult, error) {
	res := &depotInitResult{Depot: depot.Name, Path: depot.Path, Kind: depot.Kind, Base: depot.Base, Operator: operator, Yardmaster: yardmaster}
	type file struct {
		path string
		body string
		mode fs.FileMode
		// flow, for a flow file, is the flow's name: its starter stage
		// files are written with it.
		flow string
	}
	var only []string
	var files []file
	if depot.IsDir() {
		only = []string{"builder.md", "reviewer.md"}
		if withYardmaster {
			only = append(only, "yardmaster.md")
		}
		// A depot set up before a train was called that has its trains' flow
		// in type/epic.flow, which still governs them: no second one is
		// written beside it.
		trains := filepath.Join(".yardr", "flows", "type", "train.flow")
		if _, err := os.Stat(filepath.Join(depot.Path, trains)); errors.Is(err, fs.ErrNotExist) {
			old := filepath.Join(".yardr", "flows", "type", "epic.flow")
			if _, err := os.Stat(filepath.Join(depot.Path, old)); err == nil {
				trains = old
			}
		}
		files = []file{
			{filepath.Join(".yardr", "flows", "default.flow"), dirFlow, 0o644, "default"},
			{trains, dirTrainFlow, 0o644, "type/train"},
		}
	} else {
		gate, script := guessGate(depot)
		res.Gate = gate
		files = []file{
			{rigcheck.RepoPath, script, 0o755, ""},
			{filepath.Join(".yardr", "flows", "default.flow"), starterFlow, 0o644, "default"},
		}
	}
	// refresh is false: a depot's roles are the checkout's own, never rewritten.
	inst, err := installRoles(filepath.Join(depot.Path, ".yardr", "roles"), operator, yardmaster, false, only...)
	if err != nil {
		return nil, err
	}
	res.Written, res.Kept = relAll(depot.Path, inst.Written), relAll(depot.Path, inst.Kept)
	for _, f := range files {
		ok, err := writeNew(filepath.Join(depot.Path, f.path), f.body, f.mode)
		if err != nil {
			return nil, err
		}
		if !ok {
			res.Kept = append(res.Kept, f.path)
			continue
		}
		res.Written = append(res.Written, f.path)
		if f.flow == "" {
			continue
		}
		// Only beside a flow written now: a flow already there is the depot's
		// own, and a file for a stage it lacks would fail the depot's check.
		stages, err := starterStageFiles(depot, f.flow, f.body, operator, yardmaster)
		if err != nil {
			return nil, err
		}
		for _, sf := range stages {
			ok, err := writeNew(filepath.Join(depot.Path, sf.path), sf.body, 0o644)
			if err != nil {
				return nil, err
			}
			if ok {
				res.Written = append(res.Written, sf.path)
			} else {
				res.Kept = append(res.Kept, sf.path)
			}
		}
	}
	return res, nil
}

// starterStage is one starter stage file: where it goes in the depot, and its
// text with the names filled in.
type starterStage struct {
	path string
	body string
}

// starterStageFiles are the stage files depot init writes beside the starter
// flow named flow, whose text is body: one for every stage that is not
// terminal, in the order the flow first names them. A stage without a template is a bug in this
// build, not the depot's, and an error.
func starterStageFiles(depot store.Depot, flow, body, operator, yardmaster string) ([]starterStage, error) {
	f, err := store.ParseFlow(body)
	if err != nil {
		return nil, fmt.Errorf("starter flow %s: %w", flow, err)
	}
	kind := "git"
	if depot.IsDir() {
		kind = "dir"
	}
	// A parsed flow lists a stage once for every rule naming it.
	var out []starterStage
	seen := map[string]bool{}
	for _, s := range f.Stages {
		if f.IsTerminal(s) || seen[s] {
			continue
		}
		seen[s] = true
		tmpl, err := roles.Stages.ReadFile("stages/" + kind + "/" + flow + "/" + s + ".md")
		if err != nil {
			return nil, fmt.Errorf("starter flow %s has no stage file for %s: %w", flow, s, err)
		}
		out = append(out, starterStage{filepath.FromSlash(store.StageFilePath(flow, s)), string(fillRole(tmpl, operator, yardmaster))})
	}
	return out, nil
}

// guessGate picks a check command from what the repo holds, and the script
// that runs it.
func guessGate(depot store.Depot) (gate, script string) {
	head := fmt.Sprintf(`#!/bin/sh
# The merge gate of depot %s: yardr depot check runs it in a bead's worktree, and
# a bead lands only when it exits 0. yardr reads this file from the base
# branch (%s), so an edit here counts once it has landed there.
set -e
`, depot.Name, depot.Base)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(depot.Path, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		gate = "go vet ./... && go test ./..."
	case packageTest(filepath.Join(depot.Path, "package.json")):
		gate = "npm test"
		if has("pnpm-lock.yaml") {
			gate = "pnpm test"
		} else if has("yarn.lock") {
			gate = "yarn test"
		}
	default:
		return "", head + "# TODO: build and test this repository here; until then every bead passes.\nexit 0\n"
	}
	return gate, head + gate + "\n"
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

func relAll(base string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if r, err := filepath.Rel(base, p); err == nil {
			p = r
		}
		out = append(out, p)
	}
	return out
}

func (r *depotInitResult) print() {
	if len(r.Written) == 0 {
		fmt.Printf("depot %s: nothing written, every file is already in %s\n", r.Depot, r.Path)
		return
	}
	fmt.Printf("depot %s: wrote into %s\n\n", r.Depot, r.Path)
	for _, p := range r.Written {
		fmt.Printf("  %s\n", p)
	}
	for _, p := range r.Kept {
		fmt.Printf("  %s (already there, kept)\n", p)
	}
	if r.Kind == store.DepotDir {
		r.printDir()
		return
	}
	fmt.Printf("\nThe roles name %s, and %s as the yardmaster.", r.Operator, r.Yardmaster)
	if r.Gate != "" {
		fmt.Printf(" The gate runs %s (a guess from the repo: check it).\n", r.Gate)
	} else {
		fmt.Printf(" The gate passes everything until its TODO is filled in.\n")
	}
	fmt.Printf("\nNothing is committed. yardr reads roles, the gate, the flow and its stage\n"+
		"files from %s,\n"+
		"so the operator (or the yardmaster, with their OK) commits them there:\n\n"+
		"  %s\n", r.Base, commitCommand(r.Path))
}

func (r *depotInitResult) printDir() {
	fmt.Printf("\nThe roles name %s, and %s as the yardmaster. A dir depot has no gate unless one adds %s:\n"+
		"until then yardr depot check passes with \"no gate\", as nothing merges.\n", r.Operator, r.Yardmaster, rigcheck.RepoPath)
	fmt.Printf("\nThey apply as they are: yardr reads a dir depot's .yardr/ from %s.\n"+
		"Route new to the builders and review to the reviewers (or a manual group,\n"+
		"for yardr review); for trains, route open --type train to the reviewers.\n"+
		"See docs/dir-depots.md.\n", r.Path)
}
