New beads wait here, where `yardr bead create` puts them, until the yardmaster
or a person picks them for `shape`. No agent works this stage.

A bead is ready for `new` once `shape` has specified it well enough for a
builder to finish it without asking: a clear goal, where to look, what done
means, how to test. A large bead is split first, and
`yardr dep add <blocker> <bead>` orders the parts: a bead is only ready once
its blockers are closed.

The stage has no outcomes; whoever picks the bead moves it:

- to `shape`, by default: `yardr bead advance <id> --to shape`;
- to `new`, when the bead is already specified well enough to build:
  `yardr bead advance <id> --to new`;
- to `decide`, when what the bead should be is Christopher's to decide: the
  question and the options go into the bead first
  (`yardr bead advance <id> --to decide`).
