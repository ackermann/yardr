package dispatch

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"yardr.dev/yardr/internal/store"
)

// Handle keys: the model a session's agent was asked to run (the bead's
// metadata.model, else the group's config.model; absent when neither names
// one) and the one the agent itself reports once it has answered its first
// prompt. Kept on the handle, so `yardr session list --json` shows both and
// a daemon that replaces itself does not ask or tell again.
const (
	ModelAsked    = "model_asked"
	ModelReported = "model_reported"
)

// ModelWindow is how long after a session's start its agent is asked for
// its model, and for the id of its session. An agent that has not answered
// a prompt by then (one that stalled, or waits on a human) is not asked on
// every pass for ever. It is also how late after the start a session file
// found by its worktree may have started (the runner's match).
const ModelWindow = 15 * time.Minute

// Modeler is implemented by runners whose agents report the model they run
// ("" while not known yet, or for a kind that has no way to tell).
// startedAt is when the session started, by which a session found by its
// worktree, rather than by an id, is matched to its file.
type Modeler interface {
	Model(ctx context.Context, handle map[string]any, startedAt time.Time) (string, error)
}

// AgentSession is a handle key: the id of the session the session's agent
// keeps in its own session file, which is how the file of what the agent
// used is found once the session has ended. Kept on the handle, since
// herdr names no agent whose tab is closed. The store names it too, since the
// write that ends a crew session copies it off the handle it clears.
const AgentSession = store.HandleAgentSession

// SessionIDer is implemented by runners that tell the id of the session an
// agent keeps in its own session file ("" while it names none yet, or for a
// kind the runner can read no id for). startedAt is when the session
// started, by which a session found by its worktree is matched to its file.
type SessionIDer interface {
	SessionID(ctx context.Context, handle map[string]any, startedAt time.Time) (string, error)
}

// askAgentSession is the id r names for the agent of handle, "" when the handle
// has one already (nothing to ask), when r names none yet or can name none, and
// when it cannot be reached: the caller asks again later, if it does at all.
func askAgentSession(ctx context.Context, r Runner, handle map[string]any, startedAt time.Time) string {
	ider, ok := r.(SessionIDer)
	if !ok || handle[AgentSession] != nil {
		return ""
	}
	id, err := ider.SessionID(ctx, handle, startedAt)
	if err != nil {
		return ""
	}
	return id
}

// agentSession records, once per session, the id its agent keeps its session
// under, as soon as the runner has one. The model check records the model
// and nothing else, and only once a model was read; an agent that ended
// before it answered, or whose kind names no model, would have no id left to
// find its count by. So this does not wait for a model line, and does not
// depend on what the model check finds. It asks for as long after the start
// as the model check does (ModelWindow), so a kind the runner reads no id
// for is not asked on every pass for ever, and teardown asks once more
// (see lastAgentSession). s.Handle is updated with what it stores.
func (d *Dispatcher) agentSession(ctx context.Context, r Runner, s *store.Session) {
	if d.now().Sub(s.StartedAt) > ModelWindow {
		return
	}
	id := askAgentSession(ctx, r, s.Handle, s.StartedAt)
	if id == "" {
		return // none yet, or unreachable: asked again next pass
	}
	h := withHandle(s.Handle, AgentSession, id)
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("record agent session", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

// model records, once per session, the model s's agent reports next to the
// one it was asked for, and raises session_model_mismatch when they are not
// the same model: an agent that does not know the name it was given may
// run another without saying so. s.Handle is updated with what it stores.
func (d *Dispatcher) model(ctx context.Context, r Runner, s *store.Session, g store.Group) {
	m, ok := r.(Modeler)
	if !ok || s.Handle[ModelReported] != nil || s.Handle[store.PendingPrompt] != nil ||
		d.now().Sub(s.StartedAt) > ModelWindow {
		return
	}
	reported, err := m.Model(ctx, s.Handle, s.StartedAt)
	if err != nil || reported == "" {
		return // not answered yet, or unreachable: asked again next pass
	}
	asked := ""
	if b, err := d.Store.GetBead(ctx, s.Bead); err == nil {
		asked = modelFor(g, *b)
	}
	h := maps.Clone(s.Handle)
	h[ModelReported] = reported
	if asked != "" {
		h[ModelAsked] = asked
	}
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("record session model", "session", s.ID, "err", err)
		return // the event waits for the mark that keeps it to one
	}
	s.Handle = h
	if asked == "" || SameModel(asked, reported) {
		return
	}
	d.Log.Warn("session runs another model than asked", "session", s.ID, "bead", s.Bead, "asked", asked, "reported", reported)
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_model_mismatch", actor, map[string]any{
		"session": s.ID, "group": s.Group, "stage": s.Stage, "asked": asked, "reported": reported}); err != nil {
		d.Log.Warn("session_model_mismatch event", "session", s.ID, "err", err)
	}
}

// DefaultContextWindow is the context window a model is read as having
// when its asked name carries no window: 200k tokens.
const DefaultContextWindow = 200_000

// ContextWindow is the context window model asks for, in tokens: the
// bracketed suffix of its asked name (opus[1m]: a million), else the
// default. The suffix is what modelWords holds no part of the name.
func ContextWindow(model string) int {
	i := strings.IndexByte(model, '[')
	if i < 0 {
		return DefaultContextWindow
	}
	s := model[i+1:]
	if j := strings.IndexByte(s, ']'); j >= 0 {
		s = s[:j]
	}
	mult := 1
	if s != "" {
		switch s[len(s)-1] {
		case 'k', 'K':
			mult, s = 1000, s[:len(s)-1]
		case 'm', 'M':
			mult, s = 1000000, s[:len(s)-1]
		}
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n * mult
	}
	return DefaultContextWindow
}

// SameModel reports whether two names can mean one model. The asked name
// is an id or an alias (claude-opus-5-5, opus, opus[1m]) and the reported
// one an id or a display name (claude-opus-5-5-20260101, Opus 5.5): they
// match when every word and number of one is among the other's, whatever
// the case and punctuation. So opus matches any Opus, and claude-opus-5-5
// does not match claude-fable-5-1.
func SameModel(a, b string) bool {
	x, y := modelWords(a), modelWords(b)
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	return within(x, y) || within(y, x)
}

// within reports whether every word of x is in y, as often as in x.
func within(x, y []string) bool {
	rest := slices.Clone(y)
	for _, w := range x {
		i := slices.Index(rest, w)
		if i < 0 {
			return false
		}
		rest = slices.Delete(rest, i, i+1)
	}
	return true
}

// modelWords splits a model name into its lower-case words and numbers; a
// bracketed suffix (the context window in opus[1m]) is no part of the name.
func modelWords(name string) []string {
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	for _, c := range strings.ToLower(name) {
		switch {
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			flush()
		case len(cur) > 0 && unicode.IsDigit(c) != unicode.IsDigit(cur[len(cur)-1]):
			flush() // gpt5 reads as gpt 5
			cur = append(cur, c)
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return words
}
