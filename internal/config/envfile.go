package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// EnvFile is a crew member's or group's env_file: outbound credentials (an API
// token, an OAuth app's secret) for the panes its agents run in. They live in
// a file of their own, never in the store, which BDP serves, backups copy and
// every builder can read.
type EnvFile struct {
	Path string
	// Env is the file's entries as KEY=VALUE, in file order.
	Env []string
}

// Keys are the names the file sets, for showing what it would load without
// showing a value.
func (f *EnvFile) Keys() []string {
	keys := make([]string, 0, len(f.Env))
	for _, e := range f.Env {
		k, _, _ := strings.Cut(e, "=")
		keys = append(keys, k)
	}
	return keys
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ExpandPath turns a leading ~/ into the user's home and makes p absolute.
func ExpandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// ReadEnvFile reads the env file at path (~ expanded). Lines are KEY=VALUE;
// blank lines and # comments are skipped, and nothing is expanded: the value
// is the rest of the line as written. The file must be a regular file no one
// but its owner can read or write (0600 or stricter), since it holds
// secrets. Errors name the file and a line number, never a value.
func ReadEnvFile(path string) (*EnvFile, error) {
	f, p, err := openPrivate("env_file", path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env, err := parseEnv(f, "env_file "+p)
	if err != nil {
		return nil, err
	}
	return &EnvFile{Path: p, Env: env}, nil
}

// ParseEnv reads KEY=VALUE lines from r as an env file's are read, for a
// command that is handed them on its standard input. what names where they
// came from in errors, which carry a line number and never a value.
func ParseEnv(r io.Reader, what string) ([]string, error) { return parseEnv(r, what) }

// parseEnv reads KEY=VALUE lines from r, an env_file or what an env_command
// printed: the one reader of both. what names the source in errors, which
// carry a line number and never a value.
func parseEnv(r io.Reader, what string) ([]string, error) {
	var env []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !envKey.MatchString(k) {
			return nil, fmt.Errorf("%s line %d: want KEY=VALUE", what, n)
		}
		env = append(env, k+"="+v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return env, nil
}

// ReadSecretFile reads the file holding one secret (a hook's --secret-file)
// under the rule an env_file is read under, and returns what it holds
// without surrounding whitespace, and the path it resolved to. Errors name
// the file, never what is in it.
func ReadSecretFile(path string) (secret []byte, resolved string, err error) {
	f, p, err := openPrivate("secret file", path)
	if err != nil {
		return nil, p, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, p, fmt.Errorf("secret file %s: %w", p, err)
	}
	return bytes.TrimSpace(b), p, nil
}

// openPrivate opens the credential file at path (~ expanded) and returns it
// with the path it resolved to. This is the one rule for every file yardr
// reads a secret from: a regular file no one but its owner can read or
// write (0600 or stricter). what names the kind of file in errors.
func openPrivate(what, path string) (*os.File, string, error) {
	p, err := ExpandPath(path)
	if err != nil {
		return nil, path, fmt.Errorf("%s %s: %w", what, path, err)
	}
	// Non-blocking, so a FIFO at the path is refused below instead of
	// holding the caller until something writes to it.
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, p, fmt.Errorf("%s %s: %w", what, p, err)
	}
	// Stat the open file, not the path, so what is checked is what is read.
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, p, fmt.Errorf("%s %s: %w", what, p, err)
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, p, fmt.Errorf("%s %s is not a regular file", what, p)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		f.Close()
		return nil, p, fmt.Errorf("%s %s is open to others (mode %04o); chmod 600 it", what, p, perm)
	}
	return f, p, nil
}

// WriteEnvFile sets each name of values to its value in the env file at
// path (~ expanded), the one writer of the format parseEnv reads: the line
// of a given name is replaced, or added at the end where the file has none,
// and every other line and comment is kept as written. A file that is not
// there is made, with its directory. One that is there is held to the rule
// it is read under (openPrivate) and is refused, not repaired, when it
// breaks it; so is a symbolic link, whatever it points to: the write is a
// rename over the path, which would leave the link's file as it was and
// read the lines to keep from a file the rule was never asked about.
//
// The new content is written whole to a second file beside it (EnvFileNew,
// mode 0600), synced and renamed into place, so a reader sees the old file
// or the new one. A second file left by a write that died is removed first.
// renamed says the rename was done: an error with it false left the file as
// it was, to the byte, and no second file; one with it true (the
// directory's sync) left the new content in place. Errors name the file and
// the names, never a value.
func WriteEnvFile(path string, values map[string]string) (resolved string, renamed bool, err error) {
	p, err := ExpandPath(path)
	if err != nil {
		return path, false, fmt.Errorf("env_file %s: %w", path, err)
	}
	names := make([]string, 0, len(values))
	for name, value := range values {
		if !envKey.MatchString(name) {
			return p, false, fmt.Errorf("env_file %s: %q is no name of a variable", p, name)
		}
		// What the reader would not give back as it was given: it reads a
		// line, and trims it.
		switch {
		case value == "":
			return p, false, fmt.Errorf("env_file %s: the value for %s is empty", p, name)
		case strings.ContainsAny(value, "\r\n"):
			return p, false, fmt.Errorf("env_file %s: the value for %s has a line break in it", p, name)
		case strings.TrimSpace(value) != value:
			return p, false, fmt.Errorf("env_file %s: the value for %s begins or ends with white space", p, name)
		}
		names = append(names, name)
	}
	// In one order whatever the map's, so the same values write the same
	// file.
	sort.Strings(names)

	var old []byte
	switch st, err := os.Lstat(p); {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return p, false, fmt.Errorf("env_file %s: %w", p, err)
		}
	case err != nil:
		return p, false, fmt.Errorf("env_file %s: %w", p, err)
	case st.Mode()&os.ModeSymlink != 0:
		return p, false, fmt.Errorf("env_file %s is a symbolic link, and the yard writes through none: name the file itself as the env_file", p)
	default:
		f, _, err := openPrivate("env_file", p)
		if err != nil {
			return p, false, err
		}
		old, err = io.ReadAll(f)
		f.Close()
		if err != nil {
			return p, false, fmt.Errorf("env_file %s: %w", p, err)
		}
	}

	tmp := EnvFileNew(p)
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return p, false, fmt.Errorf("env_file %s: %w", p, err)
	}
	// O_EXCL takes no file that is there, and no link either.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return p, false, fmt.Errorf("env_file %s: %w", p, err)
	}
	err = func() error {
		defer f.Close()
		// Whatever the umask left of the mode.
		if err := f.Chmod(0o600); err != nil {
			return err
		}
		if _, err := f.Write(setEnv(old, names, values)); err != nil {
			return err
		}
		return f.Sync()
	}()
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return p, false, fmt.Errorf("env_file %s: %w", p, err)
	}
	// The rename is on the disk once its directory is.
	dir, err := os.Open(filepath.Dir(p))
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return p, true, fmt.Errorf("env_file %s: %w", p, err)
	}
	return p, true, nil
}

// EnvFileNew is the second file WriteEnvFile writes beside the env file at
// path before it renames it into place: one fixed name, so a write that
// died leaves at most one, for the next to remove.
func EnvFileNew(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".new")
}

// setEnv is old, the content of an env file, with each of names set to its
// value: on the first line that sets the name, as parseEnv reads a line;
// later lines of that name are dropped, since the last one is what a reader
// takes (source.Lookup); a name the file does not set is a line at the end.
func setEnv(old []byte, names []string, values map[string]string) []byte {
	var out bytes.Buffer
	done := map[string]bool{}
	text := string(old)
	for text != "" {
		line, rest, _ := strings.Cut(text, "\n")
		text = rest
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			k, _, ok := strings.Cut(t, "=")
			k = strings.TrimSpace(k)
			if _, given := values[k]; ok && given {
				if !done[k] {
					done[k] = true
					out.WriteString(k + "=" + values[k] + "\n")
				}
				continue
			}
		}
		out.WriteString(line + "\n")
	}
	for _, name := range names {
		if !done[name] {
			out.WriteString(name + "=" + values[name] + "\n")
		}
	}
	return out.Bytes()
}
