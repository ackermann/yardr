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
// dir rig's bead has no branch: its workspace is opened in the rig's
// directory itself (see startDir).
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
//	env_file   KEY=VALUE file (mode 0600 or stricter) whose entries go into
//	           the agent's pane environment: outbound credentials, which
//	           never belong in the store (see config.ReadEnvFile) nor on a
//	           command line (see call)
//
// A resident's config has those but keep and worktrees, and one more:
//
//	env_command  a command that prints KEY=VALUE lines, run at each start,
//	             whose entries join the env_file's (see
//	             config.ReadResidentEnv)
//
//	trust      false to leave Claude Code's folder-trust dialog for a human;
//	           by default yardr answers it (see acceptTrust)
//
// Worktrees live at <root>/<rig>/<bead>.
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

// ServerUp asks herdr whether its server answers, by the call a resident's
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
func AgentName(bead, stage string) string { return agentName(bead + "-" + stage) }

// ResidentAgentName is the herdr agent name of a yard's resident,
// "<yard>-<resident>": herdr lists every yard on the machine's agents
// together and refuses a second of one name, so two yards' mayors must not
// both be "mayor". Without a yard name it is the resident's.
func ResidentAgentName(yard, resident string) string { return agentName(yard + "-" + resident) }

// ResidentLabel is the herdr label of a resident's workspace, "<yard>
// <resident>", for the same reason.
func ResidentLabel(yard, resident string) string {
	return strings.TrimSpace(yard + " " + resident)
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
// working on it first, then "[<epic>] " for a child of an epic, so the tab
// bar shows which epic it belongs to, then the bead ("builders [sv6b] 2cet",
// or "builders jtyz" for a bead with no epic). The workspace is reused
// across stages and relabelled on each reopen, so the group in it is always
// the current one.
func WorkspaceLabel(rig string, group store.Group, epic, bead string) string {
	return joinLabel(GroupLabel(rig, group), rig, epic, bead)
}

// TabLabel is the herdr label for a stage's tab: the workspace's label with
// the stage in place of the group ("review [sv6b] 2cet"). The group is not
// repeated, since the workspace above the tab names it.
func TabLabel(rig, stage, epic, bead string) string {
	return joinLabel(stage, rig, epic, bead)
}

// GroupLabel is the name a group goes by in herdr labels: its `label`
// config when set, else its name. herdr already nests workspaces under the
// rig's repo, so a leading "<rig>-" on the name is noise that only gets the
// label truncated in the sidebar.
func GroupLabel(rig string, group store.Group) string {
	if l := str(group.Config, "label", ""); l != "" {
		return l
	}
	return ShortID(rig, group.Name)
}

// ShortID drops the leading "<rig>-" from an id for a herdr label, where
// the rig is already the node above. An id without it stays whole; ids
// anywhere else in yardr stay full.
func ShortID(rig, id string) string {
	if rig == "" {
		return id
	}
	return strings.TrimPrefix(id, rig+"-")
}

// joinLabel is "<head> [<epic>] <bead>", the parts that are empty left out
// and the ids shortened.
func joinLabel(head, rig, epic, bead string) string {
	var parts []string
	if head != "" {
		parts = append(parts, head)
	}
	if epic != "" {
		parts = append(parts, "["+ShortID(rig, epic)+"]")
	}
	return strings.Join(append(parts, ShortID(rig, bead)), " ")
}

func (h *Herdr) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	if job.Rig == nil {
		return nil, fmt.Errorf("herdr runner needs the bead to have a rig")
	}
	rig := job.Rig
	if rig.IsDir() {
		return h.startDir(ctx, job)
	}
	branch := Branch(job.Bead)
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	handle := map[string]any{"agent": name, "branch": branch, "repo": rig.Path}
	label := WorkspaceLabel(rig.Name, job.Group, job.Epic, job.Bead.ID)
	// Before anything is made: a group whose env_file is refused starts
	// nothing until it is fixed.
	secrets, err := h.envFile(job.Group.Config, "group "+job.Group.Name)
	if err != nil {
		return nil, err
	}

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
		if dispatch.IsRuntime(err) || !errors.As(err, &herr) {
			// herdr down, or not answering: nothing to fall back to.
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
		// An open workspace keeps the label it was opened with, which names
		// the previous stage's group and may predate the bead joining an
		// epic (or this labelling), so relabel it. Best effort: a label is cosmetic.
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
		"label": TabLabel(rig.Name, job.Bead.Stage, job.Epic, job.Bead.ID),
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

	args := []string{"agent", "start", name, "--kind", str(job.Group.Config, "kind", "claude"), "--pane", pane}
	if extra := AgentArgs(job); len(extra) > 0 {
		args = append(append(args, "--"), extra...)
	}
	prompt := fmt.Sprintf("Read %s and follow it.", briefPath)
	if h.YardBin != "" {
		prompt += fmt.Sprintf(" Run yardr as %s.", h.YardBin)
	}
	return h.startAgent(ctx, args, name, prompt, handle, trusts(job.Group.Config))
}

// startDir starts a bead of a dir rig. There is no branch and no worktree:
// the agent works in the rig's directory itself, in a workspace opened
// there for the bead (reused by its later stages), and its brief goes under
// the directory's .yardr/briefs/. An epic's child is no different: an epic
// in a dir rig only groups, so no epic branch is made either.
func (h *Herdr) startDir(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	rig := job.Rig
	name := AgentName(job.Bead.ID, job.Bead.Stage)
	if st, err := os.Stat(rig.Path); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("rig %s: directory %q does not exist", rig.Name, rig.Path)
	}
	secrets, err := h.envFile(job.Group.Config, "group "+job.Group.Name)
	if err != nil {
		return nil, err
	}
	label := WorkspaceLabel(rig.Name, job.Group, job.Epic, job.Bead.ID)
	ws, err := h.dirWorkspace(ctx, rig, job.Bead.ID, label)
	if err != nil {
		return nil, err
	}
	handle := map[string]any{"agent": name, "dir": rig.Path, "rig": rig.Name, "bead": job.Bead.ID,
		"workspace": ws.Workspace.ID}

	// As for a worktree: the environment goes over the socket, never on a
	// command line (see call).
	raw, err := h.call(ctx, "tab.create", map[string]any{
		"workspace_id": ws.Workspace.ID, "cwd": rig.Path,
		"label": TabLabel(rig.Name, job.Bead.Stage, job.Epic, job.Bead.ID),
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

	dir := DirBriefDir(rig.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return handle, err
	}
	// A dir rig may still be inside a checkout; its briefs are no change.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		return handle, err
	}
	return h.brief(ctx, job, name, rig.Path, dir, pane, handle)
}

// dirWorkspace opens the herdr workspace a dir rig's bead works in: the one
// an earlier stage opened, while it is still open, else a new one in the
// rig's directory. herdr reports no checkout for a plain directory, so the
// earlier one is found by its label, which ends in the bead's id, and a
// pane in it that sits in the rig's directory. A reused workspace is
// relabelled for the current group.
func (h *Herdr) dirWorkspace(ctx context.Context, rig *store.Rig, bead, label string) (herdrResult, error) {
	var ws herdrResult
	id, err := h.findDirWorkspace(ctx, rig, bead)
	if err != nil {
		return ws, err
	}
	if id != "" {
		_, _ = h.run(ctx, "workspace", "rename", id, label) // cosmetic, as for a worktree
		ws.Workspace.ID, ws.AlreadyOpen = id, true
		return ws, nil
	}
	raw, err := h.run(ctx, "workspace", "create", "--cwd", rig.Path, "--label", label, "--no-focus")
	if err != nil {
		return ws, fmt.Errorf("workspace for %s in %s: %w", bead, rig.Path, err)
	}
	if err := json.Unmarshal(raw, &ws); err != nil {
		return ws, fmt.Errorf("workspace result: %w", err)
	}
	return ws, nil
}

// findDirWorkspace is the id of the open workspace of bead in the dir rig,
// or "" when there is none (see dirWorkspace).
func (h *Herdr) findDirWorkspace(ctx context.Context, rig *store.Rig, bead string) (string, error) {
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
	short := ShortID(rig.Name, bead)
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
			if p.Workspace == id && sameDir(p.Cwd, rig.Path) {
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
// binary so its yardr commands reach this yard. A bead of a git rig gets
// what it lands on (YARDR_BASE); one of a dir rig, which has no base or
// branch, gets the directory it works in (YARDR_RIG, YARDR_RIG_PATH).
func (h *Herdr) agentEnv(job dispatch.Job, name string) []string {
	env := Env(job, name)
	if rig := job.Rig; rig != nil && rig.IsDir() {
		env = slices.Concat(env, config.Env("RIG", rig.Name), config.Env("RIG_PATH", rig.Path))
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

// DirBriefDir is where briefs for a bead of the dir rig at path go:
// .yardr/briefs/ in the directory itself, since there is no git directory
// to hide them in. The folder ignores itself, for a dir rig that happens to
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
// session in, or in a dir rig, whose beads share one directory.
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
	if _, err := h.run(ctx, "agent", "prompt", name, prompt); err != nil {
		return handle, err
	}
	saw, looks := h.arrived(ctx, handle, prompt)
	handle[dispatch.PromptLook] = fmt.Sprintf("%s, look %d", saw, looks)
	switch saw {
	case sawShown, sawNoPane:
	case sawWorking, sawUnreadable:
		// Taken, as far as one look can tell, and that look can be wrong:
		// the dispatcher looks again before the prompt leaves the handle.
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

// promptWait bounds the wait for a first prompt to show in the agent's pane
// after herdr sent it. Like trustWait it is spent inside the dispatcher's
// pass, and only by a prompt that got lost: one that arrived shows at once.
// A variable so tests can shorten it.
var promptWait = 10 * time.Second

// promptNow and promptSleep are the clock arrived waits by. Variables so a
// test can count its waits instead of timing them: a bound on the real time
// a look took failed on a loaded machine, where the fake herdr's forks alone
// ran past it (yardr-1ph9).
var (
	promptNow   = time.Now
	promptSleep = time.Sleep
)

// promptScan is how far back in a pane a first prompt is looked for.
const promptScan = 200

// What arrived saw of a first prompt. It is kept on the handle with the
// number of the look that saw it (dispatch.PromptLook).
const (
	sawShown      = "shown"      // echoed above the agent's input: it was taken
	sawNoPane     = "no pane"    // the handle names no pane to look at
	sawWorking    = "working"    // not echoed, and herdr reports the agent working
	sawUnreadable = "unreadable" // the pane could not be read
	sawTyped      = "typed"      // in the input of an agent that is not working
	sawAbsent     = "absent"     // nowhere in the pane for all of promptWait
)

// arrived says what the pane of the agent shows of the prompt herdr just
// sent it, and at which look. Found live (yardr-ykbt):
// `agent prompt` succeeding says the keys were sent, not that they were
// taken. A Claude Code still coming up after its trust dialog was reported
// idle and dropped them, and the session sat there without its first prompt.
// Found live (yardr-cxsb): a prompt whose text is in the agent's input did
// not arrive either. The Enter after it was not taken as a submit, and the
// resident sat idle on its own prompt until a person pressed Return. The
// dispatcher submits that one (see PromptTyped); here it is only told from
// one that arrived, and not waited on for all of promptWait.
//
// Only the echo is proof. Found live (yardr-myyv): two builders on a
// loaded machine sat idle with the prompt in their input, and their
// handles had no pending prompt: one look had taken it as arrived, and
// nothing looked again. An agent that herdr reports working, and a pane
// that cannot be read, are each one reading at one moment; startAgent
// leaves such a prompt to the dispatcher as unconfirmed, which a pane that
// can never be read then costs one pass.
func (h *Herdr) arrived(ctx context.Context, handle map[string]any, prompt string) (saw string, looks int) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return sawNoPane, 0
	}
	var typedSince time.Time
	for deadline := promptNow().Add(promptWait); ; {
		looks++
		text, err := h.readPane(ctx, pane, "recent-unwrapped", promptScan)
		if err != nil {
			return sawUnreadable, looks
		}
		taken, typed := promptPlace(text, prompt)
		if taken {
			return sawShown, looks
		}
		if s, err := h.Status(ctx, handle); err == nil && s == "working" {
			return sawWorking, looks
		}
		now := promptNow()
		switch {
		case !typed:
			typedSince = time.Time{}
		case typedSince.IsZero():
			typedSince = now
		case now.Sub(typedSince) >= promptWait/4:
			// Text that is still in the input of an agent that is not
			// working, this long after its Enter, was not submitted.
			return sawTyped, looks
		}
		if now.After(deadline) || ctx.Err() != nil {
			if typed {
				return sawTyped, looks
			}
			return sawAbsent, looks
		}
		promptSleep(promptWait / 40)
	}
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
	taken, typed = promptPlace(text, prompt)
	return taken, typed, nil
}

// inputMarks begin the first line of a harness's input: Claude Code's "❯"
// and Codex's "›". Both draw their input at the bottom of the pane and begin
// a message they took with the same mark, further up.
var inputMarks = []string{"❯", "›"}

// promptPlace finds prompt in a pane's text: taken when it shows above the
// agent's input (the harness echoed it as a sent message), typed when it
// shows only in the input. The input is what the pane holds from its last
// line that begins with an input mark: a harness draws its input below
// everything it has printed, so a prompt it took has an input, empty or
// not, after it. A pane without such a line is of a harness whose input
// yardr cannot tell from its transcript; a prompt that shows there is taken
// as arrived, as it was before yardr told the two apart.
func promptPlace(text, prompt string) (taken, typed bool) {
	lines := strings.Split(text, "\n")
	input := -1
	for i, l := range lines {
		l = strings.TrimSpace(l)
		for _, m := range inputMarks {
			if strings.HasPrefix(l, m) {
				input = i
			}
		}
	}
	if input < 0 {
		return showsPrompt(text, prompt), false
	}
	if showsPrompt(strings.Join(lines[:input], "\n"), prompt) {
		return true, false
	}
	return false, showsPrompt(strings.Join(lines[input:], "\n"), prompt)
}

// InputText is what is typed in the input of the session's agent and not
// sent: a person's unfinished message, or a prompt whose Enter was not
// taken. A harness's placeholder is not text. known is false for a harness
// whose input yardr cannot tell (no line with an input mark), and an error
// says the pane could not be read; either way nothing is known about the
// input.
//
// Found live (yardr-ps05): a person was typing in the mayor's pane when the
// yard nudged the mayor. The nudge went into the same input, and its Enter
// sent their half sentence and the nudge as one message. So what types into
// an input looks here first.
func (h *Herdr) InputText(ctx context.Context, handle map[string]any) (text string, known bool, err error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return "", false, fmt.Errorf("handle has no pane")
	}
	styled, err := h.readPaneStyled(ctx, pane, "recent-unwrapped", promptScan)
	if err != nil {
		return "", false, err
	}
	text, known = inputText(styled)
	return text, known, nil
}

// inputText finds, in a pane read with its styling, the text of the agent's
// input: what follows the mark on the pane's last line that begins with one
// (see promptPlace), and the lines after it down to the first empty line or
// rule, which is where Codex's and Claude Code's input ends and their
// footer begins. ok is false for a pane with no such line.
//
// Faint text (SGR 2) is left out: it is how both draw what is in the input
// and was not typed. Read live with `herdr pane read --format ansi` (herdr
// 0.9.0), Claude Code 2.1.288 shows `❯ ESC[2mTry "fix typecheck errors"`
// and Codex `› ESC[2mAsk Codex to do anything`, and each shows what a
// person typed there with no attribute at all. The attribute is asked, not
// the wording: Claude Code's placeholder is a different suggestion each
// time, and a person may type a placeholder's very words.
func inputText(styled string) (text string, ok bool) {
	plain, typed := unstyled(styled)
	input := -1
	for i, l := range plain {
		l = strings.TrimSpace(l)
		for _, m := range inputMarks {
			if strings.HasPrefix(l, m) {
				input = i
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
		l := strings.TrimSpace(typed[i])
		if i == input {
			for _, m := range inputMarks {
				l = strings.TrimPrefix(l, m)
			}
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
	lead := []rune(strings.Join(strings.Fields(prompt), ""))
	if len(lead) > promptLead {
		lead = lead[:promptLead]
	}
	return len(lead) > 0 && strings.Contains(strings.Join(strings.Fields(text), ""), string(lead))
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
// rig's bead has no worktree (see finishDir).
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

// finishDir cleans up after a closed bead of a dir rig: its session's
// brief is removed and the bead's workspace closed, if still open. The
// directory itself is the rig's and is left as it is; there is no worktree
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
	rig, _ := handle["rig"].(string)
	bead, _ := handle["bead"].(string)
	if bead != "" {
		// Found as a later stage would find it, so a workspace id herdr
		// handed out again since is never closed.
		if id, err := h.findDirWorkspace(ctx, &store.Rig{Name: rig, Path: dir, Kind: store.RigDir}, bead); err == nil && id != "" {
			_, _ = h.run(ctx, "workspace", "close", id)
		}
	}
	if !removed {
		return dispatch.ErrNothingLeft
	}
	return nil
}

// checkoutTimeout bounds every git call Start and Finish make on a rig (a
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

// yardName is what tells this yard's residents from another yard's in
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
// yardName), for whoever has to name a resident's agent or workspace as
// herdr lists it without a runner at hand.
func YardName(home string, settings map[string]string) string {
	if settings["name"] != "" {
		return settings["name"]
	}
	return strings.TrimLeft(filepath.Base(home), ".")
}

// residentDir is where a resident's session runs: its dir, else the yard's
// home.
func (h *Herdr) residentDir(r store.Resident) string {
	if r.Dir != "" {
		return r.Dir
	}
	return h.YardHome
}

// LiveResident looks through herdr's agents for one that already is r
// without being the session yardr recorded for it (see
// dispatch.ResidentFinder): an agent under the resident's herdr name
// (ResidentAgentName), or one under its bare name in its directory, which
// is what a pane started by hand before the resident was defined looks
// like. The recorded session itself (r.Handle's agent, in its pane) is not
// counted: after a stop for a handoff herdr may list it a moment longer.
func (h *Herdr) LiveResident(ctx context.Context, r store.Resident) (*dispatch.LiveAgent, error) {
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
	full, bare := ResidentAgentName(h.yardName(ctx), r.Name), agentName(r.Name)
	dir := realPath(h.residentDir(r))
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

// ResidentPromptLead and ResidentPromptEnd are how a resident's first prompt
// begins and ends, for whoever looks in its pane for what the agent printed
// after it. What a prompt says beyond its prime (the greeting) goes between
// the two.
const (
	ResidentPromptLead = "You are the yardr resident"
	ResidentPromptEnd  = "and follow it."
)

// GreetKey is the resident config key that names the user its first session
// greets by mail. yardr init sets it on the mayor it adds, to the operator:
// the onboarding ends in yardr chat, which shows mail and no pane, so the
// mayor's first word has to be mail.
const GreetKey = "greet"

// Greets is the user r's next session is asked to greet: the one its config
// names, for its first session only. A session after that (a rotation, a
// restart) is in a conversation that has begun, or was never answered.
func Greets(r store.Resident) string {
	if r.StartedAt != nil {
		return ""
	}
	return str(r.Config, GreetKey, "")
}

// Greeted reports whether r's running session is the one that was asked to
// greet (Greets, before it started).
func Greeted(r store.Resident) bool {
	return r.Live() && r.PrevStartedAt == nil && str(r.Config, GreetKey, "") != ""
}

// residentPrompt is a resident's first prompt: who it is and where its
// prime is, and for a session that greets (Greets) whom to write to first.
// It begins and ends as every resident's does: the start is what a pane is
// searched for to tell that the prompt arrived (showsPrompt), the end what
// the agent's answer is looked for after.
func residentPrompt(yardr string, r store.Resident) string {
	run := "Run"
	if user := Greets(r); user != "" {
		run = fmt.Sprintf("%s set this yard up a moment ago and waits for your first message in `%s chat`, which shows "+
			"them your mail and not your pane. Once you have read your prime, greet them with `%s mail %s \"<text>\"` and say "+
			"what you need from them for the next setup step; go on with them by mail. First run", user, yardr, yardr, user)
	}
	return fmt.Sprintf(ResidentPromptLead+" %q. %s `%s prime --resident %s` "+ResidentPromptEnd, r.Name, run, yardr, r.Name)
}

// StartResident opens a herdr workspace in the resident's directory, with
// its identity in the environment, starts its agent and points it at its
// prime. The agent and the workspace are named after the yard and the
// resident (ResidentAgentName).
func (h *Herdr) StartResident(ctx context.Context, r store.Resident) (map[string]any, error) {
	dir := h.residentDir(r)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("resident directory %q does not exist", dir)
	}
	secrets, err := h.residentEnv(ctx, r, dir)
	if err != nil {
		return nil, err
	}
	yard := h.yardName(ctx)
	name := ResidentAgentName(yard, r.Name)
	// The settings of the yard's server are blanked, as for a bead's agent
	// (agentEnv).
	env := slices.Concat(config.Env("RESIDENT", r.Name), config.Env("ACTOR", r.Name), config.BlankServerSettings())
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	// The environment goes over the socket, never on a command line (see
	// call). The workspace is not focused: a resident starts in the
	// background, under the daemon, not in front of whoever is at herdr.
	raw, err := h.call(ctx, "workspace.create", map[string]any{
		"cwd": dir, "label": ResidentLabel(yard, r.Name), "focus": false,
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
	// resident's pane as the resident's, whatever actor it names, which is
	// what keeps a resident from releasing a peer's bead as a user
	// (store.paneResident).
	h.shellOn(ctx, ws.RootPane.ID, handle)

	start := []string{"agent", "start", name, "--kind", str(r.Config, "kind", "claude"), "--pane", ws.RootPane.ID}
	if extra := agentArgs(r.Config, ""); len(extra) > 0 {
		start = append(append(start, "--"), extra...)
	}
	return h.startAgent(ctx, start, name, residentPrompt(h.yard(), r), handle, trusts(r.Config))
}

// envFile reads the env_file cfg names, if any, for the pane environment of
// who ("resident mayor", "group builders"). A file that is missing, not a
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

// residentEnv is what r's settings add to its pane environment: its
// env_file's entries and those its env_command prints (see
// config.ReadResidentEnv), asked for at every start. Either one refused (a
// file open to others, a command that fails, hangs or prints a line that is
// no KEY=VALUE) is Blocked, as a group's env_file is: the resident must not
// start without its credentials. Only key names are logged.
func (h *Herdr) residentEnv(ctx context.Context, r store.Resident, dir string) ([]string, error) {
	env, err := config.ReadResidentEnv(ctx, r.Name, dir, h.YardHome, r.Config)
	if err != nil {
		if ctx.Err() != nil {
			// The daemon is stopping: nothing a human has to fix.
			return nil, err
		}
		return nil, dispatch.Blocked(fmt.Errorf("resident %s: %w", r.Name, err))
	}
	if h.Log != nil && env.Set() {
		h.Log.Debug("resident environment loaded", "for", "resident "+r.Name, "from", env.From(), "keys", env.Keys())
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
// uncommitted change. A dir rig's bead has no checkout to ask; there only
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
