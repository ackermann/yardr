# The pack API: what a pack may say, run and hear

Design yardr-hkjt, proposed 2026-10-23. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean, 23 Oct 2026 (mails 71567, 71628): get yardr to a solid foundation,
then extend it with detached packs, some of them licensed; "a pack could call
yardr commands and listen to events and provide sources to agents." That
needs clear APIs, and this design names them: the **pack API**, the contract
between a yard and a pack. It covers what a pack may say in its files, which
commands and which environment its scripts may rely on, and which of yardr's
events it may hear. It is versioned, tested and written down, so that a pack
built outside this repository keeps working when the Go inside changes. Two
more parts come with it. The first is what "solid foundation" means (mail
71654): "something that can be relied upon, predictable, deterministic. yardr
to some degree is the shaper for non determinism". The second is permissions
and auth (mail 71630): "the only thing where it might get tricky and I would
love a separate thing is permissions / auth".

Words used throughout:

- A **detached pack** is a pack that is not in yardr's repository or binary.
  It is read from a directory or a URL (`yardr pack apply ./mine`,
  `yardr pack apply https://example.com/packs/mine/`).
- A **seam** is one place where a pack meets the yard: a key it writes, a
  variable its script reads, a command its script runs, an event it hears.
- **Stable** means the seam is part of the contract. A later yardr keeps it
  or raises the contract's version. **Internal** means the seam is yardr's
  own and may change with any build. A pack that uses an internal seam works
  today and is not promised tomorrow.

## 2. What exists today

### 2.1 What a pack says: its files

`internal/pack/pack.go` reads `pack.yaml` by hand from a `yaml.Node`, and every
fixed-key mapping goes through `reader.entries` (pack.go:497). That refuses an
unknown key with "unknown key (known here: …)" (pack.go:514), a duplicate, an
alias and a key YAML reads as a boolean. Every mistake is reported as
`file:line: key: why`. The root keys are `name`, `kind`, `setup`, `flows`,
`groups`, `crew`, `sources` and `presets` (pack.go:606). The full tree with
its meaning is the table in [docs/packs.md](../packs.md#packyaml):

- `flows.<flow>.type` and `flows.<flow>.stages.<stage>.{group, file,
  outcomes, manual, end}`, where an outcome may be `{to, max, then}`.
- `groups.<group>.{agent | run | people, limit, binds_as, env, calls}`.
- `crew.<name>.{role, hears.{events, stages, done}, subscribe, open_threads,
  expose_to_peers, calls}`.
- In a depot's pack, `sources.<source>.{as, hears, says, makes}`. In any
  pack, `sources.<name>.declaration`, a file that declares a service.
- In a pack of kind `source`, `presets.<preset>.{about, grants}`.

No other file of a pack is found by convention. A role, script, stage file,
setup note, declaration and the programs a declaration names are read only
because `pack.yaml` or the declaration names them (`Pack.named`,
internal/pack/digest.go:54). A role or stage file may carry the placeholders
`{{operator}}` and `{{<crew member>}}`, and any other `{{…}}` is refused
(pack.go:331-406).

A declaration is read by `internal/source` with the same strict reader
(declaration.go:476-490). Its keys are `name`, `about`, `base`, `credential`,
`check`, `idempotency`, `calls`, `notify`, and in a pack of kind `source` also
`hear`, `hook`, `thread`, `link`, `identity` and `events`. `limits` is refused
in any pack (pack.go:897-913). `packs/template/acme.yaml` names every key a
declaration in a pack may bring.

**There is no version field.** Neither `pack.yaml` nor a declaration has
`version`, `api`, `schema` or `min_yardr`. Because unknown keys are refused, an
older yardr given a pack that uses a newer key says "unknown key". It does not
say that yardr is too old.

**The digest and URL packs.** `yardr pack check` ends with `digest sha256:<hex>`
over every named file (digest.go:80). `pack apply --sha256` refuses any other
digest before it asks or writes anything. A pack at a URL is fetched with one
plain GET per file: https only (plain http to loopback alone), redirects only
within the host, 10 s and 1 MiB per file (internal/pack/url.go). No header or
credential is sent. The only way to carry a token is inside the URL, which the
code knows (url.go:47, "may carry a token"). The import records where it read
the pack as `Source` (internal/store/packs.go:35), which `pack list` shows and
a `pack` event carries (packs.go:214), so a token in the URL would be kept in
the store and shown.

**Tests over whole packs.** `packs/packs_test.go` loads every embedded pack
(`TestEmbeddedPacksAreValid`) and has a test per shipped pack.
`internal/pack/template_test.go` copies `packs/template`, renames it, and
applies it twice into a throwaway store. `internal/cli/pack_test.go` runs
`pack check` and `pack apply` on the starter. Each of these tests a pack that
yardr ships, and changes with it. No test holds the *keys* still: when a key
is renamed, the packs and their tests change in the same commit, and nothing
fails.

### 2.2 What a pack's scripts run with

A pack has two kinds of program:

- **An exec group's script.** A group with `run: scripts/<x>` becomes a group
  with runner `exec` whose `cmd` is the installed path, preceded by the pack's
  `env:` assignments (internal/pack/plan_groups.go:218-342).
- **A declared source's commands**: `hook.normalize`, `thread.*`,
  `events.pr` and `events.ci`, `notify.image`, `calls.<name>.build` and
  `credential.mint_command`.

**An exec group's script** is started by `runner.Exec.Start`
(internal/runner/exec.go:83-154) as `sh -c <cmd>`, when the dispatcher routes
a bead to its group's stage. It gets the following:

| what | value | where |
|---|---|---|
| bead, session, stage, group | `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP` | runner.go:58-71 |
| who it acts as | `YARDR_ACTOR`, the group's name | runner.go:68 |
| the yard | `YARDR_HOME`, `YARDR_BIN` (the yardr binary that runs the daemon) | exec.go:259-301 |
| the bead | `YARDR_TYPE`, `YARDR_TRAIN` (empty for none) | exec.go |
| the depot, git | `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_DEPOT_KIND=git`, `YARDR_BRANCH`, `YARDR_BASE`, `YARDR_WORKTREE`, and `YARDR_TEST` when the depot has a test command | exec.go |
| the depot, plain directory | `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_DEPOT_KIND=dir` | exec.go |
| yardr's own | `YARDR_KIND=exec`, `YARDR_MODEL`, `GIT_CONFIG_*` (the yard's commit hooks), `YARDR_GIT_SYSTEM_CLEAN`, `TRACEPARENT`, `OTEL_RESOURCE_ATTRIBUTES` | exec.go, commithook.go:515-537, internal/otel/context.go:104 |
| everything else | the daemon's own environment, minus the server settings and git config (`withheld`, exec.go:237-256), plus the group's `env_file` | exec.go:188-256 |

Its working directory is the group's `dir`, else the depot's path. It is not
the worktree, which the script reaches through `YARDR_WORKTREE`. Its stdin is
`/dev/null`, and stdout and stderr go to the session's log. No timeout applies
unless the group's config sets one. **Neither its exit code nor its output is
read** (exec.go:25-27). The script must report on its bead itself with `yardr
bead done`, `advance`, `hold` or `close`. A script that exits with the bead
still in its stage ends the session as `died`, and after three such sessions
the bead is held (internal/dispatch/dispatch.go:144-146, 451). The table above
is `docs/groups.md` "exec" in prose.

**A source's command** is run by `source.Run` (internal/source/command.go:35-60)
without a shell. Its stdin is one JSON object, and its stdout must be exactly
one JSON object of at most 64 KB with exit 0, within `source.Timeout`, 30 s
(call.go:24). Its working directory is the yard's home. Its environment is an
allow-list (`CommandEnvOf`, command.go:101-111): `PATH`, `HOME`, `USER`,
`LOGNAME`, nine proxy and CA variables (egress.go:34), the credential under
the name `credential.env` gives it, and `YARDR_SOURCE_BASE`. `hook.normalize`
gets no credential at all. The shapes are documented in
[docs/integrations.md](../integrations.md#commands-a-file-names) and held to
goldens in `internal/hooks` (`TestGitHubPackNormalize` and its siblings).

**Which yardr commands the shipped scripts call.** Every script starts with
`Y=${YARDR_BIN:-yardr}`. The five byte-identical copies of `merge-local`
(starter, design, website, contributors, contrib/assembly) call `prime --bead
<id> --json`, `bead advance --to <stage> --note -`, `bead done --outcome
<o> --note -`, `bead note`, `bead hold`, `bead show <id> --json`, `link list
<id> --json`, `bead close --reason`, `settings get <key>`, `mail`, `disk keep`
and `upgrade`. `packs/website/scripts/deploy-void` and
`packs/contributors/scripts/pr-ci` use the same few, plus `bead update --meta
<k>=<v>` and `source show github --json`. No source command calls yardr. No
script of any pack or contrib talks to the socket or to BDP directly.

**The CLI's output.** `--json` is a global flag (internal/cli/root.go:171), and
with it an error is printed as `{"error": "…"}`. Exit codes are 0, 1, 2 (no
yard, or no terminal where one is needed) and 3 (not found)
(root.go:211-223). No document promises that any command's output stays as
it is.

**The socket.** [docs/api.md](../api.md) is the one stability promise yardr
makes today ("What is stable", api.md:5215). It covers 26 methods in `rpc.Calls`
(internal/rpc/api.go), four operator calls, the stream `GET /events?after=<seq>`
with twelve documented event kinds, and blobs. Later builds may add arguments,
fields and kinds of event, and a client ignores what it does not know.
`TestAPIDoc` (internal/rpc/api_test.go) makes every documented call through a
socket and fails when the page and the server differ. These calls are the ones
a chat client and a decisions client need. There is **no public call that
moves, closes, holds or creates a bead**: those methods take yardr's internal
array form and are refused when called with named arguments. BDP
(`yardr bdp serve`, [docs/bdp.md](../bdp.md)) serves beads, dependencies and
links over HTTP. It answers GET and HEAD only (internal/bdp/server.go:117),
with no authentication and on loopback unless `--unsafe` is given.

### 2.3 What a pack hears

There are four ways an event reaches something a pack declared:

1. **A crew member's `hears`.** `hears.events` becomes the crew member's `--on`
   kinds. The starter names `attention`, which is the class of kinds that need
   someone (`AttentionKinds`, internal/store/crew.go:1078). `hears.stages`
   becomes one subscription `yardr all depot=<d> stage=<s>` per stage, and
   `hears.done` becomes `yardr all depot=<d> state=done`. Whatever matches
   reaches the inbox, and an idle crew member is nudged in its pane
   (internal/dispatch/crew.go:740-785). The hearer is an agent.
2. **A crew member's `subscribe.<source>`**, which is a source's events with
   scope `involved`, `mentions` or `all` and no filter. The hearer is an agent.
3. **A source's `says` table.** Each line has a `where` over the envelope's
   `type`, `action`, `event` and `fields.*`, plus the bead's `stage` and
   `type`. A matching line reports an outcome on the bead that tracks the
   event's thread, and the flow routes the bead on (`rules`,
   internal/dispatch/rules.go:152-302).
4. **A source's `makes` table.** A `where` over the same envelope picks a
   line, and the line makes a bead of `type:stage[@depot]` that tracks the
   event's thread (internal/dispatch/makes.go:188-352). It deduplicates by
   thread: `Untracked` makes the bead only if no open bead tracks the thread's
   URL, in the same write (internal/store/beads.go:126-133, links.go:549).
   **An event with no thread makes nothing** (makes.go:189).

Nothing else hears an event. A subscription targets a crew member, a user or a
manual group. A group with any other runner is refused
(internal/cli/subscriptions.go:808, docs/subscriptions.md:266). A timer is set
by a crew member or a user with `yardr timer add`, and its firing is mail or an
inbox entry ([docs/timers.md](../timers.md)). A pack declares no timer, and an
exec script's actor, a group, may not set one. The only script that runs
because of an event without a bead reaching a stage is yardr's own quiet run,
which is wired in Go (internal/dispatch/quiet.go:22-88).

**Which event kinds are documented.** Every kind is written through
`(*wtx).event` (internal/store/store.go:416). The complete list of kinds and
the fields each carries is `eventDataTold` (internal/store/eventtold.go:39-270),
about 190 kinds, and `TestEveryKindIsDecided` (internal/rpc/deviceevents_test.go:36)
forces a row for each new kind. That list is internal. The documented and
tested ones are the twelve of api.md's stream. `docs/subscriptions.md:142`
lists bead kinds for `--where kind=` and ends in "…".

### 2.4 What a pack can give agents: sources

A pack of kind `source` declares a service. Its `calls` are HTTPS requests the
core makes with the credential, its commands are programs of the pack, and its
`hook.verify` is data the core checks. A depot's pack asks for calls for its
groups and crew (`calls`), and a source pack suggests `presets`. Nothing is
granted until the operator grants it, at a card or with `yardr source grant`.

What a pack **cannot** declare today:

- **A verify scheme other than the two in Go.** `hook.verify.scheme` is
  `hmac-sha256` or `secret` (internal/source/hook.go:31-50). Since yardr-ek33,
  `hmac-sha256` takes a template over `{delivery}`, `{timestamp}` and `{body}`,
  hex or base64, a list of signatures, and a prefixed, encoded key. That covers
  GitHub, Linear, Slack and Standard Webhooks as data. `secret` compares a
  header the file names with the secret in constant time, which covers a
  shared token such as GitLab's. Still Go: any other algorithm (HMAC-SHA1,
  HMAC-SHA512) and any public-key signature (Ed25519, as Discord signs). This
  is [docs/integrations.md](../integrations.md#gaps) "Gaps" item 3 and the
  bead yardr-zeqk.
- **A process of its own that keeps running.** Every command of a pack is run
  per job and stopped after 30 s. An exec script runs per bead.
- **State of its own.** A pack has nowhere to keep a value between runs except
  what yardr already keeps: a bead's metadata (`bead update --meta`), its
  notes and links, and a source's hook table.

### 2.5 The last ten integration features, and what each needed in Go

The ten newest commits in `git log` that added or extended an integration
with an outside service:

| commit | bead | what | Go a pack could not say |
|---|---|---|---|
| 82fbcfc6 | yardr-ek33 | Standard Webhooks signatures | yes: new verify vocabulary (template `{delivery}`, base64, list, key) |
| 783761c2 | yardr-fm93 | merge-pr merges a PR marked merge-ok | no: one line registering a contrib script for upgrade |
| 0572bb87 | yardr-7jyj | Pushover tells with a picture | no: pack YAML and a script, once 1m93 existed |
| 7acbcaed | yardr-1m93 | the yard sends images | yes: new declaration keys (`notify.image`), `reply --file`, the dispatcher attaching a picture |
| 987e5997 | yardr-knvf | a device learns a delivery's files | yes: a new socket call and event kind (`hook_files`) |
| 5f5705c0 | yardr-efjm | a captured delivery cannot be replayed | yes: receiver rules and dedupe keys the store keeps |
| 10def4e8 | yardr-mnc8 | a Telegram file reaches the yard | yes: an envelope field, `thread.file`, a fetch with the credential, blob storage |
| 5a11f351 | yardr-qqnv | webhook pack sends to a URL that is the credential | sending yes: a credential mode and redaction (hearing was YAML alone) |
| ce7621c8 | yardr-xwgw | PostHog recordings and replay links | no: read calls and normalize |
| bb27cb18 | yardr-zz07 | the PostHog pack | no: the one `//go:embed` line every shipped pack needs |

Four of the ten needed no Go. The six that did needed one of three things:
new **vocabulary in the core's data** (verify knobs, declaration keys, a
credential mode), a **new kind of thing the store keeps** (dedupe keys, blobs,
an event kind and its call), or a **new CLI flag**. None needed a long-lived
process, a pack's own key-value store, or a script that hears an event.

### 2.6 Permissions and auth, as one table

Two layers check every socket call. The first is the method's **class**
(`rpc.Access`, internal/rpc/access.go:126, enforced at server.go:95). The
second is the store's own checks of the **actor** inside the write. The
caller's kind comes from the peer's pid and its process tree
(`store.CallerOf`, internal/store/caller.go:289). The actor comes from
`--actor`, else `$YARDR_ACTOR`, else `$USER` (internal/cli/root.go:241). It is
taken at its word except in a pane, where `paneAuthor` (graph.go:838) puts in
the pane's agent or crew member.

| subject | verb | object | checked at |
|---|---|---|---|
| any process that opens the socket (mode 0600) | a call of its method's class | the store API | rpc/access.go:72-99 |
| a bead's agent (its pane) | read; work on its own bead: note, ask, mail, create, update, a move its flow allows, a hold, an edge, a source call its grants allow | beads | access.go Agent rows; store `beadAgent` checks |
| a crew member (its pane) | all but operator and daemon calls | the yard | access.go:72 |
| **an exec group's script** | **as a person's terminal**: every class but daemon | the yard | access.go:79-83 (`CallerScript` -> `CallerPerson`) |
| a person's terminal, any non-pane process | every class but daemon; operator calls when its actor is an operator | the yard | access.go; `operatorOnly` users.go:281,349 |
| a user with role `operator` | users, roles, devices, peers, hooks, depot paths, settings, crew grants and identities, timers of others, source credentials | yard setup | users.go:281-349 and the call sites it lists |
| the operator, from no pane | approve a run ask, a pack apply, a setup ask; give a secret | asks | runask.go:1485, runask_pack.go:101, runask_setup.go:317, secretask.go:685 |
| the operator, from no pane | grant a source's calls to a group or crew member, or a preset | grants | sources.go:130, sourcecalls.go:252 |
| user X alone | grant `--as X` (calls with X's credential) | grants | sourcecalls.go:256-300 |
| a session, by its group's grant; a crew member; the operator | `yardr call` | a source call | sourcecalls.go:689-796 |
| a device, by its token | the `Calls` list as its own user; the operator's four on loopback or https | the listener's `/user/` | rpc/user.go:793-1147 |
| a browser with a view cookie or a gateway token | answer, move, give a secret as its user | web | web/viewtoken.go:104, web/jwt.go:158 |
| a linked yard, by its key | messages of the kinds its link carries | `/peers/` | yards/receiver.go:191, peerbeads.go:533 |
| a hook's sender | a delivery, verified by secret or signature | `/hooks/<name>` | hooks/receiver.go:150-170, hooks/declared.go:148 |
| anyone who reaches BDP | read every bead | BDP | loopback and the Host header (bdp/listener.go, bdp/server.go:112) |

[docs/api.md](../api.md#who-may-make-a-call) and
[docs/users.md](../users.md) say this plainly: "a line against mistakes, not a
wall". Every agent and script runs as the yard's one OS user and could read
the home's files without asking the socket.

## 3. The proposal

The pack API is written down as one page, `docs/pack-api.md`. Its model is the
one docs/api.md already has: a list of what is stable, a rule that later builds
only add, and a test that fails when the code and the page differ. No new
mechanism is proposed for the three verbs. Each verb is carried by what
exists, and three small gaps are filled with data. What is new is the
following:

- **a version number** in `pack.yaml`;
- **the page**, with stable and internal marked on every seam;
- **a frozen fixture pack** that uses every stable seam, checked and applied
  in the test suite;
- **a credential for a URL pack**, which is what a licensed pack needs.

### 3.1 The version: `api:` in pack.yaml

```yaml
name: ci-red
api: 1
kind: git
```

- `api` is a whole number, optional, and **1 when absent**, so every pack that
  exists today is a pack of API 1 without being edited.
- yardr knows the highest API it reads (`pack.API`, a constant beside `File`
  in internal/pack/pack.go). A pack whose `api` is higher is refused before
  any other key is read: `packs/mine/pack.yaml:2: api: this pack is written
  for pack API 2, and this yardr reads up to 1: upgrade yardr (yardr
  upgrade)`. The pack's other mistakes are not listed, because they would be
  the old yardr's mistakes.
- **The rule for raising it.** Each addition to the stable surface raises `api`
  by one in the release that adds it: a key, a value a key takes, a variable,
  a command or flag, an event kind or field. The page lists what each number
  added. A pack declares the lowest number whose surface it uses. So an older
  yardr names the cause ("upgrade") where today it says "unknown key".
- **Removal.** yardr reads every API from 1 to its own, and a change never
  removes a stable seam or changes its meaning within that range. Dropping one
  takes two releases. The first marks it deprecated on the page and makes
  `pack check` say so for a pack that uses it. The second stops reading the
  API numbers that had it, and refuses such a pack with the same kind of
  message as above, naming the number. The decision to do this is mckean's
  each time. The page records it with a `BREAKING CHANGE:` line, as the
  commit convention already does (docs/line.md:575).
- A declaration is part of the pack that names it and carries no number of its
  own. A declaration added with `yardr source add` outside any pack is read as
  API 1 until it says otherwise. That is an open question (5, Q2).

### 3.2 The contract, seam by seam

The page `docs/pack-api.md` lists each seam below with stable or internal. The
lists here are the proposal for API 1.

**The files (stable).** Stable are every key of `pack.yaml` in docs/packs.md's
table, with the refusals of "What a pack is refused for"; every key of a
declaration that `packs/template/acme.yaml` names; the placeholders
`{{operator}}` and `{{<crew member>}}`; the path rules (relative, inside the
pack, regular files); the digest's definition; and the URL fetch rules. The
words of an error are **internal**, but its `file:line: key:` prefix is
stable. `limits` in a declaration stays outside the contract, because a pack
may not bring it today.

**What an exec group's script gets (stable).** Stable are `YARDR_BEAD`,
`YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_ACTOR`, `YARDR_HOME`,
`YARDR_BIN`, `YARDR_TYPE`, `YARDR_TRAIN`, `YARDR_DEPOT`, `YARDR_DEPOT_PATH`,
`YARDR_DEPOT_KIND`, and for a git depot `YARDR_BRANCH`, `YARDR_BASE`,
`YARDR_WORKTREE` and `YARDR_TEST`; the pack's `env:` assignments; stdin
`/dev/null`; and the rule that the exit code is not read and the script must
move its bead. The `TRACEPARENT` header is stable as "may be present", because
it is the W3C standard's. **Internal:** `YARDR_KIND`, `YARDR_MODEL`,
`GIT_CONFIG_*`, `YARDR_GIT_SYSTEM_CLEAN`, `OTEL_RESOURCE_ATTRIBUTES`, the
working directory (a script that needs the worktree `cd`s to
`$YARDR_WORKTREE`), and whatever else the daemon's environment carries.

**What a source's command gets (stable).** Stable are each command's stdin
and stdout shape as docs/integrations.md gives them, the environment's
allow-list, the 30 s and 64 KB bounds, and a working directory that is not
the pack's (a command finds its own files beside itself, as the copies in
`<home>/sources/<name>/` are). These are already held to goldens. The page
points to them and does not repeat them.

**The commands a script may run: `$YARDR_BIN` (stable).** The answer to the
first verb is **the yardr CLI, run as `$YARDR_BIN`**. The socket and BDP are
not the answer, for three reasons:

- every script that ships already uses the CLI, and none uses the socket;
- the socket has no public call that moves a bead, and making one would mean a
  second write API beside the CLI to keep stable;
- BDP is read-only and has no authentication.

The stable commands of API 1 are the ones the shipped scripts and an importer
use:

```
prime --bead <id> --json
bead show <id> --json
bead create <title> [-d -s -t -p -l --id --meta --blocked-by --tracks --relates -b] --json
bead update <id> --meta <k>=<v>
bead note <id> <text | ->
bead done <id> [--outcome <o>] [--note <text | ->]
bead advance <id> --to <stage> [--note <text | ->]
bead hold <id>
bead close <id> --reason <text>
link list <id> --json
mail <to> <text>
settings get <key>
call <source> <call> [<param>=<value>…] --json
```

For these commands, stable are the flags shown, the exit codes 0, 1, 2 and 3,
the `{"error": …}` form under `--json`, and the JSON fields the page lists for
each. Fields not on the page are internal, as in api.md. Prose output is
internal. Every other command is internal, including the ones the starter's
own `merge-local` uses for yardr's own landing (`disk keep`, `upgrade`,
`source show --json`). A command joins the list by an `api` raise.

**What a pack hears (stable).** For the second verb, the rule is that **a pack
reacts through tables and beads, not through a script that hears events**:

- **An outside event** reaches a pack through its `makes` and `says` lines. A
  pack that must run a program on such an event makes a bead in a stage that
  an exec group works: `makes` → bead → route → `run:`. That is the
  event-driven exec group, built from what exists. The bead is its record and
  its dedupe, and a person can see it. So the bead `makes` writes is stable
  too, because the script reads its event from it: its title is the
  thread's title, its body is the event's text, a blank line and the
  event's URL (the thread's where the event has none), and it carries a
  `tracks` link to the thread (madeBead, makes.go:337-353). A field of the
  event that the bead does not carry is not reachable from the script, and
  carrying one is an API raise.
- **The yard's own events** reach a pack's crew members through
  `hears.events`, `hears.stages` and `hears.done`. In API 1, `hears.events`
  may name `attention` and the twelve kinds of api.md's stream. Any other
  kind is accepted as it is today, but it is internal.
- **The envelope a `where` matches** is stable. That covers its `type`,
  `action`, `event`, `stage` and bead `type`, and the `fields` a declaration's
  `hook.keys` names. Keys named by a pack's own declaration are that pack's
  contract to keep. A `says` line over another pack's source (the
  contributors pack over `github`'s `association`) depends on that pack's
  keys. Section 4 says why packs do not declare that dependency.

No subscription whose target is a script is proposed, and no timer a pack
declares. Section 4 says why.

**What a pack gives agents (stable).** For the third verb, a pack declares a
source, its calls and presets, and asks for grants. The operator grants them,
as today. Of the three things the bead names:

- **Verify schemes** join the contract as **data**: an `algorithm` beside
  `scheme: hmac-sha256` (sha256, sha1, sha512), so that a service that signs
  with another hash is a pack. That is yardr-zeqk, re-scoped (gap G1 in
  section 6). Ed25519 is Q3.
- **A long-lived process** is not taken in.
- **State of its own** is not taken in either. A pack keeps what it must
  remember on beads (`bead update --meta`, notes, links), which the CLI
  already offers and which a person can read.

### 3.3 The three asks, answered

| ask | today | after | answer |
|---|---|---|---|
| yardr-zeqk, verify schemes in YAML | GitHub, Linear, Slack, Standard Webhooks and a shared-token header are already data (ek33); another hash or a public key is Go | G1: one `algorithm` key, written once in Go, data from then on | **a pack after G1**; Ed25519 is Q3 |
| yardr-bss1, ci-red | `makes` dedupes by thread, but a `workflow_run` with no pull request (a run on `main`) has **no thread** in the github pack's normalize, so it makes **nothing**. The bead's note that "each run is a new thread" does not hold: there is no thread at all | G2: the github pack's normalize gives a run that belongs to no pull request the thread `<repo>@<branch>` with a stable URL for the branch's runs, and a thread that is no issue is skipped by the stage map and by `reply` | **a pack after G2**: one `makes` line, `where: "event=workflow_run conclusion=failure head_branch=main"` → `bead: { type: task, stage: new }`, with no Go of its own; while the bead is open a second failed run finds it (and a `says` line may note it). That bead carries the run's link and its words (workflow, conclusion, branch), **not the failing job's log tail** yardr-bss1 also asks for: see the paragraph under this table and Q7 |
| yardr-ba5p, import Beads over BDP | not a pack: a pack has no command a person runs once. A program outside yardr can already do it through the stable CLI: read the BDP JSON, `yardr bead create --id <bd id> --meta bdp_source=<url>`, `--blocked-by`, `-l`, `-p`, `-s`, and on a re-run `yardr bead show <id>` finds what exists | none | **Go or a contrib program, not a pack**. A `yardr bdp import` is the bead's own choice. The contract only promises that a program outside yardr could do it |

**The log tail of yardr-bss1.** No seam of API 1 reaches it. `makes` puts
the event's text and URL in the new bead's body (internal/dispatch/makes.go:
337-353), and the github normalize records no job log
(packs/github/github/normalize:78-100); it runs with no credential either
(packs/github/github.yaml:20). A stage script could ask through `yardr call`,
but a call takes JSON from the declared base and follows no redirect
(internal/source/call.go:87-100, 234-270), and GitHub answers a job's log with
a redirect to plain text on another host. An exec script is handed no
source's credential: it inherits the daemon's environment
(internal/runner/exec.go:188), which holds a token only where the operator
put one, so a pack cannot count on `curl` with its source's token either. Three ways
are open, and Q7 asks which:

- the tail is left out of ci-red, and the builder who takes the bead reads
  the log from the run's link;
- the ci-red pack's first stage is an exec script that runs `gh run view
  <run id> --log-failed`, a program of the operator's machine and outside the
  contract (`pack check` names it, 3.6), and notes the tail on the bead. The
  script finds the run's id through the stable CLI alone: `madeBead` ends
  the bead's body with the event's URL (makes.go:341-344), which for a
  `workflow_run` is the run's own page,
  `https://github.com/<owner>/<repo>/actions/runs/<run id>` (the run's
  `html_url`, normalize:86), so `yardr bead show <id> --json` and the URL's
  last segment give it. No new event field and no new seam;
- Go: a call whose answer is text, following one redirect without the
  credential (gap G4), so that `yardr call github job_log job=<id>` is API.

Of the last ten features (2.5), the contract would have let a pack say only
the four it already could. The other six were core vocabulary or storage. The
pack API makes that boundary explicit: it does not move it.

### 3.4 The fixture pack and its test

The fixture is two packs under `internal/pack/testdata/api1/`, because a pack
of kind `source` may not carry flows:

- **`api1-line`** (kind `git`) has two flows (one with `type:`), each stage key
  including an outcome limit, an agent group with `binds_as` and `calls`, an
  exec group with `env:` and a script, a people group, a crew member with
  every key, and `sources` with `as`, `hears`, `says` and `makes`.
- **`api1-source`** (kind `source`) has a declaration with every key
  `packs/template/acme.yaml` names, a stub for each command, and two presets.

The test, in `internal/pack` (step 5 beside `madeBead` in
`internal/dispatch`, where makes runs), does six things:

1. It checks both packs and compares the plan as JSON with a golden.
2. It applies both into a `storetest` store, applies them again and sees
   nothing change.
3. It runs the exec script through `runner.Exec` against a stand-in bead, and
   the script writes `env` and the result of each stable command to a file the
   test compares with a golden. This runs the built binary, which is where the
   CLI half of the contract lives.
4. It runs each source command stub in its shape.
5. It feeds one envelope through the fixture's `makes` line and compares
   the bead made (title, body, link) with a golden, so the shape a script
   reads its event from is held.
6. It pins the fixture's own digest.

The fixture is **frozen**. Its digest is a constant in the test, so an edit to
make a red test pass shows as a changed constant in review. A new API number
adds `api2/` beside it and leaves `api1/` as it is.

A change to yardr that breaks a stable seam then fails this test in the gate,
not at a customer. The golden is regenerated with `YARDR_UPDATE_GOLDEN=1` as
api.md's is. A changed golden in a diff is the reviewer's sign that the
contract moved.

### 3.5 A licensed pack

What it takes beyond the contract: **a URL pack fetched with a credential, and
nothing else**. Two things break the expectation that this needs nothing in
yardr:

- **No credential can be sent.** The fetch sets no header (internal/pack/url.go).
- **A token in the URL would be kept.** It goes into the import's record
  (`Source`), `pack list` and the `pack` event. A later `pack apply <name>`
  reads the pack from that URL again.

The smallest fix is gap G3, which adds one flag to `pack check` and `pack
apply`:

```sh
yardr pack apply https://packs.example.com/ci-red/ --auth ci-red
```

`--auth <name>` names a file, `~/.yardr/packs/<name>.auth` (mode 0600). It
holds a header line such as `Authorization: Bearer …`, and is put there once
with a secret ask or by hand. Every GET of the pack sends that header, and
only to the pack's own host: the redirect rule already keeps the fetch there.
The record keeps the URL and the name, never the value. A later `pack apply
ci-red` finds the name in the record and sends the header again.

Everything else of licensing stays outside yardr: who gets a token, payment,
signing (left out by the bead). The files a licensed pack installs are plain
files in the yard's home, readable by whoever runs the yard. A licence is a
contract between people, not something yardr enforces.

### 3.6 Permissions: what a pack touches, and whether a separate component is warranted

**What a pack may ask for at apply, and who grants it.** The table in 2.6
already holds the answer. A pack asks, and the operator alone grants:

- calls of a source for its groups and crew (`calls`);
- opening threads (`open_threads`);
- exposing a crew member to peers (`expose_to_peers`);
- a new declared source (the card shows the declaration whole);
- a preset at a setup ask.

A pack never carries a secret, an identity or a person.

**What a pack gets without asking.** The pack API has to say this plainly.
Applying a pack that has a program (a `run:` script or any source command)
means running its author's code as the yard's OS user. On the socket, an exec
script is served as a person's terminal (access.go:79-83) and its actor is
taken at its word. So a script that runs `yardr --actor <the operator>
settings set …` is the operator to every check but the CLI-side ones. Even
if that were closed, the script can read every file under the home. The
consent is the apply. The digest (`--sha256`) makes sure the code that runs
is the code that was read. The pack API page states this in its first
section, and `pack check` ends its plan with one line: `runs 3 programs of
the pack as you: scripts/merge-local, ci-red/normalize, ci-red/thread`.

**A separate auth component is not warranted now.** The pack contract can be
stated without one: what a pack asks for is the five grants above, already
checked where they are given, and what it gets is the yard's own trust in
code it was told to run. A component holding identity (tokens, OAuth, peer
keys) and policy (the table in 2.6) would earn its place when a yard serves
more than one party's trust from one process. That is hosted with several
tenants (yardr-hosted-4dx0's successors), not a detached pack. What stays at
each command in any case is the check itself, beside the code it guards, as
today. So this design files **no** auth design. It leaves Q4, the one cheap
tightening, to mckean.

### 3.7 The solid foundation: a checklist

These properties must hold before the contract is published, that is, before
the first pack ships outside yardr. Each has its check. Each goes to decide
with this design, and an item mckean strikes is struck.

| # | property | how it is checked | status today |
|---|---|---|---|
| F1 | The pack API holds | the fixture pack's test (3.4) is green in the gate | not built (bead B2) |
| F2 | The yard is healthy | `yardr doctor --json` has `faults: []` and exits 0 on this yard, and on a fresh `contrib/isolated-yard` yard after `pack apply` of the fixture | doctor exists (internal/cli/doctor.go) |
| F3 | The store only goes forward | `checkSchema` refuses a newer schema (internal/store/migrate.go:212), there are no down migrations, and `Replacement` never self-execs an older build. Missing: a test that opens a store a released build wrote. Proposed check: a store written by the last tagged release (v0.9.0) under `internal/store/testdata`, opened and migrated by the test | partly (bead B6) |
| F4 | Two parts. **(a) Where a bead may go is fixed**: the same bead, with the same flow and routes, matches the same route, and so the same ordered chain of groups it may be given, every pass. **(b) Which group and when is bounded, not fixed**: it also reads the yard's availability at that pass (the list below), so one pass may leave the bead waiting and the next claim it, and two yards in different states may give it different groups of the chain. The bound: never a group outside the chain, and every wait is either recorded as an event or ends at a time the yard set itself | (a) route matching is pure and ordered, with ties refused (routing.go:1217, 1395, 1560); `Route.Choose` with a fixed quota callback is a pure function of its route and those answers (routing.go:782, `TestRouteChoose` in routequota_test.go:256). (b) the `claimed` event names the group taken and each group passed with its reason; the waits that log an event today are `quota_wait`, `capped`, `backoff` and `unrouted` (quota.go:72-86, dispatch.go:1287, 1325, 1142) | (a) holds; (b) bounded, but three waits log nothing (gap B7) |
| F5 | The socket API holds | `TestAPIDoc` | holds |
| F6 | A flaky test is bounded at landing | one retry per package, never for a race; a log line per flake; a bead at the second sighting; a failed step at three in seven days (.yardr/test, docs/design/2026-10-19-yardr-bds8-gate-flakes.md) | holds |

What is non-deterministic in F4, and where the bound is. F4 (a) reads none
of these; F4 (b) reads all of them, and the property over them is the bound,
not the same path:

- **The yard's availability at the pass.** Before and after it asks the
  limits command, dispatch reads state that changes as sessions start and
  end: each group's running sessions against its `limit`, a group whose
  runtime is marked down until a time, a group held back for the bead's
  model after a session stalled or hit a usage limit (quota.go:44-58,
  dispatch.go:1302-1316), the yard's `max_sessions` against the sessions
  running (dispatch.go:1317-1326), the bead's own retry time after a failed
  start and its backoff after being sent back (dispatch.go:1281, 1112-1142).
  Bound: each of these only delays the bead, never sends it outside its
  chain. A slot frees when a session ends, which is a committed write and
  wakes the daemon; a backoff, a retry or a down mark ends at a time the
  yard set, and the daemon's safety-net pass (`Interval`, 30 s by default,
  dispatch.go:142, 684) sees it at most that long after. So no wait outlasts
  its cause by more than one interval. `capped` and `backoff` log an event. The gap:
  a bead that waits because its group is at its limit, its runtime is down,
  or it is held for its model logs nothing, so a person cannot tell those
  waits apart from a stuck yard. Bead B7.
- **Which group a route picks under quota.** `Route.Choose` (routing.go:782)
  walks the `--else` chain by the limits command's answer, cached for five
  minutes, and by the clock. Bound: the chain is the operator's, a group
  outside it is never picked, and the `claimed` event records `route`,
  `passed` and why (routing.go:2066-2160). A bead that waits logs
  `quota_wait`, with the time the first window resets where the answer
  names one.
- **An agent's output.** Bound: the outcomes its stage lists (a different one
  is refused), the round limit `{max, then}` that sends a bead to a person,
  and three failed sessions holding the bead (`MaxAttempts`, dispatch.go:451).
- **A flaky test at landing.** Bound: F6.
- **Time.** Timers, backoffs and fold readiness (`fold_after`) depend on the
  clock by design. Each fires once and is logged.

## 4. Left out

- **A script that hears the yard's events**, as a subscription target or a
  pack-declared timer. The three asks and the ten features need neither. An
  outside event already reaches a script through `makes` → bead → exec stage,
  with a record a person can read. A yard event already reaches an agent. A
  script fired directly by an event would be a third kind of hearer, with no
  bead to dedupe on and nothing for a person to see. It can be added when a
  pack needs it, as an API raise.
- **A `serve` command or any long-lived process of a pack.** Nothing asked for
  one. yardr would have to supervise it, restart it, log it and show it in
  doctor, and a process that runs all the time is also where a licensed pack's
  code would hide from review. A service a pack needs runs outside yardr, and
  the pack talks to it as a source.
- **A key-value store per pack.** Bead metadata, notes and links hold what the
  asks need, and a person can see them.
- **The socket or BDP as the scripts' API.** See 3.2. The socket stays the API
  for clients (chat, decisions) under docs/api.md. A BDP write profile is the
  BDP specification's to define.
- **Dependencies between packs** (`requires: github`). The contributors pack
  already depends on the github pack's keys without saying so, and its
  refusals happen at plan time against the yard's source, which is enough
  while both ship together. A detached pack that depends on a shipped one
  names it in its setup notes. A key can come with a later API number.
- **Pack signing and payment**, and the demo yard: left out by the bead.
- **Sandboxing a pack's scripts** (another OS user, a container). That is the
  only real wall, and it is a design of its own, wanted only once packs come
  from people the operator does not trust.
- **A CHANGELOG.** The pack API page's list of what each number added is the
  changelog of the contract. yardr's own changes stay in git.

## 5. Open questions

**Q1. When `api` rises.**

- (a) On every addition to the stable surface. An old yardr then says
  "upgrade" for any pack it cannot read. The cost is many small numbers, one
  per release that adds a seam.
- (b) Only on a removal or a change of meaning. Additions keep the number, and
  an old yardr says "unknown key" for a new one, as today. The cost is that a
  pack author cannot say which yardr they need.
- (c) A yardr version instead of a number (`requires: 0.10`). It is precise,
  but it ties the contract to yardr's release numbering, which the bead says
  is not the contract.

I would take **(a)**.

**Q2. Whether a declaration outside a pack (`yardr source add <file>`) carries
`api` of its own.**

- (a) No: it is read as API 1, and as the yard's own when added by hand.
- (b) Yes: the same key and the same rule.

The cost of (b) is a second place to keep in step. Every declaration that
ships is in a pack. I would take **(a)** until a declaration ships alone.

**Q3. Ed25519 (public-key) signatures in verify.**

- (a) In G1: `scheme: ed25519` with the key as a public key in the file, since
  it is no secret. This is new code for one service known today (Discord) and
  a new idea, a key that is public data.
- (b) Out until a second service needs it.

I would take **(b)**.

**Q4. Whether an exec script's actor is pinned to its group.** Today a script
can name any actor, the operator included (3.6).

- (a) Pin it, as a pane's is (`paneAuthor` for `CallerScript`). This is a few
  lines and closes the easy way for a pack's script to act as the operator.
  It is no wall, since the script can read the home. Cost: `merge-local`'s
  `yardr upgrade` step must still work. It needs checking whether any
  operator-only call is on that path; if one is, that step needs the
  operator's word in another form.
- (b) Leave it, and say in the pack API that a script is trusted as the
  operator.

I would take **(a)**, because it keeps the class table true of scripts as it is
of panes, and say (b)'s sentence anyway.

**Q5. Whether `call` is in API 1.** `yardr call` is how an agent uses a source.
A script may too, under its group's grants. Putting it in API 1 makes its
`--json` answer stable. Leaving it out makes the first verb "yardr commands
about beads". I would take **in**: providing sources to agents is the third
verb, and a pack's own script uses the same grants.

**Q6. Where the fixture lives.** It could be in `internal/pack/testdata/api1/`
(a test fixture, not shipped) or in `packs/api1/` (beside the shipped packs,
visible to authors, not in the `//go:embed` line). I would take
**`internal/pack/testdata`**. Authors start from `packs/template` and the page,
and a fixture beside the shipped packs would look like one.

**Q7. The failing job's log tail in ci-red (yardr-bss1).** yardr-bss1's goal
asks for the run's link and the failing job's log tail; API 1 reaches the
link and not the tail (3.3).

- (a) Leave the tail out of ci-red. Its bead carries the run's link and
  words, and the builder who takes it reads the log there (`gh run view
  --log-failed` is in a builder's reach). Cost: nothing; yardr-bss1's goal
  loses half a sentence.
- (b) The ci-red pack's first stage is an exec script that runs `gh run view
  <run id> --log-failed | tail`, notes it on the bead with `yardr bead note`,
  and moves on, noting "no log tail: gh is not logged in" where it cannot.
  Cost: the pack depends on a program outside the contract and on the
  operator's own GitHub login, which `pack check` names (3.6), and on the
  run's URL being the last line of the bead's body, which 3.2 makes stable
  and step 5 of the fixture test (3.4) holds; the script's test needs a
  stand-in `gh`.
- (c) Gap G4 in Go: a call whose `answer` is `text`, bounded, following one
  redirect without the credential, so `yardr call github job_log` is API
  (after a JSON call that lists the run's failed jobs, since GitHub's log
  is per job). Cost: the only place a call's credential and its answer's
  host part ways, in the code that refuses redirects on purpose
  (internal/source/call.go:234-245), and a second answer kind for every
  caller of `Do`. Dropping the credential is not enough on its own: the
  service's answer would choose where yardr's server sends its next
  request, so the redirect must be bounded too. The boundary: the target is
  `https` only (no downgrade, and no plain-http exception, whatever the
  base is); its host matches one of the call's declared `follow:` patterns
  (`*.actions.githubusercontent.com`, `*.blob.core.windows.net` for
  GitHub's logs), so the pack's reviewed text names every host yardr may
  be sent to; and the address the name resolves to is refused at dial time
  when it is loopback, private, link-local, unspecified or multicast, the
  inverse of `loopbackOnly` (call.go:294) and held at the same place for
  the same reason, since a name can be made to resolve anywhere. Behind an
  egress proxy (`ProxyFromEnvironment`, call.go:283) the dial sees the
  proxy, so the target's name is resolved and checked before the request
  as well; the proxy may still resolve it again, which is the proxy's
  bound, not yardr's, and the page says so. One hop,
  no second redirect, no cookies, and no header of the first request but
  `Accept`.

I would take **(a)** for API 1; (b) stays a pack-only change later, since
the run's id is already in the bead's body: the bead is for a builder, who reads the log better
than a 500-rune tail does, and (c) loosens a rule written to keep a
credential on its host.

## 6. The beads it would become

In the order they would land:

**B1. Write the pack API page and read `api:` in pack.yaml.** Goal:
`docs/pack-api.md` lists every seam of 3.2 as stable or internal, with API 1's
list and the rule of 3.1. `pack.yaml` takes `api: <n>`, 1 when absent, and a
pack above `pack.API` is refused with the upgrade message before any other key
is read. `pack check` ends its plan with the line naming the programs the pack
runs (3.6). Where to look: internal/pack/pack.go:606 (root keys), `File`;
docs/packs.md's key table; docs/api.md "What is stable" as the model; the
programs list from `Pack.named` (digest.go:54). Done when: a pack with `api: 2`
is refused with the message; a pack without the key plans as before
(byte-identical plans for every embedded pack); the page exists and
docs/packs.md links it; `pack check --json` carries the programs.

**B2. Add the frozen fixture packs and their contract test.** Goal: 3.4. Where
to look: internal/pack/template_test.go (copy, apply twice), packs/template,
internal/runner/exec.go (`Exec.Start`), internal/dispatch/makes.go
(`madeBead`), internal/rpc/api_test.go (`YARDR_UPDATE_GOLDEN`). Done when: the
test checks, applies twice, runs the script and the stubs, makes a bead from
one envelope, and compares with goldens; a mutation that renames one stable
key, drops one stable variable or one stable `--json` field, or moves the
URL out of a made bead's body, fails it;
the fixture's digest is pinned. Depends on B1 (and on the answers to Q5 and
Q6).

**B3. Hold the stable CLI commands' `--json` fields to the page.** Goal: the
JSON fields the page lists for each stable command come from a test, as
api.md's tables do, so the page cannot drift. Where to look:
internal/rpc/api_test.go `TestAPIDoc` (the pattern), internal/cli
`bead_move.go`, `prime`. Done when: the page's field tables between markers are
written by the test, and a field removed from a command's output fails it.
Depends on B1. B2 and B3 may land in either order.

**B4 (G1). Add `algorithm` to hmac verify.** This is yardr-zeqk, re-scoped to
one key. Goal: `hook.verify.algorithm: sha256 | sha1 | sha512`, sha256 when
absent, so a service that signs with another hash is a pack. Where to look:
internal/source/hook.go:31-50, internal/hooks/declared.go `Verify`,
docs/integrations.md Verify and Gaps item 3. Done when: every existing golden
is byte-identical; a vector for sha1 and one for sha512 pass and a wrong key
fails; Gaps item 3 says what is still Go (public-key schemes). It adds a key,
so it is API 2 under Q1 (a). Depends on B1.

**B5 (G2). Give a CI run on a branch with no pull request a thread, then ship
ci-red.** Goal: the github pack's normalize gives a `workflow_run` or
`check_suite` that belongs to no pull request the thread `<repo>@<branch>`,
with a URL that is the same for every run of the branch. The stage map,
`reply` and `thread state` skip a thread that is no issue or pull request.
yardr-bss1 is then one `makes` line in a pack, with no Go of its own. Where to
look: packs/github/github/normalize:78-100, internal/dispatch/makes.go:188-352,
internal/hooks goldens (`TestGitHubPackNormalize`), the github pack's thread
script for what it does with an unknown URL. Done when: a stand-in failed run
on `main` makes one bead, a second makes none while it is open, and one after
its close makes a new one; the existing goldens for runs that belong to a pull
request are unchanged; the stage map moves nothing for the branch thread;
the new bead's body ends with the run's own URL, not the branch's (the
event's URL comes before the thread's in `madeBead`), which Q7 (b) reads
the run's id from. B5 adds no event field. The log tail is
not B5's: it is yardr-bss1's under Q7 (b), or B10 under Q7 (c), and under Q7
(a) yardr-bss1's goal drops it. yardr-bss1 depends on B5. B5 itself depends
on nothing.

**B6. Open a released build's store in a test.** Goal: F3's check. Where to
look: internal/store/migrate.go `openMigrations`, migrate_internal_test.go.
Done when: a store written by v0.9.0 (a small fixture with a bead, a flow, a
user and a hook) opens, migrates to `schemaVersion`, and reads back. A note
says how to refresh the fixture at each release. Depends on nothing.

**B7. Log why a bead waits for a group at its limit.** Goal: F4's gap. A bead
whose group is at its limit, backing off or held for its model logs one event
per stage and reason, as `quota_wait` does. Where to look:
internal/dispatch/dispatch.go:1303-1316, quota.go:72-86 (`quota_wait`),
internal/store/eventtold.go (a row for the kind). Done when: a dispatch test
with a group at its limit sees one event, and none again on the next pass.
Depends on nothing.

**B8 (G3). Fetch a URL pack with a credential.** Goal: 3.5's `--auth <name>`.
Where to look: internal/pack/url.go (`URLFS`, `packRedirect`),
internal/store/packs.go (the record), internal/cli/pack.go (flags). Done when:
a stand-in server that answers 401 without the header serves the pack with
it; the record, `pack list` and the `pack` event carry the name and never the
value; a redirect to another host is still refused, and the header is not sent
there. Depends on B1 only for the page's line.

**B9 (Q4, if taken). Pin an exec script's actor to its group.** Where to look:
internal/store/graph.go:838 (`paneAuthor`), caller.go:259 (`CallerScript`),
`merge-local`'s upgrade step. Done when: a script that names another actor is
served as its group; the starter's landing and upgrade still pass in an
isolated yard. Depends on nothing.

**B10 (G4, only if Q7 (c) is taken). Let a call answer text after one
redirect.** Goal: `answer: { kind: text, max: <bytes> }` on a call, which
follows one redirect to another host without the credential and returns the
text, bounded, so the github pack can declare `job_log`, within Q7 (c)'s
redirect boundary (`https`, a declared `follow:` host, no non-public
address, one hop). Where to look: internal/source/call.go:87-100, 234-290
(`client`, `loopbackOnly`), internal/source/declaration.go (the call's
keys), packs/github/github.yaml `calls`. Done when: a stand-in that
redirects to a second stand-in serves the text, and the second stand-in sees
no credential header; a JSON call that is redirected is still refused; an
answer over `max` is refused, not cut; and each of these redirects is
refused with its reason and no request reaches its target: to `http`, to a
host no `follow:` pattern matches, to a name that resolves to 127.0.0.1,
10.0.0.1, 169.254.169.254 or ::1, and a second hop. The tests reach the
dial check through a resolver the test supplies, since the stand-ins
themselves listen on loopback: the address check is a function the test
swaps, as `loopbackOnly` is a separate function today. Depends on B1 (it
adds a key).

B1 comes first. B2 and B3 publish the contract, and with F1 to F6 checked it
can be announced. B4, B5 and B8 are the gaps, and each is independent of the
others; B10 is a gap only if Q7 (c) is taken.
