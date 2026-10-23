package dispatch

import (
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A lead goes on while the session its first ask was made for sits at the
// login screen, or any session of a crew member its pick switched (the login
// starts them again whatever their screens read), and ends when none does.
// The seat is compared whole, crew member and user: another session of the
// same crew member at the screen is not the one the card was made for.
func TestLeadStillAtLogin(t *testing.T) {
	t.Parallel()
	atScreen := store.CrewSession{State: "running", Handle: map[string]any{HarnessLoginKey: "Select login method:"}}
	started := store.CrewSession{State: "running", Handle: map[string]any{"agent": "yardmaster-2"}}
	// A session that stopped keeps what its handle held, and sits nowhere.
	stopped := store.CrewSession{State: "stopped", Handle: atScreen.Handle}
	crew := func(name string, line store.CrewSession, conversations map[string]store.CrewSession) store.CrewMember {
		return store.CrewMember{Name: name, CrewSession: line, Conversations: conversations}
	}
	line := loginSeat{member: "yardmaster"}
	conversation := loginSeat{member: "yardmaster", user: "ada"}
	for _, c := range []struct {
		name     string
		seat     loginSeat
		switched []string
		rs       []store.CrewMember
		want     bool
	}{
		{"its seat sits", line, nil,
			[]store.CrewMember{crew("yardmaster", atScreen, nil)}, true},
		{"its seat started again", line, nil,
			[]store.CrewMember{crew("yardmaster", started, nil)}, false},
		{"its seat stopped", line, nil,
			[]store.CrewMember{crew("yardmaster", stopped, nil)}, false},
		{"its crew member is gone", line, nil,
			[]store.CrewMember{crew("scout", atScreen, nil)}, false},
		{"its conversation sits", conversation, nil,
			[]store.CrewMember{crew("yardmaster", started, map[string]store.CrewSession{"ada": atScreen})}, true},
		{"its conversation started again, its line session sits", conversation, nil,
			[]store.CrewMember{crew("yardmaster", atScreen, map[string]store.CrewSession{"ada": started})}, false},
		{"its line session started again, a conversation sits", line, nil,
			[]store.CrewMember{crew("yardmaster", started, map[string]store.CrewSession{"ada": atScreen})}, false},
		{"another conversation sits", conversation, nil,
			[]store.CrewMember{crew("yardmaster", started, map[string]store.CrewSession{"ada": started, "bob": atScreen})}, false},
		{"a member its pick switched sits", line, []string{"scout"},
			[]store.CrewMember{crew("yardmaster", started, nil), crew("scout", atScreen, nil)}, true},
		{"a conversation of a member its pick switched sits", line, []string{"scout"},
			[]store.CrewMember{crew("yardmaster", started, nil), crew("scout", started, map[string]store.CrewSession{"ada": atScreen})}, true},
		{"a member that is not switched sits", line, []string{"scout"},
			[]store.CrewMember{crew("yardmaster", started, nil), crew("tug", atScreen, nil)}, false},
		{"its seat and the members its pick switched have left", line, []string{"scout"},
			[]store.CrewMember{crew("yardmaster", started, nil), crew("scout", stopped, nil)}, false},
		{"no crew", line, []string{"scout"}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l := &harnessLead{seat: c.seat, switched: c.switched}
			if got := l.stillAtLogin(c.rs); got != c.want {
				t.Errorf("stillAtLogin = %v, want %v", got, c.want)
			}
		})
	}
}
