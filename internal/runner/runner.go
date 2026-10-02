// Package runner holds the built-in Runner implementations.
//
//	manual  nothing is started; a human or outside agent picks the bead up
//	        with `yardr bead list --group <name>` and advances it. With
//	        config notify=true a herdr notification says a bead is waiting.
//	exec    runs config.cmd through sh -c in the rig (or config.dir), with the
//	        rig, base, branch and worktree in its environment.
//	herdr   opens a worktree per bead in herdr and starts an agent in it.
package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// All returns every built-in runner keyed by the name groups refer to.
// st and log, if set, let manual groups notify humans; onError, if set,
// receives background errors (herdr event stream down).
func All(home string, st store.API, log *slog.Logger, onError func(error)) map[string]dispatch.Runner {
	return map[string]dispatch.Runner{
		"manual": &Manual{Store: st, Show: HerdrNotifier("herdr"), Log: log},
		"exec":   &Exec{Home: home, Bin: self()},
		"herdr": &Herdr{Bin: "herdr", WorktreeRoot: filepath.Join(home, "worktrees"), YardHome: home,
			YardBin: self(), OnError: onError, Store: st},
	}
}

// Env is the environment every started process gets, under the yardr names
// and the old yard ones (see config.Env).
func Env(job dispatch.Job, actor string) []string {
	return slices.Concat(
		config.Env("BEAD", job.Bead.ID),
		config.Env("SESSION", job.Session.ID),
		config.Env("STAGE", job.Bead.Stage),
		config.Env("GROUP", job.Group.Name),
		config.Env("ACTOR", actor),
	)
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
// home is the yard's home, where relative prompts not in the rig are found.
func Brief(job dispatch.Job, actor, workdir, home string) (string, error) {
	var b strings.Builder
	if p := str(job.Group.Config, "prompt", ""); p != "" {
		text, err := readPrompt(p, job.Rig, home)
		if err != nil {
			return "", fmt.Errorf("group %s prompt: %w", job.Group.Name, err)
		}
		b.Write(text)
		b.WriteString("\n\n---\n\n")
	}
	bead := job.Bead
	fmt.Fprintf(&b, "# yardr session %s\n\n", job.Session.ID)
	fmt.Fprintf(&b, "You are `%s`, working bead **%s** (%s, stage `%s`) for group `%s`.\n\n",
		actor, bead.ID, bead.Type, bead.Stage, job.Group.Name)
	if workdir != "" {
		fmt.Fprintf(&b, "Work in `%s` (branch `%s`). Commit and push your work there.\n\n", workdir, Branch(bead))
	}
	if job.Epic != "" {
		fmt.Fprintf(&b, "This bead is a child of epic %s: your branch starts from `%s` and your work lands on that branch, not on the rig's base.\n\n",
			job.Epic, BaseOf(job))
	}
	if bead.Type == store.TypeEpic {
		writeEpic(&b, job)
	}
	fmt.Fprintf(&b, "## %s\n\n%s\n\n", bead.Title, strings.TrimSpace(bead.Body))
	advance := "- When your part is done, hand the bead to the next stage: `yardr bead advance " + bead.ID + " --to <stage>`\n"
	ends := "Advancing, closing or holding ends your session; nothing else does."
	switch {
	case len(job.Outcomes) > 0:
		advance = "- When your part is done, report how it ended with one of the outcomes below; the flow picks the next stage.\n"
		ends = "Reporting an outcome, closing or holding ends your session; nothing else does."
	case job.Next != nil:
		advance = "- When your part is done, advance to one of the next stages below.\n"
	}
	fmt.Fprintf(&b, `## Reporting back

Your environment already names you to yardr (YARDR_ACTOR=%[1]s, YARDR_BEAD, YARDR_SESSION): your commands are attributed to you and carry your session, so leave those set. Once the bead has moved on, a move or close from this session is refused as stale.

- Read the bead, its notes and dependencies: `+"`yardr prime --bead %[2]s`"+`
- Leave evidence as you go: `+"`yardr bead note %[2]s \"...\"`"+`
%[3]s- Or, if the work is finished entirely: `+"`yardr bead close %[2]s --reason \"...\"`"+`
- If you are stuck and need a human: `+"`yardr bead note %[2]s \"...\" && yardr bead hold %[2]s`"+`

%[4]s
`, actor, bead.ID, advance, ends)
	switch {
	case len(job.Outcomes) > 0:
		writeOutcomes(&b, bead.ID, bead.Stage, job.Outcomes)
	case job.Next != nil:
		writeNext(&b, bead.ID, bead.Stage, job.Next)
	}
	return b.String(), nil
}

// writeEpic says what an epic's branch is: its children's work, landed on
// it one by one, which goes to the rig's base as one unit. Whoever works the
// epic (a reviewer, above all) looks at the whole range, not one child.
func writeEpic(b *strings.Builder, job dispatch.Job) {
	branch := Branch(job.Bead)
	fmt.Fprintf(b, "This bead is an epic. Its branch `%s` holds the work of its children, each landed on it after its own review; it lands on `%s` as one unit.", branch, BaseOf(job))
	if len(job.Children) > 0 {
		fmt.Fprintf(b, " Children: %s.", strings.Join(job.Children, ", "))
	}
	fmt.Fprintf(b, " The whole change is `%s..%s` (`git log %[1]s..%[2]s`, `git diff %[1]s...%[2]s`): work on it as a whole, not child by child.\n\n", BaseOf(job), branch)
	fmt.Fprintf(b, "An epic sent back to `open` waits there only while a child is open: before sending it back, add the fix as a child (`yardr bead create ... -r <rig>`, then `yardr dep add %s <child> -k parent`), or it is integrated again unchanged.\n\n", job.Bead.ID)
}

// readPrompt reads a group's prompt file. An absolute path is read as is. A
// relative one is looked up in the rig first, then in the yard's home, so
// prompt=roles/reviewer.md finds the rig's own roles/reviewer.md or, when the
// rig has none, ~/.yardr/roles/reviewer.md: a new rig works from the
// operator's prompts until it commits its own.
func readPrompt(p string, rig *store.Rig, home string) ([]byte, error) {
	if filepath.IsAbs(p) {
		return os.ReadFile(p)
	}
	var tried []string
	for _, dir := range []string{rigPath(rig), home} {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, p)
		text, err := os.ReadFile(path)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return text, err
		}
		tried = append(tried, path)
	}
	if len(tried) == 0 {
		return os.ReadFile(p)
	}
	return nil, fmt.Errorf("%s not found (looked in %s)", p, strings.Join(tried, ", "))
}

func rigPath(rig *store.Rig) string {
	if rig == nil {
		return ""
	}
	return rig.Path
}

// writeNext lists the stages the bead's flow allows from stage, each with
// what choosing it leads to and the exact command, so role prompts need not
// hard-code a next stage.
func writeNext(b *strings.Builder, id, stage string, next []dispatch.NextStage) {
	fmt.Fprintf(b, "\n## Next stages\n\n")
	if len(next) == 0 {
		fmt.Fprintf(b, "The flow allows no move out of `%s`; close the bead or hold it for a human.\n", stage)
		return
	}
	fmt.Fprintf(b, "The flow allows these moves out of `%s`:\n\n", stage)
	for _, n := range next {
		fmt.Fprintf(b, "- `%s`: %s\n  `yardr bead advance %s --to %s`\n", n.Stage, nextWhen(n), id, n.Stage)
	}
}

// writeOutcomes lists the outcomes the bead's stage offers, each with where
// the flow sends the bead for it and the exact command, so agents report
// how their work ended instead of choosing a stage.
func writeOutcomes(b *strings.Builder, id, stage string, outcomes []dispatch.NextOutcome) {
	fmt.Fprintf(b, "\n## Outcomes\n\n")
	fmt.Fprintf(b, "Report how your work in `%s` ended; the flow moves the bead on. Add `--note \"...\"` to attach a note with the move.\n\n", stage)
	for _, o := range outcomes {
		cmd := "yardr bead done " + id
		if o.Outcome != store.DefaultOutcome {
			cmd += " --outcome " + o.Outcome
		}
		fmt.Fprintf(b, "- `%s` (to `%s`): %s\n  `%s`\n", o.Outcome, o.Stage, nextWhen(o.NextStage), cmd)
	}
}

// nextWhen says in one line when to pick a next stage, from what happens to
// the bead there: a terminal stage closes it, otherwise the routed group's
// runner decides who picks it up.
func nextWhen(n dispatch.NextStage) string {
	switch {
	case n.Terminal:
		return "closes the bead; use it when nothing is left to do."
	case n.Group == "":
		return "no group works this stage; use it to park the bead until someone moves it on."
	case n.Runner == "manual":
		return fmt.Sprintf("a human in `%s` picks it up; use it when a person must act next.", n.Group)
	case n.Runner == "exec":
		return fmt.Sprintf("`%s` runs its command on it; use it when your part is ready for that step.", n.Group)
	default:
		return fmt.Sprintf("an agent in `%s` picks it up; use it when your part is ready for them.", n.Group)
	}
}

// self is this yard's executable, so agents run the same version.
func self() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
