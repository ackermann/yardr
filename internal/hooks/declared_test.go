package hooks

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// A declared source may not take the name of one yardr has in Go, but
// for one whose pack has taken the name while its Go adapter is still
// compiled in: the pack's source wins every lookup when the yard has
// it. source.BuiltIn is the adapters but those, and yardr itself,
// written down there because that package cannot import this one.
func TestDeclaredSourceNamesKeepClearOfAdapters(t *testing.T) {
	want := append(Sources(), store.SourceYardr)
	want = slices.DeleteFunc(want, packs.Carries)
	slices.Sort(want)
	got := slices.Clone(source.BuiltIn)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("source.BuiltIn = %v, want the adapters but those with packs, and yardr: %v", got, want)
	}
}
