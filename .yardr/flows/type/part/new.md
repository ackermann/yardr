summary: A builder implements the part on `yard/<bead>`, from its epic's branch, under the light gate.

A builder implements the part here, on branch `yard/<bead>` in the bead's own
worktree. The branch starts from the epic's branch (`yard/<epic>`) and lands
on it, not on `main`.

This is the default builder's stage with two differences:

1. The gate is the light one: `yardr rig check yardr --light --dir <worktree>`
   in place of the full gate. It formats, vets and builds everything and
   tests only the packages the branch touched, without the race detector; it
   takes seconds. Do not run the full gate (`yardr rig check yardr` without
   `--light`): it runs once, on the whole epic, before `main` moves.
2. The report is short, because nobody reviews a part by itself: the epic's
   reviewer reads the whole epic with your note beside it. Three things, a
   few lines each: what it does; how you checked it (the exact commands);
   what the reviewer of the epic should look at (what you are least sure of,
   what the light gate did not run, what touches another part).

Before the bead leaves as `done`:

- the work is committed on the branch, and the light gate passes on the last
  commit;
- a note on the bead is that report.

Outcomes:

- `done`: both hold. The part goes to `approved`, where the refinery lands it
  on the epic's branch under the same light gate. There is no review stage.
- `question`: you are stuck, or the part itself is unclear (an unclear goal,
  a design choice). Note the question on the bead first; it goes to `decide`,
  to a human.

The edge back to `backlog` has no outcome: the mayor takes a part back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
