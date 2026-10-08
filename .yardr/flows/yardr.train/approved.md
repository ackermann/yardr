The reviewed train lands on `main` from here. No agent works this stage.

Locally, which is how depot yardr lands trains: the assembly
(`contrib/assembly/merge-local`) rebases `yard/<train>` onto `main` if `main`
moved, runs the gate on what will land, fast-forwards `main` to it and
advances the train straight to `merged`, by the edge no outcome offers; that
closes it.

Through a pull request, which is experimental and off unless `YARDR_PR=1` is
in the group's cmd (docs/line.md#through-a-pull-request): pr-train pushes
the branch as it was approved, with a lease, and records the pushed commit as
`pr_reviewed_sha`.

Outcomes:

- `done`: only on the pull request path. The reviewed commit is pushed and
  the pull request carries the review; the train goes to `in-pr`.
- `failed`: the gate is red on what would land, or, on the pull request path,
  the train is bound to no pull request, the push was refused, or GitHub
  reports the pull request as conflicting. The train goes back to `open`.
- `conflict`: the rebase onto `main` conflicts. The note names the
  conflicting files, and the train goes to `merge`.
- `changes` and `merged`: no session reports these. They are what GitHub's
  events say of the train's pull request, through the table on the yard's
  github hook (`yardr hook show gh`): changes a person requests there send
  the train back to `open`, and a pull request merged by hand closes it.

The train is held instead, with a note, when the gate could not run at all,
when `main`'s checkout or the train's worktree is dirty, or when a pull
request script runs without `YARDR_PR=1`.
