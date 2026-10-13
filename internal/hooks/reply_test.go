package hooks_test

import (
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

// No builtin is a source anyone can be answered in.
func TestRepliable(t *testing.T) {
	if got := hooks.Repliable(); len(got) != 0 {
		t.Errorf("repliable %v, want none", got)
	}
	if _, ok := hooks.ReplierFor("token", nil, "", ""); ok {
		t.Error("token has a replier")
	}
	if _, ok := hooks.ReplierFor("github", nil, "", ""); ok {
		t.Error("github without a declaration has a replier")
	}
}
