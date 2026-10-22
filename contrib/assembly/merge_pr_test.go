package assembly

// merge-pr the way an exec group runs it for a pr bead in approved: the
// real pr flow of the contributors pack in the yard, the group's env_file
// giving the App's id and key file, a stand-in github-mint that answers a
// token for the scope it is asked, and a stand-in gh that keeps one pull
// request's state in a directory, refuses a call that does not carry that
// token, and merges with GitHub's rule for --match-head-commit.

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

const (
	// Two commits of the pull request, whose first seven digits differ.
	headA = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	headB = "9b8a7c6d5e4f30211203a4b5c6d7e8f9a0b1c2d3"
	// The token the stand-in mint answers, and the only one gh accepts.
	mintToken = "ghs_stand_in_token"
)

// fakeMergeGh is gh as far as merge-pr uses it. Its state, in $FAKE_GH:
// calls (one argv per line, never the token), head (the pull request's
// head commit), and files a test writes to change the answer: state
// (OPEN unless it says MERGED or CLOSED), bot (the author is a bot),
// head-owner and head-name (the repository the head is in, o/r unless
// written), checks (what the check runs of any commit are, which may be two
// JSON documents, as two pages are), view-fail, api-fail and refuse (the
// error of that call), and race (the commit the head moves to as the merge
// starts). A call whose GH_TOKEN is not the mint's is refused as GitHub
// refuses a bad credential; merging with --match-head-commit refuses, in
// GitHub's words, when the head is another commit.
const fakeMergeGh = `#!/bin/sh
d=$FAKE_GH
echo "$*" >>"$d/calls"
if [ "$GH_TOKEN" != ` + mintToken + ` ]; then echo "HTTP 401: Bad credentials" >&2; exit 1; fi
case "$1 $2" in
"pr view")
  if [ -e "$d/view-fail" ]; then cat "$d/view-fail" >&2; exit 1; fi
  printf '{"number":7,"state":"%s","headRefOid":"%s","headRepository":{"name":"%s"},"headRepositoryOwner":{"login":"%s"},"author":{"is_bot":%s,"login":"%s"}}\n' \
    "$(cat "$d/state" 2>/dev/null || echo OPEN)" "$(cat "$d/head")" "$(cat "$d/head-name" 2>/dev/null || echo r)" \
    "$(cat "$d/head-owner" 2>/dev/null || echo o)" "$([ -e "$d/bot" ] && echo true || echo false)" "$([ -e "$d/bot" ] && echo app/dependabot || echo jo)" ;;
"api repos/o/r/commits/"*)
  if [ -e "$d/api-fail" ]; then cat "$d/api-fail" >&2; exit 1; fi
  if [ -e "$d/checks" ]; then cat "$d/checks"; else echo '{"total_count":1,"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}'; fi ;;
"pr merge")
  if [ -e "$d/refuse" ]; then cat "$d/refuse" >&2; exit 1; fi
  if [ -e "$d/race" ]; then cp "$d/race" "$d/head"; fi
  match=
  while [ $# -gt 0 ]; do
    [ "$1" = --match-head-commit ] && match=$2
    shift
  done
  if [ -n "$match" ] && [ "$match" != "$(cat "$d/head")" ]; then
    echo "GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)" >&2; exit 1
  fi
  echo MERGED >"$d/state"
  echo "Merged pull request o/r#7" ;;
*) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

// fakeGithubMint is contrib/github-mint as far as merge-pr uses it: it keeps
// the {inputs, scope} it is asked, answers the token, and fails with the
// error in mint-fail when a test wrote one.
const fakeGithubMint = `#!/bin/sh
cat >"$FAKE_GH/mint-stdin"
if [ -e "$FAKE_GH/mint-fail" ]; then cat "$FAKE_GH/mint-fail" >&2; exit 1; fi
printf '{"token":"` + mintToken + `","expires_at":"2099-01-01T00:00:00Z"}'
`

// mergeOpts is how a test's pull request, and the bead tracking it, differ
// from a bot's bump of its own repository that waits in approved with
// merge-ok and nothing else.
type mergeOpts struct {
	human     bool   // the author is not a bot
	headOwner string // the owner of the head's repository, o (the pull request's own) unless written
	head      string // the head commit, headA unless written
	yes       string // metadata merge_head, none unless written
	noLabel   bool   // without merge-ok
	url       string // the tracks link, prURL unless written
	noLink    bool   // tracking nothing
	noEnvFile bool   // the group has no env_file
}

// mergeYard is a yard with pr bead r-p in approved of the contributors pack's
// pr flow, tracking o/r#7, and the stand-ins for gh and github-mint around
// the exec group "pr-merges". gh is the stand-in's state directory.
func mergeYard(t *testing.T, o mergeOpts) (y *yard, gh string) {
	t.Helper()
	y = newYard(t)
	gh = t.TempDir()
	if err := os.Mkdir(filepath.Join(gh, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(gh, "bin", "gh"), fakeMergeGh, 0o755)
	mint := filepath.Join(gh, "bin", "github-mint")
	write(mint, fakeGithubMint, 0o755)
	write(filepath.Join(gh, "head"), or(o.head, headA)+"\n", 0o644)
	if o.headOwner != "" {
		write(filepath.Join(gh, "head-owner"), o.headOwner+"\n", 0o644)
	}
	if !o.human {
		write(filepath.Join(gh, "bot"), "", 0o644)
	}
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })

	// The group's environment: the App's id and the path of its key file,
	// which is no secret, in a file for the owner alone, as the runner
	// requires of an env_file. The stand-in mint reads neither.
	if !o.noEnvFile {
		y.envFile = filepath.Join(t.TempDir(), "pr-merges.env")
		write(y.envFile, "GITHUB_APP_ID=123456\nGITHUB_APP_PRIVATE_KEY_FILE="+filepath.Join(gh, "app.pem")+"\n", 0o600)
	}
	y.opts = "YARDR_GITHUB_MINT=" + shellQuote(mint)
	if o.noEnvFile {
		// Whatever the machine running the test exports is not the group's.
		y.opts += " GITHUB_APP_ID= GITHUB_APP_PRIVATE_KEY_FILE="
	}

	// The pr flow exactly as the contributors pack carries it.
	sub, err := fs.Sub(packs.FS, packs.Contributors)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Load(sub, packs.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	ctx := context.Background()
	for _, f := range p.Flows {
		if f.Name == "pr" {
			if _, err := st.PutFlow(ctx, f.StoreFlow("contributors.pr"), "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	y.pointFlow("r", "pr", "contributors.pr")
	nb := store.NewBead{Title: "bump", ID: "r-p", Depot: "r", Type: "pr", Stage: "approved", Priority: store.DefaultPriority}
	if !o.noLabel {
		nb.Labels = []string{"merge-ok"}
	}
	if !o.noLink {
		nb.Links = []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: or(o.url, prURL)}}
	}
	if o.yes != "" {
		nb.Metadata = map[string]any{"merge_head": o.yes}
	}
	if _, err := st.CreateBead(ctx, nb, "test"); err != nil {
		t.Fatal(err)
	}
	return y, gh
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// writeGh writes a file of the stand-in's state.
func writeGh(t *testing.T, gh, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(gh, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// heldUnmerged asserts that merge-pr held r-p in approved, open, with a
// note that has every one of wants, merged nothing, and made no move the
// flow refused: the outcomes at approved are merged and dropped, and a
// script that cannot do either holds.
func heldUnmerged(t *testing.T, y *yard, gh string, wants ...string) {
	t.Helper()
	if e := y.beadOf("r-p"); !e.Hold || e.Stage != "approved" || e.Status == "closed" {
		t.Fatalf("r-p %s/%s hold=%v, want held in approved, not closed", e.Stage, e.Status, e.Hold)
	}
	notes := y.notesOf("r-p")
	for _, want := range wants {
		if !strings.Contains(notes, want) {
			t.Errorf("no note with %q:\n%s", want, notes)
		}
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Errorf("gh pr merge called %d times on a pull request that was to be held:\n%v", n, ghCalls(t, gh))
	}
	if got := y.refusedOf("r-p"); len(got) != 0 {
		t.Errorf("merge-pr tried moves the flow refuses: %v", got)
	}
}

// A bot's bump of its own repository is merged at the head it has now, with
// a rebase, deleting its branch, as the App with a token minted for that
// repository alone; the bead ends merged. The token is in the environment of
// gh and nowhere else: not in an argument, the log or a note.
func TestMergePRMergesABotsBumpAtItsLiveHead(t *testing.T) {
	y, gh := mergeYard(t, mergeOpts{head: headB})

	y.exec("merge-pr", "pr-merges", "r-p")

	if e := y.beadOf("r-p"); e.Stage != "merged" || e.Status != "closed" || e.Hold {
		t.Fatalf("r-p %s/%s hold=%v, want merged and closed", e.Stage, e.Status, e.Hold)
	}
	want := "pr merge " + prURL + " --rebase --delete-branch --match-head-commit " + headB
	if n := countCalls(t, gh, want); n != 1 {
		t.Fatalf("%q called %d times, want once:\n%v", want, n, ghCalls(t, gh))
	}
	if n := countCalls(t, gh, "api repos/o/r/commits/"+headB+"/check-runs"); n != 1 {
		t.Fatalf("the check runs of %s read %d times, want once:\n%v", headB, n, ghCalls(t, gh))
	}
	// The mint was asked for the pull request's repository, from the id
	// and key file the group's env_file gave.
	var asked struct {
		Inputs map[string]string `json:"inputs"`
		Scope  string            `json:"scope"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(gh, "mint-stdin"))), &asked); err != nil {
		t.Fatal(err)
	}
	if asked.Scope != "o/r" || asked.Inputs["GITHUB_APP_ID"] != "123456" || asked.Inputs["GITHUB_APP_PRIVATE_KEY_FILE"] != filepath.Join(gh, "app.pem") {
		t.Fatalf("the mint was asked %+v, want scope o/r and the env_file's id and key file", asked)
	}
	if notes := y.notesOf("r-p"); !strings.Contains(notes, "merged "+prURL+" at "+headB+" (rebase)") {
		t.Fatalf("no note with the merge:\n%s", notes)
	}
	if got := y.refusedOf("r-p"); len(got) != 0 {
		t.Errorf("merge-pr tried moves the flow refuses: %v", got)
	}
	for what, text := range map[string]string{"the log": y.log, "the notes": y.notesOf("r-p"), "gh's calls": readFile(t, filepath.Join(gh, "calls"))} {
		if strings.Contains(text, mintToken) {
			t.Errorf("%s hold the token", what)
		}
	}
}

// A bot's bump of its own repository is merged at the head it has, also when
// a maintainer's yes recorded another (an Actions bump they said yes to, which
// the bot rebased since): the rule that holds a moved head is for the pull
// requests whose head is no bot's to move.
func TestMergePRBotBumpIgnoresARecordedHead(t *testing.T) {
	y, gh := mergeYard(t, mergeOpts{head: headB, yes: headA})

	y.exec("merge-pr", "pr-merges", "r-p")

	if e := y.beadOf("r-p"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("r-p %s/%s, want merged and closed:\n%s", e.Stage, e.Status, y.notesOf("r-p"))
	}
	want := "pr merge " + prURL + " --rebase --delete-branch --match-head-commit " + headB
	if n := countCalls(t, gh, want); n != 1 {
		t.Fatalf("%q called %d times, want once:\n%v", want, n, ghCalls(t, gh))
	}
}

// Everything but a bot's pull request from its own repository is merged at
// the head a maintainer said yes to, and at no other: the yes is one command,
// the label and the sha together. A fork is merged with a merge commit and
// its branch left alone, being its author's; a human's branch of the
// repository's own is rebased and deleted; a bot's pull request from a fork
// is no bot's of the repository, and waits for a yes as a human's does.
func TestMergePRMergesAtTheHeadOfTheYes(t *testing.T) {
	for _, c := range []struct {
		name string
		o    mergeOpts
		how  string // the flags the merge is made with
		note string
	}{
		{"a human's fork", mergeOpts{human: true, headOwner: "mallory", noLabel: true},
			"--merge --match-head-commit " + headA, "(merge commit)"},
		{"a human's branch of the repository", mergeOpts{human: true, noLabel: true},
			"--rebase --delete-branch --match-head-commit " + headA, "(rebase)"},
		{"a bot's fork", mergeOpts{headOwner: "mallory", noLabel: true},
			"--merge --match-head-commit " + headA, "(merge commit)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, gh := mergeYard(t, c.o)
			// Without the yes the pull request is not for the yard to merge.
			y.yardr("bead", "update", "r-p", "--add-label", "merge-ok", "--meta", "merge_head="+headA)

			y.exec("merge-pr", "pr-merges", "r-p")

			if e := y.beadOf("r-p"); e.Stage != "merged" || e.Status != "closed" {
				t.Fatalf("r-p %s/%s, want merged and closed:\n%s", e.Stage, e.Status, y.notesOf("r-p"))
			}
			want := "pr merge " + prURL + " " + c.how
			if n := countCalls(t, gh, want); n != 1 {
				t.Fatalf("%q called %d times, want once:\n%v", want, n, ghCalls(t, gh))
			}
			if !strings.Contains(y.notesOf("r-p"), "at "+headA+" "+c.note) {
				t.Fatalf("no note with the merge %s:\n%s", c.note, y.notesOf("r-p"))
			}
		})
	}
}

// A pull request that is not a bot's bump of its own repository is held
// before anything is asked of CI or merged when its bead has no yes, or one
// that cannot be taken: the note names the command that says it. A fork that
// was pushed to after the yes is held too, naming both commits.
func TestMergePRHoldsForTheYes(t *testing.T) {
	yes := "yardr bead update r-p --add-label merge-ok --meta merge_head="
	for _, c := range []struct {
		name  string
		o     mergeOpts
		wants []string
	}{
		{"a human's fork with no yes", mergeOpts{human: true, headOwner: "mallory"},
			[]string{"is not a bot's pull request from a branch of o/r itself (author jo, head in mallory/r)", "has none (merge_head)", yes}},
		{"a human's branch with no yes", mergeOpts{human: true},
			[]string{"is not a bot's pull request from a branch of o/r itself (author jo, head in o/r)", yes}},
		{"a bot's fork with no yes", mergeOpts{headOwner: "mallory"},
			[]string{"(author app/dependabot, head in mallory/r)", yes}},
		{"a fork pushed to after the yes", mergeOpts{human: true, headOwner: "mallory", head: headB, yes: headA},
			[]string{"moved since your yes (yes at 4f2c9e1, head now 9b8a7c6)", "The head is now " + headB, yes}},
		{"a yes with a short sha", mergeOpts{human: true, headOwner: "mallory", yes: headA[:7]},
			[]string{"not a full commit sha", yes}},
		{"a yes with no sha in it", mergeOpts{human: true, headOwner: "mallory", yes: "looks good"},
			[]string{"not a full commit sha", yes}},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, gh := mergeYard(t, c.o)

			y.exec("merge-pr", "pr-merges", "r-p")

			heldUnmerged(t, y, gh, c.wants...)
			if n := countCalls(t, gh, "api "); n != 0 {
				t.Errorf("CI was read for a pull request waiting for a yes:\n%v", ghCalls(t, gh))
			}
		})
	}
}

// The yes is given to the head, and the head alone: a pull request that
// moves after it is held, and merged only after they say yes again, to the
// head they looked at then.
func TestMergePRNeedsTheYesAgainWhenTheHeadMoves(t *testing.T) {
	y, gh := mergeYard(t, mergeOpts{human: true, headOwner: "mallory", head: headB, yes: headA})

	y.exec("merge-pr", "pr-merges", "r-p")
	heldUnmerged(t, y, gh, "moved since your yes")

	// They look at the new head and say yes to it, and unhold the bead.
	y.yardr("bead", "update", "r-p", "--add-label", "merge-ok", "--meta", "merge_head="+headB)
	y.yardr("bead", "unhold", "r-p")
	y.exec("merge-pr", "pr-merges", "r-p")

	if e := y.beadOf("r-p"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("r-p %s/%s after the new yes, want merged:\n%s", e.Stage, e.Status, y.notesOf("r-p"))
	}
	if n := countCalls(t, gh, "pr merge "+prURL+" --merge --match-head-commit "+headB); n != 1 {
		t.Fatalf("the merge at the new head called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
}

// CI must be green on the commit that is merged: at least one check run,
// every one of them completed and success, neutral or skipped, on any of
// the pages the check runs come in. Anything else holds the bead and names
// the check.
func TestMergePRHoldsUnlessCIIsGreen(t *testing.T) {
	green := `{"name":"test","status":"completed","conclusion":"success"}`
	for _, c := range []struct {
		name, checks string
		wants        []string
	}{
		{"a failed check", `{"check_runs":[` + green + `,{"name":"lint","status":"completed","conclusion":"failure"}]}`,
			[]string{"CI is not green on 4f2c9e1 in o/r: lint (failure)"}},
		{"a check still running", `{"check_runs":[{"name":"test","status":"in_progress","conclusion":null}]}`,
			[]string{"CI is not green on 4f2c9e1 in o/r: test (in_progress)"}},
		{"a cancelled check", `{"check_runs":[{"name":"test","status":"completed","conclusion":"cancelled"}]}`,
			[]string{"test (cancelled)"}},
		{"a failure on the second page", `{"check_runs":[` + green + `]}` + "\n" + `{"check_runs":[{"name":"vet","status":"completed","conclusion":"timed_out"}]}`,
			[]string{"vet (timed_out)"}},
		{"no check at all", `{"total_count":0,"check_runs":[]}`,
			[]string{"no check run on 4f2c9e1 in o/r: nothing says CI passed there"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, gh := mergeYard(t, mergeOpts{})
			writeGh(t, gh, "checks", c.checks)

			y.exec("merge-pr", "pr-merges", "r-p")

			heldUnmerged(t, y, gh, c.wants...)
		})
	}

	// Neutral and skipped runs pass beside a success.
	t.Run("success, skipped and neutral", func(t *testing.T) {
		y, gh := mergeYard(t, mergeOpts{})
		writeGh(t, gh, "checks", `{"check_runs":[{"name":"test","status":"completed","conclusion":"success"},{"name":"docs","status":"completed","conclusion":"skipped"},{"name":"codeql","status":"completed","conclusion":"neutral"}]}`)

		y.exec("merge-pr", "pr-merges", "r-p")

		if e := y.beadOf("r-p"); e.Stage != "merged" {
			t.Fatalf("r-p in %s, want merged:\n%s", e.Stage, y.notesOf("r-p"))
		}
	})
}

// The pull request is the one the bead tracks, and only when the bead says
// the yard may merge it. A bead without merge-ok, one tracking nothing, and
// one tracking something that is not a pull request address are held, and
// nothing is asked of GitHub.
func TestMergePRRefusesWhatItWasNotGiven(t *testing.T) {
	for _, c := range []struct {
		name  string
		o     mergeOpts
		wants []string
	}{
		{"no label", mergeOpts{noLabel: true},
			[]string{"does not carry merge-ok", "yardr bead update r-p --add-label merge-ok --meta merge_head="}},
		{"tracking nothing", mergeOpts{noLink: true}, []string{"tracks no GitHub pull request"}},
		{"an issue", mergeOpts{url: "https://github.com/o/r/issues/7"}, []string{"which is not a pull request address"}},
		{"a path past the number", mergeOpts{url: "https://github.com/o/r/pull/7/files"}, []string{"which is not a pull request address"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, gh := mergeYard(t, c.o)

			y.exec("merge-pr", "pr-merges", "r-p")

			heldUnmerged(t, y, gh, c.wants...)
			if _, err := os.Stat(filepath.Join(gh, "calls")); err == nil {
				t.Errorf("GitHub was asked for a bead the yard may not merge:\n%v", ghCalls(t, gh))
			}
		})
	}
}

// A pull request merged already (by a maintainer, or by a run that could not
// report it) is reported merged, and one closed unmerged dropped, without a
// merge asked for; the bead ends where the report takes it.
func TestMergePRReportsAPullRequestThatIsNotOpen(t *testing.T) {
	for _, c := range []struct{ state, stage string }{{"MERGED", "merged"}, {"CLOSED", "closed"}} {
		t.Run(c.state, func(t *testing.T) {
			y, gh := mergeYard(t, mergeOpts{})
			writeGh(t, gh, "state", c.state+"\n")

			y.exec("merge-pr", "pr-merges", "r-p")

			if e := y.beadOf("r-p"); e.Stage != c.stage || e.Status != "closed" || e.Hold {
				t.Fatalf("r-p %s/%s hold=%v, want %s and closed", e.Stage, e.Status, e.Hold, c.stage)
			}
			if n := countCalls(t, gh, "pr merge"); n != 0 {
				t.Fatalf("a merge asked for a pull request that is %s:\n%v", c.state, ghCalls(t, gh))
			}
		})
	}
}

// What GitHub refuses holds the bead with its words, and a refusal of the
// App itself names the permissions it lacks. A head that moves between the
// look and the merge is refused by --match-head-commit and held as moved,
// not merged and not sent anywhere.
func TestMergePRHoldsWhatGitHubRefuses(t *testing.T) {
	merge := "pr merge " + prURL + " --rebase --delete-branch --match-head-commit " + headA
	for _, c := range []struct {
		name  string
		files map[string]string
		wants []string
	}{
		{"the App without Contents", map[string]string{"refuse": "HTTP 403: Resource not accessible by integration (https://api.github.com/repos/o/r/pulls/7/merge)"},
			[]string{"GitHub refused the yard's App on o/r", "Contents and Workflows read and write", "Resource not accessible by integration"}},
		{"a pull request it will not merge", map[string]string{"refuse": "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge."},
			[]string{"GitHub did not merge " + prURL, "base branch policy prohibits the merge", "unhold r-p"}},
		{"a head that moved as the merge started", map[string]string{"race": headB},
			[]string{"moved while it was being merged (looked at 4f2c9e1, head now 9b8a7c6)"}},
		{"a head that is no commit sha", map[string]string{"head": "../../etc"},
			[]string{"gave a head for " + prURL + " that is no commit sha"}},
		{"a pull request it cannot read", map[string]string{"view-fail": "HTTP 404: Not Found"},
			[]string{"reading pull request " + prURL + " failed: HTTP 404: Not Found"}},
		{"check runs it cannot read", map[string]string{"api-fail": "HTTP 403: Resource not accessible by integration"},
			[]string{"reading the check runs of 4f2c9e1 in o/r failed", "The App needs Checks read"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, gh := mergeYard(t, mergeOpts{})
			for name, text := range c.files {
				writeGh(t, gh, name, text+"\n")
			}

			y.exec("merge-pr", "pr-merges", "r-p")

			// Not heldUnmerged: a refused merge is asked for once.
			if e := y.beadOf("r-p"); !e.Hold || e.Stage != "approved" || e.Status == "closed" {
				t.Fatalf("r-p %s/%s hold=%v, want held in approved:\n%s", e.Stage, e.Status, e.Hold, y.notesOf("r-p"))
			}
			notes := y.notesOf("r-p")
			for _, want := range c.wants {
				if !strings.Contains(notes, want) {
					t.Errorf("no note with %q:\n%s", want, notes)
				}
			}
			if _, raced := c.files["race"]; raced {
				if n := countCalls(t, gh, merge); n != 1 {
					t.Errorf("%q called %d times, want once:\n%v", merge, n, ghCalls(t, gh))
				}
			}
			if got := y.refusedOf("r-p"); len(got) != 0 {
				t.Errorf("merge-pr tried moves the flow refuses: %v", got)
			}
		})
	}
}

// Without the App's id and key file in the group's environment, or with a
// mint that fails, nothing is asked of GitHub: the bead is held with the
// reason, which is the mint's own words and never a token.
func TestMergePRHoldsWithoutAToken(t *testing.T) {
	t.Run("no env_file", func(t *testing.T) {
		y, gh := mergeYard(t, mergeOpts{noEnvFile: true})
		y.exec("merge-pr", "pr-merges", "r-p")
		heldUnmerged(t, y, gh, "has no GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE", "env_file")
		if _, err := os.Stat(filepath.Join(gh, "calls")); err == nil {
			t.Errorf("GitHub was asked with no token:\n%v", ghCalls(t, gh))
		}
	})
	t.Run("the mint fails", func(t *testing.T) {
		y, gh := mergeYard(t, mergeOpts{})
		writeGh(t, gh, "mint-fail", "github-mint: GitHub has no installation of the App on o/r: install the App there\n")
		y.exec("merge-pr", "pr-merges", "r-p")
		heldUnmerged(t, y, gh, "minting a token for o/r failed: github-mint: GitHub has no installation of the App on o/r")
		if _, err := os.Stat(filepath.Join(gh, "calls")); err == nil {
			t.Errorf("GitHub was asked with no token:\n%v", ghCalls(t, gh))
		}
	})
}

// merge-pr never reports failed: the outcomes at approved are merged and
// dropped, and a pull request it cannot merge is held for a maintainer, not
// sent back to be built again. It touches no checkout of the depot either:
// GitHub is the whole of it.
func TestMergePRNeverReportsFailedOrTouchesTheDepot(t *testing.T) {
	src := readFile(t, "merge-pr")
	for _, bad := range []string{"--outcome failed", "report failed", "report conflict"} {
		if strings.Contains(src, bad) {
			t.Errorf("merge-pr has %q", bad)
		}
	}
	y, _ := mergeYard(t, mergeOpts{})
	before := y.git(y.repo, "status", "--porcelain") + y.git(y.repo, "rev-parse", "HEAD")

	y.exec("merge-pr", "pr-merges", "r-p")

	if e := y.beadOf("r-p"); e.Stage != "merged" {
		t.Fatalf("r-p in %s:\n%s", e.Stage, y.notesOf("r-p"))
	}
	if after := y.git(y.repo, "status", "--porcelain") + y.git(y.repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("the depot's checkout changed:\n%s\n%s", before, after)
	}
}

// githubDecl is the part of the github source's declaration that merge-pr
// reads the yard for: a mint over the App's two inputs, which the source's
// mint_command is for. The rest of the pack's declaration is the daemon's.
const githubDecl = `name: github
about: GitHub, as far as merge-pr needs it.
base: https://api.github.com
credential:
  env: GITHUB_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE]
calls:
  user:
    kind: read
    about: Who the credential acts as.
    request: { method: GET, path: /user }
    answer: { select: [login, type] }
`

// With no mint named in the group's cmd, the mint is the github source's own
// (yardr source set github mint_command=…), the one that mints for every
// other call of the yard: one place says where github-mint is installed. A
// yard without it holds the bead and says what to do.
func TestMergePRMintsWithTheGitHubSourcesMintCommand(t *testing.T) {
	t.Run("the source's mint_command", func(t *testing.T) {
		y, gh := mergeYard(t, mergeOpts{})
		decl := filepath.Join(t.TempDir(), "github.yaml")
		if err := os.WriteFile(decl, []byte(githubDecl), 0o644); err != nil {
			t.Fatal(err)
		}
		y.yardr("source", "add", decl)
		y.yardr("source", "set", "github", "mint_command="+filepath.Join(gh, "bin", "github-mint"))
		y.opts = ""

		y.exec("merge-pr", "pr-merges", "r-p")

		if e := y.beadOf("r-p"); e.Stage != "merged" || e.Status != "closed" {
			t.Fatalf("r-p %s/%s, want merged and closed:\n%s", e.Stage, e.Status, y.notesOf("r-p"))
		}
		if !strings.Contains(readFile(t, filepath.Join(gh, "mint-stdin")), `"scope":"o/r"`) {
			t.Errorf("the source's mint was not asked for o/r: %s", readFile(t, filepath.Join(gh, "mint-stdin")))
		}
	})
	t.Run("no mint anywhere", func(t *testing.T) {
		y, gh := mergeYard(t, mergeOpts{})
		y.opts = ""

		y.exec("merge-pr", "pr-merges", "r-p")

		heldUnmerged(t, y, gh, "the github source has no mint_command", "YARDR_GITHUB_MINT", "yardr source set github mint_command=")
	})
}
