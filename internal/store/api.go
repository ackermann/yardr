package store

import (
	"context"
	"time"
)

// API is everything the rest of yard uses. *Store implements it in the
// owning process; rpc.Client implements it for every other process.
type API interface {
	CreateBead(ctx context.Context, nb NewBead, actor string) (*Bead, error)
	GetBead(ctx context.Context, id string) (*Bead, error)
	ListBeads(ctx context.Context, f BeadFilter) ([]Bead, error)
	Ready(ctx context.Context) ([]Bead, error)
	Dispatchable(ctx context.Context) ([]Bead, error)
	UpdateBead(ctx context.Context, id string, p BeadPatch, exp Expect, actor string) (*Bead, error)
	Advance(ctx context.Context, id, stage, actor string) (*Bead, error)
	AdvanceNote(ctx context.Context, id, stage, note string, exp Expect, actor string) (*Bead, error)
	Done(ctx context.Context, id, outcome, note string, exp Expect, actor string) (*Bead, error)
	CloseBead(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error)
	Reopen(ctx context.Context, id, actor string) (*Bead, error)
	SetHold(ctx context.Context, id string, hold bool, exp Expect, actor string) (*Bead, error)
	Release(ctx context.Context, id, reason, actor string) (*Bead, error)

	AddDep(ctx context.Context, from, to, kind, actor string) error
	RemoveDep(ctx context.Context, from, to, kind, actor string) error
	Deps(ctx context.Context, id string) ([]Dep, error)
	OpenBlockers(ctx context.Context, id string) ([]string, error)
	Family(ctx context.Context, id string) (*Family, error)
	AddNote(ctx context.Context, id, text, actor string) error
	Notes(ctx context.Context, id string) ([]Note, error)

	PutRig(ctx context.Context, r Rig, actor string) error
	Rigs(ctx context.Context) ([]Rig, error)
	GetRig(ctx context.Context, name string) (*Rig, error)

	PutGroup(ctx context.Context, g Group) error
	Groups(ctx context.Context) ([]Group, error)
	GetGroup(ctx context.Context, name string) (*Group, error)
	DeleteGroup(ctx context.Context, name, actor string) (int, error)
	AddRoute(ctx context.Context, r Route) (*Route, error)
	Routes(ctx context.Context) ([]Route, error)
	DeleteRoute(ctx context.Context, id string) error

	PutFlow(ctx context.Context, f Flow, actor string) (*Flow, error)
	Flows(ctx context.Context) ([]Flow, error)
	GetFlow(ctx context.Context, name string) (*Flow, error)
	DeleteFlow(ctx context.Context, name, actor string) error
	SetFlowPointer(ctx context.Context, rig, typ, flow, actor string) error
	FlowPointers(ctx context.Context) ([]FlowPointer, error)
	RepoFlows(ctx context.Context, rig string) (*RepoFlows, error)
	RigFlows(ctx context.Context, rig string) ([]RigFlow, error)
	NextStages(ctx context.Context, id string) (*NextStages, error)

	Claim(ctx context.Context, beadID, group, stage, actor string) (*Session, error)
	MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error
	EndSession(ctx context.Context, sessionID, state, reason string) error
	MarkStopped(ctx context.Context, sessionID string) error
	SetHandle(ctx context.Context, sessionID string, handle map[string]any) error
	MarkCleaned(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, id string) (*Session, error)
	Sessions(ctx context.Context, all bool) ([]Session, error)
	Unstopped(ctx context.Context) ([]Session, error)
	ClosedWithWorkspace(ctx context.Context) ([]Session, error)
	FailureCount(ctx context.Context, bead, stage string) (int, error)

	Events(ctx context.Context, bead string, limit int) ([]Event, error)
	EventsSince(ctx context.Context, after uint64, limit int) ([]Event, error)
	Head(ctx context.Context) (uint64, error)
	LogDispatch(ctx context.Context, bead, kind, actor string, data map[string]any) error
	Snapshot(ctx context.Context) (*Snapshot, error)
	Prune(ctx context.Context, keep Retention, actor string) (*PruneResult, error)
	Backup(ctx context.Context, path string) (*BackupResult, error)

	PutResident(ctx context.Context, def Resident, actor string) (*Resident, error)
	Residents(ctx context.Context) ([]Resident, error)
	GetResident(ctx context.Context, name string) (*Resident, error)
	DeleteResident(ctx context.Context, name, rev, actor string) error
	SetResidentEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error
	ResidentStarted(ctx context.Context, name string, handle map[string]any) error
	ResidentStopped(ctx context.Context, name, reason string) error
	SetResidentHandle(ctx context.Context, name string, handle map[string]any) error
	ResidentStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error
	RequestRestart(ctx context.Context, name, reason, rev, actor string) error
	MarkRotationAsked(ctx context.Context, name string) error
	MarkNotified(ctx context.Context, name string, seq uint64) error
	Journal(ctx context.Context, name, kind, text, actor string) error
	Mail(ctx context.Context, to, text, actor string) error
	Inbox(ctx context.Context, name string, limit int) (*InboxPage, error)
	AckInbox(ctx context.Context, name string, through uint64) error
	ResidentPrime(ctx context.Context, name string) (*Prime, error)
	Remember(ctx context.Context, m NewMemory, actor string) (*Bead, error)
	Recall(ctx context.Context, scope, query string) ([]MemoryInfo, error)
	ExpireMemories(ctx context.Context) (int, error)
	SetIdentity(ctx context.Context, name, source string, id Identity, actor string) error
	AddSubscription(ctx context.Context, name string, sub Subscription, actor string) error
	RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error)
	Unwatch(ctx context.Context, name, bead, actor string) (int, error)
	FollowThread(ctx context.Context, name, source, thread, actor string) error
	UnfollowThread(ctx context.Context, name, source, thread, actor string) error
	Threads(ctx context.Context, name string) ([]Participation, error)

	PutHook(ctx context.Context, h Hook, actor string) (*Hook, error)
	Hooks(ctx context.Context) ([]Hook, error)
	GetHook(ctx context.Context, name string) (*Hook, error)
	DeleteHook(ctx context.Context, name, actor string) error
	HookPayload(ctx context.Context, seq uint64) ([]byte, error)

	// Streams: closed when ctx ends or the connection to the owner drops.
	Subscribe(ctx context.Context, after uint64) (<-chan Event, error)
	TailEvents(ctx context.Context) (<-chan Event, error)
	Wake(ctx context.Context) (<-chan struct{}, error)

	Close(ctx context.Context) error
}

var _ API = (*Store)(nil)
