package hooks_test

import (
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

func TestOpenable(t *testing.T) {
	if got := hooks.Openable(); len(got) != 0 {
		t.Fatalf("openable: %v, want none: no builtin opens threads", got)
	}
	if _, ok := hooks.OpenerFor("token", nil, "", ""); ok {
		t.Fatal("a token source opens no threads")
	}
	if _, ok := hooks.OpenerFor("github", nil, "", ""); ok {
		t.Fatal("github without a declaration opens no threads")
	}
}
