package assembly

// post-review the way an exec group runs it for a bead of the public line in
// one of its two posting stages: the real yardr.public flow of this repository
// in the yard, the group's env_file giving the App's id and key file, merge-pr's
// stand-in mint, and a stand-in gh that keeps one pull request's state in a
// directory, refuses a call that does not carry the mint's token, and takes a
// review as GitHub does: it keeps what it was posted, and refuses to let the
// account that opened a pull request approve it or ask changes of it. What the
// reviewer did is the metadata the bead carries into the stage: the head it read
// and the review it stored.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// fakePostGh is gh as far as post-review uses it. Its state, in $FAKE_GH: calls
// (argv, one per line, never the token), head (the pull request's head, headA
// unless written), head-ref (the branch it is from), head-owner and head-name
// (the repository the head is in, o/r unless written), state (OPEN unless
// written), reviews (every request body posted, one line each), self (the
// account that posts is the one that opened the pull request), refuse (the
// error GitHub answers any review with) and view-fail (the error of a look at
// the pull request).
const fakePostGh = `#!/bin/sh
d=$FAKE_GH
echo "$*" >>"$d/calls"
if [ "$GH_TOKEN" != ` + mintToken + ` ]; then echo "HTTP 401: Bad credentials" >&2; exit 1; fi
case "$1 $2" in
"pr view")
  if [ -e "$d/view-fail" ]; then cat "$d/view-fail" >&2; exit 1; fi
  printf '{"state":"%s","headRefOid":"%s","headRefName":"%s","headRepositoryOwner":{"login":"%s"},"headRepository":{"name":"%s"}}\n' \
    "$(cat "$d/state" 2>/dev/null || echo OPEN)" "$(cat "$d/head" 2>/dev/null || echo ` + headA + `)" "$(cat "$d/head-ref")" \
    "$(cat "$d/head-owner" 2>/dev/null || echo o)" "$(cat "$d/head-name" 2>/dev/null || echo r)" ;;
"api --method")
  body=$(cat)
  printf '%s\n' "$body" >>"$d/reviews"
  if [ -e "$d/refuse" ]; then
    echo '{"message":"Unprocessable Entity"}'
    cat "$d/refuse" >&2
    exit 1
  fi
  case "$body" in
  *'"event":"COMMENT"'*) ;;
  *) if [ -e "$d/self" ]; then
       echo '{"message":"Unprocessable Entity","errors":["Review Can not approve your own pull request"],"status":"422"}'
       echo "gh: Unprocessable Entity (HTTP 422)" >&2
       exit 1
     fi ;;
  esac
  echo '{"id":1,"html_url":"` + prAddr + `#pullrequestreview-1"}' ;;
*) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
`

// postYard is a yard whose depot r is on the public line, with the stand-ins
// for gh and github-mint and the group's env_file with the App's id and the
// path of its key file. It holds no bead of the line: a test adds them. gh is
// the stand-in's state directory.
func postYard(t *testing.T) (y *yard, gh string) {
	t.Helper()
	y = newYard(t)
	gh = t.TempDir()
	if err := os.Mkdir(filepath.Join(gh, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	scripttest.Write(t, filepath.Join(gh, "bin", "gh"), fakePostGh)
	mint := filepath.Join(gh, "bin", "github-mint")
	scripttest.Write(t, mint, fakeGithubMint)
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })
	y.envFile = filepath.Join(t.TempDir(), "yardr-post.env")
	if err := os.WriteFile(y.envFile, []byte("GITHUB_APP_ID=123456\nGITHUB_APP_PRIVATE_KEY_FILE="+filepath.Join(gh, "app.pem")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.opts = "YARDR_GITHUB_MINT=" + shellQuote(mint+" --from-group")
	y.putFlowFile("yardr.public", storetest.RepoFlow(t, "yardr.public"))
	y.pointFlow("r", "", "yardr.public")
	return y, gh
}

// postBead adds bead id of the public line in stage, with the metadata the
// posting stages read, tracking the pull request the stand-in has (none when
// tracked is false), and with a body and a note of the yard's own that must
// never reach GitHub.
func (y *yard) postBead(id, stage string, meta map[string]any, tracked bool) {
	y.t.Helper()
	nb := store.NewBead{Title: "Add the open step", Body: yardOnlyBody, ID: id, Depot: "r", Type: "task",
		Stage: stage, Priority: store.DefaultPriority, Metadata: meta}
	if tracked {
		nb.Links = []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: prAddr}}
	}
	ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if _, err := st.CreateBead(ctx, nb, "test"); err != nil {
		y.t.Fatal(err)
	}
	if err := st.AddNote(ctx, id, yardOnlyNote, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// postStandIn forgets everything of the stand-ins' last run, sets the pull
// request to be from the branch of bead id, and sets files (a file each, the
// text in it) for the next.
func postStandIn(t *testing.T, gh, id string, files map[string]string) {
	t.Helper()
	for _, name := range []string{"calls", "state", "head", "head-ref", "head-owner", "head-name", "reviews", "self", "refuse", "view-fail", "mint-fail", "mint-stdin", "mint-args"} {
		if err := os.Remove(filepath.Join(gh, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	set := map[string]string{"head-ref": "yard/" + id}
	for name, text := range files {
		set[name] = text
	}
	for name, text := range set {
		if err := os.WriteFile(filepath.Join(gh, name), []byte(text+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// posts is the requests the stand-in was given, in order.
func posts(t *testing.T, gh string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(gh, "reviews"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// postMeta is the metadata of a bead that comes to a posting stage from a
// reviewer that did what its stage file says: the pull request open-pr opened
// for it, the head it read, and its review. over replaces what it names, and a
// nil value takes the key away.
func postMeta(over map[string]any) map[string]any {
	m := map[string]any{"own_pr": prAddr, "reviewed_head": headA, "review": aReview("done", "")}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m
}

// aReview is a review as a reviewer stores it, with two comments. Its body is
// text a shell would run if the script let it, to touch pwned when there is one.
func aReview(outcome, pwned string) map[string]any {
	return map[string]any{
		"outcome": outcome,
		"body":    "It pushes the branch and opens the pull request. $(touch " + pwned + ") `touch " + pwned + "`",
		"comments": []any{
			map[string]any{"path": "contrib/assembly/open-pr", "line": 42, "body": "The lease is the remote's tip."},
			map[string]any{"path": "README.md", "line": 1, "body": "ok"},
		},
	}
}

// A reviewer's stored review is posted on the pull request the bead tracks, as
// the App and on the head the reviewer recorded, with its comments inline on
// the right side of the diff and the verdict as the first line of its body, and
// the bead goes on the way the review says: the approving review that leads to
// the yard's tests gives the bead merge-ok, and the review is taken off the
// bead. The account that opened the pull request cannot approve it, so a review
// GitHub refuses on that ground is posted again as a comment review.
func TestPostReviewPosts(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	pwned := filepath.Join(t.TempDir(), "pwned")
	for _, c := range []struct {
		what    string
		stage   string
		outcome string
		files   map[string]string
		to      string   // where the bead goes
		events  []string // the reviews GitHub is asked for, in order
		verdict string   // the first line of the body
		yes     bool     // merge-ok is on the bead after
	}{
		{"an approval that needs the security review", "review-post", "done", nil,
			"review-security", []string{"APPROVE"}, "Approved", false},
		{"an approval that needs nothing but the tests", "review-post", "done-safe", nil,
			"ciwait", []string{"APPROVE"}, "Approved", true},
		{"changes asked at review", "review-post", "changes", nil,
			"new", []string{"REQUEST_CHANGES"}, "Changes needed", false},
		{"the security review's approval", "review-security-post", "done", nil,
			"ciwait", []string{"APPROVE"}, "Approved", true},
		{"changes asked at the security review", "review-security-post", "changes", nil,
			"new", []string{"REQUEST_CHANGES"}, "Changes needed", false},
		{"an approval of the pull request the App opened", "review-post", "done-safe", map[string]string{"self": ""},
			"ciwait", []string{"APPROVE", "COMMENT"}, "Approved", true},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := postYard(t)
			const id = "r-post"
			y.postBead(id, c.stage, postMeta(map[string]any{"review": aReview(c.outcome, pwned)}), true)
			postStandIn(t, gh, id, c.files)
			y.exec("post-review", "yardr-post", id)

			b := y.beadOf(id)
			if b.Stage != c.to || b.Hold || b.Status == "closed" {
				t.Fatalf("%s is in %s, held=%v; want %s:\n%s", id, b.Stage, b.Hold, c.to, y.notesOf(id))
			}
			if got := strings.Contains(strings.Join(b.Labels, ","), "merge-ok"); got != c.yes {
				t.Errorf("labels = %v, want merge-ok = %v", b.Labels, c.yes)
			}
			if b.Metadata["review"] != "" {
				t.Errorf("the review is still stored after it was posted: %v", b.Metadata["review"])
			}
			got := posts(t, gh)
			if len(got) != len(c.events) {
				t.Fatalf("%d reviews posted, want %d:\n%s", len(got), len(c.events), y.notesOf(id))
			}
			for i, event := range c.events {
				if !strings.Contains(got[i], `"event":"`+event+`"`) {
					t.Errorf("review %d is not a %s review:\n%s", i, event, got[i])
				}
			}
			for _, want := range []string{
				`"body":"` + c.verdict + `\n\nIt pushes the branch`, `"commit_id":"` + headA + `"`, `"side":"RIGHT"`,
				`"path":"contrib/assembly/open-pr","line":42`, `$(touch ` + pwned + `)`,
			} {
				if !strings.Contains(got[0], want) {
					t.Errorf("the request lacks %s:\n%s", want, got[0])
				}
			}
			// A comment review is the same review with the other event.
			if len(got) == 2 && strings.Replace(got[0], `"event":"APPROVE"`, `"event":"COMMENT"`, 1) != got[1] {
				t.Errorf("the comment review is not the same review:\n%s\n%s", got[0], got[1])
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Errorf("the review's text ran as a command")
			}
			for _, name := range posts(t, gh) {
				if strings.Contains(name, yardOnlyMark) {
					t.Errorf("the bead's own words reached the review:\n%s", name)
				}
			}
			if n := countCalls(t, gh, "pr view "+prAddr+" "); n != 1 {
				t.Errorf("gh was asked to look at the pull request %d times, want once:\n%v", n, ghCalls(t, gh))
			}
			if note := y.lastNoteOf(id); !strings.Contains(note, "inline comments on "+prAddr+": "+prAddr+"#pullrequestreview-1") {
				t.Errorf("no note that names the review:\n%s", note)
			}
			if got := y.refusedMoves(id); len(got) != 0 {
				t.Errorf("post-review tried moves the flow refuses: %v", got)
			}
			if !strings.Contains(readFile(t, filepath.Join(gh, "mint-stdin")), `"scope":"o/r"`) {
				t.Errorf("the mint was asked %s, want the scope o/r", readFile(t, filepath.Join(gh, "mint-stdin")))
			}
			y.leaked(gh, id)
		})
	}
}

// What the reviewer can put right goes back to the reviewer's stage with the
// reason in the note, and nothing is posted that GitHub was not asked for: no
// review stored, one that is no object or has a field wrong, an outcome the
// stage does not take, no head recorded, a pull request that has moved since
// the head was read, and a review that GitHub refuses as written. The stored
// review stays on the bead for the reviewer to correct.
func TestPostReviewSendsBackWhatTheReviewerCanPutRight(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	for _, c := range []struct {
		what  string
		stage string // review-post unless written
		meta  map[string]any
		files map[string]string
		note  string
		posts int // the reviews GitHub was asked for
	}{
		{what: "no review stored", meta: map[string]any{"review": nil}, note: "has no stored review"},
		{what: "a review that did not parse as JSON", meta: map[string]any{"review": "{\"outcome\": \"done\", "}, note: "is no JSON object"},
		{what: "an outcome that is none", meta: map[string]any{"review": map[string]any{"outcome": "merge", "body": "x"}}, note: "outcome is done, done-safe or changes"},
		{what: "an outcome the security review does not take", stage: "review-security-post", meta: map[string]any{"review": map[string]any{"outcome": "done-safe", "body": "x"}},
			note: "says done-safe, which review-security-post does not take (it takes done or changes)"},
		{what: "no text", meta: map[string]any{"review": map[string]any{"outcome": "done"}}, note: "body is the review text"},
		{what: "a comment outside the repository", meta: map[string]any{"review": map[string]any{"outcome": "done", "body": "x",
			"comments": []any{map[string]any{"path": "../x", "line": 1, "body": "y"}}}}, note: "each comment is {path, line, body}"},
		{what: "no head recorded", meta: map[string]any{"reviewed_head": nil}, note: "has no reviewed_head"},
		{what: "a head that is not a whole commit", meta: map[string]any{"reviewed_head": headA[:7]}, note: "has no reviewed_head"},
		{what: "a pull request that moved", files: map[string]string{"head": headB},
			note: "has moved since the review was written (you read " + headA[:7] + ", its head is " + headB[:7] + ")"},
		{what: "a line that is not in the diff", files: map[string]string{"refuse": "gh: Unprocessable Entity: Line could not be resolved (HTTP 422)"},
			note: "GitHub refused the review as stored", posts: 1},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := postYard(t)
			const id = "r-back"
			stage, back := or(c.stage, "review-post"), "review"
			if stage == "review-security-post" {
				back = "review-security"
			}
			y.postBead(id, stage, postMeta(c.meta), true)
			stored := y.beadOf(id).Metadata["review"]
			postStandIn(t, gh, id, c.files)
			y.exec("post-review", "yardr-post", id)

			if b := y.beadOf(id); b.Stage != back || b.Hold || b.Status == "closed" {
				t.Fatalf("%s is in %s, held=%v; want %s:\n%s", id, b.Stage, b.Hold, back, y.notesOf(id))
			}
			if note := y.lastNoteOf(id); !strings.Contains(note, c.note) || !strings.Contains(note, "post-review: ") {
				t.Errorf("no note from post-review with %q:\n%s", c.note, note)
			}
			if got := len(posts(t, gh)); got != c.posts {
				t.Errorf("%d reviews posted, want %d", got, c.posts)
			}
			b := y.beadOf(id)
			if strings.Contains(strings.Join(b.Labels, ","), "merge-ok") {
				t.Errorf("a review that was not posted gave merge-ok: %v", b.Labels)
			}
			if !reflect.DeepEqual(b.Metadata["review"], stored) {
				t.Errorf("the stored review changed on a bead whose review was not posted: %v, was %v", b.Metadata["review"], stored)
			}
			if got := y.refusedMoves(id); len(got) != 0 {
				t.Errorf("post-review tried moves the flow refuses: %v", got)
			}
			y.leaked(gh, id)
		})
	}
}

// What is not the reviewer's to put right holds the bead in its posting stage,
// with the reason in the note, for the yard to act on and unhold: a bead that
// was not sent here, a pull request that is not the bead's own (no own_pr, one
// that names another pull request, a pull request of another branch or another
// repository) or is not open, a group with no App, a mint that fails and any
// other refusal from GitHub. None of them posts a review on a pull request that
// is not the bead's own, and none touches the stored review.
func TestPostReviewHoldsWhatIsNotTheReviewers(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	for _, c := range []struct {
		what   string
		stage  string // review-post unless written
		meta   map[string]any
		noLink bool              // the bead tracks no pull request
		files  map[string]string // the stand-in's state
		noEnv  bool              // the group has no env_file
		note   string
		posts  int
		viewed bool // gh was asked to look at the pull request
		minted bool // a token was asked for
	}{
		{what: "a bead that was not sent here", stage: "new", note: "r-hold is in new, and a review is posted from review-post or review-security-post"},
		{what: "a bead that tracks no pull request", noLink: true, note: "tracks no GitHub pull request"},
		{what: "a pull request the public line did not open", meta: map[string]any{"own_pr": nil},
			note: "was not opened for its own branch by open-pr"},
		{what: "an own_pr that names another pull request", meta: map[string]any{"own_pr": prURLn(8)},
			note: "was not opened for its own branch by open-pr"},
		{what: "a pull request of another branch", files: map[string]string{"head-ref": "yard/other"}, minted: true, viewed: true,
			note: "is not from yard/r-hold of o/r"},
		{what: "a pull request of another repository", files: map[string]string{"head-owner": "x"}, minted: true, viewed: true,
			note: "is not from yard/r-hold of o/r"},
		{what: "a pull request that is closed", files: map[string]string{"state": "CLOSED"}, minted: true, viewed: true, note: "is not open"},
		{what: "a group with no App", noEnv: true, note: "no GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE"},
		{what: "a mint that fails", files: map[string]string{"mint-fail": "no key"}, minted: true, note: "minting a token for o/r failed"},
		{what: "GitHub refusing the App", files: map[string]string{"refuse": "gh: Resource not accessible by integration (HTTP 403)"},
			minted: true, viewed: true, posts: 1, note: "GitHub refused the review: "},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := postYard(t)
			const id = "r-hold"
			stage := or(c.stage, "review-post")
			y.postBead(id, stage, postMeta(c.meta), !c.noLink)
			stored := y.beadOf(id).Metadata["review"]
			if c.noEnv {
				y.envFile = ""
			}
			postStandIn(t, gh, id, c.files)
			y.exec("post-review", "yardr-post", id)

			if b := y.beadOf(id); b.Stage != stage || !b.Hold || b.Status == "closed" {
				t.Fatalf("%s is in %s, held=%v; want held in %s:\n%s", id, b.Stage, b.Hold, stage, y.notesOf(id))
			}
			if note := y.lastNoteOf(id); !strings.Contains(note, c.note) || !strings.Contains(note, "post-review: ") {
				t.Errorf("no note from post-review with %q:\n%s", c.note, note)
			}
			if got := len(posts(t, gh)); got != c.posts {
				t.Errorf("%d reviews posted, want %d", got, c.posts)
			}
			if got := countCalls(t, gh, "pr view ") > 0; got != c.viewed {
				t.Errorf("gh was asked to look at the pull request = %v, want %v:\n%v", got, c.viewed, ghCalls(t, gh))
			}
			if _, err := os.Stat(filepath.Join(gh, "mint-stdin")); (err == nil) != c.minted {
				t.Errorf("a token was asked for = %v, want %v", err == nil, c.minted)
			}
			b := y.beadOf(id)
			if !reflect.DeepEqual(b.Metadata["review"], stored) {
				t.Errorf("the stored review changed on a bead whose review was not posted: %v, was %v", b.Metadata["review"], stored)
			}
			if strings.Contains(strings.Join(b.Labels, ","), "merge-ok") {
				t.Errorf("a review that was not posted gave merge-ok: %v", b.Labels)
			}
			if got := y.refusedMoves(id); len(got) != 0 {
				t.Errorf("post-review tried moves the flow refuses: %v", got)
			}
			y.leaked(gh, id)
		})
	}
}

// A done-safe on a diff that touches a path the base lists is refused when the
// reviewer reports it, and again when the posting stage reports it, so that a
// reviewer that reports done and stores done-safe cannot skip the security
// review. The review is posted by then, so the bead is held with the way to move
// it on by hand, and the note says not to unhold it, which would post the review
// twice.
func TestPostReviewHoldsWhenTheYardRefusesDoneSafe(t *testing.T) {
	y, gh := postYard(t)
	const id = "r-safe"
	y.commitMsg(y.repo, "List the security paths", map[string]string{".yardr/security_paths": "contrib/**\n"})
	wt := filepath.Join(t.TempDir(), id)
	y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, "main")
	y.commitMsg(wt, "Touch a listed path", map[string]string{"contrib/x.txt": "x\n"})
	y.postBead(id, "review-post", postMeta(map[string]any{"review": aReview("done-safe", "")}), true)
	postStandIn(t, gh, id, nil)
	y.exec("post-review", "yardr-post", id)

	b := y.beadOf(id)
	if b.Stage != "review-post" || !b.Hold || b.Metadata["review"] != "" {
		t.Fatalf("%s is in %s, held=%v, review %v; want held in review-post with the review taken off:\n%s", id, b.Stage, b.Hold, b.Metadata["review"], y.notesOf(id))
	}
	for _, label := range b.Labels {
		if label == "merge-ok" {
			t.Errorf("a done-safe the yard refused left merge-ok on the bead: %v", b.Labels)
			break
		}
	}
	if got := len(posts(t, gh)); got != 1 {
		t.Errorf("%d reviews posted, want the one", got)
	}
	if got := y.refusedMoves(id); len(got) != 1 || !strings.Contains(got[0], "done-safe is refused") {
		t.Errorf("the yard refused %v, want the one report of done-safe", got)
	}
	note := y.lastNoteOf(id)
	for _, want := range []string{"could not report done-safe", "security_paths", prAddr + "#pullrequestreview-1", "merge-ok was removed", "Do not unhold it, which would post the review again", "yardr bead advance " + id + " --to review-security"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
}

// A review that cannot be posted goes back to its reviewer once. The flow
// counts the rounds back to the review stage, so a second failed post goes to a
// person (decide) with the reason in the note instead of round again, however
// the reviewer reports.
func TestPostReviewFailsOnceThenAPersonDecides(t *testing.T) {
	y, gh := postYard(t)
	const id = "r-twice"
	// The reviewer's review has no text; it reports done, which takes the bead
	// to the posting stage, each time.
	y.postBead(id, "review", postMeta(map[string]any{"review": map[string]any{"outcome": "done"}}), true)
	for round, want := range []string{"review", "decide"} {
		y.yardr("bead", "done", id)
		if b := y.beadOf(id); b.Stage != "review-post" {
			t.Fatalf("round %d: %s is in %s after the reviewer's report, want review-post", round+1, id, b.Stage)
		}
		postStandIn(t, gh, id, nil)
		y.exec("post-review", "yardr-post", id)
		if b := y.beadOf(id); b.Stage != want || b.Hold {
			t.Fatalf("round %d: %s is in %s, held=%v; want %s:\n%s", round+1, id, b.Stage, b.Hold, want, y.notesOf(id))
		}
		if note := y.lastNoteOf(id); !strings.Contains(note, "body is the review text") {
			t.Errorf("round %d: no note with the reason:\n%s", round+1, note)
		}
	}
	if got := len(posts(t, gh)); got != 0 {
		t.Errorf("%d reviews posted, want none", got)
	}
}

// post-review takes its token from merge-pr's text, copied from the line that
// starts "The token" to the end of merge-pr's binding: the scripts are
// installed one file each, so there is none to source. A change to one is made
// in both, as to pr-ci's (TestPRCIBindsAsMergePRDoes).
func TestPostReviewBindsAsMergePRDoes(t *testing.T) {
	t.Parallel()
	const open, close = "# The token: minted", "# --- end of github_binding ---\n"
	want := markedBlock(t, "merge-pr", open, close)
	got := markedBlock(t, "post-review", open, close)
	if got != want {
		t.Errorf("post-review's token binding differs from merge-pr's: change both\nmerge-pr:\n%s\npost-review:\n%s", want, got)
	}
}
