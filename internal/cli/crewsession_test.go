package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// sessionYard is a yard whose operator jo serves hal with a conversation:
// the user hal, for=hal with hal's env file, and hal's slice row, as the
// daemon's first pass leaves it.
func sessionYard(t *testing.T) store.API {
	t.Helper()
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "hal")
	mustAs(t, st, "jo", "crew", "set", "yardmaster", "for=hal,jo", "env_for_hal=/tmp/hal.env")
	if err := st.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "y-hal"}); err != nil {
		t.Fatal(err)
	}
	return st
}

// crew set takes the serving list and the per-user env, and refuses users
// that do not exist; crew show names the conversation's session.
func TestCrewSetServingKeys(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	mustAs(t, st, "jo", "user", "add", "hal")
	out := mustAs(t, st, "jo", "crew", "set", "yardmaster", "for=hal", "env_for_hal=/tmp/hal.env")
	if !strings.Contains(out, "for: (not set) -> hal") || !strings.Contains(out, "env_for_hal: (not set) -> /tmp/hal.env") {
		t.Errorf("crew set printed:\n%s", out)
	}
	refusedAs(t, st, "jo", "crew member yardmaster: for ghost: user ghost: not found",
		"crew", "set", "yardmaster", "for=ghost")
	refusedAs(t, st, "jo", "crew member yardmaster: env_for_ghost: user ghost: not found",
		"crew", "set", "yardmaster", "env_for_ghost=/tmp/x.env")
	if out := mustAs(t, st, "jo", "crew", "show", "yardmaster"); !strings.Contains(out, "for hal: stopped") {
		t.Errorf("crew show lacks the conversation's session:\n%s", out)
	}
}

// inbox --for shows a conversation's slice, and a session reads its own
// from $YARDR_CREW_FOR; --ack <seq> there leaves the line's unread.
func TestInboxForShowsSlice(t *testing.T) {
	t.Parallel()
	st := sessionYard(t)
	mustAs(t, st, "hal", "mail", "yardmaster", "hal asks about deploys")
	mustAs(t, st, "jo", "mail", "yardmaster", "jo notes the freeze")
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "jo notes the freeze") || strings.Contains(out, "hal asks") {
		t.Errorf("line inbox:\n%s", out)
	}
	out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--for", "hal")
	if !strings.Contains(out, "hal asks about deploys") || strings.Contains(out, "jo notes") {
		t.Errorf("hal slice:\n%s", out)
	}
	refusedAs(t, st, "jo", "has no conversation session for ada", "inbox", "--crew", "yardmaster", "--for", "ada")
	env := yardEnv(t, st)
	env["YARDR_CREW_FOR"] = "hal"
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "hal asks about deploys") {
		t.Errorf("inbox with YARDR_CREW_FOR=hal:\n%s", out)
	}
	halBox, err := st.InboxFor(context.Background(), "yardmaster", "hal", 1)
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--for", "hal", "--ack", fmt.Sprint(halBox.Last))
	env["YARDR_CREW_FOR"] = ""
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "jo notes the freeze") {
		t.Errorf("line inbox after hal's ack:\n%s", out)
	}
}

// prime --crew --for primes the conversation: its session named, its
// slice's mail, and its own handoff.
func TestPrimeCrewForShowsSlice(t *testing.T) {
	t.Parallel()
	st := sessionYard(t)
	mustAs(t, st, "hal", "mail", "yardmaster", "hal asks about deploys")
	mustAs(t, st, "jo", "mail", "yardmaster", "jo notes the freeze")
	out := mustAs(t, st, "jo", "prime", "--crew", "yardmaster", "--for", "hal")
	for _, want := range []string{"talks with `hal`", "hal asks about deploys"} {
		if !strings.Contains(out, want) {
			t.Errorf("hal prime lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "jo notes the freeze") {
		t.Errorf("hal prime holds the line's mail:\n%s", out)
	}
}

// crew show --for shows that slice's count; without it, each session's
// line carries its own.
func TestCrewShowSession(t *testing.T) {
	t.Parallel()
	st := sessionYard(t)
	mustAs(t, st, "hal", "mail", "yardmaster", "from hal")
	if out := mustAs(t, st, "jo", "crew", "show", "yardmaster", "--for", "hal"); !strings.Contains(out, "inbox 1 unread for hal") {
		t.Errorf("show --for hal:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "crew", "show", "yardmaster"); !strings.Contains(out, "for hal: running") || !strings.Contains(out, "inbox 1 unread") {
		t.Errorf("show:\n%s", out)
	}
}

// doctor reads a crew member as running where any session runs, and names
// a failing conversation: an exclusive yardmaster with its conversation
// up is no fault, and one whose conversation gave up is told of by user.
func TestDoctorSeesConversationSessions(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "hal")
	mustAs(t, st, "jo", "crew", "set", "yardmaster", "for=hal", "env_for_hal=/tmp/hal.env")
	if err := st.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "y-hal"}); err != nil {
		t.Fatal(err)
	}
	// A run of its own each time, on the yard's environment.
	doctor := func() string {
		a := appOn(st)
		a.actor = "jo"
		out, _ := run(t, a.rootCmd(), "doctor")
		return out
	}
	out := doctor()
	if !strings.Contains(out, "ok     yardmaster") || !strings.Contains(out, "yardmaster is running (herdr agent y-hal)") {
		t.Errorf("doctor with the conversation up:\n%s", out)
	}
	for range 5 {
		if err := st.CrewStartFailedFor(ctx, "yardmaster", "hal", "no login", 0); err != nil {
			t.Fatal(err)
		}
	}
	out = doctor()
	if !strings.Contains(out, "yardmaster for hal is not running") || !strings.Contains(out, "crew start yardmaster") {
		t.Errorf("doctor with the conversation failed:\n%s", out)
	}
}

// crew restart --for restarts the conversation; the line's restart is
// untouched.
func TestCrewRestartFor(t *testing.T) {
	t.Parallel()
	st := sessionYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "crew", "restart", "yardmaster", "--for", "hal")
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Conversations["hal"].RestartRequested == "" {
		t.Errorf("hal restart not requested: %+v", r.Conversations["hal"])
	}
	if r.RestartRequested != "" {
		t.Errorf("line restart requested: %q", r.RestartRequested)
	}
	refusedAs(t, st, "jo", "has no conversation session for ada", "crew", "restart", "yardmaster", "--for", "ada")
}
