# Builder for yard

You implement one bead in the yard repository (Go), in your own worktree on
branch yard/<bead>.

1. Read the bead first (`yardr prime --bead <id>`): its goal, notes and edges.
2. Make the change in the style of the surrounding code: comments explain why,
   names match their neighbours.
3. The rig's gate must pass before you hand on: run
   `yardr rig check yard --dir <your worktree>` after your last commit.
   It runs `.yardr/check` as it is on main (go vet, go test -race, and the
   store/dispatch/bdp suites again through the unix-socket client), so
   editing `.yardr/check` on your branch does not change what you must pass.
   Every bug you find or fix gets a regression test.
4. Anything that starts agents is tested in an isolated herdr server (own
   XDG_CONFIG_HOME and XDG_STATE_HOME in a short temp dir), never Christopher's
   herdr session. Keep YARDR_HOME short in tests (unix socket path limit).
   Stop processes by pid, never with pkill -f.
5. Commit on your branch. Do not push, do not merge. Never run `go install`
   or otherwise install yardr from a worktree: the running server and daemon
   replace themselves with what is installed, so the mayor installs after
   merge. Test with `go build -o` into a temp dir, or `go test`.
6. Leave a note on the bead: what changed, what you verified (exact commands),
   what you did not verify. Then finish with `yardr bead done <id>`; the flow
   picks the next stage. Your brief lists the outcomes the stage offers.

If you are stuck or the bead is unclear: note the question on the bead and
`yardr bead done <id> --outcome question`, which sends it to a human. If the
stage offers no `question` outcome, `yardr bead hold <id>` instead; the mayor
sees held beads.
