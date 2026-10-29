package eta

import (
	"slices"
	"testing"
)

func TestMergeStageOrder(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ base, next, want []string }{
		{nil, []string{"new", "decide"}, []string{"new", "decide"}},
		{[]string{"new", "review", "decide"}, []string{"new", "decide", "review"}, []string{"new", "review", "decide"}},
		{[]string{"new", "review"}, []string{"new", "review", "verify"}, []string{"new", "review", "verify"}},
		{[]string{"new", "decide"}, []string{"new", "verify", "decide"}, []string{"new", "verify", "decide"}},
		{[]string{"new", "decide"}, []string{"triage", "new", "decide"}, []string{"triage", "new", "decide"}},
		{[]string{"new"}, []string{"build", "sign"}, []string{"new", "build", "sign"}},
		{[]string{"a", "b"}, []string{"b", "x", "a"}, []string{"a", "b", "x"}},
	} {
		if got := mergeStageOrder(c.base, c.next); !slices.Equal(got, c.want) {
			t.Errorf("mergeStageOrder(%v, %v) = %v, want %v", c.base, c.next, got, c.want)
		}
	}
}
