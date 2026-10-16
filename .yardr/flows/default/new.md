A builder implements the bead here, on branch `yard/<bead>` in the bead's own
worktree.

Before the bead leaves as `done`:

- the work is committed on the branch, and the depot's test command passes
  on the last commit (`sh .yardr/test` in the worktree). The landing run
  runs once, on what lands (`YARDR_LANDING=1`); it is yours only for a
  change the plain run cannot see (the builder's role says which);
- a note on the bead says what changed, what was verified (the exact
  commands) and what was not verified, and ends with the test line:
  `test: <command> @ <commit>: pass` or `fail`, with the packages counted.

Outcomes:

- `done`: both hold. The bead goes to review, where it is reviewed forward
  and approved without a human looking at it again.
- `question`: you are stuck, or the bead itself is unclear (an unclear goal,
  a design choice). Note the question on the bead first; it goes to `decide`,
  to a human.

The edge back to `backlog` has no outcome: the yardmaster takes a bead back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
