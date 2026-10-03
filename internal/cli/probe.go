package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// probeModel asks an agent for one answer from a model (agents.ProbeModel);
// tests replace it.
var probeModel = agents.ProbeModel

// noProbeHelp describes the --no-probe flag wherever a model is set.
const noProbeHelp = "do not ask the agent whether it accepts the model"

// modelWarning asks the agent of a herdr config whether it accepts the
// config's model and returns what to tell the operator when it does not,
// "" when it does, when no model is set or when the kind has no one-shot
// mode to ask with. It only ever warns: a probe also fails for an agent
// that is offline or not logged in, and the daemon's machine, not this
// one, is where the agent finally runs.
func modelWarning(ctx context.Context, who, runner string, cfg map[string]any) string {
	if runner != "herdr" {
		return ""
	}
	model, _ := cfg["model"].(string)
	if model == "" {
		return ""
	}
	kind, _ := cfg["kind"].(string)
	if kind == "" {
		kind = "claude" // the herdr runner's default
	}
	return probeWarning(who, kind, model, probeModel(ctx, kind, model))
}

// probeWarning words a probe's outcome for who ("group builders").
func probeWarning(who, kind, model string, p agents.Probe) string {
	switch {
	case p.Cmd == "" || p.OK:
		return ""
	case !p.Checked:
		return fmt.Sprintf("warning: %s: could not check model %q with %s (%s: %s). Saved as given; a session whose agent refuses its model is reported as session_stalled.",
			who, model, kind, p.Cmd, p.Detail)
	}
	return fmt.Sprintf("warning: %s: %s did not answer with model %q (%s):\n    %s\n"+
		"  Saved as given: the probe also fails when the agent is offline or not logged in (--no-probe skips it). "+
		"If the model is wrong, its sessions sit idle on their first prompt (session_stalled).",
		who, kind, model, p.Cmd, strings.ReplaceAll(p.Detail, "\n", "\n    "))
}

// warnModel prints modelWarning's warning, if any, on stderr.
func warnModel(ctx context.Context, who, runner string, cfg map[string]any) {
	if w := modelWarning(ctx, who, runner, cfg); w != "" {
		fmt.Fprintln(stderr, w)
	}
}

// warnBeadModel checks a bead's metadata.model, which replaces the model of
// every herdr group that works the bead: each agent kind among the groups
// its routes may send it to is asked, since a model one kind accepts is
// another's stall (a claude model on a bead that codex reviews).
func warnBeadModel(ctx context.Context, st store.API, b *store.Bead) {
	model, _ := b.Metadata["model"].(string)
	if model == "" {
		return
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return
	}
	// kind -> the groups of that kind the bead may reach, whatever its stage.
	byKind := map[string][]string{}
	for _, r := range routes {
		at := *b
		at.Stage = r.Stage
		if store.MatchRoute([]store.Route{r}, at) == nil {
			continue
		}
		i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == r.Group })
		if i < 0 || groups[i].Runner != "herdr" {
			continue
		}
		kind, _ := groups[i].Config["kind"].(string)
		if kind == "" {
			kind = "claude"
		}
		if !slices.Contains(byKind[kind], r.Group) {
			byKind[kind] = append(byKind[kind], r.Group)
		}
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	warnings := make([]string, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := fmt.Sprintf("bead %s (worked by %s)", b.ID, strings.Join(byKind[kind], ", "))
			warnings[i] = probeWarning(who, kind, model, probeModel(ctx, kind, model))
		}()
	}
	wg.Wait()
	for _, w := range warnings {
		if w != "" {
			fmt.Fprintln(stderr, w)
		}
	}
}
