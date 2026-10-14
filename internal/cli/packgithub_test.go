package cli

import (
	"context"
	"strings"
	"testing"
)

// Applying the github pack adds the source it declares; running it twice
// changes nothing.
func TestPackApplyGithubAddsTheDeclaredSource(t *testing.T) {
	a := packApp(t)
	setInteractive(t, false)
	ctx := context.Background()
	out, err := run(t, a.packCmd(), "apply", "github", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source github is declared") {
		t.Errorf("the source was not added:\n%s", out)
	}
	if _, err := a.st.GetSource(ctx, "github"); err != nil {
		t.Errorf("the source was not added: %v", err)
	}
	out, err = run(t, a.packCmd(), "apply", "github", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing changed") {
		t.Errorf("second apply changed something:\n%s", out)
	}
}
