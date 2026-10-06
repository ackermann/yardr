summary: The refinery lands the car on its train's branch under the light gate, one bead at a time.

The refinery lands the car on its train's branch (`yard/<train>`) from here,
one bead at a time (`contrib/refinery/merge-local`; docs/pipeline.md, "Cars").
No agent works this stage, and nobody reviewed the car: the builder's `done`
brought it here. The refinery rebases `yard/<bead>` onto the train's branch if
that moved, runs the light gate (`yardr depot check yardr --light`) on exactly
what will land, and fast-forwards the train's branch to that commit. `main`
does not move: the full gate and the review are the train's.

Outcomes:

- `done`: the commit is on the train's branch. The note says `merged <sha>`
  and that it landed under the light gate, and `merged` closes the car.
- `failed`: the light gate is red on what would land, the branch moved while
  the gate ran, or the rebase conflicts (this flow has no `merge` stage). The
  note carries the end of the gate's output or the conflicting files, and the
  car goes back to `new`.
- `question`: landing it needs a person's decision; to `decide`.

The refinery holds the car instead, with a note, when no builder could fix
what stopped it: the gate could not run at all, or a worktree is dirty.
