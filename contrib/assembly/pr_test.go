package assembly

// A train landing through a pull request: integrate-train with YARDR_PR=1
// opens the PR (and updates it when the train is integrated again), pr-train
// pushes the reviewed branch and updates the PR, and pr-merge merges it and
// fast-forwards the depot's base. GitHub is a fake gh on PATH that records
// its argv and keeps one pull request's state in a directory.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
)

const prURL = "https://github.com/o/r/pull/7"

// fakeGh is gh as far as the scripts use it. Its state, in $FAKE_GH: calls
// (one argv per line), url (the open PR, once created), body (its body),
// state (OPEN once created), and files a test writes to make it fail or
// answer otherwise: fail-create, exists (gh pr create's error when a PR is
// open for the branch), refuse (gh pr merge's error), mergeable,
// head-owner (the owner of the PR's head repository, o unless a fork's),
// and race (a commit pushed to the PR's branch as gh pr merge starts, after
// the script last read the head). The PR's head is origin's yard/r-e.
// It has no pr list: the scripts never look a PR up by its branch.
// Merging pushes the PR's head onto origin's main and deletes its branch
// there, as GitHub would (minus the rebase, and minus the squash, whose
// --subject and --body it keeps in the files subject and merge-body); with
// --match-head-commit it refuses, in GitHub's words, when the head is
// another commit.
const fakeGh = `#!/bin/sh
d=$FAKE_GH
echo "$*" >>"$d/calls"
case "$1 $2" in
"pr create")
  if [ -e "$d/fail-create" ]; then echo "HTTP 502: Bad Gateway" >&2; exit 1; fi
  if [ -e "$d/exists" ]; then
    printf 'a pull request for branch "yard/r-e" into branch "main" already exists:\n%s\n' ` + prURL + ` >&2; exit 1
  fi
  cat >"$d/body"
  echo OPEN >"$d/state"
  echo "Creating pull request for yard/r-e into main" >&2
  echo ` + prURL + ` | tee "$d/url" ;;
"pr edit") cat >"$d/body" ;;
"pr view")
  head=$(git ls-remote origin refs/heads/yard/r-e | cut -f1)
  case "$*" in
  *mergeable*) cat "$d/mergeable" 2>/dev/null || echo MERGEABLE ;;
  *headRepository*)
    printf '{"number":7,"state":"%s","headRefName":"yard/r-e","headRefOid":"%s","headRepositoryOwner":{"login":"%s"},"headRepository":{"name":"r"}}\n' \
      "$(cat "$d/state" 2>/dev/null || echo OPEN)" "$head" "$(cat "$d/head-owner" 2>/dev/null || echo o)" ;;
  *headRefOid*) echo "$head" ;;
  *) cat "$d/state" 2>/dev/null || echo OPEN ;;
  esac ;;
"pr merge")
  if [ -e "$d/refuse" ]; then cat "$d/refuse" >&2; exit 1; fi
  if [ -e "$d/race" ]; then git push -q origin "$(cat "$d/race"):refs/heads/yard/r-e" || exit 1; fi
  head=$(git ls-remote origin refs/heads/yard/r-e | cut -f1)
  [ -n "$head" ] || exit 1
  match=
  while [ $# -gt 0 ]; do
    [ "$1" = --match-head-commit ] && match=$2
    [ "$1" = --subject ] && printf '%s' "$2" >"$d/subject"
    [ "$1" = --body ] && printf '%s' "$2" >"$d/merge-body"
    shift
  done
  if [ -n "$match" ] && [ "$match" != "$head" ]; then
    echo "GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)" >&2; exit 1
  fi
  git push -q origin "$head:refs/heads/main" && git push -q origin :refs/heads/yard/r-e || exit 1
  echo MERGED >"$d/state" ;;
*) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

// fakeGhs is the fake gh's state directory of each test that has one.
var fakeGhs sync.Map // *testing.T -> string

// withFakeGh puts the fake gh first on PATH for the scripts the exec runner
// starts for this test (see exec), and returns its state directory.
func withFakeGh(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	scripttest.Write(t, filepath.Join(state, "bin", "gh"), fakeGh)
	fakeGhs.Store(t, state)
	t.Cleanup(func() { fakeGhs.Delete(t) })
	return state
}

// ghCalls is the fake gh's argv, one call per line.
func ghCalls(t *testing.T, state string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "calls"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// countCalls counts the calls starting with prefix.
func countCalls(t *testing.T, state, prefix string) int {
	n := 0
	for _, c := range ghCalls(t, state) {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTrainThroughPullRequest(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")

	// Integrated: the PR is opened, once, with the train's children.
	y.exec("integrate-train", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" || e.Metadata["pr_url"] != prURL ||
		fmt.Sprint(e.Metadata["pr_number"]) != "7" || e.Metadata["pr_head_repo"] != "o/r" {
		t.Fatalf("train in %s with metadata %v, want review, bound to %s: number 7, head repository o/r", e.Stage, e.Metadata, prURL)
	}
	if n := countCalls(t, gh, "pr create --base main --head yard/r-e --title the train (r-e) --body-file -"); n != 1 {
		t.Fatalf("gh pr create called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
	body := readFile(t, filepath.Join(gh, "body"))
	for _, want := range []string{"## Children", "- r-1 child", "- r-2 child", "Train: r-e"} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR body lacks %q:\n%s", want, body)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "Train: r-e") {
		t.Fatalf("PR body does not end in its trailer:\n%s", body)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "Opened pull request "+prURL) {
		t.Fatalf("no note with the PR:\n%s", notes)
	}

	// Sent back and integrated again: the open PR is edited, not opened again.
	y.yardr("bead", "done", "r-e", "--outcome", "changes")
	y.exec("integrate-train", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" {
		t.Fatalf("train in %s after integrating again, want review", e.Stage)
	}
	if n := countCalls(t, gh, "pr create"); n != 1 {
		t.Fatalf("gh pr create called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
	if n := countCalls(t, gh, "pr edit "+prURL+" --body-file -"); n != 1 {
		t.Fatalf("gh pr edit called %d times, want once:\n%v", n, ghCalls(t, gh))
	}

	// The reviewer commits a fix on the branch and approves with a note.
	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	tip := y.commit(wt, "fix.txt", "the reviewer's fix\n")
	y.yardr("bead", "done", "r-e", "--note", "Approved: the children fit together; fixed a typo in fix.txt.")
	y.yardr("bead", "done", "r-e", "--note", "Security: touches nothing the network reaches.") // review-security -> approved
	// A note a script left since is not the review.
	y.yardr("bead", "note", "r-e", "assembly: the tests failed once; running them once more")

	// pr-train pushes it and puts the review on the PR.
	y.exec("pr-train", "yard-pr", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "in-pr" || e.Metadata["pr_reviewed_sha"] != tip {
		t.Fatalf("train in %s after pr-train with metadata %v, want in-pr with pr_reviewed_sha %s", e.Stage, e.Metadata, tip)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, tip) {
		t.Fatalf("origin yard/r-e is %q, want the reviewer's %s", pushed, tip)
	}
	if n := countCalls(t, gh, "pr edit "+prURL); n != 2 {
		t.Fatalf("gh pr edit called %d times, want twice:\n%v", n, ghCalls(t, gh))
	}
	body = readFile(t, filepath.Join(gh, "body"))
	review := strings.Index(body, "## Review\n\nApproved: the children fit together")
	security := strings.Index(body, "## Security review\n\nSecurity: touches nothing the network reaches")
	trailer := strings.Index(body, "Train: r-e")
	if review < 0 || security < 0 || !strings.Contains(body, "- r-2 child") || !(review < security && security < trailer) {
		t.Fatalf("PR body lacks both reviews before its trailer:\n%s", body)
	}

	// A human says "merge it"; pr-merge merges and brings main up.
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "land" {
		t.Fatalf("train in %s after in-pr, want land", e.Stage)
	}
	y.exec("pr-merge", "yard-pr-merge", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("train %s/%s after pr-merge, want merged and closed", e.Stage, e.Status)
	}
	if n := countCalls(t, gh, "pr merge "+prURL+" --rebase --delete-branch --match-head-commit "+tip); n != 1 {
		t.Fatalf("gh pr merge called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != tip {
		t.Fatalf("local main at %s, want the merged %s", got, tip)
	}
	if y.git(y.repo, "status", "--porcelain") != "" {
		t.Fatal("the depot's checkout is not clean after the fast-forward")
	}
	if _, err := os.Stat(filepath.Join(y.repo, "fix.txt")); err != nil {
		t.Fatalf("checkout of main lacks the reviewer's fix: %v", err)
	}
	// From open to merged the PR was found by what the train stores, never
	// by its branch's name, which a fork's pull request can carry too.
	if n := countCalls(t, gh, "pr list"); n != 0 {
		t.Fatalf("gh pr list called %d times, want never:\n%v", n, ghCalls(t, gh))
	}
}

// No script looks the pull request up by the train's branch.
func TestScriptsNeverListPullRequestsByBranch(t *testing.T) {
	for _, name := range []string{"integrate-train", "pr-train", "pr-merge", "merge-pr", "pr-ci"} {
		if s := readFile(t, name); strings.Contains(s, "pr list") {
			t.Errorf("%s runs gh pr list: the PR is the one bound to the train, by number", name)
		}
	}
}

// A pull request already open for the branch that the train is not bound to
// (opened by hand, or by a fork) is not adopted: the train is held for a
// human. Once they bind it by its URL, integrating stores its number and
// head repository.
func TestIntegrateTrainDoesNotAdoptAnUnboundPR(t *testing.T) {
	gh := withFakeGh(t)
	if err := os.WriteFile(filepath.Join(gh, "exists"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "open" || e.Metadata["pr_url"] != nil || e.Metadata["pr_number"] != nil {
		t.Fatalf("train %s hold=%v metadata %v, want held in open, bound to nothing", e.Stage, e.Hold, e.Metadata)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "is open that r-e is not bound to") {
		t.Fatalf("no note on the unbound PR:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr edit"); n != 0 {
		t.Fatalf("edited a PR the train is not bound to:\n%v", ghCalls(t, gh))
	}

	y.yardr("bead", "update", "r-e", "--meta", "pr_url="+prURL)
	y.yardr("bead", "unhold", "r-e")
	y.exec("integrate-train", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" || fmt.Sprint(e.Metadata["pr_number"]) != "7" || e.Metadata["pr_head_repo"] != "o/r" {
		t.Fatalf("train in %s with metadata %v, want review, bound to number 7 from o/r", e.Stage, e.Metadata)
	}
	if n := countCalls(t, gh, "pr edit "+prURL); n != 1 {
		t.Fatalf("gh pr edit called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
}

// An open bound PR whose head repository changed is not silently rebound or
// edited by integrate-train; the human must correct the binding first.
func TestIntegrateTrainHoldsWhenBoundPRHeadRepositoryChanges(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	if err := os.WriteFile(filepath.Join(gh, "head-owner"), []byte("mallory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	y.yardr("bead", "done", "r-e", "--outcome", "changes")

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "open" || e.Metadata["pr_head_repo"] != "o/r" {
		t.Fatalf("train %s hold=%v metadata %v, want held in open with original binding", e.Stage, e.Hold, e.Metadata)
	}
	if n := countCalls(t, gh, "pr edit "+prURL); n != 0 {
		t.Fatalf("edited a PR after its head repository changed:\n%v", ghCalls(t, gh))
	}
}

// A train whose bound pull request was closed gets a new one when it is
// integrated again, not an edit of the closed one.
func TestIntegrateTrainOpensANewPRWhenTheBoundOneIsClosed(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	if err := os.WriteFile(filepath.Join(gh, "state"), []byte("CLOSED\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.yardr("bead", "done", "r-e", "--outcome", "changes")
	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "review" {
		t.Fatalf("train in %s, want review", e.Stage)
	}
	if c, e := countCalls(t, gh, "pr create"), countCalls(t, gh, "pr edit"); c != 2 || e != 0 {
		t.Fatalf("gh pr create called %d times and pr edit %d, want 2 and 0:\n%v", c, e, ghCalls(t, gh))
	}
}

// gh failing to open the PR is reported failed, back to open, not held: the
// next integration tries again.
func TestIntegrateTrainPRFailureGoesBackToOpen(t *testing.T) {
	gh := withFakeGh(t)
	if err := os.WriteFile(filepath.Join(gh, "fail-create"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold || e.Metadata["pr_url"] != nil {
		t.Fatalf("train %s hold=%v metadata %v, want back in open, not held, no pr_url", e.Stage, e.Hold, e.Metadata)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "HTTP 502: Bad Gateway") {
		t.Fatalf("no note with gh's error:\n%s", notes)
	}
}

// landYard is a train yard whose train r-e is integrated and pushed, with
// its PR open (the train bound to it), in stage land. The yard is the
// template's, copied (fixture_test.go).
func landYard(t *testing.T) (*yard, string) {
	t.Helper()
	return borrowLand(t)
}

// A merge GitHub refuses goes back to open with gh's message, after one
// attempt; neither main moves.
func TestPRMergeRefusedGoesBackToOpen(t *testing.T) {
	y, gh := landYard(t)
	msg := "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge."
	if err := os.WriteFile(filepath.Join(gh, "refuse"), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := y.git(y.repo, "rev-parse", "main")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "base branch policy prohibits") {
		t.Fatalf("no note with gh's message:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 1 {
		t.Fatalf("gh pr merge called %d times, want once", n)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("local main moved")
	}
	if got := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, main) {
		t.Fatal("origin main moved")
	}
}

// unmerged asserts that pr-merge left the train held in land, open, with
// the note want, and that neither main has the pull request's commits.
func unmerged(t *testing.T, y *yard, main, want string) {
	t.Helper()
	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "land" || e.Status == "closed" {
		t.Fatalf("train %s/%s hold=%v, want held in land, not closed", e.Stage, e.Status, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, want) {
		t.Fatalf("no note with %q:\n%s", want, notes)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("local main moved")
	}
	if got := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, main) {
		t.Fatal("origin main moved")
	}
}

// unreviewed makes a commit on top of the train's branch that no one
// reviewed, without moving the branch, and returns its sha.
func unreviewed(t *testing.T, y *yard) string {
	t.Helper()
	y.git(y.repo, "branch", "sneak", "yard/r-e")
	wt := filepath.Join(t.TempDir(), "sneak")
	y.git(y.repo, "worktree", "add", "-q", wt, "sneak")
	return y.commit(wt, "sneak.txt", "pushed after the review\n")
}

// A commit pushed to the pull request after the review is not merged: the
// head is no longer the reviewed sha, so the train is held before gh pr
// merge is run at all.
func TestPRMergeHoldsWhenTheHeadMovedAfterReview(t *testing.T) {
	y, gh := landYard(t)
	reviewed := y.git(y.repo, "rev-parse", "yard/r-e")
	if got := y.beadOf("r-e").Metadata["pr_reviewed_sha"]; got != reviewed {
		t.Fatalf("pr_reviewed_sha is %v, want the pushed %s", got, reviewed)
	}
	main := y.git(y.repo, "rev-parse", "main")
	sneak := unreviewed(t, y)
	y.git(y.repo, "push", "-q", "origin", sneak+":refs/heads/yard/r-e")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	unmerged(t, y, main, "is "+sneak+", not the reviewed "+reviewed)
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatalf("gh pr merge called %d times on a head that moved, want never", n)
	}
}

// The head moves between pr-merge's look at the pull request and the
// merge: --match-head-commit makes GitHub refuse, and the train is held,
// not closed and not sent round as an ordinary refusal.
func TestPRMergeHoldsWhenGitHubRefusesAMovedHead(t *testing.T) {
	y, gh := landYard(t)
	reviewed := y.git(y.repo, "rev-parse", "yard/r-e")
	main := y.git(y.repo, "rev-parse", "main")
	sneak := unreviewed(t, y)
	if err := os.WriteFile(filepath.Join(gh, "race"), []byte(sneak+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	unmerged(t, y, main, "is "+sneak+", not the reviewed "+reviewed)
	if n := countCalls(t, gh, "pr merge "+prURL+" --rebase --delete-branch --match-head-commit "+reviewed); n != 1 {
		t.Fatalf("gh pr merge with the reviewed sha called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
}

// A train in land with no reviewed sha (moved there by hand, or in in-pr
// since before pr-train stored one) is not merged on its head as it is.
func TestPRMergeNeedsTheReviewedSha(t *testing.T) {
	y, gh := landYard(t)
	y.yardr("bead", "update", "r-e", "--meta", "pr_reviewed_sha=")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "no reviewed commit (pr_reviewed_sha") {
		t.Fatalf("no note on the missing reviewed sha:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged a pull request with no reviewed sha")
	}
}

// The train's pr_url names a pull request whose head is in a fork, not the
// repository the train is bound to: pr-merge holds the train and merges
// nothing, and pr-train pushes nothing for it either.
func TestPRScriptsHoldOnAPRFromAnotherHeadRepository(t *testing.T) {
	y, gh := landYard(t)
	if err := os.WriteFile(filepath.Join(gh, "head-owner"), []byte("mallory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	main := y.git(y.repo, "rev-parse", "main")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "land" {
		t.Fatalf("train %s hold=%v, want held in land", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "is #7 from mallory/r:yard/r-e, but r-e is bound to #7 from o/r:yard/r-e") {
		t.Fatalf("no note on the other head repository:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged a pull request from another head repository")
	}
	if got := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, main) {
		t.Fatal("origin main moved")
	}
}

func TestPRTrainHoldsOnAPRFromAnotherHeadRepository(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	integrated := y.git(y.repo, "rev-parse", "yard/r-e")
	y.commit(filepath.Join(y.home, "worktrees", "r", "r-e"), "fix.txt", "the reviewer's fix\n")
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e") // review-security -> approved
	if err := os.WriteFile(filepath.Join(gh, "head-owner"), []byte("mallory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edits := countCalls(t, gh, "pr edit")

	y.exec("pr-train", "yard-pr", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "approved" {
		t.Fatalf("train %s hold=%v, want held in approved", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, integrated) {
		t.Fatalf("origin yard/r-e is %q, want it left at the integrated %s", pushed, integrated)
	}
	if n := countCalls(t, gh, "pr edit"); n != edits {
		t.Fatal("edited a pull request from another head repository")
	}
}

// An approved train cannot be sent back to in-pr for a closed PR: the
// integrator must open a new one before review resumes.
func TestPRTrainClosedPRGoesBackToOpenWithoutPushing(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	integrated := y.git(y.repo, "rev-parse", "yard/r-e")
	y.commit(filepath.Join(y.home, "worktrees", "r", "r-e"), "fix.txt", "the reviewer's fix\n")
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e") // review-security -> approved
	if err := os.WriteFile(filepath.Join(gh, "state"), []byte("CLOSED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edits := countCalls(t, gh, "pr edit")

	y.exec("pr-train", "yard-pr", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open for a new PR", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, integrated) {
		t.Fatalf("origin yard/r-e is %q, want it left at the integrated %s", pushed, integrated)
	}
	if n := countCalls(t, gh, "pr edit"); n != edits {
		t.Fatal("edited a closed pull request")
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged a closed pull request")
	}
}

// A train with a pr_url but no number or head repository (bound before
// they were stored) is not merged on the URL alone: back to open, where
// integrating binds it.
func TestPRMergeNeedsTheBinding(t *testing.T) {
	y, gh := landYard(t)
	y.yardr("bead", "update", "r-e", "--meta", "pr_number=", "--meta", "pr_head_repo=")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "no pull request bound to r-e") {
		t.Fatalf("no note on the missing binding:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged a pull request the train is not bound to")
	}
}

// A dirty depot checkout holds the train in land before anything is merged.
func TestPRMergeDirtyCheckoutHolds(t *testing.T) {
	y, gh := landYard(t)
	if err := os.WriteFile(filepath.Join(y.repo, "a.txt"), []byte("someone's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "land" {
		t.Fatalf("train %s hold=%v, want held in land", e.Stage, e.Hold)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged under a dirty checkout")
	}

	// The checkout cleaned, the same train is held again by a YARDR_LAND that
	// is neither mode, before anything is merged, and then merged by squash:
	// one commit, with the subject and the body merge-local would write for
	// the train over the pull request's commits.
	y.git(y.repo, "checkout", "--", "a.txt")
	y.setHold("r-e", false)
	y.opts = "YARDR_PR=1 YARDR_LAND=squahs"
	y.exec("pr-merge", "yard-pr-merge", "r-e")
	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "land" {
		t.Fatalf("train %s hold=%v after a YARDR_LAND that is none, want held in land", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "YARDR_LAND is squahs") {
		t.Fatalf("no note naming the value:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged under a mode that is none")
	}
	reviewed := y.git(y.repo, "rev-parse", "yard/r-e")
	y.setHold("r-e", false)
	y.opts = "YARDR_PR=1 YARDR_LAND=squash"

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("train %s/%s after pr-merge by squash, want merged and closed:\n%s", e.Stage, e.Status, y.notesOf("r-e"))
	}
	if n := countCalls(t, gh, "pr merge "+prURL+" --squash --subject "); n != 1 {
		t.Fatalf("gh pr merge --squash called %d times, want once:\n%v", n, ghCalls(t, gh))
	}
	if calls := readFile(t, filepath.Join(gh, "calls")); strings.Contains(calls, "--rebase") ||
		!strings.Contains(calls, " --delete-branch --match-head-commit "+reviewed) {
		t.Fatalf("gh pr merge was not --squash --delete-branch --match-head-commit %s:\n%s", reviewed, calls)
	}
	if got, want := readFile(t, filepath.Join(gh, "subject")), "chore: the train (r-e)"; got != want {
		t.Fatalf("subject %q, want %q", got, want)
	}
	if got, want := readFile(t, filepath.Join(gh, "merge-body")), "child1.txt\n\nchild2.txt\n\nYardr-Bead: r-e"; got != want {
		t.Fatalf("body %q, want %q", got, want)
	}
	if !strings.Contains(y.notesOf("r-e"), "with a squash;") {
		t.Fatalf("the landing note does not say it was a squash:\n%s", y.notesOf("r-e"))
	}
}

// pr-train on a train with no PR, and on one GitHub finds conflicting, sends
// it back to open for the integrator.
func TestPRTrainNoPRGoesBackToOpen(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e") // no YARDR_PR: no PR
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e") // review-security -> approved

	y.opts = "YARDR_PR=1"
	y.exec("pr-train", "yard-pr", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "no pull request bound to r-e") {
		t.Fatalf("no note on the missing PR:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr edit"); n != 0 {
		t.Fatal("edited a PR that does not exist")
	}
}

func TestPRTrainConflictingGoesBackToOpen(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.opts = "YARDR_PR=1"
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e") // review-security -> approved
	if err := os.WriteFile(filepath.Join(gh, "mergeable"), []byte("CONFLICTING\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.exec("pr-train", "yard-pr", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "conflicts with main") {
		t.Fatalf("no note on the conflict:\n%s", notes)
	}
}

// Without YARDR_PR the pull-request path is off: pr-train, handed an approved
// train by a stray route, holds it where it is with a note naming the
// switch, pushing nothing and asking GitHub nothing, and the train lands
// through merge-local by the bare edge, never passing in-pr.
func TestPRTrainWithoutSwitchHoldsAndTrainLandsLocally(t *testing.T) {
	gh := withFakeGh(t)
	y := newTrainYard(t, "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	integrated := y.git(y.repo, "rev-parse", "yard/r-e")
	// The reviewer's commit is what pr-train would push.
	tip := y.commit(filepath.Join(y.home, "worktrees", "r", "r-e"), "fix.txt", "the reviewer's fix\n")
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e") // review-security -> approved

	y.exec("pr-train", "yard-pr", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "approved" {
		t.Fatalf("train %s hold=%v after pr-train without YARDR_PR, want held in approved", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "YARDR_PR is not set") {
		t.Fatalf("no note naming the switch:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr "); n != 0 {
		t.Fatalf("gh called without YARDR_PR:\n%v", ghCalls(t, gh))
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, integrated) {
		t.Fatalf("origin yard/r-e is %q, want it left at the integrated %s", pushed, integrated)
	}

	// Routed to the assembly instead, it lands as any train does locally.
	y.yardr("bead", "unhold", "r-e")
	y.exec("merge-local", "assembly", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("train %s/%s after merge-local, want merged and closed", e.Stage, e.Status)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != tip {
		t.Fatalf("main at %s, want the train branch %s", got, tip)
	}
	if n := countCalls(t, gh, "pr "); n != 0 {
		t.Fatalf("gh called on the local path:\n%v", ghCalls(t, gh))
	}
	// The map of the flow in prime names in-pr as a stage; the path the
	// train took is its PATH line.
	hist := y.yardr("prime", "--bead", "r-e")
	i := strings.Index(hist, "\nPATH  ")
	if i < 0 {
		t.Fatalf("prime shows no path for the train:\n%s", hist)
	}
	if path, _, _ := strings.Cut(hist[i+1:], "\n"); strings.Contains(path, "in-pr") || !strings.HasSuffix(path, "-> merged") {
		t.Fatalf("the train passed in-pr, or did not end in merged: %s", path)
	}
}

// pr-merge without YARDR_PR holds a train that reached land, before
// anything is merged: neither GitHub nor main is touched.
func TestPRMergeWithoutSwitchHolds(t *testing.T) {
	y, gh := landYard(t)
	y.opts = ""
	main := y.git(y.repo, "rev-parse", "main")

	y.exec("pr-merge", "yard-pr-merge", "r-e")

	if e := y.beadOf("r-e"); !e.Hold || e.Stage != "land" {
		t.Fatalf("train %s hold=%v after pr-merge without YARDR_PR, want held in land", e.Stage, e.Hold)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "YARDR_PR is not set") {
		t.Fatalf("no note naming the switch:\n%s", notes)
	}
	if n := countCalls(t, gh, "pr merge"); n != 0 {
		t.Fatal("merged without YARDR_PR")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("local main moved")
	}
	if got := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(got, main) {
		t.Fatal("origin main moved")
	}
}
