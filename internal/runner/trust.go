package runner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Claude Code asks whether a folder is trusted the first time it is started
// in a repository (a linked worktree counts as its repository, so one answer
// covers every bead of a rig) or in a plain directory. Nobody watches an
// agent yardr starts, and the operator already said the folder is theirs by
// making it a rig or a crew member's directory, so yardr answers the dialog.
//
// Claude Code has no flag, setting or environment variable that skips the
// dialog in an interactive session (2.1.288). The other way is to write
// projects.<path>.hasTrustDialogAccepted into ~/.claude.json, an
// undocumented file that every running Claude session rewrites under a lock
// of its own; a second writer would race them. Answering the dialog leaves
// the write to Claude Code.

// trustYes is the dialog's accepting answer, as Claude Code prints it.
const trustYes = "Yes, I trust this folder"

// trustWait bounds the wait for an agent to be ready after its trust dialog
// was answered. Start runs inside the dispatcher's pass, so it is short; an
// agent that takes longer keeps its prompt pending. A variable so tests can
// shorten it.
var trustWait = 20 * time.Second

// trustRedrawWait bounds waiting for Claude Code to repaint the dialog after
// a cursor key. A terminal redraw can be delayed under load; sending another
// key against the old screen could move the cursor back to "No, exit".
const trustRedrawWait = 2 * time.Second

// trusts reports whether yardr answers the folder-trust dialog for an agent
// with this config: only Claude Code's, whose dialog it knows, and not when
// the group or crew member says `trust=false`.
func trusts(cfg map[string]any) bool {
	return str(cfg, "kind", "claude") == "claude" && cfg["trust"] != false
}

// trustAnswer finds the accepting answer on a pane's screen: found when the
// trust dialog is up, selected when the dialog's cursor is on it.
func trustAnswer(screen string) (found, selected bool) {
	for _, l := range strings.Split(screen, "\n") {
		if l = strings.TrimSpace(l); strings.HasSuffix(l, trustYes) {
			return true, strings.HasPrefix(l, "❯")
		}
	}
	return false, false
}

// acceptTrust answers the folder-trust dialog in pane, if that is what the
// pane shows, and reports whether it did. The dialog opens with its cursor
// on "No, exit", so the cursor is moved and Enter is only sent once the
// screen shows it on the accepting answer: a dialog that changed shape is
// left for a human, as any other question is.
func (h *Herdr) acceptTrust(ctx context.Context, pane string) bool {
	screen, err := h.readPane(ctx, pane, "visible", 0)
	for moves := 0; err == nil; moves++ {
		found, selected := trustAnswer(screen)
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
			screen, err = h.readPane(ctx, pane, "visible", 0)
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
	if out, err := exec.CommandContext(ctx, h.Bin, "pane", "send-keys", pane, key).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane send-keys: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// readPane is the text of pane's terminal: herdr's snapshot `source`, the
// last `lines` of it when lines is positive. pane read prints the text
// itself, not a JSON result.
func (h *Herdr) readPane(ctx context.Context, pane, source string, lines int) (string, error) {
	return h.paneRead(ctx, pane, source, lines)
}

// readPaneStyled is readPane with the terminal's styling left in, as SGR
// escapes: what tells a harness's dimmed placeholder from typed text (see
// inputText).
func (h *Herdr) readPaneStyled(ctx context.Context, pane, source string, lines int) (string, error) {
	return h.paneRead(ctx, pane, source, lines, "--format", "ansi")
}

func (h *Herdr) paneRead(ctx context.Context, pane, source string, lines int, more ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"pane", "read", pane, "--source", source}
	if lines > 0 {
		args = append(args, "--lines", fmt.Sprint(lines))
	}
	args = append(args, more...)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, h.Bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr pane read: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
