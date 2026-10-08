summary: The train waits while any child is open, then is rebased onto `main` and tested.

The train waits here while any child is open: each child blocks it, and lands
on the train's branch `yard/<train>`, not on `main`. Once the last child closes
the train is ready and is integrated (`contrib/assembly/integrate-train`;
docs/line.md#trains): `yard/<train>` is rebased onto `main` in the train's
worktree, tested there (the depot's test command with `YARDR_LANDING=1`), and
pushed when the yard pushes.

Outcomes:

- `done`: the branch is rebased onto `main` and the suite passes on it. The
  train goes to review as what will land.
- `failed`: the suite is red on the rebased branch, or the push or the pull
  request failed. The train stays in `open`. For a red suite the fix is a new
  child (type bug, with the end of the output in its body), added before
  reporting, so the train is not integrated again until the fix has landed
  on its branch.
- `conflict`: the rebase onto `main` conflicts. It is aborted, the note names
  the conflicting files and records the tip as `old tip: <sha>`, and the train
  goes to `merge`.

The edge back to `backlog` has no outcome: the yardmaster takes the train back
with `yardr bead advance <train> --to backlog` to reshape it.

A train is held instead, with a note, when nothing in the flow can fix it:
the suite could not run at all, or the branch is missing, dirty or has nothing
ahead of `main`.
