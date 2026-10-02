// Package runner holds the built-in Runner implementations.
//
//	manual  nothing is started; a human or outside agent picks the bead up
//	        with `yardr bead list --group <name>` and advances it. With
//	        config notify=true a herdr notification says a bead is waiting.
//	exec    runs config.cmd through sh -c in the rig (or config.dir), with the
//	        rig, base, branch and worktree in its environment (a dir rig's
//	        bead has no base, branch or worktree).
//	herdr   opens a worktree per bead in herdr and starts an agent in it; a
//	        dir rig's bead is worked in the rig's directory itself.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
			YardBin: self(), OnError: onError, Store: st, Log: log},
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
	dirRig := job.Rig != nil && job.Rig.IsDir()
	switch {
	case dirRig && workdir != "":
		fmt.Fprintf(&b, "Work in `%s` directly: it is a plain directory (dir rig `%s`), with no branch to work on. Commit nothing. Do not touch files outside what the bead names. When you report, say in your note what you produced, by path.\n\n", workdir, job.Rig.Name)
	case workdir != "":
		fmt.Fprintf(&b, "Work in `%s` (branch `%s`). Commit and push your work there.\n\n", workdir, Branch(bead))
	}
	switch {
	case job.Epic != "" && dirRig:
		fmt.Fprintf(&b, "This bead is a child of epic %s, which only groups it with its siblings: they all work in the same directory.\n\n", job.Epic)
	case job.Epic != "":
		fmt.Fprintf(&b, "This bead is a child of epic %s: your branch starts from `%s` and your work lands on that branch, not on the rig's base.\n\n",
			job.Epic, BaseOf(job))
	}
	switch {
	case bead.Type == store.TypeEpic && dirRig:
		writeDirEpic(&b, job)
	case bead.Type == store.TypeEpic:
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
	// Where done itself goes back to open (merge -> open on done in
	// yardr.epic), the work is on the branch, not a child: say who takes it
	// from there, so the merger leaves the pushing to the integrator.
	for _, o := range job.Outcomes {
		if o.Outcome == store.DefaultOutcome && o.Stage == "open" && job.Bead.Stage != "open" {
			fmt.Fprintf(b, "Your work here is on the branch itself: rebase `%s` onto `%s` in its worktree and leave it clean, but do not push it. Reporting `done` sends the epic back to `open`, where the integrator takes it again with no child open: it rebases `%[1]s` onto `%[2]s` once more, gates it, pushes it (and opens or updates its pull request) and sends it to review.\n\n", branch, BaseOf(job))
		}
	}
	if url, _ := job.Bead.Metadata["pr_url"].(string); url != "" {
		writePR(b, job, url)
	}
}

// writeDirEpic says what an epic is in a dir rig: a group of children, all
// worked in the rig's directory, with no branch that lands. Whoever works
// the epic looks at what its children produced as a whole.
func writeDirEpic(b *strings.Builder, job dispatch.Job) {
	fmt.Fprintf(b, "This bead is an epic in the dir rig `%s`: it has no branch and lands nowhere; it groups its children, who worked in `%s`.", job.Rig.Name, job.Rig.Path)
	if len(job.Children) > 0 {
		fmt.Fprintf(b, " Children: %s.", strings.Join(job.Children, ", "))
	}
	fmt.Fprintf(b, " Their notes say what each produced, by path: work on that as a whole, not child by child.\n\n")
	fmt.Fprintf(b, "An epic sent back to `open` waits there only while a child is open: before sending it back, add the fix as a child (`yardr bead create ... -r %s`, then `yardr dep add %s <child> -k parent`).\n\n", job.Rig.Name, job.Bead.ID)
}

// checksTimeout bounds gh pr checks: a brief is written while the agent's
// pane waits, and GitHub being slow must not hold the session up.
var checksTimeout = 20 * time.Second

// writePR is the part of an epic's brief for an epic that lands through a
// pull request (integrate-epic stored its pr_url): the PR, its CI as GitHub
// sees it now, and that the review is posted there. The reviewer may push
// its fix-forward commits to the epic's branch, which pr-epic pushes again
// anyway; it never merges, which is pr-merge's after approval.
func writePR(b *strings.Builder, job dispatch.Job, url string) {
	branch := Branch(job.Bead)
	fmt.Fprintf(b, "## Pull request\n\nThis epic lands through the pull request %s.", url)
	fmt.Fprintf(b, " For this bead you may push to `%s` (`git push --force-with-lease origin %[1]s`, after fixing forward), and you must post your review on the pull request with `gh pr review %s` (`--approve`, `--request-changes` or `--comment`, `--body-file` your note). Never merge it: it is merged after approval.\n\n", branch, url)
	fmt.Fprintf(b, "CI on the pull request when this brief was written (`gh pr checks %s`):\n\n```\n%s\n```\n\n", url, prChecks(rigPath(job.Rig), url))
}

// prChecks is gh pr checks' table for the pull request at url, or
// "checks: unavailable" with the reason. gh exits non-zero when a check
// failed (1) or is pending (8) and still prints the table, so output wins
// over the exit status; only no output at all counts as unavailable.
func prChecks(dir, url string) string {
	ctx, cancel := context.WithTimeout(context.Background(), checksTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "checks", url)
	cmd.Dir = dir
	// gh must not wait on a prompt nobody sees.
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if text := strings.TrimSpace(out.String()); text != "" && ctx.Err() == nil {
		return text
	}
	reason := strings.TrimSpace(errb.String())
	switch {
	case ctx.Err() != nil:
		reason = fmt.Sprintf("gh pr checks took longer than %s", checksTimeout)
	case reason == "" && err != nil:
		reason = err.Error()
	case reason == "":
		reason = "gh pr checks printed nothing"
	}
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		reason = reason[:i]
	}
	return "checks: unavailable (" + reason + ")"
}

// readPrompt reads a group's prompt file. An absolute path is read as is.
//
// A role, prompt=roles/<name>.md or bare <name>.md, is looked up where roles
// live, in order: the rig's own .yardr/roles/<name>.md (a rig's repo says how
// it is worked, next to .yardr/flows and .yardr/check), the rig's
// roles/<name>.md (the older place, still read, with a warning), then the
// yard's home, ~/.yardr/roles/<name>.md: the generic roles `yardr init`
// installed, so a new rig works until it commits its own.
//
// A rig's roles are read like its gate: from the tip of the rig's base, not
// from the bead's branch, so a bead cannot rewrite the prompt of the reviewer
// that judges it; a role edit counts once it has landed. A file the base does
// not have yet (or a rig that is not a git checkout) is read from the rig's
// working tree instead. A dir rig has no base: its roles are read from its
// directory as they are.
//
// Any other relative path is looked up in the rig's working tree, then in
// the home.
func readPrompt(p string, rig *store.Rig, home string) ([]byte, error) {
	if filepath.IsAbs(p) {
		return os.ReadFile(p)
	}
	name, isRole := roleName(p)
	if !isRole {
		return readPlain(p, rig, home)
	}
	var tried []string
	if rig != nil && rig.Path != "" {
		for _, rel := range []string{filepath.Join(".yardr", "roles", name), filepath.Join("roles", name)} {
			text, where, err := readRigFile(rig, rel)
			if err == nil {
				if strings.HasPrefix(rel, "roles") {
					warnRigRootRole(rig, rel)
				}
				return text, nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			tried = append(tried, where)
		}
	}
	if home != "" {
		path := filepath.Join(home, "roles", name)
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

// roleName is the file name of a role prompt: roles/<name>.md or <name>.md.
func roleName(p string) (string, bool) {
	p = filepath.Clean(p)
	dir, file := filepath.Split(p)
	if filepath.Ext(file) != ".md" || (dir != "" && dir != "roles"+string(filepath.Separator)) {
		return "", false
	}
	return file, true
}

// readPlain reads a relative prompt that is not a role from the rig's
// working tree, else the home.
func readPlain(p string, rig *store.Rig, home string) ([]byte, error) {
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

// gitTimeout bounds each git call reading a role from a rig's base: a
// brief is written while the agent's pane waits.
var gitTimeout = 30 * time.Second

// readRigFile reads rel at the tip of the rig's base (rig.Base, which a
// bead's start has just fetched when it is a remote's), or from the rig's
// working tree when the base has no such file or does not resolve. where
// names the place looked, for the not-found error. A file absent from both
// is fs.ErrNotExist.
func readRigFile(rig *store.Rig, rel string) (text []byte, where string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", rig.Path}, args...)...)
		cmd.WaitDelay = time.Second
		var errb bytes.Buffer
		cmd.Stderr = &errb
		out, err := cmd.Output()
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
			}
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out, nil
	}
	gitPath := filepath.ToSlash(rel)
	if rig.Base != "" && !rig.IsDir() {
		// A rig that is not a repo, or whose base does not resolve, has no
		// base to read from: its working tree is all there is.
		if out, err := git("rev-parse", "--verify", "--quiet", rig.Base+"^{commit}"); err == nil {
			sha := strings.TrimSpace(string(out))
			// ls-tree tells "not on the base yet" apart from a failing git.
			out, err := git("ls-tree", "--name-only", sha, "--", gitPath)
			if err != nil {
				return nil, "", err
			}
			if strings.TrimSpace(string(out)) != "" {
				text, err := git("show", sha+":"+gitPath)
				return text, rig.Base + ":" + gitPath, err
			}
		} else if ctx.Err() != nil {
			return nil, "", err
		}
	}
	path := filepath.Join(rig.Path, rel)
	text, err = os.ReadFile(path)
	return text, path, err
}

// warnedRigRoots holds the rig-root roles already warned about, so a
// group's every session does not repeat it.
var warnedRigRoots sync.Map

// warn reports a deprecated rig-root role; tests replace it.
var warn = func(msg string, args ...any) { slog.Warn(msg, args...) }

// warnRigRootRole says, once per file, that a rig keeps a role at the
// repo's root, the older place, rather than in .yardr/roles/.
func warnRigRootRole(rig *store.Rig, rel string) {
	key := filepath.Join(rig.Path, rel)
	if _, done := warnedRigRoots.LoadOrStore(key, true); done {
		return
	}
	warn("role prompt at the rig's root is deprecated; move it to .yardr/roles/",
		"rig", rig.Name, "file", key, "to", filepath.Join(rig.Path, ".yardr", "roles", filepath.Base(rel)))
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
