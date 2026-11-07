The depot's test command is `sh .yardr/test`, run in the worktree after the
last commit. It runs gofmt, shellcheck, go vet, go build, then go test on
the packages changed since the base (`YARDR_BASE`, which your session has;
its first line says what it compared with), every package that imports one,
and the packages its table says read a changed file by path. The checks
before the tests cover the whole tree, whatever you changed; shellcheck's
files are `.yardr/test`, `install.sh` and every `contrib` script with a
shell shebang, as CI's scan job has them, and a machine without it fails the
run with the line that says how to install it. It runs the tests without
-race, whatever you changed: the race
detector runs at landing only (docs/testing.md R4), and the first line says
so. A run whose step covers the whole suite still queues on the race lock,
as class 1 behind landings; a smaller selection takes no ticket. The landing run (`YARDR_LANDING=1 sh .yardr/test` on a code change: the
selection plus the six black-box suites, all under -race, vet for linux too,
and the store, dispatch and bdp suites again through the unix-socket client
when the selection holds one of them) is the assembly's: it runs once, on
exactly what lands, and a red one comes back to you. The whole suite (every
package) runs on a quiet machine, not at each landing. A landing that changes
nothing compiled (no .go file, no embedded file) is the plain run
there too; its first line says which it is, as it does for a landing
whose change cannot be selected (go.mod or go.sum, a deleted or moved file, a
file the table has no row for), which is still the whole suite. Run the landing run yourself only when the change is of a kind the plain
run cannot see: cross-package behaviour or tests in packages it skips, a data
race (only the landing run has the race detector, and it queues for the race
lock: ask it of a change to concurrent code, not of every change), a test that reads a file by path
which the table in `.yardr/test` has no row for (add the row too), code a
test reaches by building the binary instead of importing it, or store,
dispatch or bdp behaviour over the socket. The assembly runs `.yardr/test`
as it is on main, so an edit to it on the branch is tested by your run
alone. The run ends with its test line, printed by the script itself: `test: sh
.yardr/test @ <commit>: pass, <n> packages ok, 0 failed, 112 s, waited 40
s` -- the result, the packages `ok` and `FAIL`, the wall seconds, and the
seconds queued on the race lock (`waited 0 s` for your run, unless its step covered the whole suite).
A note carries it whole.

A burn inside a builder's or reviewer's session runs at `-x 1` and names one
suspect test (`contrib/burn/burn -x 1 PACKAGE TEST`, the proof docs/testing.md
R2 asks of a fix for a test that fails only under load), never at the default
factor of 5. A burn's loops are niced, so they yield the cpus, but they still
raise the load average, and every builder route waits on `machine.load<80`
(`yardr route list`): a burn at the default factor has held the load at 100 to
200, and while it ran no builder started anywhere and every other gate and
landing ran on a loaded machine. A burn at any higher factor, the default
included, runs outside a session, when the line is idle.

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

A bead labelled `eyes` (its change shows: a page, a window, a view) reads the yardr design system first, before it draws anything: `readme.md` in the repository yardr-dev/yardr-design, cloned beside this one on this yard at /Users/mckean/projects/repos/yardr-design. Its tokens, type, marks and the railway vocabulary rule are the brand; copy tokens from `tokens/`, never invent colours or words. Its builder and reviewer write every screenshot inside the worktree's own git dir, `$(git rev-parse --git-path yardr)/shot.png`, and read it from there (a reviewer copies the note's picture there first): that directory is never committed and is inside the working directory, so the harness never stops to ask leave to read a file outside it, a prompt no one on the yard can answer. Never shoot into /tmp or $HOME.

A change that moves or adds a package updates AGENTS.md in the same commit.
