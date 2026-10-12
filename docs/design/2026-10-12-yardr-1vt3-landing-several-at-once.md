# Landing several beads without testing them one after another

Design yardr-1vt3, proposed 2026-10-12. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean, by mail on 12 Oct 2026 at 21:44, after the yardmaster said the
landing queue is where work waits: "maybe we could brainstorm on a way to be
able to test multiple at once". The bead puts it so: how do several approved
beads reach the base without each waiting for the whole suite of every bead
before it, while the base still never holds a commit that was not tested as
it landed?

The short answer of this document: on 12 Oct the queue was seldom deep, and
the runs were long. Of 66 landings in depot yardr, 57 started with no other
yardr bead waiting. What made the evening slow was that each run took six to
eight minutes on a loaded machine, and that eleven of the runs were the whole
suite for one Markdown file. So the proposal shortens the run first, and
keeps testing several beads at once as a later step, designed here so that it
can be weighed.

mckean said the same of the bead's five ideas while this was being drafted
(mail 19016, 12 Oct 21:49, noted on the bead): "I was thinking about 2 but
that won't help if there's not much (I just find it smart, but maybe tricky
to cleanly handle). So as you said, 1 and 5 first." Idea 1 is that a landing
tests what the branch touched, idea 5 a faster whole suite, and idea 2
several beads tested as one. Sections 3.1 and 3.2 are ideas 1 and 5, and 3.3
is idea 2 with what happens when it is red.

## 2. What exists today

### The assembly

The group `yardr-refinery` (`yardr group list`) is an `exec` group with limit
1. Its `cmd` is `YARDR_PUSH=origin YARDR_NOTIFY=yardmaster
~/.yardr/scripts/merge-local`, and it logs to `~/.yardr/logs/refinery.log`.
The routes (`yardr route list`) send the stage `approved` of every depot
(yardr, footplate, signalbox, aiquokka) to this one group. So limit 1 means
one landing at a time in the whole yard, not one per depot: a footplate bead
waits behind a yardr bead although they share no branch.
[docs/line.md](../line.md), "One whole-suite run per bead", says this is
meant: "How many whole suites run at once is the assembly group's `limit`:
every depot of this yard lands through one group".

[`contrib/assembly/merge-local`](../../contrib/assembly/merge-local) is
started by the exec runner once per bead, with the bead in its environment
(`YARDR_BEAD`, `YARDR_DEPOT`, `YARDR_BASE`, `YARDR_BRANCH`, `YARDR_WORKTREE`,
`YARDR_TEST`, `YARDR_TYPE`, `YARDR_TRAIN`; the list is in
`internal/runner/exec.go`). For that one bead it:

1. checks that the base and the branch exist and that the worktree is clean
   and on the branch, and holds the bead otherwise;
2. rebases `yard/<bead>` onto the base when the base moved; a conflict goes
   to `merge`, or back to the builder in a flow with no `merge`;
3. runs the depot's test command in the worktree with `YARDR_LANDING=1`,
   bounded by `YARDR_TEST_TIMEOUT` (1800 seconds). Where the command is the
   path of a file the base has, the base's copy runs. Exit 0 is green. Exit 2
   holds the bead. A run ended by a signal is run once more. Anything else is
   red and reports the outcome `failed` with the last 200 lines of output. A
   depot with no test command lands untested, and the note says so;
4. checks that the branch did not move during the run, then fast-forwards the
   base to the commit the tests ran on, by its sha, never forced;
5. pushes the base when `YARDR_PUSH` is set, and advances the bead to
   `merged`. Advancing ends the session and the daemon stops the script, so
   it is the last thing the script does.

The script prints one time, when it starts. It prints none for the rebase,
the tests or the push.

### The depot's test command

Depot yardr's is [`.yardr/test`](../../.yardr/test). It has two forms.

Run plainly, as a builder and a reviewer run it, it runs `gofmt -l`, `go vet
./...` and `go build ./...` over everything, and then `go test -race` on a
selection: the packages with a changed `.go` or embedded file, every package
that imports one of them, and the packages that a table in the script (the
variable `reads`) says read a changed file by path. "Changed" is against
`YARDR_BASE`. A changed file that is not Go, not embedded and in no row of
the table runs every package. A changed `go.mod` or `go.sum` does too.

With `YARDR_LANDING=1` it ignores what changed. It runs `gofmt -l`, `go vet
./...`, `GOOS=linux go vet ./...`, `go test -race ./...`, and then
`YARDR_TEST_RPC=1 go test ./internal/store/ ./internal/dispatch/
./internal/bdp/`, the three suites again through the unix-socket client. The
steps run one after another. The script prints no time for any of them.

footplate's test command is its own `.yardr/test`, which runs `cargo test`
for one crate from a warmed build cache. aiquokka's is the command `go vet
./... && go test ./...`. Neither reads `YARDR_LANDING`.

### Trains and wagons

A train ([docs/line.md](../line.md), "Trains") is a bead of type `train`
whose children land on its branch `yard/<train>`. A wagon is a child of type
`wagon`: merge-local tests it without `YARDR_LANDING` and it has no review of
its own. When the last child has landed,
[`contrib/assembly/integrate-train`](../../contrib/assembly/integrate-train)
rebases the train's branch onto the base and runs the whole suite on it once.
The train is then reviewed as a whole and lands through merge-local. A red
run adds a child, "Fix the tests on train <id>", which blocks the train.

This is the yard's existing way to pay for one whole suite for several
beads. It is decided before the work is built: a wagon is branched from the
train's branch, is of a type with its own flow, and is not reviewed alone.

### What the landings of 12 Oct took

The refinery's log holds 81 starts on 12 Oct up to 21:42: 68 for depot yardr
and 13 for footplate. Of the 68, one was held at once for a worktree with
uncommitted changes and one was still running; the other 66 are counted
below. Since the log has no end times,
the length of each landing below is from the bead's history (`yardr bead
history <id>`): from "claimed by yardr-refinery" to the move out of
`approved`. The wait is from the move into `approved` to that claim. The
package times are the ones `go test` printed in the log.

| | landings | shortest | median | longest |
|---|---|---|---|---|
| depot yardr, whole day | 66 (64 green, 2 red) | 168 s | 226 s | 508 s |
| depot yardr, from 17:00 | 28 | 185 s | 364 s | 508 s |
| depot footplate | 13 | 10 s | 17 s | 54 s |

The 66 yardr landings took 298 minutes together.

What a landing is made of cannot be read from the log step by step, since no
step prints its time. It can be bounded, because `go test` runs packages side
by side and prints each package's time: the first test step takes at least as
long as its slowest package, and the socket step as long as its slowest.

| | whole landing | slowest package, first run | slowest package, socket run | everything else |
|---|---|---|---|---|
| yardr-h8d1, 01:18, quiet machine | 172 s | `internal/cli` 102 s | `internal/dispatch` 51 s | 19 s |
| yardr-nx54, 21:35, loaded machine | 355 s | `internal/cli` 184 s | `internal/dispatch` 60 s | 111 s |

"Everything else" is the rebase, gofmt, both runs of vet, compiling the test
binaries, the push and the advance. It is a remainder, not a measurement.

The packages that set the length, quiet and loaded (the same two landings):
`internal/cli` 102 and 184 s, `internal/runner` 92 and 146 s,
`internal/dispatch` 86 and 161 s, `internal/store` 68 and 110 s,
`internal/hooks` 43 and 102 s, `contrib/assembly` 37 and 63 s. Over the
socket: `internal/dispatch` 51 and 60 s, `internal/store` 33 and 42 s. At the
worst of the evening (19:18) `internal/runner` took 391 s, `internal/cli`
354 s and `internal/dispatch` 294 s. None of these five packages calls
`t.Parallel()` in any test: each runs its tests one after another, 541 in
`internal/cli`, 498 in `internal/store`, 278 in `internal/dispatch`, 121 in
`internal/runner`. The packages already run side by side, so a landing is as
long as `internal/cli` alone, and then as long as `internal/dispatch` over
the socket. No one package is half of a landing: over the day's 64 green
landings the medians were `internal/cli` 126 s, `internal/runner` 102 s,
`internal/dispatch` 102 s and `internal/store` 76 s, side by side, in a
landing of 226 s. All package times of one landing add up to 823 s at the
median, between 608 and 1464 s: that is the work the ten cores do.

Four more things the day shows:

- **The queue was seldom deep.** When a yardr landing started, no other yardr
  bead was waiting 57 times of 66, one was waiting 6 times, two twice, three
  once. 19 of the 66 waited more than a minute. The longest waits were 1020 s
  (yardr-6bha, 19:46), 975 s and 945 s. All waiting together was 122 minutes.
- **Eleven runs were for one Markdown file.** Ten landings and one red run
  were a branch whose only change was one file under `docs/design/`
  (yardr-8y0f, h039, evd6, 8wxe, r6jh, xpf8, 0qs5 twice, t5ts, nxah, nx54).
  They took 3954 s, 66 minutes, 22% of the day's landing time. A plain run of
  the script on such a branch tests one package, the repository's root, whose
  test follows the README's links into `docs/`; that package took 25 to 58 s.
- **Both red runs were the machine, not the branch.** yardr-0qs5 (19:18, a
  design document) and yardr-1jn0 (19:31) failed on
  `TestEnvCommandRefusedBlocksStart`, "no answer within 3s", in runs where
  `internal/runner` took 391 and 279 s. Each cost its run (487 and 461 s), a
  round back to its builder, and a second run. The test was changed at 20:00
  by yardr-gdes.
- **footplate waits for yardr.** Five of its 13 landings waited more than a
  minute, the longest 366 s for a landing of 42 s.

## 3. The proposal

Three steps, in the order mckean gave: idea 1, then idea 5, then idea 2. The
first is a change to one script. The second is a measurement and what follows
from it. The third is the answer to "test multiple at once", written down so
that it can be built when the queue calls for it (open question 2).

### 3.1 A landing that changes nothing compiled is tested for what it changed

This is the depot's to decide, in its test script. In `.yardr/test`, a run
with `YARDR_LANDING=1` first asks what changed since `YARDR_BASE`, as a plain
run does. merge-local has just rebased the branch onto the base, so that is
exactly what the landing adds. When no changed file is a `.go` file, `go.mod`
or `go.sum`, a file some package embeds, or a file the table has no row for,
the run is the plain one: gofmt, vet, build, and the tests of the packages
the table names for the changed files. Every other landing is the whole
suite, as today.

What "tested as it landed" means for such a landing: every program and every
test binary is built from the same source as on the base, and the base
passed the whole suite when its last change to that source landed. The only
tests whose result can differ are those that open a changed file by path,
and those ran. The table that names them is the one a builder's run already
relies on, and it fails toward more testing: a file it does not know runs
everything.

What a person sees in the refinery's log and in the bead's landing note:

```text
== the tests on yard/yardr-nx54 at 6a07178: the base's copy of .yardr/test, with YARDR_LANDING=1
test: YARDR_LANDING is set, and nothing compiled changed since main (YARDR_BASE), from 51c8a9e
test: docs/design/2026-10-12-yardr-nx54-outside-pr-review.md changed: read by the tests of .
test: testing, with -race, what changed, what imports it and what reads it: yardr.dev/yardr
ok  	yardr.dev/yardr	31.4s
```

When it goes wrong, it is a row of the table that names too few packages: a
test that reads the changed file is not run, and fails at the next landing
that runs the whole suite, on a bead that did not cause it. The note of that
red run names the test, and the fix is the row. That is the risk line.md
already lists for a builder's plain run. It moves one stage later for these
landings only.

On 12 Oct this would have taken eleven runs from about six minutes to about
one each: near 55 of the day's 298 minutes, and the whole of the wait behind
each of them.

Nothing here is yardr's alone. footplate's and aiquokka's commands do not
read `YARDR_LANDING` and are not touched: a depot whose whole suite takes 17
seconds has nothing to gain. A depot with no test command lands untested as
before.

### 3.2 The whole suite is measured, then shortened

Every landing that does run the whole suite is as long as its slowest
package, and then as long as the slowest of the three over the socket. This
step finds out why those are slow, and it has two parts.

**The scripts say how long each step took.** `.yardr/test` prints one line
after each step (`test: go vet ./... took 14 s`), and merge-local prints the
seconds of the rebase, the test command and the push in its log and puts the
test's seconds in the landing note (`merged 6a07178, tested with the base's
copy of .yardr/test in 355 s`). Section 2 had to take the length of a landing
from bead history and could only bound the steps. With these lines every
later change to the suite is judged by reading the log.

**One bead measures the four slow packages test by test**, on a quiet
machine, one package at a time, and says which of the candidates below is
worth a bead. None of them is measured yet, and this document does not pick
among them. The first four are the yardmaster's, from its note on the bead.

- *The race detector on every package.* It could be on for the packages the
  branch touched and their importers only, and off for the rest. What that
  saves is not known: no landing has run without it. What it gives up is a
  race in an untouched package that only shows under load, which is the kind
  of fault a landing exists to find.
- *The store, dispatch and bdp suites again over the socket.* The second run
  starts when the first has ended and is 51 to 60 s, 30% of a quiet landing.
  It could start beside the first. That adds no work and removes no work, so
  it helps only while cores are free: on a quiet machine, not at load 40.
- *No `t.Parallel()`* in any test of `internal/cli`, `internal/runner`,
  `internal/dispatch` or `internal/store`. Whether their tests can run side
  by side depends on what they share (a home directory, the environment, a
  port), which the measuring bead reads.
- *Tests that start real processes.* `internal/runner`'s test files have 236
  lines that name exec or herdr, and eleven test files of `internal/cli`
  call `exec.Command`. Which of them build or start something slow is for
  the measurement.
- *Sleeps.* The tests of `internal/dispatch` call `time.Sleep` 36 times, and
  those of `internal/cli` 19 times.

A shorter suite also makes fewer red runs of the machine's kind: both of 12
Oct's were a deadline missed in a package that ran for minutes.

What this step costs the machine: the measurement is four packages run once
each, in a quiet hour. Starting the socket run beside the first is the only
candidate that runs more at once.

### 3.3 merge-local takes the beads that wait behind the one it was started for

This is the later step, and it is the assembly's. mckean's two doubts about
it are that it will not help where little waits, and that a red run is hard
to handle cleanly. The first is true of 12 Oct and is counted at the end of
this section. The second is step 5 below: a red run together lands nothing,
sends nothing back, and falls back to exactly what the script does today.

It is one option in the group's `cmd`, beside
`YARDR_PUSH`: `YARDR_WITH_WAITING=<n>`, the most beads merge-local takes
along with its own. Unset or 0 is today's behaviour. No new kind of bead, no
new stage and no new word in the glossary: the group's limit stays 1, and one
session of the script lands more than one bead.

Started for bead A, the script:

1. lists the beads that wait for this group in `approved` with the same depot
   and the same `YARDR_BASE`, not held, oldest first, up to n. `yardr ready
   --json` prints stage, depot and group for each; whether it prints the
   routed group for a bead not yet claimed is the first thing the builder
   checks, and if it does not, the exec runner hands the list over as one
   more variable, which would be yardr's own part of this;
2. rebases A onto the base as today, then the first waiting bead's branch
   onto A's, the next onto that, each in its own worktree. A bead whose
   rebase conflicts, whose worktree is dirty or whose branch is missing is
   left out and left alone: it is handled when its own session starts, as
   today;
3. runs the test command once, with `YARDR_LANDING=1`, in the worktree of the
   last bead, whose tip holds all of them;
4. green: checks that no branch moved and each bead is still in `approved`
   and not held, fast-forwards the base to the tested tip, pushes, advances
   the waiting beads to `merged` and its own bead last, since that ends the
   session. Each note names the tip and the others: `merged 6a07178, tested
   with the base's copy of .yardr/test at 9c1d2e3 together with yardr-t5ts
   and yardr-nxah`;
5. red: nothing lands and no bead is sent back for it. The script notes the
   tip on each waiting bead in its metadata (`yardr bead update <id> --meta
   tested_red_with=<its branch tip>`), puts A's branch back to A alone and
   runs A alone, as today. A bead whose branch tip is the one in that
   metadata is not taken along again and takes no others along: it is tested
   alone when its turn comes, which is where a red run names it. A builder's
   new commit changes the tip, and the bead may be taken along again.

What "tested as it landed" means: the base only ever points at a commit the
whole suite passed on. The commits between two such points are on the base's
history and were not each tested, which is already so for a bead of several
commits.

What a person sees when it goes wrong. In the refinery's log: `== yardr-a
with yardr-b yardr-c red at 9c1d2e3; testing yardr-a alone`. On the beads:
nothing moved, and one line of metadata on b and c. The bead at fault gets
the red note it gets today, one run later. A red run that was the machine's
(both of 12 Oct's were) costs one run and sends no bead back, where today it
sends one back to its builder.

Why not a train made by the assembly. A train is the nearest thing the yard
has, and three things make it the wrong carrier. A train's children are
branched from the train's branch, and these beads were built and reviewed on
the base. A train is reviewed as a whole before it lands, and these beads
were each reviewed. A train lands as one or not at all, and here the bead at
fault must be able to stay behind while the others land. What the two share
is one whole suite for several beads, and the base moving once.

What it costs the machine: less than today, never more. One run replaces
several, and nothing runs beside it.

What it would have saved on 12 Oct, with every bead arriving when it did: 8
runs of 66, since 57 landings had nobody behind them. That number is why open
question 2 asks whether to build it now.

## 4. Left out

**Every landing tests only what the branch touched, and the whole suite runs
on the base on a timer** (the bead's idea 1, in full). Left out beyond 3.1.
A plain run cannot see four kinds of fault that line.md lists: a row missing
from the table, tests that build the binary, the three suites over the
socket, and a test in an untouched package that fails under the load of the
whole run. With a timer these are found on the base, after several beads
have landed, and no bead owns the red: a person bisects. "Tested as it
landed" would then mean "tested in part", for branches that change code. 3.1
takes the part of the idea where the plain run is complete. The rest is
option b of open question 1.

**Speculative runs: limit 2 or 3, each bead tested on top of the ones ahead**
(idea 3). Refused. This yard's limit is CPU, and the log shows what load
does: the same suite took 172 s at 01:18 and 355 to 508 s in the evening,
and both red runs of the day were a three-second deadline missed while
`internal/runner` took 279 and 391 s. Two suites at once on ten cores, beside
the builders and reviewers, make each run slower and more of them red, and a
red run ahead throws away the runs behind it. It also needs the base to move
in order while runs finish out of order, which the exec runner and
merge-local do not have. 3.3 gets several beads through one run and lowers
the load.

**The tests on another machine** (idea 4). Left out, and nothing in the
assembly would need to change for it: the test command is the depot's, and a
depot may name a command that sends the commit elsewhere and waits for the
answer. What is missing is the elsewhere. Actions on GitHub is paused for
this repository, and a linked yard that tests needs the toolchain, the
branch and a way to answer, which is a design of its own (yardr-8wxe, the
yard as a service, and yardr-xpf8 are the nearest). The timeout of 1800
seconds and the rule that exit 2 holds the bead already cover a machine that
does not answer.

**A queue as a thing of yardr's.** No new noun: 3.3 is an option of a script
and one key of bead metadata.

## 5. Open questions

**1. How far may a landing fall short of the whole suite?**

- a. Only where nothing compiled changed, as in 3.1. Cost: a wrong row of the
  table is found one landing late. Gain on 12 Oct: about 55 minutes of 298.
- b. Every landing runs the plain selection, and the whole suite runs on the
  base on a timer, with an event when it is red. Cost: a base that can be red
  for hours with no bead to send back, and something new to run the timer.
  Gain: most landings in well under two minutes.
- c. No change: every landing is the whole suite.

I would take a.

**2. When is 3.3 built?** mckean has said 3.1 and 3.2 come first. What is
left to decide is what follows them.

- a. Straight after them. Cost: the largest change here, in a shell script
  that lands code, with a second path on red. Gain as 12 Oct went: 8 runs of
  66, and fewer once 3.1 has shortened the queue.
- b. When the numbers ask for it: 3.1 and 3.2 land, the timings are read
  after a week, and 3.3 is built if beads still wait behind each other more
  than now and then. Cost: an evening like 12 Oct's keeps part of its queue
  until then.
- c. Never: the design stays here as a record.

I would take b. The day's numbers say the run is too long far more often
than the queue is too deep.

**3. When several beads tested together are red, what then?** (Only if 3.3
is built.)

- a. The script's own bead is tested alone, and the others wait for their
  turn, marked so they are not taken along again (3.3, step 5). Cost: after a
  red run, the beads behind are tested one by one.
- b. Halve: test the first half together, then the second, down to one bead.
  Cost: more script, and up to log2(n)+1 more whole suites inside one session
  and one timeout. Gain: the innocent half still lands together.

I would take a. Two runs of 66 were red, and neither was a branch's fault.

**4. Does every depot land through one group?**

- a. As today: one group, limit 1, for all depots. Cost: footplate's
  17-second landing waits up to six minutes behind yardr's.
- b. footplate gets its own exec group with limit 1 and the same script:

  ```sh
  yardr group add footplate-assembly --runner exec --limit 1 \
    --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
    --set log="$HOME/.yardr/logs/footplate-assembly.log"
  yardr route add --stage approved --depot footplate --group footplate-assembly --priority 10
  ```

  Cost: one `cargo test` of 10 to 54 s may run beside a yardr suite, 13
  times on 12 Oct. It undoes, for one depot, what line.md says was chosen:
  one group so that one suite runs at a time.

I would take b for footplate only. It is settings and no code, and it can be
taken back with two commands.

## 6. The beads it would become

In the order they would land. Beads 1 to 4 do not depend on each other; 1 is
idea 1, 2 and 3 are idea 5.

**1. A landing that changes nothing compiled runs the plain selection.**
Goal: 3.1. Where to look: `.yardr/test`, the block that starts `if [ -n
"$YARDR_LANDING" ]`, and the selection below it, which already knows changed
packages, embedded files, the table's rows and files the table does not know;
`yardtest_test.go`, which runs the script with `YARDR_TEST_PLAN=1`;
docs/line.md, "One whole-suite run per bead". Done when: with
`YARDR_LANDING=1`, a branch that changes one file under `docs/design/` plans
the root package only and its first line says nothing compiled changed; a
branch that changes a `.go` file, `go.mod`, an embedded file or a file in no
row plans the whole suite; each case has a test; line.md says it. The
assembly runs the base's copy of the script, so the builder's own run is the
only test of the edit before it lands, and the builder says so in the note.
Needs open question 1 answered a.

**2. The test script and merge-local print how long each step took.** Goal:
the first part of 3.2. Where to look: `again` in `.yardr/test`; `runtest` and
the last lines of `contrib/assembly/merge-local`; the starter pack's copy of
merge-local (`.yardr/test` has a row that sends a change of merge-local to
the tests of `internal/pack`); `contrib/assembly/README.md`. Done when: a
landing's log has one line with the seconds of each step of the test script
and of the rebase, the tests and the push, the landing note ends with the
seconds the tests took, and `merge_local_test.go` checks the note.

**3. Say which tests make `internal/cli`, `internal/runner`,
`internal/dispatch` and `internal/store` slow.** Goal: the second part of
3.2. Where to look: `go test -race -json` on one package at a time, on a
quiet machine, and the same without `-race` for the first candidate; the
five candidates of 3.2. Done when: a note on the bead lists, for each
package, the tests that take more than two seconds and what they wait for,
says for each candidate what it would save and what it would give up, and
files one backlog bead for each change worth making. No code.

**4. footplate lands through its own group.** Settings, for the yardmaster,
not a builder: the two commands of open question 4, and one sentence in
docs/line.md where it says every depot lands through one group. Needs open
question 4 answered b.

**5. merge-local takes the beads that wait behind its own.** Later, and only
as open question 2 is answered. Goal: 3.3, with `YARDR_WITH_WAITING` unset
meaning today's behaviour exactly. Where to look:
`contrib/assembly/merge-local`; `merge_local_test.go`; `yardr ready --json`
for what waits and for which group; `internal/cli/root.go`, `expect`, since a
move of another bead from this session's environment is refused as stale and
the script must make those moves outside its session. Done when, in an
isolated yard: three approved beads land after one run of the test command
and each note names the tested tip and the others; a red run together lands
nothing, sends nothing back, marks the others and tests the first alone; a
marked bead is tested alone; a bead held or moved during the run is not
landed. Depends on bead 2, whose timings say whether it is worth having, and
on open question 3.

**6. The docs for bead 5.** `contrib/assembly/README.md`, docs/line.md
section 4 and the group setup, with the option and what a red run together
looks like. Depends on bead 5.
