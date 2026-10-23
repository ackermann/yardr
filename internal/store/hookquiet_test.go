package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// The usual gap is three times the median interval between the hook's
// accepted deliveries over the last week, with a floor of an hour. With
// fewer than two deliveries in the week, the rhythm falls back to the
// recent deliveries, however old.
func TestQuietGap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ago := func(ds ...time.Duration) []time.Time {
		out := make([]time.Time, len(ds))
		for i, d := range ds {
			out[i] = now.Add(-d)
		}
		return out
	}
	h, d := time.Hour, 24*time.Hour
	for _, c := range []struct {
		name string
		at   []time.Time
		want time.Duration
	}{
		{"none", nil, h},
		{"one", ago(h), h},
		{"two half an hour apart", ago(2*h, 90*time.Minute), 90 * time.Minute},
		{"two ten minutes apart, under the floor", ago(2*h, 110*time.Minute), h},
		{"hourly", ago(4*h, 3*h, 2*h, h), 3 * h},
		{"an even count takes the middle two", ago(390*time.Minute, 210*time.Minute, 90*time.Minute, 30*time.Minute, 0), 270 * time.Minute},
		{"any order", ago(h, 4*h, 2*h, 3*h), 3 * h},
		{"at the same instant", ago(h, h), h},
		{"older than the week is ignored", append(ago(10*d, 9*d, 8*d), ago(4*h, h)...), 9 * h},
		{"fewer than two in the week falls back to the rhythm", ago(10*d, 9*d, 8*d), 3 * d},
		{"one in the week falls back too", append(ago(10*d, 9*d, 8*d), ago(h)...), 3 * d},
		{"a monthly rhythm is its own long gap", ago(90*d, 60*d, 30*d), 90 * d},
		{"future times are ignored", append(ago(30*d, 20*d), now.Add(20*d)), 30 * d},
	} {
		if got := store.QuietGap(c.at, now); got != c.want {
			t.Errorf("%s: gap = %s, want %s", c.name, got, c.want)
		}
	}
}

// A hook is quiet when it accepted a delivery before and has accepted none
// since for longer than its usual gap; one that never delivered is not.
func TestQuiet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	h, d := time.Hour, 24*time.Hour
	last := func(ago time.Duration) *time.Time { t := now.Add(-ago); return &t }
	for _, c := range []struct {
		name       string
		last       *time.Time
		deliveries []time.Time
		quiet      bool
		gap        time.Duration
	}{
		{"never delivered", nil, nil, false, h},
		{"delivered half an hour ago", last(30 * time.Minute), []time.Time{now.Add(-30 * time.Minute)}, false, h},
		{"silent for exactly the gap", last(h), []time.Time{now.Add(-h)}, false, h},
		{"silent past the gap", last(h + time.Minute), []time.Time{now.Add(-h - time.Minute)}, true, h},
		{"a delivery after the hook snapshot ends the silence", last(2 * h), []time.Time{now.Add(-30 * time.Minute)}, false, h},
		{"a monthly hook ten days out", last(10 * d), []time.Time{now.Add(-70 * d), now.Add(-40 * d), now.Add(-10 * d)}, false, 90 * d},
		{"a monthly hook a season out", last(100 * d), []time.Time{now.Add(-160 * d), now.Add(-130 * d), now.Add(-100 * d)}, true, 90 * d},
	} {
		if quiet, gap := store.Quiet(c.last, c.deliveries, now); quiet != c.quiet || gap != c.gap {
			t.Errorf("%s: quiet, gap = %v, %s; want %v, %s", c.name, quiet, gap, c.quiet, c.gap)
		}
	}
}

// QuietGaps judges each hook from its own deliveries: a hook that delivered
// just now is not quiet; hours later, with nothing since, it is, with the
// floor for a gap. A hook that never delivered, one that is blocked and one
// the source still reaches are not judged.
func TestQuietGaps(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, name := range []string{"linear", "new", "blocked", "refused"} {
		if _, err := s.PutHook(ctx, store.Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := own.RecordHook(ctx, "refused", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	if err := own.BlockHook(ctx, "blocked", "secret file /x is gone"); err != nil {
		t.Fatal(err)
	}
	if err := own.RejectHook(ctx, "refused", "unauthorized", 401, 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	hs, err := s.Hooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if quiet, err := store.QuietGaps(ctx, s, hs, time.Now()); err != nil || len(quiet) != 0 {
		t.Fatalf("just delivered: quiet = %v, %v", quiet, err)
	}
	quiet, err := store.QuietGaps(ctx, s, hs, time.Now().Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 1 || quiet["linear"] != time.Hour {
		t.Fatalf("seven hours later: quiet = %v, want linear with the floor", quiet)
	}
}

// QuietGaps leaves out a hook whose source hears a person's chat back (a
// hear key matched by address, as telegram's): hours of silence are no
// break there. A hook of a timed source (the generic webhook's hook, the
// one timed hook among the embedded packs) is judged by its gap as
// before.
func TestQuietGapsSkipsChatHook(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, name := range []string{"telegram", "webhook"} {
		raw, err := packs.FS.ReadFile(name + "/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutSource(ctx, name+".yaml", string(raw), "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutHook(ctx, store.Hook{Name: name, Source: name, SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, _, err := own.RecordHook(ctx, name, map[string]any{"source": name}, []byte("{}"), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	hs, err := s.Hooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := store.QuietGaps(ctx, s, hs, time.Now().Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 1 || quiet["webhook"] != time.Hour {
		t.Fatalf("seven hours later: quiet = %v, want webhook with the floor", quiet)
	}
}

// QuietGaps leaves out a hook whose source's deliveries follow events, not
// a rhythm (the hook's untimed key, as github's): a day of silence after
// one delivery is no break there. A hook of the same source whose last
// delivery was refused is left out as before, through LastRefused; a chat
// source is still left out without the key; and a source with neither the
// key nor a chat hear, as the generic webhook's, is timed as before.
func TestQuietGapsSkipsUntimedHook(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, name := range []string{"github", "telegram", "webhook"} {
		raw, err := packs.FS.ReadFile(name + "/" + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutSource(ctx, name+".yaml", string(raw), "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []store.Hook{
		{Name: "gh", Source: "github", SecretFile: "/x"},
		{Name: "ghref", Source: "github", SecretFile: "/x"},
		{Name: "telegram", Source: "telegram", SecretFile: "/x"},
		{Name: "webhook", Source: "webhook", SecretFile: "/x"},
	} {
		if _, err := s.PutHook(ctx, h, "me"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := own.RecordHook(ctx, h.Name, map[string]any{"source": h.Source}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := own.RejectHook(ctx, "ghref", "unauthorized", 401, 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	hs, err := s.Hooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := store.QuietGaps(ctx, s, hs, time.Now().Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 1 || quiet["webhook"] != time.Hour {
		t.Fatalf("a day later: quiet = %v, want webhook with the floor", quiet)
	}
	for _, h := range hs {
		if h.Name == "ghref" && !h.LastRefused() {
			t.Fatalf("the refused hook is not refused: %+v", h)
		}
	}
}

// EventsOfHook reads one hook's own events of one kind, oldest first: a
// busy hook's many never crowd another's out. The tells of a hundred quiet
// hooks leave the first's in hand.
func TestEventsOfHook(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	hook := func(kind, name string) {
		t.Helper()
		if err := own.LogEvent(ctx, "", kind, "hook:"+name, map[string]any{"hook": name, "source": "token"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		hook("hook", "linear")
	}
	for i := 0; i < 60; i++ {
		hook("hook", "gh")
	}
	linear, err := s.EventsOfHook(ctx, "hook", "linear", 20)
	if err != nil || !linear.Complete || len(linear.Events) != 3 {
		t.Fatalf("linear's deliveries = %+v, %v; want its 3, complete", linear, err)
	}
	for _, e := range linear.Events {
		if e.Data["hook"] != "linear" || e.Kind != "hook" {
			t.Fatalf("linear's deliveries hold %+v", e.Data)
		}
	}
	for i := 1; i < len(linear.Events); i++ {
		if linear.Events[i].At.Before(linear.Events[i-1].At) {
			t.Fatalf("linear's deliveries are not oldest first: %v then %v", linear.Events[i-1].At, linear.Events[i].At)
		}
	}
	gh, err := s.EventsOfHook(ctx, "hook", "gh", 20)
	if err != nil || !gh.Complete || len(gh.Events) != 20 {
		t.Fatalf("gh's deliveries = %+v, %v; want its newest 20, complete", gh, err)
	}
	all, err := s.EventsOfHook(ctx, "hook", "gh", 0)
	if err != nil || !all.Complete || len(all.Events) != 50 {
		t.Fatalf("gh's deliveries with no limit = %+v, %v; want 50, complete", all, err)
	}
	none, err := s.EventsOfHook(ctx, "hook", "", 20)
	if err != nil || !none.Complete || len(none.Events) != 0 {
		t.Fatalf("no hook's deliveries = %+v, %v; want none, complete", none, err)
	}
	for i := 0; i < 105; i++ {
		name := fmt.Sprintf("q%03d", i)
		if err := s.LogDispatch(ctx, "", store.HookQuiet, "yard", map[string]any{"hook": name}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.EventsOfHook(ctx, store.HookQuiet, "q000", 1)
	if err != nil || !first.Complete || len(first.Events) != 1 || first.Events[0].Data["hook"] != "q000" {
		t.Fatalf("the first tell = %+v, %v; want q000's, complete", first, err)
	}
}

// A quiet hook is told through the yardr source on kind alone, like the
// upgrade's failure kinds: a subscription to the kind carries it, and so
// does one to the class attention. A crew member that hears the class
// through --on has it in its inbox with no subscription.
func TestHookQuietReachesSubscribers(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	crew := func(name string, on ...string) {
		t.Helper()
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", On: on}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	crew("yardmaster", "gave_up")
	crew("clerk", store.KindAttention)
	crew("deaf", "gave_up")
	sub := func(name, kind string) {
		t.Helper()
		if err := s.AddSubscription(ctx, name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
			Where: map[string]string{"kind": kind}}, name); err != nil {
			t.Fatal(err)
		}
	}
	sub("yardmaster", store.HookQuiet)
	crew("watcher", "gave_up")
	sub("watcher", store.KindAttention)
	sub("deaf", "gave_up")
	if err := s.LogDispatch(ctx, "", store.HookQuiet, "yard",
		map[string]any{"hook": "linear", "source": "linear", "last": "2026-10-14T09:12:00Z", "gap": "1h0m0s"}); err != nil {
		t.Fatalf("hook_quiet is no dispatch kind: %v", err)
	}
	evs, err := s.EventsOfKind(ctx, store.HookQuiet, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("hook_quiet events = %+v, %v", evs, err)
	}
	got, _ := evs[0].Data["deliveries"].(map[string]any)
	if len(got) != 2 || got["yardmaster"] == nil || got["watcher"] == nil {
		t.Fatalf("hook_quiet reached %v, want yardmaster and watcher", got)
	}
	for name, want := range map[string]string{"yardmaster": "hook_quiet", "clerk": "hook_quiet", "watcher": "hook_quiet", "deaf": ""} {
		if kinds := inboxKinds(t, s, name); kinds != want {
			t.Errorf("%s's inbox holds %q, want %q", name, kinds, want)
		}
	}
}
