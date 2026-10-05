package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// EnvCommandTimeout bounds a resident's env_command: a provider that waits
// for someone (a locked vault asking for a password) must not hold up the
// daemon's pass.
var EnvCommandTimeout = 10 * time.Second

// envCommandMax bounds what an env_command may print: an environment, not a
// stream.
const envCommandMax = 1 << 20

// ResidentEnv is a resident's environment as its settings give it: the
// entries of its env_file and those its env_command printed. It is the one
// answer to "what does this resident run with" for the pane its session
// starts in, the daemon's calls as the resident, `yardr resident show` and
// a pack's plan, so the four cannot disagree. Values are held in memory
// only, and asked for again at every use: nothing here is cached.
type ResidentEnv struct {
	// File is the env_file's resolved path, Command the env_command as one
	// line; empty when the resident has none. Both are shown to whoever may
	// read the resident, so neither is a place for a secret.
	File, Command string
	// FileKeys and CommandKeys are the names each set, for showing where a
	// name comes from without showing a value.
	FileKeys, CommandKeys []string
	// FileErr and CommandErr say why a source was refused. A resident with
	// either does not start.
	FileErr, CommandErr error
	// Env is the entries as KEY=VALUE: the file's, then the command's. A
	// key in both has the command's value.
	Env []string
}

// Set reports whether the resident names an env_file or an env_command.
func (e *ResidentEnv) Set() bool { return e.File != "" || e.Command != "" }

// From names where the environment comes from, for a message: "env_file
// /p", "env_command op inject ...", or both.
func (e *ResidentEnv) From() string {
	var from []string
	if e.File != "" {
		from = append(from, "env_file "+e.File)
	}
	if e.Command != "" {
		from = append(from, "env_command "+e.Command)
	}
	return strings.Join(from, " with ")
}

// Keys are the names the environment sets, each once, in Env's order.
func (e *ResidentEnv) Keys() []string {
	keys := make([]string, 0, len(e.Env))
	seen := map[string]bool{}
	for _, kv := range e.Env {
		if k, _, _ := strings.Cut(kv, "="); !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// Get is the value of name: of a name set twice the last, as in the pane's
// environment; "" for one not set.
func (e *ResidentEnv) Get(name string) string {
	val := ""
	for _, kv := range e.Env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			val = v
		}
	}
	return val
}

// StringList reads a setting that is a list of words: a list as it is, a
// string split at white space (no quoting: a word with a space in it takes
// the list form). It is how a group's or resident's args are read, and an
// env_command.
func StringList(v any) []string {
	switch v := v.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case []string:
		return v
	case string:
		return strings.Fields(v)
	}
	return nil
}

// ReadResidentEnv reads the environment of resident name from its settings
// cfg: its env_file (ReadEnvFile) and what its env_command prints, the
// command's value winning for a key in both. The command is run without a
// shell, in dir (the resident's directory; the yard's home when empty),
// with the caller's environment plus the resident's name and the yard's
// home, its stdin closed, for at most EnvCommandTimeout. Its stdout is read
// as an env_file is.
//
// The result is never nil and says what each source set or why it was
// refused; the error is the first of those. An error names the file or the
// command, a line number, an exit status and the last lines of the
// command's stderr: never its stdout, never a value.
func ReadResidentEnv(ctx context.Context, name, dir, home string, cfg map[string]any) (*ResidentEnv, error) {
	out := &ResidentEnv{}
	var file, cmd []string
	if path, _ := cfg["env_file"].(string); path != "" {
		out.File = path
		if p, err := ExpandPath(path); err == nil {
			out.File = p
		}
		f, err := ReadEnvFile(path)
		if err != nil {
			out.FileErr = err
		} else {
			file, out.FileKeys = f.Env, f.Keys()
		}
	}
	if argv := StringList(cfg["env_command"]); len(argv) > 0 {
		out.Command = strings.Join(argv, " ")
		if dir == "" {
			dir = home
		}
		cmd, out.CommandErr = runEnvCommand(ctx, argv, name, dir, home)
		out.CommandKeys = (&EnvFile{Env: cmd}).Keys()
	}
	if err := firstErr(out.FileErr, out.CommandErr); err != nil {
		return out, err
	}
	theirs := map[string]bool{}
	for _, k := range out.CommandKeys {
		theirs[k] = true
	}
	for _, kv := range file {
		if k, _, _ := strings.Cut(kv, "="); !theirs[k] {
			out.Env = append(out.Env, kv)
		}
	}
	out.Env = append(out.Env, cmd...)
	return out, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// EnvCommandMissing reports why the program of cfg's env_command cannot be
// found from dir, without running it; nil when it can, or cfg names none.
func EnvCommandMissing(dir string, cfg map[string]any) error {
	argv := StringList(cfg["env_command"])
	if len(argv) == 0 {
		return nil
	}
	prog, err := envProgram(argv[0], dir)
	if err == nil {
		_, err = exec.LookPath(prog)
	}
	if err != nil {
		return fmt.Errorf("env_command %s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// envProgram is the program an env_command names: a leading ~/ is the
// user's home, as in an env_file's path, and a relative path with a
// directory in it is taken from dir, where the command runs. A bare name is
// looked up on PATH.
func envProgram(prog, dir string) (string, error) {
	if prog == "~" || strings.HasPrefix(prog, "~/") {
		return ExpandPath(prog)
	}
	if strings.ContainsRune(prog, filepath.Separator) && !filepath.IsAbs(prog) && dir != "" {
		return filepath.Join(dir, prog), nil
	}
	return prog, nil
}

// runEnvCommand runs argv and reads its stdout as an env_file.
func runEnvCommand(ctx context.Context, argv []string, resident, dir, home string) ([]string, error) {
	what := "env_command " + strings.Join(argv, " ")
	prog, err := envProgram(argv[0], dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	ctx, cancel := context.WithTimeout(ctx, EnvCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), Env("RESIDENT", resident)...)
	if home != "" {
		cmd.Env = append(cmd.Env, Env("HOME", home)...)
	}
	stdout, stderr := &capped{max: envCommandMax}, &tail{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// The whole group is killed at the timeout, and the pipes are given up
	// a moment later: a provider that is a script leaves a child behind
	// (the tool it waits on) which would hold them open otherwise.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%s: no answer within %s%s", what, EnvCommandTimeout, stderr.lines())
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", what, ctx.Err())
	case err != nil:
		// An exit status, or why it could not be run: neither holds
		// anything the command printed.
		return nil, fmt.Errorf("%s: %w%s", what, err, stderr.lines())
	case stdout.over:
		return nil, fmt.Errorf("%s: printed more than %d bytes", what, envCommandMax)
	}
	return parseEnv(&stdout.buf, what)
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
