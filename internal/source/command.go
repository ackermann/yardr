package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
)

// MaxCommandOutput bounds what a command may print on stdout: an answer,
// not a stream.
const MaxCommandOutput = 64 << 10

// Run runs one command a declaration names and reads its answer: argv,
// without a shell, in dir, under env (nil: the caller's environment), with
// the JSON object in on its stdin (nil: its stdin closed). It has timeout
// (0: Timeout), may print at most MaxCommandOutput, and is killed with
// everything it started when the time is over. The answer is one JSON
// object; read reads it into the caller's own type and goes first, since
// its error says in the caller's words what the answer lacks — nil read
// takes any one object. Whatever else happens — no program, a non-zero
// exit, no answer in time, too much or no such object — is a failure whose
// error ends with the command's stderr and never holds its stdout.
func Run(ctx context.Context, what string, argv []string, in map[string]any, env []string, dir string, timeout time.Duration, read func(out []byte) error) error {
	var stdin []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("%s: its input is no JSON object: %v", what, err)
		}
		stdin = b
	}
	if timeout <= 0 {
		timeout = Timeout
	}
	out, err := config.Command{What: what, Argv: argv, Dir: dir, Env: env, Stdin: stdin, Timeout: timeout, Max: MaxCommandOutput}.Output(ctx)
	if err != nil {
		return err
	}
	if read != nil {
		if err := read(out); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	return oneObject(what, out)
}

// oneObject refuses an answer that is not one JSON object, saying nothing
// of what was printed: a command that fails may have printed anything.
func oneObject(what string, out []byte) error {
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("%s: it printed nothing: a command's answer is one JSON object", what)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("%s: what it printed is not one JSON object", what)
	}
	if dec.More() {
		return fmt.Errorf("%s: it printed more than one JSON object", what)
	}
	if _, ok := v.(map[string]any); !ok {
		return fmt.Errorf("%s: its answer is no JSON object", what)
	}
	return nil
}

// CommandEnv is the whole environment a command of d runs with: PATH and
// HOME as the process that asks has them, the credential's value under the
// name credential.env gives (read by the caller from the env file of
// whoever acts, as a call reads it; "" for none) and YARDR_SOURCE_BASE, the
// declaration's base, so a test can point the command at a stand-in.
// Nothing else of the yard's environment reaches a command.
func (d *Declaration) CommandEnv(credential string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	if d.Credential != nil && credential != "" {
		env = append(env, d.Credential.Env+"="+credential)
	}
	return append(env, config.Env("SOURCE_BASE", d.Base)...)
}

// Command is one command a declaration names: the key that names it
// (limits.command, thread.reply) and the program and its arguments as the
// file has them.
type Command struct {
	Key  string   `json:"key"`
	Argv []string `json:"argv"`
}

// Commands are the commands the declaration names: the limits command, and
// thread's reply, posted, read, open and state, in that order.
func (d *Declaration) Commands() []Command {
	var out []Command
	if d.Limits != nil && len(d.Limits.Command) > 0 {
		out = append(out, Command{Key: "limits.command", Argv: d.Limits.Command})
	}
	if t := d.Thread; t != nil {
		for _, c := range []Command{
			{Key: "thread.reply", Argv: t.Reply},
			{Key: "thread.posted", Argv: t.Posted},
			{Key: "thread.read", Argv: t.Read},
			{Key: "thread.open", Argv: t.Open},
			{Key: "thread.state", Argv: t.State},
		} {
			if len(c.Argv) > 0 {
				out = append(out, c)
			}
		}
	}
	return out
}

// Relative reports whether the command's program is a file beside the
// declaration's file: one with no / at its start. An absolute program is
// the operator's own and is not copied.
func (c Command) Relative() bool { return !strings.HasPrefix(c.Argv[0], "/") }

// Program is the command's program as the yard runs it: a relative one from
// the copy yardr source add made under <home>/sources/<name>/.
func (c Command) Program(home, source string) string {
	if !c.Relative() {
		return c.Argv[0]
	}
	return filepath.Join(home, "sources", source, c.Argv[0])
}

// programs are the relative programs of the declaration's commands, each
// once: the files source add copies.
func (d *Declaration) programs() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range d.Commands() {
		if c.Relative() && !seen[c.Argv[0]] {
			seen[c.Argv[0]] = true
			out = append(out, c.Argv[0])
		}
	}
	return out
}

// HasCommandFiles reports whether any command of the declaration names a
// file beside the declaration's file.
func (d *Declaration) HasCommandFiles() bool { return len(d.programs()) > 0 }

// CheckCommandFiles refuses the declaration when a program of its commands
// is no file beside the declaration's file, which stands in declDir. It is
// what keeps a source whose commands could not run from being added;
// CopyCommandFiles reads the files again when it copies them.
func (d *Declaration) CheckCommandFiles(declDir string) error {
	for _, prog := range d.programs() {
		if _, err := os.ReadFile(filepath.Join(declDir, prog)); err != nil {
			return fmt.Errorf("source %s: a command's program %s is a file beside the declaration's file: %v", d.Name, prog, err)
		}
	}
	return nil
}

// CopyCommandFiles copies each file the declaration's commands name by a
// relative program from declDir, the directory the declaration's file
// stands in, into <home>/sources/<name>/, the relative path kept, each copy
// mode 0700 so the yard can run it. The directory is made anew, so no file
// of a declaration this one replaces is kept. A program that is no file
// beside the declaration fails the copy before anything is written.
func (d *Declaration) CopyCommandFiles(declDir, home string) error {
	type fileCopy struct {
		rel  string
		data []byte
	}
	var copies []fileCopy
	for _, prog := range d.programs() {
		data, err := os.ReadFile(filepath.Join(declDir, prog))
		if err != nil {
			return fmt.Errorf("source %s: a command's program %s is a file beside the declaration's file: %v", d.Name, prog, err)
		}
		copies = append(copies, fileCopy{prog, data})
	}
	dest := filepath.Join(home, "sources", d.Name)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for _, c := range copies {
		path := filepath.Join(dest, c.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, c.data, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// RemoveCommandFiles removes what CopyCommandFiles made. A source whose
// declaration named no file has nothing to remove.
func RemoveCommandFiles(home, name string) error {
	return os.RemoveAll(filepath.Join(home, "sources", name))
}

// CommandFile is the program of one command of the declaration, for
// showing: the program as the file has it, and for one the declaration
// brings (a relative path) the copy the yard runs it from, with the copy's
// sha256 — "" when the copy is not there (added by a build that did not
// copy, or the file went since).
type CommandFile struct {
	Key      string `json:"key"`
	Program  string `json:"program"`
	Declared bool   `json:"declared,omitempty"` // the declaration brings the file
	Path     string `json:"path,omitempty"`     // the copy under <home>/sources/<name>/
	SHA256   string `json:"sha256,omitempty"`
}

// CommandFiles are the programs of the declaration's commands as the yard
// has them, for source show.
func (d *Declaration) CommandFiles(home string) []CommandFile {
	var out []CommandFile
	for _, c := range d.Commands() {
		f := CommandFile{Key: c.Key, Program: c.Argv[0]}
		if c.Relative() {
			f.Declared = true
			f.Path = c.Program(home, d.Name)
			if data, err := os.ReadFile(f.Path); err == nil {
				sum := sha256.Sum256(data)
				f.SHA256 = hex.EncodeToString(sum[:])
			}
		}
		out = append(out, f)
	}
	return out
}
