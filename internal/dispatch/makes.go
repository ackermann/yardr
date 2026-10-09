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

	"yardr.dev/yardr/internal/store"
)

// An event about a thread no open bead tracks can make the bead that tracks
// it, so that an issue or a pull request from outside enters the yard and
// its later events find a bead to say something to (github.go). What makes
// one is data, as what an event says is: the hook's other table
// (store.Hook's Makes, from a pack's sources.<source>.makes or yardr hook
// add --makes), whose first line the event matches names the bead's type,
// its stage and, optionally, its depot.
//
// Without a depot on the line the bead is made in the one depot whose
// repository the event is in: the repository its origin remote names,
// which is read once for a depot and kept. An event no depot is found for
// makes nothing, and is logged as ignored with why.
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
// (metaGitHubSeq), and every event a line matched is logged as a github_rule
// event with the line under makes, on the bead when one was made. A
// restarted daemon, which reads the log from the start, finds in those
// events how far each hook's table was put to its events, and makes nothing
// of an older one. Nor of one from before the hook was last saved: a table
// says nothing of what came before it.

// DepotRepoQuery is what DepotRepo is asked: the depot whose repository at
// the source is wanted.
type DepotRepoQuery struct {
	Source string
	Depot  store.Depot
}

// gitDepotRepo reads the depot's repository at GitHub from its origin
// remote, as owner/name: "" for a depot that has no checkout or whose
// origin is elsewhere.
func gitDepotRepo(ctx context.Context, q DepotRepoQuery) (string, error) {
	if q.Source != "github" || q.Depot.IsDir() || q.Depot.Path == "" {
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
	repo, _ := strings.CutPrefix(store.RemoteForm(string(out)), "github.com/")
	if strings.Count(repo, "/") != 1 {
		return "", nil
	}
	return repo, nil
}

// depotFor is the depot a bead is made in for ev by line: the line's, else
// the one whose repository ev is in. why says what stood in the way of
// finding one.
func (d *Dispatcher) depotFor(ctx context.Context, ev ghEvent, line *store.Make) (depot, why string) {
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
	if d.depotRepos == nil {
		d.depotRepos = map[string]string{}
	}
	var in, unread []string
	for _, dp := range depots {
		// A depot moved to another path is another checkout.
		key := ev.source + "\x00" + dp.Name + "\x00" + dp.Path
		repo, kept := d.depotRepos[key]
		if !kept {
			if repo, err = ask(ctx, DepotRepoQuery{Source: ev.source, Depot: dp}); err != nil {
				// Asked again at the next event: a remote can be added.
				unread = append(unread, fmt.Sprintf("%s (%v)", dp.Name, err))
				continue
			}
			d.depotRepos[key] = repo
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
		evs, err := d.Store.EventsSince(ctx, d.madeCursor, githubPage)
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
			case e.Kind == "github_rule" && e.Data["makes"] != nil:
				d.madeTo[name] = max(d.madeTo[name], metaSeq(e.Data["seq"]))
			}
		}
	}
	return d.madeTo[hook], nil
}

// closedSince reports whether a bead that tracks ev's thread was closed
// after ev: the event was that bead's. A thread whose links cannot be read
// is taken to be one, so that nothing is made on what is not known.
func (d *Dispatcher) closedSince(ctx context.Context, ev ghEvent) bool {
	for _, url := range []string{ev.threadURL, githubThreadAlias(ev.threadURL)} {
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
// then says to it what it would have.
func (d *Dispatcher) make(ctx context.Context, ev ghEvent, pass *hookPass) (tracked bool) {
	if ev.threadURL == "" {
		return false
	}
	h := d.hookOf(ctx, ev.hook, pass)
	line := h.Made(ev.data)
	// What the yard did itself makes nothing, as it says nothing.
	if line == nil || d.own(ctx, ev, pass) {
		return false
	}
	if d.closedSince(ctx, ev) {
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
	depot, why := d.depotFor(ctx, ev, line)
	var bead *store.Bead
	if why == "" {
		data["depot"] = depot
		bead, err = d.Store.CreateBead(ctx, madeBead(ev, line, depot), "hook:"+h.Name)
	}
	id := ""
	switch {
	case why != "":
		data["ignored"] = why
	case errors.Is(err, store.ErrConflict):
		return true
	case err != nil:
		// What the store refuses (a stage the type's flow has not there, a
		// depot that is none) is the result, as a refused outcome is.
		data["refused"] = err.Error()
		d.Log.Info("hook event refused", "hook", h.Name, "event", ev.typ, "thread", ev.threadURL, "makes", line.String(), "why", err)
	default:
		id = bead.ID
		data["bead"], data["stage"], data["action"] = bead.ID, bead.Stage, "made "+bead.ID
		d.Log.Info("hook event", "hook", h.Name, "event", ev.typ, "bead", bead.ID, "makes", line.String())
	}
	if err := d.Store.LogDispatch(ctx, id, "github_rule", actor, data); err != nil {
		d.Log.Warn("github_rule event", "err", err)
	}
	return false
}

// madeBead is the bead line makes of ev in depot: the thread's title, the
// event's text and where to read it, a tracks link to the thread, and the
// event marked seen on it, so that it says nothing to the bead it made.
func madeBead(ev ghEvent, line *store.Make, depot string) store.NewBead {
	str := func(v any) string { s, _ := v.(string); return s }
	thread, _ := ev.data["thread"].(map[string]any)
	body := strings.TrimSpace(str(ev.data["text"]))
	if url := cmp.Or(str(ev.data["url"]), ev.threadURL); url != "" {
		body = strings.TrimSpace(body + "\n\n" + url)
	}
	return store.NewBead{
		Title: cmp.Or(str(thread["title"]), ev.threadURL), Body: body,
		Type: line.Type, Stage: line.Stage, Depot: depot,
		Metadata:  map[string]any{metaGitHubSeq: ev.seq},
		Links:     []store.NewLink{{Kind: store.LinkTracks, Source: ev.source, URL: ev.threadURL, Thread: str(thread["id"])}},
		Untracked: true,
	}
}
