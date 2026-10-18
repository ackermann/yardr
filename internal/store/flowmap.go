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
	// take there: WhoAgents, WhoScript, WhoPeople, WhoPeer, WhoEnd for a
	// terminal stage, WhoNobody without a route. The group is not named: it
	// says nothing to someone in another stage. Yard is the linked yard
	// that works the stage, for WhoPeer, under this yard's name for it.
	Who  string `json:"who"`
	Yard string `json:"yard,omitempty"`
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
	WhoPeer   = "peer"
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
// the rest. Only the first line that is not blank can be that line. The
// file's `## Ask` section is no part of either: it is the stage's ask
// template, shown nowhere the file is shown.
func (sf *StageFile) split() (summary, body string) {
	if sf == nil {
		return "", ""
	}
	text := cutAskSection(sf.Text)
	body = strings.TrimLeft(text, "\r\n")
	first, rest, _ := strings.Cut(body, "\n")
	if s, ok := strings.CutPrefix(first, summaryPrefix); ok {
		return strings.TrimSpace(s), strings.TrimLeft(rest, "\r\n")
	}
	return "", text
}

// Body is the stage file as whoever works the stage reads it: the text
// without its `summary:` line, which is for the map alone, and without its
// `## Ask` section, which is the stage's ask template.
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
		name, who = max(name, len(s.Name)), max(who, len(s.worker()))
	}
	for _, s := range m.Stages {
		mark, here := " ", ""
		if s.Current {
			mark, here = ">", "   <- you are here"
		}
		line := fmt.Sprintf("%s %-*s   %-*s   %s", mark, name, s.Name, who, s.worker(), s.Summary)
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

// worker is the stage's Who as the map prints it: a stage worked by
// another yard says which.
func (s FlowMapStage) worker() string {
	if s.Who == WhoPeer && s.Yard != "" {
		return s.Who + " " + s.Yard
	}
	return s.Who
}

// FlowMap draws the flow governing bead id; nil when none does.
func (s *Store) FlowMap(ctx context.Context, id string) (*FlowMap, error) {
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, b.Depot, b.Type, PinnedFlow(b))
	if err != nil {
		return nil, err
	}
	var out *FlowMap
	err = s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, id)
		if err != nil {
			return err
		}
		f, src, err := resolveFlow(tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
		if err != nil || f == nil {
			return changedMeanwhile(err)
		}
		routes, err := sortedRoutes(tx)
		if err != nil {
			return err
		}
		out = &FlowMap{Name: f.Name, Stages: make([]FlowMapStage, 0, len(f.Stages))}
		for _, st := range f.Stages {
			who, yard, err := stageWho(tx, f, routes, row.Bead, st)
			if err != nil {
				return err
			}
			out.Stages = append(out.Stages, FlowMapStage{Name: st, Who: who, Yard: yard,
				Summary: stageFileOf(f, src, repo, st).Summary(), Current: st == row.Stage})
		}
		out.Path, err = beadPath(tx, f, row.UUID)
		return err
	})
	return out, err
}

// stageWho says who would work b in stage of f: the runner of the group
// the routes send it to there decides, as it does in a brief's outcomes.
// For a stage another yard works it also says which.
func stageWho(tx *bolt.Tx, f *Flow, sorted []Route, b Bead, stage string) (who, yard string, err error) {
	if f.IsTerminal(stage) {
		return WhoEnd, "", nil
	}
	w, err := stageWork(tx, sorted, b, stage)
	return w.Who, w.Yard, err
}

// StageWork is who works a bead in a stage that is not terminal, with what
// a sentence about it names: the group the routes lead to, a manual
// group's members and its wait, the yard a peer group sends to. It is what
// says who looks first at a bead from another yard (Peer.ArriveByDepot,
// BeadPeer.Arrived): the flow's first stage is the only statement of it.
type StageWork struct {
	Stage string `json:"stage"`
	// Who is WhoPeople, WhoAgents, WhoScript, WhoPeer or WhoNobody.
	Who   string `json:"who"`
	Group string `json:"group,omitempty"`
	// Else are the further groups of the route, in order, for a stage no
	// group has claimed the bead in: which of them takes it is chosen when
	// it is claimed, by the quota then. They all start agents.
	Else []string `json:"else,omitempty"`
	// Members are who may move a bead out of the stage, of a group people
	// work; none, and anyone may. Wait is that group's wait setting, "" for
	// a group that tells nobody of a bead that waits.
	Members []string `json:"members,omitempty"`
	Wait    string   `json:"wait,omitempty"`
	Yard    string   `json:"yard,omitempty"`
}

// stageWork is who would work b in stage, by the route it would take there.
func stageWork(tx *bolt.Tx, sorted []Route, b Bead, stage string) (StageWork, error) {
	b.Stage = stage
	operator, err := operatorIn(tx)
	if err != nil {
		return groupWork(nil, stage), err
	}
	r := MatchRoute(sorted, b, operator)
	if r == nil {
		return groupWork(nil, stage), nil
	}
	g := new(Group)
	if ok, err := get(tx, bGroups, r.Worker(), g); err != nil || !ok {
		return groupWork(nil, stage), err
	}
	w := groupWork(g, stage)
	w.Else = r.Further()
	return w, nil
}

// groupWork is the work of stage as the group g does it; of no group, nobody's.
func groupWork(g *Group, stage string) StageWork {
	if g == nil {
		return StageWork{Stage: stage, Who: WhoNobody}
	}
	w := StageWork{Stage: stage, Who: WhoAgents, Group: g.Name}
	switch g.Runner {
	case "manual":
		w.Who, w.Members = WhoPeople, slices.Clone(g.Members)
		w.Wait, _ = g.Config["wait"].(string)
	case "exec":
		w.Who = WhoScript
	case PeerRunner:
		w.Who, w.Yard = WhoPeer, g.AtPeer()
	}
	return w
}

func (w StageWork) same(o StageWork) bool {
	return w.Stage == o.Stage && w.Who == o.Who && w.Group == o.Group && w.Wait == o.Wait && w.Yard == o.Yard && slices.Equal(w.Members, o.Members) && slices.Equal(w.Else, o.Else)
}

// which names who works the stage, as the clause of a sentence about it.
func (w StageWork) which() string {
	switch w.Who {
	case WhoPeople:
		return "which people work"
	case WhoAgents:
		// Which of a route's groups takes a bead is chosen at the claim.
		switch len(w.Else) {
		case 0:
		case 1:
			return fmt.Sprintf("which agents of the group %s work, or of the group %s while the quota of %s is nearly used", w.Group, w.Else[0], w.Group)
		default:
			return fmt.Sprintf("which agents of the group %s work, or of the groups %s, each while the quota of those before it is nearly used", w.Group, strings.Join(w.Else, ", then "))
		}
		return "which agents of the group " + w.Group + " work"
	case WhoScript:
		return "which the script of the group " + w.Group + " works"
	case WhoPeer:
		return "which the yard " + or(w.Yard, "the group "+w.Group+" does not name") + " works"
	}
	return "which no route leads anywhere from"
}

// then says what happens to a bead from the yard called peer in the stage:
// to each of them for an intake, or to the one that is there. Where work
// starts on another yard's text with nobody of this yard having seen it, an
// intake says so in capitals: the line is all that says it.
func (w StageWork) then(peer string, each bool) string {
	one, it, waits := "it", "it", "it waits"
	if each {
		one, it, waits = "each", "one", "they wait"
	}
	switch w.Who {
	case WhoPeople:
		if len(w.Members) == 0 {
			return fmt.Sprintf("the group %s names no members, so anyone of this yard may move %s on, a crew member too", w.Group, it)
		}
		how := ""
		if each {
			how = " (yardr bead done)"
		}
		return fmt.Sprintf("a member of the group %s (%s) moves %s on%s or closes it", w.Group, strings.Join(w.Members, ", "), one, how)
	case WhoAgents:
		if each {
			return "WORK STARTS ON EACH UNSEEN, on the other yard's text"
		}
		return "work starts on it unseen, on the other yard's text"
	case WhoScript:
		return "it runs on " + one + " unseen"
	case WhoPeer:
		if each {
			return "each is sent on unseen"
		}
		return "it is sent on unseen"
	}
	return fmt.Sprintf("%s until one does (yardr route add --stage %s --group <group> --label from-%s)", waits, w.Stage, peer)
}

// Arrives is what an intake says of the stage for the beads of the yard
// called peer: who works it, and what that does to each.
func (w StageWork) Arrives(peer string) string {
	return w.which() + ": " + w.then(peer, true)
}

// Stands is how a bead that came from the yard called peer says where it
// stands while it is in the stage it arrived in, in the words its peer's
// intake says it in (ArriveLine).
func (w StageWork) Stands(peer string) string {
	return fmt.Sprintf("it arrived in this stage, %s: %s", w.which(), w.then(peer, false))
}

// Seen reports whether someone the yard named looks at a bead in the stage
// before anything else happens to it: people work it, and their group
// names who.
func (w StageWork) Seen() bool { return w.Who == WhoPeople && len(w.Members) > 0 }

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

// PeerLanding is an outcome of a stage a linked yard works that leads to a
// stage where nobody of this yard looks at what came back: a terminal
// stage, or one a script works. The outcome in a return is the other
// yard's word (returnedBead), so what bounds a false one is the stage it
// leads to, and here that stage is no check. It is a property of the flow
// and the routes as the operator wrote them, and may be meant (a bead
// whose result is words): yardr doctor notes it and nothing refuses it.
type PeerLanding struct {
	// Stage is the stage the yard called Yard works, Outcome one of its
	// outcomes and To the stage that leads to.
	Stage   string `json:"stage"`
	Yard    string `json:"yard,omitempty"`
	Outcome string `json:"outcome"`
	To      string `json:"to"`
	// Who is WhoEnd or WhoScript, and Group the script's group.
	Who   string `json:"who"`
	Group string `json:"group,omitempty"`
}

// Leads says where the outcome leads, as a clause about the peer's stage.
func (l PeerLanding) Leads() string {
	if l.Who == WhoEnd {
		return fmt.Sprintf("its outcome %s leads to %s, which is terminal", l.Outcome, l.To)
	}
	return fmt.Sprintf("its outcome %s leads to %s, %s", l.Outcome, l.To, StageWork{Stage: l.To, Who: l.Who, Group: l.Group}.which())
}

// PeerLandings lists the landings of f as it governs depot's beads of typ
// ("" for the depot's own flow, whose beads are of any type but those in
// typed, which have a flow of their own), in the order of f's stages and
// of each stage's outcomes.
//
// Which group works a stage depends on the bead, since a route may ask for
// a type or a label. So a stage counts as a peer's when some bead of the
// flow may be routed to a peer group there, and the stage an outcome leads
// to as a script's when such a bead may be routed to a script there: the
// note is about a bead that can land unseen, not about every bead.
func PeerLandings(f *Flow, routes []Route, groups []Group, depot, typ string, typed []string) []PeerLanding {
	byName := map[string]*Group{}
	for i := range groups {
		byName[groups[i].Name] = &groups[i]
	}
	sorted := slices.Clone(routes)
	SortRoutes(sorted)
	// may lists routes a bead may reach in stage. For the depot's own flow,
	// check each route type (except types with their own flow) and one type
	// with no type-specific route. A peer route can only be reached for one
	// of those types; treating "any type" as a single value misses type
	// routes that shadow it.
	types := []string{typ}
	if typ == "" {
		types = nil
		for _, r := range sorted {
			if r.Type != nil && !slices.Contains(typed, *r.Type) && !slices.Contains(types, *r.Type) {
				types = append(types, *r.Type)
			}
		}
		other := "\x00yardr-other-type"
		for slices.Contains(typed, other) || slices.ContainsFunc(sorted, func(r Route) bool { return r.Type != nil && *r.Type == other }) {
			other += "_"
		}
		types = append(types, other)
	}
	may := func(stage, typ string, labels []string) []Route {
		var out []Route
		for _, r := range sorted {
			if r.Stage != stage || r.Depot != nil && *r.Depot != depot {
				continue
			}
			if r.Type != nil && *r.Type != typ {
				continue
			}
			// A route before r takes every bead r would when it asks for
			// nothing r does not: each of its labels is one the bead is
			// known to have or one r asks for, and its priorities include
			// r's.
			has := append(slices.Clone(labels), r.Labels()...)
			blocked := slices.ContainsFunc(out, func(prior Route) bool {
				return !slices.ContainsFunc(prior.Labels(), func(l string) bool { return !slices.Contains(has, l) }) &&
					(prior.BeadPriority == nil || r.BeadPriority != nil && *r.BeadPriority <= *prior.BeadPriority)
			})
			if !blocked {
				out = append(out, r)
			}
		}
		return out
	}
	var out []PeerLanding
	add := func(l PeerLanding) {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	for _, stage := range f.Stages {
		if f.IsTerminal(stage) {
			continue
		}
		for _, beadType := range types {
			for _, r := range may(stage, beadType, nil) {
				g := byName[r.Worker()]
				if g == nil || g.Runner != PeerRunner {
					continue
				}
				labels := r.Labels()
				for _, o := range f.OutcomesFrom(stage) {
					l := PeerLanding{Stage: stage, Yard: g.AtPeer(), Outcome: o.Name, To: o.To}
					if f.IsTerminal(o.To) {
						l.Who = WhoEnd
						add(l)
						continue
					}
					for _, q := range may(o.To, beadType, labels) {
						if w := groupWork(byName[q.Worker()], o.To); w.Who == WhoScript {
							l.Who, l.Group = w.Who, w.Group
							add(l)
							break
						}
					}
				}
			}
		}
	}
	return out
}
