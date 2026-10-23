# The pack API: what a pack may say, run and hear

A pack is a directory the yard imports ([packs.md](packs.md)). The packs in
yardr's binary change with it. A pack built elsewhere, read from a directory
or a URL, has to keep working when the Go inside yardr changes, and this page
is the promise that lets it. It does for a pack author what
[api.md](api.md) does for the author of a chat window: it lists what is
stable, says that later builds only add to that list, and gives the list a
number, so that a pack can say which list it is written for.

Three words:

- A **seam** is one place where a pack meets the yard: a key it writes, a
  variable its script reads, a command its script runs, an event it hears.
- **Stable** means a seam is part of the contract. A later yardr keeps it, or
  raises the API number.
- **Internal** means a seam is yardr's own and may change with any build. A
  pack that uses one works today and is promised nothing for tomorrow. The
  packs yardr ships use some, because they ship with the build that has
  them. A pack built outside this repository must not.

## What applying a pack runs

A pack that names a program authorizes yardr to run its author's code as the
user who runs the yard. A program is a script that a group runs with `run:`,
or a file that a command of a declaration names. `yardr pack check`, and
`pack apply` before it asks anything, print the plan and end it with one line
that names the programs, after the digest:

```
digest sha256:<hex>

runs 3 programs of the pack as you: acme/normalize, acme/thread, scripts/land
```

The paths are the pack's own, sorted, each once. A pack with no program
prints no such line, and its digest remains the plan's last line.
`pack check --json` carries the same list as
`programs`, an empty array for none.

Applying is the consent. The digest makes sure that the code which runs is
the code that was read. It covers every file the pack names, the programs
among them, and `pack apply --sha256 <digest>` refuses a pack with any other
digest before it asks or writes anything ([Pinning a
pack](packs.md#naming-a-pack)).

Nothing fences that code in. It runs as the OS user of the yard and can read
every file under the yard's home. On the socket, a group's script is served
as a person at a terminal is, and the actor it names is taken at its word. A
script that runs `yardr --actor <the operator> settings set ...` is the
operator to every check but the ones the command line makes itself.
[api.md](api.md#who-may-make-a-call) calls the socket's own checks "a line
against mistakes, not a wall", and for a script they are less than that.
**Planned:** a script is held to its group's name, as a pane is held to its
agent's. A pack must not count on acting as anyone but `$YARDR_ACTOR`.

What a pack asks for, the operator grants, at an ask or with a command. A
pack asks for the calls of a declared source for its groups and crew members
with `calls`, for opening threads with `open_threads`, and for being seen by
linked yards with `expose_to_peers`. It also asks for a source of its own and
suggests presets. It never carries a secret, an identity or a person.

## The API number

`api` is a key at the root of `pack.yaml`:

```yaml
name: mine
api: 1
kind: git
```

It is a whole number, 1 or more, and it is optional. A pack without it is a
pack of API 1, so every pack written before the key needs no edit. A pack
says the lowest number whose list holds everything it uses.

yardr reads every API from 1 to the highest number under [What each number
added](#what-each-number-added). A pack that names a higher number is
refused before any other key is read. An older yardr that reads up to API 1,
given a pack of API 2, says:

```
packs/mine/pack.yaml:2: api: this pack is written for pack API 2, and this yardr reads up to 1: upgrade yardr (yardr upgrade)
```

That is the only mistake it lists. The pack's others are left out on
purpose: they would be the older yardr's mistakes, not the pack's. Before the
key, the same pack failed with "unknown key", which blames the pack for what
yardr lacks.

### When the number rises

The number rises by one, in the release that adds it, on every addition to
what is stable: a key, a value a key takes, a variable, a command or a flag,
an event kind or a field of one. This page lists what each number added. So
an author can say which yardr a pack needs, and a pack that is too new for
the yardr it meets is refused with the cause.

### Taking something away

Within the range yardr reads, a change never removes a stable seam and never
changes what one means. Dropping a seam takes two releases. The first marks
it deprecated on this page, and `pack check` says so for a pack that uses
it. The second stops reading the numbers that had it, and refuses such a
pack with a message like the one above that names the number. yardr's
maintainers decide that each time, and this page records it with a
`BREAKING CHANGE:` line, as a commit does
([line.md](line.md#landing-by-squash)).

### A declaration added by hand

A declaration belongs to the pack that names it and has no `api` key of its
own. One added with `yardr source add <file>` belongs to no pack, so it
carries no API number either. It is the yard's own, and it is read as API 1,
the only API there is so far. Nothing is promised for it beyond what
[integrations.md](integrations.md#a-declared-source) says of a declared
source, and a yard accepts keys in it that a pack may not bring, such as
`limits`. The day a declaration ships alone, outside a pack, it gets the key
and the rule above.

## What is stable

Stable, for a pack to build on:

- the keys of `pack.yaml`, the keys of a declaration, the placeholders, the
  path rules, the digest and the rules for fetching a pack from a URL, as
  [The files](#the-files) gives them;
- the variables an exec group's script is started with, its stdin, and what
  yardr does with its exit ([What an exec group's script
  gets](#what-an-exec-groups-script-gets));
- what a source's command is given and prints, and the environment, the time
  and the size it runs under ([What a source's command
  gets](#what-a-sources-command-gets));
- the commands a script runs as `"$YARDR_BIN"`, with the flags, exit codes and
  JSON fields listed ([The commands a script runs](#the-commands-a-script-runs));
- the tables `says` and `makes`, the bead `makes` writes, and what a crew
  member hears ([What a pack hears](#what-a-pack-hears));
- a declared source, its calls and presets, and how a pack asks for grants
  ([What a pack gives agents](#what-a-pack-gives-agents)).

Not for a pack, whatever it happens to do today:

- every seam marked internal below;
- a command, flag, variable, key or JSON field that this page does not list;
- the words of a message, and the layout of JSON: its indentation, the order
  of its keys and which line a key is on. The scripts that ship read some JSON
  by its layout, because they run where only `sh`, `git` and yardr are
  promised. They are yardr's own, and change with it. Read JSON with a JSON
  parser;
- the socket and BDP as a script's way to the yard. The socket is for the
  clients [api.md](api.md) describes, and it has no public call that moves a
  bead. BDP only reads, and has no authentication.

## The files

| seam | | |
|---|---|---|
| every key of `pack.yaml` that the [table](packs.md#packyaml) lists, `api` among them, and what a pack is [refused for](packs.md#what-a-pack-is-refused-for) | stable | |
| every key of a declaration that [`packs/template/acme.yaml`](../packs/template/acme.yaml) shows, and the keys under them that [integrations.md](integrations.md#a-declared-source) gives. A pack for a depot brings `name`, `about`, `base`, `credential`, `check`, `idempotency`, `calls` and `notify`, and a pack of kind `source` also `hear`, `hook`, `thread`, `link`, `identity` and `events` | stable | `limits` is not one: a pack may not bring it |
| the placeholders `{{operator}}` and `{{<crew member>}}` in a role or stage file | stable | any other `{{...}}` there is refused |
| a role, script, stage file, setup file or declaration is a path relative to the pack's directory, inside it, to a regular file. A command program is a file beside its declaration, inside the pack | stable | |
| the digest: sha256 over each file the pack names, as its path, a NUL, its length, a NUL and its bytes, in path order | stable | |
| a pack at a URL: one GET per file, over https, or plain http to this machine alone. A redirect stays within the host and never goes down to http. A file is at most 1 MiB and takes at most 10 s | stable | |
| the start of a mistake, `file:line: key:`, and its JSON form in `pack check --json` (`file`, `line`, `key`, `error`) | stable | |
| the words after that start | internal | |
| a file the pack does not name | not read | no file is found by convention |

## What an exec group's script gets

A group with `run: scripts/land` runs that script when a bead reaches one of
its stages. yardr starts it as `sh -c`, with the script's installed path
after the pack's `env:` assignments. It gets these variables:

| variable | | value |
|---|---|---|
| `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE` | stable | the bead, the session and the stage the bead is in |
| `YARDR_GROUP`, `YARDR_ACTOR` | stable | the group's name in the yard, which is not the pack's name for it: `web-assembly`, not `assembly`. The script acts as that name |
| `YARDR_HOME` | stable | the yard's directory |
| `YARDR_BIN` | stable | the yardr that runs the daemon. A script runs yardr as `"$YARDR_BIN"`, so that it reaches the build that started it |
| `YARDR_TYPE`, `YARDR_TRAIN` | stable | the bead's type, and the id of the train it is a child of, set and empty for a bead with none |
| `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_DEPOT_KIND` | stable | the bead's depot, its path, and `git` or `dir` |
| `YARDR_BRANCH`, `YARDR_BASE`, `YARDR_WORKTREE` | stable | for a bead of a git depot: its branch, what it lands on, and where the branch is checked out. A dir depot's bead has none of the three |
| `YARDR_TEST` | stable | the depot's test command, unset when the depot has none |
| the pack's `env:` assignments | stable | for a group the import creates. A group the yard already has keeps its own `cmd`, and the assignments in it are the operator's from then on |
| `TRACEPARENT` | stable if present | the W3C trace context of the bead's trace |
| `YARDR_KIND`, `YARDR_MODEL`, `YARDR_GIT_SYSTEM_CLEAN`, `GIT_CONFIG_*`, `OTEL_RESOURCE_ATTRIBUTES` | internal | the yard's commit hooks and its tracing |
| the working directory | internal | a script that needs the worktree runs `cd "$YARDR_WORKTREE"` |
| the rest of the daemon's environment, and the group's `env_file` | internal | the operator's, not the pack's |

Its stdin is `/dev/null`. Its stdout and stderr go to the session's log, and
no timeout applies.

yardr reads neither its exit code nor its output. The script reports on its
bead itself, with `bead done`, `advance`, `hold` or `close`. A script that
exits with the bead still in the stage ends the session as `died`, and after
three such sessions for one stage the bead is held for a person.

## What a source's command gets

The commands a declaration names are `hook.normalize`, `thread.*`,
`events.pr` and `events.ci`, `notify.image`, `calls.<name>.build` and
`credential.mint_command`. Each is a program of the pack. yardr runs one
without a shell, for one job, and stops it after 30 s.

| seam | | |
|---|---|---|
| stdin is one JSON object, and stdout must be exactly one JSON object of at most 64 KB, with exit 0. The shapes are those of [integrations.md](integrations.md#commands-a-file-names) | stable | the yard holds them to goldens in `internal/hooks` |
| the time: 30 s, after which the command and everything it started are killed | stable | |
| the environment: `PATH`, `HOME`, `USER`, `LOGNAME`, the proxy and CA variables that yardr's own process has, which are `HTTPS_PROXY`, `https_proxy`, `HTTP_PROXY`, `http_proxy`, `NO_PROXY`, `no_proxy`, `SSL_CERT_FILE`, `SSL_CERT_DIR` and `NODE_EXTRA_CA_CERTS`, the credential under the name `credential.env` gives, and `YARDR_SOURCE_BASE` | stable | nothing else of the yard's environment reaches a command |
| `hook.normalize` and `credential.mint_command` are given no credential | stable | a mint command makes one from the values on its stdin |
| the working directory is not the pack's directory | stable | a command finds the files it ships beside itself, through its own path, in the copy yardr makes under `<home>/sources/<name>/` |
| which directory it is | internal | |
| a command's stderr | internal | the error of a command that fails ends with it |

## The commands a script runs

A script reaches the yard through the yardr command line, run as
`"$YARDR_BIN"`. These commands, with these flags, are API 1:

```
prime --bead <id> --json
bead show <id> --json
bead create <title> [-d <depot>] [-s <stage>] [-t <type>] [-p <priority>] [-l <label>]
            [-b <body or ->] [--id <handle>] [--meta <k>=<v>] [--blocked-by <id>]
            [--tracks <url>] [--relates <url>] --json
bead update <id> --meta <k>=<v>
bead note <id> <text, or - for stdin>
bead done <id> [--outcome <outcome>] [--note <text, or - for stdin>]
bead advance <id> --to <stage> [--note <text, or - for stdin>]
bead hold <id>
bead close <id> --reason <text>
link list <id> --json
mail <to> <text, or - for stdin>
settings get <key>
call <source> <call> [<param>=<value> ...] --json
```

For these commands the flags above are stable, and so are the exit codes: 0
for success, 1 for a failure, 2 for a command that had to ask and could not,
because there is no yard yet or no terminal, and 3 for something that was not
found. With `--json` a failure prints `{"error": "..."}` on stdout. The JSON
fields are those listed below. Every other field is internal, as in
[api.md](api.md#what-is-stable), and so is what a command prints without
`--json`, `settings get` aside. So is every command not in the list,
including the ones that yardr's own `merge-local` uses for its landing
(`disk keep`, `upgrade`, `source show --json`). A command joins the list by a
rise of the API number.

**A bead** is what `bead show` and `bead create` print, and the `bead` of
`prime`:

| field | type | |
|---|---|---|
| `id` | string | the bead's handle |
| `title` | string | |
| `body` | string, absent when empty | |
| `type` | string | |
| `stage` | string | the stage the bead is in |
| `status` | string | `open` or `closed` |
| `priority` | number | 0 is the highest |
| `depot` | string, absent when it has none | |
| `labels` | array of string | |
| `metadata` | object, absent when empty | what `bead update --meta` writes |

**`prime --bead <id> --json`** also has `next`, where the bead may go from
its stage:

| field | type | |
|---|---|---|
| `next.stage` | string | the bead's stage |
| `next.stages` | array of string | the stages it may be moved to, empty for a bead no flow governs, which may go to any |
| `next.outcomes[].name` | string | an outcome `bead done --outcome` takes, absent when the flow leaves the stage to explicit moves |
| `next.outcomes[].goes` | string | the stage reporting it takes the bead to now, which a limit on rounds can change from the flow's edge |

**`link list <id> --json`** prints an array, one entry for each link:

| field | type | |
|---|---|---|
| `kind` | string | `tracks` or `relates`. A bead of another yard that this one is at, was at or came from is listed too, with the kind `at`, `was at` or `from`, and is no issue |
| `source` | string | the outside source, and `peer` for a bead of another yard |
| `url` | string | the issue in its one form, and for a bead of another yard its handle there |

**`settings get <key>`** prints the value alone and nothing else, and exits
3 for a setting that is not set. With `--json` it prints `key` and `value`.

**`call --json`** prints the service's answer alone, shaped as the call's
declaration says. A script calls a source under the grants the operator gave
its group, and the session in `$YARDR_SESSION` tells the yard who is calling
([integrations.md](integrations.md#a-declared-source)).

## What a pack hears

A pack reacts through tables and beads, not through a script that hears
events. An outside event reaches it through the `makes` and `says` lines of
its `sources`. A pack that must run a program on such an event makes a bead
in a stage that an exec group works. The path is a `makes` line, a bead, a
route and a `run:` script, and the bead is the record of the event and keeps
it from running twice.

| seam | | |
|---|---|---|
| the words of a `where` in `makes` and `says`: `event` for the event's own type, `action`, and the keys that the source's declaration lists under `hook.keys`. `says` also takes `stage` and `type`, the bead's | stable | the keys of a source are its pack's to keep. A pack whose lines read another pack's keys depends on that pack |
| the bead a `makes` line makes: its title is the thread's title, else the thread's URL. Its body is the event's text, a blank line and the event's URL, else the thread's. It has a `tracks` link to the thread | stable | a script reads its event from this bead. A field of the event that the bead does not carry is out of its reach, and carrying one raises the number |
| one bead for a thread: `makes` makes none while an open bead tracks the thread, decided in the same write, and an event with no thread makes nothing | stable | |
| `says`: the outcome a matching line reports on the bead that tracks the event's thread, which its flow routes on | stable | |
| `crew.<name>.hears.stages` and `hears.done`: a bead entering a stage, a bead becoming done | stable | the hearer is a crew member, who is an agent |
| `crew.<name>.hears.events`: `attention`, and the event kinds that [api.md](api.md#the-stream) documents, which are `mail`, `mail_read`, `reaction`, `crew_status`, `crew_login`, `hook_files`, `notifications_read`, `noted`, `closed`, `advanced` and `rounds_reached` | stable | |
| which kinds `attention` holds | internal | it grows, and a new kind in it reaches every crew member that names the class |
| any other event kind in `hears.events` | internal | it is accepted as it is today |
| `crew.<name>.subscribe.<source>`, with the scope `involved`, `mentions` or `all` | stable | |
| how an idle crew member is nudged in its pane | internal | |

Nothing else hears an event. A subscription targets a crew member, a user or
a manual group. A pack declares no timer. A script hears nothing, and the
only one that runs because of an event without a bead reaching a stage is
yardr's own quiet run.

## What a pack gives agents

A pack of kind `source` declares a service. Its `calls` are HTTPS requests
the yard makes with the credential, its commands are programs of the pack,
and its `hook.verify` is data the yard checks. A pack for a depot asks for
calls for its groups and crew members, and a pack of kind `source` suggests
presets. Nothing is granted until the operator grants it, at an ask or with
`yardr source grant`.

| seam | | |
|---|---|---|
| a declared source: `sources.<name>.declaration`, and the keys the declaration may bring in a pack for a depot and in a pack of kind `source` | stable | [The files](#the-files) lists them |
| `hook.verify`: `scheme: hmac-sha256` with the keys integrations.md gives, or `scheme: secret` | stable | data, checked by the yard |
| another signature algorithm, or a public-key signature | not open to a pack | it is Go |
| `calls` of a group or a crew member, `open_threads`, `expose_to_peers` | stable | the pack asks and the operator grants |
| `presets` in a pack of kind `source` | stable | they suggest, and a terminal `pack apply` ignores them |
| a process of its own that keeps running | not offered | |
| state of its own | not offered | a pack keeps what it must remember on beads, with `bead update --meta`, notes and links |

## What each number added

The last heading here is the highest API this yardr reads.

### API 1

The first. It is everything this page marks stable: [the files](#the-files),
[the variables of a script](#what-an-exec-groups-script-gets), [what a
source's command gets](#what-a-sources-command-gets), [the commands a script
runs](#the-commands-a-script-runs), [what a pack hears](#what-a-pack-hears)
and [what it gives agents](#what-a-pack-gives-agents), with `yardr call` among
the commands. It is also the meaning of a pack that has no `api` key. Later
numbers list only what they add.

## Left out

These are not in the API, and each can join it by a rise of the number if a
pack needs it.

- **A script that hears the yard's events.** That would be a subscription
  whose target is a script, or a timer that a pack declares. An outside
  event already reaches a script through `makes`, a bead and an exec group,
  and the bead is a record that a person can read. A yard event already
  reaches an agent. A script fired by an event directly would be a third
  kind of hearer with no bead to dedupe on.
- **A long-lived process of a pack.** yardr would have to supervise it,
  restart it, log it and show it in `doctor`, and a process that never stops
  is where a pack's code would hide from review. A service a pack needs runs
  outside yardr, and the pack talks to it as a source.
- **A store of a pack's own.** Beads, their metadata, notes and links hold
  what a pack must remember, and a person can see them there.
- **Dependencies between packs.** A pack that needs a shipped one names it in
  its setup notes.
- **Signing a pack, payment and licences.** The files a pack installs are
  plain files in the yard's home, readable by whoever runs the yard. A
  licence is an agreement between people, and yardr does not enforce it.
- **A wall around a pack's scripts.** Another OS user or a container would be
  one. It is the only real fence, and it needs a design of its own.
