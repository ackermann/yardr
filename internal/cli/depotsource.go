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

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
)

// sourceSwitch is what yardr depot source did: the lines of yardr depot sources
// for the object afterwards, a flow's with its stages'.
type sourceSwitch struct {
	Depot string `json:"depot"`
	// Changed is false when the object already came from the chosen source.
	Changed bool          `json:"changed"`
	Lines   []depotSource `json:"lines"`
	// Notes say what the switch leaves for the operator: a pack's record
	// that will undo it, a crew member to restart, a type still pointed elsewhere.
	Notes []string `json:"notes,omitempty"`
}

func (a *app) depotSourceCmd() *cobra.Command {
	var flowType string
	cmd := &cobra.Command{
		Use:   "source <depot> (role <group|crew> | flow [--type <type>]) (depot | pack)",
		Short: "Switch one role or flow of a depot between the depot's own and the pack's",
		Long: `Switch one line of yardr depot sources between the depot's repository and the
pack imported into the depot, and print the line as it is afterwards.

  role  sets the prompt of the group or crew member: to roles/<name>.md, which
        yardr resolves to the depot's .yardr/roles/<name>.md on the base
        branch, or to the pack's installed copy under the yard's home. A
        crew member is the yard's and reads a path as it is, so its depot's role
        is the file in the depot's checkout, by its absolute path.
  flow  clears the depot's flow pointer (depot), or points it at the pack's flow
        in the store (pack); with --type, the pointer of that type's beads.
        A flow switches whole: its stage files come with it, the depot's
        beside its flow file, the pack's from the yard's home.

Refused, changing nothing, when the chosen source does not exist: the depot
has no such file on its base, no pack is imported into the depot or none has
the role or a flow. A flow change is refused as yardr depot flow is, naming
the beads, while an open bead sits in a stage the new flow lacks.

A pack's record still says whose roles its import chose, so the next yardr
pack apply sets them again; the output says when that undoes the switch.`,
		Example: `  yardr depot source web role web-builders depot
  yardr depot source web role yardmaster pack
  yardr depot source web flow depot
  yardr depot source web flow --type decision pack`,
		Args: cobra.RangeArgs(3, 4),
		RunE: func(c *cobra.Command, args []string) error {
			depot, kind, to := args[0], args[1], args[len(args)-1]
			if (kind != "role" || len(args) != 4) && (kind != "flow" || len(args) != 3) {
				return fmt.Errorf("give role <group|crew> depot|pack, or flow [--type <type>] depot|pack")
			}
			if to != sourceDepot && to != sourcePack {
				return fmt.Errorf("the source is depot or pack, not %q", to)
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
				res, err = switchRole(c.Context(), st, depot, args[2], to, a.home(), a.who())
			} else {
				res, err = switchFlow(c.Context(), st, depot, flowType, to, a.home(), a.who())
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
	cmd.Flags().StringVarP(&flowType, "type", "t", "", "the flow of the depot's beads of this type")
	return cmd
}

// roleLine finds the role name among the lines.
func roleLine(lines []depotSource, name string) *depotSource {
	if i := slices.IndexFunc(lines, func(l depotSource) bool { return l.Kind == "role" && l.Name == name }); i >= 0 {
		return &lines[i]
	}
	return nil
}

// roleFile is the file name of the role a prompt names: <name>.md alone or
// in a directory roles, wherever that is: the depot's, the home's, a pack's
// installed copy. "" for a prompt that is no role.
func roleFile(prompt string) string {
	dir, name := filepath.Split(filepath.Clean(prompt))
	if filepath.Ext(name) != ".md" || (dir != "" && filepath.Base(dir) != "roles") {
		return ""
	}
	return name
}

// depotBase says where a depot's own files are read, for a refusal.
func depotBase(depot *store.Depot) string {
	if depot.IsDir() {
		return "in its directory"
	}
	return "on its base " + or(depot.Base, "origin/main")
}

// depotPacks are the imports into the depot, in the store's order.
func depotPacks(ctx context.Context, st store.API, depot string) ([]store.PackImport, error) {
	all, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.PackImport
	for _, p := range all {
		if p.Depot == depot {
			out = append(out, p)
		}
	}
	return out, nil
}

// switchRole sets the prompt of the group or crew member name, a role of the
// depot as yardr depot sources lists them, to the depot's own role or to the copy
// a pack imported into the depot installed. The role is the one the prompt
// names now, by its file name: nothing else says which of the depot's roles
// is the group's.
func switchRole(ctx context.Context, st store.API, depotName, name, to, home, actor string) (*sourceSwitch, error) {
	lines, err := depotSources(ctx, st, depotName, home)
	if err != nil {
		return nil, err
	}
	cur := roleLine(lines, name)
	if cur == nil {
		return nil, fmt.Errorf("depot %s has no role %s: no route of the depot leads to an agent group of that name, and no pack imported into it names such a crew member (yardr depot sources %s)", depotName, name, depotName)
	}
	res := &sourceSwitch{Depot: depotName, Lines: []depotSource{*cur}}
	if cur.Source == to && cur.Error == "" {
		return res, nil
	}
	depot, err := st.GetDepot(ctx, depotName)
	if err != nil {
		return nil, err
	}
	// The lines list groups before crew members, so a name both have is the group's.
	group, err := st.GetGroup(ctx, name)
	var member *store.CrewMember
	var conf map[string]any
	switch {
	case err == nil:
		conf = group.Config
	case errors.Is(err, store.ErrNotFound):
		if member, err = st.GetCrew(ctx, name); err != nil {
			return nil, err
		}
		conf = member.Config
	default:
		return nil, err
	}
	prompt, _ := conf["prompt"].(string)
	file := roleFile(prompt)
	if file == "" {
		return nil, fmt.Errorf("%s has the prompt %q, which names no role (roles/<name>.md): nothing says which role of the depot or of a pack is its own; set its prompt to one first", name, prompt)
	}
	packs, err := depotPacks(ctx, st, depotName)
	if err != nil {
		return nil, err
	}
	// The packs of the depot that installed a copy of the role.
	var copies []store.PackImport
	for _, p := range packs {
		if _, err := os.Stat(filepath.Join(config.At(home).PackRoles(p.Pack), file)); err == nil {
			copies = append(copies, p)
		}
	}
	var next string
	if to == sourceDepot {
		if member != nil {
			// A crew member's prompt is a path read as it is, never looked up
			// in a depot: the depot's role is the file in its checkout.
			next = filepath.Join(depot.Path, ".yardr", "roles", file)
			if _, err := os.Stat(next); depot.Path == "" || err != nil {
				return nil, fmt.Errorf("depot %s has no .yardr/roles/%s in its checkout %s, where a crew member would read it: %s stays as it is", depotName, file, depot.Path, name)
			}
		} else {
			has, err := rigcheck.DepotRole(depot, file)
			if err != nil {
				return nil, fmt.Errorf("depot %s: looking for its own role %s: %w", depotName, file, err)
			}
			if !has {
				return nil, fmt.Errorf("depot %s has no .yardr/roles/%s %s: %s stays as it is", depotName, file, depotBase(depot), name)
			}
			next = "roles/" + file
		}
	} else {
		if len(packs) == 0 {
			return nil, fmt.Errorf("no pack is imported into depot %s (yardr pack list): there is no pack's role to switch %s to", depotName, name)
		}
		// Of several packs with a copy, the one whose import named the
		// group or crew member.
		if len(copies) > 1 {
			named := slices.DeleteFunc(slices.Clone(copies), func(p store.PackImport) bool {
				return !mapsTo(p.Groups, name) && !mapsTo(p.Crew, name)
			})
			if len(named) != 1 {
				return nil, fmt.Errorf("packs %s imported into depot %s each have a role %s, and nothing says which is %s's: set its prompt yourself", packNames(copies), depotName, file, name)
			}
			copies = named
		}
		if len(copies) == 0 {
			return nil, fmt.Errorf("no pack imported into depot %s (%s) installed a role %s: %s stays as it is", depotName, packNames(packs), file, name)
		}
		next = filepath.Join(config.At(home).PackRoles(copies[0].Pack), file)
	}
	conf["prompt"] = next
	if member != nil {
		if _, err := st.PutCrew(ctx, *member, actor); err != nil {
			return nil, err
		}
		if member.Live() {
			res.Notes = append(res.Notes, fmt.Sprintf("crew member %s is running and keeps its session: it reads the new role when its next one starts (yardr crew restart %s)", name, name))
		}
	} else if err := st.PutGroup(ctx, *group, actor); err != nil {
		return nil, err
	}
	// An import repeats the choice its record holds, for every role at once.
	for _, p := range copies {
		switch {
		case to == sourceDepot && !p.DepotRoles:
			res.Notes = append(res.Notes, fmt.Sprintf("the import of pack %s chose the pack's roles: the next yardr pack apply sets its role on %s again unless given --depot-roles", p.Pack, name))
		case to == sourcePack && p.DepotRoles && member == nil:
			res.Notes = append(res.Notes, fmt.Sprintf("the import of pack %s was given --depot-roles: the next yardr pack apply sets the depot's role on %s again unless given --pack-roles", p.Pack, name))
		}
	}
	if lines, err = depotSources(ctx, st, depotName, home); err != nil {
		return nil, err
	}
	if cur = roleLine(lines, name); cur == nil {
		return nil, fmt.Errorf("depot %s lists no role %s after the switch (yardr depot sources %s)", depotName, name, depotName)
	}
	res.Changed, res.Lines = true, []depotSource{*cur}
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
// depot's own) and of its stages. A type without a flow of its own has none.
func flowLines(lines []depotSource, typ string) []depotSource {
	var out []depotSource
	for _, l := range lines {
		if (l.Kind == "flow" || l.Kind == "stage") && l.Type == typ {
			out = append(out, l)
		}
	}
	return out
}

// switchFlow gives the depot's flow, or the flow of its beads of type typ,
// to the depot's repository (clearing the pointer) or to the flow a pack
// imported into the depot left in the store (setting it). The store refuses
// either while an open bead sits in a stage the new flow lacks.
func switchFlow(ctx context.Context, st store.API, depotName, typ, to, home, actor string) (*sourceSwitch, error) {
	lines, err := depotSources(ctx, st, depotName, home)
	if err != nil {
		return nil, err
	}
	place, whose := "default", "the depot's beads"
	if typ != "" {
		place, whose = "type/"+typ, "beads of type "+typ
	}
	res := &sourceSwitch{Depot: depotName, Lines: flowLines(lines, typ)}
	var cur *depotSource
	if len(res.Lines) > 0 {
		cur = &res.Lines[0]
	}
	depot, err := st.GetDepot(ctx, depotName)
	if err != nil {
		return nil, err
	}
	name := ""
	if to == sourceDepot {
		repo, err := st.RepoFlows(ctx, depotName)
		if err != nil {
			return nil, err
		}
		path := store.RepoFlowDir + "/" + place + ".flow"
		if !slices.ContainsFunc(repo.Flows, func(rf store.RepoFlow) bool { return rf.Path == path }) {
			return nil, fmt.Errorf("depot %s has no %s %s: there is no flow of the depot for %s to switch to", depotName, path, depotBase(depot), whose)
		}
		ptrs, err := st.FlowPointers(ctx)
		if err != nil {
			return nil, err
		}
		// The depot's pointer stands in front of every type's file too.
		if i := slices.IndexFunc(ptrs, func(p store.FlowPointer) bool { return p.Depot == depotName && p.Type == "" }); typ != "" && i >= 0 {
			return nil, fmt.Errorf("depot %s is pointed at flow %s, which stands in front of %s as of every flow file of the depot: switch the depot's flow first (yardr depot source %s flow depot)", depotName, ptrs[i].Flow, path, depotName)
		}
		if cur != nil && cur.Source == sourceDepot {
			return res, nil
		}
	} else {
		packs, err := depotPacks(ctx, st, depotName)
		if err != nil {
			return nil, err
		}
		if len(packs) == 0 {
			return nil, fmt.Errorf("no pack is imported into depot %s (yardr pack list): there is no pack's flow for %s to switch to", depotName, whose)
		}
		// The flows the imports pointed the depot, or the type, at.
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
			return nil, fmt.Errorf("no pack imported into depot %s (%s) has a flow for %s", depotName, packNames(packs), whose)
		case 1:
			name = flows[0]
		default:
			return nil, fmt.Errorf("the packs imported into depot %s have several flows for %s (%s): point the depot at one yourself (yardr depot flow)", depotName, whose, strings.Join(flows, ", "))
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
	if err := st.SetFlowPointer(ctx, depotName, typ, name, actor); err != nil {
		return nil, err
	}
	if lines, err = depotSources(ctx, st, depotName, home); err != nil {
		return nil, err
	}
	res.Changed, res.Lines = true, flowLines(lines, typ)
	// The depot's flow switched; a type with a pointer of its own did not.
	for _, l := range lines {
		if typ == "" && l.Kind == "flow" && l.Type != "" && l.Source != to {
			res.Notes = append(res.Notes, fmt.Sprintf("%s still comes from %s: yardr depot source %s flow --type %s %s", l.Name, strings.TrimSpace(l.Source+" "+l.Pack), depotName, l.Type, to))
		}
	}
	return res, nil
}
