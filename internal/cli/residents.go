package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// defaultOn are the events a new resident hears about: work that needs a
// decision, and anything that went wrong.
var defaultOn = []string{"gave_up", "session_blocked", "unrouted", "no_runner", "workspace_kept",
	"resident_gave_up", "runtime_down", "session_idle", "session_aged"}

// knownKinds are offered in the interactive form.
var knownKinds = []string{"gave_up", "session_blocked", "session_died", "session_failed", "unrouted",
	"runtime_down", "runtime_up", "session_idle", "session_aged",
	"no_runner", "workspace_kept", "resident_gave_up", "created", "claimed", "advanced", "closed",
	"held", "released", "noted", "dep_added"}

func (a *app) residentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "resident", Short: "Always-on sessions not tied to a bead (a mayor)"}

	var def store.Resident
	var sets []string
	add := &cobra.Command{
		Use:   "add [<name>]",
		Short: "Create or replace a resident (no name: an interactive form)",
		Long: `A resident is an always-on agent session, such as a mayor. The daemon keeps
it running in its directory, nudges it when its inbox has something new and it
is idle, and rotates it: at --max-age it is asked to write a handoff, and its
next session starts from that handoff (forced after --handoff-grace).

Without --set kind, a herdr resident gets the yard's default_kind.`,
		Example: `  yardr resident add                       # interactive
  yardr resident add mayor --set kind=claude --set model=opus \
    --set 'args=["--permission-mode","auto"]' --set prompt=$HOME/.yardr/roles/mayor.md \
    --dir ~/projects --max-age 6h`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
					return fmt.Errorf("give a resident name, or run in a terminal for the interactive form")
				}
				if err := a.residentForm(c.Context(), st); errors.Is(err, huh.ErrUserAborted) {
					fmt.Println("cancelled")
					return nil
				} else {
					return err
				}
			}
			def.Name = args[0]
			if def.Config, err = parseSets(sets); err != nil {
				return err
			}
			withDefaultKind(c.Context(), st, def.Runner, def.Config)
			if def.Dir, err = absDir(def.Dir); err != nil {
				return err
			}
			r, err := st.PutResident(c.Context(), def, a.who())
			if err != nil {
				return err
			}
			return a.out(r, func() { fmt.Printf("resident %s saved; journal %s\n", r.Name, r.Journal) })
		},
	}
	f := add.Flags()
	f.StringVar(&def.Runner, "runner", "herdr", "runner (herdr)")
	f.StringArrayVar(&sets, "set", nil, "runner config key=value (repeatable): kind, model, args, prompt, env_file")
	f.StringVar(&def.Dir, "dir", "", "directory the resident works in (default: the yard home)")
	f.StringSliceVar(&def.On, "on", defaultOn, "event kinds delivered to its inbox (mail always is)")
	f.StringVar(&def.MaxAge, "max-age", "", "ask for a handoff after this long, e.g. 6h (default: never)")
	f.StringVar(&def.HandoffGrace, "handoff-grace", "15m", "how long a requested handoff may take before a forced restart")
	f.StringVar(&def.Revision, "revision", "", "refuse unless the resident is still at this revision (from resident show --json)")

	list := &cobra.Command{
		Use: "list", Short: "List residents", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rs, err := st.Residents(c.Context())
			if err != nil {
				return err
			}
			return a.out(rs, func() {
				rows := [][]string{}
				for _, r := range rs {
					up := "-"
					if r.Live() && r.StartedAt != nil {
						up = time.Since(*r.StartedAt).Round(time.Second).String()
					}
					state := r.State
					if !r.Enabled {
						state += " (disabled)"
					}
					if r.RestartRequested != "" {
						state += " (restart: " + r.RestartRequested + ")"
					}
					rows = append(rows, []string{r.Name, state, up, residentAgent(r), r.Journal})
				}
				table("NAME\tSTATE\tUP\tAGENT\tJOURNAL", rows)
			})
		},
	}

	show := &cobra.Command{
		Use: "show <name>", Short: "Show a resident, its inbox and recent journal", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			r, err := st.GetResident(c.Context(), args[0])
			if err != nil {
				return err
			}
			inbox, err := st.Inbox(c.Context(), r.Name, 0)
			if err != nil {
				return err
			}
			notes, err := st.Notes(c.Context(), r.Journal)
			if err != nil {
				return err
			}
			out := map[string]any{"resident": r, "inbox": inbox.Total, "journal": notes}
			env := envFileSummary(r.Config)
			if env != nil {
				out["env_file"] = env
			}
			return a.out(out, func() {
				fmt.Printf("%s  %s · %s · %s\n", r.Name, r.State, residentAgent(*r), r.Dir)
				if env != nil {
					fmt.Printf("  env_file %s", env["path"])
					if keys, _ := env["keys"].([]string); len(keys) > 0 {
						fmt.Printf(" (%s)", strings.Join(keys, ", "))
					}
					if e, _ := env["error"].(string); e != "" {
						fmt.Printf(": %s", e)
					}
					fmt.Println()
				}
				fmt.Printf("  inbox %d unread · on %s\n", inbox.Total, strings.Join(r.On, ","))
				if r.MaxAge != "" {
					fmt.Printf("  rotates after %s (handoff grace %s)\n", r.MaxAge, or(r.HandoffGrace, "15m"))
				}
				for _, src := range sortedKeys(r.Identities) {
					id := r.Identities[src]
					fmt.Printf("  %s identity: %s\n", src, strings.Join(slices.DeleteFunc([]string{id.ID, id.Key},
						func(s string) bool { return s == "" }), " / "))
				}
				for _, sub := range r.Subscriptions {
					fmt.Printf("  subscribed: %s\n", sub)
				}
				fmt.Printf("  journal %s (%d entries)\n", r.Journal, len(notes))
				for _, n := range notes[max(0, len(notes)-5):] {
					fmt.Printf("    [%s %s %s] %s\n", n.At.Local().Format("01-02 15:04"), or(n.Kind, "note"), n.Author,
						firstLine(n.Text))
				}
			})
		},
	}

	simple := func(use, short string, fn func(ctx context.Context, st store.API, name string) error) *cobra.Command {
		return &cobra.Command{Use: use + " <name>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(c *cobra.Command, args []string) error {
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				if err := fn(c.Context(), st, args[0]); err != nil {
					return err
				}
				return a.out(map[string]string{"resident": args[0], use: "ok"}, func() { fmt.Printf("%s: %s\n", args[0], use) })
			}}
	}
	// rev is --revision: the resident's revision as the caller last read
	// it (resident show --json); the write is refused if it moved since.
	var rev string
	rm := simple("rm", "Delete a resident (its journal is kept)", func(ctx context.Context, st store.API, n string) error {
		// Delete directly so the revision check and removal happen in one
		// transaction. Disabling first would mint a new revision and the
		// subsequent unchecked delete could erase a concurrent update.
		return st.DeleteResident(ctx, n, rev, a.who())
	})
	start := simple("start", "Keep the resident running (clears a failure streak)", func(ctx context.Context, st store.API, n string) error {
		return st.SetResidentEnabled(ctx, n, true, rev, a.who())
	})
	stop := simple("stop", "Stop the resident and keep it stopped", func(ctx context.Context, st store.API, n string) error {
		return st.SetResidentEnabled(ctx, n, false, rev, a.who())
	})
	restart := simple("restart", "Start a fresh session once the current one is idle", func(ctx context.Context, st store.API, n string) error {
		return st.RequestRestart(ctx, n, "requested by "+a.who(), rev, a.who())
	})
	for _, c := range []*cobra.Command{rm, start, stop, restart} {
		c.Flags().StringVar(&rev, "revision", "", "refuse unless the resident is still at this revision (from resident show --json)")
	}

	entry := func(kind, short string) *cobra.Command {
		return &cobra.Command{Use: kind + " <name> <text|->", Short: short, Args: cobra.ExactArgs(2),
			RunE: func(c *cobra.Command, args []string) error {
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				text := args[1]
				if text == "-" {
					if text, err = readAllStdin(); err != nil {
						return err
					}
				}
				if err := st.Journal(c.Context(), args[0], kind, text, a.who()); err != nil {
					return err
				}
				return a.out(map[string]string{"resident": args[0], "written": kind}, func() {
					if kind == store.NoteHandoff {
						fmt.Println("handoff written; yardr starts a fresh session from it once this one is idle")
					}
				})
			}}
	}
	journal := entry(store.NoteJournal, "Keep a decision or piece of context in the journal (≤4 KB)")
	handoff := entry(store.NoteHandoff, "End this session: write where things stand; the next session starts from it (≤16 KB)")

	cmd.AddCommand(add, list, show, rm, start, stop, restart, journal, handoff, a.identityCmd())
	return cmd
}

func residentAgent(r store.Resident) string {
	parts := []string{r.Runner}
	if k, _ := r.Config["kind"].(string); k != "" {
		parts = append(parts, k)
	}
	if m, _ := r.Config["model"].(string); m != "" {
		parts = append(parts, m)
	}
	return strings.Join(parts, " · ")
}

// envFileSummary is what a session started from cfg would load from its
// env_file: the path and key names, or why the runner would refuse it. Never
// a value. nil when cfg names no env_file.
func envFileSummary(cfg map[string]any) map[string]any {
	path, _ := cfg["env_file"].(string)
	if path == "" {
		return nil
	}
	out := map[string]any{"path": path}
	if p, err := config.ExpandPath(path); err == nil {
		out["path"] = p
	}
	f, err := config.ReadEnvFile(path)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["keys"] = f.Keys()
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 100 {
		line = line[:100] + "…"
	}
	return line
}

func absDir(d string) (string, error) {
	if d == "" {
		return "", nil
	}
	if strings.HasPrefix(d, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(home, d[2:])
	}
	d, err := filepath.Abs(d)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", d)
	}
	return d, nil
}

// currentResident is --resident, else $YARDR_RESIDENT (set in a resident's
// own session).
func currentResident(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if r := config.Getenv("RESIDENT"); r != "" {
		return r, nil
	}
	return "", fmt.Errorf("--resident is required (or set YARDR_RESIDENT)")
}

func (a *app) inboxCmd() *cobra.Command {
	var resident string
	var ack bool
	var limit int
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "A resident's unread events and mail",
		Long:  "Show a resident's unacknowledged events, oldest first. --ack marks everything shown as handled.",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			name, err := currentResident(resident)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			page, err := st.Inbox(c.Context(), name, limit)
			if err != nil {
				return err
			}
			if ack && len(page.Events) > 0 {
				if err := st.AckInbox(c.Context(), name, page.Events[len(page.Events)-1].Seq); err != nil {
					return err
				}
			}
			return a.out(page, func() {
				if page.Total == 0 {
					fmt.Println("inbox empty")
					return
				}
				for _, e := range page.Events {
					printInboxEvent(e, name)
				}
				if rest := page.Total - len(page.Events); rest > 0 {
					fmt.Printf("… %d more\n", rest)
				}
				if ack {
					fmt.Printf("acknowledged %d\n", len(page.Events))
				}
			})
		},
	}
	cmd.Flags().StringVar(&resident, "resident", "", "resident (default $YARDR_RESIDENT)")
	cmd.Flags().BoolVar(&ack, "ack", false, "mark the events shown as handled")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many to show")
	return cmd
}

func printInboxEvent(e store.Event, resident string) {
	switch e.Kind {
	case "mail":
		fmt.Printf("%6d %s  mail from %s: %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), e.Actor, show(e.Data["text"]))
	case "hook":
		printHookDelivery(e, resident)
	default:
		printBeadDelivery(e, resident)
	}
}

func (a *app) mailCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mail <resident> <text|->",
		Short: "Send a message to a resident's inbox",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			text := args[1]
			if text == "-" {
				if text, err = readAllStdin(); err != nil {
					return err
				}
			}
			if err := st.Mail(c.Context(), args[0], text, a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"to": args[0], "sent": "ok"}, func() { fmt.Printf("mail sent to %s\n", args[0]) })
		},
	}
}

func (a *app) rememberCmd() *cobra.Command {
	var m store.NewMemory
	var expires string
	cmd := &cobra.Command{
		Use:   "remember <title>",
		Short: "Keep a piece of knowledge as a memory (listed by title in primes)",
		Example: `  yardr remember "Deploy freeze" -b "No webapp deploys until Fri 10-09" --expires 7d
  yardr remember "Reviewers want screenshots on UI beads" --scope all`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			m.Title = args[0]
			if m.Body == "-" {
				if m.Body, err = readAllStdin(); err != nil {
					return err
				}
			}
			if m.Scope == "" {
				m.Scope = "all"
				if r := config.Getenv("RESIDENT"); r != "" {
					m.Scope = "resident:" + r
				}
			}
			if expires != "" {
				d, err := parseDays(expires)
				if err != nil {
					return err
				}
				t := time.Now().Add(d)
				m.Expires = &t
			}
			b, err := st.Remember(c.Context(), m, a.who())
			if err != nil {
				return err
			}
			return a.out(b, func() { fmt.Println(b.ID) })
		},
	}
	cmd.Flags().StringVarP(&m.Body, "body", "b", "", "the memory (- reads stdin)")
	cmd.Flags().StringVar(&m.Scope, "scope", "", "all, or resident:<name> (default: your resident, else all)")
	cmd.Flags().StringVar(&expires, "expires", "", "retire it after this long: 7d, 12h")
	return cmd
}

func (a *app) recallCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "recall [query]",
		Short: "List live memories (titles); yardr bead show <id> reads one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if scope == "" {
				if r := config.Getenv("RESIDENT"); r != "" {
					scope = "resident:" + r
				}
			}
			q := ""
			if len(args) == 1 {
				q = args[0]
			}
			ms, err := st.Recall(c.Context(), scope, q)
			if err != nil {
				return err
			}
			return a.out(ms, func() {
				rows := [][]string{}
				for _, m := range ms {
					exp := "-"
					if m.Expires != nil {
						exp = m.Expires.Local().Format("2006-01-02 15:04")
					}
					rows = append(rows, []string{m.ID, m.Scope, exp, m.Title})
				}
				table("ID\tSCOPE\tEXPIRES\tTITLE", rows)
			})
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "only memories visible in this scope (default: your resident's)")
	return cmd
}

// parseDays is time.ParseDuration plus a "d" (day) unit.
func parseDays(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q (use e.g. 7d or 12h)", s)
	}
	return d, nil
}

// primeResident prints what a fresh resident session starts from.
func (a *app) primeResident(ctx context.Context, st store.API, name string) error {
	p, err := st.ResidentPrime(ctx, name)
	if err != nil {
		return err
	}
	r := p.Resident
	role := ""
	if path, _ := r.Config["prompt"].(string); path != "" {
		if !filepath.IsAbs(path) && r.Dir != "" {
			path = filepath.Join(r.Dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("resident %s prompt: %w", name, err)
		}
		role = strings.TrimSpace(string(b))
	}
	return a.out(map[string]any{"prime": p, "role": role}, func() {
		fmt.Printf("# yardr resident: %s\n\n", name)
		fmt.Printf("You are `%s`, an always-on resident of this yard, working in %s. ", name, or(r.Dir, "the yard home"))
		fmt.Printf("Your yardr commands are attributed to you (YARDR_ACTOR=%s).\n\n", name)
		if role != "" {
			fmt.Printf("## Your role\n\n%s\n\n", role)
		}
		if p.Handoff != nil {
			fmt.Printf("## Your last handoff (%s, by %s)\n\n%s\n\n", p.Handoff.At.Local().Format("2006-01-02 15:04"),
				p.Handoff.Author, strings.TrimSpace(p.Handoff.Text))
		} else {
			fmt.Print("## No handoff yet\n\nThis is your first session, or none was ever written.\n\n")
		}
		if len(p.Since) > 0 {
			fmt.Printf("## Journal since then (%d", len(p.Since))
			if p.Omitted > 0 {
				fmt.Printf("; %d earlier not shown: yardr bead show %s", p.Omitted, r.Journal)
			}
			fmt.Print(")\n\n")
			for _, n := range p.Since {
				fmt.Printf("- [%s %s] %s\n", n.At.Local().Format("01-02 15:04"), n.Author, strings.TrimSpace(n.Text))
			}
			fmt.Println()
		}
		if p.NoHandoff {
			fmt.Print("## Your previous session ended without a handoff\n\nWhat happened since (by kind): ")
			kinds := make([]string, 0, len(p.EventCounts))
			for k, n := range p.EventCounts {
				kinds = append(kinds, fmt.Sprintf("%s %d", k, n))
			}
			slices.Sort(kinds)
			fmt.Printf("%s\n\nMost recent:\n", strings.Join(kinds, ", "))
			for _, e := range p.Recent {
				printEvent(e)
			}
			fmt.Println()
		}
		fmt.Printf("## Inbox: %d unread\n\n", p.InboxTotal)
		for _, e := range p.Inbox {
			printInboxEvent(e, name)
		}
		if p.InboxTotal > 0 {
			fmt.Println()
		}
		if len(p.Memories) > 0 {
			fmt.Print("## Memories (titles; yardr bead show <id> reads one)\n\n")
			for _, m := range p.Memories {
				exp := ""
				if m.Expires != nil {
					exp = " (until " + m.Expires.Local().Format("01-02 15:04") + ")"
				}
				fmt.Printf("- %s  %s%s\n", m.ID, m.Title, exp)
			}
			fmt.Println()
		}
		if note := yardSkew(); note != "" {
			fmt.Printf("## Important\n\n%s\n\n", note)
		}
		fmt.Printf(`## How you work

- yardr inbox / yardr inbox --ack: your events and mail; ack what you have handled.
  yardr nudges you when something new arrives and you are idle.
- yardr resident journal %[1]s "...": keep decisions and context worth keeping (max 4 KB).
- yardr resident handoff %[1]s "...": end your session. Write what you are doing, what is
  next and what to watch (max 16 KB); yardr starts your next session from it.
- yardr remember "title" -b "..." [--expires 7d] / yardr recall [query]: lasting knowledge.
- yardr mail <resident> "...": message another resident.
- The yard itself: yardr ready, yardr bead list|show|create|update|advance|close|hold|unhold|release,
  yardr dep add, yardr prime --bead <id>, yardr events, yardr group list, yardr route list, yardr session list.
  Do not run yardr watch: it never returns.
`, name)
	})
}

// residentForm creates or edits a resident interactively, reusing the
// agent, model and permission questions of the group form.
func (a *app) residentForm(ctx context.Context, st store.API) error {
	rs, err := st.Residents(ctx)
	if err != nil {
		return err
	}
	kind := or(defaultKind(ctx, st), "claude")
	def := store.Resident{Runner: "herdr", On: defaultOn, HandoffGrace: "15m", Config: map[string]any{"kind": kind}}
	editing := false
	if len(rs) > 0 {
		pick := newGroup
		opts := []huh.Option[string]{huh.NewOption("+ New resident", newGroup)}
		for _, r := range rs {
			opts = append(opts, huh.NewOption(r.Name+"  ("+residentAgent(r)+")", r.Name))
		}
		if err := ask(huh.NewSelect[string]().Title("Resident").Options(opts...).Value(&pick)); err != nil {
			return err
		}
		for _, r := range rs {
			if r.Name == pick {
				def, editing = r, true
				// The daemon's bookkeeping (nudges, handles) re-mints the
				// revision while the form is open; holding the form to it
				// would refuse most edits of a running resident.
				def.Revision = ""
			}
		}
	}
	if !editing {
		if err := ask(huh.NewInput().Title("Name").Placeholder("mayor").Value(&def.Name).
			Validate(func(s string) error {
				if !groupName.MatchString(s) {
					return errors.New("letters, digits, '.', '_', '-', '/'")
				}
				return nil
			})); err != nil {
			return err
		}
	}
	// The agent questions work on group answers; translate both ways.
	ga := answersFrom(store.Group{Name: def.Name, Runner: "herdr", Config: def.Config, Limit: 1})
	if ga.Kind == "" {
		ga.Kind = kind
	}
	if err := a.agentFields(ctx, &ga); err != nil {
		return err
	}
	def.Runner, def.Config = "herdr", ga.group().Config

	dir := def.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	on := slices.Clone(def.On)
	opts := []huh.Option[string]{}
	for _, k := range knownKinds {
		opts = append(opts, huh.NewOption(k, k).Selected(slices.Contains(on, k)))
	}
	if err := ask(
		huh.NewInput().Title("Directory").Description("Where the resident works.").Value(&dir).
			Validate(func(s string) error { _, err := absDir(s); return err }),
		huh.NewMultiSelect[string]().Title("Inbox").Description("Events it is nudged about (mail always is).").
			Options(opts...).Value(&on),
		huh.NewInput().Title("Rotate after").Description("Ask for a handoff after this long, e.g. 6h; empty: never.").
			Value(&def.MaxAge).Validate(func(s string) error {
			if s == "" {
				return nil
			}
			_, err := time.ParseDuration(s)
			return err
		}),
	); err != nil {
		return err
	}
	if def.Dir, err = absDir(dir); err != nil {
		return err
	}
	def.On = on
	save := true
	summary := fmt.Sprintf("%s: %s in %s\ninbox: %s\nrotate after: %s", def.Name, residentAgent(def), def.Dir,
		strings.Join(def.On, ", "), or(def.MaxAge, "never"))
	if err := ask(huh.NewConfirm().Title("Save this resident?").Description(summary).Value(&save)); err != nil {
		return err
	}
	if !save {
		fmt.Println("not saved")
		return nil
	}
	r, err := st.PutResident(ctx, def, a.who())
	if err != nil {
		return err
	}
	fmt.Printf("resident %s saved; the daemon starts it (journal %s)\n", r.Name, r.Journal)
	return nil
}

// yardSkew warns when the yardr on PATH is not the binary running now: an
// agent typing "yardr" would get a different (likely older) version.
func yardSkew() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	onPath, err := exec.LookPath("yardr")
	if err == nil {
		if r, err := filepath.EvalSymlinks(onPath); err == nil {
			onPath = r
		}
	}
	if onPath == self {
		return ""
	}
	if onPath == "" {
		onPath = "nothing"
	}
	return fmt.Sprintf("`yardr` on your PATH is %s, not this yardr. Run every yardr command below as %s instead of yardr.",
		onPath, self)
}
