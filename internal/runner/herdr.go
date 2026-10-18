package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// Herdr runs each bead in its own git worktree (branch yard/<bead>) opened as
// a herdr workspace, with one tab per stage and an agent started in it. A
// dir depot's bead has no branch: its workspace is opened in the depot's
// directory itself (see startDir). A bead that tracks a pull request has the
// worktree and the branch, but its worktree stands on the pull request's
// head, detached, and the branch stays where the base is (see pullHead).
//
// Group config:
//
//	kind       agent kind herdr knows (claude, codex, ...); default claude
//	prompt     prompt file, relative to the depot or absolute
//	args    extra agent arguments (list or space-separated string)
//	model      agent model (passed as --model); a bead's metadata.model
//	           overrides it for that bead
//	keep       true to leave the tab open after the session ends
//	worktrees  root for bead worktrees (default Herdr.WorktreeRoot)
//	env_file   KEY=VALUE file (mode 0600 or stricter) whose entries go into
//	           the agent's pane environment: outbound credentials, which
//	           never belong in the store (see config.ReadEnvFile) nor on a
//	           command line (see call)
//
// A crew member's config has those but keep and worktrees, and one more:
//
//	env_command  a command that prints KEY=VALUE lines, run at each start,
//	             whose entries join the env_file's (see
//	             config.ReadCrewEnv)
//
//	trust      false to leave the folder-trust dialog for a human; by
//	           default yardr answers the one the kind's file names (see
//	           acceptTrust)
//
// Worktrees live at <root>/<depot>/<bead>.
type Herdr struct {
	dialogMu         sync.Mutex
	dismissedDialogs map[string]string
	// stops is, by pane, the end of the pane of an agent herdr calls working
	// while it shows an error its harness stops on, and since when (stopped).
	stopMu       sync.Mutex
	stops        map[string]paneStop
	Bin          string
	WorktreeRoot string
	YardHome     string // passed to crew members, so their yard commands reach this yard
	// YardBin is this yard's executable. Agents are told to use it by path:
	// an agent's shell may find an older yard first on its PATH.
	YardBin string
	Socket  string // API socket; empty means the yard's herdr session, see SocketPath
	// FetchTimeout overrides the package FetchTimeout (tests).
	FetchTimeout time.Duration
	// OnError reports event-stream failures (herdr down, reconnecting).
	OnError func(error)
	// Store, if set, records train_branched when Start creates a train's
	// branch, and is asked before every call to herdr which herdr session
	// the yard names (followSetting).
	Store store.API
	// Log, if set, gets the key names (never the values) an env_file loads.
	Log *slog.Logger
	// AgentHomes overrides, by harness kind, where the agents keep their
	// session files (tests); see Model.
	AgentHomes map[string]string
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

// outage reports whether the error means herdr itself is down or not ready
// to serve, as opposed to refusing this one request: the runtime's failure,
// so every bead of the group would fail the same way (see dispatch.Runtime).
func (e *herdrError) outage() bool {
	switch e.Code {
	case "server_not_running", // no server at the socket
		"server_unavailable": // one that is starting or shutting down
		return true
	}
	return false
}

// ServerUp asks herdr whether its server answers, by the call a crew member's
// start begins with (agent list). It returns nil when it does; down reports
// whether the error is herdr saying that no server runs at its socket
// (server_not_running), as opposed to anything else that kept it from
// answering. yardr doctor asks it: herdr on PATH is not a server that runs.
func (h *Herdr) ServerUp(ctx context.Context) (down bool, err error) {
	_, err = h.run(ctx, "agent", "list")
	var herr *herdrError
	return errors.As(err, &herr) && herr.Code == "server_not_running", err
}

func (h *Herdr) run(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := h.followSetting(ctx); err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd := herdrCommand(ctx, h.Bin, args...)
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
			if resp.Error.outage() {
				return nil, dispatch.Runtime(sessionDown(resp.Error))
			}
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

func (h *Herdr) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	if job.Depot == nil {
		return nil, fmt.Errorf("herdr runner needs the bead to have a depot")
	}
	depot := job.Depot
	if depot.IsDir() {
		return h.startDir(ctx, job)
	}
	branch := Branch(job.Bead)
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	handle := map[string]any{"agent": name, "branch": branch, "repo": depot.Path}
	label := WorkspaceLabel(depot.Name, job.Group, job.Train, job.Bead.ID)
	// Before anything is made: a group whose env_file is refused starts
	// nothing until it is fixed.
	secrets, err := h.envFile(job.Group.Config, "group "+job.Group.Name)
	if err != nil {
		return nil, err
	}

	h.fetchBase(ctx, job)
	if job.Train != "" {
		if err := h.trainBranch(ctx, job); err != nil {
			return nil, err
		}
	}

	// First stage creates the worktree; later stages reopen it.
	root := str(job.Group.Config, "worktrees", h.WorktreeRoot)
	createArgs := []string{"worktree", "create", "--cwd", depot.Path, "--branch", branch,
		"--base", BaseOf(job), "--label", label}
	if root != "" {
		createArgs = append(createArgs, "--path", filepath.Join(root, depot.Name, job.Bead.ID))
	}
	raw, err := h.run(ctx, createArgs...)
	reopened := false
	if err != nil {
		var herr *herdrError
		if dispatch.IsRuntime(err) || !errors.As(err, &herr) {
			// herdr down, or not answering: nothing to fall back to.
			return nil, err
		}
		createErr := err
		raw, err = h.run(ctx, "worktree", "open", "--cwd", depot.Path, "--branch", branch, "--label", label)
		if err != nil && root != "" {
			// The branch exists but its worktree was cleaned up (a reopened
			// bead): check the branch out again and open that.
			path := filepath.Join(root, depot.Name, job.Bead.ID)
			// The worktree of a bead that tracks a pull request is on no
			// branch (see pullHead), so herdr does not find it by one: where
			// it still is, it is opened by its path as it stands.
			if _, serr := os.Stat(path); job.Pull == nil || serr != nil {
				if gerr := git(ctx, depot.Path, "worktree", "add", path, branch); gerr != nil {
					return nil, fmt.Errorf("worktree for %s: create: %v; open: %v; re-add: %w", branch, createErr, err, gerr)
				}
			}
			raw, err = h.run(ctx, "worktree", "open", "--cwd", depot.Path, "--path", path, "--label", label)
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
		// An open workspace keeps the label it was opened with, which names
		// the previous stage's group and may predate the bead joining a
		// train (or this labelling), so relabel it. Best effort: a label is cosmetic.
		_, _ = h.run(ctx, "workspace", "rename", wt.Workspace.ID, label)
	}
	handle["workspace"], handle["worktree"] = wt.Workspace.ID, wt.Worktree.Path
	if job.Pull != nil {
		head, err := h.pullHead(ctx, wt.Worktree.Path, job.Pull.Number)
		if err != nil {
			// The handle goes back, so what was opened is torn down. No
			// agent starts: on the base it would review nothing.
			return handle, dispatch.BeadBlocked(fmt.Errorf("pull request %s: %w", job.Pull.URL, err))
		}
		// A copy: the job's is the dispatcher's.
		pull := *job.Pull
		pull.Head = head
		job.Pull = &pull
		handle["pull"], handle["pull_head"] = pull.URL, head
	}
	// Where the branch stood when the session began, so Worked can tell a
	// commit of this session's from the ones it started on.
	if head, err := gitOut(ctx, wt.Worktree.Path, "rev-parse", "HEAD"); err == nil {
		handle["head"] = head
	}

	// The agent gets a tab of its own, created with its identity in the
	// environment: herdr's agent start takes none, and the pane a worktree
	// opens with has none. So the agent's yardr calls are attributed to it
	// and carry its session (a stale one is refused) without the agent
	// having to export anything. On a later stage the root pane may also
	// still hold the previous stage's agent. The environment goes over the
	// socket, never on a command line (see call).
	raw, err = h.call(ctx, "tab.create", map[string]any{
		"workspace_id": wt.Workspace.ID, "cwd": wt.Worktree.Path,
		"label": TabLabel(depot.Name, job.Bead.Stage, job.Train, job.Bead.ID),
		"env":   envMap(withEnvFile(h.agentEnv(job, name), secrets)),
	})
	if err != nil {
		return handle, err
	}
	var t herdrResult
	if err := json.Unmarshal(raw, &t); err != nil {
		return handle, fmt.Errorf("tab result: %w", err)
	}
	tab, pane := t.Tab.ID, t.RootPane.ID
	handle["tab"], handle["pane"] = tab, pane
	h.shellOn(ctx, pane, handle)
	if err := h.shellKnown(handle, func(handle map[string]any) error {
		return h.Store.SetHandle(ctx, job.Session.ID, handle)
	}); err != nil {
		return handle, err
	}
	if !wt.AlreadyOpen && wt.Tab.ID != "" {
		// The workspace was opened for this stage, so its root tab is an
		// idle shell. Close it, so that Stop closing the agent's tab still
		// leaves no workspace behind. Best effort: it is only clutter.
		_, _ = h.run(ctx, "tab", "close", wt.Tab.ID)
	}

	dir, err := BriefDir(ctx, wt.Worktree.Path)
	if err != nil {
		return handle, fmt.Errorf("brief dir for %s: %w", wt.Worktree.Path, err)
	}
	if err := writeWorktreeSession(dir, job, name); err != nil {
		return handle, fmt.Errorf("session file for %s: %w", wt.Worktree.Path, err)
	}
	return h.brief(ctx, job, name, wt.Worktree.Path, dir, pane, handle)
}

// brief writes the job's brief into dir, then starts the agent in pane and
// points it at the brief.
func (h *Herdr) brief(ctx context.Context, job dispatch.Job, name, workdir, dir, pane string, handle map[string]any) (map[string]any, error) {
	brief, err := Brief(job, name, workdir, h.YardHome, h.Store)
	if err != nil {
		return handle, err
	}
	briefPath, err := writeBrief(dir, job.Session.ID, brief)
	if err != nil {
		return handle, err
	}
	handle["brief"] = briefPath

	kind := agents.Kind(job.Group.Config)
	handle[harnessKind] = kind
	args := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if extra := AgentArgs(job, h.YardHome, workdir); len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	return h.startAgent(ctx, args, name, h.FirstPrompt(handle), handle, trustEntry(job.Group.Config))
}

// FirstPrompt is what the agent of a bead's session is first told: where
// its brief is. The dispatcher gives it again to a session whose harness
// answered it with "at capacity" (dispatch.FirstPrompter). A handle with no
// brief is a crew member's, whose first prompt is another and is kept on
// the handle (see firstPrompt); "" is one from before it was.
func (h *Herdr) FirstPrompt(handle map[string]any) string {
	briefPath, _ := handle["brief"].(string)
	if briefPath == "" {
		prompt, _ := handle[firstPrompt].(string)
		return prompt
	}
	prompt := fmt.Sprintf("Read %s and follow it.", briefPath)
	if h.YardBin != "" {
		prompt += fmt.Sprintf(" Run yardr as %s.", h.YardBin)
	}
	return prompt
}

// startDir starts a bead of a dir depot. There is no branch and no worktree:
// the agent works in the depot's directory itself, in a workspace opened
// there for the bead (reused by its later stages), and its brief goes under
// the directory's .yardr/briefs/. A train's child is no different: a train
// in a dir depot only groups, so no train branch is made either.
func (h *Herdr) startDir(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	depot := job.Depot
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	if st, err := os.Stat(depot.Path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("depot %s: directory %q does not exist", depot.Name, depot.Path)
	}
	secrets, err := h.envFile(job.Group.Config, "group "+job.Group.Name)
	if err != nil {
		return nil, err
	}
	label := WorkspaceLabel(depot.Name, job.Group, job.Train, job.Bead.ID)
	ws, err := h.dirWorkspace(ctx, depot, job.Bead.ID, label)
	if err != nil {
		return nil, err
	}
	handle := map[string]any{"agent": name, "dir": depot.Path, "depot": depot.Name, "bead": job.Bead.ID,
		"workspace": ws.Workspace.ID}

	// As for a worktree: the environment goes over the socket, never on a
	// command line (see call).
	raw, err := h.call(ctx, "tab.create", map[string]any{
		"workspace_id": ws.Workspace.ID, "cwd": depot.Path,
		"label": TabLabel(depot.Name, job.Bead.Stage, job.Train, job.Bead.ID),
		"env":   envMap(withEnvFile(h.agentEnv(job, name), secrets)),
	})
	if err != nil {
		return handle, err
	}
	var t herdrResult
	if err := json.Unmarshal(raw, &t); err != nil {
		return handle, fmt.Errorf("tab result: %w", err)
	}
	pane := t.RootPane.ID
	handle["tab"], handle["pane"] = t.Tab.ID, pane
	h.shellOn(ctx, pane, handle)
	if err := h.shellKnown(handle, func(handle map[string]any) error {
		return h.Store.SetHandle(ctx, job.Session.ID, handle)
	}); err != nil {
		return handle, err
	}
	if !ws.AlreadyOpen && ws.Tab.ID != "" {
		// As for a worktree: the new workspace's root tab is an idle shell.
		_, _ = h.run(ctx, "tab", "close", ws.Tab.ID)
	}

	dir := DirBriefDir(depot.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return handle, err
	}
	// A dir depot may still be inside a checkout; its briefs are no change.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		return handle, err
	}
	return h.brief(ctx, job, name, depot.Path, dir, pane, handle)
}

// dirWorkspace opens the herdr workspace a dir depot's bead works in: the one
// an earlier stage opened, while it is still open, else a new one in the
// depot's directory. herdr reports no checkout for a plain directory, so the
// earlier one is found by its label, which ends in the bead's id, and a
// pane in it that sits in the depot's directory. A reused workspace is
// relabelled for the current group.
func (h *Herdr) dirWorkspace(ctx context.Context, depot *store.Depot, bead, label string) (herdrResult, error) {
	var ws herdrResult
	id, err := h.findDirWorkspace(ctx, depot, bead)
	if err != nil {
		return ws, err
	}
	if id != "" {
		_, _ = h.run(ctx, "workspace", "rename", id, label) // cosmetic, as for a worktree
		ws.Workspace.ID, ws.AlreadyOpen = id, true
		return ws, nil
	}
	raw, err := h.run(ctx, "workspace", "create", "--cwd", depot.Path, "--label", label, "--no-focus")
	if err != nil {
		return ws, fmt.Errorf("workspace for %s in %s: %w", bead, depot.Path, err)
	}
	if err := json.Unmarshal(raw, &ws); err != nil {
		return ws, fmt.Errorf("workspace result: %w", err)
	}
	return ws, nil
}

// findDirWorkspace is the id of the open workspace of bead in the dir depot,
// or "" when there is none (see dirWorkspace).
func (h *Herdr) findDirWorkspace(ctx context.Context, depot *store.Depot, bead string) (string, error) {
	raw, err := h.run(ctx, "workspace", "list")
	if err != nil {
		return "", err
	}
	var wl struct {
		Workspaces []struct {
			ID       string          `json:"workspace_id"`
			Label    string          `json:"label"`
			Worktree json.RawMessage `json:"worktree"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &wl); err != nil {
		return "", fmt.Errorf("workspace list: %w", err)
	}
	short := ShortID(depot.Name, bead)
	var ids []string
	for _, w := range wl.Workspaces {
		f := strings.Fields(w.Label)
		if len(w.Worktree) == 0 && len(f) > 0 && f[len(f)-1] == short {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	raw, err = h.run(ctx, "pane", "list")
	if err != nil {
		return "", err
	}
	var pl struct {
		Panes []struct {
			Workspace string `json:"workspace_id"`
			Cwd       string `json:"cwd"`
		} `json:"panes"`
	}
	if err := json.Unmarshal(raw, &pl); err != nil {
		return "", fmt.Errorf("pane list: %w", err)
	}
	for _, id := range ids {
		for _, p := range pl.Panes {
			if p.Workspace == id && sameDir(p.Cwd, depot.Path) {
				return id, nil
			}
		}
	}
	return "", nil
}

// sameDir reports whether a and b name the same directory, symlinks
// resolved where they can be.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return real(a) == real(b)
}

// FetchTimeout bounds the fetch of a depot's base before a bead branches
// from it. Start runs inside the dispatcher's pass, so a fetch hanging on a
// dead remote would stall every group.
const FetchTimeout = 60 * time.Second

// BaseDiverged is the event a start logs on its bead when the depot's base,
// a local branch, has diverged from its upstream: each holds commits the
// other lacks, so no fast-forward brings them together. The bead still
// starts, branching from the base as it is; joining the two is a person's,
// by hand. Of the class attention, so the yardmaster hears of it.
const BaseDiverged = "base_diverged"

// fetchBase brings the depot's base ref up to date, best effort: a fetch that
// fails or times out leaves the bead branching from the base as it is. A
// remote asking for credentials fails at once instead of waiting on a
// terminal nobody watches. When the base is a local branch with an
// upstream, the branch itself is fast-forwarded to what was fetched (see
// fetchLocalBase).
func (h *Herdr) fetchBase(ctx context.Context, job dispatch.Job) {
	depot := job.Depot
	remote, _, ok := strings.Cut(depot.Base, "/")
	if !ok {
		h.fetchLocalBase(ctx, depot, job.Bead.ID)
		return
	}
	_ = h.fetchRemote(ctx, depot.Path, remote, false)
}

// fetchRemote fetches remote into the repository at dir, giving up after
// the fetch's deadline (see FetchTimeout). Local bases prune stale
// remote-tracking refs so a deleted upstream is not treated as current.
func (h *Herdr) fetchRemote(ctx context.Context, dir, remote string, prune bool) error {
	timeout := h.FetchTimeout
	if timeout == 0 {
		timeout = FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-C", dir, "fetch", "--quiet"}
	if prune {
		args = append(args, "--prune")
	}
	args = append(args, remote)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	// git may leave a child (ssh, a credential helper) holding its output
	// open after it is killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

// fetchLocalBase brings the depot's base, a local branch, up to the
// upstream it tracks, best effort like fetchBase: a fetch that fails or
// times out leaves the bead branching from the base as it is. The branch
// is moved without a checkout, from the repository into itself, which
// refuses anything but a fast-forward. A base checked out in any worktree
// is left as it is: pulling into a working copy a person may use is the
// operator's. So is one holding commits its upstream lacks — a landing
// whose push failed, which the next landing's push carries — and one that
// diverged from its upstream, which is left with a base_diverged event on
// the bead. The bead starts in every case.
func (h *Herdr) fetchLocalBase(ctx context.Context, depot *store.Depot, bead string) {
	// The upstream the branch tracks, by its own configuration: the
	// remote's name is exact there, where @{upstream}'s "remote/branch"
	// form cannot be split back.
	remote, err := gitOut(ctx, depot.Path, "config", "branch."+depot.Base+".remote")
	if err != nil || remote == "" {
		return // no upstream: nothing to bring up to
	}
	if err := h.fetchRemote(ctx, depot.Path, remote, true); err != nil {
		return
	}
	local, err := gitOut(ctx, depot.Path, "rev-parse", "--verify", "-q", "refs/heads/"+depot.Base)
	if err != nil || local == "" {
		return
	}
	upstream, err := gitOut(ctx, depot.Path, "rev-parse", "--verify", "-q", depot.Base+"@{upstream}")
	if err != nil || upstream == "" {
		return
	}
	if local == upstream {
		return
	}
	if baseCheckedOut(ctx, depot.Path, depot.Base) {
		return
	}
	if err := git(ctx, depot.Path, "merge-base", "--is-ancestor", local, upstream); err == nil {
		_ = git(ctx, depot.Path, "fetch", "--quiet", ".", upstream+":refs/heads/"+depot.Base)
		return
	}
	if err := git(ctx, depot.Path, "merge-base", "--is-ancestor", upstream, local); err == nil {
		return // ahead of its upstream: simply not pushed yet
	}
	if h.Store == nil {
		return
	}
	upstreamName, _ := gitOut(ctx, depot.Path, "rev-parse", "--abbrev-ref", depot.Base+"@{upstream}")
	// Best effort: the branch is left and the bead starts whatever the
	// log says.
	_ = h.Store.LogDispatch(ctx, bead, BaseDiverged, "yard", map[string]any{
		"depot": depot.Name, "base": depot.Base, "upstream": upstreamName,
		"local": local, "remote": upstream})
}

// baseCheckedOut reports whether the branch base is checked out in any
// worktree of the repository at dir.
func baseCheckedOut(ctx context.Context, dir, base string) bool {
	out, err := gitOut(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "branch refs/heads/"+base {
			return true
		}
	}
	return false
}

// pullHead makes the worktree at path stand on the head of the depot
// origin's pull request n, detached, and returns that commit. It is run at
// every start of a session on a bead that tracks the pull request, so one
// started after the pull request was pushed to stands on the new head. The
// branch yard/<bead> is left where the base is: herdr makes the worktree by
// it, and nothing of the pull request is the yard's to land. The fetch and
// the checkout run none of the pull request's code: hooks and filters are
// the depot's own, not the tree's. Unlike fetchBase's, a failure here is the
// session's: the pull request closed and its ref gone, no network, or a
// worktree with changes the checkout would overwrite, which are left.
func (h *Herdr) pullHead(ctx context.Context, path string, n int) (string, error) {
	timeout := h.FetchTimeout
	if timeout == 0 {
		timeout = FetchTimeout
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Into the worktree's own FETCH_HEAD: no ref of the depot is made or moved.
	if err := git(fctx, path, "fetch", "--quiet", "origin", fmt.Sprintf("refs/pull/%d/head", n)); err != nil {
		return "", err
	}
	if err := git(ctx, path, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return gitOut(ctx, path, "rev-parse", "HEAD")
}

// trainBranch makes sure the branch of the job's train exists before a child
// branches from it: when it does not yet, it is created from the depot's base
// tip (just fetched, when the base is a remote's) and train_branched is
// logged on the train. An existing train branch is never recreated or moved:
// it holds the children that already landed.
func (h *Herdr) trainBranch(ctx context.Context, job dispatch.Job) error {
	depot, train := job.Depot, store.Branch(job.Train)
	if _, err := gitOut(ctx, depot.Path, "rev-parse", "--verify", "-q", "refs/heads/"+train); err == nil {
		return nil
	}
	// --no-track: the train branch lands on the base later as a unit; it is
	// not a local copy of the base. Without -f git refuses to replace a
	// branch made meanwhile.
	if err := git(ctx, depot.Path, "branch", "--no-track", train, depot.Base); err != nil {
		// Another child of the same train may have created the branch after
		// our check. Treat that race like the already-existed case, but do
		// not hide failures that left no branch behind.
		if _, verifyErr := gitOut(ctx, depot.Path, "rev-parse", "--verify", "-q", "refs/heads/"+train); verifyErr == nil {
			return nil
		}
		return fmt.Errorf("train branch %s from %s: %w", train, depot.Base, err)
	}
	commit, err := gitOut(ctx, depot.Path, "rev-parse", "refs/heads/"+train)
	if err != nil {
		return err
	}
	if h.Store != nil {
		// Best effort: the branch is made; the event only reports it.
		_ = h.Store.LogDispatch(ctx, job.Train, "train_branched", "yard", map[string]any{
			"branch": train, "from": depot.Base, "commit": commit, "child": job.Bead.ID})
	}
	return nil
}

// agentEnv is the environment of a bead agent's tab: what every started
// process gets (see Env), with the agent as actor, plus this yard's home and
// binary so its yardr commands reach this yard. A bead of a git depot gets
// what it lands on (YARDR_BASE); one of a dir depot, which has no base or
// branch, gets the directory it works in (YARDR_DEPOT, YARDR_DEPOT_PATH).
func (h *Herdr) agentEnv(job dispatch.Job, name string) []string {
	env := Env(job, name)
	if depot := job.Depot; depot != nil && depot.IsDir() {
		env = slices.Concat(env, config.Env("DEPOT", depot.Name), config.Env("DEPOT_PATH", depot.Path))
	} else if base := BaseOf(job); base != "" {
		env = append(env, config.Env("BASE", base)...)
	}
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	if h.YardBin != "" {
		env = append(env, config.Env("BIN", h.YardBin)...)
	}
	// The pane starts with herdr's environment, not the daemon's, so the
	// settings of the yard's server cannot be left out of it, only blanked;
	// set here, they are not the env file's to give either (withEnvFile).
	env = append(env, config.BlankServerSettings()...)
	// A harness can skip its folder-trust dialog with an environment entry
	// (Kimi 2.2.0). When trust is disabled, blank its entry so an inherited
	// value or env_file cannot bypass the operator's choice.
	return append(env, agents.TrustEnv(agents.Kind(job.Group.Config), job.Group.Config["trust"] != false)...)
}

// shellOn puts the pid of pane's shell on handle. The shell is what the
// agent, and every yardr command it runs, descends from: with its pid on
// the handle the store recognises the session's calls by the process tree,
// whatever their environment says (store.Expect). With it goes the herdr
// session the pane is in (store.HandleHerdrSession). A herdr without
// process-info leaves the handle without one, and shellKnown then starts
// no agent whose start a store records.
func (h *Herdr) shellOn(ctx context.Context, pane string, handle map[string]any) {
	pid := h.shellPID(ctx, pane)
	if pid <= 0 {
		return
	}
	handle["shell_pid"] = pid
	// The session the shell was found in is the pane's: the call went to
	// the socket this one would (followSetting), so the handle says where
	// the pane is, whatever the setting says later (store.herdrSessionFree).
	// A socket of no session herdr names leaves it out.
	if name := sessionForSocket(h.SocketPath()); name != "" {
		handle[store.HandleHerdrSession] = name
	}
	// Its start time tells it from a later process with its pid (see
	// store.Expect).
	if st, err := peer.Describe(pid); err == nil {
		handle["shell_start"] = st.Start
	}
}

// shellKnown has the store hold handle, through put, before an agent is
// started in the pane whose shell it names: the agent takes its first
// prompt seconds before its start is recorded, and a command it runs in
// them is known as its own by the shell alone (store.paneSession,
// store.paneCrew). So a store that does not take it starts no agent. A
// handle without a shell has nothing the store could go by.
func (h *Herdr) shellKnown(handle map[string]any, put func(map[string]any) error) error {
	if h.Store == nil {
		return nil
	}
	if handle["shell_pid"] == nil {
		return errors.New("herdr did not report the pane shell; refusing to start an agent without a recorded pane")
	}
	if err := put(handle); err != nil {
		return fmt.Errorf("recording the pane before its agent starts: %w", err)
	}
	return nil
}

// shellPID is the pid of the shell in pane, 0 when herdr does not tell.
func (h *Herdr) shellPID(ctx context.Context, pane string) int {
	raw, err := h.run(ctx, "pane", "process-info", "--pane", pane)
	if err != nil {
		return 0
	}
	var info struct {
		ProcessInfo struct {
			ShellPID int `json:"shell_pid"`
		} `json:"process_info"`
	}
	_ = json.Unmarshal(raw, &info)
	return info.ProcessInfo.ShellPID
}

// checkoutTimeout bounds every git call Start and Finish make on a depot (a
// worktree added or removed, a branch made, a status). Like the fetch they
// run inside the dispatcher's pass, so one hanging on a slow disk or a hook
// would stall every group. Two minutes is herdr's own bound on the same
// work; local operations normally take well under a second. A variable so
// tests can shorten it (depotfile.GitTimeout is the role reads').
var checkoutTimeout = 2 * time.Minute

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Never wait on a terminal nobody watches, and do not wait past the
	// deadline on a child (a hook, ssh) still holding git's output open.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	cmd.WaitDelay = time.Second
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

// AgentArgs are the agent's own arguments: the group's args, then the kind's
// model flag
// from the bead's metadata.model, else the group's config.model. A model
// already in args is replaced, so a bead can override a group default. An
// agent also gets what its kind's file says it always runs with (Codex's
// --no-daemon). SocketArg in an argument is the socket of the yard at home,
// WorkdirArg the directory the session works in.
func AgentArgs(job dispatch.Job, home, workdir string) []string {
	model, _ := job.Bead.Metadata["model"].(string)
	return agentArgs(job.Group.Config, model, home, workdir)
}

// SocketArg is where an agent's argument names the yard's socket: the
// runner puts the path there, for every kind. It is for a kind whose own
// sandbox denies unix sockets and can be told of one to let through (the
// `read only` presets of harnesses/claude.yaml and codex.yaml): every yardr
// command of the session goes through that socket, and its path is the
// yard's, which a harness file and a group's args cannot know. The path
// goes in as it is, inside whatever quotes the argument has around it.
const SocketArg = "{socket}"

// WorkdirArg is where an agent's argument names the session's workdir: the
// bead's worktree, or a crew member's directory. The runner puts the path
// there as it does the socket's. It is for a sandbox that is told what to
// deny by an absolute path: Claude Code's takes "." in its settings for
// something other than the directory the session runs in, and writes there
// go through.
const WorkdirArg = "{workdir}"

func agentArgs(cfg map[string]any, override, home, workdir string) []string {
	args := strs(cfg, "args")
	// What the kind's harness file says every agent of it runs with
	// (flags.always), and why.
	kind, _ := agents.Lookup(agents.Kind(cfg))
	for _, a := range kind.Flags.Always {
		if !slices.Contains(args, a) {
			args = append(args, a)
		}
	}
	// Each path with its symlinks resolved, which is the one a sandbox is
	// asked about whatever path the session goes by (macOS's /tmp is
	// /private/tmp). A runner with no home (a test's) has no socket to
	// name, and a placeholder with no path stays as it was written.
	real := func(path string) string {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			return r
		}
		return path
	}
	var paths []string
	if home != "" {
		paths = append(paths, SocketArg, config.At(real(home)).Socket())
	}
	if workdir != "" {
		paths = append(paths, WorkdirArg, real(workdir))
	}
	if len(paths) > 0 {
		args = slices.Clone(args) // the group's own slice is left as it is
		r := strings.NewReplacer(paths...)
		for i, a := range args {
			args[i] = r.Replace(a)
		}
	}
	model := str(cfg, "model", "")
	if override != "" {
		model = override
	}
	if model == "" {
		return args
	}
	modelFlag := kind.Flags.Model
	if modelFlag == "" {
		modelFlag = "--model" // keep the runner's existing form for unknown kinds
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == modelFlag && i+1 < len(args):
			i++ // drop the group's model flag and value
		case strings.HasPrefix(args[i], modelFlag+"="):
		default:
			out = append(out, args[i])
		}
	}
	return append(out, modelFlag, model)
}
