# The test rules

Seven rules keep the suite fast, each with the number that justifies it
(docs/design/2026-10-16-yardr-635g-test-time.md measured them). The gate is
the depot's test command, `sh .yardr/test`: builders and reviewers run it in
their worktree, the assembly runs it with `YARDR_LANDING=1` on what lands.
R1 and R2 are script behavior, enforced on every run; the rest are rules the
gate, the roles and the assembly keep between them.

An allow-list in `.yardr/test` names what is over budget today, each entry
with why and which bead brings it under. A fix deletes its entries; a row
whose site is gone is deleted with the fix, not left for later.

R1. A package's tests finish in 60 s under `-race`, or the gate fails.
The wall is the slowest package. When a non-allow-listed package takes over
60 s in the race leg, the gate runs it again alone under the leg's lock.
Over 60 s alone fails the run and names both times; 60 s or less passes
with a `test: R1:` line naming both times and marking the first run as loaded.
Load multiplies package times up to ~2.5x (dispatch is 68 s alone, 79-94 s
in full runs), so the first run's time by itself does not fail the gate.
The re-measurement can be loaded too: the race lock holds off the other
gate legs, not every test on the machine. Over 60 s alone on a loaded
machine -- the one-minute load average past the core count -- passes with
a `test: R1:` line naming both times and the load instead of failing
(yardr-wq5h), unless `YARDR_R1_STRICT` is set, which only the quiet run
(R6) sets: then over 60 s alone fails however loaded the machine, and
the failure line names both times and the load beside them. Unknown
load fails as before.

R2. A test sleeps no more than 500 ms at a site, and a poll loop polls a
condition with a deadline. Exactly 500 ms complies; over fails unless
allow-listed. A test that waits on time takes a clock it sets: the fake
clocks the packages already own (the dispatcher's clock, which tests set;
`Receiver.SetClock` in hooks and yards; the peer clock in the dispatch
tests). A test that must use the real clock is allow-listed with its
reason, and the gate counts those entries. The gate reads literal
durations in `*_test.go` files; a sleep it cannot read, such as a
variable, is not judged and still owes the budget.

R3. A package over the R1 budget with over 100 tests runs its tests
parallel: `t.Parallel` on every test that owns its state, allow-list for
the rest. That binds the volume debts -- internal/dispatch and internal/store,
whose fix beads parallelize them following the assembly train-yard shape
(temp homes, no shared state); internal/cli was the third until yardr-ab6t
threaded its output and environment through its commands. Packages under
budget comply until they breach it, and the budget is their backstop then.

R4. One plain race leg per sha, in builder and review stages only. A stage
whose branch is unchanged since a green test line on that sha does not
rerun the plain suite; a stage that committed -- a review fix, a security
fix -- still runs it on its new sha. That covers a repeat builder pass
too: a pass with no new commit since a green line does not rerun
(yardr-dx5f ran one sha three times across a hold and unhold). Every
note says `reran` or `names this tip`, so the re-measure can tell a
fresh leg from a quoted line. The assembly's runs are outside this rule:
the landing always runs, on exactly what lands, every time, including
after a merger's rebase that changed nothing
(`contrib/assembly/merge-local`: the test command "runs on exactly what
will land, every time"), and its landing gate differs from any plain
run (`whole()` in `.yardr/test`: the selection plus the six black-box
suites with `-race`, vet for linux too, the socket leg beside), so no
plain test line ever satisfies it. The test line carries the leg's wall time and race-lock wait,
written by `.yardr/test` itself so every note holds them: `test: sh
.yardr/test @ <sha>: pass, <n> packages ok, 0 failed, 112 s, waited 40
s`. The fields are the result, the packages passed and failed, the
run's wall seconds, and the seconds it queued on the machine-wide race
lock.

R5. The landing runs the incremental selection -- today's package pick --
plus always the six black-box suites (contrib/assembly, contrib/peer,
contrib/deploy, contrib/server-yard, contrib/isolated-yard,
contrib/release), plus the socket leg as today. Each of the six builds
the tree's binary and drives it without importing the changed packages,
so the pick alone misses them. A landing that changed nothing compiled
runs the plain selection alone, as before; one whose change cannot be
selected (go.mod or go.sum, a deletion, a file no row knows) and one
with nothing to compare with still run the whole suite. The whole
suite otherwise runs only on a quiet machine (R6 below).

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
its own with `YARDR_LANDING=1` and `YARDR_R1_STRICT=1`, holding the machine-wide race lock for
its whole length, so it never overlaps a landing leg, and it is
single-flight: while one quiet run runs, a further event does not start
a second, and the trigger re-arms only when the first finishes. A green
run is logged and told as `quiet_passed`, a red base as `quiet_failed`
naming the failing packages and the base commit, each with its wall
time and starting load. The strict setting, which only the quiet run
sets, turns off R1's loaded pass: a package over 60 s alone fails
however loaded the machine, and its failure line names both times and
the load, so a slowdown cannot hide behind a loaded host (yardr-8gdt).
`YARDR_LANDING=1` with `YARDR_R1_STRICT=1` beside it stays the one command
anyone can run.

R7. The rules are judged by re-measurement: the same per-package table one
week after the last fix bead lands, against the design's table and its
attached JSON, with the deltas per package, the new per-bead cost, and
the same-sha rerun rate counted against R4. Every rule that missed is a
filed follow-up or a closed wont-fix with the number.
