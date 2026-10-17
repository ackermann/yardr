package pack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"yardr.dev/yardr/internal/depotfile"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// ExportOptions say which depot's way of working Export writes, and where.
type ExportOptions struct {
	Depot string
	// Name is what the pack calls itself; "" is the depot's name.
	Name string
	// Home is the yard's home, where a store flow's stage files and the
	// home's roles are read.
	Home string
	// Dir is the directory the pack is written to: one that does not
	// exist, or an empty one.
	Dir string
	// Operator is who the yard's role and stage files name where a pack
	// says {{operator}}, as Options.Operator is for an import; "" is the
	// yard's operator setting.
	Operator string
}

// Exported is what Export wrote, and what it left in the yard.
type Exported struct {
	Pack  string `json:"pack"`
	Kind  string `json:"kind,omitempty"`
	Depot string `json:"depot"`
	Dir   string `json:"dir"`
	// Flows and Groups are the pack's, each with what of the yard it was
	// read from.
	Flows  []ExportedFlow  `json:"flows"`
	Groups []ExportedGroup `json:"groups"`
	// Files are the files written, as slash paths in the pack.
	Files []string `json:"files"`
	// Operator is the word {{operator}} was put in place of, and Restored
	// each place it was, for the author to read: a word that only equals
	// the name was rewritten too.
	Operator string  `json:"operator,omitempty"`
	Restored []Place `json:"restored"`
	// Left is what the depot has and the pack does not, each with why.
	Left []string `json:"left"`
}

// ExportedFlow is a flow of the pack and the yard's flow it was read from.
type ExportedFlow struct {
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	From   string `json:"from"`
	Stages int    `json:"stages"`
}

// ExportedGroup is a group of the pack and the yard's group it was read
// from. Kind is agent, script or people, and File its role or script.
type ExportedGroup struct {
	Name string `json:"name"`
	Yard string `json:"yard"`
	Kind string `json:"kind"`
	File string `json:"file,omitempty"`
}

// Place is a line of a file of the pack.
type Place struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Export writes the way of working of one depot as a pack: the flows that
// govern it, their stage files, the groups its plain routes lead to, and
// those groups' roles and scripts. It is the inverse of an import (Build,
// Apply), and reads only what an import writes. Nothing of the work is
// read at all: no bead, note, mail, setting other than the operator's
// name (which it takes out of the files), and no file of the depot beside
// its flows and roles under .yardr/.
//
// What the depot has and no pack carries is in Exported.Left, so nothing
// is dropped unsaid; what would make a pack that works nothing and says
// nothing is refused, before anything is written. The yard is not written
// to. The directory written is read back as LoadDir reads a pack.
func Export(ctx context.Context, st store.API, o ExportOptions) (*Exported, error) {
	depot, err := st.GetDepot(ctx, o.Depot)
	if err != nil {
		return nil, err
	}
	name := o.Name
	if name == "" {
		name = depot.Name
	}
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("pack name %q: use letters, digits, '.', '_' or '-' (--name <pack>)", name)
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	if err := emptyDir(dir); err != nil {
		return nil, err
	}
	e := &exporter{ctx: ctx, st: st, o: o, depot: depot, files: map[string]exportFile{},
		p:   &Pack{Name: name},
		out: &Exported{Pack: name, Depot: depot.Name, Dir: dir, Flows: []ExportedFlow{}, Groups: []ExportedGroup{}, Files: []string{}, Restored: []Place{}, Left: []string{}}}
	if slices.Contains(Kinds, depot.Kind) {
		e.p.Kind, e.out.Kind = depot.Kind, depot.Kind
	}
	for _, step := range []func() error{e.flows, e.routes, e.groups, e.names, e.rules} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	if err := e.write(dir); err != nil {
		return nil, err
	}
	if _, err := LoadDir(dir); err != nil {
		return nil, fmt.Errorf("the pack written to %s does not read back as a pack, which is a mistake of yardr pack export, not of the yard:\n%w", dir, err)
	}
	return e.out, nil
}

// emptyDir refuses a directory with anything in it: a pack is edited by
// hand once it is written, and an export never merges with that.
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%s is not empty: a pack is written to a new or an empty directory, never over what is there", dir)
	}
	return nil
}

type exportFile struct {
	text []byte
	mode fs.FileMode
	// from is where the yard has the file, for an error that names it.
	from string
	// filled says an import fills the file's placeholders: a role or a
	// stage file, not a script.
	filled bool
}

type exporter struct {
	ctx   context.Context
	st    store.API
	o     ExportOptions
	depot *store.Depot
	p     *Pack
	out   *Exported
	// governing are the depot's flows, as p.Flows in the same order.
	governing []store.DepotFlow
	// routed are the yard's groups the exported routes lead to, in the
	// flows' order, and packGroup the pack's name of each.
	routed    []string
	packGroup map[string]string
	files     map[string]exportFile
}

func (e *exporter) left(format string, args ...any) {
	e.out.Left = append(e.out.Left, fmt.Sprintf(format, args...))
}

// flowKey is the pack's name for a flow of the depot, the inverse of
// FlowName: the name the depot's import of a pack recorded for it; else
// the store's name less the pack's that brought it; else the name whole.
// A type's flow in the depot's repository has no name, and is called by
// its type.
func (e *exporter) flowKey(g store.DepotFlow, packs []store.PackImport) string {
	name := g.Flow.Name
	for _, p := range packs {
		if p.Flow != "" && FlowName(p.Pack, p.Flow) == name {
			return p.Flow
		}
	}
	for _, p := range packs {
		if key, ok := strings.CutPrefix(name, p.Pack+"."); ok && slices.ContainsFunc(p.Objects, func(o store.PackObject) bool { return o.Kind == "flow" && o.Name == name }) {
			return key
		}
	}
	if !validName.MatchString(name) && g.Type != "" {
		return g.Type
	}
	return name
}

// flows reads the flows governing the depot, however each is governed, and
// the stage file of each stage as a brief reads it.
func (e *exporter) flows() error {
	governing, err := e.st.DepotFlows(e.ctx, e.depot.Name)
	if err != nil {
		return err
	}
	if len(governing) == 0 {
		return fmt.Errorf("depot %s has no flow: there is nothing to export", e.depot.Name)
	}
	repo, err := e.st.RepoFlows(e.ctx, e.depot.Name)
	if err != nil {
		return err
	}
	all, err := e.st.Packs(e.ctx)
	if err != nil {
		return err
	}
	packs := slices.DeleteFunc(all, func(p store.PackImport) bool { return p.Depot != e.depot.Name })
	from := map[string]string{}
	for _, g := range governing {
		f := g.Flow
		key := e.flowKey(g, packs)
		if !validName.MatchString(key) {
			return fmt.Errorf("flow %s of depot %s: a pack cannot call a flow %q (letters, digits, '.', '_' or '-')", f.Name, e.depot.Name, key)
		}
		if other, dup := from[key]; dup {
			return fmt.Errorf("flows %s and %s of depot %s would both be %s in the pack: rename one of them in the yard", other, flowOf(g), e.depot.Name, key)
		}
		from[key] = flowOf(g)
		if len(f.Terminal) == 0 {
			return fmt.Errorf("flow %s of depot %s has no stage that closes a bead: a pack's flow has one (end: true)", f.Name, e.depot.Name)
		}
		pf := Flow{Name: key, Type: g.Type}
		for _, s := range f.Stages {
			if f.IsTerminal(s) {
				pf.Stages = append(pf.Stages, Stage{Name: s, End: true})
				continue
			}
			ps := Stage{Name: s, Outcomes: slices.Clone(f.Outcomes[s])}
			// The moves no outcome names are a person's or a crew member's.
			for _, to := range f.Transitions[s] {
				if !slices.ContainsFunc(ps.Outcomes, func(o store.Outcome) bool { return o.To == to }) {
					ps.Manual = append(ps.Manual, to)
				}
			}
			text, path, err := e.stageFile(g, repo, s)
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) != "" {
				ps.File = "flows/" + key + "/" + s + ".md"
				e.files[ps.File] = exportFile{text: []byte(text), mode: 0o644, from: path, filled: true}
			}
			pf.Stages = append(pf.Stages, ps)
		}
		e.p.Flows = append(e.p.Flows, pf)
		e.governing = append(e.governing, g)
		e.out.Flows = append(e.out.Flows, ExportedFlow{Name: key, Type: g.Type, From: flowOf(g), Stages: len(pf.Stages)})
	}
	return nil
}

// flowOf names a flow of the depot as the yard has it: the store's flow, or
// the file in the depot's repository.
func flowOf(g store.DepotFlow) string {
	if !g.Source.InStore() {
		return g.Source.Path
	}
	return g.Flow.Name
}

// stageFile is the stage file of a governing flow's stage: in the yard's
// home for a flow in the store, in the depot's repository beside the flow
// file otherwise. "" for a stage without one.
func (e *exporter) stageFile(g store.DepotFlow, repo *store.RepoFlows, stage string) (text, path string, err error) {
	if g.Source.InStore() {
		sf, err := store.HomeStageFile(e.o.Home, g.Flow.Name, stage)
		if err != nil || sf == nil {
			return "", "", err
		}
		return sf.Text, sf.Path, nil
	}
	path = g.StageFiles[stage]
	if path == "" {
		return "", "", nil
	}
	for _, sf := range repo.StageFiles {
		if sf.Path == path {
			return sf.Text, path, nil
		}
	}
	return "", "", nil
}

// routeName says a route by what it asks of a bead, as a plan does.
func (e *exporter) routeName(r store.Route) string {
	name := "yard default stage " + r.Stage
	if r.Depot != nil {
		name = "depot " + *r.Depot + " stage " + r.Stage
	}
	if r.Type != nil {
		name += " type " + *r.Type
	}
	if l := r.LabelList(); l != "" {
		name += " label " + l
	}
	if p := r.BeadPriorities(); p != "" {
		name += " beads " + p
	}
	if r.Originator != nil {
		name += " originator " + *r.Originator
	}
	return name + " -> " + r.GroupList()
}

// routes gives each stage the group that works it today, where a pack can
// say so. Routes are followed, not carried: an import makes the one route
// depot, stage (and type) -> group, and nothing else.
//
// The routes of a stage are this depot's effective routes (its own, plus
// every yard default no own route of the same key sets aside). In
// MatchRoute's order, a filtered route (label, bead priority, named
// originator or a type without a flow of its own) before the first route
// that takes every bead can win for only some of the stage's beads, so
// the stage is unrouted. Otherwise the first route that takes every bead
// is what works the stage; it is exported when it is plain (no condition,
// no further group), whether it is the depot's own or a yard default the
// pack follows. A quotaed winner is written with no group. Every route
// that is not exported is listed with its groups and why.
func (e *exporter) routes() error {
	all, err := e.st.Routes(e.ctx)
	if err != nil {
		return err
	}
	settings, err := e.st.Settings(e.ctx)
	if err != nil {
		return err
	}
	depotName := e.depot.Name
	routes := store.EffectiveRoutes(all, &depotName, settings[store.OperatorSetting])
	store.SortRoutes(routes)
	// The types with a flow of their own: a route for one is that flow's.
	own := map[string]bool{}
	for _, g := range e.governing {
		own[g.Type] = true
	}
	exported := map[string]bool{}
	var seen []store.Route
	why := map[string]string{}
	saw := func(r store.Route, reason string) {
		if _, ok := why[r.ID]; !ok {
			seen = append(seen, r)
		}
		// The reason that is the route's own stands; one that only says
		// another route won gives way to it.
		if why[r.ID] == "" || reason != "" {
			why[r.ID] = reason
		}
	}
	for i, g := range e.governing {
		pf := &e.p.Flows[i]
		for j := range pf.Stages {
			ps := &pf.Stages[j]
			var cands []store.Route
			var partial []bool // a route takes only some of the stage's beads
			for _, r := range routes {
				if r.Stage != ps.Name || (r.Depot != nil && *r.Depot != e.depot.Name) {
					continue
				}
				takesSome := r.ByBead()
				if r.Type != nil && *r.Type != g.Type {
					// A type following the depot's flow is routed in its
					// stages; a type with its own flow, in that flow's.
					if g.Type != "" || own[*r.Type] {
						continue
					}
					takesSome = true
				}
				cands = append(cands, r)
				partial = append(partial, takesSome)
			}
			// A filtered route only makes this stage partly routed when it
			// precedes every route that matches the whole flow. A lower
			// priority label or type route after a whole-flow route can never
			// win MatchRoute, so it does not change what works the stage.
			some := false
			for _, takesSome := range partial {
				if !takesSome {
					break
				}
				some = true
			}
			where := fmt.Sprintf("stage %s of flow %s", ps.Name, pf.Name)
			if ps.End {
				for _, r := range cands {
					saw(r, "its stage closes the bead on entry, and nobody works it")
				}
				continue
			}
			for _, r := range cands {
				saw(r, routeWhy(r, g.Type))
			}
			switch {
			case len(cands) == 0:
				e.left("%s: no group in the pack: the yard routes it to nobody", where)
			case some:
				e.left("%s: no group in the pack: a route takes only some of its beads (a label, a bead priority, an originator or a type), which a pack's stage cannot say; its beads wait for a hand until the importer routes them", where)
			case cands[0].Quotaed():
				e.left("%s: no group in the pack: what works it is a route with a condition or further groups (%s), and a pack names one group for a stage; its beads wait for a hand until the importer routes them", where, cands[0].GroupList())
			default:
				win := cands[0]
				exported[win.ID] = true
				ps.Group = win.Group
				if !slices.Contains(e.routed, win.Group) {
					e.routed = append(e.routed, win.Group)
				}
			}
		}
	}
	var lost []string
	for _, r := range seen {
		if exported[r.ID] {
			continue
		}
		reason := why[r.ID]
		if reason == "" {
			reason = "another route of the stage is the one the pack follows"
		}
		e.left("route %s: not in the pack: %s", e.routeName(r), reason)
		for _, rg := range r.Groups() {
			if !slices.Contains(e.routed, rg.Name) && !slices.Contains(lost, rg.Name) {
				lost = append(lost, rg.Name)
			}
		}
	}
	for _, name := range lost {
		e.left("group %s: not in the pack: only routes that are not in the pack lead to it", name)
	}
	return nil
}

// routeWhy says why a route of a stage of a flow for typ is none a pack
// carries; "" for a plain route of the depot, which is one. A plain yard
// default the pack follows is exported as the stage's winner and so never
// listed; any other default is listed with its scope.
func routeWhy(r store.Route, typ string) string {
	switch {
	case r.ByBead():
		return "it asks for a label, a bead priority or an originator, and a pack says nothing of the single bead"
	case r.Type != nil && *r.Type != typ:
		return "it is for beads of type " + *r.Type + " alone, which follow the depot's flow, and a pack routes a stage for every bead of its flow"
	case r.Quotaed():
		return "it has a condition or further groups, and a pack names one group for a stage"
	case r.Depot == nil:
		return "it is a yard default (no depot): it serves every depot that does not state its own route for the same beads, an import leaves it alone, and no pack carries it"
	}
	return ""
}

// groups writes the groups the exported routes lead to, each with its role
// or script, and renames them in the stages: the pack's name is the yard's
// less the depot's prefix, which an import puts back.
func (e *exporter) groups() error {
	have, err := e.st.Groups(e.ctx)
	if err != nil {
		return err
	}
	grants, err := e.st.SourceGrants(e.ctx, "")
	if err != nil {
		return err
	}
	// A group's source grants are asks in a pack, never grants. Sources
	// themselves are outside this depot export, so an import only asks when
	// its yard has the source and a credential for it.
	calls := map[string]map[string][]string{}
	for _, grant := range grants {
		// A grant as a user is that user's to give: a pack carries no
		// call as anyone, so it is left out, not written as a plain one.
		if grant.Group == "" || grant.As != "" || !slices.Contains(e.routed, grant.Group) {
			continue
		}
		if calls[grant.Group] == nil {
			calls[grant.Group] = map[string][]string{}
		}
		calls[grant.Group][grant.Source] = append(calls[grant.Group][grant.Source], grant.Calls...)
	}
	e.packGroup = map[string]string{}
	yardOf := map[string]string{}
	for _, name := range e.routed {
		i := slices.IndexFunc(have, func(g store.Group) bool { return g.Name == name })
		if i < 0 {
			return fmt.Errorf("a route of depot %s leads to group %s, which the yard does not have: a yard to fix (yardr route list), not a pack to write", e.depot.Name, name)
		}
		g := have[i]
		pn := name
		if short, ok := strings.CutPrefix(name, e.depot.Name+"-"); ok && validName.MatchString(short) {
			pn = short
		}
		if !validName.MatchString(pn) {
			return fmt.Errorf("group %s: a pack cannot call a group %q (letters, digits, '.', '_' or '-')", name, pn)
		}
		if other, dup := yardOf[pn]; dup {
			return fmt.Errorf("groups %s and %s would both be %s in the pack: rename one of them in the yard", other, name, pn)
		}
		yardOf[pn], e.packGroup[name] = name, pn
		pg := Group{Name: pn}
		eg := ExportedGroup{Name: pn, Yard: name}
		// What the pack carries of the group's config; the rest is listed.
		carried := ""
		switch g.Runner {
		case "herdr":
			carried, eg.Kind = "prompt", "agent"
			prompt, _ := g.Config["prompt"].(string)
			if prompt == "" {
				return fmt.Errorf("group %s has no prompt: a pack's agent group names a role file. Give it one (yardr group set %s prompt=<file>), or route its stages elsewhere", name, name)
			}
			text, from, err := e.role(prompt)
			if err != nil {
				return fmt.Errorf("group %s: its prompt %s: %w: a yard to fix (yardr depot sources %s), not a pack to write", name, prompt, err, e.depot.Name)
			}
			pg.Agent, pg.Limit = "roles/"+pn+".md", g.Limit
			e.files[pg.Agent] = exportFile{text: text, mode: 0o644, from: from, filled: true}
		case "exec":
			carried, eg.Kind = "cmd", "script"
			cmd, _ := g.Config["cmd"].(string)
			script, alone := scriptOf(cmd)
			info, err := os.Stat(script)
			if script == "" || !filepath.IsAbs(script) || err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("group %s runs %q, whose command names no file by its full path: a pack carries a script as a file, and this command has none to carry", name, cmd)
			}
			text, err := os.ReadFile(script)
			if err != nil {
				return err
			}
			pg.Run, pg.Limit = "scripts/"+pn, g.Limit
			e.files[pg.Run] = exportFile{text: text, mode: 0o755, from: script}
			if !alone {
				e.left("group %s: the pack carries its script alone: the variables set before it and the arguments after it in the group's command stay in the yard (yardr group show %s)", name, name)
			}
		case "manual":
			eg.Kind, pg.People = "people", true
			// 1 is what an import gives a group whose pack names no limit.
			if g.Limit != 1 {
				pg.Limit = g.Limit
			}
		case store.PeerRunner:
			return fmt.Errorf("group %s, which a route of depot %s leads to, is worked by another yard (runner %s): a pack names no yard, and its stage would be one nobody works. Route the stage to a group of this yard, or export from the yard that does the work", name, e.depot.Name, g.Runner)
		default:
			return fmt.Errorf("group %s has runner %s: a pack's group is an agent with a role, a script, or people", name, g.Runner)
		}
		if eg.Kind == "people" && len(calls[name]) > 0 {
			e.left("group %s: source-call grants are not in the pack: a person calls as who they are, not as a people group", name)
		} else {
			for _, source := range slices.Sorted(maps.Keys(calls[name])) {
				words := slices.Clone(calls[name][source])
				slices.Sort(words)
				pg.Calls = append(pg.Calls, CallsAsked{Source: source, Calls: words})
			}
		}
		eg.File = pg.Agent + pg.Run
		var stays []string
		for k, v := range g.Config {
			if k != carried && !unset(v) {
				stays = append(stays, k)
			}
		}
		slices.Sort(stays)
		if len(g.Members) > 0 {
			stays = append(stays, "members")
		}
		if len(g.Subscriptions) > 0 {
			stays = append(stays, "subscriptions")
		}
		if len(stays) > 0 {
			asks := "an import asks for the agent, its model and its arguments, and anything else is set in the yard (yardr group set)"
			switch eg.Kind {
			case "script":
				asks = "they are set in the yard (yardr group set)"
			case "people":
				asks = "an import asks who is in the group, and anything else is set in the yard (yardr group set)"
			}
			e.left("group %s: %s: not in the pack; %s", name, strings.Join(stays, ", "), asks)
		}
		e.p.Groups = append(e.p.Groups, pg)
		e.out.Groups = append(e.out.Groups, eg)
	}
	for i := range e.p.Flows {
		for j := range e.p.Flows[i].Stages {
			if s := &e.p.Flows[i].Stages[j]; s.Group != "" {
				s.Group = e.packGroup[s.Group]
			}
		}
	}
	return nil
}

// unset reports whether a config value is one its reader takes as not set.
func unset(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	}
	return false
}

// role reads a group's prompt where a brief for a bead of the depot reads
// it: the depot's own role at its base, the home's, or the file it names.
func (e *exporter) role(prompt string) (text []byte, from string, err error) {
	src, err := runner.LocatePrompt(prompt, e.depot, e.o.Home)
	if err != nil {
		return nil, "", err
	}
	if src.Kind == runner.PromptDepot {
		text, where, _, err := depotfile.ReadDepotFile(e.depot, src.Path)
		return text, where, err
	}
	text, err = os.ReadFile(src.Path)
	return text, src.Path, err
}

// scriptOf is the script a group's command runs, the inverse of setScript:
// its first word that is no variable assignment, unquoted. alone is false
// for a command with assignments before the script or arguments after it.
// Only that word is looked at: what follows it may name any file of the
// machine, and none of those is the pack's to copy.
func scriptOf(cmd string) (script string, alone bool) {
	i := 0
	for i < len(cmd) {
		for i < len(cmd) && (cmd[i] == ' ' || cmd[i] == '\t') {
			i++
		}
		end := wordEnd(cmd, i)
		if i == end {
			break
		}
		if !assignment.MatchString(cmd[i:end]) {
			return shellUnquote(cmd[i:end]), strings.TrimSpace(cmd[:i]) == "" && strings.TrimSpace(cmd[end:]) == ""
		}
		i = end
	}
	return "", false
}

// shellUnquote is a shell word without its quotes, the inverse of
// shellQuote for the words setScript writes.
func shellUnquote(w string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(w); i++ {
		c := w[i]
		switch {
		case c == '\\' && quote != '\'' && i+1 < len(w):
			i++
			out.WriteByte(w[i])
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// names turns the role and stage files back into a pack's: the operator's
// name becomes {{operator}} again, wherever it stands as a whole word.
// Filling keeps no record of where it filled, so this goes by the text, and
// every place is listed for the author to read. A file that already holds
// a placeholder is refused: no import would fill it.
//
// The crew is not exported, so a crew member's name stays as it is, and
// each place one stands is listed: the pack brings no crew member of that
// name.
func (e *exporter) names() error {
	// What an import fills {{operator}} with (planner.names): who it was
	// told, the setting, or these words in a yard that has none.
	operator := e.o.Operator
	if operator == "" {
		settings, err := e.st.Settings(e.ctx)
		if err != nil {
			return err
		}
		if operator = settings["operator"]; operator == "" {
			operator = "the operator"
		}
	}
	crew, err := e.st.Crew(e.ctx)
	if err != nil {
		return err
	}
	var rels []string
	for rel, f := range e.files {
		if f.filled {
			rels = append(rels, rel)
		}
	}
	slices.Sort(rels)
	for _, rel := range rels {
		f := e.files[rel]
		lines := strings.Split(string(f.text), "\n")
		for i, line := range lines {
			if m := placeholder.FindString(line); m != "" {
				return fmt.Errorf("%s:%d: holds %s: the pack would carry a placeholder that no import fills. Take the braces out of the file in the yard", f.from, i+1, m)
			}
		}
		for i, line := range lines {
			for _, r := range crew {
				if r.Name != operator && len(wholeWords(line, r.Name)) > 0 {
					e.left("%s:%d: names crew member %s: the name stands in the file as plain text, and the pack brings no crew member of that name", rel, i+1, r.Name)
				}
			}
			at := wholeWords(line, operator)
			if len(at) == 0 {
				continue
			}
			for k := len(at) - 1; k >= 0; k-- {
				line = line[:at[k]] + "{{" + Operator + "}}" + line[at[k]+len(operator):]
			}
			lines[i] = line
			e.out.Restored = append(e.out.Restored, Place{File: rel, Line: i + 1})
		}
		f.text = []byte(strings.Join(lines, "\n"))
		e.files[rel] = f
	}
	if len(e.out.Restored) > 0 {
		e.out.Operator = operator
	}
	return nil
}

// wholeWords are the offsets in line where word stands as a whole word:
// with no letter, digit, '_' or '-' right before or after it, so a name
// inside a longer one (yardmaster in web-yardmaster) is not it.
func wholeWords(line, word string) []int {
	if word == "" {
		return nil
	}
	inWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' }
	var out []int
	for from := 0; ; {
		i := strings.Index(line[from:], word)
		if i < 0 {
			return out
		}
		i += from
		end := i + len(word)
		before, _ := utf8.DecodeLastRuneInString(line[:i])
		after, _ := utf8.DecodeRuneInString(line[end:])
		if (i == 0 || !inWord(before)) && (end == len(line) || !inWord(after)) {
			out = append(out, i)
			from = end
		} else {
			from = i + 1
		}
	}
}

// rules lists the depot's standing rules file, which no pack has a key for.
func (e *exporter) rules() error {
	if e.depot.Path == "" {
		return nil
	}
	_, _, _, err := depotfile.ReadDepotFile(e.depot, depotfile.DepotRules)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("depot %s rules: %w", e.depot.Name, err)
	}
	e.left("%s: not in the pack: the depot's standing rules are the depot's own, and a pack has no place for them", depotfile.DepotRules)
	return nil
}

// write makes the directory: pack.yaml, then the files it names.
func (e *exporter) write(dir string) error {
	text, err := e.yaml()
	if err != nil {
		return err
	}
	e.files[File] = exportFile{text: text, mode: 0o644}
	for rel := range e.files {
		e.out.Files = append(e.out.Files, rel)
	}
	slices.Sort(e.out.Files)
	// Before the first file: a stage's name may hold dots and slashes, and
	// its file must not lead out of the pack, nor out of its flow's
	// directory onto another file of the pack (flows/f/a/../../roles/x.md).
	for _, rel := range e.out.Files {
		if !filepath.IsLocal(filepath.FromSlash(rel)) || path.Clean(rel) != rel {
			return fmt.Errorf("%s would be written outside its flow's directory in %s: rename the stage in the yard", rel, dir)
		}
	}
	for _, rel := range e.out.Files {
		f := e.files[rel]
		to := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(to, f.text, f.mode); err != nil {
			return err
		}
		// WriteFile's mode is cut by the umask; a script must run.
		if err := os.Chmod(to, f.mode); err != nil {
			return err
		}
	}
	return nil
}

const exportHead = `# Written by yardr pack export: one depot's way of working, as a pack.
# Its role and stage files are copied as the yard has them. Read them
# through before you share the pack; docs/packs.md says what each key means.

`

func scalar(tag, v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v}
}

func str(v string) *yaml.Node {
	n := scalar("!!str", v)
	if slices.Contains(booleans, strings.ToLower(v)) {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func mapping(flow bool, pairs ...*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: pairs}
	if flow {
		n.Style = yaml.FlowStyle
	}
	return n
}

func add(m *yaml.Node, key string, v *yaml.Node) { m.Content = append(m.Content, str(key), v) }

// yaml is the pack as pack.yaml says it, in the shipped packs' layout: a
// stage and a group on one line each, in the flow's order.
func (e *exporter) yaml() ([]byte, error) {
	root := mapping(false)
	add(root, "name", str(e.p.Name))
	if e.p.Kind != "" {
		add(root, "kind", str(e.p.Kind))
	}
	flows := mapping(false)
	for _, f := range e.p.Flows {
		fn := mapping(false)
		if f.Type != "" {
			add(fn, "type", str(f.Type))
		}
		stages := mapping(false)
		for _, s := range f.Stages {
			sn := mapping(true)
			if s.End {
				add(sn, "end", scalar("!!bool", "true"))
			}
			if s.Group != "" {
				add(sn, "group", str(s.Group))
			}
			if s.File != "" {
				add(sn, "file", str(s.File))
			}
			if len(s.Outcomes) > 0 {
				on := mapping(true)
				for _, o := range s.Outcomes {
					add(on, o.Name, str(o.To))
				}
				add(sn, "outcomes", on)
			}
			if len(s.Manual) > 0 {
				mn := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
				for _, to := range s.Manual {
					mn.Content = append(mn.Content, str(to))
				}
				add(sn, "manual", mn)
			}
			add(stages, s.Name, sn)
		}
		add(fn, "stages", stages)
		add(flows, f.Name, fn)
	}
	add(root, "flows", flows)
	if len(e.p.Groups) > 0 {
		groups := mapping(false)
		for _, g := range e.p.Groups {
			gn := mapping(true)
			switch {
			case g.Agent != "":
				add(gn, "agent", str(g.Agent))
			case g.Run != "":
				add(gn, "run", str(g.Run))
			default:
				add(gn, "people", scalar("!!bool", "true"))
			}
			if len(g.Calls) > 0 {
				cn := mapping(false)
				for _, asked := range g.Calls {
					words := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
					for _, call := range asked.Calls {
						words.Content = append(words.Content, str(call))
					}
					add(cn, asked.Source, words)
				}
				add(gn, "calls", cn)
			}
			if g.Limit > 0 {
				add(gn, "limit", scalar("!!int", fmt.Sprint(g.Limit)))
			}
			add(groups, g.Name, gn)
		}
		add(root, "groups", groups)
	}
	var buf bytes.Buffer
	buf.WriteString(exportHead)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
