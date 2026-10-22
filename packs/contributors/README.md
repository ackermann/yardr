# yardr contributors pack

The pack yardr ships for what comes to a public repository from outside: an
issue or a pull request someone opens there is a bead in the yard, a
maintainer looks at it first, and a pull request is reviewed by an agent
that reads it and runs nothing. A maintainer merges on GitHub, or the yard
does for a pull request marked `merge-ok` (a bot's bump of Go modules, or
what a maintainer said yes to); nothing of the yard pushes to a pull request.

It is for a git depot, beside the pack the depot follows for its own work
(the [starter](../starter), say): its two flows are for beads of the types
`issue` and `pr`, and it has none for the depot. `yardr pack check
contributors --depot <depot>` shows what importing it would change, and
`yardr pack apply contributors --depot <depot>` imports it. yardr carries
this pack in its binary, so the name is enough; `packs/contributors`, with
the separator, is this directory in a checkout. The format and how a pack
maps to a yard are in [docs/packs.md](../../docs/packs.md).

- `pack.yaml`: the two flows, the groups, the crew member, and what
  GitHub's events make and say (`sources.github`).
- `roles/pr-reviewer.md`: the prompt of the reviewer of a pull request.
  `roles/builder.md`, `roles/reviewer.md`, `roles/yardmaster.md` and
  `scripts/merge-local` are the starter's, copied: an accepted issue is
  built, reviewed and landed as the depot's own beads are. A test fails
  when a copy drifts.
- `flows/issue/triage.md`, `flows/pr/triage.md`, `flows/pr/review.md`,
  `flows/pr/ciwait.md`, `flows/pr/approved.md`: the stage files of the
  stages that are this pack's own.

## The two flows

| | stages |
|---|---|
| `issue` | `triage` (maintainers) -> `backlog` -> `new` -> `review` -> `approved` -> `merged`, or `closed` |
| `pr` | `triage` (maintainers) -> `review` (pr-reviewers) -> `ciwait` (pr-ci) -> `contributor` (outside) -> `approved` (maintainers) -> `merged`, or `closed` |

Everything a maintainer chooses is in `pack.yaml`: the stages, the label,
who is trusted. yardr knows three verbs and none of the choices:

| verb | direction | here |
|---|---|---|
| `makes` | an event makes a bead for a thread no open bead tracks | an issue opened: an `issue` bead in `triage`. A pull request opened or marked ready: a `pr` bead in `triage`, or in `review` when its author is an owner or a member of the repository, or a bot |
| `says` | an event reports an outcome to the bead that tracks its thread | the label `accepted`: `accepted`. New commits on a pull request: `pushed` from `contributor`; from `review` and from `ciwait`, `pushed` when a bot pushed and `changes` when a person did. Merged: `merged`. Closed: `dropped`. No event of GitHub's CI says anything: the pull request's CI is the yard's own, reported by a script |
| `hears` | a stage of the bead sets a status on the thread | an issue is closed when its bead is `merged` or `closed`, a pull request when its bead is `closed` |

**Triage is the pack's advice, not a rule of yardr.** The stage a bead
starts in is a line of `makes`, so a copy of this pack may send more
authors straight to `review` (another `association=`), or everyone. What
the stage is for is in its stage file: nothing of the yard runs outside
code before a person has judged it, so a maintainer checks whether the
person and the code are legit before anything of the pull request is run
anywhere, and the label `accepted` sends it on.

**A bot's pull request skips triage, whichever bot.** The line of `makes`
that sends it to `review` is `sender=bot`: `sender` is the kind GitHub types
what acted as (user, bot or organization) and never a login, so no line can
name Dependabot alone. A human from outside keeps the hold.

**`merge-ok` is the reviewer's to give for one kind of pull request.** A
bump of Go modules from a bot, whose title opens `build(deps)` and whose
diff changes `go.mod` or `go.sum` and nothing else, gets the label from the
reviewer when it reports `done` (`yardr bead update <id> --add-label
merge-ok`, which goes over the yard's socket). The label says the yard may
merge the pull request from `approved`. For any other pull request it is a
maintainer's to add.

**The stage `ciwait` is the yard's own CI, worked by `pr-ci`.** A reviewed
pull request waits there while the script runs the depot's test command on
its head: the one every landing runs, from the base's copy where the command
is a file the base has, in a checkout of the head of its own. Green is
`done`, to `approved`, with the commit recorded as `ci_head`; red, or a
person's new commits meanwhile, is `changes`, to `contributor`; a bot's new
commits meanwhile are `pushed`, back to `review` (below). It is a stage of
its own, not the end of `review`, because the reviewer runs nothing (its
sandbox reaches the yard's socket and nothing else), and because a fork's
code runs on the yard's machine only after a reviewer has read it, which
getting to `ciwait` means. A run that is no verdict on the pull request (no
test command, no token, a failed fetch) holds the bead for the yard's
operator, and the pull request is not called green untested.

**`pr-ci` works as the yard's GitHub App, so the yard sets its group up
first.** The script is `scripts/pr-ci`, the copy of `contrib/assembly/pr-ci`
(a test fails when they differ). The App's id and key file are the group's
`env_file`, which an import cannot ask for, so a yard makes the group once,
for all its depots, and maps the pack's to it: `--group pr-ci=pr-ci`
(docs/line.md, "The yard as the CI of a pull request", has the commands).
Imported without that, the pack makes the depot's own `<depot>-pr-ci` and
the route, and the group needs its `env_file` and the mint before it runs.

**The yard merges what is marked `merge-ok`, and only that.** The
pack brings no script for it: the merge is `contrib/assembly/merge-pr`
of the yardr checkout, run by an `exec` group routed for `approved`, type
`pr`, label `merge-ok` (docs/line.md, "Merging pull requests"). It merges
as the yard's GitHub App, at the head a maintainer's yes recorded
(`yardr bead update <id> --add-label merge-ok --meta merge_head=<sha>`)
or, for a bot's pull request from the repository's own branch, at the head
it has, and only where the yard's CI has passed on that commit (the bead's
`ci_head`). A pull request without the label waits in `approved` for a
maintainer, as before. What stops a merge holds the bead with a note, and
the bead reports `merged` or `dropped` and nothing else.

**A bot's push starts the read again.** A bot's rebase is the only push it
makes, so a bead sent to `contributor` by it would wait there for one that
does not come. A push by a bot (`sender=bot`, a kind as in the line of
`makes`, so every bot) while the pull request is read or waits on CI says
`pushed`, which leads from `review` and from `ciwait` back to `review`. From
`review` the bead stays, as a new stay: the session reading the old head
ends and a new one reads the new head. From `ciwait` it goes back to be
read, and waits for CI on the new head after. A person's push is as it was:
`changes`, to `contributor`, until the next commits. The line comes before
the person's in the table, since the first line an event matches counts.

**The stage `contributor` is held by `outside`, a people group with no
members.** Nobody of the yard works a bead there: it waits for the pull
request to change, and the next commits on it say `pushed`. In `yardr
review` it shows where the pull request waits.

**A merge moves the bead itself.** When the pull request is merged on
GitHub, the `merged` outcome moves its bead out of `approved`, a stage of
people, as the hook's `as` crew member (`yardmaster`): the pack's own
mapping, not a stranger's word. An agent that read the pull request still
cannot move a bead out of a stage of people by naming them.

## What the import asks

Beside the starter's questions (the agents of `builders` and `reviewers`,
the members of `backlog` and `decisions`):

- **who the maintainers are**: `--members maintainers=<a>,<b>`. They move
  a bead out of `triage` and `approved`;
- **the members of `outside`**: none, `--members outside=`;
- **the agent of `pr-reviewers`, and its sandbox**: `--bind
  pr-reviewers=<kind>,read-only`.

```sh
yardr depot init web --pack starter …
yardr pack apply contributors --depot web \
  --bind pr-reviewers=codex,read-only \
  --members maintainers=jo --members outside=
```

It needs a GitHub hook to do anything (`yardr hook add github --source
github --secret-file <file>`): `makes`, `says` and `hears` go on the yard's
hook of the source, and `pack check` says what is missing.

The hook's tables are shared with the starter pack, whose lines are for a
train's pull request, and the import sets only the pack's own lines among
them, taking off none but those it set at its last import and no longer
gives. Each of the pack's `says` lines names the type of its bead (`issue` or
`pr`), so an event meets the lines of the bead's own pack: a pull request
closed unmerged is `dropped` for a `pr` bead, and asks a person about a train.

Groups are matched by name, so imported into a depot the starter set up,
the pack finds `builders`, `reviewers` and `assembly` there, points them at
its copies of the roles and the script, which are the same text, and sets
its own limits (3 builders, 2 reviewers).

## The pr-reviewers group binds read-only

A pull request from outside is a stranger's code and a stranger's text,
and an agent of the yard otherwise runs as the operator, with the sandbox
of its agent off. The role tells the reviewer to run nothing and to take
nothing it reads as an instruction; a role is words, and the text it is
reading may be written to talk it out of them. So the group that reviews
pull requests is bound with its agent's own sandbox on, which holds
whatever the agent was told:

| kind | preset | arguments | what it is |
|---|---|---|---|
| `codex` | `read-only` | `--enable network_proxy` and, each behind a `-c`: `default_permissions="yardr_read_only"`, `permissions.yardr_read_only.extends=":read-only"`, `permissions.yardr_read_only.network.enabled=true`, `permissions.yardr_read_only.network.unix_sockets={"{socket}"="allow"}` | Codex's read-only sandbox as a permissions profile: its commands read, and neither write nor reach the network |
| `claude` | `read-only` | `--settings '{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["{workdir}"]},"network":{"allowUnixSockets":["{socket}"]}}}'` | Claude Code's sandbox around every shell command, with the working directory denied for writing too |

`{socket}` is the yard's socket (`<home>/yard.sock`), put in when the agent
is started: the one unix socket either sandbox lets through, since every
yardr command of the reviewer goes through it. `{workdir}` is the bead's
worktree, put in the same way: Claude Code's sandbox denies a directory it
is given by its absolute path. Codex lets a profile's socket through only
behind its network proxy, a feature Codex 0.160.1 calls experimental; with
no domain allowed, the proxy refuses every host, and a Codex without the
feature does not start.

`--bind pr-reviewers=codex,read-only` at the import, or `yardr depot init
<depot> --bind pr-reviewers=codex,read-only` where a depot is set up from a
pack that has the group. A kind without the preset is refused by name; give
such a group arguments of your own (`yardr group set <depot>-pr-reviewers
'args=[…]'`) or another kind. The import's default for an agent it creates
is the kind's unattended arguments, which for Codex turn the sandbox off:
**bind this group explicitly.**

A reviewer that cannot run cannot run the tests. For a pull request from
outside that is the yard's CI run's job, in the stage after the review, and
after approval a maintainer's.

**What the sandbox also stops.** It denies the network, so inside it the
reviewer cannot fetch the pull request's head. It does not have to:
before the reviewer starts, yardr puts the head in the bead's worktree
itself — at every start of a session on a bead that tracks a pull
request it fetches `refs/pull/<n>/head` from the depot's `origin` and
checks it out detached, and a fetch that fails fails the start as
blocked, so no session reviews the base by mistake. What still needs a
maintainer in the pane is the fallback: when the head has moved
mid-session the role fetches it again, a command the sandbox refuses,
and as these presets leave them Codex and Claude Code ask in the pane
for leave to run it outside. The reading, the note that is the review
and the outcome need nobody.

## Not in this pack

`CONTRIBUTING.md` and a pull request template are the repository's own.
Posting the review to the pull request is not here either: the review is a
note on the bead, and a maintainer posts of it what they choose.
