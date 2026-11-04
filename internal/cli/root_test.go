package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every command with subcommands answers one it has not with an error on
// stderr and a failing status, as yardr itself does. Cobra answers a group
// with its help and a zero status whatever follows it, so a typo (yardr bead
// nots) read as success, and so did a program written for a yardr with a
// subcommand this one lacks. Bare, a group still prints its help and ends 0,
// and does it on a home with no yard, which it neither reads nor makes. The
// groups are found by walking the tree, not from a list a new group would
// have to join.
func TestCommandGroupsRefuseAnUnknownSubcommand(t *testing.T) {
	t.Parallel()
	// The parents whose bare call does something: yardr itself starts the
	// onboarding or prints its help, daemon runs, and review shows its form.
	runs := []string{"yardr", "yardr daemon", "yardr review"}
	var parents []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.HasSubCommands() {
			parents = append(parents, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk((&app{}).rootCmd())
	// Not for want of looking: yardr has some thirty of them, nested ones too.
	if len(parents) < 25 || !slices.Contains(parents, "yardr user device") {
		t.Fatalf("%d commands with subcommands found: %v", len(parents), parents)
	}
	for _, path := range parents {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			args := strings.Fields(path)[1:]
			// A home of its own with no yard in it: a group neither onboards
			// nor makes one.
			home := t.TempDir()
			do := func(more ...string) (out, errs string, code int) {
				var stderr bytes.Buffer
				out, code = executeOut(&app{env: map[string]string{"YARDR_HOME": home}, stderr: &stderr}, append(slices.Clone(args), more...)...)
				return out, stderr.String(), code
			}
			refusal := fmt.Sprintf("unknown command %q for %q", "nosuchsub", path)
			// Words behind the name are how a script or an agent types one.
			for _, more := range [][]string{{"nosuchsub"}, {"nosuchsub", "a", "b"}} {
				if out, errs, code := do(more...); code != 1 || out != "" || errs != "yardr: "+refusal+"\n" {
					t.Errorf("%s %s: exit %d, stdout %q, stderr %q, want exit 1, no stdout, and stderr %q", path, strings.Join(more, " "), code, out, errs, "yardr: "+refusal+"\n")
				}
			}
			// As every error of a command that takes --json.
			wantJSON, err := json.Marshal(map[string]string{"error": refusal})
			if err != nil {
				t.Fatal(err)
			}
			if out, errs, code := do("--json", "nosuchsub"); code != 1 || out != string(wantJSON)+"\n" || errs != "" {
				t.Errorf("%s --json nosuchsub: exit %d, stdout %q, stderr %q, want exit 1, %s and no stderr", path, code, out, errs, wantJSON)
			}
			if slices.Contains(runs, path) {
				return
			}
			out, errs, code := do()
			if code != 0 || errs != "" || !strings.Contains(out, "Usage:") || !strings.Contains(out, path+" [command]") {
				t.Errorf("%s bare: exit %d, stderr %q, stdout:\n%s\nwant its help and exit 0", path, code, errs, out)
			}
			if left, err := os.ReadDir(home); err != nil || len(left) != 0 {
				t.Errorf("%s bare left %v in a home with no yard (%v)", path, left, err)
			}
		})
	}
}
