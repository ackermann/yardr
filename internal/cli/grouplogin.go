package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// claudeFolderEnv names Claude Code's own directory in the environment: the
// folder a login lands in when it is set, the user's home otherwise (where
// the login file sits in the home itself, not in .claude).
const claudeFolderEnv = "CLAUDE_CONFIG_DIR"

// groupLoginEnv loads the env_file the group name declares: the same file
// the runner reads for the group's sessions, read under the same rule. It
// returns its entries and the file as the group names it. A group that
// names none, or that there is none of, is refused naming why: a login led
// for it would land in the default folder instead.
func groupLoginEnv(ctx context.Context, st store.API, name string) ([]string, string, error) {
	g, err := st.GetGroup(ctx, name)
	if err != nil {
		return nil, "", err
	}
	file, _ := g.Config["env_file"].(string)
	if file == "" {
		return nil, "", fmt.Errorf("group %s names no env_file, so a login has no folder to land in: carry its login first (yardr group set %s env_file=<file>)", name, name)
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return nil, "", fmt.Errorf("group %s: %w", name, err)
	}
	return f.Env, file, nil
}

// groupReadEnv is the env_file entries of the group name, the environment
// its sessions burn under, for a limits read of its login. A group that
// names none reads as nil: its panes run with the ambient environment, so
// the machine's login is its login too. A group there is none of, or one
// whose file cannot be read, is refused naming why: what the group burns
// cannot be known then.
func groupReadEnv(ctx context.Context, st store.API, name string) ([]string, store.Group, error) {
	g, err := st.GetGroup(ctx, name)
	if err != nil {
		return nil, store.Group{}, err
	}
	file, _ := g.Config["env_file"].(string)
	if file == "" {
		return nil, *g, nil
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return nil, store.Group{}, fmt.Errorf("group %s: %w", name, err)
	}
	return f.Env, *g, nil
}

// groupFolderAccount is the account the folder a group's env_file names
// holds a login for, "" where none can be read: no env_file, one that
// cannot be read, an env that names no folder, or a folder with no login
// in it. Callers show nothing then: a group not yet logged in is no fault.
func groupFolderAccount(g store.Group) string {
	file, _ := g.Config["env_file"].(string)
	if file == "" {
		return ""
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return ""
	}
	return loginFolderAccount(f.Env)
}

// loginFolderAccount is the account a login folder names, where the harness
// writes one: Claude Code's .claude.json holds the signed-in account's
// address (oauthAccount.emailAddress). Codex's folder names none yardr
// reads. "" when the env names no folder or the folder names no account.
func loginFolderAccount(env []string) string {
	var dir string
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == claudeFolderEnv {
			dir = v
		}
	}
	if dir == "" {
		return ""
	}
	return claudeAccountIn(dir)
}

// defaultClaudeAccount is the account the machine's own Claude login names:
// the folder the environment names, else the user's home. "" where there
// is none or nothing of it can be read.
func defaultClaudeAccount() string {
	if dir := os.Getenv(claudeFolderEnv); dir != "" {
		return claudeAccountIn(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Without the override the file sits in the home itself, not in .claude.
	return claudeAccountFile(filepath.Join(home, ".claude.json"))
}

// claudeAccountIn is the account the Claude login folder dir names, "" where
// it names none.
func claudeAccountIn(dir string) string {
	p, err := config.ExpandPath(filepath.Join(dir, ".claude.json"))
	if err != nil {
		return ""
	}
	return claudeAccountFile(p)
}

// claudeAccountFile is the address the login file at p names, "" where it
// names none: missing, unreadable, no JSON, or signed out.
func claudeAccountFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var v struct {
		OAuthAccount struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.OAuthAccount.Email
}
