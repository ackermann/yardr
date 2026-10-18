package dispatch

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// An event about a thread no open bead tracks can make the bead that tracks
// it, so that an issue or a pull request from outside enters the yard and
// its later events find a bead to say something to (rules.go). What makes
// one is data, as what an event says is: the hook's other table
// (store.Hook's Makes, from a pack's sources.<source>.makes or yardr hook
// add --makes), whose first line the event matches names the bead's type,
// its stage and, optionally, its depot.
//
// Without a depot on the line the bead is made in the one depot whose
// repository the event is in: the repository its origin remote names,
// read at each event. An event no depot is found for makes nothing, and
// is logged as ignored with why.
//
// A closed bead that tracks the thread does not stand in the way: the new
// bead takes the link and the closed one keeps a relates link. That no open
// bead tracks the thread is checked again in the write that makes the bead
// (store.NewBead's Untracked), so a second event about a thread makes no
// second bead.
//
// An event from before a bead that tracks its thread was closed was that
// bead's, and makes nothing when it is read again: only what happens to the
// thread after the close makes the next bead.
//
// An event is put to the table once. The bead it made carries its seq
// (metaHookSeq), and every event a line matched is logged as a hook_rule
// event with the line under makes, on the bead when one was made. A
// restarted daemon, which reads the log from the start, finds in those
// events how far each hook's table was put to its events, and makes nothing
// of an older one. Nor of one from before the hook was last saved: a table
// says nothing of what came before it.

// DepotRepoQuery is what DepotRepo is asked: the depot whose repository at
// the source is wanted. Decl is the yard's declared source of that name,
// nil for a builtin one.
type DepotRepoQuery struct {
	Source string
	Decl   *source.Declaration
	Depot  store.Depot
}

// gitDepotRepo reads the depot's repository at the event's source from its
// origin remote, as owner/name: "" for a source that keeps no repositories,
// a depot that has no checkout, or an origin that is elsewhere.
func gitDepotRepo(ctx context.Context, q DepotRepoQuery) (string, error) {
	r, ok := hooks.RepoerFor(q.Source, q.Decl)
	if !ok || q.Depot.IsDir() || q.Depot.Path == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", q.Depot.Path, "remote", "get-url", store.PeerRemote)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url %s: %v: %s", store.PeerRemote, err, strings.TrimSpace(stderr.String()))
	}
	return r.RemoteRepo(string(out)), nil
}

// depotFor is the depot a bead is made in for ev by line: the line's, else
// the one whose repository ev is in. why says what stood in the way of
// finding one. decl is the yard's declared source of the event's source,
// nil for a builtin one.
func (d *Dispatcher) depotFor(ctx context.Context, ev ruleEvent, line *store.Make, decl *source.Declaration) (depot, why string) {
	if line.Depot != "" {
		return line.Depot, ""
	}
	if ev.repo == "" {
		return "", "the event names no repository, and the line no depot (-> <type>:<stage>@<depot>)"
	}
	depots, err := d.Store.Depots(ctx)
	if err != nil {
		return "", "reading the depots: " + err.Error()
	}
	ask := d.DepotRepo
	if ask == nil {
		ask = gitDepotRepo
	}
	var in, unread []string
	for _, dp := range depots {
		repo, err := ask(ctx, DepotRepoQuery{Source: ev.source, Decl: decl, Depot: dp})
		if err != nil {
			// Asked again at the next event: a remote can be added.
			unread = append(unread, fmt.Sprintf("%s (%v)", dp.Name, err))
			continue
		}
		if repo != "" && strings.EqualFold(repo, ev.repo) {
			in = append(in, dp.Name)
		}
	}
	switch {
	case len(in) == 1:
		return in[0], ""
	case len(in) > 1:
		return "", fmt.Sprintf("depots %s are all of %s: the line must name one (-> <type>:<stage>@<depot>)", strings.Join(in, ", "), ev.repo)
	case len(unread) > 0:
		return "", fmt.Sprintf("no depot is known to be of %s, and the repository of %s could not be read", ev.repo, strings.Join(unread, ", "))
	}
	return "", fmt.Sprintf("no depot's origin is %s: the line must name the depot (-> <type>:<stage>@<depot>)", ev.repo)
}

// makesFrom is the seq of the last event hook's table of what its events
// make was put to, or before which it was not there: an older event makes
// nothing. The log is read once on first need, then only for events added
// since the last read, so a hook changed while this dispatcher is running
// still takes effect after its hook_added event.
func (d *Dispatcher) makesFrom(ctx context.Context, hook string) (uint64, error) {
	if d.madeTo == nil {
		d.madeTo = map[string]uint64{}
	}
	for {
		evs, err := d.Store.EventsSince(ctx, d.madeCursor, rulePage)
		if err != nil {
			return 0, err
		}
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			d.madeCursor = e.Seq
			name, _ := e.Data["hook"].(string)
			switch {
			case e.Kind == "hook_added":
				d.madeTo[name] = max(d.madeTo[name], e.Seq)
			case e.Kind == "hook_rule" && e.Data["makes"] != nil:
				d.madeTo[name] = max(d.madeTo[name], metaSeq(e.Data["seq"]))
			}
		}
	}
	return d.madeTo[hook], nil
}

// closedSince reports whether a bead that tracks ev's thread was closed
// after ev: the event was that bead's. A thread whose links cannot be read
// is taken to be one, so that nothing is made on what is not known. decl
// is the yard's declared source of the event's source, nil for a builtin
// one.
func (d *Dispatcher) closedSince(ctx context.Context, ev ruleEvent, decl *source.Declaration) bool {
	for _, url := range []string{ev.threadURL, hooks.ThreadAlias(ev.source, decl, ev.threadURL)} {
		if url == "" {
			continue
		}
		links, err := d.Store.IssueLinks(ctx, url)
		if err != nil {
			d.Log.Warn("hook event: reading the thread's links", "thread", url, "err", err)
			return true
		}
		for _, l := range links {
			if l.Kind != store.LinkTracks || l.Source != ev.source {
				continue
			}
			b, err := d.Store.GetBead(ctx, l.Bead)
			if err != nil {
				d.Log.Warn("hook event: reading the tracking bead", "bead", l.Bead, "err", err)
				return true
			}
			if b.ClosedAt != nil && !ev.at.After(*b.ClosedAt) {
				return true
			}
		}
	}
	return false
}

// make puts ev, about a thread no open bead tracks, to its hook's table of
// what its events make. It returns the bead that tracks the thread after
// all, when one came to between the look for it and the write: the event
// then says to it what it would have. decl is the yard's declared source
// of the event's source, nil for a builtin one.
func (d *Dispatcher) make(ctx context.Context, ev ruleEvent, pass *hookPass, decl *source.Declaration) (tracked bool) {
	if ev.threadURL == "" {
		return false
	}
	h := d.hookOf(ctx, ev.hook, pass)
	line := h.Made(ev.data)
	// What the yard did itself makes nothing, as it says nothing.
	if line == nil || d.own(ctx, ev, pass) {
		return false
	}
	if d.closedSince(ctx, ev, decl) {
		return false
	}
	from, err := d.makesFrom(ctx, h.Name)
	if err != nil {
		d.Log.Warn("hook event: reading what the hook's events made", "hook", h.Name, "err", err)
		return false
	}
	if ev.seq <= from {
		return false
	}
	d.madeTo[h.Name] = ev.seq
	data := map[string]any{"hook": h.Name, "event": ev.typ, "seq": ev.seq, "makes": line.String(), "thread": ev.threadURL}
	depot, why := d.depotFor(ctx, ev, line, decl)
	var bead *store.Bead
	if why == "" {
		data["depot"] = depot
		nb := madeBead(ev, line, depot)
		forUser, fallback := d.hookOriginator(ctx, ev, line, depot)
		nb.For = forUser
		for k, v := range fallback {
			data[k] = v
		}
		bead, err = d.Store.CreateBead(ctx, nb, "hook:"+h.Name)
	}
	id := ""
	switch {
	case why != "":
		data["ignored"] = why
	case errors.Is(err, store.ErrConflict):
		return true
	case err != nil:
		// What the store refuses (a stage the type's flow has not there, a
		// depot that is none) is the result, as a refused outcome is. It
		// is heard too: make_refused is of the class attention, so the
		// yardmaster hears of the bead that was never made.
		data["refused"] = err.Error()
		d.Log.Info("hook event refused", "hook", h.Name, "event", ev.typ, "thread", ev.threadURL, "makes", line.String(), "why", err)
		if err := d.Store.LogDispatch(ctx, "", "make_refused", actor, data); err != nil {
			d.Log.Warn("make_refused event", "err", err)
		}
	default:
		id = bead.ID
		data["bead"], data["stage"], data["action"] = bead.ID, bead.Stage, "made "+bead.ID
		d.Log.Info("hook event", "hook", h.Name, "event", ev.typ, "bead", bead.ID, "makes", line.String())
	}
	if err := d.Store.LogDispatch(ctx, id, "hook_rule", actor, data); err != nil {
		d.Log.Warn("hook_rule event", "err", err)
	}
	return false
}

// hookOriginator is whose subscription the bead line makes of ev builds
// on: the delivery's author when they are a linked user (yardr user
// identity) with their own login and a personal route in the bead's
// stage, else the operator's. The author is a bystander, giving input
// through a flow, unless both checks say they are an interactor who talks
// to the yard; a default never spends another person's subscription.
//
// "Their own login" is declared, not probed: a herdr group whose config
// sets login_for to them. A missing or mispointed env file does not move
// the bead: the declaration wins while the group waits runtime_down, so a
// transient file state never silently rebills interactor work to the
// operator. The route check probes MatchRoute with a bead of theirs as it
// would be made, and counts only a personal route, whose first group
// declares the same login_for; a subsidy route never proves a login.
//
// It returns the bead's For and, when a linked author falls back to the
// operator, what the hook_rule event records of it: the linked author and
// which check failed (no_personal_group, or no_personal_route with the
// matched route and worker where there is one). An unlinked or ambiguous
// author, the operator themself, and anything the store cannot answer bill
// the operator's as every hook bead does, with nothing recorded.
func (d *Dispatcher) hookOriginator(ctx context.Context, ev ruleEvent, line *store.Make, depot string) (string, map[string]any) {
	if ev.actor == "" {
		return "", nil
	}
	users, err := d.Store.Users(ctx)
	if err != nil {
		d.Log.Warn("hook event: reading the users", "err", err)
		return "", nil
	}
	linked := ""
	for _, u := range users {
		if !u.IsIn(ev.source, ev.actor) {
			continue
		}
		// Identity links are not unique across users. If more than one
		// user names this author, there is no safe originator to bill.
		if linked != "" {
			return "", nil
		}
		linked = u.Name
	}
	if linked == "" {
		return "", nil
	}
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		d.Log.Warn("hook event: reading the settings", "err", err)
		return "", nil
	}
	operator := settings[store.OperatorSetting]
	if linked == operator {
		return "", nil
	}
	groups, err := d.Store.Groups(ctx)
	if err != nil {
		d.Log.Warn("hook event: reading the groups", "err", err)
		return "", nil
	}
	byName := make(map[string]store.Group, len(groups))
	personal := false
	for _, g := range groups {
		byName[g.Name] = g
		if g.Runner == "herdr" && g.LoginFor() == linked {
			personal = true
		}
	}
	if !personal {
		return "", map[string]any{"linked_author": linked, "originator_rule": "no_personal_group"}
	}
	routes, err := d.Store.Routes(ctx)
	if err != nil {
		d.Log.Warn("hook event: reading the routes", "err", err)
		return "", nil
	}
	SortRoutes(routes)
	matched := Match(routes, store.Bead{Stage: line.Stage, Type: line.Type, Depot: &depot, Originator: linked}, operator)
	if matched == nil || !store.IsPersonalRoute(*matched, byName, linked, operator) {
		fallback := map[string]any{"linked_author": linked, "originator_rule": "no_personal_route"}
		if matched != nil {
			fallback["route"], fallback["worker"] = matched.ID, matched.Worker()
		}
		return "", fallback
	}
	return linked, nil
}

// madeBead is the bead line makes of ev in depot: the thread's title, the
// event's text and where to read it, a tracks link to the thread, and the
// event marked seen on it, so that it says nothing to the bead it made.
func madeBead(ev ruleEvent, line *store.Make, depot string) store.NewBead {
	str := func(v any) string { s, _ := v.(string); return s }
	thread, _ := ev.data["thread"].(map[string]any)
	body := strings.TrimSpace(str(ev.data["text"]))
	if url := cmp.Or(str(ev.data["url"]), ev.threadURL); url != "" {
		body = strings.TrimSpace(body + "\n\n" + url)
	}
	return store.NewBead{
		Title: cmp.Or(str(thread["title"]), ev.threadURL), Body: body,
		Type: line.Type, Stage: line.Stage, Depot: depot,
		Metadata:  map[string]any{metaHookSeq: ev.seq},
		Links:     []store.NewLink{{Kind: store.LinkTracks, Source: ev.source, URL: ev.threadURL, Thread: str(thread["id"])}},
		Untracked: true,
	}
}
