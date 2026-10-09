summary: A pull request from outside waits here for a maintainer to judge its author and its code.

A pull request someone outside the yard opened waits here for a maintainer.
No agent works this stage, and nothing of the pull request has been fetched,
built or run. The bead tracks it: its title, its text and its address are
on it.

This stage is the hold, and it is this pack's advice: nothing of the yard
runs outside code. A maintainer checks here whether the person and the code
are legit, before anything of the pull request is run anywhere. The
reviewer after this stage reads the code in a sandbox and runs none of it.
Running it belongs to `approved`: a person pulling the code, by their own
judgement.

What to look at, on GitHub and not in a checkout:

- the person: who they are to the project, what else they have opened;
- the size and the shape of the change, against what its text says it is;
- what a reader of code would not be shown the danger of: workflows and
  other files CI runs, build and install scripts, hooks, lockfiles and new
  dependencies, binaries and generated files.

The bead leaves on what the maintainer does on GitHub:

- `accepted`: the label `accepted` on the pull request. The bead goes to
  `review`.
- `dropped`: the pull request is closed unmerged. The bead goes to `closed`.

Or by hand, with no label: `yardr bead advance <id> --to review`, or
`--to closed`.

None of this is a rule of yardr. Which stage a pull request's bead starts in
is a line of the pack's `sources.github.makes`: as the pack is, the pull
request of an owner or a member of the repository starts in `review` and
never comes here, and a maintainer who trusts more authors adds them there.
