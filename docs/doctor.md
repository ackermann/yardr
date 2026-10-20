# yardr doctor

`yardr doctor` says in one go what a yard has and what it lacks: the programs
on this machine, what of the yard runs, and how far its setup is. It is the
check to run after the onboarding, after an upgrade, and whenever the yard
does nothing.

```sh
yardr doctor
yardr doctor --try                         # and one real call to each harness (costs a model call)
yardr doctor --json | jq -r '.next.next'   # the command for the next setup step
```

Doctor checks: it says what is wrong or missing, and exits 1 on a fault.
[`yardr status`](#yardr-status) lists: it says what runs now and where each
part is reached, and exits 0 on any yard.

For a harness kind yardr knows, each machine line also shows its `always`
flags after the harness file version. `yardr group show` shows the same flags
for a group's kind; the JSON report uses `harness_always` on a machine item.

## Read-only

Doctor installs nothing and changes nothing in the store. Where a program is
missing it prints where to get it and never runs that; on a Mac its `--json`
also says how a program that fetches gets it
([below](#a-program-fetched-on-a-mac)), and doctor fetches nothing itself. It
starts no daemon, no yardr server and no herdr server. What it does do:

- It runs each harness it finds with its version flag (`claude --version`),
  for up to 20 seconds each, side by side. That runs a program and changes
  nothing.
- It asks the herdr it finds whether its server answers (`herdr agent list`,
  the call a crew member's start begins with), for up to 5 seconds.
- For a program that is not on PATH it runs your login shell once (`$SHELL
  -l -c 'command -v <program>'`, stdin closed, up to 5 seconds) to ask where
  that finds it. With no `SHELL`, or a shell that does not answer, it says
  nothing of it.
- It reads the file each declared source's credential is in, as a call of
  the source would, to say whether it is there, private and sets the name
  the source's file gives. The value is printed nowhere, and nothing is
  sent to a source without `--try`.

## A server that is not running

Every other command that needs the yard starts its server. Doctor does not,
and neither does [`yardr status`](#yardr-status): a command asked what is
wrong changes nothing of what runs to answer. On a yard whose server is
stopped it says so, as a note, and reports what the machine and the home say
without the store:

- the machine's part, with every harness it finds listed as `a harness on
  this machine`: which ones the yard runs on is in the store, so one that
  does not work is a note there, not a fault;
- the daemon, and of the optional parts the web view, the login service
  (without its notes on depots) and the login service for herdr's server;
- the yardmaster, the crew, the held beads, the setup steps, hooks, sources,
  peers, packs and limits as `skipped: the server is not running`, each on the line it
  always has. Cycles, mail, names, config and codex, which have a line only when there is
  something to say, are skipped without one.

A stopped server is a state a yard can be in, not a fault: the exit status
is 0 unless something else is wrong. A daemon that does not run is, as on
any yard: a yard stopped as a whole (`yardr daemon stop`, `yardr server
stop`) exits 1 for its daemon.

`yardr doctor --try` is refused there, with exit status 1 and nothing
tried: which harnesses to call is in the store. A home counts as a yard here
when it holds a store (`yard.db`): whether that store was ever set up is
known only to its server.

## --try: one real call to each harness

A harness on PATH that answers its version flag can still not work: it is
logged out, its token is spent while its own status says logged in, or it is
a version that lacks what it is started with. A session started on it then
sits at a screen nobody sees, and the crew member's start times out.

`yardr doctor --try` makes one real call to each harness: its one-shot form
with the prompt `Reply with the one word ok and nothing else.`, stdin
closed, in an empty directory of its own, for up to 45 seconds each, side by
side. It prints the answer, or what the harness printed when the call failed
or did not end. The onboarding offers the same call for the yardmaster's harness
before it starts the yardmaster
([configuration.md](configuration.md#starting-the-yard)).

| kind | the call |
|---|---|
| claude | `claude -p <prompt>` |
| codex | `codex exec --skip-git-repo-check <prompt>` |
| grok | `grok -p <prompt>` |
| kimi | `kimi -p <prompt>` |
| opencode | `opencode run <prompt>` |
| pi | `pi -p <prompt>` |
| muse | `muse exec <prompt>` |

Any other kind is reported as not tried. `--try` makes this call even when
the separate version check fails or hangs, so it can show what the call
itself says.

With a source that says limits
([integrations.md](integrations.md#say-what-a-subscription-still-carries))
`--try` also runs that source's command, once for each provider the yard
burns that a source answers for, and once for each source that answers for
itself, for up to 30 seconds each, and says what
it answered. It is the one place doctor asks a provider of limits.

With declared sources
([integrations.md](integrations.md#a-declared-source)) `--try` also makes
the check call of each source whose file names one (`check:`, a read call
without parameters), as `yardr source check <name>` makes it: by this
command, with the credential read into this process for the one request,
for up to 30 seconds, and with nothing of the request written anywhere, no
event either. Without `--try` doctor sends a source nothing: it reads the
file the credential is in and how the source's last call ended.

**It costs a model call per harness**, on your account with it, so doctor
never makes it unasked: without `--try` one line says that no harness was
asked. A crew member's start that fails or does not answer makes it once and
says what it came to (`yardr init --start`, with a terminal or without);
`yardr crew show` names it.

The call is the harness's one-shot form, not the command herdr starts it
with in a pane: a flag herdr passes that the harness does not know is not
found by it.

On a home with no yard it says so, checks the machine, and creates nothing:
no store, no server. It runs without a terminal too, where other commands
are refused on such a home.

## Exit status

`1` when something the yard needs is missing or not working: every line
marked `FAULT`. `0` otherwise. A setup step not done, a held bead, a yardmaster
stopped on purpose and anything not configured are no fault.

## The output

The first line names the home:

| line | when |
|---|---|
| `yardr doctor: the yard in <home>` | a yard is set up there |
| ``yardr doctor: no yard in <home> yet. Create one with `yardr init --harness <kind>`, or run yardr in a terminal for the onboarding. Nothing was created.`` | no yard: only the first part and the steps follow, every step `todo` |

Then up to four headed blocks. In the first two, a line is a tag (`ok`,
`FAULT` or `note`), what it is about, and what was found.

### Needed on this machine

git, herdr, and a harness for each kind the yard runs on: its `default_kind`
and every kind a herdr group or crew member names. `<who>` lists them
(`the yard's default, crew member yardmaster, group web/builders`). On a home
with no yard the harnesses are those that are installed, any of which a yard
can be created on; they are then never a fault.

| line | meaning |
|---|---|
| `ok git <path>` | on PATH |
| `ok herdr <path>, its server is running` | on PATH, and its server answers |
| ``FAULT herdr <path>: on PATH, but its server is not running (server_not_running: <herdr's message>): the daemon cannot start agents until it is. Start it: run herdr in a terminal (ctrl+b q detaches, the server stays); on a machine without a desktop, run `herdr server`, its headless server, as a service (docs/install.md, "herdr's server")`` | herdr says no server is at its socket: every start of a crew member or a bead's agent fails with `server_not_running` until one is ([install.md](install.md#herdrs-server)). On a yard, `yardr up` starts it too ([configuration.md](configuration.md#starting-and-stopping-a-yard)) |
| `note herdr <path>: on PATH; whether its server is running could not be asked: <what herdr said>` | herdr answered with something else, or not in 5 seconds: not known to be down, so no fault |
| `FAULT <program> not on PATH: <what it is needed for>. Get it: <where>` | missing. For git and herdr the same words the onboarding uses before its first page. For a harness, `<where>` is its install command where yardr knows one (claude, codex, kiro, opencode), else a pointer to the agent's own documentation |
| ``FAULT <program> not on PATH: <what it is needed for>. Found at <path> by your login shell, which this shell's PATH does not have: add <dir> to PATH here, or for the daemon's service run `yardr daemon install` from a login shell`` | missing here and installed: your login shell finds it. A shell without a terminal (ssh with a command, a service) reads no profile, so it lacks what `~/.profile` adds, `~/.local/bin` for one. The daemon's service gets the PATH of the shell that installs it ([configuration.md](configuration.md#login-services)) |
| `FAULT a harness not on PATH: …` | a home with no yard, and no agent herdr starts is installed. When the login shell finds claude, codex, grok, kimi, kiro, opencode, pi or muse, the line above is printed for each it finds instead |
| `ok <harness> <path>, the harness of <who>: answers its version flag (<its first line>)` | found, and it runs |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but its version flag fails: <error and its first line>` | found, and it does not run |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but it did not answer its version flag within 20s` | found, and it hangs. On macOS the line goes on: `On macOS that can be a quarantined binary waiting on a Gatekeeper dialog nobody sees: run <harness> once in a terminal and answer it` |
| `ok <harness> <path>, the harness of <who>: not checked, yardr knows no version flag for <kind>` | found, and not run: the flag is known for claude, codex, grok, kimi, kiro, opencode, pi and muse |
| `note <harness> …` | the two failing lines above, for a harness nothing runs on yet (a home with no yard) |
| `…; harness file v<n>` or `…; no harness file (yardr harness list)` | the end of every line of a harness that was found: the version of the file yardr reads the kind from (`yardr harness show <kind>`, [groups.md](groups.md#what-yardr-knows-of-a-kind)), or that it has none and knows no marks, error lines or flags of the kind |
| `note harness on PATH is not "works": no harness was asked for an answer. yardr doctor --try makes one real call to each (it costs a model call per harness)` | without `--try`, when a harness was found. Not printed on a yard whose server is not running, where `--try` is refused |
| `ok <harness> tried: one real call answered (<the last line of its answer>)` | `--try`: the harness ended with status 0 |
| `FAULT <harness> tried: one real call failed: <error>: <the last three lines it printed>` | `--try`: it ended with another status. The lines are the end of what it printed, stdout and stderr as they came, joined with ` / `: where a harness says that a login is needed or which flag it does not know |
| `FAULT <harness> tried: one real call did not end within 45s; it printed: <the last three lines>` | `--try`: it hung, at a question or a login screen as a rule |
| `note <harness> not tried: yardr knows no one-shot call for <kind>` | `--try`: a kind not in the table above |
| `note <harness> tried: …` | the two failing lines, for a harness nothing runs on yet (a home with no yard) |

Each of the three herdr lines ends with the herdr session that was asked and
its socket: `; session <name>, socket <path> (the yard's herdr_session
setting)`, or `(this shell's, since the yard's herdr_session setting is
unset)` when the yard names none and doctor asked the session of the shell
it was run in, which need not be the daemon's
([configuration.md](configuration.md#settings)). For a session the setting
names that is not running, the fault names it and says `herdr session
attach <name>` in place of `herdr`.

### The yard

Not printed on a home with no yard.

| line | meaning |
|---|---|
| `ok server running` | |
| `note server not running (yardr server start, or any command but status and doctor, starts it)` | no fault. Doctor starts none ([above](#a-server-that-is-not-running)): the daemon's line follows, then `note yardmaster skipped: the server is not running` and the same for `crew` and `held`, and the block ends |
| `FAULT server cannot be reached: <error>` | nothing else of the yard can be read; the block ends here and there are no steps |
| `FAULT upgrade upgrade to <rev> failed at <stage>: <reason>; the yard is down — fix it and run yardr upgrade, or remove <home>/upgrade.failed` | a failed upgrade left the yard down ([upgrade.md](upgrade.md)). The marker is read from the home without the server, so the fault is loud while the yard is dead, until the next successful upgrade logs the failure event and clears it, or it is removed by hand |
| `ok daemon running` | a daemon holds the home's lock |
| `FAULT daemon not running: no bead is dispatched and no crew member starts. Start it in a pane of its own (yardr daemon) or in the background (yardr init --start)` | |
| `ok yardmaster <name> is running (herdr agent <agent>)` | the crew member named by the `yardmaster` setting has a session |
| `FAULT yardmaster no crew member <name>: yardr init adds it` | |
| `note yardmaster <name> is stopped (disabled). Let it run again: yardr crew start <name>` | stopped on purpose: no fault |
| `FAULT yardmaster <name> is not running: <N> failed starts so far, the last with: <reason>. Once that is fixed: yardr crew start <name>` | its starts fail; `the daemon gave up after <N> tries` once it has. A session gone within 60 seconds of its start is a failed start, with the reason `the session ended <time> after its start (…)` ([crew.md](crew.md)) |
| `FAULT yardmaster <name> is not running: the daemon starts it` | no daemon |
| `FAULT yardmaster <name> is not running, and no start has failed: the daemon may still be at it (yardr crew list)` | |
| `FAULT crew <name> is not running: …` | another crew member whose starts fail, as for the yardmaster |
| `FAULT crew <name> is not logged in: run <harness> in another terminal and log in there; then answer the login in its pane or start it fresh (yardr crew restart <name>)` | the crew member's session runs, and its pane shows its harness's login screen: one line per such crew member, the yardmaster included (whose own line still says it runs). `<harness>` is the kind's command (`claude`, `codex`, …) |
| `ok crew <N>, none failing to start` | |
| `ok held no bead is held` | |
| `note crew crew member <name> has a copy of an earlier default --on, a list that stays as it is when a kind joins the class attention. It does not hear <kinds>. To hear the class by name, whatever joins it: yardr crew set <name> --on attention[,<its other kinds>]` | no fault: its `--on` holds every kind of the first default and not the class, so it was made before the class. `It hears every kind of the class today` when the list lacks none. yardr rewrites no list: one shortened on purpose looks the same ([subscriptions.md](subscriptions.md#the-class-attention)) |
| `note held <N> held, waiting for someone: <ids> (yardr bead show <id>; yardr bead unhold <id> resumes it)` | no fault: a held bead waits for a person |
| `note cycle <a> blocks <b> blocks <a>: these beads wait on each other and are never ready. Remove the edge that is wrong: yardr dep rm <blocker> <bead>` | no fault: open beads that block each other in a circle, one line per cycle. `yardr dep add` refuses the edge that would close one, so these are from a store written before it did; yardr removes nothing itself ([line.md](line.md#cycles-of-blockers)) |
| `ok mail nothing unread for <user>` | printed when who runs the command is a user ([users.md](users.md)); absent otherwise |
| `note mail <N> unread from <names> for <user>: yardr inbox reads them, --ack <seq> marks them read` | no fault: nothing nudges a person, so this line and bare `yardr` are where they learn of mail |
| `note names group <group>: <names> are neither a user nor a crew member, so nobody of that name can be mailed (yardr user add <name>)` | no fault: members of a group are names, taken as given |
| `note config <group or crew member>: unknown config key "<key>": nothing reads it. A <group or crew member> of the <runner> runner takes: <keys>. Change it: yardr group set <name> <key>=<value>, or --unset <key>` | no fault: the store refuses a config key nothing reads, or a value its reader takes as not set (`needs config.<key> as …`), and this one was stored before it did. The yard runs as it did |
| `note config setting <key> is stored and nothing reads it (there is: <keys>). Remove it: yardr settings set <key> ""` | no fault: the same for a setting |
| `note names crew member <name> has the operator's name, so the operator has no user and no inbox: mail to <name> is the crew member's. Rename one of the two: yardr --actor <name> settings set operator <new name> gives the operator a name of their own and adds their user (as <name>, since settings are the operator's to change)` | no fault: a yard from before crew members and users shared their names |
| `note names user <name> was not told outside the yard: <when> through <source>, not sent: <reason> (<error>, <n> of the inbox in one message). Their inbox has it all the same; yardr source check <source> --user <name> sends one test message` | no fault: the last message to a user through the source they name (`yardr user set <name> notify=<source>`) was not taken. `failed (…); next try <time>` while the daemon still tries it. The reasons: `not_set_up` (the user's env file or the source's), `refused` (the source answered 4xx), `gave_up` (8 tries), `expired` (older than 6 hours). Gone with the next message the source takes |
| `note names device <device> of <user> has not been used for 30 days (last used <when> from <address>), and its token still calls as <user>. If it is no longer in use: yardr user device rm <user> <device>` | no fault: a [device](users.md#devices) whose token nothing has called with for 30 days; `(never used, added <when>)` for one that was added 30 days ago and never used. A token does not end by itself, so one nobody uses still lets in whoever has it. Gone once the device is used or removed |
| `note codex depot <depot> (<path>) is not trusted by Codex, and group <group> runs on it: a session there waits on Codex's "Trust this folder?" until someone answers it in its pane. Add to <file> the two lines `[projects."<root>"]` and `trust_level = "trusted"` ` | no fault: a route sends the depot's beads to a herdr group whose `kind` is `codex` (one that names no depot sends every depot's), and Codex's `config.toml` (in `$CODEX_HOME`, else `~/.codex`) has no such table for the depot's root. `<root>` is the depot's path with its symlinks resolved, as Codex names a folder. This check is for the depot path; Codex may require a separate entry for a worktree path. Doctor reads the file as this command finds it (the daemon's `CODEX_HOME` is its own) and only the table form Codex itself writes; it never writes the file. `note codex whether Codex trusts the depots its groups work is not known: <error>` when the file is there and cannot be read |
| `note kimi <file> has no StopFailure hook that tells herdr the agent is idle, so a Kimi turn that ends on a provider error stays working to herdr: herdr's Kimi integration maps Stop and Interrupt to idle but hears no StopFailure. Append to <file>:` followed by the block to append in a fenced `toml` block (`[[hooks]]`, `event = "StopFailure"`, `command = "bash '<home>/.kimi-code/hooks/herdr-agent-state.sh' idle"`, `timeout = 10`, with `<home>` expanded) | no fault: the yard runs on `kimi` (its default, a herdr group or a crew member names it) and Kimi's `config.toml` (`~/.kimi-code`) has no `[[hooks]]` table with `event = "StopFailure"` whose command runs `herdr-agent-state.sh` with `idle`. Without it a turn that ends on a provider error leaves the agent `working` to herdr, unless yardr's Status knows the error line. Doctor reads the file as this command finds it and never writes it. `note kimi whether Kimi tells herdr when a turn fails is not known: <error>` when the file is there and cannot be read; no line at all when the file is missing |
| `note kimi <file> lists the herdr hook for <events> twice: each hook runs twice. Remove the doubled [[hooks]] tables so each stands once (a second herdr integration install kimi wrote them again)` | no fault: the same herdr `[[hooks]]` table (the same event with the same command running `herdr-agent-state.sh`) stands more than once, so each hook runs twice. One line however many are doubled (`the herdr hooks for <events>` for more than one), after the missing-hook line when both apply |

### Setup

On a yard whose server is not running the block is one line, `note skipped:
the server is not running`, and no next step is named.

The steps from a yard to a first merged bead, in the order they are taken.
Each line is `done` or `todo`, the step, and what in the store shows it done
or what is lacking. The steps are read from the store and nothing else (the
store reads a depot's own flow from its base branch), by one function: the
yardmaster's first run uses it through this command, and `yardr setup` takes
the first step not done as a form ([setup.md](setup.md)), so whoever walks you
through the setup can take over from anyone else.

| step (`key` in JSON) | done when |
|---|---|
| `a depot` (`depot`) | the yard has a depot |
| `the depot's .yardr files on its base` (`depot_files`) | every depot has a flow where yardr reads it: in the store when the depot follows a pack's flow (`yardr depot init <depot>`), under `.yardr/flows/` on its base branch for a depot that keeps its own, or in a dir depot's directory |
| `a group that builds` (`builders`) | a route sends stage `new` to a group |
| `a group that reviews` (`reviewers`) | the same for stage `review` |
| `a group or person that lands approved beads` (`landing`) | the same for stage `approved`: an assembly or people who merge by hand, either counts once it is routed |
| `routes for new, review and approved` (`routes`) | a route exists for each of the three stages |
| `a first bead` (`first_bead`) | a bead exists in a depot (a crew member's journal and memories do not count) |
| `a first merged bead` (`merged_bead`) | a bead of a depot was closed by reaching its flow's terminal stage; one closed by hand does not count |

A group is told by its route and not by its role or its name, so the list
reads the same for a yard set up from a pack and for one set up by hand. The
command named for each of the five steps above while it is `todo` is the
import that does them all, `yardr depot init <depot>`, from the yard's
default pack: [`yardr setup`](setup.md) runs it as one page.

A yard whose depots are all dir depots lands nothing: it has no landing step, its
routes are `new and review`, and its last step reads `a first bead through to
the flow's end`.

### Optional

What a yard runs without. Each line is `configured` or `not configured`,
never a fault. Not printed on a home with no yard.

| line | meaning |
|---|---|
| `skipped hooks: the server is not running` | the yard's server is not running; the same for `sources`, `peers`, `packs` and `limits` |
| `not configured web view: not running, and nothing keeps it running; yardr web serve shows the yard in a browser while it runs, yardr web install keeps it running from login on (docs/web.md)` | no web view runs, and no login service for one is installed for this home |
| `not configured web view: running at <URL> (pid <pid>, this machine only), …; nothing keeps it running: yardr web install keeps it running from login on (docs/web.md)` | a view started by hand (`yardr web serve`, `yardr web start`) runs, each with the URL to open; no login service is installed. `not configured` is about the service alone. A view that is `open to the network (--unsafe), no login` says that in place of `this machine only` |
| `configured web view: kept running as a login service at http://<addr>/ (<file>); running at <URL> (pid <pid>, this machine only), …` | `yardr web install` wrote `<file>` ([web.md](web.md#keeping-it-running)), and these views run now, the service's or others; whether the service manager has it loaded is not asked |
| `configured web view: kept running as a login service at http://<addr>/ (<file>); not running now` | the service is installed and no view runs |
| `note web view: <URL> (pid <pid>) is open to the network, no login: it was started with --unsafe, and anyone who can reach this machine on port <port> can read the yard` | one line per running view that listens off loopback. A note, never a fault: `--unsafe` is the operator's choice ([web.md](web.md#--unsafe-every-interface)) |
| `not configured hooks: no source outside the yard reaches it; yardr hook add --help (docs/subscriptions.md)` | no hook |
| `configured hooks: <name> (<source>), …` | |
| `note hooks: hook <name> is blocked: <why>` | the receiver refuses its deliveries |
| `note hooks: hook <name>: its last delivery was refused (<reason>, <when>); it last accepted one <when>. Events of kind hook_rejected say how many` | the source reaches the yard and is turned away: a wrong or missing signature as a rule (`unauthorized`). `it has accepted none` when it never took one. The reasons, and how the events are bounded: [subscriptions.md](subscriptions.md#a-refused-delivery) |
| `note hooks: hook <name> quiet: last accepted a delivery <when> (nothing for <ago>, usual gap <gap>); the yard's listener on <addr> answers, so deliveries stop between the source and the yard: the webhook is off there, or the proxy in front of the yard is down` | the hook used to deliver and delivered nothing for longer than its usual gap (the median interval between its accepted deliveries over the last week, times three, with a floor of an hour): the webhook is off at the source, or cannot reach this yard. The yard's own listener was probed and answers, so the break is past it ([subscriptions.md](subscriptions.md#a-quiet-hook)) |
| `note hooks: hook <name> quiet: …; nothing answers on <addr>: the yard's own listener is down` | the same quiet hook, and the probe found the yard's own listener down: that is the break |
| `note hooks: hook <name> quiet: …; the server could not listen on <addr>: <why>` | the same quiet hook, and the server reports that it could not listen on the address; the listener line beside it says what to change |
| `note hooks: hook <name> quiet: last accepted a delivery <when> (nothing for <ago>, usual gap <gap>)` | the same quiet hook, and the server has no address for webhooks to probe: the hooks line beside it says what it takes |
| `note hooks: the server takes webhooks on <addr> (the hook_addr setting)` | hooks exist, or the yard has an address without one: what the running server listens on, and that the address is the yard's `hook_addr` setting ([configuration.md](configuration.md#settings)) |
| `note hooks: the server takes webhooks on <addr> (YARDR_HOOK_ADDR, set where the server started)` | the same, with the variable's address: set where the server starts, it is the one in use. `; the hook_addr setting (<addr>) is not used while the variable is set there` follows when the setting names another |
| `… ; hook_addr is <addr> now, which it takes at its next start: yardr server stop (the next command starts it again)` | the setting was changed (or unset) after the server read it |
| `note hooks: the server could not listen on <addr> (<the setting or the variable>): <error>. Another yard or program may have the address; give this yard its own with yardr settings set hook_addr <host:port>, then yardr server stop (the next command starts it again)` | the address is taken, by another yard given the same one as a rule. The yard works; no delivery reaches it. Printed with no hook too. For the variable's address: `change the variable where the server starts, or unset it there and give this yard its own with …` |
| `note hooks: the server takes no webhooks yet: hook_addr is <addr>, which it takes at its next start: yardr server stop (the next command starts it again)` | hooks exist and the setting was set after the server started with no address |
| `note hooks: the server has no address to take webhooks on: yardr settings set hook_addr 127.0.0.1:8790, then yardr server stop (the next command starts it again)` | hooks exist, and neither the setting nor the variable gives the server an address |
| `note hooks: the server has not said what it takes webhooks on` | hooks exist, and the running server is a build from before it wrote `hooks.state` |
| `not configured sources: no service outside the yard is declared, so no agent calls one through the yard; yardr source add <file> (yardr source --help, docs/integrations.md)` | no declared source ([integrations.md](integrations.md#a-declared-source)) |
| `configured sources: <name> (<n> calls), …` | the declared sources, each with how many calls its file has. Nothing was sent to any: doctor read the file each one's credential is in, the grants, and how its last call ended, from the events |
| `note sources: source <name>: no call of it can be made: <NAME> is not set up: yardr source key <name> gives <NAMES>, or yardr source set <name> env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> "<question>" --secret <name> --context "<where to get it>"` | the source's file names a credential and the operator has set no file it is read from |
| `note sources: source <name>: no call of it can be made: <why>` | the file that is set cannot be used, read under the rule of every `env_file`: it is gone (`no such file or directory`), `is open to others (mode 0644); chmod 600 it`, or `does not set <NAME>`. Every `yardr call` of the source ends `no_credential` until it is set right |
| `note sources: source <name>: an update call of it cannot be made: <why>` | the file sets the read credential but not the one the source's updates name (`update_env`): read calls can be made, every update ends `no_credential` |
| `note sources: source <name>: its last call that was answered (<call>, <when>) was answered <401 or 403>: the service did not take what <NAME> is set to. yardr source check <name> asks it again` | the last `source_call` event of the source that has a status of the service's says the service refused the credential: one that is wrong, expired or rotated away. Read from the yard's last 500 `source_call` events; a call refused before it was sent (`not_granted`, `limit`) or left without an answer (`timeout`) says nothing of the credential and is passed over. The note goes when a later call is answered otherwise; `yardr source check` logs no event, so it goes with the next `yardr call`, or under `--try` |
| `note sources: source <name>: nobody is granted a call of it, so only the operator makes one (yardr source grant <name> read --group <group>)` | the source has calls, no group and no crew member is granted any, and no user is told through it (`notify`): it is set up and used by nobody |
| `note sources: source <name>: its file is not read by this yardr (yardr source show <name>)` | a file a later yardr added, with a key this build does not read |
| `note sources: tried: source <name>: its check call <call> (<METHOD> <path>) was answered <status> in <n>ms` | `--try`: the call the source's file names as `check` was made, as `yardr source check <name>` makes it, and answered with a success |
| `note sources: tried: source <name>: its check call <call> (<METHOD> <path>) was answered <status>: the service does not take what <NAME> is set to` | `--try`: answered with no success; the part after the status is there for a 401 or a 403 |
| `note sources: tried: source <name>: its check call <call> (<METHOD> <path>) got no answer: <why>` | `--try`: nothing came back within 30 seconds, a redirect, a refused connection |
| `note sources: not tried: source <name> has no credential to make its check call with` | `--try`: see the `no call of it can be made` line above it |
| `note sources: not tried: the file of source <name> names no check call (check: <a read call without parameters>)` | `--try`: nothing can be sent that changes nothing |
| `not configured peers: this yard is linked with no other; yardr peer pair in a terminal links it with one, as a form (docs/peers.md)` | no peer |
| `configured peers: <name>, <name> (it calls), <name> (its mail reaches yardmaster, mckean), …` | the yards this one is linked with ([peers.md](peers.md)); `it calls` marks one without a URL, which is never called. `its mail reaches <names>` is on each link that takes mail (`--receive mail`): whom of this yard it [exposes](peers.md#the-roster-whom-a-peer-may-see-and-write-to), `nobody` when no one, which the note below warns of. A link with both has them in one bracket, `(it calls; its mail reaches …)`. `trusted as: mckean@airy -> mckean` is in it for a link that [trusts a name as a user of this yard](peers.md#trusting-a-name-as-a-person-of-this-yard), and `reach: mckean@airy -> mckean: every ask for mckean is also sent there, …` for one that [sends a user's asks to the name](peers.md#reach-a-users-asks-are-also-sent-to-the-name), with what is sent in words |
| `note peers: this yard has no key, so it signs nothing and its peers cannot know it: yardr peer key` | peers were added before the yard made its key |
| `note peers: this yard's key cannot be used, so nothing is sent to or taken from its peers: <why>` | `peer.key` is open to others, or does not hold a key |
| `note peers: no listener, no incoming: nothing can be posted to this yard, so it gets its peers' messages only by asking those it can call: <what the server says of its listener>` | the server takes nothing in. `; <names> cannot be called and so cannot be heard at all` follows the first part when a peer has no URL |
| `note peers: peer <name>: the beads it sends arrive in the stage <stage>, which agents of the group <group> work: WORK STARTS ON EACH UNSEEN, on the other yard's text. For a named person's look first: route that stage to a manual group with members (yardr peer intake --help)` | the link takes beads, and the first stage of one [intake's flow](peers.md#who-looks-at-a-bead-first) is routed to agents. The same note is made where a script (`it runs on each unseen`) or another yard (`each is sent on unseen`) works it. A pair run by one person may mean it: it is a note, never a fault. Where the types are routed apart it says `the beads it sends of type <types>`; where the link holds several intakes it says `peer <name>, depot <depot>` |
| `note peers: peer <name>: the beads it sends arrive in the stage <stage>, which people work: the group <group> names no members, so anyone of this yard may move one on, a crew member too. For a named person's look first: yardr group set <group> --members <name>,...` | the first stage is a manual group's, and the group names nobody: a crew member that reads the peer's bead may move it on |
| `note peers: flow <flow> (depot <depot>): a linked yard (<peer>) works the stage <stage>, and its outcome <outcome> leads to <stage>, which the script of the group <group> works. An outcome is the other yard's word, and nobody of this yard looks at what came back before that. Where that is not meant, lead the outcome to a stage that agents or people of this yard work (docs/peers.md, "What a returned outcome is evidence of")` | a route sends beads of the flow's stage to a group whose runner is `peer`, and an outcome of that stage leads to a stage a script works, or to a terminal one (`which is terminal`): what the other yard returns as that outcome lands, or closes the bead, [on its word](peers.md#what-a-returned-outcome-is-evidence-of). One note per flow and stage, with every such outcome (`; its outcome <outcome> leads to …`) and every depot the flow governs alike (`depots <depot>, <depot>`); `flow <flow>, for type <type>` for the flow of a type. None where every outcome of the stage leads to a stage of agents or people. A flow whose result is words may mean it: it is a note, never a fault |
| `note peers: the flows of depot <depot> could not be read, so they were not looked at for a peer's outcome that lands unseen: <why>` | the yard has a peer group, and the depot's flows do not load (a flow file that does not parse, a repository that is gone) |
| `note peers: <name> subscribes to the source peer with peer=<peer>, and this yard is linked with no yard <peer>, so that hears nothing: yardr unsubscribe <name> peer removes it` | a subscription to [the source `peer`](peers.md#the-source-peer) filters on a yard that is not linked: its link was removed (`yardr peer rm` leaves the subscription; a rename takes it along), or the name was mistyped. Printed on a yard with no peer left too |
| `note peers: <N> messages for peer <name> were given up (yardr peer show <name>)` | posts to it failed 24 times, or it refused them |
| `note peers: the link with peer <name> takes mail and exposes nobody, so all its mail is refused: yardr peer allow <name> --expose <crew member or user>,...` | the link takes mail (`--receive mail`) and names nobody the peer may write to: one from before [the roster](peers.md#the-roster-whom-a-peer-may-see-and-write-to), or one where `--expose` was not set |
| `note peers: the link with peer <name> trusts <name>@<peer> as <user>, and <user> is no user of this yard: yardr peer allow <name> --no-trust <name>@<peer>` | a [grant](peers.md#trusting-a-name-as-a-person-of-this-yard) names a user who is gone. The store drops a grant with its user, so this is one that came another way; it marks no mail |
| `note peers: the link with peer <name> trusts <name>@<peer> as <user>, and <name> exposes nobody called <name>: yardr peer allow <name> --no-trust <name>@<peer>` | the same for a name the peer does not expose, or a peer that has not said whom it does |
| `note peers: the link with peer <name> sends the asks for <user> to <name>@<peer>, and the link does not trust <name>@<peer> as <user>: yardr peer allow <name> --no-reach <name>@<peer>` | a grant of [reach](peers.md#reach-a-users-asks-are-also-sent-to-the-name) stands on no trust grant for the same user. The store drops reach with the trust it stands on, so this is one that came another way; it sends nothing and takes no answer |
| `note peers: the link with peer <name> sends the asks for <user> to <name>@<peer>, and <name> exposes no person called <name>: yardr peer allow <name> --no-reach <name>@<peer>` | the same for a name the peer's roster does not have as a person |
| `not configured packs: no pack is imported; yardr pack apply <dir> --depot <depot> (docs/packs.md)` | |
| `configured packs: <pack> into <depot>, …` | |
| `not configured limits: no source says what a harness kind's subscription still carries, so a usage limit is known only by the harness stopping on it; a source with a limits key says it (yardr source --help, docs/integrations.md)` | no declared source has a `limits` key ([integrations.md](integrations.md#say-what-a-subscription-still-carries)) |
| `configured limits: <source> for <kind>, <kind>; …; not asked (yardr limits asks, and yardr doctor --try)` | which source says the limits of which harness kinds, read from the sources' files (`<source> answers for itself` for one with no kinds). Its command was not run: a provider is slow, and asking is a person's to want |
| `configured limits: <source> for <kind>, <kind>; …` | the same under `--try`, followed by a `tried:` line per provider and per source that answers for itself |
| `note limits: source <source> gives no answer as this command finds it: limits command <program>: <error>. The daemon's PATH is its own: name the program by its whole path` | the program the source's file names is not found by this command; nothing was run to find out |
| `note limits: <kind> is in use (<who names it>) and no source says its limits (limits.kinds)` | a source says limits, and the yard's default, a group or a crew member runs on a kind none lists, naming no `provider` |
| `note limits: <who> names provider <provider> and no source answers for it (limits.kinds)` | a group or crew member sets `provider` to a name no source's kinds values name |
| `note limits: route <id> (stage <stage>) puts the condition <condition> on group <group>, and no source answers for its provider <provider>: the condition holds whatever is used (limits.kinds)` | a route's quota condition is on a group whose provider no source answers for (`its kind <kind>` for a group that names no `provider`), so it gates nothing (`the comparison on the provider's windows holds whatever is used` where the condition also names a source) ([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions)). Said with no limits source too |
| `note limits: route <id> (stage <stage>) puts the condition <condition> on group <group>, and no source named <source> says limits of itself: the comparison holds whatever is used (a limits key with no kinds)` | a route's comparison names a source that answers for nothing of its own, so it gates nothing. Said with no limits source too |
| `note limits: tried: <source> answered for provider <provider> (<who burns it>): <n> windows; <no window is used up, or: an agent burning <provider> waits for the <label> window, which resets <when> (in <how long>)>` | `--try`: the source's command was run once for a provider the yard burns, for up to 30 seconds |
| `note limits: tried: <source> answered for itself: <n> windows; <no window is used up, or: the <label> window is used up, which <resets <when> (in <how long>), or: reset not known>>` | `--try`: a source that answers for itself was asked once, for up to 30 seconds |
| `note limits: tried: provider <provider> (<who burns it>) is not known: <reason>` | `--try`: the command gave no answer: its exit status or the timeout with the last lines of its stderr, or what of its output is not the report |
| `not configured login service for herdr's server: herdr's server does not start at login, and after a reboot no agent starts until it runs; yardr up --login installs its login service and the daemon's (docs/install.md, "Keep it running after a reboot")` | no service file in the user's service directory or the machine's runs `herdr server`: yardr's own is not there, and none of the person's is ([install.md](install.md#keep-it-running-after-a-reboot)) |
| `configured login service for herdr's server: herdr's server starts at login (<file>); yardr down --login removes it with the last yard that starts at login` | `yardr up --login` wrote `<file>`, in this yard or another of the user's; whether the service manager has it loaded is not asked (`yardr status` asks) |
| `configured login service for herdr's server: a service that is not yardr's starts herdr's server (<file>, …); yardr leaves it as it is` | a file yardr did not write runs `herdr server`: one saved by hand, under any name. yardr writes none beside it |
| `not configured login service for herdr's server: yardr writes no login service on <system>; …` | neither macOS nor Linux |
| `not configured login service: the daemon runs while its terminal or the session that started it does (yardr daemon, yardr init --start); it does not come back after a reboot; yardr daemon install starts it at login` | no login service for the daemon is installed for this home |
| `configured login service: the daemon starts at login (<file>)` | `yardr daemon install` wrote `<file>`, a launchd agent or a systemd user unit ([configuration.md](configuration.md#login-services)); `yardr daemon status` asks the service manager whether it is loaded |
| `note login service: depot <depot> (<path>) is in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads): a launchd agent needs its own access there (System Settings, Privacy & Security, Files and Folders), which a daemon started in a terminal has from the terminal` | macOS, with the login service configured: one line per such depot |

### The last lines

| line | when |
|---|---|
| `Nothing the yard needs is missing.` | no fault |
| `Nothing that was checked is missing; what the yard's store holds was not checked (skipped above).` | no fault, on a yard whose server is not running |
| `1 fault: something the yard needs is missing or not working (FAULT above).` | |
| `<N> faults: things the yard needs are missing or not working (FAULT above).` | |
| `Next step: <step>.` and, below it, the command | a yard, and a step is not done |
| `The setup is complete.` | a yard, and every step is done |

For a home other than the default, every command doctor prints is `yardr
--home <home> …`. In JSON a step's `next` is the bare command.

## JSON

`--json` prints one object, with the same exit status:

| field | |
|---|---|
| `home`, `yard` | the home; whether a yard is set up in it |
| `machine` | the programs: `program`, `for`, `found`, `path`, `get` (when missing), `login_shell_path` (where the login shell finds one that is missing), `fetch` (on a Mac, for one that is missing and that yardr knows a way for: [below](#a-program-fetched-on-a-mac)), `any_of` (on the item `a harness`, on a Mac: the harnesses that can be fetched, each an item with its `fetch`), `harness` (the kind, for a harness), `label` (the harness's name as people write it, for a harness), `provider` (who makes it, for a harness; absent when the file names none), `harness_file` (the version of yardr's file for the kind; absent when it has none), `harness_login` (true for a kind whose login a program can lead: [below](#a-harnesss-login-led-by-a-program); absent for a kind that is logged in in a terminal), `offers` (the models the kind's file suggests: each a `model` id, a `label`, and what it is `recommended` for; absent when the file names none), `optional` (a harness nothing runs on yet), `version` with `state` (`answers`, `fails`, `hangs`, `not_checked`) and `output`; for herdr, `server` with `state` (`running`, `not_running`, `unknown`) and `output` (what herdr said); under `--try`, for a harness, `try` with `state` (`ok`, `fails`, `hangs`, `not_tried`) and `output` (the answer's last line, what it printed, or why it was not tried) |
| `tried` | whether `--try` was given |
| `state` | `server` (`running`, `not_running`, `unreachable`), `server_error`, `daemon`, `yardmaster` (`name`, `crew`, `enabled`, `running`, `state`, `agent`, `failures`, `reason`), `crew`, `failing`, `login` (the crew at their login screen, each with `name`, `kind` and `line`; absent when there is none), `held`, `cycles` (each a list of ids in blocking order, the first repeated at the end; absent when there is none), `mail` (`user`, `unread`, `from`; only when who runs it is a user), `parties` (the `names` notes), `copied_on` (the `crew` notes on a copied `--on`), `config` (the `config` notes; absent when there is none), `codex_trust` (the `codex` notes; absent when there is none), `kimi_hooks` (the `kimi` notes; absent when there is none); absent with no yard |
| `steps` | each `key`, `title`, `done`, `detail`, `next` (the command) when not done, and `depot` (the depot the command names) once the yard has one |
| `next` | the first step not done; absent when all are |
| `optional` | each `key` (`web`, `hooks`, `sources`, `peers`, `packs`, `limits`, `login_service`, `herdr_login_service`), `title`, `configured`, `detail`, `notes`; `skipped` (true on a part that was not looked at, whose `configured` then says nothing); on `web`, `views`: the views that run, as `yardr status --json` lists them |
| `skipped` | the checks that were not made, on a yard whose server is not running: `yardmaster`, `crew`, `held`, `cycles`, `mail`, `names`, `config`, `codex`, `steps`, `hooks`, `sources`, `peers`, `packs`, `limits`. `state` then holds `server` and `daemon` and nothing that was read, and `steps` is empty. Absent otherwise |
| `faults` | the `FAULT` lines, one string each; empty when the exit status is 0 |

## A program fetched on a Mac

A Mac as it comes has no npm and no Homebrew, and the person a window such
as footplate sets a yard up for has no terminal open. So for a program that
is missing, `yardr doctor --json` says on macOS how a program can fetch it
with nothing but the Mac: one file its maker publishes, of one version, with
the checksum it had when yardr's data was verified. yardr fetches nothing
itself. This is data for a program that does.

```json
{
  "program": "herdr", "found": false, "get": "https://herdr.dev",
  "fetch": {
    "version": "0.9.4",
    "url": "https://github.com/herdrdev/herdr/releases/download/v0.9.4/herdr-macos-aarch64",
    "sha256": "ea20bfff9cb8c83a996081c926e6ff9370de8e543baa859bca48d19175d895ff",
    "size": 22547232,
    "path": "/Users/pat/.local/bin/herdr",
    "verified": {"from": "https://herdr.dev/docs/install/", "on": "2026-10-13"}
  }
}
```

| field | |
|---|---|
| `version` | the program's version the file is of |
| `url` | the file, at its maker's own address, for this machine (Apple silicon or Intel). The address names the version, so the file at it does not change; it may answer with a redirect to the maker's storage |
| `sha256`, `size` | what the file must have. They are yardr's, written down when the way was verified, and not read from the maker's site at the time of the fetch: a checksum served beside a download shows that it is whole, not who made it |
| `member` | present when the file is a gzipped tar: the name of its one member that is the program. Absent when the file is the program |
| `run` | present when the program is put in place by its maker's own install command: the arguments to run the fetched file with. Absent when the file itself is the program to keep |
| `path` | where the program is afterwards, under the home of the user doctor ran as: where its maker's own installer puts it (`~/.local/bin` for all three) |
| `verified` | `from`, the maker's own page the way was read from, and `on`, the day |

What a program that fetches does with it, in this order:

1. If a file is at `path` already, it fetches nothing: the program is
   installed and not on PATH (step 6).
2. It downloads `url` over https to a place of its own.
3. It compares the size and the SHA-256 of what it got with `size` and
   `sha256`. On a difference it keeps nothing and runs nothing.
4. With `member`, it takes that one member out of the gzipped tar, a regular
   file, and nothing else.
5. With `run`, it makes the file executable, runs it with those arguments as
   the user, with no shell and stdin closed, and removes the file: the
   program is then at `path`. Without `run`, it moves the file to `path`,
   mode 0755, making the directory.
6. It puts the directory of `path` on the PATH of what it starts from then
   on, `yardr doctor` first, which then finds the program. yardr adds nothing
   to a PATH and writes no shell profile, and neither does any step here: a
   terminal opened later finds the program only once its own PATH has that
   directory, as doctor's line for a program the login shell does not find
   says.

No package manager, no shell and no `curl | sh` is in it. The version is
pinned, and each of these programs has its own way to a newer one: `herdr
update`, Claude Code's own updates in the background, `codex update`.

| program | what is fetched | verified from, on 2026-10-13 |
|---|---|---|
| herdr 0.9.4 | the release's binary for the Mac, moved to `~/.local/bin/herdr` | <https://herdr.dev/docs/install/>, "Download manually"; the checksums are those of <https://herdr.dev/latest.json>. herdr publishes no signature and its binaries are signed ad hoc, by no Developer ID: the checksum here is all that says the file is the one that was verified |
| Claude Code 2.1.296 | the release's binary, run with `install 2.1.296`, which puts the launcher at `~/.local/bin/claude` | <https://code.claude.com/docs/en/setup>, "Binary integrity and code signing", and Anthropic's own installer (<https://claude.ai/install.sh>), which does the same two things; the checksums are those of the version's `manifest.json`, which Anthropic signs. The binaries are signed by Developer ID Application: Anthropic PBC (Q6L2SF6YDW). `install` needs the network and downloads the version once more itself: the checksum here is of the installer that is run, and what ends at `path` is Anthropic's own download of that version |
| Codex 0.162.1 | the archive the README names for a Mac, whose one member is moved to `~/.local/bin/codex` | <https://github.com/openai/codex/blob/main/README.md>; the checksums are the digests of <https://releases.openai.com/codex/channels/latest>. The binaries are signed by Developer ID Application: OpenAI OpCo, LLC (2DC432GLL2). It is the README's binary alone, without what OpenAI's installer puts beside it |

git has no `fetch`: on a Mac it is always `xcode-select --install`, Apple's
own dialog. muse, opencode, grok, kimi, pi and every kind with no harness
file have none either, because no way of theirs was verified: an item with no
`fetch` is a program installed by hand, and its `get` says where. On Linux
no item has a `fetch` or an `any_of`, whatever the program: there is no one
way to install without a terminal there, so every missing program keeps its
`get`.

On a home with no yard and no harness installed, the machine has one item
for the harness, `a harness`. On a Mac it carries `any_of`: an item for each
harness that can be fetched (`harness`, `program`, `get`, `fetch`), any one
of which answers it.

`yardr harness show <kind> --json` prints a kind's `fetch` as its file has
it, on any system: `path` with `~` for the home, and under `targets` the
file for each kind of Mac (`darwin/arm64`, `darwin/amd64`), where doctor
gives the one of this machine. herdr's is beside its hint in
`internal/cli/needs.go`.

**A fetch changes only with a bead that verifies it again.** That bead reads
the maker's own page, downloads the file for both kinds of Mac, compares
each with the checksum the maker publishes, runs the program in an empty
home, and sets `verified` to that page and that day. A new version, a new
address and a new way to put the program in place are each such a change.
The loader refuses a fetch that is not https, whose address does not name
its version, or that names a system that is no Mac (`TestFetchCheck`).

## A harness's login led by a program

A harness is logged in on its own screen, in a terminal, and the person a
window such as footplate sets a yard up for has none open. So for a kind
whose file has a login recipe, a program leads the login:

```sh
yardr harness login claude          # says what to run in a terminal; starts nothing
yardr harness login claude --json   # for a program: needs on stdout, answers on stdin
yardr harness login claude --group hal-builders --json   # into that group's folder
```

With `--json` yardr starts the harness's own login command (`claude auth
login`), in an empty directory, on pipes it holds, and prints what the login
needs next, one JSON object a line. The program shows each to the person and
answers on stdin, one JSON object a line, with the one field of the need it
answers.

With `--group` the login command and the proving call run with that group's
env: its `env_file` is read as the runner reads it for the group's
sessions, and the login lands in the folder the env names
(`CLAUDE_CONFIG_DIR` for Claude Code). The JSON steps are unchanged. A
login made for a group sits in a folder on this machine, which whoever
runs the machine can read: bringing a subscription trusts the operator the
way the machine's own login does.

| line on stdout | what the program does | answer on stdin |
|---|---|---|
| `{"need":"choose","text":"How do you log in to Claude Code?","options":["Claude subscription","Anthropic Console (API usage billing)"]}` | shows the options; the one chosen becomes a flag of the login command, which is started only then | `{"choose":"Claude subscription"}` |
| `{"need":"open_url","url":"https://…"}` | offers the page to sign in on. The address is always `https`. Claude Code opens a page for the same sign-in itself where it can, and a login made on that one ends the run with nothing more to do | none |
| `{"need":"input","text":"Paste code here if prompted >","secret":true,"login":"claude-a1b2c3d4"}` | asks for what the page of `open_url` gave (a code), for as long as the login has not ended by itself. The input also waits in the yard under `login`, where a secret ask naming it (`yardr bead ask --secret-login`) answers it as stdin does, for a person with no stdin to the leader | `{"input":"<the code>"}` |
| `{"need":"confirm","text":"Check that Claude Code answers? That makes one real call to it, which costs a model call on your account with it."}` | asks. It comes once the login command has ended well | `{"confirm":true}` or `{"confirm":false}` |
| `{"done":true,"kind":"claude","proved":true,"try":{"state":"ok","output":"ok"}}` | the end | |
| `{"error":"…"}` | the end of a run that failed, as every `--json` command of yardr ends on an error | |

The proof is the call [`--try`](#--try-one-real-call-to-each-harness) makes,
for this kind only: the login command ending well is not a harness that
answers. It costs a model call, so it is made on `{"confirm":true}` only.
`proved` is whether it answered, and `try` is what it came to, as on a
machine item of doctor's JSON; after a `false` its `state` is `not_tried`.

| the run | last line | exit status |
|---|---|---|
| logged in, and the call answered | `done`, `proved` true | 0 |
| logged in, and the call was declined, or stdin closed before the answer | `done`, `proved` false, `try.state` `not_tried` | 0 |
| logged in by the login command's word, and the call failed or did not end | `done`, `proved` false, `try.state` `fails` or `hangs` | 1 |
| the login command failed (a code that is none) | `error`, with the command, how it ended and the last lines it printed | 1 |
| stdin closed before a method was chosen | `error`; the login command was never started | 1 |
| stdin closed before the login command ended | `error`; the command is stopped, its whole process group | 1 |
| the login took longer than 15 minutes | `error`; the command is stopped | 1 |
| no input within 10 minutes, on stdin or through the yard | `error`; the command is stopped | 1 |
| an answer that is no JSON object, a method that is none, an input of several lines | `error`; the command is stopped | 1 |
| a kind with no recipe, or a harness that is not on PATH | `error`; nothing was started | 1 |

An answer to a need that was not asked, or that is over, is passed by: the
login may end in the browser while the person still types the code. So the
program reads lines until `done` or `error`, and does not count on the order
of what it sent.

What is put in is passed to the login command as one line and kept nowhere:
yardr writes it to no file and no event and repeats it in no line of its
own, and the program that asked for it must not keep it either (`secret`).
An input answered through a secret ask is handed to the login the same way,
and is kept nowhere either: the answer holds the names that were given, to
no note and no file.
The `error` of a login command that failed carries the last lines that
command printed, as it printed them. The credential the login makes is the harness's own, kept where
the harness keeps it. yardr opens no browser: `open_url` is data.

**A recipe is read from what one version of a harness printed.** It is the
kind's `login_recipe` ([groups.md](groups.md#what-yardr-knows-of-a-kind);
`yardr harness show claude` prints it): the login command, the methods it
has flags for, and patterns for the line with the page and for the line it
takes input on. Claude Code's was read from `claude auth login` of 2.1.288
on 2026-10-13, run on pipes with no terminal and no credentials. A harness
that prints something else tomorrow breaks it: a line that no longer
matches is a need that never comes, and the run then ends when the person
gives up (the program closes stdin) or at 15 minutes. The login in a
terminal is not touched by that. A recipe changes only with a bead that
runs the login command again and sets `verified` to the version and the
day; what Claude Code prints once a login is made was not run, since no
builder logs in, and the recipe goes by the command's exit status there.

Only Claude Code has a recipe. For every other kind `yardr harness login
<kind> --json` ends with an `error` that says so, the item of doctor's JSON
has no `harness_login`, and the person logs in once in a terminal, on the
harness's own screen — with the group's env set, where `--group` names
one. Without `--json` the command prints what to run there.

The login needs no yard, no server and no herdr: it runs on a home with no
yard, before one is created. With `--group` it reads the yard for the
group's env, so that run needs one.

## yardr status

`yardr status` answers another question than doctor: what runs right now,
and where is it reached. It prints one block, with a part only for what the
yard has:

```
yard       /Users/jo/.yardr  name yardr  operator Jo
server     running  pid 4711  build abcdef123456  since 10-05 09:12
daemon     running (pid 4720)
           login service: not installed (yardr daemon install)
herdr      session default, socket /Users/jo/.config/herdr/herdr.sock (this shell's, since the yard's herdr_session setting is unset)
           login service for its server: not installed (yardr up --login)
crew       assembly    not running
           yardmaster  idle         herdr agent yardr-yardmaster
web view   http://127.0.0.1:8791/     pid 4801  build abcdef123456  this machine only
           http://192.168.1.20:9000/  pid 4950  build abcdef123456  open to the network (--unsafe), no login
           login service: not installed (yardr web install)
hooks      the server takes webhooks on 127.0.0.1:8790
           tickets  linear  http://127.0.0.1:8790/hooks/tickets
peers      north  https://north.example
devices    Jo  phone  last used 10-12 19:40 from 100.64.0.7
work       3 sessions running (web/builders 2, web/reviewers 1); 2 beads wait for a person (decisions 2); 1 held
```

| part | what it says |
|---|---|
| `yard` | the home, the yard's name, the operator |
| `server` | its pid, the build it runs and since when that build runs (a server that replaced itself with a new build counts from then). `this yardr is build <build>` follows when the yardr that was asked is another build. `not running: any yardr command that needs the yard starts it (yardr ready), or yardr server start` when none answers: status starts none ([below](#a-server-that-is-not-running)). `cannot be reached: <why>` when one answers and cannot be asked about the yard: then only `yard`, `daemon` and `web view` follow |
| `daemon` | `running (pid <pid>)` or `not running`, and its login service, both as `yardr daemon status` prints them |
| `herdr` | the herdr session the yard's panes are in and its socket, and whether the yard's `herdr_session` setting names it or the shell does; then the login service for its server: `installed and loaded: <manager> starts herdr's server at login (<file>)`, `installed: launchd starts herdr's server from the next login on (<file>)` for one written beside a server that ran, `one of your own, not yardr's (<file>)`, or `not installed (yardr up --login)` |
| `crew` | each with its state as [`yardr chat`](chat.md) lists it (`not running`, `working`, `idle`, `blocked`, `failed to start`, `not logged in`, `running`) and, while it runs, its herdr agent |
| `web view` | each view that runs: the URL to open, its pid, the build it runs, and `this machine only` or `open to the network (--unsafe), no login`; `this yardr is build <build>: ...` when a view runs another build than the yardr that was asked, since a view does not replace itself when a new yardr is installed; `not running: yardr web serve` when none does; then the web view's login service ([web.md](web.md#where-it-runs)) |
| `hooks` | what the running server takes webhooks on, then each hook, its source and the URL its source posts to. Absent on a yard with no hook and no address |
| `peers` | each linked yard and where it is called, or `it calls this yard`. Absent with no peer |
| `devices` | each [device](users.md#devices) of a user: the user, the device's name, and when and from which address it was last used, or `never`. Absent with no device |
| `backups` | `every <period>, <n> kept in <directory>` as the settings `backup_every`, `backup_keep` and `backup_dir` are read, then `last <when>  <file>`, the newest dated copy in the directory, or `none made yet`; `none kept: yardr settings set backup_every 1d ...` on a yard that did not ask for copies, with the last one when the directory holds some. Read from the store file when its server is stopped ([configuration.md](configuration.md#copies-of-the-store)) |
| `work` | how many sessions run something, by group: those of a group whose runner starts an agent or a script (`herdr`, `exec`). `<n> beads wait for a person`, by group, when a `manual` group has claimed beads: such a claim is a session in `yardr session list` with nothing running behind it, so it is counted here and not as a session running; a bead in `decide` is one of them. With the setting `max_sessions` set, `, <n> of max_sessions <cap>` follows the sessions: how many of them count against it ([groups.md](groups.md#how-many-sessions-the-machine-carries)), which a `peer` group's do not; at 0, `, max_sessions 0 (paused)` follows instead. Then how many open beads are held |

It judges nothing. When doctor reports a fault, one last line says so and
no more: `yardr doctor reports <N> faults, and says what they are.` To know
that, status runs doctor's checks too (the version flags, herdr's server),
so it takes as long as doctor does.

It is read-only and starts nothing. The exit status is 0 on any home that
holds a yard, faults or not, and 2 on a home with none, where it says what
any command says there and starts no onboarding. `--json` prints the same as
one object: `home`, `name`, `operator`, `server` (`running`, `error`, `pid`,
`build`, `since`, `cli_build`, `differs`), `daemon` (`running`, `pid`,
`service`), `herdr` (`name`, `socket`, `from`), `herdr_login_service`
(`supported`, `manager`, `name`, `file`, `installed`, `loaded`, and
`foreign`: the files of a service for herdr's server that is not yardr's),
`crew` (`name`, `state`, `agent`), `web` (`views`, each
`url`, `addr`, `pid`, `unsafe`, `open`, `started`, `revision`, `build`,
`differs`; `service`), `hooks` (`addr`, `listening`, `hooks` with `name`,
`source`, `url`), `peers` (`name`, `url`), `devices` (`user`, `name`,
`created_at`, `created_by`, `last_used`, `last_from`), `work` (`sessions` by group,
`waiting` by group: the beads a manual group has claimed, `held`) and `faults`, the number doctor reports.

### A server that is not running

Every other command that needs the yard starts its server. `yardr status`
does not, nor does doctor
([above](#a-server-that-is-not-running)): it says what runs, and a server it
started would be one that did not. On a yard whose server is stopped it prints what the home and the
machine say without the store, and exits 0:

```
yard       /Users/jo/.yardr
server     not running: any yardr command that needs the yard starts it (yardr ready), or yardr server start
daemon     not running
           login service: not installed (yardr daemon install)
herdr      login service for its server: not installed (yardr up --login)
web view   http://127.0.0.1:8791/  pid 4801  build abcdef123456  this machine only
           login service: not installed (yardr web install)

crew, hooks, peers and work: not known while the server is not running
```

The yard's name and its operator are settings in the store, so they are
left out, and so is the line about doctor's faults. In the JSON
`server.running` is `false` and `name`, `operator`, `crew`, `hooks`,
`peers`, `devices`, `work` and `faults` are absent. A home counts as a yard here when
it holds a store (`yard.db`): whether that store was ever set up is known
only to its server.
