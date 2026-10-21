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
// view (chat.Store) and the command that opens it; and what a client that
// shows a person's decisions needs (UserAsks, Bead and the writes).
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
	"UserInbox":            {"name", "limit"},
	"AckUserInbox":         {"name", "through"},
	"UserNotifications":    {"name", "limit"},
	"AckUserNotifications": {"name", "through"},
	"Conversation":         {"a", "b", "after", "limit", "before"},
	"PutBlob":              {"data"},
	"Mail":                 {"to", "text", "actor", "files"},
	"MailPeer":             {"to", "peer", "text", "actor", "files"},
	"React":                {"seq", "emoji", "actor"},
	"Peers":                {},
	"Crew":                 {},
	"GetCrew":              {"name"},
	"UserAsks":             {"name"},
	"Bead":                 {"bead"},
	"Arrivals":             {},
	"Flows":                {},
	"AnswerAsk":            {"bead", "ask", "option", "words", "actor", "weighed"},
	"DeferAsk":             {"bead", "ask", "actor"},
	"DeclineAsk":           {"bead", "ask", "words", "actor"},
	"TakeBackAsk":          {"bead", "ask", "reason", "actor"},
	"AddNote":              {"bead", "text", "actor", "files"},
}

// OperatorCalls are documented as Calls are, and are the operator's alone
// to make. The socket serves them to whoever the store lets make them; the
// listener serves them to a device of the yard's operator and answers every
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
// SourceGrants reads every grant of the yard's sources: who may call what,
// as whom, and each call's own when, by whom and on which card (design
// yardr-ekmx 3.4). It changes nothing and carries no secret, but the whole
// grant graph is the operator's to read, so it is here and not in Calls.
var OperatorCalls = map[string][]string{
	"GiveSecret":   {"bead", "ask", "values", "actor", "weighed"},
	"ApproveAsk":   {"bead", "ask", "actor", "weighed"},
	"ApproveSetup": {"bead", "ask", "option", "values", "account", "actor", "weighed"},
	"SourceGrants": {"source"},
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
	for _, name := range slices.Sorted(maps.Keys(given)) {
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
