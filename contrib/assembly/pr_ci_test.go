package assembly

// pr-ci the way an exec group runs it for a pr bead in ciwait: merge-pr's yard
// (the contributors pack's pr flow, the group's env_file, stand-ins for gh and
// github-mint), a bare repository for the remote origin that keeps a pull
// request's head where GitHub keeps it (refs/pull/<n>/head), and a depot whose
// test command is a script of the base, as yardr's .yardr/test is, which does
// what a test chose through files in the stand-in's directory. A pull request's
// commit is made in a clone of that remote, so the depot has it only once
// pr-ci has fetched it.
//
// Each case has a yard of its own (a copy of the template yard, which is
// cheaper than a run of the script), so the cases run beside one another. A
// bead in ciwait carries reviewed_head, the commit its reviewer recorded
// before it reported done, as the pr flow's bead does.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/testload"
)

// baseCI is the depot's test script, a file of the base. It says where it ran
// and on what, then does what the files of $FAKE_GH tell it: ci-signals (end
// itself by SIGTERM that many times), ci-sleep (sleep, in a child whose pid it
// writes to sleep-pid), during (the commit the pull request's head moves to
// while it runs) and ci-exit (exit with that status). A tree with a file RED
// is red.
const baseCI = `echo "ci.sh of the base: in $(pwd), at $(git rev-parse HEAD), landing=${YARDR_LANDING-unset}, app=${GITHUB_APP_ID-unset}"
d=$FAKE_GH
if [ -s "$d/ci-signals" ]; then
  left=$(cat "$d/ci-signals"); echo $((left - 1)) >"$d/ci-signals"
  if [ "$left" -gt 0 ]; then echo "run ended by a signal"; kill -TERM $$; fi
fi
if [ -e "$d/signal-parent" ]; then
  sleep 300 &
  echo $! >"$d/sleep-pid"
  kill -TERM "$PPID"
  wait
fi
if [ -e "$d/ci-sleep" ]; then sh -c 'sleep 300 & echo $! >"$1"; wait' sh "$d/sleep-pid"; fi
[ ! -e "$d/during" ] || cp "$d/during" "$d/head"
[ ! -e "$d/ci-exit" ] || exit "$(cat "$d/ci-exit")"
test ! -e RED || { echo "RED is in the tree"; exit 1; }
echo "test: ci.sh @ $(git rev-parse --short HEAD): pass"
`

// endsAt is the yardr the script is given: the real one, that first notes in
// $FAKE_GH/ended where the script is, and whether its checkout of the pull
// request's code is still there, whenever the script ends its session by
// reporting or holding. The daemon stops a script as soon as it has, so what is
// to be taken away must be gone by then.
const endsAt = `#!/bin/sh
case "$1 $2" in
"bead done" | "bead hold")
  printf '%s: in %s, checkout %s\n' "$1 $2" "$(pwd)" "$([ -e "${YARDR_WORKTREE}-ci" ] && echo there || echo gone)" >>"$FAKE_GH/ended"
  ;;
esac
exec {yardr} "$@"
`

// ciYard is merge-pr's yard with what pr-ci asks of a repository.
type ciYard struct {
	*yard
	gh     string // the stand-ins' state directory
	origin string // the remote, where a pull request's head is kept
	work   string // a clone of it, where the commit of a pull request is made
}

func newCIYard(t *testing.T) *ciYard {
	t.Helper()
	y, gh := mergeYard(t)
	c := &ciYard{yard: y, gh: gh, origin: filepath.Join(t.TempDir(), "origin.git"), work: filepath.Join(t.TempDir(), "work")}
	wrapper := filepath.Join(t.TempDir(), "yardr")
	scripttest.Write(t, wrapper, strings.Replace(endsAt, "{yardr}", shellQuote(yardrBin), 1))
	y.bin = wrapper
	y.commit(y.repo, "ci.sh", baseCI)
	y.tests("ci.sh")
	y.git(".", "init", "-q", "--bare", "-b", "main", c.origin)
	y.git(y.repo, "remote", "add", "origin", c.origin)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.git(".", "clone", "-q", c.origin, c.work)
	return c
}

// pull makes the head of pull request n, a commit of files on top of main, and
// puts it on origin as GitHub keeps a pull request's head. It returns the
// commit, which the depot has not got.
func (c *ciYard) pull(n int, files map[string]string) string {
	c.t.Helper()
	c.git(c.work, "reset", "-q", "--hard", "origin/main")
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(c.work, name), []byte(text), 0o644); err != nil {
			c.t.Fatal(err)
		}
	}
	c.git(c.work, "add", "-A")
	c.git(c.work, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("pull request %d", n))
	c.git(c.work, "push", "-q", "-f", "origin", fmt.Sprintf("HEAD:refs/pull/%d/head", n))
	return c.git(c.work, "rev-parse", "HEAD")
}

// reviewed records on bead id the commit its reviewer read, as the reviewer does
// before it reports done; none (empty) records nothing, a reviewer that did not.
func (c *ciYard) reviewed(id, head string) {
	c.t.Helper()
	if head == "" {
		return
	}
	ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: c.home}).Socket())
	if _, err := st.UpdateBead(ctx, id, store.BeadPatch{Metadata: map[string]any{"reviewed_head": head}}, store.Expect{}, "test"); err != nil {
		c.t.Fatal(err)
	}
}

// fetched reports whether the depot has commit sha.
func (c *ciYard) fetched(sha string) bool {
	cmd := exec.Command("git", "cat-file", "-e", sha+"^{commit}")
	cmd.Dir, cmd.Env = c.repo, c.env
	return cmd.Run() == nil
}

// ci runs pr-ci on bead id once: with the stand-in as o and files make it, and
// the base's script told nothing but what files say.
func (c *ciYard) ci(id string, o mergeOpts, files map[string]string) {
	c.ciGroup(id, "pr-ci", o, files)
}

func (c *ciYard) ciGroup(id, group string, o mergeOpts, files map[string]string) {
	c.t.Helper()
	for _, name := range []string{"ci-signals", "ci-sleep", "sleep-pid", "ci-exit", "during", "ended", "signal-parent"} {
		if err := os.Remove(filepath.Join(c.gh, name)); err != nil && !os.IsNotExist(err) {
			c.t.Fatal(err)
		}
	}
	standIn(c.t, c.gh, o, files)
	c.exec("pr-ci", group, id)
}

// has asserts that bead id ended the run for case what in stage, held or not as
// hold says, still open, having tried no move the flow refuses (a script that
// reports only what the stage offers leaves none); and returns it.
func (c *ciYard) has(id, what, stage string, hold bool) store.Bead {
	c.t.Helper()
	b := c.beadOf(id)
	if b.Stage != stage || b.Hold != hold || b.Status == "closed" {
		c.t.Fatalf("%s: %s %s/%s hold=%v, want open in %s with hold=%v:\n%s", what, id, b.Stage, b.Status, b.Hold, stage, hold, c.notesOf(id))
	}
	if got := c.refusedMoves(id); len(got) != 0 {
		c.t.Errorf("%s: pr-ci tried moves the flow refuses: %v", what, got)
	}
	return b
}

// noted asserts that the notes of bead id for case what have every one of wants.
func (c *ciYard) noted(id, what string, wants ...string) {
	c.t.Helper()
	notes := c.notesOf(id)
	for _, want := range wants {
		if !strings.Contains(notes, want) {
			c.t.Errorf("%s: no note with %q:\n%s", what, want, notes)
		}
	}
}

// clean asserts that the run for case what left no checkout of the pull
// request's code: its directory is gone, and so is its record in the depot. And
// that the script had taken everything away before it ended its session, and
// was out of the directory it worked in.
func (c *ciYard) clean(id, what string) {
	c.t.Helper()
	if ended := strings.TrimSpace(readFile(c.t, filepath.Join(c.gh, "ended"))); !strings.HasSuffix(ended, ": in /, checkout gone") {
		c.t.Errorf("%s: the script ended its session %q, want it out of its working directory and the checkout gone", what, ended)
	}
	c.checkoutGone(id, what)
}

func (c *ciYard) checkoutGone(id, what string) {
	c.t.Helper()
	wt := filepath.Join(c.home, "worktrees", "r", id+"-ci")
	if _, err := os.Stat(wt); err == nil {
		c.t.Errorf("%s: the checkout of the pull request's code is still at %s", what, wt)
	}
	if list := c.git(c.repo, "worktree", "list", "--porcelain"); strings.Contains(list, id+"-ci") {
		c.t.Errorf("%s: the depot still lists the checkout of %s:\n%s", what, id, list)
	}
}

// pr-ci runs the depot's test command on the head GitHub names when it is the
// one the bead's reviewer recorded (in any case of its letters), in a checkout
// of its own: the base's copy of the script, however the pull request edited
// it, with YARDR_LANDING=1 and without the App's inputs, and records the head
// it passed on as ci_head and reports done; a red run, one past its deadline
// and a head that moved while it ran report changes, with the end of the output
// where there is one; a run that cannot run holds the bead, as one that a
// signal ended twice does, and one ended once is run again. Whichever it is,
// the checkout is gone afterwards, the depot's own checkout and refs are as
// they were, and GitHub was asked only to read the pull request, with a token
// that is nowhere but gh's environment. A test command that is no file of the
// base runs as a command, in the same environment: the landing's switch is set
// over whatever the group's cmd left in it.
func TestPRCIJudgesTheHead(t *testing.T) {
	for _, tc := range []struct {
		what     string
		files    map[string]string // the pull request's, on top of main
		stand    map[string]string // the stand-in's, besides the head; {sha} is the pull request's commit
		head     string            // what GitHub says the head is at first, the pull request's commit unless written
		reviewed string            // what the reviewer recorded, the head GitHub says unless written; {SHA} is the pull request's commit in capitals
		tests    string            // the depot's test command, ci.sh (a file of the base) unless written
		opts     string            // more for the group's cmd
		stage    string            // where the bead ends
		hold     bool
		signal   bool     // stop the script while its test command has a child running
		norun    bool     // the tests were never started
		wants    []string // in its notes, {url}, {sha} and {short} being the pull request's
		not      []string // and not
	}{
		{what: "green", files: map[string]string{"feature.txt": "new\n"}, reviewed: "{SHA}", stage: "approved",
			wants: []string{"the tests passed on {url} at {sha} (the base's copy of ci.sh,", "ci_head={sha}", "Its last line: test: ci.sh @ {short}: pass"}},
		// A command no file of the base has runs with sh -c, and gets the landing
		// run's switch as the base's copy does. The group's cmd leaves the switch
		// empty, as a stray one in the daemon's environment would: a depot's command
		// reads that as no landing, so it is set, not defaulted.
		{what: "a test command that is no file of the base", tests: `echo "the command: landing=${YARDR_LANDING-unset}, app=${GITHUB_APP_ID-unset}"`, opts: "YARDR_LANDING=", stage: "approved",
			wants: []string{"the tests passed on {url} at {sha} (the command echo ", "ci_head={sha}"}, not: []string{"the base's copy"}},
		{what: "red, though the pull request rewrote the test script to pass", files: map[string]string{"ci.sh": "echo the copy on the branch\nexit 0\n", "RED": "red\n"}, stage: "contributor",
			wants: []string{"the tests failed on {url} at {short} (the base's copy of ci.sh, exit 1); its output:", "ci.sh of the base: in ", "RED is in the tree"},
			not:   []string{"the copy on the branch"}},
		{what: "a script that exits 2", stand: map[string]string{"ci-exit": "2"}, stage: "ciwait", hold: true,
			wants: []string{"the tests could not run on {url} at {short} (the base's copy of ci.sh said so: exit 2); its output:"}},
		{what: "ended by a signal once", stand: map[string]string{"ci-signals": "1"}, stage: "approved",
			wants: []string{"were ended by signal 15 (exit 143), not by a failing test", "running them once more on the same commit", "the tests passed on {url}"}},
		{what: "ended by a signal twice", stand: map[string]string{"ci-signals": "2"}, stage: "ciwait", hold: true,
			wants: []string{"and again by signal 15 (exit 143) when run once more", "First run:\n", "Second run:\n"},
			not:   []string{"the tests failed"}},
		{what: "past the deadline", stand: map[string]string{"ci-sleep": ""}, opts: "YARDR_TEST_TIMEOUT=1", stage: "contributor",
			wants: []string{"ran past their deadline of 1s and were killed"}, not: []string{"ended by signal"}},
		{what: "a head that moved before the tests started", head: headB, stand: map[string]string{"head-next": "{sha}"}, stage: "contributor", norun: true,
			wants: []string{"the PR changed while CI started: GitHub said its head was 9b8a7c6 and says {short} now", "Nothing was run"}},
		{what: "a head that moved while the tests ran", stand: map[string]string{"during": headB}, stage: "contributor",
			wants: []string{"the PR changed while CI ran: the tests passed on {short} (the base's copy of ci.sh), but the head is 9b8a7c6 now", "Nothing was recorded"}},
		{what: "the script stopped while its tests run", stand: map[string]string{"signal-parent": ""}, signal: true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			c := newCIYard(t)
			depot := c.git(c.repo, "status", "--porcelain") + c.git(c.repo, "rev-parse", "HEAD")
			refs := c.git(c.repo, "for-each-ref")
			id, num := "r-j", 20
			sha := c.pull(num, tc.files)
			fill := strings.NewReplacer("{url}", prURLn(num), "{sha}", sha, "{short}", sha[:7], "{SHA}", strings.ToUpper(sha))
			c.prBeadIn(id, prURLn(num), "ciwait", false)
			c.reviewed(id, fill.Replace(or(tc.reviewed, or(tc.head, sha))))
			c.tests(or(tc.tests, "ci.sh"))
			opts := c.opts
			if tc.opts != "" {
				c.opts += " " + tc.opts
			}
			stand := map[string]string{}
			for name, text := range tc.stand {
				stand[name] = fill.Replace(text)
			}
			if tc.signal {
				c.ciGroup(id, "pr-ci-signal", mergeOpts{head: or(tc.head, sha)}, stand)
			} else {
				c.ci(id, mergeOpts{head: or(tc.head, sha)}, stand)
			}
			c.opts = opts
			if tc.signal {
				if b := c.beadOf(id); b.Stage != "ciwait" || b.Hold {
					t.Errorf("a stopped script moved the bead: %+v", b)
				}
				c.checkoutGone(id, tc.what)
				rawPID, err := os.ReadFile(filepath.Join(c.gh, "sleep-pid"))
				if err != nil {
					t.Fatalf("the test command did not start its child before stopping the script: %v", err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
				if err != nil {
					t.Fatalf("the test command's child pid %q: %v", rawPID, err)
				}
				deadline := time.Now().Add(testload.Scale(500 * time.Millisecond))
				for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if syscall.Kill(pid, 0) == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Errorf("the test command's child process %d outlived the stopped script", pid)
				}
			}
			if !tc.signal {
				b := c.has(id, tc.what, tc.stage, tc.hold)
				if tc.norun && strings.Contains(c.log, "ci.sh of the base") {
					t.Errorf("%s: the tests ran:\n%s", tc.what, c.log)
				}
				for _, want := range tc.wants {
					c.noted(id, tc.what, fill.Replace(want))
				}
				for _, not := range tc.not {
					if notes := c.notesOf(id); strings.Contains(notes, not) {
						t.Errorf("%s: a note has %q:\n%s", tc.what, not, notes)
					}
				}
				c.clean(id, tc.what)
				// Done records the head it passed on, and nothing else does.
				if got, _ := b.Metadata["ci_head"].(string); (tc.stage == "approved" && got != sha) || (tc.stage != "approved" && b.Metadata["ci_head"] != nil) {
					t.Errorf("%s: ci_head = %v, want %s only for a bead that passed", tc.what, b.Metadata["ci_head"], sha)
				}
				for _, call := range ghCalls(t, c.gh) {
					if call != "" && !strings.HasPrefix(call, "pr view ") {
						t.Errorf("%s: gh was asked more than to read the pull request: %q", tc.what, call)
					}
				}
				switch tc.what {
				case "green":
					// The depot has the commit now, and the test ran on it, in a
					// checkout of its own, with the landing's switch set and the App's
					// inputs left out of its environment. TestMain leaves no YARDR_
					// variable in this process, so the switch is pr-ci's own.
					c.git(c.repo, "cat-file", "-e", sha+"^{commit}")
					if want := "ci.sh of the base: in " + filepath.Join(c.home, "worktrees", "r", id+"-ci") + ", at " + sha + ", landing=1, app=unset"; !strings.Contains(c.log, want) {
						t.Errorf("green: the log lacks %q:\n%s", want, c.log)
					}
					if n := countCalls(t, c.gh, "pr view "); n != 2 {
						t.Errorf("green: the pull request was read %d times, want twice (before the run, and after it):\n%v", n, ghCalls(t, c.gh))
					}
					for what, text := range map[string]string{"the log": c.log, "the notes": c.notesOf(id), "gh's calls": readFile(t, filepath.Join(c.gh, "calls"))} {
						if strings.Contains(text, mintToken) {
							t.Errorf("green: %s hold the token", what)
						}
					}
				case "a test command that is no file of the base":
					// It ran as a command with the same environment: the switch set over
					// the empty one the group gave it, and the App's inputs gone.
					if want := "the command: landing=1, app=unset"; !strings.Contains(c.log, want) {
						t.Errorf("%s: the log lacks %q:\n%s", tc.what, want, c.log)
					}
				case "past the deadline":
					// What the run started is killed with it.
					raw, err := os.ReadFile(filepath.Join(c.gh, "sleep-pid"))
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					for deadline := time.Now().Add(testload.Scale(5 * time.Second)); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
						if time.Now().After(deadline) {
							_ = syscall.Kill(pid, syscall.SIGKILL)
							t.Fatalf("the sleep the test script started (pid %d) outlived the deadline's kill", pid)
						}
					}
				}
			}
			if after := c.git(c.repo, "status", "--porcelain") + c.git(c.repo, "rev-parse", "HEAD"); after != depot {
				t.Errorf("the depot's checkout changed:\n%s\n%s", depot, after)
			}
			if after := c.git(c.repo, "for-each-ref"); after != refs {
				t.Errorf("the depot's refs changed:\n%s\n%s", refs, after)
			}
		})
	}

	// A bot's bump of Go modules, end to end through the real pr flow: the
	// reviewer reads the head, records it as reviewed_head, marks the bump
	// merge-ok and reports done, which has it wait in ciwait. The bot rebases
	// before pr-ci runs and the event of that push is lost (nothing here
	// delivers one, which is what a missed webhook is), so the bead is still in
	// ciwait: pr-ci holds it at a head no reviewer read, and fetches nothing.
	// The way back the hold gives is a review: the reviewer records the head it
	// now reads, pr-ci passes the bump to approved with that head, and merge-pr
	// merges it. The yard's CI is the only CI asked.
	t.Run("a bot's bump rebased after its review", func(t *testing.T) {
		c := newCIYard(t)
		ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: c.home}).Socket())
		id, num := "r-e", 60
		read := c.pull(num, map[string]string{"go.sum": "bump\n"})
		c.prBeadIn(id, prURLn(num), "review", false)
		reviewerDone := func(head string) {
			t.Helper()
			if _, err := st.UpdateBead(ctx, id, store.BeadPatch{AddLabels: []string{"merge-ok"}, Metadata: map[string]any{"reviewed_head": head}}, store.Expect{}, "test"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.Done(ctx, id, "done", "Ready", store.Expect{Stage: "review"}, "test"); err != nil {
				t.Fatal(err)
			}
			c.has(id, "reported done by the reviewer", "ciwait", false)
		}
		reviewerDone(read)
		pushed := c.pull(num, map[string]string{"go.sum": "bump, rebased\n"})
		c.ci(id, mergeOpts{head: pushed}, nil)
		c.has(id, "pushed to after its review, the event lost", "ciwait", true)
		c.noted(id, "pushed to after its review", "head "+pushed+" is not the reviewed "+read+": needs re-review")
		c.clean(id, "pushed to after its review")
		if c.fetched(pushed) {
			t.Errorf("pr-ci fetched the head %s, which no reviewer read", pushed)
		}
		if b := c.beadOf(id); b.Metadata["ci_head"] != nil {
			t.Errorf("ci_head = %v on a bead held for a head no reviewer read", b.Metadata["ci_head"])
		}
		if _, err := st.Advance(ctx, id, "review", store.Expect{}, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetHold(ctx, id, false, store.Expect{}, "test"); err != nil {
			t.Fatal(err)
		}
		reviewerDone(pushed)
		c.ci(id, mergeOpts{head: pushed}, nil)
		if b := c.has(id, "tested at the head its reviewer read", "approved", false); b.Metadata["ci_head"] != pushed {
			t.Fatalf("ci_head = %v, want the head its reviewer read, %s", b.Metadata["ci_head"], pushed)
		}
		standIn(t, c.gh, mergeOpts{head: pushed}, nil)
		c.exec("merge-pr", "pr-merges", id)
		c.mergedAs(c.gh, id, "merged by merge-pr", "merged", "pr merge "+prURLn(num)+" --rebase --delete-branch --match-head-commit "+pushed)
		if n := countCalls(t, c.gh, "api "); n != 0 {
			t.Errorf("GitHub was asked for CI, which is the yard's own:\n%v", ghCalls(t, c.gh))
		}
	})
}

// What is no verdict on a pull request holds the bead, with a note that says
// why and nothing run: a bead that is not in ciwait, where no reviewer has read
// the code; a head that is not the one its reviewer recorded (the pull request
// pushed to since, or its bead's link swapped for another pull request's), or a
// bead whose reviewer recorded none or no full sha; a depot with no test
// command; a pull request that is not open or that GitHub will not show; a
// remote that does not give the head or does not have the pull request; a token
// that cannot be had; a link that is no pull request address. Nothing is fetched
// or run for the ones that are known before it is, and the bead's checkout is
// never made.
func TestPRCIHoldsWhatItCannotJudge(t *testing.T) {
	none := ""
	for _, tc := range []struct {
		what      string
		stage     string            // the bead's stage, ciwait unless written
		url       string            // its link, its pull request's unless written
		bare      bool              // origin has no head for its pull request
		stand     map[string]string // the stand-in's, besides the head
		head      string            // what GitHub says the head is, the pull request's unless written
		reviewed  string            // what the reviewer recorded, the head GitHub says unless written, - for nothing
		tests     *string           // the depot's test command, ci.sh unless written
		change    func(c *ciYard)   // changes the group for this run
		wants     []string          // in its notes, {n}, {id} and {sha} being the pull request's number, the bead and the commit
		not       []string          // and not
		asked     bool              // GitHub is asked about the pull request
		unfetched bool              // and nothing of it was fetched, whatever origin has
	}{
		{what: "a bead no reviewer has read the pull request of", stage: "review",
			wants: []string{"is not in ciwait", "runs a pull request's code nowhere before it"}},
		{what: "a head that is not the one its reviewer read", reviewed: headB, asked: true, unfetched: true,
			wants: []string{"head {sha} is not the reviewed " + headB + ": needs re-review", "Nothing of https://github.com/o/r/pull/{n} was fetched or run", "yardr bead advance {id} --to review, then yardr bead unhold {id}"}},
		{what: "a bead whose reviewer recorded no head", reviewed: "-", asked: true, unfetched: true,
			wants: []string{"{id} has no reviewed_head that is a full commit sha", "the head of https://github.com/o/r/pull/{n}, {sha}", "yardr bead advance {id} --to review"}},
		{what: "a recorded head that is no sha, and not copied to the bead", reviewed: strings.Repeat("z", 40), asked: true, unfetched: true,
			wants: []string{"{id} has no reviewed_head that is a full commit sha"}, not: []string{strings.Repeat("z", 40)}},
		{what: "a depot with no test command", tests: &none,
			wants: []string{"has no test command", "yardr depot add r ", "a pull request is not called green untested"}},
		{what: "a link that is no pull request address", url: "https://github.com/o/r/pull/31/files",
			wants: []string{"which is not a pull request address", "not running the tests"}},
		{what: "a pull request that is closed", stand: map[string]string{"state": "CLOSED"}, asked: true,
			wants: []string{"is in state CLOSED, not OPEN", "Not running anything"}},
		{what: "a pull request gh cannot read", stand: map[string]string{"view-fail": "HTTP 404: Not Found"}, asked: true,
			wants: []string{"failed: HTTP 404: Not Found"}},
		{what: "a head that is no commit sha", head: "../../etc", asked: true,
			wants: []string{"that is no commit sha"}},
		{what: "a pull request the remote does not have", bare: true, asked: true,
			wants: []string{"fetching refs/pull/{n}/head from origin", "failed"}},
		{what: "a remote that gives another commit than GitHub's head", head: headB, asked: true,
			wants: []string{"origin did not give the commit " + headB, "is origin the repository the pull request is on?"}},
		{what: "a mint that fails", stand: map[string]string{"mint-fail": "PRIVATE_KEY_VALUE_SHOULD_NOT_ESCAPE"},
			wants: []string{"minting a token for o/r failed", "not running the tests"}},
		{what: "no env_file", change: func(c *ciYard) { c.envFile, c.opts = "", c.opts+" GITHUB_APP_ID= GITHUB_APP_PRIVATE_KEY_FILE=" },
			wants: []string{"has no GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE", "env_file"}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			c := newCIYard(t)
			id, num := "r-h", 30
			sha := headA
			if !tc.bare && tc.url == "" {
				sha = c.pull(num, map[string]string{"feature.txt": "new\n"})
			}
			stage, url, tests := or(tc.stage, "ciwait"), or(tc.url, prURLn(num)), "ci.sh"
			if tc.tests != nil {
				tests = *tc.tests
			}
			c.prBeadIn(id, url, stage, false)
			fill := strings.NewReplacer("{n}", strconv.Itoa(num), "{id}", id, "{sha}", sha)
			if reviewed := or(tc.reviewed, or(tc.head, sha)); reviewed != "-" {
				c.reviewed(id, fill.Replace(reviewed))
			}
			c.tests(tests)
			envFile, opts := c.envFile, c.opts
			if tc.change != nil {
				tc.change(c)
			}
			c.ci(id, mergeOpts{head: or(tc.head, sha)}, tc.stand)
			c.envFile, c.opts = envFile, opts

			c.has(id, tc.what, stage, true)
			for _, want := range tc.wants {
				c.noted(id, tc.what, fill.Replace(want))
			}
			for _, not := range tc.not {
				if notes := c.notesOf(id); strings.Contains(notes, not) {
					t.Errorf("%s: a note has %q:\n%s", tc.what, not, notes)
				}
			}
			c.clean(id, tc.what)
			if tc.unfetched && c.fetched(sha) {
				t.Errorf("%s: the depot has the commit %s of the pull request, which pr-ci was to leave where it was", tc.what, sha)
			}
			if b := c.beadOf(id); b.Metadata["ci_head"] != nil {
				t.Errorf("%s: ci_head = %v on a bead no verdict was given for", tc.what, b.Metadata["ci_head"])
			}
			if _, err := os.Stat(filepath.Join(c.gh, "calls")); tc.asked != (err == nil) {
				t.Errorf("%s: GitHub asked = %v, want %v:\n%v", tc.what, err == nil, tc.asked, ghCalls(t, c.gh))
			}
			for _, call := range ghCalls(t, c.gh) {
				if call != "" && !strings.HasPrefix(call, "pr view ") {
					t.Errorf("%s: gh was asked more than to read the pull request: %q", tc.what, call)
				}
			}
			if tc.what == "a mint that fails" {
				for where, text := range map[string]string{"log": c.log, "notes": c.notesOf(id)} {
					if strings.Contains(text, "PRIVATE_KEY_VALUE_SHOULD_NOT_ESCAPE") {
						t.Errorf("the mint's stderr reached the %s", where)
					}
				}
			}
			// No run reached the tests.
			if strings.Contains(c.log, "ci.sh of the base") {
				t.Errorf("%s: the tests ran:\n%s", tc.what, c.log)
			}
		})
	}

	// A link swapped after the review is a head nobody read as well: the bead
	// tracks another pull request than the one its reviewer read, and pr-ci
	// asks GitHub about the pull request the link names now, while the head the
	// reviewer recorded stays with the bead. Only a person swaps the link of a
	// bead past triage (a bead's agent is refused by store.AddLink), as a
	// maintainer would by hand: the link removed, the other added.
	t.Run("a link swapped for another pull request's after the review", func(t *testing.T) {
		c := newCIYard(t)
		id, num, to := "r-s", 70, 71
		read := c.pull(num, map[string]string{"feature.txt": "read\n"})
		swapped := c.pull(to, map[string]string{"feature.txt": "swapped in\n"})
		c.prBeadIn(id, prURLn(num), "ciwait", false)
		c.reviewed(id, read)
		ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: c.home}).Socket())
		if err := st.RemoveLink(ctx, id, prURLn(num), "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddLink(ctx, id, store.NewLink{Kind: store.LinkTracks, Source: "github", URL: prURLn(to)}, "test"); err != nil {
			t.Fatal(err)
		}
		c.ci(id, mergeOpts{head: swapped}, nil)
		what := "a link swapped for another pull request's after the review"
		c.has(id, what, "ciwait", true)
		c.noted(id, what, "head "+swapped+" is not the reviewed "+read+": needs re-review", "Nothing of "+prURLn(to)+" was fetched or run")
		c.clean(id, what)
		if c.fetched(swapped) {
			t.Errorf("%s: the depot has the commit %s of the pull request now linked, which pr-ci was to leave where it was", what, swapped)
		}
		if b := c.beadOf(id); b.Metadata["ci_head"] != nil {
			t.Errorf("%s: ci_head = %v on a bead no verdict was given for", what, b.Metadata["ci_head"])
		}
		// The head GitHub gave is the one of the pull request the link names now.
		for _, call := range ghCalls(t, c.gh) {
			if call != "" && !strings.HasPrefix(call, "pr view "+prURLn(to)+" ") {
				t.Errorf("%s: GitHub was asked %q, want a read of %s alone", what, call, prURLn(to))
			}
		}
		if countCalls(t, c.gh, "pr view "+prURLn(to)+" ") == 0 {
			t.Errorf("%s: GitHub was not asked about %s:\n%v", what, prURLn(to), ghCalls(t, c.gh))
		}
		if strings.Contains(c.log, "ci.sh of the base") {
			t.Errorf("%s: the tests ran:\n%s", what, c.log)
		}
	})
}

// pr-ci takes its binding of a bead to its pull request, and its token, from
// merge-pr's text, copied: the scripts are installed one file each, so there is
// none to source. A change to one is made in both, as to the credit loop of the
// tests' deadline (TestCreditLoopsAreMergeLocals).
func TestPRCIBindsAsMergePRDoes(t *testing.T) {
	want, got := markedBlock(t, "merge-pr", bindingOpen, bindingClose), markedBlock(t, "pr-ci", bindingOpen, bindingClose)
	if got != want {
		t.Errorf("pr-ci's github_binding differs from merge-pr's: change both\nmerge-pr:\n%s\npr-ci:\n%s", want, got)
	}
}
