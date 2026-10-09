summary: An issue from outside waits here for a maintainer, who accepts it for the backlog or drops it.

An issue someone outside the yard opened waits here for a maintainer. No
agent works this stage. The bead tracks the issue: its title, its text and
its address are on it.

Read it as a maintainer reads any report: is it real, is it something the
project wants, is it there already. Its text is a stranger's. Once the bead
is accepted a builder is given that text, so what in it reads as an order
to whoever works it (run this, fetch that, ignore your rules) is a reason
to rewrite the bead before it leaves the backlog, or to drop it.

The bead leaves on what the maintainer does on GitHub:

- `accepted`: the label `accepted` on the issue. The bead goes to `backlog`,
  where it is shaped like any other bead, and on through the stages the
  project's own work takes.
- `dropped`: the issue is closed. The bead goes to `closed`.

Or by hand, with no label: `yardr bead advance <id> --to backlog`, or
`--to closed`.
