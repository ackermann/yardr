package pack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Answers are the answers to a plan's questions, by question id. A
// question counts as answered when its id is here, whatever the value: an
// empty model is the agent's own default, empty members anyone, empty args
// (the agent's permission arguments, space-separated) none.
type Answers map[string]string

// ApplyOptions say how a plan is applied.
type ApplyOptions struct {
	Answers Answers
	// Prune removes the plan's dropped objects, those not held.
	Prune bool
	// Overwrite lets the import replace an installed file that was edited
	// by hand since the last import; without it such a plan is refused.
	Overwrite bool
	// OpenThreads are the operator's answers to what the plan asks under
	// OpenThreads: the yard's crew member -> source -> yes or no. A yes lets
	// the crew member open threads in the source, a no is recorded so the next
	// import does not ask, and a source without an answer is granted to
	// nobody and asked about again. Whoever fills this in has checked that
	// the actor is the operator: Apply does not.
	OpenThreads map[string]map[string]bool
	// MapRefused says, by source, why the stage map the plan would set on
	// that source's hook is not set: a hook acts as a crew member, which only
	// the operator or that crew member's own session may decide. The rest of
	// the import does not depend on it. As with OpenThreads, who the actor is
	// was checked by whoever fills this in.
	MapRefused map[string]string
	// Calls are the operator's answers to what the plan asks under Calls:
	// the grantee, as store.Grantee.String says it ("group web-builders")
	// -> CallKey ("crm:read") -> yes or no. A yes is a grant
	// (store.GrantSource), a no is recorded so the next import does not ask,
	// and a call without an answer is granted to nobody and asked about
	// again. The store holds a grant to the operator whoever fills this in.
	Calls map[string]map[string]bool
	// Expose are the operator's answers to what the plan asks under Expose:
	// the yard's crew member -> the peer's name -> yes or no. A yes exposes
	// the crew member to the peer (store.AllowPeer), a no is recorded so
	// the next import does not ask, and a peer without an answer is asked
	// about again. The store holds the grant to the operator whoever fills
	// this in.
	Expose map[string]map[string]bool
	// SourceRefused says, by source, why a declared source the plan would
	// add is not added: a source is where a credential of the yard is sent,
	// so adding or replacing one is the operator's (store.PutSource). The
	// rest of the import does not depend on it, and who the actor is was
	// checked by whoever fills this in.
	SourceRefused map[string]string
	Actor         string
}

// Result is what applying a plan did.
type Result struct {
	Pack  string `json:"pack"`
	Depot string `json:"depot"`
	// Created, Changed and Unchanged count the plan's objects by what was
	// done to them.
	Created   int `json:"created"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	// Pruned are the dropped objects removed, Left those still there: no
	// prune was asked for, the object is held, or its removal failed (then
	// PruneErrors says why).
	Pruned      []string `json:"pruned"`
	Left        []string `json:"left"`
	PruneErrors []string `json:"prune_errors"`
	// Restart are the running crew members whose role or subscriptions
	// changed: the import does not restart them.
	Restart []string `json:"restart"`
	// Granted are the crew the import let open threads, each with the
	// sources that are new to it.
	Granted []Grant `json:"granted"`
	// Calls are the calls of declared sources the import granted.
	Calls []CallGrant `json:"calls"`
	// Exposed are the crew the import exposed to peers, each with the
	// peers.
	Exposed []Exposed `json:"exposed"`
	// Mapped are the stage maps the import set on a hook of the yard.
	Mapped []MapOp `json:"mapped"`
	// Declared are the sources the import added or replaced.
	Declared []string `json:"declared"`
	// Folds are what the import folded from each declared source's
	// -declared name into the source itself, one per source that had
	// anything under the old name.
	Folds []Fold `json:"folds"`
	// Recorded says the yard's record of the import was written; false when
	// nothing changed.
	Recorded bool `json:"recorded"`
}

// Grant is a crew member and the sources an import let it open threads in.
type Grant struct {
	CrewMember string   `json:"crew"`
	Sources    []string `json:"sources"`
}

// Exposed is a crew member and the peers an import exposed it to.
type Exposed struct {
	CrewMember string   `json:"crew"`
	Peers      []string `json:"peers"`
}

// CallGrant is a group or a crew member and the calls of one source an
// import granted it.
type CallGrant struct {
	store.Grantee
	Source string   `json:"source"`
	Calls  []string `json:"calls"`
}

// Fold is what one apply folded from a -declared name into the source
// the pack brings: the links, and the records MoveSourceRecords moved.
type Fold struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Links int    `json:"links"`
	store.SourceMove
}

// Wrote reports whether the import changed anything in the yard.
func (r *Result) Wrote() bool {
	return r.Created+r.Changed+len(r.Pruned)+len(r.Granted)+len(r.Calls)+len(r.Exposed) > 0
}

// Missing are the plan's questions a has no answer to.
func (p *Plan) Missing(a Answers) []Question {
	var out []Question
	for _, q := range p.Questions {
		if _, ok := a[q.ID]; !ok {
			out = append(out, q)
		}
	}
	return out
}

// Apply does to the yard what the plan says, in the plan's order, then
// removes the dropped objects when told to prune, then records the import,
// then folds each declared source's -declared links and records into the
// source itself.
//
// It checks everything it can before the first write: that the plan is not
// refused, that every question has an answer and the answers are usable,
// that the pack's files are still the ones the plan was made from, that no
// installed file edited by hand is overwritten unasked, and that no group
// would be given a runner the store refuses. Past that the store
// has no transaction spanning its calls, so a write that fails stops the
// import where it is: the objects written so far stay, no record is
// written, and since every object is matched by name, applying again
// finishes the import rather than repeating it. A dropped object whose
// removal fails does not undo the import either: the rest is pruned, the
// record is written with that object still in it, and the error names it.
// The fold runs after the record, so a source whose links or records are
// not folded in leaves the import applied and recorded, and the error
// names the source.
func Apply(ctx context.Context, st store.API, p *Plan, o ApplyOptions) (*Result, error) {
	res := &Result{Pack: p.Pack, Depot: p.Depot, Pruned: []string{}, Left: []string{}, PruneErrors: []string{}, Restart: []string{}, Granted: []Grant{}, Calls: []CallGrant{}, Exposed: []Exposed{}, Mapped: []MapOp{}, Declared: []string{}, Folds: []Fold{}}
	files, err := check(p, o)
	if err != nil {
		return nil, err
	}
	for i := range p.Objects {
		obj := &p.Objects[i]
		switch obj.Action {
		case Unchanged:
			res.Unchanged++
			continue
		case NotInPack, Dropped, Takes:
			continue
		}
		if obj.refused(o) {
			continue
		}
		if err := write(ctx, st, p, obj, o, files); err != nil {
			return nil, fmt.Errorf("%s %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", obj.Kind, obj.Name, err)
		}
		if obj.Action == Create {
			res.Created++
		} else {
			res.Changed++
		}
		if obj.Kind == "map" {
			res.Mapped = append(res.Mapped, *obj.Map)
		}
		if obj.Kind == "source" {
			res.Declared = append(res.Declared, obj.Source.Name)
		}
	}
	for _, obj := range p.Objects {
		if obj.Kind == "crew" && obj.CrewMember != nil && obj.CrewMember.Restart {
			res.Restart = append(res.Restart, obj.CrewMember.CrewMember)
		}
	}
	// After the crew: one the import created is there to be granted to.
	for _, ot := range p.OpenThreads {
		var yes []string
		for _, src := range ot.Wants {
			if o.OpenThreads[ot.CrewMember][src] {
				yes = append(yes, src)
			}
		}
		if len(yes) == 0 {
			continue
		}
		added, err := st.AllowOpening(ctx, ot.CrewMember, yes, p.Pack, o.Actor)
		if err != nil {
			return nil, fmt.Errorf("letting crew member %s open threads in %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", ot.CrewMember, strings.Join(yes, ", "), err)
		}
		if len(added) > 0 {
			res.Granted = append(res.Granted, Grant{CrewMember: ot.CrewMember, Sources: added})
		}
	}
	// After the groups, the crew and the declared sources: each is there to
	// be granted, or to grant a call of.
	for _, c := range p.Calls {
		var yes []string
		for _, w := range c.Wants {
			if !o.Calls[c.Grantee().String()][CallKey(c.Source, w)] {
				continue
			}
			if _, err := st.GrantSource(ctx, c.Source, w, c.Grantee(), o.Actor); err != nil {
				return nil, fmt.Errorf("letting %s call %s of the source %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", c.Grantee(), w, c.Source, err)
			}
			yes = append(yes, w)
		}
		if len(yes) > 0 {
			res.Calls = append(res.Calls, CallGrant{Grantee: c.Grantee(), Source: c.Source, Calls: yes})
		}
	}
	// After the crew, as the threads: one the import created is there to be
	// exposed. A peer at a time, since each link is its own write.
	for _, ex := range p.Expose {
		var to []string
		for _, peer := range ex.Asks {
			if !o.Expose[ex.CrewMember][peer] {
				continue
			}
			if _, err := st.AllowPeer(ctx, peer, store.PeerAllow{Expose: []string{ex.CrewMember}}, o.Actor); err != nil {
				return nil, fmt.Errorf("exposing crew member %s to peer %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", ex.CrewMember, peer, err)
			}
			to = append(to, peer)
		}
		if len(to) > 0 {
			res.Exposed = append(res.Exposed, Exposed{CrewMember: ex.CrewMember, Peers: to})
		}
	}
	pruned := prune(ctx, st, p, o, res)

	rec := store.PackImport{Pack: p.Pack, Depot: p.Depot, Source: p.Dir, Flow: p.Flow, Setup: p.SetupFile != "", DepotRoles: p.DepotRoles, Groups: p.Groups, Crew: p.Crew}
	// A pack in no directory (one yardr embeds) has no commit of its own:
	// its source names the build it came in.
	if filepath.IsAbs(p.Dir) {
		rec.Commit, rec.Dirty = Commit(p.Dir)
	}
	rec.OpenThreads = p.openAnswers(o.OpenThreads)
	rec.Calls = p.callAnswers(o.Calls)
	rec.Expose = p.exposeAnswers(o.Expose)
	for _, obj := range p.Objects {
		po, ok := Ident(obj)
		if !ok || pruned[po.Key()] || (po.Kind == "route" && po.Group == "") || obj.refused(o) {
			continue
		}
		rec.Objects = append(rec.Objects, po)
	}
	if res.Recorded, err = st.RecordPack(ctx, rec, res.Wrote(), o.Actor); err != nil {
		return res, fmt.Errorf("recording the import: %w (the import itself is done; applying again records it)", err)
	}
	if len(res.PruneErrors) > 0 {
		return res, fmt.Errorf("the import is applied and recorded, but not everything dropped could be removed: %s", strings.Join(res.PruneErrors, "; "))
	}
	// Last of all: a source pack names its source, and what the yard
	// recorded under the same name with -declared is folded in, once.
	// A refused source is skipped: it was not added.
	for _, d := range p.Declared {
		if o.SourceRefused[d.Source] != "" {
			continue
		}
		from := d.Source + "-declared"
		links, err := st.MoveLinkSource(ctx, from, d.Source, o.Actor)
		if err != nil {
			return nil, fmt.Errorf("source %s is declared, but its links were not folded in: %w", d.Source, err)
		}
		moved, err := st.MoveSourceRecords(ctx, from, d.Source, o.Actor)
		if err != nil {
			return nil, fmt.Errorf("source %s is declared, but its records were not folded in: %w", d.Source, err)
		}
		if links > 0 || moved.Moved() {
			res.Folds = append(res.Folds, Fold{From: from, To: d.Source, Links: links, SourceMove: *moved})
		}
	}
	return res, nil
}

// refused reports whether o is a stage map the import was told not to set
// (ApplyOptions.MapRefused) or a declared source it was told not to add
// (SourceRefused): it is neither written nor recorded.
func (o Object) refused(a ApplyOptions) bool {
	if o.Action != Create && o.Action != Change {
		return false
	}
	return (o.Kind == "map" && o.Map != nil && a.MapRefused[o.Map.Source] != "") ||
		(o.Kind == "source" && o.Source != nil && a.SourceRefused[o.Source.Name] != "")
}

// openAnswers is what the record keeps of the plan's OpenThreads: the
// answers given now, and those of the last import that still hold: a no,
// and a yes the crew member still has. A yes it lost since is forgotten, so
// the next import asks.
func (p *Plan) openAnswers(now map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, ot := range p.OpenThreads {
		for _, src := range ot.Wants {
			yes, ok := now[ot.CrewMember][src]
			if !ok {
				if yes, ok = ot.Recorded[src]; ok && yes && !slices.Contains(ot.Has, src) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			if out[ot.CrewMember] == nil {
				out[ot.CrewMember] = map[string]bool{}
			}
			out[ot.CrewMember][src] = yes
		}
	}
	return out
}

// callAnswers is what the record keeps of the plan's Calls, as openAnswers
// keeps the plan's OpenThreads: the answers given now, and those of the
// last import that still hold: a no, and a yes the grantee still has.
func (p *Plan) callAnswers(now map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, c := range p.Calls {
		who := c.Grantee().String()
		for _, w := range c.Wants {
			key := CallKey(c.Source, w)
			yes, ok := now[who][key]
			if !ok {
				if yes, ok = c.Recorded[w]; ok && yes && !slices.Contains(c.Has, w) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			if out[who] == nil {
				out[who] = map[string]bool{}
			}
			out[who][key] = yes
		}
	}
	return out
}

// exposeAnswers is what the record keeps of the plan's Expose, by the
// peer's key, as openAnswers keeps the plan's OpenThreads: the answers
// given now, and those of the last import that still hold: a no, and a yes
// for a peer the crew member is still exposed to. A peer the yard no longer
// has is forgotten with its answer.
func (p *Plan) exposeAnswers(now map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, ex := range p.Expose {
		for peer, key := range ex.Keys {
			yes, ok := now[ex.CrewMember][peer]
			if !ok {
				if yes, ok = ex.Recorded[peer]; ok && yes && !slices.Contains(ex.Has, peer) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			if out[ex.CrewMember] == nil {
				out[ex.CrewMember] = map[string]bool{}
			}
			out[ex.CrewMember][key] = yes
		}
	}
	return out
}

// check is everything Apply can know to be wrong before it writes. It
// returns the pack's files as they are installed (a role or stage file with
// the plan's names filled in), by destination, so what is written is what
// was checked.
func check(p *Plan, o ApplyOptions) (map[string][]byte, error) {
	if len(p.Refused) > 0 {
		return nil, fmt.Errorf("the import is refused: %s", strings.Join(p.Refused, "; "))
	}
	var problems []string
	for _, q := range p.Missing(o.Answers) {
		problems = append(problems, fmt.Sprintf("%s has no answer (%s)", q.ID, q.Ask))
	}
	// Which flow and which crew member shape the plan: an answer that is not
	// what the plan was made with needs a new plan, not this one.
	if v := o.Answers[FlowQuestion]; v != "" && v != p.Flow {
		problems = append(problems, fmt.Sprintf("the plan is for flow %s, not %s: make it again with that flow", p.Flow, v))
	}
	if !o.Overwrite {
		problems = append(problems, p.EditedFiles()...)
	}
	// An answer is to a question of the plan: a source the yard lacks, or
	// one the pack does not name, is granted to nobody.
	for _, name := range slices.Sorted(maps.Keys(o.OpenThreads)) {
		i := slices.IndexFunc(p.OpenThreads, func(ot OpenThreads) bool { return ot.CrewMember == name })
		for _, src := range slices.Sorted(maps.Keys(o.OpenThreads[name])) {
			if i < 0 || !p.OpenThreads[i].Grantable(src) {
				problems = append(problems, fmt.Sprintf("the plan does not ask whether crew member %s may open threads in %s", name, src))
			}
		}
	}
	for _, who := range slices.Sorted(maps.Keys(o.Calls)) {
		for _, key := range slices.Sorted(maps.Keys(o.Calls[who])) {
			if !slices.ContainsFunc(p.Calls, func(c Calls) bool {
				src, call, _ := strings.Cut(key, ":")
				return c.Grantee().String() == who && c.Source == src && c.Grantable(call)
			}) {
				problems = append(problems, fmt.Sprintf("the plan does not ask whether %s may call %s", who, key))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(o.Expose)) {
		i := slices.IndexFunc(p.Expose, func(ex Expose) bool { return ex.CrewMember == name })
		for _, peer := range slices.Sorted(maps.Keys(o.Expose[name])) {
			if i < 0 || !slices.Contains(p.Expose[i].Asks, peer) {
				problems = append(problems, fmt.Sprintf("the plan does not ask whether crew member %s is exposed to peer %s", name, peer))
			}
		}
	}
	files := map[string][]byte{}
	for _, obj := range p.Objects {
		if v := o.Answers["crew."+obj.Name+".crew"]; obj.Kind == "crew" && v != "" && v != obj.Name {
			problems = append(problems, fmt.Sprintf("the plan gives the pack's %s to crew member %s, not %s: make it again with that crew member", obj.CrewMember.Pack, obj.Name, v))
		}
		if v := o.Answers["group."+obj.Name+".group"]; obj.Kind == "group" && v != "" && v != obj.Name {
			problems = append(problems, fmt.Sprintf("the plan makes the new group %s, and %s does not take it: make it again with that group", obj.Name, v))
		}
		answer := func(field string) string { return o.Answers[obj.Kind+"."+obj.Name+"."+field] }
		asks := func(field string) bool { return slices.Contains(obj.Asks, obj.Kind+"."+obj.Name+"."+field) }
		switch {
		case obj.Action == Dropped && obj.Kind == "file":
			if !p.Removable(obj.File.To) {
				problems = append(problems, fmt.Sprintf("file %s is neither under %s nor a stage file of the pack's flows under %s: it is not removed", obj.File.To, p.Install, p.StageFiles))
			}
		case obj.Action != Create && obj.Action != Change:
		case obj.Kind == "file":
			text, err := p.read(obj.File)
			if obj.File.Fill {
				text = Fill(text, p.Fill)
			}
			if err != nil {
				problems = append(problems, err.Error())
			} else if sum(text) != obj.File.SHA256 {
				problems = append(problems, fmt.Sprintf("%s changed since the plan was made", obj.File.From))
			}
			files[obj.File.To] = text
		case obj.Kind == "source" && obj.Source != nil && len(obj.Source.Commands) > 0:
			if p.src == nil {
				problems = append(problems, "the plan no longer has its pack: make it again")
				break
			}
			for _, c := range obj.Source.Commands {
				rel := path.Join(obj.Source.DeclDir, c.Program)
				data, err := fs.ReadFile(p.src, rel)
				switch {
				case err != nil:
					problems = append(problems, fmt.Sprintf("%s: %v", joinName(p.Dir, rel), err))
				case sum(data) != c.SHA256:
					problems = append(problems, fmt.Sprintf("%s changed since the plan was made", joinName(p.Dir, rel)))
				}
			}
		case obj.Kind == "group":
			if obj.Group.Runner != "manual" && len(obj.Group.Subscriptions) > 0 {
				problems = append(problems, fmt.Sprintf("group %s has subscriptions, which only a people group may: unsubscribe it first (yardr unsubscribe %s)", obj.Name, obj.Name))
			}
			if asks("kind") && strings.TrimSpace(answer("kind")) == "" {
				problems = append(problems, fmt.Sprintf("group %s needs an agent kind", obj.Name))
			}
		case obj.Kind == "crew":
			if asks("kind") && strings.TrimSpace(answer("kind")) == "" {
				problems = append(problems, fmt.Sprintf("crew member %s needs an agent kind", obj.Name))
			}
			if asks("dir") {
				if dir := answer("dir"); !filepath.IsAbs(dir) {
					problems = append(problems, fmt.Sprintf("crew member %s needs a directory, as an absolute path (got %q)", obj.Name, dir))
				} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					problems = append(problems, fmt.Sprintf("crew member %s: %s is not a directory", obj.Name, dir))
				}
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("nothing was changed: %s", strings.Join(problems, "; "))
	}
	return files, nil
}

// read is the pack's file op copies, as the pack has it now. Only a plan
// Build made knows the pack's directory: one read back from JSON does not.
func (p *Plan) read(op *FileOp) ([]byte, error) {
	rel := op.From
	if IsURL(p.Dir) {
		var ok bool
		if rel, ok = strings.CutPrefix(op.From, strings.TrimSuffix(p.Dir, "/")+"/"); !ok {
			return nil, fmt.Errorf("%s: it is not under the pack's URL %s: make the plan again", op.From, p.Dir)
		}
	} else {
		var err error
		if rel, err = filepath.Rel(p.Dir, op.From); err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
	}
	if p.src == nil {
		return nil, fmt.Errorf("%s: the plan no longer has its pack: make it again", op.From)
	}
	return fs.ReadFile(p.src, rel)
}

// write does one object of the plan.
func write(ctx context.Context, st store.API, p *Plan, obj *Object, o ApplyOptions, files map[string][]byte) error {
	answer := func(field string) (string, bool) {
		v, ok := o.Answers[obj.Kind+"."+obj.Name+"."+field]
		return strings.TrimSpace(v), ok && slices.Contains(obj.Asks, obj.Kind+"."+obj.Name+"."+field)
	}
	switch obj.Kind {
	case "file":
		return install(obj.File, files[obj.File.To])
	case "group":
		g := *obj.Group
		g.Config = map[string]any{}
		for k, v := range obj.Group.Config {
			g.Config[k] = v
		}
		if v, ok := answer("kind"); ok {
			g.Config["kind"] = v
		}
		if v, ok := answer("model"); ok && v != "" {
			g.Config["model"] = v
		}
		if v, ok := answer("args"); ok && v != "" {
			g.Config["args"] = argList(v)
		}
		if v, ok := answer("members"); ok {
			g.Members = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		}
		return st.PutGroup(ctx, g, o.Actor)
	case "flow":
		_, err := st.PutFlow(ctx, *obj.Flow, o.Actor)
		return err
	case "pointer":
		return st.SetFlowPointer(ctx, obj.Pointer.Depot, obj.Pointer.Type, obj.Pointer.Flow, o.Actor)
	case "route":
		// The new route first: a stage is then never without one, and
		// should a delete fail, the next import sees two routes for the
		// stage and replaces both by one.
		if obj.Route.Group != "" {
			r := store.Route{Stage: obj.Route.Stage, Depot: &obj.Route.Depot, Group: obj.Route.Group, Priority: obj.Route.Priority}
			if obj.Route.Type != "" {
				r.Type = &obj.Route.Type
			}
			if obj.Route.Originator != "" {
				r.Originator = &obj.Route.Originator
			}
			if _, err := st.AddRoute(ctx, r, o.Actor); err != nil {
				return err
			}
		}
		for _, id := range obj.Route.Delete {
			if err := st.DeleteRoute(ctx, id, o.Actor); err != nil {
				return err
			}
		}
		return nil
	case "crew":
		op := obj.CrewMember
		def := store.CrewMember{Name: op.CrewMember, Runner: "herdr", Config: map[string]any{}, HandoffGrace: "15m"}
		have, err := st.GetCrew(ctx, op.CrewMember)
		switch {
		case err == nil:
			// At the revision read: a crew member changed since is refused,
			// not overwritten.
			def = *have
			if def.Config == nil {
				def.Config = map[string]any{}
			}
		case !errors.Is(err, store.ErrNotFound):
			return err
		default:
			if v, ok := answer("kind"); ok {
				def.Config["kind"] = v
			}
			if v, ok := answer("model"); ok && v != "" {
				def.Config["model"] = v
			}
			if v, ok := answer("args"); ok && v != "" {
				def.Config["args"] = argList(v)
			}
			def.Dir, _ = answer("dir")
		}
		if op.Prompt != "" {
			def.Config["prompt"] = op.Prompt
		}
		def.On = op.On
		_, err = st.PutCrew(ctx, def, o.Actor)
		return err
	case "subscription":
		s := obj.Subscription
		if s.Remove != nil {
			if err := st.RemoveSubscription(ctx, s.CrewMember, *s.Remove, o.Actor); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		return st.AddSubscription(ctx, s.CrewMember, *s.Add, o.Actor)
	case "map":
		// The hook as it is now, so its name, source and secret file stay.
		h, err := st.GetHook(ctx, obj.Map.Hook)
		if err != nil {
			return err
		}
		// The parts the pack carries; the other is the yard's.
		if obj.Map.As != "" {
			h.As = obj.Map.As
			if obj.Map.Map != nil {
				h.Map = obj.Map.Map
			}
		}
		if len(obj.Map.Says) > 0 {
			h.Says = obj.Map.Says
		}
		if len(obj.Map.Makes) > 0 {
			h.Makes = obj.Map.Makes
		}
		_, err = st.PutHook(ctx, *h, o.Actor)
		return err
	case "source":
		if _, err := st.PutSource(ctx, obj.Source.From, obj.Source.Text, o.Actor); err != nil {
			return err
		}
		d, err := source.Parse(obj.Source.From, []byte(obj.Source.Text))
		if err != nil {
			return fmt.Errorf("source %s saved, but its commands' files were not copied: %v", obj.Source.Name, err)
		}
		if p.src == nil {
			return fmt.Errorf("source %s saved, but its commands' files were not copied: the plan no longer has its pack: make it again", obj.Source.Name)
		}
		if err := d.CopyCommandFilesFS(p.src, obj.Source.DeclDir, p.Home); err != nil {
			return fmt.Errorf("source %s saved, but its commands' files were not copied: %w", obj.Source.Name, err)
		}
		return nil
	}
	return fmt.Errorf("the plan has an object of unknown kind %q", obj.Kind)
}

// argList is an args answer as a group or crew member stores its arguments.
func argList(v string) []any {
	var list []any
	for _, s := range strings.Fields(v) {
		list = append(list, s)
	}
	return list
}

// install writes a pack's file under the home: to a temporary file beside
// it, then renamed, so a prompt is never read half written.
func install(op *FileOp, text []byte) error {
	mode := os.FileMode(0o644)
	if op.Mode == "0755" {
		mode = 0o755
	}
	if err := os.MkdirAll(filepath.Dir(op.To), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(op.To), ".import-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), op.To)
}

// InstallRole writes the role of the pack's crew member named member under
// home, where an import installs it and with the names an import fills in:
// operator, and takes for the member itself, the yard's crew member that
// takes it. It is for a crew member the yard has before its first depot
// (yardr init's yardmaster), which no import has reached yet: the first
// import that maps member to takes finds the file as it would write it. A
// file already there is kept unless replace says it is nobody's yet and may
// be written again. The path is "" when the pack has no such crew member or
// gives it no role, and wrote is false when the file already read so.
func InstallRole(p *Pack, home, member, takes, operator string, replace bool) (path string, wrote bool, err error) {
	i := slices.IndexFunc(p.Crew, func(r CrewMember) bool { return r.Name == member })
	if i < 0 || p.Crew[i].Role == "" {
		return "", false, nil
	}
	rel := p.Crew[i].Role
	fill := map[string]string{}
	for _, r := range p.Crew {
		fill[r.Name] = r.Name
	}
	fill[member] = takes
	// Last, as in a plan (planner.names).
	fill[Operator] = operator
	text, err := fs.ReadFile(p.fsys, rel)
	if err != nil {
		return "", false, err
	}
	text = Fill(text, fill)
	path = filepath.Join(config.At(home).Pack(p.Name), filepath.FromSlash(rel))
	have, err := os.ReadFile(path)
	switch {
	case err == nil && (!replace || bytes.Equal(have, text)):
		return path, false, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", false, err
	}
	if err := install(&FileOp{To: path}, text); err != nil {
		return "", false, err
	}
	return path, true, nil
}

// pruneOrder is the order dropped objects are removed in: a route before
// the group it leads to, a pointer before the flow it points at, a flow
// before its stage files.
var pruneOrder = []string{"route", "pointer", "flow", "group", "subscription", "map", "source", "file"}

// prune removes the plan's dropped objects, or with no prune asked for
// only lists them as left. It returns the keys of those removed.
func prune(ctx context.Context, st store.API, p *Plan, o ApplyOptions, res *Result) map[string]bool {
	gone := map[string]bool{}
	// kept are the flows whose removal failed: their stage files stay with
	// them, for the beads that still follow the flow.
	kept := map[string]bool{}
	for _, kind := range pruneOrder {
		for _, obj := range p.Objects {
			if obj.Action != Dropped || obj.Kind != kind {
				continue
			}
			what := obj.Kind + " " + obj.Name
			if !o.Prune || obj.Held != "" || (obj.Kind == "file" && obj.File != nil && kept[obj.File.Flow]) {
				res.Left = append(res.Left, what)
				continue
			}
			note, err := remove(ctx, st, p, obj, o.Actor)
			if err != nil {
				kept[obj.Name] = obj.Kind == "flow"
				res.Left = append(res.Left, what)
				res.PruneErrors = append(res.PruneErrors, what+": "+err.Error())
				continue
			}
			res.Pruned = append(res.Pruned, what+note)
			if obj.Owned != nil {
				gone[obj.Owned.Key()] = true
			}
		}
	}
	return gone
}

// remove removes one dropped object; note is what else went with it.
func remove(ctx context.Context, st store.API, p *Plan, obj Object, actor string) (note string, err error) {
	switch obj.Kind {
	case "route":
		for _, id := range obj.Route.Delete {
			if err := st.DeleteRoute(ctx, id, actor); err != nil {
				return "", err
			}
		}
	case "pointer":
		err = st.SetFlowPointer(ctx, obj.Pointer.Depot, obj.Pointer.Type, "", actor)
	case "flow":
		err = st.DeleteFlow(ctx, obj.Name, actor)
	case "group":
		var released int
		if released, err = st.DeleteGroup(ctx, obj.Name, actor); released > 0 {
			note = fmt.Sprintf(" (%d of its beads released)", released)
		}
	case "subscription":
		err = st.RemoveSubscription(ctx, obj.Subscription.CrewMember, *obj.Subscription.Remove, actor)
	case "map":
		var h *store.Hook
		if h, err = st.GetHook(ctx, obj.Map.Hook); err == nil {
			if obj.Map.As != "" {
				h.As, h.Map = "", nil
			}
			if len(obj.Map.Says) > 0 {
				h.Says = nil
			}
			if len(obj.Map.Makes) > 0 {
				h.Makes = nil
			}
			_, err = st.PutHook(ctx, *h, actor)
		}
	case "source":
		err = st.DeleteSource(ctx, obj.Name, actor)
		// The commands' files with it, as yardr source rm removes them.
		if err == nil {
			err = source.RemoveCommandFiles(p.Home, obj.Name)
		}
	case "file":
		err = os.Remove(obj.File.To)
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		err = nil // gone already
	}
	return note, err
}

// Commit is the git commit of the repository dir is in, and whether dir
// then holds changes the commit does not; "" when dir is in no repository
// or git cannot say.
func Commit(dir string) (sha string, dirty bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Stderr = new(bytes.Buffer)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	sha, err := git("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || sha == "" {
		return "", false
	}
	status, err := git("status", "--porcelain", "--", ".")
	return sha, err == nil && status != ""
}
