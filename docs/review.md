# Review: beads waiting on you

`yardr review` walks the open beads in stages routed to manual groups (only
groups that list you as a member, if they list members), longest untouched
first. Each bead shows its stage, description, last notes and
`git diff --stat <base>...yard/<bead>` from the depot. Pick where it goes next:
the outcomes its flow names for the stage (`done: move to approved`), else
the stages its flow allows (either way a stage earlier in the flow sends it
back and asks for a note; a terminal stage closes it), or without a flow any
stage a route uses. "Open diff" pages the full diff through `$PAGER`; Skip leaves it.
Where the diff splits a file, the split follows a concept, not a stem suffix:
one concept per file, never a second `routing2.go`.

```sh
yardr review                    # the form
yardr review -g decisions       # only some groups' beads (backlog is manual too)
yardr review list               # the same queue for scripts (--json for actions)
yardr bead done <id> [--outcome <outcome>] [--note <text>]
yardr bead advance <id> --to <stage> [--note <text>]
```

To have one person, or one crew member, review the beads with a certain
label, see
[groups.md](groups.md#example-a-crew-member-reviews-the-beads-with-a-label).

A bead's asks read as numbered text among its notes, each with its state
(open, answered with the pick, put aside until when, declined, taken back
by its asker with why); answers fold into their ask. `yardr bead answer <id> <ask> <n> [words]` answers
one (`--help` for words, Later and Decline). A secret ask reads `a secret:
<names>, for the source <source> (<where its calls go>)` and, once given,
`<user> gave <names>`; it is answered with `yardr bead answer <id> <ask>
--secret`, typed unseen. A run ask shows the command its approval runs,
whole, under `when you approve, the yard runs this as <operator>, and
nothing else:`, with the yard's sentence for what it changes and each
context line marked `<asker> says:`, and who can take the change back;
the operator (for a user's own subscription or tell, the one asked) approves it
with `yardr bead answer <id> <ask> --approve`, and it then reads `<user> approved, ran:
<command>` with what the command printed. The queue itself is unchanged.
It stays beads in manual stages, longest untouched first.

When the stage itself asks the question (an ask template on the stage,
[flows.md](flows.md)), the yard's ask stands first among the notes and the
move actions under it. Answering moves the bead: a pick or words that fire
an outcome, or a pick of a stage on a card of the stage's moves, move it in the
same write. Moving it by hand takes the ask back
when the ask is open or put aside; answered and declined asks stay as
history. Later puts the question aside, Decline ends it, and the bead waits
to be moved by hand. Only the stage's members may answer, with the operator
allowed when the yard used the fallback addressee. If the stage has no listed
members, the usual rule applies and anyone may answer; other non-members are
refused whole. A broken template puts no ask: the list marks the bead with
the error instead of its moves, and the form shows it after the notes; the
bead still moves by hand.
