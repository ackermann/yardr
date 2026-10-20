package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// safeFlow is depot yardr's flow as far as review: done goes to the second
// review, done-safe past it.
const safeFlow = `
backlog         -> new
new             -> review on done
review          -> review-security on done | approved on done-safe | new on changes
review-security -> approved on done | new on changes
approved        -> merged on done
merged terminal
`

// securityRepo is a depot web whose base carries safeFlow and, when list is
// not empty, SecurityPathsFile with it.
func securityRepo(t *testing.T, list string) (store.API, context.Context, string) {
	t.Helper()
	files := map[string]string{
		".yardr/flows/default.flow": safeFlow,
		"internal/store/auth.go":    "package store\n",
		"internal/other.go":         "package internal\n",
		"docs/a.md":                 "a\n",
		"go.mod":                    "module m\n",
	}
	if list != "" {
		files[store.SecurityPathsFile] = list
	}
	dir := gitRepo(t, files)
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return s, ctx, dir
}

// branchOf makes the branch of bead id, yard/<id>, started from `from` in a
// worktree of the depot at dir, with change applied there and committed.
func branchOf(t *testing.T, dir, id, from string, change func(wt string)) {
	t.Helper()
	wt := filepath.Join(t.TempDir(), id)
	runGit(t, dir, "worktree", "add", "-q", "-b", store.Branch(id), wt, from)
	change(wt)
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-q", "-m", id)
}

// reviewed makes bead id in review with its branch (branchOf); a nil change
// leaves the bead with no branch.
func reviewed(t *testing.T, s store.API, ctx context.Context, dir, id, from string, change func(wt string)) {
	t.Helper()
	if _, err := s.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "web", Stage: "review"}, "me"); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		branchOf(t, dir, id, from, change)
	}
}

// touching is a change that writes files.
func touching(t *testing.T, files ...string) func(string) {
	return func(wt string) {
		for _, f := range files {
			p := filepath.Join(wt, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func stageOf(t *testing.T, s store.API, ctx context.Context, id string) string {
	t.Helper()
	b, err := s.GetBead(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Stage
}

// done-safe on a diff that touches a path the base lists is refused, naming
// the file and what to report instead, and the bead stays where it is;
// done sends it on to the second review. A diff that touches none still
// goes straight to approved.
func TestDoneSafeRefusedOnASecurityPath(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "# what the second review may not be skipped for\n\ninternal/store/**\ngo.mod\n")
	reviewed(t, s, ctx, dir, "web-a", "main", touching(t, "internal/store/new.go", "docs/b.md"))
	reviewed(t, s, ctx, dir, "web-m", "main", touching(t, "go.mod"))
	reviewed(t, s, ctx, dir, "web-d", "main", touching(t, "docs/b.md", "internal/other.go"))

	_, err := s.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me")
	if err == nil {
		t.Fatal("done-safe on a diff that touches internal/store was taken")
	}
	for _, want := range []string{"web-a", "done-safe is refused", "against main", "internal/store/new.go", store.SecurityPathsFile, "report done instead, which sends it to review-security"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	// Only what matches is named.
	if strings.Contains(err.Error(), "docs/b.md") {
		t.Errorf("the refusal names a file no path of the list matches: %v", err)
	}
	if got := stageOf(t, s, ctx, "web-a"); got != "review" {
		t.Fatalf("a refused done-safe moved the bead to %s", got)
	}
	if b, err := s.Done(ctx, "web-a", "", "", store.Expect{}, "me"); err != nil || b.Stage != "review-security" {
		t.Fatalf("done after the refusal: %+v, %v", b, err)
	}

	if _, err := s.Done(ctx, "web-m", store.DoneSafe, "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("done-safe on go.mod: %v", err)
	}
	if b, err := s.Done(ctx, "web-d", store.DoneSafe, "", store.Expect{}, "me"); err != nil || b.Stage != "approved" {
		t.Fatalf("done-safe on a diff that touches no security path: %+v, %v", b, err)
	}
}

// With no list in the base, done-safe is taken as the flow says, whatever
// the diff touches: the check is the depot's to ask for.
func TestDoneSafeWithoutAListIsTakenAsBefore(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "")
	reviewed(t, s, ctx, dir, "web-a", "main", touching(t, "internal/store/new.go", "go.mod"))
	if b, err := s.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me"); err != nil || b.Stage != "approved" {
		t.Fatalf("done-safe with no list: %+v, %v", b, err)
	}
	// Nor does a depot with no checkout to ask git in.
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, st, "web", "", safeFlow)
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "web-a", Title: "a", Depot: "web", Stage: "review"}, "me"); err != nil {
		t.Fatal(err)
	}
	if b, err := st.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me"); err != nil || b.Stage != "approved" {
		t.Fatalf("done-safe in a depot with no path: %+v, %v", b, err)
	}
}

// The list is the base's: a bead that empties it, or adds to it, on its own
// branch changes nothing for itself.
func TestDoneSafeListIsNotTheBranchs(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "internal/store/**\n")
	reviewed(t, s, ctx, dir, "web-a", "main", func(wt string) {
		touching(t, "internal/store/new.go")(wt)
		if err := os.Remove(filepath.Join(wt, store.SecurityPathsFile)); err != nil {
			t.Fatal(err)
		}
	})
	reviewed(t, s, ctx, dir, "web-b", "main", func(wt string) {
		touching(t, "docs/b.md")(wt)
		if err := os.WriteFile(filepath.Join(wt, store.SecurityPathsFile), []byte("docs/**\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := s.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "internal/store/new.go") {
		t.Fatalf("done-safe on a branch that deleted the list: %v", err)
	}
	if b, err := s.Done(ctx, "web-b", store.DoneSafe, "", store.Expect{}, "me"); err != nil || b.Stage != "approved" {
		t.Fatalf("done-safe on a branch that added docs to the list: %+v, %v", b, err)
	}
}

// A file moved out of a security path is a change to it: the old path is
// named, not only the new one.
func TestDoneSafeRenameOutOfASecurityPath(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "internal/store/**\n")
	reviewed(t, s, ctx, dir, "web-a", "main", func(wt string) {
		runGit(t, wt, "mv", "internal/store/auth.go", "internal/moved.go")
	})
	if _, err := s.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "internal/store/auth.go") {
		t.Fatalf("done-safe on a file moved out of internal/store: %v", err)
	}
}

// A child of a train is compared with its train's branch, which holds what
// the train's earlier children landed: the diff is the child's own.
func TestDoneSafeChildOfATrainIsComparedWithItsTrain(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "internal/store/**\n")
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-t", Title: "train", Type: store.TypeTrain, Depot: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	// What an earlier child landed on the train's branch.
	branchOf(t, dir, "web-t", "main", touching(t, "internal/store/earlier.go"))
	reviewed(t, s, ctx, dir, "web-c", store.Branch("web-t"), touching(t, "docs/c.md"))
	reviewed(t, s, ctx, dir, "web-e", store.Branch("web-t"), touching(t, "internal/store/auth.go"))
	for _, child := range []string{"web-c", "web-e"} {
		if err := s.AddDep(ctx, "web-t", child, store.DepParent, "", "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := s.Done(ctx, "web-c", store.DoneSafe, "", store.Expect{}, "me"); err != nil || b.Stage != "approved" {
		t.Fatalf("done-safe on a child whose own diff touches no security path: %+v, %v", b, err)
	}
	_, err := s.Done(ctx, "web-e", store.DoneSafe, "", store.Expect{}, "me")
	if err == nil || !strings.Contains(err.Error(), "against yard/web-t") || !strings.Contains(err.Error(), "internal/store/auth.go") || strings.Contains(err.Error(), "earlier.go") {
		t.Fatalf("done-safe on a child that touches internal/store: %v", err)
	}
}

// A diff git cannot be asked for holds the report too, with the reason: a
// depot that lists paths wants the check made.
func TestDoneSafeHeldWhenTheDiffCannotBeTold(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "internal/store/**\n")
	reviewed(t, s, ctx, dir, "web-a", "main", nil) // no branch
	_, err := s.Done(ctx, "web-a", store.DoneSafe, "", store.Expect{}, "me")
	if err == nil || !strings.Contains(err.Error(), "could not be told") || !strings.Contains(err.Error(), "report done instead") {
		t.Fatalf("done-safe with no branch to diff: %v", err)
	}
	if got := stageOf(t, s, ctx, "web-a"); got != "review" {
		t.Fatalf("the bead moved to %s", got)
	}
}

// An outcome the flow does not offer at the stage is the flow's refusal to
// make, whatever the depot lists.
func TestDoneSafeNotInTheFlowIsRefusedByTheFlow(t *testing.T) {
	t.Parallel()
	s, ctx, dir := securityRepo(t, "internal/store/**\n")
	reviewed(t, s, ctx, dir, "web-a", "main", touching(t, "internal/store/new.go"))
	if _, err := s.Done(ctx, "web-a", "approve", "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "no outcome approve") {
		t.Fatalf("an outcome the flow lacks: %v", err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "web-n", Title: "n", Depot: "web", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, "web-n", store.DoneSafe, "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "no outcome done-safe for stage new") {
		t.Fatalf("done-safe in a stage that lacks it: %v", err)
	}
}

func TestParseSecurityPaths(t *testing.T) {
	t.Parallel()
	got, err := store.ParseSecurityPaths([]byte("# comment\n\n  internal/store/**  \r\ngo.mod\n:(exclude)docs/**\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{":(glob)internal/store/**", ":(glob)go.mod", ":(exclude)docs/**"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, err := store.ParseSecurityPaths([]byte("# nothing but a comment\n")); err != nil || len(got) != 0 {
		t.Fatalf("a file of comments: %q, %v", got, err)
	}
	if _, err := store.ParseSecurityPaths([]byte("a\x00b\n")); err == nil {
		t.Error("a path with a NUL in it was taken")
	}
	if _, err := store.ParseSecurityPaths([]byte(strings.Repeat("x\n", 501))); err == nil {
		t.Error("501 entries were taken")
	}
}

// Depot yardr's own list names at least the paths its review.md stage file
// has always called security-relevant, and each still matches a file of the
// checkout: a directory renamed without the list is a path no longer held.
func TestYardrsOwnSecurityPaths(t *testing.T) {
	t.Parallel()
	text, err := os.ReadFile("../../" + store.SecurityPathsFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ParseSecurityPaths(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"internal/store/**", "internal/rpc/**", "internal/web/**", "internal/hooks/**", "internal/peer/**",
		"internal/pairing/**", "internal/source*/**", "internal/yards/**", "go.mod", "go.sum", ".yardr/**", "contrib/assembly/**"} {
		if !slices.Contains(got, ":(glob)"+want) {
			t.Errorf("%s lacks %s", store.SecurityPathsFile, want)
		}
	}
	if out, err := exec.Command("git", "-C", "../..", "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil {
		t.Skipf("not a git checkout: %v: %s", err, out)
	}
	for _, spec := range got {
		out, err := exec.Command("git", "-C", "../..", "ls-files", "--", spec).CombinedOutput()
		if err != nil {
			t.Errorf("git ls-files -- %s: %v: %s", spec, err, out)
		} else if strings.TrimSpace(string(out)) == "" {
			t.Errorf("%s matches no file of the repository: the code moved and the list did not", spec)
		}
	}
}
