package runner

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
)

// A harness may ask whether a folder is trusted the first time it is
// started in it. Nobody watches an agent yardr starts, and the operator
// already said the folder is theirs by making it a depot or a crew
// member's directory, so yardr answers the dialog of a kind whose harness
// file names it (`trust`: the text that marks it, the answer that accepts
// it), and no other question. Answering the dialog leaves the harness's
// own trust record for the harness to write.
//
// Claude Code has no flag, setting or environment variable that skips the
// dialog in an interactive session (2.1.288). The other way is to write
// projects.<path>.hasTrustDialogAccepted into ~/.claude.json, an
// undocumented file that every running Claude session rewrites under a lock
// of its own; a second writer would race them.

// trustWait bounds the wait for an agent to be ready after its trust dialog
// was answered. Start runs inside the dispatcher's pass, so it is short; an
// agent that takes longer keeps its prompt pending. A variable so tests can
// shorten it.
var trustWait = 20 * time.Second

// trustRedrawWait bounds waiting for Claude Code to repaint the dialog after
// a cursor key. A terminal redraw can be delayed under load; sending another
// key against the old screen could move the cursor back to "No, exit". A
// variable so a test whose dialog does redraw can wait for it however busy
// the machine is.
var trustRedrawWait = 2 * time.Second

// trustEntry is the folder-trust dialog yardr answers for an agent with
// this config: the one its kind's harness file names, and none when the
// file names none or the group or crew member says `trust=false`.
func trustEntry(cfg map[string]any) *agents.Trust {
	kind, _ := agents.Lookup(agents.Kind(cfg))
	if kind.Trust == nil || cfg["trust"] == false {
		return nil
	}
	return kind.Trust
}

// trustAnswer finds t on a pane's screen: found when the dialog is up,
// which takes its mark and its accepting answer both, selected when the
// dialog's cursor is on the answer.
func trustAnswer(screen string, t agents.Trust) (found, selected bool) {
	dialog, answer := false, false
	for _, l := range strings.Split(screen, "\n") {
		l = strings.TrimSpace(l)
		dialog = dialog || strings.HasSuffix(l, t.Dialog)
		if !answer && strings.HasSuffix(l, t.Answer) {
			answer, selected = true, strings.HasPrefix(l, "❯")
		}
	}
	return dialog && answer, selected
}

// acceptTrust answers the folder-trust dialog t in pane, if that is what
// the pane shows, and reports whether it did. The dialog may open with its
// cursor off the accepting answer, so the cursor is moved and Enter is only
// sent once the screen shows it on the answer: a dialog that changed shape
// is left for a human, as any other question is.
func (h *Herdr) acceptTrust(ctx context.Context, pane string, t agents.Trust) bool {
	screen, err := h.readPane(ctx, pane, 0)
	for moves := 0; err == nil; moves++ {
		found, selected := trustAnswer(screen, t)
		if !found || (!selected && moves == 4) {
			return false
		}
		if selected {
			return h.sendKey(ctx, pane, "enter") == nil
		}
		if err := h.sendKey(ctx, pane, "down"); err != nil {
			return false
		}
		// Found live: the dialog takes a moment to redraw, and a second
		// `down` sent meanwhile wraps the cursor back to "No, exit". So the
		// next key waits for the screen to change.
		before := screen
		for deadline := time.Now().Add(trustRedrawWait); screen == before && err == nil; {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(trustWait / 200)
			screen, err = h.readPane(ctx, pane, 0)
		}
	}
	return false
}

// ready waits, up to trustWait, for the agent to take a prompt.
func (h *Herdr) ready(ctx context.Context, handle map[string]any) bool {
	for deadline := time.Now().Add(trustWait); ; {
		if s, err := h.Status(ctx, handle); err == nil && (s == "idle" || s == "done") {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(trustWait / 40)
	}
}

// sendKey presses key in pane. Found live: send-keys prints nothing when it
// worked, which run would take for a herdr that does not answer, so only
// the exit status is read.
func (h *Herdr) sendKey(ctx context.Context, pane, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The pane's text changed, or may have: the pass's snapshot of it no
	// longer stands, whether the key worked or not.
	defer dropPane(ctx, pane)
	if err := h.followSetting(ctx); err != nil {
		return err
	}
	if out, err := herdrCommand(ctx, h.Bin, "pane", "send-keys", pane, key).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane send-keys: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// dropPane forgets the pass's snapshot of pane (see dispatch.PaneCache):
// keys were typed into it. A context without a cache is nothing to forget.
func dropPane(ctx context.Context, pane string) {
	if c := dispatch.PaneCacheFrom(ctx); c != nil {
		c.Drop(pane)
	}
}

// How yardr reads a pane, said here and nowhere else (paneReadArgs builds
// every `herdr pane read`).
//
// herdr (0.9.0, "Alternate-screen history reads") answers a text read of
// `recent` or `recent-unwrapped` that asks for more lines than the screen
// has, of an idle agent that runs on the alternate screen (Claude Code in
// fullscreen, OpenCode), by scrolling the agent's transcript with its mouse
// wheel and back: the pane moves under whoever looks at it. Reads of
// `visible` and `detection`, and every read with --format ansi, are passive:
// they move nothing. So every read yardr makes by itself is a styled one,
// and a reader of plain text gets it with the SGR escapes stripped
// (paneView). The one read that fetches history is readHistory, for a
// command a person runs, and never in a pass of the daemon.
//
// A passive read of `recent-unwrapped` gives what herdr holds without
// asking the agent: the screen and herdr's scrollback above it. A harness
// on the normal screen has its transcript there, up to the lines asked for.
// One on the alternate screen has no scrollback, and the read is its
// screen: read live, the styled `recent-unwrapped --lines 200` of a
// fullscreen Claude Code and its `--source visible --format ansi` were the
// same 58 rows, byte for byte. A check must not count on more than the
// screen (yardr-dsmt).
type paneForm int

const (
	paneScreen  paneForm = iota // the displayed screen alone: passive
	paneRecent                  // the screen and herdr's scrollback above it, its last lines: passive
	paneHistory                 // as paneRecent, and herdr may scroll the agent's transcript to fetch more
)

// paneReadArgs is the `herdr pane read` of pane in form, lines of it for
// the forms that count lines.
func paneReadArgs(pane string, form paneForm, lines int) []string {
	args := []string{"pane", "read", pane}
	switch form {
	case paneScreen:
		return append(args, "--source", "visible", "--format", "ansi")
	case paneRecent:
		return append(args, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines), "--format", "ansi")
	}
	return append(args, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
}

// readPane is the text of pane's terminal, read passively: its last lines
// lines, or its screen when lines is 0. pane read prints the text itself,
// not a JSON result.
func (h *Herdr) readPane(ctx context.Context, pane string, lines int) (string, error) {
	return h.paneRead(ctx, pane, lines, false)
}

// readPaneStyled is readPane with the terminal's styling left in, as SGR
// escapes: what tells a harness's dimmed placeholder from typed text (see
// inputText).
func (h *Herdr) readPaneStyled(ctx context.Context, pane string, lines int) (string, error) {
	return h.paneRead(ctx, pane, lines, true)
}

// readHistory is the plain text of pane's last lines lines, history
// included: the one read for which herdr may scroll an idle agent's
// transcript, so only what a person runs calls it (see Herdr.History). In a
// pass of the daemon it is the passive read all the same.
func (h *Herdr) readHistory(ctx context.Context, pane string, lines int) (string, error) {
	if dispatch.PaneCacheFrom(ctx) != nil {
		return h.readPane(ctx, pane, lines)
	}
	return h.herdrPaneRead(ctx, pane, paneHistory, lines)
}

func (h *Herdr) paneRead(ctx context.Context, pane string, lines int, styled bool) (string, error) {
	if lines <= 0 {
		snap, err := h.herdrPaneRead(ctx, pane, paneScreen, 0)
		if err != nil || styled {
			return snap, err
		}
		return stripSGR(snap), nil
	}
	if c := dispatch.PaneCacheFrom(ctx); c != nil {
		return h.cachedPaneRead(ctx, c, pane, lines, styled)
	}
	snap, err := h.herdrPaneRead(ctx, pane, paneRecent, lines)
	if err != nil {
		return "", err
	}
	return paneView(snap, lines, styled), nil
}

// cachedPaneRead is paneRead through the pass's snapshot cache: the first
// check of the pass to read pane takes one styled snapshot, and every later
// check reads from it. The snapshot covers every check of a pass, so one
// read serves them whatever order they come in.
func (h *Herdr) cachedPaneRead(ctx context.Context, c *dispatch.PaneCache, pane string, lines int, styled bool) (string, error) {
	want := max(lines, promptScan)
	if snap, ok := c.Lookup(pane, want); ok {
		return paneView(snap, lines, styled), nil
	}
	snap, err := h.herdrPaneRead(ctx, pane, paneRecent, want)
	if err != nil {
		return "", err
	}
	c.Store(pane, snap, want)
	return paneView(snap, lines, styled), nil
}

// paneView is what a read of the pane's last lines lines, styled or plain,
// sees in snap, its styled snapshot: the plain text is the styled one
// without its SGR escapes, and a shorter read is its end.
func paneView(snap string, lines int, styled bool) string {
	if !styled {
		snap = stripSGR(snap)
	}
	return snapLines(snap, lines)
}

// snapLines is the last n lines of a snapshot: what herdr's --lines n shows
// of the pane. A snapshot with fewer lines is read whole.
func snapLines(snap string, n int) string {
	lines := strings.Split(snap, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // the empty tail a trailing newline leaves is no line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// stripSGR is a styled snapshot without its SGR escapes: the plain text the
// snapshot holds. Only SGR (ESC [ ... m) is taken off; every other byte is
// kept as herdr printed it.
func stripSGR(styled string) string {
	i := strings.IndexByte(styled, 0x1b)
	if i < 0 {
		return styled
	}
	var b strings.Builder
	b.Grow(len(styled))
	b.WriteString(styled[:i])
	rest := styled[i:]
	for len(rest) > 0 {
		if len(rest) >= 2 && rest[0] == 0x1b && rest[1] == '[' {
			j := 2
			for j < len(rest) && (rest[j] < 0x40 || rest[j] > 0x7e) {
				j++
			}
			if j < len(rest) && rest[j] == 'm' {
				rest = rest[j+1:]
				continue
			}
		}
		b.WriteByte(rest[0])
		rest = rest[1:]
	}
	return b.String()
}

func (h *Herdr) herdrPaneRead(ctx context.Context, pane string, form paneForm, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := h.followSetting(ctx); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := herdrCommand(ctx, h.Bin, paneReadArgs(pane, form, lines)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr pane read: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
