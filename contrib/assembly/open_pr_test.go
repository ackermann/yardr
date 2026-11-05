package assembly

// open-pr the way an exec group runs it for a bead of the public line in pr:
// the real yardr.public flow of this repository in the yard, a bare repository
// as the remote that the depot's github.com address is rewritten to (so the
// script reads an address it can parse and pushes to a directory), a stand-in
// gh that keeps one pull request in a directory and refuses a call that does
// not carry the mint's token, and merge-pr's stand-in mint. The gh stand-in
// has only the calls open-pr makes: pr create, edit, view and list.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// prAddr is the address of the one pull request the stand-in has.
const prAddr = "https://github.com/o/r/pull/7"

// fakeOpenGh is gh as far as open-pr uses it. Its state, in $FAKE_GH: calls
// (one argv per line, never the token), and files that say what the pull
// request is: pr (it exists), state (OPEN unless it says MERGED or CLOSED),
// head-ref (the branch it is from, which pr create sets), mergeable
// (MERGEABLE unless written), title and body (what pr create and pr edit were
// given), and create-fail (the error of pr create). A call whose GH_TOKEN is
// not the mint's is refused as GitHub refuses a bad credential; pr create for
// a branch that already has one is refused in gh's words.
const fakeOpenGh = `#!/bin/sh
d=$FAKE_GH
echo "$*" >>"$d/calls"
if [ "$GH_TOKEN" != ` + mintToken + ` ]; then echo "HTTP 401: Bad credentials" >&2; exit 1; fi
addr=` + prAddr + `
what="$1 $2"
shift 2
head= title= bf=
while [ $# -gt 0 ]; do
  case $1 in
  --head) head=$2; shift ;;
  --title) title=$2; shift ;;
  --body-file) bf=$2; shift ;;
  esac
  shift
done
case "$what" in
"pr create")
  if [ -e "$d/create-fail" ]; then cat "$d/create-fail" >&2; exit 1; fi
  if [ -e "$d/pr" ]; then
    echo "a pull request for branch \"$head\" into branch \"main\" already exists:" >&2
    echo "$addr" >&2
    exit 1
  fi
  touch "$d/pr"
  printf '%s' "$head" >"$d/head-ref"
  printf '%s' "$title" >"$d/title"
  cp "$bf" "$d/body"
  echo "Creating pull request for $head into main in o/r"
  echo "$addr" ;;
"pr edit")
  printf '%s' "$title" >"$d/title"
  cp "$bf" "$d/body" ;;
"pr view")
  printf '{"number":7,"state":"%s","headRefName":"%s","headRepositoryOwner":{"login":"o"},"headRepository":{"name":"r"},"mergeable":"%s"}\n' \
    "$(cat "$d/state" 2>/dev/null || echo OPEN)" "$(cat "$d/head-ref")" "$(cat "$d/mergeable" 2>/dev/null || echo MERGEABLE)" ;;
"pr list")
  if [ -e "$d/pr" ] && [ "$(cat "$d/state" 2>/dev/null || echo OPEN)" = OPEN ] && [ "$(cat "$d/head-ref")" = "$head" ]; then
    printf '[{"url":"%s","headRefName":"%s","headRepositoryOwner":{"login":"o"},"headRepository":{"name":"r"}}]\n' "$addr" "$head"
  else
    echo '[]'
  fi ;;
*) echo "fake gh: unexpected $what" >&2; exit 1 ;;
esac
`

// openYard is a yard whose depot r is on the public line, with the stand-ins
// for gh and github-mint, the group's env_file with the App's id and the path
// of its key file, and a bare repository as the depot's origin, reached by
// the address github.com/o/r. It holds no bead of the line: a test adds them.
// gh is the stand-in's state directory, origin the remote's.
func openYard(t *testing.T) (y *yard, gh, origin string) {
	t.Helper()
	y = newYard(t)
	gh = t.TempDir()
	if err := os.Mkdir(filepath.Join(gh, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	scripttest.Write(t, filepath.Join(gh, "bin", "gh"), fakeOpenGh)
	mint := filepath.Join(gh, "bin", "github-mint")
	scripttest.Write(t, mint, fakeGithubMint)
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })
	y.envFile = filepath.Join(t.TempDir(), "open-pr.env")
	if err := os.WriteFile(y.envFile, []byte("GITHUB_APP_ID=123456\nGITHUB_APP_PRIVATE_KEY_FILE="+filepath.Join(gh, "app.pem")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.opts = "YARDR_GITHUB_MINT=" + shellQuote(mint+" --from-group")

	origin = filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", origin)
	y.git(y.repo, "remote", "add", "origin", "git@github.com:o/r.git")
	y.git(y.repo, "config", "url."+origin+".insteadOf", "git@github.com:o/r.git")

	// The github source as the pack carries it: yardr link add reads a pull
	// request's address by the shapes its declaration gives.
	decl, err := fs.ReadFile(packs.FS, packs.GitHub+"/github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).PutSource(context.Background(), "github.yaml", string(decl), "test"); err != nil {
		t.Fatal(err)
	}

	y.putFlowFile("yardr.public", storetest.RepoFlow(t, "yardr.public"))
	y.pointFlow("r", "", "yardr.public")
	return y, gh, origin
}

// publicBead adds bead id in pr, of type task, with these labels and links
// and a body and a note of the yard's own that must never reach GitHub, and
// its branch yard/<id> in a worktree of its own, one commit past main: the
// message msg, with a file under contrib/assembly. It returns the worktree.
func (y *yard) publicBead(id, title, msg string, labels []string, links ...store.NewLink) string {
	y.t.Helper()
	ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if _, err := st.CreateBead(ctx, store.NewBead{Title: title, Body: yardOnlyBody, ID: id, Depot: "r", Type: "task",
		Stage: "pr", Priority: store.DefaultPriority, Labels: labels, Links: links}, "test"); err != nil {
		y.t.Fatal(err)
	}
	if err := st.AddNote(ctx, id, yardOnlyNote, "test"); err != nil {
		y.t.Fatal(err)
	}
	wt := filepath.Join(y.home, "worktrees", "r", id)
	y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, "main")
	if msg != "" {
		y.commitMsg(wt, msg, map[string]string{"contrib/assembly/" + id + ".txt": id + "\n"})
	}
	return wt
}

// What a bead's body and notes say of the yard's business, which is no one
// else's: the pull request is public, and none of it may be on it. The marker
// is the same in both, for the check to look for.
const (
	yardOnlyMark = "yard-only"
	yardOnlyBody = "yard-only body text, for the yard to read"
	yardOnlyNote = "builder: yard-only note text, for the yard to read"
)

// openStandIn forgets everything of the stand-ins' last run and sets files
// (a file each, the text in it) for the next.
func openStandIn(t *testing.T, gh string, files map[string]string) {
	t.Helper()
	for _, name := range []string{"calls", "pr", "state", "head-ref", "mergeable", "title", "body", "create-fail", "mint-fail", "mint-stdin", "mint-args"} {
		if err := os.Remove(filepath.Join(gh, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(gh, name), []byte(text+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// remoteTip is where origin has branch yard/<id>, empty when it has none.
func (y *yard) remoteTip(origin, id string) string {
	y.t.Helper()
	return strings.TrimSpace(y.git(origin, "for-each-ref", "--format=%(objectname)", "refs/heads/yard/"+id))
}

// linksOf is the addresses bead id tracks.
func (y *yard) linksOf(id string) []string {
	y.t.Helper()
	links, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).Links(context.Background(), id)
	if err != nil {
		y.t.Fatal(err)
	}
	var out []string
	for _, l := range links {
		if l.Kind == store.LinkTracks {
			out = append(out, l.URL)
		}
	}
	return out
}

// backToPR sends bead id round again, as a returned bead goes: from review
// through the stage that posts the review, back to new, and on to pr when its
// builder reports done.
func (y *yard) backToPR(id string) {
	y.t.Helper()
	y.yardr("bead", "advance", id, "--to", "review-post")
	y.yardr("bead", "advance", id, "--to", "new")
	y.yardr("bead", "advance", id, "--to", "pr")
}

// leaked asserts that nothing of the yard's own, nor the token, is on what
// went to GitHub (the title and body, every call) or in what the script wrote
// (its log and its last note). The bead's own body and note stay in the yard.
func (y *yard) leaked(gh, id string) {
	y.t.Helper()
	seen := y.log + y.lastNoteOf(id) + strings.Join(ghCalls(y.t, gh), "\n")
	for _, name := range []string{"title", "body"} {
		if b, err := os.ReadFile(filepath.Join(gh, name)); err == nil {
			seen += string(b)
		}
	}
	for _, secret := range []string{yardOnlyMark, mintToken} {
		if strings.Contains(seen, secret) {
			y.t.Errorf("%q reached GitHub or a note of the script:\n%s", secret, seen)
		}
	}
}

// The branch is pushed, the pull request opened as the App with the squash
// subject for its title and the commits for its body, and the bead tracks it
// and records it as its own; a bead that comes back is pushed to the same
// pull request, also when the builder rebased (the pushed commits are
// rewritten); and a remote branch with a commit the bead's branch never had
// is not overwritten. Nothing of the bead's body or notes, and not the token,
// is on GitHub or in a note.
func TestOpenPRPublishesTheBranch(t *testing.T) {
	y, gh, origin := openYard(t)
	const id = "r-2"
	msg := "Add the open step\n\nIt pushes the branch and opens the pull request.\n\nYardr-Agent: claude/model-x\n"
	wt := y.publicBead(id, "Add the open step", msg, []string{"kind:feat", "merge-ok"})
	run := func(files map[string]string) {
		t.Helper()
		openStandIn(t, gh, files)
		y.exec("open-pr", "yardr-pr", id)
	}
	creates := func() int { return countCalls(t, gh, "pr create ") }
	edits := func() int { return countCalls(t, gh, "pr edit ") }

	// The first run opens it.
	run(nil)
	if b := y.beadOf(id); b.Stage != "review" || b.Hold {
		t.Fatalf("after the first run %s is in %s, held=%v, want review:\n%s", id, b.Stage, b.Hold, y.notesOf(id))
	}
	if got, want := y.remoteTip(origin, id), y.git(wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("origin has yard/%s at %q, want %s", id, got, want)
	}
	if n := creates(); n != 1 || edits() != 0 {
		t.Errorf("the first run asked gh to create %d and edit %d pull requests, want 1 and 0:\n%v", n, edits(), ghCalls(t, gh))
	}
	if got := ghCalls(t, gh)[0]; !strings.HasPrefix(got, "pr create -R o/r --head yard/r-2 --base main --title feat(assembly): Add the open step (r-2) --body-file ") {
		t.Errorf("gh was first asked %q", got)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(gh, "title"))); got != "feat(assembly): Add the open step (r-2)" {
		t.Errorf("title = %q, want the squash subject line", got)
	}
	body := readFile(t, filepath.Join(gh, "body"))
	for _, want := range []string{"Add the open step\n\nIt pushes the branch and opens the pull request.\n", "\nYardr-Bead: r-2\n", "\nYardr-Agent: claude/model-x\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	// The trailer the hook wrote is the footer's, once.
	if n := strings.Count(body, "Yardr-Agent:"); n != 1 {
		t.Errorf("body has Yardr-Agent %d times, want once:\n%s", n, body)
	}
	if got := y.linksOf(id); len(got) != 1 || got[0] != prAddr {
		t.Errorf("%s tracks %v, want %s", id, got, prAddr)
	}
	b := y.beadOf(id)
	if b.Metadata["own_pr"] != prAddr {
		t.Errorf("own_pr = %v, want %s", b.Metadata["own_pr"], prAddr)
	}
	if strings.Join(b.Labels, ",") != "kind:feat" {
		t.Errorf("labels = %v, want merge-ok off and kind:feat kept", b.Labels)
	}
	if note := y.lastNoteOf(id); !strings.Contains(note, "opened "+prAddr+" as feat(assembly): Add the open step (r-2)") {
		t.Errorf("no note that names the pull request:\n%s", note)
	}
	// Every call carried the mint's token (the stand-in refuses one that does
	// not), which was minted for the repository alone.
	if !strings.Contains(readFile(t, filepath.Join(gh, "mint-stdin")), `"scope":"o/r"`) {
		t.Errorf("the mint was asked %s, want the scope o/r", readFile(t, filepath.Join(gh, "mint-stdin")))
	}
	y.leaked(gh, id)

	// The same bead, sent round and pushed again: the same pull request, its
	// title and body written again, and the yes of the old head off it.
	tip1 := y.git(wt, "rev-parse", "HEAD")
	y.commitMsg(wt, "Check the lease\n\nA remote tip the branch had is replaced.\n", map[string]string{"contrib/assembly/second.txt": "2\n"})
	y.backToPR(id)
	y.yardr("bead", "update", id, "--add-label", "merge-ok")
	// The stand-in keeps the pull request across the run, as GitHub does.
	prev := map[string]string{"pr": "", "head-ref": "yard/" + id}
	run(prev)
	if b := y.beadOf(id); b.Stage != "review" || b.Hold || strings.Contains(strings.Join(b.Labels, ","), "merge-ok") {
		t.Fatalf("after the second run %s is in %s, held=%v, labels %v, want review without merge-ok:\n%s", id, b.Stage, b.Hold, b.Labels, y.notesOf(id))
	}
	if n := creates(); n != 0 || edits() != 1 {
		t.Errorf("the second run asked gh to create %d and edit %d pull requests, want 0 and 1:\n%v", n, edits(), ghCalls(t, gh))
	}
	if got, want := y.remoteTip(origin, id), y.git(wt, "rev-parse", "HEAD"); got != want || got == tip1 {
		t.Errorf("origin has yard/%s at %q after the second run, want the new head %s (not %s)", id, got, want, tip1)
	}
	if body := readFile(t, filepath.Join(gh, "body")); !strings.Contains(body, "Add the open step") || !strings.Contains(body, "Check the lease") {
		t.Errorf("body after the second run has not both commits:\n%s", body)
	}
	if got := y.linksOf(id); len(got) != 1 {
		t.Errorf("%s tracks %v after the second run, want the one pull request", id, got)
	}
	y.leaked(gh, id)

	// A rebase rewrites what was pushed: the old tip is in the branch's reflog,
	// so it is replaced, and the pull request is still the one.
	y.git(wt, "commit", "-q", "--amend", "-m", "Check the lease, rebased")
	y.backToPR(id)
	run(prev)
	if b := y.beadOf(id); b.Stage != "review" || b.Hold {
		t.Fatalf("after the rebase %s is in %s, held=%v, want review:\n%s", id, b.Stage, b.Hold, y.notesOf(id))
	}
	if got, want := y.remoteTip(origin, id), y.git(wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("origin has yard/%s at %q after the rebase, want %s", id, got, want)
	}

	// Somebody else's commit on the remote branch is not overwritten.
	other := filepath.Join(t.TempDir(), "other")
	y.git(".", "clone", "-q", "-b", "yard/"+id, origin, other)
	foreign := y.commitMsg(other, "Somebody else's commit", map[string]string{"theirs.txt": "x\n"})
	y.git(other, "push", "-q", "origin", "yard/"+id)
	y.commitMsg(wt, "One more", map[string]string{"contrib/assembly/third.txt": "3\n"})
	y.backToPR(id)
	run(prev)
	if b := y.beadOf(id); b.Stage != "pr" || !b.Hold {
		t.Fatalf("with a foreign commit on the remote %s is in %s, held=%v, want held in pr:\n%s", id, b.Stage, b.Hold, y.notesOf(id))
	}
	if got := y.remoteTip(origin, id); got != foreign {
		t.Errorf("origin has yard/%s at %q, want the foreign commit %s left alone", id, got, foreign)
	}
	if note := y.lastNoteOf(id); !strings.Contains(note, "which yard/r-2 never had; not forced") {
		t.Errorf("the hold does not say the commit was never the branch's:\n%s", note)
	}
	if n := edits(); n != 0 {
		t.Errorf("the pull request was edited %d times after a push that was not made", n)
	}
	y.leaked(gh, id)
}

// What open-pr does not publish: each case is one bead's run and where it
// ends, in the stage the outcome leads to or held in pr, with what the note
// says and whether the branch reached the remote and gh was asked to open a
// pull request.
func TestOpenPRDoesNotPublish(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	const id = "r-3"
	for _, c := range []struct {
		what   string
		commit bool                             // the branch has a commit past main
		links  []store.NewLink                  // what the bead tracks already
		setup  func(y *yard, wt, origin string) // before the run
		files  map[string]string                // the stand-in's state
		noEnv  bool                             // the group has no env_file
		secret string                           // a remote URL credential that must not reach a note or log
		stage  string                           // where the bead is after
		held   bool                             // and whether it is held
		note   string                           // in the last note
		pushed bool                             // the branch is on the remote
		opened bool                             // gh was asked to open a pull request
		edited bool                             // gh was asked to write one again
		linked bool                             // the bead tracks the pull request after
	}{
		{what: "a branch with nothing past the base", stage: "new", note: "nothing to publish: yard/r-3 has no commit past main"},
		{what: "a pull request that conflicts with the base", commit: true, files: map[string]string{"mergeable": "CONFLICTING"},
			stage: "new", note: "conflicts with main; rebase yard/r-3 onto main", pushed: true, opened: true, linked: true},
		{what: "a group with no App", commit: true, noEnv: true, stage: "pr", held: true, note: "no GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE"},
		{what: "a mint that fails", commit: true, files: map[string]string{"mint-fail": "no key"}, stage: "pr", held: true, note: "minting a token for o/r failed"},
		{what: "a remote URL with credentials that is no github.com repository", commit: true, stage: "pr", held: true,
			note: "is no github.com repository", secret: "remote-test-secret",
			setup: func(y *yard, wt, origin string) {
				y.git(y.repo, "remote", "set-url", "origin", "https://builder:remote-test-secret@example.invalid/o/r.git")
			}},
		{what: "a push URL that names another GitHub repository", commit: true, stage: "pr", held: true,
			note: "fetch URL and push URL name different repositories",
			setup: func(y *yard, wt, origin string) {
				y.git(y.repo, "config", "remote.origin.pushurl", "git@github.com:x/y.git")
			}},
		{what: "a link to a pull request of another repository", commit: true,
			links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: "https://github.com/x/y/pull/3"}},
			stage: "pr", held: true, note: "no pull request of o/r"},
		{what: "a link to a pull request of another branch", commit: true,
			links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: prAddr}},
			files: map[string]string{"pr": "", "head-ref": "yard/other"},
			stage: "pr", held: true, note: "is not from yard/r-3 of o/r", linked: true},
		{what: "a link to a pull request that is closed", commit: true,
			links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: prAddr}},
			files: map[string]string{"pr": "", "head-ref": "yard/r-3", "state": "CLOSED"},
			stage: "pr", held: true, note: "is not open; not pushing to it", linked: true},
		{what: "GitHub refusing to open one", commit: true, files: map[string]string{"create-fail": "HTTP 403: Resource not accessible by integration"},
			stage: "pr", held: true, note: "did not open a pull request for yard/r-3 on o/r: HTTP 403", pushed: true, opened: true},
		{what: "a worktree with changes", commit: true, stage: "pr", held: true, note: "has uncommitted changes",
			setup: func(y *yard, wt, origin string) {
				if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x\n"), 0o644); err != nil {
					y.t.Fatal(err)
				}
			}},
		// An earlier run died between opening the pull request and linking it:
		// GitHub has one for the branch, and gh refuses to open a second.
		{what: "a pull request open for the branch that the bead does not track", commit: true,
			files: map[string]string{"pr": "", "head-ref": "yard/r-3"},
			stage: "review", note: "updated " + prAddr, pushed: true, opened: true, edited: true, linked: true},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh, origin := openYard(t)
			msg := ""
			if c.commit {
				msg = "Add the open step"
			}
			wt := y.publicBead(id, "Add the open step", msg, []string{"kind:feat"}, c.links...)
			if c.setup != nil {
				c.setup(y, wt, origin)
			}
			if c.noEnv {
				y.envFile = ""
			}
			openStandIn(t, gh, c.files)
			y.exec("open-pr", "yardr-pr", id)

			if b := y.beadOf(id); b.Stage != c.stage || b.Hold != c.held || (!c.held && b.Status == "closed") {
				t.Fatalf("%s: %s is in %s, held=%v; want %s, held=%v:\n%s", c.what, id, b.Stage, b.Hold, c.stage, c.held, y.notesOf(id))
			}
			if note := y.lastNoteOf(id); !strings.Contains(note, c.note) {
				t.Errorf("%s: no note with %q:\n%s", c.what, c.note, note)
			}
			if got := y.remoteTip(origin, id) != ""; got != c.pushed {
				t.Errorf("%s: branch on the remote = %v, want %v", c.what, got, c.pushed)
			}
			if got := countCalls(t, gh, "pr create ") > 0; got != c.opened {
				t.Errorf("%s: gh asked to open a pull request = %v, want %v:\n%v", c.what, got, c.opened, ghCalls(t, gh))
			}
			if got := countCalls(t, gh, "pr edit ") > 0; got != c.edited {
				t.Errorf("%s: gh asked to write a pull request again = %v, want %v:\n%v", c.what, got, c.edited, ghCalls(t, gh))
			}
			if got := len(y.linksOf(id)) > 0; got != (c.linked || len(c.links) > 0) {
				t.Errorf("%s: %s tracks %v", c.what, id, y.linksOf(id))
			}
			if c.linked && !c.held && y.beadOf(id).Metadata["own_pr"] != prAddr {
				t.Errorf("%s: own_pr = %v, want %s", c.what, y.beadOf(id).Metadata["own_pr"], prAddr)
			}
			if got := y.refusedMoves(id); len(got) != 0 {
				t.Errorf("%s: open-pr tried moves the flow refuses: %v", c.what, got)
			}
			y.leaked(gh, id)
			if c.secret != "" {
				seen := y.log + y.notesOf(id) + strings.Join(ghCalls(t, gh), "\n")
				if strings.Contains(seen, c.secret) {
					t.Errorf("remote credential reached a log, note or GitHub call:\n%s", seen)
				}
			}
		})
	}
}

// open-pr takes its token from merge-pr's text, copied from the line that
// starts "The token" to the end of merge-pr's binding: the scripts are
// installed one file each, so there is none to source. A change to one is made
// in both, as to pr-ci's binding (TestPRCIBindsAsMergePRDoes).
func TestOpenPRMintsAsMergePRDoes(t *testing.T) {
	t.Parallel()
	_, want, ok := strings.Cut(markedBlock(t, "merge-pr", bindingOpen, bindingClose), "# The token: minted")
	if !ok {
		t.Fatal("merge-pr's github_binding has no token section")
	}
	want = "# The token: minted" + strings.TrimSuffix(want, bindingClose)
	got := markedBlock(t, "open-pr", "# --- github_token: ", "# --- end of github_token ---\n")
	_, got, _ = strings.Cut(got, "# The token: minted")
	got = "# The token: minted" + strings.TrimSuffix(got, "# --- end of github_token ---\n")
	if got != want {
		t.Errorf("open-pr's token section differs from merge-pr's: change both\nmerge-pr:\n%s\nopen-pr:\n%s", want, got)
	}
}

// The public line's flow is default.flow with the stages its scripts work, pr
// (open-pr), the two that post a stored review (review-post and
// review-security-post, post-review) and ciwait (pr-ci), so that a bead goes
// back to the old line by taking its pin off: every stage of the default flow
// is in it. The outcomes the scripts report are the ones the flow offers at
// their stages (a report the flow does not name is refused, and the script
// holds the bead), and the stages the people and the reviewers work have the
// stage files the briefs are made of: the ones the two lines share are the
// default line's, byte for byte, and the reviewers' tell them what is posted
// and what is not, and that they hold no key.
func TestPublicFlowFitsItsScripts(t *testing.T) {
	t.Parallel()
	load := func(name string) (*store.Flow, string) {
		t.Helper()
		path := storetest.RepoFlow(t, name)
		f, err := store.ParseFlow(readFile(t, path))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return f, strings.TrimSuffix(path, ".flow")
	}
	def, defDir := load("default")
	pub, pubDir := load("yardr.public")

	// A flow lists a stage once for each rule that names it.
	pubStages, defStages := map[string]bool{}, map[string]bool{}
	for _, s := range pub.Stages {
		pubStages[s] = true
	}
	for _, s := range def.Stages {
		defStages[s] = true
		if !pubStages[s] {
			t.Errorf("yardr.public lacks the stage %s of the default flow, so a bead there cannot go back to the old line", s)
		}
	}
	var added []string
	for s := range pubStages {
		if !defStages[s] {
			added = append(added, s)
		}
	}
	slices.Sort(added)
	if want := []string{"ciwait", "pr", "review-post", "review-security-post"}; !slices.Equal(added, want) {
		t.Errorf("yardr.public adds %v to the default flow's stages, want %v", added, want)
	}

	to := func(stage, outcome string) string {
		for _, o := range pub.Outcomes[stage] {
			if o.Name == outcome {
				return o.To
			}
		}
		return ""
	}
	for _, c := range []struct{ stage, outcome, to string }{
		{"pr", "done", "review"},          // open-pr
		{"pr", "failed", "new"},           // open-pr: nothing to publish, or a conflict
		{"ciwait", "done", "approved"},    // pr-ci
		{"ciwait", "changes", "new"},      // pr-ci
		{"approved", "merged", "merged"},  // merge-pr
		{"approved", "dropped", "decide"}, // merge-pr
		{"new", "done", "pr"},             // the builder
		// The reviewers keep the outcomes the default line gives them, and each
		// approval or request goes through the stage that posts it, which sends the
		// bead on where the default line sends it. A question for a person is not
		// posted.
		{"review", "done", "review-post"},
		{"review", "done-safe", "review-post"},
		{"review", "changes", "review-post"},
		{"review", "question", "decide"},
		{"review-security", "done", "review-security-post"},
		{"review-security", "changes", "review-security-post"},
		{"review-security", "question", "decide"},
		{"review-post", "done", "review-security"},
		{"review-post", "done-safe", "ciwait"}, // past the security review, to the tests and not to a landing
		{"review-post", "changes", "new"},
		{"review-post", "failed", "review"}, // post-review: a review the reviewer can put right
		{"review-security-post", "done", "ciwait"},
		{"review-security-post", "changes", "new"},
		{"review-security-post", "failed", "review-security"},
	} {
		if got := to(c.stage, c.outcome); got != c.to {
			t.Errorf("%s offers %s to %q, want %q", c.stage, c.outcome, got, c.to)
		}
	}
	// The recoveries the scripts' holds name, and the person who reads a second
	// failed post, are moves the flow allows.
	for _, m := range [][2]string{{"ciwait", "review"}, {"approved", "ciwait"}, {"approved", "new"},
		{"decide", "review"}, {"decide", "review-security"}, {"decide", "review-post"}, {"decide", "review-security-post"}} {
		if !slices.Contains(pub.Transitions[m[0]], m[1]) {
			t.Errorf("yardr.public has no move from %s to %s, which a hold of its scripts tells a person to make", m[0], m[1])
		}
	}
	// The rounds back to new are the default line's, counted together; a review
	// that could not be posted goes back to its reviewer once, then to a person.
	for _, o := range []string{"changes", "failed"} {
		for _, stage := range []string{"pr", "review-post", "review-security-post", "ciwait"} {
			for _, out := range pub.Outcomes[stage] {
				if out.Name == o && out.To == "new" && (out.Max != 3 || out.Then != "decide") {
					t.Errorf("%s %s to new carries max %d then %q, want the default line's 3 rounds then decide", stage, o, out.Max, out.Then)
				}
			}
		}
	}
	for _, stage := range []string{"review-post", "review-security-post"} {
		for _, out := range pub.Outcomes[stage] {
			if out.Name == "failed" && (out.Max != 1 || out.Then != "decide") {
				t.Errorf("%s failed to %s carries max %d then %q, want one retry then decide", stage, out.To, out.Max, out.Then)
			}
		}
	}

	// post-review reports the outcome the stored review names, which the stage
	// takes, and gives merge-ok where that outcome leads to the tests: the table
	// in the script is the flow's, outcome for outcome.
	script := readFile(t, "post-review")
	pair := regexp.MustCompile(`review(?:-security)?-post:[a-z-]+`)
	var inFlow []string
	for _, stage := range []string{"review-post", "review-security-post"} {
		for _, out := range pub.Outcomes[stage] {
			if out.Name != "failed" {
				inFlow = append(inFlow, stage+":"+out.Name)
			}
		}
	}
	table := regexp.MustCompile(`(?s)case \$stage:\$outcome in\n(review-post:.*?)\) ;;`).FindStringSubmatch(script)
	if table == nil {
		t.Fatal("post-review has no table of the outcomes its stages take")
	}
	inScript := pair.FindAllString(table[1], -1)
	slices.Sort(inFlow)
	slices.Sort(inScript)
	if !slices.Equal(inFlow, inScript) {
		t.Errorf("post-review takes the stored outcomes %v, and the flow offers %v at the posting stages", inScript, inFlow)
	}
	var wantYes, gotYes []string
	for _, stage := range []string{"review-post", "review-security-post"} {
		for _, out := range pub.Outcomes[stage] {
			if out.To == "ciwait" {
				wantYes = append(wantYes, stage+":"+out.Name)
			}
		}
	}
	if m := regexp.MustCompile(`case \$stage:\$outcome in ([^)]*)\) yes=1`).FindStringSubmatch(script); m != nil {
		gotYes = pair.FindAllString(m[1], -1)
	}
	slices.Sort(wantYes)
	slices.Sort(gotYes)
	if !slices.Equal(wantYes, gotYes) {
		t.Errorf("post-review gives merge-ok for %v, and the flow sends %v to ciwait", gotYes, wantYes)
	}

	terminal := map[string]bool{}
	for _, s := range pub.Terminal {
		terminal[s] = true
	}
	shared := map[string]bool{"backlog": true, "shape": true, "siding": true, "ready": true, "decide": true}
	own := map[string]bool{"new": true, "pr": true, "review": true, "review-post": true, "review-security": true,
		"review-security-post": true, "ciwait": true, "approved": true}
	files := map[string]bool{}
	for _, s := range pub.Stages {
		if terminal[s] || files[s] {
			continue
		}
		files[s] = true
		text := readFile(t, filepath.Join(pubDir, s+".md"))
		if shared[s] && text != readFile(t, filepath.Join(defDir, s+".md")) {
			t.Errorf("the stage file of %s differs from the default flow's: it is the same stage on both lines, so change both", s)
		}
		if !shared[s] && !own[s] {
			t.Errorf("stage %s has a file that nothing says is shared or its own", s)
		}
	}
	// What the reviewers are told: the posted text describes the change and the
	// code only, the review is stored on the bead with the head it is of, and
	// they hold no key: nothing in their files has them run a script that posts,
	// say merge-ok or use the App's variables.
	for _, s := range []string{"review", "review-security"} {
		text := strings.Join(strings.Fields(readFile(t, filepath.Join(pubDir, s+".md"))), " ")
		for _, want := range []string{"The posted text describes the change and the code only", "no customer, lead, price", "--meta reviewed_head=", "--meta review=", "hold no key"} {
			if !strings.Contains(text, want) {
				t.Errorf("the stage file of %s on the public line does not say %q", s, want)
			}
		}
		for _, banned := range []string{"scripts/post-review", "--add-label", "GITHUB_APP", "gh api", "gh pr"} {
			if strings.Contains(text, banned) {
				t.Errorf("the stage file of %s on the public line has the reviewer use %q, but a reviewer holds no key", s, banned)
			}
		}
	}
	// The default line's reviewers are untouched: they are not told of any of it.
	for _, s := range []string{"review", "review-security"} {
		if text := readFile(t, filepath.Join(defDir, s+".md")); strings.Contains(text, "post-review") || strings.Contains(text, "reviewed_head") {
			t.Errorf("the default line's stage file of %s mentions the public line", s)
		}
	}
}
