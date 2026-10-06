package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvCommandTimeout bounds a crew member's env_command: a provider that waits
// for someone (a locked vault asking for a password) must not hold up the
// daemon's pass.
var EnvCommandTimeout = 10 * time.Second

// envCommandMax bounds what an env_command may print: an environment, not a
// stream.
const envCommandMax = 1 << 20

// CrewEnv is a crew member's environment as its settings give it: the
// entries of its env_file and those its env_command printed. It is the one
// answer to "what does this crew member run with" for the pane its session
// starts in, the daemon's calls as the crew member, `yardr crew show` and
// a pack's plan, so the four cannot disagree. Values are held in memory
// only, and asked for again at every use: nothing here is cached.
type CrewEnv struct {
	// File is the env_file's resolved path, Command the env_command as one
	// line; empty when the crew member has none. Both are shown to whoever may
	// read the crew member, so neither is a place for a secret.
	File, Command string
	// FileKeys and CommandKeys are the names each set, for showing where a
	// name comes from without showing a value.
	FileKeys, CommandKeys []string
	// FileErr and CommandErr say why a source was refused. A crew member with
	// either does not start.
	FileErr, CommandErr error
	// Env is the entries as KEY=VALUE: the file's, then the command's. A
	// key in both has the command's value.
	Env []string
}

// Set reports whether the crew member names an env_file or an env_command.
func (e *CrewEnv) Set() bool { return e.File != "" || e.Command != "" }

// From names where the environment comes from, for a message: "env_file
// /p", "env_command op inject ...", or both.
func (e *CrewEnv) From() string {
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
func (e *CrewEnv) Keys() []string {
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
func (e *CrewEnv) Get(name string) string {
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
// the list form). It is how a group's or crew member's args are read, and an
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

// ReadCrewEnv reads the environment of crew member name from its settings
// cfg: its env_file (ReadEnvFile) and what its env_command prints, the
// command's value winning for a key in both. The command is run without a
// shell, in dir (the crew member's directory; the yard's home when empty),
// with the caller's environment plus the crew member's name and the yard's
// home, its stdin closed, for at most EnvCommandTimeout. Its stdout is read
// as an env_file is.
//
// The result is never nil and says what each source set or why it was
// refused; the error is the first of those. An error names the file or the
// command, a line number, an exit status and the last lines of the
// command's stderr: never its stdout, never a value.
func ReadCrewEnv(ctx context.Context, name, dir, home string, cfg map[string]any) (*CrewEnv, error) {
	out := &CrewEnv{}
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
	return Command{What: "env_command " + strings.Join(argv, " "), Argv: argv, Dir: dir}.Missing()
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
func runEnvCommand(ctx context.Context, argv []string, member, dir, home string) ([]string, error) {
	what := "env_command " + strings.Join(argv, " ")
	env := append(os.Environ(), Env("CREW", member)...)
	if home != "" {
		env = append(env, Env("HOME", home)...)
	}
	out, err := Command{What: what, Argv: argv, Dir: dir, Env: env, Timeout: EnvCommandTimeout, Max: envCommandMax}.Output(ctx)
	if err != nil {
		return nil, err
	}
	return parseEnv(bytes.NewReader(out), what)
}
