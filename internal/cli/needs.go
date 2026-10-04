package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// requirements are the programs a yard needs beside its harness: what for,
// as the onboarding and yardr doctor say it when one is not on PATH, and
// where to get it. Nothing here is run by yardr: the text is for the person.
var requirements = []struct{ exe, need, get string }{
	{"herdr", "it starts every agent in a pane of its own, the mayor first: without it nothing runs",
		"https://herdr.dev"},
	{"git", "a rig is a git repository: beads are worked on branches, in worktrees",
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
	// Optional is a harness nothing in the yard runs on: one of those a
	// home with no yard could be created on. What is wrong with it is said,
	// and is no fault.
	Optional bool `json:"optional,omitempty"`
	// Version is what `<program> --version` came to, for a harness that was
	// found and asked (see probe).
	Version *versionCheck `json:"version,omitempty"`
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

// machineNeeds is everything the yard needs on this machine: the
// requirements, then a harness per use, each one found asked for its
// version. With no uses (a home with no yard yet) it is the harnesses that
// are installed, any of which a yard can be created on, or one line saying
// that none is.
func machineNeeds(ctx context.Context, uses []harnessUse) []need {
	out := basicNeeds()
	first := len(out)
	for _, u := range uses {
		out = append(out, harnessNeed(u.kind, "the harness of "+strings.Join(u.by, ", ")))
	}
	if len(uses) == 0 {
		for _, k := range agents.Kinds() {
			if installed(k) {
				n := harnessNeed(k, "a harness a yard can be created on")
				n.Optional = true
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
	for i := first; i < len(out); i++ {
		if !out[i].Found {
			continue
		}
		wg.Add(1)
		go func(n *need) {
			defer wg.Done()
			v := probe(ctx, n.Harness, n.Path)
			n.Version = &v
		}(&out[i])
	}
	wg.Wait()
	return out
}

func presetKinds() []string {
	var out []string
	for _, p := range agents.Presets {
		out = append(out, p.Kind)
	}
	return out
}

// versionTimeout is how long a harness gets to print its version; tests
// shorten it. A harness answers in well under a second; the rest is room
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
	// A group of its own, killed whole: a wrapper script's child would
	// otherwise keep the output pipe open past the timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
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
// it: the default_kind first, then the herdr groups and residents in the
// store's order. A group or resident whose config names no kind runs on
// nothing yardr can name, and is left out.
func harnessUses(def string, groups []store.Group, residents []store.Resident) []harnessUse {
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
	for _, r := range residents {
		if kind, _ := r.Config["kind"].(string); r.Runner == "herdr" {
			add(kind, "resident "+r.Name)
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

// protectedRigs are the rigs whose path is in one of userHome's protected
// folders, as "<rig> (<path>)".
func protectedRigs(rigs []store.Rig, userHome string) []string {
	var out []string
	for _, r := range rigs {
		for _, d := range protectedDirs {
			if rel, err := filepath.Rel(filepath.Join(userHome, d), r.Path); r.Path != "" && err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				out = append(out, fmt.Sprintf("%s (%s)", r.Name, r.Path))
			}
		}
	}
	return out
}
