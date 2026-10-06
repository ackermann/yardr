package cli

import (
	"context"
	"encoding/json"
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
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// defaultOn are the events a new resident hears about: work that needs a
// decision, anything that went wrong, and beads held: the roles tell a
// stuck agent to hold its bead for the yardmaster, so the hold must reach it.
// A resident from before a kind joined this list does not get it: resident
// add --on replaces the list, so name every kind wanted (docs/residents.md).
var defaultOn = []string{"gave_up", "session_blocked", "unrouted", "stranded", "no_runner", "workspace_kept",
	"resident_gave_up", "runtime_down", "session_idle", "session_aged", "session_stalled",
	"session_harness_error", "session_model_mismatch", "session_prompt_gave_up", "move_refused", "held"}

// knownKinds are offered in the interactive form.
var knownKinds = []string{"gave_up", "session_blocked", "session_died", "session_failed", "unrouted", "stranded",
	"runtime_down", "runtime_up", "session_idle", "session_aged", "session_stalled", "session_harness_error",
	"session_model_mismatch", "session_prompt_gave_up", "session_prompt_lost", "session_prompt_typed", "move_refused", "nudge_held",
	"no_runner", "workspace_kept", "resident_gave_up", "created", "claimed", "advanced", "closed",
	"held", "released", "noted", "dep_added", "hook_rejected"}

func (a *app) residentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "resident", Short: "Always-on sessions not tied to a bead (a yardmaster)"}

	var def store.Resident
	var sets []string
	var replace bool
	add := &cobra.Command{
		Use:   "add [<name>]",
		Short: "Create a resident, or replace one's whole definition (no name: an interactive form)",
		Long: `A resident is an always-on agent session, such as a yardmaster. The daemon keeps
it running in its directory, nudges it when its inbox has something new and it
is idle, and rotates it: at --max-age it is asked to write a handoff, and its
next session starts from that handoff (forced after --handoff-grace).

Without --set kind, a herdr resident gets the yard's default_kind.

Add writes the whole definition: on a resident that exists, every setting
left out is dropped. So there it lists what would change and asks, in a
terminal; anywhere else it is refused unless --replace is given. To change
some settings and keep the rest, use yardr resident set.

--set env_file=<path> names a file of KEY=VALUE lines (mode 0600) that join
the session's environment: its credentials. --set env_command="<command>"
names a command that prints such lines instead, so a secrets tool holds the
values: it is run without a shell (words split at spaces; a JSON list for a
word with a space in it) at each start, and the resident does not start
when it fails. With both, the command's value wins for a key in both. The
command line is stored and shown (yardr resident show): never put a secret
in it. docs/residents.md has both.

--set open_threads=linear,github lets the resident open threads of its own
in those sources (yardr thread open). It is off unless set, and only the
operator (the yard's operator setting, else the user running the command)
may add a source to it: a resident's session cannot allow itself or
another resident.

--set set_state=linear,github lets it set the status of a thread it takes
part in in those sources (yardr thread state), under the same rule.

--set peers=north,south lets it mail residents of those linked yards (yardr
mail <resident>@<peer>), under the same rule. The peers are this yard's names
for its links (yardr peer list).

--set release=north lets it release the beads that arrive held from those
linked yards (yardr bead unhold), under the same rule. Without it only a
user releases such a bead (yardr peer intake).`,
		Example: `  yardr resident add                       # interactive
  yardr resident add yardmaster --set kind=claude --set model=opus \
    --set 'args=["--permission-mode","auto"]' --set prompt=$HOME/.yardr/roles/yardmaster.md \
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
			if err := a.mayDefine(c.Context(), st, def, false); err != nil {
				return err
			}
			// Add writes the whole definition, so on a resident that is there
			// every flag left out is a setting lost: it says which, and saves
			// only once someone has said that is meant.
			var changes []defChange
			switch have, err := st.GetResident(c.Context(), def.Name); {
			case err == nil:
				changes = defChanges(*have, def)
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
			if len(changes) > 0 && !replace {
				if a.json || !interactive() || agentSession() {
					return fmt.Errorf("resident %s exists, and add replaces its whole definition. This one would change:\n%s\n"+
						"To change some settings and keep the rest: yardr resident set %s key=value. To save this definition as given: --replace",
						def.Name, changeLines(changes), def.Name)
				}
				ok := false
				if err := ask(huh.NewConfirm().Title(fmt.Sprintf("Replace the definition of %s?", def.Name)).
					Description(changeLines(changes) + "\nyardr resident set " + def.Name + " key=value changes some settings and keeps the rest.").
					Affirmative("Replace").Negative("No").Value(&ok)); err != nil && !errors.Is(err, huh.ErrUserAborted) {
					return err
				}
				if !ok {
					fmt.Printf("resident %s not changed\n", def.Name)
					return nil
				}
			}
			r, err := st.PutResident(c.Context(), def, a.who())
			if err != nil {
				return err
			}
			return a.out(r, func() {
				fmt.Printf("resident %s saved; journal %s\n", r.Name, r.Journal)
				if len(changes) > 0 {
					fmt.Println(changeLines(changes))
				}
			})
		},
	}
	f := add.Flags()
	f.BoolVar(&replace, "replace", false, "the resident exists: save this definition in its place without asking (settings left out are dropped)")
	f.StringVar(&def.Runner, "runner", "herdr", "runner (herdr)")
	f.StringArrayVar(&sets, "set", nil, "runner config key=value (repeatable): kind, model, args, prompt, env_file, env_command, open_threads, set_state, peers, release")
	f.StringVar(&def.Dir, "dir", "", "directory the resident works in (default: the yard home)")
	f.StringSliceVar(&def.On, "on", defaultOn, "event kinds delivered to its inbox (mail always is); replaces the list, so name every kind wanted")
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
					if f := startFailures(r, time.Now()); f != "" {
						state += " (" + f + ")"
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
			timers, err := st.Timers(c.Context(), r.Name)
			if err != nil {
				return err
			}
			out := map[string]any{"resident": r, "inbox": inbox.Total, "journal": notes, "timers": timers}
			env, keys := residentEnvSummary(c.Context(), *r, a.home())
			for _, e := range env {
				out[e.Setting] = e.fields()
			}
			creds := credentialSummary(*r, keys)
			if len(creds) > 0 {
				out["credentials"] = creds
			}
			return a.out(out, func() {
				fmt.Printf("%s  %s · %s · %s\n", r.Name, r.State, residentAgent(*r), r.Dir)
				if f := startFailures(*r, time.Now()); f != "" {
					fmt.Printf("  %s", f)
					if reason := lastStartFailure(c.Context(), st, *r); reason != "" {
						fmt.Printf(", the last with: %s", reason)
					}
					fmt.Println()
					// The reason says what herdr or the session did, not
					// whether the harness works: doctor can ask it.
					if r.Runner == "herdr" {
						fmt.Println("  yardr doctor --try asks its harness for one real answer (a model call), yardr doctor for the rest")
					}
				}
				for _, e := range env {
					fmt.Printf("  %s %s", e.Setting, e.From)
					if len(e.Keys) > 0 {
						fmt.Printf(" (%s)", strings.Join(e.Keys, ", "))
					}
					if e.Error != "" {
						fmt.Printf(": %s", e.Error)
					}
					fmt.Println()
				}
				for _, src := range sortedKeys(creds) {
					fmt.Printf("  %s credential: %s\n", src, creds[src])
				}
				fmt.Printf("  inbox %d unread · on %s\n", inbox.Total, strings.Join(r.On, ","))
				if may := r.MayOpen(); len(may) > 0 {
					fmt.Printf("  may open threads in %s\n", strings.Join(may, ", "))
				}
				if may := r.MaySetState(); len(may) > 0 {
					fmt.Printf("  may set the status of threads in %s\n", strings.Join(may, ", "))
				}
				if may := r.MayMail(); len(may) > 0 {
					fmt.Printf("  may mail residents of %s\n", strings.Join(may, ", "))
				}
				if may := r.MayRelease(); len(may) > 0 {
					fmt.Printf("  may release beads that arrive from %s\n", strings.Join(may, ", "))
				}
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
				printTimers(timers, r.Name, "  ")
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

	cmd.AddCommand(add, a.residentSetCmd(), list, show, rm, start, stop, restart, journal, handoff, a.identityCmd())
	return cmd
}

// residentSetCmd is yardr resident set: some settings of a definition
// changed, the rest kept. Resident add cannot do that, as it writes the
// whole definition, and a grant of one permission made with it cost a
// running resident everything that was not retyped.
func (a *app) residentSetCmd() *cobra.Command {
	var unset, on []string
	var dir, maxAge, grace, rev string
	cmd := &cobra.Command{
		Use:   "set <name> [key=value ...]",
		Short: "Change some settings of a resident and keep the rest",
		Long: `Changes the settings named and keeps the rest of the resident's definition.
key=value is a runner config key, as --set on yardr resident add takes it
(kind, model, args, prompt, env_file, env_command, open_threads, set_state,
peers, release); --unset <key> removes one. --dir, --on, --max-age and --handoff-grace
change what the same flags of add set.

It prints each setting changed, as it was and as it is. The value of a key
named like a secret (token, secret, password, key) is not printed.

open_threads, set_state, peers and release follow the rule of add: only the
operator (the yard's operator setting, else the user running the command)
may allow a resident a source or a peer it was not allowed before. Narrowing
one, or removing it with --unset, is anyone's.

A running resident keeps its session. What it may do (open_threads,
set_state, peers, release), --on and --max-age hold from the next time they are
looked at; what a session starts with (kind, model, args, prompt, its
environment, --dir) is read when the next one starts: yardr resident
restart <name>.`,
		Example: `  yardr resident set yardmaster set_state=linear
  yardr resident set yardmaster release=north
  yardr resident set yardmaster model=opus 'args=["--permission-mode","auto"]' --max-age 6h
  yardr resident set yardmaster --unset open_threads`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			name := args[0]
			// parseSets would speak of --set, which this command has not.
			for _, kv := range args[1:] {
				if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
					return fmt.Errorf("%q: want key=value (--unset <key> removes a setting)", kv)
				}
			}
			sets, err := parseSets(args[1:])
			if err != nil {
				return err
			}
			fl := c.Flags()
			if len(sets) == 0 && len(unset) == 0 && !fl.Changed("dir") && !fl.Changed("on") && !fl.Changed("max-age") && !fl.Changed("handoff-grace") {
				return fmt.Errorf("nothing to set: give key=value, --unset <key>, --dir, --on, --max-age or --handoff-grace")
			}
			for _, k := range unset {
				if _, both := sets[k]; both {
					return fmt.Errorf("%s is both set and unset", k)
				}
			}
			if fl.Changed("dir") {
				if dir, err = absDir(dir); err != nil {
					return err
				}
			}
			// The operator rule looks at what this command sets, not at
			// the settings it keeps: those were checked when they were saved.
			if err := a.mayDefine(ctx, st, store.Resident{Name: name, Config: sets}, true); err != nil {
				return err
			}
			var r *store.Resident
			var changes []defChange
			// The daemon's bookkeeping re-mints a running resident's revision
			// between the read and the write now and then. Without --revision
			// that is no reason to refuse: read again and apply the same
			// change. With it, the caller asked to be refused.
			for try := 0; ; try++ {
				have, err := st.GetResident(ctx, name)
				if err != nil {
					return err
				}
				def := *have
				def.Config = map[string]any{}
				for k, v := range have.Config {
					def.Config[k] = v
				}
				for _, k := range unset {
					if _, is := def.Config[k]; !is {
						return fmt.Errorf("--unset %s: %s has no such setting (it has: %s)", k, name, or(strings.Join(sortedKeys(have.Config), ", "), "none"))
					}
					delete(def.Config, k)
				}
				for k, v := range sets {
					def.Config[k] = v
				}
				if fl.Changed("dir") {
					def.Dir = dir
				}
				if fl.Changed("on") {
					def.On = on
				}
				if fl.Changed("max-age") {
					def.MaxAge = maxAge
				}
				if fl.Changed("handoff-grace") {
					def.HandoffGrace = grace
				}
				if changes = defChanges(*have, def); len(changes) == 0 {
					r = have
					break
				}
				def.Revision = or(rev, have.Revision)
				if r, err = st.PutResident(ctx, def, a.who()); err == nil {
					break
				}
				if rev != "" || try == 4 || !errors.Is(err, store.ErrConflict) {
					return err
				}
			}
			return a.out(r, func() {
				if len(changes) == 0 {
					fmt.Printf("resident %s unchanged: it is set so already\n", name)
					return
				}
				fmt.Printf("resident %s saved\n%s\n", name, changeLines(changes))
				var atStart []string
				for _, ch := range changes {
					if !slices.Contains([]string{store.OpenThreads, store.SetState, store.MailPeers, store.ReleasePeers, "--on", "--max-age", "--handoff-grace"}, ch.Key) {
						atStart = append(atStart, ch.Key)
					}
				}
				if r.Live() && len(atStart) > 0 {
					fmt.Printf("%s is running and keeps its session: its next one starts with the new %s (yardr resident restart %s)\n",
						name, strings.Join(atStart, ", "), name)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&unset, "unset", nil, "remove this runner config key (repeatable)")
	f.StringVar(&dir, "dir", "", "directory the resident works in (empty: the yard home)")
	f.StringSliceVar(&on, "on", nil, "event kinds delivered to its inbox; replaces the list, so name every kind wanted")
	f.StringVar(&maxAge, "max-age", "", "ask for a handoff after this long, e.g. 6h (empty: never)")
	f.StringVar(&grace, "handoff-grace", "", "how long a requested handoff may take before a forced restart")
	f.StringVar(&rev, "revision", "", "refuse unless the resident is still at this revision (from resident show --json)")
	return cmd
}

// defChange is one setting of a resident's definition that a write changes:
// a runner config key, or the flag of a field (--dir). Old and New are the
// values as printed, "" for none.
type defChange struct{ Key, Old, New string }

// defChanges lists what saving def over old changes, the fields first and
// the config keys by name. The order of --on is not a change.
func defChanges(old, def store.Resident) []defChange {
	var out []defChange
	add := func(key, was, is string) {
		if was != is {
			out = append(out, defChange{key, was, is})
		}
	}
	kinds := func(on []string) string {
		on = slices.Clone(on)
		slices.Sort(on)
		return strings.Join(on, ",")
	}
	add("--runner", old.Runner, def.Runner)
	add("--dir", old.Dir, def.Dir)
	add("--on", kinds(old.On), kinds(def.On))
	add("--max-age", old.MaxAge, def.MaxAge)
	add("--handoff-grace", old.HandoffGrace, def.HandoffGrace)
	keys := sortedKeys(old.Config)
	for _, k := range sortedKeys(def.Config) {
		if _, had := old.Config[k]; !had {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		was, had := old.Config[k]
		is, has := def.Config[k]
		// Compared as printed, which is how a value reads after the store:
		// a list given as []string and read back as []any is the same list.
		a, b := settingText(was, had), settingText(is, has)
		if had != has || a != b {
			if secretSetting(k) {
				a, b = hidden(had), hidden(has)
			}
			out = append(out, defChange{k, a, b})
		}
	}
	return out
}

// settingText is a config value as a change prints it: a string as it is,
// anything else as JSON.
func settingText(v any, set bool) string {
	if !set {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// secretSetting reports whether a config key is named like one that holds
// a secret. No setting yardr reads does (env_file names a file, and
// env_command is stored to be shown), but a definition takes any key, and
// what a change prints ends up in a terminal's scrollback and an agent's
// transcript.
func secretSetting(key string) bool {
	k := strings.ToLower(key)
	for _, part := range []string{"token", "secret", "password", "passwd", "credential", "apikey", "api_key"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return k == "key" || strings.HasSuffix(k, "_key")
}

func hidden(set bool) string {
	if !set {
		return ""
	}
	return "(a value, not shown)"
}

// changeLines prints changes one to a line, indented: "model: opus -> sonnet".
func changeLines(changes []defChange) string {
	lines := make([]string, len(changes))
	for i, ch := range changes {
		lines[i] = fmt.Sprintf("  %s: %s -> %s", ch.Key, or(ch.Old, "(not set)"), or(ch.New, "(dropped)"))
	}
	return strings.Join(lines, "\n")
}

// mayAllowOpening refuses a definition that lets its resident open threads
// (store.OpenThreads) in a source it could not before, unless the operator
// is the one saving it: speaking first in a source is theirs to grant, and
// a resident's session granting it to itself, or to another resident,
// would make the setting mean nothing. A definition that keeps or narrows
// what was allowed is anyone's to save, as the rest of it is. Like
// maySpeakAs this is a line, not a wall.
func (a *app) mayAllowOpening(ctx context.Context, st store.API, def store.Resident, set bool) error {
	return a.mayAllow(ctx, st, def, sourceSetting{set: set, key: store.OpenThreads, does: "open threads", may: store.Resident.MayOpen, able: hooks.Openable})
}

// mayAllowState is mayAllowOpening for store.SetState: moving an issue
// others work from is the operator's to grant as well.
func (a *app) mayAllowState(ctx context.Context, st store.API, def store.Resident, set bool) error {
	return a.mayAllow(ctx, st, def, sourceSetting{set: set, key: store.SetState, does: "set the status of threads", may: store.Resident.MaySetState, able: hooks.Stateable})
}

// mayAllowMail is mayAllowOpening for store.MailPeers: which other yards a
// resident writes to is the operator's to grant too, and a peer it names
// must be one of this yard's.
func (a *app) mayAllowMail(ctx context.Context, st store.API, def store.Resident, set bool) error {
	return a.mayAllowPeers(ctx, st, def, sourceSetting{set: set, key: store.MailPeers, does: "mail residents", may: store.Resident.MayMail})
}

// mayAllowRelease is mayAllowMail for store.ReleasePeers: whose beads a
// resident may release when they arrive held is the operator's to grant,
// since releasing one starts this yard's agents on another yard's text.
func (a *app) mayAllowRelease(ctx context.Context, st store.API, def store.Resident, set bool) error {
	return a.mayAllowPeers(ctx, st, def, sourceSetting{set: set, key: store.ReleasePeers, does: "release the beads", may: store.Resident.MayRelease})
}

// mayAllowPeers is mayAllow for a setting that names linked yards.
func (a *app) mayAllowPeers(ctx context.Context, st store.API, def store.Resident, set sourceSetting) error {
	if _, given := def.Config[set.key]; !given {
		return nil
	}
	ps, err := st.Peers(ctx)
	if err != nil {
		return err
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	set.able, set.peers = func() []string { return names }, true
	return a.mayAllow(ctx, st, def, set)
}

// mayDefine is every check a resident's definition passes before it is
// saved. With set, def holds only the settings yardr resident set was given,
// and the refusals name them as that command takes them.
func (a *app) mayDefine(ctx context.Context, st store.API, def store.Resident, set bool) error {
	if err := a.mayAllowOpening(ctx, st, def, set); err != nil {
		return err
	}
	if err := a.mayAllowState(ctx, st, def, set); err != nil {
		return err
	}
	if err := a.mayAllowMail(ctx, st, def, set); err != nil {
		return err
	}
	return a.mayAllowRelease(ctx, st, def, set)
}

// sourceSetting is a resident setting that names the sources it may do
// something in: its key, what it lets the resident do, how a definition
// reads it and the sources yardr can do it in. With peers it names linked
// yards instead, and able is the yard's peers. set says the command is
// yardr resident set, which takes the key bare and removes it with --unset.
type sourceSetting struct {
	key, does string
	may       func(store.Resident) []string
	able      func() []string
	peers     bool
	set       bool
}

func (a *app) mayAllow(ctx context.Context, st store.API, def store.Resident, set sourceSetting) error {
	may, able := set.may(def), set.able()
	what, in := "sources", "in"
	if set.peers {
		what, in = "peers", "of"
	}
	flag, none := "--set "+set.key, "leave it out"
	if set.set {
		flag, none = set.key, "--unset "+set.key
	}
	if _, given := def.Config[set.key]; given && len(may) == 0 {
		return fmt.Errorf("%s: want %s, comma-separated (%s); %s to allow none",
			flag, what, strings.Join(able, ","), none)
	}
	for _, src := range may {
		if !slices.Contains(able, src) && set.peers {
			return fmt.Errorf("%s: this yard has no peer %q (it has: %s)", flag, src, or(strings.Join(able, ", "), "none"))
		} else if !slices.Contains(able, src) {
			return fmt.Errorf("%s: yardr cannot %s in %q (it can in %s)", flag, set.does, src,
				strings.Join(able, ", "))
		}
	}
	var before []string
	switch old, err := st.GetResident(ctx, def.Name); {
	case err == nil:
		before = set.may(*old)
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	added := slices.DeleteFunc(slices.Clone(may), func(s string) bool { return slices.Contains(before, s) })
	if len(added) == 0 {
		return nil
	}
	operator, is, err := a.operator(ctx, st)
	if err != nil || is {
		return err
	}
	return fmt.Errorf("%s: only the operator (%s) may let %s %s %s %s; this is %s",
		flag, operator, def.Name, set.does, in, strings.Join(added, ", "), a.who())
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

// envSource is one place a resident's environment comes from, as resident
// show says it: the setting, the path or command line it names, the key
// names it sets, or why the runner would refuse it. Never a value.
type envSource struct {
	Setting string // env_file, env_command
	From    string
	Keys    []string
	Error   string
}

func (e envSource) fields() map[string]any {
	name := "path"
	if e.Setting == "env_command" {
		name = "command"
	}
	out := map[string]any{name: e.From}
	if e.Error != "" {
		out["error"] = e.Error
	} else {
		out["keys"] = e.Keys
	}
	return out
}

// residentEnvSummary is what a session of r would start with from its
// env_file and its env_command (which is run to tell: see
// config.ReadResidentEnv), and the key names the two set together; home is
// the yard's. A source that is refused sets none.
func residentEnvSummary(ctx context.Context, r store.Resident, home string) ([]envSource, []string) {
	env, _ := config.ReadResidentEnv(ctx, r.Name, r.Dir, home, r.Config)
	text := func(err error) string {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	var out []envSource
	if env.File != "" {
		out = append(out, envSource{"env_file", env.File, env.FileKeys, text(env.FileErr)})
	}
	if env.Command != "" {
		out = append(out, envSource{"env_command", env.Command, env.CommandKeys, text(env.CommandErr)})
	}
	return out, slices.Concat(env.FileKeys, env.CommandKeys)
}

// sourceCredential is the credential a resident answers in a source with,
// by kind and by the names its environment sets for it: never a value.
type sourceCredential struct {
	Kind  string   `json:"kind"` // GitHub App, token, API key; "none" without one
	Names []string `json:"names,omitempty"`
	// Note says what is amiss: one of an App's two names without the other,
	// or that the resident is someone in the source and cannot answer there.
	Note string `json:"note,omitempty"`
}

func (c sourceCredential) String() string {
	out := c.Kind
	if len(c.Names) > 0 {
		out += " (" + strings.Join(c.Names, ", ") + ")"
	}
	if c.Note != "" {
		out += ": " + c.Note
	}
	return out
}

// credentialSummary says, for each source a credential can be given for,
// which kind the resident's environment (keys, the names its env_file and
// env_command set: residentEnvSummary) gives it. A
// source it has neither a credential nor an identity in is left out: it is
// not one the resident speaks in.
func credentialSummary(r store.Resident, keys []string) map[string]sourceCredential {
	out := map[string]sourceCredential{}
	for _, src := range hooks.Repliable() {
		ways := hooks.CredentialWays(src)
		if way, ok := hooks.CredentialSet(src, keys); ok {
			out[src] = sourceCredential{Kind: way.Kind, Names: way.Names}
			continue
		}
		// Some of a way's names without the rest give no credential, and
		// say what was meant.
		var part []string
		for _, w := range ways {
			for _, n := range w.Names {
				if slices.Contains(keys, n) {
					part = append(part, n)
				}
			}
		}
		_, known := r.Identities[src]
		switch {
		case len(part) > 0:
			out[src] = sourceCredential{Kind: "none", Names: part, Note: "it takes " + hooks.CredentialNames(src)}
		case known:
			out[src] = sourceCredential{Kind: "none", Note: "it cannot answer in " + src + " until its env_file or env_command sets " + hooks.CredentialNames(src)}
		}
	}
	return out
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	// The limit is counted in characters, and the cut falls where one starts:
	// cut by bytes, a line of multi-byte characters ends inside one and prints
	// as "�…".
	n := 0
	for i := range line {
		if n == 100 {
			return line[:i] + "…"
		}
		n++
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
	var ack, mail, notes bool
	var limit int
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Unread events and mail: a resident's, or your own as a user",
		Long: `Show a resident's unacknowledged events, oldest first. --ack marks everything shown as handled.

Run as a user (your actor is a user's name, and neither --resident nor
$YARDR_RESIDENT names a resident) it shows your inbox in two parts, each
under its name and unread count:

  Mail           what a resident or a person wrote to you: the unread,
                 oldest first, then the latest you have read
  Notifications  what the yard tells you of the events you subscribed to
                 (yardr subscribe <you> ...): one line each, in the
                 mail's order, with nobody to answer

--mail and --notifications show one part. --ack marks the unread shown as
read, in the parts shown only: with --notifications your mail stays
unread, and with --mail your notifications do. Nothing nudges a person:
yardr doctor and bare yardr say what waits.`,
		Example: `  yardr inbox
  yardr inbox --mail --ack
  yardr inbox --notifications -n 50 --ack`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if resident == "" && config.Getenv("RESIDENT") == "" {
				u, err := a.actingUser(c.Context(), st)
				if err != nil {
					return err
				}
				if u == nil {
					return fmt.Errorf("--resident is required (or set YARDR_RESIDENT): %s is no user, who would read their own (yardr user list)", a.who())
				}
				// Neither flag is both parts, as both flags are.
				return a.userInbox(c.Context(), st, u.Name, limit, ack, mail || !notes, notes || !mail)
			}
			if mail || notes {
				return fmt.Errorf("--mail and --notifications are the parts of a user's inbox; a resident's has one")
			}
			name, err := currentResident(resident)
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
	cmd.Flags().BoolVar(&ack, "ack", false, "mark the events shown as handled (a user's: the unread shown as read, in the parts shown)")
	cmd.Flags().BoolVar(&mail, "mail", false, "a user's inbox: the mail only")
	cmd.Flags().BoolVar(&notes, "notifications", false, "a user's inbox: the notifications only")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many to show")
	return cmd
}

func printInboxEvent(e store.Event, resident string) {
	switch e.Kind {
	case "mail":
		from := store.MailSender(e)
		if store.FromOutside(e) {
			from += " (" + store.OutsideMail + ")"
		}
		fmt.Printf("%6d %s  mail from %s: %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), from, show(e.Data["text"]))
	case "hook":
		printHookDelivery(e, resident)
	case "timer":
		// The text says it is a timer, who set it and when.
		fmt.Printf("%6d %s  %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), show(e.Data["text"]))
	default:
		printBeadDelivery(e, resident)
	}
}

func (a *app) mailCmd() *cobra.Command {
	var with string
	var after uint64
	cmd := &cobra.Command{
		Use:   "mail <name>[@<peer>] <text|->",
		Short: "Send a message to the inbox of a resident or a user, or read a conversation back (--with)",
		Long: `Sends a message to the inbox of a resident or of a user (up to 4 KB; yardr chat
sends a longer text as several mails).

A user is a person of the yard (yardr user list), the operator among them.
A resident's session and a user mail a user; an agent working a bead does
not, its channel is the bead. Nothing nudges a person: they read with yardr
inbox.

--with <name> sends nothing and prints the mail between you and that resident
or user, both directions, oldest first: seq, time, whether its receiver has
read it (acknowledged it with yardr inbox --ack), sender and text. --after
<seq> prints only what is newer; --json prints a list of
{seq, at, from, to, text, read}. --with <resident>@<peer> prints the mail
between you and a resident of a linked yard; what you wrote says queued,
sent or failed (delivery in --json) instead, since the other yard does not
say what was read. It goes by the link, so a peer renamed since (yardr peer
rename) shows its mail from before under the name it has now.

<resident>@<peer> is a resident of a linked yard, <peer> being this yard's name
for the link (yardr peer list). That mail (up to 16 KB) is queued for the peer
and the daemon delivers it, so the command returns before it has arrived:
yardr peer show <peer> lists what still waits. The link must carry mail out
(yardr peer allow <peer> --send mail) and a resident must be allowed the peer
(yardr resident set <resident> peers=<peer>, the operator's to give). Anyone
else must be the operator. Mail the other yard does not take comes back to a
resident's inbox as mail from the yard that says why.

Mail from another yard is shown as from <name>@<peer> and marked "` + store.OutsideMail + `".
Answer it with yardr mail <name>@<peer>. Mail from a yard that is linked no
more (yardr peer rm) is shown as from "<name> at a yard no longer linked (was
<peer>)", with no address: a yard added under that name later is another yard.`,
		Example: `  yardr mail yardmaster "Deploys are frozen until Friday"
  yardr mail mckean "The refinery is red on main: yardr-1a2b"
  yardr mail --with yardmaster
  yardr mail --with yardmaster --after 412 --json
  yardr mail yardmaster@north "Our webapp rig is on Go 1.25 now"
  yardr mail --with yardmaster@north`,
		Args: func(c *cobra.Command, args []string) error {
			if with != "" {
				return cobra.NoArgs(c, args)
			}
			return cobra.ExactArgs(2)(c, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if with != "" {
				return a.conversation(ctx, st, with, after)
			}
			text := args[1]
			if text == "-" {
				if text, err = readAllStdin(); err != nil {
					return err
				}
			}
			to, peer, outside := strings.Cut(args[0], "@")
			if !outside {
				if _, err := st.GetUser(ctx, to); err == nil {
					if err := a.mayMailUser(to); err != nil {
						return err
					}
				} else if !errors.Is(err, store.ErrNotFound) {
					return err
				}
				if err := st.Mail(ctx, to, text, a.who()); err != nil {
					return err
				}
				return a.out(map[string]string{"to": to, "sent": "ok"}, func() { fmt.Printf("mail sent to %s\n", to) })
			}
			if err := a.mayMailPeer(ctx, st, peer); err != nil {
				return err
			}
			m, err := st.MailPeer(ctx, to, peer, text, a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]string{"to": to, "peer": peer, "id": m.ID, "sent": "queued"}, func() {
				fmt.Printf("mail for %s@%s queued; the daemon delivers it, and yardr peer show %s lists it while it waits\n", to, peer, peer)
			})
		},
	}
	cmd.Flags().StringVar(&with, "with", "", "print the mail between you and this resident or user, or <resident>@<peer>, instead of sending")
	cmd.Flags().Uint64Var(&after, "after", 0, "with --with: only mail newer than this seq")
	return cmd
}

// mayMailPeer refuses mail to a peer's resident from anyone who is neither
// a resident (whose definition the store then holds it to) nor the
// operator: a bead's agent has no say in what leaves the yard. Like
// maySpeakAs it is a line, not a wall.
func (a *app) mayMailPeer(ctx context.Context, st store.API, peer string) error {
	switch _, err := st.GetResident(ctx, a.who()); {
	case err == nil:
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	operator, is, err := a.operator(ctx, st)
	if err != nil || is {
		return err
	}
	return fmt.Errorf("only a resident allowed %s (yardr resident set <resident> %s=%s) or the operator (%s) may mail another yard; this is %s",
		peer, store.MailPeers, peer, operator, a.who())
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
	v := map[string]any{"prime": p, "role": role}
	op, err := primeOperator(ctx, st)
	if err != nil {
		return err
	}
	if op != nil {
		v["operator"] = op
	}
	return a.out(v, func() {
		fmt.Printf("# yardr resident: %s\n\n", name)
		fmt.Printf("You are `%s`, an always-on resident of this yard, working in %s. ", name, or(r.Dir, "the yard home"))
		fmt.Printf("Your yardr commands are attributed to you (YARDR_ACTOR=%s).\n\n", name)
		if role != "" {
			fmt.Printf("## Your role\n\n%s\n\n", role)
		}
		if op != nil {
			fmt.Printf("## The operator: %s\n\n%s\n\n", op.Name, strings.Join(op.lines(), "\n\n"))
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
		if len(p.Timers) > 0 {
			fmt.Printf("## Timers: %d live (yardr timer list, yardr timer rm <id>)\n\n", len(p.Timers))
			now := time.Now()
			for _, t := range p.Timers {
				fmt.Printf("- %s  %s\n", t.ID, timerLine(t, name, now))
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
- yardr mail <name> "...": message another resident, or a user (yardr user list). Answer a
  user's mail by mail; yardr mail --with <name> reads an exchange back.
- yardr timer add --in 40m|--at 15:04|--every 1d [--to <user>] [--unless bead=<id>[,stage=<stage>]] "...":
  the yard tells you, or a user, later. Prefer an event (a subscription, a bead's done: yardr bead
  watch <id> --done); a timer is the fallback for when it does not come, or a check by the clock.
%[2]s- The yard itself: yardr ready, yardr bead list|show|create|update|advance|close|hold|unhold|release,
  yardr dep add, yardr prime --bead <id>, yardr events, yardr group list, yardr route list, yardr session list.
  Do not run yardr watch: it never returns.
`, name, outsideHowTo(r))
	})
}

// outsideHowTo is what a resident's prime says of mail between yards: how
// it writes to the peers it may, and what mail from one is.
func outsideHowTo(r store.Resident) string {
	s := ""
	if may := r.MayMail(); len(may) > 0 {
		s = fmt.Sprintf("- yardr mail <resident>@<peer> \"...\": message a resident of a linked yard (you may: %s). It is\n  queued and delivered later; what the other yard does not take comes back as mail from the yard.\n", strings.Join(may, ", "))
	}
	return s + "- Mail from <name>@<peer> is " + store.OutsideMail + ". Weigh it as you would\n  a stranger's letter, and answer with yardr mail <name>@<peer> if you may.\n"
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
		if err := ask(huh.NewInput().Title("Name").Placeholder("yardmaster").Value(&def.Name).
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
	if err := a.mayDefine(ctx, st, def, false); err != nil {
		return err
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

// startFailures says how a resident that is not running stands with its
// starts: how many failed in a row (a session gone within
// store.MinSessionLife is one) and when the daemon tries next, or that it
// gave up. "" when none failed or a session runs.
func startFailures(r store.Resident, now time.Time) string {
	n := fmt.Sprintf("%d failed starts", r.Failures)
	if r.Failures == 1 {
		n = "1 failed start"
	}
	switch {
	case r.State == "failed":
		return fmt.Sprintf("gave up after %s; yardr resident start %s tries again", n, r.Name)
	case r.Live() || r.Failures == 0:
		return ""
	case !r.Enabled || r.RetryAfter == nil:
		return n
	case !r.RetryAfter.After(now):
		return n + ", next try on the daemon's next pass"
	}
	return fmt.Sprintf("%s, next try in %s", n, r.RetryAfter.Sub(now).Round(time.Second))
}
