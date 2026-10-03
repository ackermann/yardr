package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Flow is a bead lifecycle: which stages exist, which moves between them
// are allowed, and which stages end the bead. Routes still decide who works
// a stage; a flow only decides where a bead may go next. Flows are stored
// once under a name and rigs point at them (FlowPointer), or come from the
// rig's repository (RepoFlows); resolveFlow decides which one governs a
// bead. A bead no flow governs may move to any stage.
type Flow struct {
	Name        string              `json:"name"`
	Stages      []string            `json:"stages"` // in the order they were written
	Transitions map[string][]string `json:"transitions"`
	// Terminal stages close the bead on entry, with the stage as reason.
	Terminal []string `json:"terminal,omitempty"`
	// Outcomes name, per stage, how work there can end and where each
	// ending sends the bead (`new -> review on done`), so agents report an
	// outcome and the flow picks the stage. Every outcome's stage is also a
	// transition; a transition no outcome names is left to explicit moves.
	Outcomes map[string][]Outcome `json:"outcomes,omitempty"`
}

// Outcome is one way work in a stage can end, and the stage it leads to.
type Outcome struct {
	Name string `json:"name"`
	To   string `json:"to"`
}

// DefaultOutcome is what `yardr bead done` reports without --outcome, and
// what a stage with a single way out offers without naming it.
const DefaultOutcome = "done"

// validFlowName keeps names usable as file names (.yardr/flows/<name>.flow)
// and in the migrated <rig>.<type> form.
var validFlowName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var validStage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

var validOutcome = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// normalize fills Stages from the transitions and terminal stages (keeping
// any order already given) and checks the flow is well formed.
func (f *Flow) normalize() error {
	if f.Transitions == nil {
		f.Transitions = map[string][]string{}
	}
	add := func(s string) error {
		if !validStage.MatchString(s) {
			return fmt.Errorf("flow: stage %q: use letters, digits, '.', '_', '/' or '-'", s)
		}
		if !slices.Contains(f.Stages, s) {
			f.Stages = append(f.Stages, s)
		}
		return nil
	}
	given := f.Stages
	f.Stages = nil
	for _, s := range given {
		if err := add(s); err != nil {
			return err
		}
	}
	// Map order is random; sort sources so derived stage order is stable.
	from := make([]string, 0, len(f.Transitions))
	for s := range f.Transitions {
		from = append(from, s)
	}
	slices.Sort(from)
	for _, s := range from {
		if err := add(s); err != nil {
			return err
		}
		f.Transitions[s] = dedupeOrdered(f.Transitions[s])
		for _, to := range f.Transitions[s] {
			if err := add(to); err != nil {
				return err
			}
		}
	}
	if err := f.normalizeOutcomes(add); err != nil {
		return err
	}
	f.Terminal = dedupeOrdered(f.Terminal)
	for _, s := range f.Terminal {
		if err := add(s); err != nil {
			return err
		}
		if len(f.Transitions[s]) > 0 {
			return fmt.Errorf("flow: terminal stage %s cannot have transitions: entering it closes the bead", s)
		}
	}
	if len(f.Stages) == 0 {
		return fmt.Errorf("flow: no stages")
	}
	return nil
}

// normalizeOutcomes checks each stage's outcomes are named and unique, and
// makes their stages transitions (a JSON flow may give only the outcome).
// It runs before the terminal check, so a terminal stage with outcomes is
// refused like one with transitions.
func (f *Flow) normalizeOutcomes(add func(string) error) error {
	from := make([]string, 0, len(f.Outcomes))
	for s := range f.Outcomes {
		from = append(from, s)
	}
	slices.Sort(from)
	for _, s := range from {
		if len(f.Outcomes[s]) == 0 {
			delete(f.Outcomes, s)
			continue
		}
		if err := add(s); err != nil {
			return err
		}
		var seen []string
		for _, o := range f.Outcomes[s] {
			if !validOutcome.MatchString(o.Name) {
				return fmt.Errorf("flow: outcome %q of stage %s: use letters, digits, '.', '_' or '-'", o.Name, s)
			}
			if slices.Contains(seen, o.Name) {
				return fmt.Errorf("flow: stage %s has outcome %s twice; an outcome leads to one stage", s, o.Name)
			}
			seen = append(seen, o.Name)
			if err := add(o.To); err != nil {
				return err
			}
			if !slices.Contains(f.Transitions[s], o.To) {
				f.Transitions[s] = append(f.Transitions[s], o.To)
			}
		}
	}
	return nil
}

// OutcomesFrom lists how work in stage can end, in the order written. A
// stage that names no outcomes but has exactly one way out offers done for
// it; otherwise a stage without outcomes is left by explicit moves only.
func (f *Flow) OutcomesFrom(stage string) []Outcome {
	if out := f.Outcomes[stage]; len(out) > 0 {
		return slices.Clone(out)
	}
	if next := f.Transitions[stage]; len(next) == 1 {
		return []Outcome{{Name: DefaultOutcome, To: next[0]}}
	}
	return nil
}

// Resolve is the stage outcome sends a bead in stage to; "" means done.
// The error names the outcomes the stage has, or says it has none.
func (f *Flow) Resolve(stage, outcome string) (string, error) {
	outcome = or(strings.TrimSpace(outcome), DefaultOutcome)
	outs := f.OutcomesFrom(stage)
	for _, o := range outs {
		if o.Name == outcome {
			return o.To, nil
		}
	}
	if len(outs) == 0 {
		return "", fmt.Errorf("flow %s names no outcomes for stage %s; move the bead with advance --to (allowed next: %s)",
			f.Name, stage, strings.Join(f.Next(stage), ", "))
	}
	names := make([]string, len(outs))
	for i, o := range outs {
		names[i] = o.Name
	}
	return "", fmt.Errorf("flow %s has no outcome %s for stage %s; outcomes: %s", f.Name, outcome, stage, strings.Join(names, ", "))
}

// Next lists the stages a bead in stage may move to. A stage with no way
// out (one the flow does not know, or a terminal stage after a reopen) may
// move to the flow's first stage only, so such a bead is never stuck but
// cannot skip the flow either: from anywhere else it would jump straight
// to approved or merged.
func (f *Flow) Next(stage string) []string {
	if next := f.Transitions[stage]; len(next) > 0 {
		return slices.Clone(next)
	}
	if len(f.Stages) == 0 || f.Stages[0] == stage {
		return []string{}
	}
	return []string{f.Stages[0]}
}

// Allows reports whether a bead in from may move to to.
func (f *Flow) Allows(from, to string) bool { return slices.Contains(f.Next(from), to) }

func (f *Flow) IsTerminal(stage string) bool { return slices.Contains(f.Terminal, stage) }

// ParseFlow reads a flow file: JSON (a Flow without its name), or one
// rule per line:
//
//	backlog -> new
//	new -> review on done | decide on question | backlog
//	merged terminal
//
// "on <outcome>" names the outcome that takes the edge. Blank lines and
// lines starting with # are ignored.
func ParseFlow(text string) (*Flow, error) {
	f := &Flow{Transitions: map[string][]string{}}
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") {
		if err := json.Unmarshal([]byte(t), f); err != nil {
			return nil, fmt.Errorf("flow: %w", err)
		}
		return f, nil
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if from, to, ok := strings.Cut(line, "->"); ok {
			from = strings.TrimSpace(from)
			if from == "" {
				return nil, fmt.Errorf("flow line %d: missing stage before ->", n)
			}
			f.Stages = append(f.Stages, from)
			for _, edge := range strings.Split(to, "|") {
				fields := strings.Fields(edge)
				switch {
				case len(fields) == 0:
					return nil, fmt.Errorf("flow line %d: empty stage after ->", n)
				case len(fields) == 3 && fields[1] == "on":
					if f.Outcomes == nil {
						f.Outcomes = map[string][]Outcome{}
					}
					f.Outcomes[from] = append(f.Outcomes[from], Outcome{Name: fields[2], To: fields[0]})
				case len(fields) != 1:
					return nil, fmt.Errorf("flow line %d: want 'stage' or 'stage on outcome', got %q", n, strings.TrimSpace(edge))
				}
				f.Transitions[from] = append(f.Transitions[from], fields[0])
				f.Stages = append(f.Stages, fields[0])
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "terminal" {
			f.Terminal = append(f.Terminal, fields[0])
			f.Stages = append(f.Stages, fields[0])
			continue
		}
		return nil, fmt.Errorf("flow line %d: want 'stage -> next on outcome | other' or 'stage terminal', got %q", n, line)
	}
	return f, sc.Err()
}

// String renders the flow in the line format ParseFlow reads.
func (f *Flow) String() string {
	var b strings.Builder
	for _, s := range f.Stages {
		if next := f.Transitions[s]; len(next) > 0 {
			fmt.Fprintf(&b, "%s -> %s\n", s, strings.Join(f.Edges(s), " | "))
		}
	}
	for _, s := range f.Terminal {
		fmt.Fprintf(&b, "%s terminal\n", s)
	}
	return b.String()
}

// Edges renders stage's transitions as a flow file writes them: "<to> on
// <outcome>" once per outcome that leads there, a bare "<to>" when none does.
func (f *Flow) Edges(stage string) []string {
	out := make([]string, 0, len(f.Transitions[stage]))
	for _, to := range f.Transitions[stage] {
		named := false
		for _, o := range f.Outcomes[stage] {
			if o.To == to {
				out, named = append(out, to+" on "+o.Name), true
			}
		}
		if !named {
			out = append(out, to)
		}
	}
	return out
}

// PutFlow saves (replacing) the named flow f.Name. Every rig and type that
// points at the name follows the new version at once.
func (s *Store) PutFlow(ctx context.Context, f Flow, actor string) (*Flow, error) {
	if !validFlowName.MatchString(f.Name) {
		return nil, fmt.Errorf("flow name %q: use letters, digits, '.', '_' or '-'", f.Name)
	}
	if err := f.normalize(); err != nil {
		return nil, err
	}
	err := s.write(func(w *wtx) error {
		if err := put(w.Tx, bFlows, f.Name, f); err != nil {
			return err
		}
		return w.event("", "flow_set", actor, map[string]any{"flow": f.Name})
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Flows lists the named flows in the store, by name.
func (s *Store) Flows(ctx context.Context) ([]Flow, error) {
	out := []Flow{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bFlows, "", func(_ []byte, f *Flow) error {
			out = append(out, *f)
			return nil
		})
	})
	return out, err
}

// GetFlow returns the named flow from the store (not the repository).
func (s *Store) GetFlow(ctx context.Context, name string) (*Flow, error) {
	f := new(Flow)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bFlows, name, f)
		if err == nil && !ok {
			err = fmt.Errorf("flow %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// DeleteFlow removes a named flow. It refuses while a rig or type points at
// it, or an open bead pins it, and names them: removing the flow would
// silently change their lifecycle.
func (s *Store) DeleteFlow(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		k := []byte(name)
		if w.Bucket(bFlows).Get(k) == nil {
			return fmt.Errorf("flow %s: %w", name, ErrNotFound)
		}
		users, err := flowUsers(w.Tx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("flow %s is in use by %s; point them at another flow first", name, strings.Join(users, ", "))
		}
		if err := w.Bucket(bFlows).Delete(k); err != nil {
			return err
		}
		return w.event("", "flow_removed", actor, map[string]any{"flow": name})
	})
}

// flowUsers names what depends on the named flow: rigs and types pointing
// at it, and open beads pinning it.
func flowUsers(tx *bolt.Tx, name string) ([]string, error) {
	var out []string
	err := each(tx, bFlowPtrs, "", func(_ []byte, p *FlowPointer) error {
		if p.Flow == name {
			out = append(out, p.String())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status != "closed" && PinnedFlow(&r.Bead) == name {
			out = append(out, "bead "+r.ID)
		}
		return nil
	})
	return out, err
}

// FlowPointer says which named flow governs a rig's beads, or, with Type,
// the rig's beads of that type.
type FlowPointer struct {
	Rig  string `json:"rig"`
	Type string `json:"type,omitempty"`
	Flow string `json:"flow"`
}

func (p FlowPointer) String() string {
	if p.Type != "" {
		return "rig " + p.Rig + " type " + p.Type
	}
	return "rig " + p.Rig
}

// SetFlowPointer points rig, or with typ its beads of that type, at the
// named flow; an empty flow clears the pointer. The flow must exist, in the
// store or in the rig's repository.
func (s *Store) SetFlowPointer(ctx context.Context, rig, typ, flow, actor string) error {
	inRepo := false
	if flow != "" {
		if _, err := s.GetFlow(ctx, flow); errors.Is(err, ErrNotFound) {
			repo, err := s.RepoFlows(ctx, rig)
			if err != nil {
				return err
			}
			if repo.find(flow, "") == nil {
				return fmt.Errorf("flow %s: %w (not in the store, nor in rig %s's %s %s)", flow, ErrNotFound, rig, RepoFlowDir, repo.where())
			}
			inRepo = true
		} else if err != nil {
			return err
		}
	}
	p := FlowPointer{Rig: rig, Type: typ, Flow: flow}
	return s.write(func(w *wtx) error {
		if w.Bucket(bRigs).Get([]byte(rig)) == nil {
			return fmt.Errorf("rig %s: %w", rig, ErrNotFound)
		}
		k := []byte(join(rig, typ))
		data := map[string]any{"rig": rig, "type": typ}
		if flow == "" {
			if w.Bucket(bFlowPtrs).Get(k) == nil {
				return fmt.Errorf("%s points at no flow: %w", p, ErrNotFound)
			}
			if err := w.Bucket(bFlowPtrs).Delete(k); err != nil {
				return err
			}
			return w.event("", "flow_pointer_cleared", actor, data)
		}
		// Checked again here: the flow may have been removed meanwhile.
		if !inRepo && w.Bucket(bFlows).Get([]byte(flow)) == nil {
			return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
		}
		if err := put(w.Tx, bFlowPtrs, string(k), p); err != nil {
			return err
		}
		data["flow"] = flow
		return w.event("", "flow_pointer_set", actor, data)
	})
}

// FlowPointers lists every rig's pointers, the rig's own before its types'.
func (s *Store) FlowPointers(ctx context.Context) ([]FlowPointer, error) {
	out := []FlowPointer{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bFlowPtrs, "", func(_ []byte, p *FlowPointer) error {
			out = append(out, *p)
			return nil
		})
	})
	return out, err
}

// PinnedFlow is the flow a bead names itself (metadata flow=<name>), or "".
func PinnedFlow(b *Bead) string {
	v, _ := b.Metadata["flow"].(string)
	return strings.TrimSpace(v)
}

// FlowSource says where the flow governing a bead came from.
type FlowSource struct {
	Kind string `json:"kind"`           // pin, store or repo
	Type string `json:"type,omitempty"` // the bead type whose pointer or file matched; "" for the rig's
	// Commit is the base commit a repository flow was read at, also when a
	// pin or pointer names a flow found only in the repository. A dir
	// rig's flows have none: they are read from its directory.
	Commit string `json:"commit,omitempty"`
	Path   string `json:"path,omitempty"` // the file in the repository
}

func (s FlowSource) String() string {
	out := s.Kind
	if s.Kind == "repo" && s.Commit != "" {
		out += "@" + shortSHA(s.Commit)
	} else if s.Kind == "repo" {
		out += " (directory)"
	}
	if s.Type != "" {
		out += ", type " + s.Type
	}
	if s.Kind != "repo" && s.Commit != "" {
		out += ", from repo@" + shortSHA(s.Commit)
	}
	return out
}

func shortSHA(sha string) string { return sha[:min(len(sha), 7)] }

// errNeedRepo means the answer depends on the rig's repository flows,
// which are read before the transaction: git must not run inside one.
var errNeedRepo = errors.New("flow resolution needs the rig's repository")

// resolveFlow finds the flow governing a bead of typ in rig that pins
// pin ("" for none):
//
//	pin on the bead > store pointer > repo .yardr/flows > none
//
// Within the store and within the repository the type's flow wins over
// the rig's. A name, pinned or pointed at, is looked up in the store, then
// in the rig's repository. repo is nil until loaded; when the answer
// depends on it, resolveFlow returns errNeedRepo. Knowledge beads are never
// governed, and beads without a rig only by a pin.
func resolveFlow(tx *bolt.Tx, rig *string, typ, pin string, repo *RepoFlows) (*Flow, *FlowSource, error) {
	if NotWork[typ] {
		return nil, nil, nil
	}
	if repo != nil && (rig == nil || repo.Rig != *rig) {
		return nil, nil, errNeedRepo // loaded for another rig
	}
	byName := func(name string, src FlowSource) (*Flow, *FlowSource, error) {
		f := new(Flow)
		if ok, err := get(tx, bFlows, name, f); err != nil || ok {
			return f, &src, err
		}
		if rig == nil {
			return nil, nil, fmt.Errorf("flow %s: %w", name, ErrNotFound)
		}
		if repo == nil {
			return nil, nil, errNeedRepo
		}
		rf := repo.find(name, "")
		if rf == nil {
			return nil, nil, fmt.Errorf("flow %s: %w (not in the store, nor in rig %s's %s %s)", name, ErrNotFound, *rig, RepoFlowDir, repo.where())
		}
		src.Commit, src.Path = repo.Commit, rf.Path
		f, err := rf.flow()
		return f, &src, err
	}
	if pin != "" {
		f, src, err := byName(pin, FlowSource{Kind: "pin"})
		if errors.Is(err, ErrNotFound) {
			err = fmt.Errorf("the bead pins %w", err)
		}
		return f, src, err
	}
	if rig == nil {
		return nil, nil, nil
	}
	types := []string{typ, ""}
	for _, t := range types {
		p := new(FlowPointer)
		if ok, err := get(tx, bFlowPtrs, join(*rig, t), p); err != nil {
			return nil, nil, err
		} else if ok {
			f, src, err := byName(p.Flow, FlowSource{Kind: "store", Type: t})
			if errors.Is(err, ErrNotFound) {
				err = fmt.Errorf("%s points at %w", p, err)
			}
			return f, src, err
		}
	}
	if repo == nil {
		return nil, nil, errNeedRepo
	}
	for _, t := range types {
		if rf := repo.find("default", t); rf != nil {
			f, err := rf.flow()
			return f, &FlowSource{Kind: "repo", Type: t, Commit: repo.Commit, Path: rf.Path}, err
		}
	}
	return nil, nil, nil
}

// repoFor reads the rig's repository flows when resolving the flow of a
// bead of typ in rig pinning pin depends on them; nil when it does not.
func (s *Store) repoFor(ctx context.Context, rig *string, typ, pin string) (*RepoFlows, error) {
	need := false
	_ = s.read(func(tx *bolt.Tx) error {
		_, _, err := resolveFlow(tx, rig, typ, pin, nil)
		need = errors.Is(err, errNeedRepo)
		return nil // any other error surfaces when resolving for real
	})
	if !need {
		return nil, nil
	}
	return s.RepoFlows(ctx, *rig)
}

// changedMeanwhile explains an errNeedRepo met inside a write: the flow
// configuration or the bead changed between reading the repository and
// the transaction.
func changedMeanwhile(err error) error {
	if errors.Is(err, errNeedRepo) {
		return fmt.Errorf("the bead's flow configuration changed meanwhile; try again")
	}
	return err
}

// RigFlow is a flow governing a rig's beads: the rig's own (Type "") or
// the one for its beads of Type.
type RigFlow struct {
	Type   string     `json:"type,omitempty"`
	Flow   *Flow      `json:"flow"`
	Source FlowSource `json:"source"`
}

// RigFlows lists the flows that govern rig's beads (pins aside): the rig's
// own first, then one per type that has its own, by type.
func (s *Store) RigFlows(ctx context.Context, rig string) ([]RigFlow, error) {
	repo, err := s.RepoFlows(ctx, rig)
	if err != nil {
		return nil, err
	}
	out := []RigFlow{}
	err = s.read(func(tx *bolt.Tx) error {
		var types []string
		if err := each(tx, bFlowPtrs, join(rig, ""), func(_ []byte, p *FlowPointer) error {
			if p.Rig == rig && p.Type != "" {
				types = append(types, p.Type)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, rf := range repo.Flows {
			if rf.Type != "" {
				types = append(types, rf.Type)
			}
		}
		slices.Sort(types)
		for _, t := range append([]string{""}, slices.Compact(types)...) {
			f, src, err := resolveFlow(tx, &rig, t, "", repo)
			if err != nil {
				return err
			}
			// A type's file shadowed by a rig pointer in the store does not govern.
			if f != nil && src.Type == t {
				out = append(out, RigFlow{Type: t, Flow: f, Source: *src})
			}
		}
		return nil
	})
	return out, err
}

// NextStages is where a bead may go from its current stage.
type NextStages struct {
	Bead  string `json:"bead"`
	Stage string `json:"stage"`
	// Flow governs the bead; nil means no flow, and any stage is allowed.
	Flow     *Flow       `json:"flow,omitempty"`
	Source   *FlowSource `json:"source,omitempty"`   // where Flow came from
	Stages   []string    `json:"stages"`             // allowed next stages (empty without a flow)
	Terminal []string    `json:"terminal,omitempty"` // those of Stages that close the bead
	// Outcomes are how work in the stage can end, for `yardr bead done`;
	// empty when the flow leaves the stage to explicit moves.
	Outcomes []Outcome `json:"outcomes,omitempty"`
}

// NextStages reports the flow governing bead id and the stages it may be
// advanced to.
func (s *Store) NextStages(ctx context.Context, id string) (*NextStages, error) {
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, b.Rig, b.Type, PinnedFlow(b))
	if err != nil {
		return nil, err
	}
	var out *NextStages
	err = s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, id)
		if err != nil {
			return err
		}
		out = &NextStages{Bead: row.ID, Stage: row.Stage, Stages: []string{}}
		f, src, err := resolveFlow(tx, row.Rig, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil || f == nil {
			return changedMeanwhile(err)
		}
		out.Flow, out.Source, out.Stages = f, src, f.Next(row.Stage)
		out.Outcomes = f.OutcomesFrom(row.Stage)
		for _, st := range out.Stages {
			if f.IsTerminal(st) {
				out.Terminal = append(out.Terminal, st)
			}
		}
		return nil
	})
	return out, err
}

// firstStage is where a bead of flow f starts when its creation names no
// stage: the flow's first stage, or new when no flow governs it.
func firstStage(f *Flow) string {
	if f == nil || len(f.Stages) == 0 {
		return "new"
	}
	return f.Stages[0]
}

// checkCreateStage refuses creating a bead in a stage its flow does not
// have, or in a terminal stage (which would leave it open where it should
// be closed).
func checkCreateStage(f *Flow, stage string) error {
	if f == nil {
		return nil
	}
	if !slices.Contains(f.Stages, stage) {
		return fmt.Errorf("stage %s is not in flow %s; stages: %s", stage, f.Name, strings.Join(f.Stages, ", "))
	}
	if f.IsTerminal(stage) {
		return fmt.Errorf("stage %s is terminal in flow %s; create the bead in an earlier stage", stage, f.Name)
	}
	return nil
}

// checkAdvance refuses a move the flow does not allow, naming the moves it
// does.
func checkAdvance(f *Flow, id, from, to string) error {
	if f == nil || f.Allows(from, to) {
		return nil
	}
	return fmt.Errorf("bead %s: flow %s does not allow %s -> %s; allowed next: %s",
		id, f.Name, from, to, strings.Join(f.Next(from), ", "))
}

// dedupeOrdered drops empty and repeated stages, keeping the first of each.
func dedupeOrdered(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
