# The test rules

Seven rules keep the suite fast, each with the number that justifies it
(docs/design/2026-10-16-yardr-635g-test-time.md measured them). The gate is
the depot's test command, `sh .yardr/test`: builders and reviewers run it in
their worktree, without `-race`; the assembly runs it with `YARDR_LANDING=1`
on what lands, with it (R4). R1 and R2 are script behavior: R2 is enforced
on every run, R1 on every race leg; the rest are rules the gate, the roles
and the assembly keep between them.

An allow-list in `.yardr/test` names what is over budget today, each entry
with why and which bead brings it under. A fix deletes its entries; a row
whose site is gone is deleted with the fix, not left for later. One row has
no fix: `contrib/isolated-yard`'s, since the yard-shape matrix is long by
design and runs only in the quiet run (R6).

R1. A package's tests finish in 60 s under `-race`, or the gate fails.
Only a race leg is judged, and only the landing and the quiet run (R6) have
one (R4): a builder's or reviewer's run, and a landing that changed nothing
compiled (R5), test their selection without `-race` and so reach no R1
verdict. A package that slips over 60 s shows at its landing, not before.
The wall is the slowest package. When a non-allow-listed package takes over
60 s in the race leg, the gate runs it again alone under the leg's lock.
Several over-budget packages are re-run up to three at a time, and every
one is judged by the single load pair sampled around the batch. With
`YARDR_R1_STRICT` set, which only the quiet run does (R6), they are re-run
one after another instead, each judged by the pair sampled around its own
re-run, so a failure line names the load that package sat in: three race
suites side by side make a load and times of their own, and a run that
fails a package over 60 s alone however loaded the machine would convict
it for those (yardr-t4c0).
Over 60 s alone fails the run and names both times; 60 s or less passes
with a `test: R1:` line naming both times and marking the first run as loaded.
Load multiplies package times up to ~2.5x, so the first run's time by
itself does not fail the gate.
The re-measurement can be loaded too: the race lock holds off the other
gate legs, not every test on the machine. Over 60 s alone on a loaded
machine -- the one-minute load average past the core count -- passes with
a `test: R1:` line naming both times and the load instead of failing
(yardr-wq5h), unless `YARDR_R1_STRICT` is set by the quiet run (R6): then
over 60 s alone fails however loaded the machine. The load is sampled when
the re-runs start and again when the last ends, and the machine is loaded when
either sample is (yardr-f5f7): the average lags, so the end sample alone
read a load that dipped before the check as quiet, while the start sample
holds the loaded minutes the re-run was about to sit in. The line names the
start sample's load where that is loaded, else the end's. On a loaded
machine, the failure line names both times and the load beside them; on a
machine quiet at both samples it names both times. Unknown load counts as
quiet and fails as before. A landing's `-p` follows the same load: past the
core count the race leg runs `go test -race -p=N`, N the cores squared over the
load, rounded down (at least 2, at most the cores), and logs one `test: go test
-p=N (load A on C cores)` line; a quiet or unknown load runs go's default and
logs nothing (yardr-y6js).

R2. A test sleeps no more than 500 ms at a site, and a poll loop polls a
condition with a deadline. Exactly 500 ms complies; over fails unless
allow-listed. A test that waits on time takes a clock it sets: the fake
clocks the packages already own (the dispatcher's clock, which tests set;
`Receiver.SetClock` in hooks and yards; the peer clock in the dispatch
tests). A test that must use the real clock is allow-listed with its
reason, and the gate counts those entries. The gate reads literal
durations in `*_test.go` files; a sleep it cannot read, such as a
variable, is not judged and still owes the budget. A test waits for the
effect it asserts: it polls the condition (a store event, a file, a held
question, a ticket) with a deadline that is only a fuse against a hang,
never the measurement. In the contrib suites that fuse is written for an
idle box and goes through `testload.Scale`, which multiplies it by the
larger of two factors, each the machine's load over its cores from 1 to
4: the one the race leg is given (`YARDR_TEST_LOAD`; `.yardr/test` reads
it as the leg starts and names it in a line), and the machine's own live
load, which `testload` reads as the fuse is armed, in a plain run too
(`/proc/loadavg`, else `sysctl vm.loadavg`; a load it cannot read counts
1). So a fuse is sized to the load the suite runs in, not the load its leg
started in, and is not raised by hand after it flakes in a landing. The
deadline a test gives the program under test is that program's input, not
a fuse, and is not scaled. Where the code owns the time the test waits on,
the test sets that clock -- the fake clocks named above -- and advances
it. A deadline taken from the load is the last resort, and only where
the thing waited on is a program outside the test (a harness binary, a
browser): there the bound is product code, its failure under load is a
product question, and the test stubs the bound long (as the doctor
tests stub thirty seconds) instead of inheriting whatever load leaves
of it. Where a test must still tell a slow machine from a slow program,
as contrib/shot's real Chrome does to skip a miss of its deadline, it
asks `testload.Loaded`: the factor above 1, the leg's or the machine's
own as it is asked, so a plain run on a loaded box is found loaded too,
and a load that cannot be read is not. A test whose result depends on
that factor pins the machine with `YARDR_TEST_LOADAVG` and
`YARDR_TEST_CORES`, as the gate's tests do, so that it does not turn on
what else the machine is running. A count inside a fixed
wall-clock window, and a sleep followed
by asserting the slept-for event exists, are both the first shape
written wrong. A reviewer checks the pick without running anything, as
the sleep scan is checked: a diff that adds a test names one of the
three shapes for every new sleep over the scan, every count asserted
inside a fixed window, and every sleep-then-assert on the slept-for
event -- the polled condition and its fuse, the set clock, or the
stubbed product bound with its bead. The worked example is yardr-x51w's
peerhold fix (`internal/dispatch/peerhold_test.go`): wait for six polls
in whatever time the machine takes, and keep only the one elapsed-time
assertion (at least 200 ms over five 50 ms pauses) whose direction load
cannot falsify.

A fix for a test that fails only under load is proved the way it failed:
`contrib/burn/burn -n 100 ./internal/dispatch TestName` runs the test a
hundred times beside five times as many busy loops as the machine has cpus
(contrib/burn/README.md; `-x` sets the factor, 5 by default), first on the
code as it was, to see it fail, then on the fix, and the note names the
command, the counts it printed and the load average before and after. A fix
for a data race is proved the same way with `contrib/burn/burn -r`, which
builds the test binary with the race detector even if GOFLAGS contains
`-race=false`. Without `-r`, burn does not request the detector, though an
inherited `GOFLAGS=-race` can still enable it; use `-r` so the command says
the detector is part of the proof. The loops are niced, but they still raise
the load average, and the builder routes wait on `machine.load<80`, so that
default factor runs outside a session, when the line is idle. Inside a
builder's or reviewer's session the burn runs at `-x 1` and names the one
suspect test (`contrib/burn/burn -x 1 PACKAGE TEST`, with `-r` for a race),
the rule in `.yardr/depot.md`.

A failure seen at a load of several times the cpu count may not show in a
burn at `-x 1`, which runs as many loops as there are cpus. A fix therefore
has two burn proofs, and its note records both, each as done or outstanding:
the in-session one (the command, the counts and the load it printed) and the
full-factor one (the default `-x`, the same test and count). The
full-factor burn is run by whoever is idle outside a session, when the line
is idle: the yardmaster on ask, or the operator. Its command, counts and
load go in a note on the fix's bead, and a full-factor burn that fails files
a new bead. A fix lands on the in-session proof with the full-factor one
outstanding: the assembly gate runs no burns, so landing never waited on
them, and the note, not the gate, carries what is still owed.

R3. A package over the R1 budget with over 100 tests runs its tests
parallel: `t.Parallel` on every test that owns its state, allow-list for
the rest. That binds the volume debts -- internal/dispatch and internal/store,
whose fix beads parallelize them following the assembly train-yard shape
(temp homes, no shared state); internal/cli was the third until yardr-ab6t
threaded its output and environment through its commands. Packages under
budget comply until they breach it, and the budget is their backstop then.

R4. The depot test gate runs `go test -race` in its landing run
(`YARDR_LANDING=1`, the assembly's, on exactly what lands; for a pull request
from outside, pr-ci's run of its head, which no landing follows) and in the
quiet whole-suite run (R6). A builder or reviewer can request an earlier race
check with `contrib/burn/burn -r` (R2). A plain `.yardr/test` run by a builder
or reviewer tests the same selection without `-race`, says so in its first
line (`testing without -race (race runs at landing only, docs/testing.md
R4)`), and so never takes the machine-wide race lock for its race, nor
judges R1, which is defined under `-race`. A plain run whose step covers
the whole suite -- `go.mod` or `go.sum`, a file no row knows, or a selection
of at least half the suite's packages -- still queues on that lock as class
1, behind landings (R5): two whole-suite runs at once overload the machine,
while a leaf run beside them is the normal tolerated load (yardr-f4tr).
Measured on 22 Oct 2026 over the last 200 beads'
notes: of 170 passing test lines, 66% of the gate's wall time (129k of
193k s) was queued on that lock, and 107 runs waited over half their run;
no bead or note among 1370 reports `WARNING: DATA RACE`. The detector still
stands in front of `main`: a race fails the landing without a rerun, and the
bead comes back to its builder. A builder who changes concurrent code and
wants the detector sooner runs `YARDR_LANDING=1 sh .yardr/test`, which
queues on the lock as a landing does.
A builder or review stage runs the plain suite once per sha. A stage whose
branch is unchanged since a green test line on that sha does not rerun it;
a stage that committed -- a review fix, a security fix -- still runs it on
its new sha. That covers a repeat builder pass too: a pass with no new
commit since a green line does not rerun (yardr-dx5f ran one sha three times
across a hold and unhold). Every note says `reran` or `names this tip`, so
the re-measure can tell a fresh run from a quoted line. A stage that
committed runs over its own commits alone, `YARDR_BASE=<the commit the last
test line names> sh .yardr/test`, so its selection is what those commits
changed and whatever imports or reads it (yardr-0cw8): a fix in a leaf
package takes seconds. A review base left behind by a rebase is compared
from the commit it and `HEAD` share, which lies before the tested commit,
so the run selects more, never less. If the named base is ahead of `HEAD`,
the merge base is `HEAD` and would select nothing, so the script says
"cannot compare" and the reviewer runs plainly from the session's base. The
assembly's runs are outside this rule: the landing always runs, on exactly
what lands, every time, including after a merger's rebase that changed
nothing
(`contrib/assembly/merge-local`: the test command "runs on exactly what
will land, every time"), and its landing gate differs from any plain
run (`whole()` in `.yardr/test`: the selection plus the six black-box
suites with `-race`, vet for linux too, the socket leg beside), so no
plain test line ever satisfies it. The test line carries the run's wall time
and race-lock wait, written by `.yardr/test` itself so every note holds
them: `test: sh .yardr/test @ <sha>: pass, <n> packages ok, 0 failed, 112 s,
waited 40 s`. The fields are the result, the packages passed and failed, the
run's wall seconds, and the seconds it queued on the machine-wide race lock:
`waited 0 s` on most builder and review lines -- a leaf selection takes no
lock -- and a wait on a landing's, or on a whole-suite plain run's that
queued behind one.

R5. The landing runs the incremental selection -- today's package pick --
plus always the six black-box suites (contrib/assembly, contrib/peer,
contrib/deploy, contrib/server-yard, contrib/isolated-yard,
contrib/release), plus the socket leg as today. Each of the six builds
the tree's binary and drives it without importing the changed packages,
so the pick alone misses them. A landing that changed nothing compiled
runs the plain selection alone, as before, without `-race`: no `.go` file,
embedded file or deletion is in it, so the binaries are the base's and only
a test that opens a changed file by path can differ, which the selection
holds. If that selection covers at least half the suite's packages, its
plain go-test step queues as class 1; a smaller selection takes no ticket
(R4, yardr-f4tr). A landing whose change cannot be selected (go.mod or
go.sum, a deletion, a file no row knows) and one with nothing to compare
with still run the whole suite. The whole suite otherwise runs only when
the yard is quiet (R6 below).
A landing's race leg takes the machine-wide race lock ahead of any other
run's (yardr-0cw8). A ticket is class 0 for a landing's race leg and class
1 for a whole-suite plain step, including an unraced landing's plain
selection; class 1 also accommodates tickets from a copy of the script
from before R4 (a branch not yet rebased). The first live ticket may take
the lock, so class 0 goes first and each class keeps its arrival order.
The run that holds the lock is never pre-empted. `sh .yardr/test
--race-queue` shows each waiter's class.

R6. The whole suite still runs, but when the yard is quiet, not on a timer
and not as a gate on any bead. The daemon's trigger
(`internal/dispatch/quiet.go`) subscribes to the event stream and judges
the yard on each event that can make it quiet or owe a run -- a bead
advancing, a bead closing, a session ending, or a change to
`quiet_landings` -- never by polling and never on a timer. A run for a
depot starts only when all hold at once:
no bead waits in `approved` for the assembly anywhere in the yard, no
bead session that runs something on this machine is live anywhere in
the yard, and at least `quiet_landings` code landings (ten while unset:
about two hours at the 60-140 landings a day of 14-16 Oct 2026) have
landed on that depot's base since its last quiet run started. The
sessions that count are the ones `max_sessions` counts
([groups.md](groups.md#how-many-sessions-the-machine-carries)): those of
a `herdr` or an `exec` group, a builder, a reviewer or an assembly
script. A `manual` group's claim holds a bead for a person (the backlog
alone holds dozens); a `peer` group's is a bead another yard works.
Neither runs anything here, so neither counts: counted, they would keep
the yard from ever being quiet (yardr-hqsn). A
threshold reached while the yard is busy stays in the log and fires at
the next quiet event. Quiet is the yard's, not the machine's: nothing
here waits for low load, and the run starts however loaded the host,
which is why it judges R1 strictly instead of loud-passing as a
landing may. The run tests the depot's base in a worktree of
its own with `YARDR_LANDING=1`, `YARDR_R1_STRICT=1` and
`YARDR_QUIET_RUN=1`, holding the
machine-wide race lock for its whole length, so it never overlaps a
landing leg, and it is
single-flight: while one quiet run runs, a further event does not start
a second, and the trigger re-arms only when the first finishes. A green
run is logged and told as `quiet_passed`, a red base as `quiet_failed`
naming the failing packages, the failing tests and the base commit, each
with its wall time and starting load. The strict setting, which only the
quiet run sets, turns off R1's loaded pass: a package over 60 s alone
fails however loaded the machine. On a loaded host its failure line
names both times and the load; on a quiet host it names both times, so a
slowdown cannot hide behind a loaded host (yardr-8gdt). It also re-runs
the packages over budget one after another, not up to three at a time, so
that the time a package is failed by is its own and not what three race
suites side by side made of it, and the load a failure line names is the
one around that package's own re-run (R1, yardr-t4c0).

`YARDR_QUIET_RUN=1`, which like `YARDR_R1_STRICT` only the quiet run sets, is
the mark a test reads when it costs minutes and belongs to no other run.
Without it the test skips and says why, so `go test ./...`, a builder's gate
and a landing stay as fast as they were. Today that is `TestYardShapes`
(`contrib/isolated-yard/matrix_test.go`, yardr-n9ss), the yard-shape matrix.
It walks a yard with a git depot, one with a directory depot and one set up
as a server by `contrib/server-yard` through the same steps: init, doctor,
depot add and depot init, a bead created, listed and shown and then worked to
its end by the stand-in agent, status, and the daemon stopped, started and
stopped again. A shape that cannot do a step does not skip it. Its table
entry holds the refusal it gets, and the matrix pins the exit status and the
start of the error. A peer-linked yard and a hosted one are not in the table
yet.

A shape takes 35 to 50 s on the yard's machine and the three run beside one
another, which puts `contrib/isolated-yard` at about 75 s under `-race` (18 s
without the matrix), over R1's 60 s. The package is on R1's allow-list for
that, and no bead brings it under, because the matrix is an acceptance test
and the quiet run is where its minutes are spent. `YARDR_LANDING=1` with
`YARDR_R1_STRICT=1` and `YARDR_QUIET_RUN=1` beside it stays the one command
anyone can run.

R7. The rules are judged by re-measurement: the same per-package table one
week after the last fix bead lands, against the design's table and its
attached JSON, with the deltas per package, the new per-bead cost, and
the same-sha rerun rate counted against R4. Every rule that missed is a
filed follow-up or a closed wont-fix with the number.
