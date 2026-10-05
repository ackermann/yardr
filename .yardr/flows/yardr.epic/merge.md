summary: A conflict between the epic's branch and `main` is resolved.

A conflict between the epic's branch and `main` is resolved here: rebasing
`yard/<epic>` onto `main` conflicted, in `open` or in `approved`. The epic
was already integrated, or reviewed and approved; the job is only to make it
apply on top of what landed since, keeping the intent of both sides.

The work is on the epic's own branch, `yard/<epic>`, in the epic's worktree,
rebased onto `main`. The conflict is between the epic as a whole and what
landed on `main`, so read its children's beads for its side. Do not push
`yard/<epic>`: the integrator pushes it, with a lease.

Before the epic leaves as `done`, the branch is rebased onto `main`, the gate
passes on it (`yardr rig check yardr --dir .`), and one note on the epic
begins exactly `merge re-review:`. It lists the old tip (`old tip: <sha>`),
every conflicting file, what each side wanted, how both were kept, and the
gate result.

Outcomes:

- `done`: the above holds. The epic goes back to `open`, not to review: the
  integrator rebases it again (a no-op unless `main` moved again), gates it,
  pushes it and opens or updates its pull request before it goes to review.
- `rework`: both intents cannot be kept (`main` removed what the epic builds
  on, or the two changes contradict each other), or the conflict is too large
  to judge with confidence. The rebase is aborted and the note says what
  conflicts and why it cannot be merged mechanically. The epic goes back to
  `open`; the rework is a new child the mayor files, so make the note
  concrete enough to be a bead.
- `question`: the resolution needs a real design choice, which a person must
  make. The rebase is aborted, the note has the question and the options, and
  the epic goes to `decide`.
