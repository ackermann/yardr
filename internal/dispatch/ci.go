package dispatch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// CI is read as state, not taken from an event. A train a hook's table has
// a line for CI's result for, in the stage it is in (in-pr in yardr's own
// train flow: event=ci stage=in-pr), is judged on the required checks of the
// commit that was reviewed (pr_reviewed_sha, which pr-train stores as the
// train enters the stage), as the source reports them when the daemon asks:
//
//	green    every required check concluded success
//	red      a required check failed or timed out
//	pending  one is still running, was cancelled or has not reported: wait
//
// Green and red are put to the table as an event the daemon makes up, with
// the fields event=ci and conclusion=success or failure (ciEnvelope), and
// the line it matches names the outcome reported for the train, as for a
// hook event (rules.go): done and changes, in yardr's own.
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
// Every read is logged as a hook_rule event on the train, event "ci".
const (
	eventCI = "ci"

	ciGreen   = "green"
	ciRed     = "red"
	ciPending = "pending"

	// The conclusions the made-up event carries, as the ci command names
	// a run's.
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
// decls are the yard's declared sources, which the train's binding reads
// its pull request URL by.
func ciHook(hs []store.Hook, train *store.Bead, decls []*source.Declaration) *store.Hook {
	sha := metaString(train.Metadata[metaPRReviewed])
	for i := range hs {
		pr := boundPR(train, hs[i].Source, findDecl(decls, hs[i].Source))
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

// CIChecks is what the source says of a pull request and one commit of it,
// in the one tongue the core judges: a check run's, whatever the source
// calls one.
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
	var decls []*source.Declaration
	if len(hs) > 0 && len(trains) > 0 {
		decls = allDeclarations(ctx, d.Store)
	}
	in := make(map[string]bool, len(trains))
	for i := range trains {
		train := &trains[i]
		// A held train is a human's: it is read when it is un-held.
		if train.Hold {
			continue
		}
		h := ciHook(hs, train, decls)
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
		d.ciTrain(ctx, train, h, sha, decls)
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
// next event, whatever came of it, except when the source could not be
// asked: that is tried again, after a wait that doubles from Backoff up to
// BackoffMax.
func (d *Dispatcher) ciTrain(ctx context.Context, train *store.Bead, h *store.Hook, sha string, decls []*source.Declaration) {
	env, line, res, asked := d.judgeCI(ctx, train, h, sha, decls)
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
// when the source could not be asked, so nothing is known.
func (d *Dispatcher) judgeCI(ctx context.Context, train *store.Bead, h *store.Hook, sha string, decls []*source.Declaration) (env map[string]any, line *store.Say, res *ruleResult, asked bool) {
	pr := boundPR(train, h.Source, findDecl(decls, h.Source))
	if why := pr.other(ruleEvent{}); why != "" {
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
		decl := findDecl(decls, h.Source)
		read = func(ctx context.Context, q CIQuery) (CIChecks, error) {
			return d.lookbackCI(ctx, h, h.Source, decl, q)
		}
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
