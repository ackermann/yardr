package cli

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"yardr.dev/yardr/internal/store"
)

// firstLine cut at 100 bytes, so a line of multi-byte characters (the row of
// "─" a harness draws) ended inside one and printed as "�…" (yardr-tpxc).
func TestFirstLineCutsBetweenCharacters(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"short", "  one line  ", "one line"},
		{"first of several", "one\ntwo\n", "one"},
		{"100 ascii is whole", strings.Repeat("a", 100), strings.Repeat("a", 100)},
		{"101 ascii is cut", strings.Repeat("a", 101), strings.Repeat("a", 100) + "…"},
		{"90 three-byte is whole", strings.Repeat("─", 90), strings.Repeat("─", 90)},
		{"100 three-byte is whole", strings.Repeat("─", 100), strings.Repeat("─", 100)},
		{"101 three-byte is cut", strings.Repeat("─", 101), strings.Repeat("─", 100) + "…"},
		// 99 one-byte characters put every later boundary off a multiple of
		// the character's width, whatever byte count the cut is made at.
		{"mixed widths", strings.Repeat("a", 99) + "───", strings.Repeat("a", 99) + "─…"},
		{"cut line of several", strings.Repeat("é", 150) + "\nrest", strings.Repeat("é", 100) + "…"},
	} {
		got := firstLine(tc.in)
		if got != tc.want {
			t.Errorf("%s: firstLine = %q, want %q", tc.name, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: firstLine = %q, not valid UTF-8", tc.name, got)
		}
	}
}

// The key lists of crew add --set and crew set name only config keys the
// store takes for a crew member, so a setting taken out is taken out of
// the help with it: release stayed behind in both when the arrival hold
// went (yardr-2ya6).
func TestCrewHelpNamesOnlyStoredKeys(t *testing.T) {
	cmd := (&app{}).crewCmd()
	add, _, err := cmd.Find([]string{"add"})
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := cmd.Find([]string{"set"})
	if err != nil {
		t.Fatal(err)
	}
	usage := add.Flags().Lookup("set").Usage
	_, addList, ok := strings.Cut(usage, ": ")
	if !ok {
		t.Fatalf("crew add --set usage has no key list: %q", usage)
	}
	_, setList, ok := strings.Cut(set.Long, "takes it\n(")
	if !ok {
		t.Fatalf("crew set help has no key list:\n%s", set.Long)
	}
	setList, _, _ = strings.Cut(setList, ")")
	taken := store.ConfigKeysOf(store.CrewConfig, "herdr")
	lists := []struct{ name, list string }{{"add", addList}, {"set", setList}}
	var keys [][]string
	for _, l := range lists {
		var ks []string
		for _, k := range strings.Split(l.list, ",") {
			ks = append(ks, strings.TrimSpace(k))
		}
		if len(ks) == 0 {
			t.Errorf("crew %s help lists no keys", l.name)
		}
		for _, k := range ks {
			if !slices.Contains(taken, k) {
				t.Errorf("crew %s help names %q, which the store takes no crew member key for (takes: %s)",
					l.name, k, strings.Join(taken, ", "))
			}
		}
		keys = append(keys, slices.Sorted(slices.Values(ks)))
	}
	// Set says its keys are add's ("as --set on yardr crew add takes
	// it"), so the two lists name the same keys.
	if !slices.Equal(keys[0], keys[1]) {
		t.Errorf("crew add lists %v, crew set lists %v", keys[0], keys[1])
	}
}
