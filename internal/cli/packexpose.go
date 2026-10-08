package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// exposeAnswers is what an import does about the crew members a pack asks
// to have exposed to the yard's peers (expose_to_peers).
type exposeAnswers struct {
	// Answers are the operator's, by the yard's crew member and peer: what
	// pack.Apply exposes and records.
	Answers map[string]map[string]bool `json:"answers"`
	// Notes say who was not exposed and why, each naming who or which
	// command would.
	Notes []string `json:"notes"`
	// open are the crew members still to ask about, in a terminal.
	open []pack.Expose
}

// packExpose settles what the plan's Expose come to, as openThreads does
// the plan's OpenThreads: a terminal is asked (mayAsk), and without one
// nobody is exposed and a note names the command. No flag answers here:
// yardr peer allow <peer> --expose is the one that would, and the pack's
// asking adds nothing to it. Anyone but the operator answers nothing: the
// grant is the store's to refuse (store.AllowPeer), and said here, the
// import does the rest.
func (a *app) packExpose(ctx context.Context, st store.API, p *pack.Plan, mayAsk bool) (*exposeAnswers, error) {
	out := &exposeAnswers{Answers: map[string]map[string]bool{}, Notes: []string{}}
	var operator string
	var is, checked bool
	for _, ex := range p.Expose {
		if len(ex.Asks) == 0 {
			continue
		}
		if !checked {
			var err error
			if operator, is, err = a.operator(ctx, st); err != nil {
				return nil, err
			}
			checked = true
		}
		switch {
		case !is:
			out.Notes = append(out.Notes, fmt.Sprintf("crew member %s was not exposed to %s: only the operator (%s) may expose someone of the yard to a peer, and this is %s. The rest of the import does not depend on it; %s applies the pack again to be asked",
				ex.CrewMember, strings.Join(ex.Asks, ", "), operator, a.who(), operator))
		case mayAsk:
			out.open = append(out.open, ex)
		default:
			out.Notes = append(out.Notes, fmt.Sprintf("crew member %s was not exposed to %s: nothing is asked here, and the answer without a terminal is no. yardr peer allow <peer> --expose %s exposes it, a peer at a time",
				ex.CrewMember, strings.Join(ex.Asks, ", "), ex.CrewMember))
		}
	}
	return out, nil
}

// ask asks the operator, once per peer, whether the crew member is exposed
// to it. No is the default: leaving the form at its first key exposes it
// to nobody.
func (o *exposeAnswers) ask() error {
	for _, ex := range o.open {
		answers := map[string]bool{}
		for _, peer := range ex.Asks {
			yes := false
			if err := ask(huh.NewConfirm().Title(fmt.Sprintf("Expose %s to %s?", ex.CrewMember, peer)).
				Description(fmt.Sprintf("The pack asks for it. Yes shows %s the crew member, by name and with one line of what it does, and lets its mail reach it (yardr peer allow %s --expose %s); no leaves the link as it is. Either answer is recorded and not asked again.", peer, peer, ex.CrewMember)).
				Value(&yes)); err != nil {
				return err
			}
			answers[peer] = yes
		}
		o.Answers[ex.CrewMember] = answers
	}
	return nil
}

// planAsksExpose are the lines of "an import would ask" for the plan's
// Expose.
func planAsksExpose(p *pack.Plan) []string {
	var out []string
	for _, ex := range p.Expose {
		if len(ex.Asks) > 0 {
			out = append(out, fmt.Sprintf("  the operator: expose %s to %s (each as yardr peer allow <peer> --expose %s)? [no]",
				ex.CrewMember, strings.Join(ex.Asks, ", "), ex.CrewMember))
		}
	}
	return out
}

// printPlanExpose prints, for each crew member the pack asks to have
// exposed, where the yard's peers stand.
func printPlanExpose(p *pack.Plan) {
	for _, ex := range p.Expose {
		fmt.Printf("\nexpose to peers, for crew member %s\n", ex.CrewMember)
		if len(ex.Keys) == 0 {
			fmt.Println("  this yard is linked with no other: nothing is asked")
			continue
		}
		row := func(what string, peers []string, detail string) {
			if len(peers) > 0 {
				fmt.Printf("  %-9s %s%s\n", what, strings.Join(peers, ", "), detail)
			}
		}
		row("has", ex.Has, ": it is exposed there already, and nothing is asked")
		row("asks", ex.Asks, ": the operator is asked, once per peer, and the answer without one is no")
		row("declined", ex.Declined, ": an earlier import was told no (yardr peer allow <peer> --expose "+ex.CrewMember+" exposes it)")
	}
}

// printExposed says what an import did about the plan's Expose.
func printExposed(res *pack.Result, expose *exposeAnswers) {
	for _, e := range res.Exposed {
		fmt.Printf("crew member %s is now exposed to %s: it sees it, and its mail reaches it where the link takes mail (yardr peer show <peer>)\n", e.CrewMember, strings.Join(e.Peers, ", "))
	}
	// A no changes nothing in the yard, so it is said: it is on record.
	for _, name := range sortedKeys(expose.Answers) {
		var no []string
		for _, peer := range sortedKeys(expose.Answers[name]) {
			if !expose.Answers[name][peer] {
				no = append(no, peer)
			}
		}
		if len(no) > 0 {
			fmt.Printf("crew member %s was not exposed to %s: the no is recorded and not asked again (yardr peer allow <peer> --expose %s exposes it after all)\n",
				name, strings.Join(no, ", "), name)
		}
	}
	for _, n := range expose.Notes {
		fmt.Println(n)
	}
}
