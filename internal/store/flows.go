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
// once under a name and depots point at them (FlowPointer), or come from the
// depot's repository (RepoFlows); resolveFlow decides which one governs a
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
// and in the migrated <depot>.<type> form.
var validFlowName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var validStage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

var validOutcome = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// normalize fills Stages and checks the flow is well formed. The order
// is the one the rules were written in: the stages listed first, then
// every stage they lead to as its rule names it, by walking the
// transitions from the listed stages. A source stage the walk does not
// reach has no place in that order unless it is the only one left, since
// Transitions and Outcomes are maps and their written order is gone by
// now; with two or more such stages the flow is refused rather than
// sorted. Sorting would make the first stage, where a bead the flow does
// not know rejoins it, an accident of spelling. A flow file in the line
// format lists its stages as it goes, so only a JSON flow, or a Flow
// built in code, can be ambiguous; it gives "stages" in order.
func (f *Flow) normalize() error {
	if f.Transitions == nil {
		f.Transitions = map[string][]string{}
	}
	if err := f.normalizeOutcomes(); err != nil {
		return err
	}
	for s, next := range f.Transitions {
		f.Transitions[s] = dedupeOrdered(next)
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
	for {
		// The walk: each stage's next stages in the order its rule names
		// them, including those of stages the walk itself added.
		for i := 0; i < len(f.Stages); i++ {
			for _, to := range f.Transitions[f.Stages[i]] {
				if err := add(to); err != nil {
					return err
				}
			}
		}
		var left []string
		for s := range f.Transitions {
			if !slices.Contains(f.Stages, s) {
				left = append(left, s)
			}
		}
		if len(left) == 0 {
			break
		}
		if len(left) > 1 {
			slices.Sort(left)
			return fmt.Errorf("flow: the order of stages %s is ambiguous: none of them follows from a stage before it; list every stage under \"stages\" in order", strings.Join(left, ", "))
		}
		if err := add(left[0]); err != nil {
			return err
		}
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
func (f *Flow) normalizeOutcomes() error {
	from := make([]string, 0, len(f.Outcomes))
	for s := range f.Outcomes {
		from = append(from, s)
	}
	slices.Sort(from) // for a stable error, not a stage order
	for _, s := range from {
		if len(f.Outcomes[s]) == 0 {
			delete(f.Outcomes, s)
			continue
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
			if !validStage.MatchString(o.To) {
				return fmt.Errorf("flow: outcome %s of stage %s leads to stage %q: use letters, digits, '.', '_', '/' or '-'", o.Name, s, o.To)
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

// PutFlow saves (replacing) the named flow f.Name. Every depot and type that
// points at the name, and every bead pinning it, follows the new version at
// once, so the save is refused, naming the beads and what leads them to the
// flow, while an open bead among them sits in a stage the new version lacks
// (flowStrays). That is the check SetFlowPointer makes, for the same
// reason: a configuration command does not move beads, nor leave one in a
// stage nobody is routed to.
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
		// With the new version in place, as in SetFlowPointer; an error
		// here rolls the save back.
		strays, err := flowStrays(w.Tx, f)
		if err != nil {
			return err
		}
		if len(strays) > 0 {
			return fmt.Errorf("the new flow %s lacks the stage of open beads %s; advance them to a stage it has, or pin them to another flow (yardr bead update <id> --meta flow=<name>), then set the flow again",
				f.Name, strings.Join(strays, "; "))
		}
		return w.event("", "flow_set", actor, map[string]any{"flow": f.Name})
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// flowStrays names the open beads that follow the stored flow f.Name and
// sit in a stage f lacks, grouped by what leads them to it: "in depot yard:
// <id> (<stage>), ..." per pointer at the name, then "pinning it: ..." for
// beads that name the flow themselves. It runs after f is saved, inside the
// same transaction. A name is looked up in the store before any
// repository, so the beads a pointer reaches resolve to f without the
// depot's repository flows; that also covers a first save under a name the
// pointers so far found in a repository.
func flowStrays(tx *bolt.Tx, f Flow) ([]string, error) {
	var out []string
	var ptrs []FlowPointer
	err := each(tx, bFlowPtrs, "", func(_ []byte, p *FlowPointer) error {
		if p.Flow == f.Name {
			ptrs = append(ptrs, *p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range ptrs {
		strays, err := strayBeads(tx, p.Depot, p.Type, false, nil, nil)
		if err != nil {
			return nil, err
		}
		if len(strays) > 0 {
			out = append(out, "in "+p.String()+": "+strings.Join(strays, ", "))
		}
	}
	var pinned []string
	err = each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status != "closed" && !NotWork[r.Type] && PinnedFlow(&r.Bead) == f.Name && !slices.Contains(f.Stages, r.Stage) {
			pinned = append(pinned, fmt.Sprintf("%s (%s)", r.ID, r.Stage))
		}
		return nil
	})
	if len(pinned) > 0 {
		out = append(out, "pinning it: "+strings.Join(pinned, ", "))
	}
	return out, err
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

// DeleteFlow removes a named flow. It refuses while a depot or type points at
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

// flowUsers names what depends on the named flow: depots and types pointing
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

// FlowPointer says which named flow governs a depot's beads, or, with Type,
// the depot's beads of that type.
type FlowPointer struct {
	Depot string `json:"depot"`
	Type  string `json:"type,omitempty"`
	Flow  string `json:"flow"`
}

func (p FlowPointer) String() string {
	if p.Type != "" {
		return "depot " + p.Depot + " type " + p.Type
	}
	return "depot " + p.Depot
}

// SetFlowPointer points depot, or with typ its beads of that type, at the
// named flow; an empty flow clears the pointer. The flow must exist, in the
// store or in the depot's repository. The change is refused, naming the
// beads, while an open bead it would hand to a new flow sits in a stage
// that flow lacks (strayBeads): such a bead could only rejoin the flow at
// its first stage, and its stage, routed under the old flow, may be nobody's
// under the new. Refusing keeps a configuration command from moving beads;
// the operator moves them under the flow they still follow, or pins them
// to it, and points the depot again.
func (s *Store) SetFlowPointer(ctx context.Context, depot, typ, flow, actor string) error {
	typ = BeadType(typ)
	// Read before the transaction: git must not run inside one. A checkout
	// git cannot read matters only when a flow is looked for in it, so the
	// error waits until then.
	repo, repoErr := s.RepoFlows(ctx, depot)
	inRepo := false
	if flow != "" {
		// Under either spelling of a name that ends in a renamed type, as
		// the pointer is resolved (flowNames).
		var err error
		for _, n := range flowNames(flow) {
			if _, err = s.GetFlow(ctx, n); !errors.Is(err, ErrNotFound) {
				break
			}
		}
		if errors.Is(err, ErrNotFound) {
			if repoErr != nil {
				return repoErr
			}
			if repo.find(flow, "") == nil {
				return fmt.Errorf("flow %s: %w (not in the store, nor in depot %s's %s %s)", flow, ErrNotFound, depot, RepoFlowDir, repo.where())
			}
			inRepo = true
		} else if err != nil {
			return err
		}
	}
	p := FlowPointer{Depot: depot, Type: typ, Flow: flow}
	return s.write(func(w *wtx) error {
		if w.Bucket(bDepots).Get([]byte(depot)) == nil {
			return fmt.Errorf("depot %s: %w", depot, ErrNotFound)
		}
		k := []byte(join(depot, typ))
		data := map[string]any{"depot": depot, "type": typ}
		kind := "flow_pointer_set"
		if flow == "" {
			if w.Bucket(bFlowPtrs).Get(k) == nil {
				return fmt.Errorf("%s points at no flow: %w", p, ErrNotFound)
			}
			if err := w.Bucket(bFlowPtrs).Delete(k); err != nil {
				return err
			}
			kind = "flow_pointer_cleared"
		} else {
			// Checked again here: the flow may have been removed meanwhile.
			if !inRepo && !slices.ContainsFunc(flowNames(flow), func(n string) bool { return w.Bucket(bFlows).Get([]byte(n)) != nil }) {
				return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
			}
			if err := put(w.Tx, bFlowPtrs, string(k), p); err != nil {
				return err
			}
			data["flow"] = flow
		}
		// With the change in place, every bead it affects resolves to the
		// flow that would govern it; an error here rolls the change back.
		strays, err := strayBeads(w.Tx, depot, typ, flow == "", repo, repoErr)
		if err != nil {
			return err
		}
		if len(strays) > 0 {
			return fmt.Errorf("%s: %s; advance them to a stage the flow has, or pin them to the flow they follow now (yardr bead update <id> --meta flow=<name>), then point the depot again",
				p.change(), strings.Join(strays, ", "))
		}
		return w.event("", kind, actor, data)
	})
}

// change describes the pointer change for an error about it.
func (p FlowPointer) change() string {
	if p.Flow == "" {
		return "clearing the flow of " + p.String() + " would leave open beads in stages their flow lacks"
	}
	return "flow " + p.Flow + " lacks the stage of open beads in " + p.String()
}

// strayBeads names the open beads a change to the pointer (depot, typ)
// hands to a flow that lacks their stage, as "<id> (<stage>)". It runs
// after the change, inside its transaction, so each bead resolves to the
// flow that would govern it; cleared says the pointer was removed, which
// may leave each type to its own flow, so the flow is named per bead. A
// pinned bead follows its pin and a knowledge bead no flow, so neither is
// affected; the depot's pointer does not reach beads whose type has its own
// pointer. repo is the depot's repository flows, or nil with repoErr saying
// why they could not be read; a bead whose flow would come from there
// fails with it.
func strayBeads(tx *bolt.Tx, depot, typ string, cleared bool, repo *RepoFlows, repoErr error) ([]string, error) {
	var out []string
	err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status == "closed" || r.Depot == nil || *r.Depot != depot || NotWork[r.Type] || PinnedFlow(&r.Bead) != "" {
			return nil
		}
		if typ != "" && r.Type != typ {
			return nil
		}
		if typ == "" && tx.Bucket(bFlowPtrs).Get([]byte(join(depot, r.Type))) != nil {
			return nil
		}
		f, _, err := lacksStage(tx, r, repo, nil)
		if errors.Is(err, errNeedRepo) && repoErr != nil {
			err = repoErr
		}
		if err != nil {
			return fmt.Errorf("bead %s: %w", r.ID, err)
		}
		if f == nil {
			return nil
		}
		if cleared {
			out = append(out, fmt.Sprintf("%s (%s, not in flow %s)", r.ID, r.Stage, f.Name))
		} else {
			out = append(out, fmt.Sprintf("%s (%s)", r.ID, r.Stage))
		}
		return nil
	})
	return out, err
}

// lacksStage returns the flow governing r, and where it came from, when r
// is an open work bead in a stage that flow lacks; nil when its stage is in
// the flow, or no flow governs it. It is the one test for a stranded bead:
// strayBeads asks it of a change before the change is made, Stranded of the
// store as it is, with a memo of the flows it has found.
func lacksStage(tx *bolt.Tx, r *beadRow, repo *RepoFlows, memo flowMemo) (*Flow, *FlowSource, error) {
	if r.Status == "closed" || NotWork[r.Type] {
		return nil, nil, nil
	}
	f, src, err := memo.resolve(tx, r.Depot, r.Type, PinnedFlow(&r.Bead), repo)
	if err != nil || f == nil || slices.Contains(f.Stages, r.Stage) {
		return nil, nil, err
	}
	return f, src, nil
}

// flowMemo keeps what resolveFlow answered for a depot, a type and a pin,
// for a read that asks of every open bead: a yard's beads follow a handful
// of flows, and each answer decodes one. It holds for one transaction and
// one reading of each depot's repository; a nil memo keeps nothing. The
// flow and source it hands out are shared, and only read.
type flowMemo map[flowAsk]flowAnswer

type flowAsk struct {
	depot    string
	hasDepot bool
	typ, pin string
}

type flowAnswer struct {
	flow *Flow
	src  *FlowSource
	err  error
}

func (m flowMemo) resolve(tx *bolt.Tx, depot *string, typ, pin string, repo *RepoFlows) (*Flow, *FlowSource, error) {
	if m == nil {
		return resolveFlow(tx, depot, typ, pin, repo)
	}
	ask := flowAsk{typ: typ, pin: pin}
	if depot != nil {
		ask.depot, ask.hasDepot = *depot, true
	}
	a, ok := m[ask]
	if !ok {
		a.flow, a.src, a.err = resolveFlow(tx, depot, typ, pin, repo)
		m[ask] = a
	}
	return a.flow, a.src, a.err
}

// StrandedBead is an open bead in a stage the flow governing it lacks.
type StrandedBead struct {
	Bead   string     `json:"bead"`
	Stage  string     `json:"stage"`
	Flow   string     `json:"flow"`
	Source FlowSource `json:"source"` // where Flow came from, with its commit
}

func (b StrandedBead) String() string {
	return fmt.Sprintf("%s is in stage %s, which flow %s (%s) lacks", b.Bead, b.Stage, b.Flow, b.Source)
}

// Stranded lists depot's open beads that sit in a stage the flow governing
// them lacks, by id; with an empty depot, every such bead in the store. The
// commands that change a flow refuse to leave one (PutFlow,
// SetFlowPointer), so these come from a repository flow that lost a stage
// when the base branch moved, which no command can refuse, or from before
// the depot had a flow. A bead whose flow does not resolve is not listed: its
// moves are refused and the dispatcher reports it as bad_flow.
//
// A stranded bead is open by definition (lacksStage), so only the open
// beads are read, through the index: the web view asks on every page and
// the dispatcher on every pass, and the answer should not cost what the
// yard has closed.
func (s *Store) Stranded(ctx context.Context, depot string) ([]StrandedBead, error) {
	return s.stranded(ctx, depot, s.openBeads)
}

// stranded is Stranded over the beads rows hands it, which it only reads.
// Each bead is decoded at most once, and each flow once for the beads of a
// depot, a type and a pin: the dispatcher asks on every pass, and decoding
// the open beads, and the flow again for each, was most of what an idle
// server did at 20,000 beads.
func (s *Store) stranded(ctx context.Context, depot string, rows func(*bolt.Tx) ([]*beadRow, error)) ([]StrandedBead, error) {
	in := func(r *beadRow) bool { return depot == "" || (r.Depot != nil && *r.Depot == depot) }
	out := []StrandedBead{}
	// The beads whose answer depends on their depot's repository, as rows
	// handed them over, and those depots: the repositories are read after
	// the transaction, since git must not run inside one, and these beads
	// are answered then, without reading them again.
	var held []*beadRow
	var need []string
	err := s.read(func(tx *bolt.Tx) error {
		all, err := rows(tx)
		if err != nil {
			return err
		}
		memo := flowMemo{}
		for _, r := range all {
			if !in(r) {
				continue
			}
			f, src, err := lacksStage(tx, r, nil, memo)
			switch {
			case errors.Is(err, errNeedRepo) && r.Depot != nil:
				held = append(held, r)
				if !slices.Contains(need, *r.Depot) {
					need = append(need, *r.Depot)
				}
			case err == nil && f != nil:
				out = append(out, StrandedBead{Bead: r.ID, Stage: r.Stage, Flow: f.Name, Source: *src})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(held) > 0 {
		repos := map[string]*RepoFlows{}
		for _, name := range need {
			// A checkout git cannot read leaves its beads to bad_flow.
			if rf, err := s.RepoFlows(ctx, name); err == nil {
				repos[name] = rf
			}
		}
		err = s.read(func(tx *bolt.Tx) error {
			memo := flowMemo{}
			for _, r := range held {
				if f, src, err := lacksStage(tx, r, repos[*r.Depot], memo); err == nil && f != nil {
					out = append(out, StrandedBead{Bead: r.ID, Stage: r.Stage, Flow: f.Name, Source: *src})
				}
			}
			return nil
		})
	}
	slices.SortFunc(out, func(a, b StrandedBead) int { return strings.Compare(a.Bead, b.Bead) })
	return out, err
}

// FlowPointers lists every depot's pointers, the depot's own before its types'.
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
	Type string `json:"type,omitempty"` // the bead type whose pointer or file matched; "" for the depot's
	// Commit is the base commit a repository flow was read at, also when a
	// pin or pointer names a flow found only in the repository. A dir
	// depot's flows have none: they are read from its directory.
	Commit string `json:"commit,omitempty"`
	Path   string `json:"path,omitempty"` // the file in the repository
}

// InStore reports whether the flow itself is in the store, rather than a
// file in the depot's repository: its stage files are then in the yard's
// home (HomeStageFile), not in the repository.
func (s FlowSource) InStore() bool { return s.Path == "" }

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

// errNeedRepo means the answer depends on the depot's repository flows,
// which are read before the transaction: git must not run inside one.
var errNeedRepo = errors.New("flow resolution needs the depot's repository")

// resolveFlow finds the flow governing a bead of typ in depot that pins
// pin ("" for none):
//
//	pin on the bead > store pointer > repo .yardr/flows > none
//
// Within the store and within the repository the type's flow wins over
// the depot's. A name, pinned or pointed at, is looked up in the store, then
// in the depot's repository. repo is nil until loaded; when the answer
// depends on it, resolveFlow returns errNeedRepo. Knowledge beads are never
// governed, and beads without a depot only by a pin.
func resolveFlow(tx *bolt.Tx, depot *string, typ, pin string, repo *RepoFlows) (*Flow, *FlowSource, error) {
	if NotWork[typ] {
		return nil, nil, nil
	}
	if repo != nil && (depot == nil || repo.Depot != *depot) {
		return nil, nil, errNeedRepo // loaded for another depot
	}
	byName := func(name string, src FlowSource) (*Flow, *FlowSource, error) {
		for _, n := range flowNames(name) {
			f := new(Flow)
			if ok, err := get(tx, bFlows, n, f); err != nil || ok {
				return f, &src, err
			}
		}
		if depot == nil {
			return nil, nil, fmt.Errorf("flow %s: %w", name, ErrNotFound)
		}
		if repo == nil {
			return nil, nil, errNeedRepo
		}
		rf := repo.find(name, "")
		if rf == nil {
			return nil, nil, fmt.Errorf("flow %s: %w (not in the store, nor in depot %s's %s %s)", name, ErrNotFound, *depot, RepoFlowDir, repo.where())
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
	if depot == nil {
		return nil, nil, nil
	}
	types := []string{typ, ""}
	for _, t := range types {
		p := new(FlowPointer)
		if ok, err := get(tx, bFlowPtrs, join(*depot, t), p); err != nil {
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

// repoFor reads the depot's repository flows when resolving the flow of a
// bead of typ in depot pinning pin depends on them; nil when it does not.
func (s *Store) repoFor(ctx context.Context, depot *string, typ, pin string) (*RepoFlows, error) {
	need := false
	_ = s.read(func(tx *bolt.Tx) error {
		_, _, err := resolveFlow(tx, depot, typ, pin, nil)
		need = errors.Is(err, errNeedRepo)
		return nil // any other error surfaces when resolving for real
	})
	if !need {
		return nil, nil
	}
	return s.RepoFlows(ctx, *depot)
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

// stageFileOf is the stage file of stage in f, which came from src: a
// repository flow's is the file beside it in repo, read with it. A store
// flow has none in any repository, whatever a depot holds under its name.
func stageFileOf(f *Flow, src *FlowSource, repo *RepoFlows, stage string) *StageFile {
	if src.InStore() {
		return nil
	}
	return repo.StageFile(f.Name, stage)
}

// stageFilesOf maps each stage of f that has a stage file in repo to the
// file's path.
func stageFilesOf(f *Flow, src *FlowSource, repo *RepoFlows) map[string]string {
	var out map[string]string
	for _, st := range f.Stages {
		if sf := stageFileOf(f, src, repo, st); sf != nil {
			if out == nil {
				out = map[string]string{}
			}
			out[st] = sf.Path
		}
	}
	return out
}

// DepotFlow is a flow governing a depot's beads: the depot's own (Type "") or
// the one for its beads of Type.
type DepotFlow struct {
	Type   string     `json:"type,omitempty"`
	Flow   *Flow      `json:"flow"`
	Source FlowSource `json:"source"`
	// StageFiles maps each stage of a repository flow that has a stage
	// file to the file's path in the depot's repository. A store flow's are
	// files in the yard's home, which the store does not read
	// (HomeStageFiles).
	StageFiles map[string]string `json:"stage_files,omitempty"`
}

// DepotFlows lists the flows that govern depot's beads (pins aside): the depot's
// own first, then one per type that has its own, by type.
func (s *Store) DepotFlows(ctx context.Context, depot string) ([]DepotFlow, error) {
	repo, err := s.RepoFlows(ctx, depot)
	if err != nil {
		return nil, err
	}
	out := []DepotFlow{}
	err = s.read(func(tx *bolt.Tx) error {
		var types []string
		if err := each(tx, bFlowPtrs, join(depot, ""), func(_ []byte, p *FlowPointer) error {
			if p.Depot == depot && p.Type != "" {
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
			f, src, err := resolveFlow(tx, &depot, t, "", repo)
			if err != nil {
				return err
			}
			// A type's file shadowed by a depot pointer in the store does not govern.
			if f != nil && src.Type == t {
				out = append(out, DepotFlow{Type: t, Flow: f, Source: *src, StageFiles: stageFilesOf(f, src, repo)})
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
	// StageFile is a repository flow's definition of Stage, when it has
	// one. A store flow's is a file in the yard's home, which the store
	// does not read: whoever knows the home asks HomeStageFile.
	StageFile *StageFile `json:"stage_file,omitempty"`
}

// NextStages reports the flow governing bead id, the stages it may be
// advanced to, and a repository flow's stage file of the stage it is in.
func (s *Store) NextStages(ctx context.Context, id string) (*NextStages, error) {
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, b.Depot, b.Type, PinnedFlow(b))
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
		f, src, err := resolveFlow(tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil || f == nil {
			return changedMeanwhile(err)
		}
		out.Flow, out.Source, out.Stages = f, src, f.Next(row.Stage)
		out.Outcomes = f.OutcomesFrom(row.Stage)
		out.StageFile = stageFileOf(f, src, repo, row.Stage)
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
