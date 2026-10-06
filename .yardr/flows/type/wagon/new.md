summary: A builder implements the wagon on `yard/<bead>`, from its train's branch, under the light gate.

A builder implements the wagon here, on branch `yard/<bead>` in the bead's own
worktree. The branch starts from the train's branch (`yard/<train>`) and lands
on it, not on `main`.

This is the default builder's stage with two differences:

1. The light gate (`yardr depot check yardr --light --dir <worktree>`) is the
   only gate, here and where the wagon lands. It formats, vets and builds
   everything and tests, with the race detector, only the packages your
   wagon changed (since the train's branch, not `main`: its first line says
   `light gate, changes since yard/<train> at <sha>`) and the packages that
   import them: seconds for a leaf package, close to the full gate for one
   nearly everything imports (`store`, `config`). Do not run the full gate (`yardr depot check yardr`
   without `--light`), even for a change the light one cannot see: it runs
   once, on the whole train, before `main` moves. Say in your report what the
   light gate did not run.
2. The report is short, because nobody reviews a wagon by itself: the train's
   reviewer reads the whole train with your note beside it. Three things, a
   few lines each: what it does; how you checked it (the exact commands);
   what the reviewer of the train should look at (what you are least sure of,
   what the light gate did not run, what touches another wagon).

Before the bead leaves as `done`:

- the work is committed on the branch, and the light gate passes on the last
  commit;
- a note on the bead is that report.

Outcomes:

- `done`: both hold. The wagon goes to `approved`, where the assembly lands it
  on the train's branch under the same light gate. There is no review stage.
- `question`: you are stuck, or the wagon itself is unclear (an unclear goal,
  a design choice). Note the question on the bead first; it goes to `decide`,
  to a human.

The edge back to `backlog` has no outcome: the yardmaster takes a wagon back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
