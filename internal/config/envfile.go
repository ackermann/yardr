package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// EnvFile is a resident's or group's env_file: outbound credentials (an API
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
	p, err := ExpandPath(path)
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", path, err)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", p, err)
	}
	defer f.Close()
	// Stat the open file, not the path, so what is checked is what is read.
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("env_file %s: %w", p, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("env_file %s is not a regular file", p)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("env_file %s is open to others (mode %04o); chmod 600 it", p, perm)
	}
	out := &EnvFile{Path: p}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !envKey.MatchString(k) {
			return nil, fmt.Errorf("env_file %s line %d: want KEY=VALUE", p, n)
		}
		out.Env = append(out.Env, k+"="+v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("env_file %s: %w", p, err)
	}
	return out, nil
}
