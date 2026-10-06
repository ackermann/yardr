# contrib/bench

Measures yardr at 250, 2,000 and 20,000 beads and prints one table: how long
its commands take, how big the store file is, what the server and the daemon
cost while nothing happens, how long a bead waits for its session, and what
several clients writing at once do to each other.

```
contrib/bench/bench                                  # about 35 minutes
contrib/bench/bench -sizes 100,400 -runs 5 -idle 10s # a minute, to try it
contrib/bench/bench -out results.json                # the numbers as JSON too
```

Run it from the checkout to measure. It needs what yardr needs (go, git,
herdr) and curl. Progress goes to stderr, the table to stdout.

## It never touches your yard

`bench` starts an isolated yard (`../isolated-yard/isolated-yard up`: a home
under `/tmp/iy.*`, a herdr server of its own, a stand-in for the agent),
builds yardr and the benchmark from the checkout into it, runs the benchmark
through the yard's `D/e`, and takes the yard down again whatever happened.
The benchmark itself refuses to start unless `YARDR_HOME` is such a yard's
home (its name, and the mark `up` left in it) and `HERDR_SOCKET_PATH` is not
set: it stops that home's server and writes its store file.
`BENCH_KEEP=1` leaves the yard standing, to look at it.

## The yard it builds

Modelled on the yard yardr is built in, when it had 244 beads and 6,900
events: two git depots following one flow (backlog, new, review, approved,
merged, with decide beside it), a yardmaster running in the isolated herdr,
manual groups for backlog and decide with a limit of 50, and per bead about 27
events.

95 of 100 beads are closed and went the whole way: created, advanced to new,
claimed and started for a builder, noted, done; the same for a reviewer (one
in five sends it back once) and for the assembly; closed, its workspace
removed. That is 3 or 5 sessions, 3 to 5 notes and one edge on every fourth
bead, and around it what a yard logs on no bead: the yardmaster's status,
its journal, mail both ways. The other 5 are open in the stages people work:
most in backlog, some in decide, some blocked by another open bead, a few
held. No open bead waits in a stage an agent works, so the daemon starts
nothing but the yardmaster.

The fill writes through `internal/store`'s own functions, in the benchmark's
process, while the server is stopped: `CreateBead`, `AddDep`, `Advance`,
`Claim`, `MarkRunning`, `AddNote`, `Done`, `Journal`, `Mail` and so on, each
a transaction with its event, exactly what the server runs for the CLI. Two
things differ from a yard that grew: no commit waits for the disk
(`YARDR_STORE_NOSYNC`, as in the tests), or 20,000 beads would take hours,
and everything is of one day, where a real log spans months. Webhook
deliveries and their stored bodies are left out.

After each fill the server, the daemon and a web view are started as usual
(the server syncing every commit again) and left to settle: the daemon
claims the waiting beads for their manual groups.

## What is measured, at each size

- **Commands**: each run 20 times (`-runs`), one after the other, from
  process start to exit; the median and the 95th percentile (of 20 runs, the
  19th). Each read is run once unmeasured first. `version` talks to no
  server and `server status` makes one round trip: together they are the
  floor under every other row. `bead show` and `prime --bead` read an open
  bead in decide, `inbox` the yardmaster's, `bead advance` moves a bead from
  backlog to decide. The web page is `GET /beads` with curl. The rows after
  it were added to see which filters and listings read every record.
- **Dispatch**: a bead is advanced into a stage that an exec group whose
  command is `true` works; the delay is from the commit of that advance to
  the commit of the `claimed` and the `started` event, by the times the
  store gave them (read from `yardr watch`), so no client's start-up is in
  it.
- **Store file**: `yard.db`; then, on a copy made with `yardr store backup`,
  the pages its buckets hold, and its size after `yardr store compact`; then
  the same after every open bead of the copy was closed and everything
  prunable pruned (retention of a millisecond), which leaves what a yard
  keeps for good: beads, notes, edges. "Of it in use" counts whole pages,
  however full; a compact packs them too, so its file can be smaller still.
  bbolt grows a small file in doubling steps, so below some tens of MB a
  compacted file's size is coarse.
- **Memory**: RSS of the server, the daemon and the web view after the idle
  time.
- **Idle CPU**: the CPU seconds each of the three used over 5 minutes
  (`-idle`) in which the benchmark does nothing. On Linux `ps` counts whole
  seconds only.
- **Writers at once**: 1, 4 and 16 clients, each a loop of CLI runs (`bead
  note`: 20 each, `bead create`: 10 each): commands finished per second and
  the 95th percentile of one command. Then `yardr ready`, 20 times, while 16
  clients write notes without pause.
- **In this process, on a copy**: bbolt alone reading every byte of the
  beads bucket; the store's `ListBeads`, `Ready` and `GetBead`, with no
  socket and no CLI between; and one `AddNote` committed with and without
  the sync. They say how much of a command's time is the engine's and how
  much is yardr's.

The last column says how a row grew from the smallest size to the largest:
the factor, and flat (less than half as much again), linear, or worse. It
compares the growth per bead of the last step with that of the first: equal
is linear (a fixed cost plus one per bead), twice as much is worse. Where
the first step grew by less than a tenth it tells nothing, and the row is
judged by the last step alone. It is a reading aid: look at the numbers.

The measures leave beads behind (about 290 a size with the defaults), which
are closed before the next fill and counted into its size; "beads when
timed" is what the commands saw.

## What it does not measure

Other machines, a quiet machine (it prints the load average it ran under), a
log that spans months, stored webhook bodies, real agents, and commands
other than those in the table.
