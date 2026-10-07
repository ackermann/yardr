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
`XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `YARDR_HOME` under D, so nothing of
the calling session (no `HERDR_SOCKET_PATH`, no `YARDR_*`) reaches a command
run through it. One thing of the machine it does share, on purpose: the
gate slots (`yardr depot check --help`). A `D/e yardr depot check` runs on the
same cores as every other gate, so it takes one of the same slots, which
yardr finds by the user database's home, not by `HOME`. Use agent kind `claude` only: `D/agents.log` shows what each
stand-in was started with. Started as `claude -p <prompt>` (what `yardr
doctor --try` and the onboarding's "Check that claude answers?" run) the
stand-in prints `ok` and exits; with a file
`D/print-fails` it prints that file's text on stderr and exits 1 instead.
With a file `D/login-needed` a stand-in started in a pane sits at a login
screen (`Not logged in · Run /login`, no prompt box) and takes what it is
sent without showing it, holding its first prompt until it is ready; removing
the file is the login, and the prompt box and held prompt come. With a file
`D/answer` a stand-in says that file's text (as `⏺ <line>`) to every prompt
it takes and waits for input again, as a harness does that stops on an error
of its own (`API Error: Repeated 529 Overloaded errors`). The box
leaves the cursor after its `❯`, so text sent to the pane and not yet
submitted shows on that line, where yardr reads an agent's input. A stand-in
whose first prompt asks it to greet a user by mail, as the yardmaster's does,
sends that mail. The script's header says why each of these holds.

`isolated-yard bare` makes the same yard without a herdr server and prints D
alone: for what starts no agent (a server, a daemon, the CLI), and where
herdr is not installed. Nothing in it can open a pane. It ends with `down`
too.

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
token it takes (`crm-standin`).

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
