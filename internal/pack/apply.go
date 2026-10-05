package pack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	// OpenThreads: the yard's resident -> source -> yes or no. A yes lets
	// the resident open threads in the source, a no is recorded so the next
	// import does not ask, and a source without an answer is granted to
	// nobody and asked about again. Whoever fills this in has checked that
	// the actor is the operator: Apply does not.
	OpenThreads map[string]map[string]bool
	// MapRefused says, by source, why the stage map the plan would set on
	// that source's hook is not set: a hook acts as a resident, which only
	// the operator or that resident's own session may decide. The rest of
	// the import does not depend on it. As with OpenThreads, who the actor is
	// was checked by whoever fills this in.
	MapRefused map[string]string
	Actor      string
}

// Result is what applying a plan did.
type Result struct {
	Pack string `json:"pack"`
	Rig  string `json:"rig"`
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
	// Restart are the running residents whose role or subscriptions
	// changed: the import does not restart them.
	Restart []string `json:"restart"`
	// Granted are the residents the import let open threads, each with the
	// sources that are new to it.
	Granted []Grant `json:"granted"`
	// Mapped are the stage maps the import set on a hook of the yard.
	Mapped []MapOp `json:"mapped"`
	// Recorded says the yard's record of the import was written; false when
	// nothing changed.
	Recorded bool `json:"recorded"`
}

// Grant is a resident and the sources an import let it open threads in.
type Grant struct {
	Resident string   `json:"resident"`
	Sources  []string `json:"sources"`
}

// Wrote reports whether the import changed anything in the yard.
func (r *Result) Wrote() bool { return r.Created+r.Changed+len(r.Pruned)+len(r.Granted) > 0 }

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
// removes the dropped objects when told to prune, then records the import.
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
func Apply(ctx context.Context, st store.API, p *Plan, o ApplyOptions) (*Result, error) {
	res := &Result{Pack: p.Pack, Rig: p.Rig, Pruned: []string{}, Left: []string{}, PruneErrors: []string{}, Restart: []string{}, Granted: []Grant{}, Mapped: []MapOp{}}
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
		case NotInPack, Dropped:
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
	}
	for _, obj := range p.Objects {
		if obj.Kind == "resident" && obj.Resident != nil && obj.Resident.Restart {
			res.Restart = append(res.Restart, obj.Resident.Resident)
		}
	}
	// After the residents: one the import created is there to be granted to.
	for _, ot := range p.OpenThreads {
		var yes []string
		for _, src := range ot.Wants {
			if o.OpenThreads[ot.Resident][src] {
				yes = append(yes, src)
			}
		}
		if len(yes) == 0 {
			continue
		}
		added, err := st.AllowOpening(ctx, ot.Resident, yes, p.Pack, o.Actor)
		if err != nil {
			return nil, fmt.Errorf("letting resident %s open threads in %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", ot.Resident, strings.Join(yes, ", "), err)
		}
		if len(added) > 0 {
			res.Granted = append(res.Granted, Grant{Resident: ot.Resident, Sources: added})
		}
	}
	pruned := prune(ctx, st, p, o, res)

	dir := p.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	rec := store.PackImport{Pack: p.Pack, Rig: p.Rig, Source: dir, Flow: p.Flow, RigRoles: p.RigRoles, Groups: p.Groups, Residents: p.Residents}
	rec.Commit, rec.Dirty = Commit(dir)
	rec.OpenThreads = p.openAnswers(o.OpenThreads)
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
	return res, nil
}

// refused reports whether o is a stage map the import was told not to set
// (ApplyOptions.MapRefused): it is neither written nor recorded.
func (o Object) refused(a ApplyOptions) bool {
	return o.Kind == "map" && o.Map != nil && (o.Action == Create || o.Action == Change) && a.MapRefused[o.Map.Source] != ""
}

// openAnswers is what the record keeps of the plan's OpenThreads: the
// answers given now, and those of the last import that still hold: a no,
// and a yes the resident still has. A yes it lost since is forgotten, so
// the next import asks.
func (p *Plan) openAnswers(now map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, ot := range p.OpenThreads {
		for _, src := range ot.Wants {
			yes, ok := now[ot.Resident][src]
			if !ok {
				if yes, ok = ot.Recorded[src]; ok && yes && !slices.Contains(ot.Has, src) {
					ok = false
				}
			}
			if !ok {
				continue
			}
			if out[ot.Resident] == nil {
				out[ot.Resident] = map[string]bool{}
			}
			out[ot.Resident][src] = yes
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
	// Which flow and which resident shape the plan: an answer that is not
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
		i := slices.IndexFunc(p.OpenThreads, func(ot OpenThreads) bool { return ot.Resident == name })
		for _, src := range slices.Sorted(maps.Keys(o.OpenThreads[name])) {
			if i < 0 || !p.OpenThreads[i].Grantable(src) {
				problems = append(problems, fmt.Sprintf("the plan does not ask whether resident %s may open threads in %s", name, src))
			}
		}
	}
	files := map[string][]byte{}
	for _, obj := range p.Objects {
		if v := o.Answers["resident."+obj.Name+".resident"]; obj.Kind == "resident" && v != "" && v != obj.Name {
			problems = append(problems, fmt.Sprintf("the plan gives the pack's %s to resident %s, not %s: make it again with that resident", obj.Resident.Pack, obj.Name, v))
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
			text, err := os.ReadFile(obj.File.From)
			if obj.File.Fill {
				text = Fill(text, p.Fill)
			}
			if err != nil {
				problems = append(problems, err.Error())
			} else if sum(text) != obj.File.SHA256 {
				problems = append(problems, fmt.Sprintf("%s changed since the plan was made", obj.File.From))
			}
			files[obj.File.To] = text
		case obj.Kind == "group":
			if obj.Group.Runner != "manual" && len(obj.Group.Subscriptions) > 0 {
				problems = append(problems, fmt.Sprintf("group %s has subscriptions, which only a people group may: unsubscribe it first (yardr unsubscribe %s)", obj.Name, obj.Name))
			}
			if asks("kind") && strings.TrimSpace(answer("kind")) == "" {
				problems = append(problems, fmt.Sprintf("group %s needs an agent kind", obj.Name))
			}
		case obj.Kind == "resident":
			if asks("kind") && strings.TrimSpace(answer("kind")) == "" {
				problems = append(problems, fmt.Sprintf("resident %s needs an agent kind", obj.Name))
			}
			if asks("dir") {
				if dir := answer("dir"); !filepath.IsAbs(dir) {
					problems = append(problems, fmt.Sprintf("resident %s needs a directory, as an absolute path (got %q)", obj.Name, dir))
				} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					problems = append(problems, fmt.Sprintf("resident %s: %s is not a directory", obj.Name, dir))
				}
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("nothing was changed: %s", strings.Join(problems, "; "))
	}
	return files, nil
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
		return st.PutGroup(ctx, g)
	case "flow":
		_, err := st.PutFlow(ctx, *obj.Flow, o.Actor)
		return err
	case "pointer":
		return st.SetFlowPointer(ctx, obj.Pointer.Rig, obj.Pointer.Type, obj.Pointer.Flow, o.Actor)
	case "route":
		// The new route first: a stage is then never without one, and
		// should a delete fail, the next import sees two routes for the
		// stage and replaces both by one.
		if obj.Route.Group != "" {
			r := store.Route{Stage: obj.Route.Stage, Rig: &obj.Route.Rig, Group: obj.Route.Group, Priority: obj.Route.Priority}
			if obj.Route.Type != "" {
				r.Type = &obj.Route.Type
			}
			if _, err := st.AddRoute(ctx, r); err != nil {
				return err
			}
		}
		for _, id := range obj.Route.Delete {
			if err := st.DeleteRoute(ctx, id); err != nil {
				return err
			}
		}
		return nil
	case "resident":
		op := obj.Resident
		def := store.Resident{Name: op.Resident, Runner: "herdr", Config: map[string]any{}, HandoffGrace: "15m"}
		have, err := st.GetResident(ctx, op.Resident)
		switch {
		case err == nil:
			// At the revision read: a resident changed since is refused,
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
		_, err = st.PutResident(ctx, def, o.Actor)
		return err
	case "subscription":
		s := obj.Subscription
		if s.Remove != nil {
			if err := st.RemoveSubscription(ctx, s.Resident, *s.Remove, o.Actor); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		return st.AddSubscription(ctx, s.Resident, *s.Add, o.Actor)
	case "map":
		// The hook as it is now, so its name, source and secret file stay.
		h, err := st.GetHook(ctx, obj.Map.Hook)
		if err != nil {
			return err
		}
		h.As, h.Map = obj.Map.As, obj.Map.Map
		_, err = st.PutHook(ctx, *h, o.Actor)
		return err
	}
	return fmt.Errorf("the plan has an object of unknown kind %q", obj.Kind)
}

// argList is an args answer as a group or resident stores its arguments.
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

// pruneOrder is the order dropped objects are removed in: a route before
// the group it leads to, a pointer before the flow it points at, a flow
// before its stage files.
var pruneOrder = []string{"route", "pointer", "flow", "group", "subscription", "map", "file"}

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
			note, err := remove(ctx, st, obj, o.Actor)
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
func remove(ctx context.Context, st store.API, obj Object, actor string) (note string, err error) {
	switch obj.Kind {
	case "route":
		for _, id := range obj.Route.Delete {
			if err := st.DeleteRoute(ctx, id); err != nil {
				return "", err
			}
		}
	case "pointer":
		err = st.SetFlowPointer(ctx, obj.Pointer.Rig, obj.Pointer.Type, "", actor)
	case "flow":
		err = st.DeleteFlow(ctx, obj.Name, actor)
	case "group":
		var released int
		if released, err = st.DeleteGroup(ctx, obj.Name, actor); released > 0 {
			note = fmt.Sprintf(" (%d of its beads released)", released)
		}
	case "subscription":
		err = st.RemoveSubscription(ctx, obj.Subscription.Resident, *obj.Subscription.Remove, actor)
	case "map":
		var h *store.Hook
		if h, err = st.GetHook(ctx, obj.Map.Hook); err == nil {
			h.As, h.Map = "", nil
			_, err = st.PutHook(ctx, *h, actor)
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
