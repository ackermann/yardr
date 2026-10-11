package hooks

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A declared source may not take the name of one yardr has in Go.
// source.BuiltIn is the adapters, and yardr itself, written down there
// because that package cannot import this one.
func TestDeclaredSourceNamesKeepClearOfAdapters(t *testing.T) {
	want := append(Sources(), store.SourceYardr)
	slices.Sort(want)
	got := slices.Clone(source.BuiltIn)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("source.BuiltIn = %v, want the adapters, and yardr: %v", got, want)
	}
}
