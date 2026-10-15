package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

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
agent reports is read, whether yardr answers its folder-trust dialog, and
the login command a program can lead its login by. list and show print the
files this build of yardr runs on;
login leads a kind's login for a program.

A kind herdr starts that has no file is started all the same: yardr then
knows no marks, no error lines and no flags of it, and it may stop to ask.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "The kinds yardr has a file for, and what each file says", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return a.out(map[string]any{"harnesses": agents.Harnesses}, func() {
				var rows [][]string
				for _, h := range agents.Harnesses {
					rows = append(rows, []string{h.Kind, h.Label, fmt.Sprintf("v%d", h.Version), strings.Join(harnessKnown(h), ", ")})
				}
				table("KIND\tHARNESS\tFILE\tYARDR KNOWS", rows)
				var none []string
				for _, k := range agents.Kinds() {
					if _, ok := agents.Lookup(k); !ok {
						none = append(none, k)
					}
				}
				fmt.Printf("\nNo file, so started as herdr starts it and nothing more known: %s.\n", strings.Join(none, ", "))
				fmt.Println("yardr harness show <kind> prints a file's content.")
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "show <kind>", Short: "Everything a kind's file says, as yardr reads it", Args: cobra.ExactArgs(1),
		Example: `  yardr harness show codex
  yardr harness show kimi --json | jq -r '.errors[].pattern'`,
		RunE: func(_ *cobra.Command, args []string) error {
			h, ok := agents.Lookup(args[0])
			if !ok {
				if !agents.IsKind(args[0]) {
					return fmt.Errorf("%s is no harness kind; herdr starts: %s", args[0], strings.Join(agents.Kinds(), ", "))
				}
				h.Kind = args[0]
			}
			return a.out(h, func() { printHarness(h, ok) })
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
	add(h.Trust != nil, "trust dialog")
	add(len(h.Flags.Unattended) > 0, "unattended flags")
	add(h.Flags.Model != "", "model flag")
	add(len(h.Flags.Always) > 0, "flags always passed")
	add(len(h.Flags.Permissions) > 0, "permission presets")
	add(len(h.Flags.Version) > 0, "version flag")
	add(len(h.Flags.OneShot) > 0, "one-shot call")
	add(h.Install != "", "where to get it")
	add(h.Fetch != nil, "a fetch for a Mac")
	add(h.LoginRecipe != nil, "a login a program leads")
	return out
}

// printHarness prints h as yardr harness show does; has is whether its kind
// has a file.
func printHarness(h agents.Harness, has bool) {
	if !has {
		fmt.Printf("%s: no harness file in this build of yardr.\n", h.Kind)
		fmt.Println("herdr starts it and says whether it is idle, working, blocked or done. yardr knows no marks,")
		fmt.Println("no error lines and no flags of it: its pane is read by the marks of no kind, an error of")
		fmt.Println("its own shows as session_idle only, and it runs with the arguments its group gives.")
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
	fmt.Printf("%s  (harnesses/%s.yaml, version %d)\n", head, h.Kind, h.Version)
	if len(h.Aliases) > 0 {
		fmt.Printf("  also asked for as: %s\n", strings.Join(h.Aliases, ", "))
	}
	fmt.Printf("  get it: %s\n", agents.InstallHint(h.Kind))
	if f := h.Fetch; f == nil {
		fmt.Println("  on a Mac without a terminal: no way known, it is installed by hand")
	} else {
		how := "moved to " + f.Path
		if len(f.Run) > 0 {
			how = "run with `" + strings.Join(f.Run, " ") + "`, which puts it at " + f.Path
		}
		fmt.Printf("  on a Mac without a terminal: version %s is fetched, checked against its sha256 and %s\n", f.Version, how)
		fmt.Printf("    (verified from %s on %s; --json has the addresses; yardr fetches nothing itself)\n", f.Verified.From, f.Verified.On)
	}
	if h.Trust != nil {
		fmt.Println("  trust: yardr answers its folder-trust dialog (unless the group or crew member says trust=false)")
	} else {
		fmt.Println("  trust: yardr answers no dialog of it")
	}
	fmt.Println("\nMarks its pane is read by")
	if m := h.Marks; m.Input == "" {
		fmt.Println("  none known: its input is not told from what it printed")
	} else {
		fmt.Printf("  input begins with:    %s\n", m.Input)
		if m.Edge != "" {
			fmt.Printf("  after the box's edge: %s\n", m.Edge)
		}
		fmt.Printf("  a message it took:    %s\n", or(m.Message, "no mark known"))
	}
	if h.Marks.Error != "" {
		fmt.Printf("  before an error line: any of %s\n", h.Marks.Error)
	}
	fmt.Println("\nFlags")
	fmt.Printf("  unattended: %s\n", args(h.Flags.Unattended))
	fmt.Printf("  model flag: %s\n", or(h.Flags.Model, "--model (runner default)"))
	if len(h.Flags.Always) > 0 {
		fmt.Printf("  always:     %s\n", args(h.Flags.Always))
	}
	fmt.Printf("  version:    %s\n", args(h.Flags.Version))
	if len(h.Flags.OneShot) > 0 {
		fmt.Printf("  one shot:   %s <prompt>\n", args(h.Flags.OneShot))
	} else {
		fmt.Println("  one shot:   none known")
	}
	for _, p := range h.Flags.Permissions {
		fmt.Printf("  permission %q: %s  (%s)\n", p.Label, args(p.Args), p.Note)
	}
	fmt.Println("\nModels it offers")
	if len(h.Offers) == 0 {
		fmt.Println("  none named: --model takes any id")
	}
	for _, o := range h.Offers {
		line := "  " + o.Model
		if o.Label != "" {
			line += " (" + o.Label + ")"
		}
		if o.Recommended != "" {
			line += ", recommended for a " + o.Recommended
		}
		fmt.Println(line)
	}
	fmt.Println("\nThe model its agent reports")
	if m := h.Model; m == nil {
		fmt.Println("  no source known: nothing is recorded and nothing compared")
	} else {
		dir := "~/" + m.Home
		if m.HomeEnv != "" {
			dir += " (or $" + m.HomeEnv + ")"
		}
		line := "first line"
		if m.Type != "" {
			line = "first line of type " + m.Type
		}
		fmt.Printf("  %s/%s: %s of the %s that names one\n", dir, m.Glob, m.Field, line)
	}
	fmt.Println("\nError lines it stops on, in the order they are tried")
	if len(h.Errors) == 0 {
		fmt.Println("  none known: nothing is reported, and session_idle with its tail is what there is")
	}
	var rows [][]string
	for _, e := range h.Errors {
		rows = append(rows, []string{"  " + e.Reason, e.Pattern})
	}
	if len(rows) > 0 {
		table("  REASON\tPATTERN", rows)
	}
	if len(h.Dialogs) > 0 {
		fmt.Println("\nDialogs it draws in place of its input: told of at once (nudge_held), never answered")
		rows = nil
		for _, d := range h.Dialogs {
			rows = append(rows, []string{"  " + d.Dialog, d.Pattern})
		}
		table("  DIALOG\tPATTERN", rows)
	}
	if len(h.Login) > 0 {
		fmt.Println("\nLogin screen, drawn in place of its input: told of at once (nudge_held), never typed into")
		for _, l := range h.Login {
			fmt.Printf("  %s\n", l.Pattern)
		}
	}
	if r := h.LoginRecipe; r != nil {
		fmt.Printf("\nLogin led by a program, with no terminal (yardr harness login %s --json)\n", h.Kind)
		fmt.Printf("  the command: %s %s\n", agents.Executable(h.Kind), strings.Join(r.Argv, " "))
		for _, m := range r.Methods {
			fmt.Printf("  method %q: %s\n", m.Label, strings.Join(m.Args, " "))
		}
		fmt.Printf("  the page to sign in on: %s\n", r.OpenURL)
		if r.Input != "" {
			fmt.Printf("  where it takes input:   %s\n", r.Input)
		}
		fmt.Printf("  (read from %s on %s; it may stop matching when the harness prints something else)\n", r.Verified.From, r.Verified.On)
	}
	if len(h.Background) > 0 {
		fmt.Println("\nBackground work of its agent, drawn around its input: not idle to the idle limit while it shows")
		for _, b := range h.Background {
			fmt.Printf("  %s\n", b.Pattern)
		}
	}
	if h.Hints.Reset != "" {
		fmt.Println("\nWhat it adds to a hint of yardr's")
		fmt.Printf("  reset (a prompt at a usage limit's reset that it has not taken): %s\n", h.Hints.Reset)
	}
}
