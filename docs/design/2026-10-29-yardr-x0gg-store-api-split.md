# The split of store.API: one interface per concept, and every method mapped

Design yardr-x0gg, proposed 2026-10-29. The full method map behind
`docs/architecture.md`'s "The split of `store.API`": every one of the 296
methods of `store.API` (counted at f75abf88; this bead adds no store
method), its interface from that page, and its concept. The page names
the interfaces and assigns each cross-concept verb; this document maps
every method, so none is silently dropped. The split itself is later
work: this map is the proposal the split beads build from.

How it was read: the method list is `store.API` in `internal/store/api.go`
in order, and each method's concept was checked against its implementing
file and, where the name did not settle it, its body. A method is filed
under the concept of the records it reads and writes; a method spanning
concepts stands under exactly one owner, the page's cross-concept list.
Two methods fit no interface and stand unassigned with the reason, never
forced.

Runner has no interface and no section below: the store holds no runner
records, and sessions carry runner state (the handle `Runner.Start`
returns is stored on the session).

## Beads (58)

Beads and everything bead-scoped: the lifecycle, notes, deps, links, asks, folds, and bead reads.

| method | concept |
|---|---|
| `CreateBead` | bead |
| `GetBead` | bead |
| `ListBeads` | bead |
| `Ready` | bead |
| `Waiting` | bead |
| `Dispatchable` | bead |
| `UpdateBead` | bead |
| `Advance` | bead |
| `AdvanceNote` | bead |
| `Done` | bead |
| `CloseBead` | bead |
| `Reopen` | bead |
| `SetHold` | bead |
| `Release` | bead |
| `FoldBeads` | bead |
| `Unfold` | bead |
| `SidingBeads` | bead |
| `FoldSidings` | bead |
| `AddDep` | bead |
| `RemoveDep` | bead |
| `Deps` | bead |
| `AllDeps` | bead |
| `OpenBlockers` | bead |
| `HoldingDeps` | bead |
| `BlockerCycles` | bead |
| `CheckBlockers` | bead |
| `Family` | bead |
| `Discovery` | bead |
| `AddLink` | bead |
| `RemoveLink` | bead |
| `MoveLink` | bead |
| `MoveLinkSource` | bead |
| `Links` | bead |
| `IssueLinks` | bead |
| `AddNote` | bead |
| `AddNoted` | bead |
| `AddNotedAs` | bead |
| `AckNote` | bead |
| `MarkNoteTold` | bead |
| `MarkNoteUntold` | bead |
| `Notes` | bead |
| `AddAsk` | bead |
| `AskNew` | bead |
| `AddAnswer` | bead |
| `AddAnswerExpected` | bead |
| `UserAsks` | bead |
| `WriterAsks` | bead |
| `Bead` | bead |
| `Estimate` | bead |
| `EstimateMany` | bead |
| `Arrivals` | bead |
| `AnswerAsk` | bead |
| `DeferAsk` | bead |
| `DeclineAsk` | bead |
| `TakeBackAsk` | bead |
| `GiveSecret` | bead |
| `ApproveAsk` | bead |
| `ApproveSetup` | bead |

## Flows (12)

Flows, their pointers, and reads of beads through their flows.

| method | concept |
|---|---|
| `Flows` | flow |
| `PutFlow` | flow |
| `ListFlows` | flow |
| `GetFlow` | flow |
| `DeleteFlow` | flow |
| `SetFlowPointer` | flow |
| `FlowPointers` | flow |
| `RepoFlows` | flow |
| `DepotFlows` | flow |
| `NextStages` | flow |
| `FlowMap` | flow |
| `Stranded` | flow |

## Routes (4)

Routes: which group takes a stage's beads.

| method | concept |
|---|---|
| `AddRoute` | route |
| `SetRoute` | route |
| `Routes` | route |
| `DeleteRoute` | route |

## Groups (5)

Groups: who works a stage.

| method | concept |
|---|---|
| `PutGroup` | group |
| `SetGroup` | group |
| `Groups` | group |
| `GetGroup` | group |
| `DeleteGroup` | group |

## Sessions (17)

Sessions: claims and the work's state.

| method | concept |
|---|---|
| `Claim` | session |
| `MarkRunning` | session |
| `EndSession` | session |
| `MarkStopped` | session |
| `MarkCountDue` | session |
| `SetHandle` | session |
| `RecordUsage` | session |
| `MarkCleaned` | session |
| `SnoozeSession` | session |
| `GetSession` | session |
| `Sessions` | session |
| `ListSessions` | session |
| `Unstopped` | session |
| `ClosedWithWorkspace` | session |
| `Uncounted` | session |
| `FailureCount` | session |
| `SessionScreen` | session |

## Events (8)

Events: the log, its head, and its streams.

| method | concept |
|---|---|
| `Events` | event |
| `EventsOfKind` | event |
| `EventsSince` | event |
| `Head` | event |
| `LogDispatch` | event |
| `Subscribe` | event |
| `TailEvents` | event |
| `Wake` | event |

## Depots (4)

Depots: where beads are worked.

| method | concept |
|---|---|
| `PutDepot` | depot |
| `Depots` | depot |
| `GetDepot` | depot |
| `DeleteDepot` | depot |

## Sources (16)

Sources: declarations, keys, grants, and calls.

| method | concept |
|---|---|
| `MoveSourceRecords` | source |
| `PutSource` | source |
| `Sources` | source |
| `GetSource` | source |
| `SetSource` | source |
| `KeySource` | source |
| `KeyUser` | source |
| `KeyUserCredential` | source |
| `DeleteSource` | source |
| `UnignoreSource` | source |
| `GrantSource` | source |
| `RevokeSource` | source |
| `SourceGrants` | source |
| `SourceCalls` | source |
| `SessionCalls` | source |
| `CallSource` | source |

## Users (16)

Users: users, devices and pairing, teams, and the checks that name a user.

| method | concept |
|---|---|
| `CheckFor` | user |
| `AddUser` | user |
| `Users` | user |
| `GetUser` | user |
| `SetUser` | user |
| `DeleteUser` | user |
| `Teammates` | user |
| `UninviteTeammate` | user |
| `AddDevice` | user |
| `Devices` | user |
| `RemoveDevice` | user |
| `DevicePairOpen` | user |
| `DevicePairClose` | user |
| `DevicePairOpenSelf` | user |
| `DevicePairCloseSelf` | user |
| `DevicePairCodeSelf` | user |

## Mail (14)

Mail: mail, inboxes, notifications, and the outbound push queue.

| method | concept |
|---|---|
| `Mail` | mail |
| `React` | mail |
| `Inbox` | mail |
| `AckInbox` | mail |
| `InboxFor` | mail |
| `AckInboxFor` | mail |
| `UserInbox` | mail |
| `AckUserInbox` | mail |
| `UserNotifications` | mail |
| `AckUserNotifications` | mail |
| `Conversation` | mail |
| `Pushes` | mail |
| `DropPushes` | mail |
| `PushTried` | mail |

## Crew (36)

Crew: crew members and the members serving a user.

| method | concept |
|---|---|
| `PutCrew` | crew |
| `Crew` | crew |
| `GetCrew` | crew |
| `DeleteCrew` | crew |
| `SetCrewEnabled` | crew |
| `CrewStarting` | crew |
| `CrewStarted` | crew |
| `CrewSettled` | crew |
| `CrewStopped` | crew |
| `CrewGone` | crew |
| `CrewLeft` | crew |
| `CrewStatus` | crew |
| `SetCrewHandle` | crew |
| `CrewStartFailed` | crew |
| `RequestRestart` | crew |
| `MarkRotationAsked` | crew |
| `MarkContextAsked` | crew |
| `SetContext` | crew |
| `MarkNotified` | crew |
| `CrewStartingFor` | crew |
| `CrewStartedFor` | crew |
| `CrewSettledFor` | crew |
| `CrewStoppedFor` | crew |
| `CrewGoneFor` | crew |
| `CrewLeftFor` | crew |
| `CrewStatusFor` | crew |
| `SetCrewHandleFor` | crew |
| `CrewStartFailedFor` | crew |
| `RequestRestartFor` | crew |
| `MarkRotationAskedFor` | crew |
| `MarkContextAskedFor` | crew |
| `SetContextFor` | crew |
| `MarkNotifiedFor` | crew |
| `RecordCrewUsage` | crew |
| `CrewPrime` | crew |
| `CrewPrimeFor` | crew |

## Peers (38)

Peers: peers, pairing, and beads, mail and reads across the link.

| method | concept |
|---|---|
| `AddPeer` | peer |
| `Peers` | peer |
| `GetPeer` | peer |
| `DeletePeer` | peer |
| `AllowPeer` | peer |
| `RenamePeer` | peer |
| `SetPeerURL` | peer |
| `SetPeerIntake` | peer |
| `RemovePeerIntake` | peer |
| `SetPeerIdentity` | peer |
| `RemovePeerIdentity` | peer |
| `PeerSend` | peer |
| `MailPeer` | peer |
| `PeerOutbox` | peer |
| `PeerDelivered` | peer |
| `PeerSendFailed` | peer |
| `PeerRefused` | peer |
| `PeerRefusedRate` | peer |
| `PeerSendDisallowed` | peer |
| `PeerBeadSend` | peer |
| `PeerRead` | peer |
| `PeerReadKept` | peer |
| `PeerNote` | peer |
| `SettlePeerBeads` | peer |
| `PeerFetches` | peer |
| `PeerFetched` | peer |
| `PeerFetchFailed` | peer |
| `PeerBases` | peer |
| `PeerPulled` | peer |
| `PeerPostAnswered` | peer |
| `CheckPeer` | peer |
| `PairPeer` | peer |
| `PairRefused` | peer |
| `PairOpen` | peer |
| `GetPairing` | peer |
| `PairAnswer` | peer |
| `PairName` | peer |
| `PairClose` | peer |

## Hooks (12)

Hooks: hooks, their deliveries, and the issue moves a hook's map drives.

| method | concept |
|---|---|
| `PutHook` | hook |
| `Hooks` | hook |
| `GetHook` | hook |
| `DeleteHook` | hook |
| `HookPayload` | hook |
| `DeliveryFiles` | hook |
| `EventsOfHook` | hook |
| `IssueMoves` | hook |
| `IssueMoveAllowed` | hook |
| `IssueMoveFailed` | hook |
| `IssueMoved` | hook |
| `IssueMoveSkipped` | hook |

## Threads (10)

Threads: participation in outside threads.

| method | concept |
|---|---|
| `FollowThread` | thread |
| `UnfollowThread` | thread |
| `Threads` | thread |
| `Replying` | thread |
| `Replied` | thread |
| `Opening` | thread |
| `Opened` | thread |
| `AllowOpening` | thread |
| `SettingState` | thread |
| `StateSet` | thread |

## Subscriptions (8)

Subscriptions: names subscribed to sources, and their identities.

| method | concept |
|---|---|
| `SetIdentity` | subscription |
| `AddSubscription` | subscription |
| `AddSubscriptionKind` | subscription |
| `RemoveSubscriptions` | subscription |
| `RemoveSubscriptionsKind` | subscription |
| `RemoveSubscription` | subscription |
| `RemoveSubscriptionKind` | subscription |
| `Unwatch` | subscription |

## Timers (4)

Timers: asking the yard to tell something later.

| method | concept |
|---|---|
| `AddTimer` | timer |
| `Timers` | timer |
| `RemoveTimer` | timer |
| `FireTimers` | timer |

## Memories (5)

Memories: remembered beads and crew journals.

| method | concept |
|---|---|
| `Journal` | memory |
| `Remember` | memory |
| `Recall` | memory |
| `RecallJournal` | memory |
| `ExpireMemories` | memory |

## Previews (9)

Previews: proposed, approved and running.

| method | concept |
|---|---|
| `ProposePreview` | preview |
| `Previews` | preview |
| `GetPreview` | preview |
| `ApprovePreview` | preview |
| `RefusePreview` | preview |
| `RestartPreview` | preview |
| `PreviewHead` | preview |
| `PreviewStop` | preview |
| `ForgetPreview` | preview |

## Packs (2)

Packs: the import record.

| method | concept |
|---|---|
| `RecordPack` | pack |
| `Packs` | pack |

## Blobs (4)

Blobs: note, mail and delivery attachments by hash.

| method | concept |
|---|---|
| `NoteBlob` | blob |
| `MailBlob` | blob |
| `DeliveryBlob` | blob |
| `PutBlob` | blob |

## Settings (4)

Settings: yard settings and the telemetry switch.

| method | concept |
|---|---|
| `Settings` | setting |
| `SetSetting` | setting |
| `TelemetryShow` | setting |
| `TelemetrySet` | setting |

## Permissions (1)

Permissions: the gate every record stands behind.

| method | concept |
|---|---|
| `May` | permission |

## Maintenance (7)

Maintenance: keeping the stored state healthy.

| method | concept |
|---|---|
| `Snapshot` | maintenance |
| `Prune` | maintenance |
| `Backup` | maintenance |
| `RecordGOCACHEOld` | maintenance |
| `GOCACHEOldDirs` | maintenance |
| `RemoveGOCACHEOld` | maintenance |
| `Close` | maintenance |

## Unassigned (2)

No record, so no interface: `LoginWait` and `LoginWaitReady` are
an in-memory rendezvous between the leader that prints a login
reference and the waiters for it. They store nothing (they only read
the group's owner), so no split interface owns them.

| method | concept |
|---|---|
| `LoginWait` | — |
| `LoginWaitReady` | — |

## Tally

58 + 12 + 4 + 5 + 17 + 8 + 4 + 16 + 16 + 14 + 36 + 38 + 12 + 10 + 8 + 4 + 5 + 9 + 2 + 4 + 4 + 1 + 7 assigned, 2 unassigned: 296, the whole of `store.API`.
