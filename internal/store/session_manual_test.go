package store

import "testing"

// A manual group's claim is told by the runner its session recorded, and,
// for a session from before sessions recorded one, by its handle.
func TestSessionManual(t *testing.T) {
	for _, c := range []struct {
		name string
		s    Session
		want bool
	}{
		{"manual runner", Session{Runner: "manual"}, true},
		{"old manual claim", Session{Handle: map[string]any{"manual": true}}, true},
		{"herdr", Session{Runner: "herdr", Handle: map[string]any{"agent": "a"}}, false},
		{"exec", Session{Runner: "exec", Handle: map[string]any{"pid": 7}}, false},
		{"old herdr session", Session{Handle: map[string]any{"agent": "a"}}, false},
		{"starting, no handle yet", Session{}, false},
	} {
		if got := c.s.Manual(); got != c.want {
			t.Errorf("%s: Manual() = %v, want %v", c.name, got, c.want)
		}
	}
}
