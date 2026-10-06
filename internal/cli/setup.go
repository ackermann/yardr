package cli

import (
	"context"
	"fmt"
	"slices"
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
	// Depot is the depot the step's command names, once the yard has one.
	Depot string `json:"depot,omitempty"`
	// args are the yardr command lines that do the step, when they follow
	// from the store alone with nothing left to ask: what the guided setup
	// shows and runs for it (yardr setup).
	args [][]string
}

// The setup steps' keys, in their order.
const (
	stepDepot      = "depot"
	stepDepotFiles = "depot_files"
	stepBuilders   = "builders"
	stepReviewers  = "reviewers"
	stepLanding    = "landing"
	stepRoutes     = "routes"
	stepFirstBead  = "first_bead"
	stepMerged     = "merged_bead"
)

// setupState is what the setup steps are read from.
type setupState struct {
	depots []store.Depot
	groups []store.Group
	routes []store.Route
	beads  []store.Bead // closed ones too
	// flows are the flows on each depot's base (in a dir depot's directory), by
	// depot; flowErr is why a depot's could not be read.
	flows   map[string]int
	flowErr map[string]string
	// pointed are the depots that follow a flow in the store (a pack's, say)
	// rather than their own file, with the flow's name.
	pointed map[string]string
}

// setupSteps are the steps of a yard's setup, each done or not, in the order
// they are taken. They are read from the store and nothing else (the store
// reads a depot's files from its base), so everything that walks a person
// through the setup sees the same list and can take over from any other:
// yardr doctor, the yardmaster on its first run, a guided form. The first
// step not done is where the setup goes on (nextStep).
func setupSteps(ctx context.Context, st store.API) ([]setupStep, error) {
	var s setupState
	var err error
	if s.depots, err = st.Depots(ctx); err != nil {
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
			s.pointed[p.Depot] = p.Flow
		}
	}
	for _, r := range s.depots {
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
	depot := "<depot>"
	if len(s.depots) > 0 {
		depot = s.depots[0].Name
	}
	// A yard of dir depots only lands nothing: its flow ends at review, so it
	// has no step for landing and no route for approved.
	lands := len(s.depots) == 0
	for _, r := range s.depots {
		lands = lands || !r.IsDir()
	}
	out := []setupStep{s.depotStep(), s.filesStep()}
	out = append(out,
		s.groupStep(stepBuilders, "a group that builds", "new",
			fmt.Sprintf("yardr group add %s/builders --runner herdr --limit 2 --set prompt=roles/builder.md", depot)),
		s.groupStep(stepReviewers, "a group that reviews", "review",
			fmt.Sprintf("yardr group add %s/reviewers --runner herdr --set prompt=roles/reviewer.md", depot)))
	if lands {
		// Beads land in a git depot: the first of them is the one its command names.
		landDepot := depot
		for _, r := range s.depots {
			if !r.IsDir() {
				landDepot = r.Name
				break
			}
		}
		step := s.groupStep(stepLanding, "a group or person that lands approved beads", "approved",
			fmt.Sprintf("a refinery (an exec group running merge-local, which yardr setup installs in a terminal; see docs/pipeline.md), or people: "+
				"yardr group add %s/mergers --runner manual --members <them>", landDepot))
		if len(s.depots) > 0 {
			step.Depot = landDepot
		}
		out = append(out, step)
	}
	out = append(out, s.routesStep(depot, lands), s.beadStep(depot), s.mergedStep(lands))
	if len(s.depots) > 0 {
		for i := range out {
			out[i].Depot = or(out[i].Depot, depot)
		}
	}
	return out
}

func (s setupState) depotStep() setupStep {
	step := setupStep{Key: stepDepot, Title: "a depot"}
	if len(s.depots) == 0 {
		step.Detail = "no repository is a depot of this yard yet"
		step.Next = "yardr depot add <depot> <path> --base <branch>"
		return step
	}
	var names []string
	for _, r := range s.depots {
		names = append(names, r.Name)
	}
	step.Done, step.Detail = true, strings.Join(names, ", ")
	return step
}

// filesStep is done when every depot has a flow where yardr reads it: under
// .yardr/flows/ on its base (in a dir depot's directory), or in the store
// when the depot points at one there, as a pack's import makes it. The flow
// stands for the depot's files: depot init writes it with the roles and the
// gate, and it is the one of them the store reads.
func (s setupState) filesStep() setupStep {
	step := setupStep{Key: stepDepotFiles, Title: "the depot's .yardr files on its base",
		Detail: "there is no depot yet", Next: "yardr depot init <depot>, then commit .yardr/ on its base"}
	if len(s.depots) == 0 {
		return step
	}
	var have, lack []string
	step.Next = ""
	for _, r := range s.depots {
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
			lack = append(lack, fmt.Sprintf("depot %s has no flow under .yardr/flows/ %s", r.Name, where))
		default:
			have = append(have, fmt.Sprintf("%s has its flow %s", r.Name, where))
			continue
		}
		if step.Next == "" {
			step.Next, step.Depot = "yardr depot init "+r.Name+next, r.Name
		}
	}
	if len(lack) > 0 {
		step.Detail = strings.Join(lack, "; ")
		return step
	}
	step.Done, step.Detail = true, strings.Join(have, "; ")
	return step
}

// stageRoles are the stages the setup routes, each with the role file of
// the group that works it.
var stageRoles = map[string]string{"new": "builder.md", "review": "reviewer.md", "approved": "merger.md"}

// doing are the groups that do the work of stage: one whose prompt is the
// stage's role file, then one routed for the stage. An exec group whose
// command merges lands too (a refinery not yet routed).
func (s setupState) doing(stage string) []string {
	var names []string
	add := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, g := range s.groups {
		prompt, _ := g.Config["prompt"].(string)
		cmd, _ := g.Config["cmd"].(string)
		if roleFile(prompt) == stageRoles[stage] || (stage == "approved" && g.Runner == "exec" && strings.Contains(cmd, "merge")) {
			add(g.Name)
		}
	}
	for _, r := range s.routes {
		if r.Stage == stage {
			add(r.Group)
		}
	}
	return names
}

// groupStep is done when a group does the work of stage (doing).
func (s setupState) groupStep(key, title, stage, next string) setupStep {
	step := setupStep{Key: key, Title: title}
	names := s.doing(stage)
	if len(names) == 0 {
		step.Detail = fmt.Sprintf("no group has the role %s or a route for stage %s", strings.TrimSuffix(stageRoles[stage], ".md"), stage)
		step.Next = next
		return step
	}
	step.Done, step.Detail = true, strings.Join(names, ", ")
	return step
}

func (s setupState) routesStep(depot string, lands bool) setupStep {
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
	// A stage's route goes to the group that does its work, when the yard
	// has one: then the command is whole, and the guided setup runs it.
	for _, stage := range lack {
		group := "<group>"
		if names := s.doing(stage); len(names) > 0 {
			group = names[0]
			step.args = append(step.args, []string{"route", "add", "--stage", stage, "--depot", depot, "--group", group})
		}
		if step.Next == "" {
			step.Next = fmt.Sprintf("yardr route add --stage %s --depot %s --group %s", stage, depot, group)
		}
	}
	return step
}

// work reports whether b is a bead of work in a depot: not a crew member's
// journal or a memory.
func work(b store.Bead) bool { return b.Depot != nil && !store.NotWork[b.Type] }

func (s setupState) beadStep(depot string) setupStep {
	step := setupStep{Key: stepFirstBead, Title: "a first bead"}
	for _, b := range s.beads {
		if work(b) {
			step.Done, step.Detail = true, b.ID
			return step
		}
	}
	step.Detail = "no bead was created in a depot yet"
	step.Next = fmt.Sprintf(`yardr bead create "<title>" --depot %s -b "<goal, done when>", then yardr bead advance <id> --to new`, depot)
	return step
}

// mergedStep is done once a bead of work went through to its flow's end:
// closed by entering a terminal stage (merged, or a dir depot's done), which
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
