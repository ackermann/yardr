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

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// sourceSwitch is what yardr rig source did: the lines of yardr rig sources
// for the object afterwards, a flow's with its stages'.
type sourceSwitch struct {
	Rig string `json:"rig"`
	// Changed is false when the object already came from the chosen source.
	Changed bool        `json:"changed"`
	Lines   []rigSource `json:"lines"`
	// Notes say what the switch leaves for the operator: a pack's record
	// that will undo it, a resident to restart, a type still pointed elsewhere.
	Notes []string `json:"notes,omitempty"`
}

func (a *app) rigSourceCmd() *cobra.Command {
	var flowType string
	cmd := &cobra.Command{
		Use:   "source <rig> (role <group|resident> | flow [--type <type>]) (rig | pack)",
		Short: "Switch one role or flow of a rig between the rig's own and the pack's",
		Long: `Switch one line of yardr rig sources between the rig's repository and the
pack imported into the rig, and print the line as it is afterwards.

  role  sets the prompt of the group or resident: to roles/<name>.md, which
        yardr resolves to the rig's .yardr/roles/<name>.md on the base
        branch, or to the pack's installed copy under the yard's home. A
        resident is the yard's and reads a path as it is, so its rig's role
        is the file in the rig's checkout, by its absolute path.
  flow  clears the rig's flow pointer (rig), or points it at the pack's flow
        in the store (pack); with --type, the pointer of that type's beads.
        A flow switches whole: its stage files come with it, the rig's
        beside its flow file, the pack's from the yard's home.

Refused, changing nothing, when the chosen source does not exist: the rig
has no such file on its base, no pack is imported into the rig or none has
the role or a flow. A flow change is refused as yardr rig flow is, naming
the beads, while an open bead sits in a stage the new flow lacks.

A pack's record still says whose roles its import chose, so the next yardr
pack apply sets them again; the output says when that undoes the switch.`,
		Example: `  yardr rig source web role web-builders rig
  yardr rig source web role yardmaster pack
  yardr rig source web flow rig
  yardr rig source web flow --type decision pack`,
		Args: cobra.RangeArgs(3, 4),
		RunE: func(c *cobra.Command, args []string) error {
			rig, kind, to := args[0], args[1], args[len(args)-1]
			if (kind != "role" || len(args) != 4) && (kind != "flow" || len(args) != 3) {
				return fmt.Errorf("give role <group|resident> rig|pack, or flow [--type <type>] rig|pack")
			}
			if to != sourceRig && to != sourcePack {
				return fmt.Errorf("the source is rig or pack, not %q", to)
			}
			if kind == "role" && flowType != "" {
				return fmt.Errorf("--type is for a flow")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			var res *sourceSwitch
			if kind == "role" {
				res, err = switchRole(c.Context(), st, rig, args[2], to, a.home(), a.who())
			} else {
				res, err = switchFlow(c.Context(), st, rig, flowType, to, a.home(), a.who())
			}
			if err != nil {
				return err
			}
			return a.out(res, func() {
				sourceTable(res.Lines)
				if !res.Changed {
					fmt.Println("unchanged: it already came from there")
				} else if kind == "flow" {
					fmt.Println("the flow switched whole: its stage files are the ones above now")
				}
				for _, n := range res.Notes {
					fmt.Println("note: " + n)
				}
			})
		},
	}
	cmd.Flags().StringVarP(&flowType, "type", "t", "", "the flow of the rig's beads of this type")
	return cmd
}

// roleLine finds the role name among the lines.
func roleLine(lines []rigSource, name string) *rigSource {
	if i := slices.IndexFunc(lines, func(l rigSource) bool { return l.Kind == "role" && l.Name == name }); i >= 0 {
		return &lines[i]
	}
	return nil
}

// roleFile is the file name of the role a prompt names (builder.md):
// <name>.md alone or in a directory roles, wherever that is: the rig's, the
// home's, a pack's installed copy. "" for a prompt that is no role.
func roleFile(prompt string) string {
	dir, name := filepath.Split(filepath.Clean(prompt))
	if filepath.Ext(name) != ".md" || (dir != "" && filepath.Base(dir) != "roles") {
		return ""
	}
	return name
}

// rigBase says where a rig's own files are read, for a refusal.
func rigBase(rig *store.Rig) string {
	if rig.IsDir() {
		return "in its directory"
	}
	return "on its base " + or(rig.Base, "origin/main")
}

// rigPacks are the imports into the rig, in the store's order.
func rigPacks(ctx context.Context, st store.API, rig string) ([]store.PackImport, error) {
	all, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.PackImport
	for _, p := range all {
		if p.Rig == rig {
			out = append(out, p)
		}
	}
	return out, nil
}

// switchRole sets the prompt of the group or resident name, a role of the
// rig as yardr rig sources lists them, to the rig's own role or to the copy
// a pack imported into the rig installed. The role is the one the prompt
// names now, by its file name: nothing else says which of the rig's roles
// is the group's.
func switchRole(ctx context.Context, st store.API, rigName, name, to, home, actor string) (*sourceSwitch, error) {
	lines, err := rigSources(ctx, st, rigName, home)
	if err != nil {
		return nil, err
	}
	cur := roleLine(lines, name)
	if cur == nil {
		return nil, fmt.Errorf("rig %s has no role %s: no route of the rig leads to an agent group of that name, and no pack imported into it names such a resident (yardr rig sources %s)", rigName, name, rigName)
	}
	res := &sourceSwitch{Rig: rigName, Lines: []rigSource{*cur}}
	if cur.Source == to && cur.Error == "" {
		return res, nil
	}
	rig, err := st.GetRig(ctx, rigName)
	if err != nil {
		return nil, err
	}
	// The lines list groups before residents, so a name both have is the group's.
	group, err := st.GetGroup(ctx, name)
	var resident *store.Resident
	var config map[string]any
	switch {
	case err == nil:
		config = group.Config
	case errors.Is(err, store.ErrNotFound):
		if resident, err = st.GetResident(ctx, name); err != nil {
			return nil, err
		}
		config = resident.Config
	default:
		return nil, err
	}
	prompt, _ := config["prompt"].(string)
	file := roleFile(prompt)
	if file == "" {
		return nil, fmt.Errorf("%s has the prompt %q, which names no role (roles/<name>.md): nothing says which role of the rig or of a pack is its own; set its prompt to one first", name, prompt)
	}
	packs, err := rigPacks(ctx, st, rigName)
	if err != nil {
		return nil, err
	}
	// The packs of the rig that installed a copy of the role.
	var copies []store.PackImport
	for _, p := range packs {
		if _, err := os.Stat(filepath.Join(home, "packs", p.Pack, "roles", file)); err == nil {
			copies = append(copies, p)
		}
	}
	var next string
	if to == sourceRig {
		if resident != nil {
			// A resident's prompt is a path read as it is, never looked up
			// in a rig: the rig's role is the file in its checkout.
			next = filepath.Join(rig.Path, ".yardr", "roles", file)
			if _, err := os.Stat(next); rig.Path == "" || err != nil {
				return nil, fmt.Errorf("rig %s has no .yardr/roles/%s in its checkout %s, where a resident would read it: %s stays as it is", rigName, file, rig.Path, name)
			}
		} else {
			has, err := runner.RigRole(rig, file)
			if err != nil {
				return nil, fmt.Errorf("rig %s: looking for its own role %s: %w", rigName, file, err)
			}
			if !has {
				return nil, fmt.Errorf("rig %s has no .yardr/roles/%s %s: %s stays as it is", rigName, file, rigBase(rig), name)
			}
			next = "roles/" + file
		}
	} else {
		if len(packs) == 0 {
			return nil, fmt.Errorf("no pack is imported into rig %s (yardr pack list): there is no pack's role to switch %s to", rigName, name)
		}
		// Of several packs with a copy, the one whose import named the
		// group or resident.
		if len(copies) > 1 {
			named := slices.DeleteFunc(slices.Clone(copies), func(p store.PackImport) bool {
				return !mapsTo(p.Groups, name) && !mapsTo(p.Residents, name)
			})
			if len(named) != 1 {
				return nil, fmt.Errorf("packs %s imported into rig %s each have a role %s, and nothing says which is %s's: set its prompt yourself", packNames(copies), rigName, file, name)
			}
			copies = named
		}
		if len(copies) == 0 {
			return nil, fmt.Errorf("no pack imported into rig %s (%s) installed a role %s: %s stays as it is", rigName, packNames(packs), file, name)
		}
		next = filepath.Join(home, "packs", copies[0].Pack, "roles", file)
	}
	config["prompt"] = next
	if resident != nil {
		if _, err := st.PutResident(ctx, *resident, actor); err != nil {
			return nil, err
		}
		if resident.Live() {
			res.Notes = append(res.Notes, fmt.Sprintf("resident %s is running and keeps its session: it reads the new role when its next one starts (yardr resident restart %s)", name, name))
		}
	} else if err := st.PutGroup(ctx, *group); err != nil {
		return nil, err
	}
	// An import repeats the choice its record holds, for every role at once.
	for _, p := range copies {
		switch {
		case to == sourceRig && !p.RigRoles:
			res.Notes = append(res.Notes, fmt.Sprintf("the import of pack %s chose the pack's roles: the next yardr pack apply sets its role on %s again unless given --rig-roles", p.Pack, name))
		case to == sourcePack && p.RigRoles && resident == nil:
			res.Notes = append(res.Notes, fmt.Sprintf("the import of pack %s was given --rig-roles: the next yardr pack apply sets the rig's role on %s again unless given --pack-roles", p.Pack, name))
		}
	}
	if lines, err = rigSources(ctx, st, rigName, home); err != nil {
		return nil, err
	}
	if cur = roleLine(lines, name); cur == nil {
		return nil, fmt.Errorf("rig %s lists no role %s after the switch (yardr rig sources %s)", rigName, name, rigName)
	}
	res.Changed, res.Lines = true, []rigSource{*cur}
	return res, nil
}

func mapsTo(m map[string]string, name string) bool {
	for _, v := range m {
		if v == name {
			return true
		}
	}
	return false
}

func packNames(packs []store.PackImport) string {
	names := make([]string, len(packs))
	for i, p := range packs {
		names[i] = p.Pack
	}
	return strings.Join(names, ", ")
}

// flowLines are the line of the flow governing typ's beads ("" for the
// rig's own) and of its stages. A type without a flow of its own has none.
func flowLines(lines []rigSource, typ string) []rigSource {
	var out []rigSource
	for _, l := range lines {
		if (l.Kind == "flow" || l.Kind == "stage") && l.Type == typ {
			out = append(out, l)
		}
	}
	return out
}

// switchFlow gives the rig's flow, or the flow of its beads of type typ,
// to the rig's repository (clearing the pointer) or to the flow a pack
// imported into the rig left in the store (setting it). The store refuses
// either while an open bead sits in a stage the new flow lacks.
func switchFlow(ctx context.Context, st store.API, rigName, typ, to, home, actor string) (*sourceSwitch, error) {
	lines, err := rigSources(ctx, st, rigName, home)
	if err != nil {
		return nil, err
	}
	place, whose := "default", "the rig's beads"
	if typ != "" {
		place, whose = "type/"+typ, "beads of type "+typ
	}
	res := &sourceSwitch{Rig: rigName, Lines: flowLines(lines, typ)}
	var cur *rigSource
	if len(res.Lines) > 0 {
		cur = &res.Lines[0]
	}
	rig, err := st.GetRig(ctx, rigName)
	if err != nil {
		return nil, err
	}
	name := ""
	if to == sourceRig {
		repo, err := st.RepoFlows(ctx, rigName)
		if err != nil {
			return nil, err
		}
		path := store.RepoFlowDir + "/" + place + ".flow"
		if !slices.ContainsFunc(repo.Flows, func(rf store.RepoFlow) bool { return rf.Path == path }) {
			return nil, fmt.Errorf("rig %s has no %s %s: there is no flow of the rig for %s to switch to", rigName, path, rigBase(rig), whose)
		}
		ptrs, err := st.FlowPointers(ctx)
		if err != nil {
			return nil, err
		}
		// The rig's pointer stands in front of every type's file too.
		if i := slices.IndexFunc(ptrs, func(p store.FlowPointer) bool { return p.Rig == rigName && p.Type == "" }); typ != "" && i >= 0 {
			return nil, fmt.Errorf("rig %s is pointed at flow %s, which stands in front of %s as of every flow file of the rig: switch the rig's flow first (yardr rig source %s flow rig)", rigName, ptrs[i].Flow, path, rigName)
		}
		if cur != nil && cur.Source == sourceRig {
			return res, nil
		}
	} else {
		packs, err := rigPacks(ctx, st, rigName)
		if err != nil {
			return nil, err
		}
		if len(packs) == 0 {
			return nil, fmt.Errorf("no pack is imported into rig %s (yardr pack list): there is no pack's flow for %s to switch to", rigName, whose)
		}
		// The flows the imports pointed the rig, or the type, at.
		var flows []string
		for _, p := range packs {
			found := false
			for _, o := range p.Objects {
				if o.Kind == "pointer" && o.Type == typ && o.Flow != "" {
					found = true
					if !slices.Contains(flows, o.Flow) {
						flows = append(flows, o.Flow)
					}
				}
			}
			if n := pack.FlowName(p.Pack, p.Flow); !found && typ == "" && p.Flow != "" && !slices.Contains(flows, n) {
				flows = append(flows, n)
			}
		}
		switch len(flows) {
		case 0:
			return nil, fmt.Errorf("no pack imported into rig %s (%s) has a flow for %s", rigName, packNames(packs), whose)
		case 1:
			name = flows[0]
		default:
			return nil, fmt.Errorf("the packs imported into rig %s have several flows for %s (%s): point the rig at one yourself (yardr rig flow)", rigName, whose, strings.Join(flows, ", "))
		}
		if _, err := st.GetFlow(ctx, name); errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("the pack's flow %s is no longer in the store: apply the pack again (yardr pack apply)", name)
		} else if err != nil {
			return nil, err
		}
		if cur != nil && cur.Source == sourcePack && cur.Flow == name {
			return res, nil
		}
	}
	if err := st.SetFlowPointer(ctx, rigName, typ, name, actor); err != nil {
		return nil, err
	}
	if lines, err = rigSources(ctx, st, rigName, home); err != nil {
		return nil, err
	}
	res.Changed, res.Lines = true, flowLines(lines, typ)
	// The rig's flow switched; a type with a pointer of its own did not.
	for _, l := range lines {
		if typ == "" && l.Kind == "flow" && l.Type != "" && l.Source != to {
			res.Notes = append(res.Notes, fmt.Sprintf("%s still comes from %s: yardr rig source %s flow --type %s %s", l.Name, strings.TrimSpace(l.Source+" "+l.Pack), rigName, l.Type, to))
		}
	}
	return res, nil
}
