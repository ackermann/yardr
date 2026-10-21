# contrib/isolated-yard

A throwaway yard with its own herdr server, for testing anything that starts
agents without touching your own herdr session or yard. Run it from the
worktree under test:

```
contrib/isolated-yard/isolated-yard up        # builds it, proves it, prints its directory D
D/e yardr init --harness claude --start       # every command of the test goes through D/e
D/e yardr bead list
contrib/isolated-yard/isolated-yard down D    # stops what runs in it, removes D
```

`up` builds yardr from the current directory into `D/bin`, with a stand-in
`claude` (`fake/`) and a copy of herdr, and starts a herdr server of its
own. It then checks, in a pane of that herdr, that `command -v claude` is
`D/bin/claude` and that the agent list is empty, and prints both; if either
fails it tears the yard down and exits 1. `D/e` is `env -i` with `HOME`,
`XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `YARDR_HOME` under D, and
`__CF_USER_TEXT_ENCODING` from the caller's environment (without it a fresh
binary's first run under the codex sandbox stalls in dyld, before main),
so nothing else of the calling session (no `HERDR_SOCKET_PATH`, no `YARDR_*`)
reaches a command run through it. `D/h/.profile` sets `PATH` to the same directories, so the
yard's login shell, which yardr asks for a program its PATH lacks (`yardr
doctor`, and `yardr up` for herdr), finds nothing of the machine's own: a
trial of that lookup writes the file itself, with directories that hold no
real herdr or agent. Use agent kind `claude` only: `D/agents.log` shows what each
stand-in was started with. Started as `claude -p <prompt>` (what `yardr
doctor --try` and the onboarding's "Check that claude answers?" run) the
stand-in prints `ok` and exits; with a file
`D/print-fails` it prints that file's text on stderr and exits 1 instead.
Started as `claude auth login` (what `yardr harness login claude --json`
plays) it prints what Claude Code's login command prints on pipes, with an
address of no host, and reads one line: `good#code` is the login, which
removes `D/login-needed` and `D/print-fails`, and any other line ends it
with `Login failed: Request failed with status code 400` and status 1.
With a file `D/login-needed` a stand-in started in a pane sits at a login
screen (`Not logged in · Run /login`, no prompt box) and takes what it is
sent without showing it, holding its first prompt until it is ready; removing
the file is the login, and the prompt box and held prompt come. With a file
`D/answer` a stand-in says that file's text (as `⏺ <line>`) to every prompt
it takes and waits for input again, as a harness does that stops on an error
of its own (`API Error: Repeated 529 Overloaded errors`). With a file
`D/busy` a stand-in that takes a prompt stays in that turn until the file is
removed: it shows the line Claude Code draws while it works and no prompt
box, so herdr says `working` of it. The box
leaves the cursor after its `❯`, so text sent to the pane and not yet
submitted shows on that line, where yardr reads an agent's input. A stand-in
whose first prompt asks it to greet a user by mail, as the yardmaster's does,
sends that mail. The script's header says why each of these holds.

The stand-in runs no command a prompt asks for, and its pane has no shell to
type one into. To run a command from an agent's own pane, as the session
yardr started there (the store tells such a caller by the pane's shell),
write it to `D/run` before the session starts. A stand-in runs each line of
that file (`sh -c`, with its own environment and directory, no input) when
it takes its first prompt, and appends the line, its output and its exit
status to `D/run.out`, each marked `pid=<the stand-in's> session=<its
YARDR_SESSION>`; then it draws its box and goes on as before. Every stand-in
that takes a first prompt while the file is there runs it, the yardmaster's
too: write it once those you do not mean have started, and remove it after.
No file, nothing runs.

`isolated-yard bare` makes the same yard without a herdr server and prints D
alone: for what starts no agent (a server, a daemon, the CLI), and where
herdr is not installed. Nothing in it can open a pane: `yardr up` there
finds no herdr, on PATH or with the login shell, and says so. It ends with
`down` too.

`sources/` is a stand-in for the Linear and GitHub APIs, for testing packs
and hooks in the yard. With `-github-app-key D/app.pem` it is a GitHub App
too: it writes a throwaway private key there and gives installation tokens
for the repositories of `-github-app-repos`, so a crew member whose env_file
has `GITHUB_APP_ID=12345` and `GITHUB_APP_PRIVATE_KEY_FILE=D/app.pem`
answers as `yardr[bot]`. It also takes messages for a person at
`/1/messages.json`, as Pushover does, for a declared source that tells a
user outside the yard (`yardr user set <name> notify=<source>`): `GET
/messages` lists what it took, and `POST /messages/answer` with `status=500`
makes it fail.
And it is a customer system under `/crm/`, for a declared source whose calls
a session makes with `yardr call`: `sources/crm.yaml` is its declaration
(replace `BASE` by the stand-in's address), and `-crm-token` the bearer
token it takes (`crm-standin`). It takes a note on a customer too, an
update call (`POST /crm/customers/<id>/notes`), with the token of
`-crm-update-token` (`crm-update-standin`) and once for each
`Idempotency-Key`. `curl http://A/notes` lists the notes it wrote and how
many requests it got for one; `curl -X POST http://A/notes/hold` has it
write the next note and hold the answer until the caller gives up, which is
how a trial gets a timeout after a write.

## The yard-shape matrix

`matrix_test.go` walks every way a yard is set up through the same list of
steps, so that proving the default setup does not stand in for the others. A
shape is a row of its table. The first three are a yard with a git depot, one
with a directory depot, and one set up as a server by
[contrib/server-yard](../server-yard/README.md), whose daemon and web view
are login services kept by the script's stand-in service managers. Each shape
is a yard of its own, made by `up`, and every step runs through its `D/e`
(for the server yard, `D/e env ...` with the variables the managers read).

The steps, in order: init; doctor; depot add; depot init, where the pack of
the other kind of depot is refused; a bead created, listed and shown; the bead
worked to its end by the stand-in agents, a builder and a reviewer that do
what `D/run` tells them; land; status, and doctor with no setup step left;
the daemon stopped, started (a second start is refused) and stopped again,
then stopped once more with none running.

A shape that cannot do a step does not skip it. Its table entry holds the
refusal, and the matrix runs the attempt and pins the exit status and the
start of the error. The exit status says what kind of refusal it is: 1 for a
command refused, 2 for one that had to ask and had no terminal to ask in, 3
for something not there (`exitCode` in `internal/cli/root.go`). A dir depot
cannot land. Its flow ends at review, with no stage to hand a bead to, so
that is its entry for `land`. A new shape is a new row, and each step it
cannot do is one more entry.

A shape takes 35 to 50 s and the three run beside one another. The matrix
skips unless `YARDR_QUIET_RUN` is set, which the daemon's quiet run sets for
the whole suite (docs/testing.md R6) and nothing else does. By hand, for all
shapes or for one:

```
YARDR_QUIET_RUN=1 go test -run TestYardShapes ./contrib/isolated-yard
YARDR_QUIET_RUN=1 go test -run TestYardShapes/dir ./contrib/isolated-yard
```

It needs herdr, as `up` does, and with the variable set it is red without it.

## What `down` stops

`down D` stops a yard by its home, not by its binary. It stops every process
whose `YARDR_HOME` is D or inside it, whichever yardr it runs and whoever
started it, together with every process running a file of `D/bin` (the
herdr server, the stand-in agents):

1. the herdr server, by its own `server stop`;
2. the daemon, then the server, by the pid each left in its home
   (`daemon.lock`, `server.pid`), so the daemon ends its pass with its
   server still there. A pid from a file is signalled only when it is also
   one of the yard's: a file left by a process that died may name someone
   else's;
3. everything still found, with TERM, then KILL after 5 seconds;
4. it removes D, looks once more for a process whose home is in it, and
   exits 1 naming those it finds. If something survived KILL, D is kept and
   the exit status is 1 as well.

Two cases an earlier `down`, which went by the files in `D/bin` alone, left
running while it printed "stopped and removed":

- **`D/bin/yardr` rebuilt during the test** (`go build -o D/bin/yardr .`, or
  a new file moved over it). The running server and daemon then run a file
  `D/bin` no longer names.
- **a server started by another yardr** with `YARDR_HOME` in D, such as the
  installed one. It runs no file of `D/bin` at all.

Rebuilding `D/bin/yardr` is fine, then: the server and the daemon replace
themselves with the new build within about 10 seconds, as they do after an
install, and `down` finds them either way.

`down` refuses a directory `up` (or `bare`) did not make. D must be
`/tmp/iy.*` and carry the mark `up` left in it, `D/.isolated-yard` with D's
own path; otherwise nothing is stopped or removed and the exit status is 2.
A real yard has neither the name nor the mark, and no process of it has a
home under `/tmp/iy.*`.

The processes are found by the environment they were started with:
`ps axeww` on both Linux and macOS. Neither shows another user's processes,
which an isolated yard does not have. macOS's `ps` also shows no environment
for the system's own binaries (`/bin/sleep`, `/bin/sh`): one of those is
found by its home only on Linux.

The home is compared as a string, the same way on both: `YARDR_HOME` equal
to D or a path inside it. It is never made into a pattern, and `down` stops
nothing when it cannot tell: for a home that is empty, `/` or no isolated
yard, or when the processes cannot be listed, it says so, exits 1 and
leaves D. An earlier `down` escaped the home into a regular expression with
a `sed` expression GNU sed refuses; on Linux the pattern came out empty and
`down` signalled every process that had any `YARDR_HOME`.

`isolated-yard pids D` prints the pids `down D` would stop and stops none.

A server or daemon that `down` does not get to (the yard removed by hand, a
temp home of your own) stops by itself within a minute of its home going
away: see "yardr's own state" in
[docs/configuration.md](../../docs/configuration.md#yardrs-own-state).

`down_test.go` tests the two cases and the refusals with bare yards, that
processes of other yards outlive a `down`, and the comparison itself
(`isolated-yard match <home>`, which reads `<pid> <environment>` lines) with
homes that hold `.`, `[`, `*`, `+` and spaces.
