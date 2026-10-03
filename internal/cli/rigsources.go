package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// Where an object of a rig comes from (rigSource.Source).
const (
	sourceRig   = "rig"   // the rig's repository, or a dir rig's directory
	sourcePack  = "pack"  // a pack's flow in the store, or its installed copy under the home
	sourceStore = "store" // a named flow in the store that no pack of the rig brought
	sourceHome  = "home"  // a file under the yard's home
	sourceFile  = "file"  // a file elsewhere, named by its absolute path
	sourceNone  = "none"  // nothing: no flow governs, the stage has no file, the group no prompt
)

// rigSource is one flow, stage file or role of a rig and where it comes
// from. Nothing here is decided by this view: the store's flow pointers and
// each group's and resident's prompt decide, and the view reads them the
// way a brief does.
type rigSource struct {
	Kind string `json:"kind"` // flow, stage or role
	// Name is the flow's place in the rig (default, or type/<type>), the
	// stage, or the group or resident.
	Name string `json:"name"`
	// Flow is the governing flow, of a flow and of each of its stages; Type
	// the bead type it governs, "" for the rig's own.
	Flow   string `json:"flow,omitempty"`
	Type   string `json:"type,omitempty"`
	Source string `json:"source"`
	Pack   string `json:"pack,omitempty"`
	// Path is the file: relative to the rig's root for a file of the rig,
	// else absolute. A flow in the store has none.
	Path string `json:"path,omitempty"`
	// Commit is the base commit a file of the rig was read at.
	Commit string `json:"commit,omitempty"`
	// Shadows are the rig's files that nothing reads while the object comes
	// from where it does.
	Shadows []string `json:"shadows,omitempty"`
	// Available says what else the rig could take the object from.
	Available []string `json:"available,omitempty"`
	// Error says why the source could not be read: a prompt that names a
	// file nobody has.
	Error string `json:"error,omitempty"`
}

func (a *app) rigSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sources <rig>",
		Short: "Show where each flow, stage file and role of a rig comes from: its repository, a pack or the yard",
		Long: `Show, one line per object, where each flow, stage file and role of a rig
comes from, and which file of the rig's repository it shadows:

  flow   the flows governing the rig's beads, the rig's own (default) first,
         then one per type (type/<type>), as yardr flow show <rig> resolves them
  stage  each non-terminal stage of those flows: its stage file
  role   the prompt of each agent group a route of the rig leads to, and of
         each resident a pack imported into the rig names

The source is one of: rig (` + store.RepoFlowDir + `, .yardr/roles on the base branch, with
the commit read), pack <name> (the pack's flow in the store, a stage file of
that flow or the pack's installed role, under the yard's home), store (a
named flow no pack of the rig brought), home (a file under the yard's home),
file (any other path) and none.

"shadows <file>" names a file of the rig's repository that nothing reads
while the line is as it is: the flow file behind a store pointer, the stage
file of a repository flow that does not govern, the rig's own role behind a
group prompted from elsewhere. yardr rig check names the same files in notes.

This view changes nothing. yardr rig flow sets and clears the flow pointer,
yardr pack apply --rig-roles or --pack-roles chooses the roles.`,
		Example: `  yardr rig sources yard
  yardr rig sources yard --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			lines, err := rigSources(c.Context(), st, args[0], a.home())
			if err != nil {
				return err
			}
			return a.out(lines, func() {
				qualify := false
				for _, l := range lines {
					qualify = qualify || (l.Kind == "stage" && l.Type != "")
				}
				rows := [][]string{}
				for _, l := range lines {
					name := l.Name
					if l.Kind == "stage" && qualify {
						// A stage name alone does not say which flow's it is.
						name = or(l.Type, "default") + ":" + name
					}
					row := []string{l.Kind, name, l.where()}
					if aside := l.aside(); aside != "" {
						row = append(row, aside)
					}
					rows = append(rows, row)
				}
				table("KIND\tNAME\tSOURCE", rows)
			})
		},
	}
}

// where says the line's source the way the listing prints it.
func (l rigSource) where() string {
	at := tilde(l.Path)
	if l.Commit != "" {
		at += "@" + l.Commit[:min(len(l.Commit), 7)]
	}
	if l.Kind == "flow" && l.Path == "" && l.Flow != "" {
		at = "store " + l.Flow
	}
	out := l.Source
	if l.Pack != "" {
		out += " " + l.Pack
	}
	if at != "" {
		out += " (" + at + ")"
	}
	if l.Error != "" {
		out += ": " + l.Error
	}
	return out
}

// aside says what the line shadows and what else is available.
func (l rigSource) aside() string {
	var out []string
	if len(l.Shadows) > 0 {
		out = append(out, "shadows "+strings.Join(l.Shadows, ", "))
	}
	return strings.Join(append(out, l.Available...), "; ")
}

// tilde writes a path under the user's home directory with ~, as the docs
// do (~/.yardr/roles/mayor.md).
func tilde(p string) string {
	if dir, err := os.UserHomeDir(); err == nil && dir != "" && filepath.IsAbs(p) {
		if rel, err := filepath.Rel(dir, p); err == nil && filepath.IsLocal(rel) {
			return filepath.Join("~", rel)
		}
	}
	return p
}

// rigSources lists where each flow, stage file and role of rig comes from.
// home is the yard's home.
func rigSources(ctx context.Context, st store.API, rigName, home string) ([]rigSource, error) {
	rig, err := st.GetRig(ctx, rigName)
	if err != nil {
		return nil, err
	}
	all, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	var packs []store.PackImport
	for _, p := range all {
		if p.Rig == rig.Name {
			packs = append(packs, p)
		}
	}
	b := &sourcer{ctx: ctx, st: st, rig: rig, home: home, packs: packs}
	for _, step := range []func() error{b.flows, b.groups, b.residents} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.lines, nil
}

type sourcer struct {
	ctx   context.Context
	st    store.API
	rig   *store.Rig
	home  string
	packs []store.PackImport // those imported into the rig
	lines []rigSource
	// staged are the groups the stages of the governing flows lead to, in
	// the flows' order.
	staged []string
}

// flowPack names the pack of the rig that brought the store flow name.
func (b *sourcer) flowPack(name string) string {
	for _, p := range b.packs {
		if slices.ContainsFunc(p.Objects, func(o store.PackObject) bool { return o.Kind == "flow" && o.Name == name }) {
			return p.Pack
		}
	}
	return ""
}

// packFlows says which flows the packs imported into the rig left in the
// store for it to follow: what the rig could point at while its repository,
// or nothing, governs.
func (b *sourcer) packFlows() []string {
	var out []string
	for _, p := range b.packs {
		if p.Flow == "" {
			continue
		}
		if _, err := b.st.GetFlow(b.ctx, pack.FlowName(p.Pack, p.Flow)); err == nil {
			out = append(out, fmt.Sprintf("pack %s has flow %s", p.Pack, pack.FlowName(p.Pack, p.Flow)))
		}
	}
	return out
}

// flows adds a line per flow governing the rig and per non-terminal stage of
// each. A flow the store points at stands in front of the repository's flow
// files and of the stage files beside them.
func (b *sourcer) flows() error {
	governing, err := b.st.RigFlows(b.ctx, b.rig.Name)
	if err != nil {
		return err
	}
	repo, err := b.st.RepoFlows(b.ctx, b.rig.Name)
	if err != nil {
		return err
	}
	routes, err := b.st.Routes(b.ctx)
	if err != nil {
		return err
	}
	dispatch.SortRoutes(routes)
	if !slices.ContainsFunc(governing, func(g store.RigFlow) bool { return g.Type == "" }) {
		b.lines = append(b.lines, rigSource{Kind: "flow", Name: "default", Source: sourceNone, Available: b.packFlows()})
	}
	// The types whose pointer in the store answers for their own file.
	pointed := map[string]bool{}
	for _, g := range governing {
		pointed[g.Type] = g.Source.Kind == "store"
	}
	for _, g := range governing {
		f := g.Flow
		place := "default"
		if g.Type != "" {
			place = "type/" + g.Type
		}
		fl := rigSource{Kind: "flow", Name: place, Flow: f.Name, Type: g.Type}
		switch {
		case !g.Source.InStore():
			fl.Source, fl.Path, fl.Commit = sourceRig, g.Source.Path, g.Source.Commit
		case b.flowPack(f.Name) != "":
			fl.Source, fl.Pack = sourcePack, b.flowPack(f.Name)
		default:
			fl.Source = sourceStore
		}
		// The repository flows this one stands in front of, by name: its
		// own place, and under the rig's pointer every type's file too,
		// since the store wins as a whole.
		var behind []string
		if g.Source.Kind == "store" {
			behind = append(behind, place)
			for _, rf := range repo.Flows {
				if g.Type == "" && rf.Type != "" && !pointed[rf.Type] {
					behind = append(behind, "type/"+rf.Type)
				}
			}
		} else if g.Type == "" {
			fl.Available = b.packFlows()
		}
		// A pointer may name a flow of the repository: that file governs.
		shadowed := func(path string) bool { return path != g.Source.Path }
		for _, name := range behind {
			if path := store.RepoFlowDir + "/" + name + ".flow"; shadowed(path) && slices.ContainsFunc(repo.Flows, func(rf store.RepoFlow) bool { return rf.Path == path }) {
				fl.Shadows = append(fl.Shadows, path)
			}
		}
		files := g.StageFiles
		if g.Source.InStore() {
			if files, _, err = store.HomeStageFiles(b.home, f); err != nil {
				return err
			}
		}
		claimed := map[string]bool{}
		var stages []rigSource
		for _, s := range f.Stages {
			probe := store.Bead{Stage: s, Type: or(g.Type, "task"), Rig: &b.rig.Name}
			if r := dispatch.Match(routes, probe); r != nil && !slices.Contains(b.staged, r.Group) {
				b.staged = append(b.staged, r.Group)
			}
			if f.IsTerminal(s) {
				continue
			}
			sl := rigSource{Kind: "stage", Name: s, Flow: f.Name, Type: g.Type, Source: sourceNone}
			if path := files[s]; path != "" {
				sl.Source, sl.Pack, sl.Path = fl.Source, fl.Pack, path
				switch {
				case !g.Source.InStore():
					sl.Commit = g.Source.Commit
				case fl.Source == sourceStore:
					// A store flow's stage files are plain files in the home.
					sl.Source = sourceHome
				}
			}
			// The same stage of the repository flow this one stands in front of.
			if len(behind) > 0 {
				if sf := repo.StageFile(behind[0], s); sf != nil && shadowed(store.RepoFlowDir+"/"+behind[0]+".flow") {
					sl.Shadows = append(sl.Shadows, sf.Path)
					claimed[sf.Path] = true
				}
			}
			stages = append(stages, sl)
		}
		// Their other stage files are read by nothing either; the flow's
		// line names them, so no shadowed file goes unsaid.
		for _, name := range behind {
			if !shadowed(store.RepoFlowDir + "/" + name + ".flow") {
				continue
			}
			for _, sf := range repo.StageFiles {
				if strings.HasPrefix(sf.Path, store.RepoFlowDir+"/"+name+"/") && strings.TrimSpace(sf.Text) != "" && !claimed[sf.Path] {
					fl.Shadows = append(fl.Shadows, sf.Path)
					claimed[sf.Path] = true
				}
			}
		}
		b.lines = append(append(b.lines, fl), stages...)
	}
	// A route of the rig no stage of its flows uses still leads to a group.
	var rest []string
	for _, r := range routes {
		if r.Rig != nil && *r.Rig == b.rig.Name && !slices.Contains(b.staged, r.Group) && !slices.Contains(rest, r.Group) {
			rest = append(rest, r.Group)
		}
	}
	slices.Sort(rest)
	b.staged = append(b.staged, rest...)
	return nil
}

// groups adds a line per agent group a route of the rig leads to: where a
// brief for a bead of the rig reads the group's prompt. Script and people
// groups have no role.
func (b *sourcer) groups() error {
	groups, err := b.st.Groups(b.ctx)
	if err != nil {
		return err
	}
	for _, name := range b.staged {
		i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == name })
		if i < 0 {
			continue
		}
		prompt, _ := groups[i].Config["prompt"].(string)
		if prompt == "" && groups[i].Runner != "herdr" {
			continue
		}
		l := rigSource{Kind: "role", Name: name, Source: sourceNone}
		if prompt != "" {
			src, err := runner.LocatePrompt(prompt, b.rig, b.home)
			if err != nil {
				l.Error = err.Error()
			} else if err := b.role(&l, src); err != nil {
				return err
			}
		}
		b.lines = append(b.lines, l)
	}
	return nil
}

// residents adds a line per resident a pack imported into the rig names. A
// resident is the yard's, not the rig's: its prompt is a path, read as it
// is, or from the resident's directory when it is relative.
func (b *sourcer) residents() error {
	var names []string
	for _, p := range b.packs {
		for _, name := range p.Residents {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	for _, name := range names {
		r, err := b.st.GetResident(b.ctx, name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return err
		}
		l := rigSource{Kind: "role", Name: name, Source: sourceNone}
		if path, _ := r.Config["prompt"].(string); path != "" {
			if !filepath.IsAbs(path) && r.Dir != "" {
				path = filepath.Join(r.Dir, path)
			}
			if _, err := os.Stat(path); err != nil {
				l.Error = err.Error()
			}
			if err := b.role(&l, runner.PromptSource{Kind: runner.PromptFile, Path: path}); err != nil {
				return err
			}
		}
		b.lines = append(b.lines, l)
	}
	return nil
}

// role fills in a role's line from where its prompt was found. A prompt
// from outside the rig, named like a role (roles/<name>.md), shadows the
// rig's own .yardr/roles/<name>.md; a rig's role that a pack of the rig has
// a copy of could be the pack's instead.
func (b *sourcer) role(l *rigSource, src runner.PromptSource) error {
	l.Path, l.Commit = src.Path, src.Commit
	switch src.Kind {
	case runner.PromptRig:
		l.Source = sourceRig
		if name, ok := strings.CutPrefix(filepath.ToSlash(src.Path), ".yardr/roles/"); ok {
			for _, p := range b.packs {
				if _, err := os.Stat(filepath.Join(b.home, "packs", p.Pack, "roles", name)); err == nil {
					l.Available = append(l.Available, fmt.Sprintf("pack %s has a copy", p.Pack))
				}
			}
		}
		return nil
	case runner.PromptHome:
		// Found in the home because the rig has no role of the name.
		l.Source = sourceHome
		return nil
	}
	l.Source = sourceFile
	if rel, err := filepath.Rel(b.home, src.Path); b.home != "" && err == nil && filepath.IsLocal(rel) {
		l.Source = sourceHome
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) > 2 && parts[0] == "packs" {
			l.Source, l.Pack = sourcePack, parts[1]
		}
	}
	dir, name := filepath.Split(src.Path)
	if filepath.Base(dir) != "roles" || filepath.Ext(name) != ".md" {
		return nil
	}
	has, err := runner.RigRole(b.rig, name)
	if err != nil {
		return fmt.Errorf("rig %s: looking for its own role %s: %w", b.rig.Name, name, err)
	}
	if has {
		l.Shadows = append(l.Shadows, ".yardr/roles/"+name)
	}
	return nil
}

// shadowNotes says, per file of the rig's repository that nothing reads,
// what stands in front of it, for yardr rig check.
func shadowNotes(rig string, lines []rigSource) []string {
	var out []string
	for _, l := range lines {
		by := l.where()
		switch {
		case l.Kind == "flow":
			by = "flow " + l.Flow + " (" + l.Source + strings.TrimSuffix(" "+l.Pack, " ") + ")"
		case l.Kind == "stage" && l.Source == sourceNone:
			by = "flow " + l.Flow + ", which has no stage file for " + l.Name
		case l.Kind == "stage":
			by = "the stage file of " + l.Name + " in flow " + l.Flow + ", " + l.where()
		case l.Kind == "role":
			by = "the prompt of " + l.Name + ", " + l.where()
		}
		for _, s := range l.Shadows {
			out = append(out, fmt.Sprintf("%s is shadowed by %s (yardr rig sources %s)", s, by, rig))
		}
	}
	return out
}
