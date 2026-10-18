package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hook", Short: "Webhook endpoints that turn deliveries into hook events"}

	var h store.Hook
	var maps, says, makes []string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a webhook endpoint POST /hooks/<name> (a replacement stays in a terminal)",
		Long: `A hook receives webhooks from one source on the yardr server's hook listener,
which runs when the yard has an address for it: yardr settings set hook_addr
127.0.0.1:8790, or YARDR_HOOK_ADDR in the environment the server starts in,
which is used instead of the setting. The server reads the address when it
starts. A session the yard starts and a script group's command do not inherit
YARDR_HOOK_ADDR, so a server started from one does not listen there. The listener serves POST /hooks/<name>, the yard's peers under
/peers/ (yardr peer --help), and nothing else. Each verified delivery becomes one 'hook' event; its raw body is
kept for 'yardr hook show <seq>'. A redelivery (the same Linear or GitHub body,
or a repeated Linear-Delivery, X-GitHub-Delivery or Yard-Delivery id) is
answered 200 and dropped.

The secret stays in --secret-file and is read on every delivery, so rotating it
is rewriting the file. It must be a regular file of mode 0600 or stricter, as
an env_file must: a file that is not is refused here, and one that stops being
so later blocks the hook (503 to the sender, 'blocked' in 'yardr hook list')
until it is fixed. Sources:
  token   a shared bearer token (Authorization: Bearer <token>)

or a declared source with a hook key (yardr source --help): the secret its
webhooks are signed with, verified as the file says. GitHub's is the
webhook's secret (X-Hub-Signature-256 HMAC); content type application/json.

A hook can also move the issues beads track (yardr link). --map
<type>:<stage>=<state> says that when a bead of that type enters that stage,
the issue of this hook's source it tracks goes to started, review or done, as
yardr thread state names them (on GitHub also label:<name>, which adds the
label to the issue or pull request and is never removed); --as names the crew member it is done as, and a says outcome is reported as that crew member too, so the table's mapping crosses a stage of people they belong to. No
agent takes part: the daemon makes the call with that crew member's credential,
from its env_file, and only where the crew member's definition allows it
(yardr crew set <crew member> set_state=<source>, which only the operator
may grant), so a map does nothing until the operator has allowed it. An issue is
only ever moved forward: one that stands at or past the status is left
alone. See docs/links.md.

The other way round, the source's events move the bead the event is about:
the one that tracks its thread, else the train of its branch (docs/links.md).
--says "<key>=<value> ... -> <outcome>" is a line of the hook's table: an event
the words before the arrow match reports that outcome for the bead, as yardr
bead done --outcome does, and the bead's flow routes it. The words are a
subscription's --where (yardr subscribe --help: all must hold, a key given
twice holds either value), and two more keys, stage and type: the stage the
bead is in and its type, for a line that holds in some stages or for some
types only. The event's own type is named with event there. The first line
an event matches counts, so give them in that order. ": <words>"
after the outcome begins the note the move adds. A line whose outcome no stage
of a flow in the yard's depots has an edge for is refused (for a line with
type, no stage of the flows beads of that type follow). What the yard did
itself (an event whose actor is a crew member's identity in the source) says
nothing. A hook without a table moves nothing, and each event about a train
says so (yardr events).

An event about a thread no open bead tracks can make the bead that tracks it,
so that an issue or a pull request from outside enters the yard. --makes
"<key>=<value> ... -> <type>:<stage>[@<depot>]" is a line of the hook's other
table: an event the words before the arrow match (as for --says, without
stage and without type, which the line's bead already says) makes a bead of
that type in that stage, with the thread's title, the event's text and a
tracks link to the thread, as hook:<name>. The event's own type is named
with event there. The first line
an event matches counts. The depot is the line's, else the one depot whose
origin remote is the repository the event is in; an event no depot is found
for makes nothing, and yardr events says why. A closed bead that tracks the
thread does not stand in the way: the new bead takes the link, and the closed
one keeps a relates link. A line is refused when the type's flow in the depot
has no such stage a bead can be made in (without a depot: in none of the
yard's). What the yard did itself makes nothing.

add replaces the definition: give the whole map and every --says and --makes
again to change a line. The crew member must exist and have an identity in the source
(yardr crew identity), which keeps the source's delivery of the change out
of its inbox.`,
		Example: `  yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret
  yardr hook add gh --source github --secret-file ~/.yardr/github.secret \
      --says "event=pull_request action=closed merged=true -> merged: PR merged" \
      --says "event=pull_request_review action=submitted state=changes_requested sender=user -> changes"
  yardr hook add gh --source github --secret-file ~/.yardr/github.secret \
      --makes "event=issues action=opened -> issue:triage" \
      --makes "event=pull_request action=opened action=ready_for_review -> task:triage@webapp" \
      --says "event=issues action=closed -> dropped"
  yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --as yardmaster \
      --map task:new=started --map task:review=review --map task:merged=done \
      --map train:open=started --map train:done=done`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := checkSource(c.Context(), st, h.Source); err != nil {
				return err
			}
			path, err := secretPath(h.SecretFile)
			if err != nil {
				return err
			}
			if h.Map, err = hookMap(c.Context(), st, a.home(), h.Source, maps); err != nil {
				return err
			}
			if len(h.Map) > 0 && h.As == "" {
				return fmt.Errorf("--map needs --as <crew member>: the issue is moved with that crew member's credential, under its identity")
			}
			if h.Says, err = hookSays(c.Context(), st, says); err != nil {
				return err
			}
			if h.Makes, err = hookMakes(c.Context(), st, makes); err != nil {
				return err
			}
			h.Name, h.SecretFile = args[0], path
			saved, err := st.PutHook(c.Context(), h, a.who())
			if err != nil {
				return err
			}
			return a.out(saved, func() {
				fmt.Printf("hook %s saved: POST /hooks/%s (%s)\n", saved.Name, saved.Name, saved.Source)
				if len(saved.Map) > 0 {
					fmt.Printf("moves tracked %s issues as %s: %s\n", saved.Source, saved.As, strings.Join(mapLines(saved.Map), " "))
					if r, err := st.GetCrew(c.Context(), saved.As); err == nil && !slices.Contains(r.MaySetState(), saved.Source) {
						fmt.Printf("note: %s may not set a status in %s yet, so nothing is moved: the operator allows it with yardr crew set %s %s=%s\n",
							saved.As, saved.Source, saved.As, store.SetState, strings.Join(append(r.MaySetState(), saved.Source), ","))
					}
				}
				if len(saved.Says) > 0 {
					fmt.Println("its events say of a bead:")
					for _, l := range store.SayLines(saved.Says) {
						fmt.Printf("  %s\n", l)
					}
				}
				if len(saved.Makes) > 0 {
					fmt.Println("its events make a bead for a thread none tracks:")
					for _, l := range store.MakeLines(saved.Makes) {
						fmt.Printf("  %s\n", l)
					}
				}
				if a.cfg == nil {
					return
				}
				if listening, text := receiver(c.Context(), a.cfg, st); !listening {
					fmt.Println("note: " + text)
				}
			})
		},
	}
	add.Flags().StringVar(&h.Source, "source", "", "source adapter: "+strings.Join(hooks.Sources(), ", "))
	add.Flags().StringVar(&h.SecretFile, "secret-file", "", "file holding the secret (never the secret itself)")
	add.Flags().StringVar(&h.As, "as", "", "crew member the hook moves tracked issues as (with --map) and reports says outcomes as")
	add.Flags().StringArrayVar(&maps, "map", nil, "<bead type>:<stage>=<state> (repeatable): a bead entering the stage moves the issue it tracks to "+strings.Join(hooks.States(), ", ")+", or on GitHub adds label:<name> to it")
	add.Flags().StringArrayVar(&says, "says", nil, `"<key>=<value> ... -> <outcome>[: <note>]" (repeatable, in order): an event of the source that matches reports the outcome for the bead it is about: the one that tracks its thread, else the train of its branch`)
	add.Flags().StringArrayVar(&makes, "makes", nil, `"<key>=<value> ... -> <type>:<stage>[@<depot>]" (repeatable, in order): an event of the source that matches, about a thread no open bead tracks, makes a bead of the type in the stage that tracks it, in the depot or the one whose origin is the event's repository`)
	_ = add.MarkFlagRequired("source")
	_ = add.MarkFlagRequired("secret-file")

	list := &cobra.Command{
		Use: "list", Short: "List hooks", Args: cobra.NoArgs,
		Long: `Lists the hooks: the path each is served on, its secret file, whether the
receiver takes its deliveries (STATE), and when it last accepted and last
refused one. A hook that used to deliver and has delivered nothing for
longer than its usual gap (the median interval between its accepted
deliveries over the last week, times three, with a floor of an hour) reads
quiet there instead of ok, with how long nothing came and the gap.

LAST REFUSED is the time and the reason of the hook's last hook_rejected
event: unauthorized (no signature, a wrong one, or a timestamp too old),
bad_request, too_large, blocked (the secret file), secret (it is empty). A
hook that was last refused after it last accepted is not reached by its
source as it is set up; one with neither time has been sent nothing the
server saw. Refusals are logged at most once per hook and reason in ten
minutes, with a count, so the time can trail the last refusal by that long.
yardr events shows them as hook_rejected, also those sent to a name that is
no hook's (unknown_hook). See docs/subscriptions.md.`,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			hs, err := st.Hooks(c.Context())
			if err != nil {
				return err
			}
			now := hookNow()
			quiet, err := store.QuietGaps(c.Context(), st, hs, now)
			if err != nil {
				return err
			}
			out := make([]hookRow, 0, len(hs))
			for _, h := range hs {
				one := hookRow{Hook: h}
				if gap, ok := quiet[h.Name]; ok {
					one.Quiet = &hookQuietJSON{Silent: span(now.Sub(*h.Accepted)), Gap: span(gap)}
				}
				out = append(out, one)
			}
			return a.out(out, func() {
				rows := [][]string{}
				for _, h := range hs {
					state := "ok"
					if h.Blocked != "" {
						state = "blocked: " + h.Blocked
					}
					if gap, ok := quiet[h.Name]; ok {
						state = "quiet: nothing for " + span(now.Sub(*h.Accepted)) + " (usual gap " + span(gap) + ")"
					}
					moves := "-"
					if len(h.Map) > 0 {
						moves = "as " + h.As + ": " + strings.Join(mapLines(h.Map), " ")
					}
					accepted, refused := "-", "-"
					if h.Accepted != nil {
						accepted = since(*h.Accepted)
					}
					if h.Refused != nil {
						refused = since(*h.Refused) + " " + h.RefusedFor
					}
					rows = append(rows, []string{h.Name, h.Source, "/hooks/" + h.Name, h.SecretFile, state, accepted, refused, moves})
				}
				table("NAME\tSOURCE\tPATH\tSECRET FILE\tSTATE\tLAST ACCEPTED\tLAST REFUSED\tMOVES ISSUES", rows)
			})
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Short: "Delete a hook (events it accepted are kept)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.DeleteHook(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"hook": args[0], "rm": "ok"}, func() { fmt.Printf("%s: removed\n", args[0]) })
		},
	}

	show := &cobra.Command{
		Use:   "show <event seq|name>",
		Short: "Print the raw payload of a hook event, or a hook's definition",
		Long: `With the seq of a hook event, prints the delivery's raw body. With a hook's
name, prints its definition: the source, the secret file's path, the crew member
it acts as and its map, what its events say of a bead, and the issue moves it has still to make, each with
the last error when a try failed (see yardr hook add).`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			seq, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return a.showHook(c.Context(), args[0])
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			raw, err := st.HookPayload(c.Context(), seq)
			if err != nil {
				return err
			}
			var payload any = string(raw)
			if json.Valid(raw) {
				payload = json.RawMessage(raw)
			}
			return a.out(map[string]any{"seq": seq, "payload": payload}, func() {
				var pretty bytes.Buffer
				if json.Indent(&pretty, raw, "", "  ") == nil {
					raw = pretty.Bytes()
				}
				os.Stdout.Write(raw)
				if !bytes.HasSuffix(raw, []byte("\n")) {
					fmt.Println()
				}
			})
		},
	}

	cmd.AddCommand(add, list, rm, show)
	return cmd
}

// secretPath resolves the secret file and checks it is one the receiver
// will take (config.ReadSecretFile) and holds something, without ever
// showing what.
func secretPath(path string) (string, error) {
	secret, p, err := config.ReadSecretFile(path)
	if err != nil {
		return "", fmt.Errorf("--secret-file: %w", err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("--secret-file %s is empty", p)
	}
	return p, nil
}

// hookMap reads --map lines, <bead type>:<stage>=<state>, into a hook's map
// for source, and refuses a source whose issues yardr cannot move and a
// state the source has no status for. home is the yard's home, which a
// declared source's command programs run from.
func hookMap(ctx context.Context, st store.API, home, source string, lines []string) (map[string]string, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	d, _, err := declaredSource(ctx, st, source)
	if err != nil {
		return nil, err
	}
	f, ok := hooks.ForwarderFor(source, d, home, "")
	if !ok {
		return nil, fmt.Errorf("--map: yardr cannot move an issue in %s (it can in %s)", source, hooks.NamesOrNone(hooks.Forwardable(allDeclarations(ctx, st)...), ", "))
	}
	out := map[string]string{}
	for _, line := range lines {
		key, state, ok := strings.Cut(strings.TrimSpace(line), "=")
		typ, stage, colon := strings.Cut(key, ":")
		if !ok || !colon || typ == "" || stage == "" || state == "" {
			return nil, fmt.Errorf("--map %q: want <bead type>:<stage>=<state>, e.g. task:review=review", line)
		}
		if !hooks.Word(state) {
			return nil, fmt.Errorf("--map %s: %q is no state: want %s", line, state, strings.Join(hooks.States(), ", "))
		}
		if err := f.Places(state); err != nil {
			return nil, fmt.Errorf("--map %s: %w", line, err)
		}
		if was, dup := out[key]; dup && was != state {
			return nil, fmt.Errorf("--map %s: %s is mapped to %s already", line, key, was)
		}
		out[key] = state
	}
	return out, nil
}

// hookSays reads --says lines into a hook's table, in the order given. It
// refuses what a pack's check does: a line no bead of the yard's depots
// could take the outcome of, since the flow would refuse it at every event.
// A line with type is held to the flows beads of that type follow alone.
func hookSays(ctx context.Context, st store.API, lines []string) ([]store.Say, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	stages, err := depotOutcomes(ctx, st)
	if err != nil {
		return nil, err
	}
	var out []store.Say
	for _, line := range lines {
		say, err := store.ParseSay(line)
		if err != nil {
			return nil, fmt.Errorf("--says %w", err)
		}
		if typ, ok := say.Where[store.SayType]; ok {
			for _, t := range store.WhereValues(typ) {
				typed, err := depotOutcomesFor(ctx, st, t)
				if err != nil {
					return nil, err
				}
				if err := say.Unrouted(typed); err != nil {
					return nil, fmt.Errorf("--says %q: no bead of type %s of the yard's depots could take it: %w", line, t, err)
				}
			}
		} else if err := say.Unrouted(stages); err != nil {
			return nil, fmt.Errorf("--says %q: no bead of the yard's depots could take it: %w", line, err)
		}
		out = append(out, say)
	}
	return out, nil
}

// hookMakes reads --makes lines into a hook's table, in the order given. It
// refuses what a pack's check does: a line whose stage the type's flow has
// not, since the store would refuse the bead at every event. The flow is
// the one beads of the type follow in the line's depot; a line that names
// none is held to the yard's depots, and passes when one of them could
// take it.
func hookMakes(ctx context.Context, st store.API, lines []string) ([]store.Make, error) {
	var out []store.Make
	for _, line := range lines {
		m, err := store.ParseMake(line)
		if err != nil {
			return nil, fmt.Errorf("--makes %w", err)
		}
		in := []string{m.Depot}
		if m.Depot == "" {
			depots, err := st.Depots(ctx)
			if err != nil {
				return nil, err
			}
			in = nil
			for _, d := range depots {
				in = append(in, d.Name)
			}
			if len(in) == 0 {
				return nil, fmt.Errorf("--makes %q: the yard has no depot to make a bead in", line)
			}
		}
		var why []string
		for _, depot := range in {
			stages, governed, err := makeStages(ctx, st, depot, m.Type)
			if err == nil && governed {
				err = m.Unstaged(stages)
			}
			if err == nil {
				why = nil
				break
			}
			why = append(why, fmt.Sprintf("depot %s: %v", depot, err))
		}
		if why != nil {
			return nil, fmt.Errorf("--makes %q: no bead of type %s could be made there: %s", line, m.Type, strings.Join(why, "; "))
		}
		out = append(out, m)
	}
	return out, nil
}

// makeStages are the stages a bead of type typ can be made in in depot:
// those of the flow the type follows there that are not terminal. Where no
// flow governs them (governed is false) any stage will do.
func makeStages(ctx context.Context, st store.API, depot, typ string) (stages []string, governed bool, err error) {
	flows, err := st.DepotFlows(ctx, depot)
	if err != nil {
		return nil, false, fmt.Errorf("its flows: %w", err)
	}
	var f *store.Flow
	for _, df := range flows {
		if df.Flow != nil && (df.Type == typ || (df.Type == "" && f == nil)) {
			f = df.Flow
		}
	}
	if f == nil {
		return nil, false, nil
	}
	for _, stage := range f.Stages {
		if !f.IsTerminal(stage) {
			stages = append(stages, stage)
		}
	}
	return stages, true, nil
}

// depotOutcomes are the outcomes a bead can report, by the stage it is
// in, under any flow of the yard's depots: the bead an event is about may
// be of any type, so a line is held to every flow a depot has, its own and
// those for a type.
func depotOutcomes(ctx context.Context, st store.API) (map[string][]string, error) {
	depots, err := st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range depots {
		flows, err := st.DepotFlows(ctx, d.Name)
		if err != nil {
			return nil, fmt.Errorf("the flows of depot %s: %w", d.Name, err)
		}
		for _, df := range flows {
			addOutcomes(out, df.Flow)
		}
	}
	return out, nil
}

// depotOutcomesFor are the outcomes a bead of type typ can report, by the
// stage it is in, under the flow that type follows in each of the yard's
// depots: its own where it has one, else the depot's.
func depotOutcomesFor(ctx context.Context, st store.API, typ string) (map[string][]string, error) {
	depots, err := st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range depots {
		flows, err := st.DepotFlows(ctx, d.Name)
		if err != nil {
			return nil, fmt.Errorf("the flows of depot %s: %w", d.Name, err)
		}
		var own *store.Flow
		for _, df := range flows {
			if df.Type == typ && df.Flow != nil {
				addOutcomes(out, df.Flow)
				own = nil
				break
			}
			if df.Type == "" {
				own = df.Flow
			}
		}
		if own != nil {
			addOutcomes(out, own)
		}
	}
	return out, nil
}

func addOutcomes(out map[string][]string, f *store.Flow) {
	if f == nil {
		return
	}
	for _, stage := range f.Stages {
		if f.IsTerminal(stage) {
			continue
		}
		if _, has := out[stage]; !has {
			out[stage] = nil
		}
		for _, o := range f.OutcomesFrom(stage) {
			if !slices.Contains(out[stage], o.Name) {
				out[stage] = append(out[stage], o.Name)
			}
		}
	}
}

// mapLines are a hook's map as --map takes it, sorted.
func mapLines(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// showHook prints the hook's definition and the issue moves it has still to
// make.
func (a *app) showHook(ctx context.Context, name string) error {
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	h, err := st.GetHook(ctx, name)
	if err != nil {
		return err
	}
	all, err := st.IssueMoves(ctx)
	if err != nil {
		return err
	}
	moves := []store.IssueMove{}
	for _, m := range all {
		if m.Hook == name {
			moves = append(moves, m)
		}
	}
	return a.out(map[string]any{"hook": h, "issue_moves": moves}, func() {
		fmt.Printf("%s  %s  POST /hooks/%s\n", h.Name, h.Source, h.Name)
		fmt.Printf("  secret file %s\n", h.SecretFile)
		if h.Blocked != "" {
			fmt.Printf("  blocked: %s\n", h.Blocked)
		}
		if h.Accepted != nil {
			fmt.Printf("  last accepted a delivery %s\n", since(*h.Accepted))
		}
		if h.Refused != nil {
			fmt.Printf("  last refused one %s: %s\n", since(*h.Refused), h.RefusedFor)
		}
		if h.As != "" {
			fmt.Printf("  acts as %s\n", h.As)
		}
		for _, l := range mapLines(h.Map) {
			fmt.Printf("  map %s\n", l)
		}
		for _, l := range store.SayLines(h.Says) {
			fmt.Printf("  says %s\n", l)
		}
		for _, l := range store.MakeLines(h.Makes) {
			fmt.Printf("  makes %s\n", l)
		}
		for _, m := range moves {
			line := fmt.Sprintf("  to do: %s (%s in %s) -> %s %s", m.Bead, m.Type, m.Stage, m.URL, m.State)
			if m.Attempts > 0 {
				line += fmt.Sprintf("; tried %d times, next at %s: %s", m.Attempts, m.RetryAt.Local().Format("15:04:05"), m.Error)
			}
			fmt.Println(line)
		}
	})
}

// lastRefused is what doctor says of a hook whose last delivery was
// refused (store.Hook.LastRefused): it is set up, and its source is not
// getting through.
func lastRefused(h store.Hook) string {
	accepted := "it has accepted none"
	if h.Accepted != nil {
		accepted = "it last accepted one " + since(*h.Accepted)
	}
	return fmt.Sprintf("hook %s: its last delivery was refused (%s, %s); %s. Events of kind %s say how many",
		h.Name, h.RefusedFor, since(*h.Refused), accepted, store.HookRejected)
}

// receiver says whether the yard's server takes webhooks and on what, with
// the commands that change it as they are typed for this home.
func receiver(ctx context.Context, cfg *config.Config, st store.API) (listening bool, text string) {
	all, err := st.Settings(ctx)
	if err != nil {
		return false, "the hook_addr setting could not be read: " + err.Error()
	}
	l, known := hooks.ReadListener(cfg)
	listening, text = hooks.Listens(l, known, all[hooks.FromSetting])
	return listening, strings.ReplaceAll(text, "yardr ", yardrAt(cfg.Home)+" ")
}

// hookAddrTakes is what yardr settings set hook_addr says after the value:
// when the server takes it, and that the variable is used instead where the
// server runs with one.
func hookAddrTakes(cfg *config.Config) string {
	text := fmt.Sprintf("The server takes it at its next start: %s server stop, and the next command starts it again.", yardrAt(cfg.Home))
	if l, known := hooks.ReadListener(cfg); known && l.From == hooks.FromVariable {
		text += "\n" + variableWins(l)
	}
	return text
}

// variableWins says that the server was started with the variable, which
// the hook_addr setting does not change: said wherever the setting is named
// as what to change.
func variableWins(l hooks.Listener) string {
	return fmt.Sprintf("The running server was started with %s=%s, which is used instead of the setting while it is set there.", hooks.FromVariable, l.Addr)
}
