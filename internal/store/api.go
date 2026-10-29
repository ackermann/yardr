package store

import (
	"context"
	"encoding/json"
	"time"
)

// API is everything the rest of yard uses. *Store implements it in the
// owning process; rpc.Client implements it for every other process.
type API interface {
	CreateBead(ctx context.Context, nb NewBead, actor string) (*Bead, error)
	GetBead(ctx context.Context, id string) (*Bead, error)
	ListBeads(ctx context.Context, f BeadFilter) ([]Bead, error)
	Ready(ctx context.Context) ([]Bead, error)
	Waiting(ctx context.Context) ([]Dep, error)
	Dispatchable(ctx context.Context) ([]ReadyBead, error)
	UpdateBead(ctx context.Context, id string, p BeadPatch, exp Expect, actor string) (*Bead, error)
	Advance(ctx context.Context, id, stage string, exp Expect, actor string) (*Bead, error)
	AdvanceNote(ctx context.Context, id, stage, note string, exp Expect, actor string) (*Bead, error)
	Done(ctx context.Context, id, outcome, note string, exp Expect, actor string) (*Bead, error)
	CloseBead(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error)
	Reopen(ctx context.Context, id string, exp Expect, actor string) (*Bead, error)
	SetHold(ctx context.Context, id string, hold bool, exp Expect, actor string) (*Bead, error)
	Release(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error)
	FoldBeads(ctx context.Context, ids []string, opts FoldOptions, actor string) (*Bead, error)
	Unfold(ctx context.Context, carrier, member, to, reason, actor string) (*Bead, error)
	SidingBeads(ctx context.Context, ids []string) ([]SidingBead, error)
	FoldSidings(ctx context.Context, at time.Time, beads []Bead, routes []Route) (*SidingPass, error)

	AddDep(ctx context.Context, from, to, kind, at, until, actor string) error
	RemoveDep(ctx context.Context, from, to, kind, at, until, actor string) error
	Deps(ctx context.Context, id string) ([]Dep, error)
	AllDeps(ctx context.Context) ([]Dep, error)
	OpenBlockers(ctx context.Context, id string) ([]string, error)
	HoldingDeps(ctx context.Context, id string) ([]Dep, error)
	BlockerCycles(ctx context.Context) ([][]string, error)
	CheckBlockers(ctx context.Context, refs []string) error
	CheckFor(ctx context.Context, user string) error
	Family(ctx context.Context, id string) (*Family, error)
	Discovery(ctx context.Context, id string) (*Discovery, error)
	AddLink(ctx context.Context, id string, nl NewLink, actor string) (*LinkChange, error)
	RemoveLink(ctx context.Context, id, issue, actor string) error
	MoveLink(ctx context.Context, issue, to, actor string) (*LinkChange, error)
	MoveLinkSource(ctx context.Context, from, to, actor string) (int, error)
	MoveSourceRecords(ctx context.Context, from, to, actor string) (*SourceMove, error)
	Links(ctx context.Context, id string) ([]Link, error)
	IssueLinks(ctx context.Context, issue string) ([]Link, error)
	AddNote(ctx context.Context, id, text, actor string, files ...BlobFile) error
	AddNoted(ctx context.Context, id, text, actor string) (*Note, error)
	AddNotedAs(ctx context.Context, id, text, actor, session string, files ...BlobFile) (*Note, error)
	AckNote(ctx context.Context, id string, seq uint64, session, actor string) (*Note, error)
	MarkNoteTold(ctx context.Context, id string, seq uint64, session, delivery string) error
	MarkNoteUntold(ctx context.Context, id string, seq uint64, session, reason string) error
	Notes(ctx context.Context, id string) ([]Note, error)
	NoteBlob(ctx context.Context, hash string) (*BlobRef, error)
	MailBlob(ctx context.Context, viewer, hash string) (*BlobRef, error)
	DeliveryBlob(ctx context.Context, hash string) (*BlobRef, error)
	AddAsk(ctx context.Context, id string, ask Ask, actor string) (uint64, error)
	AskNew(ctx context.Context, nb NewBead, ask Ask, actor string) (*AskedBead, error)
	AddAnswer(ctx context.Context, id string, ans Answer, actor string) error
	// AddAnswerExpected is AddAnswer guarded by the bead revision, checked
	// in the same write as the answer. Browser forms use it so two tabs
	// cannot both answer from the same page.
	AddAnswerExpected(ctx context.Context, id string, ans Answer, actor, revision string) error
	UserAsks(ctx context.Context, name, actor string, viewer ...string) (*AskQueue, error)
	IdentityHeldPayloads(ctx context.Context, bead string, ask uint64, actor string) ([]string, error)
	WriterAsks(ctx context.Context, writer string, viewer ...string) ([]WriterAsk, error)
	Bead(ctx context.Context, id string, viewer ...string) (*BeadView, error)
	// ETACell and BeadStays are the figures an estimate is computed
	// from over the socket: the cell's answered durations, and each
	// bead's stay clocks, which the served bead does not carry.
	ETACell(ctx context.Context, group, label, stage string) (ETACell, error)
	BeadStays(ctx context.Context, ids []string) (map[string]BeadStay, error)
	AnswerAsk(ctx context.Context, id string, ask uint64, option int, words, actor string, weighed bool) error
	DeferAsk(ctx context.Context, id string, ask uint64, actor string) error
	DeclineAsk(ctx context.Context, id string, ask uint64, words, actor string) error
	TakeBackAsk(ctx context.Context, id string, ask uint64, reason, actor string) error
	GiveSecret(ctx context.Context, id string, ask uint64, values map[string]string, actor string, weighed bool) (*SourceKeyed, error)
	LoginWait(ctx context.Context, id, group string) (string, error)
	LoginWaitReady(ctx context.Context, id string) error
	ApproveAsk(ctx context.Context, id string, ask uint64, actor string, weighed bool, values map[string]string) (*AskApproved, error)
	ApproveSetup(ctx context.Context, id string, ask uint64, option int, values map[string]string, account, actor string, weighed bool) (*AskSetupApproved, error)

	PutDepot(ctx context.Context, r Depot, actor string) error
	Depots(ctx context.Context) ([]Depot, error)
	GetDepot(ctx context.Context, name string) (*Depot, error)
	DeleteDepot(ctx context.Context, name, actor string) (*DepotRemoval, error)

	PutGroup(ctx context.Context, g Group, actor string) error
	SetGroup(ctx context.Context, name string, e GroupEdit, actor string) (*GroupChange, error)
	Groups(ctx context.Context) ([]Group, error)
	GetGroup(ctx context.Context, name string) (*Group, error)
	DeleteGroup(ctx context.Context, name, actor string) (int, error)
	AddRoute(ctx context.Context, r Route, actor string) (*Route, error)
	SetRoute(ctx context.Context, id string, e RouteEdit, actor string) (*RouteChange, error)
	Routes(ctx context.Context) ([]Route, error)
	DeleteRoute(ctx context.Context, id, actor string) error

	PutFlow(ctx context.Context, f Flow, actor string) (*Flow, error)
	ListFlows(ctx context.Context) ([]Flow, error)
	GetFlow(ctx context.Context, name string) (*Flow, error)
	DeleteFlow(ctx context.Context, name, actor string) error
	SetFlowPointer(ctx context.Context, depot, typ, flow, actor string) error
	FlowPointers(ctx context.Context) ([]FlowPointer, error)
	RepoFlows(ctx context.Context, depot string) (*RepoFlows, error)
	DepotFlows(ctx context.Context, depot string) ([]DepotFlow, error)
	NextStages(ctx context.Context, id string) (*NextStages, error)
	FlowMap(ctx context.Context, id string) (*FlowMap, error)
	Stranded(ctx context.Context, depot string) ([]StrandedBead, error)

	Claim(ctx context.Context, beadID, group, stage, actor string, via ...Routed) (*Session, error)
	MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error
	EndSession(ctx context.Context, sessionID, state, reason string) error
	MarkStopped(ctx context.Context, sessionID string) error
	MarkCountDue(ctx context.Context, sessionID string) error
	SetHandle(ctx context.Context, sessionID string, handle map[string]any) error
	RecordUsage(ctx context.Context, sessionID string, usage map[string]any) error
	MarkCleaned(ctx context.Context, sessionID string) error
	SnoozeSession(ctx context.Context, sessionID string, dur time.Duration, why, actor string) (time.Time, error)
	GetSession(ctx context.Context, id string) (*Session, error)
	Sessions(ctx context.Context, all bool) ([]Session, error)
	ListSessions(ctx context.Context, f SessionFilter) ([]Session, error)
	Unstopped(ctx context.Context) ([]Session, error)
	ClosedWithWorkspace(ctx context.Context) ([]Session, error)
	Uncounted(ctx context.Context) ([]Session, error)
	FailureCount(ctx context.Context, bead, stage string) (int, error)
	SessionScreen(ctx context.Context, session string) (*SessionScreen, error)

	Events(ctx context.Context, bead string, limit int) ([]Event, error)
	EventsOfKind(ctx context.Context, kind string, limit int) ([]Event, error)
	EventsSince(ctx context.Context, after uint64, limit int) ([]Event, error)
	Head(ctx context.Context) (uint64, error)
	LogDispatch(ctx context.Context, bead, kind, actor string, data map[string]any) error
	Snapshot(ctx context.Context) (*Snapshot, error)
	Prune(ctx context.Context, keep Retention, actor string) (*PruneResult, error)
	Backup(ctx context.Context, path string) (*BackupResult, error)

	Settings(ctx context.Context) (map[string]string, error)
	SetSetting(ctx context.Context, key, value, actor string) error
	TelemetryShow(ctx context.Context) (*TelemetryPreview, error)
	TelemetrySet(ctx context.Context, on bool, actor string) (*TelemetryChange, error)

	RecordGOCACHEOld(ctx context.Context, dir string, at time.Time) error
	GOCACHEOldDirs(ctx context.Context) ([]GOCACHEDir, error)
	RemoveGOCACHEOld(ctx context.Context, dir string) error

	PutCrew(ctx context.Context, def CrewMember, actor string) (*CrewMember, error)
	Crew(ctx context.Context) ([]CrewMember, error)
	GetCrew(ctx context.Context, name string) (*CrewMember, error)
	DeleteCrew(ctx context.Context, name, rev, actor string) error
	SetCrewEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error
	CrewStarting(ctx context.Context, name string, handle map[string]any) error
	CrewStarted(ctx context.Context, name string, handle map[string]any) error
	CrewSettled(ctx context.Context, name string) error
	CrewStopped(ctx context.Context, name, reason string) error
	CrewGone(ctx context.Context, name, reason string) error
	CrewLeft(ctx context.Context, name string, left []map[string]any) error
	CrewStatus(ctx context.Context, name, status string) error
	SetCrewHandle(ctx context.Context, name string, handle map[string]any) error
	CrewStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error
	RequestRestart(ctx context.Context, name, reason, rev, actor string) error
	MarkRotationAsked(ctx context.Context, name string) error
	MarkContextAsked(ctx context.Context, name string, pct, window int) error
	SetContext(ctx context.Context, name string, pct, window int) error
	MarkNotified(ctx context.Context, name string, seq uint64) error
	CrewStartingFor(ctx context.Context, name, user string, handle map[string]any) error
	CrewStartedFor(ctx context.Context, name, user string, handle map[string]any) error
	CrewSettledFor(ctx context.Context, name, user string) error
	CrewStoppedFor(ctx context.Context, name, user, reason string) error
	CrewGoneFor(ctx context.Context, name, user, reason string) error
	CrewLeftFor(ctx context.Context, name, user string, left []map[string]any) error
	CrewStatusFor(ctx context.Context, name, user, status string) error
	SetCrewHandleFor(ctx context.Context, name, user string, handle map[string]any) error
	CrewStartFailedFor(ctx context.Context, name, user, reason string, backoff time.Duration) error
	RequestRestartFor(ctx context.Context, name, user, reason, rev, actor string) error
	MarkRotationAskedFor(ctx context.Context, name, user string) error
	MarkContextAskedFor(ctx context.Context, name, user string, pct, window int) error
	SetContextFor(ctx context.Context, name, user string, pct, window int) error
	MarkNotifiedFor(ctx context.Context, name, user string, seq uint64) error
	RecordCrewUsage(ctx context.Context, name, user, agentSession string, usage map[string]any) error
	Journal(ctx context.Context, name, kind, text, actor string) (bool, error)
	Mail(ctx context.Context, to, text, actor string, files ...BlobFile) error
	React(ctx context.Context, seq uint64, emoji, actor string) error
	PutBlob(ctx context.Context, data []byte) (*BlobRef, error)
	Inbox(ctx context.Context, name string, limit int) (*InboxPage, error)
	AckInbox(ctx context.Context, name string, through uint64) error
	InboxFor(ctx context.Context, name, user string, limit int) (*InboxPage, error)
	AckInboxFor(ctx context.Context, name, user string, through uint64) error
	AddUser(ctx context.Context, name, actor string) (*User, error)
	Users(ctx context.Context) ([]User, error)
	GetUser(ctx context.Context, name string) (*User, error)
	SetUser(ctx context.Context, name string, set map[string]string, actor string) (*User, error)
	DeleteUser(ctx context.Context, name, actor string) error
	Teammates(ctx context.Context) ([]Teammate, error)
	UninviteTeammate(ctx context.Context, user, actor string) (*Uninvited, error)
	AddDevice(ctx context.Context, user, name, actor string) (*NewDevice, error)
	Devices(ctx context.Context, user string) ([]Device, error)
	RemoveDevice(ctx context.Context, user, name, actor string) (*Device, error)
	DevicePairOpen(ctx context.Context, user, name, actor string) (*DevicePairingCode, error)
	DevicePairClose(ctx context.Context, actor string) (*DevicePairing, error)
	DevicePairOpenSelf(ctx context.Context, user, name string) (*DevicePairingCode, error)
	DevicePairCloseSelf(ctx context.Context, user string) (*DevicePairing, error)
	DevicePairCodeSelf(ctx context.Context, user string) (*DevicePairingCode, error)
	UserInbox(ctx context.Context, name string, limit int, actor string) (*Mailbox, error)
	AckUserInbox(ctx context.Context, name string, through uint64, actor string) error
	UserNotifications(ctx context.Context, name string, limit int, actor string) (*NotificationPage, error)
	AckUserNotifications(ctx context.Context, name string, through uint64, actor string) error
	Conversation(ctx context.Context, a, b string, after uint64, limit int, before uint64) ([]Letter, bool, error)
	AddTimer(ctx context.Context, nt NewTimer, actor string) (*Timer, error)
	Timers(ctx context.Context, name string) ([]Timer, error)
	RemoveTimer(ctx context.Context, id, actor string) (*Timer, error)
	FireTimers(ctx context.Context, at time.Time) (*TimerPass, error)
	CrewPrime(ctx context.Context, name string) (*Prime, error)
	CrewPrimeFor(ctx context.Context, name, user string) (*Prime, error)
	Remember(ctx context.Context, m NewMemory, actor string) (*Bead, error)
	Recall(ctx context.Context, scope, query string) ([]MemoryInfo, error)
	RecallJournal(ctx context.Context, scope, query string) ([]JournalHit, error)
	ExpireMemories(ctx context.Context) (int, error)
	SetIdentity(ctx context.Context, name, source string, id Identity, actor string) error
	AddSubscription(ctx context.Context, name string, sub Subscription, actor string) error
	AddSubscriptionKind(ctx context.Context, name, kind string, sub Subscription, actor string) error
	RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error)
	RemoveSubscriptionsKind(ctx context.Context, name, kind, source, scope string, where map[string]string, actor string) (int, error)
	RemoveSubscription(ctx context.Context, name string, sub Subscription, actor string) error
	RemoveSubscriptionKind(ctx context.Context, name, kind string, sub Subscription, actor string) error
	Unwatch(ctx context.Context, name, bead, actor string) (int, error)
	FollowThread(ctx context.Context, name, source, thread, actor string) error
	UnfollowThread(ctx context.Context, name, source, thread, actor string) error
	Threads(ctx context.Context, name string) ([]Participation, error)
	Replying(ctx context.Context, name string, seq uint64, to, body, actor string, files ...BlobFile) (*Reply, error)
	Replied(ctx context.Context, name string, rp Reply, actor string) error
	Opening(ctx context.Context, name, source, in, title, body, actor string) (*Reply, error)
	Opened(ctx context.Context, name string, rp Reply, actor string) error
	AllowOpening(ctx context.Context, name string, sources []string, pack, actor string) ([]string, error)
	SettingState(ctx context.Context, name, source, thread, actor string) (*Participation, error)
	StateSet(ctx context.Context, name string, ts ThreadState, actor string) error

	PutHook(ctx context.Context, h Hook, actor string) (*Hook, error)
	Hooks(ctx context.Context) ([]Hook, error)
	GetHook(ctx context.Context, name string) (*Hook, error)
	DeleteHook(ctx context.Context, name, actor string) error
	HookPayload(ctx context.Context, seq uint64) ([]byte, error)
	DeliveryFiles(ctx context.Context, seq uint64) ([]BlobRef, error)
	EventsOfHook(ctx context.Context, kind, hook string, limit int) (*HookEvents, error)
	PutSource(ctx context.Context, file, text, actor string) (*Source, error)
	Sources(ctx context.Context) ([]Source, error)
	GetSource(ctx context.Context, name string) (*Source, error)
	SetSource(ctx context.Context, name, key, value, actor string) (*Source, error)
	KeySource(ctx context.Context, name string, values map[string]string, actor string) (*SourceKeyed, error)
	KeyUser(ctx context.Context, name, src string, values map[string]string, actor string) (*SourceKeyed, error)
	KeyUserCredential(ctx context.Context, name, src string, values map[string]string, actor string) (*SourceKeyed, error)
	DeleteSource(ctx context.Context, name, actor string) error
	UnignoreSource(ctx context.Context, name, sender, actor string) error
	GrantSource(ctx context.Context, src, what string, to Grantee, as, actor string) (*SourceGrant, error)
	RevokeSource(ctx context.Context, src, what string, to Grantee, as, actor string) (*SourceGrant, error)
	SourceGrants(ctx context.Context, src string) ([]SourceGrant, error)
	SourceCalls(ctx context.Context, session, actor string) (*Callables, error)
	SessionCalls(ctx context.Context, session string) (*Callables, error)
	CallSource(ctx context.Context, req SourceCall) (*SourceAnswer, error)
	Pushes(ctx context.Context) ([]Push, error)
	DropPushes(ctx context.Context, name string, seqs []uint64) error
	PushTried(ctx context.Context, name string, seqs []uint64, res PushResult) error
	IssueMoves(ctx context.Context) ([]IssueMove, error)
	IssueMoveAllowed(ctx context.Context, id string) (string, error)
	IssueMoveFailed(ctx context.Context, id, reason string, retryAt time.Time) error
	IssueMoved(ctx context.Context, id string, ts ThreadState) error
	IssueMoveSkipped(ctx context.Context, id string, skip IssueSkip) error

	AddPeer(ctx context.Context, p Peer, actor string) (*Peer, error)
	Peers(ctx context.Context) ([]Peer, error)
	GetPeer(ctx context.Context, name string) (*Peer, error)
	DeletePeer(ctx context.Context, name, actor string) error
	AllowPeer(ctx context.Context, name string, ch PeerAllow, actor string) (*Peer, error)
	RenamePeer(ctx context.Context, name, to, actor string) (*Peer, error)
	SetPeerURL(ctx context.Context, name, url, actor string) (*Peer, error)
	SetPeerIntake(ctx context.Context, name string, in *PeerIntake, actor string) (*Peer, error)
	RemovePeerIntake(ctx context.Context, name, depot, actor string) (*Peer, error)
	SetPeerIdentity(ctx context.Context, name string, id PeerIdentity, actor string) (*Peer, error)
	RemovePeerIdentity(ctx context.Context, name, crew, source, actor string) (*Peer, error)
	PeerSend(ctx context.Context, name, kind string, body json.RawMessage, actor string) (*PeerOut, error)
	MailPeer(ctx context.Context, to, peer, text, actor string, files ...BlobFile) (*PeerOut, error)
	PeerOutbox(ctx context.Context, name string, limit int) ([]PeerOut, error)
	PeerDelivered(ctx context.Context, name string, seq uint64) error
	PeerSendFailed(ctx context.Context, name string, seq uint64, reason string, retryAt time.Time, final bool) error
	PeerRefused(ctx context.Context, name string, seq uint64, reason string) error
	PeerRefusedRate(ctx context.Context, name string, seq uint64, limit int, until time.Time) error
	PeerSendDisallowed(ctx context.Context, name string, seq uint64) (bool, error)
	PeerBeadSend(ctx context.Context, session string) (*BeadPeer, error)
	PeerRead(ctx context.Context, bead, actor string) (*PeerReading, error)
	PeerReadKept(ctx context.Context, bead, actor string) (*PeerReading, error)
	PeerNote(ctx context.Context, bead, text, actor string) (*PeerNoted, error)
	SettlePeerBeads(ctx context.Context) error
	PeerFetches(ctx context.Context) ([]PeerFetch, error)
	PeerFetched(ctx context.Context, bead, message, notTaken string) error
	PeerFetchFailed(ctx context.Context, bead, message, reason string, retryAt time.Time, final bool) error
	PeerBases(ctx context.Context) ([]PeerBase, error)
	PeerPulled(ctx context.Context, name string, b PeerBatch) (*PeerPulled, error)
	PeerPostAnswered(ctx context.Context, name string, answer PeerAnswer) error
	CheckPeer(ctx context.Context, p Peer) error
	PairPeer(ctx context.Context, p Peer, actor string) (*Peer, error)
	PairRefused(ctx context.Context, reason, peer, actor string) error
	PairOpen(ctx context.Context, o PairOffer, actor string) (*PairingCode, error)
	GetPairing(ctx context.Context, id string) (*Pairing, error)
	PairAnswer(ctx context.Context, id string, yes bool, actor string) (*Pairing, error)
	PairName(ctx context.Context, id, name, actor string) (*Pairing, error)
	PairClose(ctx context.Context, id, actor string) error

	ProposePreview(ctx context.Context, np NewPreview, actor string) (*Preview, error)
	Previews(ctx context.Context) ([]Preview, error)
	GetPreview(ctx context.Context, app string) (*Preview, error)
	ApprovePreview(ctx context.Context, app, actor string) (*Preview, error)
	RefusePreview(ctx context.Context, app, actor string) (*Preview, error)
	RestartPreview(ctx context.Context, app, actor string) (*Preview, error)
	PreviewHead(ctx context.Context, app string) (string, error)
	PreviewStop(ctx context.Context, app, actor string) (*Preview, error)
	ForgetPreview(ctx context.Context, app, actor string) (*Preview, error)

	RecordPack(ctx context.Context, rec PackImport, changed bool, actor string) (bool, error)
	Packs(ctx context.Context) ([]PackImport, error)

	May(ctx context.Context, actor string, act Action, obj Object) error

	// Streams: closed when ctx ends or the connection to the owner drops.
	Subscribe(ctx context.Context, after uint64) (<-chan Event, error)
	TailEvents(ctx context.Context) (<-chan Event, error)
	Wake(ctx context.Context) (<-chan struct{}, error)

	Close(ctx context.Context) error
}

var _ API = (*Store)(nil)
