# yardr doctor

`yardr doctor` says in one go what a yard has and what it lacks: the programs
on this machine, what of the yard runs, and how far its setup is. It is the
check to run after the onboarding, after an upgrade, and whenever the yard
does nothing.

```sh
yardr doctor
yardr doctor --json | jq -r '.next.next'   # the command for the next setup step
```

## Read-only

Doctor installs nothing and changes nothing in the store. Where a program is
missing it prints where to get it and never runs that. It starts no daemon.
Two things it does do:

- It runs each harness it finds with its version flag (`claude --version`),
  for up to 5 seconds each, side by side. That runs a program and changes
  nothing.
- On a yard whose server is not running, it starts the server, as the first
  command on a yard always does. It says so.

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
| `ok git <path>`, `ok herdr <path>` | on PATH |
| `FAULT <program> not on PATH: <what it is needed for>. Get it: <where>` | missing. For git and herdr the same words the onboarding uses before its first page. For a harness, `<where>` is its install command where yardr knows one (claude, codex, opencode), else a pointer to the agent's own documentation |
| `FAULT a harness not on PATH: …` | a home with no yard, and no agent herdr starts is installed |
| `ok <harness> <path>, the harness of <who>: answers its version flag (<its first line>)` | found, and it runs |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but its version flag fails: <error and its first line>` | found, and it does not run |
| `FAULT <harness> <path>, the harness of <who>: on PATH, but it did not answer its version flag within 5s` | found, and it hangs. On macOS the line goes on: `On macOS that can be a quarantined binary waiting on a Gatekeeper dialog nobody sees: run <harness> once in a terminal and answer it` |
| `ok <harness> <path>, the harness of <who>: not checked, yardr knows no version flag for <kind>` | found, and not run: the flag is known for claude, codex, grok, kimi, opencode and pi |
| `note <harness> …` | the two failing lines above, for a harness nothing runs on yet (a home with no yard) |

### The yard

Not printed on a home with no yard.

| line | meaning |
|---|---|
| `ok server running` | |
| `ok server was not running; this command started it, as the first command on a yard does` | |
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

### Setup

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
| `not configured web view: nothing keeps it running; yardr web serve shows the yard in a browser while it runs, yardr web install keeps it running from login on (docs/web.md)` | no login service for the web view is installed for this home. One started by hand (`yardr web serve`, `yardr web start`) is not looked for |
| `configured web view: kept running as a login service at http://<addr>/ (<file>)` | `yardr web install` wrote `<file>` ([web.md](web.md#keeping-it-running)); whether the service manager has it loaded is not asked |
| `not configured hooks: no source outside the yard reaches it; yardr hook add --help (docs/subscriptions.md)` | no hook |
| `configured hooks: <name> (<source>), …` | |
| `note hooks: hook <name> is blocked: <why>` | the receiver refuses its deliveries |
| `note hooks: the server takes webhooks on <addr> (the hook_addr setting)` | hooks exist, or the yard has an address without one: what the running server listens on, and that the address is the yard's `hook_addr` setting ([configuration.md](configuration.md#settings)) |
| `note hooks: the server takes webhooks on <addr> (YARDR_HOOK_ADDR, set where the server started)` | the same, with the variable's address: set where the server starts, it is the one in use. `; the hook_addr setting (<addr>) is not used while the variable is set there` follows when the setting names another |
| `… ; hook_addr is <addr> now, which it takes at its next start: yardr server stop (the next command starts it again)` | the setting was changed (or unset) after the server read it |
| `note hooks: the server could not listen on <addr> (<the setting or the variable>): <error>. Another yard or program may have the address; give this yard its own with yardr settings set hook_addr <host:port>, then yardr server stop (the next command starts it again)` | the address is taken, by another yard given the same one as a rule. The yard works; no delivery reaches it. Printed with no hook too. For the variable's address: `change the variable where the server starts, or unset it there and give this yard its own with …` |
| `note hooks: the server takes no webhooks yet: hook_addr is <addr>, which it takes at its next start: yardr server stop (the next command starts it again)` | hooks exist and the setting was set after the server started with no address |
| `note hooks: the server has no address to take webhooks on: yardr settings set hook_addr 127.0.0.1:8790, then yardr server stop (the next command starts it again)` | hooks exist, and neither the setting nor the variable gives the server an address |
| `note hooks: the server has not said what it takes webhooks on` | hooks exist, and the running server is a build from before it wrote `hooks.state` |
| `not configured peers: this yard is linked with no other; yardr peer --help (docs/peers.md)` | no peer |
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
| `machine` | the programs: `program`, `for`, `found`, `path`, `get` (when missing), `harness` (the kind, for a harness), `optional` (a harness nothing runs on yet), `version` with `state` (`answers`, `fails`, `hangs`, `not_checked`) and `output` |
| `state` | `server` (`running`, `started`, `unreachable`), `server_error`, `daemon`, `mayor` (`name`, `resident`, `enabled`, `running`, `state`, `agent`, `failures`, `reason`), `residents`, `failing`, `held`; absent with no yard |
| `steps` | each `key`, `title`, `done`, `detail`, `next` (the command) when not done, and `rig` (the rig the command names) once the yard has one |
| `next` | the first step not done; absent when all are |
| `optional` | each `key` (`web`, `hooks`, `peers`, `packs`, `login_service`), `title`, `configured`, `detail`, `notes` |
| `faults` | the `FAULT` lines, one string each; empty when the exit status is 0 |
