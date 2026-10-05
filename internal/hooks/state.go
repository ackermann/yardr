package hooks

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Setting a thread's status is the third outbound call, after a reply and
// an opened thread: an issue moves to started, review or done. The call
// takes a thread's id and a credential and nothing else, so whoever holds
// both can make it: the CLI in a resident's pane, with the resident's own
// credential, and the daemon for a hook that acts as a resident (forward.go).

// The states a thread can be asked into. The words are yardr's; each source
// has its own statuses, and its Stater says which one a word means there.
const (
	StateStarted = "started"
	StateReview  = "review"
	StateDone    = "done"
)

// States lists the words, in the order work passes through them.
func States() []string { return []string{StateStarted, StateReview, StateDone} }

// Stated is what SetState found and did.
type Stated struct {
	Thread Thread `json:"thread"`
	// Status is the source's name of the status the thread is in now, and
	// Was the one it was in before.
	Status string `json:"status"`
	Was    string `json:"was,omitempty"`
	// Changed is false when the thread was in the status already: no call
	// changed anything.
	Changed bool `json:"changed"`
	// Instead is the word acted on when it is not the one asked: started,
	// for a review asked of a Linear team that has no review status.
	Instead string `json:"instead,omitempty"`
}

// Stater is what a Replier whose source lets a thread's status be set adds.
type Stater interface {
	Replier
	// SetState moves the thread (its id as deliveries name it) to the
	// status state means in this source. A thread already there is left as
	// it is, and a word the source has no status for is refused.
	SetState(ctx context.Context, thread, state, cred string) (Stated, error)
}

// StaterFor is the source's Stater, if a thread's status can be set in it.
func StaterFor(source string) (Stater, bool) {
	s, ok := Adapters[source].(Stater)
	return s, ok
}

// Stateable lists the sources a thread's status can be set in, sorted.
func Stateable() []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := StaterFor(s); ok {
			out = append(out, s)
		}
	}
	return out
}

func noState(state string) error {
	return final(fmt.Errorf("%q is no state: want %s", state, strings.Join(States(), ", ")))
}

// Linear

type linearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

// linearStatus picks the team's status for a word: started is the first
// status of type started, done the first of type completed, and review the
// first of type started whose name contains "review" in any case. First is
// lowest position, as the team orders them. A team without a review status
// gets started for review, and instead says so.
func linearStatus(states []linearState, state string) (to linearState, instead string, err error) {
	states = slices.Clone(states)
	slices.SortStableFunc(states, func(a, b linearState) int {
		switch {
		case a.Position < b.Position:
			return -1
		case a.Position > b.Position:
			return 1
		}
		return 0
	})
	first := func(typ string, ok func(linearState) bool) (linearState, bool) {
		for _, s := range states {
			if s.Type == typ && ok(s) {
				return s, true
			}
		}
		return linearState{}, false
	}
	every := func(linearState) bool { return true }
	typ := "started"
	switch state {
	case StateStarted:
	case StateDone:
		typ = "completed"
	case StateReview:
		review := func(s linearState) bool { return strings.Contains(strings.ToLower(s.Name), "review") }
		if s, ok := first(typ, review); ok {
			return s, "", nil
		}
		instead = StateStarted
	default:
		return linearState{}, "", noState(state)
	}
	if s, ok := first(typ, every); ok {
		return s, instead, nil
	}
	return linearState{}, "", fmt.Errorf("linear: the issue's team has no status of type %s", typ)
}

// linearStanding is an issue with its status and its team's statuses, as
// one query reads them.
type linearStanding struct {
	linearIssueNode
	State linearState `json:"state"`
	Team  struct {
		// 250 is the most Linear gives in one page, and far more
		// statuses than a team has.
		States struct {
			Nodes []linearState `json:"nodes"`
		} `json:"states"`
	} `json:"team"`
}

// linearRead reads the issue thread names: its id, as deliveries name it,
// or its identifier (YRD-12), which issue(id:) takes as well.
func linearRead(ctx context.Context, thread, cred string) (linearStanding, error) {
	if thread == "" {
		return linearStanding{}, final(fmt.Errorf("linear: no issue to set the status of"))
	}
	var q struct {
		Issue linearStanding `json:"issue"`
	}
	err := linearGraphQL(ctx, cred, `query($id: String!) {
  issue(id: $id) {
    id title url
    state { id name type position }
    team { states(first: 250) { nodes { id name type position } } }
  }
}`, map[string]any{"id": thread}, &q)
	if err != nil {
		return linearStanding{}, err
	}
	if q.Issue.ID == "" {
		return linearStanding{}, fmt.Errorf("linear: no issue %s", thread)
	}
	return q.Issue, nil
}

// linearMove puts the issue in the status to.
func linearMove(ctx context.Context, issue string, to linearState, cred string) error {
	var m struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	err := linearGraphQL(ctx, cred, `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`,
		map[string]any{"id": issue, "input": map[string]any{"stateId": to.ID}}, &m)
	if err != nil {
		return err
	}
	if !m.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate did not succeed")
	}
	return nil
}

// SetState sets the issue's status to the one of its team the word means
// (linearStatus). The issue and its team's statuses are read first, so an
// issue already there gets no mutation.
func (Linear) SetState(ctx context.Context, thread, state, cred string) (Stated, error) {
	if !slices.Contains(States(), state) {
		return Stated{}, noState(state)
	}
	issue, err := linearRead(ctx, thread, cred)
	if err != nil {
		return Stated{}, err
	}
	to, instead, err := linearStatus(issue.Team.States.Nodes, state)
	if err != nil {
		return Stated{}, err
	}
	out := Stated{Thread: issue.thread(), Status: to.Name, Was: issue.State.Name, Instead: instead}
	if issue.State.ID == to.ID {
		return out, nil
	}
	if err := linearMove(ctx, issue.ID, to, cred); err != nil {
		return Stated{}, err
	}
	out.Changed = true
	return out, nil
}

// GitHub

// githubIssueURL is the endpoint of the issue a thread id (owner/repo#number)
// names.
func githubIssueURL(thread string) (string, error) {
	repo, n, ok := strings.Cut(thread, "#")
	if !ok || !githubRepoName.MatchString(repo) || n == "" || strings.Trim(n, "0123456789") != "" {
		return "", final(fmt.Errorf("github: thread %q: want owner/repo#number", thread))
	}
	return GitHubAPI + "/repos/" + repo + "/issues/" + n, nil
}

// githubStanding is an issue as the API gives it.
type githubStanding struct {
	githubOpened
	State       string         `json:"state"`
	StateReason string         `json:"state_reason"`
	PullRequest map[string]any `json:"pull_request"`
}

// status is the issue's status as yardr names it: open, closed, or closed
// with the reason when it was not closed as completed.
func (i githubStanding) status() string {
	if i.State == "closed" && i.StateReason != "" && i.StateReason != "completed" {
		return "closed (" + strings.ReplaceAll(i.StateReason, "_", " ") + ")"
	}
	return i.State
}

// githubRead reads the issue thread names, at the endpoint u, and refuses a
// pull request, which the same endpoint serves and would close unmerged.
func githubRead(ctx context.Context, u, thread, cred string) (githubStanding, error) {
	var i githubStanding
	if err := githubCall(ctx, http.MethodGet, u, cred, nil, &i); err != nil {
		return i, fmt.Errorf("github: %w", err)
	}
	if i.PullRequest != nil {
		return i, final(fmt.Errorf("github: %s is a pull request: yardr sets the status of issues only", thread))
	}
	if i.State != "open" && i.State != "closed" {
		return i, fmt.Errorf("github: the answer names no state for %s", thread)
	}
	return i, nil
}

// githubMove opens or closes the issue at the endpoint u; closed is closed
// as completed.
func githubMove(ctx context.Context, u, want, cred string) error {
	patch := map[string]string{"state": want}
	if want == "closed" {
		patch["state_reason"] = "completed"
	}
	if err := githubCall(ctx, http.MethodPatch, u, cred, patch, nil); err != nil {
		return fmt.Errorf("github: %w", err)
	}
	return nil
}

// githubWant is the state of an issue a word means: open for started,
// closed for done. An issue has no more statuses than those, so review is
// refused.
func githubWant(state string) (string, error) {
	switch state {
	case StateStarted:
		return "open", nil
	case StateDone:
		return "closed", nil
	case StateReview:
		return "", final(fmt.Errorf("github: an issue has no review status: the states it has are %s and %s", StateStarted, StateDone))
	}
	return "", noState(state)
}

// SetState closes the issue as completed for done and reopens a closed one
// for started. An issue has no more statuses than open and closed, so
// review is refused; so is a pull request, which the same endpoint would
// close unmerged. A closed issue is done whatever it was closed as: the
// reason someone gave is not rewritten.
func (GitHub) SetState(ctx context.Context, thread, state, cred string) (Stated, error) {
	want, err := githubWant(state)
	if err != nil {
		return Stated{}, err
	}
	u, err := githubIssueURL(thread)
	if err != nil {
		return Stated{}, err
	}
	i, err := githubRead(ctx, u, thread, cred)
	if err != nil {
		return Stated{}, err
	}
	repo, _, _ := strings.Cut(thread, "#")
	out := Stated{Thread: i.thread(repo), Status: i.status(), Was: i.status()}
	if i.State == want {
		return out, nil
	}
	if err := githubMove(ctx, u, want, cred); err != nil {
		return Stated{}, err
	}
	out.Status, out.Changed = want, true
	return out, nil
}
