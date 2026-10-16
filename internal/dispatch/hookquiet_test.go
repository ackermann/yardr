package dispatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

type failHookEventsOnce struct {
	store.API
	failed bool
}

func (s *failHookEventsOnce) EventsOfHook(ctx context.Context, kind, hook string, limit int) (*store.HookEvents, error) {
	if !s.failed {
		s.failed = true
		return nil, errors.New("temporary event read failure")
	}
	return s.API.EventsOfHook(ctx, kind, hook, limit)
}

// A hook that delivered and then nothing for longer than its usual gap logs
// hook_quiet once, by the daemon's clock, and the yardmaster has it in its
// inbox. A later pass, and a daemon that has just started, log nothing
// more; a delivery ends the spell, and the next quiet logs anew. A hook
// that never delivered, one that is blocked and one the source still
// reaches log nothing.
func TestQuietHookLogsHookQuietOnce(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	f := &crewRunner{status: map[string]string{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake", On: []string{store.KindAttention}}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"linear", "new", "blocked", "refused"} {
		if _, err := s.PutHook(ctx, store.Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(name string) {
		t.Helper()
		if _, _, err := own.RecordHook(ctx, name, map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		deliver("linear")
	}
	deliver("refused")
	if err := own.BlockHook(ctx, "blocked", "secret file /x is gone"); err != nil {
		t.Fatal(err)
	}
	if err := own.RejectHook(ctx, "refused", "unauthorized", 401, 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	quiets := func() []store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, store.HookQuiet, 10)
		must(t, err)
		return evs
	}
	start := time.Now()
	now := start
	dispatch.SetClock(d, func() time.Time { return now })

	must(t, d.Reconcile(ctx))
	if n := len(quiets()); n != 0 {
		t.Fatalf("hook_quiet logged %d times just after the deliveries", n)
	}
	now = start.Add(7 * time.Hour)
	must(t, d.Reconcile(ctx))
	evs := quiets()
	if len(evs) != 1 {
		t.Fatalf("hook_quiet logged %d times seven hours on, want 1", len(evs))
	}
	e := evs[0]
	last, err := time.Parse(time.RFC3339, e.Data["last"].(string))
	h, _ := s.GetHook(ctx, "linear")
	if err != nil || last.Sub(*h.Accepted).Abs() > time.Minute ||
		e.Data["hook"] != "linear" || e.Data["source"] != "token" || e.Data["gap"] != "1h0m0s" || e.Actor != "yard" || e.Bead != nil {
		t.Fatalf("hook_quiet = %+v, %v; want hook, source, last and gap, by the yard, about no bead", e.Data, err)
	}
	page, err := s.Inbox(ctx, "yardmaster", 0)
	must(t, err)
	inbox := 0
	for _, e := range page.Events {
		if e.Kind == store.HookQuiet {
			inbox++
		}
	}
	if inbox != 1 {
		t.Fatalf("the yardmaster, who hears the class attention, has %d hook_quiet in its inbox, want 1: %+v", inbox, page.Events)
	}

	// Not again: on a later pass, an hour on, or by a daemon that has just
	// started and remembers nothing.
	must(t, d.Reconcile(ctx))
	now = start.Add(8*time.Hour + time.Minute)
	must(t, d.Reconcile(ctx))
	if n := len(quiets()); n != 1 {
		t.Fatalf("hook_quiet logged %d times an hour later, want the 1", n)
	}
	fresh := &dispatch.Dispatcher{Store: s, Runners: d.Runners, PromptSettle: -1, PromptWait: -1}
	dispatch.SetClock(fresh, func() time.Time { return now })
	must(t, fresh.Reconcile(ctx))
	if n := len(quiets()); n != 1 {
		t.Fatalf("hook_quiet logged %d times after the daemon was replaced, want the 1", n)
	}

	// A delivery ends the spell: looked at half an hour later, the hook is
	// not quiet. Two hours on it is quiet again, and told of anew.
	deliver("linear")
	back := time.Now()
	second := &dispatch.Dispatcher{Store: s, Runners: d.Runners, PromptSettle: -1, PromptWait: -1}
	at := back.Add(30 * time.Minute)
	dispatch.SetClock(second, func() time.Time { return at })
	must(t, second.Reconcile(ctx))
	if n := len(quiets()); n != 1 {
		t.Fatalf("hook_quiet logged %d times half an hour after a delivery, want the 1", n)
	}
	at = back.Add(2 * time.Hour)
	must(t, second.Reconcile(ctx))
	if n := len(quiets()); n != 2 {
		t.Fatalf("hook_quiet logged %d times in the second spell, want 2", n)
	}
	for _, e := range quiets() {
		if e.Data["hook"] != "linear" {
			t.Fatalf("hook_quiet for %v, want linear's alone", e.Data["hook"])
		}
	}
}

// Hooks that go quiet together are each told of once: one's tell never
// crowds another's out of the read.
func TestQuietHooksTellEachOnce(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	f := &crewRunner{status: map[string]string{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f}, PromptSettle: -1, PromptWait: -1}
	for _, name := range []string{"linear", "gh"} {
		if _, err := s.PutHook(ctx, store.Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, _, err := own.RecordHook(ctx, name, map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	quiets := func() []store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, store.HookQuiet, 10)
		must(t, err)
		return evs
	}
	told := func() map[string]int {
		t.Helper()
		seen := map[string]int{}
		for _, e := range quiets() {
			seen[e.Data["hook"].(string)]++
		}
		return seen
	}
	now := time.Now().Add(7 * time.Hour)
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, d.Reconcile(ctx))
	if seen := told(); len(quiets()) != 2 || seen["linear"] != 1 || seen["gh"] != 1 {
		t.Fatalf("hook_quiet told of %v, want linear and gh once each", seen)
	}
	now = now.Add(dispatch.HookQuietEvery + time.Minute)
	must(t, d.Reconcile(ctx))
	if n := len(quiets()); n != 2 {
		t.Fatalf("hook_quiet logged %d times an hour later, want the 2", n)
	}
}

// A store read failure does not count as the hourly check: the next pass
// retries and tells the yardmaster if the hook is still quiet.
func TestQuietHookReadFailureRetriesNextPass(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := s.PutHook(ctx, store.Hook{Name: "linear", Source: "token", SecretFile: "/x"}, "me"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	now := start.Add(7 * time.Hour)
	wrapped := &failHookEventsOnce{API: s}
	d := &dispatch.Dispatcher{Store: wrapped, Runners: map[string]dispatch.Runner{}}
	dispatch.SetClock(d, func() time.Time { return now })

	must(t, d.Reconcile(ctx))
	now = now.Add(time.Minute)
	must(t, d.Reconcile(ctx))
	evs, err := s.EventsOfKind(ctx, store.HookQuiet, 10)
	must(t, err)
	if len(evs) != 1 || evs[0].Data["hook"] != "linear" {
		t.Fatalf("hook_quiet after the retry = %+v, want linear's event", evs)
	}
}
