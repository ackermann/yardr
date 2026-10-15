package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

func (h *Herdr) Alive(ctx context.Context, handle map[string]any) (bool, error) {
	status, err := h.Status(ctx, handle)
	return status != "", err
}

// Status returns herdr's view of the agent (idle, working, blocked, done,
// unknown), or "" when the agent is gone. An error means herdr is unreachable.
// An agent herdr calls working whose harness stopped on an error is idle
// (see stopped).
func (h *Herdr) Status(ctx context.Context, handle map[string]any) (string, error) {
	name, _ := handle["agent"].(string)
	if name == "" {
		return "", nil
	}
	raw, err := h.run(ctx, "agent", "get", name)
	var herr *herdrError
	switch {
	case err == nil:
		var info struct {
			Agent struct {
				Status   string `json:"agent_status"`
				Kind     string `json:"agent"`
				NoScreen bool   `json:"screen_detection_skipped"`
			} `json:"agent"`
		}
		_ = json.Unmarshal(raw, &info)
		if info.Agent.Status == "" {
			return "unknown", nil
		}
		if h.stopped(ctx, handle, info.Agent.Kind, info.Agent.Status == "working" && info.Agent.NoScreen) {
			return "idle", nil
		}
		return info.Agent.Status, nil
	case errors.As(err, &herr) && herr.Code == "agent_not_found":
		return "", nil
	default:
		return "", err // herdr unreachable: unknown, not dead
	}
}

// paneStop is the end of a pane that shows an error of its harness, and
// when it was first seen as it is.
type paneStop struct {
	tail  string
	since time.Time
}

const (
	// stopLines is how much of a pane's end is read for the error its
	// harness stopped on: what dispatch.HarnessError looks at.
	stopLines = 20
	// stopStill is how long the end of a pane has to stand unchanged, with
	// an error of its harness in it, before an agent herdr calls working is
	// taken for stopped. A harness at work redraws its screen (a spinner,
	// the seconds a turn has taken, what a tool prints); the one that
	// stopped draws nothing more. It is shorter than the two minutes after
	// which the dispatcher looks at what a prompt of its own did, so an
	// agent that did not take one is idle again by then.
	stopStill = time.Minute
)

// stopped reports whether the agent of handle, which herdr calls working,
// stopped on an error of its harness and waits for input. looks is whether
// that can be at all: herdr says working and has its word from the
// harness's hooks alone (`screen_detection_skipped`), not from the screen.
//
// Such a harness tells herdr when a turn ends, and one that ends on an error
// may tell nothing: herdr's hooks for Kimi (integration v7) hear Stop and
// Interrupt, and Kimi ends a turn its provider refused with StopFailure. So
// a Kimi builder that hit its 5-hour limit mid-work stayed "working" to
// herdr for three and a half hours, and the dispatcher, which reads the
// pane of an idle agent only, raised neither session_harness_error nor
// session_idle (yardr-re0h). The pane says it: the error line is at its end
// (kind's `errors`), and nothing is drawn after it. A line alone is not
// enough, since an agent at work may print one (a test's source, a log), so
// the pane's end has to stand as it is for stopStill; what is remembered of
// it is this process's, and a daemon that replaced itself counts again.
func (h *Herdr) stopped(ctx context.Context, handle map[string]any, kind string, looks bool) bool {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return false
	}
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	if !looks {
		delete(h.stops, pane)
		return false
	}
	// The kind the session was started as names its harness file; herdr's
	// word for the agent is the same for the kinds it knows.
	if k, _ := handle["kind"].(string); k != "" {
		kind = k
	}
	tail, err := h.Tail(ctx, handle, stopLines)
	if err != nil {
		return false // a pane that cannot be read says nothing either way
	}
	if _, _, found := dispatch.HarnessError(kind, tail); !found {
		delete(h.stops, pane)
		return false
	}
	now := time.Now()
	if was, ok := h.stops[pane]; ok && was.tail == tail {
		return now.Sub(was.since) >= stopStill
	}
	if h.stops == nil {
		h.stops = map[string]paneStop{}
	}
	h.stops[pane] = paneStop{tail: tail, since: now}
	return false
}

func (h *Herdr) Stop(ctx context.Context, handle map[string]any) error {
	if pane, _ := handle["pane"].(string); pane != "" {
		h.dialogMu.Lock()
		delete(h.dismissedDialogs, pane)
		h.dialogMu.Unlock()
		h.stopMu.Lock()
		delete(h.stops, pane)
		h.stopMu.Unlock()
	}
	tab, _ := handle["tab"].(string)
	if tab == "" {
		return nil
	}
	_, err := h.run(ctx, "tab", "close", tab)
	var herr *herdrError
	if errors.As(err, &herr) && strings.Contains(herr.Code, "not_found") {
		return nil
	}
	return err
}

// Finish removes the bead's worktree once the bead is closed. It never
// forces: uncommitted or untracked changes keep the worktree in place, and
// the branch is always kept, so committed work survives either way. A dir
// depot's bead has no worktree (see finishDir).
func (h *Herdr) Finish(ctx context.Context, handle map[string]any) error {
	if dir, _ := handle["dir"].(string); dir != "" {
		return h.finishDir(ctx, handle)
	}
	path, _ := handle["worktree"].(string)
	repo, _ := handle["repo"].(string)
	if path == "" {
		return dispatch.ErrNothingLeft
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = git(ctx, repo, "worktree", "prune")
		return dispatch.ErrNothingLeft
	}
	// Briefs live in the worktree's git dir, so they do not count as a
	// change (nor does a pre-yardr .yard/, which ignores itself); anything
	// else does. `worktree remove` deletes the git dir, briefs with it.
	if out, err := gitOut(ctx, path, "status", "--porcelain"); err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	} else if out != "" {
		return fmt.Errorf("uncommitted changes in %s:\n%s", path, out)
	}
	if ws, _ := handle["workspace"].(string); ws != "" && h.workspaceAt(ctx, ws, path) {
		// Close the workspace first so herdr does not keep a pane in a
		// directory that is about to disappear. Ids can be reused after a
		// herdr restart, hence the path check above.
		_, _ = h.run(ctx, "workspace", "close", ws)
	}
	if repo == "" {
		repo = path
	}
	if err := git(ctx, repo, "worktree", "remove", path); err != nil {
		return err
	}
	return nil
}

// finishDir cleans up after a closed bead of a dir depot: its session's
// brief is removed and the bead's workspace closed, if still open. The
// directory itself is the depot's and is left as it is; there is no worktree
// to keep or remove.
func (h *Herdr) finishDir(ctx context.Context, handle map[string]any) error {
	brief, _ := handle["brief"].(string)
	removed := false
	if brief != "" {
		switch err := os.Remove(brief); {
		case err == nil:
			removed = true
		case !os.IsNotExist(err):
			return err
		}
	}
	dir, _ := handle["dir"].(string)
	depot, _ := handle["depot"].(string)
	bead, _ := handle["bead"].(string)
	if bead != "" {
		// Found as a later stage would find it, so a workspace id herdr
		// handed out again since is never closed.
		if id, err := h.findDirWorkspace(ctx, &store.Depot{Name: depot, Path: dir, Kind: store.DepotDir}, bead); err == nil && id != "" {
			_, _ = h.run(ctx, "workspace", "close", id)
		}
	}
	if !removed {
		return dispatch.ErrNothingLeft
	}
	return nil
}

// Tail is the last lines the session's agent printed in its pane, blank
// lines left out: where an agent says why it does nothing (a model it may
// not use, no login). The read is passive (see paneForm): of an agent on
// the alternate screen it is the end of its screen, which may be fewer
// lines than were asked for, and what scrolled off it is not fetched.
func (h *Herdr) Tail(ctx context.Context, handle map[string]any, lines int) (string, error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", fmt.Errorf("handle has no pane")
	}
	// A TUI pads its screen with blank lines, so read well more than is kept.
	text, err := h.readPane(ctx, pane, 4*lines+40)
	if err != nil {
		return "", err
	}
	return cutTail(text, lines), nil
}

// History is Tail with the agent's history fetched: herdr scrolls the
// transcript of an idle agent on the alternate screen to collect it, and
// back, so the pane moves under whoever looks at it. Only a command a
// person runs asks for it (`yardr start`'s wait for the yardmaster, when
// the pane's screen no longer shows the first prompt), never the daemon.
func (h *Herdr) History(ctx context.Context, handle map[string]any, lines int) (string, error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", fmt.Errorf("handle has no pane")
	}
	text, err := h.readHistory(ctx, pane, 4*lines+40)
	if err != nil {
		return "", err
	}
	return cutTail(text, lines), nil
}

// cutTail is the last n lines of a pane's text that say something, cut to
// what an event has room for.
func cutTail(text string, lines int) string {
	tail := lastLines(text, lines)
	// An event is no place for a screenful of one long line.
	const maxTail = 4000
	if len(tail) > maxTail {
		tail = "…" + strings.ToValidUTF8(tail[len(tail)-maxTail:], "")
	}
	return tail
}

// lastLines is the last n lines of text that say something: blank lines and
// trailing space are dropped, since a TUI's screen is mostly both.
func lastLines(text string, n int) string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Worked reports whether the session changed anything in its worktree: a
// commit since it started (its handle keeps the head it started on) or an
// uncommitted change. A dir depot's bead has no checkout to ask; there only
// the bead's events tell.
func (h *Herdr) Worked(ctx context.Context, handle map[string]any) (bool, error) {
	path, _ := handle["worktree"].(string)
	if path == "" {
		return false, nil
	}
	if start, _ := handle["head"].(string); start != "" {
		head, err := gitOut(ctx, path, "rev-parse", "HEAD")
		if err != nil {
			return false, err
		}
		if head != start {
			return true, nil
		}
	}
	out, err := gitOut(ctx, path, "status", "--porcelain")
	return out != "", err
}

// Nudge sends text to the session's agent as its next prompt, and waits as
// the first prompt does for herdr to see the agent start on it (see
// prompt). An agent at a dialog before is typed nothing. For one at a
// dialog after, only a readable pane with no prompt echo says it took
// nothing (dispatch.ErrAgentBlocked); an echo or an uncertain pane keeps
// the send guarded. Text the agent did not start on is said to be so
// (dispatch.ErrPromptStalled): it was typed, and the caller looks for it.
func (h *Herdr) Nudge(ctx context.Context, handle map[string]any, text string) error {
	name, _ := handle["agent"].(string)
	if name == "" {
		return fmt.Errorf("handle has no agent")
	}
	pane, _ := handle["pane"].(string)
	switch took, err := h.prompt(ctx, name, pane, text); {
	case err != nil:
		return err
	case took == tookRefused:
		return dispatch.ErrAgentBlocked
	case took == tookBlocked:
		// Herdr reports the agent at a dialog after the send, but the prompt
		// may already have reached it. Only a readable pane that rules out
		// the prompt's echo says nothing was taken. Keep the send guarded if
		// the pane shows it or cannot settle the question.
		if h.arrived(ctx, handle, text) == sawAbsent {
			return dispatch.ErrAgentBlocked
		}
		// A shown or typed echo, the text in a pane with no input drawn
		// (a question may have replaced it), and any uncertain look keep
		// the delivery counted so it cannot reopen trust acceptance.
		return nil
	case took == tookStalled:
		return dispatch.ErrPromptStalled
	}
	return nil
}

// NudgeDirect submits text to the session's agent as its next prompt,
// without waiting for the agent to start on it: herdr submits it itself.
// It is the delivery for a pane that shows no prompt of its harness (see
// tellNotes): nothing there can be typed, and there is no input to look
// for the text in after. An agent at a dialog is submitted nothing
// (dispatch.ErrAgentBlocked), as with a nudge.
func (h *Herdr) NudgeDirect(ctx context.Context, handle map[string]any, text string) error {
	name, _ := handle["agent"].(string)
	if name == "" {
		return fmt.Errorf("handle has no agent")
	}
	pane, _ := handle["pane"].(string)
	_, err := h.run(ctx, "agent", "prompt", name, text)
	var herr *herdrError
	if errors.As(err, &herr) && herr.Code == "agent_blocked" {
		return dispatch.ErrAgentBlocked
	}
	if err == nil {
		dropPane(ctx, pane)
	}
	return err
}

func (h *Herdr) yard() string {
	if h.YardBin != "" {
		return h.YardBin
	}
	return "yardr"
}
