# A yard's basic settings in a file in its home, and what stays in the store

Design yardr-8cgx, proposed 2026-10-13. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. The store is `yard.db` in the yard's home, owned by the yardr server, which every other yardr process reaches over a unix socket. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work. The operator is the person who runs the yard. The daemon is the process that dispatches beads and starts agents; herdr is the terminal multiplexer agents run in, and a herdr session is one named herdr server the yard's panes live in. A pack is a way of working as one directory (flows, groups, roles); a flow is the stages a bead passes. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, by mail on 13 Oct 2026: "should we just introduce a config.yml or toml in the root of the yard?", then "I just thought it could be some basic settings about the yard, vs jamming it all into the db", with the rule "flags are overrides, envs are overrides, config is source". It came up with a second yard on one machine (each needs its own ports) and yardr-d4xx (web_addr and bdp_addr as settings). The question is whether the values a yard's processes read when they start should live in one file in the yard's home instead of as rows in the store: readable, diffable, writable before the first start, and copyable to make a second yard, with each value in exactly one place.

## 2. What exists today

### 2.1. Every setting is a row in the store

The yard's settings live in the `settings` bucket of `yard.db` (`key -> value`), listed by `yardr settings get` and changed by `yardr settings set <key> <value>` (an empty value unsets). Which keys exist and what each takes is the `KnownSettings` table in internal/store/settings.go: sixteen keys, each with its help, its default, its environment variable if any, and its `Check`:

- identity and voice: `operator`, `yardmaster`, `operator_style` (`guide`, `show`, `work`, `report`), `operator_note` (one line);
- setup defaults: `default_kind`, `default_pack`;
- addresses: `hook_addr` (the address the server takes webhooks on), `web_addr` and `bdp_addr` (the addresses the web view and the BDP server listen on when their `--addr` is not given; landed by yardr-d4xx);
- the machine: `max_sessions`, `herdr_session`, `name`;
- caps: `timer_limit`, `timer_min_every`, `blob_max_bytes`, `peer_file_max_bytes`.

`Store.SetSetting` (internal/store/settings.go) refuses an unknown key with the keys there are, and a value the key's check does not take, whoever writes it. Settings are the operator's alone to change (yardr-mkfn, landed): the write is guarded by `operatorOnly` (internal/store/users.go) in the same words whatever is asked (`only the operator (<name>) may change a setting; this is <actor>`), nothing is stored on a refusal, and a yard that names no operator yet takes a setting from anyone who is no agent of its own, which is how `yardr init` names the first operator. Every accepted write commits a `setting_changed` event with the actor, the key and the value. Reading (`yardr settings get`) is anyone's.

### 2.2. Where each value is read, and when

`yardr settings get` prints the value the yard goes by and where it is from: `set`, `default`, or `environment` (internal/cli/init.go `settingRows`, `store.Setting.Effective`). The environment wins over what is set, which wins over the default, and three settings have an environment variable: `hook_addr`, `web_addr` and `bdp_addr` (`YARDR_HOOK_ADDR`, `YARDR_WEB_ADDR`, `YARDR_BDP_ADDR`). Two defaults are of the machine and the home, not the table: `operator` defaults to the login name, `name` to the home's directory name (internal/cli/init.go, `runner.YardName` in internal/runner/herdr.go).

Readers, by when they read:

- At a process's start: the server reads `hook_addr` once, in `serveHooks` (internal/server/server.go): `YARDR_HOOK_ADDR` from the environment the server started in, else the setting. A change takes the server's next start (`yardr server stop`; the next command starts it again), and `settings set hook_addr` says so (`hookAddrTakes`, internal/cli/hooks.go). What the server listens on, and whether it is the variable's or the setting's, is written to `<home>/hooks.state`, which `yardr doctor` and the pack plan read. A server that cannot listen there (another yard has the address) keeps serving its yard, logs a `hook_listener_failed` event, and doctor says what to change. This degrade-on-taken-address is decided behavior.
- Before every herdr call: `herdr_session` (internal/runner/herdr_session.go; docs/configuration.md "Settings"). Set, every call the yard makes to herdr goes to that session's socket, from the daemon, from any command and from a login service. It is changed only while nothing of the yard runs in a pane (`herdrSessionFree`, internal/store/settings.go), since panes are not moved; the one live change allowed is from unset to the session the panes are recorded in.
- On every daemon pass: `max_sessions` (`store.MaxSessions`), so a change holds at once with no restart; unset or `0` is no cap.
- When a session starts: `operator_style` and `operator_note` (both primes print them as instructions), `default_kind` (a herdr group or crew member with no kind gets it), `name` (crew agents and workspaces carry `<name>-…`).
- When something is set up or done: `default_pack` (`yardr depot init`, `yardr init`), the timer and blob caps (per operation), `operator` and `yardmaster` (role prompts are installed naming them; doctor finds the yardmaster crew member by the setting).

`st.Settings(ctx)` has on the order of thirty call sites across internal/cli, internal/runner, internal/web, internal/dispatch, internal/server and internal/rpc (including internal/rpc/user.go, which reads `operator` and the blob cap for devices calling over the network). The socket serves `Settings` and `SetSetting` (internal/rpc/client.go). Any move must reckon with that fan-out: the merge belongs in one place, where settings are served, not at every reader.

### 2.3. The web view's and the BDP server's addresses are settings since yardr-d4xx

`yardr web serve|start|install` take `--addr` (default `127.0.0.1:8791`, `webLocalAddr` in internal/cli/service.go; internal/cli/web.go), on loopback unless `--unsafe`; `yardr bdp serve|start|install` take `--addr` (default `127.0.0.1:8765`, internal/cli/bdp.go). Bead yardr-d4xx, landed, made them settings too: `web_addr` and `bdp_addr` in the `KnownSettings` table, checked as the flags are checked (`CheckWebAddr` and `checkHostPort` in internal/store/settings.go). Each service resolves its address as flag, else environment (`YARDR_WEB_ADDR`, `YARDR_BDP_ADDR`), else setting, else today's default, by one mechanism: the `longRunning` table (internal/cli/longrunning.go) names each service's setting (`Setting`) and environment reader (`EnvAddr`), and `addrFromSetting` gives the flag the environment's or the setting's value, unmarked as given, before anything starts. `args` still hands the address on always, so an install writes the resolved address into the service's file as `--addr`, given or not; the settings' help says what that means (a login service keeps the one in its file until it is installed again), and `settings set` says it too (`addrTakes` in internal/cli/longrunning.go, beside `hookAddrTakes` in internal/cli/hooks.go).

### 2.4. Login services write values into their own files at install

`yardr daemon install`, `yardr web install`, `yardr bdp install` (docs/configuration.md "Login services") write a launchd agent or systemd user unit naming the yardr binary by path, the command with its flags (`web serve --addr <addr>`), `YARDR_HOME`, `HOME`, the installing shell's `PATH`, the server-read variables (`YARDR_HOOK_ADDR`, `YARDR_WEB_ADDR`, `YARDR_BDP_ADDR` and the retentions), and herdr's socket variables where set. A changed `PATH` or a moved yardr reaches the service by installing again. The daemon reads the yard over the socket once it runs; only the web view's and BDP server's listen addresses are baked in as flags.

### 2.5. The file formats the repository already reads

Packs are YAML: `pack.yaml`, parsed with go.yaml.in/yaml/v3 (internal/pack/pack.go, go.mod), down to `yaml.Node` level for line-numbered errors. Flow files (`.flow`, as `.yardr/flows/default.flow`) are one rule per line (`new -> review on done | decide on question`), parsed by internal/store/flows.go and internal/store/repoflows.go; the grammar holds transitions only, not keyed values. A pack deliberately names no machine thing: no agent product, model, argument, depot, person, secret or identity, which are the yard's and stay in it (docs/packs.md). There is no TOML anywhere in the repository.

### 2.6. Doctor reads the store, or skips what needs it

`yardr doctor` starts no server (docs/doctor.md). On a yard whose server is stopped it reports the machine and the home and marks every store-read check `skipped: the server is not running`, including the `config` notes (one of which today says a setting `is stored and nothing reads it`). Doctor is therefore the natural reader of a file the server has not opened yet, and the natural place to say the file is wrong.

## 3. The proposal

What follows is the smallest version under the file direction. Open question 1 takes file, pack settings, or both; if the direction is the file, this is the version to build.

### 3.1. One file, one rule, each value in one place

The yard's home gains one file, `<home>/config.yaml`. The rule, in one sentence: what a yard's processes read when they start, and what names the yard and its machine, lives in the file; what the operator tunes while beads are in flight stays in the store.

| in the file (9) | stays in the store (7) |
|---|---|
| `name`, `operator`, `yardmaster`, `hook_addr`, `web_addr`, `bdp_addr`, `herdr_session`, `default_kind`, `default_pack` | `operator_style`, `operator_note`, `max_sessions`, `timer_limit`, `timer_min_every`, `blob_max_bytes`, `peer_file_max_bytes` |

The file holds identity (`name`, `operator`, `yardmaster`), every listen address (`hook_addr`, `web_addr`, `bdp_addr`), the machine binding (`herdr_session`, `default_kind`) and the setup default (`default_pack`): copy the file and the shape of the yard comes with it, including the ports a second yard must change. The store keeps the operator's live voice to the next sessions (`operator_style`, `operator_note`, read at every session start), the per-operation caps, and `max_sessions`: the most sessions on this machine at once is the operator's live throttle, read on every daemon pass and holding at once, tuned while beads are in flight, so it stays where a change keeps its `setting_changed` trail with its actor. `herdr_session` is re-read as today (before every herdr call), from the merged read below, so its live behavior does not change; only where the value is kept moves.

The file is YAML, pack.yaml's format, parsed by the same library (go.yaml.in/yaml/v3): no new parser, no new dependency. The `.flow` grammar holds transitions, not keyed values, and TOML would add a parser for nothing YAML does not do. The file is written mode 0600, as `peer.key` is: the home is already 0700, and a value the operator alone changes is not world-readable by accident.

```yaml
# <home>/config.yaml: the yard's basic settings. yardr settings set writes
# these keys here; a hand edit takes the next start of what reads it.
name: yardr
operator: mckean
hook_addr: 127.0.0.1:8790
web_addr: 127.0.0.1:8791
herdr_session: gc
default_kind: claude
```

Absent keys read as today (the table's default, or the machine's for `operator` and `name`); a missing file is a yard that never set any, which is every yard until the move (open question 4).

### 3.2. One merged read, where settings are served

Nothing about the thirty readers changes. `Store.Settings` overlays the file's values for the nine moved keys onto what the store holds, so every call site, the socket's `Settings`, and devices calling over the network keep working byte for byte. The store refuses a moved key on write (`SetSetting` answers `hook_addr lives in <home>/config.yaml now: yardr settings set hook_addr …` writes it there), so a value is never in both: the yardmaster's no is honored structurally, not by convention. Unknown keys in the file are what stored unknown keys are today: kept, unread, and named by doctor (`nothing reads it`).

`yardr settings set` keeps working for every key and writes the file for the nine: the same check, the same sentences, refused for anyone but the operator in `operatorOnly`'s words, checked by the server against the file's `operator` before it writes. The write is atomic (temporary file and rename, as `yardr init` writes a role prompt) and preserves the rest of the file byte for byte, comments included, by editing at `yaml.Node` level as the pack reader does. `yardr settings get` prints the merged values with one new source in FROM: `file`. The seven store keys work exactly as today.

`yardr init` and the onboarding write the moved keys they set (`operator`, `yardmaster`, `default_kind`) to the file, and read them back from the merged read; a yard from before them is finished by the next run as today.

### 3.3. Flag over environment over file, by the existing mechanism

`store.Setting` already carries `Env` and `Effective` already resolves environment over set over default; that becomes environment over file over default for the nine, and every file key gets its `YARDR_<KEY>` (`YARDR_HOOK_ADDR`, `YARDR_WEB_ADDR` and `YARDR_BDP_ADDR` are the landed pattern, joined by `YARDR_HERDR_SESSION` and the rest). The three address variables are already in `ServerSettings` in internal/config/config.go, so sessions still start without them and a server started from a pane does not take the yard's addresses; no further variable joins it (section 3.6). Flags keep winning over all of it wherever a flag names the value (`--addr` of web and bdp serve: flag, else environment, else file, else default).

### 3.4. A bad hand edit, and what doctor says

The check is the `KnownSettings` table's, run wherever the value is read, and it names the file, the key, the line, and the check's sentence. `yardr doctor` reads and checks the file itself, with the server stopped or running, so a yard whose file is wrong is diagnosed by the command that starts nothing. Whether a bad value refuses the start of what reads it or is run past as if unset is open question 3; either way the sentence is the table's and doctor says it.

### 3.5. What is lost against today, and the trail

A file is edited by any process of the account, as the store's file is; `operatorOnly` was already a line against mistakes, not a wall (docs/configuration.md "Settings"). What is genuinely lost for the nine is the event: `setting_changed` with its actor, visible in `yardr watch`, subscriptions and history. Whether the yard notes a file that changed under it is open question 2.

### 3.6. What follows the file and what needs a new install

The daemon's service follows the file: the daemon reads the home's file at every start, so a value change needs no reinstall; only a moved binary, a changed `PATH` or a moved home does, as today. The web view's and the BDP server's services follow it too once they resolve flag, else environment, else file, else default: `install` writes `--addr` into the service file only when the flag was given (a change to `args` in internal/cli/longrunning.go, which today always hands the address on), and a service installed without one takes the file at its next start (change the file, restart the service, no reinstall). The server is started on demand, not by a service, and reads the file at every start.

A web or BDP service installed before that change keeps the address baked into its file until it is installed again: those files pin the resolved address as `--addr` even after the file moves on, exactly as the settings' help already says. The reinstall is one command per service (`yardr web install`, `yardr bdp install` with no `--addr`); `settings set` names it, as `addrTakes` names it today. No migration rewrites service files: the operator's explicit install stays the thing that changes what login starts.

Only the variables in `config.ServerSettings` are captured into a service's file at install (`server.ServerEnv`, by way of `serviceEnv` in internal/cli/service.go): `HOOK_ADDR`, `WEB_ADDR`, `BDP_ADDR` and the retentions, which stay as they are. The new non-address variables (`YARDR_HERDR_SESSION`, `YARDR_NAME`, `YARDR_OPERATOR`, `YARDR_YARDMASTER`, `YARDR_DEFAULT_KIND`, `YARDR_DEFAULT_PACK`) are honored where a process starts but are not baked in: every reader reads the file itself, and the daemon in particular reads `herdr_session` from it before every herdr call, which is why a yard that names its session already needs none of the herdr socket variables in its service. A `YARDR_*` set in a service's file still wins over the file while it is there, as `YARDR_HOOK_ADDR` does today.

## 4. Left out

- A pack `settings:` key. Packs deliberately name no machine thing, and machine values imported from a shared pack break the motive: two yards from one pack would share the ports. Import-time copies would then drift from the pack with no live link, or a live link would put one value in two places, the thing the yardmaster's no was about. Open question 1 confirms this.
- The seven live knobs (`operator_style`, `operator_note`, `max_sessions`, the timer and blob caps). They are tuned mid-flight and keep their event with its actor. Moving them would gain a shorter store table and cost the only trail of who told the sessions what; `max_sessions` in particular is the live throttle on a loaded machine, not a start-up value.
- TOML, and any new parser. YAML is pack.yaml's format and the library is already a dependency.
- `yardr settings edit` (open the file in `$EDITOR`). A hand edit plus doctor's diagnosis covers it; the command can come later if the file grows beyond nine keys.
- `--host` and `--unsafe` as settings, a free port picked by itself, and anything for several yards beyond distinct files. yardr-d4xx leaves the first two out already.
- A last-known-good cache for bad edits. It is a second source for the nine values, against section 3.1; open question 3 picks refuse or run-past instead.
- Watching the file with inotify. If the yard notes changes (open question 2), the daemon stats one small file on the pass it already makes every 30 seconds.

## 5. Open questions

### 5.1. File, pack settings, or both?

- **File only.** One new file, one rule, each value in one place. Costs: a second store beside the store (nine keys, one merge), and the lost event of section 3.5.
- **A pack `settings:` key instead, no file.** Costs more: a new pack key with its import semantics (copy or link? what an apply overwrites?); packs naming machine values against their founding rule; two yards from one pack sharing ports until someone hand-edits; and the values still land somewhere per yard, which is the file under another name.
- **Both: the file, and packs may carry defaults for it.** Costs the most: one value in two places with a precedence between them, import-overwrite prompts, and drift questions, for values whose whole point is that they differ per yard.

Take the file only. A pack carries a way of working, shared across yards; these nine values are what makes one yard not another.

### 5.2. Does the yard note a file that changed under it?

- **Nothing.** The file is source; readers read at start, hand edits take then, no events. Smallest: no code beyond the read. Costs: the trail of section 3.5 stays lost; nobody learns of a change except by diffing.
- **The daemon notes it.** Each pass it hashes the file; on a change it logs an event with the key, the old and new values, and actor `file`, shaped like `setting_changed` but saying honestly that no actor is known. Costs: a hash held in memory, one comparison per pass, and events for changes the operator made on purpose and knows about.

Take the daemon note. It restores what changed, in the shape subscribers already read, for one file hashed on a pass that already runs; only the actor stays unknown, and the event says so.

### 5.3. What does a hand edit with a bad value do at the next start?

- **Refuse the start.** The process that reads the bad value exits before it serves, naming the file, the key, the line and the check's sentence; doctor, which starts nothing, says the same. Costs: one typo can stop server starts, so every command fails until one edit fixes it; the failure is loud and the fix is obvious.
- **Run past it as if unset.** The reader takes the default (or no address) and the yard limps: doctor reports a fault and, where the store is writable, an event is logged. Costs: silent drift with teeth, a mistyped `hook_addr` quietly dropping webhooks, a mistyped `herdr_session` quietly reading the wrong session; and two behaviors (bad vs unset) to hold apart in every reader.

Take refuse. Bad source, no start; the file is small, the sentence names the fix, and doctor diagnoses it without the server. Keep one settled exception: an address that passes the check but cannot be listened on (another yard has the port) degrades exactly as today (`hook_listener_failed`, serving otherwise), since invalid and taken are different failures.

### 5.4. One move at the first start, or none?

- **One move.** The first server to open a store with moved keys set copies each to the file where the file lacks it and removes the store row, logging one event naming what moved; where a hand-written file already names the key differently, the file wins and the event names the dropped store value. Costs: once-only code that runs on every yard exactly once, and must be right first time on the yards that have the most to lose.
- **None.** The file starts empty; moved store rows are ignored (and removed on sight, with an event). Costs: every existing yard re-sets its addresses by hand, and a yard that had `hook_addr` set silently takes no webhooks until someone notices and re-sets it.

Take the one move. Breaking is fine but silent loss of webhooks is not; the move is a handful of keys copied once, with an event saying what happened, against every operator retyping by hand and finding out by absence.

## 6. The beads it would become

In the order they land. yardr-d4xx (web_addr, bdp_addr as settings) and yardr-mkfn (operator-only settings) have both landed; the beads below build on them, and none waits on another bead outside this list. Bead 2 reuses mkfn's `operatorOnly`.

### 6.1. The file and the merged read

Goal: `<home>/config.yaml` exists, is read, and is the source of the nine values everywhere settings are served.

Where to look: internal/config/config.go (`ServerSettings`, the `Config` paths), internal/store/settings.go (the table, `Effective`, `Settings`, `SetSetting`), internal/cli/init.go (`settingRows`, `settings get`), internal/server/server.go (`serveHooks`), internal/runner/herdr_session.go, internal/pack/pack.go (yaml.Node handling).

Done when: a hand-written file drives `hook_addr`, `name`, `herdr_session` and `default_kind` in an isolated yard (`contrib/isolated-yard`); `yardr settings get` shows FROM `file` for the nine; every file key honors its `YARDR_*` variable over the file; bad-value behavior is as decided under open question 3; `go test` of the touched packages passes. Docs ride along: docs/configuration.md "Settings" and "What lives where" say which nine and the rule.

### 6.2. `settings set` writes the file, init writes it, the store lets go

Goal: the nine are never in both; every writer writes the file.

Where to look: bead 6.1's loader; `Store.SetSetting` (refuse moved keys with the file's path); the `settings set` command and `yardr init`/onboarding in internal/cli; the store's open path for the one move (open question 4).

Done when: `settings set hook_addr 127.0.0.1:8790` rewrites only that key, keeps comments, is atomic, and is refused to anyone but the operator in the table's words; `yardr init` on an empty home writes the file, not the rows; an upgraded yard moves its values once with one event (or drops them loudly, as decided); a moved key with a store row left behind is impossible; `go test` of the touched packages passes. Depends on 6.1.

### 6.3. The web view and the BDP server follow the file

Goal: no address is a flag-or-nothing; installed services follow the file unless the flag was given.

Where to look: the `longRunning` address rows d4xx landed (`Setting`, `EnvAddr`, `addrFromSetting`, `args` in internal/cli/longrunning.go), internal/cli/web.go, internal/cli/bdp.go, `serviceEnv` and `webServiceAddr` in internal/cli/service.go; bead 6.1's merged read.

Done when: with `web_addr`/`bdp_addr` in the file, `web start`, `web serve`, `web install` and the bdp verbs listen there with no flag and `status` prints it; `--addr` still wins; a value the flag would refuse is refused with the same sentence; an install without `--addr` writes no `--addr` and follows later file edits after a service restart, while a service installed before this bead keeps its baked `--addr` until it is installed again (`settings set` says so, by way of `addrTakes`); docs/configuration.md "Login services" and docs/web.md say so; `go test` of the touched packages passes. Depends on 6.1.

### 6.4. Doctor, status and the daemon read the file on its own

Goal: the file is diagnosed without the server, shown without the server, and, if decided, noticed when it changes.

Where to look: internal/cli/doctor.go and docs/doctor.md (the `config` notes, the stopped-server block), internal/cli/status.go (the `yard` line's name and operator), the daemon's pass for the hash-and-note (open question 2).

Done when: with the server stopped, doctor names a bad key with file, line and sentence and still exits as it should, and status shows the yard's name and operator from the file; a file changed under a running yard produces the decided note (or provably nothing); `go test` of the touched packages passes. Depends on 6.1 (and 6.2 for nothing: the note compares bytes, not writers).
