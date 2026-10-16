# The test suite's time, measured once, and the rules that keep it

Design yardr-635g, proposed 2026-10-16. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

## 1. The question

mckean, 16 Oct 2026, in the yardmaster's pane: "the test suite is getting big
and each work takes several minutes to test ... the proposals change, the
assumptions change, the observations change. We need to do something solid."
This design is that solid thing: one measured profile of where the suite's
time goes, the rules that follow from it, and the beads that apply them, in
one document, before anything is changed.

## 2. What exists today

The gate is the depot's test command, `.yardr/test`, run in the worktree
plainly by builders and reviewers, and by the assembly with `YARDR_LANDING=1`
(`contrib/assembly/merge-local`, which rebases the branch, runs the base's
copy of the script, fast-forwards by sha, pushes, and upgrades). Every step
prints how many seconds it took (`test: ... took N s`), and the assembly adds
one line per landing (`== <bead> took: rebase A s, the tests B s, push C s,
upgrade D s`), all in `~/.yardr/logs/refinery.log`.

What the gate does. Plainly it runs gofmt, `go vet ./...`, `go build ./...`,
then `go test -race` on the packages the branch touched and every package
that imports them, however far up (`pick` in `.yardr/test`, via
`go list -test`, whose dependency lists include test-only imports). A table
of 34 rows maps files tests read by path (packs, flows, testdata, scripts,
docs) to the packages whose tests read them; a changed file the table does
not know, a change to `go.mod`/`go.sum`, or (at landing) a deletion tests
every package. `YARDR_TEST_PLAN=1` prints the selection and stops. With
`YARDR_LANDING=1` it is the whole suite instead -- race detector on every
package, vet for linux too -- except when the branch changed nothing
compiled, in which case it is the plain run. When the plain selection holds
store, dispatch or bdp, those three suites run a second time over the unix
socket beside the first run. Since yardr-cecb (15 Oct) every `go test -race`
step runs under one machine-wide flock (`$HOME/.yardr/race.lock`, via perl),
printing `test: waited N s for the race lock` when it queued; nested runs
share the hold via `YARDR_RACE_HELD`. A step that fails runs its failed
packages once more and reports `test: flaky: ...` when they pass.

The suite at the measured commit (f0b1a22d, 16 Oct): 47 packages, 42 with
tests, 3,342 test functions in 531 test files, 186,272 test lines against
135,542 code lines. New test files per week, from git: 108, 251, 199 over
the last three weeks. Eight `testing.Short` skips exist, all in
`contrib/release`, and the gate never passes `-short`, so they never skip.

### 2.1 The measurement

Two full runs, `go test -race -json`, on this Mac (10 cores), plus
single-package runs. Run 1 ran 07:46-07:50 at load 12-23 with no competing
race leg: wall 216 s, 1,191 package-seconds, green. Run 2 ran 07:50-07:54
with `-count=1` while the landing of yardr-dx5f held the race lock:
wall 238 s, 1,196 package-seconds, green, with cli and store inflated by the
rival. The raw JSON of run 1 is attached to the bead's note; run 2's is
attached beside it. Per-package seconds under `-race`:

```
package                      tests   run1  run2  alone  par  sleep
internal/cli                   834  117.7 180.3 118.3    0     36
contrib/release                 29  109.5  75.2  35.6    0      0
contrib/assembly                66  106.2  77.9  91.7    3      3
internal/dispatch              384   94.3 118.2  67.7    0     38
. (root)                        12   93.6  66.5    -     0      2
contrib/isolated-yard           13   92.2  51.4    -     0      2
internal/store                 798   92.0 173.6  90.3    4     13
contrib/deploy                  39   61.3  41.0    -     1      1
internal/web                   237   57.8  89.3    -     0      8
internal/runner                158   35.1  41.6    -     0     12
contrib/server-yard              2   34.8  16.6    -     0      2
internal/chat                   70   30.5  26.7    -     0      0
contrib/peer                     9   27.5   9.2    -     1      1
internal/hooks                  98   26.9  21.6    -     0      0
internal/yards                  45   25.5  38.0    -     0      0
internal/pack                  139   21.0  21.8    -     0      0
internal/depotfile               7   20.6  18.0    -     0      0
internal/limits                 12   17.3  17.5    -     0      1
internal/rpc                    51   17.2  16.5    -     0      9
internal/config                 41   14.2   8.1    -     0      1
internal/source                 57   12.7  11.4    -     0      0
contrib/shot                    14   12.0  12.6    -     0      2
contrib/isolated-yard/fake       8   10.7  11.4    -     0      1
internal/server                 40    9.7   8.5    -     0     11
internal/pairing                 3    7.1   3.9    -     0      0
contrib/github-mint              3    5.6   2.9    -     0      0
contrib/machine-limits           3    4.4   2.5    -     0      0
internal/otel                   13    4.4   4.4    -     0      1
internal/flowdraw               19    3.1   2.0    -     0      0
internal/scripttest              3    2.8   2.3    -     0      0
internal/eta                    45    2.7   2.3    -     0      0
packs                           21    2.6   3.0    -     0      0
internal/notify                  5    2.4   2.4    -     0      0
internal/yardkey                 4    2.4   1.3    -     0      0
internal/bdp                    12    2.1   2.8    -     0      0
contrib/isolated-yard/sources    1    1.9   2.4    -     0      0
internal/version                 8    1.8   2.0    -     0      0
internal/standin                 3    1.8   1.9    -     0      0
contrib/bench                    4    1.5   1.5    -     0      0
internal/agents                 26    1.5   2.6    -     0      0
internal/rpc/clientgen           3    1.4   1.5    -     0      0
internal/peer                    3    1.4   1.6    -     0      0
```

`run1` ran beside usual yard load with no rival race leg; `run2` beside a
landing's locked race leg; `alone` is the package by itself with `-count=1`
as the load fell from 16 to 3 (an uncontended floor, not a quiet yard, which
this machine never offered); `par` counts `t.Parallel` calls;
`sleep` counts `time.Sleep` sites. Without `-race`, cli takes 68 s and
store 56 s, so the detector costs x1.6-1.7 there. Five packages have no test
files (server-yard/fake, harnesses, flowdraw/gen, rpc/gen, storetest).

Three shapes explain the slow packages. First, volume: cli's 832 tests
average 0.14 s (slowest 4.2 s), dispatch's 383 average 0.24 s (slowest
5.2 s), store's 794 average 0.13 s -- hundreds of sequential sub-second
tests, nearly all without `t.Parallel`, many shelling to git (30 cli test
files name it). Second, a few big black-box tests: release runs the real
`./build` script (compile, sign, notarize step, pack), server-yard builds
yardr from the tree and runs the setup script with real daemon starts,
isolated-yard drives the real down script over sleeper processes. Third,
real-clock waits and repo walks, in the top 40 below.

The top 40 tests by wall time in run 1, with what each waits on:

- Real clock, a full minute (1 test, 60 s): assembly
  `TestServerStopsWhenItsTestOwnerIsGone` (60.3 s, the n668 end-to-end
  test) polls once a second for up to 90 s while the server's owner watch
  ticks on the real `HomeCheck` of one minute (`internal/config/held.go`).
- Real release builds (4 tests, 83 s): release
  `TestBuildSignsAndNotarizesTheDarwinBinaryBeforeItPacksIt` (35.3 s),
  `TestBuildStampsTheVersionAndPacksTheArchive` (27.9 s),
  `TestBuildSaysThatUnsignedDarwinBinariesAreNotSigned` (11.5 s),
  `TestBuildFailsWithTheNotaryLogWhenAppleRefuses` (8.0 s) each run the
  real `./build` for this target and unpack the archive.
- Temp yard plus real CLI plus git, with a fake GitHub where a pull
  request is involved (13 tests, 88 s): assembly `TestPRScriptsHoldOnAPRFromAnotherHeadRepository` (9.1 s),
  `TestTrainThroughPullRequest` (8.7 s),
  `TestPRMergeHoldsWhenTheHeadMovedAfterReview` (7.0 s),
  `TestPRMergeHoldsWhenGitHubRefusesAMovedHead` (6.8 s),
  `TestPRMergeRefusedGoesBackToOpen` (6.8 s),
  `TestPRMergeNeedsTheReviewedSha` (6.5 s),
  `TestMergeLocalInstallsOnTheNamedDepot` (6.5 s),
  `TestIntegrateTrainHoldsWhenBoundPRHeadRepositoryChanges` (6.1 s),
  `TestIntegrateTrainOpensANewPRWhenTheBoundOneIsClosed` (6.1 s),
  `TestPRMergeWithoutSwitchHolds` (6.0 s),
  `TestIntegrateTrainDoesNotAdoptAnUnboundPR` (5.4 s),
  `TestTrainPushRefusesForeignRemoteCommits` (5.3 s),
  `TestPRTrainHoldsOnAPRFromAnotherHeadRepository` (5.3 s). Each builds a
  fresh home (`newYard`/`newTrainYard`), inits git repos, and runs a dozen
  real CLI commands of the test binary; the train yards run `t.Parallel`.
- Real down-script runs over sleeper processes (6 tests, 76 s):
  isolated-yard `TestDownAfterTheBinaryWasRebuilt` (28.7 s, rebuilds the
  binaries first), `TestDownLeavesTheProcessesOfOtherYards` (16.3 s),
  `TestDownStopsAServerStartedFromAnotherBinary` (11.9 s),
  `TestDownLeavesProbesOutOfTheStandInCount` (7.6 s),
  `TestDownRefusesWhenPsFails` (6.8 s),
  `TestFirstRunWithStdinClosed` (6.4 s) and `TestWrapperSetsCFUserTextEncoding`
  (5.2 s).
- Whole-repo walks, one per test (3 tests, 31 s): root
  `TestNoGoCodeNamesGitHub` (14.9 s), `TestNoGoCodeNamesARole` (8.9 s),
  `TestNoGoCodeNamesANotifyService` (7.6 s) each walk every file and match
  per line; all CPU under race.
- Gate-script runs with a fake go toolchain (2 tests, 36 s): root
  `TestYardTestSelects` (29.6 s, dozens of subtests each execing the
  script) and `TestYardTestRunsAgainWhenTheBuildCacheLostAFile` (12.0 s).
- Real setup-script runs (2 tests, 33 s): server-yard
  `TestServerYardSetsUpAYard` (23.5 s) and
  `TestServerYardWithoutTheHarnessLogin` (9.8 s) build yardr and a fake
  herdr from the tree and really start the server and daemon.
- Volume through real machinery (3 tests, 48 s): store
  `TestSessionIndexesMatchAWalkAfterEveryWrite` (24.6 s, a seeded write
  workload comparing index against full walk after every write, CPU under
  race, `t.Parallel`), yards `TestFilesPastTheHourAreRefused` (16.8 s,
  pushes 1,920 files through the real peer path plus an 8-way race check),
  web `TestBeadsPageManyBeads` (6.9 s, seeds 300 beads and a git repo,
  renders the page).
- Small-clock and process-spawn tests (5 tests, 47 s): web
  `TestSteadyClient` (16.1 s, httptest server with a 50 ms sleep loop),
  limits `TestAskThatIsNotAnswered` (9.0 s, 18 subtests each spawning a
  shell stand-in), depotfile `TestReadDepotFileHasADeadline` (6.4 s, real
  git under a deadline), root `TestYardTestRaceLegWaitsForTheLock`
  (6.1 s, a 3 s sleep plus script runs), chat
  `TestTypedSecretIsInNoFrame` (6.0 s, terminal view renders, CPU).
- Just outside, the largest real-sleep test after the owner watch:
  dispatch `TestPeersDaemonHoldsOrPolls` (5.2 s) sleeps 2 s plus 2.5 s of
  real time to count poll rates.

Sleep survey over all tests: 144 `time.Sleep` sites; 137 are 500 ms or
less (mostly 1-100 ms polls); 8 elapse longer (1 s x3, 2 s, 2.05 s, 2.5 s,
3 s, 600 ms x1); one 5-minute sleep never elapses (the isolated-yard
sleeper process, killed at test end).

### 2.2 What a bead pays

From `refinery.log` for 9-16 Oct: about 500 yardr landings (27, 3, 33, 85,
125, 142, 68, 17 per day). Per-step timings exist from 13 Oct, per-landing
totals from 14 Oct. The landing's tests over 14-16 Oct (110 landings):
11.9 machine-hours, median 372 s, max 887 s. The race leg over 13-16 Oct
(260 runs): 20.2 machine-hours, daily medians 165, 251, 345, 213 s (15 Oct,
pre-lock, was the worst). The socket leg ran 223 times for 8.4 machine-hours
beside the race leg, so it costs the machine but usually not the wall. Since
the race lock landed, 7 landings on 16 Oct queued 52-263 s each (19 minutes
in all). Nine `test: flaky:` lines in the whole log: reruns are rare. 39
landings in the week changed nothing compiled and ran the plain selection
instead of the whole suite.

Away from the landing, the sample of the last ten landings' notes shows
three plain runs per bead (builder, reviewer, security reviewer), usually
on the same sha, selecting 2-30 packages (median 24). Replaying those ten
landings in plan mode at their landed commits selects 3-32 packages
(median 26) and selects internal/cli in all ten: the plain wall is the
cli wall, 118-180 s, plus vet and build.

The import-graph claim holds in-process but not over the binary. `go list
-test` dependency lists include test-only imports (contrib/assembly imports
no yardr package in its code but over twenty in its test variant), and
`pick` propagates transitively, so a change reaches exactly the packages
whose test binaries link it -- plus the 34 reads-table rows for files tests
open by path, with unknown files, `go.mod`/`go.sum`, and (at landing)
deletions falling back to everything. The hole is the suites that build the
tree's binary and drive it as a black box: contrib/server-yard and
contrib/isolated-yard import no yardr package at all, contrib/release only
itself, and assembly/peer/deploy do not import cli, web, chat, pack, bdp
or flowdraw -- yet their tests run CLI commands of a binary that links all
of them. A change to internal/web, for example, selects only root, cli and
web (landed f0b1a22d replays to exactly that), while the assembly and
server-yard suites exercise a binary containing the change and do not run.
Today three things cover the hole: `go build ./...` catches compile breaks
everywhere, the importing packages' own tests catch behavior breaks, and
the landing runs the whole suite anyway. An incremental landing must
instead always run the three black-box suites (server-yard ~35 s,
isolated-yard ~92 s, release ~36-110 s), or accept the hole. The socket
leg's existing gate already accepts the same reach argument: it runs only
when the plain selection holds store, dispatch or bdp.

## 3. The proposal

Seven rules, each with the number that justifies it. They land as
`docs/testing.md` (new; no such file exists) plus the script changes in
the beads below. What a bead pays today, and after: today a bead pays
about three plain runs of ~175 s wall each (cli binds all three) plus a
landing of 372 s median plus a lock queue when one forms -- roughly 900
machine-seconds (~15 minutes) over four race legs. After the beads, the
plain wall is ~100 s (a cli at budget plus vet and build) over two or
three legs, and the landing is the incremental selection at ~150 s --
roughly 400-500 machine-seconds (~7-8 minutes). At 500 landings a week,
landing machine time falls from ~20 hours toward ~8.

R1. A package's tests finish in 60 s under `-race`, or the gate fails.
Seven packages are over today (run 1: cli 118, release 110, assembly 106,
dispatch 94, root 94, isolated-yard 92, store 92); the wall is the slowest
package, so one package over budget is every run over budget. The script
enforces the cap with an allow-list that names, per package, why it is
over and which bead fixes it; the fix beads below delete the entries.
R2. A test sleeps no more than 500 ms at a site, and a poll loop polls a
condition with a deadline. 137 of 144 sleep sites already comply; the 8
longer sites are named in the allow-list, and the 60 s owner end-to-end
test gets a test-only tick (precedent: `YARDR_TEST_OWNER`) instead of the
real minute. The package cap is the backstop a site rule cannot be: a
1 s sleep in a 90 s poll loop is a 60 s test.
R3. A package with over 100 tests or over 30 s runs its tests parallel:
`t.Parallel` on every test that owns its state, allow-list for the rest.
cli (834 tests, none parallel), dispatch (384, none) and store (798, four)
are the debt; assembly's parallel train yards are the established shape,
and temp homes and dirs are already what the tests use.
R4. One race leg per sha per bead. The reviewer runs `-race` only when the
branch moved since the builder's green run, and the security review runs
no tests: the sample shows three identical plain runs on the same sha
(24-30 packages each), and a rerun of the same binary only re-detects
flakes (nine in the log's history). Saves one to two legs, ~175-350
machine-seconds, per bead.
R5. The landing runs the incremental selection -- today's `pick` -- plus
always the three black-box suites (server-yard, isolated-yard, release),
plus the socket leg as today. The replay selects 3-32 packages (median
26) against today's 42; the wall drops from the 372 s median toward the
cli wall (~120 s now, ~60 s at budget), and the always-run suites close
the measured binary-reaching hole at no wall cost (they run inside the
same `-p 10` invocation as the rest).
R6. The whole suite still runs, but on a quiet machine, not on a timer and
not on any bead: when the load is low it runs over main, and a red base
raises an event. mckean declined the timer; load-gating keeps his earlier
condition (from design yardr-1vt3) that the base is held to the whole
suite at a stated point, while the incremental landing, the always-run
suites and the budgets close every known gap. `YARDR_LANDING=1` stays the
one command anyone can run.
R7. The rules are judged by re-measurement: the same per-package table one
week after the last fix bead lands, against this document's table and its
attached JSON. Timer t107 (20 Oct) already reads landing timings; its card
takes the comparison (this author could not read the timer as a builder,
so the re-measure bead confirms and extends it rather than assuming it).

## 4. Left out

- The whole suite on a timer: mckean declined it; R6 is load-gated instead.
- Batching at landing: yardr-8zm4 owns it, with its card about 20 Oct
  (timer t66); this design's numbers feed that card, and batching's savings
  multiply these (fewer landings, each cheaper) rather than overlapping them.
- A `-short` fast leg: only release's build tests honor `-short`, the gate
  never passes it, and the budgets subsume what it would skip.
- Capping `-p`/`GOMAXPROCS` or raising per-package `-timeout`: refused with
  yardr-cecb -- slower, not fixed.
- Speculative runs and a second machine for tests: refused with yardr-1vt3 --
  this yard's limit is CPU, and there is no elsewhere with the toolchain.
- Per-test budgets: the package cap plus the sleep cap cover them, and a
  per-test cap would flake under load (run 2 moved single packages by x1.5).
- Test-level selection (finer than packages): `go test` runs packages, and
  the measured waste is whole slow packages, not tests within fast ones.

## 5. Open questions

1. The budget numbers: 60 s per package and 500 ms per sleep, as proposed,
   or looser (120 s / 1 s)? Tighter means seven fix beads now; looser
   leaves the wall at ~120 s and the sleeps where they are. Take 60/500:
   the fixes are ordinary parallelism, and the allow-list carries the tail.
2. The incremental landing (R5): now, or after the fix beads? The script
   change alone saves ~170 s per landing (~3 machine-hours a day at current
   volume) with the hole closed by the always-run suites; waiting adds
   nothing but delay. Take now.
3. The review legs (R4): conditional race for the reviewer and no tests for
   the security review, as proposed, or keep all three legs? Keeping them
   re-detects flakes at ~175-350 machine-seconds per bead; the log shows
   nine flakes ever. Take the proposal.
4. The whole-suite backstop (R6): a quiet-machine run that raises an event
   on red, as proposed, or never scheduled (the gaps are closed, the
   command stays manual)? "Never" is simpler and honest if the re-measure
   agrees; the quiet run catches what the table's rows and the always-run
   list miss, like a new black-box suite nobody registered. Take the quiet
   run, and let the re-measure argue for never.
5. The 60 s owner end-to-end test: a test-only tick setting (take it;
   `YARDR_TEST_OWNER` is the precedent), allow-list the 60 s (assembly can
   never meet R1 then), or move the test to the quiet leg (a new leg for
   one test)?

## 6. The beads it would become

In landing order. B2-B7 are independent of each other and can build in
parallel; B1 first, since it writes the cap and the allow-list the rest
delete from; B8 after B1; B10 after B2-B8.

B1. The gate enforces the budgets. Goal: R1 and R2 as script behavior plus
`docs/testing.md` carrying all seven rules. Where to look: `.yardr/test`
(the `again` wrapper, which already times steps), this design's tables for
the initial allow-list (7 packages with reasons, 8 sleep sites).
Done when: a package over 60 s under `-race` fails the gate unless
allow-listed; a `time.Sleep` over 500 ms in a test file fails it unless
allow-listed (the isolated-yard sleeper exempt as non-elapsing);
`docs/testing.md` states the rules; the gate is green on a clean tree.

B2. internal/cli runs parallel, to 60 s. Goal: the wall binder stops
binding. Where to look: the 834 tests (mean 0.14 s, no `t.Parallel`),
following the assembly train-yard shape (temp homes, no shared state);
30 test files shell to git -- keep the subprocesses, parallelize around
them. Done when: the package's uncontended floor (`-count=1`, alone) is at
most 60 s; no test is allow-listed without a reason; the B1 allow-list
entry for cli is deleted.

B3. internal/dispatch runs parallel, to 60 s. Goal and shape as B2 for 384
tests (mean 0.24 s), including `TestPeersDaemonHoldsOrPolls` (2 s + 2.5 s
of real sleeps to count poll rates -- shorten against the fake clock the
test already owns or allow-list with the number). Done when: as B2, for
dispatch.

B4. internal/store runs parallel and the index walks get cheaper, to 60 s.
Goal: 798 tests (mean 0.13 s) parallel, plus
`TestSessionIndexesMatchAWalkAfterEveryWrite` (24.6 s) and
`TestOpenIndexMatchesAWalkAfterEveryWrite` (4.4 s) compare less often than
after every write while still pinning the invariant. Done when: as B2,
for store.

B5. The root package shares one repo walk, to 60 s. Goal: the three
`TestNoGoCodeNames*` tests (31 s together) walk the repository once and
match thrice; `TestYardTestSelects` (29.6 s) stays but runs beside them
(`t.Parallel`). Done when: as B2, for root.

B6. The assembly owner test leaves the real minute, package to 60 s. Goal:
per open question 5's pick, a test-only tick shortens the owner watch in
the end-to-end test while the real CLI path stays exercised; the rest is
already largely parallel. Done when: the package floor is at most 60 s;
the e2e still starts the server through the real CLI and still fails if
the watch is removed; B1's entry for assembly is deleted.

B7. The black-box suites stabilize, to 60 s. Goal: contrib/release
(35-110 s across runs -- find whether the swing is build cache,
parallelism, or the notarize step, and pin it) and contrib/isolated-yard
(51-92 s) hold still and under budget. Done when: three consecutive
`-count=1` runs of each land within 20% of each other and at most 60 s;
B1's entries are deleted.

B8. The landing runs the incremental selection plus the black-box suites.
Goal: R5 as script behavior. Where to look: `whole()` in `.yardr/test`
(the Go-free early-out is the shape to extend), `contrib/assembly/merge-local`
(the test invocation and timing lines), `docs/line.md` and
`contrib/assembly/README.md` for the landing docs. Done when: the ten
replayed landings select as in section 2.2; a landing on a fixture branch
runs the selection plus server-yard, isolated-yard and release and says so
in the log; the socket leg still follows the 022s gate.

B9. One race leg per sha. Goal: R4 as behavior. Where to look: the review
stages' instructions and `.yardr/test` (a `YARDR_RACE=0` plain run without
`-race`, or the equivalent the builder's test line already supports).
Done when: the mechanism exists, the review instructions name it (unchanged
sha since the builder's green test line), and one bead's notes show the
pattern.

B10. Re-measure in a week. Goal: R7 -- the same table, the verdict on the
rules. Where to look: this document's table and attached JSON, timer t107
(20 Oct, landing timings -- confirm what it reads and fold the comparison
into its card), `refinery.log`. Done when: the per-package table is taken
again both ways (beside load and alone), the deltas per package and the
new per-bead cost are on the bead as a note, and every rule that missed is
a filed follow-up or a closed wont-fix with the number.

