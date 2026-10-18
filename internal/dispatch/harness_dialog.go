package dispatch

import (
	"context"
	"maps"
	"strings"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// A session whose agent sits at its harness's folder-trust dialog with
// answers yardr does not know: the harness renamed them, and the kind's
// file still names the old ones. The runner answers only the dialog the
// file names, so it types nothing, and the session sits idle from its
// start: Kimi 2.2.0 renamed 'Trust this folder' to 'Trust and continue'
// and the drift showed only as a plain session_idle, found by the
// yardmaster reading the pane (yardr-k9ew). The end of an idle agent's
// pane is read at the start of each idle spell anyway (harness.go), so a
// known dialog with none of its known answers raises
// session_harness_dialog at once, with the kind, the installed harness's
// version, and the dialog's lines. Only a session with its first prompt
// still pending is told of: the dialog stands only at a start, and an
// agent's own words about trust later are its own.

// HarnessDialogKey is a handle key: the kind of the session_harness_dialog
// raised in the session's current idle spell. It is kept on the handle, so
// a daemon that replaces itself does not tell the yardmaster again, and it
// is taken off when the agent is seen doing something.
const HarnessDialogKey = "harness_dialog"

// Versioner is implemented by runners that can say which version of kind's
// harness is installed (the herdr runner, as the kind's file says to ask):
// what a session_harness_dialog names beside the dialog. "" says nothing
// is known.
type Versioner interface {
	HarnessVersion(ctx context.Context, kind string) string
}

// HarnessDialog finds, in the end of a pane, the folder-trust dialog a
// harness of kind drew with none of the answers its file names: the
// dialog's lines, from its mark to the pane's end. ok is false for a kind
// whose file names no dialog, and where one of its answers shows.
func HarnessDialog(kind, tail string) (dialog string, ok bool) {
	h, _ := agents.Lookup(kind)
	if h.Trust == nil {
		return "", false
	}
	t := h.Trust
	lines := strings.Split(tail, "\n")
	mark, answer := -1, false
	for i, l := range lines {
		if mark < 0 && t.ShowsDialog(l) {
			mark = i
		}
		if !answer {
			if a, _ := t.ShowsAnswer(l); a != "" {
				answer = true
			}
		}
	}
	if mark < 0 || answer {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[mark:], "\n")), true
}

// harnessDialog raises session_harness_dialog for the idle session s when
// tail, the end of its pane, shows its harness's folder-trust dialog with
// none of the answers its file names, once per idle spell: the harness
// renamed its answers and yardr types nothing. Only a session with its
// first prompt still pending is told of. s.Handle is updated with the mark
// it stores.
func (d *Dispatcher) harnessDialog(ctx context.Context, r Runner, s *store.Session, g store.Group, tail string) {
	if s.Handle[HarnessDialogKey] != nil || s.Handle[store.PendingPrompt] == nil {
		return
	}
	kind := harnessKind(g.Config)
	dialog, ok := HarnessDialog(kind, tail)
	if !ok {
		return
	}
	version := ""
	if v, ok := r.(Versioner); ok {
		version = v.HarnessVersion(ctx, kind)
	}
	d.Log.Warn("session's harness drew a dialog yardr does not know", "session", s.ID, "bead", s.Bead,
		"group", s.Group, "kind", kind, "version", version)
	data := map[string]any{"session": s.ID, "group": s.Group, "stage": s.Stage, "kind": kind,
		"dialog": dialog, "tail": tail, "hint": dialogHint(kind)}
	if version != "" {
		data["version"] = version
	}
	if look := s.Handle[PromptLook]; look != nil {
		data[PromptLook] = look
	}
	if err := d.Store.LogDispatch(ctx, s.Bead, "session_harness_dialog", actor, data); err != nil {
		d.Log.Warn("session_harness_dialog event", "session", s.ID, "err", err)
	}
	d.markHarnessDialog(ctx, s, kind)
}

// dialogHint is the hint of a session_harness_dialog for kind: what the
// session sits at, and that the kind's file wants the dialog's answers.
func dialogHint(kind string) string {
	known := ""
	if h, _ := agents.Lookup(kind); h.Trust != nil {
		known = " (this yardr answers: '" + strings.Join(h.Trust.Answers, "', '") + "')"
	}
	return "the agent's harness sits idle at its start at its folder-trust dialog, with answers this yardr " +
		"does not know; yardr typed nothing. The dialog is in dialog, its pane's last lines in tail. " +
		"The harness renamed its answers since harnesses/" + kind + ".yaml was written" + known +
		": add what the dialog shows to its trust.answers. Answer it in its pane meanwhile; " +
		"the session carries on once its prompt arrives"
}

// markHarnessDialog keeps kind on s's handle: the session_harness_dialog
// raised in the session's current idle spell.
func (d *Dispatcher) markHarnessDialog(ctx context.Context, s *store.Session, kind string) {
	h := maps.Clone(s.Handle)
	if h == nil {
		h = map[string]any{}
	}
	h[HarnessDialogKey] = kind
	if err := d.Store.SetHandle(ctx, s.ID, h); err != nil {
		d.Log.Warn("mark session's harness dialog", "session", s.ID, "err", err)
		return
	}
	s.Handle = h
}

// crewHarnessDialog raises session_harness_dialog on the crew member's
// journal for the idle crew session s when the end of its pane shows its
// harness's folder-trust dialog with none of the answers its file names,
// once per idle spell: the harness renamed its answers and yardr types
// nothing. Only a session with its first prompt still pending is told of.
// The handle is updated with the mark it stores.
func (d *Dispatcher) crewHarnessDialog(ctx context.Context, r Runner, s *memberSession) {
	if s.st.Handle[HarnessDialogKey] != nil || s.st.Handle[store.PendingPrompt] == nil {
		return
	}
	t, ok := r.(Tailer)
	if !ok {
		return
	}
	tail, err := t.Tail(ctx, s.st.Handle, stallTail)
	if err != nil {
		return // a pane that cannot be read says nothing either way
	}
	kind := harnessKind(s.member.Config)
	dialog, ok := HarnessDialog(kind, tail)
	if !ok {
		return
	}
	version := ""
	if v, ok := r.(Versioner); ok {
		version = v.HarnessVersion(ctx, kind)
	}
	d.Log.Warn("crew session's harness drew a dialog yardr does not know", "crew", s.member.Name,
		"session", s.label(), "kind", kind, "version", version)
	data := s.who(map[string]any{"kind": kind, "dialog": dialog, "tail": tail, "hint": dialogHint(kind)})
	if version != "" {
		data["version"] = version
	}
	if look := s.st.Handle[PromptLook]; look != nil {
		data[PromptLook] = look
	}
	if err := d.Store.LogDispatch(ctx, s.member.Journal, "session_harness_dialog", actor, data); err != nil {
		d.Log.Warn("session_harness_dialog event", "crew", s.member.Name, "session", s.label(), "err", err)
	}
	h := maps.Clone(s.st.Handle)
	if h == nil {
		h = map[string]any{}
	}
	h[HarnessDialogKey] = kind
	d.setCrewHandle(ctx, s, h)
}

// crewHarnessDialogOver ends the spell a session_harness_dialog was raised
// in: the session's agent is doing something again, so its next dialog is
// told again.
func (d *Dispatcher) crewHarnessDialogOver(ctx context.Context, s *memberSession) {
	if s.st.Handle[HarnessDialogKey] == nil {
		return
	}
	h := maps.Clone(s.st.Handle)
	delete(h, HarnessDialogKey)
	d.setCrewHandle(ctx, s, h)
}
