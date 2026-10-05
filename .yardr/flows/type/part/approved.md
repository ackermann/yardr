summary: The refinery lands the part on its epic's branch under the light gate, one bead at a time.

The refinery lands the part on its epic's branch (`yard/<epic>`) from here,
one bead at a time (`contrib/refinery/merge-local`; docs/pipeline.md, "Parts").
No agent works this stage, and nobody reviewed the part: the builder's `done`
brought it here. The refinery rebases `yard/<bead>` onto the epic's branch if
that moved, runs the light gate (`yardr rig check yardr --light`) on exactly
what will land, and fast-forwards the epic's branch to that commit. `main`
does not move: the full gate and the review are the epic's.

Outcomes:

- `done`: the commit is on the epic's branch. The note says `merged <sha>`
  and that it landed under the light gate, and `merged` closes the part.
- `failed`: the light gate is red on what would land, the branch moved while
  the gate ran, or the rebase conflicts (this flow has no `merge` stage). The
  note carries the end of the gate's output or the conflicting files, and the
  part goes back to `new`.
- `question`: landing it needs a person's decision; to `decide`.

The refinery holds the part instead, with a note, when no builder could fix
what stopped it: the gate could not run at all, or a worktree is dirty.
