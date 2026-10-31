package hooks_test

import (
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

// A delivery is an outsider's when its source set outsider to yes on it,
// whichever source it is: no core code judges who a source's own people are.
// It reads the same stored (fields as []any) or built in process.
func TestOutsider(t *testing.T) {
	t.Parallel()
	delivery := func(source string, fields any) map[string]any {
		return map[string]any{"source": source, "type": "issue_comment", "fields": fields}
	}
	for _, c := range []struct {
		what string
		data map[string]any
		want bool
	}{
		{"set to yes, as stored", delivery("github", map[string]any{"outsider": []any{"yes"}, "sender": []any{"user"}}), true},
		{"set to yes, built in process as strings", delivery("github", map[string]any{"outsider": []string{"yes"}}), true},
		{"set to yes, built in process as a map of strings", delivery("github", map[string][]string{"outsider": {"yes"}}), true},
		{"another source's", delivery("forge", map[string]any{"outsider": []any{"yes"}}), true},
		{"set to anything but yes", delivery("github", map[string]any{"outsider": []any{"no"}}), false},
		{"set to nothing", delivery("github", map[string]any{"outsider": []any{""}}), false},
		{"not set, as a run of CI is not", delivery("github", map[string]any{"sender": []any{"user"}}), false},
		{"not set, with an association that is not a judgement", delivery("github", map[string]any{"association": []any{"none"}}), false},
		{"no fields", delivery("github", nil), false},
		{"no source", map[string]any{"fields": map[string]any{"outsider": []any{"yes"}}}, true},
	} {
		if got := hooks.Outsider(c.data); got != c.want {
			t.Errorf("%s: Outsider = %v, want %v", c.what, got, c.want)
		}
	}
}
