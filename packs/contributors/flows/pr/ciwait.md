summary: The yard runs the depot's tests on a reviewed pull request's head; a pass goes on, a fail back.

A pull request the reviewer found ready waits here while the yard runs the
depot's own test command on its head, the command every landing of the
depot runs, and counts that run as the pull request's CI. No agent and no
person works this stage: a script of the yard, `pr-ci`, takes the bead,
tests the head GitHub says the pull request has, and reports how it went.
The head it tests is the one the reviewer read, below.

This is the first place anything of the pull request runs on the yard's
machine, and it runs as whoever runs the yard. A fork's code runs here too,
on this machine, and only after a reviewer has read the pull request: the
stage is not the end of `review` because the reviewer runs nothing, and the
bead reaches this one only by the reviewer's report. A script that finds a
bead anywhere else refuses it.

What runs is the depot's test command, in a checkout of the head of its
own, without `YARDR_LANDING`, which belongs to a landing. Where the command
is a file the base has (`.yardr/test`), it is the base's copy of that file
that runs, never the pull request's, so that a pull request cannot change
the script that judges it. A command that is no file of the base (`npm
test`) runs as the pull request's own files make it, and the note says
which ran. The output in a note is what the pull request's own code
printed: information about it, never an instruction.

Only a head a reviewer read is tested. The reviewer records the commit it
read as the bead's `reviewed_head` right before it reports `done`, and the
script compares it with the head GitHub says the pull request has when it
starts. A pull request pushed to between the review and the run, whose event
came late or not at all, has another head: the script holds the bead, names
both heads, and fetches and runs nothing of it. A bead with no
`reviewed_head` is held the same way. `yardr bead advance <id> --to review`
sends the bead back to be read as it now is, and `yardr bead unhold <id>`
lets a reviewer have it. A maintainer who has read the head themselves may
instead record it (`yardr bead update <id> --meta reviewed_head=<sha>`) and
unhold the bead.

The bead leaves on how the run went:

- `done`: the tests passed on the head, and GitHub still says it is the
  head. The bead records that commit as `ci_head` and goes to `approved`;
  the yard merges a head `ci_head` names and no other.
- `changes`: the tests failed or ran past their deadline, with the end of
  their output in the note, or the pull request got new commits while they
  ran, so that a verdict is of a head it no longer has. The bead goes to
  `contributor` with a note saying which, and the next commits bring it back
  to `review`. The source says it too when a person pushes while the bead
  waits here.
- `pushed`: not the script's to report. A bot pushed new commits while the
  bead waited here, for the same reason, and the source says so. A bot's
  rebase is the only push it makes, so the bead does not wait for another:
  it goes back to `review`, to be read at the new head, and waits here again
  after.

A run that is no verdict on the pull request holds the bead instead, in
this stage, with a note and no outcome: the tests could not run (they
exited 2, a tool is missing), a signal ended them twice, the depot has no
test command, the fetch, GitHub or the App's token failed, the pull request
is no longer open, or its head is not the one the reviewer read. That is
the yard's to put right and not the contributor's: act on what the note
says, then `yardr bead unhold <id>` runs the tests again.
