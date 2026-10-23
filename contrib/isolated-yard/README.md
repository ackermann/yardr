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
so nothing else of the calling session (no `HERDR_SOCKET_PATH`, no `YARDR_*`
but the two below) reaches a command run through it. Those two are
`YARDR_TEST_OWNER` and `YARDR_TEST_HOME_CHECK`, with which a Go test binary
names itself to the servers it starts (`storetest.NameTestBinary`): `D/e`
carries each that the environment making the yard has, so the yard's server
and daemon stop within a few seconds of that binary's end, however it ended (a
kill or a timeout runs no cleanup). A yard made by hand has neither, and its
`D/e` carries neither. The herdr server of an `up` yard and the stand-in
agents in its panes do not look for the owner; the yard's watchdog does (see
"When the test binary is killed", below).
`D/h/.profile` sets `PATH` to the same directories, so the
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
screen (Claude Code's menu of login methods: `Select login method:` with the
selected option carrying `❯`, then `Paste code here if prompted>`; no prompt
box) and takes what it is sent without showing it, holding its first prompt
until it is ready; removing the file is the login, and the prompt box and
held prompt come. With a file
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
user outside the yard (`yardr user set <name> notify=<source>`), as a JSON
body or as a multipart form with a picture for its `attachment` part (at
most 5,242,880 bytes, with a file name and a content type, as the service
requires): `GET /messages` lists what it took, a picture by its name, type,
size and SHA-256, and `POST /messages/answer` with `status=500` makes it
fail.
With `-bot-dir D/bot` it is the Bot API of Telegram too, for a declared
source whose file has `base: http://A` and takes its credential in the
path (`packs/telegram`, with that one line changed). It takes `sendMessage`
as JSON and `sendPhoto` and `sendDocument` as a multipart form, under the
token of `-bot-token`, and answers `getWebhookInfo`, `setWebhook` and
`deleteWebhook` without keeping a webhook. It keeps every request, taken or
refused, and the bytes of every file in `D/bot`, a refused picture's too.
`curl http://A/requests` lists the requests as JSON (method, chat, text or
caption; for a file its name, type, size and SHA-256; for a picture its
width and height; and the status each was answered with and, for a
refusal, the service's words), and `curl http://A/requests/<n>/file`
returns the bytes of the nth as they came, to compare with the file that
was sent. A photo is held to the rule that
[sendPhoto](https://core.telegram.org/bots/api#sendphoto) states (read 22
October 2026): at most 10 MB, width and height at most 10000 in total, a
ratio of at most 20. A photo over one of them is a 400 with the service's
words, `file is too big` or `PHOTO_INVALID_DIMENSIONS`, and a file that is
no picture is `IMAGE_PROCESS_FAILED`. Nothing else is checked: not the 4096
characters of a text, the 1024 of a caption or the 50 MB of a document, and
`getFile` is not served, so an inbound file is not rehearsed.
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

## Rehearsing the Telegram pack

A builder whose change reaches Telegram, in a tell, in `yardr reply --file`
or in the pack's scripts, cannot make a live receipt. A bead's session may
not run `yardr reply`, the installed yardr is the last landing's, and the
operator's own tells may not go through Telegram at all. `rehearse-telegram`
is the rehearsal in its place: the real binary, daemon and pack scripts in a
bare yard, against the stand-in above. From any directory of the checkout:

```
contrib/isolated-yard/rehearse-telegram
```

It builds yardr from the checkout, so the `yardr version` it prints names
the commit rehearsed (`-modified` when the tree has changes not committed).
It starts the stand-in and the yard's hook listener on free loopback ports,
applies `packs/telegram` with its `base:` line pointed at the stand-in (the
one line that differs, which the script checks), and sets the operator `ada`
up to be told through it. Then it runs these, with the five pictures that
`pictures/` makes (PNGs, each its own, one of them 1280x9000):

- A delivery from the operator's chat reaches the yardmaster's inbox.
- C1, `yardr reply <seq> "..." --file reply.png`, goes by `sendPhoto` with
  the words as its caption.
- C2, a reply of 1025 characters with two pictures and a document, goes as
  the text first, whole, by `sendMessage`, then each file without a caption,
  in order, the document under its own name (`shed;log, v2.txt`) and type.
- C3, a reply file whose name holds a line feed, is refused before anything
  is stored or sent.
- T1, `yardr mail ada "..." --file mail.png`, is told by the daemon with the
  picture.
- T2, a bead closed by a builder with a screenshot on the second of its
  three notes, is told with that screenshot.
- T3, a mail with a 1280x9000 picture, which the stand-in refuses as the
  service does, is told as the same words alone, and the daemon logs `user
  told without the picture`.

It prints each request the stand-in took and, for a file, the file of the run
it equals, with the same SHA-256 and the same byte count. At the end it
fetches each upload back from the stand-in and compares its bytes with the
file using `cmp`. A check that fails prints `FAIL` and the request, the run
goes on, and the script exits 1 at the end. A command that fails stops the
run, with the daemon's last lines and what the stand-in held by then. A run
takes about 90 seconds, since each of the three tells waits out its 20
second grace. `--keep` leaves the yard and the stand-in up and says how to
end them.

It needs `go`, `curl`, `jq`, `file`, `perl` (for a free port) and `shasum` or
`sha256sum`. It is no live receipt. The stand-in is not Telegram, and the
real-API smoke test stays the operator's
([the design](../../docs/design/2026-10-13-yardr-c55e-telegram-hears.md)).

### Traps

Each of these cost a run when the rehearsal was first made by hand.

- Under `D/e` there is no `USER`, so a command acts as the login name, and
  the operator's own acts are refused (`only the operator (ada) may change a
  setting; this is mckean`). Give every operator act `--actor <operator>`.
- `yardr reply` calls the source with the credential in its own environment
  and is refused with none (`TELEGRAM_BOT_TOKEN is not set in this
  environment`). A crew member's pane has it from its env file. The
  rehearsal runs `D/e env TELEGRAM_BOT_TOKEN=<token> yardr --actor ada reply
  <seq> "..." --crew yardmaster`.
- The hook listener needs an address, and the server reads `hook_addr` only
  when it starts. The script takes a free port first (perl asks the kernel
  for one), sets it, stops the server, lets `daemon start` start it again,
  and waits for a delivery with no secret to be answered 401. Never 8790 or
  8791, which are the real yard's.
- A user's own acts are never told to them. The mail comes from the
  yardmaster, and T2's bead is closed by `yd-builder`, because a bead closed
  by `ada` would tell her nothing.
- A tell comes about 20 seconds after what caused it (`store.PushGrace`, a
  constant). The script polls the stand-in's list and the daemon's log for
  the effect, each poll with a fuse against a hang, and does not sleep for a
  set time and hope.
- A bare yard has no herdr, so the daemon logs that the yardmaster failed to
  start, every 30 seconds. That is not a failure of the rehearsal.
- curl sends a double quote in a file name as `%22`, and the stand-in lists a
  name as it arrived. The document's name holds `;` and `,`, which curl's
  `-F` read as syntax until the pack quoted them, and no quote.

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
homes that hold `.`, `[`, `*`, `+` and spaces. The sleepers it starts as
processes of other yards end with the test binary that named them too.

## When the test binary is killed

A Go test binary that is killed, or timed out, runs none of its cleanups, so
nothing calls `down`. The servers and daemons of its yards stop themselves
(they look for the binary that named them). The herdr server and the stand-in
agents do not, so `up` and `bare` start a watchdog for each yard
(`isolated-yard watchdog D`, its output in `D/watchdog.log`), through `D/e`,
which hands it the name. Once a second it asks `D/bin/alive` (`alive/main.go`),
which goes by the pid and the start time, as the servers do. When the binary
is gone it runs `down D`: everything in the yard stops and D is removed, a few
seconds after the end. Only status 1 of `alive` (gone) and its status 2
(`YARDR_TEST_OWNER` missing or malformed: a handoff that broke takes the yard
down, where it would otherwise leak) do that. Any other status (a signal, a
failed start) says nothing about the binary and is looked at again a second
later. A yard made by hand has no owner: its watchdog says so in its log and
ends at once.

The watchdog is a process of the yard like the stand-ins: a normal `down`
stops it with the rest, and it ends by itself if D is removed by other means.
macOS's `ps` shows no environment for the system's own binaries, a `/bin/sh`
among them, so `down` cannot find the shell by the yard's home as it finds the
stand-ins. The watchdog is started with `D/bin/alive` as its stdin instead,
and `lsof` finds what has a file of `D/bin` open (the herdr server and the
stand-in agents are found so too). The `down` it runs gets `/dev/null` as its
stdin: with the watchdog's, `down` and every program it starts would hold that
file open, and `pids` would list them as the yard's own.

`watchdog_test.go` tests the watchdog with owners that have ended, and with an
`alive` that answers as told: nothing in it kills a test binary.
