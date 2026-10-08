# Glossary: yardr's words

Every word yardr uses for its own things, each with what it is. The first
eight are the yard's own, for what a person meets every day; the rest are
plain words kept on purpose, because they name general machinery.

| yardr | what it is |
|---|---|
| **yard** | one installation of yardr: its store, its crew and its depots |
| **depot** | a git repository or a plain directory that beads are worked in |
| **bead** | one durable piece of work (an issue, in a tracker) |
| **train** | a bead whose children land on its branch and reach the base as one, after one review of the whole (an epic, in most trackers) |
| **wagon** | a child of a train on the fast lane: built in parallel, a quick run of the tests, no review of its own |
| **crew**, crew member | the always-on agents of the yard, tied to no bead |
| **yardmaster** | the crew member that coordinates: shapes beads, keeps the line moving, answers people |
| **assembly** | the starter pack's group that tests an approved bead and lands it on the base |
| flow | the stages a bead of some type goes through, and the moves allowed between them |
| stage | one step of a flow: a status or column, in a tracker (in the starter flow: backlog, new, review, approved, merged, decide) |
| route | which group takes the beads of a stage |
| group | who works a stage: a pool of agents, a script, people, or another yard |
| runner | how a group's work starts: `herdr` (an agent), `exec` (a command), `manual` (people), `peer` (another yard) |
| session | one agent working one bead in one stage |
| outcome | how a session says its stage ended (`done`, `changes`, `question`, `failed`); the flow picks the next stage |
| hook | a webhook endpoint that turns an outside system's deliveries into events |
| source | the outside system a hook receives from: `linear`, `github`, `token` |
| thread | a conversation in a source that a crew member takes part in: in Linear, an issue |
| link | a bead's tie to an outside issue it tracks or relates to |
| peer | another yard this one is linked with |
| pack | a way of working as one directory: flows, groups, roles, crew members |
| operator | the person who runs the yard; one of its users |
| user | a person of the yard: a name and an inbox |

## Words yardr used before

mayor (now yardmaster), epic (train), part (wagon), resident (crew member),
rig (depot), refinery (assembly). yardr prints only the new words; where an
old one is still read is in
[configuration.md](configuration.md#words-that-changed). In the BDP view a
train is still published as BDP's `epic`.
