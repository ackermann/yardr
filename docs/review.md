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
