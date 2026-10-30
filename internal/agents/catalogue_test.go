package agents

import (
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The store validates kinds, presets and harness keys against static tables
// instead of this catalogue, so that it imports no internal package. Those
// tables are copies of what the harness files say; this test pins them to
// the catalogue, so a file that renames a label, adds login_env or offers a
// new preset fails until the tables follow.
func TestStoreTablesMatchCatalogue(t *testing.T) {
	t.Parallel()
	if len(store.HarnessLabels) != len(Harnesses) {
		t.Fatalf("store.HarnessLabels has %d kinds, the catalogue %d", len(store.HarnessLabels), len(Harnesses))
	}
	var presets []string
	for _, h := range Harnesses {
		if store.HarnessLabels[h.Kind] != h.Label {
			t.Errorf("store.HarnessLabels[%q] = %q, the file says %q", h.Kind, store.HarnessLabels[h.Kind], h.Label)
		}
		if !slices.Equal(store.HarnessLoginEnv[h.Kind], h.LoginEnv) {
			t.Errorf("store.HarnessLoginEnv[%q] = %v, the file says %v", h.Kind, store.HarnessLoginEnv[h.Kind], h.LoginEnv)
		}
		for _, p := range h.Flags.Permissions {
			if k := store.PresetLabel(p.Label); !slices.Contains(presets, k) {
				presets = append(presets, k)
			}
		}
	}
	for kind := range store.HarnessLoginEnv {
		if _, ok := store.HarnessLabels[kind]; !ok {
			t.Errorf("store.HarnessLoginEnv names %q, which has no harness file", kind)
		}
	}
	slices.Sort(presets)
	if !slices.Equal(store.PresetNames, presets) {
		t.Errorf("store.PresetNames = %v, the files say %v", store.PresetNames, presets)
	}
}
