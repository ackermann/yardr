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
// field names an open epic:
//
//	ci_green           a run or suite on the PR head succeeded, epic in in-pr: to land
//	ci_red             it failed or was cancelled, epic in in-pr or review: a fix child, in-pr to open
//	pr_merged          the PR was merged: land (or in-pr, approved) to merged
//	pr_closed          the PR was closed unmerged: in-pr or review to decide
//	changes_requested  a human asked for changes: to open, for the mayor to file the fix
//
// Each event an epic has been judged on is recorded on the epic
// (metaGitHubSeq), so a restarted daemon that reads the log from the start
// again skips what it already did; a rule that finds the epic past the stage
// it would set does nothing. Every action, and every event ignored because
// of the epic's stage, is logged as a github_rule event on the epic.
const (
	ruleGreen   = "ci_green"
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
)

// PRQuery names the pull request whose head PRHead asks GitHub for: by its
// URL (the epic's pr_url) when known, else by number in repo, else by the
// epic's branch in the rig's checkout.
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
		args = append(args, store.Branch(q.Epic))
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
		repo: field("repo"), number: field("number")}
}

// rule is the rule ev falls under, "" for none. head reports whether ev
// tells the pull request's head commit (sha).
func (ev ghEvent) rule() (rule string, head bool) {
	switch ev.typ {
	case "workflow_run", "check_suite":
		if ev.action != "completed" {
			return "", false
		}
		switch ev.conclusion {
		case "success":
			return ruleGreen, false
		case "failure", "cancelled", "timed_out":
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
	if ev.epic == "" || (rule == "" && !head) {
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
	if rule != "" {
		res := d.applyRule(ctx, rule, ev, epic)
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
	if head && ev.sha != "" {
		meta[metaPRHead] = ev.sha
	}
	if _, err := d.Store.UpdateBead(ctx, epic.ID, store.BeadPatch{Metadata: meta}, store.Expect{}, actor); err != nil {
		d.Log.Warn("github rule: marking the event seen", "epic", epic.ID, "seq", ev.seq, "err", err)
	}
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
	case ruleGreen:
		if epic.Stage != "in-pr" {
			return ignored("epic in %s, not in-pr", epic.Stage)
		}
		if r, ok := d.onHead(ctx, ev, epic); !ok {
			return r
		}
		note := fmt.Sprintf("CI green on %s: %s", ev.sha, ev.url)
		if _, err := d.Store.AdvanceNote(ctx, epic.ID, "land", note, at("in-pr"), actor); err != nil {
			return failed(err)
		}
		return did("in-pr -> land")

	case ruleRed:
		if epic.Stage != "in-pr" && epic.Stage != "review" {
			return ignored("epic in %s, not in-pr or review", epic.Stage)
		}
		if ev.sha == "" {
			return ignored("no head_sha")
		}
		if r, ok := d.onHead(ctx, ev, epic); !ok {
			return r
		}
		fix, made, err := d.ciFix(ctx, ev, epic)
		if err != nil {
			return failed(err)
		}
		if epic.Stage == "review" {
			if !made {
				return ignored("%s already fixes CI on %s", fix, ev.sha)
			}
			return did("added %s", fix)
		}
		note := fmt.Sprintf("CI %s on %s: %s\nAdded %s to fix it; the epic is integrated again once it lands.",
			ev.conclusion, ev.sha, ev.url, fix)
		if _, err := d.Store.AdvanceNote(ctx, epic.ID, "open", note, at("in-pr"), actor); err != nil {
			return failed(err)
		}
		if !made {
			return did("in-pr -> open (%s was there)", fix)
		}
		return did("added %s; in-pr -> open", fix)

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
// says: a conclusion on an older commit (a run a push superseded and
// cancelled) says nothing about what would be merged.
func (d *Dispatcher) onHead(ctx context.Context, ev ghEvent, epic *store.Bead) (ruleResult, bool) {
	head, _ := epic.Metadata[metaPRHead].(string)
	if head == "" {
		q := PRQuery{Epic: epic.ID, Repo: ev.repo, Number: ev.number}
		q.URL, _ = epic.Metadata["pr_url"].(string)
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

// ciFix returns the child that fixes CI on ev's commit, adding it when
// there is none. Its handle is made from the epic and the commit, so there
// is one per head_sha however many runs fail on it, also across restarts
// that interrupted adding it.
func (d *Dispatcher) ciFix(ctx context.Context, ev ghEvent, epic *store.Bead) (id string, made bool, err error) {
	id = epic.ID + "-ci-" + ev.sha[:min(len(ev.sha), 7)]
	if _, err = d.Store.GetBead(ctx, id); errors.Is(err, store.ErrNotFound) {
		name := or(ev.workflow, "CI")
		rig := ""
		if epic.Rig != nil {
			rig = *epic.Rig
		}
		body := fmt.Sprintf("%s concluded %s on %s, the head of epic %s's pull request (%s): %s\n\nMake it pass on %s.",
			name, ev.conclusion, ev.sha, epic.ID, store.Branch(epic.ID), ev.url, store.Branch(epic.ID))
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
