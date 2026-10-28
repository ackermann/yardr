package rpc

import (
	"context"
	"fmt"

	"yardr.dev/yardr/internal/store"
)

// Class is who may make a call on the socket, as Access gives it. It is a
// line the socket draws before a call reaches the store, from the store's
// own records of who the caller is (store.CallerOf), so a method whose own
// check was forgotten is still not open to a bead's agent. The store's
// checks stay where they are: a call let through here is checked there as
// before, so nothing is let in that was refused.
type Class int

const (
	// Unclassed is no class: a method with none is refused to every
	// caller but the yard's own process, as an agent is refused what is
	// not marked for it. The test of this file holds every served method
	// to a class.
	Unclassed Class = iota
	// Read is anyone's: it changes nothing.
	Read
	// Agent is what a bead's agent may do from its session's pane, the
	// work itself: note, ask, mail, create and update beads, move its own
	// with an outcome, hold or close it, add an edge, call a source its
	// grants allow. What it adds it may not take away. The store refuses it
	// lifting a hold or removing a blocks edge, closing a bead not its own,
	// changing a bead's type or depot, and choosing a new bead's flow or a
	// stage past its first.
	Agent
	// Admin is what defines how the yard works (groups, routes, flows,
	// crew, depots, packs): the operator's and the crew's, the yardmaster
	// among them, and no bead's agent's.
	Admin
	// Operator is the operator's alone (the store's operatorOnly): no
	// agent, of a bead or of the crew.
	Operator
	// Daemon is the daemon's alone (the store's daemonOnly): the writes
	// the pane checks read back, and the session lifecycle.
	Daemon
	// DaemonOrOperator is the daemon's or the operator's: no agent's.
	DaemonOrOperator
)

func (c Class) String() string {
	switch c {
	case Read:
		return "read"
	case Agent:
		return "agent"
	case Admin:
		return "admin"
	case Operator:
		return "operator"
	case Daemon:
		return "daemon"
	case DaemonOrOperator:
		return "daemon or operator"
	}
	return "unclassed"
}

// allows reports whether a caller of kind may make a call of class c.
// The yard's own process and a person outside every pane (or an exec
// session's command, the operator's own) are let through to the store's
// checks, but for the daemon's calls; a bead's agent's pane only to what
// is read or marked for agents, and a crew member's to all but the
// operator's and the daemon's.
func (c Class) allows(kind store.CallerKind) bool {
	switch kind {
	case store.CallerUnknown:
		return c == Read
	case store.CallerOwner:
		return true
	case store.CallerDaemon:
		return c != Unclassed
	case store.CallerScript:
		// The operator's command, as the group: served as a person's
		// terminal is (store.CallerScript).
		kind = store.CallerPerson
	}
	switch c {
	case Read, Agent:
		return true
	case Admin:
		return kind != store.CallerBead
	case Operator, DaemonOrOperator:
		return kind == store.CallerPerson
	}
	return false
}

// allowed refuses the call name when the caller ctx carries may not make
// it (Access). The listener's devices have their own list (UserServer) and
// carry no caller, so this is the socket's line.
func allowed(ctx context.Context, st *store.Store, name string) error {
	c := Access[name]
	kind, err := st.CallerOf(ctx)
	if err != nil {
		return err
	}
	if c.allows(kind) {
		return nil
	}
	if kind == store.CallerUnknown {
		return fmt.Errorf("%s is refused because the socket peer could not be identified", name)
	}
	// In the store's words where it has its own check of the kind.
	switch c {
	case Daemon:
		return fmt.Errorf("%s is the daemon's alone", name)
	case Admin:
		return fmt.Errorf("%s is the operator's or the crew's, not for a bead's agent; this is %s", name, kind)
	case Operator, DaemonOrOperator:
		return fmt.Errorf("%s is the operator's alone; this is %s", name, kind)
	}
	return fmt.Errorf("%s has no class on the socket (rpc.Access), so no agent may make it; this is %s", name, kind)
}

// Access is the class of every method the socket serves (newAPI): who may
// make it. A method added to store.API is refused to every pane until it
// is given one here, and the test of this file fails until then.
var Access = map[string]Class{
	// Beads.
	"CreateBead":   Agent, // but a pin of its flow or a stage past its first (store.CreateBead)
	"GetBead":      Read,
	"ListBeads":    Read,
	"Ready":        Read,
	"Waiting":      Read,
	"Dispatchable": Read,
	"SidingBeads":  Read,
	"UpdateBead":   Agent, // but a pin of its flow, its type or depot (store.UpdateBead)
	// A move is held by the store to the edges its flow allows, and to the
	// members of the stage's group: it is no wider than an outcome.
	"Advance":     Agent,
	"AdvanceNote": Agent, // but along done-safe's edge on a security path (store.AdvanceNote)
	"Done":        Agent,
	"CloseBead":   Agent, // its own bead alone (store.CloseBead)
	"Reopen":      Admin,
	"SetHold":     Agent, // but not to lift a hold (store.SetHold)
	"Release":     Agent,
	"FoldBeads":   Admin, // the shaper's, the yardmaster's and a person's; no bead's agent folds (store.FoldBeads also asks checkCloseOwn of every member)
	"Unfold":      Agent, // the members of its own carrier alone (store.Unfold)

	"AddDep":            Agent,
	"RemoveDep":         Agent, // but not a blocks edge (store.RemoveDep)
	"Deps":              Read,
	"AllDeps":           Read,
	"OpenBlockers":      Read,
	"HoldingDeps":       Read,
	"BlockerCycles":     Read,
	"CheckBlockers":     Read,
	"CheckFor":          Read,
	"Family":            Read,
	"Discovery":         Read,
	"AddLink":           Agent, // but not a change to the tracks link of a pr bead past triage (store.AddLink)
	"RemoveLink":        Agent, // but not the tracks link of a pr bead past triage (store.RemoveLink)
	"MoveLink":          Admin,
	"MoveLinkSource":    Operator,
	"MoveSourceRecords": Operator,
	"Links":             Read,
	"IssueLinks":        Read,
	"AddNote":           Agent,
	"AddNoted":          Agent,
	"AddNotedAs":        Agent,
	"AckNote":           Agent,
	"MarkNoteTold":      Admin,
	"MarkNoteUntold":    Admin,
	"Notes":             Read,
	"NoteBlob":          Read,
	"MailBlob":          Read,
	"DeliveryBlob":      Read,

	// Asks. An agent asks and takes its ask back; answering is a person's.
	"AddAsk":            Agent,
	"AskNew":            Agent,
	"AddAnswer":         Admin,
	"AddAnswerExpected": Admin,
	"UserAsks":          Read,
	"WriterAsks":        Read,
	"Bead":              Read,
	"Estimate":          Read,
	"EstimateMany":      Read,
	"Arrivals":          Read,
	"Flows":             Read,
	"AnswerAsk":         Admin,
	"DeferAsk":          Admin,
	"DeclineAsk":        Admin,
	"TakeBackAsk":       Agent,
	"GiveSecret":        Admin,
	"LoginWait":         Admin,
	"LoginWaitReady":    Read,
	"ApproveAsk":        Admin,
	"ApproveSetup":      Admin,

	// Depots, groups, routes, flows: how the yard routes its work.
	"PutDepot":       Admin,
	"Depots":         Read,
	"GetDepot":       Read,
	"DeleteDepot":    Operator,
	"PutGroup":       Admin,
	"SetGroup":       Admin,
	"Groups":         Read,
	"GetGroup":       Read,
	"DeleteGroup":    Admin,
	"AddRoute":       Admin,
	"SetRoute":       Admin,
	"Routes":         Read,
	"DeleteRoute":    Admin,
	"PutFlow":        Admin,
	"ListFlows":      Read,
	"GetFlow":        Read,
	"DeleteFlow":     Admin,
	"SetFlowPointer": Admin,
	"FlowPointers":   Read,
	"RepoFlows":      Read,
	"DepotFlows":     Read,
	"NextStages":     Read,
	"FlowMap":        Read,
	"Stranded":       Read,

	// Sessions: the daemon records them, and the pane checks read them.
	"Claim":               Daemon,
	"MarkRunning":         Daemon,
	"EndSession":          Daemon,
	"MarkStopped":         Daemon,
	"MarkCountDue":        Daemon,
	"SetHandle":           Daemon,
	"RecordUsage":         Daemon,
	"MarkCleaned":         Daemon,
	"SnoozeSession":       Agent, // its own pane's session alone, and the two snooze keys alone (store.SnoozeSession)
	"GetSession":          Read,
	"Sessions":            Read,
	"ListSessions":        Read,
	"Unstopped":           Read,
	"ClosedWithWorkspace": Read,
	"Uncounted":           Read,
	"FailureCount":        Read,
	"SessionScreen":       Operator, // a screen can show what an agent printed, a secret among it

	// The log and the store.
	"Events":        Read,
	"EventsOfKind":  Read,
	"EventsSince":   Read,
	"Head":          Read,
	"LogDispatch":   Admin,
	"Snapshot":      Read,
	"Prune":         DaemonOrOperator,
	"Backup":        DaemonOrOperator,
	"Settings":      Read,
	"SetSetting":    Admin,
	"TelemetryShow": Operator, // the install id and next request are the operator's alone
	"TelemetrySet":  Operator, // the store's ActChangeSetting gate refuses anyone but the operator

	"RecordGOCACHEOld": Admin,
	"GOCACHEOldDirs":   Read,
	"RemoveGOCACHEOld": Admin,

	// Crew. What the daemon records of a crew member's session is its own,
	// as a bead session's is; a stop by yardr down or yardr crew stop is
	// a person's too.
	"PutCrew":              Admin,
	"Crew":                 Read,
	"GetCrew":              Read,
	"DeleteCrew":           Admin,
	"SetCrewEnabled":       Admin,
	"CrewStarting":         Admin, // a handle the daemon's alone (store), a clear anyone's
	"CrewStarted":          Daemon,
	"CrewSettled":          Daemon,
	"CrewStopped":          Admin,
	"CrewGone":             Daemon,
	"CrewLeft":             Daemon,
	"CrewStatus":           Daemon,
	"SetCrewHandle":        Daemon,
	"CrewStartFailed":      Daemon,
	"RequestRestart":       Admin,
	"MarkRotationAsked":    Daemon,
	"MarkContextAsked":     Daemon,
	"SetContext":           Daemon,
	"MarkNotified":         Daemon,
	"CrewStartingFor":      Admin, // as CrewStarting
	"CrewStartedFor":       Daemon,
	"CrewSettledFor":       Daemon,
	"CrewStoppedFor":       Admin,
	"CrewGoneFor":          Daemon,
	"CrewLeftFor":          Daemon,
	"CrewStatusFor":        Daemon,
	"SetCrewHandleFor":     Daemon,
	"CrewStartFailedFor":   Daemon,
	"RequestRestartFor":    Admin,
	"MarkRotationAskedFor": Daemon,
	"MarkContextAskedFor":  Daemon,
	"SetContextFor":        Daemon,
	"MarkNotifiedFor":      Daemon,
	"RecordCrewUsage":      Daemon,
	"Journal":              Admin,
	"CrewPrime":            Read,
	"CrewPrimeFor":         Read,
	"Remember":             Admin,
	"Recall":               Read,
	"RecallJournal":        Read,
	"ExpireMemories":       Admin,

	// Mail, users and their devices.
	"Mail":             Agent,
	"React":            Agent,
	"PutBlob":          Agent,
	"Inbox":            Read,
	"AckInbox":         Admin,
	"InboxFor":         Read,
	"AckInboxFor":      Admin,
	"AddUser":          Operator,
	"Users":            Read,
	"GetUser":          Read,
	"SetUser":          Admin,
	"DeleteUser":       Operator,
	"Teammates":        Operator, // the mapped addresses are the operator's, not for an agent or crew pane
	"UninviteTeammate": Agent,    // files an ask, as AskNew; the store refuses a bead's agent a run ask
	"AddDevice":        Admin,
	"Devices":          Read,
	"RemoveDevice":     Admin,
	"DevicePairOpen":   Admin,
	"DevicePairClose":  Operator,
	// One's own pairing from the web view: the socket lets a person's own
	// process (the view among them) call it, and no agent's pane; the user
	// it names is the web session's, which the view alone supplies. It is
	// in neither Calls nor OperatorCalls, so no device is served it.
	"DevicePairOpenSelf":   Operator,
	"DevicePairCloseSelf":  Operator,
	"DevicePairCodeSelf":   Operator,
	"UserInbox":            Read,
	"AckUserInbox":         Admin,
	"UserNotifications":    Read,
	"AckUserNotifications": Admin,
	"Conversation":         Read,
	"AddTimer":             Admin,
	"Timers":               Read,
	"RemoveTimer":          Admin,
	"FireTimers":           Admin,
	"FoldSidings":          Admin,

	// Previews: a crew member proposes and may stop its own; approval,
	// refusal, restart and forgetting are the yard's person's alone.
	"ProposePreview": Agent,
	"Previews":       Read,
	"GetPreview":     Read,
	"ApprovePreview": Operator,
	"RefusePreview":  Operator,
	"RestartPreview": Operator,
	"PreviewHead":    Read,
	"PreviewStop":    Agent,
	"ForgetPreview":  Operator,

	// Who someone is in a source, what they follow, and speaking there as
	// a crew member.
	"SetIdentity":             Admin,
	"AddSubscription":         Admin,
	"AddSubscriptionKind":     Admin,
	"RemoveSubscriptions":     Admin,
	"RemoveSubscriptionsKind": Admin,
	"RemoveSubscription":      Admin,
	"RemoveSubscriptionKind":  Admin,
	"Unwatch":                 Admin,
	"FollowThread":            Admin,
	"UnfollowThread":          Admin,
	"Threads":                 Read,
	"Replying":                Admin,
	"Replied":                 Admin,
	"Opening":                 Admin,
	"Opened":                  Admin,
	"AllowOpening":            Admin,
	"SettingState":            Admin,
	"StateSet":                Admin,

	// Hooks and sources. A source call is checked against its grants.
	"PutHook":           Admin,
	"Hooks":             Read,
	"GetHook":           Read,
	"DeleteHook":        Operator,
	"HookPayload":       Read,
	"DeliveryFiles":     Read,
	"EventsOfHook":      Read,
	"PutSource":         Admin,
	"Sources":           Read,
	"GetSource":         Read,
	"SetSource":         Admin,
	"KeySource":         Admin,
	"KeyUser":           Admin,
	"KeyUserCredential": Admin,
	"DeleteSource":      Admin,
	"UnignoreSource":    Admin,
	"GrantSource":       Admin,
	"RevokeSource":      Admin,
	"SourceGrants":      Read,
	"SourceCalls":       Read,
	"SessionCalls":      Read,
	"CallSource":        Agent,
	"Pushes":            Read,
	"DropPushes":        Admin,
	"PushTried":         Admin,
	"IssueMoves":        Read,
	"IssueMoveAllowed":  Read,
	"IssueMoveFailed":   Admin,
	"IssueMoved":        Admin,
	"IssueMoveSkipped":  Admin,

	// Linked yards. A bead at a peer is read and noted by its agent.
	"AddPeer":            Operator,
	"Peers":              Read,
	"GetPeer":            Read,
	"DeletePeer":         Operator,
	"AllowPeer":          Admin,
	"RenamePeer":         Operator,
	"SetPeerURL":         Operator,
	"SetPeerIntake":      Admin,
	"RemovePeerIntake":   Admin,
	"SetPeerIdentity":    Operator,
	"RemovePeerIdentity": Operator,
	"PeerSend":           Admin,
	"MailPeer":           Agent,
	"PeerOutbox":         Read,
	"PeerDelivered":      Admin,
	"PeerSendFailed":     Admin,
	"PeerRefused":        Admin,
	"PeerRefusedRate":    Admin,
	"PeerSendDisallowed": Read,
	"PeerBeadSend":       Admin,
	"PeerRead":           Agent,
	"PeerReadKept":       Agent,
	"PeerNote":           Agent,
	"SettlePeerBeads":    Admin,
	"PeerFetches":        Read,
	"PeerFetched":        Admin,
	"PeerFetchFailed":    Admin,
	"PeerBases":          Read,
	"PeerPulled":         Admin,
	"PeerPostAnswered":   Admin,
	"CheckPeer":          Read,
	"PairPeer":           Operator,
	"PairRefused":        Admin,
	"PairOpen":           Operator,
	"GetPairing":         Read,
	"PairAnswer":         Operator,
	"PairName":           Operator,
	"PairClose":          Operator,

	// Packs.
	"RecordPack": Admin,
	"Packs":      Read,

	// Policy. A read: it changes nothing, and whoever asks is told only
	// what the policy tells the actor it names.
	"May": Read,
}
