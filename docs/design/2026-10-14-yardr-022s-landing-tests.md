# What the yardr landing run's minutes buy, and a smaller suite that keeps the same safety

Design yardr-022s, proposed 2026-10-14. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question.

mckean, 14 Oct 2026 (mail 29869): "perhaps we could dispatch a design bead to
evaluate the possibility of consolidating, simplifying the test suite? maybe
we are over testing?" The context is the yard's own machine, 10 cores and 24
GB, running four builders and the refinery at once: memory-bound at load
50-70, with three load-only flakes failing one landing that day (yardr-2nss).
The question for this design is what each minute of the landing run buys, and
whether a smaller suite keeps the same safety.

## 2. What exists today.

### The landing run

A yardr landing runs the depot's test command, `.yardr/test`, with
`YARDR_LANDING=1`, using the copy on the base (contrib/assembly/merge-local).
Its `whole()` function runs gofmt, `go vet ./...`, `go vet` for linux, then
`go test -race ./...` with the socket leg beside it: `env YARDR_TEST_RPC=1 go
test ./internal/store/ ./internal/dispatch/ ./internal/bdp/` starts in the
background and its output is printed when the first leg ends, so a landing
lasts as long as the slower of the two legs, not their sum. The beside-run is
recent: commit 07f5d8a (yardr-9xd9), 13 Oct. A failing `go test` step reruns
its failed packages once and names the first run's failures as flaky (commit
3b96a85, yardr-fk4h, 14 Oct). A landing that changed nothing compiled runs the
plain run instead: the packages the branch touched, their importers, and the
packages the `reads` table says read a changed file.

Wall time is therefore gofmt plus both vets plus the slower leg. The socket
leg never wins that race, so it contributes no wall seconds; its cost is the
load it adds beside the first leg. Six recent landings, from
~/.yardr/logs/refinery.log:

| landing | total | gofmt+vet+linux vet | -race leg | socket leg |
|---|---|---|---|---|
| yardr-asdg 1162fb1 | 522 s | 108 s (by subtraction*) | 414 s (cache retry) | 184 s |
| yardr-pat1 1bec422 | 665 s | 32 s (by subtraction*) | 633 s | 144 s |
| yardr-2nss 8811758 | 712 s | ~50 s | 662 s | 153 s |
| yardr-fk4h 3b96a85 | 729 s | ~46 s | 683 s | 271 s |
| yardr-1t7y 9be3211 | 479 s | ~40 s | 439 s | 214 s |
| yardr-ykkq 027e5cc | 318 s | 21 s | 297 s | 141 s |

(The bead's "184 s, 30% of a landing" was measured on asdg but reads as
additive; with the beside-run the socket leg's 184 s overlapped the -race
leg's 414 s. Its first attempt also shows the shared build cache failing
under load: five packages failed from lost cache files and passed on the
script's retry.)

(* The kept output for asdg and pat1 lacks the gofmt and vet lines, so their
middle column is the total minus the slower leg. asdg's 108 s is plausibly
the same cache-loss storm slowing vet's builds; the other four landings show
21-50 s with the lines present.)

### Size

At 9be3211: 159,174 lines of `_test.go` against 121,084 lines of product Go.
Tests per package (`^func Test` count, test lines):

| package | tests | test lines |
|---|---|---|
| internal/cli | 721 | 41,452 |
| internal/store | 629 | 39,435 |
| internal/dispatch | 328 | 17,153 |
| internal/web | 178 | 9,941 |
| internal/runner | 144 | 7,970 |
| internal/pack | 136 | 7,321 |
| internal/hooks | 98 | 5,737 |
| internal/rpc | 47 | 4,276 |
| contrib/assembly | 62 | 2,590 |
| contrib/deploy | 35 | 1,388 |
| root | 10 | 997 |
| contrib/isolated-yard | 12 | 733 |
| contrib/release | 12 | 642 |

Heaviest packages in the ykkq -race leg (297 s wall, ~1,456 s of package
time, ~4.9x parallel on 10 cores): cli 196 s, release 165 s, store 148 s,
dispatch 140 s, isolated-yard 101 s, root 99 s, assembly 82 s, deploy 59 s,
runner 56 s, web 55 s, chat 43 s, hooks 34 s, server-yard 32 s, pack 30 s.
The contrib script suites (assembly, deploy, isolated-yard, release,
server-yard, peer, bench, shot, github-mint) add to ~497 s of package time,
about a third of the leg; the big four alone (release, isolated-yard,
assembly, deploy) are 407 s.

### The socket leg

`internal/storetest/storetest.go`: with `YARDR_TEST_RPC=1`, `New` returns an
`rpc.Client` talking to the store over a real unix socket, so every suite
that builds its store that way runs the same calls through JSON over HTTP:
argument and result encoding, the server handler's dispatch, per-call
timeouts. 71 test files (153 call sites) across store, dispatch and bdp use
`storetest.New`; store's tests also open 88 stores directly (migrations and
other file-level tests that never cross the socket). The leg's wall time
above, 141-271 s, is fully overlapped by the first leg.

### The race detector

The `-race` multiplier, measured for this design: the full suite with
`-count=1` on a warm private build cache, `-p 10`, loaded machine (load
55-62), took 474 s wall with `-race` and 264 s without, 1.8x; package-time
sums 2,880 s vs 1,540 s, 1.9x. The no-race run compiled its binaries cold
(the cache was warmed with `-race`), so the true multiplier is at least
that. Per package (`-race` time, plain time, multiple): store 271/68/4.0x,
yards 47/11/4.2x, web 99/34/2.9x, root 223/84/2.7x, pack 78/30/2.6x, chat
77/30/2.6x, rpc 36/15/2.4x, cli 370/190/2.0x, dispatch 261/147/1.8x,
release 339/209/1.6x, hooks 90/57/1.6x, server-yard 74/48/1.6x, deploy
142/104/1.4x, assembly 193/147/1.3x, isolated-yard 225/171/1.3x, runner
77/61/1.3x. The detector's cost concentrates in store (about 200 s of
package time), web, root and cli; the process-based contrib suites pay
about 1.3x, since `-race` slows the driving binary but not the processes
it starts.

Which packages have concurrency worth racing (non-test code only):

| | packages |
|---|---|
| goroutines in product code | cli (43 `go` sites), store (14), bench (7), server (6), runner (4), dispatch (3), rpc (3), chat, config, source, rpc/clientgen and the isolated-yard and server-yard fakes (1 each) |
| sync but no `go` (locks guard what callers' goroutines touch) | bdp, hooks, peer, version, web, yards, scripttest, standin |
| pure: no sync, no `go` | root, assembly, deploy, shot, harnesses, agents, depotfile, flowdraw (+gen), limits, notify, otel, pack, pairing, yardkey, packs, rpc/gen, isolated-yard/sources, plus the test-only contrib suites |

The heavy packages are all concurrent or process-based; the pure ones are
fast. Dropping `-race` for pure packages can only save their share, and a
race is reported where the memory is shared, so a pure package's tests can
still trip a race in a concurrent library they link.

### Parallelism

Tests run sequentially inside a package: 8 uses of `t.Parallel` in the whole
suite. Package parallelism (`go test -p`, default = GOMAXPROCS = 10) is the
only knob, and it caps build and link concurrency too. A running `-race` test
binary holds tens of MB (about 40-80 MB RSS seen); a big compile holds
hundreds (the store compile reached 420 MB RSS). Measured for this design,
`go test -race -count=1 ./...` on a warm private cache (the shared cache
kept losing files under load, so all four runs used a private GOCACHE),
same worktree, back to back:

| `-p` | wall | exit | load during | package-time sum | effective parallel |
|---|---|---|---|---|---|
| 4 | 631 s | 0, 40 ok | 70 down to 35 | 2,007 s | 3.2x |
| 6 | 634 s | 0, 40 ok | 35 up to 55 | 2,500 s | 3.9x |
| 10 | 474 s | 0, 40 ok | 55 to 62 | 2,880 s | 6.1x |

Lower `-p` makes each package faster (less contention: the sums fall from
2,880 s to 2,007 s) but the wall slower: `-p` 4 and 6 cost about 160 s
(+33%) against `-p` 10. Efficiency falls with parallelism (80% of 4,
65% of 6, 61% of 10) yet parallelism still wins on wall. The sketch's hope
that `-p` 4 costs less wall than it looks is not borne out here: it costs
about what it looks like. A quiet-machine comparison was not measured; the
machine never went quiet (load 35 and up throughout), and landings always
run beside builders anyway, so the loaded case is the case that matters.
Per-package times swing widely with background load (release took 414 s in
the `-p` 4 run against 165 s in the ykkq landing), so the ~160 s carries
that noise, but the ranking held across all three runs with the middle
point confirming it.

### What the suite caught: eleven days of landings

`~/.yardr/logs/refinery.log` covers 3-14 Oct: 661 merges and 64 red landing
attempts, about 725 attempts in all. Every red attempt was classified from
the log and the bead's notes:

| cause | attempts | beads |
|---|---|---|
| rebase conflicts | 36 | zn62, m4dw, sbv3, bmc6 x2, 8btj, 822r, n63q, 8j3n, 85jy, ahgv, dskf, k3dy, fhda, r1k9 (1st), 87dd (1st), en6b, wav4, bcx5, 3wt9, vsen, 3xtx, b42p, 5kfj, pxsv x2, 765d x2, a77f, zkhr, c9z6, 33s8, ps6s, 3w5g, n98q, n9jh |
| push races (main moved mid-landing) | 2 | jd27, spc4 (2nd) |
| infrastructure (SIGTERM, exit 143; build-cache loss) | 2 | b1ys (filed z0bw), n1v5 |
| flakes and pre-existing test bugs, branch unchanged | 15 | ygyf (zn62), ca0d (sfmd), jcw1, r1k9 (2nd, 1abw), b8ee (1abw), 87dd (2nd, sxw6), 4ktb (h991), spc4 (1st, wpk8), 0qs5 (gdes), 1jn0 (gdes), vgdm (ywkn), ngc1, mmh8 (ykhn), dafd (6tnp, a real race, see below), 2nss (3 load flakes) |
| rebase composition, test-only fix | 7 | 8tn3 x2 (refusal vs main's tests; vet: Advance signature), 5cz6 x2 (vet: Release signature; herdr socket fake), 4f2e x2 (main's new tests vs branch behaviour), mkfn (m32z's tests vs branch guard) |
| real catches | 2 | 6k0b, 2c8b (below) |

The two real catches, and the race:

- yardr-6k0b: the branch (plus its review) made the herdr runner refuse to
start an agent whose pane shell is unknown. Nothing the plain run tests
imports that contract, but contrib/server-yard's stand-in herdr answers
`pane process-info` with an error, so its suites failed at the landing.
Fixed in 1b2590b (the stand-in answers with its pid; a comment in
internal/runner/herdr.go). Only the full suite saw it, because only the full
suite runs a package that imports nothing that changed.
- yardr-2c8b: TestPutHookChecksItsCIReads failed in the socket leg only. The
cause was a product bug, not the transport: `checkSays` walked the says
filter map in range order, so a line with two unknown keys was refused for
either one depending on the run (reproduced 5/20 with `-count=20`). Fixed in
b69ed9e (sorted key walk). The socket leg caught it by seed luck: the same
logic runs in-process in the first leg, which drew the good order that run.
- yardr-dafd: the `-race` leg reported a genuine data race in
TestDaemonFollowsAChangeOfTheHerdrSession (the interval global read from a
reconnect goroutine); the branch touched no runner code. Filed as yardr-6tnp
and fixed on main (1000903, 84e89bb).

The 7 composition fixes share one shape: the landing tests the rebased
branch, and the rebase is the new information, not the scope. All seven
would fail a plain run on the rebased branch too. The full suite's unique
value in eleven days is the two catches above: one needed full scope (6k0b),
one needed a second run with a fresh map seed (2c8b).

### Inside cli and store

Thirty tests sampled at random from each of internal/cli (721 tests) and
internal/store (629). Median length about 35 lines; the longest in the sample
are walkthroughs (TestDoctorSetUpYard, 125 lines over eight setup steps;
TestSchema2StoreMigratesTo3, 132 lines). Setup is in-process: `storetest.New`
(Bolt in a temp dir), commands invoked through `run(t, cmd, ...)` rather than
a built binary, `httptest` stand-ins for HTTP (15 cli files), fake tools on
PATH for git/herdr. Markers of weight across cli, store and dispatch tests:
73 `Sleep` calls (nearly all 1-200 ms; five reach 300 ms to 2.5 s), `exec.Command` in
19 files (mostly the fakes; one `/bin/sh` for environment inheritance), no
test that starts a yard or a server. Same-path-different-words redundancy was
not found: the closest pairs share a helper and differ in the facet they pin
(for example the two `runask_pane` tests use different panes, different ask
kinds, different expected messages, and check both the refusal and what it
left unchanged). The 41k/39k lines are many small tests, not bloated ones,
and the names are one behaviour per test, in the codebase's style. There is
no cull here; the lever is a rule that stops the growth.

### The roles

`packs/starter/roles/builder.md` line 25, identical in the contributors and
design packs: "Every bug you find or fix gets a regression test."
`packs/starter/roles/reviewer.md` line 13, identical in the same packs:
"tests test behaviour, and every fixed bug has a regression test", plus "Fix
forward ... Include missing tests". Both roles push test count up; neither
says anything about a test's cost: its seconds, its flakes under load, or
preferring the cheapest setup that pins the behaviour.

### The backstop

`.github/workflows/ci.yml` runs on every push to main: `YARDR_LANDING=1 sh
.yardr/test`, then plain `go test ./...`, then `YARDR_TEST_RPC=1 go test
./...` over every package. Anything the landing stops running still runs on
main after it lands, minutes later; a failure there is a bead, not a blocked
landing.

## 3. The proposal.

Keep the landing whole, and make it cheaper and quieter. If the operator
takes the draft's picks in section 5, the change is two small ones, each
carried by a mechanism the repository already has. (The questions stay the
operator's; what follows is the shape the answers would take.)

1. Run the socket leg only when the plain-run selection already includes
store, dispatch or bdp. The leg re-runs exactly those three suites; when a
landing's plain selection has none of them, over-the-socket behaviour cannot
differ from what the base passed except through a package none of the three
imports, which is the same reach the plain run already accepts everywhere
else. The condition reuses `pick()`: no new file list, no new table. A
change to internal/rpc still runs the leg, because the three suites import
the rpc client and land in the selection as importers. Since the leg runs
beside the first one, this saves no wall seconds; it saves the leg's three
heavy test binaries' share of CPU and memory on about half of landings
(27% of recent commits touch rpc, store, dispatch or bdp; a landing touches
the union of its commits). What it stops catching on the skipped half is a
transport-specific failure, of which the log shows none in eleven days; CI
still runs the socket suites over every package on main after each landing.
2. Say in the builder and reviewer roles what a test may cost. After the
"regression test" line in the three builder.md copies (starter,
contributors, design packs) and after the checklist line in the three
reviewer.md copies, the same rule:

> A new test earns its seconds and its stability: drive the behaviour
> through the cheapest setup that pins it (a function call, then a
> throwaway store, then a stand-in; a process, a sleep, or a build only
> where nothing cheaper reaches the behaviour), and wait on the effect,
> not on wall time. A test that fails only under load is a bug in the
> test until shown otherwise; file it from the flaky line and fix it
> there, not on the bead it blocked.

What stays, deliberately: every contrib suite on every landing (the 6k0b
catch came from a suite that imports nothing the branch changed (the next
contrib change's plain run would find such a break days later instead of
minutes)); `-race` over every package (the 6tnp race; the pure packages'
share is small and their tests still link concurrent libraries); and the
default `-p` (measured above: `-p` 4 costs about 160 s of wall for its
smaller footprint, and the refinery lands about sixty times a day, so wall
is throughput).

## 4. Left out.

- A changed-package condition, or a nightly, for the contrib script suites.
This would save about a minute of wall on most landings (the big four are
407 s of package time and release is often the longest pole), but 6k0b is
what it stops catching: an end-to-end behavioural break that no importer
edge reaches. Open question 3 prices it for the operator to weigh.
- A nightly `-race ./...` with a smaller race scope at landing. CI already
runs the whole suite, with and without `-race` and over the socket, on every
push to main; a second scheduled suite adds machinery for what CI covers.
Moving `-race` off the landing would trade the gate (which refuses the bead)
for an observation (which files a bead after the code is on main).
- A `-race` allowlist by package in `.yardr/test`. The saving is bounded by
the pure packages' share of package time, which is small because the heavy
packages are all concurrent or process-based, and the list would rot: a
package grows its first goroutine without updating the script. The detector's
record in the window is one genuine race found (6tnp).
- Rewriting or culling tests in cli and store by hand. The sample shows
small, in-process, distinct tests; a cull would trade review-sized effort for
seconds, and the count would grow back without the role rule proposed above.
- Changing what the plain run tests, and footplate's and signalbox's suites:
out of scope on the bead.
- A private build cache per run. Observed while measuring: the shared
~/Library/Caches/go-build loses files under concurrent load (asdg's first
attempt failed five packages on it; the author's own first comparison runs
failed the same way; n1v5's landing failed on it outright). That costs
retries and whole landings, but it is a reliability fix of its own, not a
suite-size question; it wants a bead of its own if the operator wants it.

## 5. Open questions.

1. What `-p` does the landing run at?
   - 10 (the default), as proposed. Measured 474 s wall here.
   - 4. Measured 631 s wall here, about 160 s (+33%) dearer, for a peak of
   4 test binaries instead of 10 beside the builders: citizenship bought
   with wall. 6 measured the same wall as 4 (634 s), so there is no middle
   ground in this data.
   - The draft would keep 10: the refinery lands about sixty times a day
   and the 160 s is throughput, while the contention relief the sketch
   wanted from `-p` comes without wall cost from the conditional socket
   leg. If the operator values a quieter machine over faster landings,
   `-p=4` on both legs is one bead (spelled with `=`: see the trap in the
   beads section).
2. Is the socket leg conditional on the plain selection including store,
dispatch or bdp?
   - Always, as today. Costs: three heavy test binaries beside the first
   leg on every landing; no wall seconds either way. Catches a
   transport-specific failure before the code lands; none seen in eleven
   days.
   - Conditional, as proposed. Saves the contention on about half of
   landings. A transport-specific failure lands and is caught by CI on main
   minutes later, as a bead.
   - Dropped entirely. Saves the contention on every landing. Loses the
   in-landing wire coverage; CI remains the only socket run.
   - The draft would take the conditional leg: it keeps the coverage where
   the change can reach it and costs nothing where it cannot.
3. Do the contrib script suites stay on every landing?
   - Always, as proposed. Costs about a minute of wall on most landings.
   Keeps the end-to-end net that caught 6k0b.
   - Conditional on the plain selection (contrib changed, or a package the
   suites import changed). Saves that minute on most landings for the
   import-nothing suites (release, isolated-yard, server-yard, shot); the
   suites that import the product (assembly, deploy, peer) still run on
   most landings anyway. A 6k0b-class break lands and waits for the next
   contrib change's plain run, days later.
   - Moved to a nightly scheduled workflow. Same saving; detection waits up
   to a day and needs the nightly machinery plus its failure bead.
   - The draft would keep them always: one real catch in eleven days is a
   cheap minute.
4. Does the landing keep `-race ./...`?
   - Yes, as proposed. Costs about 210 s of wall here (474 s with `-race`
   against 264 s without, 1.8x; store alone pays 4x). Keeps the detector
   on the gate; record in the window is one genuine race (6tnp).
   - `-race` for concurrent packages only, plain `go test` for the rest.
   Saves the pure packages' race overhead, bounded and small; adds a list
   in the script that rots.
   - Plain tests at landing, `-race ./...` nightly. Saves the whole
   multiplier at landing; races land and are filed the next day. CI already
   runs `-race` on main after each landing, so the nightly adds little over
   what exists.
   - The draft would keep `-race ./...`: the measured multiplier is the
   price of the gate, and the saving alternatives either save little or
   duplicate CI.
5. The role rule's wording, as proposed in section 3?
   - As written. It names the setup ladder, the wait-on-effect rule, and
   the load-flake presumption.
   - Shorter (the ladder only). Less to follow; loses the flake rule that
   recent load failures kept re-teaching.
   - The draft would take it as written: every clause answers a failure the
   log shows more than once.

## 6. The beads it would become.

In landing order; each is small enough to build without asking.

1. Title: "The landing's socket leg runs only when the plain selection holds
store, dispatch or bdp".
Goal: in `whole()`, compute the plain selection with `pick()` and start the
`YARDR_TEST_RPC=1` leg only when it names at least one of the three socket
suites; otherwise print one line saying why the leg was skipped. The first
leg and the plain run are unchanged.
Where to look: `.yardr/test` (`whole()`, `pick()`, the `reads` table);
`yardtest_test.go` for the plan and fake-go tests.
Done when: a landing-mode plan over a fixture that touches only docs shows
the skip line and no socket leg; a fixture touching internal/rpc shows the
leg (as an importer effect); `sh .yardr/test` passes.
Depends on: open question 2. Nothing else.

2. Title: "The builder and reviewer roles say what a test may cost".
Goal: the rule proposed in section 3, in the three builder.md copies and the
three reviewer.md copies that carry the regression-test lines
(packs/starter, packs/contributors, packs/design), worded identically.
Where to look: the six role files; the packs tests that read role content
(packs/packs_test.go, internal/pack/*_test.go).
Done when: all six files carry the rule; `sh .yardr/test` passes.
Depends on: open question 5. Independent of bead 1.

If the operator picks `-p` 4 (question 1), the conditional contrib suites
(question 3), or the narrowed `-race` scope (question 4) instead, each is one
more bead of the same size in `.yardr/test` with the same done-when shape
(plan output, `yardtest_test.go`, green run), landing after bead 1. One trap
for the `-p` bead: `again` rebuilds the flag list for a flaky rerun from the
step's words by keeping every word that starts with `-`, so a `-p 4` spelled
with a space would come back as a bare `-p` eating the first package; spell
it `-p=4` or teach the rebuild about valued flags.

