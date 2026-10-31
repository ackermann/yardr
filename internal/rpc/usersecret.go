package rpc

import (
	"context"
	"encoding/json"
	"slices"

	"yardr.dev/yardr/internal/store"
)

// The secrets a device may give are told from the body of the call that
// carries them: which ask a GiveSecret names, and whether an ApproveSetup types
// the account. The listener reads the body to judge such a call before it takes
// it down the link from the edge (UserServer.call, inTheClear).

// ownSecret reports whether body, the arguments of GiveSecret from the
// device c of a user who is not the operator, names a secret ask for that
// user's own address in a source (store.AskSecret.User), for their own
// credential in one (store.AskSecret.As) or for the input of their own
// login (As beside a Login: the code their sign-in page shows). Those
// givings are the user's own to make, on the socket (the store's mayTell)
// and so here: each writes a file of theirs or hands their code to their
// own waiting login, and sets no credential of the yard. For every other ask, and
// for arguments that name none, the call stays what it is to anyone but
// the operator: not there. Who may give is still the store's to decide,
// with the user of the device's token as the actor (asUser).
func (u *UserServer) ownSecret(ctx context.Context, c *Caller, body []byte) bool {
	if c.User == "" {
		return false
	}
	sec := u.namedSecret(ctx, body)
	return sec != nil && (sec.User == c.User || sec.As == c.User)
}

// loginCode reports whether body is a GiveSecret call from an operator's
// device that names the daemon's ask for a harness login's input and carries
// only that ask's values. Other argument shapes must be refused before the
// edge reads them, even if a client later rejects them. It is the one secret
// taken down the link from the edge on a yard that is not hosted
// (inTheClear), because it is used once, within minutes, and the login it is
// handed to keeps it in memory and nowhere else. A user's own login code (As)
// is not it: widening this is a decision of its own.
func (u *UserServer) loginCode(ctx context.Context, body []byte, user string) bool {
	if _, err := named("GiveSecret", body); err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	decode := func(name string, value any) bool {
		raw, ok := fields[name]
		return !ok || json.Unmarshal(raw, value) == nil
	}
	var id string
	var ask uint64
	var values map[string]string
	var actor string
	var weighed bool
	if !decode("bead", &id) || !decode("ask", &ask) || !decode("values", &values) ||
		!decode("actor", &actor) || !decode("weighed", &weighed) {
		return false
	}
	if id == "" || ask == 0 || !weighed || actor != "" && actor != user {
		return false
	}
	sec := u.namedSecret(ctx, body)
	if sec == nil || !sec.OperatorLoginInput() || len(values) != len(sec.Env) {
		return false
	}
	for _, name := range sec.Env {
		if _, ok := values[name]; !ok {
			return false
		}
	}
	return true
}

// crossesConnected reports whether the operator's call name, with the
// arguments body from the device of user, is one that a yard connected to the
// edge and not hosted takes down the link: a GiveSecret for the code of a
// harness login (loginCode), and an ApproveSetup without typed account values
// (typesAccount returns false), which settles by a candidate taken or the
// account kept.
// Hosted yards take both whole and do not ask (call).
func (u *UserServer) crossesConnected(ctx context.Context, name string, body []byte, user string) bool {
	switch name {
	case "GiveSecret":
		return u.loginCode(ctx, body, user)
	case "ApproveSetup":
		return !typesAccount(body)
	}
	return false
}

// typesAccount reports whether body, the arguments of an ApproveSetup, carries
// values: the account a person typed, which is a secret on its way to the file
// a source's credential is read from. It is read as the call reads it (named),
// so a body the call would refuse, or values that are no object of strings, are
// taken to carry some: the refusal is the safe answer to what cannot be told.
func typesAccount(body []byte) bool {
	args, err := named("ApproveSetup", body)
	if err != nil {
		return true
	}
	var values map[string]string
	if json.Unmarshal(args[slices.Index(OperatorCalls["ApproveSetup"], "values")], &values) != nil {
		return true
	}
	return len(values) > 0
}

// namedSecret is the secret that the ask body, the arguments of GiveSecret,
// names asks for: nil for arguments that name no ask of a bead, and for an ask
// that asks for none. Only the bead and the ask are read from the arguments,
// and nothing of what is found is said back; who may give is the store's to
// decide.
func (u *UserServer) namedSecret(ctx context.Context, body []byte) *store.AskSecret {
	var named map[string]json.RawMessage
	if json.Unmarshal(body, &named) != nil {
		return nil
	}
	var id string
	if json.Unmarshal(named["bead"], &id) != nil || id == "" {
		return nil
	}
	var ask uint64
	if json.Unmarshal(named["ask"], &ask) != nil {
		return nil
	}
	notes, err := u.st.Notes(ctx, id)
	if err != nil {
		return nil
	}
	for _, n := range notes {
		if n.Seq != ask || n.Kind != store.NoteAsk {
			continue
		}
		ask, err := store.ParseAsk(n)
		if err != nil {
			return nil
		}
		return ask.Secret
	}
	return nil
}
