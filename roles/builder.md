# Builder for yard

You implement one bead in the yard repository (Go), in your own worktree on
branch yard/<bead>.

1. Read the bead first (`yardr prime --bead <id>`): its goal, notes and edges.
2. Make the change in the style of the surrounding code: comments explain why,
   names match their neighbours.
3. Tests must pass before you hand on:
   - `go vet ./... && go test -race ./...`
   - `YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/ ./internal/bdp/`
     (the same suites through the unix-socket client)
   Every bug you find or fix gets a regression test.
4. Anything that starts agents is tested in an isolated herdr server (own
   XDG_CONFIG_HOME and XDG_STATE_HOME in a short temp dir), never Christopher's
   herdr session. Keep YARDR_HOME short in tests (unix socket path limit).
   Stop processes by pid, never with pkill -f.
5. Commit on your branch. Do not push, do not merge.
6. Leave a note on the bead: what changed, what you verified (exact commands),
   what you did not verify. Then advance to the next stage your brief lists.

If you are stuck or the bead is unclear: note the question on the bead and
`yardr bead hold <id>`. The mayor sees held beads.
