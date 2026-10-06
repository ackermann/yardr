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

## Read-only

Doctor installs nothing and changes nothing in the store. Where a program is
missing it prints where to get it and never runs that. It starts no daemon,
no yardr server and no herdr server. What it does do:

- It runs each harness it finds with its version flag (`claude --version`),
  for up to 5 seconds each, side by side. That runs a program and changes
  nothing.
- It asks the herdr it finds whether its server answers (`herdr agent list`,
  the call a resident's start begins with), for up to 5 seconds.
- For a program that is not on PATH it runs your login shell once (`$SHELL
  -l -c 'command -v <program>'`, stdin closed, up to 5 seconds) to ask where
  that finds it. With no `SHELL`, or a shell that does not answer, it says
  nothing of it.

## A server that is not running

Every other command that needs the yard starts its server. Doctor does not,
and neither does [`yardr status`](#yardr-status): a command asked what is
wrong changes nothing of what runs to answer. On a yard whose server is
stopped it says so, as a note, and reports what the machine and the home say
without the store:

- the machine's part, with every harness it finds listed as `a harness on
  this machine`: which ones the yard runs on is in the store, so one that
  does not work is a note there, not a fault;
- the daemon, and of the optional parts the web view and the login service
  (without its notes on rigs);
- the mayor, the residents, the held beads, the setup steps, hooks, peers
  and packs as `skipped: the server is not running`, each on the line it
  always has. Cycles, mail and names, which have a line only when there is
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
sits at a screen nobody sees, and the resident's start times out.

`yardr doctor --try` makes one real call to each harness: its one-shot form
with the prompt `Reply with the one word ok and nothing else.`, stdin
closed, in an empty directory of its own, for up to 45 seconds each, side by
side. It prints the answer, or what the harness printed when the call failed
or did not end. The onboarding offers the same call for the mayor's harness
before it starts the mayor
([configuration.md](configuration.md#starting-the-yard)).

| kind | the call |
|---|---|
| claude | `claude -p <prompt>` |
| codex | `codex exec --skip-git-repo-check <prompt>` |
| grok | `grok -p <prompt>` |
| kimi | `kimi -p <prompt>` |
| opencode | `opencode run <prompt>` |
| pi | `pi -p <prompt>` |

Any other kind is reported as not tried. `--try` makes this call even when
the separate version check fails or hangs, so it can show what the call
itself says.

**It costs a model call per harness**, on your account with it, so doctor
never makes it unasked: without `--try` one line says that no harness was
asked. A resident's start that fails or does not answer names it
(`yardr init --start`, `yardr resident show`).

The call is the harness's one-shot form, not the command herdr starts it
with in a pane: a flag herdr passes that the harness does not know is not
found by it.

On a home with no yard it says so, checks the machine, and creates nothing:
no store, no server. It runs without a terminal too, where other commands
are refused on such a home.

## Exit status

`1` when something the yard needs is missing or not working: every line
marked `FAULT`. `0` otherwise. A setup step not done, a held bead, a mayor
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
and every kind a herdr group or resident names. `<who>` lists them (`the
yard's default, resident mayor, group web/builders`). On a home with no yard
the harnesses are those that are installed, any of which a yard can be
created on; they are then never a fault.

| line | meaning |
|---|---|
| `ok git <path>` | on PATH |
| `ok herdr <path>, its server is running` | on PATH, and its server answers |
| ``FAULT herdr <path>: on PATH, but its server is not running (server_not_running: <herdr's message>): the daemon cannot start agents until it is. Start it: run herdr in a terminal (ctrl+b q detaches, the server stays); on a machine without a desktop, run `herdr server`, its headless server, as a service (docs/install.md, "herdr's server")`` | herdr says no server is at its socket: every start of a resident or a bead's agent fails with `server_not_running` until one is ([install.md](install.md#herdrs-server)) |
| `note herdr <path>: on PATH; whether its server is running could not be asked: <what herdr said>` | herdr answered with something else, or not in 5 seconds: not known to be down, so no fault |
| `FAULT <program> not on PATH: <what it is needed for>. Get it: <where>` | missing. For git and herdr the same words the onboarding uses before its first page. For a harness, `<where>` is its install command where yardr knows one (claude, codex, opencode), else a pointer to the agent's own documentation |
| ``FAULT <program> not on PATH: <what it is needed for>. Found at <path> by your login shell, which this shell's PATH does not have: add <dir> to PATH here, or for the daemon's service run `yardr daemon install` from a login shell`` | missing here and installed: your login shell finds it. A shell without a terminal (ssh with a command, a service) reads no profile, so it lacks what `~/.profile` adds, `~/.local/bin` for one. The daemon's service gets the PATH of the shell that installs it ([configuration.md](configuration.md#login-services)) |
| `FAULT a harness not on PATH: …` | a home with no yard, and no agent herdr starts is installed. When the login shell finds claude, codex, grok, kimi, opencode or pi, the line above is printed for each it finds instead |
| `ok <harness> <path>, the harness of <who>: answers its version flag (<its first line>)` | found, and it runs |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but its version flag fails: <error and its first line>` | found, and it does not run |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but it did not answer its version flag within 5s` | found, and it hangs. On macOS the line goes on: `On macOS that can be a quarantined binary waiting on a Gatekeeper dialog nobody sees: run <harness> once in a terminal and answer it` |
| `ok <harness> <path>, the harness of <who>: not checked, yardr knows no version flag for <kind>` | found, and not run: the flag is known for claude, codex, grok, kimi, opencode and pi |
| `note <harness> …` | the two failing lines above, for a harness nothing runs on yet (a home with no yard) |
| `note harness on PATH is not "works": no harness was asked for an answer. yardr doctor --try makes one real call to each (it costs a model call per harness)` | without `--try`, when a harness was found. Not printed on a yard whose server is not running, where `--try` is refused |
| `ok <harness> tried: one real call answered (<the last line of its answer>)` | `--try`: the harness ended with status 0 |
| `FAULT <harness> tried: one real call failed: <error>: <the last three lines it printed>` | `--try`: it ended with another status. The lines are the end of what it printed, stdout and stderr as they came, joined with ` / `: where a harness says that a login is needed or which flag it does not know |
| `FAULT <harness> tried: one real call did not end within 45s; it printed: <the last three lines>` | `--try`: it hung, at a question or a login screen as a rule |
| `note <harness> not tried: yardr knows no one-shot call for <kind>` | `--try`: a kind not in the table above |
| `note <harness> tried: …` | the two failing lines, for a harness nothing runs on yet (a home with no yard) |

### The yard

Not printed on a home with no yard.

| line | meaning |
|---|---|
| `ok server running` | |
| `note server not running (yardr server start, or any command but status and doctor, starts it)` | no fault. Doctor starts none ([above](#a-server-that-is-not-running)): the daemon's line follows, then `note mayor skipped: the server is not running` and the same for `residents` and `held`, and the block ends |
| `FAULT server cannot be reached: <error>` | nothing else of the yard can be read; the block ends here and there are no steps |
| `ok daemon running` | a daemon holds the home's lock |
| `FAULT daemon not running: no bead is dispatched and no resident starts. Start it in a pane of its own (yardr daemon) or in the background (yardr init --start)` | |
| `ok mayor <name> is running (herdr agent <agent>)` | the resident named by the `mayor` setting has a session |
| `FAULT mayor no resident <name>: yardr init adds it` | |
| `note mayor <name> is stopped (disabled). Let it run again: yardr resident start <name>` | stopped on purpose: no fault |
| `FAULT mayor <name> is not running: <N> failed starts so far, the last with: <reason>. Once that is fixed: yardr resident start <name>` | its starts fail; `the daemon gave up after <N> tries` once it has. A session gone within 60 seconds of its start is a failed start, with the reason `the session ended <time> after its start (…)` ([residents.md](residents.md)) |
| `FAULT mayor <name> is not running: the daemon starts it` | no daemon |
| `FAULT mayor <name> is not running, and no start has failed: the daemon may still be at it (yardr resident list)` | |
| `FAULT resident <name> is not running: …` | another resident whose starts fail, as for the mayor |
| `ok residents <N>, none failing to start` | |
| `ok held no bead is held` | |
| `note held <N> held, waiting for someone: <ids> (yardr bead show <id>; yardr bead unhold <id> resumes it)` | no fault: a held bead waits for a person |
| `note cycle <a> blocks <b> blocks <a>: these beads wait on each other and are never ready. Remove the edge that is wrong: yardr dep rm <blocker> <bead>` | no fault: open beads that block each other in a circle, one line per cycle. `yardr dep add` refuses the edge that would close one, so these are from a store written before it did; yardr removes nothing itself ([pipeline.md](pipeline.md#cycles-of-blockers)) |
| `ok mail nothing unread for <user>` | printed when who runs the command is a user ([users.md](users.md)); absent otherwise |
| `note mail <N> unread from <names> for <user>: yardr inbox reads them, --ack marks them read` | no fault: nothing nudges a person, so this line and bare `yardr` are where they learn of mail |
| `note names group <group>: <names> are neither a user nor a resident, so nobody of that name can be mailed (yardr user add <name>)` | no fault: members of a group are names, taken as given |
| `note names resident <name> has the operator's name, so the operator has no user and no inbox: mail to <name> is the resident's. Rename one of the two: yardr settings set operator <name> gives the operator a name of their own and adds their user` | no fault: a yard from before residents and users shared their names |

### Setup

On a yard whose server is not running the block is one line, `note skipped:
the server is not running`, and no next step is named.

The steps from a yard to a first merged bead, in the order they are taken.
Each line is `done` or `todo`, the step, and what in the store shows it done
or what is lacking. The steps are read from the store and nothing else (the
store reads a rig's flow from its base branch), by one function: the mayor's
first run uses it through this command, and `yardr setup` takes the first
step not done as a form ([setup.md](setup.md)), so whoever walks you through
the setup can take over from anyone else.

| step (`key` in JSON) | done when |
|---|---|
| `a rig` (`rig`) | the yard has a rig |
| `the rig's .yardr files on its base` (`rig_files`) | every rig has a flow where yardr reads it: under `.yardr/flows/` on its base branch (written by `yardr rig init` and committed), in a dir rig's directory, or in the store when the rig follows a pack's flow |
| `a group that builds` (`builders`) | a group has the role `builder.md` as its prompt, or a route for stage `new` |
| `a group that reviews` (`reviewers`) | the same with `reviewer.md` and `review` |
| `a group or person that lands approved beads` (`landing`) | a group has the role `merger.md`, is an exec group whose command merges (a refinery), or has a route for stage `approved`: people who merge by hand count once they are routed |
| `routes for new, review and approved` (`routes`) | a route exists for each of the three stages. The command for a missing one names the group that does the stage's work (the builders for `new`), once the yard has one |
| `a first bead` (`first_bead`) | a bead exists in a rig (a resident's journal and memories do not count) |
| `a first merged bead` (`merged_bead`) | a bead of a rig was closed by reaching its flow's terminal stage; one closed by hand does not count |

A yard whose rigs are all dir rigs lands nothing: it has no landing step, its
routes are `new and review`, and its last step reads `a first bead through to
the flow's end`.

### Optional

What a yard runs without. Each line is `configured` or `not configured`,
never a fault. Not printed on a home with no yard.

| line | meaning |
|---|---|
| `skipped hooks: the server is not running` | the yard's server is not running; the same for `peers` and `packs` |
| `not configured web view: not running, and nothing keeps it running; yardr web serve shows the yard in a browser while it runs, yardr web install keeps it running from login on (docs/web.md)` | no web view runs, and no login service for one is installed for this home |
| `not configured web view: running at <URL> (pid <pid>, this machine only), …; nothing keeps it running: yardr web install keeps it running from login on (docs/web.md)` | a view started by hand (`yardr web serve`, `yardr web start`) runs, each with the URL to open; no login service is installed. `not configured` is about the service alone. A view that is `open to the network (--unsafe), no login` says that in place of `this machine only` |
| `configured web view: kept running as a login service at http://<addr>/ (<file>); running at <URL> (pid <pid>, this machine only), …` | `yardr web install` wrote `<file>` ([web.md](web.md#keeping-it-running)), and these views run now, the service's or others; whether the service manager has it loaded is not asked |
| `configured web view: kept running as a login service at http://<addr>/ (<file>); not running now` | the service is installed and no view runs |
| `note web view: <URL> (pid <pid>) is open to the network, no login: it was started with --unsafe, and anyone who can reach this machine on port <port> can read the yard` | one line per running view that listens off loopback. A note, never a fault: `--unsafe` is the operator's choice ([web.md](web.md#--unsafe-every-interface)) |
| `not configured hooks: no source outside the yard reaches it; yardr hook add --help (docs/subscriptions.md)` | no hook |
| `configured hooks: <name> (<source>), …` | |
| `note hooks: hook <name> is blocked: <why>` | the receiver refuses its deliveries |
| `note hooks: hook <name>: its last delivery was refused (<reason>, <when>); it last accepted one <when>. Events of kind hook_rejected say how many` | the source reaches the yard and is turned away: a wrong or missing signature as a rule (`unauthorized`). `it has accepted none` when it never took one. The reasons, and how the events are bounded: [subscriptions.md](subscriptions.md#a-refused-delivery) |
| `note hooks: the server takes webhooks on <addr> (the hook_addr setting)` | hooks exist, or the yard has an address without one: what the running server listens on, and that the address is the yard's `hook_addr` setting ([configuration.md](configuration.md#settings)) |
| `note hooks: the server takes webhooks on <addr> (YARDR_HOOK_ADDR, set where the server started)` | the same, with the variable's address: set where the server starts, it is the one in use. `; the hook_addr setting (<addr>) is not used while the variable is set there` follows when the setting names another |
| `… ; hook_addr is <addr> now, which it takes at its next start: yardr server stop (the next command starts it again)` | the setting was changed (or unset) after the server read it |
| `note hooks: the server could not listen on <addr> (<the setting or the variable>): <error>. Another yard or program may have the address; give this yard its own with yardr settings set hook_addr <host:port>, then yardr server stop (the next command starts it again)` | the address is taken, by another yard given the same one as a rule. The yard works; no delivery reaches it. Printed with no hook too. For the variable's address: `change the variable where the server starts, or unset it there and give this yard its own with …` |
| `note hooks: the server takes no webhooks yet: hook_addr is <addr>, which it takes at its next start: yardr server stop (the next command starts it again)` | hooks exist and the setting was set after the server started with no address |
| `note hooks: the server has no address to take webhooks on: yardr settings set hook_addr 127.0.0.1:8790, then yardr server stop (the next command starts it again)` | hooks exist, and neither the setting nor the variable gives the server an address |
| `note hooks: the server has not said what it takes webhooks on` | hooks exist, and the running server is a build from before it wrote `hooks.state` |
| `not configured peers: this yard is linked with no other; yardr peer pair in a terminal links it with one, as a form (docs/peers.md)` | no peer |
| `configured peers: <name>, <name> (it calls), …` | the yards this one is linked with ([peers.md](peers.md)); `(it calls)` marks one without a URL, which is never called |
| `note peers: this yard has no key, so it signs nothing and its peers cannot know it: yardr peer key` | peers were added before the yard made its key |
| `note peers: this yard's key cannot be used, so nothing is sent to or taken from its peers: <why>` | `peer.key` is open to others, or does not hold a key |
| `note peers: no listener, no incoming: nothing can be posted to this yard, so it gets its peers' messages only by asking those it can call: <what the server says of its listener>` | the server takes nothing in. `; <names> cannot be called and so cannot be heard at all` follows the first part when a peer has no URL |
| `note peers: <N> messages for peer <name> were given up (yardr peer show <name>)` | posts to it failed 24 times, or it refused them |
| `not configured packs: no pack is imported; yardr pack apply <dir> --rig <rig> (docs/packs.md)` | |
| `configured packs: <pack> into <rig>, …` | |
| `not configured login service: the daemon runs while its terminal or the session that started it does (yardr daemon, yardr init --start); it does not come back after a reboot; yardr daemon install starts it at login` | no login service for the daemon is installed for this home |
| `configured login service: the daemon starts at login (<file>)` | `yardr daemon install` wrote `<file>`, a launchd agent or a systemd user unit ([configuration.md](configuration.md#login-services)); `yardr daemon status` asks the service manager whether it is loaded |
| `note login service: rig <rig> (<path>) is in a folder macOS protects (~/Desktop, ~/Documents, ~/Downloads): a launchd agent needs its own access there (System Settings, Privacy & Security, Files and Folders), which a daemon started in a terminal has from the terminal` | macOS, with the login service configured: one line per such rig |

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
| `machine` | the programs: `program`, `for`, `found`, `path`, `get` (when missing), `login_shell_path` (where the login shell finds one that is missing), `harness` (the kind, for a harness), `optional` (a harness nothing runs on yet), `version` with `state` (`answers`, `fails`, `hangs`, `not_checked`) and `output`; for herdr, `server` with `state` (`running`, `not_running`, `unknown`) and `output` (what herdr said); under `--try`, for a harness, `try` with `state` (`ok`, `fails`, `hangs`, `not_tried`) and `output` (the answer's last line, what it printed, or why it was not tried) |
| `tried` | whether `--try` was given |
| `state` | `server` (`running`, `not_running`, `unreachable`), `server_error`, `daemon`, `mayor` (`name`, `resident`, `enabled`, `running`, `state`, `agent`, `failures`, `reason`), `residents`, `failing`, `held`, `cycles` (each a list of ids in blocking order, the first repeated at the end; absent when there is none), `mail` (`user`, `unread`, `from`; only when who runs it is a user), `parties` (the `names` notes); absent with no yard |
| `steps` | each `key`, `title`, `done`, `detail`, `next` (the command) when not done, and `rig` (the rig the command names) once the yard has one |
| `next` | the first step not done; absent when all are |
| `optional` | each `key` (`web`, `hooks`, `peers`, `packs`, `login_service`), `title`, `configured`, `detail`, `notes`; `skipped` (true on a part that was not looked at, whose `configured` then says nothing); on `web`, `views`: the views that run, as `yardr status --json` lists them |
| `skipped` | the checks that were not made, on a yard whose server is not running: `mayor`, `residents`, `held`, `cycles`, `mail`, `names`, `steps`, `hooks`, `peers`, `packs`. `state` then holds `server` and `daemon` and nothing that was read, and `steps` is empty. Absent otherwise |
| `faults` | the `FAULT` lines, one string each; empty when the exit status is 0 |

## yardr status

`yardr status` answers another question than doctor: what runs right now,
and where is it reached. It prints one block, with a part only for what the
yard has:

```
yard       /Users/jo/.yardr  name yardr  operator Jo
server     running  pid 4711  build abcdef123456  since 10-05 09:12
daemon     running (pid 4720)
           login service: not installed
residents  mayor     idle         herdr agent yardr-mayor
           refinery  not running
web view   http://127.0.0.1:8791/     pid 4801  build abcdef123456  this machine only
           http://192.168.1.20:9000/  pid 4950  build abcdef123456  open to the network (--unsafe), no login
           login service: not installed
hooks      the server takes webhooks on 127.0.0.1:8790
           tickets  linear  http://127.0.0.1:8790/hooks/tickets
peers      north  https://north.example
work       3 sessions running (web/builders 2, web/reviewers 1); 2 beads wait for a person (decisions 2); 1 held
```

| part | what it says |
|---|---|
| `yard` | the home, the yard's name, the operator |
| `server` | its pid, the build it runs and since when that build runs (a server that replaced itself with a new build counts from then). `this yardr is build <build>` follows when the yardr that was asked is another build. `not running: any yardr command that needs the yard starts it (yardr ready), or yardr server start` when none answers: status starts none ([below](#a-server-that-is-not-running)). `cannot be reached: <why>` when one answers and cannot be asked about the yard: then only `yard`, `daemon` and `web view` follow |
| `daemon` | `running (pid <pid>)` or `not running`, and its login service, both as `yardr daemon status` prints them |
| `residents` | each with its state as [`yardr chat`](chat.md) lists it (`not running`, `working`, `idle`, `blocked`, `failed to start`, `running`) and, while it runs, its herdr agent |
| `web view` | each view that runs: the URL to open, its pid, the build it runs, and `this machine only` or `open to the network (--unsafe), no login`; `this yardr is build <build>: ...` when a view runs another build than the yardr that was asked, since a view does not replace itself when a new yardr is installed; `not running: yardr web serve` when none does; then the web view's login service ([web.md](web.md#where-it-runs)) |
| `hooks` | what the running server takes webhooks on, then each hook, its source and the URL its source posts to. Absent on a yard with no hook and no address |
| `peers` | each linked yard and where it is called, or `it calls this yard`. Absent with no peer |
| `work` | how many sessions run something, by group: those of a group whose runner starts an agent or a script (`herdr`, `exec`). `<n> beads wait for a person`, by group, when a `manual` group has claimed beads: such a claim is a session in `yardr session list` with nothing running behind it, so it is counted here and not as a session running; a bead in `decide` is one of them. Then how many open beads are held |

It judges nothing. When doctor reports a fault, one last line says so and
no more: `yardr doctor reports <N> faults, and says what they are.` To know
that, status runs doctor's checks too (the version flags, herdr's server),
so it takes as long as doctor does.

It is read-only and starts nothing. The exit status is 0 on any home that
holds a yard, faults or not, and 2 on a home with none, where it says what
any command says there and starts no onboarding. `--json` prints the same as
one object: `home`, `name`, `operator`, `server` (`running`, `error`, `pid`,
`build`, `since`, `cli_build`, `differs`), `daemon` (`running`, `pid`,
`service`), `residents` (`name`, `state`, `agent`), `web` (`views`, each
`url`, `addr`, `pid`, `unsafe`, `open`, `started`, `revision`, `build`,
`differs`; `service`), `hooks` (`addr`, `listening`, `hooks` with `name`,
`source`, `url`), `peers` (`name`, `url`), `work` (`sessions` by group,
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
           login service: not installed
web view   http://127.0.0.1:8791/  pid 4801  build abcdef123456  this machine only
           login service: not installed

residents, hooks, peers and work: not known while the server is not running
```

The yard's name and its operator are settings in the store, so they are
left out, and so is the line about doctor's faults. In the JSON
`server.running` is `false` and `name`, `operator`, `residents`, `hooks`,
`peers`, `work` and `faults` are absent. A home counts as a yard here when
it holds a store (`yard.db`): whether that store was ever set up is known
only to its server.
