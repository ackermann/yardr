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
		s.groupStep(stepBuilders, "a group that builds", "new", s.packNext(depot)),
		s.groupStep(stepReviewers, "a group that reviews", "review", s.packNext(depot)))
	if lands {
		// Beads land in a git depot: the first of them is the one its command names.
		landDepot := depot
		for _, r := range s.depots {
			if !r.IsDir() {
				landDepot = r.Name
				break
			}
		}
		step := s.groupStep(stepLanding, "a group or person that lands approved beads", "approved", s.packNext(landDepot))
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

// packNext is the command that sets depot up from a pack: the one command
// for the depot's flow, its groups and their routes, so every step between
// the depot and the first bead names it. The pack is the one yardr carries
// for the depot's kind (setupPack).
func (s setupState) packNext(depot string) string {
	pack := setupPack(store.Depot{})
	for _, r := range s.depots {
		if r.Name == depot {
			pack = setupPack(r)
		}
	}
	return fmt.Sprintf("yardr depot init %s --pack %s", depot, pack)
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

// filesStep is done when every depot has a flow where yardr reads it: in the
// store when the depot points at one there, as a pack's import makes it, or
// under .yardr/flows/ on its base (in a dir depot's directory), where depot
// init without a pack writes it and a depot set up by hand keeps it.
func (s setupState) filesStep() setupStep {
	step := setupStep{Key: stepDepotFiles, Title: "the depot's .yardr files on its base",
		Detail: "there is no depot yet", Next: s.packNext("<depot>")}
	if len(s.depots) == 0 {
		return step
	}
	var have, lack []string
	step.Next = ""
	for _, r := range s.depots {
		where := "on its base " + or(r.Base, "origin/main")
		if r.IsDir() {
			where = "in " + r.Path
		}
		switch {
		case s.pointed[r.Name] != "":
			have = append(have, fmt.Sprintf("%s follows flow %s from the store", r.Name, s.pointed[r.Name]))
			continue
		case s.flowErr[r.Name] != "":
			lack = append(lack, s.flowErr[r.Name])
		case s.flows[r.Name] == 0:
			lack = append(lack, fmt.Sprintf("depot %s follows no flow of a pack, and has none under .yardr/flows/ %s", r.Name, where))
		default:
			have = append(have, fmt.Sprintf("%s has its flow %s", r.Name, where))
			continue
		}
		if step.Next == "" {
			step.Next, step.Depot = s.packNext(r.Name), r.Name
		}
	}
	if len(lack) > 0 {
		step.Detail = strings.Join(lack, "; ")
		return step
	}
	step.Done, step.Detail = true, strings.Join(have, "; ")
	return step
}

// doing are the groups that do the work of stage: the ones a route sends its
// beads to. A route is what a pack's import and a setup by hand both end in,
// and it says who works a stage without a word about the group's role.
func (s setupState) doing(stage string) []string {
	var names []string
	for _, r := range s.routes {
		if r.Stage == stage && !slices.Contains(names, r.Group) {
			names = append(names, r.Group)
		}
	}
	return names
}

// groupStep is done when a group does the work of stage (doing).
func (s setupState) groupStep(key, title, stage, next string) setupStep {
	step := setupStep{Key: key, Title: title}
	names := s.doing(stage)
	if len(names) == 0 {
		step.Detail = fmt.Sprintf("no group is routed for stage %s", stage)
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
	step.Next = s.packNext(depot)
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
