summary: The assembly tests the bead and lands it on `main`, one bead at a time.

The assembly lands the bead on `main` from here, one bead at a time
(`contrib/assembly/merge-local`; docs/line.md). No agent works this
stage. The bead was built, reviewed and approved; the assembly rebases
`yard/<bead>` onto `main` if `main` moved, runs the depot's test command on
exactly what will land (`main`'s copy of `.yardr/test`, with
`YARDR_LANDING=1`: the whole suite, or the plain selection when the landing
changes nothing compiled; docs/line.md, "One whole-suite run per bead"),
whatever the builder and the reviewer reported, and fast-forwards `main` to
that commit.

Outcomes:

- `done`: the commit is on `main`. The note says `merged <sha>`, and `merged`
  closes the bead.
- `failed`: the tests are red on what would land, the branch moved while
  they ran, or the rebase conflicts (this flow has no `merge` stage). The
  note carries the end of the tests' output or the conflicting files, and the
  bead goes back to `new`.
- `question`: landing it needs a person's decision; to `decide`.

The assembly holds the bead instead, with a note, when no builder could fix
what stopped it: the tests could not run at all, or `main`'s checkout or the
bead's worktree is dirty.
