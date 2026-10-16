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
the rest. That binds the three volume debts -- internal/cli, internal/dispatch
and internal/store -- whose fix beads parallelize them following the
assembly train-yard shape (temp homes, no shared state). Packages under
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
will land, every time"), and its whole-suite gate differs from any plain
run (`whole()` in `.yardr/test`: every package with `-race`, vet for
linux too, the socket leg beside), so no plain test line ever satisfies
it. The test line carries the leg's wall time and race-lock wait,
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
so the pick alone misses them. This takes effect with the incremental
landing, which lands after the quiet whole-suite run below; until then
the landing runs the whole suite.

R6. The whole suite still runs, but on a quiet machine, not on a timer
and not as a gate on any bead. It starts on yard events, never by
polling and never on a timer: the landing queue empty and no builder or
reviewer sessions running, observed through the event stream, firing only
when at least N code landings passed since the last whole-suite run. It
takes the machine-wide race lock, so it never overlaps a landing leg,
and it is single-flight: while one quiet run runs, a further event does
not start a second. A red base raises an event naming the failing
packages and the base commit. `YARDR_LANDING=1` stays the one command
anyone can run.

R7. The rules are judged by re-measurement: the same per-package table one
week after the last fix bead lands, against the design's table and its
attached JSON, with the deltas per package, the new per-bead cost, and
the same-sha rerun rate counted against R4. Every rule that missed is a
filed follow-up or a closed wont-fix with the number.
