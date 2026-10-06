// Package rigcheck finds and runs a depot's merge gate: the command that must
// exit 0 before a bead's branch may land on the depot's base.
//
// The gate comes from the store (`yardr depot add --check <cmd>`, the
// operator's override) or else from the repo: .yardr/check at the tip of the
// depot's base branch. It is never read from the bead's branch or worktree, so
// a bead cannot lower its own bar; an edit to .yardr/check counts only once
// it has landed on the base.
//
// When the base has a remote, the check is read from the remote-tracking
// ref after fetching it, not from the local branch: refs are shared by every
// worktree of the depot, so a bead could `git update-ref` the local base to a
// commit of its own choosing. A depot without a remote reads the local base.
//
// A child of a train lands on the train's branch, not the base, but its gate
// is still the base's: read from the base exactly as above, and run on the
// child's branch. So a train cannot change the bar its children land under.
//
// A depot with no gate at all fails ("no gate") unless it opts out with
// allow_no_check: a missing file must not read as a passing one.
//
// A dir depot is not a repository and has no base: its .yardr/check is read
// from the depot's directory as it is, and run in place. Without one a dir depot
// passes ("no gate"), the opposite of a git depot on purpose: nothing of a dir
// depot's merges anywhere, so there is no bar to lower.
//
// A depot may have a second, light gate (RunLight): `--check-light <cmd>` in
// the store, or .yardr/check-light in the repo, found and run under every
// rule above. It is for a branch that lands where the full gate still
// stands before the base moves: a wagon of a train, landing on the train's
// branch (docs/pipeline.md, "Wagons"). A depot without one runs its full gate
// instead and says so (Result.Fallback): asking for the light gate never
// passes for want of a file. The light gate comes from where the full one
// does: a depot whose gate is a store override has its light gate in the store
// too, and a .yardr/check-light in its repo is not read.
//
// No more than N full gates run at once on the machine (slots.go): RunWith
// takes a slot before the check starts and holds it until the check ends.
//
// Either gate is told what it is compared with: YARDR_CHECK_BASE is the
// commit the repo's gate was read at (the tip of the base, as fetched), so
// a check can look at what the branch changed (git diff against the merge
// base with it). Under a store override it is the depot's base as it resolves
// locally, and it is unset when that does not resolve and for a dir depot: a
// check that needs it must fail without it.
//
// The light gate of a wagon of a train is compared with the train's branch
// instead (Options.Since): the range from the depot's base holds the train's
// earlier wagons, so every wagon after one that touched a widely imported
// package would test nearly everything again. Only what the gate is compared
// with changes: the gate is still read from the base, and the train's own
// full gate runs everything before the base moves. The full gate, the one
// run for want of a light one included, is always compared with the base.
package rigcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// RepoPath is where a depot keeps its check, relative to the repo root.
const RepoPath = ".yardr/check"

// LightPath is where a depot keeps its light check, beside the full one.
const LightPath = ".yardr/check-light"

// checkEnv is what a check's command inherits of environ. An outer check's
// base (a gate whose tests run a gate) is not this one's: it is dropped,
// and set only when this check has one. The settings of the yard's server
// are dropped too: a gate's tests start servers of their own, and one that
// read this yard's webhook address would listen on it
// (config.WithoutServerSettings).
func checkEnv(environ []string) []string {
	return slices.DeleteFunc(config.WithoutServerSettings(environ), func(kv string) bool {
		return strings.HasPrefix(kv, BaseVar+"=")
	})
}

// BaseVar names the commit a check is compared with, in its environment.
const BaseVar = "YARDR_CHECK_BASE"

// FetchTimeout bounds the fetch of the base before the check is read: a
// dead remote must not hang the gate, and with it the merge queue.
var FetchTimeout = 60 * time.Second

// GitTimeout bounds each other git call made to find the gate, so a wedged
// repository (a stale lock, a hung filesystem) fails the run instead of
// hanging it.
var GitTimeout = 30 * time.Second

// DefaultTimeout bounds a check run when the caller sets no deadline of its
// own (yardr depot check --timeout).
const DefaultTimeout = 30 * time.Minute

// Check is a depot's gate as resolved right now.
type Check struct {
	// Source says where the gate came from: "store", "repo@<sha>" (the base
	// commit it was read from), "dir" (a dir depot's own file), or "" when
	// the depot has none.
	Source string `json:"source"`
	// From says which ref the repo's check was read from and how, e.g.
	// "origin/main (fetched)" or "main (no remote)".
	From string `json:"from,omitempty"`
	// Cmd is the store override, run with sh -c.
	Cmd string `json:"cmd,omitempty"`
	// Script is the content of .yardr/check at the base.
	Script []byte `json:"-"`
	// File is a dir depot's .yardr/check, run where it is.
	File string `json:"file,omitempty"`
	// Base is the commit the check is compared with (BaseVar); "" when
	// there is none. BaseRef names it: the ref it is the tip of.
	Base    string `json:"base,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	// Light: this is the depot's light gate. Fallback says, when the light
	// gate was asked for and the depot has none, why this is the full one.
	Light    bool   `json:"light,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	// FailPattern is the content of the depot's FailPatternPath, read where
	// the repo's gate is; empty when the depot has none.
	FailPattern []byte `json:"-"`
}

// None reports whether the depot has no gate.
func (c *Check) None() bool { return c.Source == "" }

// Resolve finds the depot's gate. The store override wins; otherwise
// .yardr/check is read from the base commit with git, not from any
// checkout, so whatever a bead's branch holds there does not matter.
func Resolve(ctx context.Context, depot store.Depot) (*Check, error) {
	return resolve(ctx, depot, false)
}

// ResolveLight finds the depot's light gate the same way (--check-light, else
// .yardr/check-light on the base), or its full gate, with Fallback set,
// when it has no light one.
func ResolveLight(ctx context.Context, depot store.Depot) (*Check, error) {
	return resolve(ctx, depot, true)
}

func resolve(ctx context.Context, depot store.Depot, light bool) (*Check, error) {
	switch {
	case light && depot.CheckLight != "":
		c := &Check{Source: "store", Cmd: depot.CheckLight, Base: localBase(ctx, depot), Light: true}
		c.nameBase(depot.Base)
		c.FailPattern = failPattern(ctx, depot, c.Base)
		return c, nil
	case depot.Check != "":
		// The pattern is still the repository's, at the base as it is
		// locally: the store has no setting for it, and it lowers no bar.
		c := &Check{Source: "store", Cmd: depot.Check, Base: localBase(ctx, depot)}
		c.nameBase(depot.Base)
		c.FailPattern = failPattern(ctx, depot, c.Base)
		if light {
			// The operator took the gate out of the repo's hands: a light
			// gate in the repo does not take it back.
			c.Fallback = fmt.Sprintf("depot %s has no light gate (its gate is a store override, with no --check-light beside it): running the full gate", depot.Name)
		}
		return c, nil
	}
	paths := []string{RepoPath}
	if light {
		paths = []string{LightPath, RepoPath}
	}
	if depot.IsDir() {
		return resolveDir(depot, paths)
	}
	ref, from, err := BaseRef(ctx, depot)
	if err != nil {
		return nil, err
	}
	sha, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("depot %s: base %q does not resolve: %w", depot.Name, ref, err)
	}
	sha = strings.TrimSpace(sha)
	for _, path := range paths {
		// ls-tree tells "no check" apart from a failing git: an absent file is
		// no gate, which Run decides about, not a broken one.
		if out, err := git(ctx, depot.Path, "ls-tree", "--name-only", sha, "--", path); err != nil {
			return nil, err
		} else if strings.TrimSpace(out) == "" {
			continue
		}
		script, err := git(ctx, depot.Path, "show", sha+":"+path)
		if err != nil {
			return nil, err
		}
		c := &Check{Source: "repo@" + sha[:min(12, len(sha))], From: from, Script: []byte(script), Base: sha, Light: path == LightPath}
		c.nameBase(strings.TrimPrefix(ref, "refs/remotes/"))
		c.FailPattern = failPattern(ctx, depot, sha)
		if light && !c.Light {
			c.Fallback = fmt.Sprintf("depot %s has no light gate (no --check-light, no %s on %s): running the full gate", depot.Name, LightPath, from)
		}
		return c, nil
	}
	return &Check{From: from}, nil
}

// nameBase names the commit the check is compared with, when it has one.
func (c *Check) nameBase(ref string) {
	if c.Base != "" {
		c.BaseRef = ref
	}
}

// since compares a light gate with the tip of branch, as it is in dir's
// repository, instead of the depot's base. A full gate keeps its base, and so
// does a light one whose branch does not resolve there: the wider range
// tests more, never less.
func (c *Check) since(ctx context.Context, dir, branch string) {
	if branch == "" || !c.Light {
		return
	}
	sha, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return
	}
	c.Base, c.BaseRef = strings.TrimSpace(sha), branch
}

// localBase is the depot's base as it resolves in the depot's checkout, with no
// fetch, or "": what a store override is compared with. The override never
// needed the repository to resolve, so a base that does not is no error.
func localBase(ctx context.Context, depot store.Depot) string {
	if depot.IsDir() || depot.Base == "" {
		return ""
	}
	sha, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", depot.Base+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sha)
}

// resolveDir finds a dir depot's check in its directory: the first of paths
// that is there.
func resolveDir(depot store.Depot, paths []string) (*Check, error) {
	for _, path := range paths {
		file := filepath.Join(depot.Path, filepath.FromSlash(path))
		info, err := os.Stat(file)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("depot %s: %w", depot.Name, err)
		case info.IsDir():
			return nil, fmt.Errorf("depot %s: %s is a directory, not a check", depot.Name, file)
		}
		script, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("depot %s: %w", depot.Name, err)
		}
		c := &Check{Source: "dir", Script: script, File: file, Light: path == LightPath}
		c.FailPattern = failPattern(context.Background(), depot, "")
		if len(paths) > 1 && !c.Light {
			c.Fallback = fmt.Sprintf("depot %s has no light gate (no --check-light, no %s in %s): running the full gate", depot.Name, LightPath, depot.Path)
		}
		return c, nil
	}
	return &Check{}, nil
}

// BaseRef is the ref the depot's repo config is read from, and a note on how
// it was chosen. When the base has a remote (depot.Base is "<remote>/<branch>",
// or a local branch whose upstream, or else origin, has it) the
// remote-tracking ref is fetched, bounded by FetchTimeout, with a forced
// refspec, so a tracking ref moved by hand is put back. A fetch that fails
// leaves the tracking ref as last fetched, and the note says so; only a depot
// with no remote, or whose remote lacks the branch, reads the local base.
func BaseRef(ctx context.Context, depot store.Depot) (ref, from string, err error) {
	remote, branch := remoteOf(ctx, depot)
	if remote == "" {
		return depot.Base, depot.Base + " (no remote)", nil
	}
	tracking := remote + "/" + branch
	ferr := fetch(ctx, depot.Path, remote, "+refs/heads/"+branch+":refs/remotes/"+tracking)
	if _, err := git(ctx, depot.Path, "rev-parse", "--verify", "--quiet", "refs/remotes/"+tracking+"^{commit}"); err != nil {
		if tracking == depot.Base {
			return "", "", fmt.Errorf("depot %s: base %q does not resolve (fetch: %v)", depot.Name, depot.Base, ferr)
		}
		return depot.Base, fmt.Sprintf("%s (%s has no %s)", depot.Base, remote, branch), nil
	}
	if ferr != nil {
		return "refs/remotes/" + tracking, fmt.Sprintf("%s (fetch failed, as last fetched: %v)", tracking, ferr), nil
	}
	return "refs/remotes/" + tracking, tracking + " (fetched)", nil
}

// remoteOf names the remote and branch the depot's base tracks, or "" when it
// has none (store.BaseRemote: a bead sent to a linked yard names the same).
func remoteOf(ctx context.Context, depot store.Depot) (remote, branch string) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()
	return store.BaseRemote(ctx, depot)
}

func fetch(ctx context.Context, repo, remote, refspec string) error {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--quiet", "--no-tags", remote, refspec)
	// A remote asking for credentials fails at once instead of waiting on a
	// terminal nobody watches.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	cmd.Stderr = &stderr
	// git may leave a child (ssh) holding its output open after it is
	// killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", FetchTimeout)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Result is one run of a check.
type Result struct {
	Depot  string `json:"depot"`
	Kind   string `json:"kind,omitempty"` // the depot's: git or dir
	Dir    string `json:"dir"`
	Source string `json:"source"`
	From   string `json:"from,omitempty"`
	// Light: the depot's light gate ran. Fallback: it was asked for, the depot
	// has none, and the full gate ran instead; the line says so.
	Light    bool   `json:"light,omitempty"`
	Fallback string `json:"light_fallback,omitempty"`
	// Base is the commit the check was given to compare with (BaseVar), and
	// BaseRef the ref it is the tip of: the depot's base, or, for the light
	// gate of a wagon of a train, the train's branch (Options.Since).
	Base    string `json:"base,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	// SlotWaited is how long the run waited for a slot of the machine, in
	// seconds; 0 when one was free, or it took none.
	SlotWaited float64 `json:"slot_waited_seconds,omitempty"`
	Pass       bool    `json:"pass"`
	// ExitCode is the check's exit status (-1 when it was killed).
	ExitCode int `json:"exit_code"`
	// TimedOut: the check ran past the caller's deadline and was killed,
	// with everything it started. That fails the gate.
	TimedOut bool `json:"timed_out,omitempty"`
	// Signal is the signal that ended the check, when one did and it was
	// not this package's own kill at the deadline: the check's process died
	// of it (ExitCode -1), or exited 128+Signal, as a shell does when a
	// command it ran was killed. It fails the gate, but says nothing about
	// the branch: the caller reports it apart from a red gate.
	Signal int    `json:"signal,omitempty"`
	Output string `json:"output"` // stdout and stderr, interleaved
	// Matched is, for a check that did not pass in a depot with a
	// FailPatternPath, the lines of Output matching it, with their context
	// (see Matching); MatchedMore is how many matching lines it left out.
	// PatternNotes name the lines of the pattern file that were skipped.
	Matched      string   `json:"matched,omitempty"`
	MatchedMore  int      `json:"matched_more,omitempty"`
	PatternNotes []string `json:"pattern_notes,omitempty"`
	// LogFile is where the whole Output of a check that did not pass was
	// kept (see Keep), and LogError why it could not be. The caller fills
	// them in: Run does not know the yard's home.
	LogFile  string `json:"log_file,omitempty"`
	LogError string `json:"log_error,omitempty"`
	// Stranded are the depot's open beads in a stage their flow lacks, which
	// fail the check whatever the gate said. The caller fills it in: Run
	// has no store.
	Stranded []store.StrandedBead `json:"stranded,omitempty"`
	// StageNotes name, per flow governing the depot, the non-terminal stages
	// without a stage file: in Dir for a repository flow, in the yard's
	// home for a store flow. They never fail the check: stage files are
	// optional. The caller fills them in, as it does StrayStageFiles.
	StageNotes []string `json:"stage_notes,omitempty"`
	// ShadowNotes name each file of the depot's repository that nothing reads
	// while a flow in the store, or a prompt from a pack or the yard's home,
	// stands in front of it. They never fail the check: shadowing is how a
	// pack governs a depot. The caller fills them in.
	ShadowNotes []string `json:"shadow_notes,omitempty"`
	// StrayStageFiles are the stage files in Dir named after no stage of
	// their flow, which fail the check whatever the gate said: nobody is
	// ever shown one, so nobody else would see the typo.
	StrayStageFiles []string `json:"stray_stage_files,omitempty"`
	// StrayHomeStageFiles are the same for the store flows governing the
	// depot, whose stage files are in the yard's home: no branch's to fix.
	StrayHomeStageFiles []string `json:"stray_home_stage_files,omitempty"`
}

// Run resolves the depot's gate and runs it in dir (a bead's worktree, or the
// depot itself when dir is empty). A failing check is a Result with Pass
// false, not an error; errors mean the check could not be run at all. A depot
// with no gate fails too, unless it sets AllowNoCheck or is a dir depot. A check still running
// at ctx's deadline is killed and fails (TimedOut); finding the gate past
// the deadline is an error.
func Run(ctx context.Context, depot store.Depot, dir string) (*Result, error) {
	return run(ctx, depot, dir, Options{})
}

// RunLight is Run for the depot's light gate, and for its full gate when it
// has no light one (Result.Fallback then says so).
func RunLight(ctx context.Context, depot store.Depot, dir string) (*Result, error) {
	return run(ctx, depot, dir, Options{Light: true})
}

// Options are how RunWith runs a gate.
type Options struct {
	// Light asks for the depot's light gate (RunLight).
	Light bool
	// Since is a branch the light gate is compared with instead of the depot's
	// base: the train's, for a wagon of one (see the package comment). The
	// caller finds it: this package has no store. It is read in the
	// directory the check runs in, and changes nothing for a full gate, a
	// dir depot, or a branch that is not there.
	Since string
	// Timeout bounds the check itself, from when it starts: finding the gate
	// and waiting for a slot are not counted. 0 leaves ctx's deadline alone.
	Timeout time.Duration
	// Slots is how the run takes its slot of the machine (see slots.go);
	// nil takes none. A light gate takes none either, unless the depot says
	// its light gate takes one (store.Depot.LightSlot): it is meant to be
	// cheap. The full gate run for want of a light one takes one. So does
	// no check started from inside a check that has one (SlotVar).
	Slots *Slots
}

// RunWith is Run with options. A wait for a slot that runs past its bound
// is an error wrapping ErrSlotWait: the check could not run.
func RunWith(ctx context.Context, depot store.Depot, dir string, o Options) (*Result, error) {
	return run(ctx, depot, dir, o)
}

func run(ctx context.Context, depot store.Depot, dir string, o Options) (*Result, error) {
	if dir == "" {
		dir = depot.Path
	}
	c, err := resolve(ctx, depot, o.Light)
	if err != nil {
		return nil, err
	}
	if !depot.IsDir() {
		c.since(ctx, dir, o.Since)
	}
	res := &Result{Depot: depot.Name, Kind: depot.Kind, Dir: dir, Source: c.Source, From: c.From,
		Light: c.Light, Fallback: c.Fallback, Base: c.Base, BaseRef: c.BaseRef}
	if c.None() {
		res.Pass = depot.AllowNoCheck || depot.IsDir()
		if !res.Pass {
			res.ExitCode = 1
			res.Output = fmt.Sprintf("no gate: depot %s has no --check and no %s on %s; add one, or set allow_no_check\n", depot.Name, RepoPath, c.From)
		}
		return res, nil
	}
	slot := false
	if o.Slots != nil && o.Slots.Limit > 0 && os.Getenv(SlotVar) == "" && (!c.Light || depot.LightSlot) {
		release, waited, err := o.Slots.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		slot, res.SlotWaited = true, waited.Seconds()
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	var cmd *exec.Cmd
	switch {
	case c.Cmd != "":
		cmd = exec.CommandContext(ctx, "sh", "-c", c.Cmd)
	case c.File != "":
		// A dir depot's check is a file already, with no git directory to
		// copy it to: run it in place, with sh unless it is executable.
		if info, err := os.Stat(c.File); err == nil && bytes.HasPrefix(c.Script, []byte("#!")) && info.Mode()&0o111 != 0 {
			cmd = exec.CommandContext(ctx, c.File)
		} else {
			cmd = exec.CommandContext(ctx, "sh", c.File)
		}
	default:
		// The script is a blob in git, not a file: write it out to run it.
		// It goes under the depot's git dir, not /tmp, which may be noexec,
		// and not the worktree, where it would show up as a change.
		path, err := writeScript(ctx, depot.Path, c.Script)
		if err != nil {
			return nil, err
		}
		defer os.Remove(path)
		if bytes.HasPrefix(c.Script, []byte("#!")) {
			cmd = exec.CommandContext(ctx, path)
		} else {
			cmd = exec.CommandContext(ctx, "sh", path)
		}
	}
	var out bytes.Buffer
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	// Under the name it had too (YARDR_RIG): a repository's check may read it.
	cmd.Env = append(checkEnv(os.Environ()), config.Env("DEPOT", depot.Name)...)
	if c.Base != "" {
		cmd.Env = append(cmd.Env, BaseVar+"="+c.Base)
	}
	if slot {
		cmd.Env = append(cmd.Env, SlotVar+"=1")
	}
	// The check runs in its own process group, killed whole at the
	// deadline: killing only sh would leave go test and its test binaries
	// running, holding the output open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	res.Output = out.String()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Pass = true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode, res.TimedOut = -1, true
		res.Output += "\nyardr: the check ran past its deadline and was killed\n"
	case ctx.Err() != nil:
		return nil, fmt.Errorf("run check: %w", ctx.Err())
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		res.Signal = signalOf(exitErr)
	default:
		return nil, fmt.Errorf("run check: %w", err)
	}
	if !res.Pass {
		c.matching(res)
	}
	return res, nil
}

// signalOf is the signal a check ended by, or 0: the one its process died
// of, or the N of an exit status 128+N, which is how a shell reports a
// command of its script killed by signal N.
func signalOf(err *exec.ExitError) int {
	if ws, ok := err.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return int(ws.Signal())
	}
	if n := err.ExitCode() - 128; n > 0 && n <= 64 {
		return n
	}
	return 0
}

func writeScript(ctx context.Context, repo string, script []byte) (string, error) {
	dir, err := GitPath(ctx, repo, "yardr")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "check-*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(script)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(f.Name(), 0o700)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// GitPath is `git rev-parse --git-path <name>` in dir, made absolute: a
// place inside the checkout's git directory (per worktree for a linked
// worktree) that git never tracks.
func GitPath(ctx context.Context, dir, name string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

// Tail is the last n lines of s.
func Tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
