package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
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

// A hook that hears a person's chat back raises no hook_quiet for hours of
// silence while the source holds a healthy webhook; a broken one is told
// of once per spell, with why instead of a gap. The source is telegram at
// a stand-in, whose getWebhookInfo the probe reads with the source's
// credential. An old failure, an answer that says nothing usable, and one
// the probe never gets tell nothing.
func TestQuietChatHookProbesWebhook(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	const token = "test-token"
	var body string
	status := 200
	probed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed++
		if r.URL.Path != "/bot"+token+"/getWebhookInfo" {
			t.Errorf("probed %s, want the check call with the source's credential in its path", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	raw, err := packs.FS.ReadFile("telegram/telegram.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declare := func(base string) {
		t.Helper()
		text := strings.Replace(string(raw), "base: https://api.telegram.org", "base: "+base, 1)
		if _, err := s.PutSource(ctx, "telegram.yaml", text, "me"); err != nil {
			t.Fatal(err)
		}
	}
	declare(srv.URL)
	env := filepath.Join(t.TempDir(), "telegram.env")
	must(t, os.WriteFile(env, []byte("TELEGRAM_BOT_TOKEN="+token+"\n"), 0o600))
	if _, err := s.SetSource(ctx, "telegram", store.SourceEnvFile, env, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutHook(ctx, store.Hook{Name: "telegram", Source: "telegram", SecretFile: "/x"}, "me"); err != nil {
		t.Fatal(err)
	}
	deliver := func() {
		t.Helper()
		if _, _, err := own.RecordHook(ctx, "telegram", map[string]any{"source": "telegram"}, []byte("{}"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		deliver()
	}
	quiets := func() []store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, store.HookQuiet, 10)
		must(t, err)
		return evs
	}
	var logged bytes.Buffer
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{}, PromptSettle: -1, PromptWait: -1, Log: slog.New(slog.NewTextHandler(&logged, nil))}
	now := time.Now()
	dispatch.SetClock(d, func() time.Time { return now })
	pass := func() {
		t.Helper()
		now = now.Add(dispatch.HookQuietEvery + time.Minute)
		must(t, d.Reconcile(ctx))
	}

	// Hours of silence with a healthy webhook: nothing, though the probe
	// went out.
	url := "https://yard.example.com/hooks/telegram"
	body = `{"ok":true,"result":{"url":"` + url + `","pending_update_count":0}}`
	pass()
	if n := len(quiets()); n != 0 {
		t.Fatalf("hook_quiet logged %d times for hours of silence with a healthy webhook, want 0", n)
	}
	if probed == 0 {
		t.Fatal("the webhook was never probed")
	}

	// A declaration that sends to another host than the credential was given
	// for: the probe is not tried, not even to judge a webhook that is broken,
	// so the credential reaches neither host and no break is told over a
	// probe that was not sent. The log says why. The declaration restored,
	// the probe judges again, as the next step shows.
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(other.Close)
	body = `{"ok":true,"result":{"url":"","pending_update_count":0}}`
	declare(other.URL)
	probes := probed
	pass()
	if probed != probes || elsewhere.Load() != 0 {
		t.Fatalf("with the credential held the probe reached the host it was given for %d times and the other %d times, want none", probed-probes, elsewhere.Load())
	}
	if n := len(quiets()); n != 0 {
		t.Fatalf("hook_quiet logged %d times for a probe that was not sent, want 0", n)
	}
	for _, want := range []string{"quiet hooks", "hook=telegram", "its credential was given for " + srv.URL, "the source's calls go to " + other.URL + " now"} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("the log of a held probe lacks %q:\n%s", want, logged.String())
		}
	}
	if strings.Contains(logged.String(), token) {
		t.Fatalf("the log of a held probe has the credential in it:\n%s", logged.String())
	}
	declare(srv.URL)

	// No webhook set at the source: told once, with why, and not again on
	// the next pass.
	body = `{"ok":true,"result":{"url":"","pending_update_count":0}}`
	pass()
	evs := quiets()
	if len(evs) != 1 {
		t.Fatalf("hook_quiet logged %d times with no webhook set, want 1", len(evs))
	}
	e := evs[0]
	h, _ := s.GetHook(ctx, "telegram")
	last, err := time.Parse(time.RFC3339, e.Data["last"].(string))
	if err != nil || last.Sub(*h.Accepted).Abs() > time.Minute ||
		e.Data["hook"] != "telegram" || e.Data["source"] != "telegram" ||
		e.Data["reason"] != "no webhook is set at the source" || e.Actor != "yard" || e.Bead != nil {
		t.Fatalf("hook_quiet = %+v, %v; want hook, source, last and reason, by the yard, about no bead", e.Data, err)
	}
	if _, has := e.Data["gap"]; has {
		t.Fatalf("a chat hook's hook_quiet carries a gap: %+v", e.Data)
	}
	pass()
	if n := len(quiets()); n != 1 {
		t.Fatalf("hook_quiet logged %d times an hour later, want the 1", n)
	}

	// A delivery ends the spell; a failure since it tells anew, with what
	// waits behind it. The service reports whole seconds, so an error in
	// the same second as the accepted delivery is treated as possibly later.
	deliver()
	h, _ = s.GetHook(ctx, "telegram")
	failed := h.Accepted.Unix()
	body = fmt.Sprintf(`{"ok":true,"result":{"url":"%s","pending_update_count":3,"last_error_date":%d,"last_error_message":"Connection timed out; TELEGRAM_BOT_TOKEN=never-log-me"}}`, url, failed)
	pass()
	evs = quiets()
	if len(evs) != 2 {
		t.Fatalf("hook_quiet logged %d times with deliveries failing, want 2", len(evs))
	}
	want := "the source's deliveries fail (last failed " +
		time.Unix(failed, 0).UTC().Format(time.RFC3339) + ", 3 pending)"
	if evs[1].Data["reason"] != want {
		t.Fatalf("hook_quiet reason = %q, want %q", evs[1].Data["reason"], want)
	}
	if strings.Contains(evs[1].Data["reason"].(string), "never-log-me") {
		t.Fatalf("hook_quiet copied the source's error message: %+v", evs[1].Data)
	}

	// A failure from before the last delivery is no break; neither is an
	// answer that says nothing usable, nor one the probe never gets.
	deliver()
	h, _ = s.GetHook(ctx, "telegram")
	old := h.Accepted.Add(-time.Hour).Unix()
	for name, answer := range map[string]string{
		"an old failure":       fmt.Sprintf(`{"ok":true,"result":{"url":"%s","pending_update_count":0,"last_error_date":%d,"last_error_message":"gone now"}}`, url, old),
		"no result":            `{"ok":true}`,
		"a result of no shape": `{"ok":true,"result":"up"}`,
	} {
		body = answer
		pass()
		if n := len(quiets()); n != 2 {
			t.Fatalf("hook_quiet logged %d times for %s, want the 2", n, name)
		}
	}
	status = http.StatusInternalServerError
	pass()
	if n := len(quiets()); n != 2 {
		t.Fatalf("hook_quiet logged %d times when the probe got no answer, want the 2", n)
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
