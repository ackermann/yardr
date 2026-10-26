package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// A Policy says who may do what in the yard. Every permission gate of the
// store asks it, through may, and nothing else: a gate names the action it
// guards and the object it is done to, and never compares the actor with
// a role or the operator setting itself. A new gate is a new Action in the
// list below with its rule in coreRules, asked through may like the rest;
// that is what lets another Policy (SetPolicy) decide all of them.
//
// May answers nil when actor may do act to obj, and else the refusal the
// caller is shown. It runs inside the transaction the gate guards (tx,
// read-only or not), so what it reads is what the write sees. A policy
// that decides only some actions hands the others to CorePolicy.
type Policy interface {
	May(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error
}

// An Action is what a gate asks leave for, one of the fixed list below.
// Its value is the words a refusal names it in ("add a peer"), unless the
// object says more (Object.Does).
type Action string

// The actions the store's gates ask leave for, by the rule the core
// policy holds each to (coreRules).
const (
	// The operator's alone (operatorOnly).
	ActChangeOriginator   Action = "change a bead's originator"
	ActGrantCrew          Action = "grant a crew member a call" // Object.Does names the grant
	ActChangeCrew         Action = "change a crew member"       // Object.Does names the key
	ActAddDevice          Action = "add a device"
	ActEndDevicePairing   Action = "end a device's pairing"
	ActChangeDepotPath    Action = "change a depot's path"
	ActChangeDepotTest    Action = "change a depot's test command"
	ActChangeDepotBase    Action = "change a depot's base"
	ActChangeDepotKind    Action = "change a depot's kind"
	ActRemoveDepot        Action = "remove a depot"
	ActMoveLinks          Action = "move links from one source to another"
	ActAddHook            Action = "add a hook"
	ActReplaceHook        Action = "replace a hook"
	ActChangeHookSource   Action = "change a hook's source or secret file"
	ActChangeHookUnknown  Action = "change a hook's unmapped sender policy"
	ActRemoveHook         Action = "remove a hook"
	ActPair               Action = pairs
	ActMapPeerIdentity    Action = "say who a peer's crew member is in a source"
	ActUnmapPeerIdentity  Action = "take back who a peer's crew member is in a source"
	ActAddPeer            Action = "add a peer"
	ActRemovePeer         Action = "remove a peer"
	ActExposeToPeer       Action = "expose someone of the yard to a peer"
	ActTrustPeerName      Action = "trust a name of a peer as a user of the yard"
	ActSendAsksToPeer     Action = "send a user's asks to a name of a peer"
	ActAllowPeerMessages  Action = "allow a peer's messages"
	ActRaisePeerMail      Action = "take more mail from a peer"
	ActChangePeerAddress  Action = "change where a peer is called"
	ActRenamePeer         Action = "rename a peer"
	ActRoutePeerBeads     Action = "say where a peer's beads go"
	ActMoveSource         Action = "move a source's records"
	ActApplyPack          Action = "apply a pack"
	ActSetUpPackSources   Action = "set up a pack's sources"
	ActBringUp            Action = "bring the yard up"
	ActAddUser            Action = "add a user"
	ActRemoveUser         Action = "remove a user"
	ActChangeRole         Action = "change a user's role"
	ActMapIdentity        Action = "say who a crew member is in a source"
	ActChangeSetting      Action = "change a setting"
	ActPruneStore         Action = "prune the store"
	ActGiveHarnessKey     Action = "give a harness API key"
	ActGiveCrewCredential Action = "give a crew member's credential"
	ActGiveHookSecret     Action = "give a hook's signing secret"
	ActAllowThreads       Action = "let a crew member open threads" // Object.Does names whom and where
	ActRemoveTimer        Action = "remove a timer"                 // Object.Does names the timer
	ActRemoveDevice       Action = "remove a device"                // the user's own, or the operator's
	ActTakeBackAsk        Action = "take an ask back"               // its asker's, or the operator's
	ActReadInbox          Action = "read an inbox"                  // its user's, or the operator's; Object.Does says read or acknowledge
	ActSpeakAs            Action = "speak as a crew member"         // its own session's, or the operator's
	ActAnswerAsk          Action = "answer an ask"                  // its addressee's, or an operator's
	ActAnswerIdentityAsk  Action = "answer a mapping ask"           // the operator the card asks
	ActAnswerPeerAsk      Action = "answer another yard's ask"      // the person it is for
	ActAnswerStageAsk     Action = "answer a stage ask"             // a stage member, a trusted member, or its fallback addressee
	ActCallSourceAny      Action = "make any call of a source"      // an operator's, or anyone's in a yard with none

	// The operator's or the yardmaster's (operatorOrYardmaster).
	ActRemoveCrew  Action = "remove a crew member"
	ActEnableCrew  Action = "enable a crew member"
	ActRestartCrew Action = "restart a crew member"
	ActRemoveFlow  Action = "remove a flow"
	ActRemoveGroup Action = "remove a group"
	ActRemoveRoute Action = "remove a route"

	// The operator's, and never an agent's pane (maySource): what decides
	// where a credential of the yard is sent.
	ActAddSource         Action = "adding a source"
	ActSetSourceKey      Action = "setting a source's credential"
	ActRemoveSource      Action = "removing a source"
	ActIgnoreSender      Action = "ignoring a sender in a source"
	ActUnignoreSender    Action = "un-ignoring a sender in a source"
	ActGrantSource       Action = "granting a call of a source"
	ActAnswerSourceLogin Action = "answering the login"
	ActGrantSourceAs     Action = "granting a call of a source as a user" // the user's alone; Object.Does is the refusal's reason

	// Approvals of a run ask, by the store's records of who calls.
	ActApproveRun  Action = "approve a command"            // any operator's
	ActApproveOwn  Action = "approve a user's own command" // the one asked, the user or an operator
	ActApproveSole Action = "approve a command one user alone gives"

	// The user's own, or the operator's for them, never an agent's pane.
	ActTell           Action = "set where a user is told"
	ActAnswerLogin    Action = "give the code that signs a user in"
	ActIdentify       Action = "say who a user is in a source"
	ActSubscribe      Action = "change what a user is told"
	ActSubscribeCrew  Action = "change what a crew member is told"
	ActSubscribeGroup Action = "change what a group tells its members"

	// Of other yards: a crew member allowed the peer, a user, or the operator.
	ActAskPeer    Action = "ask another yard"
	ActMailPeer   Action = "mail another yard"
	ActReadAtPeer Action = "read a bead that is at"
	ActNoteAtPeer Action = "write on a bead of"

	// By the store's records of the calling pane alone.
	ActMoveOut   Action = "move a bead out of its stage" // a member of the stage's group
	ActWaitLogin Action = "wait on a login's input"      // the yardmaster's pane or no agent's

	// The CLI-only gates, asked through May: what the operator did in the
	// CLI before any write of the store's could refuse it (an edge
	// connection, upgrade --from, timer list --all, uninstall --purge,
	// source check --user).
	ActConnectEdge      Action = "connect this yard to an edge"
	ActDisconnectEdge   Action = "disconnect this yard from the edge"
	ActNameUpgradeDepot Action = "name the depot to install from (--from)"
	ActListAllTimers    Action = "list everyone's timers"
	ActDeleteYardHome   Action = "delete the yard's home"
	// The user's own, or the operator's for them, never an agent's
	// pane, like the tell family's above: one rule below says so.
	ActSourceCheckTell Action = "send a test message through a source"

	// Seeing a bead: asked per bead by the reads (Bead, ListBeads,
	// Arrivals, the listener's event stream), for the viewer they name.
	// The core lets everyone see every bead; a later policy holds back.
	ActSee Action = "see a bead"
)

// ObjectKind says what kind of thing an action is done to.
type ObjectKind string

const (
	KindYard    ObjectKind = "yard"
	KindBead    ObjectKind = "bead"
	KindDepot   ObjectKind = "depot"
	KindUser    ObjectKind = "user"
	KindSource  ObjectKind = "source"
	KindCrew    ObjectKind = "crew"
	KindGroup   ObjectKind = "group"
	KindFlow    ObjectKind = "flow"
	KindRoute   ObjectKind = "route"
	KindHook    ObjectKind = "hook"
	KindPeer    ObjectKind = "peer"
	KindDevice  ObjectKind = "device"
	KindHarness ObjectKind = "harness"
	KindLogin   ObjectKind = "login"
	KindAsk     ObjectKind = "ask"
	KindTimer   ObjectKind = "timer"
	KindSetting ObjectKind = "setting"
	KindPack    ObjectKind = "pack"
)

// An Object is what an action is done to, with what today's rules read
// of it beside the name.
type Object struct {
	Kind ObjectKind
	// Name is the object's id or name, as fits its Kind.
	Name string
	// Does is the words a refusal names the deed in, where they say more
	// than the action's: "change model on crew member ada".
	Does string
	// Ask marks whether a run ask may propose the action, beside the
	// refusal it marks (OperatorOnlyError).
	Ask AskClass
	// To is whom an ask was put to, the user an ask of another yard is for,
	// or the destination of a source transfer. Whose is the user whose own
	// a command is (ActApproveOwn), the one who alone gives it
	// (ActApproveSole), the user a grant is as (ActGrantSourceAs), or who
	// asked an ask (ActTakeBackAsk).
	To, Whose string
	// Seq is an ask's number on its bead.
	Seq uint64
	// Group is the group whose stage a bead leaves (ActMoveOut), or whose
	// subscriptions change (ActSubscribeGroup).
	Group *Group
	// Fallback marks a stage ask put to its operator as fallback.
	Fallback bool

	// The move the core's ActMoveOut reads the calling session from.
	exp Expect
	row *beadRow
}

// does is the words a refusal names the deed in.
func (o Object) does(act Action) string { return or(o.Does, string(act)) }

// CorePolicy is today's rules: an operator (a user whose role is
// RoleOperator) does what defines the yard, a user what is their own, and
// an agent's pane none of either, each as coreRules says.
var CorePolicy Policy = corePolicy{}

type corePolicy struct{}

func (corePolicy) May(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	r, ok := coreRules[act]
	if !ok {
		return fmt.Errorf("%s: no rule of the yard says who may", act)
	}
	return r(ctx, tx, actor, act, obj)
}

// SetPolicy has every gate of the store ask p instead of CorePolicy; nil
// puts CorePolicy back. The process that opens the store sets it before
// the store serves anything.
func (s *Store) SetPolicy(p Policy) { s.policy = p }

// may asks the store's policy whether actor may do act to obj, inside tx.
// A store with none set, and the hand-made wtx of a migration (no store),
// asks CorePolicy.
func (s *Store) may(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	p := CorePolicy
	if s != nil && s.policy != nil {
		p = s.policy
	}
	return p.May(ctx, tx, actor, act, obj)
}

func (w *wtx) may(ctx context.Context, actor string, act Action, obj Object) error {
	return w.store.may(ctx, w.Tx, actor, act, obj)
}

// mayRead is may for a change that is no write of the database (a
// pairing, which lives in the server's memory), in a read of its own.
func (s *Store) mayRead(ctx context.Context, actor string, act Action, obj Object) error {
	return s.read(func(tx *bolt.Tx) error { return s.may(ctx, tx, actor, act, obj) })
}

// May asks the store's policy whether actor may do act to obj, changing
// nothing. It is on the API so a command whose gate is no write of the
// store's — an edge connection, upgrade --from, timer list --all,
// uninstall --purge, source check --user — asks the same policy the
// writes ask, over the socket like the writes, instead of deciding in
// the CLI.
func (s *Store) May(ctx context.Context, actor string, act Action, obj Object) error {
	return s.mayRead(ctx, actor, act, obj)
}

// A rule is how the core decides one action.
type rule func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error

// coreRules is who may do what, by today's rules: the one place that says
// it. Each action has one rule.
var coreRules = map[Action]rule{}

func init() {
	for _, act := range []Action{
		ActChangeOriginator, ActGrantCrew, ActChangeCrew, ActEndDevicePairing,
		ActChangeDepotPath, ActChangeDepotTest, ActChangeDepotBase, ActChangeDepotKind, ActRemoveDepot,
		ActMoveLinks, ActAddHook, ActReplaceHook, ActChangeHookSource, ActChangeHookUnknown, ActRemoveHook,
		ActPair, ActMapPeerIdentity, ActUnmapPeerIdentity, ActAddPeer, ActRemovePeer, ActExposeToPeer,
		ActTrustPeerName, ActSendAsksToPeer, ActAllowPeerMessages, ActRaisePeerMail, ActChangePeerAddress,
		ActRenamePeer, ActRoutePeerBeads, ActMoveSource, ActApplyPack, ActSetUpPackSources,
		ActBringUp,
		ActAddUser, ActRemoveUser, ActChangeRole, ActMapIdentity, ActChangeSetting, ActPruneStore,
		ActGiveHarnessKey, ActGiveCrewCredential, ActGiveHookSecret, ActAllowThreads, ActRemoveTimer,
		ActConnectEdge, ActDisconnectEdge, ActNameUpgradeDepot, ActListAllTimers, ActDeleteYardHome,
	} {
		coreRules[act] = ruleOperator
	}
	for _, act := range []Action{ActEnableCrew, ActRestartCrew, ActRemoveFlow, ActRemoveGroup, ActRemoveRoute} {
		coreRules[act] = ruleOperatorOrYardmaster
	}
	for _, act := range []Action{ActAddSource, ActSetSourceKey, ActRemoveSource, ActIgnoreSender, ActUnignoreSender, ActGrantSource, ActAnswerSourceLogin} {
		coreRules[act] = maySource
	}
	for act, r := range map[Action]rule{
		ActAddDevice:         mayAddDevice,
		ActRemoveDevice:      ruleRemoveDevice,
		ActTakeBackAsk:       ruleTakeBackAsk,
		ActReadInbox:         ruleReadInbox,
		ActSpeakAs:           maySpeakAs,
		ActAnswerAsk:         ruleAnswerAsk,
		ActAnswerIdentityAsk: ruleAnswerIdentityAsk,
		ActAnswerPeerAsk:     ruleAnswerPeerAsk,
		ActAnswerStageAsk:    ruleAnswerStageAsk,
		ActRemoveCrew:        ruleRemoveCrew,
		ActCallSourceAny:     ruleCallSourceAny,
		ActGrantSourceAs:     ruleGrantSourceAs,
		ActApproveRun:        mayApprove,
		ActApproveOwn:        mayApproveOwn,
		ActApproveSole:       mayApproveSole,
		ActTell:              mayTell,
		ActAnswerLogin:       mayAnswerLogin,
		ActIdentify:          mayIdentify,
		ActSourceCheckTell:   maySourceCheckTell,
		ActSubscribe:         maySubscribe,
		ActSubscribeCrew:     maySubscribeCrew,
		ActSubscribeGroup:    maySubscribeGroup,
		ActAskPeer:           ruleAskPeer,
		ActMailPeer:          ruleMailPeer,
		ActReadAtPeer:        mayAtPeer,
		ActNoteAtPeer:        mayAtPeer,
		ActMoveOut:           checkMembers,
		ActWaitLogin:         mayWaitLogin,
		ActSee:               ruleSee,
	} {
		coreRules[act] = r
	}
}

// ruleSee is who may see a bead: everyone, the viewer named or none. It
// is the seam a later policy filters through; the core holds nothing
// back, so every read answers as before. A refusal hides the bead (the
// read omits it, or answers not-found), so no words are said here.
func ruleSee(_ context.Context, _ *bolt.Tx, _ string, _ Action, _ Object) error {
	return nil
}

func ruleRemoveCrew(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	if err := ruleOperatorOrYardmaster(ctx, tx, actor, act, obj); err != nil {
		return err
	}
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	var yardmaster string
	if _, err := get(tx, bSettings, YardmasterSetting, &yardmaster); err != nil {
		return err
	}
	if yardmaster == "" {
		yardmaster = "yardmaster"
	}
	if resolved == yardmaster && obj.Name == yardmaster {
		return fmt.Errorf("the yardmaster cannot remove itself; the operator can")
	}
	return nil
}

func ruleOperator(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	return operatorOnly(ctx, tx, actor, obj.does(act), obj.Ask)
}

func ruleOperatorOrYardmaster(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	return operatorOrYardmaster(ctx, tx, actor, obj.does(act), obj.Ask)
}

// notOperator says whether actor acts as anyone but an operator (a user
// whose role is RoleOperator): this is then how a refusal names them, and
// operator how it names the operators.
// It is the one place the rule is read, inside the write it guards, so
// every client of the store is held to it. From a session's or a crew
// member's pane the actor is the pane's (paneAuthor) before the rule is
// read, so a pane can never pass an admin write as the operator by
// --actor alone; from anywhere else it is the caller's word (YARDR_ACTOR,
// --actor): every agent runs as one OS user, so this is a line against
// mistakes, not a wall, and moves when yardr hosts more than one person.
// A yard that has no operator has nobody to hold it to, and takes the
// change from anyone who is no agent of its own, as maySource reads it:
// no crew member, and no call from the pane of a bead's session or of a
// crew member.
func notOperator(ctx context.Context, tx *bolt.Tx, actor string) (operator, this string, err error) {
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return "", "", err
	}
	actor = resolved
	ops, err := operatorsIn(tx)
	switch {
	case err != nil:
		return "", "", err
	case ops.is(actor):
		return "", "", nil
	case !ops.none():
		return ops.words(), or(actor, "nobody"), nil
	case tx.Bucket(bCrew).Get([]byte(actor)) != nil:
		return "the operator", "the crew member " + actor, nil
	}
	if sess, err := paneSession(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if sess != nil && !sess.Manual() {
		return "the operator", "the session " + sess.ID + " of a bead's agent", nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if r != nil {
		return "the operator", "the crew member " + r.Name, nil
	}
	return "", "", nil
}

// operatorOnly refuses what only the operator may do (does, as in "add a
// peer") when actor is anyone else (notOperator), in the same words
// whatever is asked and whoever asks. ask marks whether a run ask may
// propose it, beside the refusal it marks.
func operatorOnly(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("only %s may %s; this is %s", operator, does, this), ask: ask}
}

// operatorOrYardmaster passes the operator's call (notOperator) or the
// crew member's the yardmaster setting names, and refuses anyone else in
// words naming both. It covers undoing what the yardmaster may already
// add (crew, groups, flows, routes: adding each is anyone's, attributed)
// and restarting crew: removing a crew member, a group, a flow or a
// route, enabling a crew member, and asking one's session to restart.
// ask marks whether a run ask may propose it, as with operatorOnly.
func operatorOrYardmaster(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	operator, _, err := notOperator(ctx, tx, actor)
	if err != nil || operator == "" {
		return err
	}
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	var ym string
	if _, err := get(tx, bSettings, YardmasterSetting, &ym); err != nil {
		return err
	}
	if ym == "" {
		ym = "yardmaster"
	}
	// The setting names a crew member; a matching user name or stale
	// setting is not a delegation.
	if resolved == ym && tx.Bucket(bCrew).Get([]byte(ym)) != nil {
		return nil
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("only %s or the yardmaster (%s) may %s", operator, ym, does), ask: ask}
}

// maySource refuses a change to the declared sources by anyone but the
// operator. A source is where a credential of the yard is sent: whoever
// may replace its file can point the next call, and the credential with
// it, at an address of their own. And its file may name a command the yard
// runs (limits), as the user the daemon runs as. Who asks is told by the store's own
// records before the caller's word, as for a move out of a stage whose
// group has members (checkMembers): a call from the pane of a bead's session or of a crew
// member is that session's or that crew member's, whatever actor it
// names. A yard without an operator has nobody to hold it to, and takes
// the change from anyone who is no agent of its own. Object.Ask marks
// whether a run ask may propose it, beside the refusal it marks.
func maySource(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	why := obj.does(act) + " is " + ops.whose()
	why += ": a source is where a credential of the yard is sent, and its file may name a command the yard runs"
	refuse := func(format, a string) error {
		return &OperatorOnlyError{msg: fmt.Sprintf(format, why, a), ask: obj.Ask}
	}
	sess, err := paneSession(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() {
		return refuse("%s; this is the session %s of a bead's agent", sess.ID)
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if r != nil {
		return refuse("%s; this is the crew member %s", r.Name)
	}
	if err := startingRefused(tx, sessionOf(ctx)); err != nil {
		return err
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return &OperatorOnlyError{msg: fmt.Sprintf("%s; %s is a crew member", why, actor), ask: obj.Ask}
	}
	if !ops.none() && !ops.is(actor) {
		return refuse("%s; this is %s", or(actor, "nobody"))
	}
	return nil
}

// ruleGrantSourceAs is who gives a grant as the user Object.Whose: that
// user alone, with no operator leg, so that a person trusts what is
// called as them to their own word alone. Object.Does is the reason a
// refusal opens with.
func ruleGrantSourceAs(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	as := obj.Whose
	refuse := func(who string) error {
		return &OperatorOnlyError{msg: obj.Does + "; this is " + who, ask: Askable}
	}
	sess, err := paneSession(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() {
		return refuse("the session " + sess.ID + " of a bead's agent")
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if r != nil {
		return refuse("the crew member " + r.Name)
	}
	if err := startingRefused(tx, sessionOf(ctx)); err != nil {
		return err
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return refuse("the crew member " + actor)
	}
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	if resolved == as {
		return nil
	}
	who := or(resolved, "nobody")
	if ops, err := operatorsIn(tx); err != nil {
		return err
	} else if ops.is(resolved) {
		who = OperatorWords([]string{resolved})
	}
	return refuse(who)
}

// ruleCallSourceAny is who may make any call of a source, as any user, to
// try it: an operator, or anyone in a yard without one, which has nobody
// to hold a call to as it has nobody to hold a change to its sources to
// (maySource). Its refusal is an OperatorOnlyError, which the caller
// keeps as why a call is not granted; any other error fails the call.
func ruleCallSourceAny(_ context.Context, tx *bolt.Tx, actor string, _ Action, _ Object) error {
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if ops.none() || ops.is(actor) {
		return nil
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("this is %s, who is no session, crew member or operator (%s) of the yard: a call is granted to a group or a crew member (yardr source grant)", or(actor, "nobody"), ops)}
}

// mayApprove refuses an approval by anyone but an operator: any of them,
// whomever of them the ask was put to, so an approval does not wait on one
// person. It is the firmest check the store has, as maySource is: who asks
// is told by the store's own record of the calling process before the
// caller's word, so a call from the pane of a bead's session or of a crew
// member is refused whatever actor it names. A yard without an operator
// has nobody whose command it would be.
func mayApprove(ctx context.Context, tx *bolt.Tx, actor string, _ Action, _ Object) error {
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if ops.none() {
		return fmt.Errorf("approving a command is the operator's, and this yard has none: yardr settings set operator <name>")
	}
	why := "approving a command is " + ops.whose() + ": the yard runs it as them"
	pane, err := agentPane(ctx, tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if !ops.is(actor) {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// mayApproveOwn refuses an approval of a command that is the user
// Object.Whose's own to give, as it is the operator's for them, by anyone
// but the one the ask was put to (Object.To), who is one of the two: the
// card says the yard runs the command as the one asked, and so it does.
// An agent's pane is refused first, whatever actor the call names, as
// mayApprove refuses it. Whether the one asked may still make the change
// is the command's own check, which runs after this one.
func mayApproveOwn(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	to, whose := obj.To, obj.Whose
	why := "approving this command is " + to + "'s, whom it was asked of: the yard runs it as them"
	pane, err := agentPane(ctx, tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != to {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if to != whose && !ops.is(to) {
		return fmt.Errorf("this command is %s's own to approve, or an operator's for them, and %s is neither now", whose, to)
	}
	return nil
}

// mayApproveSole is mayApproveOwn for a command one user (Object.Whose)
// alone gives: the one asked must be that user, and the operator has no
// leg beside them, so an approval by the operator (or by anyone else) is
// refused whoever the ask was put to. An agent's pane is refused first,
// as in mayApproveOwn.
func mayApproveSole(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	to, sole := obj.To, obj.Whose
	why := "approving this command is " + sole + "'s alone, and nobody else's: the yard runs it as them"
	pane, err := agentPane(ctx, tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != to {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	if to != sole {
		return fmt.Errorf("this command is %s's own to approve alone, and %s is not them", sole, to)
	}
	return nil
}

// mayTell refuses a change of where the user Object.Name is told outside
// the yard by anyone but that user and the operator. What lands in a
// person's inbox goes where these settings point, so whoever sets them can
// send it out of the yard, to an address of their own. yardr checks
// nobody's name, so who asks is told by the store's own records before
// the caller's word, as for a declared source (maySource): a call from
// the pane of a bead's session or of a crew member is that session's or
// that crew member's, whatever actor it names.
func mayTell(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	return mayStandFor(ctx, tx, name, actor, func(ops operators) string {
		why := "where " + name + " is told outside the yard is set by " + name
		if !ops.none() && !ops.is(name) {
			why += ", or by " + ops.words()
		}
		return why + ": what lands in their inbox goes there"
	})
}

// mayAnswerLogin is mayTell for the input of a login that is the user
// Object.Name's own (AskSecret.As beside a Login): the code their sign-in
// page shows is theirs to read, or the operator's for them, and never the
// pane of an agent's or a crew member's.
func mayAnswerLogin(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	return mayStandFor(ctx, tx, name, actor, func(ops operators) string {
		why := "the code that signs " + name + " in is " + name + "'s to give"
		if !ops.none() && !ops.is(name) {
			why += ", or " + ops.whose() + " for them"
		}
		return why + ": it goes to their login and to nothing else"
	})
}

// mayIdentify refuses a change of who the user Object.Name is in a
// source by anyone but that user and the operator. A thread opened
// assigned to them goes to whoever the identity names, so whoever sets it
// sends what is meant for a person to another. Who asks is told as for
// mayTell: by the store's own records of the pane before the caller's
// word.
func mayIdentify(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	return mayStandFor(ctx, tx, name, actor, func(ops operators) string {
		why := "who " + name + " is in a source is said by " + name
		if !ops.none() && !ops.is(name) {
			why += ", or by " + ops.words()
		}
		return why + ": a thread assigned to them goes there"
	})
}

// maySourceCheckTell is mayTell for yardr source check --user: one test
// message through the source to the user Object.Name, which buzzes their
// phone. Theirs to ask for, or the operator's for them, and never the
// pane of an agent's or a crew member's. The words are the check's own,
// kept from when the CLI refused it.
func maySourceCheckTell(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	return mayStandFor(ctx, tx, name, actor, func(ops operators) string {
		return "a test message to " + name + " is theirs to ask for, or " + ops.whose()
	})
}

// mayStandFor is the rule behind mayTell, mayAnswerLogin and
// mayIdentify: the user name and the operators, by the store's own
// records of who calls, and refuses the pane of a session or a crew
// member with says's words, given the operators.
func mayStandFor(ctx context.Context, tx *bolt.Tx, name, actor string, says func(ops operators) string) error {
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	why := says(ops)
	if who, err := agentPane(ctx, tx); err != nil {
		return err
	} else if who != "" {
		return fmt.Errorf("%s; this is %s", why, who)
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != name && !ops.is(actor) {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// maySubscribe refuses actor changing what the user Object.Name is told:
// that is the user's own to decide, or the operator's for them. A crew
// member deciding what a person hears of is what notifications are there
// to end. From a session's or a crew member's pane the actor is the
// pane's (paneAuthor) before the rule is read, so a pane can never pass as
// the user or the operator by --actor alone.
func maySubscribe(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if actor == name || ops.is(actor) {
		return nil
	}
	who := "the operator"
	if !ops.none() && !ops.is(name) {
		who = ops.words()
	}
	if tx.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s is a crew member, and a crew member does not decide what a person is told: %s subscribes themselves, or %s does it for them",
			actor, name, who)
	}
	return fmt.Errorf("%s does not decide what %s is told: %s subscribes themselves, or %s does it for them", or(actor, "nobody"), name, name, who)
}

// maySubscribeCrew refuses actor changing what the crew member
// Object.Name is told: that is the member's own to decide, or the
// operator's for them. From a session's or a crew member's pane the actor
// is the pane's (paneAuthor) before the rule is read, so a pane can never
// pass as the member or the operator by --actor alone.
func maySubscribeCrew(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if actor == name || ops.is(actor) {
		return nil
	}
	who := "the operator"
	if !ops.none() && !ops.is(name) {
		who = ops.words()
	}
	return fmt.Errorf("only %s or %s changes what %s is told; this is %s", name, who, name, or(actor, "nobody"))
}

// maySubscribeGroup refuses actor changing what the manual group
// Object.Group tells its members: that is a member's to decide, or the
// operator's for them. A group with no members is the operator's alone to
// change, not anyone's as a move is (Group.Members): a subscription
// blinds whoever it is removed from. From a session's or a crew member's
// pane the actor is the pane's (paneAuthor) before the rule is read, so a
// pane can never pass as a member or the operator by --actor alone.
func maySubscribeGroup(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	g := obj.Group
	if g == nil {
		// Asked over May, whose object carries only what the socket
		// names: a change with no group is refused, not read.
		return fmt.Errorf("change what a group tells its members: no group was named")
	}
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if slices.Contains(g.Members, actor) || ops.is(actor) {
		return nil
	}
	who := "the operator"
	if !ops.none() && !ops.is(g.Name) {
		who = ops.words()
	}
	return fmt.Errorf("only a member of %s or %s changes what %s is told; this is %s", g.Name, who, g.Name, or(actor, "nobody"))
}

// maySpeakAs refuses to speak as the crew member Object.Name to anyone
// but the crew member itself and the operator: a bead's agent, or another
// crew member, would otherwise speak in its name, with its credential or
// the operator's. From a session's or a crew member's pane the actor is
// the pane's (paneAuthor) before the rule is read, so a pane can never
// pass as the crew member or the operator by --actor alone; the pane yardr
// started it in still speaks as it when a command there names another.
// Like operatorOnly it is a line, not a wall.
func maySpeakAs(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	if actor == name {
		return nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return err
	} else if r != nil && r.Name == name {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s's own session or %s may speak as %s; this is %s", name, operator, name, this)
}

// ruleReadInbox refuses to read or acknowledge (Object.Does) the inbox of
// the user Object.Name to anyone but them and an operator. The reader
// comes resolved (readerOf): from a pane it is the pane's author, so a
// pane can never read another's inbox by naming them.
func ruleReadInbox(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	name := obj.Name
	if actor == name {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s or %s may %s %s's inbox; this is %s", name, operator, obj.Does, name, this)
}

// ruleRemoveDevice refuses removing a device of the user Object.Name to
// anyone but them and the operator. The actor comes resolved from the
// pane (paneAuthor).
func ruleRemoveDevice(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	user := obj.Name
	if actor == user {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s or %s may remove a device of %s; this is %s", user, operator, user, this)
}

// ruleTakeBackAsk refuses taking ask Object.Seq back to anyone but who
// asked it (Object.Whose) and the operator.
func ruleTakeBackAsk(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	asker := obj.Whose
	if actor == asker {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s, who asked it, or %s may take ask %d back; this is %s", asker, operator, obj.Seq, this)
}

// ruleAnswerAsk refuses an answer to a plain ask (Object.Seq) by anyone
// but its addressee (Object.To) and an operator. The actor comes resolved
// from the pane (paneAuthor).
func ruleAnswerAsk(_ context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if actor != obj.To && !ops.is(actor) {
		return fmt.Errorf("ask %d is for %s: only they or %s may answer it; this is %s", obj.Seq, obj.To, ops.words(), or(actor, "nobody"))
	}
	return nil
}

// ruleAnswerIdentityAsk holds a mapping card to the operator it names.
// Unlike a plain ask, another operator is not an addressee for this card.
func ruleAnswerIdentityAsk(_ context.Context, _ *bolt.Tx, actor string, _ Action, obj Object) error {
	if actor != obj.To {
		return fmt.Errorf("ask %d %s: this is %s", obj.Seq, obj.Does, or(actor, "nobody"))
	}
	return nil
}

// ruleAnswerPeerAsk holds an ask received from another yard to the user it
// was addressed to; that yard takes no other person's answer.
func ruleAnswerPeerAsk(_ context.Context, _ *bolt.Tx, actor string, _ Action, obj Object) error {
	if actor != obj.To {
		return fmt.Errorf("ask %d on %s is for %s: %s takes no one else's answer to it", obj.Seq, obj.Name, obj.To, obj.Does)
	}
	return nil
}

// ruleAnswerStageAsk keeps the stage answer's trusted-peer and fallback
// paths inside the policy decision, so a replacement policy has the last
// word on every way of answering it.
func ruleAnswerStageAsk(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	err := checkMembers(ctx, tx, actor, act, obj)
	if err == nil {
		return nil
	}
	spoof, serr := stageAnswerSpoofTx(ctx, tx, obj.row, actor)
	if serr != nil || spoof {
		if serr != nil {
			return serr
		}
		return err
	}
	var peers []Peer
	if e := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		peers = append(peers, *p)
		return nil
	}); e != nil {
		return e
	}
	if as, ok := TrustedAs(peers)[actor]; ok &&
		(slices.Contains(obj.Group.Members, as) || obj.Fallback && as == obj.To) {
		return nil
	}
	if obj.Fallback && actor == obj.To {
		return nil
	}
	return err
}

// mayAddDevice is who gives the user Object.Name a device: the operator,
// outside an agent's pane. It is asked where the operator asks for one,
// by AddDevice and by the opening of a pairing (DevicePairOpen).
func mayAddDevice(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	if who, err := agentPane(ctx, tx); err != nil {
		return err
	} else if who != "" {
		return fmt.Errorf("a device is added by the operator, outside an agent's pane: whoever adds one decides who may speak as %s; this is %s", obj.Name, who)
	}
	return operatorOnly(ctx, tx, actor, string(act), Alone)
}

// ruleAskPeer is who may ask the user Object.To at the peer Object.Name:
// a crew member its definition allows that yard (MailPeers), a user of
// the yard, or the operator. The actor comes resolved from the pane.
func ruleAskPeer(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	peer, to := obj.Name, obj.To
	var r CrewMember
	if ok, err := get(tx, bCrew, actor, &r); err != nil {
		return err
	} else if ok && !slices.Contains(r.MayMail(), peer) {
		return fmt.Errorf("crew member %s may not ask %s@%s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
			actor, to, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
	} else if !ok && tx.Bucket(bUsers).Get([]byte(actor)) == nil {
		if _, this, err := notOperator(ctx, tx, actor); err != nil {
			return err
		} else if this != "" {
			return fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may ask another yard; this is %s",
				peer, MailPeers, peer, this)
		}
	}
	return nil
}

// ruleMailPeer is ruleAskPeer for mail to the crew members of the peer
// Object.Name.
func ruleMailPeer(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	peer := obj.Name
	var r CrewMember
	if ok, err := get(tx, bCrew, actor, &r); err != nil {
		return err
	} else if ok && !slices.Contains(r.MayMail(), peer) {
		return fmt.Errorf("crew member %s may not mail crew members of %s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
			actor, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
	} else if !ok && tx.Bucket(bUsers).Get([]byte(actor)) == nil {
		if _, this, err := notOperator(ctx, tx, actor); err != nil {
			return err
		} else if this != "" {
			return fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may mail another yard; this is %s",
				peer, MailPeers, peer, this)
		}
	}
	return nil
}

// mayAtPeer says why actor may not read a bead that is at the peer
// Object.Name (ActReadAtPeer), or write on that yard's bead for it
// (ActNoteAtPeer): it needs what writing to a peer needs (MailPeer). A
// crew member needs the peer in its MailPeers setting, a user of the yard
// needs nothing, and anyone else must be the operator: a bead's agent
// reads nothing of another yard beyond what its own bead holds. From a
// session's or a crew member's pane the actor is the pane's (paneAuthor)
// before the rule is read, so a pane can never pass as a user or the
// operator by --actor alone.
func mayAtPeer(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
	peer, what := obj.Name, string(act)
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	var r CrewMember
	if ok, err := get(tx, bCrew, actor, &r); err != nil {
		return err
	} else if ok && !slices.Contains(r.MayMail(), peer) {
		return fmt.Errorf("crew member %s may not %s %s: its definition does not allow that yard. The operator allows it with yardr crew set %s %s=%s",
			actor, what, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
	} else if !ok && tx.Bucket(bUsers).Get([]byte(actor)) == nil {
		if _, this, err := notOperator(ctx, tx, actor); err != nil {
			return err
		} else if this != "" {
			return fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may %s another yard; this is %s",
				peer, MailPeers, peer, what, this)
		}
	}
	return nil
}

// checkMembers refuses moving the bead out of its stage when the stage's
// group (Object.Group) has members and whoever asks is none of them,
// naming who may. Who asks is told by the store's own records before the
// caller's word: a move from the pane of a bead's agent, or one that names
// such a session as its own (callerSession), is that agent's, which is a
// member of no group, and one from a crew member's pane is that crew
// member's, whatever actor the command names. A stage of people is what
// stands between another yard's text and this yard's agents
// (docs/peers.md, "The intake"), and a crew member that reads such a bead
// and is talked into moving it on cannot do it by naming someone who may.
// The one exception is a session of the exec runner: a script, not a
// model, so there is no one for another yard's text to talk into
// anything, and it counts as its group where the members list that group.
// A call the store knows no pane of is told by its actor, as a manual
// group's session is: a person at work.
func checkMembers(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	g, b := obj.Group, obj.row
	if g == nil || len(g.Members) == 0 {
		return nil
	}
	if b == nil {
		// Asked over May, whose object carries only what the socket
		// names: a move with members and no bead is refused, not read.
		return fmt.Errorf("move a bead out of its stage: no bead was named")
	}
	only := fmt.Sprintf("bead %s: only members of group %s may move it out of stage %s: %s", b.ID, g.Name, b.Stage, strings.Join(g.Members, ", "))
	sess, err := callerSessionTx(tx, callerOf(ctx), obj.exp, b)
	if err != nil {
		return err
	}
	allowed, who, err := memberAllowed(ctx, tx, sess, actor, g.Members)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	return fmt.Errorf("%s %s", only, who)
}

// memberAllowed says whether whoever asks counts as a member of a group
// with the given members, and, when they do not, the words in brackets
// that say who asks instead, for a refusal that opens with who may. It is
// checkMembers' who-asks resolution with no bead of its own, so filing a
// bead past a members stage (checkCreateMembers) asks it the same way a
// move out of one does: a session that is not a person's (sess, resolved
// by the caller as its records tell it) counts only as an exec session
// whose group the members list, a crew member's pane counts as that crew
// member, and any other call counts as the actor it names. There is no
// operator leg: membership is membership.
func memberAllowed(ctx context.Context, tx *bolt.Tx, sess *Session, actor string, members []string) (bool, string, error) {
	if sess != nil && !sess.Manual() {
		if sess.RunnerName() == "exec" && slices.Contains(members, sess.Group) {
			return true, "", nil
		}
		return false, fmt.Sprintf("(this is the session %s of a bead's agent, whatever actor the command names)", sess.ID), nil
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil {
		return false, "", err
	}
	switch {
	case r != nil && slices.Contains(members, r.Name):
		return true, "", nil
	case r != nil && r.Name != actor:
		return false, fmt.Sprintf("(this is the pane of the crew member %s, whatever actor the command names: %q)", r.Name, actor), nil
	case r == nil && slices.Contains(members, actor):
		return true, "", nil
	}
	return false, fmt.Sprintf("(you are %q)", actor), nil
}

// mayWaitLogin refuses a wait registered from the pane of any agent but
// the yardmaster: what a wait is handed is a value the operator typed into
// a secret ask's field, told that no agent is shown it. The yardmaster leads
// the login in its pane (design yardr-bwm2, 3.5) and the operator may lead
// one from their own terminal; a bead's agent or another crew member that
// registered a wait, and asked the operator for its input, would read what
// the operator typed. It goes by the store's own records of the process
// that called, as maySource does, and reads no actor.
func mayWaitLogin(ctx context.Context, tx *bolt.Tx, _ string, _ Action, _ Object) error {
	const why = "a login's input is handed to the login the yardmaster or the operator leads"
	sess, err := paneSession(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() {
		return fmt.Errorf("%s; this is the session %s of a bead's agent", why, sess.ID)
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil || r == nil {
		return err
	}
	var yardmaster string
	if _, err := get(tx, bSettings, YardmasterSetting, &yardmaster); err != nil {
		return err
	}
	if yardmaster == "" {
		yardmaster = "yardmaster"
	}
	if r.Name != yardmaster {
		return fmt.Errorf("%s; this is the crew member %s", why, r.Name)
	}
	return nil
}
