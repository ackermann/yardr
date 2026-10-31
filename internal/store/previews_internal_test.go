package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func previewYard(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(context.Background(), OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func TestPreviewProposeApproveForget(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	p, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "serve .", Dir: "/tmp/app", Port: 8080}, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if p.State != PreviewPending || p.Approval != nil {
		t.Fatalf("proposal = %+v", p)
	}
	if _, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "x", Dir: "/tmp", Port: 1}, "builder"); err == nil ||
		!strings.Contains(err.Error(), "already in the yard") {
		t.Fatalf("duplicate proposal err = %v", err)
	}
	for _, bad := range []NewPreview{
		{App: "Bad_Name", Command: "x", Dir: "/tmp", Port: 1},
		{App: "ok", Command: "", Dir: "/tmp", Port: 1},
		{App: "ok", Command: "x", Dir: "/tmp", Port: 0},
		{App: "-lead", Command: "x", Dir: "/tmp", Port: 1},
		{App: "double--dash", Command: "x", Dir: "/tmp", Port: 1},
	} {
		if _, err := s.ProposePreview(ctx, bad, "builder"); err == nil {
			t.Fatalf("proposal %+v accepted", bad)
		}
	}
	if _, err := s.ProposePreview(ctx, NewPreview{App: "anonymous", Command: "x", Dir: "/tmp", Port: 1}, ""); err == nil || !strings.Contains(err.Error(), "proposer is required") {
		t.Fatalf("empty proposer err = %v", err)
	}
	if _, err := s.ApprovePreview(ctx, "demo", "builder"); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("builder approval err = %v, want operator-only refusal", err)
	}
	got, err := s.ApprovePreview(ctx, "demo", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PreviewApproved || got.Approval == nil || got.Approval.By != "ada" || got.Approval.Command != "serve ." {
		t.Fatalf("approved = %+v", got)
	}
	if _, err := s.UpdatePreview(ctx, "demo", PreviewUpdate{
		ExpectedState: PreviewStopped,
		State:         PreviewRunning,
	}); err == nil {
		t.Fatal("running update with a stale expected state succeeded")
	}
	if _, err := s.PreviewStop(ctx, "demo", "another-builder"); err == nil || !strings.Contains(err.Error(), "only builder") {
		t.Fatalf("another builder's stop err = %v, want proposer-only refusal", err)
	}
	if got, _ := s.GetPreview(ctx, "demo"); got.State != PreviewApproved {
		t.Fatalf("after refused stop = %+v", got)
	}
	if _, err := s.PreviewStop(ctx, "demo", "ada"); err != nil {
		t.Fatalf("operator stop: %v", err)
	}
	if _, err := s.PreviewStop(ctx, "demo", "builder"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPreview(ctx, "demo"); got.State != PreviewStopped {
		t.Fatalf("after stop = %+v", got)
	}
	if _, err := s.ForgetPreview(ctx, "demo", "builder"); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("builder forget err = %v, want operator-only refusal", err)
	}
	if _, err := s.ForgetPreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPreview(ctx, "demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after forget err = %v", err)
	}
	// The name is free again once forgotten.
	if _, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "x", Dir: "/tmp", Port: 2}, "builder"); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewLimitAndIdle(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	for _, app := range []string{"one", "two", "three"} {
		if _, err := s.ProposePreview(ctx, NewPreview{App: app, Command: "x", Dir: "/tmp", Port: 8000}, "builder"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ProposePreview(ctx, NewPreview{App: "four", Command: "x", Dir: "/tmp", Port: 8000}, "builder"); err == nil ||
		!strings.Contains(err.Error(), "the most it may") {
		t.Fatalf("fourth proposal err = %v", err)
	}
	if got := PreviewIdle(map[string]string{}); got != 4*time.Hour {
		t.Fatalf("default idle = %v", got)
	}
	if got := PreviewIdle(map[string]string{PreviewIdleSetting: "30m"}); got != 30*time.Minute {
		t.Fatalf("set idle = %v", got)
	}
	if got := PreviewIdle(map[string]string{PreviewIdleSetting: "junk"}); got != 4*time.Hour {
		t.Fatalf("bad idle = %v", got)
	}
	setting, ok := SettingOf(PreviewIdleSetting)
	if !ok {
		t.Fatal("preview idle setting is not registered")
	}
	if err := setting.Check("0s"); err == nil {
		t.Fatal("zero preview idle duration was accepted")
	}
	if n := len(mustPreviews(t, s)); n != 3 {
		t.Fatalf("previews = %d", n)
	}
}

// A refused proposal stays listed as refused, with its proposer and no
// approval, so nothing starts; refusal is the operator's, and only a
// pending proposal is refused. Forgetting the refused row frees the name
// for a later identical proposal, which is a new card.
func TestPreviewRefuse(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	if _, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "serve .", Dir: "/tmp/app", Port: 8080}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefusePreview(ctx, "demo", "builder"); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("builder refuse err = %v, want operator-only refusal", err)
	}
	if _, err := s.RefusePreview(ctx, "missing", "ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing refuse err = %v, want not found", err)
	}
	got, err := s.RefusePreview(ctx, "demo", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PreviewRefused || got.Approval != nil || got.Proposer != "builder" || got.Command != "serve ." {
		t.Fatalf("refused = %+v", got)
	}
	if _, err := s.RefusePreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "has no proposal to refuse") {
		t.Fatalf("second refuse err = %v", err)
	}
	if _, err := s.ApprovePreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "has no proposal to approve") {
		t.Fatalf("refused approval err = %v", err)
	}
	if _, err := s.ForgetPreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	again, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "serve .", Dir: "/tmp/app", Port: 8080}, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != PreviewPending {
		t.Fatalf("reproposal = %+v, want a pending card", again)
	}
}

// previewRepo is a directory with one commit, for a preview to read its
// depot tree from; the second result runs git in it.
func previewRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "user.email", "a@b.c")
	run("config", "user.name", "a")
	run("commit", "-q", "--allow-empty", "-m", "one")
	return dir, run
}

// A restart asks for the approved form to stop and start again: the row
// is left stopped for the supervisor. Restarting is the operator's, a
// proposal with no approval has no form to restart, and the form
// restarts only on the depot tree its approval was given on. Once the
// depot HEAD has moved the ask is refused, the approval is taken back
// and the card, pending again at the new commit, waits for a new one.
// PreviewHead reads that HEAD without touching the row.
func TestPreviewRestartOnTheApprovedTree(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	dir, git := previewRepo(t)
	first := git("rev-parse", "HEAD")
	if _, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "x", Dir: dir, Port: 8080}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestartPreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "no approved form") {
		t.Fatalf("pending restart err = %v", err)
	}
	if _, err := s.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestartPreview(ctx, "demo", "builder"); err == nil || !strings.Contains(err.Error(), "only the operator") {
		t.Fatalf("builder restart err = %v, want operator-only refusal", err)
	}
	got, err := s.RestartPreview(ctx, "demo", "ada")
	if err != nil {
		t.Fatalf("restart on the tree it was approved on: %v", err)
	}
	if got.State != PreviewStopped || got.StateDetail != PreviewRestartDetail || got.Commit != first {
		t.Fatalf("restart ask = %+v, want stopped for the supervisor at %q", got, first)
	}
	if got.Approval == nil || got.Approval.Command != "x" || got.Approval.Commit != first {
		t.Fatalf("restart ask approval = %+v, want the approved form kept", got.Approval)
	}
	git("commit", "-q", "--allow-empty", "-m", "two")
	second := git("rev-parse", "HEAD")
	if second == first {
		t.Fatal("the depot HEAD did not move")
	}
	if head, err := s.PreviewHead(ctx, "demo"); err != nil || head != second {
		t.Fatalf("head = %q, %v, want %q", head, err, second)
	}
	if got, _ := s.GetPreview(ctx, "demo"); got.Commit != first {
		t.Fatalf("after head read commit = %q, want the row untouched at %q", got.Commit, first)
	}
	if _, err := s.RestartPreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "approval is taken back") {
		t.Fatalf("restart on another tree err = %v, want the approval taken back", err)
	}
	got, err = s.GetPreview(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PreviewPending || got.StateDetail != PreviewTreeChangedDetail || got.Approval != nil || got.Commit != second {
		t.Fatalf("after the refused restart = %+v, want pending at %q with no approval", got, second)
	}
	if _, err := s.RestartPreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "no approved form") {
		t.Fatalf("restart of the taken-back card err = %v", err)
	}
	// A new approval is of the tree the card now shows, and restarts.
	if _, err := s.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	got, err = s.RestartPreview(ctx, "demo", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PreviewStopped || got.StateDetail != PreviewRestartDetail || got.Commit != second || got.Approval == nil || got.Approval.Commit != second {
		t.Fatalf("restart after a new approval = %+v, want stopped at %q", got, second)
	}
	if _, err := s.PreviewHead(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing head err = %v, want not found", err)
	}
}

// An approval is of the depot tree its card showed: the commit and the
// dirty mark. CheckPreviewTree, which the supervisor asks before it
// starts the approved command, passes a tree that reads the same without
// writing anything, and on any other takes the approval back: the row is
// pending again with no approval, shows the tree as it now stands and
// says why, until a person approves it anew.
func TestPreviewApprovalStandsOnItsTree(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	dir, git := previewRepo(t)
	first := git("rev-parse", "HEAD")
	check := func() error { return s.CheckPreviewTree(ctx, "demo") }
	row := func() *Preview {
		t.Helper()
		p, err := s.GetPreview(ctx, "demo")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "x", Dir: dir, Port: 8080}, "builder"); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "no approval record") {
		t.Fatalf("check of a proposal err = %v", err)
	}
	// The commit moves between the proposal and the approval: the person
	// approves the card they saw, which is not the tree now.
	git("commit", "-q", "--allow-empty", "-m", "two")
	second := git("rev-parse", "HEAD")
	approved, err := s.ApprovePreview(ctx, "demo", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Approval.Commit != first || approved.Approval.Dirty {
		t.Fatalf("approval = %+v, want the card's tree: commit %q, clean", approved.Approval, first)
	}
	seq, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "approval is taken back") {
		t.Fatalf("check after the commit moved err = %v, want the approval taken back", err)
	}
	if got := row(); got.State != PreviewPending || got.StateDetail != PreviewTreeChangedDetail || got.Approval != nil || got.Commit != second || got.Dirty {
		t.Fatalf("taken back = %+v, want pending at %q with no approval", got, second)
	}
	events, err := s.EventsSince(ctx, seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "preview_refused" || events[0].Data["detail"] != PreviewTreeChangedDetail {
		t.Fatalf("events after the approval was taken back = %+v, want one preview_refused", events)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "no approval record") {
		t.Fatalf("check of a taken-back card err = %v", err)
	}

	// Approved anew on the tree the card shows, the same tree passes, and
	// nothing is written.
	if _, err := s.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	before := row()
	seq, _ = s.Head(ctx)
	if err := check(); err != nil {
		t.Fatalf("check on the tree it was approved on: %v", err)
	}
	if after := row(); after.State != PreviewApproved || after.Approval == nil || !after.Approval.At.Equal(before.Approval.At) || after.Approval.Commit != second {
		t.Fatalf("after a passing check = %+v, want the approval kept", after)
	}
	if now, _ := s.Head(ctx); now != seq {
		t.Fatalf("a passing check wrote events up to %d, was %d", now, seq)
	}

	// An untracked file makes the tree dirty.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "approval is taken back") {
		t.Fatalf("check on a dirtied tree err = %v, want the approval taken back", err)
	}
	if got := row(); got.State != PreviewPending || got.Approval != nil || got.Commit != second || !got.Dirty {
		t.Fatalf("taken back for a dirty tree = %+v, want pending, dirty, at %q", got, second)
	}

	// Approved on the dirty tree, the approval stands while it reads the
	// same: the dirty mark and the commit are what it compares.
	if got, err := s.ApprovePreview(ctx, "demo", "ada"); err != nil || !got.Approval.Dirty || got.Approval.Commit != second {
		t.Fatalf("approval on a dirty tree = %+v, %v", got, err)
	}
	if err := check(); err != nil {
		t.Fatalf("check on the dirty tree it was approved on: %v", err)
	}
	git("add", "new.txt")
	git("commit", "-q", "-m", "three")
	if err := check(); err == nil {
		t.Fatal("check after the dirty tree was committed passed")
	}
	if got := row(); got.State != PreviewPending || got.Approval != nil || got.Commit != git("rev-parse", "HEAD") || got.Dirty {
		t.Fatalf("taken back after the commit = %+v, want pending, clean, at the new commit", got)
	}

	// An approval from before the tree was recorded names none, so on a
	// repository it is not one the tree can be said to match.
	if _, err := s.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.write(func(w *wtx) error {
		p := new(Preview)
		if _, err := get(w.Tx, bPreviews, "demo", p); err != nil {
			return err
		}
		p.Approval.Commit, p.Approval.Dirty = "", false
		return put(w.Tx, bPreviews, "demo", p)
	}); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "approval is taken back") {
		t.Fatalf("check of an approval with no tree err = %v, want the approval taken back", err)
	}

	// A directory in no repository has no commit and is never dirty, so
	// there is nothing for the approval to differ from.
	loose := t.TempDir()
	if _, err := s.ProposePreview(ctx, NewPreview{App: "loose", Command: "x", Dir: loose, Port: 8081}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePreview(ctx, "loose", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPreviewTree(ctx, "loose"); err != nil {
		t.Fatalf("check of a directory in no repository: %v", err)
	}
	if err := s.CheckPreviewTree(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("check of a missing preview err = %v, want not found", err)
	}
}

func mustPreviews(t *testing.T, s *Store) []Preview {
	t.Helper()
	all, err := s.Previews(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// A proposal's commit and dirty mark are the yard's own reading of its
// directory, so a proposer on the socket cannot name a commit for the
// person to approve that is not the one in the tree.
func TestPreviewProposalReadsItsOwnCommit(t *testing.T) {
	ctx := context.Background()
	s := previewYard(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"},
		{"commit", "-q", "--allow-empty", "-m", "one"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, Depot{Name: "demo", Path: dir, Kind: DepotGit, Base: "main"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProposePreview(ctx, NewPreview{App: "demo", Command: "x", Dir: dir, Port: 8080}, "builder")
	if err != nil {
		t.Fatal(err)
	}
	if p.Commit != strings.TrimSpace(string(head)) || !p.Dirty || p.Depot != "demo" {
		t.Fatalf("proposal = %+v, want commit %s, dirty, depot demo", p, head)
	}
	if _, err := s.ProposePreview(ctx, NewPreview{App: "rel", Command: "x", Dir: "app", Port: 8080}, "builder"); err == nil ||
		!strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative directory err = %v", err)
	}
}
