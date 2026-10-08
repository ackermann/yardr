package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// CI is read as state, not taken from an event. A train a hook's table has
// a line for CI's result for, in the stage it is in (in-pr in yardr's own
// train flow: event=ci stage=in-pr), is judged on the required checks of the
// commit that was reviewed (pr_reviewed_sha, which pr-train stores as the
// train enters the stage), as GitHub reports them when the daemon asks:
//
//	green    every required check concluded success
//	red      a required check failed or timed out
//	pending  one is still running, was cancelled or has not reported: wait
//
// Green and red are put to the table as an event the daemon makes up, with
// the fields event=ci and conclusion=success or failure (ciEnvelope), and
// the line it matches names the outcome reported for the train, as for a
// hook event (github.go): done and changes, in yardr's own.
//
// The daemon asks when it first sees the train in the stage (so CI that finished
// during review, or while the daemon was down, is seen), and again when a
// hook event about the train's pull request arrives: an event says
// only that the state may have changed, never what it is. One green
// workflow among several required ones is therefore pending, and a run
// that is the first to finish moves nothing.
//
// What is required is the base branch's: its protection and its rulesets.
// A base that requires no check gives the daemon nothing to call green, so
// it lands nothing there and a human says merge; a failed check still
// sends the train back.
//
// Every read is logged as a github_rule event on the train, event "ci".
const (
	eventCI = "ci"

	ciGreen   = "green"
	ciRed     = "red"
	ciPending = "pending"

	// The conclusions the made-up event carries, as GitHub names a run's.
	ciSuccess = "success"
	ciFailure = "failure"

	// metaPRReviewed is the commit pr-train pushed as the train entered
	// the stage its pull request waits on CI in: what was reviewed, and
	// what pr-merge merges.
	metaPRReviewed = "pr_reviewed_sha"
)

// ciEnvelope is CI's result for the train's pull request at commit sha as a
// hook's table reads an event: of source, type ci, with what the read found
// as its text and url.
func ciEnvelope(source string, train *store.Bead, pr prBinding, sha, conclusion, text, url string) map[string]any {
	fields := map[string][]string{"event": {eventCI}, "conclusion": {conclusion}, "train": {train.ID}}
	for k, v := range map[string]string{"head_sha": sha, "repo": pr.repo, "number": pr.number, "head_repo": pr.headRepo} {
		if v != "" {
			fields[k] = []string{v}
		}
	}
	return map[string]any{"source": source, "type": eventCI, "action": "completed", "text": text, "url": url, "fields": fields}
}

// ciHook is the hook whose table CI's result for the train is put to: the
// first, by name, with a line for one of the two results for a train in its
// stage. nil when no hook has one, and the train's CI is then not read.
func ciHook(hs []store.Hook, train *store.Bead) *store.Hook {
	pr, sha := boundPR(train), metaString(train.Metadata[metaPRReviewed])
	for i := range hs {
		for _, conclusion := range []string{ciSuccess, ciFailure} {
			if hs[i].Said(ciEnvelope(hs[i].Source, train, pr, sha, conclusion, "", ""), train.Stage) != nil {
				return &hs[i]
			}
		}
	}
	return nil
}

// CIQuery names the commit whose checks CI reads: SHA, in the pull request
// Number of Repo (owner/name) or, with no repo known, of the repository of
// the depot's checkout.
type CIQuery struct {
	Train  string
	Repo   string
	Number string
	SHA    string
	Dir    string // the depot's checkout; "" when the train has no depot
}

// CIChecks is what GitHub says of a pull request and one commit of it.
type CIChecks struct {
	Head  string // the pull request's head commit
	Base  string // its base branch
	State string // open or closed
	// Required are the names of the checks the base branch requires.
	Required []string
	// Checks are the check runs and commit statuses on the commit, the
	// latest of each name.
	Checks []CICheck
}

// CICheck is one check run or commit status. A status is given as a check
// run would be: Status completed with Conclusion success or failure, or
// Status pending.
type CICheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

// result is what the check says of the commit. A cancelled or stale check,
// or one waiting for someone's approval, says nothing: someone, or a newer
// push, stopped it, and it is pending until it runs again.
func (c CICheck) result() string {
	if c.Status != "completed" {
		return ciPending
	}
	switch c.Conclusion {
	case "success", "neutral", "skipped":
		return ciGreen
	case "failure", "timed_out", "startup_failure":
		return ciRed
	}
	return ciPending
}

// ciVerdict is the state of a commit's CI: the checks that make it so
// (the required ones when green, the failed ones when red, the ones waited
// for when pending) and, when red, the first failed check's URL.
type ciVerdict struct {
	state  string
	checks []string
	url    string
	// unrequired: the base requires no check, so nothing can be green.
	unrequired bool
}

// verdict judges the checks. Red wins over pending: a failed check is not
// made good by the ones still running.
func (c CIChecks) verdict() ciVerdict {
	var red, waiting []string
	var redURL string
	fail := func(ck CICheck) {
		red = append(red, ck.Name)
		if redURL == "" {
			redURL = ck.URL
		}
	}
	if len(c.Required) == 0 {
		for _, ck := range c.Checks {
			if ck.result() == ciRed {
				fail(ck)
			}
		}
		if len(red) > 0 {
			return ciVerdict{state: ciRed, checks: red, url: redURL, unrequired: true}
		}
		return ciVerdict{state: ciPending, unrequired: true}
	}
	required := slices.Clone(c.Required)
	slices.Sort(required)
	required = slices.Compact(required)
	for _, name := range required {
		// A name may be reported more than once (a check run and a status,
		// or two apps): all of them must be green.
		state, seen := ciGreen, false
		for _, ck := range c.Checks {
			if ck.Name != name {
				continue
			}
			seen = true
			switch ck.result() {
			case ciRed:
				if state != ciRed {
					fail(ck)
				}
				state = ciRed
			case ciPending:
				if state == ciGreen {
					state = ciPending
				}
			}
		}
		switch {
		case !seen:
			waiting = append(waiting, name+" (not reported)")
		case state == ciPending:
			waiting = append(waiting, name)
		}
	}
	switch {
	case len(red) > 0:
		return ciVerdict{state: ciRed, checks: red, url: redURL}
	case len(waiting) > 0:
		return ciVerdict{state: ciPending, checks: waiting}
	}
	return ciVerdict{state: ciGreen, checks: required}
}

// ghAPI runs `gh api path`, with jq applied to each page of the answer.
func ghAPI(ctx context.Context, dir, path, jq string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", "api", path, "--paginate", "--jq", jq)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api %s: %v: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// ghAPIOptional is for branch policy endpoints: GitHub returns 404 when a
// branch has no protection (and when no ruleset applies), which means that
// endpoint contributes no required checks. Other failures remain errors.
func ghAPIOptional(ctx context.Context, dir, path, jq string) ([]byte, error) {
	out, err := ghAPI(ctx, dir, path, jq)
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		return nil, nil
	}
	return out, err
}

// ghCI asks gh for the pull request, what its base branch requires and the
// checks on q.SHA.
func ghCI(ctx context.Context, q CIQuery) (CIChecks, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// gh fills {owner}/{repo} from the checkout it runs in.
	repo := "repos/" + or(q.Repo, "{owner}/{repo}")
	var c CIChecks
	out, err := ghAPI(ctx, q.Dir, repo+"/pulls/"+q.Number, "{head: .head.sha, base: .base.ref, state: .state}")
	if err != nil {
		return c, err
	}
	var pr struct{ Head, Base, State string }
	if err := json.Unmarshal(out, &pr); err != nil {
		return c, fmt.Errorf("gh api %s/pulls/%s: %v", repo, q.Number, err)
	}
	if pr.Head == "" || pr.Base == "" {
		return c, fmt.Errorf("gh api %s/pulls/%s: no head or base in %s", repo, q.Number, bytes.TrimSpace(out))
	}
	c.Head, c.Base, c.State = pr.Head, pr.Base, pr.State

	// Required by the branch's protection, and by the rulesets that apply
	// to it: a repository may use either, or both.
	branch := url.PathEscape(c.Base)
	for _, ask := range [][2]string{
		{repo + "/branches/" + branch, ".protection.required_status_checks.contexts[]?"},
		{repo + "/rules/branches/" + branch, `.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context`},
	} {
		out, err := ghAPIOptional(ctx, q.Dir, ask[0], ask[1])
		if err != nil {
			return c, err
		}
		for _, name := range strings.Split(string(out), "\n") {
			if name = strings.TrimSpace(name); name != "" && !slices.Contains(c.Required, name) {
				c.Required = append(c.Required, name)
			}
		}
	}

	// GitHub lists the latest check run of each name, and the combined
	// status has the latest status of each context.
	for _, ask := range [][2]string{
		{repo + "/commits/" + q.SHA + "/check-runs?per_page=100", ".check_runs[] | {name, status, conclusion, url: .html_url}"},
		{repo + "/commits/" + q.SHA + "/status?per_page=100", `.statuses[] | {name: .context, url: .target_url,
			status: (if .state == "pending" then "pending" else "completed" end),
			conclusion: (if .state == "success" then "success" elif .state == "pending" then "" else "failure" end)}`},
	} {
		out, err := ghAPI(ctx, q.Dir, ask[0], ask[1])
		if err != nil {
			return c, err
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var ck CICheck
			if err := dec.Decode(&ck); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return c, fmt.Errorf("gh api %s: %v", ask[0], err)
			}
			c.Checks = append(c.Checks, ck)
		}
	}
	return c, nil
}

// ci reads the CI of every open train a hook's table has a line for it for
// (ciHook) whose state has not been read since it got to its stage, since
// the train last changed, or since the last hook event about its pull
// request.
func (d *Dispatcher) ci(ctx context.Context) error {
	if d.ciRead == nil {
		d.ciRead, d.ciWait = map[string]string{}, map[string]*outage{}
	}
	hs, err := d.Store.Hooks(ctx)
	if err != nil {
		return err
	}
	hs = slices.DeleteFunc(hs, func(h store.Hook) bool { return len(h.Says) == 0 })
	var trains []store.Bead
	if len(hs) > 0 {
		if trains, err = d.Store.ListBeads(ctx, store.BeadFilter{Type: store.TypeTrain}); err != nil {
			return err
		}
	}
	in := make(map[string]bool, len(trains))
	for i := range trains {
		train := &trains[i]
		// A held train is a human's: it is read when it is un-held.
		if train.Hold {
			continue
		}
		h := ciHook(hs, train)
		if h == nil {
			continue
		}
		in[train.ID] = true
		sha := metaString(train.Metadata[metaPRReviewed])
		// The revision tells a train that left the stage and came back, or
		// was given another reviewed commit, between two passes.
		if read, ok := d.ciRead[train.ID]; ok && read == train.Revision {
			continue
		}
		if o := d.ciWait[train.ID]; o != nil && d.now().Before(o.until) {
			continue
		}
		d.ciTrain(ctx, train, h, sha)
	}
	// Nothing is remembered of a train whose CI is no longer read.
	for id := range d.ciRead {
		if !in[id] {
			d.ciAgain(id)
		}
	}
	for id := range d.ciWait {
		if !in[id] {
			d.ciAgain(id)
		}
	}
	return nil
}

// ciAgain has the train's CI read on this pass, or the next: something
// about its pull request changed.
func (d *Dispatcher) ciAgain(train string) {
	delete(d.ciRead, train)
	delete(d.ciWait, train)
}

// ciTrain reads the CI of a train at the reviewed commit, puts what it
// finds to h's table and logs what came of it. The read counts until the
// next event, whatever came of it, except when GitHub could not be asked:
// that is tried again, after a wait that doubles from Backoff up to
// BackoffMax.
func (d *Dispatcher) ciTrain(ctx context.Context, train *store.Bead, h *store.Hook, sha string) {
	env, line, res, asked := d.judgeCI(ctx, train, h, sha)
	if asked {
		d.ciRead[train.ID] = train.Revision
		delete(d.ciWait, train.ID)
	} else {
		o := d.ciWait[train.ID]
		if o == nil {
			o = &outage{delay: d.Backoff}
			d.ciWait[train.ID] = o
		} else {
			o.delay = min(o.delay*2, d.BackoffMax)
		}
		o.until = d.now().Add(o.delay)
	}
	if env == nil {
		env = map[string]any{"type": eventCI}
	}
	d.logSaid(ctx, train, h.Name, env, line, *res, map[string]any{"sha": sha})
}

// judgeCI reads the state and, when it is green or red, reports the outcome
// h's table names for it: env is the event made of the state and line the
// line it matched, both nil when it did not come to that. asked is false
// when GitHub could not be asked, so nothing is known.
func (d *Dispatcher) judgeCI(ctx context.Context, train *store.Bead, h *store.Hook, sha string) (env map[string]any, line *store.Say, res *ruleResult, asked bool) {
	pr := boundPR(train)
	if why := pr.other(ghEvent{}); why != "" {
		return nil, nil, ignored("%s", why), true
	}
	if sha == "" {
		return nil, nil, ignored("train has no %s: pr-train stores it as the train enters %s", metaPRReviewed, train.Stage), true
	}
	q := CIQuery{Train: train.ID, Repo: pr.repo, Number: pr.number, SHA: sha}
	if train.Depot != nil {
		if depot, err := d.Store.GetDepot(ctx, *train.Depot); err == nil {
			q.Dir = depot.Path
		}
	}
	read := d.CI
	if read == nil {
		read = ghCI
	}
	checks, err := read(ctx, q)
	if err != nil {
		return nil, nil, failed(fmt.Errorf("CI on %s is unknown: %w", sha, err)), false
	}
	if checks.State != "" && checks.State != "open" {
		return nil, nil, ignored("the PR is %s", checks.State), true
	}
	// What is merged is the reviewed commit: checks on it say nothing of a
	// head pushed since, and pr-merge would not merge that head.
	if checks.Head != sha {
		return nil, nil, ignored("the PR's head %s is not the reviewed commit %s", checks.Head, sha), true
	}
	v := checks.verdict()
	switch v.state {
	case ciGreen:
		env = ciEnvelope(h.Source, train, pr, sha, ciSuccess,
			fmt.Sprintf("CI green on %s: %s (the checks %s requires)", sha, strings.Join(v.checks, ", "), checks.Base), "")
	case ciRed:
		env = ciEnvelope(h.Source, train, pr, sha, ciFailure,
			fmt.Sprintf("CI red on %s: %s failed", sha, strings.Join(v.checks, ", ")), v.url)
	default:
		if v.unrequired {
			return nil, nil, ignored("%s requires no checks, so none can be called green: a human says merge", checks.Base), true
		}
		return nil, nil, ignored("pending: waiting for %s", strings.Join(v.checks, ", ")), true
	}
	if line = h.Said(env, train.Stage); line == nil {
		return env, nil, ignored("%s, and hook %s has no line for that for a train in %s", v.state, h.Name, train.Stage), true
	}
	res = d.report(ctx, train, line, env)
	if res.action != "" {
		res.action = v.state + ": " + res.action
	}
	return env, line, res, true
}
