summary: The assembly lands the wagon on its train's branch after the plain run of its tests, one bead at a time.

The assembly lands the wagon on its train's branch (`yard/<train>`) from here,
one bead at a time (`contrib/assembly/merge-local`; docs/line.md, "Wagons").
No agent works this stage, and nobody reviewed the wagon: the builder's `done`
brought it here. The assembly rebases `yard/<bead>` onto the train's branch if
that moved, runs the depot's test command without `YARDR_LANDING` (the copy
of `.yardr/test` on the train's branch: what the wagon changed) on exactly
what will land, and fast-forwards the train's branch to that commit. `main` does not move:
the whole suite and the review are the train's.

Outcomes:

- `done`: the commit is on the train's branch. The note says `merged <sha>`
  and that it was tested without `YARDR_LANDING`, and `merged` closes the wagon.
- `failed`: the tests are red on what would land, the branch moved while
  they ran, or the rebase conflicts (this flow has no `merge` stage). The
  note carries the end of the tests' output or the conflicting files, and the
  wagon goes back to `new`.
- `question`: landing it needs a person's decision; to `decide`.

The assembly holds the wagon instead, with a note, when no builder could fix
what stopped it: the tests could not run at all, or a worktree is dirty.
