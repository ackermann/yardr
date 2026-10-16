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
of 40 rows maps files tests read by path (packs, flows, testdata, scripts,
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

The suite at the measured commit (f0b1a22d, 16 Oct; the later runs below are
at ec4aa64e, a docs-only commit on top, so the package times compare): 47
packages, 42 with tests, 3,337 test functions in 531 test files, 186,272
test lines against 135,542 code lines. New test files per calendar week: 98,
220 and 187 in the weeks of 28 Sep, 5 Oct and 12 Oct (the last to 16 Oct).
Eight `testing.Short` skips exist, all in `contrib/release`, and the gate
never passes `-short`, so they never skip. Main has since moved to
b1f20c0e, 76 files on from f0b1a22d including cli, dispatch and store test
files; the numbers below are the f0b1a22d measurement, named so the
re-measure (B11) can show what the move changed.

### 2.1 The measurement

Four full runs, `go test -race -json -count=1 ./...` (run 1 without
`-count=1`, but no package read cached), on this Mac (10 cores), plus
single-package runs. Run 1 ran 07:46-07:50 at load 12-23 with no competing
race leg: wall 216 s, 1,191 package-seconds, green. Run 2 ran 07:50-07:54
while the landing of yardr-dx5f held the race lock: wall 238 s, 1,196
package-seconds, green, with cli and store inflated by the rival. Run 3 ran
08:22:46-08:27:27 while the landing of yardr-8w9z held the race lock from
08:23:39 (load 5.6 rising past 28): wall 281 s, 1,382 package-seconds,
green. The quiet run ran 08:38:43-08:41:11 at load 3.5-6.6 with no rival
race leg and no landing anywhere in the window: wall 148 s, 959
package-seconds, green. The raw JSON of runs 1 and 2 is attached to the
first draft's notes; run 3's and the quiet run's to the second's. Two
selection replays ran 09:04-09:07 at bb062869 (docs-only over f0b1a22d,
so the package times compare), load 2.5-8.6, no landing in the window;
their commands and raw JSON are with the third draft's notes (5966-5967)
and section 2.2.
Per-package seconds under `-race`:

```
package                      tests   run1   run2   run3  quiet  alone  par  sleep
internal/cli                   834  117.7  180.3  191.4  106.2  118.3    0     36
contrib/release                 29  109.5   75.2  120.6   73.7   35.6    0      0
contrib/assembly                66  106.2   77.9  103.0   92.9   91.7    3      3
internal/dispatch              384   94.3  118.2  124.0   79.5   67.7    0     38
. (root)                        12   93.6   66.5   67.1   65.5   31.2    0      2
contrib/isolated-yard           13   92.2   51.4   69.0   53.0   30.3    0      2
internal/store                 798   92.0  173.6  207.9   83.7   90.3    4     13
contrib/deploy                  39   61.3   41.0   47.3   42.4   22.1    1      1
internal/web                   237   57.8   89.3   87.6   52.0     -     0      8
internal/runner                158   35.1   41.6   42.1   29.4     -     0     12
contrib/server-yard              2   34.8   16.6   20.2   21.4     -     0      2
internal/chat                   70   30.5   26.7   26.9   25.5     -     0      0
contrib/peer                     9   27.5    9.2   11.0   10.7     -     1      1
internal/hooks                  98   26.9   21.6   25.6   21.3     -     0      0
internal/yards                  45   25.5   38.0   44.1   20.8     -     0      0
internal/pack                  139   21.0   21.8   25.1   18.1     -     0      0
internal/depotfile               7   20.6   18.0   21.8   19.5     -     0      0
internal/limits                 12   17.3   17.5   14.4   19.1     -     0      1
internal/rpc                    51   17.2   16.5   17.5   14.9     -     0      9
internal/config                 41   14.2    8.1   10.4   14.3     -     0      1
internal/source                 57   12.7   11.4    9.8   11.7     -     0      0
contrib/shot                    14   12.0   12.6   14.8   15.9     -     0      2
contrib/isolated-yard/fake       8   10.7   11.4   13.7   16.3     -     0      1
internal/server                 40    9.7    8.5   10.5    7.4     -     0     11
internal/pairing                 3    7.1    3.9   10.3    3.2     -     0      0
contrib/github-mint              3    5.6    2.9    3.5    3.3     -     0      0
contrib/machine-limits           3    4.4    2.5    3.4    3.3     -     0      0
internal/otel                   13    4.4    4.4    4.1    4.1     -     0      1
internal/flowdraw               19    3.1    2.0    3.0    1.8     -     0      0
internal/scripttest              3    2.8    2.3    4.2    3.6     -     0      0
internal/eta                    45    2.7    2.3    1.6    1.6     -     0      0
packs                           21    2.6    3.0    2.9    2.0     -     0      0
internal/notify                  5    2.4    2.4    2.2    2.2     -     0      0
internal/yardkey                 4    2.4    1.3    1.6    1.9     -     0      0
internal/bdp                    12    2.1    2.8    2.8    2.6     -     0      0
contrib/isolated-yard/sources    1    1.9    2.4    3.0    3.1     -     0      0
internal/version                 8    1.8    2.0    1.5    1.6     -     0      0
internal/standin                 3    1.8    1.9    1.7    1.7     -     0      0
contrib/bench                    4    1.5    1.5    1.9    1.9     -     0      0
internal/agents                 26    1.5    2.6    3.3    3.3     -     0      0
internal/rpc/clientgen           3    1.4    1.5    1.4    1.4     -     0      0
internal/peer                    3    1.4    1.6    1.7    1.7     -     0      0
```

`run1` ran beside usual yard load with no rival race leg; `run2` and `run3`
each beside a landing's locked race leg; `quiet` with no rival and no
landing, load 3.5-6.6; `alone` is the package by itself with `-count=1`
and no rival (cli, release, assembly, dispatch and store as the load fell
from 16 to 3; root, isolated-yard and deploy at load 3.8-5.8); `par`
counts `t.Parallel` calls (9 in the whole suite); `sleep` counts
`time.Sleep` sites. Without `-race`, cli takes 68 s and store 56 s, so the
detector costs x1.6-1.7 there. Five packages have no test files
(server-yard/fake, harnesses, flowdraw/gen, rpc/gen, storetest).

Three shapes explain the slow packages. First, volume: cli's 832 tests
average 0.14 s (slowest 4.2 s), dispatch's 383 average 0.24 s (slowest
5.2 s), store's 794 average 0.13 s -- hundreds of sequential sub-second
tests, nearly all without `t.Parallel`, many shelling to git (30 cli test
files name it). These are contention-proof: cli takes 118 s alone and
106-117 s in a full run without a rival. Second, contention-sensitive
integration: root, isolated-yard, deploy and release take 22-36 s alone
but 41-121 s in full runs, and even the quiet run doubles deploy (22 s
alone, 42 s quiet) -- their subprocess storms (gate-script runs, down
scripts over sleeper processes, real `./build` runs, a server per deploy
test) slow as soon as ten race-instrumented packages share the machine.
Third, real-clock waits and repo walks, in the top 40 below.

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
- Real down-script runs over sleeper processes (7 tests, 83 s):
  isolated-yard `TestDownAfterTheBinaryWasRebuilt` (28.6 s, rebuilds the
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

The quiet run's top 40 has the same tests in nearly the same order: the
owner watch (60.2 s) and the index walk (24.1 s) are identical to run 1 to
the second, the release builds are faster (21.9 s for the darwin sign
test), and single-package CPU tests barely move (chat secret 6.0 s in
both) -- while the repo walks swing 14.9 s to 5.5 s and the down tests
halve. What a test waits on decides how much contention moves it.

Sleep survey over all tests: 144 `time.Sleep` sites in test files; 135
are 500 ms or less (134 literal, plus one `time.Sleep(delay)` in
`internal/web/jwt_test.go` whose delay is set to at most 300 ms); 8 elapse
longer (1 s x3 in dispatch/harness_reset_test.go, server/server_test.go
and assembly/owner_test.go, 2 s and 2.5 s in
dispatch/peerhold_test.go, 2050 ms in dispatch/failure_test.go, 600 ms in
rpc/stream_test.go, 3 s in yardtest_test.go); one 5-minute sleep never
elapses (the isolated-yard sleeper process, killed at test end).

### 2.2 What a bead pays

From `refinery.log` for 9-16 Oct: 500 yardr landings (27, 3, 33, 85,
125, 142, 68, 17 per day). Per-step timings exist from 13 Oct, per-landing
totals from 14 Oct. Wall and machine are told apart below: a `took` line
is wall time, and the lock wait inside it is idle, not machine; machine
time is package-seconds summed, from the runs above. The landing's test
step over 14-16 Oct (212 landings with times, of 227; 15 went red without
one): 70,241 wall-seconds summed (19.5 wall-hours), median 283 s, max
1800 s (a timeout, yardr-mc52 on 14 Oct). The whole-suite race leg over
13-16 Oct (266 runs): 74,418 wall-seconds (20.7 wall-hours; the race-leg
`took` includes the lock wait), daily medians 165, 251, 345, 213 s (15
Oct, pre-lock, was the worst). The socket leg ran 229 times for 31,138
machine-seconds (8.6 machine-hours, median 115 s) beside the race leg, so
it costs the machine but usually not the wall. Since the race lock landed,
7 landings on 16 Oct queued 52-263 s each (1,162 s, 19 minutes, in all).
Nine `test: flaky:` lines in the whole log: reruns are rare. 39 landings
in the week changed nothing compiled and ran the plain selection instead
of the whole suite.

The twelve latest landings, each with its test-step wall, race-leg wall
(wait included), socket machine time, and lock wait:

```
bead        tests  race  socket  waited
yardr-n668    168   162     103      -
yardr-65vy    306   261      64     83
yardr-vn86    487   439      86    261
yardr-0qrh    red     -       -      -   (first attempt; landed later)
yardr-005c    225   210     119      -
yardr-x9kv    366   313     102    124
yardr-2sy5    482   453      71    263
yardr-3x17    308     -       -    156   (docs only: plain selection, no whole leg)
yardr-0qrh    535   510      66    223
yardr-cs5z    252   233       -      -   (socket skipped: selection holds none of the three)
yardr-dx5f    235   205     126      -
yardr-fhsd    188   173     103      -
```

The gap between the test step and the race leg (6-53 s) is vet, linux
vet, build and the package pick (vet median 8 s, linux vet 11 s, build
2 s over 14-16 Oct). The waits hit plain legs too: 3x17, a docs change
with a 6-package selection, waited 156 s behind other legs. cs5z shows
the socket gate working: a web-only change skips the socket leg.

Away from the landing, reviewers do not rerun an unchanged sha. The
eleven latest beads' notes (n668 through fhsd) show one builder plain run
each (last-line selections of 30, 24, 24, 25, 14, 24, 6, 13, 3, 28 and 24
packages: range 2-30, median 24), zero same-sha reruns by reviewers or
security reviewers -- every review note quotes the builder's line with
"did not rerun" or equivalent -- and extra runs only when the branch moved
(x9kv: two shas; 0qrh: five shas over five runs) or a bead passed through
extra builder passes (dx5f ran the same sha three times across a hold and
unhold). That is 7 extra plain runs over 11 beads, about 0.6 per bead.
The roles already say this (packs/starter/roles/reviewer.md and
review-security.md: skip the rerun when the test line names the handed
tip); the sample shows the practice holds, except repeat builder passes,
which no rule covers.

No central log records how long those builder and extra plain runs took
or how long they queued: a note carries only the package count, and
refinery.log carries only landing legs -- a plain run's `waited` line
goes to the session transcript, which no query reads. So the plain-leg
wall below is not a sample from the week; it is a range from controlled
replays, and the builder lock wait is bounded, not measured, by the
landing waits on the same lock (52-263 s, n=7 since the lock landed,
plus the docs-only 3x17 landing's 156 s wait on a 6-package selection).

A plain run's wall is its race leg (the selection's slowest package,
under the race lock, so with a wait but no rival) plus gofmt,
`go vet ./...`, `go build ./...` and the package pick. Measured at
bb062869 with no landing in the window and load 2.5-3.3:
`go test -race -json -count=1 . ./internal/cli ./internal/web` (the cs5z
replay selection: root, cli, web) takes 91 s wall (cli 88.9 s binds;
root 31.8 s, web 51.7 s; note 5966). On a warm cache vet takes about 4 s
and build about 1 s, the pick a few more, so a quiet cli-bound plain run
is about 100-110 s wall before any lock wait. Under usual load the same
leg is cli at 106-118 s (the quiet and run-1 full-run columns, no rival)
plus vet at 8-25 s, about 120-145 s. Replaying ten landings in plan mode
at their landed commits selects 3-32 packages (median 26) and selects
internal/cli in all ten; both endpoints re-verified for this revision
(cs5z: exactly root, cli, web; dx5f: 31 packages, 28 with tests, matching
the live "28 packages ok" line).

Forcing the three always-run suites onto that replay (root, cli, web plus
release, isolated-yard and server-yard):
`go test -race -json -count=1 . ./internal/cli ./internal/web
./contrib/release ./contrib/isolated-yard ./contrib/server-yard`, same
conditions, load 3.2-8.6, takes 98 s wall (cli 96.3, root 40.2, web
54.4, release 48.1, isolated-yard 35.0, server-yard 14.3; note 5967)
against 91 s bare. The suites' own slowest (release 48.1 s) runs inside
cli's 96.3 s, and the selection's own packages slow under the added
contention (cli 88.9 to 96.3 s, root 31.8 to 40.2 s, web 51.7 to 54.4
s), so the suites cost about 7 s of wall on a cli-bound selection when
quiet. The previous revision's 119 s against 89 s bare (about 30 s) was
the same replay under load. Together they bound the cost at about 7-30 s
of wall on a cli-bound selection, load-dependent. On a small selection
with no slow package the suites set the floor instead: about 50-75 s
quiet (release 48 s binds here), about 90-110 s under load.

The import-graph claim holds in-process but not over the binary. `go list
-test` dependency lists include test-only imports (contrib/assembly imports
no yardr package in its code but over twenty in its test variant), and
`pick` propagates transitively, so a change reaches exactly the packages
whose test binaries link it -- plus the 40 reads-table rows for files tests
open by path, with unknown files, `go.mod`/`go.sum`, and (at landing)
deletions falling back to everything. The hole is the suites that build the
tree's binary and drive it as a black box: contrib/server-yard and
contrib/isolated-yard import no yardr package at all, contrib/release only
itself, and assembly/peer/deploy do not import cli, web, chat, pack, bdp
or flowdraw -- yet their tests run CLI commands of a binary that links all
of them. A change to internal/web, for example, selects only root, cli and
web (the cs5z replay above), while the assembly and server-yard suites
exercise a binary containing the change and do not run. Today three things
cover the hole: `go build ./...` catches compile breaks everywhere, the
importing packages' own tests catch behavior breaks, and the landing runs
the whole suite anyway. An incremental landing must instead always run the
three black-box suites (server-yard ~14 s quiet/~35 s loaded,
isolated-yard ~35/~92, release ~48/~110), at the measured 7-30 s of wall
on a cli-bound selection depending on load, or accept the hole. The
socket leg's existing gate
already accepts the same reach argument: it runs only when the plain
selection holds store, dispatch or bdp.

## 3. The proposal

Seven rules, each with the number that justifies it. They land as
`docs/testing.md` (new; no such file exists) plus the script changes in
the beads below. What a bead pays today, and after -- wall time first,
then machine time. Today, in wall time: one builder plain run of about
100-145 s (91 s race leg quiet for the 3-package cli-bound replay plus
vet, build and pick, section 2.2; about 120-145 s loaded), plus about 0.6
extra plain runs at the same rate from branch moves and repeat passes (7
over the 11-bead sample: 5 from new shas, 2 same-sha), plus the landing's
283 s median wall (14-16 Oct, n=212, lock waits included) -- 100-145 +
0.6 x 100-145 + 283 = roughly 445-515 s of waiting (8-9 minutes), over
typically two race legs (one plain, one landing), not four: reviewers in
the sample never reran an unchanged sha. Builder lock waits are not in
that sum: they are never logged (section 2.2), so the sum understates the
wait by whatever the plain leg queued on the shared lock (0-263 s seen on
landings). In machine time the same bead costs 300-920 machine-s plain (3
vs 28 test packages, race package-seconds summed, run-1 column) plus the
extras at the same rate, plus about 1,340 at landing (race
package-seconds 959-1,382 plus the socket leg's 115 s median beside the
wall plus the vets) -- roughly 2,000-2,700 machine-s (35-45
machine-minutes); lock waits are idle and cost no machine time. After the
beads, the plain wall is about 75 s (a cli at budget plus vet, build and
pick) over one leg per sha plus branch-move extras (about 0.4 per bead:
R4 removes only the same-sha repeats), and the landing, if open question
2 is answered yes, is the incremental selection plus the always-run
suites at about 110-150 s wall (98 s race leg quiet plus vet, build and
pick, toward 145 s loaded) and about 700 machine-s beside the socket
leg's ~115 -- 75 + 0.4 x 75 + 110-150 = roughly 215-255 s of waiting
(under 4.5 minutes) and about 1,000-1,300 machine-s per bead. At 60-140
landings a day (13-15 Oct), landing machine
time falls from about 23-52 machine-hours a day toward about 12-32. If
question 2 is answered no, the landing stays whole and only the budgets
pay: plain about 75 s, landing toward 100-150 s wall as packages hit
budget, at about 1,000 machine-s per landing.

R1. A package's tests finish in 60 s under `-race`, or the gate fails.
Eight packages are over today (run 1: cli 118, release 110, assembly 106,
dispatch 94, root 94, isolated-yard 92, store 92, deploy 61); the wall is
the slowest package, so one package over budget is every run over budget.
Four are over even alone (cli 118, assembly 92, store 90, dispatch 68)
and four only in full runs (root 31, isolated-yard 30, deploy 22,
release 36 alone). The script enforces the cap with an allow-list that
names, per package, why it is over and which bead fixes it; the fix beads
below delete the entries.
R2. A test sleeps no more than 500 ms at a site, and a poll loop polls a
condition with a deadline. 135 of 144 sleep sites already comply; the 8
longer sites are named in the allow-list, and the 60 s owner end-to-end
test gets a test-only tick (precedent: `YARDR_TEST_OWNER`) instead of the
real minute. The package cap is the backstop a site rule cannot be: a
1 s sleep in a 90 s poll loop is a 60 s test.
R3. A package over the R1 budget with over 100 tests runs its tests
parallel: `t.Parallel` on every test that owns its state, allow-list for
the rest. That binds exactly the three measured volume debts -- cli (834
tests, none parallel), dispatch (384, none) and store (798, four) -- whose
fix beads are B2-B4; assembly's parallel train yards are the established
shape, and temp homes and dirs are already what the tests use. The other
five over-budget packages have under 100 tests each and are fixed without
this rule (B5-B7); packages under budget comply until they breach it, and
the budget is their backstop then.
R4. One plain race leg per sha, in builder and review stages only. A
builder or review stage whose branch is unchanged since a green test line
on that sha does not rerun the plain suite. The roles already say this
for reviewers, and the 11-bead sample shows zero same-sha review reruns,
so the rule restates the practice as the depot's and closes the one gap
the sample shows: repeat builder passes (dx5f ran the same sha three
times across a hold and unhold). Each note says `reran` or `names this
tip` so the next sample can tell. A stage that committed -- a review fix,
a security fix -- still runs the suite on its new sha. The assembly's
runs are outside this rule: the landing always runs the base's script
with `YARDR_LANDING=1` on exactly what lands, every time, including after
a merger's rebase that changed nothing
(`contrib/assembly/merge-local`:177-180), and its whole-suite gate differs
from any plain run (`.yardr/test`:434-460), so no plain test line ever
satisfies it.
R5. The landing runs the incremental selection -- today's `pick` -- plus
always the three black-box suites (server-yard, isolated-yard, release),
plus the socket leg as today. The replay selects 3-32 packages (median
26) against today's 42; the forced replay measures 98 s wall against 91 s
bare quiet (the suites cost about 7 s, contention included) and 119 s
against 89 s under load (about 30 s), so the suites cost about 7-30 s of
wall on a cli-bound selection depending on load, and set a 50-75 s floor
under small selections quiet (about 90-110 s loaded); the wall drops from
the 283 s median toward 110-150 s. This rule needs the
operator to lift the yardr-1vt3 condition (open question 2): today the
whole suite gates every landing that changes code, and the gate still
implements that (`.yardr/test` `whole()`).
R6. If question 2 is answered yes, the whole suite still runs, but on a
quiet machine, not on a timer and not on any bead: when the load is low
it runs over main, and a red base raises an event (bead B10 builds the
trigger and the event). This replaces per-landing gating with gating at a
stated quiet point plus the always-run suites; it does not keep mckean's
1vt3 condition, which is why it is asked, not assumed. mckean declined
the timer; load-gating runs the suite when it is cheapest and most stable
instead (148 s wall at load 3.5-6.6, green). `YARDR_LANDING=1` stays the
one command anyone can run.
R7. The rules are judged by re-measurement: the same per-package table one
week after the last fix bead lands, against this document's table and its
attached JSON. Timer t107 (20 Oct) already reads landing timings; its card
takes the comparison (this author could not read the timer as a builder --
listing everyone's timers is the operator's -- so the re-measure bead
confirms and extends it rather than assuming it).

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
  per-test cap would flake under load (run 3 moved store by x2.5).
- Test-level selection (finer than packages): `go test` runs packages, and
  the measured waste is whole slow packages, not tests within fast ones.
- A new script flag for skipping race legs: the roles already skip the
  rerun on an unchanged sha, so R4 needs text, not a flag.

## 5. Open questions

1. The budget numbers: 60 s per package and 500 ms per sleep, as proposed,
   or looser (120 s / 1 s)? Tighter means eight packages over budget now
   and six fix beads (B2-B7 cover all eight, deploy with B7); looser
   leaves the wall at ~120 s and the sleeps where they are. Take 60/500:
   the fixes are ordinary parallelism, and the allow-list carries the tail.
2. May a landing that changes code run less than the whole suite? (a) Yes,
   as R5+R6: the incremental selection plus the three always-run suites at
   landing, the whole suite on a quiet machine with a red-base event.
   Cost: the base can be red between quiet runs with no bead to send back,
   and B10 is new machinery to build and watch. Gain: landing wall 283 s
   toward 110-150 s, landing machine about 1,340 s toward about 700 s per
   bead beside the socket leg, shorter lock queues. (b) No: the yardr-1vt3
   condition stands (the whole suite stays one command and gates every
   landing that changes code, per mckean's pick on ask 2847), every code
   landing stays whole,
   and R5/R6/B8/B10 close as wont-fix; the budgets still pay (plain about
   75 s, landing toward 100-150 s wall as packages hit budget, at full
   machine cost). Take (a): the forced replay shows the selection plus the
   suites covers the measured hole at a known 7-30 s depending on load,
   and the quiet backstop
   keeps one whole-suite verdict on the base. The timer variant is not
   offered: mckean declined it.
3. The incremental landing (R5), if question 2 is yes: now, or after the
   fix beads? The script change alone saves about 150 s of wall and about
   600 machine-s per code landing (at 100 landings a day, about 4 hours of
   wall waits and about 17 machine-hours) with the hole closed by the
   always-run suites; waiting adds nothing but delay. Take now.
4. The builder and review legs (R4): restate one-plain-race-leg-per-sha
   as the depot's rule and close the repeat-pass gap, as proposed, or
   leave the roles as the only text? The landing always runs either way
   (R4 excludes it). Leaving the roles as the only text risks nothing
   measured -- the sample shows zero same-sha review reruns -- but repeat
   builder passes have no rule at all (dx5f ran one sha three times).
   Take the proposal: one short docs bead (B9) pins the practice where
   the re-measure can check it.
5. The whole-suite backstop (R6), if question 2 is yes: a quiet-machine run
   that raises an event on red, as proposed, or never scheduled (the gaps
   are closed, the command stays manual)? "Never" is simpler and honest if
   the re-measure agrees; the quiet run catches what the table's rows and
   the always-run list miss, like a new black-box suite nobody registered.
   Take the quiet run, and let the re-measure argue for never. If question
   2 is no, there is no backstop question: the landing is the whole suite.
6. The 60 s owner end-to-end test: a test-only tick setting (take it;
   `YARDR_TEST_OWNER` is the precedent), allow-list the 60 s (assembly can
   never meet R1 then), or move the test to the quiet leg (a new leg for
   one test)?

## 6. The beads it would become

In landing order. B2-B7 are independent of each other and can build in
parallel; B1 first, since it writes the cap and the allow-list the rest
delete from; B8 after B1 and after question 2 is answered yes; B9 after
B1, since it writes into `docs/testing.md`; B10 after questions 2 and 5
are answered yes and quiet-run; B11 after the last of B2-B10 lands.

B1. The gate enforces the budgets. Goal: R1 and R2 as script behavior plus
`docs/testing.md` carrying all seven rules. Where to look: `.yardr/test`
(the `again` wrapper, which already times steps), this design's tables for
the initial allow-list (8 packages with reasons -- cli, assembly, store
and dispatch over even alone; root, isolated-yard, deploy and release
over only in full runs -- and the 8 sleep sites of section 2.1).
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
after every write while still pinning the invariant. Where to look: the
600-step seeded workload in `internal/store/sessionindex_test.go`, which
compares the full index against a walk after every write over 13 op kinds
(claim, start, handle, die, release, close, reopen, stop, clean, backdate,
prune and kin) with two index-dropping reopens. Done when: the comparison
runs after every 10th successful write (about 60 comparisons over the 600
steps) and after the first success of each of the 13 op kinds, the two
reopens and the last write, so every op kind is compared at least once
and the seen-coverage gate (a running pane, a shared pid, an unstopped
session, a closed workspace, a pruned session) stays; the package floor
is at most 60 s as B2; B1's entry is deleted.

B5. The root package shares one repo walk, to 60 s. Goal: the three
`TestNoGoCodeNames*` tests (31 s together) walk the repository once and
match thrice; `TestYardTestSelects` (29.6 s) stays but runs beside them
(`t.Parallel`). The package is 31 s alone and 65-94 s in full runs, so
the budget is met in the full run, where contention multiplies it. Done
when: as B7 (three consecutive `-count=1` full-suite runs at most 60 s),
for root; B1's entry for root is deleted.

B6. The assembly owner test leaves the real minute, package to 60 s. Goal:
per open question 6's pick, a test-only tick shortens the owner watch in
the end-to-end test while the real CLI path stays exercised; the rest is
already largely parallel. Done when: the package floor is at most 60 s;
the e2e still starts the server through the real CLI and still fails if
the watch is removed; B1's entry for assembly is deleted.

B7. The integration suites hold still and under budget, to 60 s. Goal:
contrib/release (36-121 s across runs -- find whether the swing is build
cache, parallelism, or the notarize step, and pin it),
contrib/isolated-yard (30-92 s) and contrib/deploy (22-61 s, 39 tests
each starting a server of its own, one `t.Parallel` -- parallelize
following the assembly train-yard shape after verifying no shared state
beyond the one shared built binary). All three pass alone; the budget is
met in the full run, where contention multiplies them. Done when: three
consecutive `-count=1` runs of each in the full suite land within 20% of
each other and at most 60 s; B1's three entries are deleted.

B8. The landing runs the incremental selection plus the black-box suites.
Goal: R5 as script behavior. Needs open question 2 answered yes; on no,
close as wont-fix. Where to look: `whole()` in `.yardr/test` (the Go-free
early-out is the shape to extend), `contrib/assembly/merge-local` (the
test invocation and timing lines), `docs/line.md` and
`contrib/assembly/README.md` for the landing docs. Done when: the ten
replayed landings select as in section 2.2; a landing on a fixture branch
runs the selection plus server-yard, isolated-yard and release and says so
in the log; the forced replay of section 2.2 (98 s wall quiet, 91 s
bare, notes 5966-5967) is the wall reference within 20% under quiet
conditions; the socket leg still follows the 022s gate.

B9. One plain race leg per sha, as the depot's rule. Goal: R4 as text
plus the repeat-pass gap closed, with no new script flag: the reviewer
and security roles already skip the rerun on an unchanged sha
(packs/starter/roles/reviewer.md, review-security.md) and the 11-bead
sample shows zero same-sha review reruns, so the rule restates that
practice as the depot's in `docs/testing.md`, requires each note to say
`reran` or `names this tip` so the next sample can tell, and covers
repeat builder passes (dx5f ran one sha three times across hold and
unhold: a pass with no new commit since a green line does not rerun). A
stage that committed -- a review fix, a security fix -- still runs the
suite on its new sha. The rule's scope stops at the assembly: the landing
always runs, and the text says so. Done when: `docs/testing.md` carries
the rule with the reran-or-names requirement and the landing-always-runs
exclusion (citing merge-local's every-time run and the landing gate's
difference from any plain run); the roles are verified sufficient as they
stand, by reading; B11 counts same-sha reruns in its window.

B10. The whole suite runs on a quiet machine. Goal: R6 as machinery. Needs
open questions 2 and 5 answered yes and quiet-run. Where to look: the
quiet-run evidence in section 2.1 (148 s wall at load 3.5-6.6, green),
`docs/timers.md` (why a timer is the wrong shape -- mckean declined it;
the trigger is load, not time), the red-base event precedents (find what
raises an event today), `.yardr/test` `whole()` (the suite it runs). Done
when: a load trigger runs the whole suite over main when the machine is
quiet (the bead states the threshold it measured, with no race leg
running as part of it); a red base raises an event naming the failing
packages and the base commit, proven by a drill against a worktree at a
historical red commit; one quiet whole-suite run over main is logged with
its wall and load.

B11. Re-measure in a week. Goal: R7 -- the same table, the verdict on the
rules. Where to look: this document's table and attached JSON, timer t107
(20 Oct, landing timings -- confirm what it reads and fold the comparison
into its card), `refinery.log`. Done when: the per-package table is taken
again both ways (beside load and quiet), the deltas per package and the
new per-bead cost are on the bead as a note, the same-sha rerun rate is
counted against R4, and every rule that missed is a filed follow-up or a
closed wont-fix with the number.
