package dispatch_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// modelRunner is a statusRunner whose agents report the model they run,
// once they have answered ("" until then).
type modelRunner struct {
	*statusRunner
	model string
	asked int
}

func (m *modelRunner) Model(context.Context, map[string]any, time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked++
	return m.model, nil
}

func modelSetup(t *testing.T, asked string) (store.API, *modelRunner, *dispatch.Dispatcher, *store.Bead) {
	t.Helper()
	s := storetest.New(t)
	r := &modelRunner{statusRunner: &statusRunner{fakeRunner: &fakeRunner{dead: map[string]bool{}}, status: "working"}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	ctx := context.Background()
	cfg := map[string]any{}
	if asked != "" {
		cfg["model"] = asked
	}
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Config: cfg}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	must(t, err)
	return s, r, d, b
}

func sessionHandle(t *testing.T, s store.API, bead string) map[string]any {
	t.Helper()
	live, err := s.Sessions(context.Background(), false)
	must(t, err)
	for _, sess := range live {
		if sess.Bead == bead {
			return sess.Handle
		}
	}
	t.Fatalf("no live session for %s", bead)
	return nil
}

// An agent that reports another model than the group asked for (an agent
// that does not know the name runs one it does) raises
// session_model_mismatch once, with both names, and both are kept on the
// session's handle.
func TestAnotherModelThanAskedIsReportedOnce(t *testing.T) {
	t.Parallel()
	s, r, d, b := modelSetup(t, "claude-opus-5-5")
	ctx := context.Background()
	must(t, d.Reconcile(ctx)) // start
	must(t, d.Reconcile(ctx)) // no answer yet: nothing known
	if n := len(eventKinds(t, s, b.ID, "session_model_mismatch")); n != 0 {
		t.Fatalf("mismatch before the agent reported a model: %d", n)
	}
	if h := sessionHandle(t, s, b.ID); h[dispatch.ModelReported] != nil {
		t.Fatalf("handle = %v", h)
	}
	r.mu.Lock()
	r.model = "claude-fable-5-1"
	r.mu.Unlock()
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	// A daemon that replaced itself reads the handle and tells nobody again.
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": r}}
	must(t, d2.Reconcile(ctx))
	evs := eventKinds(t, s, b.ID, "session_model_mismatch")
	if len(evs) != 1 {
		t.Fatalf("session_model_mismatch events = %d, want 1", len(evs))
	}
	if data := evs[0].Data; data["asked"] != "claude-opus-5-5" || data["reported"] != "claude-fable-5-1" || data["group"] != "g" {
		t.Fatalf("event data = %v", data)
	}
	h := sessionHandle(t, s, b.ID)
	if h[dispatch.ModelAsked] != "claude-opus-5-5" || h[dispatch.ModelReported] != "claude-fable-5-1" {
		t.Fatalf("handle = %v", h)
	}
	r.mu.Lock()
	asked := r.asked
	r.mu.Unlock()
	must(t, d2.Reconcile(ctx))
	if r.asked != asked {
		t.Fatal("the agent is asked for its model again after it was recorded")
	}
}

// The model asked for under another of its names is no mismatch: a display
// name for an id, a dated id for an alias. The bead's own model is what
// was asked when it has one. Without any model asked, the reported one is
// recorded and nothing compared.
func TestSameModelUnderAnotherNameIsNoMismatch(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		group, bead, reported string
		mismatch              bool
	}{
		"display name":           {group: "claude-opus-5-5", reported: "Opus 5.5"},
		"same id":                {group: "gpt-6-luna", reported: "gpt-6-luna"},
		"alias":                  {group: "opus", reported: "claude-opus-5-5-20260801"},
		"alias with a window":    {group: "opus[1m]", reported: "claude-opus-5-5"},
		"muse contributor asked": {group: "muse-spark-1.3-contributor", reported: "muse-spark-1.3"},
		"muse short asked":       {group: "muse-spark-1.3", reported: "muse-spark-1.3-contributor"},
		"muse another model":     {group: "muse-spark-1.3-contributor", reported: "muse-wonder-1.0", mismatch: true},
		"nothing asked":          {reported: "claude-fable-5-1"},
		"bead's model is run":    {group: "claude-opus-5-5", bead: "haiku", reported: "claude-haiku-4-5-20251001"},
		"bead's model is not":    {group: "claude-opus-5-5", bead: "haiku", reported: "claude-opus-5-5", mismatch: true},
		"another version":        {group: "claude-opus-5-5", reported: "claude-opus-5-1", mismatch: true},
		"another display family": {group: "claude-opus-5-5", reported: "Fable 5.1", mismatch: true},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b := modelSetup(t, tc.group)
			ctx := context.Background()
			if tc.bead != "" {
				var err error
				b, err = s.CreateBead(ctx, store.NewBead{Title: "own", Priority: -1, Metadata: map[string]any{"model": tc.bead}}, "me")
				must(t, err)
			}
			r.model = tc.reported
			must(t, d.Reconcile(ctx)) // start
			must(t, d.Reconcile(ctx))
			if n := len(eventKinds(t, s, b.ID, "session_model_mismatch")); (n == 1) != tc.mismatch || n > 1 {
				t.Fatalf("session_model_mismatch events = %d, want mismatch %v", n, tc.mismatch)
			}
			h := sessionHandle(t, s, b.ID)
			want := tc.group
			if tc.bead != "" {
				want = tc.bead
			}
			if h[dispatch.ModelReported] != tc.reported || (want != "" && h[dispatch.ModelAsked] != want) ||
				(want == "" && h[dispatch.ModelAsked] != nil) {
				t.Fatalf("handle = %v, want asked %q reported %q", h, want, tc.reported)
			}
		})
	}
}

// The context window is the asked name's bracketed suffix, or 200k: the
// suffix modelWords holds no part of the name.
func TestContextWindow(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		model string
		want  int
	}{
		"no name":        {"", 200000},
		"no suffix":      {"opus", 200000},
		"a dated id":     {"claude-opus-5-5-20260801", 200000},
		"a million":      {"opus[1m]", 1000000},
		"thousands":      {"sonnet[200k]", 200000},
		"bare tokens":    {"opus[500000]", 500000},
		"upper unit":     {"opus[1M]", 1000000},
		"no unit":        {"opus[]", 200000},
		"no number":      {"opus[m]", 200000},
		"no window":      {"opus[none]", 200000},
		"zero":           {"opus[0]", 200000},
		"unclosed":       {"opus[1m", 1000000},
		"reported names": {"Opus 5.5", 200000},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := dispatch.ContextWindow(tc.model); got != tc.want {
				t.Errorf("ContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}
