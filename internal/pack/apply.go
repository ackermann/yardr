package pack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
// empty model is the agent's own default, empty members anyone.
type Answers map[string]string

// ApplyOptions say how a plan is applied.
type ApplyOptions struct {
	Answers Answers
	// Prune removes the plan's dropped objects, those not held.
	Prune bool
	Actor string
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
	// Recorded says the yard's record of the import was written; false when
	// nothing changed.
	Recorded bool `json:"recorded"`
}

// Wrote reports whether the import changed anything in the yard.
func (r *Result) Wrote() bool { return r.Created+r.Changed+len(r.Pruned) > 0 }

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
// that the pack's files are still the ones the plan was made from, and that
// no group would be given a runner the store refuses. Past that the store
// has no transaction spanning its calls, so a write that fails stops the
// import where it is: the objects written so far stay, no record is
// written, and since every object is matched by name, applying again
// finishes the import rather than repeating it. A dropped object whose
// removal fails does not undo the import either: the rest is pruned, the
// record is written with that object still in it, and the error names it.
func Apply(ctx context.Context, st store.API, p *Plan, o ApplyOptions) (*Result, error) {
	res := &Result{Pack: p.Pack, Rig: p.Rig, Pruned: []string{}, Left: []string{}, PruneErrors: []string{}, Restart: []string{}}
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
		if err := write(ctx, st, p, obj, o, files); err != nil {
			return nil, fmt.Errorf("%s %s: %w (the import stopped here: what it wrote before stays, nothing is recorded, and applying again finishes it)", obj.Kind, obj.Name, err)
		}
		if obj.Action == Create {
			res.Created++
		} else {
			res.Changed++
		}
	}
	for _, obj := range p.Objects {
		if obj.Kind == "resident" && obj.Resident != nil && obj.Resident.Restart {
			res.Restart = append(res.Restart, obj.Resident.Resident)
		}
	}
	pruned := prune(ctx, st, p, o, res)

	dir := p.Dir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	rec := store.PackImport{Pack: p.Pack, Rig: p.Rig, Source: dir, Flow: p.Flow, Groups: p.Groups, Residents: p.Residents}
	rec.Commit, rec.Dirty = Commit(dir)
	for _, obj := range p.Objects {
		po, ok := Ident(obj)
		if !ok || pruned[po.Key()] || (po.Kind == "route" && po.Group == "") {
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

// check is everything Apply can know to be wrong before it writes. It
// returns the pack's files as read, by destination, so what is written is
// what was checked.
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
	files := map[string][]byte{}
	for _, obj := range p.Objects {
		if v := o.Answers["resident."+obj.Name+".resident"]; obj.Kind == "resident" && v != "" && v != obj.Name {
			problems = append(problems, fmt.Sprintf("the plan gives the pack's %s to resident %s, not %s: make it again with that resident", obj.Resident.Pack, obj.Name, v))
		}
		answer := func(field string) string { return o.Answers[obj.Kind+"."+obj.Name+"."+field] }
		asks := func(field string) bool { return slices.Contains(obj.Asks, obj.Kind+"."+obj.Name+"."+field) }
		switch {
		case obj.Action == Dropped && obj.Kind == "file":
			if rel, err := filepath.Rel(p.Install, obj.File.To); err != nil || !filepath.IsLocal(rel) {
				problems = append(problems, fmt.Sprintf("file %s is outside %s: it is not removed", obj.File.To, p.Install))
			}
		case obj.Action != Create && obj.Action != Change:
		case obj.Kind == "file":
			text, err := os.ReadFile(obj.File.From)
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
	}
	return fmt.Errorf("the plan has an object of unknown kind %q", obj.Kind)
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
// the group it leads to, a pointer before the flow it points at.
var pruneOrder = []string{"route", "pointer", "flow", "group", "subscription", "file"}

// prune removes the plan's dropped objects, or with no prune asked for
// only lists them as left. It returns the keys of those removed.
func prune(ctx context.Context, st store.API, p *Plan, o ApplyOptions, res *Result) map[string]bool {
	gone := map[string]bool{}
	for _, kind := range pruneOrder {
		for _, obj := range p.Objects {
			if obj.Action != Dropped || obj.Kind != kind {
				continue
			}
			what := obj.Kind + " " + obj.Name
			if !o.Prune || obj.Held != "" {
				res.Left = append(res.Left, what)
				continue
			}
			note, err := remove(ctx, st, obj, o.Actor)
			if err != nil {
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
