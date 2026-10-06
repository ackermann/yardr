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

// starterCommit is the message of the commit that puts depot init's files on
// the base.
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
		Use:   "init <depot> [--operator <name>] [--yardmaster <name>] [--with-yardmaster] | init <depot> --pack <pack>",
		Short: "Seed a depot's repo with starter roles, a gate, a flow and its stage files under .yardr/, or set it up from a pack",
		Long: `Write a depot's starting configuration into its checkout:

  .yardr/roles/{yardmaster,builder,reviewer,merger}.md   the generic roles, naming --operator and --yardmaster
  ` + rigcheck.RepoPath + `                                       the merge gate, guessed from the repo
  .yardr/flows/default.flow                         backlog, new, review, approved, merged, decide
  .yardr/flows/default/<stage>.md                   a stage file for each stage but merged

A stage file says what its stage is for, what must be true to leave it and
when each outcome applies (docs/flows.md); the roles say how a group works.

--operator defaults to the yard's operator setting and --yardmaster to its
yardmaster setting (yardr init stores both), else to the yard's one crew member.
The gate is a guess: go.mod runs go vet and go test, a package.json with a
test script runs it, anything else passes with a TODO. Read it before relying
on it.

A file already there is never overwritten, and a flow already there gets no
stage files: they are written for the flows depot init writes, whose stages
they name. Nothing is committed: yardr reads
roles, the gate and flows from the depot's base branch, so commit the files
there before they take effect.

A dir depot gets less, since nothing merges there (see docs/dir-depots.md):

  .yardr/roles/{builder,reviewer}.md                plus yardmaster.md with --with-yardmaster
  .yardr/flows/default.flow                         backlog, new, review, done, decide
  .yardr/flows/type/train.flow                      the trains': backlog, open, review, done, decide
  .yardr/flows/{default,type/train}/<stage>.md      a stage file for each stage but done

and no check: a dir depot has no gate unless one adds .yardr/check. The files
apply at once, as yardr reads a dir depot's .yardr/ from the directory.

With --pack the depot is set up from a pack instead, and none of the above is
written but the gate: ` + rigcheck.RepoPath + `, when the depot has none (no file there in
the checkout, no --check in the store), to be committed on the base. The
roles, the flow and its stage files are the pack's, imported as yardr pack
apply <pack> --depot <depot> imports them: under the yard's home, with the
depot's groups, routes and flow pointer in the store. <pack> is named as
there (a directory with a path separator in it, else the name of a pack the
yard has imported or yardr carries: starter, and dir for a dir depot), the
plan is printed first, and what the import asks is answered by the same
flags: --bind, --members, --crew, --flow, --open-threads, --depot-roles. A
role the depot has under .yardr/roles/ is named in the plan as shadowed.
Nothing is written, gate included, before a yes in the terminal or --yes. The operator and the
yardmaster the pack's roles name are the yard's operator setting and the
crew member --crew gives, so --operator, --yardmaster and --with-yardmaster
do not go with --pack. Applying the pack again later is yardr pack apply.

A dir depot takes --pack dir and gets no gate: nothing is written into the
directory. Of the two packs yardr carries each is for one kind of depot:
starter is refused on a dir depot, whose beads have no base branch to land
on, and dir on a git depot. See docs/packs.md and docs/dir-depots.md.`,
		Example: `  yardr depot init webapp
  yardr depot init webapp --operator Jo --yardmaster boss
  yardr depot init invoices          # a dir depot
  yardr depot init invoices --with-yardmaster
  yardr depot init webapp --pack starter
  yardr depot init webapp --pack starter --yes --crew yardmaster=boss \
    --bind builders=claude:opus --bind reviewers=codex --members backlog= --members decisions=jo
  yardr depot init invoices --pack dir --yes --bind builders=claude --bind reviewers=claude`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := packOrSeed(c, packArg != ""); err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			depot, err := st.GetDepot(c.Context(), args[0])
			if err != nil {
				return err
			}
			if packArg != "" {
				return a.depotInitPack(c.Context(), *depot, packArg, &imp)
			}
			if operator == "" {
				operator = yardOperator(c.Context(), st)
			}
			if yardmaster == "" {
				yardmaster = yardmasterOf(c.Context(), st)
			}
			res, err := depotInit(*depot, operator, yardmaster, withYardmaster)
			if err != nil {
				return err
			}
			return a.out(res, res.print)
		},
	}
	cmd.Flags().StringVar(&operator, "operator", "", "who the roles name (default: the yard's operator setting, else your login name)")
	cmd.Flags().StringVar(&yardmaster, "yardmaster", "", "the yardmaster the roles tell agents to mail (default: the yard's yardmaster setting, else its one crew member, else yardmaster)")
	cmd.Flags().BoolVar(&withYardmaster, "with-yardmaster", false, "dir depots: write yardmaster.md too (a git depot always gets it)")
	oldFlag(cmd, "mayor", "yardmaster")
	oldFlag(cmd, "with-mayor", "with-yardmaster")
	cmd.Flags().StringVar(&packArg, "pack", "", "set the depot up from this pack instead: write the gate alone (none for a dir depot), and import the pack as yardr pack apply does (a directory, or the name of an imported or embedded pack)")
	imp.registerShape(cmd)
	imp.registerAnswers(cmd)
	return cmd
}

// seedFlags are depot init's flags for the files it writes without --pack,
// each with what says the same for a pack.
var seedFlags = [][2]string{
	{"operator", "a pack's roles name the yard's operator setting (yardr settings set operator <name>)"},
	{"yardmaster", "--crew yardmaster=<name> says which crew member takes the pack's"},
	{"with-yardmaster", "it writes the yardmaster's role into a dir depot, and with a pack the roles are the pack's"},
}

// packOrSeed refuses a flag of depot init's other way: with --pack one for
// the files it then does not write, without it one that answers an import.
func packOrSeed(c *cobra.Command, withPack bool) error {
	if withPack {
		for _, f := range seedFlags {
			if c.Flags().Changed(f[0]) {
				return fmt.Errorf("--%s does not go with --pack: %s", f[0], f[1])
			}
		}
		return nil
	}
	for _, name := range []string{"bind", "members", "crew", "flow", "open-threads", "depot-roles", "yes"} {
		if c.Flags().Changed(name) {
			return fmt.Errorf("--%s answers a pack's import: it needs --pack <pack>", name)
		}
	}
	return nil
}

// gateCommit is the message of the commit that puts the gate depot init
// --pack wrote on the base.
const gateCommit = "yardr: gate"

// gateResult is what depot init --pack did about the depot's gate.
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
// gate, as from depot init without --pack: nothing lands there for one to
// hold back, and what would check a folder of documents is no guess from
// its files.
func (a *app) depotInitPack(ctx context.Context, depot store.Depot, arg string, f *importFlags) error {
	gate := &gateResult{Depot: depot.Name, Kind: depot.Kind, Path: depot.Path, File: rigcheck.RepoPath, Base: depot.Base, Store: depot.Check}
	file := filepath.Join(depot.Path, rigcheck.RepoPath)
	var also string
	if !depot.IsDir() {
		if depot.Path == "" {
			return fmt.Errorf("depot %s has no path: depot init --pack writes %s into its checkout", depot.Name, rigcheck.RepoPath)
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
	// The one file by name: a depot may hold other things under .yardr/
	// that are not this command's to commit.
	fmt.Printf("Nothing is committed. yardr reads the gate from %s:\n  git -C %s add %s && git -C %s commit -m %s -- %s\n",
		g.Base, shellQuote(g.Path), g.File, shellQuote(g.Path), shellQuote(gateCommit), g.File)
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
// there. A dir depot gets no merger, the yardmaster's role only when asked
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
