package rpc

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ClientHeader is where a program that is not yardr says what it is
// ("footplate/0.1"). Nothing is decided by it: the revision header is for
// builds of yardr, and a caller without one is served as it is.
const ClientHeader = "X-Yardr-Client"

// Calls are the methods a program that is not yardr may rely on, each with
// the names its arguments have in a request that gives them by name:
// docs/api.md is the contract, and the test of this file holds the two
// together. They are what a chat client needs, yardr chat among them: its
// view (chat.Store) and the command that opens it; what a client that
// shows a person's decisions needs (UserAsks, Bead and the writes); and
// what a client that shows a kept hook delivery's files needs (DeliveryFiles,
// with the hook_files event to read it again on; the bytes are the blob
// route's).
// Every other method is yardr's own, changes with its builds,
// and takes its arguments as yardr sends them, in an array.
//
// A name here is on the wire for good. An argument a later build adds is
// one a client of today leaves out, so it must mean what the call did
// before when it is the zero value.
var Calls = map[string][]string{
	"Head":                 {},
	"Settings":             {},
	"Users":                {},
	"GetUser":              {"name"},
	"UserInbox":            {"name", "limit", "actor"},
	"AckUserInbox":         {"name", "through", "actor"},
	"UserNotifications":    {"name", "limit", "actor"},
	"AckUserNotifications": {"name", "through", "actor"},
	"Conversation":         {"a", "b", "after", "limit", "before"},
	"PutBlob":              {"data"},
	"Mail":                 {"to", "text", "actor", "files"},
	"MailPeer":             {"to", "peer", "text", "actor", "files"},
	"React":                {"seq", "emoji", "actor"},
	"Peers":                {},
	"Crew":                 {},
	"GetCrew":              {"name"},
	"UserAsks":             {"name", "actor", "viewer"},
	"Bead":                 {"bead", "viewer"},
	"Arrivals":             {"viewer"},
	"Flows":                {"viewer"},
	"AnswerAsk":            {"bead", "ask", "option", "words", "actor", "weighed"},
	"DeferAsk":             {"bead", "ask", "actor"},
	"DeclineAsk":           {"bead", "ask", "words", "actor"},
	"TakeBackAsk":          {"bead", "ask", "reason", "actor"},
	"AddNote":              {"bead", "text", "actor", "files"},
	"DeliveryFiles":        {"seq"},
}

// OperatorCalls are documented as Calls are, and are an operator's alone
// to make. The socket serves them to whoever the store lets make them; the
// listener serves them to a device of an operator (a user whose role is
// store.RoleOperator) and answers every
// other caller as it answers a method the yard does not have
// (UserServer.operatorCall). GiveSecret carries a secret a person typed, on
// its way to the file a source's credential is read from: it crosses a
// network only from the operator's own device, under the TLS in front of
// the listener and no other protection, and never a link to another yard.
// One giving is not the operator's alone: a secret ask for a user's own
// address or credential in a source is that user's to answer too, and the
// listener serves it to a device of theirs (UserServer.ownSecret).
//
// ApproveAsk approves a run ask, and the yard then runs the one command the
// ask holds, as the operator (design yardr-x3g8, section 3.6). It is the
// first call here that causes an operator's command, and what it can cause
// is the store's fixed list (store.ApproveAsk) and nothing else. It is the
// operator's device's and no other's: a run ask for a user's own
// subscription, which that user approves on the socket, is not served to a
// device of theirs.
//
// ApproveSetup approves a setup ask with the preset pick and the account
// together, and the yard then applies the pack, takes the account, and
// grants the preset picked, as the operator (design yardr-7d4e, section
// 3.6). Like GiveSecret it carries a secret a person typed, on its way to
// the file a source's credential is read from, or the candidate taken
// instead, or on an update nothing: the account that stands is kept. Like
// ApproveAsk it is the operator's device's and no other's.
//
// Each of the three, and AnswerAsk for a pick on a mapping ask, takes the
// argument weighed: the client's word that its person saw the weight of a
// weighty card before approving it (design yardr-ekmx 3.3). The yard
// refuses a weighty card without it and checks nothing more, so it is the
// client's gate that stands behind the word. A client from before it leaves
// it out, and a routine card asks for none.
//
// SourceGrants reads every grant of the yard's sources: who may call what
// (each call with its kind, so that a client without the sources' files tells
// a read from an update), as whom, and each call's own when, by whom and on
// which card (design yardr-ekmx 3.4). It changes nothing and carries no
// secret, but the whole grant graph is the operator's to read, so it is here
// and not in Calls.
//
// SessionScreen reads what a running session's pane shows: its visible
// screen with the ANSI styling, so the operator sees whether something
// hangs. It changes nothing, but a screen can show what an agent printed,
// a secret among it, so it is the operator's to read and no one else's.
//
// Teammates reads the yard's teammates: one row per address of the
// standing web_jwt_users mapping, each with its yard user name, role,
// email, who invited them and when, and their devices with last use. A
// row with devices is paired; a row without is waiting. Whether anyone
// signed in over the edge's web door is not told: nothing at the yard
// records it. It is the operator's to read: on the socket it is a read,
// served as every read of the store is to a caller on the yard's
// machine, and on the listener it is there for a device of the operator
// and for no other caller, since the mapping is theirs. It changes
// nothing, but the operator's device token goes with the request, so the
// listener takes it only over https or from the yard's own machine, like
// the other calls of this list, and down the link from the edge from a
// paired device of the operator, as an approval.
//
// UninviteTeammate files the removal of a teammate: the run ask
// `user uninvite <user>` on a new bead, addressed to the yard's
// operator, as AskNew files it. It files the ask and no more: approval,
// the edge-first removal and the retry are the card's built path, and
// approving from the same device uses ApproveAsk. It refuses a user the
// yard has not, one no mapping names, and the caller's own user. On the
// socket it files as its actor, as AskNew does; on the listener it is
// there for a device of the operator and for no other caller. The
// operator's device token goes with the request, so the listener takes
// it only over https or from the yard's own machine, like the other
// calls of this list, and down the link from the edge from a paired
// device of the operator, as an approval.
//
// TelemetryShow reads what yardr telemetry show prints: the switch, the
// install id with its pseudonymous sentence, and the next request or why
// there is none. It changes nothing, but the id groups the yard's events
// across time, so it is the operator's to read and no one else's: its
// socket class is Operator, and the listener serves it to an operator's
// device alone.
// TelemetrySet turns the switch on or off through the CLI's own
// functions, so consent bookkeeping is the same however it is turned. It
// is the operator's to change, and this device call is never served down
// the yard link from the edge. A hosted customer without a terminal uses
// the web view's control through its separate web link, where the view
// verifies the gateway's JWT and maps it to a yard user.
//
// DevicePairOpen opens a pairing for one device of one user, as `yardr
// user device pair` does, and answers its code and expiry;
// DevicePairState reads the open pairing back with its code and QR
// string, so a reopened window shows the same pairing again; and
// DevicePairClose ends it, as `yardr user device pair --end` does. The
// three are the socket's alone: a device token never opens a pairing, so
// the listener serves them to no device, not even the operator's, and
// answers each as a method the yard does not have, down the yard link
// from the edge alike (UserServer.operatorCall).
var OperatorCalls = map[string][]string{
	"GiveSecret":       {"bead", "ask", "values", "actor", "weighed"},
	"ApproveAsk":       {"bead", "ask", "actor", "weighed", "values"},
	"ApproveSetup":     {"bead", "ask", "option", "values", "account", "actor", "weighed"},
	"SourceGrants":     {"source"},
	"SessionScreen":    {"session"},
	"Teammates":        {},
	"UninviteTeammate": {"user", "actor"},
	"TelemetryShow":    {},
	"TelemetrySet":     {"on", "actor"},
	"DevicePairOpen":   {"user", "name", "actor"},
	"DevicePairState":  {},
	"DevicePairClose":  {"actor"},
}

// socketOnly are the calls of OperatorCalls the listener serves to no
// device, not even the operator's: opening, reading and ending a device
// pairing are the socket's alone, since a device token never opens a
// pairing. The link from the edge is served with the listener's mux, so
// down it they are answered as not there too.
var socketOnly = map[string]bool{
	"DevicePairOpen":  true,
	"DevicePairState": true,
	"DevicePairClose": true,
}

func isOperatorCall(method string) bool {
	_, ok := OperatorCalls[method]
	return ok
}

// argNames are the names of the arguments of a call docs/api.md names, of
// either list.
func argNames(method string) ([]string, bool) {
	if names, ok := Calls[method]; ok {
		return names, true
	}
	names, ok := OperatorCalls[method]
	return names, ok
}

// named are the arguments of the call method as its body gives them by
// name, in the method's order. One left out is the zero value of its type
// (null decodes to it); one the call does not have is refused, since a
// misspelt name would otherwise be a call that silently means another.
func named(method string, body json.RawMessage) ([]json.RawMessage, error) {
	names, ok := argNames(method)
	if !ok {
		return nil, fmt.Errorf("arguments: %s takes them as a JSON array: only the calls docs/api.md names take them by name", method)
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal(body, &given); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	raw := make([]json.RawMessage, len(names))
	for i, name := range names {
		raw[i] = json.RawMessage("null")
		if v, ok := given[name]; ok {
			raw[i] = v
			delete(given, name)
		}
	}
	if len(given) > 0 {
		// One stray name refuses the call, and the first by order is the one
		// said, so the refusal reads the same on every call whatever order
		// the map gives.
		name := slices.Min(slices.Collect(maps.Keys(given)))
		takes := "none"
		if len(names) > 0 {
			takes = strings.Join(names, ", ")
		}
		if _, secret := OperatorCalls[method]; secret {
			// Not said back: a client that put a secret's value where a name
			// goes would read it in the refusal.
			return nil, fmt.Errorf("an argument: %s has none of that name (it takes %s)", method, takes)
		}
		return nil, fmt.Errorf("argument %q: %s has none of that name (it takes %s)", name, method, takes)
	}
	return raw, nil
}
