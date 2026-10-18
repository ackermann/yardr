package assembly

// These tests cover what merge-local does with YARDR_PUSH before it
// rebases: it fetches the remote and fast-forwards the local base to the
// remote's branch, so the bead is tested against and lands on the remote's
// commits. A diverged base fails the landing; anything less (a fetch that
// fails, a remote with no such branch, a base that is simply not pushed
// yet) lands on the base as it is.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushYard is newYard with a bare origin that has main, pushed from the
// depot, and YARDR_PUSH naming it.
func pushYard(t *testing.T) (*yard, string) {
	t.Helper()
	y := newYard(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
	y.push = "origin"
	return y, remote
}

// pushRemote commits file in a fresh clone of remote and pushes it, as a
// commit that reached the remote without the yard would.
func pushRemote(t *testing.T, y *yard, remote, file, content string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	y.git(".", "clone", "-q", remote, other)
	y.commit(other, file, content)
	y.git(other, "push", "-q", "origin", "main")
	return y.git(other, "rev-parse", "main")
}

// When the remote's branch moved, the landing brings the base up to it
// before the rebase, and the bead lands on the remote's commits.
func TestMergeLocalBringsBaseUpToPush(t *testing.T) {
	y, remote := pushYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	pushed := pushRemote(t, y, remote, "m.txt", "laptop\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	main := y.git(y.repo, "rev-parse", "main")
	if tip := y.git(y.wt, "rev-parse", "HEAD"); tip != main {
		t.Fatalf("main %s is not the rebased branch %s", main, tip)
	}
	// The rebase stood on the fresh base: the remote's commit is in.
	y.git(y.repo, "merge-base", "--is-ancestor", pushed, "main")
	for _, f := range []string{"b.txt", "m.txt"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("checkout of main lacks %s: %v", f, err)
		}
	}
	if got := y.git(remote, "rev-parse", "main"); got != main {
		t.Fatalf("origin main %s, want %s", got, main)
	}
	if !strings.Contains(y.log, "bringing main up to origin/main") {
		t.Fatalf("the log does not say the base was brought up:\n%s", y.log)
	}
}

// The same with the base checked out nowhere: the ref alone moves.
func TestMergeLocalBringsUpABaseThatIsCheckedOutNowhere(t *testing.T) {
	y, remote := pushYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	pushed := pushRemote(t, y, remote, "m.txt", "laptop\n")
	y.git(y.repo, "checkout", "-q", "--detach")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	y.git(y.repo, "merge-base", "--is-ancestor", pushed, "main")
	if !strings.Contains(y.log, "bringing main up to origin/main") {
		t.Fatalf("the log does not say the base was brought up:\n%s", y.log)
	}
}

// A base that diverged from the remote's branch fails the landing, with a
// note saying so: joining the two is a person's, never merged or forced
// here. Nothing is rebased or landed.
func TestMergeLocalDivergedBaseFails(t *testing.T) {
	y, remote := pushYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	y.commit(y.repo, "c.txt", "local\n")
	local := y.git(y.repo, "rev-parse", "main")
	branch := y.git(y.repo, "rev-parse", "yard/r-1")
	pushed := pushRemote(t, y, remote, "m.txt", "laptop\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "new" || b.Status != "open" {
		t.Fatalf("bead %s/%s, want it failed back to new", b.Stage, b.Status)
	}
	notes := y.notes()
	for _, want := range []string{"diverged from origin/main", "Join them by hand"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != local {
		t.Fatalf("main moved to %s, want it left at %s", got, local)
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-1"); got != branch {
		t.Fatalf("the branch moved to %s, want it left at %s", got, branch)
	}
	if got := y.git(remote, "rev-parse", "main"); got != pushed {
		t.Fatalf("origin main moved to %s", got)
	}
}

// A base holding commits the remote lacks is simply not pushed yet, from a
// landing whose push failed: the bead lands, and the push carries them.
func TestMergeLocalUnpushedBaseStillLands(t *testing.T) {
	y, remote := pushYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	y.commit(y.repo, "c.txt", "landed but not pushed\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	main := y.git(y.repo, "rev-parse", "main")
	if got := y.git(remote, "rev-parse", "main"); got != main {
		t.Fatalf("origin main %s, want the landed %s", got, main)
	}
	if !strings.Contains(y.log, "holds commits origin lacks; the push below carries them") {
		t.Fatalf("the log does not say the base was ahead:\n%s", y.log)
	}
}

// A fetch that fails lands on the base as it is: the bead still merges,
// and the push fails as it would have.
func TestMergeLocalFailedFetchLandsOnBaseAsIs(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.push = "missing-remote"
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if !strings.Contains(y.log, "could not fetch missing-remote, landing on main as it is") {
		t.Fatalf("the log does not say the fetch failed:\n%s", y.log)
	}
}
