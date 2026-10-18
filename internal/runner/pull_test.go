package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// pullHerdr is a fake herdr that keeps worktrees as herdr does: create makes
// one on a new branch from --base and refuses a branch that exists, open
// finds one by the branch checked out in it, or takes it by --path. A
// detached worktree is on no branch, so open --branch does not find it.
func pullHerdr(t *testing.T, log string) string {
	return fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create"|"worktree open")
    verb=$2
    shift 2
    while [ $# -gt 1 ]; do
      case "$1" in --cwd) cwd=$2 ;; --branch) br=$2 ;; --base) base=$2 ;; --path) p=$2 ;; esac
      shift 2
    done
    if [ "$verb" = create ]; then
      git -C "$cwd" worktree add -q -b "$br" "$p" "$base" 2>/dev/null ||
        { echo '{"error":{"code":"worktree_exists","message":"branch exists"}}' >&2; exit 1; }
    elif [ -z "$p" ]; then
      p=$(git -C "$cwd" worktree list --porcelain |
        awk -v b="refs/heads/$br" '/^worktree /{w=substr($0,10)} $1=="branch" && $2==b {print w}')
      [ -n "$p" ] || { echo '{"error":{"code":"worktree_not_found","message":"no worktree on the branch"}}' >&2; exit 1; }
    elif [ ! -d "$p" ]; then
      echo '{"error":{"code":"worktree_not_found","message":"no worktree at the path"}}' >&2; exit 1
    fi
    echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"'"$p"'"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
}

// A bead that tracks a pull request is worked on the pull request's head:
// the runner fetches refs/pull/N/head from the depot's origin and leaves the
// worktree on it, detached, with yard/<bead> where the base is and the brief
// saying which commit it is. A later session fetches again, so it stands on
// what was pushed meanwhile; a pull request whose ref is gone fails the
// start with git's error and starts no agent. A bead that tracks none is on
// its branch as ever.
func TestHerdrPullRequestHeadInTheWorktree(t *testing.T) {
	ctx := context.Background()
	// The origin, bare, with main and a pull request's head that no clone
	// fetches by default.
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", "main")
	gitT(t, src, "commit", "-q", "--allow-empty", "-m", "A")
	gitT(t, src, "checkout", "-q", "-b", "theirs")
	gitT(t, src, "commit", "-q", "--allow-empty", "-m", "P1")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitT(t, src, "init", "-q", "--bare", "-b", "main", origin)
	gitT(t, src, "push", "-q", origin, "main", "theirs:refs/pull/1/head")
	repo := filepath.Join(t.TempDir(), "repo")
	gitT(t, src, "clone", "-q", origin, repo)
	a := gitOutT(t, repo, "rev-parse", "main")
	p1 := gitOutT(t, src, "rev-parse", "theirs")

	const url = "https://github.com/acme/web/pull/1"
	depot := store.Depot{Name: "r", Path: repo, Base: "main"}
	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: pullHerdr(t, log), WorktreeRoot: t.TempDir()}
	job := func(id, stage string, pull *dispatch.Pull) dispatch.Job {
		return dispatch.Job{Bead: store.Bead{ID: id, Stage: stage}, Group: store.Group{Name: "reviewers"},
			Depot: &depot, Session: store.Session{ID: "s-" + id + "-" + stage}, Pull: pull}
	}
	wt := filepath.Join(h.WorktreeRoot, "r", "r-1")
	// stands says the worktree is on commit, on no branch, and that the
	// bead's branch did not move from the base.
	stands := func(commit string) {
		t.Helper()
		if got := gitOutT(t, wt, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("worktree at %s, want the pull request's head %s", got, commit)
		}
		if br, err := gitOut(ctx, wt, "symbolic-ref", "-q", "HEAD"); err == nil {
			t.Fatalf("worktree is on %s, want it detached", br)
		}
		if got := gitOutT(t, repo, "rev-parse", "yard/r-1"); got != a {
			t.Fatalf("yard/r-1 at %s, want it left at the base %s", got, a)
		}
	}

	pull := &dispatch.Pull{URL: url, Number: 1}
	handle, err := h.Start(ctx, job("r-1", "review", pull))
	if err != nil {
		t.Fatal(err)
	}
	stands(p1)
	if pull.Head != "" {
		t.Fatalf("the dispatcher's job was written to: %+v", pull)
	}
	// Worked compares with the head the session started on: the pull
	// request's, not the base's.
	if handle["head"] != p1 || handle["pull_head"] != p1 || handle["pull"] != url {
		t.Fatalf("handle = %v, want head and pull_head %s", handle, p1)
	}
	if worked, err := h.Worked(ctx, handle); err != nil || worked {
		t.Fatalf("Worked = %v, %v on a session that did nothing", worked, err)
	}
	brief := string(mustRead(t, handle["brief"].(string)))
	if want := "it is the head of the pull request " + url + " at `" + p1 + "`, detached"; !strings.Contains(brief, want) {
		t.Fatalf("brief lacks %q:\n%s", want, brief)
	}
	if strings.Contains(brief, "Commit your work there") {
		t.Fatalf("brief still tells the session to commit on the bead's branch:\n%s", brief)
	}

	// The pull request is pushed to and the bead comes back: the next
	// session's worktree is the same one, moved to the new head.
	gitT(t, src, "commit", "-q", "--allow-empty", "-m", "P2")
	gitT(t, src, "push", "-q", origin, "theirs:refs/pull/1/head")
	p2 := gitOutT(t, src, "rev-parse", "theirs")
	os.Remove(log)
	if handle, err = h.Start(ctx, job("r-1", "review", pull)); err != nil {
		t.Fatal(err)
	}
	stands(p2)
	if handle["pull_head"] != p2 {
		t.Fatalf("handle = %v, want pull_head %s", handle, p2)
	}
	if calls := string(mustRead(t, log)); !strings.Contains(calls, "worktree open --cwd "+repo+" --path "+wt) {
		t.Fatalf("the detached worktree was not reopened by its path:\n%s", calls)
	}

	// Its worktree cleaned up meanwhile: made again from the branch, then
	// put on the head as the first time.
	gitT(t, repo, "worktree", "remove", wt)
	if _, err = h.Start(ctx, job("r-1", "review", pull)); err != nil {
		t.Fatal(err)
	}
	stands(p2)

	// The pull request's ref is gone: the start fails with what git said,
	// its handle kept for the teardown, and no agent is started on what the
	// worktree held before.
	gitT(t, origin, "update-ref", "-d", "refs/pull/1/head")
	os.Remove(log)
	handle, err = h.Start(ctx, job("r-1", "review", pull))
	if err == nil || !strings.Contains(err.Error(), url) || !strings.Contains(err.Error(), "refs/pull/1/head") {
		t.Fatalf("start on a pull request with no ref: %v", err)
	}
	if dispatch.IsRuntime(err) || dispatch.IsBlocked(err) {
		t.Fatalf("the failure is the bead's, not the group's: %v", err)
	}
	if handle["worktree"] != wt {
		t.Fatalf("handle = %v, want the worktree kept on it", handle)
	}
	if calls := string(mustRead(t, log)); strings.Contains(calls, "agent start") {
		t.Fatalf("an agent was started without the pull request's head:\n%s", calls)
	}

	// A bead that tracks no pull request: on its branch, at the base.
	handle, err = h.Start(ctx, job("r-2", "new", nil))
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(h.WorktreeRoot, "r", "r-2")
	if br := gitOutT(t, plain, "symbolic-ref", "--short", "HEAD"); br != "yard/r-2" || gitOutT(t, plain, "rev-parse", "HEAD") != a {
		t.Fatalf("plain bead's worktree on %s, want yard/r-2 at the base", br)
	}
	if _, has := handle["pull_head"]; has {
		t.Fatalf("plain bead's handle names a pull request: %v", handle)
	}
	if brief := string(mustRead(t, handle["brief"].(string))); !strings.Contains(brief, "(branch `yard/r-2`). Commit your work there.") {
		t.Fatalf("plain bead's brief changed:\n%s", brief)
	}
}
