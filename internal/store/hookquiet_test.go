package store_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The usual gap is three times the median interval between the hook's
// accepted deliveries over the last week, with a floor of an hour. With
// fewer than two deliveries in the week, the rhythm falls back to the
// recent deliveries, however old.
func TestQuietGap(t *testing.T) {
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

// A quiet hook is told through the yardr source on kind alone, like the
// upgrade's failure kinds: a subscription to the kind carries it, and so
// does one to the class attention. A crew member that hears the class
// through --on has it in its inbox with no subscription.
func TestHookQuietReachesSubscribers(t *testing.T) {
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
			Where: map[string]string{"kind": kind}}, "me"); err != nil {
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
