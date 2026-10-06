package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
