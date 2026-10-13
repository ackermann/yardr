package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// requirements are the programs a yard needs beside its harness: what for,
// as the onboarding and yardr doctor say it when one is not on PATH, and
// where to get it. Nothing here is run by yardr: the text is for the person.
var requirements = []struct{ exe, need, get string }{
	{"herdr", "it starts every agent in a pane of its own, the yardmaster first: without it nothing runs",
		"https://herdr.dev"},
	{"git", "a depot is a git repository: beads are worked on branches, in worktrees",
		"https://git-scm.com/downloads (on macOS: xcode-select --install)"},
}

// need is one program this machine must have for the yard, and whether it
// has it. It is what yardr doctor prints as its first part and what the
// onboarding asks about before its first page.
type need struct {
	Program string `json:"program"`
	// For is what the yard needs it for; for a harness, who runs on it.
	For   string `json:"for"`
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
	// Get is where to get it, when it is not found.
	Get string `json:"get,omitempty"`
	// Harness is the herdr kind the program is the harness of; "" for git
	// and herdr.
	Harness string `json:"harness,omitempty"`
	// HarnessFile is the version of the file yardr reads the harness's kind
	// from (yardr harness show); 0 for a kind it has none for.
	HarnessFile int `json:"harness_file,omitempty"`
	// HarnessAlways are the flags yardr always adds for this harness kind.
	HarnessAlways []string `json:"harness_always,omitempty"`
	// Optional is a harness nothing in the yard runs on: one of those a
	// home with no yard could be created on. What is wrong with it is said,
	// and is no fault.
	Optional bool `json:"optional,omitempty"`
	// Version is what `<program> --version` came to, for a harness that was
	// found and asked (see probe).
	Version *versionCheck `json:"version,omitempty"`
	// LoginPath is where the user's login shell finds a program this shell's
	// PATH does not have (see loginShellFinds); "" when it does not either,
	// or could not be asked.
	LoginPath string `json:"login_shell_path,omitempty"`
	// Server is whether herdr's server answers, for a herdr that was found
	// (see herdrServer).
	Server *serverCheck `json:"server,omitempty"`
	// Try is what one real call to a harness came to, under doctor --try
	// (see try).
	Try *tryCheck `json:"try,omitempty"`
}

// The states of a serverCheck.
const (
	herdrUp      = "running"
	herdrDown    = "not_running"
	herdrUnknown = "unknown"
)

// serverCheck is what asking herdr for its server came to. herdr on PATH is
// not a server that runs: without one the daemon starts no agent, and every
// start fails with herdr's server_not_running.
type serverCheck struct {
	State string `json:"state"` // running not_running unknown
	// Output is what herdr said when its server is not running, or why it
	// could not be asked.
	Output string `json:"output,omitempty"`
	// Session is the herdr session that was asked: the one the yard's
	// herdr_session setting names, else this shell's own, which need not
	// be the daemon's.
	Session *runner.HerdrSession `json:"session,omitempty"`
}

// The states of a tryCheck.
const (
	tryOK       = "ok"
	tryFails    = "fails"
	tryHangs    = "hangs"
	tryNotTried = "not_tried"
)

// tryCheck is what one real call to a harness came to (see try).
type tryCheck struct {
	State string `json:"state"` // ok fails hangs not_tried
	// Output is the last line of its answer when it is ok, the last lines it
	// printed (or the error) when it fails or hangs, and why when it was not
	// tried.
	Output string `json:"output,omitempty"`
	// err is how a call that fails ended, and printed the last lines it
	// printed when it fails or hangs, one a line: what Output joins, for a
	// text that shows them apart (askHarness).
	err, printed string
}

// The states of a versionCheck.
const (
	versionAnswers    = "answers"
	versionFails      = "fails"
	versionHangs      = "hangs"
	versionNotChecked = "not_checked"
)

// versionCheck is what running a harness's version flag came to. On PATH is
// not "works": a binary can be there and never start (see probe).
type versionCheck struct {
	State string `json:"state"` // answers fails hangs not_checked
	// Output is the first line it printed: the version when it answers,
	// what it said (or the error) when it fails.
	Output string `json:"output,omitempty"`
}

// harnessUse is a harness kind and who in the yard runs on it.
type harnessUse struct {
	kind string
	by   []string
}

// basicNeeds are the requirements, each found or not: all the onboarding
// asks about, and the head of doctor's list.
func basicNeeds() []need {
	var out []need
	for _, r := range requirements {
		n := need{Program: r.exe, For: r.need, Get: r.get}
		if p, err := lookPath(r.exe); err == nil {
			n.Found, n.Path, n.Get = true, p, ""
		}
		out = append(out, n)
	}
	return out
}

// harnessNeed is the harness of kind, found or not, not yet asked for its
// version.
func harnessNeed(kind, what string) need {
	n := need{Program: agents.Executable(kind), For: what, Harness: kind, Get: agents.InstallHint(kind)}
	if h, ok := agents.Lookup(kind); ok {
		n.HarnessFile = h.Version
		n.HarnessAlways = h.Flags.Always
	}
	if p, err := lookPath(n.Program); err == nil {
		n.Found, n.Path, n.Get = true, p, ""
	}
	return n
}

// installed reports whether kind's harness is on PATH.
func installed(kind string) bool {
	_, err := lookPath(agents.Executable(kind))
	return err == nil
}

// forNewYard is what a harness is needed for on a home with no yard yet.
const forNewYard = "a harness a yard can be created on"

// machineNeeds is everything the yard needs on this machine: the
// requirements, then a harness per use, each one found asked for its
// version, and herdr for its server. With no uses (a home with no yard yet)
// it is the harnesses that are installed, any of which a yard can be created
// on, or one line saying that none is. What is not on PATH is looked for
// with the user's login shell, once. With try, each harness that answers its
// one-shot form is given one real call (see try), regardless of how its
// version flag came to.
func machineNeeds(ctx context.Context, uses []harnessUse, try bool) []need {
	out := basicNeeds()
	first := len(out)
	for _, u := range uses {
		out = append(out, harnessNeed(u.kind, "the harness of "+strings.Join(u.by, ", ")))
	}
	if len(uses) == 0 {
		for _, k := range agents.Kinds() {
			if installed(k) {
				n := harnessNeed(k, forNewYard)
				n.Optional = true
				out = append(out, n)
			}
		}
	}
	// One run of the login shell for everything missing. With no harness at
	// all, for those a yard is offered: one of them in a directory this
	// shell's PATH lacks is what the person has to hear of.
	none := len(uses) == 0 && len(out) == first
	var missing []string
	for _, n := range out {
		if !n.Found {
			missing = append(missing, n.Program)
		}
	}
	if none {
		for _, k := range presetKinds() {
			missing = append(missing, agents.Executable(k))
		}
	}
	finds := loginShellFinds(ctx, missing)
	for i := range out {
		out[i].LoginPath = finds[out[i].Program]
	}
	if none {
		for _, k := range presetKinds() {
			if p := finds[agents.Executable(k)]; p != "" {
				n := harnessNeed(k, forNewYard)
				n.Optional, n.LoginPath = true, p
				out = append(out, n)
			}
		}
		if len(out) == first {
			out = append(out, need{Program: "a harness", For: "the agent yardr starts: a yard is created on one",
				Get: "install one of the agents herdr starts (" + strings.Join(presetKinds(), ", ") + ", ...)"})
		}
	}
	// Side by side: a harness that hangs costs the whole timeout.
	var wg sync.WaitGroup
	for i := range out {
		n := &out[i]
		switch {
		case !n.Found:
		case n.Program == "herdr":
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := herdrServer(ctx, n.Path)
				in := runner.SessionInUse()
				s.Session = &in
				n.Server = &s
			}()
		case n.Harness != "":
			wg.Add(1)
			go func() {
				defer wg.Done()
				v := probe(ctx, n.Harness, n.Path)
				n.Version = &v
				if try {
					t := tryHarnessCall(ctx, n.Harness, n.Path)
					n.Try = &t
				}
			}()
		}
	}
	wg.Wait()
	return out
}

// herdrServer asks the herdr at path whether its server answers; tests
// replace it. It is given the time a harness gets for its version.
var herdrServer = func(ctx context.Context, path string) serverCheck {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	down, err := (&runner.Herdr{Bin: path}).ServerUp(ctx)
	switch {
	case err == nil:
		return serverCheck{State: herdrUp}
	case down:
		return serverCheck{State: herdrDown, Output: err.Error()}
	}
	return serverCheck{State: herdrUnknown, Output: lastLines(err.Error(), 1)}
}

// loginShell is the user's login shell, "" when it is not known; tests
// replace it.
var loginShell = func() string { return os.Getenv("SHELL") }

// loginShellTimeout is how long the login shell gets to read its profile
// and answer; tests shorten it.
var loginShellTimeout = 5 * time.Second

// plainName is a program name that can stand in a shell command as it is.
var plainName = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// loginShellFinds asks the user's login shell where each of programs is,
// in one run, and returns those it finds by name. A shell without a
// terminal (ssh with a command, a service) reads no profile, so its PATH
// can lack a directory the person's own terminal has: ~/.local/bin, where
// claude and codex install themselves, is added by ~/.profile on Ubuntu. The
// shell is run as a login shell and not as an interactive one, with stdin
// closed, so it asks nothing. Whatever keeps it from answering (no SHELL, a
// shell that does not take -l -c, a profile that hangs) yields nothing: the
// report then says what it says without it.
func loginShellFinds(ctx context.Context, programs []string) map[string]string {
	shell := loginShell()
	var asks []string
	asked := map[string]bool{}
	for _, p := range programs {
		if plainName.MatchString(p) && !asked[p] {
			asked[p] = true
			asks = append(asks, "command -v "+p)
		}
	}
	if shell == "" || len(asks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", strings.Join(asks, "; "))
	ownGroup(cmd)
	// The status is that of the last program asked for, found or not: what
	// was printed is the answer.
	out, _ := cmd.Output()
	if ctx.Err() != nil {
		return nil
	}
	finds := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		// A profile may print what it likes: a find is an absolute path to
		// a file that can be run, named as a program that was asked for.
		l = strings.TrimSpace(l)
		name := filepath.Base(l)
		if !filepath.IsAbs(l) || !asked[name] {
			continue
		}
		if fi, err := os.Stat(l); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			finds[name] = l
		}
	}
	return finds
}

// ownGroup gives cmd a process group of its own, killed whole when its
// context ends: a wrapper script's child would otherwise keep the output
// pipe open past the timeout.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
}

// tryTimeout is how long a harness gets for its one real call; tests
// shorten it. An answer of one word comes in a few seconds; the rest is
// room for a cold start and a slow model.
var tryTimeout = 45 * time.Second

// tryPrompt is what the one real call asks: as little as a model can be
// asked for.
const tryPrompt = "Reply with the one word ok and nothing else."

// try makes one real call to the harness at path: its one-shot form (see
// agents.OneShotArgs) with a prompt that asks for one word, stdin closed, in
// an empty directory of its own so that it reads nobody's project. On PATH
// and answering a version flag is not "works": a harness can be logged out,
// hold a token that is spent while its own status says logged in, or be a
// version that lacks what it is started with, and a session started on it
// then sits at a screen nobody sees. This costs a model call, so doctor
// makes it only when asked (--try), and the onboarding only on a yes
// (askHarness). A kind with no one-shot form known is not run. A variable
// so that no test makes the call unasked: a test of it puts it back.
var tryHarnessCall = func(ctx context.Context, kind, path string) tryCheck {
	args := agents.OneShotArgs(kind, tryPrompt)
	if args == nil {
		return tryCheck{State: tryNotTried, Output: "yardr knows no one-shot call for " + kind}
	}
	dir, err := os.MkdirTemp("", "yardr-try")
	if err != nil {
		return tryCheck{State: tryNotTried, Output: err.Error()}
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, tryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	ownGroup(cmd)
	// The answer is what it prints on stdout; what went wrong can be on
	// either, so a failure shows the end of both as they came.
	var answer bytes.Buffer
	all := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&answer, all), all
	err = cmd.Run()
	printed := tailLines(all.String(), 3)
	switch {
	case ctx.Err() != nil:
		return tryCheck{State: tryHangs, Output: strings.Join(printed, " / "), printed: strings.Join(printed, "\n")}
	case err != nil:
		return tryCheck{State: tryFails, Output: strings.TrimSuffix(err.Error()+": "+strings.Join(printed, " / "), ": "),
			err: err.Error(), printed: strings.Join(printed, "\n")}
	}
	return tryCheck{State: tryOK, Output: lastLines(answer.String(), 1)}
}

// lockedBuffer is a buffer two of a command's streams write to.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// lastLines are the last n lines of out that are not blank, on one line,
// each cut to a length a report line carries: where a program that fails
// says why.
func lastLines(out string, n int) string {
	return strings.Join(tailLines(out, n), " / ")
}

// tailLines are the lines lastLines joins, one each.
func tailLines(out string, n int) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 160 {
				l = string(r[:160]) + "…"
			}
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func presetKinds() []string {
	var out []string
	for _, p := range agents.Harnesses {
		out = append(out, p.Kind)
	}
	return out
}

// versionTimeout is how long a harness gets to print its version; tests
// replace it. A harness answers in well under a second; the rest is room
// for a cold start.
var versionTimeout = 5 * time.Second

// goos is the system doctor explains things for; tests replace it.
var goos = runtime.GOOS

// probe runs the harness's version flag at path and says how it ended: it
// answers, fails, or hangs. A program on PATH can be one that never starts:
// on macOS a quarantined binary waits on a Gatekeeper dialog nobody sees,
// and a session started on it sits dead. This runs a program but changes
// nothing. A kind yardr knows no version flag for is not run at all.
func probe(ctx context.Context, kind, path string) versionCheck {
	args := agents.VersionArgs(kind)
	if args == nil {
		return versionCheck{State: versionNotChecked}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	ownGroup(cmd)
	out, err := cmd.CombinedOutput()
	line := versionLine(string(out))
	switch {
	case ctx.Err() != nil:
		return versionCheck{State: versionHangs, Output: line}
	case err != nil:
		var exit *exec.ExitError
		if line == "" || !errors.As(err, &exit) {
			line = strings.TrimSpace(err.Error() + ": " + line)
			line = strings.TrimSuffix(line, ":")
		} else {
			line = fmt.Sprintf("%v: %s", err, line)
		}
		return versionCheck{State: versionFails, Output: line}
	}
	return versionCheck{State: versionAnswers, Output: line}
}

// versionLine is the first line of what a version flag printed that is not
// blank, cut to a length a report line carries.
func versionLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 60 {
				l = string(r[:60]) + "…"
			}
			return l
		}
	}
	return ""
}

// harnessUses are the harness kinds the yard runs on, each with who names
// it: the default_kind first, then the herdr groups and crew members in the
// store's order. A group or crew member whose config names no kind runs on
// nothing yardr can name, and is left out.
func harnessUses(def string, groups []store.Group, crew []store.CrewMember) []harnessUse {
	var out []harnessUse
	add := func(kind, by string) {
		if kind == "" {
			return
		}
		for i := range out {
			if out[i].kind == kind {
				out[i].by = append(out[i].by, by)
				return
			}
		}
		out = append(out, harnessUse{kind, []string{by}})
	}
	add(def, "the yard's default")
	for _, r := range crew {
		if kind, _ := r.Config["kind"].(string); r.Runner == "herdr" {
			add(kind, "crew member "+r.Name)
		}
	}
	for _, g := range groups {
		if kind, _ := g.Config["kind"].(string); g.Runner == "herdr" {
			add(kind, "group "+g.Name)
		}
	}
	return out
}

// protectedDirs are the folders of a macOS home a launchd agent may not
// read without its own grant (Files and Folders, in Privacy & Security). A
// daemon started in a terminal has the terminal's.
var protectedDirs = []string{"Desktop", "Documents", "Downloads"}

// protectedDepots are the depots whose path is in one of userHome's protected
// folders, as "<depot> (<path>)".
func protectedDepots(depots []store.Depot, userHome string) []string {
	var out []string
	for _, r := range depots {
		for _, d := range protectedDirs {
			if rel, err := filepath.Rel(filepath.Join(userHome, d), r.Path); r.Path != "" && err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				out = append(out, fmt.Sprintf("%s (%s)", r.Name, r.Path))
			}
		}
	}
	return out
}
