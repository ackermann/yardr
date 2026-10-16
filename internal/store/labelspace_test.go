package store_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A label holds no white space: routes match labels exactly, so one with
// a space never matches anything and fails silently (a shell loop once
// filed "difficulty:3 size:large" as one label). Create and add refuse
// one, naming it, and create nothing and change nothing; an ordinary
// label is still accepted (yardr-edhj).
func TestLabelWithWhitespaceIsRefused(t *testing.T) {
	s, ctx, b := yardrDepot(t)
	before, err := s.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	for _, label := range []string{"difficulty:3 size:large", "size:\tlarge", "topic:\nwords"} {
		want := fmt.Sprintf("label %q: labels are given one per flag", label)
		if _, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new", Labels: []string{label}}, "me"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("create %q: %v", label, err)
		}
		if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{AddLabels: []string{label}}, store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("add %q: %v", label, err)
		}
	}
	if after, _ := s.ListBeads(ctx, store.BeadFilter{All: true}); len(after) != len(before) {
		t.Fatalf("refused creates left %d beads, want %d", len(after), len(before))
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("refused writes logged %d events", now-head)
	}
	if got, _ := s.GetBead(ctx, b.ID); !slices.Equal(got.Labels, b.Labels) || got.Revision != b.Revision {
		t.Fatalf("refused add changed the bead: %+v", got)
	}
	made, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new", Labels: []string{"difficulty:3"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(made.Labels, []string{"difficulty:3"}) {
		t.Fatalf("created labels = %v", made.Labels)
	}
	added, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{AddLabels: []string{"size:large"}}, store.Expect{}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added.Labels, []string{"size:large", "ui"}) {
		t.Fatalf("added labels = %v", added.Labels)
	}
}
