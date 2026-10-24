# AGENTS.md

yardr routes beads of work to whoever works them. One Go module
(`yardr.dev/yardr`), one binary, one store (bbolt) with one writer. Start
with `README.md` (concepts) and `.yardr/depot.md` (how this depot is
worked); user docs live in `docs/` and are not repeated here.

## Layout

- `main.go`, `main_test.go`, `yardtest_test.go`: entry (routes to
  `cli.Execute`) and the root black-box suites, over the built binary and
  the test gate itself.
- `internal/`: all Go code, one package per area (below). Nothing outside
  `internal/` imports it except `main` and the tests.
- `docs/`: user and design docs, including `docs/api.md` (the socket API
  contract), `docs/testing.md` (the test rules), `docs/design/`, `docs/flows/`;
  `docs/docs.go` beside them embeds the top-level pages in the binary (`yardr docs`).
- `packs/`: packs shipped inside the binary (`packs/packs.go` embeds them);
  per-service and per-depot-kind directories.
- `harnesses/`: one `<kind>.yaml` per agent harness kind, embedded and read
  by `internal/agents`.
- `contrib/`: operator and developer tools, each with its own README
  (assembly, bench, deploy, github-mint, isolated-yard, machine-limits,
  peer, release, server-yard, shot). google-mint is no longer here: each
  google pack under `packs/` carries it as its declarations' mint command.
  `contrib/server-yard/managers` installs the stand-in service managers
  that the server-yard suite and the yard-shape matrix
  (`contrib/isolated-yard/matrix_test.go`, run by the quiet run alone) both
  run the server-yard script against.
- `conformance/bdp`: the BDP conformance suite (`run.sh`, vitest).
- `.yardr/`: the yard's own rules for this depot: `depot.md`, the `test`
  gate, `flows/`, `roles/`, `security_paths` (the paths a reviewer may not
  call `done-safe` for).
- `.github/workflows/`: CI (`ci.yml`) and release.

## Packages (`internal/`)

To find your way inside one, run `go doc -short ./internal/<pkg>` first:
it lists every exported name with its comment, faster than grepping.

- `agents`: what yardr knows of each harness kind (models, login recipes)
  and the fetch data `yardr doctor` reports; not herdr's agent state.
- `bdp`: serves the yard over the Bead Protocol Read profile (HTTP).
- `chat`: the `yardr chat` terminal UI (bubbletea); not the socket API it
  reads through.
- `cli`: cobra commands, one file per area, registered in `root.go`; no
  business logic, which lives in the package the command calls.
- `config`: yard settings, the home layout, `YARDR_*` environment, and
  running operator-configured commands for what they print.
- `depotfile`: reads a depot's standing working-rules file.
- `dispatch`: the daemon's pass: wakes on a committed write, re-reads
  state, routes beads, runs the crew, keeps backups; never polls.
- `diskkeep`: keeping the machine's disk free for landings' builds
  (GOCACHE rotation, stale go-tmp), run by `yardr disk keep`.
- `edgelink`: the yard's side of the edge (`yardr edge connect`): fetching
  the edge's key and enrolling the yard by a code, each call signed with the
  yard's key, and the link `yardr server` keeps open to it (dialled,
  challenged, every frame checked, served with the listener's mux); the
  edge itself is the `yardr-hosted` router, not here.
- `eta`: arrival-time estimates and the arrivals board rows.
- `flowdraw`: flows drawn as SVG signalling diagrams (generated into
  `docs/flows/`); the drawing holds no beads.
- `hooks`: receives webhooks (GitHub, Linear, …), routes envelopes into
  yard events; declared-source notifiers live here too.
- `limits`: what a harness subscription still carries, answered by the
  operator's command through a yard source; no provider logic.
- `notify`: one message to a person through the source their record names;
  decides nothing about what is told.
- `otel`: trace context plus a hand-written OTLP/HTTP exporter; no SDK.
- `pack`: reading, checking, planning and applying packs.
- `pairing`: the short-code handshake two yards pair with; no store, no HTTP.
- `peer`: who is on the other end of a unix socket (pid, process tree), so
  the store recognises a call from a session's pane.
- `rpc`: the store API over a local unix socket (`POST /call/<Method>`);
  the client is generated from `store.API`.
- `runner`: starts work: herdr agents and exec commands in worktrees at
  `<root>/<depot>/<bead>`.
- `scripttest`: stand-in scripts for tests (one launcher, symlinks to it).
- `server`: the `yardr server` process: owns the store, serves the socket,
  re-execs itself on upgrade.
- `shared`: what eta, the store and limits share but cannot pass between
  them: route matching over a minimal route shape each side adapts, and
  duration spans.
- `source`: declared sources: declarations, credentials, mints, calls; the
  operator's provider logic lives in commands, not here.
- `standin`: fake Linear/GitHub APIs and the edge for tests and tryouts,
  written from the services' docs and the design, not recorded.
- `store`: the bbolt store and `API`, the interface everything uses
  (`*Store` in the owning process, `rpc.Client` elsewhere); migrations are
  `migrate_NNN_*.go`.
- `storetest`: throwaway stores for tests; with `YARDR_TEST_RPC=1` they
  talk over a real unix socket.
- `testload`: the factor the gate gives a loaded race leg
  (`YARDR_TEST_LOAD`), `Scale`, which makes a wait written for an idle box
  that many times longer, and `Loaded`, the gate's verdict that the machine
  is loaded; the contrib suites' fuses go through `Scale`.
- `udiff`: one unified-diff rendering shared by history and ask cards.
- `version`: build identity, noticing when the binary was replaced, and
  keeping the daemon's own descriptors out of what it execs (self-replace,
  an exec group's command, the quiet run).
- `web`: the browser UI: server-rendered pages plus htmx; routes in
  `server.go`, CSS/JS in `assets/`.
- `yardkey`: a yard's keypair, invites, and peer signatures; no
  store, no HTTP servers.
- `yards`: talking to peer yards (caller and receiver sides).

## Where to start

- New command: a method on `*app` in `internal/cli/<area>.go` returning a
  `*cobra.Command`, registered in `root.go`'s `AddCommand`; interactive
  forms use `huh` (see `groupform.go`), output goes through `a.out` so
  `--json` holds.
- New socket call: a method on `store.API` in `internal/store/api.go`,
  implemented on `*Store`; `go generate ./internal/rpc` rewrites the
  client, and the server exposes it as `POST /call/<Method>` by
  reflection (`internal/rpc/server.go`). If programs outside yardr may
  rely on it, add it to `Calls` in `internal/rpc/api.go` and `docs/api.md`.
- New web page: a handler in `internal/web/<area>.go` plus a route in
  `server.go`'s `routes()`; static files live in `internal/web/assets/`.
- New TUI: the bubbletea model in `internal/chat/`; the command that opens
  it is in `internal/cli/chat.go`.
- New pack: a directory under `packs/` plus its name in the `//go:embed`
  line in `packs/packs.go`.
- New harness kind: `harnesses/<kind>.yaml`; `agents.Harness` in
  `internal/agents` says what each field means.
- New store migration: `internal/store/migrate_NNN_<name>.go`, following
  the previous one.

## Conventions

- Comments explain why, in full sentences, beside the code they justify;
  names match their neighbours. Prefer extending what is there over a new
  concept, flag or special case.
- Errors are lowercase prose built with `fmt.Errorf`, context outside and
  `%w` inside; expected cases are sentinels (`ErrNotFound`) that callers
  match with `errors.Is`.
- Files are lowercase, one area per file; tests live beside the code as
  `*_test.go`.
- Fixtures: throwaway stores from `internal/storetest`, stand-in scripts
  from `internal/scripttest`; wait on the effect, not on wall time. A
  package whose tests start a real yardr names its test binary to the
  servers it starts: its `TestMain` calls `storetest.NameTestBinary()`.
- A wait in a contrib suite (a fuse against a hang, the deadline of a poll)
  is written for an idle box and goes through `testload.Scale`: the gate
  gives the race leg a factor from the machine's load (`YARDR_TEST_LOAD`,
  1 to 4), so no wait is raised by hand after it flakes in a landing. What
  a test measures, and what it gives the program under test as a deadline,
  does not go through it.
- The test rules are `docs/testing.md` (per-package time budget, sleep
  budget, parallelism); the gate is `sh .yardr/test` in the worktree, and
  its printed `test:` line is the record of a run.
- The gate sets `GOFLAGS=-trimpath`, so one worktree's cached builds and
  test results serve the others. A source file's path in a trace or from
  `runtime.Caller` is then its import path: a test finds a repo file from
  its package directory (`../../packs/starter`) or through `packs.FS`.
- A `test: flaky:` line files itself: the gate appends one line per flaky
  line -- date, short sha, packages, tests, then `beads=<ids>` where the
  run is of a bead -- to `yardr-flaky.log` in the git common dir, which
  the repo owns and every worktree shares, files a backlog P3
  `Flaky: <Test> (<pkg>)` when the test is already named in the log, and fails
  the step when the test flaked in 3 different changes in 7 days, with exit 3
  when nothing else failed it, which the assembly holds on instead of sending
  back. A change
  is the set of beads a line names, whatever commits it was tested at:
  `YARDR_TEST_BEADS` (integrate-train gives a train's tests its children,
  since their `YARDR_BEAD` is the train) else `YARDR_BEAD`, so a builder's
  runs, a reviewer's and the landing's, which is rebased and squashed, are
  one. A line with no beads (written before the field, or by CI, the quiet
  run or a hand) is a change of its short sha. A test and its subtests fail
  together and share one Flaky bead. An open bead for the test, its
  top-level test or another subtest of that test (matched by title and
  package) gets the new log line as a note, and no second bead is filed. A
  strike that names a test with its subtests files or notes once. Filing and
  noting are skipped where the yardr CLI is not there. The
  rerun's output replaces the first run's, so the log line also keeps what
  the first run printed for each failed test, behind ` | `: its `--- FAIL:`
  line and the lines under it (6 lines of 200 characters at most for a
  test, 20 tests at most, a literal `\n` between lines); a failure with no
  `--- FAIL:` line keeps its timeout's panic line and running tests, its
  fatal error's or panic's line and the dump's head, else its first output
  lines, and a timeout names its running tests, a fatal error or panic its
  crashing test, in the head, striking and filing as failures do. The line
  ends with the load the gate measured as it wrote it (`load <avg> on
  <cores> cores`, or `load unknown`), so a strike on a quiet machine reads
  apart from one under load. The filed bead or the note carries the whole
  line. Only the part before the ` | ` names the tests that flaked and the
  beads the run was of. A hand reading old
  lines files or notes the same way -- a repeat, or a failure that accuses
  the product (a race report, a product assertion, a wrong result rather
  than a timeout); otherwise the log line is the record, named in the
  handover note. A race fails the step without a retry, and a package that
  fails twice fails as before.
- `gofmt` and `go vet` are clean on every change; the module builds with
  the Go in `go.mod`.

## What not to touch

- `internal/rpc/client_gen.go`: generated; change `internal/store/api.go`
  and run `go generate ./internal/rpc`.
- `docs/flows/*.svg`: generated by `internal/flowdraw/gen`; change the
  flows or `internal/flowdraw`.
- Git-ignored build outputs (`/yard`, `*.test`, `/dist`,
  `conformance/bdp/node_modules/`); there is no `vendor/` directory.
- `.yardr/depot.md` holds the yard's rules for builders. One of them: a
  change that moves or adds a package updates this map (AGENTS.md) in the
  same commit.
