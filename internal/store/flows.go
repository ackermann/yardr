package store

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Flow is a rig's bead lifecycle: which stages exist, which moves between
// them are allowed, and which stages end the bead. Routes still decide who
// works a stage; a flow only decides where a bead may go next. A flow with a
// Type governs only beads of that type and wins over the rig's flow; a rig
// without a flow lets beads move to any stage.
type Flow struct {
	Rig         string              `json:"rig"`
	Type        string              `json:"type,omitempty"`
	Stages      []string            `json:"stages"` // in the order they were written
	Transitions map[string][]string `json:"transitions"`
	// Terminal stages close the bead on entry, with the stage as reason.
	Terminal []string `json:"terminal,omitempty"`
}

var validStage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// normalize fills Stages from the transitions and terminal stages (keeping
// any order already given) and checks the flow is well formed.
func (f *Flow) normalize() error {
	if f.Rig == "" {
		return fmt.Errorf("flow: rig is required")
	}
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

// Next lists the stages a bead in stage may move to. A stage with no way
// out (one the flow does not know, or a terminal stage after a reopen) may
// move to any stage of the flow, so such a bead is never stuck.
func (f *Flow) Next(stage string) []string {
	if next := f.Transitions[stage]; len(next) > 0 {
		return slices.Clone(next)
	}
	return slices.DeleteFunc(slices.Clone(f.Stages), func(s string) bool { return s == stage })
}

// Allows reports whether a bead in from may move to to.
func (f *Flow) Allows(from, to string) bool { return slices.Contains(f.Next(from), to) }

func (f *Flow) IsTerminal(stage string) bool { return slices.Contains(f.Terminal, stage) }

func (f *Flow) name() string {
	if f.Type != "" {
		return f.Rig + "/" + f.Type
	}
	return f.Rig
}

func flowKey(rig, typ string) string { return join(rig, typ) }

// ParseFlow reads a flow file: JSON (a Flow without rig and type), or one
// rule per line:
//
//	backlog -> new
//	new -> review | backlog
//	merged terminal
//
// Blank lines and lines starting with # are ignored.
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
			for _, s := range strings.Split(to, "|") {
				s = strings.TrimSpace(s)
				if s == "" {
					return nil, fmt.Errorf("flow line %d: empty stage after ->", n)
				}
				f.Transitions[from] = append(f.Transitions[from], s)
				f.Stages = append(f.Stages, s)
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "terminal" {
			f.Terminal = append(f.Terminal, fields[0])
			f.Stages = append(f.Stages, fields[0])
			continue
		}
		return nil, fmt.Errorf("flow line %d: want 'stage -> next | other' or 'stage terminal', got %q", n, line)
	}
	return f, sc.Err()
}

// String renders the flow in the line format ParseFlow reads.
func (f *Flow) String() string {
	var b strings.Builder
	for _, s := range f.Stages {
		if next := f.Transitions[s]; len(next) > 0 {
			fmt.Fprintf(&b, "%s -> %s\n", s, strings.Join(next, " | "))
		}
	}
	for _, s := range f.Terminal {
		fmt.Fprintf(&b, "%s terminal\n", s)
	}
	return b.String()
}

// PutFlow saves (replacing) the flow for f.Rig, or for f.Rig and f.Type.
func (s *Store) PutFlow(ctx context.Context, f Flow, actor string) (*Flow, error) {
	if err := f.normalize(); err != nil {
		return nil, err
	}
	err := s.write(func(w *wtx) error {
		if w.Bucket(bRigs).Get([]byte(f.Rig)) == nil {
			return fmt.Errorf("rig %s: %w", f.Rig, ErrNotFound)
		}
		if err := put(w.Tx, bFlows, flowKey(f.Rig, f.Type), f); err != nil {
			return err
		}
		return w.event("", "flow_set", actor, map[string]any{"rig": f.Rig, "type": f.Type})
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Flows lists every flow, rig flows before their type-specific ones.
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

// GetFlow returns the flow saved for exactly rig and typ ("" for the rig's
// own flow), without falling back.
func (s *Store) GetFlow(ctx context.Context, rig, typ string) (*Flow, error) {
	f := new(Flow)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bFlows, flowKey(rig, typ), f)
		if err == nil && !ok {
			err = fmt.Errorf("flow %s: %w", (&Flow{Rig: rig, Type: typ}).name(), ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (s *Store) DeleteFlow(ctx context.Context, rig, typ, actor string) error {
	return s.write(func(w *wtx) error {
		k := []byte(flowKey(rig, typ))
		if w.Bucket(bFlows).Get(k) == nil {
			return fmt.Errorf("flow %s: %w", (&Flow{Rig: rig, Type: typ}).name(), ErrNotFound)
		}
		if err := w.Bucket(bFlows).Delete(k); err != nil {
			return err
		}
		return w.event("", "flow_removed", actor, map[string]any{"rig": rig, "type": typ})
	})
}

// flowFor returns the flow governing beads of typ in rig: the type's flow,
// else the rig's, else nil. Beads without a rig and knowledge beads
// (journals, memories) are never governed.
func flowFor(tx *bolt.Tx, rig *string, typ string) (*Flow, error) {
	if rig == nil || NotWork[typ] {
		return nil, nil
	}
	for _, t := range []string{typ, ""} {
		f := new(Flow)
		ok, err := get(tx, bFlows, flowKey(*rig, t), f)
		if err != nil || ok {
			return f, err
		}
	}
	return nil, nil
}

// NextStages is where a bead may go from its current stage.
type NextStages struct {
	Bead  string `json:"bead"`
	Stage string `json:"stage"`
	// Flow governs the bead; nil means no flow, and any stage is allowed.
	Flow     *Flow    `json:"flow,omitempty"`
	Stages   []string `json:"stages"`             // allowed next stages (empty without a flow)
	Terminal []string `json:"terminal,omitempty"` // those of Stages that close the bead
}

// NextStages reports the stages bead id may be advanced to.
func (s *Store) NextStages(ctx context.Context, id string) (*NextStages, error) {
	var out *NextStages
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, id)
		if err != nil {
			return err
		}
		out = &NextStages{Bead: row.ID, Stage: row.Stage, Stages: []string{}}
		f, err := flowFor(tx, row.Rig, row.Type)
		if err != nil || f == nil {
			return err
		}
		out.Flow, out.Stages = f, f.Next(row.Stage)
		for _, st := range out.Stages {
			if f.IsTerminal(st) {
				out.Terminal = append(out.Terminal, st)
			}
		}
		return nil
	})
	return out, err
}

// checkCreateStage refuses creating a bead in a stage its flow does not
// have, or in a terminal stage (which would leave it open where it should
// be closed).
func checkCreateStage(f *Flow, stage string) error {
	if f == nil {
		return nil
	}
	if !slices.Contains(f.Stages, stage) {
		return fmt.Errorf("stage %s is not in flow %s; stages: %s", stage, f.name(), strings.Join(f.Stages, ", "))
	}
	if f.IsTerminal(stage) {
		return fmt.Errorf("stage %s is terminal in flow %s; create the bead in an earlier stage", stage, f.name())
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
		id, f.name(), from, to, strings.Join(f.Next(from), ", "))
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
