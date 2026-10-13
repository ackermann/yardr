# yardr design pack

The pack yardr ships for a question of direction: what to build and how,
before anything is built. A bead of type `design` asks for a document and
no code. An agent drafts it on the bead's branch, an agent that did not
write it argues against it, a person decides, and the document lands on the
base as any change does.

It is for a git depot, beside the pack the depot follows for its own work
(the [starter](../starter), say): its one flow is for beads of the type
`design`, and it has none for the depot. A yard takes it when it wants
designs. `yardr pack check design --depot <depot>` shows what importing it
would change, and `yardr pack apply design --depot <depot>` imports it.
yardr carries this pack in its binary, so the name is enough;
`packs/design`, with the separator, is this directory in a checkout. The
format and how a pack maps to a yard are in
[docs/packs.md](../../docs/packs.md).

- `pack.yaml`: the flow, the groups that work it, and the crew member its
  roles name.
- `flows/design/`: a stage file for every stage a bead waits or is worked
  in. What a design asks of a builder and a reviewer is here, not in their
  roles.
- `roles/builder.md`, `roles/reviewer.md` and `scripts/merge-local` are
  the starter's, copied: a pack names a group together with the role or the
  script it runs, and these groups are the starter's. A test fails when a
  copy drifts.

## The flow

```
backlog -> draft | decide
draft -> critique on done | decide on question | backlog
decide -> approved | draft | backlog
critique -> decide on done | draft on changes
approved -> merged on done | draft on failed | decide on question
merged terminal
```

| stage | worked by | what happens |
|---|---|---|
| `backlog` | `backlog`, people | the bead waits until its question is clear enough to draft from |
| `draft` | `builders` | an agent writes `docs/design/<date>-<bead>-<name>.md` on the bead's branch: what exists, the smallest proposal, the open questions, the beads it would become |
| `critique` | `reviewers` | another agent checks the document's claims in the code and its proposal against the repository's principles, and changes nothing. `changes` sends it back to `draft`; from the third round on the stage file has it go on to a person |
| `decide` | `decisions`, people | a person reads the document and both notes, answers the open questions, and moves the bead to `approved`, back to `draft`, or to `backlog` |
| `approved` | `assembly` | the document lands on the depot's base, after the depot's test command |

A design bead is made as any bead, with its type: `yardr bead create
"<question>" --depot <depot> --type design`. It starts in `backlog`.

## What it needs to be there already

A depot that already lands its own work: an agent group that builds, one
that reviews, and whatever lands a branch on the base. The pack brings no
agents of its own. Its `builders`, `reviewers` and `assembly` are the
starter's groups by the starter's names, and `backlog` and `decisions` are
the yard's two people groups.

- **In a depot the starter set up**, the import finds `<depot>-builders`,
  `<depot>-reviewers` and `<depot>-assembly` by name and routes the lane to
  them. It points them at this pack's copies of the roles and the script,
  which are the same text, and leaves their limits, agents and arguments as
  they are.
- **In a depot whose groups have other names or roles of their own**, say
  which group of the yard takes each, and nothing of those groups is
  changed:

  ```sh
  yardr pack apply design --depot web \
    --group builders=web-writers --group reviewers=web-critics \
    --group assembly=web-refinery
  ```

  Without `--group`, a group the depot lacks is made, and a group it has
  under the pack's name is pointed at the pack's role: `pack check` shows
  either before anything is written.

The pack names the crew member `yardmaster`, because the copied roles tell
an agent to mail it. It carries no role for it, so the yardmaster keeps the
prompt it has, and it hears what the starter has it hear: the kinds of
event that need someone, a bead entering `decide`, a bead closing.

## What the import asks

Beside a depot the starter set up, with a crew member called `yardmaster`:
nothing. Otherwise what any pack asks for what the yard lacks:

- **the agent of a group it makes** (`--bind builders=<kind>`, `--bind
  reviewers=<kind>`), or the group of the yard that takes it (`--group`);
- **the members of `backlog` and `decisions`**, when the yard has no such
  group (`--members decisions=<a>,<b>`);
- **which crew member takes `yardmaster`**, when the yard has none of that
  name (`--crew yardmaster=<name>`).

## Not in this pack

A role of its own: who drafts and who critiques are the depot's builders
and reviewers, and the stage files say what a design asks of them. Shaping
the beads a decided design lists is the yardmaster's, after the bead has
landed.
