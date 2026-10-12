# Review: beads waiting on you

`yardr review` walks the open beads in stages routed to manual groups (only
groups that list you as a member, if they list members), longest untouched
first. Each bead shows its stage, description, last notes and
`git diff --stat <base>...yard/<bead>` from the depot. Pick where it goes next:
the outcomes its flow names for the stage (`done: move to approved`), else
the stages its flow allows (either way a stage earlier in the flow sends it
back and asks for a note; a terminal stage closes it), or without a flow any
stage a route uses. "Open diff" pages the full diff through `$PAGER`; Skip leaves it.

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
(open, answered with the pick, put aside until when, declined); answers
fold into their ask. `yardr bead answer <id> <ask> <n> [words]` answers
one (`--help` for words, Later and Decline). A secret ask reads `a secret:
<names>, for the source <source> (<where its calls go>)` and, once given,
`<user> gave <names>`; it is answered with `yardr bead answer <id> <ask>
--secret`, typed unseen. The queue itself is unchanged.
It stays beads in manual stages, longest untouched first.
