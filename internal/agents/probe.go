package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ProbeTimeout bounds one probe: the agent starts, sends one request and
// prints the answer or the refusal.
const ProbeTimeout = 90 * time.Second

// probePrompt is what a probe asks: the shortest request that still reaches
// the model, since an agent only learns on its first request that it may
// not use a model.
const probePrompt = "Reply with the single word ok."

// Probe is the outcome of asking an agent for one answer from a model.
type Probe struct {
	// Cmd is what ran, as a shell would read it; "" when the kind has no
	// one-shot mode, and then nothing else is set.
	Cmd string
	// Checked is false when the probe says nothing about the model: the
	// agent is not installed here, or did not answer in time.
	Checked bool
	// OK reports that the agent answered with the model.
	OK bool
	// Detail is the tail of the agent's output when it refused, or why the
	// probe could not run.
	Detail string
}

// probeArgs is the one-shot command of a kind for model, nil when the kind
// has none. Each was run by hand against a model that does not exist: both
// exit non-zero with the provider's refusal and neither falls back to
// another model.
func probeArgs(kind, model string) []string {
	switch kind {
	case "claude":
		return []string{"claude", "-p", "--model", model, probePrompt}
	case "codex":
		// Outside a repository, nothing kept, nothing writable: the probe
		// runs wherever the operator happens to stand.
		return []string{"codex", "exec", "-m", model, "--skip-git-repo-check", "--ephemeral",
			"-s", "read-only", probePrompt}
	}
	return nil
}

// ProbeModel asks the installed agent of kind for one answer from model. A
// model the agent only lists is not thereby usable, and one it does not
// list may be, so the list is not consulted: the request is made. It costs
// one short request and a few seconds.
func ProbeModel(ctx context.Context, kind, model string) Probe {
	args := probeArgs(kind, model)
	if args == nil {
		return Probe{}
	}
	p := Probe{Cmd: strings.Join(args[:len(args)-1], " ")}
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// Not the caller's directory: an agent reads the project it starts in.
	cmd.Dir = os.TempDir()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// Do not wait past the deadline on a child still holding the output.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		p.Checked, p.OK = true, true
	case ctx.Err() != nil:
		p.Detail = fmt.Sprintf("no answer within %s", ProbeTimeout)
	case errors.As(err, &exit):
		p.Checked = true
		p.Detail = clip(Tail(out.String(), 4), 300)
		if p.Detail == "" {
			p.Detail = err.Error()
		}
	default:
		// Not started at all: not installed, not executable.
		p.Detail = err.Error()
	}
	return p
}

// Tail is the last n lines of text that say something: blank lines and
// trailing space are dropped, since a terminal's output is mostly both.
func Tail(text string, n int) string {
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

// clip cuts each line of text to at most n runes: an agent's notice can be
// a paragraph on one line, and the refusal is in the lines, not their ends.
func clip(text string, n int) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if r := []rune(l); len(r) > n {
			lines[i] = string(r[:n]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}
