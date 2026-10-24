# yardr

Your coding agents' changes, reviewed, tested and merged.
Why it exists: [I made fire: the rise of the 0x human](https://yardr.dev/why).

yardr carries each change through the review and tests your depot configures. The tests run on exactly what will merge, on your machine, with your logins.

Route beads of work to whoever works them. A **bead** is a durable unit of
work. **Routes** send open, unblocked beads in a **stage** to a **group**; a
group's **runner** starts the work (an agent in herdr, a shell command, or
nobody: a human). Whoever works the bead advances it to the next stage or
closes it, and the next route takes over. A **depot** is where a bead is
worked: a git repository, or a plain directory. Roles are not built in:
"builder" or "reviewer" is just a group plus a prompt file.

## Principles

A design or a change to yardr is judged by three things.

- **Generalization.** Few concepts, each carrying many cases.
- **Composability.** The concepts combine without knowing each other.
- **Simplicity.** One store, one writer, events in order; the smaller
  mechanism is chosen and the special case is left out until use asks for it.
  Nothing polls the store: the dispatcher wakes on a committed write,
  re-reads state and acts idempotently, so a restart is just another pass.

## Install

A release archive needs no Go (darwin and linux, arm64 and amd64):

```sh
tag=v1.2.3; target=darwin_arm64  # current tag: version in https://yardr.dev/releases/latest.json, or the invite reply; target or darwin_amd64, linux_amd64, linux_arm64
curl -fSL -O https://yardr.dev/releases/$tag/checksums.txt -O https://yardr.dev/releases/$tag/yardr_${tag#v}_$target.tar.gz
shasum -a 256 --ignore-missing -c checksums.txt   # must say OK; Linux: sha256sum --ignore-missing -c checksums.txt
tar -xzf yardr_${tag#v}_$target.tar.gz yardr && mv yardr ~/.local/bin/  # any directory on PATH
```

From a checkout, with Go: `go install .`

Either way yardr needs git, herdr and an agent harness (claude, codex, …) on PATH;
`yardr doctor` says which are missing. [docs/install.md](docs/install.md) has the rest:
macOS and a downloaded binary, herdr's server, a server without a desktop, upgrading.

## Core concepts

- **Bead**: one durable piece of work, with a type, a stage, notes and
  dependencies. Not a branch or an agent's session: those come and go while
  the bead is worked.
- **Stage and flow**: a stage is one step of a bead's life (`new`, `review`);
  a flow is the stages and the moves allowed between them. A flow does not
  say who works a stage.
- **Route**: which group takes the beads of a stage. A rule, not an
  assignment of one bead.
- **Group and runner**: a group is who works a stage, and its runner is how
  the work starts: `herdr` (an agent), `exec` (a command), `manual` (people)
  or `peer` (another yard). Not a role: a role is a group plus a prompt file.
- **Depot**: a git repository or a plain directory that beads are worked in.
  Not a project kept in the store: the code stays in the repository, and
  the store holds where it is and what tests it.
- **Pack**: a way of working as one directory (flows, groups, roles, crew
  members) that a depot is set up from. Not a plugin: it is files a yard
  imports.
- **Crew**: the always-on agents of the yard, tied to no bead; the yardmaster
  is the one that coordinates. Not the agents that work beads: those are
  sessions a group starts for one stage.
- **User**: a person of the yard, a name and an inbox; the operator is the
  one who runs it. Not an account: there is no password, and who acts is
  `$YARDR_ACTOR`, else the login name.
- **Peer**: another yard this one is linked with by its key; signed messages
  and mail cross the link. Not a shared store: each yard keeps its own.
- **Source**: a service outside the yard (Linear, GitHub, Pushover) that
  hooks receive from and calls go to. Not built in one by one: a new service
  is declared by one file.

[docs/glossary.md](docs/glossary.md) has all of yardr's words, each with what it is.

## Quick start

```sh
yardr                                   # the onboarding: answer the form; it starts the yard and the yardmaster
                                        # then talk to the yardmaster (yardr chat) or go on with forms (yardr setup)

# what either sets up, by hand, for a repository in ~/projects/webapp:
yardr depot add webapp ~/projects/webapp --base main
yardr depot init webapp                 # flow, groups, roles and routes from the starter pack, and a guess at what tests it
yardr depot show webapp                 # test: the command builders, reviewers and the assembly run; none is set when the repository does not say

# one bead down the line: backlog, new, review (then review-security, if its diff needs it), clear (your yes to land it), approved, merged
id=$(yardr bead create "Add login page" --depot webapp -b "Use the OAuth flow") # it starts in backlog
yardr bead advance "$id" --to new       # a builder takes it from here, then a reviewer, then the assembly lands it
yardr watch                             # every event as it happens
yardr session list --running            # what agents and scripts work on now
yardr bead show "$id"                   # the bead, its notes and where it stands
yardr doctor                            # at any time: what the yard has and what it lacks
```

[docs/setup.md](docs/setup.md) has the setup as forms and as a script, and
[docs/line.md](docs/line.md) how a bead moves and how one is worked.

## Commands

Every command takes `--json`, and `yardr <command> --help` says the rest.

| for | commands | what they are for |
|---|---|---|
| the work | `bead`, `dep`, `ready`, `session`, `flow`, `route`, `group`, `depot`, `pack` | beads and the edges between them, what is ready and what is being worked; the stages a bead goes through, who works each, where, and a way of working as a pack |
| people and crew | `crew`, `user`, `mail`, `inbox`, `chat`, `react`, `timer`, `review`, `prime`, `remember`, `recall` | the always-on agents and the people of the yard: mail between them, what waits on you, what an agent reads when it starts, what the yard remembers |
| outside | `hook`, `source`, `call`, `reply`, `thread`, `link`, `subscribe`, `unsubscribe`, `limits`, `peer`, `edge` | services outside the yard and other yards: what comes in, what goes out, the issues beads belong to, who is told what |
| the yard | `init`, `setup`, `doctor`, `status`, `up`, `down`, `server`, `daemon`, `store`, `settings`, `events`, `watch`, `web`, `bdp`, `otel`, `harness`, `version` | creating a yard and setting it up, what it has and what runs, its processes, its store and settings, its events, and the views of it |

## Where the rest is

[docs/README.md](docs/README.md) lists every page and the code's layout.

## Acknowledgements

- **Required.** yardr needs [herdr](https://herdr.dev): it starts every
  agent through it, and reads its agents' state and panes from it.
- **Implemented.** [BDP](https://github.com/gastownhall/bdp), the Bead
  Protocol: `yardr bdp serve` serves the yard over its Read profile
  ([docs/bdp.md](docs/bdp.md)).
- **Inspiration.** [Beads](https://github.com/steveyegge/beads), where the
  bead comes from, and [Gas Town](https://github.com/steveyegge/gastown), by
  Steve Yegge, and [Gas City](https://github.com/gastownhall/gascity): the
  town of agents with a mayor and roles. yardr owes them its ideas and its
  vocabulary.

Thank you to everyone who built them.

## Contributing

The repository takes no outside contributions yet. When it does, every
outside contribution must carry a `Signed-off-by` line with the author's name
and email address (`git commit -s`): it certifies the Developer Certificate of
Origin, <https://developercertificate.org>, that the author wrote the change,
or may submit it, under the project's licence. A yardr agent session instead
writes the `Yardr-Bead` and `Yardr-Agent` trailers described in
[groups.md](docs/groups.md#runners).

## Licence

Source-available under the Functional Source License, FSL-1.1-ALv2
([LICENSE](LICENSE)): use, change and run it for anything except a competing
product or service; each release becomes Apache 2.0 two years after it was
made available.

The licence covers the code. It does not cover the name yardr or the ram,
which are the project's marks (the FSL's Trademarks clause): a fork takes a
name and a mark of its own.
