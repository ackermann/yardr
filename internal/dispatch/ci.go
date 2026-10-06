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

// CI is read as state, not taken from an event. A train in in-pr is judged
// on the required checks of the commit that was reviewed (pr_reviewed_sha,
// which pr-train stores as the train enters in-pr), as GitHub reports them
// when the daemon asks:
//
//	green    every required check concluded success: in-pr to land
//	red      a required check failed or timed out: a fix child, in-pr to open
//	pending  one is still running, was cancelled or has not reported: wait
//
// The daemon asks when it first sees the train in in-pr (so CI that finished
// during review, or while the daemon was down, is seen), and again when a
// github hook event about the train's pull request arrives: an event says
// only that the state may have changed, never what it is. One green
// workflow among several required ones is therefore pending, and a run
// that is the first to finish moves nothing.
//
// What is required is the base branch's: its protection and its rulesets.
// A base that requires no check gives the daemon nothing to call green, so
// it lands nothing there and a human says merge; a failed check still
// sends the train back.
//
// Every read is logged as a github_rule event on the train, rule "ci".
const (
	ruleCI = "ci"

	ciGreen   = "green"
	ciRed     = "red"
	ciPending = "pending"

	// metaPRReviewed is the commit pr-train pushed as the train entered
	// in-pr: what was reviewed, and what pr-merge merges.
	metaPRReviewed = "pr_reviewed_sha"
)

// CIQuery names the commit whose checks CI reads: SHA, in the pull request
// Number of Repo (owner/name) or, with no repo known, of the repository of
// the rig's checkout.
type CIQuery struct {
	Train  string
	Repo   string
	Number string
	SHA    string
	Dir    string // the rig's checkout; "" when the train has no rig
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

// ci reads the CI of every train in in-pr whose state has not been read
// since it got there, since the train last changed, or since the last github
// event about its pull request.
func (d *Dispatcher) ci(ctx context.Context) error {
	trains, err := d.Store.ListBeads(ctx, store.BeadFilter{Stage: "in-pr", Type: store.TypeTrain})
	if err != nil {
		return err
	}
	if d.ciRead == nil {
		d.ciRead, d.ciWait = map[string]string{}, map[string]*outage{}
	}
	in := make(map[string]bool, len(trains))
	for i := range trains {
		train := &trains[i]
		// A held train is a human's: it is read when it is un-held.
		if train.Hold {
			continue
		}
		in[train.ID] = true
		sha := metaString(train.Metadata[metaPRReviewed])
		// The revision tells a train that left in-pr and came back, or was
		// given another reviewed commit, between two passes.
		if read, ok := d.ciRead[train.ID]; ok && read == train.Revision {
			continue
		}
		if o := d.ciWait[train.ID]; o != nil && d.now().Before(o.until) {
			continue
		}
		d.ciTrain(ctx, train, sha)
	}
	// Nothing is remembered of a train that left in-pr.
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

// ciTrain reads the CI of a train in in-pr at the reviewed commit, acts on
// it and logs what it did. The read counts until the next event, whatever
// came of it, except when GitHub could not be asked: that is tried again,
// after a wait that doubles from Backoff up to BackoffMax.
func (d *Dispatcher) ciTrain(ctx context.Context, train *store.Bead, sha string) {
	res, asked := d.judgeCI(ctx, train, sha)
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
	data := map[string]any{"rule": ruleCI, "train": train.ID, "sha": sha, "stage": train.Stage}
	switch {
	case res.err != nil:
		data["error"] = res.err.Error()
		d.Log.Warn("github rule failed", "rule", ruleCI, "train", train.ID, "err", res.err)
	case res.ignored != "":
		data["ignored"] = res.ignored
	default:
		data["action"] = res.action
		d.Log.Info("github rule", "rule", ruleCI, "train", train.ID, "sha", sha, "action", res.action)
	}
	if err := d.Store.LogDispatch(ctx, train.ID, "github_rule", actor, data); err != nil {
		d.Log.Warn("github_rule event", "err", err)
	}
}

// judgeCI reads the state and acts on it. asked is false when GitHub could
// not be asked, so nothing is known.
func (d *Dispatcher) judgeCI(ctx context.Context, train *store.Bead, sha string) (res ruleResult, asked bool) {
	pr := boundPR(train)
	if why := pr.other(ghEvent{}); why != "" {
		return ignored("%s", why), true
	}
	if sha == "" {
		return ignored("train has no %s: pr-train stores it as the train enters in-pr", metaPRReviewed), true
	}
	q := CIQuery{Train: train.ID, Repo: pr.repo, Number: pr.number, SHA: sha}
	if train.Rig != nil {
		if rig, err := d.Store.GetRig(ctx, *train.Rig); err == nil {
			q.Dir = rig.Path
		}
	}
	read := d.CI
	if read == nil {
		read = ghCI
	}
	checks, err := read(ctx, q)
	if err != nil {
		return failed(fmt.Errorf("CI on %s is unknown: %w", sha, err)), false
	}
	if checks.State != "" && checks.State != "open" {
		return ignored("the PR is %s", checks.State), true
	}
	// What is merged is the reviewed commit: checks on it say nothing of a
	// head pushed since, and pr-merge would not merge that head.
	if checks.Head != sha {
		return ignored("the PR's head %s is not the reviewed commit %s", checks.Head, sha), true
	}
	v := checks.verdict()
	at := store.Expect{Stage: "in-pr"}
	switch v.state {
	case ciGreen:
		note := fmt.Sprintf("CI green on %s: %s (the checks %s requires)", sha, strings.Join(v.checks, ", "), checks.Base)
		if _, err := d.Store.AdvanceNote(ctx, train.ID, "land", note, at, actor); err != nil {
			return failed(err), true
		}
		return did("green: in-pr -> land"), true

	case ciRed:
		name := strings.Join(v.checks, ", ")
		fix, made, err := d.ciFix(ctx, train, sha, name, "failure", v.url)
		if err != nil {
			return failed(err), true
		}
		note := fmt.Sprintf("CI red on %s: %s failed: %s\nAdded %s to fix it; the train is integrated again once it lands.",
			sha, name, v.url, fix)
		if _, err := d.Store.AdvanceNote(ctx, train.ID, "open", note, at, actor); err != nil {
			return failed(err), true
		}
		if !made {
			return did("red: in-pr -> open (%s was there)", fix), true
		}
		return did("red: added %s; in-pr -> open", fix), true
	}
	if v.unrequired {
		return ignored("%s requires no checks, so none can be called green: a human says merge", checks.Base), true
	}
	return ignored("pending: waiting for %s", strings.Join(v.checks, ", ")), true
}
