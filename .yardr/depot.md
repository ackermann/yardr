The depot's test command is `sh .yardr/test`, run in the worktree after the
last commit. It runs gofmt, go vet, go build, then go test -race on the
packages changed since the base (`YARDR_BASE`, which your session has; its
first line says what it compared with), every package that imports one, and
the packages its table says read a changed file by path. The whole suite
(`YARDR_LANDING=1 sh .yardr/test`: every package, vet for linux too, and
the store, dispatch and bdp suites again through the unix-socket client) is
the assembly's: it runs once, on exactly what lands, and a red one comes
back to you. A landing that changes nothing compiled (no .go file, go.mod
or go.sum, no embedded file, no file the table has no row for, nothing
deleted) is the plain run there too; its first line says which it is. Run it yourself only when the change is of a kind the plain
run cannot see: cross-package behaviour or tests in packages it skips, race
coverage outside the packages it selects, a test that reads a file by path
which the table in `.yardr/test` has no row for (add the row too), code a
test reaches by building the binary instead of importing it, or store,
dispatch or bdp behaviour over the socket. The assembly runs `.yardr/test`
as it is on main, so an edit to it on the branch is tested by your run
alone. The test line counts the packages go test printed `ok` and `FAIL`
for: `test: sh .yardr/test @ <commit>: pass, <n> packages ok, 0 failed`.

Anything that starts agents is tested in an isolated yard, never the
operator's herdr session or yard. From the worktree:
`contrib/isolated-yard/isolated-yard up` builds one in a directory D and
must print `command -v claude, in a pane: D/bin/claude` and an empty agent
list; if it does not, stop. Run every command of the test as
`D/e <command>` (`D/e yardr ...`, `D/e herdr ...`), never bare: `D/e` is
`env -i`, so your session's HERDR_SOCKET_PATH and YARDR_* cannot reach the
real herdr or yard. Agents are kind `claude` only: `D/bin/claude` is a
stand-in, and D/agents.log shows what each was started with. `D/e`
isolates environment and sockets, not TCP ports: a server a trial starts
takes a free loopback port (127.0.0.1:0), never a fixed one. 8791 is
yardr web's, and 8790 is the hook listener's suggested port. A listener
on 127.0.0.1 stands in front of the real one on all addresses. Afterwards
`contrib/isolated-yard/isolated-yard down D` stops everything by pid and
removes D. Never pkill -f.

Builders do not push: for a bead of this depot the brief never says you
may, and the assembly lands the branch from the worktree. Never run
`go install` or otherwise install yardr from a worktree: the running
server and daemon replace themselves with what is installed, so the
yardmaster installs after merge. Test with `go build -o` into a temp dir,
or `go test`.

A bead labelled `eyes` (its change shows: a page, a window, a view) reads the yardr design system first, before it draws anything: `readme.md` in the repository yardr-dev/yardr-design, cloned beside this one on this yard at /Users/mckean/projects/repos/yardr-design. Its tokens, type, marks and the railway vocabulary rule are the brand; copy tokens from `tokens/`, never invent colours or words.
