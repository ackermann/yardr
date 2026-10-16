package udiff

import (
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	if got := Unified("same\n", "same\n"); got != "" {
		t.Fatalf("one text diffed = %q", got)
	}
	got := Unified("a\nold\nb\n", "a\nnew\nb\n")
	want := "--- was\n+++ is\n@@ -1,3 +1,3 @@\n a\n-old\n+new\n b\n"
	if got != want {
		t.Fatalf("Unified = %q, want %q", got, want)
	}
	if ins, del := Counts(got); ins != 1 || del != 1 {
		t.Fatalf("Counts = %d, %d, want 1, 1", ins, del)
	}
	if got := Unified("", "x\n"); !strings.Contains(got, "+x") {
		t.Fatalf("an added file diffed = %q", got)
	}
}
