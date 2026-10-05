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

	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/roles"
)

// starterFlow is the flow rig init seeds: the stages the mayor's setup
// routes (new, review, approved), with backlog before them and decide for
// what a person must answer. A merge-local refinery whose rebase conflicts
// reports failed in a flow without a merge stage, so approved needs no more.
const starterFlow = `# The rig's flow, written by yardr rig init. yardr reads it from the base
# branch; see docs/flows.md.
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes | decide on question
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
`

// dirFlow is the flow rig init seeds in a dir rig: nothing merges there,
// so a reviewed bead is done, and done closes it.
const dirFlow = `# The rig's flow, written by yardr rig init for a directory rig. yardr reads
# it from the directory as it is; see docs/dir-rigs.md.
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> done on done | new on changes | decide on question
decide -> backlog | new
done terminal
`

// dirEpicFlow governs a dir rig's epics. They only group their children,
// which work in the same directory: there is no epic branch to integrate,
// so once the children are closed the epic is reviewed as a whole. Its open
// stage goes to the rig's reviewers by a route the operator adds.
const dirEpicFlow = `# The flow of the rig's epics, written by yardr rig init for a directory rig.
# An epic waits in open while its children are open (each child blocks it);
# then a route for open (--type epic) hands it to the rig's reviewers, who
# look at what the children produced as a whole. See docs/dir-rigs.md.
backlog -> open
open -> review on done | backlog
review -> done on done | open on changes | decide on question
decide -> open | backlog
done terminal
`

// starterCommit is the message of the commit that puts rig init's files on
// the base.
const starterCommit = "yardr: starter roles, gate, flow and stage files"

// commitCommand is the command line that commits rig init's files in the
// checkout at path, and nothing else staged there.
func commitCommand(path string) string {
	return fmt.Sprintf("git -C %s add .yardr && git -C %s commit -m %s -- .yardr", shellQuote(path), shellQuote(path), shellQuote(starterCommit))
}

// rigInitResult is what rig init wrote into a rig's checkout.
type rigInitResult struct {
	Rig      string   `json:"rig"`
	Path     string   `json:"path"`
	Kind     string   `json:"kind"`
	Base     string   `json:"base"`
	Operator string   `json:"operator"`
	Mayor    string   `json:"mayor"`   // the resident the roles tell agents to mail
	Gate     string   `json:"gate"`    // the guessed check command
	Written  []string `json:"written"` // relative to Path
	Kept     []string `json:"kept"`    // already there, left as they were
}

func (a *app) rigInitCmd() *cobra.Command {
	var operator, mayor string
	var withMayor bool
	cmd := &cobra.Command{
		Use:   "init <rig> [--operator <name>] [--mayor <name>] [--with-mayor]",
		Short: "Seed a rig's repo with starter roles, a gate, a flow and its stage files under .yardr/",
		Long: `Write a rig's starting configuration into its checkout:

  .yardr/roles/{mayor,builder,reviewer,merger}.md   the generic roles, naming --operator and --mayor
  ` + rigcheck.RepoPath + `                                       the merge gate, guessed from the repo
  .yardr/flows/default.flow                         backlog, new, review, approved, merged, decide
  .yardr/flows/default/<stage>.md                   a stage file for each stage but merged

A stage file says what its stage is for, what must be true to leave it and
when each outcome applies (docs/flows.md); the roles say how a group works.

--operator defaults to the yard's operator setting and --mayor to its mayor
setting (yardr init stores both), else to the yard's one resident. The
gate is a guess: go.mod runs go vet and go test, a package.json with a test
script runs it, anything else passes with a TODO. Read it before relying on it.

A file already there is never overwritten, and a flow already there gets no
stage files: they are written for the flows rig init writes, whose stages
they name. Nothing is committed: yardr reads
roles, the gate and flows from the rig's base branch, so commit the files
there before they take effect.

A dir rig gets less, since nothing merges there (see docs/dir-rigs.md):

  .yardr/roles/{builder,reviewer}.md                plus mayor.md with --with-mayor
  .yardr/flows/default.flow                         backlog, new, review, done, decide
  .yardr/flows/type/epic.flow                       the epics': backlog, open, review, done, decide
  .yardr/flows/{default,type/epic}/<stage>.md       a stage file for each stage but done

and no check: a dir rig has no gate unless one adds .yardr/check. The files
apply at once, as yardr reads a dir rig's .yardr/ from the directory.`,
		Example: `  yardr rig init webapp
  yardr rig init webapp --operator Jo --mayor boss
  yardr rig init invoices          # a dir rig
  yardr rig init invoices --with-mayor`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rig, err := st.GetRig(c.Context(), args[0])
			if err != nil {
				return err
			}
			if operator == "" {
				operator = yardOperator(c.Context(), st)
			}
			if mayor == "" {
				mayor = yardMayor(c.Context(), st)
			}
			res, err := rigInit(*rig, operator, mayor, withMayor)
			if err != nil {
				return err
			}
			return a.out(res, res.print)
		},
	}
	cmd.Flags().StringVar(&operator, "operator", "", "who the roles name (default: the yard's operator setting, else your login name)")
	cmd.Flags().StringVar(&mayor, "mayor", "", "the mayor the roles tell agents to mail (default: the yard's mayor setting, else its one resident, else mayor)")
	cmd.Flags().BoolVar(&withMayor, "with-mayor", false, "dir rigs: write mayor.md too (a git rig always gets it)")
	return cmd
}

// yardOperator is the yard's operator setting, or who it defaults to.
func yardOperator(ctx context.Context, st store.API) string {
	all, err := st.Settings(ctx)
	if err != nil {
		return defaultOperator()
	}
	return or(all["operator"], defaultOperator())
}

// yardMayor is the mayor resident the roles tell agents to mail: the yard's
// mayor setting, else the one resident of a yard from before the setting,
// else init's default name.
func yardMayor(ctx context.Context, st store.API) string {
	if all, err := st.Settings(ctx); err == nil && all["mayor"] != "" {
		return all["mayor"]
	}
	if rs, err := st.Residents(ctx); err == nil && len(rs) == 1 {
		return rs[0].Name
	}
	return "mayor"
}

// rigInit writes the starter files into rig's checkout, keeping any already
// there. A dir rig gets no merger, the mayor's role only when asked
// (withMayor), no check and the dir flows.
func rigInit(rig store.Rig, operator, mayor string, withMayor bool) (*rigInitResult, error) {
	res := &rigInitResult{Rig: rig.Name, Path: rig.Path, Kind: rig.Kind, Base: rig.Base, Operator: operator, Mayor: mayor}
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
	if rig.IsDir() {
		only = []string{"builder.md", "reviewer.md"}
		if withMayor {
			only = append(only, "mayor.md")
		}
		files = []file{
			{filepath.Join(".yardr", "flows", "default.flow"), dirFlow, 0o644, "default"},
			{filepath.Join(".yardr", "flows", "type", "epic.flow"), dirEpicFlow, 0o644, "type/epic"},
		}
	} else {
		gate, script := guessGate(rig)
		res.Gate = gate
		files = []file{
			{rigcheck.RepoPath, script, 0o755, ""},
			{filepath.Join(".yardr", "flows", "default.flow"), starterFlow, 0o644, "default"},
		}
	}
	// refresh is false: a rig's roles are the checkout's own, never rewritten.
	inst, err := installRoles(filepath.Join(rig.Path, ".yardr", "roles"), operator, mayor, false, only...)
	if err != nil {
		return nil, err
	}
	res.Written, res.Kept = relAll(rig.Path, inst.Written), relAll(rig.Path, inst.Kept)
	for _, f := range files {
		ok, err := writeNew(filepath.Join(rig.Path, f.path), f.body, f.mode)
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
		// Only beside a flow written now: a flow already there is the rig's
		// own, and a file for a stage it lacks would fail the rig's check.
		stages, err := starterStageFiles(rig, f.flow, f.body, operator, mayor)
		if err != nil {
			return nil, err
		}
		for _, sf := range stages {
			ok, err := writeNew(filepath.Join(rig.Path, sf.path), sf.body, 0o644)
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

// starterStage is one starter stage file: where it goes in the rig, and its
// text with the names filled in.
type starterStage struct {
	path string
	body string
}

// starterStageFiles are the stage files rig init writes beside the starter
// flow named flow, whose text is body: one for every stage that is not
// terminal, in the order the flow first names them. A stage without a template is a bug in this
// build, not the rig's, and an error.
func starterStageFiles(rig store.Rig, flow, body, operator, mayor string) ([]starterStage, error) {
	f, err := store.ParseFlow(body)
	if err != nil {
		return nil, fmt.Errorf("starter flow %s: %w", flow, err)
	}
	kind := "git"
	if rig.IsDir() {
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
		out = append(out, starterStage{filepath.FromSlash(store.StageFilePath(flow, s)), string(fillRole(tmpl, operator, mayor))})
	}
	return out, nil
}

// guessGate picks a check command from what the repo holds, and the script
// that runs it.
func guessGate(rig store.Rig) (gate, script string) {
	head := fmt.Sprintf(`#!/bin/sh
# The merge gate of rig %s: yardr rig check runs it in a bead's worktree, and
# a bead lands only when it exits 0. yardr reads this file from the base
# branch (%s), so an edit here counts once it has landed there.
set -e
`, rig.Name, rig.Base)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(rig.Path, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		gate = "go vet ./... && go test ./..."
	case packageTest(filepath.Join(rig.Path, "package.json")):
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

func (r *rigInitResult) print() {
	if len(r.Written) == 0 {
		fmt.Printf("rig %s: nothing written, every file is already in %s\n", r.Rig, r.Path)
		return
	}
	fmt.Printf("rig %s: wrote into %s\n\n", r.Rig, r.Path)
	for _, p := range r.Written {
		fmt.Printf("  %s\n", p)
	}
	for _, p := range r.Kept {
		fmt.Printf("  %s (already there, kept)\n", p)
	}
	if r.Kind == store.RigDir {
		r.printDir()
		return
	}
	fmt.Printf("\nThe roles name %s, and %s as the mayor.", r.Operator, r.Mayor)
	if r.Gate != "" {
		fmt.Printf(" The gate runs %s (a guess from the repo: check it).\n", r.Gate)
	} else {
		fmt.Printf(" The gate passes everything until its TODO is filled in.\n")
	}
	fmt.Printf("\nNothing is committed. yardr reads roles, the gate, the flow and its stage\n"+
		"files from %s,\n"+
		"so the operator (or the mayor, with their OK) commits them there:\n\n"+
		"  %s\n", r.Base, commitCommand(r.Path))
}

func (r *rigInitResult) printDir() {
	fmt.Printf("\nThe roles name %s, and %s as the mayor. A dir rig has no gate unless one adds %s:\n"+
		"until then yardr rig check passes with \"no gate\", as nothing merges.\n", r.Operator, r.Mayor, rigcheck.RepoPath)
	fmt.Printf("\nThey apply as they are: yardr reads a dir rig's .yardr/ from %s.\n"+
		"Route new to the builders and review to the reviewers (or a manual group,\n"+
		"for yardr review); for epics, route open --type epic to the reviewers.\n"+
		"See docs/dir-rigs.md.\n", r.Path)
}
