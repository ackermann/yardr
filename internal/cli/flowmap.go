package cli

import (
	"context"
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// pathShown is how many stages of a bead's path prime prints before it
// leaves the middle out, keeping pathHead from the start and pathTail from
// the end: where the bead began, and how it got where it is.
const (
	pathShown = 8
	pathHead  = 2
	pathTail  = 5
)

// flowMap is the map of the flow governing bead id, for prime; nil when no
// flow governs it. next is what NextStages said of the bead.
func (a *app) flowMap(ctx context.Context, st store.API, id string, next *store.NextStages) (*store.FlowMap, error) {
	if next == nil || next.Flow == nil {
		return nil, nil
	}
	m, err := st.FlowMap(ctx, id)
	if err != nil || m == nil {
		return nil, err
	}
	// A store flow's stage files are files in the yard's home, which the
	// store does not read.
	if next.Source.InStore() {
		for i := range m.Stages {
			sf, err := store.HomeStageFile(a.home(), m.Name, m.Stages[i].Name)
			if err != nil {
				return nil, err
			}
			m.Stages[i].Summary = sf.Summary()
		}
	}
	return m, nil
}

// flowMapText renders m as prime's FLOW block: a line per stage in the
// flow's order, the bead's stage marked, and the path it took. hasFile
// says whether the bead's stage has a stage file, which the heading then
// points at: the map is context, the file says what must be done.
func flowMapText(m *store.FlowMap, hasFile bool) string {
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
