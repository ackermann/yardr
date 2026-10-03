The reviewed epic lands on `main` from here. No agent works this stage.

Locally, which is how rig yardr lands epics: the refinery
(`contrib/refinery/merge-local`) rebases `yard/<epic>` onto `main` if `main`
moved, runs the gate on what will land, fast-forwards `main` to it and
advances the epic straight to `merged`, by the edge no outcome offers; that
closes it.

Through a pull request, which is experimental and off unless `YARDR_PR=1` is
in the group's cmd (docs/pipeline.md#through-a-pull-request): pr-epic pushes
the branch as it was approved, with a lease, and records the pushed commit as
`pr_reviewed_sha`.

Outcomes:

- `done`: only on the pull request path. The reviewed commit is pushed and
  the pull request carries the review; the epic goes to `in-pr`.
- `failed`: the gate is red on what would land, or, on the pull request path,
  the epic is bound to no pull request, the push was refused, or GitHub
  reports the pull request as conflicting. The epic goes back to `open`.
- `conflict`: the rebase onto `main` conflicts. The note names the
  conflicting files, and the epic goes to `merge`.

The epic is held instead, with a note, when the gate could not run at all,
when `main`'s checkout or the epic's worktree is dirty, or when a pull
request script runs without `YARDR_PR=1`.
