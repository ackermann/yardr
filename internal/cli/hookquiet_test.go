package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// quietYard is a yard with a hook that delivered three times just now, one
// that never delivered, one the receiver blocked and one whose last
// delivery was refused.
func quietYard(t *testing.T) (store.API, *store.Store) {
	t.Helper()
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	for _, name := range []string{"linear", "new", "blocked", "refused"} {
		if _, err := st.PutHook(ctx, store.Hook{Name: name, Source: "token", SecretFile: "/x"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := own.BlockHook(ctx, "blocked", "secret file /x is gone"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := own.RecordHook(ctx, "refused", map[string]any{"source": "token"}, []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	if err := own.RejectHook(ctx, "refused", "unauthorized", 401, 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return st, own
}

// Doctor notes a quiet hook on its hooks line: when it last accepted a
// delivery, how long that is and the usual gap. Where the server has an
// address the note probes the yard's own listener: one that answers leaves
// the break past it, and one that does not answer is the break.
func TestDoctorNotesQuietHook(t *testing.T) {
	st, _ := quietYard(t)
	ctx := context.Background()
	now := time.Now().Add(7 * time.Hour)
	stub(t, &hookNow, func() time.Time { return now })
	parts, err := optionalParts(ctx, &config.Config{Home: t.TempDir()}, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHook(ctx, "linear")
	if err != nil {
		t.Fatal(err)
	}
	want := "hook linear quiet: last accepted a delivery " + since(*h.Accepted) + " (nothing for 7h, usual gap 1h)"
	var notes []string
	for _, p := range parts {
		if p.Key == "hooks" {
			for _, n := range p.Notes {
				if strings.Contains(n, "quiet") {
					notes = append(notes, n)
				}
			}
		}
	}
	if len(notes) != 1 || notes[0] != want {
		t.Fatalf("doctor's hooks notes = %q, want only %q", notes, want)
	}

	// With an address: the probe answers, or it does not.
	l := hooks.Listener{Addr: "127.0.0.1:8790", From: hooks.FromSetting}
	stub(t, &answers, func(addr string) bool {
		if addr != "127.0.0.1:8790" {
			t.Errorf("probed %q", addr)
		}
		return true
	})
	if got := quietNote(*h, time.Hour, now, l); !strings.HasPrefix(got, want+"; the yard's listener on 127.0.0.1:8790 answers, "+
		"so deliveries stop between the source and the yard: the webhook is off there, or the proxy in front of the yard is down") {
		t.Errorf("an answering listener:\n%s", got)
	}
	stub(t, &answers, func(string) bool { return false })
	if got := quietNote(*h, time.Hour, now, l); !strings.HasPrefix(got, want+"; nothing answers on 127.0.0.1:8790: the yard's own listener is down") {
		t.Errorf("a silent listener:\n%s", got)
	}
	called := false
	stub(t, &answers, func(string) bool {
		called = true
		return true // another process may own this address after bind failed
	})
	l.Error = "address already in use"
	if got := quietNote(*h, time.Hour, now, l); got != want+"; the server could not listen on 127.0.0.1:8790: address already in use" || called {
		t.Errorf("a failed listener was probed or misreported: %q (probed = %v)", got, called)
	}
}

// Hook list reads quiet in STATE for a hook that delivered nothing for
// longer than its usual gap, and carries the silence and the gap in JSON.
func TestHookListShowsQuiet(t *testing.T) {
	st, _ := quietYard(t)
	a := &app{st: st, actor: "me"}
	now := time.Now().Add(7 * time.Hour)
	stub(t, &hookNow, func() time.Time { return now })
	out, err := run(t, a.hookCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	var linear string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "linear ") {
			linear = l
		}
	}
	if !strings.Contains(linear, "quiet: nothing for 7h (usual gap 1h)") {
		t.Fatalf("hook list:\n%s", out)
	}
	for _, name := range []string{"new", "blocked", "refused"} {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, name+" ") && strings.Contains(l, "quiet:") {
				t.Fatalf("hook list judges %s quiet:\n%s", name, out)
			}
		}
	}
	out, err = run(t, (&app{st: st, actor: "me", json: true}).hookCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	var rows []hookRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("hook list --json: %v\n%s", err, out)
	}
	for _, r := range rows {
		switch r.Name {
		case "linear":
			if r.Quiet == nil || r.Quiet.Silent != "7h" || r.Quiet.Gap != "1h" {
				t.Fatalf("linear's json quiet = %+v", r.Quiet)
			}
		default:
			if r.Quiet != nil {
				t.Fatalf("%s's json quiet = %+v, want none", r.Name, r.Quiet)
			}
		}
	}
}

func TestSpanAndLoopback(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Hour:                 "1h",
		7 * time.Hour:             "7h",
		7*time.Hour - time.Minute: "7h",
		47 * time.Hour:            "47h",
		50 * time.Hour:            "2d",
		7 * 24 * time.Hour:        "7d",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%s) = %q, want %q", d, got, want)
		}
	}
	for addr, want := range map[string]string{
		"127.0.0.1:8790": "127.0.0.1:8790",
		"0.0.0.0:8790":   "127.0.0.1:8790",
		":8790":          "127.0.0.1:8790",
		"[::]:8790":      "127.0.0.1:8790",
		"example.com:1":  "example.com:1",
		"garbage":        "garbage",
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %q, want %q", addr, got, want)
		}
	}
}
