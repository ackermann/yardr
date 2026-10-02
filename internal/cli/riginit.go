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

// rigInitResult is what rig init wrote into a rig's checkout.
type rigInitResult struct {
	Rig      string   `json:"rig"`
	Path     string   `json:"path"`
	Base     string   `json:"base"`
	Operator string   `json:"operator"`
	Gate     string   `json:"gate"`    // the guessed check command
	Written  []string `json:"written"` // relative to Path
	Kept     []string `json:"kept"`    // already there, left as they were
}

func (a *app) rigInitCmd() *cobra.Command {
	var operator string
	cmd := &cobra.Command{
		Use:   "init <rig> [--operator <name>]",
		Short: "Seed a rig's repo with starter roles, a gate and a flow under .yardr/",
		Long: `Write a rig's starting configuration into its checkout:

  .yardr/roles/{mayor,builder,reviewer,merger}.md   the generic roles, naming --operator
  ` + rigcheck.RepoPath + `                                       the merge gate, guessed from the repo
  .yardr/flows/default.flow                         backlog, new, review, approved, merged, decide

--operator defaults to the yard's operator setting (yardr init stores it). The
gate is a guess: go.mod runs go vet and go test, a package.json with a test
script runs it, anything else passes with a TODO. Read it before relying on it.

A file already there is never overwritten. Nothing is committed: yardr reads
roles, the gate and flows from the rig's base branch, so commit the files
there before they take effect.`,
		Example: `  yardr rig init webapp
  yardr rig init webapp --operator Jo`,
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
			res, err := rigInit(*rig, operator)
			if err != nil {
				return err
			}
			return a.out(res, res.print)
		},
	}
	cmd.Flags().StringVar(&operator, "operator", "", "who the roles name (default: the yard's operator setting, else $USER)")
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

// rigInit writes the starter files into rig's checkout, keeping any already
// there.
func rigInit(rig store.Rig, operator string) (*rigInitResult, error) {
	res := &rigInitResult{Rig: rig.Name, Path: rig.Path, Base: rig.Base, Operator: operator}
	written, kept, err := installRoles(filepath.Join(rig.Path, ".yardr", "roles"), operator)
	if err != nil {
		return nil, err
	}
	res.Written, res.Kept = relAll(rig.Path, written), relAll(rig.Path, kept)
	gate, script := guessGate(rig)
	res.Gate = gate
	for _, f := range []struct {
		path string
		body string
		mode fs.FileMode
	}{
		{rigcheck.RepoPath, script, 0o755},
		{filepath.Join(".yardr", "flows", "default.flow"), starterFlow, 0o644},
	} {
		ok, err := writeNew(filepath.Join(rig.Path, f.path), f.body, f.mode)
		if err != nil {
			return nil, err
		}
		if ok {
			res.Written = append(res.Written, f.path)
		} else {
			res.Kept = append(res.Kept, f.path)
		}
	}
	return res, nil
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
	fmt.Printf("\nThe roles name %s.", r.Operator)
	if r.Gate != "" {
		fmt.Printf(" The gate runs %s (a guess from the repo: check it).\n", r.Gate)
	} else {
		fmt.Printf(" The gate passes everything until its TODO is filled in.\n")
	}
	fmt.Printf("\nNothing is committed. yardr reads roles, the gate and the flow from %s,\n"+
		"so the operator (or the mayor, with their OK) commits them there:\n\n"+
		"  git -C %s add .yardr && git -C %s commit -m 'yardr: starter roles, gate and flow'\n", r.Base, r.Path, r.Path)
}
