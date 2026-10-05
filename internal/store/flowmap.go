package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// FlowMap is the flow governing a bead as a short map: every stage in the
// flow's order, who works it and one line about it, and the path the bead
// took through them. It is context for whoever works the bead (what comes
// before and after, what is somebody else's job); the stage file of the
// stage the bead is in stays the authority on what must hold there.
type FlowMap struct {
	Name   string         `json:"name"`
	Stages []FlowMapStage `json:"stages"`
	// Path is every stage the bead entered, oldest first, starting with the
	// one it was in before its first move; empty for a bead that never moved.
	Path []PathStep `json:"path"`
}

// FlowMapStage is one stage on a FlowMap.
type FlowMapStage struct {
	Name string `json:"name"`
	// Who works a bead like this one in the stage, by the route it would
	// take there: WhoAgents, WhoScript, WhoPeople, WhoEnd for a terminal
	// stage, WhoNobody without a route. The group is not named: it says
	// nothing to someone in another stage.
	Who string `json:"who"`
	// Summary is the stage file's one line (StageFile.Summary); empty for a
	// stage without a file. A store flow's files are in the yard's home,
	// which the store does not read: whoever knows the home fills it in.
	Summary string `json:"summary"`
	Current bool   `json:"current"`
}

// PathStep is one stage a bead entered.
type PathStep struct {
	Stage string `json:"stage"`
	// Back is set where the move went against the flow's order (the bead
	// was sent back, or stayed for another try): the outcome that took it
	// there, or BackByHand for a move that reported none.
	Back string `json:"back,omitempty"`
}

const (
	WhoAgents = "agents"
	WhoScript = "script"
	WhoPeople = "people"
	WhoEnd    = "end"
	WhoNobody = "nobody"

	// BackByHand is a PathStep's Back for a move no outcome named. No
	// outcome can be called this: validOutcome has no space.
	BackByHand = "by hand"
)

// pathShown is how many stages of a bead's path Text prints before it
// leaves the middle out, keeping pathHead from the start and pathTail from
// the end: where the bead began, and how it got where it is.
const (
	pathShown = 8
	pathHead  = 2
	pathTail  = 5
)

// SummaryMax is how long a stage's one line on the map may be, in characters.
const SummaryMax = 100

// summaryPrefix starts the line a stage file may open with to say its one
// line itself, when its first sentence does not serve.
const summaryPrefix = "summary:"

// split parts a stage file's text into what its `summary:` line says and
// the rest. Only the first line that is not blank can be that line.
func (sf *StageFile) split() (summary, body string) {
	if sf == nil {
		return "", ""
	}
	body = strings.TrimLeft(sf.Text, "\r\n")
	first, rest, _ := strings.Cut(body, "\n")
	if s, ok := strings.CutPrefix(first, summaryPrefix); ok {
		return strings.TrimSpace(s), strings.TrimLeft(rest, "\r\n")
	}
	return "", sf.Text
}

// Body is the stage file as whoever works the stage reads it: the text
// without its `summary:` line, which is for the map alone.
func (sf *StageFile) Body() string {
	_, body := sf.split()
	return body
}

// Summary is the stage's one line on a FlowMap: what the file's `summary:`
// line says, else its first sentence, cut on a word to SummaryMax
// characters. Empty without a file.
func (sf *StageFile) Summary() string {
	summary, body := sf.split()
	if summary == "" {
		summary = firstSentence(body)
	}
	return cutOnWord(strings.Join(strings.Fields(summary), " "), SummaryMax)
}

// firstSentence is the first sentence of markdown text: its first
// paragraph on one line, up to the first '.', '!' or '?' that a space or
// the paragraph's end follows, so `docs/flows.md` and `v1.2` do not end
// it. A heading counts as a paragraph, without its marks.
func firstSentence(text string) string {
	var words []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		words = append(words, strings.Fields(strings.TrimLeft(line, "# \t"))...)
	}
	for i, w := range words {
		if strings.ContainsRune(".!?", rune(w[len(w)-1])) {
			return strings.Join(words[:i+1], " ")
		}
	}
	return strings.Join(words, " ")
}

// cutOnWord shortens s to at most max characters, the "…" that marks the
// cut included: at the last space that fits, or within a word longer than
// that, and never inside a character.
func cutOnWord(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := string([]rune(s)[:max-1])
	if i := strings.LastIndexByte(keep, ' '); i > 0 {
		keep = keep[:i]
	}
	return strings.TrimRight(keep, " ") + "…"
}

// HomeSummaries fills in every stage's line from the stage files of the
// store flow m in the yard's home, which the store does not read.
func (m *FlowMap) HomeSummaries(home string) error {
	for i := range m.Stages {
		sf, err := HomeStageFile(home, m.Name, m.Stages[i].Name)
		if err != nil {
			return err
		}
		m.Stages[i].Summary = sf.Summary()
	}
	return nil
}

// Text renders m as the FLOW block of prime and of a brief, which both
// call it so the two cannot drift: a line per stage in the flow's order,
// the bead's stage marked, and the path it took. hasFile says whether the
// bead's stage has a stage file, which the heading then points at: the map
// is context, the file says what must be done.
func (m *FlowMap) Text(hasFile bool) string {
	var b strings.Builder
	hint := "context: it does not say what you must do"
	if hasFile {
		hint = "context: your stage's file below says what you must do"
	}
	plural := "s"
	if len(m.Stages) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "FLOW %s · %d stage%s (%s)\n", m.Name, len(m.Stages), plural, hint)
	name, who := 0, 0
	for _, s := range m.Stages {
		name, who = max(name, len(s.Name)), max(who, len(s.Who))
	}
	for _, s := range m.Stages {
		mark, here := " ", ""
		if s.Current {
			mark, here = ">", "   <- you are here"
		}
		line := fmt.Sprintf("%s %-*s   %-*s   %s", mark, name, s.Name, who, s.Who, s.Summary)
		b.WriteString(strings.TrimRight(line, " ") + here + "\n")
	}
	if len(m.Path) > 0 {
		steps := make([]string, 0, len(m.Path))
		for _, p := range m.Path {
			if p.Back != "" {
				steps = append(steps, fmt.Sprintf("%s (%s)", p.Stage, p.Back))
			} else {
				steps = append(steps, p.Stage)
			}
		}
		if len(steps) > pathShown {
			steps = append(append(steps[:pathHead:pathHead], "…"), steps[len(steps)-pathTail:]...)
		}
		fmt.Fprintf(&b, "PATH  %s\n", strings.Join(steps, " -> "))
	}
	return b.String()
}

// FlowMap draws the flow governing bead id; nil when none does.
func (s *Store) FlowMap(ctx context.Context, id string) (*FlowMap, error) {
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, b.Rig, b.Type, PinnedFlow(b))
	if err != nil {
		return nil, err
	}
	var out *FlowMap
	err = s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, id)
		if err != nil {
			return err
		}
		f, src, err := resolveFlow(tx, row.Rig, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil || f == nil {
			return changedMeanwhile(err)
		}
		var routes []Route
		if err := each(tx, bRoutes, "", func(_ []byte, r *Route) error {
			routes = append(routes, *r)
			return nil
		}); err != nil {
			return err
		}
		SortRoutes(routes)
		out = &FlowMap{Name: f.Name, Stages: make([]FlowMapStage, 0, len(f.Stages))}
		for _, st := range f.Stages {
			who, err := stageWho(tx, f, routes, row.Bead, st)
			if err != nil {
				return err
			}
			out.Stages = append(out.Stages, FlowMapStage{Name: st, Who: who,
				Summary: stageFileOf(f, src, repo, st).Summary(), Current: st == row.Stage})
		}
		out.Path, err = beadPath(tx, f, row.UUID)
		return err
	})
	return out, err
}

// stageWho says who would work b in stage of f: the runner of the group
// the routes send it to there decides, as it does in a brief's outcomes.
func stageWho(tx *bolt.Tx, f *Flow, sorted []Route, b Bead, stage string) (string, error) {
	if f.IsTerminal(stage) {
		return WhoEnd, nil
	}
	b.Stage = stage
	r := MatchRoute(sorted, b)
	if r == nil {
		return WhoNobody, nil
	}
	g := new(Group)
	if ok, err := get(tx, bGroups, r.Group, g); err != nil || !ok {
		return WhoNobody, err
	}
	switch g.Runner {
	case "manual":
		return WhoPeople, nil
	case "exec":
		return WhoScript, nil
	}
	return WhoAgents, nil
}

// beadPath reads the stages the bead entered off its advanced events. A
// move to a stage that is not later in f's order than the one it left is
// marked with its outcome, or as made by hand; a stage f no longer has is
// in no order, and a move from or to it is not marked.
func beadPath(tx *bolt.Tx, f *Flow, uuid string) ([]PathStep, error) {
	path := []PathStep{}
	for _, k := range keys(tx, bEventsBy, uuid+"\x00") {
		var e Event
		if ok, err := get(tx, bEvents, k[len(uuid)+1:], &e); err != nil {
			return nil, err
		} else if !ok || e.Kind != "advanced" {
			continue
		}
		from, _ := e.Data["from"].(string)
		to, _ := e.Data["to"].(string)
		if len(path) == 0 {
			path = append(path, PathStep{Stage: from})
		}
		step := PathStep{Stage: to}
		if i, j := slices.Index(f.Stages, from), slices.Index(f.Stages, to); i >= 0 && j >= 0 && j <= i {
			outcome, _ := e.Data["outcome"].(string)
			step.Back = or(outcome, BackByHand)
		}
		path = append(path, step)
	}
	return path, nil
}
