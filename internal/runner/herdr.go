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
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
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
	// FetchTimeout overrides the package FetchTimeout (tests).
	FetchTimeout time.Duration
	// OnError reports event-stream failures (herdr down, reconnecting).
	OnError func(error)
	// Store, if set, records epic_branched when Start creates an epic's
	// branch.
	Store store.API
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
	// No error object: herdr is missing, down or not answering. That is the
	// runtime's failure, not the bead's (see dispatch.RuntimeError).
	if runErr != nil {
		return nil, dispatch.Runtime(fmt.Errorf("herdr %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), runErr,
			strings.TrimSpace(stderr.String()+" "+stdout.String())))
	}
	return nil, dispatch.Runtime(fmt.Errorf("herdr %s: unparseable output: %s", strings.Join(args[:min(2, len(args))], " "), stdout.String()))
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Branch is the git branch a bead is worked on (see store.Branch).
func Branch(b store.Bead) string { return store.Branch(b.ID) }

// BaseOf is what the job's bead branches from and lands on: its epic's
// branch for a child of an epic, else the rig's base.
func BaseOf(job dispatch.Job) string {
	if job.Base != "" || job.Rig == nil {
		return job.Base
	}
	return job.Rig.Base
}

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
// stage): the group first, then name. herdr already nests workspaces under
// the rig's repo, so a leading "<rig>-" on the group is noise that only gets
// the label truncated in the sidebar. The bead or stage name stays unchanged.
func WorkspaceLabel(rig, group, name string) string {
	if rig != "" {
		group = strings.TrimPrefix(group, rig+"-")
	}
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
	branch := Branch(job.Bead)
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	handle := map[string]any{"agent": name, "branch": branch, "repo": rig.Path}
	// herdr shows the workspace label, so lead with who is working on the
	// bead: "builders 27" reads better than the bare bead id.
	label := WorkspaceLabel(rig.Name, job.Group.Name, job.Bead.ID)

	h.fetchBase(ctx, rig)
	if job.Epic != "" {
		if err := h.epicBranch(ctx, job); err != nil {
			return nil, err
		}
	}

	// First stage creates the worktree; later stages reopen it.
	root := str(job.Group.Config, "worktrees", h.WorktreeRoot)
	createArgs := []string{"worktree", "create", "--cwd", rig.Path, "--branch", branch,
		"--base", BaseOf(job), "--label", label}
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

	// The agent gets a tab of its own, created with its identity in the
	// environment: herdr's agent start takes none, and the pane a worktree
	// opens with has none. So the agent's yardr calls are attributed to it
	// and carry its session (a stale one is refused) without the agent
	// having to export anything. On a later stage the root pane may also
	// still hold the previous stage's agent.
	tabArgs := []string{"tab", "create", "--workspace", wt.Workspace.ID,
		"--cwd", wt.Worktree.Path, "--label", WorkspaceLabel(rig.Name, job.Group.Name, job.Bead.Stage)}
	for _, e := range h.agentEnv(job, name) {
		tabArgs = append(tabArgs, "--env", e)
	}
	raw, err = h.run(ctx, tabArgs...)
	if err != nil {
		return handle, err
	}
	var t herdrResult
	if err := json.Unmarshal(raw, &t); err != nil {
		return handle, fmt.Errorf("tab result: %w", err)
	}
	tab, pane := t.Tab.ID, t.RootPane.ID
	handle["tab"], handle["pane"] = tab, pane
	if !wt.AlreadyOpen && wt.Tab.ID != "" {
		// The workspace was opened for this stage, so its root tab is an
		// idle shell. Close it, so that Stop closing the agent's tab still
		// leaves no workspace behind. Best effort: it is only clutter.
		_, _ = h.run(ctx, "tab", "close", wt.Tab.ID)
	}

	brief, err := Brief(job, name, wt.Worktree.Path, h.YardHome)
	if err != nil {
		return handle, err
	}
	briefPath, err := writeBrief(ctx, wt.Worktree.Path, job.Session.ID, brief)
	if err != nil {
		return handle, err
	}
	handle["brief"] = briefPath

	args := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if extra := AgentArgs(job); len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	prompt := fmt.Sprintf("Read %s and follow it.", briefPath)
	if h.YardBin != "" {
		prompt += fmt.Sprintf(" Run yardr as %s.", h.YardBin)
	}
	return h.startAgent(ctx, args, name, prompt, handle)
}

// FetchTimeout bounds the fetch of a rig's base before a bead branches
// from it. Start runs inside the dispatcher's pass, so a fetch hanging on a
// dead remote would stall every group.
const FetchTimeout = 60 * time.Second

// fetchBase brings the rig's base ref up to date, best effort: a fetch that
// fails or times out leaves the bead branching from the base as it is. A
// remote asking for credentials fails at once instead of waiting on a
// terminal nobody watches.
func (h *Herdr) fetchBase(ctx context.Context, rig *store.Rig) {
	remote, _, ok := strings.Cut(rig.Base, "/")
	if !ok {
		return
	}
	timeout := h.FetchTimeout
	if timeout == 0 {
		timeout = FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", rig.Path, "fetch", "--quiet", remote)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	// git may leave a child (ssh, a credential helper) holding its output
	// open after it is killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
}

// epicBranch makes sure the branch of the job's epic exists before a child
// branches from it: when it does not yet, it is created from the rig's base
// tip (just fetched, when the base is a remote's) and epic_branched is
// logged on the epic. An existing epic branch is never recreated or moved:
// it holds the children that already landed.
func (h *Herdr) epicBranch(ctx context.Context, job dispatch.Job) error {
	rig, epic := job.Rig, store.Branch(job.Epic)
	if _, err := gitOut(ctx, rig.Path, "rev-parse", "--verify", "-q", "refs/heads/"+epic); err == nil {
		return nil
	}
	// --no-track: the epic branch lands on the base later as a unit; it is
	// not a local copy of the base. Without -f git refuses to replace a
	// branch made meanwhile.
	if err := git(ctx, rig.Path, "branch", "--no-track", epic, rig.Base); err != nil {
		// Another child of the same epic may have created the branch after
		// our check. Treat that race like the already-existed case, but do
		// not hide failures that left no branch behind.
		if _, verifyErr := gitOut(ctx, rig.Path, "rev-parse", "--verify", "-q", "refs/heads/"+epic); verifyErr == nil {
			return nil
		}
		return fmt.Errorf("epic branch %s from %s: %w", epic, rig.Base, err)
	}
	commit, err := gitOut(ctx, rig.Path, "rev-parse", "refs/heads/"+epic)
	if err != nil {
		return err
	}
	if h.Store != nil {
		// Best effort: the branch is made; the event only reports it.
		_ = h.Store.LogDispatch(ctx, job.Epic, "epic_branched", "yard", map[string]any{
			"branch": epic, "from": rig.Base, "commit": commit, "child": job.Bead.ID})
	}
	return nil
}

// agentEnv is the environment of a bead agent's tab: what every started
// process gets (see Env), with the agent as actor, plus this yard's home and
// binary so its yardr commands reach this yard.
func (h *Herdr) agentEnv(job dispatch.Job, name string) []string {
	env := Env(job, name)
	if base := BaseOf(job); base != "" {
		env = append(env, config.Env("BASE", base)...)
	}
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	if h.YardBin != "" {
		env = append(env, config.Env("BIN", h.YardBin)...)
	}
	return env
}

// BriefDir is where briefs for the worktree at path go: yardr/ in the
// worktree's own git directory (.git/worktrees/<name>/yardr for a linked
// worktree). That is outside the tracked tree, so a brief never shows up as
// a change, and .yardr/ in the tree is left for committed config.
func BriefDir(ctx context.Context, worktree string) (string, error) {
	return rigcheck.GitPath(ctx, worktree, "yardr")
}

func writeBrief(ctx context.Context, worktree, session, brief string) (string, error) {
	dir, err := BriefDir(ctx, worktree)
	if err != nil {
		return "", fmt.Errorf("brief dir for %s: %w", worktree, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "brief-"+session+".md")
	if err := os.WriteFile(path, []byte(brief), 0o600); err != nil {
		return "", err
	}
	return path, nil
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
	args := []string{"workspace", "create", "--cwd", dir, "--label", r.Name, "--no-focus"}
	env := slices.Concat(config.Env("RESIDENT", r.Name), config.Env("ACTOR", r.Name))
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	for _, e := range env {
		args = append(args, "--env", e)
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
	prompt := fmt.Sprintf("You are the yardr resident %q. Run `%s prime --resident %s` and follow it.",
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
	return "yardr"
}
