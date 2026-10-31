summary: A reviewed pull request is merged from here: by the yard if it is merge-ok, else by a maintainer.

A pull request the reviewer found ready, and whose tests the yard ran green
on its head, waits here. The review is a note on the bead; it was made by
reading. The tests were the yard's CI run in `ciwait`, of the depot's own
test command alone, and the bead has the commit they passed on as `ci_head`.

Running it beyond that is here, and a person's: a maintainer who wants to see
it work pulls the code and runs it where they choose, by their own judgement
of the author and of what the review says.

The label `merge-ok` says the yard may merge the pull request. One that
carries it is merged by the yard, as its GitHub App and in no person's
name, and the bead leaves on the merge. The reviewer gives the label to a
bot's bump of Go modules and to nothing else. For any other pull request it
is a maintainer's yes, and the yes is one command, with the sha of the head
commit they looked at on the pull request's page:

    yardr bead update <id> --add-label merge-ok --meta merge_head=<sha>

That is how a yes reaches the bead, whoever types it. A bead's agent may not:
`merge_head` is refused to it on any bead, its own included. A pull request
that is not a bot's from a branch of the repository itself (a fork, a person's
branch, a bot's fork) is merged at that commit and at no other: a head that
moved since is held, not merged. A bot's pull request from the repository
itself is merged at the head it has when the yard looks, which nothing
records, since such a bot rebases its branches all the time and a recorded
sha would hold every one. Either way the yard's CI must have passed on that
commit (`ci_head` is it), and the merge is refused by GitHub if the head
moves while it is made. A
fork is merged with a merge commit and its branch is left alone, being its
author's; a branch of the repository's own is rebased on and deleted.

A pull request without the label waits here for a maintainer, who merges
it on GitHub or says yes as above. When the yard holds the bead instead of
merging (CI that has not passed on the head, a head that moved, a yes that
is missing, a refusal of GitHub or of the App), the note says why and
nothing was merged: a maintainer acts on GitHub, then unholds the bead,
which is looked at again. A pull request pushed to since its tests ran has no
CI on its new head: `yardr bead advance <id> --to ciwait` has the yard run
them again (the bead is held, so `yardr bead unhold <id>` too), and the bead
comes back here. The yard tests only a head a reviewer read, so for a head
none did, `ciwait` holds the bead instead and its note says how to send it to
`review`: it comes back here after the reviewer and the tests. For a pull
request that waited in `triage` it tests only a head a maintainer accepted
too, and a head that was not has the note say how to record it
(`accepted_head`). The yard reports a bead merged or dropped and nothing
else.

What of the review reaches the contributor is the maintainer's to post, in
their own words or the note's, approved text only: `yardr reply --crew
yardmaster <seq> - < review.md`, where `<seq>` is the number the pull
request's delivery starts with in the inbox and review.md holds the
approved text. Run the command in an environment that sets the credential
variables in the yardmaster's `env_file`; for a GitHub App, the source's
`mint_command` turns its two inputs into the token for the comment. It goes
out under the yard's bot, the App's `<slug>[bot]`. A command without a
credential set in its environment is refused; yardr does not use the shell's
`gh` login. Nothing posts by itself:
no reviewer, no stage and no hook puts the review on the pull request.

A write through the yard's GitHub App is not proven everywhere:
check the comment appeared on the pull request.

The bead leaves on what comes of the pull request on GitHub, and the
merge by the yard is one of those. Nothing of the yard pushes to a pull
request:

- `merged`: the pull request is merged. The bead goes to `merged`.
- `dropped`: it is closed unmerged. The bead goes to `closed`.

Or by hand: `yardr bead advance <id> --to merged`, or `--to closed`.
