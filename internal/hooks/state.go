package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Setting a thread's status is the third outbound call, after a reply and
// an opened thread: an issue moves to started, review or done. The call
// takes a thread's id and a credential and nothing else, so whoever holds
// both can make it: the CLI in a crew member's pane, with the crew member's own
// credential, and the daemon for a hook that acts as a crew member (forward.go).

// The states a thread can be asked into. The words are yardr's; each source
// has its own statuses, and its Stater says which one a word means there.
const (
	StateStarted = "started"
	StateReview  = "review"
	StateDone    = "done"
)

// States lists the words, in the order work passes through them.
func States() []string { return []string{StateStarted, StateReview, StateDone} }

// Label is the label a word asks for in place of a state, and whether it is
// such a word: label:<name>. A source whose threads take labels adds it and
// never removes one; any other refuses the word.
func Label(word string) (string, bool) { return store.ThreadLabel(word) }

// Word reports whether word is one a thread can be asked into in some
// source: a state, or a label.
func Word(word string) bool {
	_, label := Label(word)
	return label || slices.Contains(States(), word)
}

// Stated is what SetState found and did.
type Stated struct {
	Thread Thread `json:"thread"`
	// Status is the source's name of the status the thread is in now, and
	// Was the one it was in before. For a label Status is "labeled <name>",
	// as the source spells the name, and Was is empty.
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
	// status state means in this source, or adds the label a label:<name>
	// names. A thread already there is left as it is, and a word the source
	// has no status for is refused.
	SetState(ctx context.Context, thread, state, cred string) (Stated, error)
}

// StaterFor is the source's Stater, if a thread's status can be set in
// it: the one built from d, the yard's declared source of that name,
// when it has the keys setting takes, else the builtin one of that
// name. No name is both, so at most one applies. d is nil when the yard
// declares no such source. home is the yard's home, which a relative
// command program runs from; mint is the source's mint_command
// setting, "" when the yard set none.
func StaterFor(source string, d *source.Declaration, home, mint string) (Stater, bool) {
	if dec, ok := declared(source, d, home, mint); ok && dec.stateable() {
		return dec, true
	}
	if s, ok := Adapters[source].(Stater); ok {
		return s, true
	}
	return nil, false
}

// Stateable lists the sources a thread's status can be set in, sorted: the
// builtin ones, and the yard's declared sources among decls with the keys
// setting takes.
func Stateable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := StaterFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := StaterFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func noState(state string) error {
	return final(fmt.Errorf("%q is no state: want %s", state, strings.Join(States(), ", ")))
}

// noLabel refuses a label in a source whose threads yardr sets none on.
func noLabel(source, word string) error {
	return final(fmt.Errorf("%s: yardr sets no label there, so %q is no state: want %s", source, word, strings.Join(States(), ", ")))
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
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// label is the issue's label of that name as GitHub spells it, which takes
// a label's name in any case.
func (i githubStanding) label(name string) (string, bool) {
	for _, l := range i.Labels {
		if strings.EqualFold(l.Name, name) {
			return l.Name, true
		}
	}
	return "", false
}

// status is the issue's status as yardr names it: open, closed, or closed
// with the reason when it was not closed as completed.
func (i githubStanding) status() string {
	if i.State == "closed" && i.StateReason != "" && i.StateReason != "completed" {
		return "closed (" + strings.ReplaceAll(i.StateReason, "_", " ") + ")"
	}
	return i.State
}

// githubRead reads the issue thread names, at the endpoint u. The same
// endpoint serves a pull request, which is refused unless pulls: a state
// would close it unmerged, where a label is as good on it as on an issue.
func githubRead(ctx context.Context, u, thread, cred string, pulls bool) (githubStanding, error) {
	var i githubStanding
	if err := githubCall(ctx, http.MethodGet, u, cred, nil, &i); err != nil {
		return i, fmt.Errorf("github: %w", err)
	}
	if i.PullRequest != nil && !pulls {
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

// githubAddLabel adds the label to the issue or pull request thread names,
// unless it has it already: one read, and at most one write. A label the
// repository has not is made by GitHub as it is added. Nothing is ever
// removed, and the issue stays open or closed as it was.
func githubAddLabel(ctx context.Context, thread, name, cred string) (Stated, githubStanding, error) {
	u, err := githubIssueURL(thread)
	if err != nil {
		return Stated{}, githubStanding{}, err
	}
	i, err := githubRead(ctx, u, thread, cred, true)
	if err != nil {
		return Stated{}, i, err
	}
	repo, _, _ := strings.Cut(thread, "#")
	out := Stated{Thread: i.thread(repo)}
	if has, ok := i.label(name); ok {
		out.Status = "labeled " + has
		return out, i, nil
	}
	if err := githubCall(ctx, http.MethodPost, u+"/labels", cred, map[string][]string{"labels": {name}}, nil); err != nil {
		return Stated{}, i, fmt.Errorf("github: %w", err)
	}
	out.Status, out.Changed = "labeled "+name, true
	return out, i, nil
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
// reason someone gave is not rewritten. A label:<name> adds the label, to a
// pull request too, and one the thread has already is left as it is.
func (GitHub) SetState(ctx context.Context, thread, state, cred string) (Stated, error) {
	if name, ok := Label(state); ok {
		out, _, err := githubAddLabel(ctx, thread, name, cred)
		return out, err
	}
	want, err := githubWant(state)
	if err != nil {
		return Stated{}, err
	}
	u, err := githubIssueURL(thread)
	if err != nil {
		return Stated{}, err
	}
	i, err := githubRead(ctx, u, thread, cred, false)
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

// Declared

// SetState moves the thread to the status state means in this source, by
// thread.state: in {thread, state}, out {status, moved, was?}. The command
// picks the source's status for the word and moves exactly: only a thread
// already in the status is reported as not moved. Was is what a moved
// thread was in, which the command may not say. A label:<name> adds the
// label, when thread.labels, and one the thread has already is reported
// as not moved.
func (d Declared) SetState(ctx context.Context, thread, state, cred string) (Stated, error) {
	return d.setState(ctx, thread, state, false, cred)
}

// setState runs thread.state, with forward for a stage map's move, which
// the command never moves backwards: in {thread, state, forward?}.
func (d Declared) setState(ctx context.Context, thread, state string, forward bool, cred string) (Stated, error) {
	if err := d.Places(state); err != nil {
		return Stated{}, err
	}
	if thread == "" {
		return Stated{}, final(fmt.Errorf("%s: no thread to set the status of", d.D.Name))
	}
	in := map[string]any{"thread": thread, "state": state}
	if forward {
		in["forward"] = true
	}
	var out Stated
	err := d.runThread(ctx, "thread.state", d.D.Thread.State, in, cred, func(raw []byte) error {
		var o struct {
			Status string `json:"status"`
			Moved  bool   `json:"moved"`
			Was    string `json:"was"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("its answer is no {status, moved}: %v", err)
		}
		if o.Status == "" {
			return fmt.Errorf("its answer names no status")
		}
		out = Stated{Thread: Thread{ID: thread}, Status: o.Status, Was: o.Was, Changed: o.Moved}
		if !o.Moved {
			out.Was = o.Status
		}
		return nil
	})
	if err != nil {
		return Stated{}, err
	}
	return out, nil
}
