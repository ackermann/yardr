# yardr contributors pack

The pack yardr ships for what comes to a public repository from outside: an
issue or a pull request someone opens there is a bead in the yard, a
maintainer looks at it first, and a pull request is reviewed by an agent
that reads it and runs nothing. A maintainer merges on GitHub; nothing of
the yard pushes to a pull request or merges one.

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
  `flows/pr/approved.md`: the stage files of the stages that are this
  pack's own.

## The two flows

| | stages |
|---|---|
| `issue` | `triage` (maintainers) -> `backlog` -> `new` -> `review` -> `approved` -> `merged`, or `closed` |
| `pr` | `triage` (maintainers) -> `review` (pr-reviewers) -> `contributor` (outside) -> `approved` (maintainers) -> `merged`, or `closed` |

Everything a maintainer chooses is in `pack.yaml`: the stages, the label,
who is trusted. yardr knows three verbs and none of the choices:

| verb | direction | here |
|---|---|---|
| `makes` | an event makes a bead for a thread no open bead tracks | an issue opened: an `issue` bead in `triage`. A pull request opened or marked ready: a `pr` bead in `triage`, or in `review` when its author is an owner or a member of the repository |
| `says` | an event reports an outcome to the bead that tracks its thread | the label `accepted`: `accepted`. New commits on a pull request: `pushed` from `contributor`, `changes` from `review`. Merged: `merged`. Closed: `dropped` |
| `hears` | a stage of the bead sets a status on the thread | an issue is closed when its bead is `merged` or `closed`, a pull request when its bead is `closed` |

**Triage is the pack's advice, not a rule of yardr.** The stage a bead
starts in is a line of `makes`, so a copy of this pack may send more
authors straight to `review` (another `association=`), or everyone. What
the stage is for is in its stage file: nothing of the yard runs outside
code, so a maintainer checks whether the person and the code are legit
before anything of the pull request is run anywhere, and the label
`accepted` sends it on.

**The stage `contributor` is held by `outside`, a people group with no
members.** Nobody of the yard works a bead there: it waits for the pull
request to change, and the next commits on it say `pushed`. In `yardr
review` it shows where the pull request waits.

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
| `claude` | `read-only` | `--settings '{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["."]},"network":{"allowUnixSockets":["{socket}"]}}}'` | Claude Code's sandbox around every shell command, with the working directory denied for writing too |

`{socket}` is the yard's socket (`<home>/yard.sock`), put in when the agent
is started: the one unix socket either sandbox lets through, since every
yardr command of the reviewer goes through it. Codex lets a profile's
socket through only behind its network proxy, a feature Codex 0.160.1
calls experimental; with no domain allowed, the proxy refuses every host.

`--bind pr-reviewers=codex,read-only` at the import, or `yardr depot init
<depot> --bind pr-reviewers=codex,read-only` where a depot is set up from a
pack that has the group. A kind without the preset is refused by name; give
such a group arguments of your own (`yardr group set <depot>-pr-reviewers
'args=[…]'`) or another kind. The import's default for an agent it creates
is the kind's unattended arguments, which for Codex turn the sandbox off:
**bind this group explicitly.**

A reviewer that cannot run cannot run the tests. For a pull request from
outside that is CI's job, under the repository's own rules, and after
approval a maintainer's.

**What the sandbox also stops.** It denies the network, so inside it the
reviewer cannot fetch the pull request's head. What an agent does with a
command its sandbox refuses is the agent's: as these presets leave them,
Codex and Claude Code ask in the pane for leave to run it outside, and a
maintainer answers there; the reading, the note that is the review and the
outcome need nobody. A group that should work with nobody watching needs
the pull request's head put in the bead's worktree before the reviewer
starts, which yardr does not do today.

## Not in this pack

`CONTRIBUTING.md` and a pull request template are the repository's own.
Posting the review to the pull request is not here either: the review is a
note on the bead, and a maintainer posts of it what they choose.
