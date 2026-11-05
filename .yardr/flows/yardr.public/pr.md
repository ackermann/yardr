summary: The yard pushes the bead's branch and opens (or updates) its pull request as the GitHub App.

The bead's branch is published here, in public. No agent and no person works
this stage: `contrib/assembly/open-pr`, the `cmd` of an exec group, takes the
bead, pushes `yard/<bead>` to the depot's remote and opens the pull request
from it as the yard's GitHub App (docs/line.md, "The public line"). It never
uses the operator's `gh` login, and never touches the bead's worktree.

- **Title and body.** The title is the squash subject line the landing would
  write (`<type>[(<scope>)][!]: <bead title> (<bead id>)`). The body is made
  from the branch's commit messages and the bead's title alone. Nothing of the
  bead's body or notes is copied into either.
- **The same pull request.** The bead tracks the pull request from then on
  (its tracks link, which pr-ci and merge-pr read), and records it as its own
  (`own_pr`), so its sessions work on the branch and not on the pull
  request's head. A bead that comes back has the pull request it had: the
  script pushes the branch to it and rewrites its title and body, and opens
  no second one.
- **A new head voids the yes.** The script takes the label `merge-ok` off the
  bead each time it pushes. The script that posts the approving review gives
  it again, on the head that review is of.

Outcomes:

- `done`: the branch is on the remote and the pull request is open and up to
  date. The note names both. The bead goes to `review`.
- `failed`: there is nothing to publish (the branch has no commit past the
  base), or GitHub says the pull request conflicts with the base. The note
  says which; the bead goes back to `new`, to be rebased onto the base. The
  flow limits the rounds back to `new` (`yardr flow show` writes a limit as
  `max 3 then decide`).

A run that is no verdict on the branch holds the bead with a note and reports
nothing: no token (the group's environment has no `GITHUB_APP_ID` and
`GITHUB_APP_PRIVATE_KEY_FILE`), the push refused, a remote branch with commits
the bead's branch never had, a pull request that is not the one the bead
tracks, a worktree with uncommitted changes. That is the yard's to put right:
act on what the note says, then `yardr bead unhold <id>` runs the script again.
