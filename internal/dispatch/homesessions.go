package dispatch

import (
	"maps"
	"slices"

	"yardr.dev/yardr/internal/store"
)

// HomeSession is one herdr session the daemon may have to close itself: a
// crew member's line or conversation session, a start underway, or a running
// bead session. The dispatcher keeps the last pass's, so that when its home
// is removed under it — the store behind the server's socket unreachable,
// no listing to be made — it still closes what it opened. See HomeSessions.
type HomeSession struct {
	Kind string // crew or bead
	// Name is the crew member, or the bead's handle.
	Name string
	// For is the user of a conversation session.
	For string
	// Stage and Group are the bead's; Session its session's id.
	Stage   string
	Group   string
	Session string
	// Runner is herdr: the snapshot keeps no other runner's.
	Runner string
	// Agent is herdr's name for it; "" when the handle names none.
	Agent  string
	Handle map[string]any
}

// String is the session as the close names it, as yardr down names an agent.
func (s HomeSession) String() string {
	if s.Kind == "crew" {
		name := "crew member " + s.Name
		if s.For != "" {
			name += " for " + s.For
		}
		return name
	}
	return "bead " + s.Name + " (stage " + s.Stage + ", group " + s.Group + ")"
}

// key distinguishes a crew member's line session from each conversation;
// bead session IDs are unique already.
func (s HomeSession) key() string {
	if s.Kind == "crew" {
		return crewHomeKey(s.Name, s.For)
	}
	return beadHomeKey(s.Session)
}

func crewHomeKey(name, user string) string { return "crew\x00" + name + "\x00" + user }
func beadHomeKey(session string) string    { return "bead\x00" + session }

// homeAgent is herdr's name on the handle, "" when it names none.
func homeAgent(handle map[string]any) string {
	agent, _ := handle["agent"].(string)
	return agent
}

// beadHome is the snapshot's entry for the listed session s, started by the
// runner named.
func beadHome(s store.Session, runner string) HomeSession {
	return HomeSession{Kind: "bead", Name: s.Bead, Stage: s.Stage, Group: s.Group,
		Session: s.ID, Runner: runner, Agent: homeAgent(s.Handle), Handle: maps.Clone(s.Handle)}
}

// startedBeadHome is the entry for a session the pass started itself.
func startedBeadHome(bead, stage, group, session, runner string, handle map[string]any) HomeSession {
	return HomeSession{Kind: "bead", Name: bead, Stage: stage, Group: group,
		Session: session, Runner: runner, Agent: homeAgent(handle), Handle: maps.Clone(handle)}
}

// startedCrewHome is the entry for a crew session the pass started itself.
func startedCrewHome(name, user, runner string, handle map[string]any) HomeSession {
	return HomeSession{Kind: "crew", Name: name, For: user,
		Runner: runner, Agent: homeAgent(handle), Handle: maps.Clone(handle)}
}

// noteHomeCrew records r's herdr sessions: the line session and each
// conversation one, live or with a start underway.
func (d *Dispatcher) noteHomeCrew(r store.CrewMember) {
	if r.Runner != "herdr" {
		return
	}
	note := func(user string, s store.CrewSession) {
		entry := HomeSession{Kind: "crew", Name: r.Name, For: user, Runner: r.Runner}
		switch handle, starting := s.Handle, s.Starting; {
		case s.Live():
			entry.Agent, entry.Handle = homeAgent(handle), maps.Clone(handle)
			d.noteHome(entry)
		case starting["tab"] != nil:
			entry.Agent, entry.Handle = homeAgent(starting), maps.Clone(starting)
			d.noteHome(entry)
		}
	}
	note("", r.CrewSession)
	for user, s := range r.Conversations {
		note(user, s)
	}
}

// beginHomePass starts the snapshot the pass collects: the sessions its
// reads see, and the ones it starts.
func (d *Dispatcher) beginHomePass() {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	d.pass = map[string]HomeSession{}
}

// endHomePass publishes the pass's snapshot: the pass ran through, so what
// it saw is current. A pass that returned early keeps the last one.
func (d *Dispatcher) endHomePass() {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	if d.pass != nil {
		d.home = d.pass
		d.pass = nil
	}
}

// noteHome records a session the pass's reads saw.
func (d *Dispatcher) noteHome(s HomeSession) {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	if d.pass != nil {
		d.pass[s.key()] = s
	}
}

// noteHomeStarted records a session the pass started: in the pass's
// snapshot, and in the last one at once, so that a pass which never ends
// (its store gone halfway through) still leaves it behind.
func (d *Dispatcher) noteHomeStarted(s HomeSession) {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	if d.home == nil {
		d.home = map[string]HomeSession{}
	}
	d.home[s.key()] = s
	if d.pass != nil {
		d.pass[s.key()] = s
	}
}

// dropHome forgets a session the pass stopped or found gone: its pane is
// closed, or its handle is kept where the runtime closes leftovers.
func (d *Dispatcher) dropHome(key string) {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	delete(d.home, key)
	if d.pass != nil {
		delete(d.pass, key)
	}
}

// HomeSessions are the herdr sessions the daemon is responsible for as of
// the last pass that ran through, with the handles it saw: each crew
// member's line and conversation sessions, starts underway, and each
// running bead session, and each session started since. The daemon closes
// from these when its home is gone under it, with no store call: the
// socket's path is gone with the home, so no listing can be made. A session
// already gone is a Stop that fails, as any other.
func (d *Dispatcher) HomeSessions() []HomeSession {
	d.homeMu.Lock()
	defer d.homeMu.Unlock()
	keys := make([]string, 0, len(d.home))
	for k := range d.home {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]HomeSession, 0, len(keys))
	for _, k := range keys {
		out = append(out, d.home[k])
	}
	return out
}
