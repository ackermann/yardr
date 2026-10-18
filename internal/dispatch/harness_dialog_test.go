package dispatch_test

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// kimiDialogUnknown is the end of a pane at Kimi's folder-trust dialog
// with answers this yardr does not know (yardr-tzkk): a later version
// renamed them again.
const kimiDialogUnknown = ` ────────────────────────────────────────────
Trust this folder?
↑↓ navigate · Enter select · Esc exit
/private/tmp/y1z/wt/r1/b1
No project integrations or instructions to activate.
   ❯ Trust this folder and remember it
     Exit without trusting`

// kimiDialogKnown is the same dialog with an answer the file names.
const kimiDialogKnown = ` ────────────────────────────────────────────
Trust this folder?
↑↓ navigate · Enter select · Esc exit
/private/tmp/y1z/wt/r1/b1
   ❯ Trust and continue
     Exit`

// dialogRunner is an agentRunner whose agent is at a dialog, so nothing is
// taken, and whose harness version is known.
type dialogRunner struct {
	*agentRunner
	version string
}

func (d *dialogRunner) Nudge(context.Context, map[string]any, string) error {
	return dispatch.ErrAgentBlocked
}

func (d *dialogRunner) HarnessVersion(context.Context, string) string { return d.version }

// setPending leaves prompt on the handle of the session that works b, with
// look as what the start saw of it.
func setPending(t *testing.T, s store.API, b store.Bead, prompt, look string) {
	t.Helper()
	ctx := context.Background()
	got, err := s.GetBead(ctx, b.ID)
	must(t, err)
	sess, err := s.GetSession(ctx, *got.Session)
	must(t, err)
	h := maps.Clone(sess.Handle)
	if h == nil {
		h = map[string]any{}
	}
	h[store.PendingPrompt], h[dispatch.PromptLook] = prompt, look
	must(t, s.SetHandle(ctx, sess.ID, h))
}

// A session that sits idle at its start at its harness's folder-trust
// dialog with answers yardr does not know is told of at once, on the
// first pass that finds it idle, once per idle spell (yardr-tzkk): the
// kind, the installed harness's version, and the dialog's lines. Kimi
// 2.2.0 renamed its answers and the drift showed only as a plain
// session_idle (yardr-k9ew).
func TestHarnessDialogIsReportedAtTheFirstIdlePass(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "kimi", nil)
	v := &dialogRunner{agentRunner: r, version: "kimi-code 2.9.9"}
	d.Runners["fake"] = v
	ctx := context.Background()
	setPending(t, s, b, "read the brief", "no screen, not typed")
	told := func() []store.Event { t.Helper(); return eventKinds(t, s, b.ID, "session_harness_dialog") }

	r.show("idle", kimiDialogUnknown)
	must(t, d.Reconcile(ctx))
	evs := told()
	if len(evs) != 1 {
		t.Fatalf("session_harness_dialog events = %d, want 1 on the first idle pass", len(evs))
	}
	got, _ := s.GetBead(ctx, b.ID)
	data := evs[0].Data
	wantDialog := strings.TrimPrefix(kimiDialogUnknown, " ────────────────────────────────────────────\n")
	hint, _ := data["hint"].(string)
	if data["kind"] != "kimi" || data["version"] != "kimi-code 2.9.9" || data["session"] != *got.Session ||
		data["group"] != "g" || data["stage"] != "new" || data["dialog"] != wantDialog ||
		data["tail"] != kimiDialogUnknown || data["prompt_look"] != "no screen, not typed" ||
		!strings.Contains(hint, "harnesses/kimi.yaml") || !strings.Contains(hint, "Trust this folder") ||
		!strings.Contains(hint, "Trust and continue") {
		t.Fatalf("session_harness_dialog data = %v", data)
	}
	if n := len(eventKinds(t, s, b.ID, "session_harness_error")); n != 0 {
		t.Fatalf("session_harness_error events = %d, want none for a dialog", n)
	}
	if h := handleOfBead(t, s, b); h[dispatch.HarnessDialogKey] != "kimi" {
		t.Fatalf("handle = %v; want the dialog marked", h)
	}

	// Once per spell, and a daemon that replaced itself tells nobody again:
	// the mark is on the session's handle.
	must(t, d.Reconcile(ctx))
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": v}, Backoff: time.Hour}
	must(t, d2.Reconcile(ctx))
	must(t, d2.Reconcile(ctx))
	if n := len(told()); n != 1 {
		t.Fatalf("session_harness_dialog events = %d, want 1 per idle spell", n)
	}

	// Someone answers the dialog and the agent gets going; when it sits at
	// it again, that is told.
	r.show("working", "• Working")
	must(t, d2.Reconcile(ctx))
	if sess, _ := s.GetSession(ctx, *got.Session); sess.Handle[dispatch.HarnessDialogKey] != nil {
		t.Fatalf("handle = %v; the mark outlived the idle spell", sess.Handle)
	}
	r.show("idle", kimiDialogUnknown)
	must(t, d2.Reconcile(ctx))
	if n := len(told()); n != 2 {
		t.Fatalf("session_harness_dialog events = %d, want a second one in the next spell", n)
	}
}

// What is not an unknown dialog at a start raises nothing: the dialog with
// an answer the file names, an unknown dialog past the start, a kind whose
// file names no dialog, and an agent that only idles. A harness's error
// still raises as an error, not as a dialog.
func TestHarnessDialogOnlyForAnUnknownDialogAtAStart(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		kind    string
		tail    string
		pending bool
		error   bool // a session_harness_error instead
	}{
		"an answer the file names": {kind: "kimi", tail: kimiDialogKnown, pending: true},
		"past the start":           {kind: "kimi", tail: kimiDialogUnknown},
		"a kind with no dialog":    {kind: "grok", tail: kimiDialogUnknown, pending: true},
		"only idle":                {kind: "kimi", tail: "• Done. The tests passed; I wait for the reviewer's answer.", pending: true},
		"an error, not a dialog":   {kind: "codex", tail: atCapacity + "\n" + kimiDialogUnknown, pending: true, error: true},
	} {
		t.Run(name, func(t *testing.T) {
			s, r, d, b := harnessSetup(t, tc.kind, nil)
			d.Runners["fake"] = &dialogRunner{agentRunner: r, version: "9.9"}
			if tc.pending {
				setPending(t, s, b, "read the brief", "no screen, not typed")
			}
			r.show("idle", tc.tail)
			must(t, d.Reconcile(context.Background()))
			must(t, d.Reconcile(context.Background()))
			evs := eventKinds(t, s, b.ID, "session_harness_dialog")
			if len(evs) != 0 {
				t.Fatalf("session_harness_dialog = %v, want none", evs[0].Data)
			}
			errs := eventKinds(t, s, b.ID, "session_harness_error")
			if tc.error && (len(errs) != 1 || errs[0].Data["reason"] != dispatch.ReasonAtCapacity) {
				t.Fatalf("session_harness_error = %v, want the one at capacity", errs)
			}
			if !tc.error && len(errs) != 0 {
				t.Fatalf("session_harness_error = %v, want none", errs[0].Data)
			}
		})
	}
}

// A crew session that sits idle at its start at its harness's folder-trust
// dialog with answers yardr does not know is told of the way a bead's
// session is (yardr-5v7m): on its journal, once per idle spell. The crew
// pending branch runs before any harness look and crew sessions have no
// idle limit, so without this nothing is ever told.
func TestCrewHarnessDialogIsReported(t *testing.T) {
	t.Parallel()
	s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
	ctx := context.Background()
	f.sent = true
	must(t, d.Reconcile(ctx)) // start with the first prompt pending
	agent := agentOf(t, s)
	f.noScreen = true // at the dialog: idle, no screen
	f.show(kimiDialogUnknown)
	must(t, d.Reconcile(ctx))
	r, _ := s.GetCrew(ctx, "yardmaster")
	evs, _ := s.Events(ctx, r.Journal, 100)
	var told []store.Event
	for _, e := range evs {
		if e.Kind == "session_harness_dialog" {
			told = append(told, e)
		}
	}
	if len(told) != 1 {
		t.Fatalf("session_harness_dialog events = %d, want 1 on the first idle pass", len(told))
	}
	data := told[0].Data
	wantDialog := strings.TrimPrefix(kimiDialogUnknown, " ────────────────────────────────────────────\n")
	hint, _ := data["hint"].(string)
	if data["crew"] != "yardmaster" || data["kind"] != "kimi" || data["dialog"] != wantDialog ||
		data["tail"] != kimiDialogUnknown || !strings.Contains(hint, "harnesses/kimi.yaml") {
		t.Fatalf("session_harness_dialog data = %v", data)
	}
	if len(f.trustKeys) != 0 {
		t.Fatalf("trust keys = %v, want none typed into a dialog yardr does not know", f.trustKeys)
	}
	if r.Handle[dispatch.HarnessDialogKey] != "kimi" {
		t.Fatalf("handle = %v; want the dialog marked", r.Handle)
	}

	// Once per spell.
	must(t, d.Reconcile(ctx))
	evs, _ = s.Events(ctx, r.Journal, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "session_harness_dialog" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("session_harness_dialog events = %d, want 1 per idle spell", n)
	}
	// The dialog's mark kept what the pass stored: the screen wait is told
	// once, not on every pass.
	if n, _ := kindCount(t, s, r.Journal, "session_harness_starting"); n != 1 {
		t.Fatalf("session_harness_starting events = %d, want 1", n)
	}

	// Someone answers the dialog and the agent gets going; when it sits at
	// it again, that is told.
	f.set(agent, "working")
	must(t, d.Reconcile(ctx))
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.Handle[dispatch.HarnessDialogKey] != nil {
		t.Fatalf("handle = %v; the mark outlived the idle spell", r.Handle)
	}
	f.set(agent, "idle")
	must(t, d.Reconcile(ctx))
	evs, _ = s.Events(ctx, r.Journal, 100)
	n = 0
	for _, e := range evs {
		if e.Kind == "session_harness_dialog" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("session_harness_dialog events = %d, want a second one in the next spell", n)
	}
}

// A crew session at a dialog with an answer the file names is not told of
// an unknown one, and one past its start is not either.
func TestCrewHarnessDialogOnlyForAnUnknownDialogAtAStart(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		tail    string
		pending bool
	}{
		"an answer the file names": {tail: kimiDialogKnown, pending: true},
		"past the start":           {tail: kimiDialogUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, d := crewSetup(t, store.CrewMember{Config: map[string]any{"kind": "kimi"}})
			ctx := context.Background()
			f.sent = tc.pending
			must(t, d.Reconcile(ctx))
			f.noScreen = true
			f.show(tc.tail)
			must(t, d.Reconcile(ctx))
			must(t, d.Reconcile(ctx))
			r, _ := s.GetCrew(ctx, "yardmaster")
			evs, _ := s.Events(ctx, r.Journal, 100)
			for _, e := range evs {
				if e.Kind == "session_harness_dialog" {
					t.Fatalf("session_harness_dialog = %v, want none", e.Data)
				}
			}
		})
	}
}

// noVersionRunner is an agentRunner whose agent is at a dialog and whose
// harness version is nothing known.
type noVersionRunner struct{ *agentRunner }

func (n *noVersionRunner) Nudge(context.Context, map[string]any, string) error {
	return dispatch.ErrAgentBlocked
}

// A runner that cannot say the harness's version still tells of the
// dialog, without one.
func TestHarnessDialogWithoutAVersion(t *testing.T) {
	t.Parallel()
	s, r, d, b := harnessSetup(t, "kimi", nil)
	d.Runners["fake"] = &noVersionRunner{r}
	setPending(t, s, b, "read the brief", "no screen, not typed")
	r.show("idle", kimiDialogUnknown)
	must(t, d.Reconcile(context.Background()))
	evs := eventKinds(t, s, b.ID, "session_harness_dialog")
	if len(evs) != 1 || evs[0].Data["kind"] != "kimi" || evs[0].Data["version"] != nil {
		t.Fatalf("session_harness_dialog = %v, want one with the kind and no version", evs)
	}
}

// HarnessDialog is the dialog's lines, from its mark to the pane's end,
// where the mark shows and none of the kind's answers does.
func TestHarnessDialog(t *testing.T) {
	t.Parallel()
	claudeUnknown := "Quick safety check: Is this a project you created or one you trust?\n ❯ Absolutely\n   No, exit\n"
	for _, c := range []struct {
		kind string
		tail string
		want string
		ok   bool
	}{
		{"kimi", kimiDialogUnknown, strings.TrimPrefix(kimiDialogUnknown, " ────────────────────────────────────────────\n"), true},
		{"kimi", kimiDialogKnown, "", false},
		{"kimi", "Trust this folder?\n", "Trust this folder?", true},
		{"claude", claudeUnknown, "Quick safety check: Is this a project you created or one you trust?\n ❯ Absolutely\n   No, exit", true},
		{"grok", kimiDialogUnknown, "", false},
		{"cursor", kimiDialogUnknown, "", false},
		{"kimi", "", "", false},
	} {
		if dialog, ok := dispatch.HarnessDialog(c.kind, c.tail); dialog != c.want || ok != c.ok {
			t.Errorf("HarnessDialog(%s, %q) = %q, %v; want %q, %v", c.kind, c.tail, dialog, ok, c.want, c.ok)
		}
	}
}
