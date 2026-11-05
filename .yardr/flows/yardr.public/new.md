summary: A builder implements the bead here, on branch `yard/<bead>`; the next stage publishes it as a pull request.

A builder implements the bead here, on branch `yard/<bead>` in the bead's own
worktree. This bead is on the public line (flow `yardr.public`; docs/line.md,
"The public line"): when you report `done`, a script of the yard pushes the
branch and opens a pull request from it on the depot's public repository,
and the reviewers read it there.

Before the bead leaves as `done`:

- the work is committed on the branch, and the depot's test command passes
  on the last commit (`sh .yardr/test` in the worktree). The landing run
  runs once, on what lands (`YARDR_LANDING=1`); it is yours only for a
  change the plain run cannot see (the builder's role says which);
- a note on the bead says what changed, what was verified (the exact
  commands) and what was not verified, and ends with the test line:
  `test: <command> @ <commit>: pass` or `fail`, with the packages counted.

What is published is the branch's commits as they are, and the bead's title.
The pull request's title is the squash subject line the landing writes for a
bead (docs/line.md, "Landing by squash"), and its body is made from your
commit messages and the bead's title alone: nothing of the bead's body or
notes is copied into it. So every commit
message is read by anyone, and says what the change does and why the code is
right and nothing of who wants it or why (the brief's section on customers
holds for each word you commit, the messages included).

You push nothing and open no pull request: the next stage does, as the yard's
GitHub App. A bead that comes back (a review, the security review or the
yard's tests sent it back, or the pull request conflicted with `main`) has
its pull request already. Fix it on the same branch and report `done` again:
the script pushes the branch to the same pull request, and the old head is
replaced where you rebased. A conflict with `main` is yours to resolve by a
rebase (`git rebase main`), after which the tests run again.

Outcomes:

- `done`: both hold. The bead goes to `pr`, which publishes it, and from there
  to `review`, where it is reviewed forward and approved without a human
  looking at it again.
- `question`: you are stuck, or the bead itself is unclear (an unclear goal,
  a design choice). Note the question on the bead first; it goes to `decide`,
  to a human.

The edge back to `backlog` has no outcome: the yardmaster takes a bead back with
`yardr bead advance <id> --to backlog` when it has to be reshaped.
