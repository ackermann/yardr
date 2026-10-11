package hooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Moving an issue forward is what a hook with a map does when a bead enters
// a stage (see store.IssueMove): the same change as SetState, made only when
// it takes the issue further than it is. SetState puts an issue in exactly
// the status asked, so started on an issue in review moves it back; a
// stage's map must never do that, to a bead sent back or to an issue a
// person moved ahead by hand.

// Where an issue stands among the states, in the order work passes through
// them. StoodOther is a status yardr cannot place (cancelled, a duplicate, a
// type it does not know): it counts as past every state, so the issue is
// left alone.
const (
	StoodBefore = "before"
	StoodOther  = "other"
)

// standings orders where an issue can stand.
var standings = []string{StoodBefore, StateStarted, StateReview, StateDone, StoodOther}

func standing(stood string) int {
	for i, s := range standings {
		if s == stood {
			return i
		}
	}
	return len(standings) // unknown: past everything
}

// Forwarded is what Forward found and did.
type Forwarded struct {
	Stated
	// Stood is where the issue stood before: before, started, review, done
	// or other. When Changed is false it was at or past the state asked, or
	// had the label asked.
	Stood string `json:"stood"`
}

// Forwarder is what a Stater whose issues a bead's stage can move adds.
type Forwarder interface {
	Stater
	// IssueThread is what Forward takes for the issue at fixed, its URL in
	// the source's fixed form (Linker.Issue). A link made from a URL has no
	// thread id, so the issue is named from the URL alone.
	IssueThread(fixed string) (string, error)
	// CredentialFrom is the source's credential as a crew member's env_file
	// gives it, get being what the file sets a name to: the one Credential
	// reads from the environment in the crew member's pane. When the file sets
	// none of the names it fails with a *MissingCredential.
	CredentialFrom(get func(name string) string) (string, error)
	// Places reports whether the source has a status for state at all, so a
	// map that names one it has not is refused when it is written.
	Places(state string) error
	// Forward moves the thread to the status state means in this source,
	// unless it stands at or past it already, or adds the label a
	// label:<name> names, unless the thread has it. It reads once and
	// writes at most once.
	Forward(ctx context.Context, thread, state, cred string) (Forwarded, error)
}

// ForwarderFor is the source's Forwarder, if a bead's stage can move its
// issues.
func ForwarderFor(source string) (Forwarder, bool) {
	f, ok := Adapters[source].(Forwarder)
	return f, ok
}

// Forwardable lists the sources a bead's stage can move issues in, sorted.
func Forwardable() []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := ForwarderFor(s); ok {
			out = append(out, s)
		}
	}
	return out
}

// KeyChecker is what a Forwarder whose credential can be a key file of the
// caller's adds (a GitHub App's): a plan checks that the key can sign
// without calling the source.
type KeyChecker interface {
	Forwarder
	// CheckKey reports why the key file the names get reads cannot be
	// signed with; nil when they name no key file, or it signs.
	CheckKey(get func(name string) string) error
}

// KeyCheckerFor is the source's KeyChecker, if its credential can name a
// key file.
func KeyCheckerFor(source string) (KeyChecker, bool) {
	k, ok := Adapters[source].(KeyChecker)
	return k, ok
}

// finalError marks an error no retry changes: yardr refused the call itself
// (a word the source has no status for, a pull request), where anything
// else may be the source failing.
type finalError struct{ err error }

func (e *finalError) Error() string { return e.err.Error() }
func (e *finalError) Unwrap() error { return e.err }

func final(err error) error { return &finalError{err} }

// Final reports whether err is one a retry cannot change.
func Final(err error) bool {
	var fe *finalError
	return errors.As(err, &fe)
}

// Linear

func (Linear) CredentialFrom(get func(string) string) (string, error) {
	return namedCredential("LINEAR_API_KEY", get)
}

func (Linear) Places(state string) error {
	if _, ok := Label(state); ok {
		return noLabel("linear", state)
	}
	if standing(state) < standing(StateStarted) || standing(state) > standing(StateDone) {
		return noState(state)
	}
	return nil
}

// IssueThread is the issue's identifier, the KEY-n its URL ends in, which
// Linear's issue(id:) takes in place of the id.
func (Linear) IssueThread(fixed string) (string, error) {
	i := strings.LastIndex(fixed, "/issue/")
	if i < 0 || fixed[i+len("/issue/"):] == "" {
		return "", final(fmt.Errorf("linear: %s names no issue", fixed))
	}
	return fixed[i+len("/issue/"):], nil
}

// linearStood places a status of the team: by its type, and among those of
// type started by whether it is at or after the team's review status, in
// the team's order. Triage, backlog and unstarted are before; anything that
// is none of Linear's types for work in hand or completed (canceled, which
// duplicates are too) is other.
func linearStood(states []linearState, s linearState) string {
	switch s.Type {
	case "triage", "backlog", "unstarted":
		return StoodBefore
	case "started":
		if review, instead, err := linearStatus(states, StateReview); err == nil && instead == "" && s.Position >= review.Position {
			return StateReview
		}
		return StateStarted
	case "completed":
		return StateDone
	}
	return StoodOther
}

// Forward is SetState for an issue that stands before the status the word
// means; one at or past it gets no mutation. A team without a review status
// has started for review, as in SetState, so an issue in progress is at it.
func (Linear) Forward(ctx context.Context, thread, state, cred string) (Forwarded, error) {
	if err := (Linear{}).Places(state); err != nil {
		return Forwarded{}, err
	}
	issue, err := linearRead(ctx, thread, cred)
	if err != nil {
		return Forwarded{}, err
	}
	to, instead, err := linearStatus(issue.Team.States.Nodes, state)
	if err != nil {
		return Forwarded{}, err
	}
	states := issue.Team.States.Nodes
	out := Forwarded{Stood: linearStood(states, issue.State),
		Stated: Stated{Thread: issue.thread(), Status: issue.State.Name, Was: issue.State.Name, Instead: instead}}
	if issue.State.ID == to.ID || standing(out.Stood) >= standing(linearStood(states, to)) {
		return out, nil
	}
	if err := linearMove(ctx, issue.ID, to, cred); err != nil {
		return Forwarded{}, err
	}
	out.Status, out.Changed = to.Name, true
	return out, nil
}

// GitHub

// CredentialFrom is the App the file names, else its GITHUB_TOKEN; never
// the gh login, which is the operator's.
func (GitHub) CredentialFrom(get func(string) string) (string, error) { return githubCredential(get) }

// Places takes a label beside started and done: an issue is open or closed
// and no more, so a label is the status GitHub has for anything between.
func (GitHub) Places(state string) error {
	if _, ok := Label(state); ok {
		return nil
	}
	_, err := githubWant(state)
	return err
}

// IssueThread is owner/repo#number, read from the issue's URL, or the pull
// request's: GitHub numbers both in one row, and Forward is what refuses a
// pull request a state is asked of.
func (GitHub) IssueThread(fixed string) (string, error) {
	rest, ok := strings.CutPrefix(fixed, "https://"+GitHub{}.IssueHost()+"/")
	repo, n, cut := strings.Cut(rest, "/issues/")
	if !cut {
		repo, n, cut = strings.Cut(rest, "/pull/")
	}
	if !ok || !cut {
		return "", final(fmt.Errorf("github: %s names no issue", fixed))
	}
	thread := repo + "#" + n
	if _, err := githubIssueURL(thread); err != nil {
		return "", final(fmt.Errorf("github: %s names no issue", fixed))
	}
	return thread, nil
}

// githubStood places an issue: open it stands at started, closed as
// completed at done, closed as anything else at other.
func githubStood(i githubStanding) string {
	switch {
	case i.State != "closed":
		return StateStarted
	case i.StateReason != "" && i.StateReason != "completed":
		return StoodOther
	}
	return StateDone
}

// Forward closes an open issue as completed for done. An open issue stands
// at started, which is all GitHub can say of it, and a closed one is never
// reopened: closed as completed it is done, closed as anything else (not
// planned, a duplicate) it is other. A label has no order to stand in: it is
// added wherever the issue or pull request stands, unless it is there.
func (GitHub) Forward(ctx context.Context, thread, state, cred string) (Forwarded, error) {
	if name, ok := Label(state); ok {
		out, i, err := githubAddLabel(ctx, thread, name, cred)
		if err != nil {
			return Forwarded{}, err
		}
		return Forwarded{Stood: githubStood(i), Stated: out}, nil
	}
	want, err := githubWant(state)
	if err != nil {
		return Forwarded{}, err
	}
	u, err := githubIssueURL(thread)
	if err != nil {
		return Forwarded{}, err
	}
	i, err := githubRead(ctx, u, thread, cred, false)
	if err != nil {
		return Forwarded{}, err
	}
	stood := githubStood(i)
	repo, _, _ := strings.Cut(thread, "#")
	out := Forwarded{Stood: stood, Stated: Stated{Thread: i.thread(repo), Status: i.status(), Was: i.status()}}
	if standing(stood) >= standing(state) {
		return out, nil
	}
	if err := githubMove(ctx, u, want, cred); err != nil {
		return Forwarded{}, err
	}
	out.Status, out.Changed = want, true
	return out, nil
}
