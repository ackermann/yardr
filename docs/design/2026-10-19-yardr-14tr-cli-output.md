# CLI commands write to an output and read a config they are given

Design yardr-14tr, proposed 2026-10-19. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

## 1. The question

From yardr-cayh (635g B2), note 6084 by its builder, 16 Oct 2026:
"internal/cli is 103 s alone under -race and cannot reach the 60 s budget
by t.Parallel, because its 837 tests share two process globals. Output:
commands print to os.Stdout directly (936 fmt.Print sites; a.out, table
and json name os.Stdout) and tests swap the global in capture()
(show_test.go), ~51% of wall. Environment: t.Setenv/t.Chdir reach ~465
tests (newHome 339, noHome, noGitHubCredential, newAsker), and Go refuses
t.Setenv with t.Parallel. cli is allow-listed meanwhile (B1, reason and
number from B2)."

This design decides how a command gets its writer and how the print
sites move in slices that each land green; how config, home and cwd
reach a command without the process environment; what stays global and
serial on purpose; the expected wall time with the numbers; and the
build beads in order, each sized. It also answers whether threading
output helps the product beyond tests.

## 2. What exists today

All counts below are this worktree at `ce8216df` (19 Oct 2026) unless a
line names the 16 Oct measurement it updates. They were taken with
`grep` over `internal/cli`, the same method note 6084 used, so the two
sets of numbers compare; where a count is transitive (tests reached
through a helper's helpers) the AST count from note 6084 is quoted as
such, because a text count cannot see it.

### 2.1 Output: one process global, swapped by the tests

The command object is `app` in `internal/cli/root.go:25`. It holds
`json`, `actor`, `homeDir`, `cfg *config.Config` and `st store.API` --
no writer. Everything a command prints reaches `os.Stdout` by one of
four routes:

- `(*app).out` (`root.go:268`) is the funnel most commands use: 185 call
  sites `grep '\.out('`. In JSON mode it encodes to
  `json.NewEncoder(os.Stdout)`; for humans it calls a `text func()`
  closure, and the closure prints with `fmt.Print*` to the same global.
  The funnel therefore gives no indirection today: both branches name
  the process's stdout.
- `table` (`root.go:279`, 77 call sites) builds a
  `tabwriter.NewWriter(os.Stdout, ...)`.
- Direct printing in command bodies and helpers: 1,000 `fmt.Print*`
  line hits in the 52 non-test files of the package (936 on 16 Oct).
  The largest files are `pack.go` (114), `crew.go` (96),
  `sourcecmd.go` (80), `beads.go` (62), `peers.go` (55) and
  `harness.go` (51). A further 47 line hits name `os.Stdout` or
  `os.Stderr` directly (raw writes in `flows.go` and `hooks.go`, JSON
  encoders in `pack.go`, `ops.go` and `pair.go`, terminal checks, and
  the child wiring in `start.go`).
- Package-level print helpers with no writer parameter, about 35 of
  them: `printLinks`, `printBeadPeer`, `printFamily`, `printBlockedBy`,
  `printNotes`, `printDiscovery` (`beads.go`), `printAsk` and
  `printForeignAsk` (`bead_ask.go`), `printInboxEvent` (`crew.go`),
  `printEvent` (`ops.go`), `printLines` (`doctor.go`), `printSteps`
  (`guide.go`), and the `print*` family in `pack.go`, `packcalls.go`,
  `packexpose.go` and `packexportcmd.go`. Each prints with `fmt.Print*`
  and can only be tested through the global.

One helper already shows the shape this design extends:
`printCallables(w io.Writer, indent string, calls []store.Callable)`
(`call.go:183`) takes its writer, and its two call sites
(`beads.go:1000`, `call.go:121`) pass `os.Stdout` explicitly. Cobra's
own seam is nearly unused by product code: two sites write to
`c.ErrOrStderr()` (`reply.go:245`, `threadopen.go:245`, plus one in
`threadstate.go:183`), no product site calls `SetOut`/`SetErr`, and the
only `cmd.SetOut(&buf)` in the tree is a test (`watch_test.go:123`).
Cobra help and usage therefore also go to the process's stdout/stderr
by cobra's defaults.

Errors have their own global: `var stderr io.Writer = os.Stderr`
(`root.go:54`, "tests replace it"), written by `execute` for the final
error and by helpers in `beads.go`, `identity.go`, `init.go`,
`pair.go`, `peers.go`, `start.go` and `upgrade_flow.go` through
`fmt.Fprintf(stderr, ...)`. In JSON mode `execute` instead encodes the
error to `os.Stdout` directly (`root.go:69`). Input mirrors output:
`var stdin io.Reader = os.Stdin` (`harnesslogin.go:28`, "tests replace"
it) is the one named reader; terminal detection reads the real file
descriptors (`term.IsTerminal(int(os.Stdin.Fd()))` in `init.go:214`
and elsewhere), which is a read of hardware state, not a mutable
global.

The test harness is built on the swap. `capture(t, fn)`
(`show_test.go:196`) replaces `os.Stdout` with a pipe, runs `fn`, and
restores it; 50 direct `capture(t, ...)` calls remain. `run(t, c,
args...)` (`beads_test.go:21`) executes a cobra command inside
`capture`, and is the package's workhorse (on the order of 1,700 call
sites; 384 of them spell `(&app{...}).rootCmd()` inline, the rest reach
a command through a local helper). `execute(&app{}, args)` under
`capture` accounts for 32 more. The pipe is load-bearing and known to
bite: `TestCaptureOutputLargerThanThePipe` (`show_test.go:219`) exists
because a read deferred past the command's return deadlocks once the
output passes the pipe's 64 KB. Note 6084 measured about 51% of the
package's wall time inside `capture`, and that concurrent captures of
one global cannot attribute bytes to a test at all: with a mutex they
serialise, without one `-race` is red and the bytes interleave. That --
not test count -- is why only 67 of today's tests carry `t.Parallel`.

### 2.2 Config, home and cwd: the process environment

`config.Load` and `config.LoadReadOnly` (`internal/config/config.go:131`,
`:148`) resolve the yard from the process: `YARDR_HOME` (read through
`config.Getenv`, which adds the `YARDR_` prefix), else
`os.UserHomeDir()` plus `DefaultHome`, plus the hook/web/BDP addresses
from their `YARDR_*` variables. The `--home` flag reaches that reader
by mutating the process: `PersistentPreRunE` (`root.go:100`) calls
`config.UseHome(a.homeDir)` (`config.go:102`), which `os.Setenv`s
`YARDR_HOME` so that "every process this one starts is in the same yard
without each start having to pass it on". A command that needs the
config therefore reads a global, and 36 non-test sites call
`config.Load`/`LoadReadOnly` for themselves rather than using a config
the run already resolved; `(*app).store` (`root.go:244`) is one of
them, caching into `a.cfg` only on its own path.

Identity and flag defaults read the same global. `(*app).identity`
(`identity.go:33`) takes `YARDR_ACTOR`, `YARDR_SESSION` and
`YARDR_BEAD` through `config.Getenv`, then calls `os.Getwd()` (one of
only three product `os.Getwd` sites, with `crew.go:1997` and
`guide.go:437`). Commands spell their defaults against the environment
in help text and resolve them at run time from it -- `--bead` from
`YARDR_BEAD`, `--crew` from `YARDR_CREW`, `--for` from
`YARDR_CREW_FOR` (`beads.go:838`, `crew.go:1021`). In all, 34 non-test
sites read `config.Getenv`/`os.Getenv`/`os.LookupEnv`. The working
directory is likewise ambient: no field carries it, so the three
`os.Getwd` sites and any subprocess started without an explicit `Dir`
inherit the process's.

The tests pay for this with the second global. `newHome`
(`init_test.go:49`) `t.Setenv`s `YARDR_WEB_ADDR` and `YARDR_BDP_ADDR`
to empty and `YARDR_HOME` to a fresh short directory; `noHome`
(`home_test.go:28`), `noGitHubCredential` (`main_test.go:279`) and
`newAsker` (`askharness_test.go:45`) choreograph related variables.
Direct counts today: 324 `t.Setenv`/`t.Chdir` sites across 66 test
files, 139 direct `newHome(t)` calls, 10 `t.Chdir` sites. Note 6084's
transitive AST count -- the number that matters for parallelism, since
a test that merely calls a helper which calls `newHome` is just as
pinned -- was about 465 of 837 tests (`newHome` alone reaching 339).
Go enforces the pin: a test calling `t.Setenv` cannot call
`t.Parallel`, in either order, and an environment writer cannot even
overlap an environment reader, because every command reads config.

For panes, yardr already builds the alternative this design copies. A
session's environment is assembled as an explicit slice --
`slices.Concat(config.Env("BEAD", ...), config.Env("SESSION", ...),
...)` (`internal/runner/runner.go:64`, and the crew equivalent in
`herdr_crew.go:185`) -- and child-environment builders such as
`daemonEnv(environ, home)`, `server.ServerEnv(environ, home)` and
`serviceEnv(environ, home, kind)` already take the parent environment
as a parameter (they are asserted against in `home_test.go:93`). The
machinery to hand a run its environment as data exists; the CLI's own
run is the one consumer still reading the process's copy.

### 2.3 The remaining globals: seams the tests stub

Beyond the two globals above, the package keeps function variables as
test seams. The ones a parallel test could collide with are:
`connect` (`root.go:235`, how `store` reaches the server), `lookPath`
(`init.go:218`, `exec.LookPath`), `interactive` (`init.go:213`) and
`ask` (`groupform.go:417`, the huh form runner), `runChat`
(`chat.go:195`), `stdin` (`harnesslogin.go:28`), the service seams
`serviceDir`/`serviceCtl` (`service.go:65`, `:81`), `serverRunning`,
`loginService`, `webService`, `userHomeDir` (`doctor.go:165`-`177`),
and the start/stop seams `spawnDaemon`, `spawn`, `signalStop`,
`focusAgent`, `paneTail` and kin (`start.go`, `longrunning.go`). Test
helpers assign them with restoration: `stubLookPath` and `setInteractive`
and `stubAsk` (`init_test.go:911`-`934`), `fakeTools` (`doctor_test.go:43`,
which writes stand-in tools into a directory of its own and replaces
the package's `lookPath` with a function that finds those and nothing
else; it does not touch `PATH`), and `stubServices` (`service_test.go:57`, which replaces
the service seams and the OS it reports). Note 6084 classed the tests
doing this kind of choreography -- mid-test environment sequences,
interactive and `runChat` paths, `lookPath` and service stubs, stderr
swaps -- as serial-essential: about 150 tests, about 20.6 s under
`-race`. Nothing in this design disputes that class; section 3.3 keeps
it serial on purpose.

### 2.4 The numbers, then and now

`docs/testing.md` states R1 (a package's tests finish in 60 s under
`-race`, `docs/testing.md:14`) and R3 (a package over budget with over
100 tests runs parallel, `:38`). cli is on the gate's allow-list
(`.yardr/test:689`):

```
internal/cli yardr-14tr 769 sequential tests at 0.10 s mean plus 67 parallel; 75 s alone; stdout and env are process globals in cli tests; parallel needs output/config threaded through commands (design yardr-14tr)
```

That row is yardr-cayh's landing state: 836 tests, alone
`go test -race -count=1 ./internal/cli/` passing in 75.4 s under the
race lock at `fa98fe66`, down from 86.6 s on its base (note 6167). The
package has since grown to 904 `func Test` (counted 19 Oct), still
with 67 `t.Parallel`. A fresh alone run in this worktree at `ce8216df`
reported `ok ... 151.071s` -- on a machine whose load average was
about 21 on 10 cores, with `user 1m15s`: the CPU sum still matches the
allow-list's arithmetic (769 sequential at 0.10 s mean is about 77 s
of work), and the wall is contention, exactly the loaded-versus-quiet
distinction R1 already draws (`docs/testing.md:19`). The honest
baseline for the proposal is therefore: about 75 s of mostly
sequential work, 67 tests parallel, a 60 s budget the package cannot
meet while output and environment pin the other 769 in sequence.

### 2.5 Output is not shared with rpc, web or chat

Nothing outside `main` imports the CLI's printing: `main.go` calls
`cli.Execute`, and the only other importer is the root black-box suite
(`yardtest_test.go`). The socket API is `store.API` served by
reflection (`internal/rpc/server.go`); `internal/web` renders its own
pages from the store, and `internal/chat`'s bubbletea model reads the
store through the socket client. No product code reuses a command's
printed text, and `--json` -- one `out` funnel encoding the same value
the text closure describes -- is already the machine-readable form of
every command. Section 3.6 draws the consequence.

## 3. The proposal

The smallest version that answers the question threads two values
through the command run -- its writers and its environment -- on the
objects yardr already has, and changes no command's words, flags or
`--json` shape. A person running yardr sees nothing new:

```
$ yardr bead show yardr-14tr        # unchanged, byte for byte
yardr-14tr  Design: cli commands write to an output ...
```

What changes is who supplies the bytes' destination and the run's
config when a test drives the same command in-process. Two decisions
carry the whole design; both were put as open questions in section 5
with their alternatives and answered with the recommended option, which
the text below describes (Q1 for the writer, Q2 for the environment).

### 3.1 The writer: cobra at run time, the app as its preset

A command gets its writer from its cobra command at run time --
`c.OutOrStdout()` for output, `c.ErrOrStderr()` for warnings and
errors -- and the `app` carries the preset those resolve to when no
test overrides them. Concretely:

- `app` gains two fields, `stdout io.Writer` and `stderr io.Writer`
  (alongside the existing `cfg` and `st`). They are presets, not the
  run-time source: `Execute` leaves them nil, cobra's defaults
  (`os.Stdout`, `os.Stderr`) then apply, and the one place that must
  name a writer before a command exists -- `execute`'s final error --
  writes JSON to `root.OutOrStdout()` and prose to
  `root.ErrOrStderr()`, which deletes both the `os.Stdout` encoder at
  `root.go:69` and the package-global `var stderr` at `root.go:54`.
- `rootCmd()` applies a non-nil preset with `root.SetOut(a.stdout)` and
  `root.SetErr(a.stderr)`, so `yardr --json`'s error, cobra's help and
  the commands all land in one place when a caller preset one, and in
  the process's streams when none did. A test that builds a subcommand
  directly needs no preset: the test harness sets the command's own
  output (below), and `OutOrStdout` walks to it.
- The funnel learns the writer. `(*app).out` becomes
  `out(w io.Writer, v any, text func(w io.Writer))`: JSON encodes to
  `w`, and the text closure receives the same `w`. This is the one
  signature change every `a.out` call site feels, and inside each
  closure it is mechanical -- `fmt.Printf(...)` becomes
  `fmt.Fprintf(w, ...)`, `fmt.Println(x)` becomes
  `fmt.Fprintln(w, x)`. `table` becomes `table(w io.Writer, header
  string, rows [][]string)` with its tabwriter on `w`. Each `RunE`
  passes `c.OutOrStdout()`; each helper an `app` method calls passes
  the writer it was handed.
- Every package-level print helper takes the writer first, exactly as
  `printCallables(w, ...)` already does (`call.go:183`): `printNotes(w,
  notes, home, as)`, `printEvent(w, e)`, `printInboxEvent(w, e, member,
  home)`, and the rest of the section 2.1 list. The two existing
  `printCallables(os.Stdout, ...)` call sites pass the writer they now
  hold instead. Raw `os.Stdout.Write` and `json.NewEncoder(os.Stdout)`
  sites (`flows.go`, `hooks.go`, `pack.go`, `ops.go`, `pair.go`) write
  to the handed writer; the SVG and hook-body bytes are output like
  any other.
- Warnings follow the same rule through `c.ErrOrStderr()`, which two
  sites already use. The helpers with no command in reach -- above all
  `identity`'s worktree line (`identity.go:63`) -- take the writer from
  the app: `PersistentPreRunE` records `a.stderr = c.ErrOrStderr()` as
  its first act (before it resolves the actor, which is what can print
  that line), and an unset `a.stderr` means `os.Stderr`. Input is the
  mirror image in miniature: `harnesslogin.go`'s package-global `stdin`
  becomes an `app` field preset to `os.Stdin`, because it is per-run
  data with exactly that shape already. Terminal detection keeps
  reading the real descriptors: whether a terminal is attached is a
  fact about the process, and no test asserts bytes from it.
- The harness stops swapping the process. `run` sets a buffer on the
  command (`c.SetOut(&buf)`, the idiom `watch_test.go:123` already
  uses) and returns its contents after `Execute`; `capture` is deleted
  once no caller needs it. During the move (section 3.4), `run` runs
  both captures at once -- the command's buffer and the legacy pipe --
  and returns their concatenation: a migrated command's output is
  wholly in the buffer, an unmigrated one's wholly in the pipe, so
  either slice lands green and no command's bytes are ever split
  across the two. Tests of print helpers stop capturing at all: they
  hand the helper a `bytes.Buffer` and assert on it.

Why this split and not the app alone: commands already receive their
cobra command in every `RunE`, cobra already implements
inherit-and-override (`SetOut` on the test's command, preset on the
root, process streams by default), and `ErrOrStderr` is already the
spelling two call sites use. Making the app's fields the run-time
source instead would strand every site that has a command but no
reason to consult the app twice, and would leave cobra's own help
writing somewhere else. The app's fields remain the right home for
what predates a command (the preset, and `execute`'s error before any
`RunE` ran) -- which is why the proposal is "both", with cobra as the
run-time source and the app as its preset. Q1 prices the alternatives.

### 3.2 Config, home and cwd: an environment the run is given

The run's environment becomes a value on the app, in the shape the
runner already builds for panes. Concretely:

- `app` gains `env map[string]string`, the run's environment by full
  key (`YARDR_HOME`, `YARDR_ACTOR`, `USER`, `HERDR_ENV`, ...), less
  the process facts named below, which the map never carries.
  `Execute` seeds it from `os.Environ()` once, at the process's edge;
  tests construct the map they mean. Two lookups read it:
  `a.getenv(key)` for full keys and a yardr lookup in the shape of
  `config.Getenv` for short names, so `identity`, the `--bead` /
  `--crew` / `--for` defaults and the actor resolution read the run's
  map and never the process's. With no map set they fall back to the
  process environment, which is what keeps every unmigrated test and
  every direct subcommand call working while slices land.
- `config` learns to resolve from a lookup instead of the process:
  `config.LoadWith(get func(string) string)` beside today's `Load`,
  sharing its body -- `Load` becomes `LoadWith(config.Getenv)` in
  effect, and the socket-length refusal and home creation stay in the
  one place. Only the `YARDR_*` reads move to the lookup: the default
  home still comes from `os.UserHomeDir()`, and a relative home is
  still made absolute against the process's working directory, for
  the reasons given below, where this section says what the map does
  not carry.
  `config.ActorWith(get)` does the same for `config.Actor`
  (`config.go:422`), which reads `YARDR_ACTOR` and then `USER` from
  the process today and is where `resolveActor` (`root.go:194`) and
  `init` (`init.go:970`, `:1046`) end up.
  `(*app).config()` resolves `a.cfg` once from `a.env` (through
  `LoadWith`) and the 36 `config.Load` call sites in commands use it.
  The `--home` flag stops mutating the process: `PersistentPreRunE`
  validates the directory as `UseHome` does today (made absolute with
  `filepath.Abs` against the process's working directory, refused when
  empty) and records it in `a.env["YARDR_HOME"]`; `config.UseHome`
  itself stays for the non-CLI callers that still need it.
- What a run starts gets the run's environment explicitly, which the
  child builders already accept: the daemon, server and service
  environments are built from `a.environ()` where today they are
  built from `os.Environ()` -- `daemonEnv`, `server.ServerEnv` and
  `serviceEnv` already take that slice as their first argument
  (`home_test.go:93`), so the behaviour `--home` guarantees a child
  ("a child with the command's environment", `TestHomeFlagReachesChildren`)
  is kept by construction rather than by a mutated global.
  `a.environ()` renders the map as `KEY=VALUE`, with the resolved home
  applied, and adds the process facts of the next point as the
  process holds them at the moment it renders, not as they were when
  `Execute` seeded the map. That is how `up`'s `PATH` reaches what it
  starts. `herdrProgram` (`updown.go:147`-`168`) appends the login
  shell's directories to the process `PATH` when only that shell finds
  herdr, and `useLoginPATH` (`login.go:240`-`250`) puts the login
  shell's `PATH` first for `up --login`. Both keep calling
  `os.Setenv("PATH", ...)`, because every reader of `PATH` they serve
  reads the process: `lookPath` (`exec.LookPath`), `exec.Command`'s own
  lookup, `spawnHerdr`'s environment (`updown.go:112`-`115`), the
  login service's `PATH` line (`login.go:124`) and `internal/source`'s
  command environment (`command.go:101`). Both run before `up` starts
  herdr, the server or the daemon, and `a.environ()` is called when
  each child is built, so each child gets the merged `PATH`. A
  snapshot of `PATH` in the map would lose those directories; that is
  why `PATH` is a process fact and not a map entry.
- The map does not carry the process facts: the values that a package
  below the CLI reads from the process itself, or that a command sets
  on the process for those readers. Threading them would change those
  packages' APIs, which the bead leaves out. They are:
  - the user's home (`HOME`): `config.IsDefaultHome` (`config.go:173`,
    called through `yardrAt`, `init.go:196`, at about a hundred sites),
    `config.ExpandPath` (`envfile.go:41`, at `users.go:241`,
    `sourcecmd.go:1007`, `packnotify.go:84` and `:118`,
    `grouplogin.go:114`), `LoadReadOnly`'s default, four direct
    `os.UserHomeDir` calls (`crew.go:999` in `absDir`,
    `depotsources.go:166` in `tilde`, `grouplogin.go:103`,
    `updown.go:120`) and the `userHomeDir` seam (`doctor.go:177`, read
    at ten sites);
  - the working directory: the three `os.Getwd` sites
    (`identity.go:55`, `crew.go:1997`, `guide.go:437`), nine
    `filepath.Abs` calls in `internal/cli`, `filepath.Abs` in
    `config.UseHome`, `LoadReadOnly` and `ExpandPath`, `pack.LoadDir`
    (`internal/pack/pack.go:263`, reached from `resolvePack`,
    `pack.go:130`), and every relative path a command opens or hands
    to a subprocess that inherits its directory;
  - `PATH`, for the readers listed in the point above, and `TMPDIR`,
    read by `os.TempDir`;
  - herdr's session variables, `HERDR_SOCKET_PATH`, `HERDR_SESSION`,
    `XDG_CONFIG_HOME` and `XDG_STATE_HOME`: `internal/runner` resolves
    the herdr session from them (`herdr_session.go:89`, `:144`, `:149`;
    `herdr_events.go:27`);
  - declared-source credentials and logins, such as `GITHUB_TOKEN`,
    `GITHUB_APP_ID` or a declaration's own `ACME_TOKEN`:
    `hooks.Declared.Credential` reads them with `os.Getenv`
    (`internal/hooks/reply.go:185`), and `internal/limits` hands a
    limits command the process environment (`limits.go:345`);
  - the setting variables (`YARDR_WEB_ADDR`, `YARDR_BDP_ADDR`,
    `YARDR_HOOK_ADDR`, `YARDR_WEB_AS`, `YARDR_WEB_PAGES_ADDR`,
    `YARDR_WEB_PAGES_HOST`, `YARDR_UPGRADE_RESTORE`) as
    `store.Setting.Effective` reads them (`internal/store/settings.go:470`,
    called from `init.go:1275`, `upgrade_install.go:417`, `web.go:160`
    and `:448`). They are the one exception to "never carries": the
    map also holds them, for the CLI's own reads through `LoadWith`,
    while `Effective` keeps reading the process. `TestMain` keeps the
    two in agreement for parallel tests by blanking them in the
    process, and a test that sets one to a value stays serial.
  In a test binary these values are constant while parallel tests
  run. Go refuses `t.Setenv` and `t.Chdir` in a test that calls
  `t.Parallel`, and it starts parallel tests only after the sequential
  ones have finished (section 3.3). `TestMain` already sets `HOME` once
  (`main_test.go:96`); it also blanks the setting variables once, which
  `newHome` does per test today (`init_test.go:51`-`52`). So every
  parallel test reads the same process facts, and the setting
  variables are unset in both its map and its process. A real run
  seeds `a.env` from the process, so its map and its helpers agree. To
  keep a test from building a run whose map and helpers disagree,
  `testApp` fails the test (`t.Fatal`) when its map holds `HOME`,
  `PATH`, `TMPDIR` or a herdr session variable. Credential names are
  open-ended and cannot be listed there, but a misplaced one fails
  loudly rather than flakily: a parallel test that puts a credential
  in its map, where `Declared.Credential` reads the process, finds it
  unset every time and fails the same way on every run.
  What this costs is a named set of tests that stay serial, because
  they set a process fact. By test function, at the base of this
  revision:
  - `HOME`, 16: through `noHome` (`home_test.go:28`; 12 calls in
    `harnesslogin_test.go`, `harness_test.go`, `home_test.go` and
    `version_test.go`) or a direct `t.Setenv("HOME", ...)`
    (`grouplogin_test.go`, `init_test.go:554`,
    `main_guard_test.go:114`).
  - The working directory, 7, which keep `t.Chdir` because they test
    resolution against the directory: `TestHomeFlagReachesChildren`
    (a relative `--home y1/`, `home_test.go:80`), the two in
    `packname_test.go` (`./starter` at `:70`, a name from elsewhere at
    `:212`), `TestDefaultPackSetting` (`defaultpack_test.go:126`) and
    three in `identity_test.go`, which stand in a session's worktree.
  - `PATH`, 4: `TestUpWithoutHerdr` and
    `TestUpFindsHerdrWithTheLoginShell` (`updown_test.go:562`, `:595`),
    `TestUseLoginPATH` (`login_test.go:543`) and
    `TestCrewWithoutGitHubCredentialIsRefused`
    (`githubapp_test.go:193`, a stand-in `gh` first on `PATH`).
  - `TMPDIR`, 1: `TestDoctorTry` (`doctor_test.go:1071`).
  - herdr's session variables, 6: `TestDaemonServiceReachesTheHerdrSessionSet`
    and the callers of `otherShell` (`herdrsession_test.go:32`-`34`,
    `:157`), `TestDaemonWithoutYard` (`init_test.go:712`-`714`),
    `TestSecondDaemonRefused` (`lock_test.go:31`-`32`) and
    `TestHerdrLoginRejectsUnsafeSessionName` (`login_test.go:184`).
  - Credentials and logins, about 29: six in `declaredthread_test.go`,
    six in `reply_test.go`, `TestCrewAnswersWithAProvidersCredential`
    (`envcommand_test.go`), `TestLimitsGroupReadsTheGroupsLogin`
    (`limits_test.go:161`), `TestThreadOpenRefusals`,
    `TestThreadStateRefusals`, and the callers of the yard helpers that
    set them: `newAppYard` (`githubapp_test.go:97`-`100`, 3 callers),
    `openYard` (`threadopen_test.go:173`-`176`, 4) and `stateYard`
    (`threadstate_test.go:159`-`162`, 6).
  - The setting variables set to a value, 6:
    `TestAddrSettingsAreCheckedAsTheFlag`,
    `TestSettingsGetPrintsTheValueInEffect`,
    `TestStartAndInstallTakeThePagesFlags`, `TestUpgradeRestoreOn`,
    `TestWebServeAsTakesFlagEnvAndSetting` and
    `TestWebServeJWTRefusals`.
  - `stubServices` (`service_test.go:57`-`70`) sets `PATH`, `HOME`,
    `HERDR_SOCKET_PATH`, `HERDR_SESSION` and `XDG_CONFIG_HOME` as well
    as the service seams. Its 42 callers are already serial as service
    seam tests (section 3.3), and this is a second reason. So are the
    seven `userHomeDir` stubs.
  That is about 69 test functions beyond the 42 `stubServices` callers,
  some of them also in note 6084's serial-essential class. The counts
  are by test function and helper caller at the base of this revision;
  B6 recounts them at its own base, and S7's hygiene data lists them by
  name.
  The other three `t.Chdir` sites are in yard helpers (`beadYard`,
  `peerbeads_test.go:28`; `replyMailYard`, `replymail_test.go:24`;
  `userYardOn`, `users_test.go:31`, together reaching about 40 tests).
  They move only so that `worktreeIdentity` (`identity.go:50`) does not
  find the test package's own worktree. That check reads the directory
  only when the run names a bead, and these helpers' maps name none, so
  their `t.Chdir` is deleted rather than kept, and their tests join the
  parallel set.
- Test homes stop touching the process. `newHome` keeps its directory
  work (a fresh short directory, `shortDir` and the socket-length
  reason it exists) and returns the home; a new constructor beside it
  -- `testApp(t, st, home)` in the test files -- builds
  `&app{st: st, env: map[string]string{"YARDR_HOME": home,
  "YARDR_WEB_ADDR": "", "YARDR_BDP_ADDR": "", ...}}` with the blanks
  `newHome` sets today. During the move the old `t.Setenv` form and the
  new map form coexist per file: a file's tests use one or the other,
  never both, and the file joins the parallel set only in the second
  form. `newAsker` gets the same treatment: values that were
  `t.Setenv`s become entries in the map the test hands its app.
  `noGitHubCredential` (`main_test.go:279`) only blanks the GitHub
  credential variables, which are a process fact, so its blanks move
  into `TestMain`, once, beside the setting variables, and the helper
  is deleted. `noHome` keeps its process form, because what it sets
  is the user's home, which the map does not carry, and its tests stay
  serial; so does every other setting of a process fact listed above.

This is `config.Env`'s lesson applied inward: the runner never lets a
pane read the daemon's process environment for the values that name
its work (`runner.go:64`); after this change a command run never reads
its test process's environment for the values that name its yard.

### 3.3 What stays global and serial, on purpose

Parallelism is claimed per test, not per package, and one class of
tests stays sequential by rule. A test may carry `t.Parallel` only
when it neither assigns a package variable nor calls a helper that
does, in its file's migrated form (writer in a buffer, environment in
its app's map, the process facts of section 3.2 left as the process
has them). These stay global, and
the tests that stub them stay serial:

| Seam | Why it stays global and its tests serial |
|---|---|
| `interactive`, `ask` (huh), `runChat` (bubbletea) | They own the terminal: one keyboard, one screen, real descriptors. Parallel forms would interleave on hardware, not on a writer this design can hand them. |
| `lookPath` (`init.go:218`) | One package variable, which `stubLookPath` and `fakeTools` (`doctor_test.go:43`-`57`) replace with a function that finds only a test's stand-in tools; neither rewrites `PATH`. A test that replaces it must run alone, because every parallel test would see its replacement. Moving it onto the app is possible (Q3) but buys only its own tests, which are few and already fast. |
| `connect`, `spawnDaemon`, `spawn`, `focusAgent`, `paneTail` and kin | They start or address real processes, servers and herdr panes -- the isolated-yard cases of the brief, serial for the same reason a port is. |
| Service seams (`serviceDir`, `serviceCtl`, `serverRunning`, `loginService`, `webService`, `userHomeDir`) | They describe this machine's service manager and user's home. `stubServices` replaces the set as one gesture; per-app copies would multiply that gesture across every doctor test. |
| The process facts (`HOME`, the working directory, `PATH`, `TMPDIR`, herdr's session variables, declared credentials, the setting variables as `store` reads them) | Read below the CLI by `config`, `pack`, `runner`, `hooks`, `limits`, `source` and `store`, or set on the process by `up` for those readers (section 3.2). They are constant while parallel tests run, because Go refuses `t.Setenv` and `t.Chdir` in a parallel test. The tests that set one stay serial; section 3.2 names them. |
| Terminal facts (`term.IsTerminal` on the real descriptors) | Not a seam at all: a read-only fact about the process, safe to read in parallel, never stubbed per test. |

The safety rule that makes this sound is Go's own scheduling: tests
that call `t.Parallel` pause until the sequential tests in the package
have finished, so a sequential test that stubs a seam and restores it
in `t.Cleanup` can never overlap a parallel test reading the default.
What is forbidden is a parallel test that stubs, or a parallel test
that silently depends on another test's stub; the hygiene check in
section 3.4 (S7) is what enforces the rule mechanically. The
serial-essential remainder is the class note 6084 measured -- about
150 tests, about 20.6 s under `-race` -- and the wall arithmetic in
section 3.5 carries it as a sequential block, not as a problem to be
argued away.

### 3.4 The slices, each landing green

Every slice keeps `sh .yardr/test` green on its own commit and changes
no behaviour a user can see. The harness's dual capture (section 3.1)
and the environment's process fallback (section 3.2) are the two
bridges that make the middle slices safe; both bridges are removed by
the slices that finish each thread, not before.

- **S1, writer plumbing.** `app.stdout`/`stderr` fields and their root
  preset, `out(w, ...)` and `table(w, ...)` signatures, `execute`'s
  error on the root's writers, `run` setting the command's buffer
  beside the legacy pipe. The 185 `a.out` closures and 77 `table` call
  sites are updated mechanically in the same commit so the funnel is
  never half-converted; no print helper or direct `fmt.Print*` site
  moves yet, and no test changes shape. This is the only slice whose
  diff is wide by construction, and it is wide in one repeated edit.
- **S2, print helpers take their writer**, in three file groups, each
  its own commit: `beads.go` with `ops.go`'s `printEvent` and the prime
  path; `crew.go`'s inbox family with `subscriptions.go`; then
  `bead_ask.go`, the `pack*.go` family, and the remaining helpers
  (`doctor.go`, `guide.go`, `harness.go`, `timers.go`, `users.go`).
  Their tests hand buffers to the helpers directly and drop `capture`
  for those cases.
- **S3, direct sites by command family.** The remaining `fmt.Print*`
  and raw `os.Stdout` sites move to the writer their `RunE` holds,
  grouped so a command's whole call tree converts at once: bead and
  prime commands; crew, depot and group commands; pack and source
  commands; then the long tail (`peers.go`, `start.go`, `ops.go`,
  `init.go`, `guide.go`, `service.go`, `reply.go`, and the rest of the
  52 files). Warnings in the same files move to `ErrOrStderr` (or the
  app's recorded `stderr` where no command is in reach), the
  package-global `var stderr` is deleted, and `stdin` becomes the
  app's field.
- **S4, the harness lets go of the process.** With no product site
  naming `os.Stdout` for command output, `run` drops the pipe and
  returns the command's buffer alone, and `capture` and its pipe test
  are deleted (the 64 KB case becomes an ordinary large-buffer
  assertion). A grep gate joins the slice: no `fmt.Print` without a
  writer argument, and no `os.Stdout` outside the terminal checks and
  child wiring named in section 3.3, may remain in non-test files.
- **S5, environment plumbing.** `config.LoadWith`, `(*app).config()`,
  `config.ActorWith`, `a.env` with its two lookups, `--home` recorded
  in the map instead of `UseHome` (made absolute against the process's
  directory, as today), and child environments built from
  `a.environ()`, which adds the process facts as the process holds
  them when it renders, so `up`'s merged `PATH` reaches herdr, the
  server and the daemon. The process facts stay process reads
  (section 3.2).
  Fallback to the
  process environment is still in place, so every existing test
  passes unaltered; this slice proves itself by the tests that already
  assert the flag order and the child environment
  (`TestHomeFlagOrder`, `TestHomeFlagReachesChildren`), and by
  `TestUpFindsHerdrWithTheLoginShell` extended to the daemon's
  environment (B5b).
- **S6, test homes become maps**, file group by file group in the same
  groupings as S2/S3: `testApp` (which refuses a map holding a process
  fact it can name) and the map forms of `newHome` and
  `newAsker`; `TestMain` blanks the setting variables and the GitHub
  credential variables once, and `noGitHubCredential` is deleted; the `t.Chdir` in `beadYard`, `replyMailYard` and
  `userYardOn` is deleted. A file is finished when none of its tests
  calls `t.Setenv` or `t.Chdir`, except the named serial tests of
  section 3.2, which set a process fact; the process fallback stays
  until the last file moves.
- **S7, parallelism, and the bridges come down.** Per finished file,
  `t.Parallel` is added to every test the section 3.3 rule allows; the
  fallback to the process environment in the env lookups is deleted
  (an app with no map in a test is a construction error the helpers
  make impossible); a hygiene test in the package (an AST walk in the
  shape of note 6084's classifier) fails when a parallel test reaches
  `capture`-era helpers, `noHome`, a helper that sets a process fact
  (section 3.2) or a named stub helper (Go itself
  refuses `t.Setenv` and `t.Chdir` there);
  and the allow-list row at `.yardr/test:689` is rewritten with the new
  measured number or deleted, as R1/R3 require. The serial remainder
  is named in that test's allow data with its seams, so a future test
  cannot join the serial set silently.

S1-S4 are the output thread (about 1,000 print sites, but one funnel,
about 35 helpers, and then per-family mechanical edits); S5-S6 are the
environment thread (36 `config.Load` sites, 34 environment reads, then
about 66 test files). The two threads cannot interleave, because they
edit the same files. In `internal/cli/root.go`, S1 changes `app`,
`execute`, `out` and `table`, and S5 changes `PersistentPreRunE` and
`store`. Outside root.go, 15 non-test files call `config.Load`
(`guide.go`, `harnesslogin.go`, `init.go`, `limits.go`,
`longrunning.go`, `ops.go`, `pack.go`, `peers.go`, `root.go`,
`sourcecmd.go`, `storecmd.go`, `uninstall.go`, `updown.go`, `web.go`,
`upgrade.go`), and 21 read the environment or the working directory
directly (`beads.go`, `chat.go`, `codextrust.go`, `crew.go`,
`grouplogin.go`, `guide.go`, `harnesslogin.go`, `identity.go`,
`init.go`, `lock.go`, `login.go`, `longrunning.go`, `needs.go`,
`otel.go`, `reply.go`, `review.go`, `root.go`, `service.go`,
`start.go`, `updown.go`, `users.go`). Of the 29 distinct files in
those two lists, 22 also hold print sites that S1, S2 or S3 converts;
only `codextrust.go`, `grouplogin.go`, `identity.go`, `lock.go`,
`needs.go`, `otel.go` and `web.go` hold none. A split by file
ownership would cut across every command family, so the slices are
put in sequence instead: S1, S2, S3, S4, then S5, S6, S7. Each slice
starts from a base where the slice before it has landed, and no two
slices are open on the same file at once. The one part of S5 outside
`internal/cli`, `config.LoadWith` and `config.ActorWith` in
`internal/config/config.go`, touches no file the output thread edits. It may land at any point
before the rest of S5 (bead B5a in section 6).

### 3.5 The expected wall, with the numbers

The work to be parallelised is the allow-list's own arithmetic: 769
sequential tests at 0.10 s mean is about 77 s of work, plus the 67
already parallel (section 2.4; the loaded run's `user 1m15s` agrees).
Threading does not make that work smaller; it lets the machine do it
concurrently. The wall after S7 is bounded by two blocks:

- the serial-essential block of section 3.3 -- about 150 tests,
  measured at about 20.6 s under `-race` in note 6084, carried here
  unshrunk, because this design does not promise to convert it. The
  about 69 tests that set a process fact (section 3.2, beyond the
  `stubServices` callers already in that class) are partly of that
  class already (mid-test environment sequences); counted as wholly
  extra at the 0.10 s mean, they add about 7 s, for a serial block of
  at most about 28 s;
- the parallel block -- the remaining roughly 680 tests, about 50 s
  of the 77 s total -- spread across the machine's cores by
  `t.Parallel` (the test binary runs parallel tests up to
  `GOMAXPROCS`; this machine has 10 cores). That block's ideal is
  about 5 s; its real floor is set by what the tests share underneath:
  git subprocesses (30 test files shell to git), temp directories and
  bbolt files per test, and the race detector's own cost. The
  precedent in the same suite is store's parallel tests and the
  assembly train yards, which parallelise around the same machinery.

Carried honestly, the expectation is an alone wall of about 30-45 s
under `-race` (a serial block of 21-28 s, followed by a parallel block
that contends its way from 5 s ideal to roughly 10-15 s; the two add,
because Go starts the parallel tests only after the sequential ones
have finished), against
today's about 75 s quiet -- under R1's 60 s with the
margin R1's loaded-recheck exists for, and no claim of more. In a full
suite the package also stops setting every other package's wall from
the serial side: it remains one of the larger packages but no longer
the binder at two to three times the next. The beads' done-when
(section 6) therefore asks for the R1 evidence, not for this
projection: three consecutive `go test -race -count=1 ./internal/cli/`
alone at or under 60 s, and the same in representative full-suite runs,
with the new alone number written into the allow-list row if any part
of the serial remainder keeps a row at all.

### 3.6 Beyond tests: what threading output does not buy

It does not buy rpc, web or chat a way to reuse commands. Those parts
already share the level worth sharing -- `store.API`, whose methods
carry typed values -- and a command's text is the least reusable form
of its result: prose laid out for a terminal, or the JSON encoding of
the same value the API returns directly (section 2.5). No bead below
proposes that web or chat capture a command's buffer, and this design
recommends against ever doing so; a future need to show a command's
result on a page is a need for the store call behind it. The honest
product-side gains are smaller and worth naming at their size: the
`--json` error in `execute` and the command's JSON body will provably
go to the same stream (today they name `os.Stdout` in two places);
help, warnings and results will have one documented destination each
instead of a funnel, a global and cobra's defaults; and the root
black-box suites gain nothing, since they drive the built binary,
which is the point of them. The reason for this work is the test wall
and the R3 rule, and that is reason enough; the design does not sell
it as a product feature.

## 4. Left out

- **Any change to the budget numbers.** R1's 60 s and R2's 500 ms
  (`docs/testing.md:14`, `:28`) are the yardr-635g design's decided
  numbers; this design's job is to meet R1 for one package, not to
  renegotiate it. The allow-list row's number is updated with the
  measured result, never with a looser budget.
- **Other packages.** dispatch and store carry the same R3 rule and
  their own fix beads from the 635g design; nothing here proposes a
  mechanism for them, and the app's environment map is a CLI-run value,
  not a new yard-wide config service.
- **A library form of the CLI.** Exporting the commands, or a
  `Run(args, out, env) (int)` entry for embedders, would give rpc, web
  or chat nothing they need (section 3.6) and would freeze a text
  surface as an API. `Execute()` keeps its signature; the new fields
  are unexported presets on an unexported struct.
- **Capturing or threading the interactive surfaces.** The huh forms,
  the bubbletea chat and the login screens keep the terminal they
  already own (section 3.3). Their tests stay serial; making a form
  render into a buffer is a different design with a different user --
  a person at a keyboard -- in view.
- **Threading the process facts.** Every helper that reads `HOME` or
  resolves a relative path would take a base (`config.IsDefaultHome`,
  `ExpandPath`, `UseHome`, `LoadReadOnly`, `pack.LoadDir`, and the CLI
  sites section 3.2 lists); `internal/runner`'s herdr session lookup,
  `hooks.Declared.Credential`, `internal/limits`' command environment
  and `store.Setting.Effective` would each take a lookup; and `up`
  would carry its merged `PATH` in a value instead of the process.
  Those are changes to other packages' APIs, which the bead leaves
  out, and together they buy about 69 tests about 7 s of serial wall.
  The cheapest of them, should B7's measurement ask for one, is the
  credentials: `hooks`' unexported `declaredCredential` already takes
  a lookup (`internal/hooks/reply.go:159`), and the about 29
  credential tests are the largest group. The values stay process
  facts; `testApp`'s refusal keeps a test from pretending otherwise.
- **Moving every seam onto the app.** `lookPath`, `connect`, the
  service and spawn seams could each become an `app` field, and Q3
  prices that. It is left out of the proposal because it converts
  tests this design does not need to convert, at one constructor
  change per seam per test file, for no wall time the serial block
  does not already bound.
- **Region-mutex surgery and sharding the package.** Note 6084's
  option (a) -- a mutex around each global's region so captures and
  environment writers serialise in smaller units -- is refused again:
  it lands at best near the budget with flake risk across 900 tests,
  and it teaches the next 900 tests to share globals carefully instead
  of not sharing them. Splitting `internal/cli` into subpackages to
  parallelise across package binaries is likewise out: the package is
  one command surface over one `app`, and the split would be a second
  design about package shape, taken only if S7's measured wall says
  this one was not enough.
- **Per-test subprocesses.** Running each test's command as a built
  binary would isolate the globals by forking, at the root suites'
  cost model -- a build and a process per test -- and would stop
  testing the in-process paths (`capture`'s pipe test exists because
  in-process output has its own failure modes). The black-box suites
  already cover the binary as a binary.

## 5. Open questions, answered

Each question was the operator's to decide. mckean answered all five
on 19 Oct 2026 through asks 7479-7483 on the bead, choosing the
recommended option each time. The proposal above is written for those
answers. The options are kept below as they were put, so a reader can
see what was weighed.

**Q1. Where does a command's writer live: the app's fields, cobra's
`OutOrStdout`, or both as proposed?** Answered: (c), both (ask 7479).
(a) App fields only (`a.stdout`/`a.stderr`, with `a.out` writing
there): one home for every writer, but every `RunE` and helper must
consult the app even when cobra already carries the same value, and
cobra's help needs a separate wire from the root to the app's field or
it keeps writing to the process. Cost: the S1 slice grows a sync rule
("the root copies its cobra writers onto the app") that every direct
subcommand test must remember.
(b) Cobra only: no app fields at all. `execute`'s pre-command error
then has nowhere per-run to go except the root command it just built
(which is workable -- `root.ErrOrStderr()`), and helpers with no
command in reach, above all `identity`, must be given a writer by
every caller. Cost: a longer tail of call-site changes in S3 for a
distinction users never see.
(c) Both, with cobra as the run-time source and the app as its preset
(section 3.1): cobra's inherit-and-override does the work it already
does, the app's fields exist only for what predates a command, and
`printCallables(w, ...)` is the precedent both halves extend.
Recommended and chosen: (c). It is the smallest new mechanism -- no sync
rule, no second help wire -- and it is the only option under which the
existing test idiom (`cmd.SetOut(&buf)`, `watch_test.go:123`) is
already correct.

**Q2. How do config, home and cwd reach a command: an explicit
environment map on the app (as proposed), or only a resolved
`*config.Config`?** Answered: (b), the environment map with
`config.LoadWith` (ask 7480).
(a) Only `*config.Config`, resolved once: the 36 `config.Load` sites
collapse onto `a.cfg`, and `--home` becomes a field on it. But the
actor, session, bead, crew and `--for` defaults, the blanked
`YARDR_WEB_ADDR`/`YARDR_BDP_ADDR` of `newHome`, and the child
environments are environment, not `Config` fields; they would keep
reading the process, and the tests that set them would stay pinned.
Cost: roughly the `newHome`-reached share of tests -- the largest
single block in note 6084's count -- never parallelises.
(b) The full environment map on the app (`a.env map[string]string`,
section 3.2), with `config.LoadWith` resolving the `Config` from it:
every yardr value a run reads comes from one value a test can
construct (the process facts of section 3.2 -- the user's home, the
working directory, `PATH` and the others read below the CLI -- stay
on the process under either option), and
children receive it through the builders that already take an
environment slice. Cost: one new function in `internal/config` and a
map at every test's app construction (hidden behind `testApp`), plus
the discipline that a value not in the map is absent, not defaulted
from the process -- which is the point, and also the migration's most
likely source of a red slice if a reader is missed.
Recommended and chosen: (b). Option (a) solves the smaller half of the
bead's question and leaves its stated goal -- tests running in
parallel -- unmet for the tests that most need it.

**Q3. Do the stubbed seams (`lookPath`, `connect`, the service and
spawn seams) stay package-global with serial tests, as proposed, or
move onto the app so their tests can parallelise too?** Answered:
they stay global and serial (ask 7481).
Moving them is a field per seam and a constructor change at every
stubbing test's app literal (503 `&app{` literals in the test files
today, though only the stubbing files would change). What it buys is
the parallel conversion of the serial-essential class's stub-driven
share -- part of the about 150 tests and 20.6 s carried in section
3.5 -- at the price of the package's widest test-only diff, taken
before the wall it would improve can be measured without it.
Recommended and chosen: stay global and serial (section 3.3), and let
S7's measured wall decide whether a follow-up bead moves any one seam.
If the measured serial block, not the parallel block, is what keeps
the package near 60 s on a quiet machine, that bead will know exactly
which seam to move; taken now, the move is speculative in the sense
the yard's rules refuse.

**Q4. Is an expected alone wall of about 25-35 s -- under R1's 60 s,
but not near the parallel block's 6 s ideal -- the right promise for
the build beads' done-when?** Answered: R1 as written gates, and the
projection is reported, not gated (ask 7482). The question was put
with the 25-35 s projection; the third critique round added the
serial tests that set a process fact (section 3.2), and section 3.5
now projects about 30-45 s. The answer does not depend on the
projection, because the gate is R1's 60 s.
The alternative promises are a tighter internal target (for example
30 s alone, which would force Q3's seam move or the fat-test trims of
yardr-cayh to be re-opened if contention runs high) or R1 as written
(60 s in three consecutive alone and full-suite runs, the evidence
635g B2 already uses). A tighter target costs a bead's worth of
contingency work against machine load this design cannot control --
the same package measured 75 s quiet and 151 s at load 21 on the same
day (section 2.4) -- while R1's own loaded-recheck already names how
a loaded run is judged.
Recommended and chosen: R1 as written, with section 3.5's projection
reported as the measurement to beat, not as a gate. If S7 lands above the
projection on a quiet machine, that is a finding for the re-measure,
with the number attached.

**Q5. Does the operator confirm that no rpc, web or chat reuse of
command output is in scope?** Answered: none is in scope (ask 7483).
Section 3.6 argues the shared level is `store.API` and that capturing
command text into a page or a socket reply would be a new, worse API.
If the operator instead wants a named consumer -- for example the web
view showing what a named command would print -- that consumer, its
command and its format belong in this decision, because it would
change S2/S3 from "thread the writer" to "stabilise the text as a
contract", a materially larger promise.
Recommended and chosen: none in scope. The beads below thread the
writer for tests and for stream discipline only, and no command's
prose becomes a contract by landing them.

## 6. The beads it would become

In the order they would land. Sizes use the yard's rough scale
(small: a sitting; medium: a day or so of building and review; large:
several sittings, split as noted). Every bead's gate is
`sh .yardr/test` in its worktree, green, and each carries the test
line the brief requires. No bead changes a word, flag or `--json`
shape a user sees; any that finds it must is a different bead and
says so in its note.

**B1. Writer plumbing in internal/cli (S1).** Goal: `app` carries the
writer presets, the root applies them to cobra, `out` and `table`
write to a handed writer, `execute`'s error uses the root's writers,
and `run` captures both the command's buffer and the legacy pipe.
Where to look: `internal/cli/root.go` (`app`, `execute`, `out`,
`table`), `internal/cli/beads_test.go` (`run`),
`internal/cli/show_test.go` (`capture`). Done when: the 185 `a.out`
and 77 `table` call sites compile against the new signatures with
their output unchanged (the existing assertions are the proof);
`capture` still serves the unmigrated helpers; the gate is green.
Size: medium (one repeated edit across 52 files' funnel calls, not
yet their bodies). Depends on: the Q1 decision.

**B2. Print helpers take their writer (S2).** Goal: the about 35
package-level `print*` helpers take `w io.Writer` first, following
`printCallables`, and their tests hand buffers instead of capturing.
Where to look: `beads.go`, `ops.go` (`printEvent`), `crew.go`,
`subscriptions.go`, `bead_ask.go`, `pack.go` and its `pack*.go`
siblings, `doctor.go`, `guide.go`, `harness.go`, `timers.go`,
`users.go`, `call.go`. Done when: no helper in the bead's file group
prints to a global; its tests use no `capture` for those helpers; the
gate is green. Size: medium, and it splits by the three file groups
of S2 into B2a/B2b/B2c of small-to-medium each if review prefers.
Depends on: B1.

**B3. Direct print sites by command family (S3).** Goal: every
remaining `fmt.Print*` and raw `os.Stdout` site in command bodies
writes to its `RunE`'s writer, warnings to its `ErrOrStderr`, the
global `var stderr` is deleted, and `stdin` becomes the app's field.
Where to look: the 52 non-test files by family -- bead/prime; crew,
depot, group; pack, source; then the long tail (`peers.go`,
`start.go`, `ops.go`, `init.go`, `guide.go`, `service.go`,
`reply.go`). Done when: per family, its commands' output is captured
by `run`'s buffer alone in its tests, and no `fmt.Print*` without a
writer argument and no raw `os.Stdout` remains in the family's files
(the package-wide check is B4's). Size: large, landing
as one small-to-medium bead per family (four beads). Depends on: B1,
B2 (a family's helpers must already take the writer its bodies pass).

**B4. The harness lets go of the process (S4).** Goal: `run` returns
the command's buffer with no pipe; `capture` and the 64 KB pipe test
are deleted; a grep gate in the package's tests proves no non-test
`fmt.Print*` lacks a writer argument and no non-test site names
`os.Stdout` outside the terminal checks and child wiring of section
3.3. Where to look: `beads_test.go`, `show_test.go`
(`TestCaptureOutputLargerThanThePipe` becomes a large-buffer
assertion), `root.go`. Done when: `capture` has no callers and is
gone; the package's tests still assert the same output; the gate is
green. Size: small. Depends on: B3.

**B5a. config.LoadWith and config.ActorWith (the internal/config
part of S5).** Goal: `config.LoadWith(get)` resolves a `Config` from
a lookup for its `YARDR_*` values, and `Load` becomes a call to it
with the process lookup, sharing one body (the socket-length refusal
and home creation included); `config.ActorWith(get)` does the same
for `Actor`'s `YARDR_ACTOR` and `USER`, keeping its OS lookup and
timeout. The default home and a relative home's resolution stay on
the process's user home and directory (section 3.2). Where to look:
`internal/config/config.go` (`Load`, `Getenv`, `Actor`) and its
tests. Done when: a test in `internal/config` resolves two different
homes and two different actors from two maps without touching the
process environment; every existing `Load` and `Actor` caller is
unchanged; the gate is green. Size: small. Depends on: nothing; Q2
is answered. It edits no file in `internal/cli`, so
it may land at any point before B5b, in parallel with B1-B4.

**B5b. Environment plumbing in internal/cli (the rest of S5).** Goal:
`app` carries `env` and `config()`; the 36 `config.Load`
call sites use `a.config()`, and `resolveActor` and `init` use
`config.ActorWith`; `--home` is recorded in the run's map; children
are started from `a.environ()`, which adds the process facts of
section 3.2 as the process holds them when it renders; the process
fallback keeps every existing test true. Where to look:
`internal/cli/root.go` (`PersistentPreRunE`, `store`,
`resolveActor`), `internal/cli/identity.go`, the 15 files that call
`config.Load` and the 21 that read the environment (section 3.4
lists them), the child builders (`daemonEnv`, `server.ServerEnv`,
`serviceEnv`; `home_test.go`), and `up`'s `PATH` merges
(`herdrProgram`, `updown.go:147`-`168`; `useLoginPATH`,
`login.go:240`-`250`), which keep setting the process `PATH`. Done
when: `TestHomeFlagOrder` and `TestHomeFlagReachesChildren` pass
against the threaded path (both stay serial: they set `HOME` through
`noHome`, and the second keeps `t.Chdir` for its relative `--home`);
`TestUpFindsHerdrWithTheLoginShell` also asserts that the environment
the daemon is started with (`daemonEnv` over `a.environ()`) holds the
merged `PATH`, login-shell directories included, and a test of
`up --login` asserts the same after `useLoginPATH`, both staying
serial because they set `PATH`; no `--home` handling calls
`os.Setenv`; the other process-fact reads of section 3.2 are
unchanged; the gate is green. Size: medium. Depends on: B4 and B5a. It edits root.go
after B1 does, and `ops.go`, `init.go`, `guide.go` and the other
long-tail files after B3 has converted them, so it starts from a
base where the whole output thread has landed.

**B6. Test homes become environment maps (S6).** Goal: `testApp`
(refusing a map holding `HOME`, `PATH`, `TMPDIR` or a herdr session
variable) and the map forms of `newHome` and `newAsker` replace
`t.Setenv` file group by file group; `TestMain` blanks the setting
variables and the GitHub credential variables once, in place of
`newHome`'s and `noGitHubCredential`'s per-test blanks, and
`noGitHubCredential` is deleted; and the `t.Chdir` in the yard helpers `beadYard`,
`replyMailYard` and `userYardOn` is deleted. The tests section 3.2
names as setting a process fact keep their process form: `noHome`
and the direct `HOME` settings, the seven direct `t.Chdir` tests,
the `PATH`, `TMPDIR` and herdr-session tests, the credential tests
and their yard helpers (`newAppYard`, `openYard`, `stateYard`), the
six setting-variable tests, and `stubServices`. Where to look:
`init_test.go` (`newHome`, `shortDir`), `home_test.go` (`noHome`),
`main_test.go` (`TestMain`, `noGitHubCredential`),
`askharness_test.go` (`newAsker`), then the 66 files holding
`t.Setenv` sites, grouped as in B3. Done when: per file group, every
remaining `t.Setenv` or `t.Chdir` is in a test or helper on section
3.2's list, recounted at the bead's base and written in its note; the
groups' outputs and homes are asserted as before; the gate is
green. Size: large, landing as one small-to-medium bead per
file group. Depends on: B5b. Its test files are the ones B2 and B3
already edited to drop `capture`, so it follows them as well.

**B7. Parallelism and the bridges come down (S7).** Goal:
`t.Parallel` on every test the section 3.3 rule allows, the process
fallback in the environment lookups deleted, the AST hygiene test
guarding the rule, and the `.yardr/test:689` allow-list row rewritten
with the measured number or deleted. Where to look: every migrated
test file, `.yardr/test` (the row), `docs/testing.md` (R3's account
of cli), and note 6084's classifier for the hygiene test's shape.
Done when: three consecutive `go test -race -count=1
./internal/cli/` alone and three in representative full-suite runs
are each at or under R1's 60 s (or the row remains, rewritten with
the quiet-machine number and the reason, naming the seam that keeps
its remainder serial); the serial remainder is enumerated in the
hygiene test's allow data; the gate is green, with the before/after
alone times on the bead. Size: medium (the marking is mechanical;
the work is in what the first parallel runs shake out). Depends on:
B4 and B6.

**B8. Re-measure cli against the 635g table.** Goal: the R7
re-measurement for this package -- the alone and full-suite numbers
beside B7's, one week on, with flake or contention findings filed as
their own beads rather than fixed here. Where to look:
`docs/design/2026-10-16-yardr-635g-test-time.md` section 2.1 (the
table this updates for one row), timer t107's card, and
`.yardr/test`'s R1 lines. Done when: the deltas are on the bead as a
note; R3's allow-list holds no cli row B7 could have deleted; any
rule that missed is a filed follow-up or a closed wont-fix with the
number. Size: small. Depends on: B7.


