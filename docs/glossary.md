# Glossary: yardr's words beside Gas Town's and a tracker's

For someone who comes from [Gas Town](https://github.com/gastownhall/gastown)
or from an issue tracker (Linear, GitHub, Jira), and for an agent that
reaches for "epic" or "rig" out of habit. The first eight words are the
yard's own, for what a person meets every day; the rest are plain words kept
on purpose, because they name general machinery.

An empty cell means the other side has no such thing. A word in brackets is
the nearest thing there and not the same thing; the notes under the table
say how it differs.

| yardr | what it is | Gas Town | a tracker |
|---|---|---|---|
| **yard** | one installation of yardr: its store, its crew and its depots | town | workspace |
| **depot** | a git repository or a plain directory that beads are worked in | rig | project, repository |
| **bead** | one durable piece of work | bead | issue, ticket |
| **train** | a bead whose children land on its branch and reach the base as one, after one review of the whole | epic with an integration branch | epic |
| **wagon** | a child of a train on the fast lane: built in parallel, light gate, no review of its own | | sub-issue |
| **crew**, crew member | the always-on agents of the yard, tied to no bead | (crew) | |
| **yardmaster** | the crew member that coordinates: shapes beads, keeps the line moving, answers people | mayor | |
| **assembly** | the starter pack's group that gates an approved bead and lands it on the base | refinery | merge queue (a code host's) |
| flow | the stages a bead of some type goes through, and the moves allowed between them | | workflow |
| stage | one step of a flow (in the starter flow: backlog, new, review, approved, merged, decide) | | status, column |
| route | which group takes the beads of a stage | (sling) | assignment rule |
| group | who works a stage: a pool of agents, a script, people, or another yard | | team, assignee |
| runner | how a group's work starts: `herdr` (an agent), `exec` (a command), `manual` (people), `peer` (another yard) | | |
| session | one agent working one bead in one stage | a polecat's session | |
| outcome | how a session says its stage ended (`done`, `changes`, `question`, `failed`); the flow picks the next stage | | |
| gate | the check a bead passes before it lands; a depot has a light one and a full one | verification gates | required checks, CI |
| hook | a webhook endpoint that turns an outside system's deliveries into events | | webhook |
| source | the outside system a hook receives from: `linear`, `github`, `token` | | integration |
| thread | a conversation in a source that a crew member takes part in: in Linear, an issue | | an issue's comments |
| link | a bead's tie to an outside issue it tracks or relates to | | linked issue |
| peer | another yard this one is linked with | (federation) | |
| pack | a way of working as one directory: flows, groups, roles, crew members | (Gas City: pack) | |
| operator | the person who runs the yard; one of its users | overseer | admin, owner |
| user | a person of the yard: a name and an inbox | | member |

## Where a word does not carry over

- **convoy** is not a train. A Gas Town convoy tracks related beads, across
  rigs, and reports when they are all closed; each bead still lands by
  itself. What lands as one in Gas Town is an epic with an integration
  branch (`gt mq integration create <epic>`), and that is a train. yardr has
  no word for a convoy: `yardr bead watch` and `yardr subscribe` tell you
  when beads are done.
- **hook** means something else in Gas Town: there it is an agent's own work
  queue ("if there is work on your hook, you must run it"). In yardr an
  agent's work reaches it through a route, and a hook is a webhook endpoint.
- **crew** in Gas Town belongs to one rig: long-lived, named agents that a
  person works with hands-on. yardr's crew belongs to the yard, and the
  yardmaster is one of it; Gas Town's mayor and deacon are roles of the
  town, not crew.
- **sling** is an act, `gt sling <bead> <rig>`, once for each bead. A route
  is a standing rule: every bead that enters the stage goes to the group.
- **polecat** has no row. A polecat is a worker with a name that outlives
  its sessions; in yardr a group has no named workers, only sessions.
- **federation** is Gas Town's design for towns that reference each other's
  work, built in part. A peer is one linked yard, and the link carries only
  what each yard's operator allowed.
- **pack** is Gas City's word, not Gas Town's: a directory of agent
  definitions, formulas, prompts and orders. yardr's pack is the same idea
  with yardr's contents.
- **rig** is always a git repository. A depot may be a plain directory.

## Words yardr used before

mayor (now yardmaster), epic (train), part (wagon), resident (crew member),
rig (depot), refinery (assembly). yardr prints only the new words; where an
old one is still read is in
[configuration.md](configuration.md#words-that-changed). In the BDP view a
train is still published as BDP's `epic`.

## Where the Gas Town words were checked

Gas Town at commit `649b832` (29 Sep 2026): `docs/glossary.md` (town, rig,
mayor, refinery, crew, polecat, bead, hook, convoy, sling),
`docs/concepts/convoy.md` (convoy), `docs/concepts/integration-branches.md`
(epic with an integration branch), `README.md` (verification gates,
overseer, crew, session), `docs/design/federation.md` (federation). Gas
City at commit `8d8a6be`: `engdocs/architecture/glossary.md` (pack).
