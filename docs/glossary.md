# Glossary: yardr's words

Every word yardr uses for its own things, each with what it is. The first
twelve are the yard's own, for what a person meets every day; the rest are
plain words kept on purpose, because they name general machinery.

| yardr | what it is |
|---|---|
| **yard** | one installation of yardr: its store, its crew and its depots |
| **depot** | a git repository or a plain directory that beads are worked in |
| **bead** | one durable piece of work (an issue, in a tracker) |
| **train** | a bead whose children land on its branch and reach the base as one, after one review of the whole |
| **wagon** | a child of a train on the fast lane: built in parallel, a quick run of the tests, no review of its own |
| **question** | the type of the bead `yardr bead ask --new` makes for a question that is about no bead: it carries the ask and is nobody's to build, so nothing routes it and it is never reported unrouted; its asker closes it when the answer is on it |
| **crew**, crew member | the always-on agents of the yard, tied to no bead |
| **yardmaster** | the crew member that coordinates: shapes beads, keeps the line moving, answers people |
| **assembly** | the starter pack's group that tests an approved bead and lands it on the base |
| **line** | what a bead travels in a depot: its flow's stages, the groups the routes put at them, and the agents and people bound to those groups; a pack is a line written down, bound to no one, and importing it into a depot makes one |
| **ask** | one question as data on a bead (`yardr bead ask`), answered by picking an option, put aside, or declined, or taken back by whoever asked it (`yardr bead ask --take-back`), with why. A **secret ask** (`--secret <source>`) asks the operator for a declared source's credential: the server writes its answer to a file and keeps the value out of notes. It is typed hidden unless the source marks every asked mint input as not secret, in which case a field that supports the setting shows the value as typed. A **run ask** (`--run "<command>"`) proposes one yardr command of a fixed list that is the operator's to give (a link's mail limit or kinds, where a linked yard's beads go, who a linked yard's crew member is in a source, a source's calls an hour, a grant of a source's calls, a new source a file declares with its declaration on the card, a user added or removed, a crew member's grants, a crew member's identity in a source, a setting, a link renamed, a bead's originator, a timer removed), or a user's own (their subscription to the yard's events, which parts their phone is told of): its card shows the command whole with the yard's own sentence for what it changes, and the approval of the one asked (`yardr bead answer <id> <ask> --approve`) has the yard run exactly that command, once. A **setup ask** (`--setup <pack> --crew <name>`) carries the whole setup of a pack's sources as one decision, the apply, the account and the grants: its card shows the declaration whole with the mint, the account to type unseen, and one option per preset rendered by the yard, and the approval (`yardr bead answer <id> <ask> <n> --secret`) has the yard apply the pack, take the account and grant the preset picked, once |
| **answer** | what one person did with one ask (`yardr bead answer`): the option picked with any words, words alone, put aside, or declined; for a secret ask, the names that were given, never the value; for a run ask the operator approved, the command the yard ran and what it printed; for a setup ask the operator approved, the preset picked and the names given, with the setup read as being carried out, ran or failed |
| **topic** | what questions that belong together stand under: a bead label with the prefix `topic:`; a bead's topic is its first such label |
| triage | the contributors pack's first stage: an issue or a pull request from outside waits there for a maintainer, before anything of it is built or run |
| contributor | someone outside the yard who opened an issue or a pull request; in the contributors pack also the stage a pull request waits in for its author's next commits |
| maintainer | a person of the yard who decides what of a contributor's work the project takes; the contributors pack's group `maintainers` |
| flow | the stages a bead of some type goes through, and the moves allowed between them |
| stage | one step of a flow: a status or column, in a tracker (in the starter flow: backlog, new, review, review-security, approved, merged, decide) |
| route | which group takes the beads of a stage; a route with no depot is a yard default, serving every depot that does not state its own route for the same beads |
| group | who works a stage: a pool of agents, a script, people, or another yard |
| runner | how a group's work starts: `herdr` (an agent), `exec` (a command), `manual` (people), `peer` (another yard) |
| session | one agent working one bead in one stage |
| outcome | how a session says its stage ended (`done`, `changes`, `question`, `failed`); the flow picks the next stage |
| hook | a webhook endpoint that turns an outside system's deliveries into events |
| source | the outside system a hook receives from: `linear`, `github`, `token`. Two are yardr's own and need no hook: `yardr`, this yard's bead events, and `peer`, what a linked yard says of the beads this yard has at it |
| thread | a conversation in a source that a crew member takes part in: in Linear, an issue; in `peer`, a bead of this yard that is at a linked yard |
| link | a bead's tie to an outside issue it tracks or relates to |
| peer | another yard this one is linked with |
| pack | a way of working as one directory: flows, groups, roles, crew members |
| operator | the person who runs the yard; one of its users |
| user | a person of the yard: a name and an inbox |
| device | one program of a user that calls the yard from outside it, let in by a token the yard gave it ([users.md](users.md#devices)) |
| edge | a service that stands in front of yards and hands them the webhooks sent to it; yardr runs one at https://my.yardr.dev. A yard connects to one with `yardr edge connect`, pins its key when it does, and still verifies every webhook itself |
