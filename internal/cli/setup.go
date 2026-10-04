package cli

import (
	"context"
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// setupStep is one step from a yard that exists to a first merged bead:
// done or not, what shows it (or what is lacking), and the command that
// does it when it is not.
type setupStep struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
	// Detail is what in the store shows the step done, or what is lacking.
	Detail string `json:"detail,omitempty"`
	// Next is how to do the step, when it is not done.
	Next string `json:"next,omitempty"`
}

// The setup steps' keys, in their order.
const (
	stepRig       = "rig"
	stepRigFiles  = "rig_files"
	stepBuilders  = "builders"
	stepReviewers = "reviewers"
	stepLanding   = "landing"
	stepRoutes    = "routes"
	stepFirstBead = "first_bead"
	stepMerged    = "merged_bead"
)

// setupState is what the setup steps are read from.
type setupState struct {
	rigs   []store.Rig
	groups []store.Group
	routes []store.Route
	beads  []store.Bead // closed ones too
	// flows are the flows on each rig's base (in a dir rig's directory), by
	// rig; flowErr is why a rig's could not be read.
	flows   map[string]int
	flowErr map[string]string
	// pointed are the rigs that follow a flow in the store (a pack's, say)
	// rather than their own file, with the flow's name.
	pointed map[string]string
}

// setupSteps are the steps of a yard's setup, each done or not, in the
// order they are taken. They are read from the store and nothing else (the
// store reads a rig's files from its base), so everything that walks a
// person through the setup sees the same list and can take over from any
// other: yardr doctor, the mayor on its first run, a guided form. The first
// step not done is where the setup goes on (nextStep).
func setupSteps(ctx context.Context, st store.API) ([]setupStep, error) {
	var s setupState
	var err error
	if s.rigs, err = st.Rigs(ctx); err != nil {
		return nil, err
	}
	if s.groups, err = st.Groups(ctx); err != nil {
		return nil, err
	}
	if s.routes, err = st.Routes(ctx); err != nil {
		return nil, err
	}
	if s.beads, err = st.ListBeads(ctx, store.BeadFilter{All: true}); err != nil {
		return nil, err
	}
	s.flows, s.flowErr, s.pointed = map[string]int{}, map[string]string{}, map[string]string{}
	ptrs, err := st.FlowPointers(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ptrs {
		if p.Type == "" {
			s.pointed[p.Rig] = p.Flow
		}
	}
	for _, r := range s.rigs {
		if s.pointed[r.Name] != "" {
			continue // its base is not where its flow is read
		}
		rf, err := st.RepoFlows(ctx, r.Name)
		if err != nil {
			// A base that does not resolve is a step not done, not a
			// failure of the list.
			s.flowErr[r.Name] = err.Error()
			continue
		}
		s.flows[r.Name] = len(rf.Flows)
	}
	return s.steps(), nil
}

// noSetup are the steps of a home with no yard: none done.
func noSetup() []setupStep { return setupState{}.steps() }

// nextStep is the first step not done, nil when the setup is complete.
func nextStep(steps []setupStep) *setupStep {
	for i := range steps {
		if !steps[i].Done {
			return &steps[i]
		}
	}
	return nil
}

func (s setupState) steps() []setupStep {
	rig := "<rig>"
	if len(s.rigs) > 0 {
		rig = s.rigs[0].Name
	}
	// A yard of dir rigs only lands nothing: its flow ends at review, so it
	// has no step for landing and no route for approved.
	lands := len(s.rigs) == 0
	for _, r := range s.rigs {
		lands = lands || !r.IsDir()
	}
	out := []setupStep{s.rigStep(), s.filesStep()}
	out = append(out,
		s.groupStep(stepBuilders, "a group that builds", "new", "builder.md",
			fmt.Sprintf("yardr group add %s/builders --runner herdr --limit 2 --set prompt=roles/builder.md", rig)),
		s.groupStep(stepReviewers, "a group that reviews", "review", "reviewer.md",
			fmt.Sprintf("yardr group add %s/reviewers --runner herdr --set prompt=roles/reviewer.md", rig)))
	if lands {
		out = append(out, s.groupStep(stepLanding, "a group or person that lands approved beads", "approved", "merger.md",
			fmt.Sprintf("a refinery (an exec group running merge-local, see docs/pipeline.md), or people: "+
				"yardr group add %s/mergers --runner manual --members <them>", rig)))
	}
	return append(out, s.routesStep(rig, lands), s.beadStep(rig), s.mergedStep(lands))
}

func (s setupState) rigStep() setupStep {
	step := setupStep{Key: stepRig, Title: "a rig"}
	if len(s.rigs) == 0 {
		step.Detail = "no repository is a rig of this yard yet"
		step.Next = "yardr rig add <rig> <path> --base <branch>"
		return step
	}
	var names []string
	for _, r := range s.rigs {
		names = append(names, r.Name)
	}
	step.Done, step.Detail = true, strings.Join(names, ", ")
	return step
}

// filesStep is done when every rig has a flow where yardr reads it: under
// .yardr/flows/ on its base (in a dir rig's directory), or in the store
// when the rig points at one there, as a pack's import makes it. The flow
// stands for the rig's files: rig init writes it with the roles and the
// gate, and it is the one of them the store reads.
func (s setupState) filesStep() setupStep {
	step := setupStep{Key: stepRigFiles, Title: "the rig's .yardr files on its base",
		Detail: "there is no rig yet", Next: "yardr rig init <rig>, then commit .yardr/ on its base"}
	if len(s.rigs) == 0 {
		return step
	}
	var have, lack []string
	step.Next = ""
	for _, r := range s.rigs {
		where, next := "on its base "+or(r.Base, "origin/main"), ""
		if r.IsDir() {
			where = "in " + r.Path
		} else {
			next = ", then commit .yardr/ " + where
		}
		switch {
		case s.pointed[r.Name] != "":
			have = append(have, fmt.Sprintf("%s follows flow %s from the store", r.Name, s.pointed[r.Name]))
			continue
		case s.flowErr[r.Name] != "":
			lack = append(lack, s.flowErr[r.Name])
		case s.flows[r.Name] == 0:
			lack = append(lack, fmt.Sprintf("rig %s has no flow under .yardr/flows/ %s", r.Name, where))
		default:
			have = append(have, fmt.Sprintf("%s has its flow %s", r.Name, where))
			continue
		}
		if step.Next == "" {
			step.Next = "yardr rig init " + r.Name + next
		}
	}
	if len(lack) > 0 {
		step.Detail = strings.Join(lack, "; ")
		return step
	}
	step.Done, step.Detail = true, strings.Join(have, "; ")
	return step
}

// groupStep is done when a group does the work of stage: one routed for
// the stage, or one whose prompt is the role file. An exec group whose
// command merges lands too (a refinery not yet routed).
func (s setupState) groupStep(key, title, stage, role, next string) setupStep {
	step := setupStep{Key: key, Title: title}
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, g := range s.groups {
		prompt, _ := g.Config["prompt"].(string)
		cmd, _ := g.Config["cmd"].(string)
		if roleFile(prompt) == role || (stage == "approved" && g.Runner == "exec" && strings.Contains(cmd, "merge")) {
			add(g.Name)
		}
	}
	for _, r := range s.routes {
		if r.Stage == stage {
			add(r.Group)
		}
	}
	if len(names) == 0 {
		step.Detail = fmt.Sprintf("no group has the role %s or a route for stage %s", strings.TrimSuffix(role, ".md"), stage)
		step.Next = next
		return step
	}
	step.Done, step.Detail = true, strings.Join(names, ", ")
	return step
}

func (s setupState) routesStep(rig string, lands bool) setupStep {
	stages := []string{"new", "review"}
	if lands {
		stages = append(stages, "approved")
	}
	step := setupStep{Key: stepRoutes, Title: "routes for " + list(stages)}
	var have, lack []string
	for _, stage := range stages {
		found := false
		for _, r := range s.routes {
			if r.Stage == stage {
				have = append(have, stage+" to "+r.Group)
				found = true
				break
			}
		}
		if !found {
			lack = append(lack, stage)
		}
	}
	if len(lack) == 0 {
		step.Done, step.Detail = true, strings.Join(have, ", ")
		return step
	}
	step.Detail = "no route for " + list(lack)
	step.Next = fmt.Sprintf("yardr route add --stage %s --rig %s --group <group>", lack[0], rig)
	return step
}

// work reports whether b is a bead of work in a rig: not a resident's
// journal or a memory.
func work(b store.Bead) bool { return b.Rig != nil && !store.NotWork[b.Type] }

func (s setupState) beadStep(rig string) setupStep {
	step := setupStep{Key: stepFirstBead, Title: "a first bead"}
	for _, b := range s.beads {
		if work(b) {
			step.Done, step.Detail = true, b.ID
			return step
		}
	}
	step.Detail = "no bead was created in a rig yet"
	step.Next = fmt.Sprintf(`yardr bead create "<title>" --rig %s -b "<goal, done when>", then yardr bead advance <id> --to new`, rig)
	return step
}

// mergedStep is done once a bead of work went through to its flow's end:
// closed by entering a terminal stage (merged, or a dir rig's done), which
// leaves the stage as the reason. A bead closed by hand does not show that
// the pipeline works.
func (s setupState) mergedStep(lands bool) setupStep {
	step := setupStep{Key: stepMerged, Title: "a first merged bead"}
	if !lands {
		step.Title = "a first bead through to the flow's end"
	}
	for _, b := range s.beads {
		if work(b) && b.Status == "closed" && b.CloseReason != nil && *b.CloseReason == b.Stage {
			step.Done, step.Detail = true, fmt.Sprintf("%s (%s)", b.ID, b.Stage)
			return step
		}
	}
	step.Detail = "no bead has reached the end of its flow yet"
	step.Next = "see where the first bead is: yardr bead list"
	return step
}

// list is items as "a, b and c".
func list(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
