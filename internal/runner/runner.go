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
//	peer    sends the bead to the linked yard config.peer names, which
//	        works it in a flow of its own; that yard's return ends the
//	        session with the outcome its flow came to.
package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	"yardr.dev/yardr/internal/otel"
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
		store.PeerRunner: &Peer{Store: st, Home: home},
	}
}

// Env is the environment every started process gets, under the yardr names
// and the old yard ones (see config.Env), and the trace context of its
// session: TRACEPARENT and OTEL_RESOURCE_ATTRIBUTES, so that whatever in it
// exports OpenTelemetry sends it under the bead (see otel.Env). They are set
// whether or not anything collects; the resource attributes this process
// was itself started with are kept in front of the bead's.
func Env(job dispatch.Job, actor string) []string {
	rig := ""
	if job.Rig != nil {
		rig = job.Rig.Name
	}
	return slices.Concat(
		config.Env("BEAD", job.Bead.ID),
		config.Env("SESSION", job.Session.ID),
		config.Env("STAGE", job.Bead.Stage),
		config.Env("GROUP", job.Group.Name),
		config.Env("ACTOR", actor),
		otel.Env(os.Getenv(otel.ResourceVar), job.Bead.ID, job.Session.ID, job.Bead.Stage, job.Group.Name, rig),
	)
}

func str(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

func strs(cfg map[string]any, key string) []string {
	return config.StringList(cfg[key])
}

// Brief renders the instructions an agent session starts from: the group's
// prompt file followed by the bead, the yard commands to report back and,
// when a flow governs the bead, the map of the flow and the stage file it
// has for the bead's stage.
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
		// Builders never push: the branch is landed from this worktree (the
		// refinery reads it here; an epic's integrator pushes the epic's).
		// The brief is the one place that says when a push is allowed, so
		// the role prompts defer to it: a section below grants one when it
		// says "you may push".
		fmt.Fprintf(&b, "Work in `%s` (branch `%s`). Commit your work there. Do not push it unless a section below says you may: the branch is landed from this worktree.\n\n", workdir, Branch(bead))
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
	// Before the text it is about: what follows was written in another
	// yard, and is read by an agent with this yard's permissions.
	if from := bead.From(); from != nil {
		fmt.Fprintf(&b, "%s\n\n", from.Marking())
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
	// What the flow says about this stage, for whoever works it, right
	// before the ways out of it. The heading is the brief's, so the file
	// itself needs none.
	sf := job.StageFile
	if sf == nil && job.StoreFlow != "" {
		// A store flow's stage files are files in the yard's home, like
		// the roles a rig does not carry.
		var err error
		if sf, err = store.HomeStageFile(home, job.StoreFlow, bead.Stage); err != nil {
			return "", err
		}
	}
	// Without its `summary:` line, which is for the map of the flow.
	body := strings.TrimSpace(sf.Body())
	// The whole flow and the path the bead took, as yardr prime draws
	// them: what comes before and after the stage, and for a bead sent back
	// that it was, which the stage file does not say. Fenced, since its
	// columns are aligned.
	if m := job.FlowMap; m != nil {
		if job.StoreFlow != "" {
			// The job's map is left as the store drew it.
			filled := *m
			filled.Stages = slices.Clone(m.Stages)
			if err := filled.HomeSummaries(home); err != nil {
				return "", err
			}
			m = &filled
		}
		fmt.Fprintf(&b, "\n## Flow `%s`\n\n```text\n%s```\n", m.Name, m.Text(body != ""))
	}
	if body != "" {
		fmt.Fprintf(&b, "\n## Stage `%s`\n\n%s\n", bead.Stage, body)
	}
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
// its fix-forward commits to the epic's branch, with a lease, which pr-epic
// pushes again anyway; it never merges, which is pr-merge's after approval.
// GitHub refuses approve and request-changes reviews from the PR's author,
// and integrate-epic opens the PR as the operator, whose gh the reviewer
// also uses: on its own PR the review is a comment, the verdict in its
// first line, and the yard learns the verdict from bead done.
func writePR(b *strings.Builder, job dispatch.Job, url string) {
	branch := Branch(job.Bead)
	fmt.Fprintf(b, "## Pull request\n\nThis epic lands through the pull request %s.", url)
	fmt.Fprintf(b, " For this bead you may push to `%s` (`git push --force-with-lease origin %[1]s`, after fixing forward; with a lease only, never forced, and only this branch), and you must post your review on the pull request with `gh pr review %s` (`--approve`, `--request-changes` or `--comment`, `--body-file` your note). GitHub refuses `--approve` and `--request-changes` from the login that opened the pull request: when `gh pr view %[2]s --json author --jq .author.login` is your own login (`gh api user --jq .login`), post `--comment` whatever the verdict, with the verdict as the note's first line. Never merge it: it is merged after approval.\n\n", branch, url)
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
// A rig's roles are read like its gate: from the tip of the rig's base and
// from nowhere else in the rig, so a bead cannot rewrite the prompt of the
// reviewer that judges it, and a role in a checkout's working tree (or on
// its current branch) counts once it has landed on the base; until then the
// home's copy serves. Only a rig that is not a git checkout, or whose base
// does not resolve, is read from its working tree. A dir rig has no base:
// its roles are read from its directory as they are.
//
// Any other relative path is looked up in the rig's working tree, then in
// the home.
func readPrompt(p string, rig *store.Rig, home string) ([]byte, error) {
	text, src, err := findPrompt(p, rig, home)
	if err == nil && src.Kind == PromptRig && strings.HasPrefix(src.Path, "roles") {
		warnRigRootRole(rig, src.Path)
	}
	return text, err
}

// Where a prompt is found (PromptSource.Kind).
const (
	PromptRig  = "rig"  // a file of the rig
	PromptHome = "home" // a file under the yard's home, found by name
	PromptFile = "file" // an absolute path, read as it is
)

// PromptSource says where readPrompt finds a prompt.
type PromptSource struct {
	Kind string
	// Path is relative to the rig's root for a file of the rig, else
	// absolute.
	Path string
	// Commit is the base commit a rig's file was read at; empty for one
	// read from the rig's working tree or a dir rig's directory.
	Commit string
}

// LocatePrompt says where the prompt p of a group working a bead of rig is
// read from, by the lookup a brief makes: `yardr rig sources` shows it. An
// error means a brief could not be written either.
func LocatePrompt(p string, rig *store.Rig, home string) (PromptSource, error) {
	_, src, err := findPrompt(p, rig, home)
	return src, err
}

// yardmasterRoles are the file names of the yardmaster's role: yardmaster.md,
// and mayor.md, as it was called until this release.
var yardmasterRoles = [2]string{"yardmaster.md", "mayor.md"}

// findPrompt is lookPrompt, and for the yardmaster's role under one of its
// two names, when no file has that name, the other: a yard set up before
// the rename has mayor.md where a prompt now says yardmaster.md, and a rig
// that renamed its file is still named by a resident's prompt=…/mayor.md.
// For this release only.
func findPrompt(p string, rig *store.Rig, home string) ([]byte, PromptSource, error) {
	text, src, err := lookPrompt(p, rig, home)
	if err == nil {
		return text, src, nil
	}
	dir, file := filepath.Split(p)
	for i, name := range yardmasterRoles {
		if file != name {
			continue
		}
		// A file that is there and cannot be read is the error to report;
		// only one that nobody has under this name is looked up under the
		// other.
		if filepath.IsAbs(p) && !errors.Is(err, fs.ErrNotExist) {
			break
		}
		if text, src, err2 := lookPrompt(dir+yardmasterRoles[1-i], rig, home); err2 == nil {
			return text, src, nil
		}
	}
	return text, src, err
}

func lookPrompt(p string, rig *store.Rig, home string) ([]byte, PromptSource, error) {
	if filepath.IsAbs(p) {
		text, err := os.ReadFile(p)
		return text, PromptSource{Kind: PromptFile, Path: p}, err
	}
	name, isRole := roleName(p)
	if !isRole {
		return findPlain(p, rig, home)
	}
	var tried []string
	if rig != nil && rig.Path != "" {
		for _, rel := range []string{filepath.Join(".yardr", "roles", name), filepath.Join("roles", name)} {
			text, where, sha, err := readRigFile(rig, rel)
			if err == nil {
				return text, PromptSource{Kind: PromptRig, Path: rel, Commit: sha}, nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, PromptSource{}, err
			}
			tried = append(tried, where)
		}
	}
	if home != "" {
		path := filepath.Join(home, "roles", name)
		text, err := os.ReadFile(path)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return text, PromptSource{Kind: PromptHome, Path: path}, err
		}
		tried = append(tried, path)
	}
	if len(tried) == 0 {
		text, err := os.ReadFile(p)
		return text, PromptSource{Kind: PromptFile, Path: p}, err
	}
	return nil, PromptSource{}, fmt.Errorf("%s not found (looked in %s)", p, strings.Join(tried, ", "))
}

// RigRole reports whether the rig has its own role prompt called name
// (builder.md): .yardr/roles/<name> where readPrompt looks for it, at the
// tip of the rig's base. A pack's import asks: it names the file its role
// shadows, or keeps the rig's role when told to.
func RigRole(rig *store.Rig, name string) (bool, error) {
	if rig == nil || rig.Path == "" {
		return false, nil
	}
	_, _, _, err := readRigFile(rig, filepath.Join(".yardr", "roles", name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
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

// findPlain reads a relative prompt that is not a role from the rig's
// working tree, else the home.
func findPlain(p string, rig *store.Rig, home string) ([]byte, PromptSource, error) {
	var tried []string
	for _, dir := range []string{rigPath(rig), home} {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, p)
		text, err := os.ReadFile(path)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			src := PromptSource{Kind: PromptHome, Path: path}
			if dir != home {
				src = PromptSource{Kind: PromptRig, Path: p}
			}
			return text, src, err
		}
		tried = append(tried, path)
	}
	if len(tried) == 0 {
		text, err := os.ReadFile(p)
		return text, PromptSource{Kind: PromptFile, Path: p}, err
	}
	return nil, PromptSource{}, fmt.Errorf("%s not found (looked in %s)", p, strings.Join(tried, ", "))
}

// gitTimeout bounds the git call reading a role from a rig's base: a
// brief is written while the agent's pane waits.
var gitTimeout = 30 * time.Second

// readRigFile reads rel at the tip of the rig's base (rig.Base, which a
// bead's start has just fetched when it is a remote's). A file the base
// does not have is fs.ErrNotExist, whatever the working tree holds: the
// working tree is read only when the rig has no base that resolves (a
// directory that is no git checkout) or is a dir rig. where names the
// place looked, for the not-found error, and sha the base commit read, empty
// for the working tree.
func readRigFile(rig *store.Rig, rel string) (text []byte, where, sha string, err error) {
	// A base with a newline in it is no ref, and would be two requests to
	// the git that reads a line for each.
	if rig.Base != "" && !rig.IsDir() && !strings.Contains(rig.Base, "\n") {
		gitPath := filepath.ToSlash(rel)
		text, sha, found, err := readAtBase(rig, gitPath)
		if err != nil {
			return nil, "", "", err
		}
		// A rig that is not a repo, or whose base does not resolve, has no
		// base to read from: its working tree is all there is.
		if sha != "" {
			where := rig.Base + ":" + gitPath
			if !found {
				return nil, where, "", fs.ErrNotExist
			}
			return text, where, sha, nil
		}
	}
	path := filepath.Join(rig.Path, rel)
	text, err = os.ReadFile(path)
	return text, path, "", err
}

// readAtBase reads gitPath at the commit the rig's base names, in one git
// process: a `git cat-file --batch` asked for the commit and then, with the
// sha it answered, for the file, so the file is read at the commit that was
// resolved. sha is empty when there is no base to read from: git does not
// know it, or ends before it has answered (no checkout here, no git). found
// is false for a path that commit does not have. A git that is cut off at
// gitTimeout, or fails once the base is known, is an error.
func readAtBase(rig *store.Rig, gitPath string) (text []byte, sha string, found bool, err error) {
	if strings.Contains(gitPath, "\n") {
		return nil, "", false, fmt.Errorf("%s:%q: a path with a newline in it cannot be read from the base", rig.Base, gitPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", rig.Path, "cat-file", "--batch")
	cmd.WaitDelay = time.Second
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", false, nil
	}
	out := bufio.NewReader(stdout)
	// ask sends one object name and reads git's answer to it: the object's
	// id, type and content, or known false for the "missing" (or
	// "ambiguous") git says of a name it cannot give an object for.
	ask := func(name string) (oid, kind string, body []byte, known bool, err error) {
		// A failed write shows as the read that follows it failing.
		io.WriteString(stdin, name+"\n")
		line, err := out.ReadString('\n')
		if err != nil {
			return "", "", nil, false, err
		}
		head, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), name+" ")
		if ok && (head == "missing" || head == "ambiguous") {
			return "", "", nil, false, nil
		}
		var size int64
		if n, _ := fmt.Sscanf(line, "%s %s %d\n", &oid, &kind, &size); n != 3 || size < 0 {
			return "", "", nil, false, fmt.Errorf("unexpected answer %q", strings.TrimSpace(line))
		}
		// The content is followed by a newline of git's own.
		body = make([]byte, size+1)
		if _, err := io.ReadFull(out, body); err != nil {
			return "", "", nil, false, err
		}
		return oid, kind, body[:size], true, nil
	}
	// failed ends git and says why the answer to name could not be read:
	// the deadline, else what git said as it went. Git is ended by closing
	// its stdin and waited for, not killed: one that has answered on stdout
	// may not have said why on stderr yet, and a kill would take that with
	// it. One that does not go at the end of its input goes at the deadline.
	failed := func(name string, cause error) error {
		stdin.Close()
		werr := cmd.Wait()
		timedOut := ctx.Err()
		if timedOut != nil {
			return fmt.Errorf("git cat-file --batch: %s: %w", name, timedOut)
		}
		if errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
			if werr != nil {
				cause = werr
			} else {
				cause = errors.New("git ended without an answer")
			}
		}
		return fmt.Errorf("git cat-file --batch: %s: %w: %s", name, cause, strings.TrimSpace(errb.String()))
	}

	commit := rig.Base + "^{commit}"
	sha, _, _, known, err := ask(commit)
	if err != nil || !known {
		// A git that ends with nothing said has no base to give, as one
		// that says so: only the deadline or an answer that is none of
		// git's is a failure here.
		ferr := failed(commit, err)
		if err == nil || (errors.Is(err, io.EOF) && !errors.Is(ferr, context.DeadlineExceeded)) {
			return nil, "", false, nil
		}
		return nil, "", false, ferr
	}
	blob := sha + ":" + gitPath
	_, kind, text, known, err := ask(blob)
	if err != nil {
		return nil, "", false, failed(blob, err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, "", false, fmt.Errorf("git cat-file --batch: %s: %w: %s", blob, err, strings.TrimSpace(errb.String()))
	}
	if !known {
		return nil, sha, false, nil
	}
	if kind != "blob" {
		return nil, "", false, fmt.Errorf("%s:%s is a %s, not a file", rig.Base, gitPath, kind)
	}
	return text, sha, true, nil
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
	case n.Runner == store.PeerRunner:
		return fmt.Sprintf("the yard %s picks it up (`%s` sends it there); use it when your part is ready for that yard.", n.Peer, n.Group)
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
