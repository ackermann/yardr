package cli

import (
	"context"
	"strings"
	"testing"
)

// Applying the linear pack adds the source it declares; running it twice
// changes nothing.
func TestPackApplyLinearAddsTheDeclaredSource(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	requireNonInteractive(t)
	ctx := context.Background()
	out, err := run(t, a.packCmd(), "apply", "linear", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source linear is declared") {
		t.Errorf("the source was not added:\n%s", out)
	}
	if _, err := a.st.GetSource(ctx, "linear"); err != nil {
		t.Errorf("the source was not added: %v", err)
	}
	out, err = run(t, a.packCmd(), "apply", "linear", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing changed") {
		t.Errorf("second apply changed something:\n%s", out)
	}
}
