The refinery lands the bead on `main` from here, one bead at a time
(`contrib/refinery/merge-local`; docs/pipeline.md). No agent works this
stage. The bead was built, reviewed and approved; the refinery rebases
`yard/<bead>` onto `main` if `main` moved, runs the gate on exactly what will
land, whatever the builder and the reviewer reported, and fast-forwards
`main` to that commit.

Outcomes:

- `done`: the commit is on `main`. The note says `merged <sha>`, and `merged`
  closes the bead.
- `failed`: the gate is red on what would land, the branch moved while the
  gate ran, or the rebase conflicts (this flow has no `merge` stage). The
  note carries the end of the gate's output or the conflicting files, and the
  bead goes back to `new`.
- `question`: landing it needs a person's decision; to `decide`.

The refinery holds the bead instead, with a note, when no builder could fix
what stopped it: the gate could not run at all, or `main`'s checkout or the
bead's worktree is dirty.
