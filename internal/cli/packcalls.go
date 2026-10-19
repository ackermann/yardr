package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// callAnswers is what an import does about the calls of declared sources a
// pack asks that its groups' sessions and its crew may make.
type callAnswers struct {
	// Answers are the operator's, by grantee ("group web-builders") and call
	// ("crm:read"): what pack.Apply grants and records.
	Answers map[string]map[string]bool `json:"answers"`
	// Notes say what was not granted and why, each naming who or which flag
	// would grant it.
	Notes []string `json:"notes"`
	// open are the requests still to ask about, in a terminal.
	open []pack.Calls
}

// callsFlag is the name a request has in --calls: the pack's name for the
// group, or crew:<the pack's name for the crew member>.
func callsFlag(c pack.Calls) string {
	if c.CrewMember != "" {
		return "crew:" + c.Pack
	}
	return c.Pack
}

// packCalls settles what the plan's Calls come to, as openThreads does for
// the plan's OpenThreads: --calls answers for the group or crew member and
// the source it names, a terminal is asked for the rest (mayAsk), and
// without either nothing is granted and a note names the flag. The safe
// answer is no, so a missing flag is no error. Anyone but the operator
// answers nothing, by flag or by form, and the import does the rest: the
// store refuses the grant itself (store.GrantSource), and said here it does
// not stop the import halfway.
func (a *app) packCalls(ctx context.Context, st store.API, p *pack.Plan, flags []string, mayAsk bool) (*callAnswers, error) {
	out := &callAnswers{Answers: map[string]map[string]bool{}, Notes: []string{}}
	named := map[int][]string{} // by the request's place in the plan
	for _, kv := range flags {
		name, v, ok := strings.Cut(kv, "=")
		src, words, ok2 := strings.Cut(v, ":")
		if !ok || !ok2 || name == "" || src == "" || words == "" {
			return nil, fmt.Errorf("--calls %q: want <group>=<source>:<call>[,<call>] with read for every read call, crew:<crew member>=… for a crew member, or <source>:none", kv)
		}
		member, isCrew := strings.CutPrefix(name, "crew:")
		names, what := p.Groups, "group"
		if isCrew {
			names, what = p.Crew, "crew member"
		} else {
			member = name
		}
		if _, ok := names[member]; !ok {
			return nil, fmt.Errorf("--calls %q: the pack has no %s %s (it has: %s)", kv, what, member, strings.Join(sortedKeys(names), ", "))
		}
		i := slices.IndexFunc(p.Calls, func(c pack.Calls) bool { return callsFlag(c) == name && c.Source == src })
		if i < 0 {
			return nil, fmt.Errorf("--calls %q: the pack asks for no call of the source %s for its %s %s (calls)", kv, src, what, member)
		}
		if _, twice := named[i]; twice {
			return nil, fmt.Errorf("--calls %q: %s is answered twice for the source %s", kv, name, src)
		}
		named[i] = []string{}
		if words == "none" {
			continue
		}
		for _, w := range strings.Split(words, ",") {
			if w = strings.TrimSpace(w); !slices.Contains(p.Calls[i].Wants, w) {
				return nil, fmt.Errorf("--calls %q: the pack does not ask that %s may call %q of %s (it asks for: %s)", kv, name, w, src, strings.Join(p.Calls[i].Wants, ", "))
			}
			named[i] = append(named[i], w)
		}
	}
	var operator string
	var is, checked bool
	for i, c := range p.Calls {
		yes, flagged := named[i]
		who := c.Grantee().String()
		answers := map[string]bool{}
		// What this run would have to be the operator for.
		var about []string
		for _, w := range c.Wants {
			switch {
			case flagged && slices.Contains(yes, w) && c.Grantable(w):
				answers[pack.CallKey(c.Source, w)] = true
				about = append(about, w)
			case flagged && slices.Contains(yes, w) && slices.Contains(c.Lacks, w):
				out.Notes = append(out.Notes, fmt.Sprintf("--calls %s=%s:%s grants nothing: %s (the import asks once the yard has it)",
					callsFlag(c), c.Source, w, missing(c)))
			case slices.Contains(c.Asks, w):
				// A flag that leaves a call out says no to it.
				if flagged {
					answers[pack.CallKey(c.Source, w)] = false
				}
				about = append(about, w)
			}
		}
		if len(about) == 0 {
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
			out.Notes = append(out.Notes, fmt.Sprintf("%s was not let call %s of the source %s: only the operator (%s) may grant that, and this is %s. The rest of the import does not depend on it; %s applies the pack again to be asked",
				who, c.Words(about), c.Source, operator, a.who(), operator))
		case flagged:
			for k, v := range answers {
				if out.Answers[who] == nil {
					out.Answers[who] = map[string]bool{}
				}
				out.Answers[who][k] = v
			}
		case mayAsk:
			out.open = append(out.open, c)
		default:
			out.Notes = append(out.Notes, fmt.Sprintf("%s was not let call %s of the source %s: nothing is asked here, and the answer without a flag is no. --calls %s=%s:%s grants it, --calls %s=%s:none says no for good",
				who, c.Words(about), c.Source, callsFlag(c), c.Source, strings.Join(about, ","), callsFlag(c), c.Source))
		}
	}
	return out, nil
}

// missing says in a line why nothing of a request is asked or granted.
func missing(c pack.Calls) string {
	out := make([]string, len(c.Missing))
	for i, n := range c.Missing {
		out[i] = n.Detail
	}
	return strings.Join(out, "; ")
}

// ask asks the operator, once per group or crew member and source, whether
// it may make the calls still open, and takes the one answer for all of
// them. No is the default: leaving the form at its first key grants
// nothing.
func (o *callAnswers) ask() error {
	for _, c := range o.open {
		yes := false
		who := c.Grantee().String()
		desc := "The pack asks for it. Yes lets " + who + " make these calls with yardr call, with the yard's credential; no grants nothing. Either answer is recorded and not asked again."
		if slices.Contains(c.Asks, store.GrantRead) {
			desc += " read is every read call of the source, one a later file adds included."
		}
		if err := ask(huh.NewConfirm().Key("calls").Title(fmt.Sprintf("Let %s call %s of the source %s?", who, c.Words(c.Asks), c.Source)).
			Description(desc).Value(&yes)); err != nil {
			return err
		}
		if o.Answers[who] == nil {
			o.Answers[who] = map[string]bool{}
		}
		for _, w := range c.Asks {
			o.Answers[who][pack.CallKey(c.Source, w)] = yes
		}
	}
	return nil
}

// planAsksCalls are the lines of a plan's "an import would ask" for the
// calls no answer in said covers yet.
func planAsksCalls(p *pack.Plan, said map[string]map[string]bool) []string {
	var out []string
	for _, c := range p.Calls {
		var open []string
		for _, w := range c.Asks {
			if _, done := said[c.Grantee().String()][pack.CallKey(c.Source, w)]; !done {
				open = append(open, w)
			}
		}
		if len(open) > 0 {
			out = append(out, fmt.Sprintf("  the operator: let %s call %s of the source %s (--calls %s=%s:%s, or :none)? [no]",
				c.Grantee(), c.Words(open), c.Source, callsFlag(c), c.Source, strings.Join(open, ",")))
		}
	}
	return out
}

// printPlanCalls prints, for each request of the plan, what the pack asks
// for and what an import does about each call. said are the answers given
// already, by grantee and call.
func printPlanCalls(w io.Writer, p *pack.Plan, said map[string]map[string]bool) {
	for _, c := range p.Calls {
		fmt.Fprintf(w, "\ncalls of source %s, for %s\n", c.Source, c.Grantee())
		fmt.Fprintf(w, "  the pack asks for %s\n", c.Words(c.Wants))
		row := func(what string, calls []string, detail string) {
			if len(calls) > 0 {
				fmt.Fprintf(w, "  %-9s %s%s\n", what, c.Words(calls), detail)
			}
		}
		row("has", c.Has, ": it may already, and nothing is asked")
		var yes, no, asked []string
		for _, word := range c.Wants {
			switch answer, done := said[c.Grantee().String()][pack.CallKey(c.Source, word)]; {
			case done && answer:
				yes = append(yes, word)
			case done:
				no = append(no, word)
			case slices.Contains(c.Asks, word):
				asked = append(asked, word)
			}
		}
		row("grants", yes, ": --calls says so")
		row("declines", no, ": --calls leaves it out")
		row("asks", asked, ": the operator is asked, and the answer without one is no")
		row("declined", slices.DeleteFunc(slices.Clone(c.Declined), func(word string) bool { return slices.Contains(yes, word) }),
			": an earlier import was told no (--calls "+callsFlag(c)+"="+c.Source+":<call> grants it)")
		row("lacks", c.Lacks, ": nothing is asked or granted")
		for _, n := range c.Missing {
			fmt.Fprintf(w, "    %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
}

// printCallResult says what an import did about the calls a pack asks for:
// each grant, each no on record, and what was neither asked nor granted.
func printCallResult(w io.Writer, plan *pack.Plan, res *pack.Result, calls *callAnswers) {
	for _, g := range res.Calls {
		c := plan.Calls[slices.IndexFunc(plan.Calls, func(c pack.Calls) bool { return c.Grantee() == g.Grantee && c.Source == g.Source })]
		fmt.Fprintf(w, "%s may now call %s of the source %s (yardr source show %s)\n", g.Grantee, c.Words(g.Calls), g.Source, g.Source)
	}
	// A no changes nothing in the yard, so it is said: it is on record.
	for _, c := range plan.Calls {
		var no []string
		for _, word := range c.Wants {
			if yes, done := calls.Answers[c.Grantee().String()][pack.CallKey(c.Source, word)]; done && !yes {
				no = append(no, word)
			}
		}
		if len(no) > 0 {
			fmt.Fprintf(w, "%s was not let call %s of the source %s: the no is recorded and not asked again (--calls %s=%s:%s grants it after all)\n",
				c.Grantee(), c.Words(no), c.Source, callsFlag(c), c.Source, strings.Join(no, ","))
		}
		if len(c.Lacks) > 0 {
			fmt.Fprintf(w, "nothing was asked about %s calling %s of the source %s: %s\n", c.Grantee(), c.Words(c.Lacks), c.Source, missing(c))
		}
	}
	for _, n := range calls.Notes {
		fmt.Fprintln(w, n)
	}
}
