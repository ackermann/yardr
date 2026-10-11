package hooks

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A declared source may not take the name of one yardr has in Go, but
// for linear: the pack's source is named linear while its Go adapter
// is still compiled in, and the declared one wins every lookup when
// the yard has it. source.BuiltIn is the adapters but linear, and
// yardr itself, written down there because that package cannot import
// this one.
func TestDeclaredSourceNamesKeepClearOfAdapters(t *testing.T) {
	want := append(Sources(), store.SourceYardr)
	want = slices.DeleteFunc(want, func(s string) bool { return s == "linear" })
	slices.Sort(want)
	got := slices.Clone(source.BuiltIn)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("source.BuiltIn = %v, want the adapters but linear, and yardr: %v", got, want)
	}
}
