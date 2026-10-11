package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// commandKillAgain is how long after the kill of a command's group the
// group is killed again: time for a child that was being forked to be there.
const commandKillAgain = 50 * time.Millisecond

// Command is a program the operator named in a setting, which the yard runs
// for what it prints: a crew member's env_command, the command of a source
// that says a subscription's limits. It is run without a shell, its stdin
// closed unless Stdin says what it reads, in a process group of its own
// that is killed whole when Timeout is over, so that nothing of it is left
// behind.
type Command struct {
	// What names the command in an error: the setting and the argv.
	What string
	// Argv is the program and its arguments. The program is found as
	// envProgram says: a leading ~/ is the user's home, a relative path with
	// a directory in it is from Dir, a bare name is looked up on PATH.
	Argv []string
	// Dir is where it runs.
	Dir string
	// Env is its environment; nil is the caller's.
	Env []string
	// Stdin is what it reads; nil is a closed stdin.
	Stdin []byte
	// Timeout bounds the run, and Max what it may print on stdout, in bytes.
	Timeout time.Duration
	Max     int
}

// Output runs the command and returns what it printed on stdout. An error
// names the command, an exit status or the timeout, and the last lines of
// its stderr: never its stdout.
func (c Command) Output(ctx context.Context) ([]byte, error) {
	out, _, err := c.OutputWithStderr(ctx)
	return out, err
}

// OutputWithStderr runs the command like Output and also returns the last
// lines of stderr on success. This lets a caller report a successful process
// that printed an invalid answer without ever exposing its stdout.
func (c Command) OutputWithStderr(ctx context.Context) ([]byte, string, error) {
	prog, err := envProgram(c.Argv[0], c.Dir)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", c.What, err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, c.Argv[1:]...)
	cmd.Dir, cmd.Env = c.Dir, c.Env
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	stdout, stderr := &capped{max: c.Max}, &tail{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// The whole group is killed at the timeout, and the pipes are given up
	// a moment later: a command that is a script leaves a child behind
	// (the tool it waits on) which would hold them open otherwise.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	// One kill is not enough: a child the command's shell is forking as
	// the kill lands is missed by it (seen on macOS) and left running, in
	// the group, when the shell is gone. It is there a moment later, so
	// the group is killed again then, and once more when the pipes have
	// been given up, for a child that took longer to appear.
	cmd.Cancel = func() error {
		err := kill()
		time.Sleep(commandKillAgain)
		kill()
		return err
	}
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		kill()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, "", fmt.Errorf("%s: no answer within %s%s", c.What, c.Timeout, stderr.lines())
	case ctx.Err() != nil:
		return nil, "", fmt.Errorf("%s: %w", c.What, ctx.Err())
	case err != nil:
		// An exit status, or why it could not be run: neither holds
		// anything the command printed.
		return nil, "", fmt.Errorf("%s: %w%s", c.What, err, stderr.lines())
	case stdout.over:
		return nil, "", fmt.Errorf("%s: printed more than %d bytes%s", c.What, c.Max, stderr.lines())
	}
	return stdout.buf.Bytes(), stderr.lines(), nil
}

// Missing reports why the command's program cannot be found, without
// running it; nil when it can.
func (c Command) Missing() error {
	prog, err := envProgram(c.Argv[0], c.Dir)
	if err == nil {
		_, err = exec.LookPath(prog)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", c.What, err)
	}
	return nil
}

// capped keeps the first max bytes written to it and notes that there were
// more.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); len(p) > room {
		c.over = true
		c.buf.Write(p[:max(room, 0)])
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// lines is the last few lines kept, as the end of an error: what the
// command said went wrong. Empty when it said nothing.
func (t *tail) lines() string {
	const keep = 5
	var out []string
	for _, l := range strings.Split(string(t.buf), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > keep {
		out = out[len(out)-keep:]
	}
	return "; its stderr ended: " + strings.Join(out, " | ")
}
