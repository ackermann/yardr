package assembly

// A design bead lands one document under docs/design/ and no code: a
// design branch that changes any other path is held in approved before the
// tests run, naming every such path, so code no reviewer read never lands
// (yardr-mdaf). A branch with only the document lands as any change does.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A design branch with the document and a file beside it is held where it
// is, naming the file: the tests, red if run, never run on it.
func TestMergeLocalHoldsADesignThatChangesCode(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.yardr("bead", "update", "r-1", "--type", "design")
	if err := os.MkdirAll(filepath.Join(y.wt, "docs", "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	y.commit(y.wt, "docs/design/2026-10-15-r-1-direction.md", "# direction\n")
	y.commit(y.wt, "evil.go", "package main\n")
	y.tests("echo the tests ran; exit 1")

	y.assembly()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	notes := y.notes()
	for _, want := range []string{"docs/design", "evil.go"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("the hold names no %q:\n%s", want, notes)
		}
	}
	if strings.Contains(y.log, "the tests ran") {
		t.Fatalf("the tests ran on a design that cannot land:\n%s", y.log)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved under a design that changes code")
	}
}

// A design branch with only the document lands as any change does.
func TestMergeLocalLandsADesignDocument(t *testing.T) {
	y := newYard(t)
	y.yardr("bead", "update", "r-1", "--type", "design")
	if err := os.MkdirAll(filepath.Join(y.wt, "docs", "design"), 0o755); err != nil {
		t.Fatal(err)
	}
	tip := y.commit(y.wt, "docs/design/2026-10-15-r-1-direction.md", "# direction\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead %s/%s hold=%v, want merged and closed", b.Stage, b.Status, b.Hold)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the design commit %s", main, tip)
	}
}
