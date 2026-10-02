package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
)

// Herdr runs each bead in its own git worktree (branch yard/<bead>) opened as
// a herdr workspace, with one tab per stage and an agent started in it.
//
// Group config:
//
//	kind       agent kind herdr knows (claude, codex, ...); default claude
//	prompt     prompt file, relative to the rig or absolute
//	args    extra agent arguments (list or space-separated string)
//	model      agent model (passed as --model); a bead's metadata.model
//	           overrides it for that bead
//	keep       true to leave the tab open after the session ends
//	worktrees  root for bead worktrees (default Herdr.WorktreeRoot)
//
// Worktrees live at <root>/<rig>/<bead>. Agents such as Claude Code ask to
// trust every new folder but inherit trust from a parent, so trust the root
// once instead of answering a prompt per bead.
type Herdr struct {
	Bin          string
	WorktreeRoot string
	YardHome     string // passed to residents, so their yard commands reach this yard
	// YardBin is this yard's executable. Agents are told to use it by path:
	// an agent's shell may find an older yard first on its PATH.
	YardBin string
	Socket  string // API socket; empty means SocketPath's default
	// OnError reports event-stream failures (herdr down, reconnecting).
	OnError func(error)
}

type herdrResult struct {
	Workspace struct {
		ID string `json:"workspace_id"`
	} `json:"workspace"`
	Tab struct {
		ID string `json:"tab_id"`
	} `json:"tab"`
	RootPane struct {
		ID string `json:"pane_id"`
	} `json:"root_pane"`
	Worktree struct {
		Path string `json:"path"`
	} `json:"worktree"`
	AlreadyOpen bool `json:"already_open"`
}

type herdrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *herdrError) Error() string { return e.Code + ": " + e.Message }

func (h *Herdr) run(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, h.Bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *herdrError     `json:"error"`
	}
	// herdr prints results on stdout and error objects on stderr.
	for _, out := range [][]byte{stdout.Bytes(), stderr.Bytes()} {
		if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		if runErr == nil && resp.Result != nil {
			return resp.Result, nil
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("herdr %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), runErr,
			strings.TrimSpace(stderr.String()+" "+stdout.String()))
	}
	return nil, fmt.Errorf("herdr %s: unparseable output: %s", strings.Join(args[:min(2, len(args))], " "), stdout.String())
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// AgentName derives a herdr agent name ([a-z][a-z0-9_-]{0,31}) from the bead
// and stage, so `herdr agent list` reads like the yard.
func AgentName(bead, stage string) string {
	n := nameUnsafe.ReplaceAllString(strings.ToLower(bead+"-"+stage), "-")
	n = strings.Trim(n, "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "y" + n
	}
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	return n
}

// WorkspaceLabel is the herdr label for a group's work on name (a bead or a
// stage): the group first, then name.
func WorkspaceLabel(group, name string) string {
	if group == "" {
		return name
	}
	return group + " " + name
}

func (h *Herdr) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	if job.Rig == nil {
		return nil, fmt.Errorf("herdr runner needs the bead to have a rig")
	}
	rig := job.Rig
	kind := str(job.Group.Config, "kind", "claude")
	branch := "yard/" + job.Bead.ID
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	handle := map[string]any{"agent": name, "branch": branch, "repo": rig.Path}
	// herdr shows the workspace label, so lead with who is working on the
	// bead: "yard-builders yard-27" reads better than the bare bead id.
	label := WorkspaceLabel(job.Group.Name, job.Bead.ID)

	// Best effort: the base ref should be current before branching from it.
	if remote, _, ok := strings.Cut(rig.Base, "/"); ok {
		_ = exec.CommandContext(ctx, "git", "-C", rig.Path, "fetch", "--quiet", remote).Run()
	}

	// First stage creates the worktree; later stages reopen it.
	root := str(job.Group.Config, "worktrees", h.WorktreeRoot)
	createArgs := []string{"worktree", "create", "--cwd", rig.Path, "--branch", branch,
		"--base", rig.Base, "--label", label}
	if root != "" {
		createArgs = append(createArgs, "--path", filepath.Join(root, rig.Name, job.Bead.ID))
	}
	raw, err := h.run(ctx, createArgs...)
	reopened := false
	if err != nil {
		var herr *herdrError
		if !errors.As(err, &herr) {
			return nil, err
		}
		createErr := err
		raw, err = h.run(ctx, "worktree", "open", "--cwd", rig.Path, "--branch", branch, "--label", label)
		if err != nil && root != "" {
			// The branch exists but its worktree was cleaned up (a reopened
			// bead): check the branch out again and open that.
			path := filepath.Join(root, rig.Name, job.Bead.ID)
			if gerr := git(ctx, rig.Path, "worktree", "add", path, branch); gerr != nil {
				return nil, fmt.Errorf("worktree for %s: create: %v; open: %v; re-add: %w", branch, createErr, err, gerr)
			}
			raw, err = h.run(ctx, "worktree", "open", "--cwd", rig.Path, "--path", path, "--label", label)
		}
		if err != nil {
			return nil, fmt.Errorf("worktree for %s: create: %v; open: %w", branch, createErr, err)
		}
		reopened = true
	}
	var wt herdrResult
	if err := json.Unmarshal(raw, &wt); err != nil {
		return nil, fmt.Errorf("worktree result: %w", err)
	}
	if reopened {
		// An open workspace keeps the label of the stage that opened it, so
		// relabel it for this stage's group. Best effort: a label is cosmetic.
		_, _ = h.run(ctx, "workspace", "rename", wt.Workspace.ID, label)
	}
	handle["workspace"], handle["worktree"] = wt.Workspace.ID, wt.Worktree.Path

	tab, pane := wt.Tab.ID, wt.RootPane.ID
	if wt.AlreadyOpen {
		// The root pane may still hold the previous stage's agent.
		raw, err := h.run(ctx, "tab", "create", "--workspace", wt.Workspace.ID,
			"--cwd", wt.Worktree.Path, "--label", WorkspaceLabel(job.Group.Name, job.Bead.Stage))
		if err != nil {
			return handle, err
		}
		var t herdrResult
		if err := json.Unmarshal(raw, &t); err != nil {
			return handle, fmt.Errorf("tab result: %w", err)
		}
		tab, pane = t.Tab.ID, t.RootPane.ID
	}
	handle["tab"], handle["pane"] = tab, pane

	brief, err := Brief(job, name, wt.Worktree.Path)
	if err != nil {
		return handle, err
	}
	// The brief goes inside the worktree: agents may read their working
	// directory without asking. .yard/ ignores itself so it is never committed.
	briefDir := filepath.Join(wt.Worktree.Path, ".yard")
	if err := os.MkdirAll(briefDir, 0o700); err != nil {
		return handle, err
	}
	if err := os.WriteFile(filepath.Join(briefDir, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		return handle, err
	}
	briefPath := filepath.Join(briefDir, "brief-"+job.Session.ID+".md")
	if err := os.WriteFile(briefPath, []byte(brief), 0o600); err != nil {
		return handle, err
	}
	handle["brief"] = briefPath

	args := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if extra := AgentArgs(job); len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	rel, _ := filepath.Rel(wt.Worktree.Path, briefPath)
	prompt := fmt.Sprintf("Read %s and follow it. Start with `export YARD_ACTOR=%s`.", rel, name)
	if h.YardBin != "" {
		prompt += fmt.Sprintf(" Run yard as %s.", h.YardBin)
	}
	return h.startAgent(ctx, args, name, prompt, handle)
}

// startAgent starts the agent and gives it its first prompt. An agent that
// is not ready yet (blocked on a trust or permission question a human must
// answer, or just slow) is not a failure: the session is kept and the
// prompt is left on the handle as pending, for the dispatcher to deliver
// once the agent is idle.
func (h *Herdr) startAgent(ctx context.Context, args []string, name, prompt string, handle map[string]any) (map[string]any, error) {
	var err error
	// A pane created a moment ago may not have its shell prompt up yet.
	for attempt := 0; ; attempt++ {
		_, err = h.run(ctx, args...)
		var herr *herdrError
		if err == nil || attempt == 20 || !errors.As(err, &herr) || herr.Code != "agent_pane_busy" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	var herr *herdrError
	if errors.As(err, &herr) && herr.Code == "agent_not_ready" {
		handle[dispatch.PendingPrompt] = prompt
		return handle, nil
	}
	if err != nil {
		return handle, err
	}
	if _, err := h.run(ctx, "agent", "prompt", name, prompt); err != nil {
		return handle, err
	}
	return handle, nil
}

func (h *Herdr) Alive(ctx context.Context, handle map[string]any) (bool, error) {
	status, err := h.Status(ctx, handle)
	return status != "", err
}

// Status returns herdr's view of the agent (idle, working, blocked, done,
// unknown), or "" when the agent is gone. An error means herdr is unreachable.
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
				Status string `json:"agent_status"`
			} `json:"agent"`
		}
		_ = json.Unmarshal(raw, &info)
		if info.Agent.Status == "" {
			return "unknown", nil
		}
		return info.Agent.Status, nil
	case errors.As(err, &herr) && herr.Code == "agent_not_found":
		return "", nil
	default:
		return "", err // herdr unreachable: unknown, not dead
	}
}

func (h *Herdr) Stop(ctx context.Context, handle map[string]any) error {
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
// the branch is always kept, so committed work survives either way.
func (h *Herdr) Finish(ctx context.Context, handle map[string]any) error {
	path, _ := handle["worktree"].(string)
	repo, _ := handle["repo"].(string)
	if path == "" {
		return dispatch.ErrNothingLeft
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_ = git(ctx, repo, "worktree", "prune")
		return dispatch.ErrNothingLeft
	}
	// yard's own scratch (.yard/brief-*.md) is ignored, so it does not count
	// as a change; anything else does.
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

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// workspaceAt reports whether herdr workspace ws is the checkout at path.
func (h *Herdr) workspaceAt(ctx context.Context, ws, path string) bool {
	raw, err := h.run(ctx, "workspace", "get", ws)
	if err != nil {
		return false
	}
	var r struct {
		Workspace struct {
			Worktree *struct {
				CheckoutPath string `json:"checkout_path"`
			} `json:"worktree"`
		} `json:"workspace"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Workspace.Worktree == nil {
		return false
	}
	return filepath.Clean(r.Workspace.Worktree.CheckoutPath) == filepath.Clean(path)
}

// AgentArgs are the agent's own arguments: the group's args, then --model
// from the bead's metadata.model, else the group's config.model. A model
// already in args is replaced, so a bead can override a group default.
func AgentArgs(job dispatch.Job) []string {
	model, _ := job.Bead.Metadata["model"].(string)
	return agentArgs(job.Group.Config, model)
}

func agentArgs(cfg map[string]any, override string) []string {
	args := strs(cfg, "args")
	model := str(cfg, "model", "")
	if override != "" {
		model = override
	}
	if model == "" {
		return args
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--model" && i+1 < len(args):
			i++ // drop the group's --model <x>
		case strings.HasPrefix(args[i], "--model="):
		default:
			out = append(out, args[i])
		}
	}
	return append(out, "--model", model)
}

// StartResident opens a herdr workspace in the resident's directory, with
// its identity in the environment, starts its agent and points it at its
// prime. The agent is named after the resident.
func (h *Herdr) StartResident(ctx context.Context, r store.Resident) (map[string]any, error) {
	dir := r.Dir
	if dir == "" {
		dir = h.YardHome
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("resident directory %q does not exist", dir)
	}
	name := AgentName(r.Name, "")
	args := []string{"workspace", "create", "--cwd", dir, "--label", r.Name, "--no-focus",
		"--env", "YARD_RESIDENT=" + r.Name, "--env", "YARD_ACTOR=" + r.Name}
	if h.YardHome != "" {
		args = append(args, "--env", "YARD_HOME="+h.YardHome)
	}
	raw, err := h.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("workspace for %s: %w", r.Name, err)
	}
	var ws herdrResult
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("workspace result: %w", err)
	}
	handle := map[string]any{"agent": name, "workspace": ws.Workspace.ID, "tab": ws.Tab.ID,
		"pane": ws.RootPane.ID, "dir": dir}

	start := []string{"agent", "start", name, "--kind", str(r.Config, "kind", "claude"), "--pane", ws.RootPane.ID}
	if extra := agentArgs(r.Config, ""); len(extra) > 0 {
		start = append(append(start, "--"), extra...)
	}
	prompt := fmt.Sprintf("You are the yard resident %q. Run `%s prime --resident %s` and follow it.",
		r.Name, h.yard(), r.Name)
	return h.startAgent(ctx, start, name, prompt, handle)
}

// Nudge sends text to the session's agent as its next prompt.
func (h *Herdr) Nudge(ctx context.Context, handle map[string]any, text string) error {
	name, _ := handle["agent"].(string)
	if name == "" {
		return fmt.Errorf("handle has no agent")
	}
	_, err := h.run(ctx, "agent", "prompt", name, text)
	return err
}

func (h *Herdr) yard() string {
	if h.YardBin != "" {
		return h.YardBin
	}
	return "yard"
}
