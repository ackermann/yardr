package dispatch

import (
	"context"
	"maps"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A session whose agent never got going: it was given its first prompt, was
// idle every time the dispatcher looked since, and has left nothing behind
// (no note, no commit, no change in its worktree). That is what an agent
// looks like whose first request was refused: a model it may not use, no
// login, no credit. Its TUI starts, the error shows in its pane, and it
// sits idle. Waiting for the idle limit (30m) would hold the bead, the
// group's slot and every bead after it meanwhile, so it is reported as
// session_stalled as soon as it has been idle for the group's `stall`
// (default 2m; "off" or "0" turns it off), with the pane's last lines.
//
// Until the stalled session is ended (`yardr bead release`), gets going
// after all, or the group's model changes, the group starts no further
// session with the same model: each would stall the same way.
const DefaultStallLimit = 2 * time.Minute

// Handle keys: when session_stalled was raised for the session, and the
// model its agent was started with. They are kept on the handle, not in
// memory, so a daemon that replaces itself still holds the group back.
const (
	StalledAt    = "stalled_at"
	StalledModel = "stalled_model"
)

// stallTail is how many lines of the pane a session_stalled event carries.
const stallTail = 20

// Tailer is implemented by runners that can show what a session's agent
// last printed (a herdr pane). A stalled session's event carries it, so its
// reader sees the agent's own error.
type Tailer interface {
	Tail(ctx context.Context, handle map[string]any, lines int) (string, error)
}

// Worker is implemented by runners that can tell whether a session has
// changed anything where it works (a commit, an edited file) since it
// started.
type Worker interface {
	Worked(ctx context.Context, handle map[string]any) (bool, error)
}

// prompted notes that s's agent has just been given its first prompt; from
// here the dispatcher watches whether it ever gets going. Only this process
// saw the prompt go out: a daemon that replaced itself since does not judge
// the sessions it inherited, which the idle limit still covers.
func (d *Dispatcher) prompted(r Runner, id string) {
	if _, ok := r.(Statuser); ok {
		d.fresh[id] = d.now()
	}
}

// stall reports s as stalled when its agent has been idle since its first
// prompt for the group's stall limit and has done nothing. It is called on
// every pass that finds s idle, and judges a session once. s.Handle is
// updated with the mark it stores.
func (d *Dispatcher) stall(ctx context.Context, r Runner, s *store.Session, g store.Group) {
	since, ok := d.fresh[s.ID]
	// A prompt still on the handle has not arrived yet.
	if !ok || s.Handle[PendingPrompt] != nil {
		return
	}
	lim, on := limit(g.Config, "stall", DefaultStallLimit, true)
	if !on {
		delete(d.fresh, s.ID)
		return
	}
	idleFor := d.now().Sub(since)
	if idleFor < lim {
		return
	}
	worked, err := d.worked(ctx, r, *s)
	if err != nil {
		d.Log.Warn("cannot tell whether an idle session did anything; asking again", "session", s.ID, "err", err)
		return
	}
	delete(d.fresh, s.ID)
	if worked {
		return
	}

	model := ""
	if b, err := d.Store.GetBead(ctx, s.Bead); err == nil {
		model = modelFor(g, *b)
	}
	tail := ""
	if t, ok := r.(Tailer); ok {
		if tail, err = t.Tail(ctx, s.Handle, stallTail); err != nil {
			tail = "(pane not readable: " + err.Error() + ")"
		}
	}
	d.Log.Warn("session stalled: idle since its first prompt, nothing done", "session", s.ID, "bead", s.Bead,
		"group", s.Group, "model", model)
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage, "model": model,
		"idle": round(idleFor).String(), "limit": lim.String(), "tail": tail,
		"hint": "the agent did nothing with its first prompt; its pane's last lines are in tail. " +
			"The group starts no further session with this model until this one is ended " +
			"(`yardr bead release " + s.Bead + "`) or the group's model changes"}
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_stalled", actor, data); err != nil {
		d.Log.Warn("session_stalled event", "session", s.ID, "err", err)
	}
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	h[StalledAt], h[StalledModel] = d.now().UTC().Format(time.RFC3339), model
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("mark session stalled", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

// worked reports whether s has left anything behind: an event on its bead
// by its agent (a note, a move) since it started, or, where the runner can
// tell, a change where it works.
func (d *Dispatcher) worked(ctx context.Context, r Runner, s store.Session) (bool, error) {
	evs, err := d.Store.Events(ctx, s.Bead, returnsPage)
	if err != nil {
		return false, err
	}
	agent, _ := s.Handle["agent"].(string)
	for _, e := range evs {
		if e.At.Before(s.StartedAt) || e.Actor == actor {
			continue
		}
		// A runner that names its agent tells its events from a human's or
		// the mayor's on the same bead; otherwise anyone's count.
		if agent == "" || e.Actor == agent {
			return true, nil
		}
	}
	if w, ok := r.(Worker); ok {
		return w.Worked(ctx, s.Handle)
	}
	return false, nil
}

// holdBack notes that g starts no further session with the model s stalled
// on, for this pass (see paused).
func (d *Dispatcher) holdBack(s store.Session) {
	if s.Handle[StalledAt] == nil {
		return
	}
	// A stalled agent someone got going again (it is working, or asks its
	// human something) no longer says anything about its model.
	if st := d.status[s.ID]; st != "idle" && st != "done" {
		return
	}
	model, _ := s.Handle[StalledModel].(string)
	if d.paused[s.Group] == nil {
		d.paused[s.Group] = map[string]string{}
	}
	d.paused[s.Group][model] = s.ID
}

// modelFor is the model a session of g for b starts with: the bead's
// metadata.model, else the group's; "" is the agent's own default.
func modelFor(g store.Group, b store.Bead) string {
	if m, _ := b.Metadata["model"].(string); m != "" {
		return m
	}
	m, _ := g.Config["model"].(string)
	return m
}
