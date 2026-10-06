summary: The train's pull request is merged, at the reviewed commit and no other.

The train's pull request is merged here (`contrib/assembly/pr-merge`;
docs/pipeline.md#through-a-pull-request). No agent works this stage. What is
merged is the reviewed commit and no other: the merge names
`pr_reviewed_sha`, and afterwards the depot's checkout of `main` is
fast-forwarded to the remote's.

Outcomes:

- `done`: the pull request is merged and `main` fast-forwarded. The train goes
  to `merged`, which closes it.
- `failed`: the train has no `pr_reviewed_sha`, or GitHub refuses the merge
  (checks pending, a review required, conflicts). The note carries GitHub's
  message, and the train goes back to `open`.

The train is held instead, unmerged, when the pull request's head is not the
reviewed commit: bring the new commits into the train's branch and send it
back to `open` to have them reviewed, or put the remote branch back at the
reviewed commit and unhold. It is held too when `main`'s checkout is not
clean and on `main`, or when the script runs without `YARDR_PR=1`.
