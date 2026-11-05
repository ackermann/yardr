summary: The yard merges the bead's pull request as the GitHub App, at the head its tests passed on.

A bead whose pull request passed the reviews and the yard's tests waits here
for the merge. No agent and no person works this stage: `merge-pr`, the `cmd`
of an exec group, takes the bead and merges the pull request it tracks, as the
yard's GitHub App and in no person's name (docs/line.md, "Merging pull
requests").

The label `merge-ok` is the permission, and the yard's own review is the yes:
the script that posts the approving review gives it (`review-post` for a
`done-safe`, `review-security-post` for the security review's `done`), and the
script that pushes a new head takes it off. The yard merges only the head the
bead's `ci_head` names, and `--match-head-commit` has GitHub refuse the merge
if the head moves while it is made. The pull request's branch is the
repository's own: it is merged by rebase, or by squash where the group says
`YARDR_LAND=squash`, with the pull request's own title and body as the
commit's message, and the branch is deleted.

Outcomes:

- `merged`: the pull request is merged, by this script or on GitHub. The bead
  goes to `merged`, which closes it.
- `dropped`: the pull request was closed unmerged. The bead goes to `decide`,
  where a person reads why.

Whatever else stops the merge holds the bead here with a note, and nothing was
merged: no `merge-ok`, a head that moved since the tests ran, GitHub's refusal
(a conflict with `main`, the App without a permission). Nothing retries. Act on
what the note says, then `yardr bead unhold <id>`, or send the bead back:

- `yardr bead advance <id> --to ciwait` has the yard run the tests again on the
  head as it now is (the head must have been read by a reviewer, else
  `ciwait` holds it and says how to send it to `review`);
- `yardr bead advance <id> --to new` has a builder rebase a branch that
  conflicts with `main`.
