# Architecture

Yardr's core types, the future split of `store.API`, yardr's packages in
layers, and which internal package may import which. This page is the
norm; `TestImportRules` in `arch_test.go` (package main, at the
repository root) is its check, and the two change together: a new import
that breaks the rules fails the gate.

## Core types

Seven of the eight live in `internal/store`, where the records live:

| type | owning package | what it is |
|---|---|---|
| bead | `internal/store` | one durable piece of work: type, stage, notes, edges |
| flow | `internal/store` | the stages of a bead's life and the moves allowed |
| route | `internal/store` | which group takes the beads of a stage |
| group | `internal/store` | who works a stage, and its runner |
| runner | `internal/dispatch` | how the work starts: the `Runner` interface, implemented in `internal/runner` |
| event | `internal/store` | the ordered log of what happened |
| depot | `internal/store` | where a bead is worked: the repository and what tests it |
| source | `internal/store` | a service outside the yard: the `Source` record, declared and called through `internal/source` |

Other packages keep minimal shapes where they must (eta); the store's
are canonical.

Three more words from the [core concepts](../README.md#core-concepts)
are not types of their own:

- a pack is the files a depot's flows, groups, routes and sources arrive as; the store holds the records they become.
- the crew is the sessions that do the work, tied to no bead; runners start them, the store records them as sessions and events.
- a user is who mail goes to; identity is an actor string on beads, notes and events, not a type the core passes around.

## The split of `store.API`

One named interface per concept, for the future split of the 296-method
`store.API` (the split itself is later work; the names are decided
here). Runner has no interface: the store holds no runner records, and
sessions carry runner state. The full map of all 296 methods to these
interfaces is `docs/design/2026-10-29-yardr-x0gg-store-api-split.md`.

Beads (beads and everything bead-scoped: notes, deps, links, asks,
folds), Flows (flows, their pointers, flow-scoped reads), Routes,
Groups, Sessions (claims and the work's state), Events (the log and its
streams), Depots, Sources (declarations, keys, grants, calls), Users
(users, devices, teams), Mail (mail, inboxes, notifications, the push
queue), Crew, Peers (peers, pairing, beads across the link), Hooks
(hooks, deliveries, issue moves), Threads (participation in outside
threads), Subscriptions, Timers, Memories (remembered beads and crew
journals), Previews, Packs, Blobs, Settings (settings and the telemetry
switch), Permissions (`May`), Maintenance (snapshot, prune, backup,
close, cache rotation).

Each cross-concept verb is owned by exactly one interface:

- Beads: `Advance`, `AdvanceNote`, `Done` (a bead's stage moves,
  validated against flows); `Release` (frees the bead's session);
  `Ready`, `Dispatchable`, `Waiting`, `SidingBeads`, `FoldSidings`
  (routing reads over beads); `MoveLink`, `MoveLinkSource` (a bead's
  tracked issue); `GiveSecret` (a secret ask keys its source);
  `UserAsks` (asks awaiting a user); `Estimate`, `EstimateMany`,
  `Arrivals` (a bead's ETA); `Family` (a bead's train and base).
- Flows: `NextStages`, `FlowMap`, `Stranded` (a bead read through its
  flow); `SetFlowPointer`, `RepoFlows`, `DepotFlows` (a depot's flows);
  `Flows` (the travelling board).
- Sessions: `Claim` (a bead, group and stage become a session).
- Sources: `SourceCalls`, `SessionCalls` (what a session may call);
  `KeyUser`, `KeyUserCredential` (a user's credentials for a source);
  `MoveSourceRecords` (a source's rename).
- Users: `CheckFor` (`--for` names a user); `SetUser` (notify names a
  source); `Teammates`, `UninviteTeammate` (the mapping joined to users);
  the `DevicePair` methods (a user's device enrolment).
- Mail: `Conversation`; `UserInbox`, `AckUserInbox`, `UserNotifications`,
  `AckUserNotifications`; `Pushes`, `DropPushes`, `PushTried` (the
  outbound queue).
- Crew: the `*For` methods (a crew member serving a user);
  `RecordCrewUsage`, `CrewPrime`, `CrewPrimeFor`.
- Peers: `PeerBeadSend`, `SettlePeerBeads`, `PeerFetches`,
  `PeerFetched`, `PeerFetchFailed`, `PeerBases`, `PeerPulled` (beads
  across the link); `PeerRead`, `PeerReadKept`, `PeerNote` (reading and
  noting a peer's bead); `MailPeer`; `SetPeerIntake`,
  `RemovePeerIntake` (a peer's depots); `SetPeerIdentity`,
  `RemovePeerIdentity` (who a peer acts as).
- Hooks: the `IssueMove` methods (a hook's map moves the tracked issue).
- Threads: `FollowThread` through `StateSet` (a crew member's or user's
  participation in an outside thread).
- Subscriptions: `SetIdentity`; the `Add/RemoveSubscription` methods
  (names subscribed to sources).
- Timers: `FireTimers` (due timers fire, reading bead state).
- Memories: `Remember` (a memory is kept as a bead); `Journal`,
  `RecallJournal` (a crew member's journal).
- Maintenance: `Snapshot`, `Prune`, `Backup` (the whole store).
- Permissions: `May` (every record's gate).
- Unassigned: `LoginWait`, `LoginWaitReady` (an in-memory rendezvous
  between a leader and waiters; no record, so no interface).

## Layers

38 packages under `internal/` at f75abf88, each in one layer (36 since
the store-as-leaf splits folded `shared` and `udiff` into `store`):

- leaves, importing no internal package (5): `pairing`, `peer`, `rpc/clientgen`, `scripttest`, `version`.
- store (1): `store`. Measured: 155 non-test Go files, a 296-method `store.API`, 3 internal imports — the 2 allow-listed edges and the peer exception under the rules. Shared types the leaf splits move stay in store itself: there is no separate types package.
- services (26): `agents`, `bdp`, `chat`, `config`, `depotfile`, `diskkeep`, `edgelink`, `eta`, `flowdraw`, `flowdraw/gen`, `hooks`, `limits`, `notify`, `otel`, `pack`, `preview`, `rpc`, `rpc/gen`, `source`, `standin`, `storetest`, `telemetry`, `testload`, `web`, `yardkey`, `yards`. (`storetest` and `testload` are test-only helpers; `rpc/gen` and `flowdraw/gen` are generators.)
- dispatch, runner, server (3): the control loop and the process owners.
- cli (1): `cli`. Measured: imports 27 of the 36 internal packages.
- main: the root package, the binary; the only importer of `cli`.

## Import rules

Three rules; the test holds them, with today's breaking edges in one
allow table, each row naming the bead that removes it:

(a) `internal/store` imports no `internal` package, except
`internal/peer`, which stays: it is OS process code with no yard
types, so the edge carries none of the coupling the rule is there to
stop. Besides that one, today it imports 2 (`config`, `source`);
each stands in the table until its split bead cuts it.

(b) A leaf imports no internal package.

(c) No internal package imports `internal/cli`; only the root `main` may.

The rules cover `internal/` only; `contrib/` and the packs are outside
them, as is moving any code, which the split beads do.
