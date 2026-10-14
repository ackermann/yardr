package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadAskCmd() *cobra.Command {
	var askTo, askWhy string
	var askContext, askOptions, askAbout []string
	var askRecommend int
	var askNew bool
	var askBead store.NewBead
	var askSecret, askForUser, askRun, askSecretLogin string
	var askEnv []string
	var askTakeBack bool
	ask := &cobra.Command{
		Use:   "ask <id> <question> | ask --new <question> | ask --take-back <id> <ask> <reason>",
		Short: "Ask someone a question as data: options to pick, answered, put aside or declined, or taken back",
		Long: `Asks a person a question as data, on any bead in any stage: the question in
one line, who must answer, two to five lines of context, the options each
with its consequence in a line, the one recommended and why, and the other
beads it is about. The person answers by picking an option, puts the
question aside, or declines it (yardr bead answer); the answer is the
option, so it cannot be misread.

The ask is a note of kind ask; its text is JSON, one schema: question, to,
context, options (a label and a consequence each), recommend and why,
about. prime, review and chat render it as numbered text with the
recommendation marked and its state; nobody reads the raw form.

yardr adds the option "You decide" to every ask that has options; the
writer does not write it and cannot leave it out. Picking it records that
the person left the question to the asker, who decides and notes what it
took.

An ask with no --option wants words; it is answered with --words. An ask
needs --context (at least one line; write two to five) and a consequence
on every option, so the card is never empty; both are refused without.
--to defaults to the yard's operator. --recommend names an option's
number with --why saying why in a line; without them no option is
marked.

--to <person>@<peer> asks a person of a linked yard: the ask is stored on
the bead as any ask, and queued for the peer's yard in the same write,
where it stands among the person's decisions; their pick comes back onto
the bead. The link must carry asks out (yardr peer allow <peer> --send
ask). That is all a user of the yard needs; a crew member must be allowed
the peer as well (yardr crew set <crew member> peers=<peer>), and an
agent working a bead asks no other yard. The daemon delivers it, so the
command returns before it has arrived: yardr peer show <peer> lists what
still waits.

An ask for a user of this yard is also sent to each name of a linked yard
the operator gave reach for that user (yardr peer allow <peer> --reach
<name>@<peer>=<user>), whoever asks: it stands among their decisions there
too, and the answer from there lands on the bead as one given here does.
The command names where it was queued for.

--secret <source> asks the operator for the credential of a declared
source: an API key or a token, never a login. The ask says where its
answer goes and the answer is no words: the person types the value where
nothing shows it (yardr bead answer <id> <ask> --secret), the yard's server
writes it into the file the source's credential is read from, and the
answer on the bead holds the names that were given and never the value. So
the value is in no note, event, mail, brief or pane; an agent that needs a
key asks for it this way and never has the person paste it. The names are
the variables the source's declaration names for its credential; --env
narrows the ask to some of them. The asker names no path and invents no
name. --context says where the person gets the value. The card's line "a
secret: <names>, for the source <source> (<where its calls go>)" is the
yard's, from the declaration, whatever the question says.

A secret ask takes no --option, --recommend or --about. It is refused, and
nothing is stored, for a source the yard does not have (the operator adds
it: asking gives the asker nothing), for one whose declaration names no
credential, for --to anyone but the operator, and for --to <person>@<peer>:
nothing of a secret crosses a link, and no copy goes to a linked yard.

--secret <source> --for-user <user> asks for that user's own address in a
source that tells people instead: the one variable the source's notify key
names (notify.address.env), which the yard
reads from the user's env_file at each send. It is asked of the user, which
is where --to then defaults, or of the operator for them, and of nobody
else. The value typed goes into the user's env_file, or with none set into
~/.yardr/users/<user>.env, which is then set as yardr user set sets it. It
is --for-user beside --secret because the source still names the variable
and where the value is sent, and the user only whose file it is kept in.
Giving it does not say the user is told through the source: the card and
the answer name the command that does (yardr user set <user>
notify=<source>). It takes no --env, and is refused for a source with no
notify key and for a user the yard does not have.

--secret-login <login> asks the operator for the input of the harness login
that waits under that reference: the login its leader's input need names.
The value typed goes to the waiting login and is kept nowhere, not even in
the answer, which holds the names as any giving does. It is asked of the
operator, takes no --env and no --for-user, and is refused when no login
waits under the reference at the giving. The yardmaster asks one per input
need of a login it relays.

--run "<command>" proposes one yardr command that is the operator's to
give, for the operator to approve: a crew member that needs it asks this
way instead of mailing the person a line to paste into a terminal. The
command is written as it is typed, with or without the leading yardr, and
is one of a fixed list:

  peer allow <peer> --mail-limit <n>
      the link takes more mail in an hour
  source set <source> calls_per_hour=<n>|updates_per_hour=<n>
      one caller makes that many calls of the source in an hour, or that
      many updates among them; a lower number too, which is the operator's
  source grant <source> read|<call> --group <group>|--crew <crew member>
      a group's sessions or a crew member may make the source's read
      calls, or one call by its name; both flags repeat, and it is one
      command and one change
  subscribe <user> yardr [--done] [--where <key>=<value>]
      a user is told of the yard's own events in their inbox; with --done,
      --where or both
  user set <user> tell=<parts>
      which parts a source tells a user of outside the yard, on their
      phone: a list of notifications, mail and asks with commas between,
      or nothing after tell= for all three again

Anything else is typed by the operator: a flag of peer allow that is not
--mail-limit, a source's env_file and mint_command, source add, a user's
notify and env_file, and every other command. The last two rows are a
user's own as they are the operator's, since a crew member does not decide
what a person is told: the ask is for that user or for the operator
(--to), and the one asked approves it. The yard splits the command at
spaces and nowhere else, so nothing is quoted: a quote, a backslash, ;, |,
&, $ or a line break is refused, and a comma anywhere but in the parts of
tell=. It checks the
command against the yard as it stands, and refuses one the yard would
refuse and one that would change nothing (a peer, source, group, crew
member or user the yard has not, a number out of range, the limit that
stands, a grant or a subscription that is held already, the parts a user
is told of already; a lower mail limit needs no approval, anyone sets it). The ask is stored with the command in the yard's own form and with the yard's
sentences for what it changes, made from the yard's records, whatever the
question says. The card shows the command whole and those sentences, and
each --context line with the asker's name before it. Nothing runs until
the operator approves (yardr bead answer <id> <ask> --approve), and then
the yard runs exactly the command shown, as the operator; an approval is
refused when the yard changed since the ask, which is then asked again:
what the command changes is not what the card says it is, or the source,
the link, the group or the crew member it names was replaced or given
another config under its name.

A run ask takes no --option, --recommend or --about, and at most five
--context lines: why the asker wants it. It is refused, and nothing is
stored, for --to anyone but the operator (for a user's subscription or
tell, that user or the operator) and for --to <person>@<peer>; no copy goes to a
linked yard. A crew member or a user writes one. An agent
working a bead does not: it says what it needs in a note on the bead.

--new asks a question that is about no bead: it takes the question alone,
makes a bead titled with it and the ask on that bead in one write, and
prints both. An ask that would be refused makes no bead. -b gives the bead
a body and --label its labels (a topic: label is where the question shows
among the person's decisions). The bead is of the type question: it exists
to carry the question, so nothing routes it, no agent is started on it and
no group is told of it, in whatever stage it stands. Whoever asked closes
it when the answer is on it, with what they did with the answer as the
reason (yardr bead close). Without --depot it is of no depot and under no
flow, in the stage new; with --depot it stands where that depot's beads
start. --stage places the bead instead, as bead create does and as a task:
it is routed from that stage like any bead (a depot's decide stage, where
the group of people that stage is routed to holds it), and a stage the
depot's flow does not have is refused.

--take-back takes an open ask back: yardr bead ask --take-back <id> <ask>
"<reason>", for a question that is asked no more (it was settled another
way, or asked again in other words). It is the asker's to do, and the
operator's; anyone else is refused, since nobody ends a question in the
person's place. The ask leaves the person's open decisions wherever it
stands and shows among the decided ones as "taken back by <who>: <reason>",
never as an answer of theirs; the bead's notes say the same. It takes no
answer afterwards: a pick that arrives late is refused with who took it
back and why. An ask that is answered, declined or taken back already is
refused; one put aside is taken back as an open one is. Where the ask was
also sent to a linked yard (--to <person>@<peer>, or by reach) that yard is
told in the same write, and it leaves the decisions there with "it was
taken back in <yard>'s yard". It is a flag of ask, not of answer: answer is
what the person asked does, and this is the asker's own.`,
		Example: `  yardr bead ask yardr-n8ta "Move this yard's links to the declared source now?" \
    --to mckean \
    --context "The links file declares the new source; 64 links still point at the old one." \
    --context "The move was tested on a copy just now and passed; the old source goes away Friday." \
    --option "Move them now: all 64 links point at the new source tonight" \
    --option "After Friday: keep the old source until the release is out" \
    --recommend 1 --why "the test passed; waiting only risks Friday's rush"
  yardr bead ask yardr-n8ta "Move them now?" --to mckean@airy \
    --context "The links file declares the new source." \
    --option "Move them now: all 64 links point at the new source tonight"
  yardr bead ask yardr-k3vd "The API key for the CRM" --secret crm \
    --context "Create one at https://crm.example.com/account/api (read only)."
  yardr bead ask yardr-k3vd "Your id at the phone service" --secret phone --for-user mckean \
    --context "It is on the first page of the service after login."
  yardr bead ask yardr-k3vd "The code the sign-in page showed" --secret-login claude-a1b2c3d4 \
    --context "The login waits for what the sign-in page showed."
  yardr bead ask yardr-k3vd "Take more mail from airy?" \
    --context "airy's yard was refused 4 mails in the last hour: the link takes 30." \
    --run "peer allow airy --mail-limit 300"
  yardr bead ask --new "Rename 'depot'?" --label topic:words \
    --context "The glossary keeps it as the yard's word for a repository." \
    --option "Keep depot: nothing changes" \
    --option "Rename it: every command, page and document changes once"
  yardr bead ask --take-back yardr-n8ta 2913 "the design went back to draft; a new question follows"`,
		Args: func(c *cobra.Command, args []string) error {
			if askTakeBack {
				// It asks nothing: no flag of a question goes with it.
				var other []string
				c.Flags().Visit(func(f *pflag.Flag) {
					if f.Name != "take-back" {
						other = append(other, "--"+f.Name)
					}
				})
				if len(other) > 0 {
					return fmt.Errorf("--take-back takes the bead, the ask and why, and nothing of a question: not %s", strings.Join(other, ", "))
				}
				if len(args) != 3 {
					return fmt.Errorf("--take-back takes the bead, the ask and why: %s --take-back <id> <ask> \"<reason>\" (got %d arguments)", c.CommandPath(), len(args))
				}
				return nil
			}
			if askNew {
				if len(args) != 1 {
					return fmt.Errorf("--new takes the question alone: it makes the bead the question stands on (got %d arguments)", len(args))
				}
				return nil
			}
			for _, name := range []string{"body", "depot", "stage", "label"} {
				if c.Flags().Changed(name) {
					return fmt.Errorf("--%s is for the bead --new makes; the bead %s names is changed with yardr bead update", name, c.CommandPath())
				}
			}
			return cobra.ExactArgs(2)(c, args)
		},
		PreRunE: func(c *cobra.Command, args []string) error {
			if len(askEnv) > 0 && askSecret == "" && !c.Flags().Changed("secret-login") {
				return fmt.Errorf("--env narrows a secret ask: it goes with --secret <source>")
			}
			switch {
			case askForUser != "" && askSecret == "" && !c.Flags().Changed("secret-login"):
				return fmt.Errorf("--for-user says whose own address in a source is asked for: it goes with --secret <source>")
			case askForUser != "" && len(askEnv) > 0:
				return fmt.Errorf("--for-user takes no --env: the source's notify key names the one variable that is a user's address")
			case c.Flags().Changed("secret") && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--secret asks for a source's credential and --secret-login for a waiting login's input: one of the two")
			case c.Flags().Changed("secret-login") && len(askEnv) > 0:
				return fmt.Errorf("--secret-login takes no --env: a login input is the one name it is asked for")
			case c.Flags().Changed("secret-login") && askForUser != "":
				return fmt.Errorf("--secret-login takes no --for-user: a login input is the operator's login's to take")
			case c.Flags().Changed("run") && c.Flags().Changed("secret"):
				return fmt.Errorf("--run proposes a command and --secret asks for a value: one of the two")
			case c.Flags().Changed("run") && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--run proposes a command and --secret-login asks for a value: one of the two")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if askTakeBack {
				// A bead@peer ref is the store's to refuse, with whose the
				// question is to take back.
				id := args[0]
				if !strings.Contains(id, "@") {
					b, err := st.GetBead(c.Context(), id)
					if err != nil {
						return err
					}
					id = b.ID
				}
				seq, err := strconv.ParseUint(args[1], 10, 64)
				if err != nil || seq == 0 {
					return fmt.Errorf("ask %q: want the ask's seq, as prime, review and chat print it", args[1])
				}
				if err := st.TakeBackAsk(c.Context(), id, seq, args[2], a.who()); err != nil {
					return err
				}
				return a.out(map[string]any{"bead": id, "ask": seq, "state": askState(c.Context(), st, id, seq)},
					func() { printAsk(c.Context(), st, id, seq) })
			}
			question := args[len(args)-1]
			var b *store.Bead
			if !askNew {
				if b, err = st.GetBead(c.Context(), args[0]); err != nil {
					return err
				}
			}
			who := askTo
			if who == "" {
				// Their own address is theirs to give before it is the
				// operator's.
				who = askForUser
			}
			if who == "" {
				operator, _, err := a.operator(c.Context(), st)
				if err != nil {
					return err
				}
				who = operator
			}
			var options []store.AskOption
			for _, o := range askOptions {
				label, consequence, ok := strings.Cut(o, ":")
				if !ok {
					return fmt.Errorf("--option %q: want \"<label>: <consequence>\"", o)
				}
				options = append(options, store.AskOption{Label: label, Consequence: consequence})
			}
			q := store.Ask{
				Question: question, To: who, Context: askContext,
				Options: options, Recommend: askRecommend, Why: askWhy, About: askAbout,
			}
			if c.Flags().Changed("secret") {
				q.Secret = &store.AskSecret{Source: askSecret, Env: askEnv, User: askForUser}
			}
			if c.Flags().Changed("secret-login") {
				q.Secret = &store.AskSecret{Login: askSecretLogin}
			}
			if c.Flags().Changed("run") {
				// As typed, in one string: the yard splits and parses it.
				q.Run = &store.AskRun{Argv: []string{askRun}}
			}
			var seq uint64
			if askNew {
				nb := askBead
				nb.Priority = defaultPriority
				if nb.Body == "-" {
					if nb.Body, err = readAllStdin(); err != nil {
						return err
					}
				}
				// One call, so one write: a server from before it knows
				// no such call and makes nothing.
				made, err := st.AskNew(c.Context(), nb, q, a.who())
				if err != nil {
					return err
				}
				b, seq = made.Bead, made.Ask
			} else if seq, err = st.AddAsk(c.Context(), b.ID, q, a.who()); err != nil {
				return err
			}
			out := map[string]any{"bead": b.ID, "ask": seq, "to": who}
			if askNew {
				out["created"] = true
			}
			// Where the yard queued a copy of it, by the rule the write
			// that stored it went by (store.AskReach).
			var also []string
			// What the yard stored of a secret ask: the names are its own,
			// from the source's declaration.
			var secret *store.AskSecret
			if q.Secret != nil {
				if secret, err = askedSecret(c.Context(), st, b.ID, seq); err != nil {
					return err
				}
				out["secret"] = secret
			}
			// What the yard stored of a run ask: the command in its own
			// form, which is what an approval runs.
			var run *store.AskRun
			if q.Run != nil {
				if run, err = askedRun(c.Context(), st, b.ID, seq); err != nil {
					return err
				}
				out["run"] = run
			}
			if !strings.Contains(who, "@") && secret == nil && run == nil {
				peers, err := st.Peers(c.Context())
				if err != nil {
					return err
				}
				for _, t := range store.AskReach(peers, who, a.who()) {
					also = append(also, t.Address())
				}
				if len(also) > 0 {
					out["also"] = also
				}
			}
			return a.out(out, func() {
				if run != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					fmt.Printf("%s for %s: to approve, %s\n", on, who, run.Command())
					return
				}
				if secret != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					if secret.Login != "" {
						fmt.Printf("%s for %s: a secret, %s for the waiting login %s\n", on, who, secret.Names(), secret.Login)
						return
					}
					if secret.User != "" {
						fmt.Printf("%s for %s: a secret, %s, %s's own address in the source %s\n", on, who, secret.Names(), secret.User, secret.Source)
						return
					}
					fmt.Printf("%s for %s: a secret, %s of the source %s\n", on, who, secret.Names(), secret.Source)
					return
				}
				printAsk(c.Context(), st, b.ID, seq)
				if _, peer, outside := strings.Cut(who, "@"); outside {
					fmt.Printf("ask for %s queued; the daemon delivers it, and yardr peer show %s lists it while it waits\n", who, peer)
				}
				switch {
				case askNew && len(also) > 0:
					fmt.Printf("%s created; ask %d on it for %s; also queued for %s\n", b.ID, seq, who, strings.Join(also, ", "))
				case askNew:
					fmt.Printf("%s created; ask %d on it for %s\n", b.ID, seq, who)
				case len(also) > 0:
					fmt.Printf("ask %d on %s for %s; also queued for %s\n", seq, b.ID, who, strings.Join(also, ", "))
				}
			})
		},
	}
	ask.Flags().StringVar(&askTo, "to", "", "who must answer (default the yard's operator)")
	ask.Flags().StringArrayVar(&askContext, "context", nil, "what the question is about, a line (repeatable; write two to five, at least one)")
	ask.Flags().StringArrayVar(&askOptions, "option", nil, "an option as \"<label>: <consequence>\" (repeatable; none wants words)")
	ask.Flags().IntVar(&askRecommend, "recommend", 0, "the recommended option's number, with --why")
	ask.Flags().StringVar(&askWhy, "why", "", "why the recommended option, in a line")
	ask.Flags().StringSliceVar(&askAbout, "about", nil, "the other beads the question is about")
	ask.Flags().StringVar(&askSecret, "secret", "", "ask for the credential of this declared source: answered with a value typed unseen, never with words")
	ask.Flags().StringVar(&askSecretLogin, "secret-login", "", "ask for the input of the harness login waiting under this reference (its leader's input need names it): handed to the login, kept nowhere")
	ask.Flags().StringVar(&askForUser, "for-user", "", "with --secret: ask for this user's own address in the source (the variable its notify key names), kept in the user's env_file")
	ask.Flags().StringVar(&askRun, "run", "", "propose this yardr command for the operator to approve, as it is typed: one of a fixed list (yardr bead ask --help)")
	ask.Flags().StringSliceVar(&askEnv, "env", nil, "with --secret: only these variables of the source's credential (default every one its declaration names)")
	ask.Flags().BoolVar(&askTakeBack, "take-back", false, "take an open ask back instead of asking: <id> <ask> \"<reason>\" (the asker's, or the operator's)")
	ask.Flags().BoolVar(&askNew, "new", false, "the question is about no bead: make one titled with it, and the ask on it, in one write")
	// The bead of --new, by bead create's flags and defaults.
	ask.Flags().StringVarP(&askBead.Body, "body", "b", "", "with --new: the bead's description (- reads stdin)")
	ask.Flags().StringVarP(&askBead.Depot, "depot", "d", "", "with --new: the depot the bead is of (default none: under no flow, in stage new)")
	ask.Flags().StringVarP(&askBead.Stage, "stage", "s", "", "with --new: place the bead in this stage as a task, routed as bead create's is (decide, for a depot's); default a question nothing routes")
	ask.Flags().StringSliceVarP(&askBead.Labels, "label", "l", nil, "with --new: the bead's labels")
	return ask
}

func (a *app) newBeadAnswerCmd() *cobra.Command {
	var ansWords, ansDecline string

	var ansLater, ansSecret, ansApprove bool
	answer := &cobra.Command{
		Use:   "answer <id> <ask> [<n> [words]]",
		Short: "Answer an ask: pick an option, answer with words, give a secret, approve a command, put it aside, or decline it",
		Long: `Answers an ask on a bead: picks option <n> with any words, answers with
words alone, puts the ask aside, or declines it. The answer is a note of
kind answer naming who picked which option; a newer pick supersedes the
older one for display, and every pick stays as a note. Nothing is deleted.

  yardr bead answer <id> <ask> <n> [words]      pick option n with words alongside
  yardr bead answer <id> <ask> --words "<text>"   answer an ask with no options
  yardr bead answer <id> <ask> --later             put it aside for 24 hours
  yardr bead answer <id> <ask> --decline "<words>"   refuse to decide it
  yardr bead answer <id> <ask> --secret            give the secret a secret ask asks for
  yardr bead answer <id> <ask> --approve           approve the command a run ask proposes

<ask> is the ask note's seq, as prime, review and chat print it. <n> counts
the ask's options as shown, "You decide" last.

Words alone on an ask with options are refused. They go as a plain note
(yardr bead note) and leave the ask open. Talk about the question stays
where talk lives, in the conversation with the asker.

--later leaves the person's queue for 24 hours or until the asker's next
note on the bead, whichever is first; answering early is a pick like any
other. --decline ends the ask with no option picked. Wherever the ask is
shown it reads "<user> declined to decide this: <words>". A declined ask
and an ask answered with words take no further answer; a picked ask takes
only a newer pick.

--secret answers a secret ask (yardr bead ask --secret), as yardr source
key gives a credential: on a terminal each name the ask asks for is asked
for in turn and nothing is shown as it is typed; when standard input is no
terminal it is read as NAME=value lines, one for each name. The value is
never an argument and never a variable of the command's environment. The
yard's server writes it into the file the source's credential is read
from, and the answer on the bead says who gave which names: the value is
in that file and nowhere else. It is the operator's to give, and is
refused from the pane of an agent. An ask for a user's own address in a
source (yardr bead ask --secret <source> --for-user <user>) is that user's
to give, or the operator's, into the user's env_file, and names the
command that tells the user through the source when that is still to be
run. An ask for a waiting login's input (yardr bead ask --secret-login
<login>) is handed to the login and kept nowhere. Giving again replaces
what was given.
When the yard wrote the file and could not record it, it says so with the
names and the file: give it again. A secret ask takes no pick and no
words, which are refused and kept nowhere; --later puts it aside as any
ask, and it is declined with --decline "", without words.

--approve answers a run ask (yardr bead ask --run): the yard runs the
command the ask holds, exactly as its card shows it, as the operator, and
records on the bead what ran and what it printed. It is the operator's to
give and nobody else's, and is refused from the pane of an agent whatever
--actor says. A user's own subscription, and which parts they are told
of, is approved by the one it was asked of, that user or the operator, and runs as them. A command runs once for one ask: an ask that was approved,
declined or taken back takes no approval. It is refused too, and nothing
runs, when the yard is no longer what the card was written from: what the
command changes was changed in between, or what it names was replaced
under its name. The ask is then declined and asked again. What the command
itself would refuse at a terminal is refused in the same words. A run ask
takes no pick and no words, which are refused; --later puts it aside and
--decline "<words>" ends it, as for any ask. The card names the command
that takes the change back and who may run it: anyone, but for a source's
limits (the operator) and a user's subscription (the user or the
operator).

An ask another yard asked is answered as <bead>@<peer>, the ref the
decisions queue shows: the answer is queued home for that yard instead
of written as a note, and the question stands decided here from then
on. Only the person the question is for answers it, the link must carry
asks out (yardr peer allow <peer> --send ask), and --later there is 24
hours by this yard's clock and nothing else. Words alone on an ask with
options go as mail to the asker instead (yardr mail <asker>@<peer>).`,
		Args: cobra.RangeArgs(2, 4),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// A bead@peer ref names a question another yard asked: it
			// stands on no bead of this yard, so there is no bead to
			// read, and the answer is queued home instead of noted.
			id := args[0]
			if _, _, foreign := strings.Cut(id, "@"); !foreign {
				b, err := st.GetBead(c.Context(), id)
				if err != nil {
					return err
				}
				id = b.ID
			}
			askSeq, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil || askSeq == 0 {
				return fmt.Errorf("ask %q: want the ask's seq, as prime, review and chat print it", args[1])
			}
			declined := c.Flags().Changed("decline")
			if ansSecret {
				if len(args) > 2 || ansWords != "" || ansLater || declined {
					return fmt.Errorf("--secret gives the secret and takes nothing else: the value is typed, or read from standard input as NAME=value lines")
				}
				return a.giveSecret(c, st, id, askSeq)
			}
			if ansApprove {
				if len(args) > 2 || ansWords != "" || ansLater || declined {
					return fmt.Errorf("--approve runs the command the ask holds and takes nothing else")
				}
				done, err := st.ApproveAsk(c.Context(), id, askSeq, a.who())
				if err != nil {
					return err
				}
				return a.out(done, func() {
					ran := store.AskRan{Argv: done.Argv}
					fmt.Printf("ask %d, asked by %s: %s\n", done.Ask, done.Asker, ran.Command())
					for _, does := range done.Does {
						fmt.Println(does)
					}
					fmt.Printf("%s approved it, and the yard ran it:\n%s\n", a.who(), done.Result)
				})
			}
			ans := store.Answer{Ask: askSeq}
			if len(args) >= 3 {
				n, err := strconv.Atoi(args[2])
				if err != nil || n < 1 {
					return fmt.Errorf("%q is not an option number: yardr bead answer %s %d <n> [words], or --words, --later, --decline",
						args[2], id, askSeq)
				}
				ans.Option = n
				ans.Words = ansWords
				if len(args) == 4 {
					if ansWords != "" {
						return fmt.Errorf("words after the option and --words: one of the two")
					}
					ans.Words = args[3]
				}
				if ansLater || declined {
					return fmt.Errorf("an option with --later or --decline: pick, put aside, or decline, one of the three")
				}
			} else {
				switch {
				case ansLater && (ansWords != "" || declined):
					return fmt.Errorf("--later puts the ask aside with nothing else")
				case declined && ansWords != "":
					return fmt.Errorf("--decline ends the ask with its own words, not --words")
				case !ansLater && !declined && ansWords == "":
					return fmt.Errorf("nothing to record: pick <n> [words], answer with --words, put it aside with --later, or decline it")
				}
				ans.Words, ans.Later, ans.Decline = ansWords, ansLater, declined
				if ans.Decline {
					ans.Words = ansDecline
				}
			}
			if err := st.AddAnswer(c.Context(), id, ans, a.who()); err != nil {
				return err
			}
			return a.out(map[string]any{"bead": id, "ask": askSeq, "state": askState(c.Context(), st, id, askSeq)}, func() {
				printAsk(c.Context(), st, id, askSeq)
			})
		},
	}
	answer.Flags().StringVar(&ansWords, "words", "", "words alone, answering an ask with no options (or with a pick, in place of words after it)")
	answer.Flags().BoolVar(&ansLater, "later", false, "put the ask aside for 24 hours, or until the asker's next note on the bead")
	answer.Flags().StringVar(&ansDecline, "decline", "", "end the ask with no option picked: why you will not decide it (\"\" on a secret ask, which takes no words)")
	answer.Flags().BoolVar(&ansApprove, "approve", false, "approve the command a run ask proposes: the yard runs it as you, exactly as the card shows it")
	answer.Flags().BoolVar(&ansSecret, "secret", false, "give the secret a secret ask asks for: typed unseen, or NAME=value lines on standard input")
	return answer
}

// printAsk prints the ask seq on the bead as prime shows it: numbered
// text with its state. The write went through; what follows only decides
// the confirmation, so a bead that cannot be read now is said plainly.
// askedSecret reads the secret the ask seq on the bead id asks for, as the
// yard stored it.
func askedSecret(ctx context.Context, st store.API, id string, seq uint64) (*store.AskSecret, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.Secret == nil {
			return nil, fmt.Errorf("ask %d asks for no secret: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, id, seq)
		}
		return r.Ask.Secret, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// askedRun reads the command the ask seq on the bead id proposes, as the
// yard stored it.
func askedRun(ctx context.Context, st store.API, id string, seq uint64) (*store.AskRun, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.Run == nil {
			return nil, fmt.Errorf("ask %d proposes no command", seq)
		}
		return r.Ask.Run, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// giveSecret is yardr bead answer --secret: it reads the value of each name
// the ask asks for as yardr source key reads one, unseen from a terminal or
// as NAME=value lines from anything else, and gives them to the yard. What
// it prints is who gave which names and where the credential is set, never
// a value. A login input is read the same way, with no source to read it
// against, and handed to the waiting login.
func (a *app) giveSecret(c *cobra.Command, st store.API, id string, seq uint64) error {
	secret, err := askedSecret(c.Context(), st, id, seq)
	if err != nil {
		return err
	}
	if secret.Login != "" {
		values, err := readLoginKeys(secret, c.InOrStdin(), typedUnseen(c.InOrStdin()))
		if err != nil {
			return err
		}
		keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who())
		if err != nil {
			return secretErr(err)
		}
		return a.out(keyed, func() {
			fmt.Printf("%s gave %s · it went to the waiting login %s, and is kept nowhere\n", a.who(), strings.Join(keyed.Given, " and "), keyed.Login)
		})
	}
	src, err := st.GetSource(c.Context(), secret.Source)
	if err != nil {
		return err
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	values, err := readKeys(d, secret.Env, c.InOrStdin(), typedUnseen(c.InOrStdin()))
	if err != nil {
		return err
	}
	keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who())
	if err != nil {
		return secretErr(err)
	}
	return a.out(keyed, func() {
		line := fmt.Sprintf("%s gave %s · it is set in %s", a.who(), strings.Join(keyed.Given, " and "), keyed.File)
		if keyed.Missing != "" {
			line += " · " + keyed.Missing
		}
		fmt.Println(line)
		if keyed.Next != "" {
			fmt.Println("next: " + keyed.Next)
		}
	})
}

// readLoginKeys reads the values a login-input secret ask asks for, as
// readKeys reads a source's: unseen from a terminal, or as NAME=value lines
// from anything else. There is no declaration to read them against: the ask
// names the one. No error holds a value.
func readLoginKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	values := map[string]string{}
	if typed == nil {
		env, err := config.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				// The input is intended to be NAME=value lines, but do not
				// repeat an unexpected name: a caller may have put the
				// secret there by mistake.
				return nil, fmt.Errorf("the waiting login %s: standard input sets a name that was not asked for (%s)", secret.Login, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the waiting login %s: standard input has no line %s=<value>", secret.Login, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the waiting login %s: standard input has no NAME=value line, and nothing is handed over", secret.Login)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s for the waiting login %s (nothing is shown as you type): ", n, secret.Login))
		if err != nil {
			return nil, fmt.Errorf("the waiting login %s: reading %s: %w", secret.Login, n, err)
		}
		// What a paste brings along at its ends is no part of an input.
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the waiting login %s: nothing was typed for %s, and nothing is handed over", secret.Login, n)
		}
	}
	return values, nil
}

func printAsk(ctx context.Context, st store.API, id string, seq uint64) {
	if _, _, foreign := strings.Cut(id, "@"); foreign {
		printForeignAsk(ctx, st, id, seq)
		return
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		fmt.Printf("ask %d on %s\n", seq, id)
		return
	}
	as := trustMarks(ctx, st)
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			lines := r.Lines()
			if as != nil {
				lines = r.LinesMarked(as)
			}
			fmt.Println(strings.Join(lines, "\n"))
			return
		}
	}
	fmt.Printf("ask %d on %s\n", seq, id)
}

// printForeignAsk prints the ask seq of a bead@peer ref as the answer's
// confirmation: the question with its state as this yard knows it. The
// question stands on no bead of this yard, so there is no numbered text
// for it here; the card is in the decisions UI. The question, To and
// asker are the other yard's writer words, so each stands folded to one
// line, as the local card's one-line slots do, and none starts a line of
// its own.
func printForeignAsk(ctx context.Context, st store.API, id string, seq uint64) {
	v, err := st.Bead(ctx, id)
	if err != nil {
		fmt.Printf("ask %d on %s\n", seq, id)
		return
	}
	for _, a := range v.Asks {
		if a.Ask == seq {
			fmt.Printf("ask %d · %s · to %s · asked by %s\n%s\n",
				a.Ask, a.State, store.OneLine(a.To), store.OneLine(a.Asker), store.OneLine(a.Question))
			return
		}
	}
	fmt.Printf("ask %d on %s\n", seq, id)
}

// askState is the ask's state as words, "" when the bead cannot be read
// now.
func askState(ctx context.Context, st store.API, id string, seq uint64) string {
	if _, _, foreign := strings.Cut(id, "@"); foreign {
		v, err := st.Bead(ctx, id)
		if err != nil {
			return ""
		}
		for _, a := range v.Asks {
			if a.Ask == seq {
				return a.State
			}
		}
		return ""
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return ""
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			return r.State.String()
		}
	}
	return ""
}
