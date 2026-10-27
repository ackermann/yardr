# contrib/burn

One shell script that runs a Go package's tests over and over on a machine
kept busy, to show whether a test that failed only under load still does.

```sh
contrib/burn/burn [-r] [-n COUNT] [-x FACTOR] [-c CPUS] [-N NICE] PACKAGE [TEST...]

contrib/burn/burn ./internal/dispatch TestPushBurstIsOneMessage TestQuietExecRunSetsStrictR1
contrib/burn/burn -n 5 ./internal/dispatch
contrib/burn/burn -r ./internal/dispatch TestPushBurstIsOneMessage
```

A test that fails once in fifty runs on a loaded machine passes on a quiet
one, and so does its fix, so one green run proves nothing. The burn makes the
machine as busy as the failures were seen on, and runs the test enough times
to tell.

It builds the package's test binary once, before any load. Then it starts
FACTOR times as many `yes` loops as the machine has cpus (`-x`, 5 by
default: a machine at load 50 on ten cores; `-c` says how many cpus to count
instead of asking the machine), and runs each TEST COUNT times (`-n`, 100),
one run at a time, in the package's directory, as `go test` does. Each TEST
is the name of a top-level test; its subtests run with it. With no TEST the
whole of the package's tests is what is run COUNT times, every test beside the
others as `go test` runs them.

A data race shows in a burn when the test binary carries the race detector.
Use `-r` to request it (`go test -c -race`); without `-r`, burn does not add
it, though an inherited `GOFLAGS=-race` can still enable it. A burn without
the detector proves nothing about a race fix. The detector slows each run,
which is why it is not the default. `-r` adds the flag to the build itself
and leaves the caller's `GOFLAGS` alone, so a `-race=false` among them does
not turn it off.

The default factor is for an idle yard: the loops are niced, but they still
raise the load average, and the yard's builder routes wait on
`machine.load<80`, so inside a builder's or reviewer's session burn runs at
`-x 1` and names one test (the rule in `.yardr/depot.md`).

A run passes when the binary exits 0 and prints the test's own `--- PASS`
line (a whole package's, its closing `PASS`). A name that matches no test and
a test that skips are not passes, though `go test` calls both ok. The output
of the first five failing runs of a test is shown, down to the lines that say
what failed (a data race's `WARNING: DATA RACE` is one), and the last lines say
how many runs of each test passed:

```text
TestPushBurstIsOneMessage: 100/100 passed
TestQuietExecRunSetsStrictR1: 97/100 passed
```

The exit status is 1 when any run failed and 2 when burn could not start. A
note that claims a fix names the command, the counts it printed, and the load
average it printed before and after.

## Being kind to the machine

The loops and the tests run at one nice level (`-N`, 19), so the load weighs
on the test and not on whoever else uses the machine: the tests contend with
FACTOR times the cpus' worth of loops, and give way to everything else. On a
machine that is busy with other work the tests get less than their share, and
a run that takes longer says so; it is the harder case, not a wrong one. A
test's own children (`git`, `sh`) inherit the level.

The loops end with burn, by pid, on a signal as at the normal end; nothing is
killed by name. A run in flight when burn is interrupted is killed with it,
and its own children are left to finish.

## What it does not do

Given tests by name, it runs one at a time, in a process of its own, so the
package's other tests are not running beside it. A failure that needs them (a
test that shares a machine-wide resource with its neighbours, or that waits
behind hundreds of parallel ones for a goroutine) shows in the package's whole
run under load: give no TEST for that, and expect each run to take as long as
the package does.

`go test`'s own limits apply: a hang ends at the binary's ten minutes, and is
a failed run.

`go test ./contrib/burn/` runs it against stand-ins for `go` and `yes`: the
counts, what is not a pass, the whole-package run, the loops' number and nice
level, and the end on a signal. `-r` is the one case that builds a package of
its own with the real `go`, since only a real build shows whether the binary
has the detector.
