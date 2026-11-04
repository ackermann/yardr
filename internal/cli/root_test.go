package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// A help text that tells its reader to run a subcommand of a group names one
// the group has. yardr peer intake's help sent people to a flow subcommand that
// flow never had (add, where it has set), and nothing noticed while a group
// answered a word it had not with its help and a zero status. The test reads
// the mentions from every command's own help and holds them to the tree
// rootCmd builds, so a command added later is checked without anyone listing
// it.
//
// It judges little, because help is prose. Only the word after a group counts.
// The words after a command that runs are its arguments, and the word right
// after yardr is prose. And only a word some other command is named by counts,
// since a sentence about what a group's process does would read as a mistaken
// command, while a person who types a word no command has is refused by the
// group.
func TestHelpNamesOnlyCommandsThatExist(t *testing.T) {
	t.Parallel()
	root := (&app{}).rootCmd()
	// known is every name a command answers to, at any depth.
	known := map[string]bool{}
	var names func(c *cobra.Command)
	names = func(c *cobra.Command) {
		known[c.Name()] = true
		for _, alias := range c.Aliases {
			known[alias] = true
		}
		for _, sub := range c.Commands() {
			names(sub)
		}
	}
	names(root)
	// A mention is a run of lowercase words; a flag, a <placeholder>, a
	// bracket or the end of the line ends it.
	mention := regexp.MustCompile(`\byardr((?: [a-z][a-z0-9-]*)+)`)
	// stray walks the words of a mention down the tree and returns the group
	// that has no subcommand of the next word, and that word, when some other
	// command is named by it.
	stray := func(words []string) (*cobra.Command, string) {
		cur := root
		for _, w := range words {
			var next *cobra.Command
			for _, sub := range cur.Commands() {
				if sub.Name() == w || sub.HasAlias(w) {
					next = sub
					break
				}
			}
			if next == nil {
				if cur == root || !cur.HasSubCommands() || !known[w] {
					return nil, ""
				}
				return cur, w
			}
			cur = next
		}
		return nil, ""
	}
	var checked int
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		texts := []string{c.Short, c.Long, c.Example}
		for _, flags := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			flags.VisitAll(func(f *pflag.Flag) { texts = append(texts, f.Usage) })
		}
		for _, text := range texts {
			for _, m := range mention.FindAllStringSubmatch(text, -1) {
				words := strings.Fields(m[1])
				checked++
				if group, word := stray(words); group != nil {
					t.Errorf("%s: its help names \"yardr %s\", and %s has no subcommand %q", c.CommandPath(), strings.Join(words, " "), group.CommandPath(), word)
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	// Not for want of looking: the help names commands by the hundred (900
	// when this was written). The two calls below hold stray itself: it flags
	// the wording that stood and lets the one that holds by.
	if checked < 500 {
		t.Fatalf("%d mentions of a command found in the help of the whole tree", checked)
	}
	if group, word := stray([]string{"flow", "add"}); group == nil || group.CommandPath() != "yardr flow" || word != "add" {
		t.Errorf("flow, add: stray gave %v, %q, want the flow group and add", group, word)
	}
	if group, word := stray([]string{"flow", "set", "named"}); group != nil {
		t.Errorf("flow, set, named: stray gave %s and %q, want none", group.CommandPath(), word)
	}
}
