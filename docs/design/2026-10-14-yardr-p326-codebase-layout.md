# yardr's codebase layout: what store and cli hold, and how to split the large files without stopping the line

Design yardr-p326, proposed 2026-10-14. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory that beads are worked in; a bead is one durable piece of work. A flow is the stages a bead passes through; a stage is one step of a flow. The crew are the yard's always-on agents, tied to no bead; the yardmaster is the crew member that coordinates. The assembly is the group that tests an approved bead and lands it on the base. The line is what a bead travels in a depot. A pack is a way of working as one directory. A hook is a webhook endpoint that turns an outside system's deliveries into events; a source is that outside system. A peer is another yard this one is linked with. The operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

Every size and count below was measured at 2469fd9 (main, 14 Oct 2026) in the bead's worktree; each names the command it came from.

## 1. The question

mckean, 14 Oct 2026: "the structure of yardr, is it proper? codebase layout." The yardmaster's look, from sizes and the import graph without reading the code, found the layout mostly sound and three strains: `internal/store` at 35k lines in 59 files, `internal/cli` at 33k lines with logic in its commands, and three single files of 2400-3000 lines (`pack/plan.go`, `runner/herdr.go`, `source/declaration.go`). The question is what, if anything, to change about that, and in what order, so the work can be built in small beads while builders keep working in parallel worktrees.

## 2. What exists today

### 2.1. The layout, measured

One binary: `main.go` is 10 lines and calls `cli.Execute()`. All code is under `internal/` in 27 packages (`ls internal/`), with no import cycles (the tree builds: `go list -deps ./internal/...`). Non-test source is 114,166 lines; tests are 144,709 lines (`find internal -name '*.go' [! -name '*_test.go'] | xargs wc -l`).

The one seam to the yard is `store.API`, the interface in `internal/store/api.go`: 221 methods (`grep -cE '^\t[A-Z][A-Za-z0-9]*\(ctx' internal/store/api.go`). `*Store` implements it in the owning process (one process holds the bbolt file; see the package comment in `internal/store/store.go`), and `rpc.Client` implements it for every other process over the yard's unix socket. The server side needs no per-method code: `rpc/server.go` (`newAPI`) builds its dispatch table from the interface by reflection. The client side is handwritten: `rpc/client.go` holds 225 per-method wrappers (`grep -c '^func (c \*Client)'`). So adding one API method means editing three files in two packages: the interface, the `*Store` method, and the client wrapper. That funnel matters in section 2.4.

`internal/cli` reaches the yard over the socket: `root.go` (`app.store`) connects through `server.Connect`, starting the server on first use. The command handlers take `store.API`, never `*Store`, so commands cannot tell the socket from the owning process. `internal/web` and `internal/chat` also program to `store.API`. The socket's wire format (method name plus arguments as JSON, named for the 22 calls in `rpc.Calls` plus 2 in `rpc.OperatorCalls`, an array otherwise) is unchanged by everything proposed here.

The non-test import graph between internal packages (`grep` over non-test files only, per package directory) is:

```text
store   -> agents config pairing peer source yardkey
cli     -> agents bdp chat config depotfile dispatch flowdraw hooks limits
           notify otel pack pairing rpc runner server source store version
           web yardkey yards                        (22 of the other 26)
dispatch -> agents config hooks limits notify source store version yardkey yards
runner  -> agents config depotfile dispatch hooks otel peer scripttest
           source store yardkey
pack    -> config depotfile hooks runner source store
server  -> config hooks rpc runner store version yards
web     -> dispatch flowdraw limits store
chat    -> agents dispatch store
rpc     -> pairing peer store version
notify  -> config hooks source store
limits  -> config source store
hooks   -> config source store
otel    -> agents store
flowdraw -> pack store
yards   -> pairing store version yardkey
bdp     -> store
depotfile -> store
source  -> agents config
pairing -> yardkey
yardkey -> config
agents, config, peer, scripttest, standin, version -> (nothing internal)
storetest -> config rpc store   (test helper: throwaway stores, over the
                                socket with YARDR_TEST_RPC=1)
```

Two corrections to the yardmaster's look: `store` also imports `config` (environment reads such as `STORE_NOSYNC`), and only these files reach outside it: `source` from `runask_list.go`, `secretask.go`, `sourcecalls.go`, `sources.go`; `agents` from `migrate.go`, `routing.go`, `settings.go`; `peer` from `caller.go`; `pairing` from `devicepair.go`, `pairing.go`; `yardkey` from `pairing.go`, `peers.go`, `peersend.go`. Everything else in `store` is bbolt, JSON and the standard library.

### 2.2. What store holds, and how its files share

`internal/store` is 35,108 lines in 59 files (`wc -l`, non-test files). The largest are `routing.go` (1663), `peers.go` (1622), `beads.go` (1523), `migrate.go` (1471), `peerbeads.go` (1459), `graph.go` (1284) and `peersend.go` (1248). Each file is one area of the yard's model, and each mixes the area's rules with its bbolt reads and writes: `Route.Choose` judges quotas next to the route buckets in `routing.go`; gate logic (`ready`, `openBlockers`, `holds`) sits next to bead rows in `beads.go`; peer message checks sit next to the outbox in `peers.go`. The yardmaster's phrase is accurate: it is the yard's model, not only its storage. The pure exceptions are already free functions (`Route.Choose` takes a quota function; `Widened` and `SameCall` take declarations and grants), which is what a split of rules from storage would have to invent everywhere else.

The files share one transaction core, and share it densely:

- `.event(` (append an event in this transaction) is called from 37 of the 59 files; `s.write(` from 32; `resolve(` (any bead reference to its UUID) from 8.
- `store.go` defines the `Store` struct, the `wtx` transaction type, the event pump and hub, 23 bucket variables and the `get`/`put`/`each`/`keys` helpers every area file uses. Area files own their own further buckets (`peers.go` appends its three to `allBuckets` in `init()`), so buckets are the one thing already factored per area.
- `migrate.go` holds the schema stamp (currently 19) and every migration, which run in order inside `Open`. It is the only store file that grows without bound: each schema bump appends to it.

The tests around it are 122 files and 38,093 lines, of which 38 files are `package store` (internal tests) plus `export_test.go`, which exposes unexported helpers to the rest. Any split of the package breaks those internal tests first.

### 2.3. What cli holds, and where its logic sits

`internal/cli` is 32,654 lines in 59 files. The largest are `beads.go` (2055), `pack.go` (1929), `crew.go` (1787), `sourcecmd.go` (1541), `init.go` (1460) and `peers.go` (1434). A cli package that imports 22 internal packages is expected: commands wire flags to the system. Reading the large files, most of their lines are legitimately the terminal's: flag parsing, interactive questions, and printing. Three shapes of logic sit in them:

1. **Inline subcommand closures.** `beadCmd` (`beads.go:355-1543`) builds 14 subcommands with their `RunE` bodies inline, about 1200 lines that can only be reached through cobra. The same shape, smaller, holds `pack.go`'s verbs and `crew.go`'s `crewCmd`/`crewSetCmd`.
2. **Multi-call orchestration.** `plannedImport.apply` (`pack.go:836-911`) calls `pack.Apply` correctly, then folds each declared source's `-declared` links and records into the pack's source itself (`MoveLinkSource` plus `MoveSourceRecords` per source, with its own failure sentences). The fold is pack-apply behaviour living in the command.
3. **Pure declaration diffing that only the terminal reads.** `callChanges`, `stillGranted` and `grantedCall` (`sourcecmd.go:1336-1438`) diff two `source.Declaration` values against the yard's grants. They return English sentences for the terminal; no other surface reads them. The same file already delegates the one shared computation, `store.Widened`, down to store.

The rule below cli is already followed where a second surface exists: `routewhy.go` (205 lines) explains routing by calling `store.RouteChoice.Why()`, and `MatchRoute` in `store/routing.go` serves dispatch, web, pack and cli alike. And one apparent misplacement is deliberate: `store/runask_list.go` parses command words ("how its words are read into one stored form") because the server executes an approved run ask itself; the parser must live where execution is trusted, and cli never parses those words. It stays.

### 2.4. Where changes collide, measured

**Churn.** Of the last 300 commits on main, 75 touch `internal/store` (25%), 115 touch `internal/cli` (38%), 22 touch `internal/rpc`, and the three large files barely move: `plan.go` 9, `herdr.go` 12, `declaration.go` 4. (Method: `git log --format=%H -300`, then `git show --name-only` per commit.) So the large files are a readability strain, not a collision strain.

**Co-touching.** Of the last 200 commits touching store, `api.go` is in 40 (every new API method), then `beads.go` 25, `peers.go` 24, `asks.go` 24, `crew.go` 22, `store.go` 20, `routing.go` 20, `migrate.go` 19. Outside store the same commits touch `rpc/client.go` 40 times (the wrapper for each new method), `cli/beads.go` 35, `rpc/api_test.go` 24. Within store, 42 commits touch 1 file, 50 touch 2, 30 touch 3, with a tail to 30; no pair of area files dominates. New behaviour routinely crosses `api.go` plus one area file plus, sometimes, `store.go` or `migrate.go`: areas are not independently changeable in practice.

**Rebase conflicts.** `~/.yardr/logs/refinery.log` spans 2026-10-03 to 2026-10-14: 619 landings, 36 conflict events (`== <bead> conflicts`). By file: `rpc/api_test.go` 6, `cli/beads.go` 3, `docs/configuration.md` 3, `docs/api.md` 3, `store/crew.go` 2, `runner_test.go` 2, `cli/web.go` 2, then singles including `store.go`, `events.go`, `blobs.go`, `beads.go` and `herdr.go`. Store files as a whole barely conflict; the hot spots are the API contract test (a registry every documented call joins, with `TestAPIDoc` regenerating `docs/api.md`, which is why the doc conflicts too) and the biggest cli command file.

**In flight, today.** 8 branches are unmerged (`git merge-base --is-ancestor` over 585 `yard/*` branches; the rest are landed). Two of them, `yard/yardr-9tha` and `yard/yardr-sk5h`, overlap on 14 files: `cli/subscriptions.go`, `rpc/client.go`, `store/api.go`, `store/export_test.go`, `store/hooks.go`, `store/migrate.go`, `store/migrate_test.go`, `store/notifications.go`, `store/notifications_test.go`, `store/notifications_internal_test.go`, `store/store.go`, `store/subscriptions.go`, `store/users.go`, `store/yardrsource.go`. That is the funnel of section 2.1 happening live: two beads adding API surface meet in `api.go`, `client.go`, `store.go` and `migrate.go`. The yard holds 44 open beads in the `yardr` depot (`yardr bead list -d yardr`).

**Blast radius.** `.yardr/test` says it outright: "A change in a package nearly everything imports (store, config) makes it nearly the whole suite, which is right." Every store change is tested by the whole tree's tests.

**Verdict for the bead's first question.** Store's size is not a merge-conflict problem (6 store-file conflicts in 619 landings) and not a findability problem (files are named by area, and areas are where changes land). It is a funnel problem: `api.go`, `rpc/client.go`, `store.go` and `migrate.go` take a share of every API-adding change, and every store change runs the whole suite. The proposal relieves the funnels without moving the areas.

### 2.5. The three large files, by concern

`internal/pack/plan.go` (2968 lines) is the import planner: `Build` plus the `planner` type's per-object passes, in plan order. By line number (`grep -n '^func '`): the plan's vocabulary, types plus small helpers (29-610); `Build`, the planner core and name/file passes (612-990); groups (994-1250); flows and strays (1251-1415); routes (1416-1519); crew, subscriptions and open threads (1519-1930); stage maps (1929-2130); declared sources (2130-2454); calls and grants (2454-2647); records and removals (2647-2968). Churn 9/300, no conflicts: a reading problem.

`internal/runner/herdr.go` (2507 lines) is the herdr runner: start a bead's agent in a worktree, watch its pane, stop it. By line number: errors, `run` and `ServerUp` (109-180); names and labels, all pure (180-272); `Start`, briefs, dir depots, git base and worktree setup (272-700); agent environment and shell detection (696-780); brief files (780-868); agent start and prompt placing (868-1180); harness screen and pane parsing, nearly all pure functions (1180-1779); status, stop and finish (1779-2013); git and workspace helpers plus agent arguments (1991-2121); crew start and environment (2121-2400); tail, history, worked and nudge (2401-2507). The package already splits this runner by concern into `herdr_api.go`, `herdr_events.go` and `herdr_session.go`. Churn 12/300, one conflict.

`internal/source/declaration.go` (2405 lines) is the declared-source file: the model (types plus accessors, 1-855) and its YAML reader (`Parse`, the `reader` core and dispatch, 856-1129; per-key readers, 1129-2353). The per-key readers follow the keys of the file: limits and commands (1129-1240); thread, link and identity (1240-1509); hook and events (1509-1819); notify and hear (1819-2009); credential, calls, params, requests and bodies (2022-2353). Sibling files are bare nouns per concern (`call.go`, `command.go`, `credential.go`, `mint.go`). Churn 4/300, no conflicts: a reading problem.

## 3. The proposal

The smallest version that answers the question: keep `store` one package, split the three large files plus the one unbounded file by concern with no API change, move one orchestration down out of cli, and write down the rule that keeps cli thin. Each step is its own bead (section 6); the pure moves are reviewed as moves.

**Store stays whole.** No model/storage split and no area split (section 4 says what each would cost). The areas share one transaction core too densely to separate, changes cross areas too routinely for a split to isolate them, and the evidence shows no collision or findability problem a split would fix. What store gets is the funnel relief in the next paragraphs and open question 1.

**Split the three large files by concern, inside their packages, no API change.** Unexported functions move; exported names stay where callers find them (same package, so even that is invisible). The new files, with their line ranges at 2469fd9:

- `plan.go` keeps the vocabulary, `Build`, the planner core and the file passes (29-990). New: `plan_groups.go` (994-1250), `plan_flows.go` with flows, strays and routes (1251-1519), `plan_crew.go` (1519-1930), `plan_maps.go` (1929-2130), `plan_sources.go` with declared sources, calls and grants (2130-2647), `plan_drop.go` with records and removals (2647-2968). The `plan_` prefix follows the `herdr_` prefix `runner` already uses; `pack` has no underscore files today, and the prefix keeps the planner's parts visibly together.
- `herdr.go` keeps the struct, `run`, `ServerUp` and the whole start path with its environment, git and argument helpers (1-180, 272-780, 1991-2121). New: `herdr_labels.go` (180-272), `herdr_brief.go` (780-868), `herdr_prompt.go` (868-1180), `herdr_screen.go` (1180-1779), `herdr_status.go` with status, stop, finish, tail, history, worked and nudge (1779-2013, 2401-2507), `herdr_crew.go` (2121-2400).
- `declaration.go` keeps the package doc, the core types (`Declaration`, `Credential`, `Call`, `Param`, `Request`, `Answer`, `Error`) with their accessors, `Parse`, the reader core and dispatch, and the credential/call/param/request/body readers (1-161, 644-1240 in rearranged order, 2022-2353). New, one key's types plus its reader plus its accessors each, as bare nouns like the existing `call.go` and `command.go`: `limits.go`, `thread.go` (with identity and link), `hook.go` (with events), `notify.go` (with hear).

**Split `migrate.go` by era.** The migrations up to a settled schema version move to `migrate_old.go`; `migrate.go` keeps the stamp, the checks and the live migrations. Same package, unexported functions, order preserved: a pure move that stops the only unbounded file from being the file every migration bead opens. Open question 3 takes whether this bead runs.

**Move the `-declared` fold from `cli/pack.go` into `pack.Apply`.** `plannedImport.apply` folds each declared source after applying; that loop moves into `apply.go` (on `ApplyOptions`, which already carries actor and answers) and cli keeps the printing. `pack.Apply` has exactly one caller today (`grep pack.Apply`, only `cli/pack.go`), so the move changes no other surface; its tests move with it.

**Extract the bead subcommands from the `beadCmd` closure.** Each of the 14 subcommands in `beads.go:355-1543` (create, show, history, list, update, note, ask, answer, advance, done, close, hold, unhold, release) becomes a named constructor (`newBeadShowCmd` and so on) in files by group (`beads.go` keeps the command and list/show; new `bead_note.go`, `bead_ask.go`, `bead_move.go` take note, ask/answer, and the moves), with `beadCmd` assembling them. No flag, sentence or behaviour changes; the bodies become namable and the hottest cli file stops being one textual collision span. Open question 2 takes now or later.

**The rule for builders.** There is no builder role file in this repository (`.yardr/roles/` holds `merger.md` and `yardmaster.md`; the builder's instructions arrive in its brief), so this sentence is for the yard's builder brief, not for a repo edit: "A command parses flags, asks the operator, and prints: anything two surfaces need, or any orchestration with failure modes of its own, goes in the package the command read it from, behind a function the tests call without a terminal." The positive example is `routewhy.go` delegating to `store.RouteChoice.Why()`; the negative example being removed is the `-declared` fold.

If open question 1 goes the draft's way, one further bead generates `rpc/client.go`'s wrappers from `store.API` (see the question).

## 4. Left out

**Splitting store by area (peers/pairing apart first).** Cost: the shared core (`Store`, `wtx`, `event`, the hub and pump, `get`/`put`/`each`/`keys`, `resolve`, the buckets in `store.go`, the migrations) must live somewhere both packages import, which is either a new exported surface on store (worse than today: the one seam becomes two) or a new `storekernel`-style package both import (a layer for its own sake, against the flat-packages constraint). It breaks the 38 internal test files and `export_test.go`, touches all 8 in-flight branches that reach store (2 do today), and buys nothing the evidence asks for: changes cross areas routinely (section 2.4), so area packages would still change together, only now across package boundaries and with wider review.

**Splitting the model from its storage.** The rules take `*bolt.Tx` or `*wtx` almost everywhere (`ready` and the gates in `beads.go`, `queuePeer` in `peers.go`, the settlement pass in `store.go`); separating them means threading transactions through a new boundary across 59 files, with the pure exceptions (`Route.Choose`, `Widened`, `SameCall`) proving the split buys little where it already exists. Same test cost as above, plus a seam the codebase would have to learn.

**Moving cli's sentence-building diff helpers down.** `callChanges`, `stillGranted` and `grantedCall` in `sourcecmd.go` return terminal sentences and have one reader. Moving them to `source` would spread terminal wording through a lower package for no second consumer; if web or chat ever needs declaration diffs, the set logic moves then, returning data, with cli keeping the sentences.

**Generating `docs/api.md` further, or unifying the bead views.** `TestAPIDoc` already generates the doc's call sections from socket replies, which is why the doc conflicts when the contract test does; the remaining conflicts are small and mechanical. `cli`'s `printBeadFlow` and `web/beads.go` each assemble family plus next stages for their own surface; unifying them would couple the terminal's sentences to the page's, and neither reader has asked.

**A `store.API` change of any kind, and any wire change.** Every option above keeps the interface and the socket format byte for byte; the server's reflection dispatch means several of the moves need no rpc change at all.

## 5. Open questions

1. **Generate `rpc/client.go`'s wrappers from `store.API`?** Options: (a) a `go generate` step that writes the 225 wrappers from the interface, so adding a method edits `api.go` and the area file only; (b) keep them handwritten, as the most explicit form of the client. (a) costs one bead plus generator upkeep, removes the 40-in-200 co-touch and one conflict source, and changes no wire byte; its risk is a generated file builders must remember to regenerate, held by a test that fails when the two drift. (b) costs nothing and keeps the funnel. The draft's pick is (a).
2. **Extract the bead subcommands from the `beadCmd` closure now, or leave it until it bites again?** Options: (a) now, as section 3 says: one pure-move bead on the hottest cli file (3 conflicts, the only cli file above 2000 lines); (b) later, when a bead's change forces it. (a) needs a quiet line on `beads.go`, which at 35 co-touches per 200 store commits is the hardest quiet here to find; (b) risks nothing except the next textual collision. The draft's pick is (a), dispatched when nothing in flight touches `beads.go`.
3. **Split `migrate.go` by era?** Options: (a) one pure-move bead now, so migration beads stop opening the 1471-line file that holds every era; (b) leave it, since migrations append at the end and rarely collide (no `migrate.go` conflict in the log's span). The draft's pick is (a): it is small, and it is the only file in the package guaranteed to grow.
4. **Ever split store by area, with peers/pairing first?** Options: (a) no, keep one package (section 3); (b) yes, starting with the peer files. (b) costs a shared kernel package, the 38 internal test files rewritten, and coordination with every in-flight store branch, for areas that the co-touch data shows change together anyway. The draft's pick is (a), revisited only if a repeated funnel analysis shows area pairs colliding on their own.

## 6. The beads it would become

In landing order. A pure move changes no behaviour: same package, unexported functions moved, diff reviewed as a move. "Quiet" means the yardmaster dispatches it when no in-flight branch touches the named files; the three large files qualify today (none of the 8 unmerged branches touches any of them).

1. **Split `pack/plan.go` by concern (pure move).** Goal: the sections of 2.5 become `plan.go` plus `plan_groups.go`, `plan_flows.go`, `plan_crew.go`, `plan_maps.go`, `plan_sources.go`, `plan_drop.go`. Where to look: `internal/pack/plan.go`, function map in section 2.5. Done when: the package builds, its tests pass unchanged, and no exported name moves packages. Quiet on `plan.go`. Depends on nothing.
2. **Split `runner/herdr.go` by concern (pure move).** Goal: `herdr.go` plus `herdr_labels.go`, `herdr_brief.go`, `herdr_prompt.go`, `herdr_screen.go`, `herdr_status.go`, `herdr_crew.go`. Where to look: `internal/runner/herdr.go`, following the existing `herdr_api.go`/`herdr_events.go`/`herdr_session.go` precedent. Done when: as bead 1. Quiet on `herdr.go`. Depends on nothing.
3. **Split `source/declaration.go` by key (pure move).** Goal: `declaration.go` plus `limits.go`, `thread.go`, `hook.go`, `notify.go`, each holding its key's types, reader and accessors. Where to look: `internal/source/declaration.go`, beside `call.go`/`command.go`/`credential.go`/`mint.go`. Done when: as bead 1, plus the hooks test that holds `BuiltIn` against the adapters still passes. Quiet on `declaration.go`. Depends on nothing.
4. **Split `store/migrate.go` by era (pure move).** Goal: settled migrations move to `migrate_old.go`; order and behaviour identical. Where to look: `internal/store/migrate.go`, schema 19. Done when: the store suite passes including `migrate_test.go`, and a store stamped at each old version still opens (the suite's migration tests say how). Quiet on `migrate.go`. Depends on nothing; runs only if open question 3 is (a).
5. **Move the `-declared` fold into `pack.Apply`.** Goal: the fold loop leaves `cli/pack.go`'s `plannedImport.apply` for `pack/apply.go`; cli keeps the printing. Where to look: `internal/cli/pack.go:836-911`, `internal/pack/apply.go`. Done when: `pack apply` of a source pack folds `-declared` links and records exactly as today (a pack test asserts it), and no cli test covers the fold anymore. Runs beside other work. Depends on nothing.
6. **Extract the bead subcommands from the `beadCmd` closure (pure move).** Goal: 14 named constructors in `beads.go`, `bead_note.go`, `bead_ask.go`, `bead_move.go`; `beadCmd` assembles. Where to look: `internal/cli/beads.go:355-1543`. Done when: `yardr bead --help` and every subcommand's help are byte-identical, and the cli bead tests pass unchanged. Quiet on `beads.go`. Depends on nothing; runs only if open question 2 is (a).
7. **Generate the rpc client wrappers from `store.API`.** Goal: `go generate` writes `rpc/client.go`'s method wrappers; a test fails when generated output drifts from the interface. Where to look: `internal/rpc/client.go`, `internal/store/api.go`, `rpc/server.go`'s reflection table for the method list. Done when: the generated file is byte-identical in behaviour (the rpc suite passes, including over the socket), and deleting a wrapper by hand fails the drift test. Runs beside other work; wide review. Depends on nothing; runs only if open question 1 is (a).
