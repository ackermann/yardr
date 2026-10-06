package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// The github rules move an epic whose pull request GitHub reports on, so
// that nothing waits on a human for a PR that is green and approved (see
// docs/pipeline.md#epics). They read the `hook` events the github adapter
// records (internal/hooks), in seq order, and act only on those whose epic
// field names an open epic, and only when the event is about the one pull
// request the epic is bound to (boundPR):
//
//	ci_red             a run or suite on the PR head failed or timed out, epic in review: a fix child
//	pr_merged          the PR was merged: land (or in-pr, approved) to merged
//	pr_closed          the PR was closed unmerged: in-pr or review to decide
//	changes_requested  a human asked for changes: to open, for the yardmaster to file the fix
//
// No rule takes CI's result from an event for an epic in in-pr: there a
// run or suite, whatever its conclusion, and any event about the pull
// request only has the state read again (see ci.go). A cancelled run or
// suite is not red: someone or a newer push stopped it, so it says nothing
// about the commit. A held epic is a human's: the rules leave it where it
// is, whatever GitHub reports, until it is un-held.
//
// Each event an epic has been judged on is recorded on the epic
// (metaGitHubSeq), so a restarted daemon that reads the log from the start
// again skips what it already did; a rule that finds the epic past the stage
// it would set does nothing. Every action, and every event ignored because
// of the epic's stage or hold, is logged as a github_rule event on the epic.
const (
	ruleRed     = "ci_red"
	ruleMerged  = "pr_merged"
	ruleClosed  = "pr_closed"
	ruleChanges = "changes_requested"
)

// Metadata the rules keep on an epic.
const (
	// metaGitHubSeq is the seq of the last github hook event judged for
	// the epic.
	metaGitHubSeq = "github_seq"
	// metaPRHead is the head commit of the epic's pull request as the last
	// pull_request (or review) event for it gave it: what CI must have run
	// on for its conclusion to count.
	metaPRHead = "pr_head"

	// metaPRURL, metaPRNumber and metaPRHeadRepo bind the epic to one pull
	// request: its URL, its number, and the repository its head branch is
	// in (owner/name). integrate-epic stores all three when it opens the
	// pull request.
	metaPRURL      = "pr_url"
	metaPRNumber   = "pr_number"
	metaPRHeadRepo = "pr_head_repo"
)

// PRQuery names the pull request whose head PRHead asks GitHub for: by its
// URL (the epic's pr_url) when known, else by the number the epic is bound
// to, in repo or, with no repo known, in the rig's checkout. Never by the
// epic's branch, which a fork's pull request has too.
type PRQuery struct {
	Epic   string
	URL    string
	Repo   string
	Number string
	Dir    string // the rig's checkout; "" when the epic has no rig
}

// ghPRHead asks gh for the head commit of the epic's pull request.
func ghPRHead(ctx context.Context, q PRQuery) (string, error) {
	args := []string{"pr", "view"}
	switch {
	case q.URL != "":
		args = append(args, q.URL)
	case q.Repo != "" && q.Number != "":
		args = append(args, q.Number, "-R", q.Repo)
	default:
		args = append(args, q.Number)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", append(args, "--json", "headRefOid", "--jq", ".headRefOid")...)
	cmd.Dir = q.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// ghEvent is the part of a github hook event the rules read.
type ghEvent struct {
	seq                  uint64
	typ, action          string
	actor, url, text     string
	conclusion, workflow string
	sha, epic, merged    string
	state, repo, number  string
	headRepo             string
}

func readGHEvent(e store.Event) ghEvent {
	str := func(v any) string { s, _ := v.(string); return s }
	fields, _ := e.Data["fields"].(map[string]any)
	field := func(k string) string {
		switch v := fields[k].(type) {
		case []any:
			if len(v) > 0 {
				return str(v[0])
			}
		case []string:
			if len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	actor, _ := e.Data["actor"].(map[string]any)
	return ghEvent{seq: e.Seq, typ: str(e.Data["type"]), action: str(e.Data["action"]),
		actor: str(actor["id"]), url: str(e.Data["url"]), text: str(e.Data["text"]),
		conclusion: field("conclusion"), workflow: field("workflow"), sha: field("head_sha"),
		epic: field("epic"), merged: field("merged"), state: field("state"),
		repo: field("repo"), number: field("number"), headRepo: field("head_repo")}
}

// rule is the rule ev falls under, "" for none. head reports whether ev
// tells the pull request's head commit (sha).
func (ev ghEvent) rule() (rule string, head bool) {
	switch ev.typ {
	case "workflow_run", "check_suite":
		if ev.action != "completed" {
			return "", false
		}
		if ev.conclusion == "failure" || ev.conclusion == "timed_out" {
			return ruleRed, false
		}
	case "pull_request":
		if ev.action == "closed" {
			if ev.merged == "true" {
				return ruleMerged, false
			}
			return ruleClosed, false
		}
		return "", true
	case "pull_request_review":
		// GitHub Apps and Actions review as <name>[bot]; the yard itself
		// posts no reviews (pr-epic puts its review in the PR's body), so
		// any other reviewer is a human.
		if ev.action == "submitted" && ev.state == "changes_requested" && !strings.HasSuffix(ev.actor, "[bot]") {
			return ruleChanges, true
		}
		return "", true
	}
	return "", false
}

// ci reports whether ev is about a run of CI, whatever became of it.
func (ev ghEvent) ci() bool { return ev.typ == "workflow_run" || ev.typ == "check_suite" }

// githubPage bounds the events one read of the log returns.
const githubPage = 500

// github applies the rules to the github hook events logged since the last
// pass. The first pass after a start reads the whole retained log; the
// epics' metaGitHubSeq makes that cheap and safe.
func (d *Dispatcher) github(ctx context.Context) error {
	for {
		evs, err := d.Store.EventsSince(ctx, d.ghCursor, githubPage)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		for _, e := range evs {
			d.ghCursor = e.Seq
			if e.Kind == "hook" && e.Data["source"] == "github" {
				d.githubEvent(ctx, readGHEvent(e))
			}
		}
	}
}

// githubEvent judges one event. Failures are logged, on the epic, and the
// event is not tried again: the next event for the epic, or a human, moves
// it on.
func (d *Dispatcher) githubEvent(ctx context.Context, ev ghEvent) {
	rule, head := ev.rule()
	if ev.epic == "" || (rule == "" && !head && !ev.ci()) {
		return
	}
	epic, err := d.Store.GetBead(ctx, ev.epic)
	if errors.Is(err, store.ErrNotFound) {
		return // a branch that only looks like an epic's
	} else if err != nil {
		d.Log.Warn("github rule: reading epic", "epic", ev.epic, "err", err)
		return
	}
	if epic.Type != store.TypeEpic || epic.Status == "closed" || ev.seq <= metaSeq(epic.Metadata[metaGitHubSeq]) {
		return
	}
	// An epic is judged on the pull request it is bound to only: a branch
	// of the same name in a fork or another repository, or another PR for
	// the branch, says nothing about it, and its head is not the epic's PR
	// head either. An epic bound to none is moved by no event: the branch
	// name is anyone's to use.
	pr := boundPR(epic)
	elsewhere := pr.other(ev)
	if elsewhere == "" {
		// Something about the epic's pull request changed: its CI is read
		// again, when it is in in-pr. An event elsewhere (a fork's run)
		// has GitHub asked nothing.
		d.ciAgain(epic.ID)
	}
	if rule == ruleRed && epic.Stage == "in-pr" {
		rule = "" // read as state, by ci
	}
	if rule != "" {
		var res ruleResult
		if elsewhere != "" {
			res = ignored("%s", elsewhere)
		} else if epic.Hold {
			res = ignored("epic is held")
		} else {
			res = d.applyRule(ctx, rule, ev, epic)
		}
		data := map[string]any{"rule": rule, "epic": epic.ID, "sha": ev.sha, "seq": ev.seq, "stage": epic.Stage}
		switch {
		case res.err != nil:
			data["error"] = res.err.Error()
			d.Log.Warn("github rule failed", "rule", rule, "epic", epic.ID, "err", res.err)
		case res.ignored != "":
			data["ignored"] = res.ignored
		default:
			data["action"] = res.action
			d.Log.Info("github rule", "rule", rule, "epic", epic.ID, "sha", ev.sha, "action", res.action)
		}
		if err := d.Store.LogDispatch(ctx, epic.ID, "github_rule", actor, data); err != nil {
			d.Log.Warn("github_rule event", "err", err)
		}
	}
	meta := map[string]any{metaGitHubSeq: ev.seq}
	if head && ev.sha != "" && elsewhere == "" {
		meta[metaPRHead] = ev.sha
	}
	seen, err := d.Store.UpdateBead(ctx, epic.ID, store.BeadPatch{Metadata: meta}, store.Expect{}, actor)
	if err != nil {
		d.Log.Warn("github rule: marking the event seen", "epic", epic.ID, "seq", ev.seq, "err", err)
	} else if elsewhere != "" && d.ciRead[epic.ID] == epic.Revision {
		// Marking the event seen changed the epic, and nothing else did:
		// an event that is not about its pull request is no reason to
		// read its CI again (see ci).
		d.ciRead[epic.ID] = seen.Revision
	}
}

// prBinding is the pull request an epic is bound to: number in repo
// (owner/name, the repository the PR is in), its head branch in headRepo.
type prBinding struct {
	url, repo, number, headRepo string
}

// boundPR reads the epic's binding from its metadata. An epic bound before
// integrate-epic stored pr_number and pr_head_repo has only pr_url: its
// number is the URL's, and its head is taken to be in the PR's own
// repository, as it is for every pull request integrate-epic opens from the
// remote it pushed to.
func boundPR(epic *store.Bead) prBinding {
	pr := prBinding{url: metaString(epic.Metadata[metaPRURL]), number: metaString(epic.Metadata[metaPRNumber]),
		headRepo: metaString(epic.Metadata[metaPRHeadRepo])}
	var urlNumber string
	pr.repo, urlNumber = parsePRURL(pr.url)
	if pr.number == "" {
		pr.number = urlNumber
	}
	if pr.headRepo == "" {
		pr.headRepo = pr.repo
	}
	return pr
}

// other says why ev is not about the bound pull request: the epic is bound
// to none, or the event's repository, number or head repository differ. ""
// when it is about it. An event that names no pull request number (a run
// GitHub lists no PR for, as for every run on a fork's branch) passes on
// its repositories alone; one that names no head repository (a check
// suite, whose head_branch GitHub leaves empty for a fork) on the rest.
func (pr prBinding) other(ev ghEvent) string {
	if pr.number == "" {
		if pr.url != "" {
			return fmt.Sprintf("pr_url %s is not a pull request URL", pr.url)
		}
		return "epic is bound to no pull request (integrate-epic stores pr_number and pr_head_repo)"
	}
	name := pr.repo + "#" + pr.number
	switch {
	case pr.repo != "" && ev.repo != "" && !strings.EqualFold(ev.repo, pr.repo):
		return fmt.Sprintf("event is in %s, not the epic's PR %s", ev.repo, name)
	case ev.number != "" && ev.number != pr.number:
		return fmt.Sprintf("event is for %s#%s, not the epic's PR %s", pr.repo, ev.number, name)
	case pr.headRepo != "" && ev.headRepo != "" && !strings.EqualFold(ev.headRepo, pr.headRepo):
		return fmt.Sprintf("event's head is in %s, not %s, the head repository of the epic's PR %s", ev.headRepo, pr.headRepo, name)
	}
	return ""
}

// metaString reads a string kept in metadata; a number (pr_number set
// through the API as one) is written as its digits.
func metaString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}

// parsePRURL reads owner/repo and the number from a GitHub pull request
// URL; "" when url is not one.
func parsePRURL(url string) (repo, number string) {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(url, "/"), "https://github.com/")
	if !ok {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[2] != "pull" || parts[3] == "" {
		return "", ""
	}
	if _, err := strconv.Atoi(parts[3]); err != nil {
		return "", ""
	}
	return parts[0] + "/" + parts[1], parts[3]
}

// ruleResult is what a rule did: an action, or why it did nothing.
type ruleResult struct {
	action, ignored string
	err             error
}

func did(format string, args ...any) ruleResult {
	return ruleResult{action: fmt.Sprintf(format, args...)}
}

func ignored(format string, args ...any) ruleResult {
	return ruleResult{ignored: fmt.Sprintf(format, args...)}
}

func failed(err error) ruleResult { return ruleResult{err: err} }

func (d *Dispatcher) applyRule(ctx context.Context, rule string, ev ghEvent, epic *store.Bead) ruleResult {
	at := func(stage string) store.Expect { return store.Expect{Stage: stage} }
	switch rule {
	case ruleRed:
		// In review only: an epic in in-pr is judged on its required
		// checks (ci.go), and gets its fix child there.
		if epic.Stage != "review" {
			return ignored("epic in %s, not review", epic.Stage)
		}
		if ev.sha == "" {
			return ignored("no head_sha")
		}
		if r, ok := d.onHead(ctx, ev, epic); !ok {
			return r
		}
		fix, made, err := d.ciFix(ctx, epic, ev.sha, or(ev.workflow, "CI"), ev.conclusion, ev.url)
		if err != nil {
			return failed(err)
		}
		if !made {
			return ignored("%s already fixes CI on %s", fix, ev.sha)
		}
		return did("added %s", fix)

	case ruleMerged:
		note := "PR merged: " + ev.url
		switch epic.Stage {
		case "land":
			// pr-merge merged it and still has the rig's checkout to
			// bring up; it reports done itself.
			if epic.Session != nil {
				return ignored("epic in land with session %s, which lands it", *epic.Session)
			}
			if _, err := d.Store.Done(ctx, epic.ID, "", note, at("land"), actor); err != nil {
				return failed(err)
			}
			return did("land -> merged")
		case "in-pr":
			// Merged by hand: through the flow, in-pr -> land -> merged.
			if _, err := d.Store.AdvanceNote(ctx, epic.ID, "land", note+" (by hand)", at("in-pr"), actor); err != nil {
				return failed(err)
			}
			if _, err := d.Store.Done(ctx, epic.ID, "", "", at("land"), actor); err != nil {
				return failed(err)
			}
			return did("in-pr -> land -> merged")
		case "approved":
			if _, err := d.Store.AdvanceNote(ctx, epic.ID, "merged", note+" (by hand)", at("approved"), actor); err != nil {
				return failed(err)
			}
			return did("approved -> merged")
		}
		return ignored("epic in %s, not land, in-pr or approved", epic.Stage)

	case ruleClosed:
		if epic.Stage != "in-pr" && epic.Stage != "review" {
			return ignored("epic in %s, not in-pr or review", epic.Stage)
		}
		note := fmt.Sprintf("PR closed unmerged: reopen, or drop the epic? %s", ev.url)
		if _, err := d.Store.AdvanceNote(ctx, epic.ID, "decide", note, at(epic.Stage), actor); err != nil {
			return failed(err)
		}
		return did("%s -> decide", epic.Stage)

	case ruleChanges:
		switch epic.Stage {
		case "review", "approved", "in-pr", "land":
		default:
			return ignored("epic in %s, not under review or in its PR", epic.Stage)
		}
		if epic.Stage == "land" && epic.Session != nil {
			return ignored("epic in land with session %s, which is merging it", *epic.Session)
		}
		note := fmt.Sprintf("Changes requested on the PR by %s: %s\n%s\nFile the fix as a child of the epic.", ev.actor, ev.url, ev.text)
		if _, err := d.Store.AdvanceNote(ctx, epic.ID, "open", note, at(epic.Stage), actor); err != nil {
			return failed(err)
		}
		return did("%s -> open", epic.Stage)
	}
	return ignored("no rule %s", rule)
}

// onHead reports whether ev's commit is the head of the epic's pull
// request, as the last pull_request event gave it or, without one, as gh
// says: a failure on an older commit (a run a push superseded) says
// nothing about what is under review.
func (d *Dispatcher) onHead(ctx context.Context, ev ghEvent, epic *store.Bead) (ruleResult, bool) {
	head, _ := epic.Metadata[metaPRHead].(string)
	if head == "" {
		pr := boundPR(epic)
		q := PRQuery{Epic: epic.ID, URL: pr.url, Repo: pr.repo, Number: pr.number}
		if epic.Rig != nil {
			if rig, err := d.Store.GetRig(ctx, *epic.Rig); err == nil {
				q.Dir = rig.Path
			}
		}
		prHead := d.PRHead
		if prHead == nil {
			prHead = ghPRHead
		}
		var err error
		if head, err = prHead(ctx, q); err != nil {
			return failed(fmt.Errorf("the PR's head is unknown: %w", err)), false
		}
	}
	if ev.sha == "" || ev.sha != head {
		return ignored("%s is not the PR's head %s", ev.sha, head), false
	}
	return ruleResult{}, true
}

// ciFix returns the child that fixes CI on commit sha, where name (a
// workflow, or the failed checks) concluded conclusion, adding it when
// there is none. Its handle is made from the epic and the commit, so there
// is one per commit however many runs fail on it, also across restarts
// that interrupted adding it.
func (d *Dispatcher) ciFix(ctx context.Context, epic *store.Bead, sha, name, conclusion, url string) (id string, made bool, err error) {
	id = epic.ID + "-ci-" + sha[:min(len(sha), 7)]
	if _, err = d.Store.GetBead(ctx, id); errors.Is(err, store.ErrNotFound) {
		rig := ""
		if epic.Rig != nil {
			rig = *epic.Rig
		}
		body := fmt.Sprintf("%s concluded %s on %s, the head of epic %s's pull request (%s): %s\n\nMake it pass on %s.",
			name, conclusion, sha, epic.ID, store.Branch(epic.ID), url, store.Branch(epic.ID))
		_, err = d.Store.CreateBead(ctx, store.NewBead{ID: id, Title: fmt.Sprintf("Fix CI on epic %s: %s", epic.ID, name),
			Body: body, Type: "bug", Stage: "backlog", Priority: epic.Priority, Rig: rig}, actor)
		made = err == nil
	}
	if err != nil {
		return "", false, err
	}
	fam, err := d.Store.Family(ctx, id)
	if err != nil {
		return "", false, err
	}
	if fam.Epic != epic.ID {
		if err := d.Store.AddDep(ctx, epic.ID, id, store.DepParent, actor); err != nil {
			return "", false, err
		}
	}
	return id, made, nil
}

// metaSeq reads a seq kept in metadata, which JSON may have made a float.
func metaSeq(v any) uint64 {
	switch n := v.(type) {
	case float64:
		return uint64(n)
	case uint64:
		return n
	case int:
		return uint64(n)
	case string:
		u, _ := strconv.ParseUint(n, 10, 64)
		return u
	}
	return 0
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
