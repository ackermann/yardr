package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// serveCrew serves users with the env files named, as crew set would.
func serveCrew(t *testing.T, s store.API, for_ string, envFor map[string]string) {
	t.Helper()
	ctx := context.Background()
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	def := *r
	if def.Config == nil {
		def.Config = map[string]any{}
	}
	if for_ == "" {
		delete(def.Config, "for")
	} else {
		def.Config["for"] = for_
	}
	for k := range def.Config {
		if store.EnvForUser(k) != "" {
			delete(def.Config, k)
		}
	}
	for user, path := range envFor {
		def.Config["env_for_"+user] = path
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, def, settings[store.OperatorSetting]); err != nil {
		t.Fatal(err)
	}
}

// The daemon keeps the line session and one conversation session per
// served user with its env set: two sessions started, on two agents.
func TestMemberStartsConversationSessions(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	serveCrew(t, s, "", map[string]string{"hal": "/tmp/hal.env"})
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if f.n != 2 {
		t.Fatalf("started %d sessions, want 2 (line and hal)", f.n)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Live() {
		t.Errorf("line session state = %q, want running", r.State)
	}
	cs := r.Conversations["hal"]
	if !cs.Live() {
		t.Errorf("hal session state = %q, want running", cs.State)
	}
	if r.Handle["agent"] == cs.Handle["agent"] {
		t.Errorf("line and hal share agent %v, want two agents", r.Handle["agent"])
	}
}

// A for= that leaves the operator unnamed runs no line session: only the
// conversation starts. One that names them runs both.
func TestMemberExclusiveRunsNoLineSession(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, for_ string) (store.API, *crewRunner) {
		s, f, d := crewSetup(t, store.CrewMember{})
		if _, err := s.AddUser(ctx, "hal", "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "me"); err != nil {
			t.Fatal(err)
		}
		serveCrew(t, s, for_, map[string]string{"hal": "/tmp/hal.env"})
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx))
		return s, f
	}
	s, f := setup(t, "hal")
	if f.n != 1 {
		t.Fatalf("exclusive: started %d sessions, want 1 (hal alone)", f.n)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Live() || len(r.Handle) != 0 {
		t.Errorf("exclusive: line session runs (%q), want none", r.State)
	}
	if !r.Conversations["hal"].Live() {
		t.Errorf("exclusive: hal session state = %q, want running", r.Conversations["hal"].State)
	}
	s, f = setup(t, "jo,hal")
	if f.n != 2 {
		t.Fatalf("operator served: started %d sessions, want 2", f.n)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil || !r.Live() {
		t.Errorf("operator served: line session state = %q, want running", r.State)
	}
}

// Each session is nudged about its own slice: hal's mail goes to hal's
// pane, jo's to the line's, and each nudge marks only its slice.
func TestMemberNudgesConversationSlice(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	serveCrew(t, s, "", map[string]string{"hal": "/tmp/hal.env"})
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	lineAgent := r.Handle["agent"].(string)
	halAgent := r.Conversations["hal"].Handle["agent"].(string)
	lineNotified := r.Notified

	must(t, s.Mail(ctx, "yardmaster", "from hal", "hal"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 || !strings.HasPrefix(f.nudges[0], halAgent+":") {
		t.Fatalf("nudges after hal's mail = %v, want one to %s", f.nudges, halAgent)
	}
	r, err = s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Notified != lineNotified {
		t.Errorf("line notified = %d, want %d (hal's mail is not its slice)", r.Notified, lineNotified)
	}
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 1 {
		t.Fatalf("nudges = %v, want no repeat for the same events", f.nudges)
	}

	must(t, s.Mail(ctx, "yardmaster", "from jo", "jo"))
	must(t, d.Reconcile(ctx))
	if len(f.nudges) != 2 || !strings.HasPrefix(f.nudges[1], lineAgent+":") {
		t.Fatalf("nudges after jo's mail = %v, want the next to %s", f.nudges, lineAgent)
	}
}

// A serving list narrowed past the operator stops the line session and
// keeps the conversation: the crew member runs no operator-login session.
func TestMemberStopsLineWhenServingNarrows(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	serveCrew(t, s, "jo,hal", map[string]string{"hal": "/tmp/hal.env"})
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Live() || !r.Conversations["hal"].Live() {
		t.Fatalf("both sessions not running: %+v %v", r, err)
	}
	stops := f.stops
	starts := f.n
	serveCrew(t, s, "hal", map[string]string{"hal": "/tmp/hal.env"})
	must(t, d.Reconcile(ctx))
	if f.stops != stops+1 {
		t.Fatalf("stops = %d, want one more (the line session)", f.stops)
	}
	if f.n != starts {
		t.Fatalf("started %d sessions, want no new start", f.n-starts)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if r.Live() || len(r.Handle) != 0 {
		t.Errorf("line session still runs: %q", r.State)
	} else if !r.Conversations["hal"].Live() {
		t.Errorf("hal session state = %q, want still running", r.Conversations["hal"].State)
	}
}

// A conversation the crew member no longer declares keeps no session:
// what still runs of it is stopped, and its row reads stopped.
func TestMemberStopsStaleConversation(t *testing.T) {
	s, f, d := crewSetup(t, store.CrewMember{})
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	serveCrew(t, s, "", map[string]string{"hal": "/tmp/hal.env"})
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Conversations["hal"].Live() {
		t.Fatalf("hal session not running: %+v %v", r.Conversations["hal"], err)
	}
	stops := f.stops
	serveCrew(t, s, "", nil)
	must(t, d.Reconcile(ctx))
	if f.stops != stops+1 {
		t.Fatalf("stops = %d, want one more (hal's session)", f.stops)
	}
	if r, err = s.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatal(err)
	} else if cs := r.Conversations["hal"]; cs.Live() || len(cs.Handle) != 0 {
		t.Errorf("hal session after undeclare = %q, want stopped", cs.State)
	} else if !r.Live() {
		t.Errorf("line session state = %q, want still running", r.State)
	}
}
