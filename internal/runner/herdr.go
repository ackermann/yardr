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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/otel"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
)

// Herdr runs each bead in its own git worktree (branch yard/<bead>) opened as
// a herdr workspace, with one tab per stage and an agent started in it. A
// dir depot's bead has no branch: its workspace is opened in the depot's
// directory itself (see startDir).
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
//	trust      false to leave Claude Code's folder-trust dialog for a human;
//	           by default yardr answers it (see acceptTrust)
//
// Worktrees live at <root>/<depot>/<bead>.
type Herdr struct {
	Bin          string
	WorktreeRoot string
	YardHome     string // passed to crew members, so their yard commands reach this yard
	// YardBin is this yard's executable. Agents are told to use it by path:
	// an agent's shell may find an older yard first on its PATH.
	YardBin string
	Socket  string // API socket; empty means SocketPath's default
	// FetchTimeout overrides the package FetchTimeout (tests).
	FetchTimeout time.Duration
	// OnError reports event-stream failures (herdr down, reconnecting).
	OnError func(error)
	// Store, if set, records train_branched when Start creates a train's
	// branch.
	Store store.API
	// Log, if set, gets the key names (never the values) an env_file loads.
	Log *slog.Logger
	// ClaudeDir and CodexDir override where the agents keep their session
	// files (tests); see Model.
	ClaudeDir, CodexDir string
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
			if resp.Error.outage() {
				return nil, dispatch.Runtime(resp.Error)
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

var nameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Branch is the git branch a bead is worked on (see store.Branch).
func Branch(b store.Bead) string { return store.Branch(b.ID) }

// BaseOf is what the job's bead branches from and lands on: its train's
// branch for a child of a train, the commit a linked yard named for a bead
// that came from it (store.Family), else the depot's base.
func BaseOf(job dispatch.Job) string {
	if job.Base != "" || job.Depot == nil {
		return job.Base
	}
	return job.Depot.Base
}

// AgentName derives a herdr agent name ([a-z][a-z0-9_-]{0,31}) from the bead
// and stage, so `herdr agent list` reads like the yard.
func AgentName(bead, stage string) string { return agentName(bead + "-" + stage) }

// CrewAgentName is the herdr agent name of a yard's crew member,
// "<yard>-<crew member>": herdr lists every yard on the machine's agents
// together and refuses a second of one name, so two yards' yardmasters must not
// both be "yardmaster". Without a yard name it is the crew member's.
func CrewAgentName(yard, member string) string { return agentName(yard + "-" + member) }

// CrewLabel is the herdr label of a crew member's workspace, "<yard>
// <crew member>", for the same reason.
func CrewLabel(yard, member string) string {
	return strings.TrimSpace(yard + " " + member)
}

// agentName is raw as a herdr agent name.
func agentName(raw string) string {
	n := nameUnsafe.ReplaceAllString(strings.ToLower(raw), "-")
	n = strings.Trim(n, "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "y" + n
	}
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	return n
}

// WorkspaceLabel is the herdr label for a bead's workspace: the group
// working on it first, then "[<train>] " for a child of a train, so the tab
// bar shows which train it belongs to, then the bead ("builders [sv6b] 2cet",
// or "builders jtyz" for a bead with no train). The workspace is reused
// across stages and relabelled on each reopen, so the group in it is always
// the current one.
func WorkspaceLabel(depot string, group store.Group, train, bead string) string {
	return joinLabel(GroupLabel(depot, group), depot, train, bead)
}

// TabLabel is the herdr label for a stage's tab: the workspace's label with
// the stage in place of the group ("review [sv6b] 2cet"). The group is not
// repeated, since the workspace above the tab names it.
func TabLabel(depot, stage, train, bead string) string {
	return joinLabel(stage, depot, train, bead)
}

// GroupLabel is the name a group goes by in herdr labels: its `label`
// config when set, else its name. herdr already nests workspaces under the
// depot's repo, so a leading "<depot>-" on the name is noise that only gets the
// label truncated in the sidebar.
func GroupLabel(depot string, group store.Group) string {
	if l := str(group.Config, "label", ""); l != "" {
		return l
	}
	return ShortID(depot, group.Name)
}

// ShortID drops the leading "<depot>-" from an id for a herdr label, where
// the depot is already the node above. An id without it stays whole; ids
// anywhere else in yardr stay full.
func ShortID(depot, id string) string {
	if depot == "" {
		return id
	}
	return strings.TrimPrefix(id, depot+"-")
}

// joinLabel is "<head> [<train>] <bead>", the parts that are empty left out
// and the ids shortened.
func joinLabel(head, depot, train, bead string) string {
	var parts []string
	if head != "" {
		parts = append(parts, head)
	}
	if train != "" {
		parts = append(parts, "["+ShortID(depot, train)+"]")
	}
	return strings.Join(append(parts, ShortID(depot, bead)), " ")
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

	h.fetchBase(ctx, depot)
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
			if gerr := git(ctx, depot.Path, "worktree", "add", path, branch); gerr != nil {
				return nil, fmt.Errorf("worktree for %s: create: %v; open: %v; re-add: %w", branch, createErr, err, gerr)
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
	brief, err := Brief(job, name, workdir, h.YardHome)
	if err != nil {
		return handle, err
	}
	briefPath, err := writeBrief(dir, job.Session.ID, brief)
	if err != nil {
		return handle, err
	}
	handle["brief"] = briefPath

	kind := str(job.Group.Config, "kind", "claude")
	handle[harnessKind] = kind
	args := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if extra := AgentArgs(job); len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	return h.startAgent(ctx, args, name, h.FirstPrompt(handle), handle, trusts(job.Group.Config))
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

// fetchBase brings the depot's base ref up to date, best effort: a fetch that
// fails or times out leaves the bead branching from the base as it is. A
// remote asking for credentials fails at once instead of waiting on a
// terminal nobody watches.
func (h *Herdr) fetchBase(ctx context.Context, depot *store.Depot) {
	remote, _, ok := strings.Cut(depot.Base, "/")
	if !ok {
		return
	}
	timeout := h.FetchTimeout
	if timeout == 0 {
		timeout = FetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", depot.Path, "fetch", "--quiet", remote)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	// git may leave a child (ssh, a credential helper) holding its output
	// open after it is killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	_ = cmd.Run()
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
	return append(env, config.BlankServerSettings()...)
}

// shellOn puts the pid of pane's shell on handle. The shell is what the
// agent, and every yardr command it runs, descends from: with its pid on
// the handle the store recognises the session's calls by the process tree,
// whatever their environment says (store.Expect). Best effort: a herdr
// without process-info leaves the session checked by its environment alone.
func (h *Herdr) shellOn(ctx context.Context, pane string, handle map[string]any) {
	pid := h.shellPID(ctx, pane)
	if pid <= 0 {
		return
	}
	handle["shell_pid"] = pid
	// Its start time tells it from a later process with its pid (see
	// store.Expect).
	if st, err := peer.Describe(pid); err == nil {
		handle["shell_start"] = st.Start
	}
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

// BriefDir is where briefs for the worktree at path go: yardr/ in the
// worktree's own git directory (.git/worktrees/<name>/yardr for a linked
// worktree). That is outside the tracked tree, so a brief never shows up as
// a change, and .yardr/ in the tree is left for committed config.
func BriefDir(ctx context.Context, worktree string) (string, error) {
	return rigcheck.GitPath(ctx, worktree, "yardr")
}

// DirBriefDir is where briefs for a bead of the dir depot at path go:
// .yardr/briefs/ in the directory itself, since there is no git directory
// to hide them in. The folder ignores itself, for a dir depot that happens to
// be a checkout.
func DirBriefDir(path string) string {
	return filepath.Join(path, ".yardr", "briefs")
}

// WorktreeSession is who works in a bead's worktree: the session a herdr
// group last started there, as its tab's environment names it. It is kept
// next to the briefs (see BriefDir) for a yardr command whose environment
// is another session's: an agent whose commands run in a process that
// outlives its pane (Codex's shared background server) hands them the
// environment of whichever session started that process. The worktree a
// command runs in is the one thing such a process does not get wrong.
type WorktreeSession struct {
	Bead    string `json:"bead"`
	Session string `json:"session"`
	Actor   string `json:"actor"`
	Stage   string `json:"stage"`
	Group   string `json:"group"`
}

const worktreeSessionFile = "session.json"

// writeWorktreeSession records job's session as the one working the
// worktree whose brief directory is dir, in place of an earlier stage's.
func writeWorktreeSession(dir string, job dispatch.Job, actor string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(WorktreeSession{Bead: job.Bead.ID, Session: job.Session.ID, Actor: actor,
		Stage: job.Bead.Stage, Group: job.Group.Name})
	if err != nil {
		return err
	}
	// Renamed into place, so a command never reads half of one.
	tmp := filepath.Join(dir, worktreeSessionFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, worktreeSessionFile))
}

// SessionOf is the session working the bead worktree that dir is in, nil
// when dir is in none: outside a checkout, in one no herdr group started a
// session in, or in a dir depot, whose beads share one directory.
func SessionOf(ctx context.Context, dir string) *WorktreeSession {
	briefs, err := BriefDir(ctx, dir)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(briefs, worktreeSessionFile))
	if err != nil {
		return nil
	}
	ws := new(WorktreeSession)
	if json.Unmarshal(raw, ws) != nil || ws.Bead == "" || ws.Session == "" {
		return nil
	}
	return ws
}

// writeBrief writes a session's brief into dir, which it creates.
func writeBrief(dir, session, brief string) (string, error) {
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
// is not ready yet (blocked on a question a human must answer, or just
// slow) is not a failure: the session is kept and the prompt is left on the
// handle as pending, for the dispatcher to deliver once the agent is idle.
// So is a prompt that was sent and did not arrive (see arrived): one that
// never showed is sent again, one left in the agent's input is submitted,
// and one taken as arrived without its echo is looked at again.
// With trust, the one question yardr answers itself is Claude Code's
// folder-trust dialog (see acceptTrust); the handle then says so.
func (h *Herdr) startAgent(ctx context.Context, args []string, name, prompt string, handle map[string]any, trust bool) (map[string]any, error) {
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
		pane, _ := handle["pane"].(string)
		if !trust || pane == "" || !h.acceptTrust(ctx, pane) {
			handle[dispatch.PendingPrompt] = prompt
			return handle, nil
		}
		handle["trust_accepted"] = true
		if !h.ready(ctx, handle) {
			// Slow, or at a next question: the dispatcher takes it from here.
			handle[dispatch.PendingPrompt] = prompt
			return handle, nil
		}
	} else if err != nil {
		return handle, err
	}
	took, err := h.prompt(ctx, name, prompt)
	if err != nil {
		return handle, err
	}
	if took == tookRefused {
		// At a dialog, and nothing was typed: as an agent that was not ready.
		handle[dispatch.PendingPrompt] = prompt
		return handle, nil
	}
	saw := h.arrived(ctx, handle, prompt)
	handle[dispatch.PromptLook] = saw + ", herdr " + took
	switch {
	case saw == sawShown:
	case took == tookBlocked:
		// Sent, and the agent is at a dialog with no echo of it: the
		// dispatcher says who waits for a human, and delivers the prompt
		// after the answer unless it shows by then.
		handle[dispatch.PendingPrompt] = prompt
	case saw == sawNoPane:
	case took == tookWorking, saw == sawUnreadable:
		// Taken, as far as herdr or a pane that cannot be read can tell,
		// and either can be wrong: the dispatcher looks again before the
		// prompt leaves the handle.
		handle[dispatch.PendingPrompt] = prompt
		handle[dispatch.PromptUnconfirmed] = true
	default:
		// Sent, and nothing in the pane to show that it was taken: the
		// dispatcher sends it again once the agent is idle, or submits the
		// text that is in its input, unless it shows up meanwhile.
		handle[dispatch.PendingPrompt] = prompt
	}
	return handle, nil
}

// promptGate bounds herdr's wait for an agent to start on a prompt
// (`agent prompt --wait --timeout`): a little over the five seconds herdr
// itself gives a prompt before it calls it stalled, so that herdr's answer
// comes first. Like trustWait it is spent inside the dispatcher's pass, and
// only by a prompt the agent did not start on. A variable so tests can
// shorten it.
var promptGate = 6 * time.Second

// What herdr said of a prompt it was given to wait on (see prompt). The
// first three are kept on the handle next to what the pane showed
// (dispatch.PromptLook).
const (
	tookWorking = "working" // typed, and herdr saw the agent working
	tookBlocked = "blocked" // typed, and herdr saw the agent at a dialog
	tookStalled = "stalled" // typed, and herdr saw the agent start on nothing
	tookRefused = "refused" // not typed: the agent was at a dialog already
)

// prompt has herdr type text into the agent called name, submit it and wait
// until it sees the agent working or at a dialog, and says which. herdr
// gives a prompt sent to an agent that is not working five seconds to show
// either and then says agent_prompt_stalled; an agent already at a dialog
// is refused before anything is typed (agent_blocked), since the keys would
// answer the dialog.
//
// Found live (yardr-7y16): the yardmaster's first prompt stalled and nothing
// said so for seven minutes. yardr sent prompts without --wait and learned
// from its own looks at the pane, over ten seconds, what herdr says in
// five. The wait is herdr's now. The look is still yardr's (see arrived):
// stalled does not say the text never reached the agent, so what is done
// next is decided by where the pane shows it.
//
// A timeout, and any error herdr gives other than agent_blocked, is read as
// a stall: the text may be in the pane, and the look tells. So is an answer
// that names neither state. An error that is no answer of herdr's (it is
// down, or missing) is returned: nothing is known of the prompt then.
func (h *Herdr) prompt(ctx context.Context, name, text string) (took string, err error) {
	raw, err := h.run(ctx, "agent", "prompt", name, text, "--wait", "--until", "working", "--until", "blocked",
		"--timeout", strconv.FormatInt(promptGate.Milliseconds(), 10))
	var herr *herdrError
	switch {
	case err == nil:
		var info struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		}
		_ = json.Unmarshal(raw, &info)
		switch info.Agent.Status {
		case "working":
			return tookWorking, nil
		case "blocked":
			return tookBlocked, nil
		}
		return tookStalled, nil
	case errors.As(err, &herr) && herr.Code == "agent_blocked":
		return tookRefused, nil
	default:
		// Even an error that says nothing about delivery does not say the
		// text was not typed. Let the pane decide, as it does for a stall.
		return tookStalled, nil
	}
}

// promptScan is how far back in a pane a first prompt is looked for.
const promptScan = 200

// What arrived saw of a first prompt in the agent's pane. It is kept on the
// handle with what herdr said of the prompt (dispatch.PromptLook).
const (
	sawShown      = "shown"      // echoed above the agent's input: it was taken
	sawNoPane     = "no pane"    // the handle names no pane to look at
	sawUnreadable = "unreadable" // the pane could not be read
	sawTyped      = "typed"      // in the agent's input, not submitted
	sawNoInput    = "no input"   // in a pane whose harness has drawn no input
	sawAbsent     = "absent"     // nowhere in the pane
)

// arrived says what the pane of the agent shows of the prompt herdr just
// sent it. It is one look: herdr has waited for the agent to start on the
// prompt (see prompt), and what the look leaves open the dispatcher's
// passes close. Found live (yardr-ykbt):
// `agent prompt` succeeding says the keys were sent, not that they were
// taken. A Claude Code still coming up after its trust dialog was reported
// idle and dropped them, and the session sat there without its first prompt.
// Found live (yardr-cxsb): a prompt whose text is in the agent's input did
// not arrive either. The Enter after it was not taken as a submit, and the
// crew member sat idle on its own prompt until a person pressed Return. The
// dispatcher submits that one (see PromptTyped); here it is only told from
// one that arrived.
//
// Only the echo is proof. Found live (yardr-myyv): two builders on a
// loaded machine sat idle with the prompt in their input, and their
// handles had no pending prompt: one look had taken it as arrived, and
// nothing looked again. An agent that herdr saw working, and a pane
// that cannot be read, are each one reading at one moment; startAgent
// leaves such a prompt to the dispatcher as unconfirmed, which a pane that
// can never be read then costs one pass.
//
// Found live (yardr-7y16): the yardmaster's handle said "shown, look 1" and
// its prompt sat in Claude Code's input for seven minutes. The keys had
// come while Claude Code was still starting: the pane echoed them, with no
// input drawn yet to tell them from a message it took, and that text was
// read as the echo. For a harness whose input mark yardr knows, a pane
// without the mark is one that is still coming up and proves nothing: a
// prompt that shows in no other way is left to the dispatcher (see
// promptPlace).
func (h *Herdr) arrived(ctx context.Context, handle map[string]any, prompt string) (saw string) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return sawNoPane
	}
	text, err := h.readPane(ctx, pane, "recent-unwrapped", promptScan)
	if err != nil {
		return sawUnreadable
	}
	kind, _ := handle[harnessKind].(string)
	switch taken, typed, bare := promptPlace(text, prompt, kind); {
	case taken:
		return sawShown
	case typed:
		return sawTyped
	case bare:
		return sawNoInput
	}
	return sawAbsent
}

// PromptShown reports whether the pane of the session shows prompt as one
// the agent took: a first prompt left pending because it had not shown in
// time (see arrived) may have been taken since, and is then not sent a
// second time; one the dispatcher delivered is pending until it shows. Text
// that is only in the agent's input is not shown (see PromptTyped). An error
// says the pane could not be read, which tells nothing either way (see
// arrived).
func (h *Herdr) PromptShown(ctx context.Context, handle map[string]any, prompt string) (bool, error) {
	taken, _, err := h.promptPlace(ctx, handle, prompt)
	return taken, err
}

// PromptTyped reports whether prompt is in the input of the session's agent
// and nowhere above it: typed, and not submitted.
func (h *Herdr) PromptTyped(ctx context.Context, handle map[string]any, prompt string) (bool, error) {
	_, typed, err := h.promptPlace(ctx, handle, prompt)
	return typed, err
}

// SubmitPrompt presses Enter in the session's pane: what a prompt that is
// typed in the agent's input still needs. The text is not sent again, which
// would put it in the input twice.
func (h *Herdr) SubmitPrompt(ctx context.Context, handle map[string]any) error {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return fmt.Errorf("handle has no pane")
	}
	return h.sendKey(ctx, pane, "enter")
}

func (h *Herdr) promptPlace(ctx context.Context, handle map[string]any, prompt string) (taken, typed bool, err error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return false, false, fmt.Errorf("handle has no pane")
	}
	text, err := h.readPane(ctx, pane, "recent-unwrapped", promptScan)
	if err != nil {
		return false, false, err
	}
	kind, _ := handle[harnessKind].(string)
	taken, typed, _ = promptPlace(text, prompt, kind)
	return taken, typed, nil
}

// harnessKind is a handle key: the kind of harness the session's agent was
// started as (claude, codex, ...), which says how its pane is read (see
// promptPlace). A handle from before the key was written has none, and its
// pane is read as one of a kind yardr does not know.
const harnessKind = "kind"

// firstPrompt is a handle key: what a crew member's agent was first told.
// A bead's session is told where its brief is, which its handle says; a
// crew member's prompt is made of what the handle does not hold, so the
// dispatcher, which looks for it in the agent's input later, reads it here.
const firstPrompt = "first_prompt"

// InputMarked reports whether the agent of handle is of a kind whose input
// mark yardr knows (dispatch.InputMarker): only in the pane of such a one
// is the last line that begins with a mark known to be its input.
func (h *Herdr) InputMarked(handle map[string]any) bool {
	kind, _ := handle[harnessKind].(string)
	_, ok := kindMarks[kind]
	return ok
}

// inputMarks begin the first line of a harness's input: Claude Code's "❯"
// and Codex's "›". Both draw their input at the bottom of the pane and begin
// a message they took with the same mark, further up. They are what a pane
// is read by when the marks of its own kind do not tell.
var inputMarks = []string{"❯", "›"}

// paneMarks are the marks the pane of a harness kind is read by: input
// begins the first line of its input, and message a message it took, which
// it echoes above the input. edge, for a kind that draws its input in a
// box, is the side of the box: it comes before the input mark, and before
// and after each line of the input's text, of which it is no part.
type paneMarks struct{ input, message, edge string }

// kindMarks is the marks of each harness kind yardr knows them for. Claude
// Code and Codex begin a message they took with the mark of their input.
// Kimi Code does not (read live, Kimi Code 2.1.1): its input is a box,
//
//	╭──────────────────────────╮
//	│ > half a thought that is │
//	│   long enough to wrap    │
//	╰──────────────────────────╯
//
// and a message it took begins with "✨". The input mark is asked for after
// the edge only: a ">" that begins a line is what many a transcript holds.
var kindMarks = map[string]paneMarks{
	"claude": {input: "❯", message: "❯"},
	"codex":  {input: "›", message: "›"},
	"kimi":   {input: ">", message: "✨", edge: "│"},
}

// begins reports whether line is the first of the input.
func (m paneMarks) begins(line string) bool {
	if m.edge != "" && !strings.HasPrefix(strings.TrimSpace(line), m.edge) {
		return false
	}
	return strings.HasPrefix(m.unboxed(line), m.input)
}

// unboxed is line without the white space around it and, for a kind that
// draws its input in a box, without the box's edges.
func (m paneMarks) unboxed(line string) string {
	line = strings.TrimSpace(line)
	if m.edge == "" {
		return line
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, m.edge), m.edge))
}

// promptPlace finds prompt in the text of a pane whose harness is of kind:
// taken when it shows above the agent's input (the harness echoed it as a
// sent message), typed when it shows only in the input. The input is what
// the pane holds from its last line that begins with an input mark: a
// harness draws its input below everything it has printed, so a prompt it
// took has an input, empty or not, after it.
//
// For a kind whose marks yardr knows, only they tell. A pane without the
// input mark is of a harness that has not drawn its input yet, and a prompt
// that shows there is bare: the keys echoed by the pane before the harness
// read them, which says nothing of what the harness did with them. Such an
// echo stays in the pane, above the input the harness draws later, so above
// the input the prompt is taken only where it begins a line with the
// message mark, as a message the harness took does.
//
// A pane of any other kind without a marked line is of a harness whose
// input yardr cannot tell from its transcript; a prompt that shows there is
// taken as arrived, as it was before yardr told the two apart.
func promptPlace(text, prompt, kind string) (taken, typed, bare bool) {
	lines := strings.Split(text, "\n")
	m, known := kindMarks[kind]
	input := -1
	for i, l := range lines {
		if known {
			if m.begins(l) {
				input = i
			}
			continue
		}
		for _, mark := range inputMarks {
			if strings.HasPrefix(strings.TrimSpace(l), mark) {
				input = i
			}
		}
	}
	switch {
	case input < 0 && known:
		return false, false, showsPrompt(text, prompt)
	case input < 0:
		return showsPrompt(text, prompt), false, false
	case !known:
		taken = showsPrompt(strings.Join(lines[:input], "\n"), prompt)
	default:
		for i, l := range lines[:input] {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, m.message) {
				continue
			}
			message := strings.TrimPrefix(l, m.message) + "\n" + strings.Join(lines[i+1:input], "\n")
			if beginsWithPrompt(message, prompt) {
				taken = true
				break
			}
		}
	}
	if taken {
		return true, false, false
	}
	// A box's edges are between the lines of a prompt it wraps.
	in := make([]string, 0, len(lines)-input)
	for _, l := range lines[input:] {
		in = append(in, m.unboxed(l))
	}
	return false, showsPrompt(strings.Join(in, "\n"), prompt), false
}

// InputText is what is typed in the input of the session's agent and not
// sent: a person's unfinished message, or a prompt whose Enter was not
// taken. A harness's placeholder is not text. known is false for a harness
// whose input yardr cannot tell (no line with an input mark), and an error
// says the pane could not be read; either way nothing is known about the
// input.
//
// Found live (yardr-ps05): a person was typing in the yardmaster's pane when
// the yard nudged the yardmaster. The nudge went into the same input, and its
// Enter sent their half sentence and the nudge as one message. So what types
// into an input looks here first.
func (h *Herdr) InputText(ctx context.Context, handle map[string]any) (text string, known bool, err error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", false, fmt.Errorf("handle has no pane")
	}
	styled, err := h.readPaneStyled(ctx, pane, "recent-unwrapped", promptScan)
	if err != nil {
		return "", false, err
	}
	kind, _ := handle[harnessKind].(string)
	text, known = inputText(styled, kind)
	return text, known, nil
}

// inputText finds, in a pane of a harness of kind read with its styling,
// the text of the agent's input: what follows the mark on the pane's last
// line that begins with one (see promptPlace), and the lines after it down
// to the first empty line or rule, which is where Codex's and Claude Code's
// input ends and their footer begins, as the bottom of its box is for
// Kimi's. ok is false for a pane with no such line.
//
// The mark is the kind's own. Where the pane shows none of it, or the kind
// is one yardr knows no marks for, any of inputMarks is taken for one: what
// follows may be text the harness printed, and yardr types nothing after it
// all the same. Kimi's question to a session that sat idle is drawn in place
// of its box, with "❯" before the answer Enter would choose.
//
// Faint text (SGR 2) is left out: it is how Claude Code and Codex draw what
// is in the input and was not typed. Read live with `herdr pane read
// --format ansi` (herdr 0.9.0), Claude Code 2.1.288 shows `❯ ESC[2mTry "fix
// typecheck errors"` and Codex `› ESC[2mAsk Codex to do anything`, and each
// shows what a person typed there with no attribute at all, as Kimi Code
// 2.1.1 does. The attribute is asked, not the wording: Claude Code's
// placeholder is a different suggestion each time, and a person may type a
// placeholder's very words.
func inputText(styled, kind string) (text string, ok bool) {
	plain, typed := unstyled(styled)
	m, known := kindMarks[kind]
	input := -1
	if known {
		for i, l := range plain {
			if m.begins(l) {
				input = i
			}
		}
	}
	if input < 0 {
		for i, l := range plain {
			for _, mark := range inputMarks {
				if strings.HasPrefix(strings.TrimSpace(l), mark) {
					input, m = i, paneMarks{input: mark}
				}
			}
		}
	}
	if input < 0 {
		return "", false
	}
	var lines []string
	for i := input; i < len(plain); i++ {
		if i > input && (strings.TrimSpace(plain[i]) == "" || isRule(plain[i])) {
			break
		}
		l := m.unboxed(typed[i])
		if i == input {
			l = strings.TrimPrefix(l, m.input)
		}
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), true
}

// isRule reports whether line is drawn of box-drawing characters alone, as
// the rules above and below Claude Code's input are.
func isRule(line string) bool {
	line = strings.TrimSpace(line)
	for _, r := range line {
		if r < 0x2500 || r > 0x257f {
			return false
		}
	}
	return line != ""
}

// unstyled splits a pane read with its styling into lines, twice: plain is
// each line's text, and typed the same without what is drawn faint. Every
// escape sequence is dropped; of SGR only faint (2) and what ends it (22,
// 0) are followed. The numbers after 38, 48 and 58 are a colour's, so a 2
// among them (`38;2;r;g;b`, `38;5;2`) is not the attribute.
func unstyled(styled string) (plain, typed []string) {
	var p, t strings.Builder
	faint := false
	flush := func() {
		plain, typed = append(plain, p.String()), append(typed, t.String())
		p.Reset()
		t.Reset()
	}
	rs := []rune(styled)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\n':
			flush()
		case r == '\r':
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == '[':
			j := i + 2
			for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
				j++
			}
			if j < len(rs) && rs[j] == 'm' {
				faint = sgrFaint(string(rs[i+2:j]), faint)
			}
			i = j
		case r == 0x1b && i+1 < len(rs) && rs[i+1] == ']':
			// An OSC (a title, a hyperlink) runs to BEL or ESC \.
			j := i + 2
			for j < len(rs) && rs[j] != 0x07 && !(rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\') {
				j++
			}
			if j < len(rs) && rs[j] == 0x1b {
				j++
			}
			i = j
		case r == 0x1b:
			i++ // a two-character escape
		default:
			p.WriteRune(r)
			if !faint {
				t.WriteRune(r)
			}
		}
	}
	flush()
	return plain, typed
}

// sgrFaint is whether text is faint after the SGR parameters params, given
// whether it was before.
func sgrFaint(params string, faint bool) bool {
	if params == "" {
		return false // ESC[m is ESC[0m
	}
	codes := strings.Split(params, ";")
	for i := 0; i < len(codes); i++ {
		// A colon carries a parameter's own arguments (38:2::r:g:b): only
		// what is before it is the code.
		code, _, _ := strings.Cut(codes[i], ":")
		switch code {
		case "", "0", "22":
			faint = false
		case "2":
			faint = true
		case "38", "48", "58":
			if codes[i] != code || i+1 >= len(codes) {
				break
			}
			switch codes[i+1] {
			case "5":
				i += 2
			case "2":
				i += 4
			}
		}
	}
	return faint
}

// promptLead is how much of a prompt, counted without its white space, is
// looked for in a pane: enough to tell it from anything else there, and
// short enough that an agent which shortens what it echoes still shows it.
const promptLead = 24

// showsPrompt reports whether a pane's text holds the start of prompt.
// White space is left out on both sides: an agent wraps a long prompt to
// its pane's width, in the middle of a brief's path if it must.
func showsPrompt(text, prompt string) bool {
	lead := promptLeadOf(prompt)
	return lead != "" && strings.Contains(strings.Join(strings.Fields(text), ""), lead)
}

// beginsWithPrompt is showsPrompt for a text that must begin with the
// prompt, not only hold it.
func beginsWithPrompt(text, prompt string) bool {
	lead := promptLeadOf(prompt)
	return lead != "" && strings.HasPrefix(strings.Join(strings.Fields(text), ""), lead)
}

func promptLeadOf(prompt string) string {
	lead := []rune(strings.Join(strings.Fields(prompt), ""))
	if len(lead) > promptLead {
		lead = lead[:promptLead]
	}
	return string(lead)
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
	depot, ok := handle["depot"].(string)
	if !ok {
		// A session started before a depot was called that.
		depot, _ = handle["rig"].(string)
	}
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

// checkoutTimeout bounds every git call Start and Finish make on a depot (a
// worktree added or removed, a branch made, a status). Like the fetch they
// run inside the dispatcher's pass, so one hanging on a slow disk or a hook
// would stall every group. Two minutes is herdr's own bound on the same
// work; local operations normally take well under a second. A variable so
// tests can shorten it (gitTimeout, in runner.go, is the role reads').
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

// codexNoDaemon makes a Codex session run without the shared background
// server, even one that is already running (codex --help).
const codexNoDaemon = "--no-daemon"

// AgentArgs are the agent's own arguments: the group's args, then --model
// from the bead's metadata.model, else the group's config.model. A model
// already in args is replaced, so a bead can override a group default. A
// Codex agent also gets --no-daemon (see agentArgs).
func AgentArgs(job dispatch.Job) []string {
	model, _ := job.Bead.Metadata["model"].(string)
	return agentArgs(job.Group.Config, model)
}

func agentArgs(cfg map[string]any, override string) []string {
	args := strs(cfg, "args")
	// Codex otherwise runs its shell commands in one background server
	// shared by all of the user's Codex sessions, with the environment of
	// the session that started it: every later session's yardr commands
	// would name that first session as actor and session, and descend from
	// no pane the store knows.
	if str(cfg, "kind", "claude") == "codex" && !slices.Contains(args, codexNoDaemon) {
		args = append(args, codexNoDaemon)
	}
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

// yardName is what tells this yard's crew from another yard's in
// herdr, which lists every yard on the machine's agents together: the
// `name` setting, else the home's directory name (~/.yardr reads as yardr).
func (h *Herdr) yardName(ctx context.Context) string {
	var all map[string]string
	if h.Store != nil {
		all, _ = h.Store.Settings(ctx)
	}
	return YardName(h.YardHome, all)
}

// YardName is the yard's name from its settings and its home (see
// yardName), for whoever has to name a crew member's agent or workspace as
// herdr lists it without a runner at hand.
func YardName(home string, settings map[string]string) string {
	if settings["name"] != "" {
		return settings["name"]
	}
	return strings.TrimLeft(filepath.Base(home), ".")
}

// crewDir is where a crew member's session runs: its dir, else the yard's
// home.
func (h *Herdr) crewDir(r store.CrewMember) string {
	if r.Dir != "" {
		return r.Dir
	}
	return h.YardHome
}

// LiveCrew looks through herdr's agents for one that already is r
// without being the session yardr recorded for it (see
// dispatch.CrewFinder): an agent under the crew member's herdr name
// (CrewAgentName), or one under its bare name in its directory, which
// is what a pane started by hand before the crew member was defined looks
// like. The recorded session itself (r.Handle's agent, in its pane) is not
// counted: after a stop for a handoff herdr may list it a moment longer.
func (h *Herdr) LiveCrew(ctx context.Context, r store.CrewMember) (*dispatch.LiveAgent, error) {
	raw, err := h.run(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var list struct {
		Agents []struct {
			Name string `json:"name"`
			Cwd  string `json:"cwd"`
			Pane string `json:"pane_id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("agent list: %w", err)
	}
	full, bare := CrewAgentName(h.yardName(ctx), r.Name), agentName(r.Name)
	dir := realPath(h.crewDir(r))
	own, _ := r.Handle["agent"].(string)
	ownPane, _ := r.Handle["pane"].(string)
	for _, a := range list.Agents {
		if a.Name == "" || (a.Name == own && a.Pane == ownPane) {
			continue
		}
		if a.Name == full || (a.Name == bare && dir != "" && realPath(a.Cwd) == dir) {
			return &dispatch.LiveAgent{Name: a.Name, Pane: a.Pane}, nil
		}
	}
	return nil, nil
}

// realPath is path with its symlinks resolved, as herdr reports a pane's
// cwd (/tmp is /private/tmp on macOS); a path that cannot be resolved is
// only cleaned.
func realPath(path string) string {
	if path == "" {
		return ""
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

// CrewPromptLead and CrewPromptEnd are how a crew member's first prompt
// begins and ends, for whoever looks in its pane for what the agent printed
// after it. What a prompt says beyond its prime (the greeting) goes between
// the two. OldCrewPromptLead is how it began until this release, when the
// crew were the residents: a session started before the upgrade has it in
// its pane.
const (
	CrewPromptLead    = "You are the yardr crew member"
	OldCrewPromptLead = "You are the yardr resident"
	CrewPromptEnd     = "and follow it."
)

// GreetKey is the crew member config key that names the user its first session
// greets by mail. yardr init sets it on the yardmaster it adds, to the
// operator: the onboarding ends in yardr chat, which shows mail and no pane,
// so the yardmaster's first word has to be mail.
const GreetKey = "greet"

// Greets is the user r's next session is asked to greet: the one its config
// names, for its first session only. A session after that (a rotation, a
// restart) is in a conversation that has begun, or was never answered.
func Greets(r store.CrewMember) string {
	if r.StartedAt != nil {
		return ""
	}
	return str(r.Config, GreetKey, "")
}

// Greeted reports whether r's running session is the one that was asked to
// greet (Greets, before it started).
func Greeted(r store.CrewMember) bool {
	return r.Live() && r.PrevStartedAt == nil && str(r.Config, GreetKey, "") != ""
}

// crewPrompt is a crew member's first prompt: who it is and where its
// prime is, and for a session that greets (Greets) whom to write to first.
// It begins and ends as every crew member's does: the start is what a pane is
// searched for to tell that the prompt arrived (showsPrompt), the end what
// the agent's answer is looked for after.
func crewPrompt(yardr string, r store.CrewMember) string {
	run := "Run"
	if user := Greets(r); user != "" {
		run = fmt.Sprintf("%s set this yard up a moment ago and waits for your first message in `%s chat`, which shows "+
			"them your mail and not your pane. Once you have read your prime, greet them with `%s mail %s \"<text>\"` and say "+
			"what you need from them for the next setup step; go on with them by mail. First run", user, yardr, yardr, user)
	}
	return fmt.Sprintf(CrewPromptLead+" %q. %s `%s prime --crew %s` "+CrewPromptEnd, r.Name, run, yardr, r.Name)
}

// StartCrew opens a herdr workspace in the crew member's directory, with
// its identity in the environment, starts its agent and points it at its
// prime. The agent and the workspace are named after the yard and the
// crew member (CrewAgentName).
func (h *Herdr) StartCrew(ctx context.Context, r store.CrewMember) (map[string]any, error) {
	dir := h.crewDir(r)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("crew member directory %q does not exist", dir)
	}
	secrets, err := h.crewEnv(ctx, r, dir)
	if err != nil {
		return nil, err
	}
	yard := h.yardName(ctx)
	name := CrewAgentName(yard, r.Name)
	// The settings of the yard's server are blanked, as for a bead's agent
	// (agentEnv).
	env := slices.Concat(config.Env("CREW", r.Name), config.Env("ACTOR", r.Name), config.BlankServerSettings())
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	// The environment goes over the socket, never on a command line (see
	// call). The workspace is not focused: a crew member starts in the
	// background, under the daemon, not in front of whoever is at herdr.
	raw, err := h.call(ctx, "workspace.create", map[string]any{
		"cwd": dir, "label": CrewLabel(yard, r.Name), "focus": false,
		"env": envMap(withEnvFile(env, secrets)),
	})
	if err != nil {
		return nil, fmt.Errorf("workspace for %s: %w", r.Name, err)
	}
	var ws herdrResult
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("workspace result: %w", err)
	}
	handle := map[string]any{"agent": name, "workspace": ws.Workspace.ID, "tab": ws.Tab.ID,
		"pane": ws.RootPane.ID, "dir": dir}
	// As for a bead's session: the store then knows a command run in the
	// crew member's pane as the crew member's, whatever actor it names, which is
	// what keeps a crew member from releasing a peer's bead as a user
	// (store.paneCrew).
	h.shellOn(ctx, ws.RootPane.ID, handle)

	kind := str(r.Config, "kind", "claude")
	handle[harnessKind] = kind
	start := []string{"agent", "start", name, "--kind", kind, "--pane", ws.RootPane.ID}
	if extra := agentArgs(r.Config, ""); len(extra) > 0 {
		start = append(append(start, "--"), extra...)
	}
	prompt := crewPrompt(h.yard(), r)
	handle[firstPrompt] = prompt
	return h.startAgent(ctx, start, name, prompt, handle, trusts(r.Config))
}

// envFile reads the env_file cfg names, if any, for the pane environment of
// who ("crew member yardmaster", "group builders"). A file that is missing, not a
// regular file or open to others is refused as Blocked: the session must
// not start without its credentials, nor with credentials others can read.
// Only key names are logged.
func (h *Herdr) envFile(cfg map[string]any, who string) ([]string, error) {
	path := str(cfg, "env_file", "")
	if path == "" {
		return nil, nil
	}
	f, err := config.ReadEnvFile(path)
	if err != nil {
		return nil, dispatch.Blocked(fmt.Errorf("%s: %w", who, err))
	}
	if h.Log != nil {
		h.Log.Debug("env_file loaded", "for", who, "path", f.Path, "keys", f.Keys())
	}
	return f.Env, nil
}

// crewEnv is what r's settings add to its pane environment: its
// env_file's entries and those its env_command prints (see
// config.ReadCrewEnv), asked for at every start. Either one refused (a
// file open to others, a command that fails, hangs or prints a line that is
// no KEY=VALUE) is Blocked, as a group's env_file is: the crew member must not
// start without its credentials. Only key names are logged.
func (h *Herdr) crewEnv(ctx context.Context, r store.CrewMember, dir string) ([]string, error) {
	env, err := config.ReadCrewEnv(ctx, r.Name, dir, h.YardHome, r.Config)
	if err != nil {
		if ctx.Err() != nil {
			// The daemon is stopping: nothing a human has to fix.
			return nil, err
		}
		return nil, dispatch.Blocked(fmt.Errorf("crew member %s: %w", r.Name, err))
	}
	if h.Log != nil && env.Set() {
		h.Log.Debug("crew member environment loaded", "for", "crew member "+r.Name, "from", env.From(), "keys", env.Keys())
	}
	return env.Env, nil
}

// withEnvFile is env with the env file's entries added. A key yardr sets
// itself (the session's identity, the yard's home) is left out of the file's
// entries, so a credentials file cannot make an agent someone else. The one
// such key that is a list, OTEL_RESOURCE_ATTRIBUTES, keeps the file's
// attributes in front of yardr's: a group's env file is where an operator
// turns a harness's telemetry on, and may name attributes of its own.
func withEnvFile(env, file []string) []string {
	set := make(map[string]int, len(env))
	out := slices.Clone(env)
	for i, e := range out {
		k, _, _ := strings.Cut(e, "=")
		set[k] = i
	}
	for _, e := range file {
		k, v, _ := strings.Cut(e, "=")
		i, ours := set[k]
		switch {
		case !ours:
			out = append(out, e)
		case k == otel.ResourceVar:
			if theirs := otel.ResourceAttributes(v); theirs != "" {
				_, mine, _ := strings.Cut(out[i], "=")
				out[i] = k + "=" + theirs + "," + mine
			}
		}
	}
	return out
}

// Tail is the last lines the session's agent printed in its pane, blank
// lines left out: where an agent says why it does nothing (a model it may
// not use, no login).
func (h *Herdr) Tail(ctx context.Context, handle map[string]any, lines int) (string, error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", fmt.Errorf("handle has no pane")
	}
	// A TUI pads its screen with blank lines, so read well more than is kept.
	text, err := h.readPane(ctx, pane, "recent-unwrapped", 4*lines+40)
	if err != nil {
		return "", err
	}
	tail := lastLines(text, lines)
	// An event is no place for a screenful of one long line.
	const maxTail = 4000
	if len(tail) > maxTail {
		tail = "…" + strings.ToValidUTF8(tail[len(tail)-maxTail:], "")
	}
	return tail, nil
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
// prompt). An agent at a dialog is typed nothing (dispatch.ErrAgentBlocked),
// and text the agent did not start on is said to be so
// (dispatch.ErrPromptStalled): it was typed, and the caller looks for it.
func (h *Herdr) Nudge(ctx context.Context, handle map[string]any, text string) error {
	name, _ := handle["agent"].(string)
	if name == "" {
		return fmt.Errorf("handle has no agent")
	}
	switch took, err := h.prompt(ctx, name, text); {
	case err != nil:
		return err
	case took == tookRefused:
		return dispatch.ErrAgentBlocked
	case took == tookStalled:
		return dispatch.ErrPromptStalled
	}
	return nil
}

func (h *Herdr) yard() string {
	if h.YardBin != "" {
		return h.YardBin
	}
	return "yardr"
}
