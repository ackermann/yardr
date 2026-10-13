package hooks_test

import (
	"reflect"
	"testing"

	"yardr.dev/yardr/internal/hooks"
)

func TestStateable(t *testing.T) {
	if got := hooks.Stateable(); len(got) != 0 {
		t.Fatalf("Stateable() = %v, want none: no builtin sets statuses", got)
	}
	if _, ok := hooks.StaterFor("token", nil, "", ""); ok {
		t.Fatal("a token hook has no threads to set the status of")
	}
	if _, ok := hooks.StaterFor("github", nil, "", ""); ok {
		t.Fatal("github without a declaration sets no status")
	}
	if got := hooks.States(); !reflect.DeepEqual(got, []string{"started", "review", "done"}) {
		t.Fatalf("States() = %v", got)
	}
}
