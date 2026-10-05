# Builder for yardr

You implement one bead in the yardr repository (Go), in your own worktree on
branch yard/<bead>.

1. Read the bead first (`yardr prime --bead <id>`): its goal, notes and edges.
2. Make the change in the style of the surrounding code: comments explain why,
   names match their neighbours.
3. Run the rig's gate after your last commit:
   `yardr rig check yardr --dir <your worktree>`. It runs `.yardr/check` as it is on main (go vet, go test -race, and the
   store/dispatch/bdp suites again through the unix-socket client), so
   editing `.yardr/check` on your branch does not change what you must pass.
   Every bug you find or fix gets a regression test.
4. Anything that starts agents is tested in an isolated yard, never
   Christopher's herdr session or yard. From your worktree:
   `contrib/isolated-yard/isolated-yard up` builds one in a directory D and
   must print `command -v claude, in a pane: D/bin/claude` and an empty agent
   list; if it does not, stop. Run every command of the test as
   `D/e <command>` (`D/e yardr ...`, `D/e herdr ...`), never bare: `D/e` is
   `env -i`, so your session's HERDR_SOCKET_PATH and YARDR_* cannot reach the
   real herdr or yard. Agents are kind `claude` only: `D/bin/claude` is a
   stand-in, and D/agents.log shows what each was started with. Afterwards
   `contrib/isolated-yard/isolated-yard down D` stops everything by pid and
   removes D. Never pkill -f.
5. Commit on your branch. Do not merge. Do not push unless your brief says
   you may (it never does for a bead of this rig: the refinery lands your
   branch from your worktree). Never run `go install`
   or otherwise install yardr from a worktree: the running server and daemon
   replace themselves with what is installed, so the mayor installs after
   merge. Test with `go build -o` into a temp dir, or `go test`.
6. Finish as the stage asks. Your brief's "Stage" section says what must be
   true before the bead leaves the stage and when each outcome applies, and
   lists the outcomes with their commands; the flow picks the next stage.

If you are stuck and the stage offers no outcome for it:
`yardr bead note <id> "..."`, then `yardr bead hold <id>`; a hold reaches the
mayor's inbox.

## Something outside your bead

1. It does not block you (a bug elsewhere, a stale doc, a flaky test): file
   it and carry on, never fix it on your branch. `yardr bead create "<title>"
   --rig yardr --stage backlog -b - --discovered-from <your bead>`, with what
   you saw, where, and how to reproduce. The new bead names the bead it was
   found from. The mayor shapes it from backlog.
2. It blocks you but is no question about the bead (the gate is red on
   main, a tool is missing, the base moved under you): file it as above,
   then mail the mayor: `yardr mail yardmaster "<bead id>: <one line>"`. If
   you cannot finish without it, `yardr bead note <id> "blocked by <bead
   id>" && yardr bead hold <id>`; the mayor orders the two with
   `yardr dep add`.
3. It is a question about the bead itself (an unclear goal, a design
   choice): that is for the stage's outcomes, not for a new bead. Your brief
   says which outcome takes a question, or hold as above.
