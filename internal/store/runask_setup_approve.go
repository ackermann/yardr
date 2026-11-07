// The setup approval (design yardr-7d4e, B2): approving a setup ask with
// the preset pick and the account typed, taken or kept. The approval is
// validated, a typed account is tried with the source's check call before
// anything is stored, and the write records approved-and-not-carried-out
// with the carrying record beside it; the server carries it out (B3).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
)

// ApproveSetup approves the setup ask ask on the bead id as actor: the
// preset pick option with the account typed unseen (values), the candidate
// taken (account), or neither where a drift card's standing account is
// kept. In one write it checks the ask and who approves, holds the approval
// to the yard the card was written from, and records an answer that holds
// the preset, the names given, the candidate taken or the kept account,
// and CarryPending, with the carrying record beside it for the server to
// take up. Typed values are handed to the server's memory after the write
// commits, never stored: between the approval and the account's write they
// live there alone. A picked candidate and a kept account hand nothing.
// Where the pack's source names a check call, the typed account is tried
// with it before the write, with no store write open: a refused check
// refuses the approval with the check's words, records nothing and leaves
// the ask open.
//
// It refuses an ask that is not on the bead, sets up no pack, or was
// declined, taken back or approved already: a setup runs once for one ask.
// It refuses anyone but the operator, and every call from the pane of an
// agent whatever actor it names (mayApprove). It refuses when the yard
// would not write the same card now (setupStale), and — on a first setup —
// when every grantee holds the picked preset already. Picking YouDecide
// grants the asker's recommended preset, and is refused where none is
// recommended. Where candidates stand, exactly one of the typed values and
// the picked candidate settles the account, and where none stand the typed
// values do; a drift card whose file holds every name is also kept with a
// bare pick, and one with missing names wants those typed. A refused
// approval records nothing and changes nothing, and leaves the ask open.
//
// It is served on the yard's socket, and on the listener to a device of
// the operator and to no other caller (rpc.OperatorCalls), as run
// approvals and secret answers travel. No answer that comes over a link
// approves.
//
// A setup takes the account, so its card is always weighty (weight.go): it
// is approved only by a call that says its person saw the weight first
// (weighed), and without it the approval is refused, changes nothing and
// leaves the ask open (weighedRefusal). The yard checks what the client
// says and no more.
func (s *Store) ApproveSetup(ctx context.Context, id string, ask uint64, option int, values map[string]string, account, actor string, weighed bool) (*AskSetupApproved, error) {
	// A bead's handle never holds an "@" (validID).
	if strings.Contains(id, "@") {
		return nil, fmt.Errorf("ask %d on %s is a setup of another yard's operator: it is approved in the yard that asked, never over a link", ask, id)
	}
	// A typed account is tried with the source's check call before
	// anything is stored, with no store write open: a refused check
	// refuses the answer and leaves the card open. The write below
	// validates the approval again, so a card answered between the check
	// and the write is still refused.
	var job *setupCheck
	if err := s.writeVia(ctx, func(w *wtx) (err error) {
		var v *setupApproval
		v, err = w.validateSetupApproval(ctx, id, ask, option, values, account, actor, weighed)
		if err != nil {
			return err
		}
		job, err = setupCheckOf(ask, v, values)
		return err
	}); err != nil {
		return nil, err
	}
	if err := runSetupCheck(ctx, ask, job); err != nil {
		return nil, err
	}
	// The carry watcher can wake as soon as the write commits. Hold its
	// one-time take until the values are in memory beside the committed
	// carrying record.
	s.setupSecrets.mu.Lock()
	defer s.setupSecrets.mu.Unlock()
	var out *AskSetupApproved
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		out, err = w.approveSetup(ctx, id, ask, option, values, account, actor, weighed)
		return err
	})
	if err != nil {
		return nil, err
	}
	if account == "" && len(values) > 0 {
		s.handSetupSecretLocked(carryKey(out.Bead, ask), values)
	}
	return out, nil
}

// setupApproval is a setup approval validated and ready to record: the
// bead, who asked and the weight the card showed, the setup as asked, the
// preset picked, the candidate taken or the kept account, the names the
// typed values gave, and the actor the call came as.
type setupApproval struct {
	row     *beadRow
	asker   string
	weight  []AskWeight
	was     AskSetup
	seq     uint64
	picked  int
	account string
	keep    bool
	given   []string
	actor   string
}

// validateSetupApproval is the read-only half of approveSetup: every
// check an approval must pass, and what recording it needs. A batch may
// run it twice for one call (write): it reads the yard's records anew
// each time and ends the same.
func (w *wtx) validateSetupApproval(ctx context.Context, id string, seq uint64, option int, values map[string]string, account, actor string, weighed bool) (*setupApproval, error) {
	who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = who
	row, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return nil, err
	}
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", row.ID, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Setup == nil:
		return nil, fmt.Errorf("ask %d sets up no pack: a preset pick with the account settles a setup ask alone", seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	case r.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a setup runs once for one ask", seq, r.Current.Note.Author)
	}
	was := *r.Ask.Setup
	// Before anything is written: an agent's approval changes nothing, not
	// even what the transaction would take back.
	act, obj, _ := ApprovalGate(row.ID, seq, r.Ask)
	if err := w.may(ctx, actor, act, obj); err != nil {
		return nil, err
	}
	if option < 1 || option > len(r.Ask.Options) {
		return nil, fmt.Errorf("ask %d has options 1..%d", seq, len(r.Ask.Options))
	}
	picked := option
	if r.Ask.Options[option-1].Label == YouDecide {
		if r.Ask.Recommend == 0 {
			return nil, fmt.Errorf("ask %d: You decide grants the recommended preset, and none is recommended: pick a preset, or decline it", seq)
		}
		picked = r.Ask.Recommend
	}
	if picked < 1 || picked > len(was.Presets) {
		return nil, fmt.Errorf("ask %d holds options the yard did not write: nothing ran", seq)
	}
	account = strings.TrimSpace(account)
	if account != "" && len(values) > 0 {
		return nil, fmt.Errorf("ask %d takes the account typed or a candidate taken, not both: --secret or --account <source>", seq)
	}
	keep := false
	if account != "" {
		found := false
		for _, c := range was.Candidates {
			if c.Source == account {
				found = true
			}
		}
		if !found {
			if len(was.Candidates) == 0 {
				return nil, fmt.Errorf("ask %d lists no candidate to take: type the account %s", seq, strings.Join(was.Account, " and "))
			}
			var names []string
			for _, c := range was.Candidates {
				names = append(names, c.Source)
			}
			return nil, fmt.Errorf("ask %d lists no candidate %s: it lists %s, or type another %s", seq, account, strings.Join(names, ", "), strings.Join(was.Account, " and "))
		}
	} else if len(was.Missing) > 0 {
		// A drift card with missing names wants those typed: the
		// standing names stay where they are.
		if err := checkSetupValues(seq, was, values); err != nil {
			return nil, err
		}
		for _, n := range was.Missing {
			if _, ok := values[n]; !ok {
				if len(was.Candidates) > 0 {
					return nil, fmt.Errorf("ask %d asks for the missing account %s: %s was not given, and no candidate was taken (--account <source>)", seq, strings.Join(was.Missing, " and "), n)
				}
				return nil, fmt.Errorf("ask %d asks for the missing account %s: %s was not given", seq, strings.Join(was.Missing, " and "), n)
			}
		}
		if !sameNames(values, was.Missing) {
			return nil, fmt.Errorf("ask %d asks for the missing account %s: type %s", seq, strings.Join(was.Missing, " and "), strings.Join(was.Missing, " and "))
		}
	} else if was.Given {
		// A drift card whose file holds every name is kept with a bare
		// pick, typed another to change it, or a candidate taken.
		if len(values) == 0 {
			keep = true
		} else {
			if err := checkSetupValues(seq, was, values); err != nil {
				return nil, err
			}
			if !sameNames(values, was.Account) {
				return nil, fmt.Errorf("ask %d holds the account %s: type %s to change it, or approve keeping it with a bare preset pick", seq, strings.Join(was.Account, " and "), strings.Join(was.Account, " and "))
			}
		}
	} else {
		// What was sent is not said back: a client that put a value
		// where a name goes would read it in the refusal.
		if err := checkSetupValues(seq, was, values); err != nil {
			return nil, err
		}
		for _, n := range was.Account {
			if _, ok := values[n]; !ok {
				if len(was.Candidates) > 0 {
					return nil, fmt.Errorf("ask %d asks for the account %s: %s was not given, and no candidate was taken (--account <source>)", seq, strings.Join(was.Account, " and "), n)
				}
				return nil, fmt.Errorf("ask %d asks for the account %s: %s was not given", seq, strings.Join(was.Account, " and "), n)
			}
		}
	}
	card, err := w.setupCard(was.Pack, was.To, was.Base)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	now := *card
	if !sameMap(now.Seen, was.Seen) || !slices.Equal(now.Does, was.Does) || now.Back != was.Back || now.BackBy != was.BackBy {
		if err := w.setupStale(seq, was, now); err != nil {
			return nil, err
		}
	}
	if err := w.setupHeldStale(seq, was, picked); err != nil {
		return nil, err
	}
	// Weighed again from the card as it stands: a card whose weight is
	// not the one it showed, or that showed none, is approved no more. A
	// weighty one is approved by a client that says its person saw it.
	weight, err := w.setupWeight(now)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if err := weightStale(seq, r.Ask.Weight, weight); err != nil {
		return nil, err
	}
	if err := w.gateRefusal(seq, weight, r.Ask.Weight, actor, weighed); err != nil {
		return nil, err
	}
	var given []string
	for _, n := range was.Account {
		if _, ok := values[n]; ok {
			given = append(given, n)
		}
	}
	return &setupApproval{row: row, asker: r.Note.Author, weight: r.Ask.Weight, was: was,
		seq: seq, picked: picked, account: account, keep: keep, given: given, actor: actor}, nil
}

// recordSetupApproval is the writing half of approveSetup: the carrying
// record for the server to take up, and the answer that holds the preset,
// the names given, the candidate taken or the kept account.
func (w *wtx) recordSetupApproval(v *setupApproval) (*AskSetupApproved, error) {
	// An approval the yard carries out after this write leaves its
	// carrying record in it, for the server to take up (carry.go).
	if err := w.recordSetupCarry(v.row.ID, v.seq, v.was, v.account, v.keep, v.actor); err != nil {
		return nil, err
	}
	ans := Answer{Ask: v.seq, Option: v.picked, Ran: &AskRan{Argv: []string{"setup", v.was.Pack}, Result: CarryPending}}
	out := &AskSetupApproved{Bead: v.row.ID, Ask: v.seq, Asker: v.asker, Pack: v.was.Pack, Preset: v.was.Presets[v.picked-1].Name, Result: CarryPending}
	switch {
	case v.account != "":
		ans.Account, out.Account = v.account, v.account
	case v.keep:
		out.Keep = true
	default:
		ans.Given, out.Given = v.given, v.given
	}
	text, err := json.Marshal(ans)
	if err != nil {
		return nil, err
	}
	if _, err := w.addWitnessed(v.row, string(text), v.actor, v.weight); err != nil {
		return nil, err
	}
	return out, nil
}

// approveSetup is ApproveSetup inside the transaction: validated, then
// recorded. A batch may run it twice for one call (write): it reads the
// yard's records anew each time and ends the same. The values are handed
// to the memory after the write commits, so a rolled-back run hands
// nothing.
func (w *wtx) approveSetup(ctx context.Context, id string, seq uint64, option int, values map[string]string, account, actor string, weighed bool) (*AskSetupApproved, error) {
	v, err := w.validateSetupApproval(ctx, id, seq, option, values, account, actor, weighed)
	if err != nil {
		return nil, err
	}
	return w.recordSetupApproval(v)
}

// setupCheck is the pre-store check of a setup approval that typed its
// account: the source's declaration with the card's base in place of its
// own, and the typed read credential the check call is made with.
type setupCheck struct {
	d    *source.Declaration
	cred string
}

// setupCheckOf is the check a validated setup approval makes before
// anything is stored, nil where the card is stored as today: where no
// value was typed (a candidate taken or a standing account kept), where
// no carried source names a credential, where the source names no check,
// or where the typed values do not carry the read credential the check
// call is sent with — the missing names of a drift card alone, or a
// mint's inputs, whose token is minted only once the pack is in the
// yard. The declaration is the first carried source that names a
// credential, as the account the card asks for is (setupAccount), with
// the card's base choice in place of its own, as a check after setup is.
func setupCheckOf(seq uint64, v *setupApproval, values map[string]string) (*setupCheck, error) {
	if len(values) == 0 {
		return nil, nil
	}
	var d *source.Declaration
	for _, carried := range v.was.Carried {
		parsed, err := source.Parse("pack source "+carried.Name, []byte(carried.Text))
		if err != nil {
			return nil, fmt.Errorf("ask %d: %s", seq, OneLine(err.Error()))
		}
		if parsed.Credential == nil {
			continue
		}
		d = parsed
		break
	}
	if d == nil || d.Check == "" {
		return nil, nil
	}
	// The check call is a read, so it is sent with the credential's own
	// variable, never the one of its updates.
	cred := values[d.Credential.Env]
	if cred == "" {
		return nil, nil
	}
	return &setupCheck{d: d.WithBase(v.was.Base), cred: cred}, nil
}

// runSetupCheck makes the check call of a setup approval with its typed
// credential, the one call the declaration names for it, as yardr source
// check makes it after setup: with the credential as a call carries it
// and the timeout a call uses, and nothing written anywhere. A check the
// service refuses, or that is never answered, refuses the approval with
// the check's words, and nothing is stored: the card stays open to be
// answered again. Nil checks nothing.
func runSetupCheck(ctx context.Context, seq uint64, job *setupCheck) error {
	if job == nil {
		return nil
	}
	res, err := source.Do(ctx, job.d, job.d.Check, nil, job.cred, ReadSecretFile)
	call, _ := job.d.Call(job.d.Check)
	if err != nil {
		return fmt.Errorf("ask %d: the check call %s (%s) failed: %v. Nothing was stored", seq, job.d.Check, call.Request.Line(), err)
	}
	if !res.OK() {
		return fmt.Errorf("ask %d: the check call %s (%s) was answered %d%s. Nothing was stored", seq, job.d.Check, call.Request.Line(), res.Status, source.StatusHint(res.Status, job.d))
	}
	return nil
}

// checkSetupValues checks typed account values against the ask: every name
// sent is one the card asked for, and every value is a login value. What
// was sent is not said back: a client that put a value where a name goes
// would read it in the refusal.
func checkSetupValues(seq uint64, was AskSetup, values map[string]string) error {
	for n, value := range values {
		if !slices.Contains(was.Account, n) {
			return fmt.Errorf("ask %d asks for the account %s, and was given a name it did not ask for", seq, strings.Join(was.Account, " and "))
		}
		if err := loginValueError(seq, n, value); err != nil {
			return err
		}
	}
	return nil
}

// sameNames reports whether the values hold exactly the names: no more,
// no fewer.
func sameNames(values map[string]string, names []string) bool {
	if len(values) != len(names) {
		return false
	}
	for _, n := range names {
		if _, ok := values[n]; !ok {
			return false
		}
	}
	return true
}
