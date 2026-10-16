package store

import "testing"

// A session's runner is the one it recorded, and, for a session from before
// sessions recorded one, the one its handle tells; a manual group's claim is
// the session whose runner is manual.
func TestSessionRunnerName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		s    Session
		want string
	}{
		{"manual runner", Session{Runner: "manual"}, "manual"},
		{"old manual claim", Session{Handle: map[string]any{"manual": true}}, "manual"},
		{"herdr", Session{Runner: "herdr", Handle: map[string]any{"agent": "a"}}, "herdr"},
		{"exec", Session{Runner: "exec", Handle: map[string]any{"pid": 7}}, "exec"},
		{"old herdr session", Session{Handle: map[string]any{"agent": "a"}}, "herdr"},
		{"old exec session", Session{Handle: map[string]any{"pid": 7}}, "exec"},
		{"old peer session", Session{Handle: map[string]any{"peer": "north"}}, "peer"},
		{"the recorded runner over the handle", Session{Runner: "peer", Handle: map[string]any{"agent": "a", "manual": true}}, "peer"},
		{"starting, no handle yet", Session{}, ""},
	} {
		if got := c.s.RunnerName(); got != c.want {
			t.Errorf("%s: RunnerName() = %q, want %q", c.name, got, c.want)
		}
		if got := c.s.Manual(); got != (c.want == "manual") {
			t.Errorf("%s: Manual() = %v", c.name, got)
		}
	}
}
