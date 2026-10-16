package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A busy hook's deliveries never crowd another's rhythm out of the read: a
// hook that delivers every three days is judged by its own nine-day gap
// past a thousand of another's, quiet only when its own gap has passed.
func TestQuietGapsBusyHook(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	ctx := context.Background()
	for _, name := range []string{"linear", "gh"} {
		if _, err := s.PutHook(ctx, Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(name string) {
		t.Helper()
		if _, _, err := s.RecordHook(ctx, name, map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, day := range []int{0, 3, 6} {
		at = time.Date(2026, 10, 1+day, 12, 0, 0, 0, time.UTC)
		deliver("linear")
	}
	for i := 0; i < 3; i++ {
		deliver("gh")
	}
	for i := 0; i < 1050; i++ {
		if err := s.LogEvent(ctx, "", "hook", "hook:gh", map[string]any{"hook": "gh", "source": "token"}); err != nil {
			t.Fatal(err)
		}
	}
	hs, err := s.Hooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	judge := func(day int) map[string]time.Duration {
		t.Helper()
		quiet, err := QuietGaps(ctx, s, hs, time.Date(2026, 10, 1+day, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		return quiet
	}
	if quiet := judge(8); len(quiet) != 1 || quiet["gh"] != time.Hour {
		t.Fatalf("two days after the deliveries: quiet = %v, want gh alone (linear's gap is nine days)", quiet)
	}
	quiet := judge(16)
	if len(quiet) != 2 || quiet["linear"] != 9*24*time.Hour || quiet["gh"] != time.Hour {
		t.Fatalf("ten days after: quiet = %v, want linear with its nine-day gap and gh", quiet)
	}
}

// A hook whose deliveries the walk does not reach is not judged: with the
// walk stubbed short past a busy hook's many, neither hook reads quiet,
// whatever their silence.
func TestQuietGapsSkipsUnreachedHook(t *testing.T) {
	// Sequential: process environment and the quietScan window.
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	quietScan = 10
	t.Cleanup(func() { quietScan = 10000 })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	ctx := context.Background()
	for _, name := range []string{"linear", "gh"} {
		if _, err := s.PutHook(ctx, Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, _, err := s.RecordHook(ctx, "linear", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.RecordHook(ctx, "gh", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		if err := s.LogEvent(ctx, "", "hook", "hook:gh", map[string]any{"hook": "gh", "source": "token"}); err != nil {
			t.Fatal(err)
		}
	}
	hs, err := s.Hooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := QuietGaps(ctx, s, hs, time.Now().Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 0 {
		t.Fatalf("past an unreached walk: quiet = %v, want none judged", quiet)
	}
}
