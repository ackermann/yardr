package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// Codex's own directory: the one CODEX_HOME names, else .codex in the user's
// home. Its config.toml is where Codex keeps the folders it trusts.
const (
	codexKind    = "codex"
	codexHomeEnv = "CODEX_HOME"
	codexHomeDir = ".codex"
)

// codexConfig is the file Codex reads its trusted folders from, as this
// command finds it: the daemon's CODEX_HOME is its own.
func codexConfig() string {
	if p := os.Getenv(codexHomeEnv); p != "" {
		return filepath.Join(p, "config.toml")
	}
	home, _ := userHomeDir()
	return filepath.Join(home, codexHomeDir, "config.toml")
}

// A folder's table in Codex's config, and the key that trusts it, as Codex
// itself writes them:
//
//	[projects."/path/to/repo"]
//	trust_level = "trusted"
var (
	codexProject = regexp.MustCompile(`^\[\s*projects\s*\.\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*\]\s*(?:#.*)?$`)
	codexTrust   = regexp.MustCompile(`^trust_level\s*=\s*(?:"trusted"|'trusted')\s*(?:#.*)?$`)
)

// codexTrusted are the folders config trusts, each cleaned. It reads the
// form Codex writes, a table per folder, and no other: a folder trusted in
// an inline table or by a dotted key is not seen, and is noted as untrusted.
func codexTrusted(config string) []string {
	var out []string
	project := ""
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		switch m := codexProject.FindStringSubmatch(line); {
		case m != nil:
			project = m[1][1 : len(m[1])-1]
			if m[1][0] == '"' {
				// A basic string: its escapes are Go's.
				if p, err := strconv.Unquote(m[1]); err == nil {
					project = p
				}
			}
		case strings.HasPrefix(line, "["):
			project = ""
		case project != "" && codexTrust.MatchString(line):
			out = append(out, filepath.Clean(project))
		}
	}
	return out
}

// codexTrustNotes are what yardr doctor notes about Codex's trust in the
// yard's depots: one line for each depot a route sends to a group whose kind
// is codex and whose root Codex's config does not trust, with the lines that
// trust it. Codex asks "Trust this folder?" in a folder it does not trust,
// and a session yardr started waits on that question until a person answers
// it in its pane (footplate-rn6b). Notes, not faults: the yard runs, and
// doctor reads the config as this command finds it. Nothing is written: whom
// Codex trusts is the operator's to say.
func codexTrustNotes(ctx context.Context, st store.API, groups []store.Group) ([]string, error) {
	var codex []string
	for _, g := range groups {
		if kind, _ := g.Config["kind"].(string); g.Runner == "herdr" && kind == codexKind {
			codex = append(codex, g.Name)
		}
	}
	if len(codex) == 0 {
		return nil, nil
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	depots, err := st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	file := codexConfig()
	config, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return []string{fmt.Sprintf("whether Codex trusts the depots its groups work is not known: %v", err)}, nil
	}
	trusted := codexTrusted(string(config))
	var notes []string
	for _, d := range depots {
		if d.Path == "" {
			continue
		}
		var by []string
		for _, r := range routes {
			// A route that names no depot takes the beads of all of them.
			if r.Depot != nil && *r.Depot != d.Name {
				continue
			}
			for _, g := range r.Groups() {
				if slices.Contains(codex, g.Name) && !slices.Contains(by, g.Name) {
					by = append(by, g.Name)
				}
			}
		}
		if len(by) == 0 {
			continue
		}
		// Codex names a folder by its real path, so that is the one to add;
		// one trusted as the depot names it is trusted too.
		root := filepath.Clean(d.Path)
		if real, err := filepath.EvalSymlinks(root); err == nil {
			if slices.Contains(trusted, root) {
				continue
			}
			root = real
		}
		if slices.Contains(trusted, root) {
			continue
		}
		group := "group " + by[0] + " runs"
		if len(by) > 1 {
			group = "groups " + list(by) + " run"
		}
		notes = append(notes, fmt.Sprintf("depot %s (%s) is not trusted by Codex, and %s on it: a session there waits on Codex's \"Trust this folder?\" "+
			"until someone answers it in its pane. Add to %s the two lines `[projects.%s]` and `trust_level = \"trusted\"`",
			d.Name, d.Path, group, file, strconv.Quote(root)))
	}
	return notes, nil
}
