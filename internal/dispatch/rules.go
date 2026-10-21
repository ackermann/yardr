package dispatch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A source's events move the bead they are about, so that nothing waits on
// a human for a pull request that is green and approved (see
// docs/line.md#trains). What an event says is data, not Go: the hook
// that recorded it carries a table (store.Hook's Says, set from a pack's
// sources.<source>.says or by yardr hook add --says), and the first line
// of it the event matches names an outcome, which is reported for the bead
// as a session reports one (Store.Done). The bead's flow routes it. A stage
// with no edge for the outcome refuses, and the refusal is logged with the
// outcome and the stage.
//
// The bead an event is about is the open bead that tracks the event's thread
// (a tracks link to the issue or pull request, yardr link), of any type.
// For a pull request no bead tracks it is the open train its train field
// names (the receiver sets it from the head branch, yard/<train>), and
// only when the event is about the one pull request the train is bound to
// (boundPR). Where both find a bead the tracking bead wins: a link says
// which bead the thread is about, a branch name only looks like a train's.
// A run or suite must also be on the pull request's head: one on an older
// commit says nothing of what is there now. A held bead is a human's: it
// is left where it is, whatever the source reports, until it is un-held.
//
// An event about no bead says nothing. It may make the bead that tracks its
// thread, by the hook's other table (makes.go), and the thread's next event
// then finds that bead here.
//
// What the yard itself did in the source says nothing: an event whose actor
// is a crew member's identity there (the label a stage set, the comment a
// crew member wrote) is left alone, so the yard never moves itself.
//
// CI's result for a train whose pull request waits on it is not taken from
// an event but read as state, and put to the same table (see ci.go); a
// table keeps its line for a failed run to the stages before that with the
// key stage.
//
// The events are read from the log in seq order. Each one a bead has been
// judged on is recorded on the bead (metaHookSeq), so a restarted daemon
// that reads the log from the start again skips what it already did. Every
// move, every refusal, and every event left alone because of the bead's
// hold, its pull request, the yard's own hand or a hook without a table is
// logged as a hook_rule event on the bead.

const (
	// metaHookSeq is the seq of the last hook event judged for the bead.
	metaHookSeq = "hook_seq"
	// metaPRHead is the head commit of the train's pull request as the last
	// pull_request (or review) event for it gave it: what CI must have run
	// on for its conclusion to count.
	metaPRHead = "pr_head"

	// metaPRURL, metaPRNumber and metaPRHeadRepo bind the train to one pull
	// request: its URL, its number, and the repository its head branch is
	// in (owner/name). integrate-train stores all three when it opens the
	// pull request.
	metaPRURL      = "pr_url"
	metaPRNumber   = "pr_number"
	metaPRHeadRepo = "pr_head_repo"
)

// PRQuery names the pull request whose head is read live: by its URL (the
// train's pr_url) when known, else by the number the train is bound to, in
// repo or, with no repo known, in the depot's checkout. Never by the
// train's branch, which a fork's pull request has too.
type PRQuery struct {
	Train  string
	URL    string
	Repo   string
	Number string
	Dir    string // the depot's checkout; "" when the train has no depot
}

// ruleEvent is a hook event as the dispatcher reads it: the envelope the
// hook's table is matched on, and the fields that tell which bead and which
// pull request it is about.
type ruleEvent struct {
	seq              uint64
	at               time.Time
	hook             string // the hook that recorded it
	data             map[string]any
	typ              string
	sha, train       string
	repo, number     string
	headRepo, source string
	// threadURL is the URL of the issue or pull request the event is about,
	// and actor who did what it tells of, by its id in the source.
	threadURL, actor string
	// run is whether ev is about a run of CI, whatever became of it, and
	// tellsHead whether it tells the pull request's head commit (sha):
	// an event about the pull request itself, or a review of it.
	run, tellsHead bool
}

func readRuleEvent(e store.Event, decl *source.Declaration) ruleEvent {
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
	thread, _ := e.Data["thread"].(map[string]any)
	who, _ := e.Data["actor"].(map[string]any)
	ev := ruleEvent{seq: e.Seq, at: e.At, hook: str(e.Data["hook"]), data: e.Data, typ: str(e.Data["type"]),
		source: str(e.Data["source"]), sha: field("head_sha"), train: field("train"),
		repo: field("repo"), number: field("number"), headRepo: field("head_repo"),
		threadURL: str(thread["url"]), actor: str(who["id"])}
	if decl != nil && decl.Events != nil {
		ev.run = slices.Contains(decl.Events.Runs, ev.typ)
		ev.tellsHead = slices.Contains(decl.Events.HeadFrom, ev.typ)
	}
	// A source without an events key sends neither: no type of its is a
	// run, and none tells a head.
	// A run's delivery names its pull request by number alone.
	if ev.threadURL == "" && ev.run && ev.repo != "" && ev.number != "" {
		if p, ok := hooks.PullerFor(ev.source, decl); ok {
			ev.threadURL = p.PullURL(ev.repo, ev.number)
		}
	}
	return ev
}

// rulePage bounds the events one read of the log returns.
const rulePage = 500

// rules puts the hook events logged since the last pass to their hooks'
// tables. The first pass after a start reads the whole retained log; the
// trains' metaHookSeq makes that cheap and safe.
func (d *Dispatcher) rules(ctx context.Context) error {
	// The hooks and the crew as they are on this pass, read once each.
	tables := &hookPass{hooks: map[string]*store.Hook{}}
	for {
		evs, err := d.Store.EventsSince(ctx, d.ruleCursor, rulePage)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		for _, e := range evs {
			d.ruleCursor = e.Seq
			if e.Kind == "hook" {
				src, _ := e.Data["source"].(string)
				// A delivery of the source peer came over a link and by
				// no hook: it has no table to be put to, and says nothing
				// of a bead but what its record already learned.
				if src == store.SourcePeer {
					continue
				}
				decl := d.declOf(ctx, src)
				d.hookEvent(ctx, readRuleEvent(e, decl), tables, decl)
			}
		}
	}
}

// hookPass is what one pass over the log reads of the yard, once.
type hookPass struct {
	hooks map[string]*store.Hook
	crew  []store.CrewMember
	asked bool // whether crew was read
}

// hookOf is the hook an event was recorded by, as it is now. One that is
// gone has no table.
func (d *Dispatcher) hookOf(ctx context.Context, name string, pass *hookPass) *store.Hook {
	if h, ok := pass.hooks[name]; ok {
		return h
	}
	h, err := d.Store.GetHook(ctx, name)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("hook event: reading the hook", "hook", name, "err", err)
		}
		h = &store.Hook{Name: name}
	}
	pass.hooks[name] = h
	return h
}

// own reports whether ev tells of something the yard did itself: its actor
// is who a crew member is in the event's source, the one the hook acts as
// (an App's <slug>[bot]) among them. A crew that cannot be read makes
// no event the yard's: the next pass reads it again.
func (d *Dispatcher) own(ctx context.Context, ev ruleEvent, pass *hookPass) bool {
	if ev.actor == "" {
		return false
	}
	if !pass.asked {
		crew, err := d.Store.Crew(ctx)
		if err != nil {
			d.Log.Warn("hook event: reading the crew", "err", err)
			return false
		}
		pass.crew, pass.asked = crew, true
	}
	for _, r := range pass.crew {
		if r.IsIn(ev.source, ev.actor) {
			return true
		}
	}
	return false
}

// about is the bead ev is about, nil when it is about none: the open bead
// that tracks its thread, else the open train of its branch. tracked says
// which, and pr is then the thread as a pull request, when it is one. The
// tracking bead is asked for first, so it wins where a tracked pull request
// is on a train's branch too. decl is the yard's declared source of the
// event's source, nil for a builtin one.
func (d *Dispatcher) about(ctx context.Context, ev ruleEvent, decl *source.Declaration) (bead *store.Bead, pr prBinding, tracked bool) {
	if ev.threadURL != "" {
		links, err := d.Store.IssueLinks(ctx, ev.threadURL)
		if err != nil {
			d.Log.Warn("hook event: reading the thread's links", "thread", ev.threadURL, "err", err)
			return nil, pr, false
		}
		// A source with pull requests gives one both an issue URL and a
		// pull URL. A bead may have been linked with either form, while
		// the payload uses the form for the event type. Try the
		// equivalent URL too.
		if !hasTrackingLink(links, ev.source) {
			if alias := hooks.ThreadAlias(ev.source, decl, ev.threadURL); alias != "" {
				other, err := d.Store.IssueLinks(ctx, alias)
				if err != nil {
					d.Log.Warn("hook event: reading the thread's links", "thread", alias, "err", err)
					return nil, pr, false
				}
				links = append(links, other...)
			}
		}
		for _, l := range links {
			if l.Kind != store.LinkTracks || l.Source != ev.source {
				continue
			}
			b, err := d.Store.GetBead(ctx, l.Bead)
			if err != nil {
				d.Log.Warn("hook event: reading the tracking bead", "bead", l.Bead, "err", err)
				return nil, pr, false
			}
			// A closed bead keeps its link and is moved by nothing: the
			// thread is then no open bead's.
			if b.Status != "closed" {
				pr = prBinding{url: l.URL}
				pr.repo, pr.number = hooks.PullParts(l.Source, decl, l.URL)
				return b, pr, true
			}
		}
	}
	if ev.train == "" {
		return nil, pr, false
	}
	train, err := d.Store.GetBead(ctx, ev.train)
	if errors.Is(err, store.ErrNotFound) {
		return nil, pr, false // a branch that only looks like a train's
	} else if err != nil {
		d.Log.Warn("hook event: reading train", "train", ev.train, "err", err)
		return nil, pr, false
	}
	if train.Type != store.TypeTrain || train.Status == "closed" {
		return nil, pr, false
	}
	return train, boundPR(train, ev.source, decl), false
}

func hasTrackingLink(links []store.Link, source string) bool {
	for _, l := range links {
		if l.Kind == store.LinkTracks && l.Source == source {
			return true
		}
	}
	return false
}

// hookEvent judges one event. Failures are logged, on the bead, and the
// event is not tried again: the next event for the bead, or a human, moves
// it on. decl is the yard's declared source of the event's source, nil
// for a builtin one.
func (d *Dispatcher) hookEvent(ctx context.Context, ev ruleEvent, pass *hookPass, decl *source.Declaration) {
	bead, pr, tracked := d.about(ctx, ev, decl)
	// An event about no bead may make the one that tracks its thread
	// (makes.go). Where a bead came to track it meanwhile, it is that
	// bead's.
	if bead == nil && d.make(ctx, ev, pass, decl) {
		bead, pr, tracked = d.about(ctx, ev, decl)
	}
	if bead == nil || ev.seq <= metaSeq(bead.Metadata[metaHookSeq]) {
		return
	}
	h := d.hookOf(ctx, ev.hook, pass)
	ownAction := d.own(ctx, ev, pass)
	// Every delivery about a thread a bead tracks comes by here, also from
	// a hook that only sets the thread's status (a map and no table). Such
	// a hook says nothing of the bead, and is not logged as saying nothing
	// at each of its events, as it is for a train found by its branch.
	if tracked && len(h.Says) == 0 && !ownAction {
		return
	}
	line := h.Said(ev.data, bead.Stage, bead.Type)
	head := ev.tellsHead
	// An event the table has no line for moves nothing and logs nothing.
	// It is still judged when it tells the pull request's head or that CI
	// ran, which the next read of a train's CI goes by.
	if len(h.Says) > 0 && line == nil && !head && !ev.run && !ownAction {
		return
	}
	// A train found by its branch is judged on the pull request it is bound
	// to only: a branch of the same name in a fork or another repository,
	// or another PR for the branch, says nothing about it, and its head is
	// not the train's PR head either. A train bound to none is moved by no
	// event: the branch name is anyone's to use. A tracked thread is the
	// bead's by its link, and needs no such check.
	elsewhere := ""
	if !tracked {
		elsewhere = pr.other(ev)
	}
	if bead.Type == store.TypeTrain && boundPR(bead, ev.source, decl).other(ev) == "" {
		// Something about the train's pull request changed: its CI is read
		// again, where the table has a line for it. An event elsewhere (a
		// fork's run) has the source asked nothing.
		d.ciAgain(bead.ID)
	}
	var res *ruleResult
	switch {
	case ownAction:
		// The label a stage of the bead set, or a crew member's comment,
		// comes back as an event like any other. It reports nothing.
		res = ignored("the yard's own action")
	case len(h.Says) == 0:
		res = ignored("hook %s has no table of what its events say of a bead (yardr hook add --says, or a pack's sources.%s.says)",
			h.Name, cmp.Or(ev.source, "<source>"))
	case line == nil:
	case elsewhere != "":
		res = ignored("%s", elsewhere)
	case bead.Hold:
		res = ignored("%s is held", beadWord(bead))
	default:
		res = d.said(ctx, ev, bead, pr, line, decl, h)
	}
	if res != nil {
		d.logSaid(ctx, bead, h.Name, ev.data, line, *res, map[string]any{"sha": ev.sha, "seq": ev.seq})
	}
	meta := map[string]any{metaHookSeq: ev.seq}
	if head && ev.sha != "" && elsewhere == "" {
		meta[metaPRHead] = ev.sha
	}
	seen, err := d.Store.UpdateBead(ctx, bead.ID, store.BeadPatch{Metadata: meta}, store.Expect{}, actor)
	if err != nil {
		d.Log.Warn("hook event: marking the event seen", "bead", bead.ID, "seq", ev.seq, "err", err)
	} else if elsewhere != "" && d.ciRead[bead.ID] == bead.Revision {
		// Marking the event seen changed the train, and nothing else did:
		// an event that is not about its pull request is no reason to
		// read its CI again (see ci).
		d.ciRead[bead.ID] = seen.Revision
	}
}

// beadWord is what a rule's words call the bead: a train, or a bead.
func beadWord(b *store.Bead) string {
	if b.Type == store.TypeTrain {
		return "train"
	}
	return "bead"
}

// said reports the outcome line names for the bead, on an event about its
// thread, the pull request pr when it is one. A run's result counts for the
// pull request's head only. decl is the yard's declared source of the
// event's source, nil for a builtin one, and h the hook that recorded it.
func (d *Dispatcher) said(ctx context.Context, ev ruleEvent, bead *store.Bead, pr prBinding, line *store.Say, decl *source.Declaration, h *store.Hook) *ruleResult {
	if ev.run {
		if ev.sha == "" {
			return ignored("no head_sha")
		}
		if r, ok := d.onHead(ctx, ev, bead, pr, decl, h); !ok {
			return r
		}
	}
	return d.report(ctx, bead, h, line, ev.data)
}

// report is the move itself: the outcome goes through the bead's flow as a
// session's would (yardr bead done --outcome), from the stage the line was
// matched in, reported as the hook's crew member (Hook's As) when it has
// one, else as the yard. The table is the operator's own mapping, so what
// it says crosses a stage of people the member belongs to, as their own
// done would; a hook without one reports as the yard, which such a stage
// refuses. The store tells this path from any other call that names the
// member by nothing new: Done follows the stage's outcomes edges only,
// never a manual move, and from a pane the move is its session's or its
// crew member's whatever actor it names (paneAuthor), so an agent talked
// into naming the member moves nothing. What the store refuses (a stage
// with no edge for the outcome, a group whose members leave the hook's
// member out) is the result, not a failure.
func (d *Dispatcher) report(ctx context.Context, bead *store.Bead, h *store.Hook, line *store.Say, env map[string]any) *ruleResult {
	moved, err := d.Store.Done(ctx, bead.ID, line.Outcome, sayNote(line, env), store.Expect{Stage: bead.Stage}, cmp.Or(h.As, actor))
	if err != nil {
		return &ruleResult{refused: err.Error()}
	}
	return did("%s -> %s", bead.Stage, moved.Stage)
}

// sayNote is the note a move made for an event adds: the line's own words
// and where to look, what the event said, and what it was.
func sayNote(line *store.Say, env map[string]any) string {
	str := func(v any) string { s, _ := v.(string); return s }
	source := str(env["source"])
	// The line's words come first; a line without any leaves the event's.
	first, text := line.Note, str(env["text"])
	if first == "" {
		first, text = text, ""
	}
	first = cmp.Or(first, cmp.Or(source, "the source")+" says "+line.Outcome)
	if url := str(env["url"]); url != "" {
		first += ": " + url
	}
	out := []string{first}
	if text != "" {
		out = append(out, text)
	}
	what := strings.Join(strings.Fields(source+" "+str(env["type"])+" "+str(env["action"])), " ")
	if who, _ := env["actor"].(map[string]any); str(who["id"]) != "" {
		what += ", by " + str(who["id"])
	}
	return strings.Join(append(out, "("+what+")"), "\n")
}

// logSaid logs what came of putting the event with envelope env to hook's
// table for the bead, as a hook_rule event on it: the line matched and
// its outcome, when one was, and the move, the refusal, or why nothing was
// tried. more is what the caller knows besides (the commit, the event's seq).
// The event names the bead as bead, and as train too when it is one, which
// is how it was named while only trains were moved.
func (d *Dispatcher) logSaid(ctx context.Context, bead *store.Bead, hook string, env map[string]any, line *store.Say, res ruleResult, more map[string]any) {
	event, _ := env["type"].(string)
	data := map[string]any{"hook": hook, "event": event, "bead": bead.ID, "stage": bead.Stage}
	if bead.Type == store.TypeTrain {
		data["train"] = bead.ID
	}
	for k, v := range more {
		data[k] = v
	}
	if line != nil {
		data["says"], data["outcome"] = line.String(), line.Outcome
	}
	switch {
	case res.err != nil:
		data["error"] = res.err.Error()
		d.Log.Warn("hook event failed", "hook", hook, "event", event, "bead", bead.ID, "err", res.err)
	case res.refused != "":
		data["refused"] = res.refused
		d.Log.Info("hook event refused", "hook", hook, "event", event, "bead", bead.ID, "outcome", data["outcome"], "stage", bead.Stage, "why", res.refused)
	case res.ignored != "":
		data["ignored"] = res.ignored
	default:
		data["action"] = res.action
		d.Log.Info("hook event", "hook", hook, "event", event, "bead", bead.ID, "outcome", data["outcome"], "action", res.action)
	}
	if err := d.Store.LogDispatch(ctx, bead.ID, "hook_rule", actor, data); err != nil {
		d.Log.Warn("hook_rule event", "err", err)
	}
}

// prBinding is the pull request a train is bound to: number in repo
// (owner/name, the repository the PR is in), its head branch in headRepo.
type prBinding struct {
	url, repo, number, headRepo string
}

// boundPR reads the train's binding from its metadata. A train bound before
// integrate-train stored pr_number and pr_head_repo has only pr_url: its
// number is the URL's, and its head is taken to be in the PR's own
// repository, as it is for every pull request integrate-train opens from the
// remote it pushed to. source names whose pull request URL pr_url is, and
// decl is the yard's declared source of that name, nil for a builtin one.
func boundPR(train *store.Bead, source string, decl *source.Declaration) prBinding {
	pr := prBinding{url: metaString(train.Metadata[metaPRURL]), number: metaString(train.Metadata[metaPRNumber]),
		headRepo: metaString(train.Metadata[metaPRHeadRepo])}
	var urlNumber string
	pr.repo, urlNumber = hooks.PullParts(source, decl, pr.url)
	if pr.number == "" {
		pr.number = urlNumber
	}
	if pr.headRepo == "" {
		pr.headRepo = pr.repo
	}
	return pr
}

// other says why ev is not about the bound pull request: the train is bound
// to none, or the event's repository, number or head repository differ. ""
// when it is about it. An event that names no pull request number (a run
// the source lists no PR for, as for every run on a fork's branch) passes
// on its repositories alone; one that names no head repository (a check
// suite, whose head_branch the source leaves empty for a fork) on the
// rest.
func (pr prBinding) other(ev ruleEvent) string {
	if pr.number == "" {
		if pr.url != "" {
			return fmt.Sprintf("pr_url %s is not a pull request URL", pr.url)
		}
		return "train is bound to no pull request (integrate-train stores pr_number and pr_head_repo)"
	}
	name := pr.repo + "#" + pr.number
	switch {
	case pr.repo != "" && ev.repo != "" && !strings.EqualFold(ev.repo, pr.repo):
		return fmt.Sprintf("event is in %s, not the train's PR %s", ev.repo, name)
	case ev.number != "" && ev.number != pr.number:
		return fmt.Sprintf("event is for %s#%s, not the train's PR %s", pr.repo, ev.number, name)
	case pr.headRepo != "" && ev.headRepo != "" && !strings.EqualFold(ev.headRepo, pr.headRepo):
		return fmt.Sprintf("event's head is in %s, not %s, the head repository of the train's PR %s", ev.headRepo, pr.headRepo, name)
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

// ruleResult is what came of an event: the move, the store's refusal of it,
// why nothing was tried, or what failed before it could be.
type ruleResult struct {
	action, refused, ignored string
	err                      error
}

func did(format string, args ...any) *ruleResult {
	return &ruleResult{action: fmt.Sprintf(format, args...)}
}

func ignored(format string, args ...any) *ruleResult {
	return &ruleResult{ignored: fmt.Sprintf(format, args...)}
}

func failed(err error) *ruleResult { return &ruleResult{err: err} }

// onHead reports whether ev's commit is the head of the bead's pull
// request pr, as the last event that told it gave it or, without one, as
// the source says live: a failure on an older commit (a run a push
// superseded) says nothing about what is under review. decl is the yard's
// declared source of the event's source, nil when the yard declares none,
// and h the hook that recorded it.
func (d *Dispatcher) onHead(ctx context.Context, ev ruleEvent, bead *store.Bead, pr prBinding, decl *source.Declaration, h *store.Hook) (*ruleResult, bool) {
	head, _ := bead.Metadata[metaPRHead].(string)
	if head == "" {
		q := PRQuery{Train: bead.ID, URL: pr.url, Repo: pr.repo, Number: pr.number}
		if bead.Depot != nil {
			if depot, err := d.Store.GetDepot(ctx, *bead.Depot); err == nil {
				q.Dir = depot.Path
			}
		}
		prHead := d.PRHead
		if prHead == nil {
			prHead = func(ctx context.Context, q PRQuery) (string, error) {
				return d.lookbackHead(ctx, h, ev.source, decl, q)
			}
		}
		var err error
		if head, err = prHead(ctx, q); err != nil {
			return failed(fmt.Errorf("the PR's head is unknown: %w", err)), false
		}
	}
	if ev.sha == "" || ev.sha != head {
		return ignored("%s is not the PR's head %s", ev.sha, head), false
	}
	return nil, true
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
