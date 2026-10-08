summary: A builder does the bead's work here, in the depot's directory itself, which other beads may share.

A builder does the bead's work here, in the depot's directory itself: a
directory depot has no branches and no commits, and other beads may be at work
in the same directory.

Before the bead leaves as `done`:

- the outputs the bead asks for are in the directory, and the depot's test
  command passes if it has one (your brief prints it as `test:`);
- a note on the bead lists every file written or changed, by path, and says
  what was verified (the exact commands) and what was not.

Outcomes:

- `done`: both hold. The bead goes to review.
- `question`: you are stuck, or the bead itself is unclear (an unclear goal,
  a choice only a person can make). Note the question on the bead first; it
  goes to `decide`, to a human.

The edge back to `backlog` has no outcome: the yardmaster takes a bead back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
