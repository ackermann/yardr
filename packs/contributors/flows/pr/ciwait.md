summary: A reviewed pull request waits here for CI to pass on its head; nobody works this stage.

A pull request the reviewer found ready waits here for the repository's CI to
pass on its head. Nobody works this stage, no agent and no person, and
nothing of the pull request is fetched, built or run by the yard: the bead
leaves when the source reports how CI went.

It is a stage of its own, not the end of `review`, because the reviewer
cannot see CI. A success reported while the reviewer still read would
approve a pull request nobody had finished reading.

The bead leaves on what the source reports:

- `done`: CI succeeded on the pull request's head. The bead goes to
  `approved`.
- `changes`: CI failed, or a person pushed new commits to the pull request
  while it waited, so that a verdict for the head it had is of no use. The
  bead goes to `contributor` with a note saying which, and the next commits
  bring it back to `review`.
- `pushed`: a bot pushed new commits while it waited, for the same reason.
  A bot's rebase is the only push it makes, so the bead does not wait for
  another: it goes back to `review`, to be read at the new head, and waits
  here again after.

A run that was cancelled or ended neutral says nothing, and the bead keeps
waiting for the next one.

A verdict that never comes leaves the bead here. The runs of a fork's
branch name no pull request, so no bead hears them, and a repository with
no CI sends none. A maintainer who has looked at the pull request on GitHub
moves the bead by hand: `yardr bead advance <id> --to approved`, or
`--to contributor`.
