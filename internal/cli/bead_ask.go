package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadAskCmd() *cobra.Command {
	var askTo, askWhy string
	var askContext, askOptions, askAbout []string
	var askRecommend int
	var askNew bool
	var askBead store.NewBead
	var askSecret, askForUser, askForCrew, askAs, askRun, askSecretLogin, askSecretHook, askHookSource string
	var askSetup string
	var askDepotPush, askDepotPushOff string
	var askCrew, askGroup []string
	var askSourceBase string
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
what the source needs given: the mint's inputs where the declaration names
a mint (credential.mint), else every variable of its credential; the mint's
names and the static ones are alternatives, not all required, so a minted
token is never asked for. --env narrows the ask to any names the
declaration has. The asker names no path and invents no name. --context
says where the person gets the value. The card's line "a
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

--secret <source> --for-crew <crew member> asks for that crew member's own
credential in the source, which a reply answers with: what --secret takes
(the mint's inputs where the declaration names a mint, else every variable
of its credential), which --env narrows to any names the declaration has.
The value typed goes into the crew member's env_file, or
with none set into ~/.yardr/crew/<name>.env, which is then set as yardr
crew set sets it. The crew member's running session does not see the new
value: it takes effect when it next starts (rotation or yardr crew restart
<name>), as the card and the answer say. It is --for-crew beside --secret
because the source still names the variables, and the crew member only
whose file they are kept in. It is asked of the operator only, and is
refused for a crew member the yard has not and for a source with no
credential. A crew member with an env_command as well keeps both: the
env_file line is the one written, and for a name both set the command's
value is what its session runs with.

--secret <source> --as <user> asks for that user's own credential in the
source, which a call made as them reads: every name under credential.mint
where the declaration names a mint, else the static ones (credential.env,
and credential.update_env where it names one), which --env narrows to any
name the declaration has. The value typed goes into the user's env_file,
or with none set into ~/.yardr/users/<user>.env, which is then set as
yardr user set sets it. It is --as beside --secret because the source
still names the variables, and the user only whose file they are kept in
and whose calls read them. It is asked of the user, which is where --to
then defaults, or of the operator for them, and of nobody else. It is
refused for a source with no credential and for a user the yard does not
have. It is not --for-user: that asks for the user's own address in a
source that tells people, and this for the credential a call made as them
reads; a source may have both to ask for.

--secret-login <login> asks for the input of the harness login that waits
under that reference: the login its leader's input need names. The value
typed goes to the waiting login and is kept nowhere, not even in the
answer, which holds the names as any giving does. It is asked of the
operator. With --as <user> the login is that user's own, led for the group
that carries it (yardr user add --login, yardr user login): the code its
sign-in page shows is the person's to read, so it is asked of them, where
--to then defaults, or of the operator for them, and they give it. It takes
no --env, no --for-user and no --for-crew. At the giving it is refused when
no login waits under the reference, and when the login that waits is
another user's than the one the ask is for (the --as user, else the
operator): the yard records whose login each wait is when its leader
registers it, from the group the leader was given, and a giving it refuses
leaves the wait waiting. The yardmaster asks one per input need of a login
it relays.

--secret-hook <hook> --source <source> asks the operator for the signing
secret of the hook <hook> of the declared source <source>: the value the
service signs its webhooks with, pasted from where the service shows it.
The yard's server writes it to the file it owns for the hook,
<home>/hooks/<hook>.secret (mode 0600), which the hook then names; the
value is in no note, event, mail, brief or pane, as for --secret. It is
asked of the operator, takes no --env, no --for-user, no --for-crew and
no --as, and is refused for a source the yard does not declare, one whose
declaration has no hook key, and a hook the yard has already. The hook
itself is a second
ask on the same bead (--run "hook add <hook> --source <source>"), asked
after this one: the operator gives the secret first, then approves the
hook, whose approval runs only with the secret standing in its file.

--run "<command>" proposes one yardr command that is the operator's to
give, for the operator to approve: a crew member that needs it asks this
way instead of mailing the person a line to paste into a terminal. The
command is written as it is typed, with or without the leading yardr, and
is one of the list the yard runs from a card:

  peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>
      the link carries kinds each way (ask, bead, file, mail, ping,
      thread), and the card names what it carries in and out after and
      before; or it takes more mail in an hour; --no-send and --no-receive
      ride along with a kind that is added, and a command that only stops
      one needs no approval; who a peer sees and is trusted and reached as
      stay in a terminal
  peer intake <peer> --depot <depot> --flow <flow> [--limit <n>] [--default]
      the beads the peer sends become beads of the depot, pinned to the
      flow, and the card names the depot, the flow, the stage they arrive
      in and who works it, which starts this yard's agents on the other
      yard's text; --none needs no approval
  peer identity <peer> <crew> --source <source> --id <id>
      the sender with the id in the source is the crew member of the peer's
      yard: a delivery from it is kept with the speaker <crew>@<peer>,
      information and not instructions, and it is no user of this yard;
      --rm and the listing stay in a terminal
  source set <source> calls_per_hour=<n>|updates_per_hour=<n>
      one caller makes that many calls of the source in an hour, or that
      many updates among them; a lower number too, which is the operator's
  source grant <source> read|<call>|--preset <preset> --group <group>|--crew <crew member> [--as <user>]
      a group's sessions or a crew member may make the source's read
      calls, or one call by its name; both flags repeat, and it is one
      command and one change; with --preset <name> the preset's calls
      of the pack that declares the source, named on the card with the
      pack; with --as <user> the call is made as that user, and that
      user alone approves it
  source add <file>
      a new source the file declares: the ask carries the declaration
      and each command file it names, the card shows the whole
      declaration, and a commands-free add runs at approval while one
      naming commands is carried out by the server after it; a
      replacement and an absolute program stay in a terminal
  source ignore <source> --rm <id>
      a sender ignored in the source is heard again: their next message
      follows the hook's word, a fresh mapping card under unknown, heard
      as an outsider under allow
  source check <source> --user <user>
      one real test message to the user through the source, sent when
      the user or the operator approves; a check without --user needs
      no approval and is run directly
  pack apply <name>
      a pack yardr embeds, with no flag: of kind source, or of a depot
      kind re-applied to the one depot that holds it where its plan
      asks nothing; the card is written from the server's own binary
      and shows each declared source whole, and for a depot pack each
      object and each changed file's diff; the approval is carried out
      by the server after it; a directory, URL or imported pack, any
      flag, a first install and a plan that asks stay in a terminal
  hook add <name> --source <declared source> [--unknown unknown|allow|ignore]
      a new webhook endpoint POST /hooks/<name> for the declared
      source, verified with its signing secret given on the hook's own
      secret card (--secret-hook) into the file the yard owns for it;
      for a source that hears by identity, --unknown names what an
      unmapped sender is (unknown without it); --map, --as and a
      replacement stay in a terminal
  subscribe <user> yardr [--done] [--where <key>=<value>]
      a user is told of the yard's own events in their inbox; with --done,
      --where or both
  user set <user> tell=<parts>
      which parts a source tells a user of outside the yard, on their
      phone: a list of notifications, mail and asks with commas between,
      or nothing after tell= for all three again
  user key <user> <source>
      the person's own address in the source, as yardr user key gives
      it: a card of this row is offered by the yard itself, when a
      delivery reaches it from an address no user has, and carries
      that address shown on the card; approving writes it into the
      user's env file (0600) exactly where yardr user key writes it.
      A --run naming it in a terminal carries no address and is
      refused: the value there is typed unseen, as yardr user key
      types it
  user add <name>
      a person becomes a user of the yard, and reads mail from now on
  user rm <name>
      a user goes, with their subscriptions and devices; the mail stays
  user invite <name> --email <address> [--role operator]
      a teammate is invited by email: a user is made (a member, or an
      operator with --role operator), the address is mapped to them in
      web_jwt_users, and the invite is reported to the edge the yard is
      connected to; the approval is carried out by the server after it,
      and there is no such command at a terminal: the card is the way in
  user uninvite <name>
      a teammate goes: the removal is reported to the edge first, then
      the user, their devices and the mapping go; a plain user, whom no
      mapping names, is removed with user rm instead
  crew set <name> open_threads=<sources> | set_state=<sources> | peers=<peers>
      a crew member may open threads or set the status of threads in the
      sources, or mail crew members of the peers
  crew identity <name> --source <source> [--id <id>] [--key <key>]
      who the crew member is in the source: mentions and assignments of
      the id or key there are then addressed to it
  user identity <user> --source <source> [--id <id>] [--key <key>]
      who the user is in the source: threads opened there for them are
      then assigned to the id or key, and where the source hears by
      identity a delivery from it is heard as theirs; asked of them or
      of the operator, and --rm stays in a terminal
  settings set <key> <value>
      the yard's own setting changes; who the operator is and the settings
      of where the yard reads and serves from are the operator's alone,
      while hook_public_url binds nothing and is a run ask's
  peer rename <old> <new>
      the link is called by its new name; its key and what it carries stay
  bead update <bead> --originator <user>
      the bead is for the user
  timer rm <id>
      another's timer is removed; one's own needs no approval
  up
      everything the yard needs that is not running is started, and what
      runs is left alone; safe to repeat, so an approval is never stale
  crew restart <name> [--for <user>]
      a fresh session of the crew member starts once the current one is
      idle; with --for, of its conversation session for the user

Whether each is the operator's is the command's own check: when the asker
may run the command themselves the ask is refused with that. What is the
operator's alone is refused as a run ask: what defines what the yard runs
or whom it trusts (a command of a group or a source, who a peer sees and
is trusted and reached as, a device, another yard paired or removed or
called elsewhere), what takes a file path, and what reaches another yard.
Such a command is typed in a terminal. A card about a link (peer allow,
peer intake, peer identity) is approved from this yard, in a terminal or
from a device paired with it, and an approval that arrives through the
reach of the peer the card is about is refused: a peer does not widen its
own link, say where its own beads go or say who its own crew is here. The subscription, the tell, the user's identity and the test
message to them are a user's own as they are the operator's, since a
crew member does not decide what a person is told or who they are in
a source: the ask is for that user or for the operator (--to), and
the one asked approves it. The yard splits the command at spaces and nowhere else, so
nothing is quoted: a quote, a backslash, ;, |, &, $ or a line break is
refused, and a comma anywhere but in a list the command takes (the parts
of tell=, the kinds peer allow carries, the grants crew set gives). It checks the
command against the yard as it stands, and refuses one the yard would
refuse and one that would change nothing (a peer, source, group, crew
member, user, setting, timer or bead the yard has not, a number out of
range, what stands already, a grant or a subscription that is held
already, the parts a user is told of already; what the asker may run
needs no approval). The ask is stored with the command in the yard's own form and with the yard's
sentences for what it changes, made from the yard's records, whatever the
question says. The card shows the command whole and those sentences, and
each --context line with the asker's name before it. Nothing runs until
the operator approves (yardr bead answer <id> <ask> --approve), and then
the yard runs exactly the command shown, as the operator; an approval is
refused when the yard changed since the ask, which is then asked again:
what the command changes is not what the card says it is, or what it
names was replaced or set under its name since: the source, the link,
the group, the crew member, the user, the setting, the bead or the
timer.

A run ask takes no --option, --recommend or --about, and at most five
--context lines: why the asker wants it. It is refused, and nothing is
stored, for --to anyone but the operator (for a user's subscription,
tell, identity or test message, that user or the operator) and for
--to <person>@<peer>; no copy goes to a
linked yard. A crew member or a user writes one. An agent
working a bead does not: it says what it needs in a note on the bead.

--setup <pack> sets up an embedded pack of kind source as one decision:
the apply, the account and the grants. It takes --crew <name> and --group
<group>, repeatable, at least one: who the grants are for. --source-base
<name> calls the pack's region base <name>, for a pack whose source
declares bases; the card shows the base it will call. --recommend
counts the card's preset options as yardr pack show prints them, with
--why saying why, as on any option ask. The card shows the pack's
declaration whole with its mint, the account to type unseen or the listed
candidates to take, and one option per preset, each rendered by the yard
from the declarations with the grantee, and You decide last, which grants
the recommended preset. The operator approves with the preset pick and
the account together (yardr bead answer <id> <ask> <n> --secret, or
--account <source> where the card lists one); the yard then applies the
pack, sets the base where one was chosen, takes the account, and grants
the preset picked, as the operator.
An approval is refused when the yard changed since the ask, which is then
asked again. A pack past 40 lines, one carrying sources under different
credentials, and one with no presets stay on the existing per-step flow:
a pack apply card, an account card per source, and a grant card per
source and kind. --as is refused: a grant as a user is that user's alone
to give, and this card is the operator's.

A setup ask takes no --option, --about or --as, and at most five
--context lines. It is refused, and nothing is stored, for --to anyone
but the operator and for --to <person>@<peer>; no copy goes to a linked
yard. A crew member or a user writes one. An agent working a bead does
not: it says what it needs in a note on the bead.

--depot-push <depot> turns one depot's push to its origin on as one
decision, and --depot-push-off <depot> turns it off again: the yard's
server, on the operator's approval, adds YARDR_PUSH=origin at the front
of the cmd of the group <depot>-assembly (the rest of the cmd standing
untouched, YARDR_LAND as it was) and points that depot's own git at the
credential helper of the source whose host its origin is on, for that
host alone, through git -C <depot> config; the off card takes exactly
those two back out. Which source that is, its host, its helper and
the variable its token is in are the pack's to say, read from its
declaration. The card carries the outside weight, is refused on a yard
with no edge, for a depot whose origin is not https on such a host,
with no token of that source standing, already fully on (or, off, not
on), with no <depot>-assembly group, or with YARDR_PUSH of another
value standing, and where a helper already answers for the host the
approval verifies it is the pack's helper answering with the source's
token and writes nothing. A carry that failed after one half is
completed by filing the card again. Nothing here runs git push, and
no token stands in the card, the cmd, the config or a log.

A depot-push ask takes no --option, --about or --as, and at most five
--context lines. It is refused, and nothing is stored, for --to anyone
but the operator and for --to <person>@<peer>; no copy goes to a linked
yard. A crew member or a user writes one. An agent working a bead does
not: it says what it needs in a note on the bead.

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
  yardr bead ask yardr-k3vd "The token it answers with" --secret phone --for-crew yardmaster \
    --context "Create one at https://phone.example.com/account/api."
  yardr bead ask yardr-k3vd "Your Google account for your mail?" --secret google-mail --as ada \
    --context "The yard reads your mail as you when it is granted it as you."
  yardr bead ask yardr-k3vd "The code the sign-in page showed" --secret-login claude-a1b2c3d4 \
    --context "The login waits for what the sign-in page showed."
  yardr bead ask yardr-k3vd "The signing secret for it" --secret-hook <hook> --source <source> \
    --context "It is shown once where the webhook is made in the service."
  yardr bead ask yardr-k3vd "Set up the webhook?" \
    --context "The signing secret is asked for on the ask above; give it first." \
    --run "hook add <hook> --source <source>"
  yardr bead ask yardr-k3vd "Set up Gmail for the yardmaster?" --setup google-mail --crew yardmaster \
    --context "The yardmaster reads mail to work beads; drafts only, the person sends." \
    --recommend 1 --why "reading is the job; drafts can follow"
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
			if len(askEnv) > 0 && askSecret == "" && !c.Flags().Changed("secret-login") && !c.Flags().Changed("secret-hook") {
				return fmt.Errorf("--env narrows a secret ask: it goes with --secret <source>")
			}
			switch {
			// A setup's own combinations first, so their reasons stand:
			// --as with --setup is refused as the card's, not as a
			// secret's missing flag.
			case c.Flags().Changed("setup") && c.Flags().Changed("run"):
				return fmt.Errorf("--setup sets up a pack and --run proposes a command: one of the two")
			case c.Flags().Changed("setup") && c.Flags().Changed("secret"):
				return fmt.Errorf("--setup sets up a pack and --secret asks for a value: one of the two")
			case c.Flags().Changed("setup") && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--setup sets up a pack and --secret-login asks for a value: one of the two")
			case c.Flags().Changed("setup") && c.Flags().Changed("secret-hook"):
				return fmt.Errorf("--setup sets up a pack and --secret-hook asks for a value: one of the two")
			case c.Flags().Changed("depot-push") && c.Flags().Changed("depot-push-off"):
				return fmt.Errorf("--depot-push turns a depot's push on and --depot-push-off turns it off: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && c.Flags().Changed("setup"):
				return fmt.Errorf("--depot-push turns a depot's push on or off and --setup sets up a pack: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && c.Flags().Changed("run"):
				return fmt.Errorf("--depot-push turns a depot's push on or off and --run proposes a command: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && c.Flags().Changed("secret"):
				return fmt.Errorf("--depot-push turns a depot's push on or off and --secret asks for a value: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--depot-push turns a depot's push on or off and --secret-login asks for a value: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && c.Flags().Changed("secret-hook"):
				return fmt.Errorf("--depot-push turns a depot's push on or off and --secret-hook asks for a value: one of the two")
			case (c.Flags().Changed("depot-push") || c.Flags().Changed("depot-push-off")) && askAs != "":
				return fmt.Errorf("--depot-push refuses --as: this card is the operator's")
			case c.Flags().Changed("setup") && askAs != "":
				return fmt.Errorf("--setup refuses --as: a grant as a user is that user's alone to give, and this card is the operator's")
			case c.Flags().Changed("setup") && askForUser != "":
				return fmt.Errorf("--for-user says whose own address in a source is asked for: it goes with --secret <source>, not --setup")
			case c.Flags().Changed("setup") && askForCrew != "":
				return fmt.Errorf("--for-crew says whose own credential in a source is asked for: it goes with --secret <source>, not --setup")
			case !c.Flags().Changed("setup") && (c.Flags().Changed("crew") || c.Flags().Changed("group")):
				return fmt.Errorf("--crew and --group name who a setup's grants are for: they go with --setup <pack>")
			case !c.Flags().Changed("setup") && c.Flags().Changed("source-base"):
				return fmt.Errorf("--source-base names the region base a setup's calls go to: it goes with --setup <pack>")
			case askForUser != "" && askSecret == "" && !c.Flags().Changed("secret-login"):
				return fmt.Errorf("--for-user says whose own address in a source is asked for: it goes with --secret <source>")
			case askForUser != "" && len(askEnv) > 0:
				return fmt.Errorf("--for-user takes no --env: the source's notify key names the one variable that is a user's address")
			case askForCrew != "" && askSecret == "" && !c.Flags().Changed("secret-login"):
				return fmt.Errorf("--for-crew says whose own credential in a source is asked for: it goes with --secret <source>")
			case askAs != "" && askSecret == "" && !c.Flags().Changed("secret-login"):
				return fmt.Errorf("--as says whose own credential in a source is asked for: it goes with --secret <source>, or whose login a waiting login is: --secret-login <login>")
			case askForUser != "" && askForCrew != "":
				return fmt.Errorf("--for-user asks for a user's own address and --for-crew for a crew member's credential: one of the two")
			case askForUser != "" && askAs != "":
				return fmt.Errorf("--for-user asks for a user's own address and --as for a user's own credential: one of the two")
			case askForCrew != "" && askAs != "":
				return fmt.Errorf("--for-crew asks for a crew member's credential and --as for a user's own credential: one of the two")
			case c.Flags().Changed("secret") && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--secret asks for a source's credential and --secret-login for a waiting login's input: one of the two")
			case c.Flags().Changed("secret") && c.Flags().Changed("secret-hook"):
				return fmt.Errorf("--secret asks for a source's credential and --secret-hook for a hook's signing secret: one of the two")
			case c.Flags().Changed("secret-login") && c.Flags().Changed("secret-hook"):
				return fmt.Errorf("--secret-login asks for a waiting login's input and --secret-hook for a hook's signing secret: one of the two")
			case c.Flags().Changed("secret-login") && len(askEnv) > 0:
				return fmt.Errorf("--secret-login takes no --env: a login input is the one name it is asked for")
			case c.Flags().Changed("secret-login") && askForUser != "":
				return fmt.Errorf("--secret-login takes no --for-user: a login input is the login's to take, not a user's address")
			case c.Flags().Changed("secret-login") && askForCrew != "":
				return fmt.Errorf("--secret-login takes no --for-crew: a login input is the login's to take, not a crew member's credential")
			case c.Flags().Changed("secret-hook") && len(askEnv) > 0:
				return fmt.Errorf("--secret-hook takes no --env: the signing secret is one value")
			case c.Flags().Changed("secret-hook") && askForUser != "":
				return fmt.Errorf("--secret-hook takes no --for-user: a signing secret is the operator's to give")
			case c.Flags().Changed("secret-hook") && askForCrew != "":
				return fmt.Errorf("--secret-hook takes no --for-crew: a signing secret is the operator's to give")
			case c.Flags().Changed("secret-hook") && askAs != "":
				return fmt.Errorf("--secret-hook takes no --as: a signing secret is the operator's to give")
			case c.Flags().Changed("secret-hook") && strings.TrimSpace(askSecretHook) == "":
				return fmt.Errorf("--secret-hook needs a hook name: lowercase letters, digits, - and _")
			case c.Flags().Changed("secret-hook") && askHookSource == "":
				return fmt.Errorf("--secret-hook needs --source <source>: the declared source the hook serves")
			case !c.Flags().Changed("secret-hook") && c.Flags().Changed("source"):
				return fmt.Errorf("--source names the hook's source: it goes with --secret-hook <hook>")
			case c.Flags().Changed("run") && c.Flags().Changed("secret"):
				return fmt.Errorf("--run proposes a command and --secret asks for a value: one of the two")
			case c.Flags().Changed("run") && c.Flags().Changed("secret-login"):
				return fmt.Errorf("--run proposes a command and --secret-login asks for a value: one of the two")
			case c.Flags().Changed("run") && c.Flags().Changed("secret-hook"):
				return fmt.Errorf("--run proposes a command and --secret-hook asks for a value: one of the two; ask for the hook's secret and the hook as two asks, the secret first")
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
				return a.out(c.OutOrStdout(), map[string]any{"bead": id, "ask": seq, "state": askState(c.Context(), st, id, seq)},
					func(w io.Writer) { printAsk(w, c.ErrOrStderr(), c.Context(), st, id, seq) })
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
				// Their own address or credential is theirs to give
				// before it is the operator's.
				who = askForUser
			}
			if who == "" {
				who = askAs
			}
			if who == "" {
				// The operator setting names whom an ask with no --to
				// goes to, whichever users hold the operator role.
				who = yardOperator(c.Context(), st)
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
				q.Secret = &store.AskSecret{Source: askSecret, Env: askEnv, User: askForUser, Crew: askForCrew, As: askAs}
			}
			if c.Flags().Changed("secret-login") {
				q.Secret = &store.AskSecret{Login: askSecretLogin, As: askAs}
			}
			if c.Flags().Changed("secret-hook") {
				q.Secret = &store.AskSecret{Source: askHookSource, Hook: askSecretHook}
			}
			if c.Flags().Changed("setup") {
				// As named, in the order typed: the yard stores them once
				// each, the groups before the crew members.
				var to []store.Grantee
				for _, g := range askGroup {
					to = append(to, store.Grantee{Group: g})
				}
				for _, cm := range askCrew {
					to = append(to, store.Grantee{Crew: cm})
				}
				q.Setup = &store.AskSetup{Pack: askSetup, To: to, Base: askSourceBase}
			}
			if c.Flags().Changed("depot-push") {
				q.DepotPush = &store.AskDepotPush{Depot: askDepotPush}
			}
			if c.Flags().Changed("depot-push-off") {
				q.DepotPush = &store.AskDepotPush{Depot: askDepotPushOff, Off: true}
			}
			if c.Flags().Changed("run") {
				// As typed, in one string: the yard splits and parses it.
				q.Run = &store.AskRun{Argv: []string{askRun}}
				// Source add carries its file: read once here, beside
				// the ask, so the store holds the bytes the card shows.
				carried, err := sourceAddCarried(askRun)
				if err != nil {
					return err
				}
				q.Run.Carried = carried
			}
			var seq uint64
			if askNew {
				nb := askBead
				nb.Priority = defaultPriority
				if nb.Body == "-" {
					b, err := io.ReadAll(c.InOrStdin())
					if err != nil {
						return err
					}
					nb.Body = strings.TrimRight(string(b), "\n")
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
			// What the yard stored of a setup ask: the pack with the
			// yard's sentences for it, which is what an approval runs.
			var setup *store.AskSetup
			if q.Setup != nil {
				if setup, err = askedSetup(c.Context(), st, b.ID, seq); err != nil {
					return err
				}
				out["setup"] = setup
			}
			var depotPush *store.AskDepotPush
			if q.DepotPush != nil {
				if depotPush, err = askedDepotPush(c.Context(), st, b.ID, seq); err != nil {
					return err
				}
				out["depot_push"] = depotPush
			}
			if !strings.Contains(who, "@") && secret == nil && run == nil && setup == nil && depotPush == nil {
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
			return a.out(c.OutOrStdout(), out, func(w io.Writer) {
				if run != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					fmt.Fprintf(w, "%s for %s: to approve, %s\n", on, who, run.Command())
					return
				}
				if secret != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					if secret.Hook != "" {
						fmt.Fprintf(w, "%s for %s: a secret, the signing secret for the hook %s (POST /hooks/%s)\n", on, who, secret.Hook, secret.Hook)
						return
					}
					if secret.Login != "" {
						whose := "the"
						if secret.As != "" {
							whose = secret.As + "'s"
						}
						fmt.Fprintf(w, "%s for %s: a secret, %s for %s waiting login %s\n", on, who, secret.Names(), whose, secret.Login)
						return
					}
					if secret.User != "" {
						fmt.Fprintf(w, "%s for %s: a secret, %s, %s's own address in the source %s\n", on, who, secret.Names(), secret.User, secret.Source)
						return
					}
					if secret.Harness != "" {
						label := secret.Harness
						if h, ok := agents.Lookup(secret.Harness); ok && h.Label != "" {
							label = h.Label
						}
						fmt.Fprintf(w, "%s for %s: a secret, %s, the API key %s runs with on the operator's login, asked for crew member %s\n", on, who, secret.Names(), label, secret.Crew)
						return
					}
					if secret.Crew != "" {
						fmt.Fprintf(w, "%s for %s: a secret, %s, for crew member %s in the source %s\n", on, who, secret.Names(), secret.Crew, secret.Source)
						return
					}
					if secret.As != "" {
						fmt.Fprintf(w, "%s for %s: a secret, %s of the source %s, as %s\n", on, who, secret.Names(), secret.Source, secret.As)
						return
					}
					fmt.Fprintf(w, "%s for %s: a secret, %s of the source %s\n", on, who, secret.Names(), secret.Source)
					return
				}
				if setup != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					fmt.Fprintf(w, "%s for %s: to approve, yardr bead answer %s %d <preset> --secret\n", on, who, b.ID, seq)
					return
				}
				if depotPush != nil {
					on := fmt.Sprintf("ask %d on %s", seq, b.ID)
					if askNew {
						on = fmt.Sprintf("%s created; ask %d on it", b.ID, seq)
					}
					verb := "on"
					if depotPush.Off {
						verb = "off"
					}
					fmt.Fprintf(w, "%s for %s: to approve, yardr bead answer %s %d --approve (turns push %s for depot %s)\n", on, who, b.ID, seq, verb, depotPush.Depot)
					return
				}
				printAsk(w, c.ErrOrStderr(), c.Context(), st, b.ID, seq)
				if _, peer, outside := strings.Cut(who, "@"); outside {
					fmt.Fprintf(w, "ask for %s queued; the daemon delivers it, and yardr peer show %s lists it while it waits\n", who, peer)
				}
				switch {
				case askNew && len(also) > 0:
					fmt.Fprintf(w, "%s created; ask %d on it for %s; also queued for %s\n", b.ID, seq, who, strings.Join(also, ", "))
				case askNew:
					fmt.Fprintf(w, "%s created; ask %d on it for %s\n", b.ID, seq, who)
				case len(also) > 0:
					fmt.Fprintf(w, "ask %d on %s for %s; also queued for %s\n", seq, b.ID, who, strings.Join(also, ", "))
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
	ask.Flags().StringVar(&askSecretHook, "secret-hook", "", "with --source: ask for the signing secret of this hook of a declared source: answered with a value typed unseen, kept in the hook's secret file")
	ask.Flags().StringVar(&askHookSource, "source", "", "with --secret-hook: the declared source the hook serves")
	ask.Flags().StringVar(&askSecretLogin, "secret-login", "", "ask for the input of the harness login waiting under this reference (its leader's input need names it): handed to the login, kept nowhere; with --as, the login is that user's own")
	ask.Flags().StringVar(&askForUser, "for-user", "", "with --secret: ask for this user's own address in the source (the variable its notify key names), kept in the user's env_file")
	ask.Flags().StringVar(&askForCrew, "for-crew", "", "with --secret: ask for this crew member's own credential in the source (the variables its credential names), kept in the crew member's env_file")
	ask.Flags().StringVar(&askAs, "as", "", "with --secret: ask for this user's own credential in the source (the names a call made as them reads), kept in the user's env_file; with --secret-login: the login is this user's own, and theirs to give")
	ask.Flags().StringVar(&askRun, "run", "", "propose this yardr command for the operator to approve, as it is typed: one the yard runs from a card (yardr bead ask --help)")
	ask.Flags().StringVar(&askSetup, "setup", "", "set up this embedded pack of kind source as one decision: the apply, the account and the grants, with --crew/--group naming who the grants are for")
	ask.Flags().StringVar(&askDepotPush, "depot-push", "", "turn this depot's push to its origin on as one decision for the operator to approve: YARDR_PUSH=origin in <depot>-assembly's cmd and the pack's helper in the depot's git config, carried out by the server")
	ask.Flags().StringVar(&askDepotPushOff, "depot-push-off", "", "turn this depot's push off as one decision for the operator to approve: the exact reverse of --depot-push for the depot")
	ask.Flags().StringArrayVar(&askCrew, "crew", nil, "with --setup: the grants are for this crew member (repeatable; at least one of --crew and --group)")
	ask.Flags().StringArrayVar(&askGroup, "group", nil, "with --setup: the grants are for this group's sessions (repeatable; at least one of --crew and --group)")
	ask.Flags().StringVar(&askSourceBase, "source-base", "", "with --setup: the setup's calls go to the pack's region base <name>, for a pack whose source declares bases")
	ask.Flags().StringSliceVar(&askEnv, "env", nil, "with --secret: only these variables of the source's credential (default the mint's inputs where the declaration names a mint, else every one it names)")
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
	var ansWords, ansDecline, ansAccount string

	var ansLater, ansSecret, ansApprove bool
	var ansValue []string
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
  yardr bead answer <id> <ask> <n> --secret        pick a setup ask's preset with the account typed
  yardr bead answer <id> <ask> <n> --account <source>   pick a setup ask's preset, taking a listed account
  yardr bead answer <id> <ask> <n>                 pick a drift card's preset, keeping the standing account
  yardr bead answer <id> <ask> --approve           approve the command a run ask proposes
  yardr bead answer <id> <ask> --approve --value <name>=<value>   approve it giving the values the card asks for

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
key gives a credential: each name the ask asks for is asked for in turn at
a terminal and nothing is shown as it is typed, once the gate below is
passed. The value is never an argument and never a variable of the
command's environment. The
yard's server writes it into the file the source's credential is read
from, and the answer on the bead says who gave which names: the value is
in that file and nowhere else. It is the operator's to give, and is
refused from the pane of an agent. An ask for a user's own address in a
source (yardr bead ask --secret <source> --for-user <user>) is that user's
to give, or the operator's, into the user's env_file, and names the
command that tells the user through the source when that is still to be
run. An ask for a crew member's own credential in a source (yardr bead ask
--secret <source> --for-crew <name>) is the operator's to give, into the
crew member's env_file, and takes effect when the crew member next starts.
An ask for a user's own credential in a source (yardr bead ask --secret
<source> --as <user>) is that user's to give, or the operator's, into the
user's env_file, and names the check that tries it as them.
An ask for a waiting login's input (yardr bead ask --secret-login
<login>) is handed to the login and kept nowhere; with --as <user> it is
that user's to give, or the operator's. Giving again replaces what was
given.
When the yard wrote the file and could not record it, it says so with the
names and the file: give it again. A secret ask takes no pick and no
words, which are refused and kept nowhere; --later puts it aside as any
ask, and it is declined with --decline "", without words.

<n> --secret answers a setup ask (yardr bead ask --setup): the preset
pick with the account typed unseen beside it, as --secret types it.
<n> --account <source> answers it taking the named candidate's account
instead, one the card lists with its address; where the card lists any,
exactly one of the two settles the account, and where it lists none the
typed value does. A drift card, on a pack already applied, is kept with a
bare <n> where its file holds every name, typed missing names where the
update newly needs them, or a candidate's file taken as ever. The yard
then applies the pack, takes the account, and grants the preset picked,
as the operator, and records what ran. It is the operator's to give, and
is refused from the pane of an agent. A pick with --secret or --account
is allowed on a setup ask only, as a bare pick that keeps is; everywhere
else a secret answer still takes no pick.

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

A card that asks for values in the clear (a hook add the yard registers
itself, whose register call wants the project's id) is approved with
--value <name>=<value>, repeatable, one for each name the card shows:
the yard sends them to the register call with the URL and the secret,
and keeps none. A name the card asks for nothing of, and a missing one,
refuse the approval and nothing runs.

A weighty card is one whose approval widens what reaches outside the yard,
whose word or credential counts as a person's, or what runs inside it; its
WEIGHTY lines say how, and every secret ask and every setup ask is one. The
approvals above stand behind a gate for it: --approve, --secret, <n> --secret,
<n> --account <source>, a bare <n> on a drift card, and a pick on a mapping
ask that writes a user's identity. At a terminal the command prints the
card's weight and asks for the ask's number typed back, before it reads a
value, and goes on only when it is: friction against approving on reflex,
and no authentication, since the terminal is the yard's own machine and its
shell is the operator's. Without a terminal (standard input is not one) it
declines, and says to approve the card in footplate, or in a
terminal on the yard's machine. No flag gets round that: a gate that a
script walks through is not a gate. A routine card has no gate.

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
			if ansSecret && ansAccount != "" {
				return fmt.Errorf("--secret types the account and --account takes a listed one: one of the two")
			}
			if ansAccount != "" {
				if len(args) != 3 || ansWords != "" || ansLater || declined || len(ansValue) > 0 {
					return fmt.Errorf("--account takes a candidate of a setup ask with its preset pick: yardr bead answer %s %d <n> --account <source>", id, askSeq)
				}
				n, pickErr := strconv.Atoi(args[2])
				if pickErr != nil || n < 1 {
					return fmt.Errorf("--account takes a candidate of a setup ask with its preset pick: yardr bead answer %s %d <n> --account <source>", id, askSeq)
				}
				return a.approveSetupAccount(c, st, id, askSeq, n, ansAccount)
			}
			if ansSecret {
				if len(args) > 2 || ansWords != "" || ansLater || declined || len(ansValue) > 0 {
					// A pick with --secret settles a setup ask alone.
					if len(args) == 3 && ansWords == "" && !ansLater && !declined && len(ansValue) == 0 {
						if n, pickErr := strconv.Atoi(args[2]); pickErr == nil && n >= 1 {
							return a.approveSetup(c, st, id, askSeq, n)
						}
					}
					return fmt.Errorf("--secret gives the secret and takes nothing else: the value is typed at a terminal")
				}
				if setup, err := askedSetup(c.Context(), st, id, askSeq); err == nil && setup != nil {
					return fmt.Errorf("ask %d sets up a pack: pick its preset with the account: yardr bead answer %s %d <n> --secret (or --account <source> where the card lists one)", askSeq, id, askSeq)
				}
				return a.giveSecret(c, st, id, askSeq)
			}
			if ansApprove {
				if len(args) > 2 || ansWords != "" || ansLater || declined {
					return fmt.Errorf("--approve runs the command the ask holds, with --value alone beside it")
				}
				values := map[string]string{}
				for _, kv := range ansValue {
					name, value, ok := strings.Cut(kv, "=")
					if !ok || name == "" || value == "" {
						return fmt.Errorf("--value %q: want <name>=<value>, one the card asks for", kv)
					}
					values[name] = value
				}
				weighed, err := a.gate(c, st, id, askSeq, "approve", "--approve")
				if err != nil {
					return err
				}
				done, err := st.ApproveAsk(c.Context(), id, askSeq, a.who(), weighed, values)
				if err != nil {
					return err
				}
				return a.out(c.OutOrStdout(), done, func(w io.Writer) {
					ran := store.AskRan{Argv: done.Argv}
					fmt.Fprintf(w, "ask %d, asked by %s: %s\n", done.Ask, done.Asker, ran.Command())
					for _, does := range done.Does {
						fmt.Fprintln(w, does)
					}
					fmt.Fprintf(w, "%s approved it, and the yard ran it:\n%s\n", a.who(), done.Result)
				})
			}
			if len(ansValue) > 0 {
				return fmt.Errorf("--value gives the values an approval carries, with --approve")
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
				if len(args) == 3 && ans.Words == "" {
					if setup, err := askedSetup(c.Context(), st, id, askSeq); err == nil && setup != nil && setup.Given && len(setup.Missing) == 0 {
						return a.approveSetupKeep(c, st, id, askSeq, n)
					}
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
			if ans.Option != 0 {
				// A pick that writes a user's identity on a mapping card is
				// a weighty approval: it takes its person's word that the
				// weight was seen, which here is the gate. Any other pick
				// asks for none.
				weighed := false
				if card, ok := askedCard(c.Context(), st, id, askSeq); ok && card.PickWrites(ans.Option) {
					var err error
					if weighed, err = a.weighed(c, id, askSeq, card.Weight, "pick", fmt.Sprintf("%d [words]", ans.Option)); err != nil {
						return err
					}
				}
				if err := st.AnswerAsk(c.Context(), id, askSeq, ans.Option, ans.Words, a.who(), weighed); err != nil {
					return err
				}
			} else if err := st.AddAnswer(c.Context(), id, ans, a.who()); err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"bead": id, "ask": askSeq, "state": askState(c.Context(), st, id, askSeq)}, func(w io.Writer) {
				printAsk(w, c.ErrOrStderr(), c.Context(), st, id, askSeq)
			})
		},
	}
	answer.Flags().StringVar(&ansWords, "words", "", "words alone, answering an ask with no options (or with a pick, in place of words after it)")
	answer.Flags().BoolVar(&ansLater, "later", false, "put the ask aside for 24 hours, or until the asker's next note on the bead")
	answer.Flags().StringVar(&ansDecline, "decline", "", "end the ask with no option picked: why you will not decide it (\"\" on a secret ask, which takes no words)")
	answer.Flags().BoolVar(&ansApprove, "approve", false, "approve the command a run ask proposes: the yard runs it as you, exactly as the card shows it")
	answer.Flags().StringArrayVar(&ansValue, "value", nil, "with --approve: <name>=<value> the approval gives in the clear, one the card asks for (repeatable)")
	answer.Flags().BoolVar(&ansSecret, "secret", false, "give the secret a secret ask asks for: typed unseen at a terminal, after its card's gate")
	answer.Flags().StringVar(&ansAccount, "account", "", "with a preset pick on a setup ask: take this listed candidate's account instead of typing one")
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

// sourceAddCarried reads what a --run "source add <file>" ask carries:
// the declaration file once, resolved as yardr source add resolves it,
// and each relative program's bytes from beside it as CopyCommandFiles
// reads them, each with its SHA-256. It answers nil for any other
// command. A file that cannot be read or does not parse is refused
// here, in the parser's words, and nothing is stored.
func sourceAddCarried(command string) ([]store.AskSource, error) {
	// Match parseRow's character gate before touching a path: a command
	// the store will reject must not make the CLI read a file first.
	if strings.ContainsRune(command, ',') {
		return nil, nil
	}
	for _, r := range command {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(" \t._/=-@:", r) {
			continue
		}
		return nil, nil
	}
	words := strings.Fields(command)
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	if len(words) != 3 || words[0] != "source" || words[1] != "add" {
		return nil, nil
	}
	file := words[2]
	text, err := readSourceAskFile(file, store.MaxRunSourceDeclaration, "the declaration")
	if err != nil {
		return nil, err
	}
	d, err := source.Parse(file, text)
	if err != nil {
		return nil, fmt.Errorf("%s", store.OneLine(err.Error()))
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	declDir := filepath.Dir(abs)
	total := len(text)
	var programs []string
	seen := map[string]bool{}
	for _, c := range d.Commands() {
		if c.Relative() && !seen[c.Argv[0]] {
			seen[c.Argv[0]] = true
			programs = append(programs, c.Argv[0])
		}
	}
	var files []store.AskFile
	for _, prog := range programs {
		path := filepath.Join(declDir, prog)
		data, err := readSourceAskFile(path, store.MaxRunSourceCommandFile, "source "+d.Name+" command file "+store.OneLine(prog))
		if err != nil {
			return nil, fmt.Errorf("source %s: a command's program %s is a file beside the declaration's file: %v", d.Name, store.OneLine(prog), err)
		}
		total += len(data)
		if total > store.MaxRunSourceTexts {
			return nil, fmt.Errorf("source %s: its declaration with command files exceeds %d bytes together: a card carries at most that, so add it with yardr source add %s in a terminal", d.Name, store.MaxRunSourceTexts, file)
		}
		sum := sha256.Sum256(data)
		files = append(files, store.AskFile{Program: prog, Text: string(data), SHA256: hex.EncodeToString(sum[:])})
	}
	return []store.AskSource{{Name: d.Name, Text: string(text), Files: files}}, nil
}

func readSourceAskFile(file string, limit int, what string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", store.OneLine(file), what)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes: a run ask carries at most that, so add it with yardr source add %s in a terminal", what, limit, store.OneLine(file))
	}
	return data, nil
}

// askedSetup reads the setup the ask seq on the bead id proposes, as the
// yard stored it.
func askedSetup(ctx context.Context, st store.API, id string, seq uint64) (*store.AskSetup, error) {
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
		if r.Ask.Setup == nil {
			return nil, fmt.Errorf("ask %d sets up no pack", seq)
		}
		return r.Ask.Setup, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// askedDepotPush reads the depot-push the ask seq on the bead id
// proposes, as the yard stored it.
func askedDepotPush(ctx context.Context, st store.API, id string, seq uint64) (*store.AskDepotPush, error) {
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
		if r.Ask.DepotPush == nil {
			return nil, fmt.Errorf("ask %d turns no depot's push on or off", seq)
		}
		return r.Ask.DepotPush, nil
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

// giveSecret is yardr bead answer --secret: past the gate of a weighty card
// (weighed.go), it reads the value of each name the ask asks for as yardr
// source key reads one, unseen from a terminal or as NAME=value lines from
// anything else, and gives them to the yard. What
// it prints is who gave which names and where the credential is set, never
// a value. A login input is read the same way, with no source to read it
// against, and handed to the waiting login.
func (a *app) giveSecret(c *cobra.Command, st store.API, id string, seq uint64) error {
	secret, err := askedSecret(c.Context(), st, id, seq)
	if err != nil {
		return err
	}
	// Every secret ask is weighty: its gate comes before a value is typed,
	// so a person sees what the card weighs first, and a command with no
	// terminal declines before it reads any.
	weighed, err := a.gate(c, st, id, seq, "give", "--secret")
	if err != nil {
		return err
	}
	if secret.Hook != "" {
		values, err := readHookKeys(secret, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()))
		if err != nil {
			return err
		}
		keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who(), weighed)
		if err != nil {
			return secretErr(err)
		}
		return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
			fmt.Fprintf(w, "%s gave %s · it is set in %s\n", a.who(), strings.Join(keyed.Given, " and "), keyed.File)
		})
	}
	if secret.Login != "" {
		values, err := readLoginKeys(secret, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()))
		if err != nil {
			return err
		}
		keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who(), weighed)
		if err != nil {
			return secretErr(err)
		}
		return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
			fmt.Fprintf(w, "%s gave %s · it went to the waiting login %s, and is kept nowhere\n", a.who(), strings.Join(keyed.Given, " and "), keyed.Login)
		})
	}
	if secret.Harness != "" {
		values, err := readHarnessKeys(secret, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()))
		if err != nil {
			return err
		}
		keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who(), weighed)
		if err != nil {
			return secretErr(err)
		}
		return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
			line := fmt.Sprintf("%s gave %s · it is set in %s", a.who(), strings.Join(keyed.Given, " and "), keyed.File)
			if keyed.Missing != "" {
				line += " · " + keyed.Missing
			}
			fmt.Fprintln(w, line)
			if keyed.Next != "" {
				fmt.Fprintln(w, "next: "+keyed.Next)
			}
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
	values, err := readKeys(d, secret.Env, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()), "")
	if err != nil {
		return err
	}
	keyed, err := st.GiveSecret(c.Context(), id, seq, values, a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
		line := fmt.Sprintf("%s gave %s · it is set in %s", a.who(), strings.Join(keyed.Given, " and "), keyed.File)
		if keyed.Missing != "" {
			line += " · " + keyed.Missing
		}
		fmt.Fprintln(w, line)
		if keyed.Next != "" {
			fmt.Fprintln(w, "next: "+keyed.Next)
		}
	})
}

// approveSetup is yardr bead answer <id> <ask> <n> --secret on a setup
// ask: it reads the account values as yardr source key reads them, unseen
// from a terminal or as NAME=value lines from anything else, and approves
// the setup with the preset pick. What it prints is the preset picked and
// who gave which names, never a value. Where the card lists candidates,
// --account <source> takes one instead (approveSetupAccount); where a drift
// card names missing names, only those are read.
func (a *app) approveSetup(c *cobra.Command, st store.API, id string, seq uint64, n int) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("--secret gives the secret and takes nothing else: the value is typed at a terminal")
	}
	// The sources the pack brings are not in the yard yet: read what the
	// account is typed against from the carried texts the yard stored.
	var d *source.Declaration
	for _, carried := range setup.Carried {
		parsed, err := source.Parse("pack source "+carried.Name, []byte(carried.Text))
		if err != nil {
			return fmt.Errorf("%s", store.OneLine(err.Error()))
		}
		if parsed.Credential != nil {
			d = parsed
			break
		}
	}
	if d == nil || d.Credential == nil {
		return fmt.Errorf("ask %d sets up a pack with no credentialed source: nothing typed settles it", seq)
	}
	names := setup.Account
	if len(setup.Missing) > 0 {
		names = setup.Missing
	}
	// A setup takes the account, so its card is weighty: the gate comes
	// before a value is typed, as a secret's does.
	weighed, err := a.gate(c, st, id, seq, "approve", fmt.Sprintf("%d --secret", n))
	if err != nil {
		return err
	}
	values, err := readKeys(d, names, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()), "")
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, values, "", a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s gave %s\n", a.who(), strings.Join(done.Given, " and "))
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
	})
}

// approveSetupAccount is yardr bead answer <id> <ask> <n> --account
// <source> on a setup ask: it approves the setup with the preset pick,
// taking the named candidate's account file instead of typing a value.
// What it prints is the preset picked and who took which source.
func (a *app) approveSetupAccount(c *cobra.Command, st store.API, id string, seq uint64, n int, account string) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("--account takes a candidate of a setup ask: yardr bead answer %s %d <n> --account <source>", id, seq)
	}
	weighed, err := a.gate(c, st, id, seq, "approve", fmt.Sprintf("%d --account %s", n, account))
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, nil, account, a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s took %s\n", a.who(), done.Account)
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
	})
}

// approveSetupKeep is yardr bead answer <id> <ask> <n> on a drift card
// whose file holds every account name: it approves the setup with the
// preset pick, keeping the standing account. What it prints is the preset
// picked and who kept it.
func (a *app) approveSetupKeep(c *cobra.Command, st store.API, id string, seq uint64, n int) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("ask %d sets up no pack", seq)
	}
	weighed, err := a.gate(c, st, id, seq, "approve", strconv.Itoa(n))
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, nil, "", a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s kept the standing account\n", a.who())
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
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

// readHookKeys reads the value a hook-signing-secret ask asks for, as
// readLoginKeys reads a login's: unseen from a terminal, or as a
// NAME=value line from anything else. The ask names the one name. No
// error holds the value.
func readHookKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	values := map[string]string{}
	if typed == nil {
		env, err := config.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				return nil, fmt.Errorf("the hook %s: standard input sets a name that was not asked for (%s)", secret.Hook, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the hook %s: standard input has no line %s=<value>", secret.Hook, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the hook %s: standard input has no NAME=value line, and nothing is written", secret.Hook)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s for the hook %s (nothing is shown as you type): ", n, secret.Hook))
		if err != nil {
			return nil, fmt.Errorf("the hook %s: reading %s: %w", secret.Hook, n, err)
		}
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the hook %s: nothing was typed for %s, and nothing is written", secret.Hook, n)
		}
	}
	return values, nil
}

// readHarnessKeys reads the values a harness-key secret ask asks for, as
// readHookKeys reads a hook's: unseen from a terminal, or as NAME=value
// lines from anything else. There is no declaration to read them against:
// the ask names them from the kind's harness file. No error holds a value.
func readHarnessKeys(secret *store.AskSecret, in io.Reader, typed func(prompt string) (string, error)) (map[string]string, error) {
	label := secret.Harness
	if h, ok := agents.Lookup(secret.Harness); ok && h.Label != "" {
		label = h.Label
	}
	values := map[string]string{}
	if typed == nil {
		env, err := config.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if !slices.Contains(secret.Env, k) {
				return nil, fmt.Errorf("the API key %s runs with: standard input sets a name that was not asked for (%s)", label, strings.Join(secret.Env, ", "))
			}
			values[k] = v
		}
		for _, n := range secret.Env {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("the API key %s runs with: standard input has no line %s=<value>", label, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("the API key %s runs with: standard input has no NAME=value line, and nothing is written", label)
		}
		return values, nil
	}
	for _, n := range secret.Env {
		v, err := typed(fmt.Sprintf("%s, the API key %s runs with (nothing is shown as you type): ", n, label))
		if err != nil {
			return nil, fmt.Errorf("the API key %s runs with: reading %s: %w", label, n, err)
		}
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else {
			return nil, fmt.Errorf("the API key %s runs with: nothing was typed for %s, and nothing is written", label, n)
		}
	}
	return values, nil
}

// printAsk prints the ask seq of the bead id on w, as the confirmation of
// the command that put or answered it. warn is where it says that the
// yard's links could not be read (see trustMarks).
func printAsk(w, warn io.Writer, ctx context.Context, st store.API, id string, seq uint64) {
	if _, _, foreign := strings.Cut(id, "@"); foreign {
		printForeignAsk(w, ctx, st, id, seq)
		return
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		fmt.Fprintf(w, "ask %d on %s\n", seq, id)
		return
	}
	as := trustMarks(warn, ctx, st)
	if as == nil {
		as = map[string]string{}
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			fmt.Fprintln(w, strings.Join(r.LinesMarked(as), "\n"))
			return
		}
	}
	fmt.Fprintf(w, "ask %d on %s\n", seq, id)
}

// printForeignAsk prints the ask seq of a bead@peer ref as the answer's
// confirmation: the question with its state as this yard knows it. The
// question stands on no bead of this yard, so there is no numbered text
// for it here; the card is in the decisions UI. The question, To and
// asker are the other yard's writer words, so each stands folded to one
// line, as the local card's one-line slots do, and none starts a line of
// its own.
func printForeignAsk(w io.Writer, ctx context.Context, st store.API, id string, seq uint64) {
	v, err := st.Bead(ctx, id)
	if err != nil {
		fmt.Fprintf(w, "ask %d on %s\n", seq, id)
		return
	}
	for _, a := range v.Asks {
		if a.Ask == seq {
			fmt.Fprintf(w, "ask %d · %s · to %s · asked by %s\n%s\n",
				a.Ask, a.State, store.OneLine(a.To), store.OneLine(a.Asker), store.OneLine(a.Question))
			return
		}
	}
	fmt.Fprintf(w, "ask %d on %s\n", seq, id)
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
