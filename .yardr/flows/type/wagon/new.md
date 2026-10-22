summary: A builder implements the wagon on `yard/<bead>`, from its train's branch, tested for what it changed.

A builder implements the wagon here, on branch `yard/<bead>` in the bead's own
worktree. The branch starts from the train's branch (`yard/<train>`) and lands
on it, not on `main`.

This is the default builder's stage with two differences:

1. The plain run of the depot's test command (`sh .yardr/test` in the
   worktree) is the only one, here and where the wagon lands. It formats,
   vets and builds everything, then tests the packages selected by your
   changes since the train's branch (not `main`): packages whose Go files or
   embedded files changed, their importers, plus packages named by the reads
   table. A selected change with no compiled input runs without `-race` and
   takes no race lock; the gate keeps the race detector for Go or embedded
   files, deletions, module files, and changes its reads table cannot select.
   The first line says which case it is. Do not run the whole
   suite (`YARDR_LANDING=1`), even for a change the plain run cannot see: it
   runs once, on the whole train, before `main` moves. Say in your report
   what your run did not test.
2. The report is short, because nobody reviews a wagon by itself: the train's
   reviewer reads the whole train with your note beside it. Three things, a
   few lines each: what it does; how you checked it (the exact commands);
   what the reviewer of the train should look at (what you are least sure of,
   what your run did not test, what touches another wagon).

Before the bead leaves as `done`:

- the work is committed on the branch, and `sh .yardr/test` passes on the
  last commit;
- a note on the bead is that report.

Outcomes:

- `done`: both hold. The wagon goes to `approved`, where the assembly lands it
  on the train's branch after the same run. There is no review stage.
- `question`: you are stuck, or the wagon itself is unclear (an unclear goal,
  a design choice). Note the question on the bead first; it goes to `decide`,
  to a human.

The edge back to `backlog` has no outcome: the yardmaster takes a wagon back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
