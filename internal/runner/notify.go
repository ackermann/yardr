package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
)

// Notified is the event logged on a bead once a human was told it waits in
// a stage. It is the memory behind the once-per-bead+stage rule, kept in the
// store so a daemon restart (or a released and re-claimed manual session)
// does not notify again.
const Notified = "human_notified"

// ErrNotifyOff is returned by a notifier that will not show anything until
// its configuration changes; retrying is pointless.
var ErrNotifyOff = errors.New("notifications are disabled")

// Manual starts nothing. The bead waits on its group until someone advances
// or closes it. Groups with config notify=true get a notification (through
// Show) the first time a bead reaches them in a stage. A notification that
// could not be shown (nobody attached, busy) is retried on later liveness
// checks while the session lives.
type Manual struct {
	Store store.API
	// Show displays a notification; nil disables notifications.
	Show func(ctx context.Context, title, body string) error
	Log  *slog.Logger

	mu     sync.Mutex
	done   map[string]bool   // session -> notified or given up
	logged map[string]string // session -> last error logged
}

func (m *Manual) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	h := map[string]any{"manual": true}
	if !wantsNotify(job.Group) || m.Show == nil || m.Store == nil {
		return h, nil
	}
	h["notify"] = map[string]any{"bead": job.Bead.ID, "title": job.Bead.Title, "stage": job.Bead.Stage,
		"group": job.Group.Name}
	m.notify(ctx, job.Session.ID, h)
	return h, nil
}

// Alive is always true; it retries a pending notification on the way.
func (m *Manual) Alive(ctx context.Context, h map[string]any) (bool, error) {
	if sess, _ := h["session"].(string); sess != "" && m.Show != nil && m.Store != nil {
		m.notify(ctx, sess, h)
	}
	return true, nil
}

func (*Manual) Stop(context.Context, map[string]any) error { return nil }

// notify shows the handle's notification unless it was shown before for
// this bead and stage. Failures are logged (once per session and cause),
// never returned: notifying must not fail or end a claim.
func (m *Manual) notify(ctx context.Context, sess string, h map[string]any) {
	n, _ := h["notify"].(map[string]any)
	field := func(k string) string { v, _ := n[k].(string); return v }
	bead, stage, group := field("bead"), field("stage"), field("group")
	if bead == "" {
		return
	}
	h["session"] = sess
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == nil {
		m.done, m.logged = map[string]bool{}, map[string]string{}
	}
	if m.done[sess] {
		return
	}
	err := func() error {
		seen, err := AlreadyNotified(ctx, m.Store, bead, stage)
		if err != nil || seen {
			return err
		}
		title, body := NotificationText(bead, field("title"), stage, group)
		if err := m.Show(ctx, title, body); err != nil {
			return err
		}
		return m.Store.LogEvent(ctx, bead, Notified, "yard", map[string]any{
			"stage": stage, "group": group, "session": sess})
	}()
	if err == nil || errors.Is(err, ErrNotifyOff) {
		m.done[sess] = true
	}
	if err != nil && m.logged[sess] != err.Error() {
		m.logged[sess] = err.Error()
		m.log().Warn("notify", "bead", bead, "stage", stage, "session", sess, "err", err)
	}
}

func (m *Manual) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// wantsNotify is opt-in so placeholder groups (backlog, decide) stay quiet.
// When groups get members (yard-19), groups with members should default on.
func wantsNotify(g store.Group) bool {
	switch v := g.Config["notify"].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

// AlreadyNotified reports whether a human was notified about bead in stage
// since the bead last entered a stage. Re-entering the stage later (sent
// back from review, reopened) notifies again.
func AlreadyNotified(ctx context.Context, st store.API, bead, stage string) (bool, error) {
	evs, err := st.Events(ctx, bead, 1000)
	if err != nil {
		return false, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		switch e := evs[i]; e.Kind {
		case Notified:
			if e.Data["stage"] == stage {
				return true, nil
			}
		case "advanced", "reopened", "created":
			return false, nil
		}
	}
	return false, nil
}

// NotificationText names the bead, its title and stage, and how to act.
func NotificationText(bead, beadTitle, stage, group string) (title, body string) {
	title = fmt.Sprintf("yard: %s needs you (%s)", bead, stage)
	body = fmt.Sprintf("%s\nstage %s · group %s\nyard bead show %s", beadTitle, stage, group, bead)
	return title, body
}

// HerdrNotifier shows notifications with `herdr notification show`. herdr
// answers success even when it showed nothing, so its result is checked:
// only "shown" counts.
func HerdrNotifier(bin string) func(ctx context.Context, title, body string) error {
	return func(ctx context.Context, title, body string) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "notification", "show", title,
			"--body", body, "--sound", "request").CombinedOutput()
		if err != nil {
			return fmt.Errorf("herdr notification show: %w: %s", err, strings.TrimSpace(string(out)))
		}
		var resp struct {
			Result struct {
				Shown  bool   `json:"shown"`
				Reason string `json:"reason"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return fmt.Errorf("herdr notification show: unparseable output: %s", out)
		}
		switch {
		case resp.Result.Shown:
			return nil
		case resp.Result.Reason == "disabled":
			return fmt.Errorf("herdr: %w in its config", ErrNotifyOff)
		}
		return fmt.Errorf("herdr did not show the notification: %s", resp.Result.Reason)
	}
}
