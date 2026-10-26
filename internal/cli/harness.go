package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/agents"
)

// harnessCmd prints what yardr knows of the harness kinds: the files it was
// built with. It touches neither the store nor the server, so it answers on
// a home with no yard.
func (a *app) harnessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harness",
		Short: "What yardr knows of each harness kind: the files it runs on",
		Long: `A harness is the agent program a group's or a crew member's kind names
(claude, codex, kimi, ...). herdr starts it and says whether it is idle,
working, blocked or done. What yardr knows of a kind beyond that is in one
file per kind, built into yardr: the marks its pane is read by, the error
lines it stops on, the questions and the login screen it draws in place of
its input, what it adds to a hint of yardr's, the flags yardr starts it
with or offers, the models it suggests a person pick, where the model its
agent reports is read, where the context it holds and the tokens it used
are read, whether yardr answers its folder-trust dialog and its
first-start screens, the login command a program can lead its login by,
and the API key it runs with. list and show print the
files this build of yardr runs on;
login leads a kind's login for a program.

A kind herdr starts that has no file is started all the same: yardr then
knows no marks, no error lines and no flags of it, and it may stop to ask.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "The kinds yardr has a file for, and what each file says", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return a.out(c.OutOrStdout(), map[string]any{"harnesses": agents.Harnesses}, func(w io.Writer) {
				var rows [][]string
				for _, h := range agents.Harnesses {
					rows = append(rows, []string{h.Kind, h.Label, fmt.Sprintf("v%d", h.Version), strings.Join(harnessKnown(h), ", ")})
				}
				table(w, "KIND\tHARNESS\tFILE\tYARDR KNOWS", rows)
				var none []string
				for _, k := range agents.Kinds() {
					if _, ok := agents.Lookup(k); !ok {
						none = append(none, k)
					}
				}
				fmt.Fprintf(w, "\nNo file, so started as herdr starts it and nothing more known: %s.\n", strings.Join(none, ", "))
				fmt.Fprintln(w, "yardr harness show <kind> prints a file's content.")
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "show <kind>", Short: "Everything a kind's file says, as yardr reads it", Args: cobra.ExactArgs(1),
		Example: `  yardr harness show codex
  yardr harness show kimi --json | jq -r '.errors[].pattern'`,
		RunE: func(c *cobra.Command, args []string) error {
			h, ok := agents.Lookup(args[0])
			if !ok {
				if !agents.IsKind(args[0]) {
					return fmt.Errorf("%s is no harness kind; herdr starts: %s", args[0], strings.Join(agents.Kinds(), ", "))
				}
				h.Kind = args[0]
			}
			return a.out(c.OutOrStdout(), h, func(w io.Writer) { printHarness(w, h, ok) })
		},
	})
	cmd.AddCommand(a.harnessLoginCmd())
	return cmd
}

// harnessKnown names, in a few words each, what h's file says.
func harnessKnown(h agents.Harness) []string {
	var out []string
	add := func(has bool, what string) {
		if has {
			out = append(out, what)
		}
	}
	add(h.Marks.Input != "", "pane marks")
	add(len(h.Errors) > 0, "error lines")
	add(len(h.Dialogs) > 0, "dialogs told of")
	add(len(h.Login) > 0, "login screen")
	add(len(h.Background) > 0, "background work")
	add(h.Hints != (agents.Hints{}), "hints")
	add(h.Model != nil, "reported model")
	add(h.Context != nil, "context use")
	add(len(h.Usage) > 0, "token count")
	add(h.Trust != nil, "trust dialog")
	add(h.Trust != nil && len(h.Trust.Env) > 0, "trust environment")
	add(len(h.Onboarding) > 0, "onboarding screens answered")
	add(len(h.Flags.Unattended) > 0, "unattended flags")
	add(h.Flags.Model != "", "model flag")
	add(len(h.Flags.Always) > 0, "flags always passed")
	add(len(h.Flags.Permissions) > 0, "permission presets")
	add(len(h.Flags.Version) > 0, "version flag")
	add(len(h.Flags.OneShot) > 0, "one-shot call")
	add(h.Install != "", "where to get it")
	add(h.Fetch != nil, "a fetch for a Mac")
	add(h.LoginRecipe != nil, "a login a program leads")
	add(len(h.LoginEnv) > 0, "an API key login")
	return out
}

// envKeys are the names of KEY=VALUE entries, in order: what printHarness
// names of a kind's pane environment. Values are never printed: a file's
// entries may one day hold a secret.
func envKeys(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		keys = append(keys, k)
	}
	return keys
}

// sessionFiles names where a kind's agent keeps the session files that its
// model, context use and tokens are read from: the directory (moved by the
// variable the file names, if it names one) and the glob that finds the
// session's file in it.
func sessionFiles(m *agents.ModelSource) string {
	dir := "~/" + m.Home
	if m.HomeEnv != "" {
		dir += " (or $" + m.HomeEnv + ")"
	}
	return dir + "/" + m.Glob
}

// lineOf names a line of a session file by its type: any line for no type,
// and by the key typeField where the type is not under "type".
func lineOf(typ, typeField string) string {
	switch {
	case typ == "":
		return "line"
	case typeField != "" && typeField != "type":
		return "line whose " + typeField + " is " + typ
	}
	return "line of type " + typ
}

// atUnits are the words for the units a usage entry's at_unit names.
var atUnits = map[string]string{"s": "seconds", "ms": "milliseconds", "us": "microseconds"}

// usageEntry says what one entry of a kind's usage reads: in lines, which
// lines of the session file it takes and how they add up; in counts, the
// field each of the four counts is read from, and in at, where a line's time
// is. A count the entry names no field for reads as 0, and an entry that
// names no time for its lines is left out of a count that stops at a time
// (usageEntries in internal/runner/usage.go).
func usageEntry(u agents.UsageSource) (lines, counts, at string) {
	numbers := " that has numbers at " + u.Usage
	if slices.Contains(strings.Split(u.Usage, "."), "*") {
		numbers += " (the * adds up every key there)"
	}
	line := lineOf(u.Type, u.TypeField)
	switch {
	case u.Read == "sum" && u.Key != "":
		lines = "every " + line + numbers + ", the last for each " + u.Key + ", added up"
	case u.Read == "sum":
		lines = "every " + line + numbers + ", added up"
	default:
		lines = "the last " + line + numbers
	}
	if u.Partial {
		lines += " (partial: it reads short of what the session used)"
	}
	count := func(label, field string) string {
		if field == "" {
			return label + " none (reads as 0)"
		}
		return label + " " + field
	}
	input := count("input", u.Input)
	if u.CacheInInput {
		input += " (includes the cache read)"
	}
	counts = strings.Join([]string{input, count("output", u.Output), count("cache read", u.CacheRead), count("cache write", u.CacheWrite)}, ", ")
	switch {
	case u.At == "":
		at = "none of its own, so a count that stops at a time leaves it out"
	case u.AtUnit != "":
		at = "from " + u.At + ", RFC 3339 when a string or Unix time in " + atUnits[u.AtUnit]
	default:
		at = "from " + u.At + ", RFC 3339 when a string or Unix time in seconds"
	}
	return lines, counts, at
}

// printHarness prints h as yardr harness show does; has is whether its kind
// has a file.
func printHarness(w io.Writer, h agents.Harness, has bool) {
	if !has {
		fmt.Fprintf(w, "%s: no harness file in this build of yardr.\n", h.Kind)
		fmt.Fprintln(w, "herdr starts it and says whether it is idle, working, blocked or done. yardr knows no marks,")
		fmt.Fprintln(w, "no error lines and no flags of it: its pane is read by the marks of no kind, an error of")
		fmt.Fprintln(w, "its own shows as session_idle only, and it runs with the arguments its group gives.")
		return
	}
	or := func(s, none string) string {
		if s == "" {
			return none
		}
		return s
	}
	args := func(a []string) string { return or(strings.Join(a, " "), "none known") }
	head := fmt.Sprintf("%s  %s", h.Kind, h.Label)
	if h.Provider != "" {
		head += ", by " + h.Provider
	}
	fmt.Fprintf(w, "%s  (harnesses/%s.yaml, version %d)\n", head, h.Kind, h.Version)
	if len(h.Aliases) > 0 {
		fmt.Fprintf(w, "  also asked for as: %s\n", strings.Join(h.Aliases, ", "))
	}
	fmt.Fprintf(w, "  get it: %s\n", agents.InstallHint(h.Kind))
	if f := h.Fetch; f == nil {
		fmt.Fprintln(w, "  on a Mac without a terminal: no way known, it is installed by hand")
	} else {
		how := "moved to " + f.Path
		if len(f.Run) > 0 {
			how = "run with `" + strings.Join(f.Run, " ") + "`, which puts it at " + f.Path
		}
		fmt.Fprintf(w, "  on a Mac without a terminal: version %s is fetched, checked against its sha256 and %s\n", f.Version, how)
		fmt.Fprintf(w, "    (verified from %s on %s; --json has the addresses; yardr fetches nothing itself)\n", f.Verified.From, f.Verified.On)
	}
	if h.Trust != nil {
		fmt.Fprintln(w, "  trust: yardr answers its folder-trust dialog (unless the group or crew member says trust=false)")
		if len(h.Trust.Env) > 0 {
			fmt.Fprintf(w, "  trust environment (automatic only): %s\n", strings.Join(envKeys(h.Trust.Env), ", "))
		}
	} else {
		fmt.Fprintln(w, "  trust: yardr answers no dialog of it")
	}
	fmt.Fprintln(w, "\nMarks its pane is read by")
	if m := h.Marks; m.Input == "" {
		fmt.Fprintln(w, "  none known: its input is not told from what it printed")
	} else {
		fmt.Fprintf(w, "  input begins with:    %s\n", m.Input)
		if m.Edge != "" {
			fmt.Fprintf(w, "  after the box's edge: %s\n", m.Edge)
		}
		fmt.Fprintf(w, "  a message it took:    %s\n", or(m.Message, "no mark known"))
		if m.Placeholder != "" {
			fmt.Fprintf(w, "  an empty input shows: %s\n", m.Placeholder)
		}
	}
	if h.Marks.Error != "" {
		fmt.Fprintf(w, "  before an error line: any of %s\n", h.Marks.Error)
	}
	fmt.Fprintln(w, "\nFlags")
	fmt.Fprintf(w, "  unattended: %s\n", args(h.Flags.Unattended))
	fmt.Fprintf(w, "  model flag: %s\n", or(h.Flags.Model, "--model (runner default)"))
	if len(h.Flags.Always) > 0 {
		fmt.Fprintf(w, "  always:     %s\n", args(h.Flags.Always))
	}
	fmt.Fprintf(w, "  version:    %s\n", args(h.Flags.Version))
	if len(h.Flags.OneShot) > 0 {
		fmt.Fprintf(w, "  one shot:   %s <prompt>\n", args(h.Flags.OneShot))
	} else {
		fmt.Fprintln(w, "  one shot:   none known")
	}
	for _, p := range h.Flags.Permissions {
		fmt.Fprintf(w, "  permission %q: %s  (%s)\n", p.Label, args(p.Args), p.Note)
	}
	fmt.Fprintln(w, "\nModels it offers")
	if len(h.Offers) == 0 {
		fmt.Fprintln(w, "  none named: --model takes any id")
	}
	for _, o := range h.Offers {
		line := "  " + o.Model
		if o.Label != "" {
			line += " (" + o.Label + ")"
		}
		if o.Recommended != "" {
			line += ", recommended for a " + o.Recommended
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "\nThe model its agent reports")
	if m := h.Model; m == nil {
		fmt.Fprintln(w, "  no source known: nothing is recorded and nothing compared")
	} else {
		fmt.Fprintf(w, "  %s: %s of the first %s that names one\n", sessionFiles(m), m.Field, lineOf(m.Type, m.TypeField))
	}
	// The loader refuses a context or usage with no model source, so the
	// file they are read from is the model's.
	if c := h.Context; c != nil {
		tokens := c.Tokens
		if len(tokens) == 0 {
			tokens = agents.DefaultContextTokens
		}
		fmt.Fprintln(w, "\nThe context its agent holds: a crew member is asked for a handoff at its --context-handoff percent of the window")
		fmt.Fprintf(w, "  %s: %s at %s of the last %s that has any of them\n",
			sessionFiles(h.Model), strings.Join(tokens, " + "), c.Usage, lineOf(c.Type, c.TypeField))
	}
	if len(h.Usage) > 0 {
		head := "\nTokens a session used, counted once it has stopped"
		if len(h.Usage) > 1 {
			head += ": the first entry that finds a line is the reading"
		}
		fmt.Fprintln(w, head)
		fmt.Fprintf(w, "  %s\n", sessionFiles(h.Model))
		for i, u := range h.Usage {
			lines, counts, at := usageEntry(u)
			fmt.Fprintf(w, "  %d. %s\n     counts: %s\n     time:   %s\n", i+1, lines, counts, at)
		}
	}
	fmt.Fprintln(w, "\nError lines it stops on, in the order they are tried")
	if len(h.Errors) == 0 {
		fmt.Fprintln(w, "  none known: nothing is reported, and session_idle with its tail is what there is")
	}
	var rows [][]string
	for _, e := range h.Errors {
		rows = append(rows, []string{"  " + e.Reason, e.Pattern})
	}
	if len(rows) > 0 {
		table(w, "  REASON\tPATTERN", rows)
	}
	if len(h.Dialogs) > 0 {
		fmt.Fprintln(w, "\nDialogs it draws in place of its input: told of at once (nudge_held), never answered")
		rows = nil
		for _, d := range h.Dialogs {
			rows = append(rows, []string{"  " + d.Dialog, d.Pattern})
		}
		table(w, "  DIALOG\tPATTERN", rows)
	}
	if len(h.Login) > 0 {
		fmt.Fprintln(w, "\nLogin screen, drawn in place of its input: told of at once (nudge_held), never typed into")
		for _, l := range h.Login {
			fmt.Fprintf(w, "  %s\n", l.Pattern)
		}
	}
	if r := h.LoginRecipe; r != nil {
		fmt.Fprintf(w, "\nLogin led by a program, with no terminal (yardr harness login %s --json)\n", h.Kind)
		fmt.Fprintf(w, "  the command: %s %s\n", agents.Executable(h.Kind), strings.Join(r.Argv, " "))
		for _, m := range r.Methods {
			fmt.Fprintf(w, "  method %q: %s\n", m.Label, strings.Join(m.Args, " "))
		}
		fmt.Fprintf(w, "  the page to sign in on: %s\n", r.OpenURL)
		if r.Input != "" {
			fmt.Fprintf(w, "  where it takes input:   %s\n", r.Input)
		}
		fmt.Fprintf(w, "  (read from %s on %s; it may stop matching when the harness prints something else)\n", r.Verified.From, r.Verified.On)
	}
	if len(h.LoginEnv) > 0 {
		fmt.Fprintln(w, "\nAPI key it runs with, offered on the yard's first-login ask; no login program is led for it")
		fmt.Fprintf(w, "  variables: %s\n", strings.Join(h.LoginEnv, ", "))
	}
	if k := h.ApiKey; k != nil {
		fmt.Fprintf(w, "\nUse-this-API-key dialog, drawn with no login: yardr answers %q with %s\n", k.Dialog, strings.Join(k.Answers, ", "))
	}
	if len(h.Onboarding) > 0 {
		fmt.Fprintln(w, "\nFirst-start screens, drawn before its input is up: yardr answers each once, in turn")
		for _, o := range h.Onboarding {
			shown := make([]string, len(o.Answers))
			for i, a := range o.Answers {
				shown[i] = fmt.Sprintf("%q", a)
			}
			answers := strings.Join(shown, " or ")
			if o.Cursor == "" {
				fmt.Fprintf(w, "  %q: Enter, once %s shows\n", o.Dialog, answers)
			} else {
				fmt.Fprintf(w, "  %q: the cursor %s is moved onto %s, then Enter\n", o.Dialog, o.Cursor, answers)
			}
		}
	}
	if len(h.Background) > 0 {
		fmt.Fprintln(w, "\nBackground work of its agent, drawn around its input: not idle to the idle limit while it shows")
		for _, b := range h.Background {
			fmt.Fprintf(w, "  %s\n", b.Pattern)
		}
	}
	if h.Hints.Reset != "" {
		fmt.Fprintln(w, "\nWhat it adds to a hint of yardr's")
		fmt.Fprintf(w, "  reset (a prompt at a usage limit's reset that it has not taken): %s\n", h.Hints.Reset)
	}
}
