// Package runner holds the built-in Runner implementations.
//
//	manual  nothing is started; a human or outside agent picks the bead up
//	        with `yard bead list --group <name>` and advances it.
//	exec    runs config.cmd through sh -c in the rig (or config.dir).
//	herdr   opens a worktree per bead in herdr and starts an agent in it.
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mckean/yard/internal/dispatch"
)

// All returns every built-in runner keyed by the name groups refer to.
// onError, if set, receives background errors (herdr event stream down).
func All(home string, onError func(error)) map[string]dispatch.Runner {
	return map[string]dispatch.Runner{
		"manual": Manual{},
		"exec":   NewExec(),
		"herdr":  &Herdr{Bin: "herdr", WorktreeRoot: filepath.Join(home, "worktrees"), OnError: onError},
	}
}

// Env is the environment every started process gets.
func Env(job dispatch.Job, actor string) []string {
	return []string{
		"YARD_BEAD=" + job.Bead.ID,
		"YARD_SESSION=" + job.Session.ID,
		"YARD_STAGE=" + job.Bead.Stage,
		"YARD_GROUP=" + job.Group.Name,
		"YARD_ACTOR=" + actor,
	}
}

func str(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

func strs(cfg map[string]any, key string) []string {
	switch v := cfg[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return strings.Fields(v)
	}
	return nil
}

// Brief renders the instructions an agent session starts from: the group's
// prompt file followed by the bead and the yard commands to report back.
func Brief(job dispatch.Job, actor, workdir string) (string, error) {
	var b strings.Builder
	if p := str(job.Group.Config, "prompt", ""); p != "" {
		path := p
		if !filepath.IsAbs(path) && job.Rig != nil {
			path = filepath.Join(job.Rig.Path, p)
		}
		text, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("group %s prompt: %w", job.Group.Name, err)
		}
		b.Write(text)
		b.WriteString("\n\n---\n\n")
	}
	bead := job.Bead
	fmt.Fprintf(&b, "# yard session %s\n\n", job.Session.ID)
	fmt.Fprintf(&b, "You are `%s`, working bead **%s** (%s, stage `%s`) for group `%s`.\n\n",
		actor, bead.ID, bead.Type, bead.Stage, job.Group.Name)
	if workdir != "" {
		fmt.Fprintf(&b, "Work in `%s` (branch `yard/%s`). Commit and push your work there.\n\n", workdir, bead.ID)
	}
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", bead.Title, strings.TrimSpace(bead.Body))
	fmt.Fprintf(&b, `## Reporting back

Run every yard command with YARD_ACTOR=%[1]s set (export it once).

- Read the bead, its notes and dependencies: `+"`yard prime --bead %[2]s`"+`
- Leave evidence as you go: `+"`yard bead note %[2]s \"...\"`"+`
- When your part is done, hand the bead to the next stage: `+"`yard bead advance %[2]s --to <stage>`"+`
- Or, if the work is finished entirely: `+"`yard bead close %[2]s --reason \"...\"`"+`
- If you are stuck and need a human: `+"`yard bead note %[2]s \"...\" && yard bead hold %[2]s`"+`

Advancing or closing ends your session; nothing else does.
`, actor, bead.ID)
	return b.String(), nil
}
