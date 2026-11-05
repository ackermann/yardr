package assembly

// merge-pr the way an exec group runs it for a pr bead in approved: the
// real pr flow of the contributors pack in the yard, the group's env_file
// giving the App's id and key file, a stand-in github-mint that answers a
// token for the scope it is asked, and a stand-in gh that keeps one pull
// request's state in a directory, refuses a call that does not carry that
// token, and merges with GitHub's rule for --match-head-commit.
//
// Each case has a yard of its own (a copy of the template yard, which is
// cheaper than a run of the script), so the cases run beside one another:
// on a bead of its own where the run ends the bead (a merge), else on a
// bead it holds and a maintainer unholds before running it again.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/scripttest"
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
// written), title and body (bump and a line of text unless written),
// view-fail and refuse (the error of that call), race (the commit
// the head moves to as the merge starts) and head-next (the commit the head
// moves to once the next look at the pull request has been answered). A call whose GH_TOKEN is not
// the mint's is refused as GitHub refuses a bad credential; merging with
// --match-head-commit refuses, in GitHub's words, when the head is another
// commit. It has no api call: CI is the yard's own, and a script that asks
// GitHub for check runs is told so.
const fakeMergeGh = `#!/bin/sh
d=$FAKE_GH
echo "$*" >>"$d/calls"
if [ "$GH_TOKEN" != ` + mintToken + ` ]; then echo "HTTP 401: Bad credentials" >&2; exit 1; fi
case "$1 $2" in
"pr view")
  if [ -e "$d/view-fail" ]; then cat "$d/view-fail" >&2; exit 1; fi
  printf '{"number":7,"state":"%s","headRefOid":"%s","headRepository":{"name":"%s"},"headRepositoryOwner":{"login":"%s"},"author":{"is_bot":%s,"login":"%s"},"title":"%s","body":"%s"}\n' \
    "$(cat "$d/state" 2>/dev/null || echo OPEN)" "$(cat "$d/head")" "$(cat "$d/head-name" 2>/dev/null || echo r)" \
    "$(cat "$d/head-owner" 2>/dev/null || echo o)" "$([ -e "$d/bot" ] && echo true || echo false)" "$([ -e "$d/bot" ] && echo app/dependabot || echo jo)" \
    "$(cat "$d/title" 2>/dev/null || echo bump)" "$(cat "$d/body" 2>/dev/null || echo a body of one line)"
  if [ -e "$d/head-next" ]; then mv "$d/head-next" "$d/head"; fi ;;
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
printf '%s\n' "$*" >"$FAKE_GH/mint-args"
if [ -e "$FAKE_GH/mint-fail" ]; then cat "$FAKE_GH/mint-fail" >&2; exit 1; fi
printf '{"token":"` + mintToken + `","expires_at":"2099-01-01T00:00:00Z"}'
`

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

// mergeOpts is how a case's pull request, and what the bead has recorded of
// it, differ from a bot's bump of its own repository at headA that nobody
// has said yes to and that the yard's CI has passed on.
type mergeOpts struct {
	human     bool   // the author is not a bot
	headOwner string // the owner of the head's repository, o (the pull request's own) unless written
	head      string // the head commit, headA unless written
	yes       string // metadata merge_head, none unless written
	ci        string // metadata ci_head, the pull request's head unless written
	noCI      bool   // the yard's CI has passed on nothing: no ci_head
}

// prURLn is the address of pull request n of o/r.
func prURLn(n int) string { return fmt.Sprintf("https://github.com/o/r/pull/%d", n) }

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// mergeYard is a yard with the contributors pack's pr flow exactly as the pack
// carries it, the stand-ins for gh and github-mint around the exec group
// "pr-merges", and the group's env_file with the App's id and the path of its
// key file. It holds no bead: a test adds the pr beads it runs merge-pr on.
// gh is the stand-in's state directory.
func mergeYard(t *testing.T) (y *yard, gh string) {
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
	scripttest.Write(t, filepath.Join(gh, "bin", "gh"), fakeMergeGh)
	mint := filepath.Join(gh, "bin", "github-mint")
	scripttest.Write(t, mint, fakeGithubMint)
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })

	// The group's environment: the App's id and the path of its key file,
	// which is no secret, in a file for the owner alone, as the runner
	// requires of an env_file. The stand-in mint reads neither.
	y.envFile = filepath.Join(t.TempDir(), "pr-merges.env")
	write(y.envFile, "GITHUB_APP_ID=123456\nGITHUB_APP_PRIVATE_KEY_FILE="+filepath.Join(gh, "app.pem")+"\n", 0o600)
	y.opts = "YARDR_GITHUB_MINT=" + shellQuote(mint+" --from-group")

	sub, err := fs.Sub(packs.FS, packs.Contributors)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Load(sub, packs.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	for _, f := range p.Flows {
		if f.Name == "pr" {
			if _, err := st.PutFlow(context.Background(), f.StoreFlow("contributors.pr"), "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	y.pointFlow("r", "pr", "contributors.pr")
	return y, gh
}

// prBead adds pr bead id in approved, tracking the pull request at url (none
// when it is empty) and carrying merge-ok when label is set.
func (y *yard) prBead(id, url string, label bool) { y.prBeadIn(id, url, "approved", label) }

// prBeadIn is prBead for a bead in stage.
func (y *yard) prBeadIn(id, url, stage string, label bool) {
	y.t.Helper()
	nb := store.NewBead{Title: "bump", ID: id, Depot: "r", Type: "pr", Stage: stage, Priority: store.DefaultPriority}
	if label {
		nb.Labels = []string{"merge-ok"}
	}
	if url != "" {
		nb.Links = []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: url}}
	}
	if _, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).CreateBead(context.Background(), nb, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// standIn sets the stand-in's state for the next run to o and files (a file
// each, with the text in it), and forgets everything of the last run: its
// calls, its answers, its failures.
func standIn(t *testing.T, gh string, o mergeOpts, files map[string]string) {
	t.Helper()
	for _, name := range []string{"calls", "state", "bot", "head-owner", "head-name", "title", "body", "view-fail", "refuse", "race", "head-next", "mint-fail", "mint-stdin", "mint-args"} {
		if err := os.Remove(filepath.Join(gh, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	set := map[string]string{"head": or(o.head, headA)}
	if !o.human {
		set["bot"] = ""
	}
	if o.headOwner != "" {
		set["head-owner"] = o.headOwner
	}
	for name, text := range files {
		set[name] = text
	}
	for name, text := range set {
		if err := os.WriteFile(filepath.Join(gh, name), []byte(text+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// mergePR runs merge-pr on bead id once more: with the stand-in as o and
// files make it, the bead's merge_head as o.yes (none clears it) and its
// ci_head as o says (the head, unless it says otherwise), and the bead unheld
// if the last run held it.
func (y *yard) mergePR(gh, id string, o mergeOpts, files map[string]string) {
	y.t.Helper()
	standIn(y.t, gh, o, files)
	// Over the store API, not the CLI: a process per step is what keeps this
	// package near its time budget, and the yes as a maintainer types it is
	// the one command the merging cases give (TestMergePRMerges).
	ctx, st := context.Background(), rpc.NewClient((&config.Config{Home: y.home}).Socket())
	ci := or(o.ci, or(o.head, headA))
	if o.noCI {
		ci = ""
	}
	if _, err := st.UpdateBead(ctx, id, store.BeadPatch{Metadata: map[string]any{"merge_head": o.yes, "ci_head": ci}}, store.Expect{}, "test"); err != nil {
		y.t.Fatal(err)
	}
	if y.beadOf(id).Hold {
		if _, err := st.SetHold(ctx, id, false, store.Expect{}, "test"); err != nil {
			y.t.Fatal(err)
		}
	}
	y.exec("merge-pr", "pr-merges", id)
}

// refusedMoves are the move_refused events on bead id: moves the yard refused
// to merge-pr's session, each a fault to the crew. A script that tries only
// moves the flow allows leaves none.
func (y *yard) refusedMoves(id string) []string {
	y.t.Helper()
	evs, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).Events(context.Background(), id, 500)
	if err != nil {
		y.t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Kind == "move_refused" {
			out = append(out, fmt.Sprint(e.Data))
		}
	}
	return out
}

// lastNoteOf is the text of the last note on bead id.
func (y *yard) lastNoteOf(id string) string {
	y.t.Helper()
	notes, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).Notes(context.Background(), id)
	if err != nil {
		y.t.Fatal(err)
	}
	if len(notes) == 0 {
		return ""
	}
	return notes[len(notes)-1].Text
}

// held asserts that the run of merge-pr on id for case what held it in
// approved, open, with a last note that has every one of wants; that gh was
// asked to merge asked times (none for a bead held before the merge, one for a
// merge GitHub refused); and that it tried no move the flow refuses: the
// outcomes at approved are merged and dropped, and a script that can do
// neither holds.
func (y *yard) held(gh, id, what string, asked int, wants ...string) {
	y.t.Helper()
	if e := y.beadOf(id); !e.Hold || e.Stage != "approved" || e.Status == "closed" {
		y.t.Fatalf("%s: %s %s/%s hold=%v, want held in approved, not closed:\n%s", what, id, e.Stage, e.Status, e.Hold, y.notesOf(id))
	}
	note := y.lastNoteOf(id)
	for _, want := range wants {
		if !strings.Contains(note, want) {
			y.t.Errorf("%s: no note with %q:\n%s", what, want, note)
		}
	}
	if n := countCalls(y.t, gh, "pr merge"); n != asked {
		y.t.Errorf("%s: gh pr merge called %d times, want %d:\n%v", what, n, asked, ghCalls(y.t, gh))
	}
	if got := y.refusedMoves(id); len(got) != 0 {
		y.t.Errorf("%s: merge-pr tried moves the flow refuses: %v", what, got)
	}
}

// mergedAs asserts that the run for case what ended id in stage want (merged,
// or closed for a dropped pull request), closed and not held, and that gh was
// asked for exactly the merges given, once each.
func (y *yard) mergedAs(gh, id, what, want string, merges ...string) {
	y.t.Helper()
	if e := y.beadOf(id); e.Stage != want || e.Status != "closed" || e.Hold {
		y.t.Fatalf("%s: %s %s/%s hold=%v, want %s and closed:\n%s", what, id, e.Stage, e.Status, e.Hold, want, y.notesOf(id))
	}
	if n := countCalls(y.t, gh, "pr merge"); n != len(merges) {
		y.t.Errorf("%s: gh pr merge called %d times, want %d:\n%v", what, n, len(merges), ghCalls(y.t, gh))
	}
	for _, m := range merges {
		if n := countCalls(y.t, gh, m); n != 1 {
			y.t.Errorf("%s: %q called %d times, want once:\n%v", what, m, n, ghCalls(y.t, gh))
		}
	}
	if got := y.refusedMoves(id); len(got) != 0 {
		y.t.Errorf("%s: merge-pr tried moves the flow refuses: %v", what, got)
	}
}

// What merge-pr merges, and how. A bot's bump of its own repository is
// merged at the head it has now, also when a maintainer's yes recorded
// another (an Actions bump they said yes to, which the bot rebased since),
// with a rebase and deleting its branch, as the App with a token minted for
// that repository alone. Everything else is merged at the head a maintainer
// said yes to, with the yes as the maintainers give it, the label and the sha
// in one command: a fork with a merge commit and its branch left alone, being
// its author's; a person's branch of the repository, and a bot's pull request
// from a fork, which is no bot's of the repository, as that one is. A pull
// request merged already is reported merged and one closed unmerged dropped,
// with no merge asked for. The token is in the environment of gh and nowhere
// else: not in an argument, the log or a note, and the depot's checkout is
// never touched.
func TestMergePRMerges(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	for _, c := range []struct {
		what   string
		o      mergeOpts
		said   bool              // a maintainer says yes to the head, label and sha, before the run
		files  map[string]string // the stand-in, besides o
		want   string            // the stage it ends in
		flags  string            // what the merge is asked with, none when no merge is
		note   string
		source bool   // the mint is the github source's own, not the group's cmd
		land   string // YARDR_LAND in the group's cmd, none when empty
	}{
		{"a bot's bump of its own repository", mergeOpts{head: headB, yes: headA}, false, nil, "merged",
			"--rebase --delete-branch --match-head-commit " + headB, "merged %s at " + headB + " (rebase)", false, ""},
		{"a human's fork", mergeOpts{human: true, headOwner: "mallory", yes: headA}, true, nil, "merged",
			"--merge --match-head-commit " + headA, "merged %s at " + headA + " (merge commit)", false, ""},
		{"a person's branch of the repository", mergeOpts{human: true, yes: headA}, true, nil, "merged",
			"--rebase --delete-branch --match-head-commit " + headA, "merged %s at " + headA + " (rebase)", false, ""},
		{"a bot's fork", mergeOpts{headOwner: "mallory", yes: headA}, true, nil, "merged",
			"--merge --match-head-commit " + headA, "merged %s at " + headA + " (merge commit)", false, ""},
		{"a pull request merged already", mergeOpts{}, false, map[string]string{"state": "MERGED"}, "merged", "", "%s is merged already", false, ""},
		{"a pull request closed unmerged", mergeOpts{}, false, map[string]string{"state": "CLOSED"}, "closed", "", "%s is closed, unmerged", false, ""},
		{"a bot's bump, squashed", mergeOpts{head: headB}, false, map[string]string{"title": "chore(deps): bump x (r-m)", "body": "Bump x.  Yardr-Bead: r-m"}, "merged",
			"--squash --delete-branch --match-head-commit " + headB + " --subject chore(deps): bump x (r-m) --body Bump x.  Yardr-Bead: r-m", "merged %s at " + headB + " (squash)", false, "squash"},
		{"a mint that is the github source's own", mergeOpts{}, false, nil, "merged",
			"--rebase --delete-branch --match-head-commit " + headA, "merged %s at " + headA + " (rebase)", true, ""},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := mergeYard(t)
			depot := y.git(y.repo, "status", "--porcelain") + y.git(y.repo, "rev-parse", "HEAD")
			id, url := "r-m", prURLn(10)
			y.prBead(id, url, !c.said)
			if c.land != "" {
				y.opts += " YARDR_LAND=" + c.land
			}
			if c.said {
				y.yardr("bead", "update", id, "--add-label", "merge-ok", "--meta", "merge_head="+c.o.yes)
			}
			if c.source {
				decl := filepath.Join(t.TempDir(), "github.yaml")
				if err := os.WriteFile(decl, []byte(githubDecl), 0o644); err != nil {
					t.Fatal(err)
				}
				y.yardr("source", "add", decl)
				y.yardr("source", "set", "github", "mint_command="+filepath.Join(gh, "bin", "github-mint")+" --from-source")
				y.opts = ""
			}

			y.mergePR(gh, id, c.o, c.files)

			var merges []string
			if c.flags != "" {
				merges = []string{"pr merge " + url + " " + c.flags}
			}
			y.mergedAs(gh, id, c.what, c.want, merges...)
			if note := y.lastNoteOf(id); !strings.Contains(note, fmt.Sprintf(c.note, url)) {
				t.Errorf("%s: no note with %q:\n%s", c.what, fmt.Sprintf(c.note, url), note)
			}
			// Every run that asked GitHub asked the mint, for the repository of
			// the pull request and from the id and key file the group's env_file
			// gave, and asked GitHub nothing of CI, which is the yard's own: no
			// call of the api, where the check runs are.
			if c.flags != "" {
				var asked struct {
					Inputs map[string]string `json:"inputs"`
					Scope  string            `json:"scope"`
				}
				if err := json.Unmarshal([]byte(readFile(t, filepath.Join(gh, "mint-stdin"))), &asked); err != nil {
					t.Fatalf("%s: %v", c.what, err)
				}
				if asked.Scope != "o/r" || asked.Inputs["GITHUB_APP_ID"] != "123456" || asked.Inputs["GITHUB_APP_PRIVATE_KEY_FILE"] != filepath.Join(gh, "app.pem") {
					t.Errorf("%s: the mint was asked %+v, want scope o/r and the env_file's id and key file", c.what, asked)
				}
				wantArgs := "--from-group\n"
				if c.source {
					wantArgs = "--from-source\n"
				}
				if got := readFile(t, filepath.Join(gh, "mint-args")); got != wantArgs {
					t.Errorf("%s: mint arguments = %q, want %q", c.what, got, wantArgs)
				}
				if n := countCalls(t, gh, "api "); n != 0 {
					t.Errorf("%s: GitHub was asked for CI, which is the yard's own, %d times:\n%v", c.what, n, ghCalls(t, gh))
				}
			}
			for what, text := range map[string]string{"the log": y.log, "the notes": y.notesOf(id), "gh's calls": readFile(t, filepath.Join(gh, "calls"))} {
				if strings.Contains(text, mintToken) {
					t.Errorf("%s hold the token", what)
				}
			}
			if after := y.git(y.repo, "status", "--porcelain") + y.git(y.repo, "rev-parse", "HEAD"); after != depot {
				t.Errorf("the depot's checkout changed:\n%s\n%s", depot, after)
			}
		})
	}
}

// A pull request that is not a bot's bump of its own repository is held
// before anything is asked of CI or merged when its bead has no yes, or one
// that cannot be taken: the note names the command that says it. A fork that
// was pushed to after the yes is held too, naming both commits, and merged
// only after they say yes again, to the head they looked at then.
func TestMergePRHoldsForTheYes(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	yes := "yardr bead update r-p --add-label merge-ok --meta merge_head="
	for _, c := range []struct {
		what  string
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
		t.Run(c.what, func(t *testing.T) {
			y, gh := mergeYard(t)
			y.prBead("r-p", prURL, true)
			y.mergePR(gh, "r-p", c.o, nil)

			y.held(gh, "r-p", c.what, 0, c.wants...)
			if n := countCalls(t, gh, "api "); n != 0 {
				t.Errorf("%s: GitHub was asked about a pull request waiting for a yes:\n%v", c.what, ghCalls(t, gh))
			}
		})
	}

	// They look at the new head and say yes to it: the pull request is
	// merged at that head.
	t.Run("saying yes to the new head merges it", func(t *testing.T) {
		y, gh := mergeYard(t)
		y.prBead("r-p", prURL, true)
		y.mergePR(gh, "r-p", mergeOpts{human: true, headOwner: "mallory", head: headB, yes: headA}, nil)
		y.held(gh, "r-p", "a fork pushed to after the yes", 0, "moved since your yes (yes at 4f2c9e1, head now 9b8a7c6)")
		y.yardr("bead", "update", "r-p", "--add-label", "merge-ok", "--meta", "merge_head="+headB)
		y.mergePR(gh, "r-p", mergeOpts{human: true, headOwner: "mallory", head: headB, yes: headB}, nil)
		y.mergedAs(gh, "r-p", "the new yes", "merged", "pr merge "+prURL+" --merge --match-head-commit "+headB)
	})
}

// The yard's CI must have passed on the commit that is merged: the bead's
// ci_head is that commit, whole, and nothing of GitHub is asked for it. No
// ci_head, one of another commit (the pull request was pushed to since the run,
// for a bot's too), one that is not a full sha and a head that is another
// commit than the yes's all hold the bead before anything is merged, and the
// note says what CI passed on and how to have it run again, on a head a
// reviewer read. The commit is the same in any case of its letters.
func TestMergePRHoldsUnlessTheYardsCIPassed(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	again := "yardr bead advance r-p --to ciwait, then yardr bead unhold r-p"
	for _, c := range []struct {
		what  string
		o     mergeOpts
		wants []string
	}{
		{"no ci_head", mergeOpts{noCI: true},
			[]string{"CI has not passed on 4f2c9e1 (yard CI passed nothing)", "pr-ci", "only on a head a reviewer read", again}},
		{"a ci_head of another commit", mergeOpts{ci: headB},
			[]string{"CI has not passed on 4f2c9e1 (yard CI passed 9b8a7c6)", again}},
		{"a ci_head that is no full sha", mergeOpts{ci: headA[:7]},
			[]string{"CI has not passed on 4f2c9e1 (yard CI passed nothing)"}},
		{"a bot's pull request pushed to after the run", mergeOpts{head: headB, ci: headA},
			[]string{"CI has not passed on 9b8a7c6 (yard CI passed 4f2c9e1)", again}},
		{"a fork pushed to after the run, and the yes said to the new head", mergeOpts{human: true, headOwner: "mallory", head: headB, yes: headB, ci: headA},
			[]string{"CI has not passed on 9b8a7c6 (yard CI passed 4f2c9e1)", again}},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := mergeYard(t)
			y.prBead("r-p", prURL, true)
			y.mergePR(gh, "r-p", c.o, nil)

			y.held(gh, "r-p", c.what, 0, c.wants...)
			if n := countCalls(t, gh, "api "); n != 0 {
				t.Errorf("%s: GitHub was asked for CI, which is the yard's own:\n%v", c.what, ghCalls(t, gh))
			}
		})
	}

	// The commit it passed on, in capitals: the same commit.
	t.Run("a ci_head in capitals", func(t *testing.T) {
		y, gh := mergeYard(t)
		y.prBead("r-p", prURL, true)
		y.mergePR(gh, "r-p", mergeOpts{ci: strings.ToUpper(headA)}, nil)
		y.mergedAs(gh, "r-p", "a ci_head in capitals", "merged", "pr merge "+prURL+" --rebase --delete-branch --match-head-commit "+headA)
	})
}

// What GitHub refuses holds the bead with its words, and a refusal of the App
// itself names the permissions it lacks. A head that moves between the look
// and the merge is refused by --match-head-commit and held as moved, not merged
// and not sent anywhere. Without a token nothing is asked of GitHub: the App's
// id and key file missing from the group's environment, a mint that fails or
// none to mint with hold the bead with the reason, which is the mint's own
// words and never a token.
func TestMergePRHoldsWhatGitHubRefuses(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	for _, c := range []struct {
		what   string
		files  map[string]string
		asked  int // merges asked of GitHub
		wants  []string
		tokens bool          // the case has no token: GitHub is not asked at all
		change func(y *yard) // changes the group for this run
	}{
		{what: "the App without Contents", files: map[string]string{"refuse": "HTTP 403: Resource not accessible by integration (https://api.github.com/repos/o/r/pulls/7/merge)"}, asked: 1,
			wants: []string{"GitHub refused the yard's App on o/r", "Contents and Workflows read and write", "Resource not accessible by integration"}},
		{what: "a pull request it will not merge", files: map[string]string{"refuse": "X Pull request o/r#7 is not mergeable: the base branch policy prohibits the merge."}, asked: 1,
			wants: []string{"GitHub did not merge " + prURL, "base branch policy prohibits the merge", "unhold r-p"}},
		{what: "a head that moved as the merge started", files: map[string]string{"race": headB}, asked: 1,
			wants: []string{"moved while it was being merged (looked at 4f2c9e1, head now 9b8a7c6)"}},
		{what: "a head that is no commit sha", files: map[string]string{"head": "../../etc"},
			wants: []string{"gave a head for " + prURL + " that is no commit sha"}},
		{what: "a pull request it cannot read", files: map[string]string{"view-fail": "HTTP 404: Not Found"},
			wants: []string{"reading pull request " + prURL + " failed: HTTP 404: Not Found"}},
		{what: "a mint that fails", files: map[string]string{"mint-fail": "PRIVATE_KEY_VALUE_SHOULD_NOT_ESCAPE"}, tokens: true,
			wants: []string{"minting a token for o/r failed", "the mint's output is not copied to the bead"}},
		{what: "no env_file", tokens: true,
			wants: []string{"has no GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE", "env_file"},
			// Whatever the machine running the test exports is not the group's.
			change: func(y *yard) { y.envFile, y.opts = "", y.opts+" GITHUB_APP_ID= GITHUB_APP_PRIVATE_KEY_FILE=" }},
		{what: "a YARDR_LAND that is neither mode", tokens: true,
			wants:  []string{"YARDR_LAND is rebas in this group's cmd", "rebase or by squash"},
			change: func(y *yard) { y.opts += " YARDR_LAND=rebas" }},
		{what: "no mint anywhere", tokens: true,
			wants:  []string{"the github source has no mint_command", "YARDR_GITHUB_MINT", "yardr source set github mint_command="},
			change: func(y *yard) { y.opts = "" }},
	} {
		t.Run(c.what, func(t *testing.T) {
			y, gh := mergeYard(t)
			y.prBead("r-p", prURL, true)
			envFile, opts := y.envFile, y.opts
			if c.change != nil {
				c.change(y)
			}
			y.mergePR(gh, "r-p", mergeOpts{}, c.files)
			y.envFile, y.opts = envFile, opts

			y.held(gh, "r-p", c.what, c.asked, c.wants...)
			if c.what == "a mint that fails" {
				for where, text := range map[string]string{"log": y.log, "notes": y.notesOf("r-p")} {
					if strings.Contains(text, "PRIVATE_KEY_VALUE_SHOULD_NOT_ESCAPE") {
						t.Errorf("the mint's stderr reached the %s", where)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(gh, "calls")); c.tokens && err == nil {
				t.Errorf("%s: GitHub was asked with no token:\n%v", c.what, ghCalls(t, gh))
			}
			if c.asked == 1 {
				// The merge is asked for once, with the commit looked at.
				if want := "pr merge " + prURL + " --rebase --delete-branch --match-head-commit " + headA; countCalls(t, gh, want) != 1 {
					t.Errorf("%s: %q not called once:\n%v", c.what, want, ghCalls(t, gh))
				}
			}
		})
	}
}

// The pull request is the one the bead tracks, and only when the bead says
// the yard may merge it. A bead without merge-ok, one tracking nothing, and
// one tracking something that is not a pull request address are held, and
// nothing is asked of GitHub.
func TestMergePRRefusesWhatItWasNotGiven(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	for _, c := range []struct {
		id, url string
		label   bool
		wants   []string
	}{
		{"r-nolabel", prURLn(1), false, []string{"does not carry merge-ok", "yardr bead update r-nolabel --add-label merge-ok --meta merge_head="}},
		{"r-nolink", "", true, []string{"tracks no GitHub pull request"}},
		{"r-issue", "https://github.com/o/r/issues/7", true, []string{"which is not a pull request address"}},
		{"r-files", "https://github.com/o/r/pull/7/files", true, []string{"which is not a pull request address"}},
	} {
		t.Run(c.id, func(t *testing.T) {
			y, gh := mergeYard(t)
			standIn(t, gh, mergeOpts{}, nil)
			y.prBead(c.id, c.url, c.label)

			y.exec("merge-pr", "pr-merges", c.id)

			y.held(gh, c.id, c.id, 0, c.wants...)
			if _, err := os.Stat(filepath.Join(gh, "calls")); err == nil {
				t.Errorf("%s: GitHub was asked for a bead the yard may not merge:\n%v", c.id, ghCalls(t, gh))
			}
		})
	}
}

// merge-pr never reports failed: the outcomes at approved are merged and
// dropped, and a pull request it cannot merge is held for a maintainer, not
// sent back to be built again.
func TestMergePRNeverReportsFailed(t *testing.T) {
	src := readFile(t, "merge-pr")
	for _, bad := range []string{"--outcome failed", "report failed", "report conflict"} {
		if strings.Contains(src, bad) {
			t.Errorf("merge-pr has %q", bad)
		}
	}
}
