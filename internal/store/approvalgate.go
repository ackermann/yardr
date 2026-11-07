package store

// ApprovalGate is the gate of the store's policy that an approval of the
// ask seq on the bead id, whose card is a, asks: the Action and Object to
// hand May. It is the one place that names it, read by the writes that
// approve (approveAsk and prepareSourceCheck through runGate, giveSecret
// through secretGate, the setup and depot-push approvals through it) and
// by a client that must refuse a person who may not approve before it asks
// them anything (the CLI's gate, which wants the ask's number typed back):
// the refusal comes first, in the store's words, and the two cannot drift.
// ok is false for a card no approval of this yard is asked of: one with
// nothing to approve, or a command the yard cannot read; the call that
// follows refuses it in its own words. A bead@peer's ask is the caller's to
// keep out (the CLI's askedCard does): its id alone says so, not its card.
func ApprovalGate(id string, seq uint64, a Ask) (act Action, obj Object, ok bool) {
	obj = Object{Kind: KindAsk, Name: id, Seq: seq, To: a.To}
	switch {
	case a.Secret != nil:
		act, obj = secretGate(*a.Secret)
		return act, obj, true
	case a.Setup != nil, a.DepotPush != nil:
		return ActApproveRun, obj, true
	case a.Run == nil:
		return "", Object{}, false
	}
	c, argv, err := parseRun(a.Run.Argv)
	if err != nil {
		return "", Object{}, false
	}
	act, obj = runGate(obj, c, argv)
	return act, obj, true
}

// runGate is the gate of an approval of the run command argv (c) on the
// ask obj names: the one user who alone gives it, the one asked where it
// is a user's own (a source check as a user among them), and an operator
// for any other.
func runGate(obj Object, c *runCommand, argv []string) (Action, Object) {
	switch {
	case c.sole != nil && c.sole(argv) != "":
		obj.Whose = c.sole(argv)
		return ActApproveSole, obj
	case c.whose != nil:
		obj.Whose = c.whose(argv)
		return ActApproveOwn, obj
	}
	return ActApproveRun, obj
}

// secretGate is the gate of giving the secret sec asks for: whose the
// value is, by what it is for.
func secretGate(sec AskSecret) (Action, Object) {
	switch {
	case sec.Harness != "":
		return ActGiveHarnessKey, Object{Kind: KindHarness, Name: sec.Harness, To: sec.Crew}
	case sec.User != "":
		return ActTell, Object{Kind: KindUser, Name: sec.User}
	case sec.Login != "" && sec.As != "":
		return ActAnswerLogin, Object{Kind: KindUser, Name: sec.As}
	case sec.As != "":
		return ActTell, Object{Kind: KindUser, Name: sec.As}
	case sec.Crew != "":
		return ActGiveCrewCredential, Object{Kind: KindCrew, Name: sec.Crew}
	case sec.Hook != "":
		return ActGiveHookSecret, Object{Kind: KindHook, Name: sec.Hook}
	case sec.Login != "":
		return ActAnswerSourceLogin, Object{Kind: KindLogin, Name: sec.Login}
	}
	return ActSetSourceKey, Object{Kind: KindSource, Name: sec.Source}
}
