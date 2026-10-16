package store

import "testing"

// The done-route follows done from the stage, implicit single-edge done
// included, stopping at visited stages and at stages with no done.
func TestDoneReaches(t *testing.T) {
	t.Parallel()
	f := &Flow{
		Transitions: map[string][]string{
			"a": {"b"},
			"b": {"c", "a"},
			"c": {"d"},
			"x": {"y"},
			"y": {"x"},
		},
		Outcomes: map[string][]Outcome{
			"a": {{Name: "done", To: "b"}},
			"b": {{Name: "question", To: "a"}, {Name: "done", To: "c"}},
			// c names no outcomes: its single way out is an implicit done.
			"x": {{Name: "done", To: "y"}},
			"y": {{Name: "done", To: "x"}},
		},
	}
	for _, tc := range []struct {
		from, stage string
		want        bool
	}{
		{"a", "a", true},
		{"a", "b", true},
		{"a", "c", true},
		{"a", "d", true}, // through c's implicit done
		{"b", "a", false},
		{"b", "c", true},
		{"b", "d", true},
		{"c", "d", true},
		{"c", "a", false},
		{"d", "a", false}, // d has no way out at all
		{"a", "x", false},
		// The cyclic pair terminates, reaching only each other.
		{"x", "x", true},
		{"x", "y", true},
		{"x", "a", false},
		{"y", "x", true},
	} {
		if got := f.doneReaches(tc.from, tc.stage); got != tc.want {
			t.Errorf("doneReaches(%q, %q) = %v, want %v", tc.from, tc.stage, got, tc.want)
		}
	}
	// A stage with outcomes but no done ends the walk.
	g := &Flow{
		Transitions: map[string][]string{"a": {"b"}, "b": {"c"}},
		Outcomes: map[string][]Outcome{
			"a": {{Name: "done", To: "b"}},
			"b": {{Name: "failed", To: "c"}},
		},
	}
	if !g.doneReaches("a", "b") || g.doneReaches("a", "c") {
		t.Error("the walk passes a stage with outcomes but no done")
	}
}
