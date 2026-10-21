// A card's weight (design yardr-ekmx 3.1): the yard's own mark on an ask
// whose approval widens what reaches outside the yard, whose word or
// credential counts as a person's, or what runs inside the yard. The yard
// weighs a card in the write that stores it, from the card it has just
// written and its own records, exactly as it writes a run ask's Does:
// never from the asker's words. The approval weighs it again in its own
// write, and a weight that differs from the one the card showed is stale:
// nothing runs, so no weighty card is approved from a card that did not
// show its weight. A card that narrows (a revoke, a lower limit, user rm,
// timer rm) is never weighty.
package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"yardr.dev/yardr/internal/source"
)

// The classes of weight. Each is a widening; a card carries several where
// its approval widens in several ways, and none where it is routine.
// WeightReads is the sixth, beside the design's five: a replaced
// declaration whose new or changed read calls reach every standing grant
// of read (Widened) lets those holders read what they could not, which
// brings no program and changes nothing outside.
const (
	WeightCredential = "credential"
	WeightAsUser     = "as-user"
	WeightOutside    = "outside"
	WeightCode       = "code"
	WeightTrust      = "trust"
	WeightReads      = "reads"
)

// weightSays is each class's line, as the card prints it before the
// yard's particulars.
var weightSays = map[string]string{
	WeightCredential: "gives a credential or an address to the yard",
	WeightAsUser:     "lets calls or words count as a person's",
	WeightOutside:    "lets the yard change something outside it",
	WeightCode:       "brings a program the yard will run",
	WeightTrust:      "widens who the yard listens to",
	WeightReads:      "widens what a standing grant of read reaches",
}

// AskWeight is one class of a card's weight: the class, and the yard's
// line for it with the particulars of this card, as the card prints it
// after the class.
type AskWeight struct {
	Class string `json:"class"`
	Line  string `json:"line"`
}

// WeightMark starts a card's weight lines where they are shown (Marked):
// its words end where the line begins, so nothing its asker typed stands
// for a weight the yard gave. Every renderer of cards keeps the mark out
// of the asker's own words (WithoutWeightMark), so a line starting with
// it is the yard's, and any other "WEIGHTY · ..." is words.
const WeightMark = "WEIGHTY · "

// String is the weight as a terminal card prints it, after the mark.
func (a AskWeight) String() string { return a.Class + ": " + a.Line }

// Marked is the weight as a terminal prints it on a line of its own: the
// card's line, and the gate's before a weighty approval, so that both say
// it in the same words.
func (a AskWeight) Marked() string { return WeightMark + OneLine(a.String()) }

// WithoutWeightMark is s without the mark that starts a card's weight
// line: what an asker's own words are shown as, so an asker cannot forge
// the line by typing the mark (WeightMark). Only the mark at a line's
// first non-whitespace position goes, the only position the line is
// recognised by; a mark further in is an asker's own words and stays
// byte-identical. Residual: an asker's mark at a line's first non-space
// position still drops. That is the safe direction (it is exactly the
// forgery shape), vanishingly rare, and the stored bytes are untouched,
// so any future escape re-renders them.
func WithoutWeightMark(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		rest := trimmed
		for strings.HasPrefix(rest, WeightMark) {
			rest = rest[len(WeightMark):]
		}
		lines[i] = line[:len(line)-len(trimmed)] + rest
	}
	return strings.Join(lines, "\n")
}

// weighs is the class's weight with the yard's particulars after its line.
func weighs(class, particulars string) AskWeight {
	return AskWeight{Class: class, Line: weightSays[class] + ": " + oneLine(particulars)}
}

// WeightWords names a card's weight in a refusal, the yard's and a
// terminal's: routine, or weighty with its classes.
func WeightWords(ws []AskWeight) string {
	if len(ws) == 0 {
		return "routine"
	}
	classes := make([]string, len(ws))
	for i, w := range ws {
		classes[i] = w.Class
	}
	return "weighty (" + strings.Join(classes, ", ") + ")"
}

// weightStale says why the ask seq is approved no more when the weight the
// yard gives it now (now) is not the one its card showed (was), and nil
// when they are one. An ask written before cards were weighed carries
// none: it is stale wherever it weighs as weighty now.
func weightStale(seq uint64, was, now []AskWeight) error {
	if slices.Equal(was, now) {
		return nil
	}
	if len(was) == 0 {
		return fmt.Errorf("ask %d was written without its weight, and the yard weighs it %s now: a weighty card is approved only from a card that showed its weight. %s", seq, WeightWords(now), nothingRan)
	}
	return fmt.Errorf("ask %d was weighed %s when it was written, and the yard weighs it %s now: the card is from before that changed. %s", seq, WeightWords(was), WeightWords(now), nothingRan)
}

// weighedRefusal says why the approval of the ask seq is refused when the
// yard weighs its card as weight and the client did not say its person saw
// that weight (weighed, design yardr-ekmx 3.3): nil for a routine card,
// which asks for no word of the client's, and for an approval that says it.
// It checks what a client says and proves nothing: any holder of the
// operator's token or shell can send weighed. What it buys is that a client
// which has never heard of weight (an old window, a saved request, a script
// written before this) cannot approve a weighty card by accident, and that
// a client which does send it was written knowing it promises a gate. It is
// no stale refusal: the card is the one that was shown, so the ask stays
// open to be approved where its weight is shown.
func weighedRefusal(seq uint64, weight []AskWeight, weighed bool) error {
	if len(weight) == 0 || weighed {
		return nil
	}
	return fmt.Errorf("ask %d is %s: approve it where its weight is shown; a client sends \"weighed\": true once its person has passed its gate", seq, WeightWords(weight))
}

// weighAsk weighs the card the ask a holds, as the yard wrote it: a secret,
// a run, a setup or a mapping ask by its own rules, and any other ask as
// routine.
func (w *wtx) weighAsk(a Ask) ([]AskWeight, error) {
	switch {
	case a.Secret != nil:
		return secretWeight(*a.Secret), nil
	case a.Run != nil:
		c, _, err := parseRow(a.Run.Argv)
		if err != nil {
			return nil, err
		}
		return w.weighRun(c, *a.Run)
	case a.Setup != nil:
		return w.setupWeight(*a.Setup)
	case a.Identity != nil:
		return mappingWeight(*a.Identity), nil
	}
	return nil, nil
}

// weighRun weighs the card of the row c: nil for a routine row.
func (w *wtx) weighRun(c *runCommand, card AskRun) ([]AskWeight, error) {
	if c.weigh == nil {
		return nil, nil
	}
	return c.weigh(w, card)
}

// storedWeight is the weight the ask seq among notes was stored with.
func storedWeight(notes []Note, seq uint64) []AskWeight {
	for _, n := range notes {
		if n.Seq == seq && n.Kind == NoteAsk {
			if a, err := ParseAsk(n); err == nil {
				return a.Weight
			}
		}
	}
	return nil
}

// secretWeight is a secret ask's weight: every form gives the yard a
// value, and one for a user's own credential is read when a call is made
// as them.
func secretWeight(sec AskSecret) []AskWeight {
	out := []AskWeight{weighs(WeightCredential, strings.TrimPrefix(sec.Line(), "a secret: "))}
	if sec.As != "" {
		out = append(out, weighs(WeightAsUser, fmt.Sprintf("a call of the source %s made --as %s reads it as %s's own", sec.Source, sec.As, sec.As)))
	}
	return out
}

// mappingWeight is a mapping ask's weight: its picks write a user's
// identity, so the sender's messages then count as that user's.
func mappingWeight(id AskIdentity) []AskWeight {
	return []AskWeight{weighs(WeightAsUser, fmt.Sprintf("a pick says which user %s in %s is, and a delivery from them is then heard as that user's", id.Sender, id.Source))}
}

// carriedWeight is the weight of the sources a card carries (source add,
// pack apply, a setup): code for each command file that is new or differs
// from the copy that stands (AskSource.Inventory), and reads where a
// replaced declaration widens a standing grant of read (Widened).
func (w *wtx) carriedWeight(carried []AskSource) ([]AskWeight, error) {
	var files, reads []string
	for _, c := range carried {
		standing := map[string]string{}
		for _, e := range c.Inventory {
			if !e.Symlink {
				standing[e.Path] = e.SHA256
			}
		}
		for _, f := range c.Files {
			switch has, ok := standing[f.Program]; {
			case !ok:
				files = append(files, fmt.Sprintf("%s of %s (new)", f.Program, c.Name))
			case has != f.SHA256:
				files = append(files, fmt.Sprintf("%s of %s (changed)", f.Program, c.Name))
			}
		}
		_, was, err := sourceIn(w.Tx, c.Name)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		d, err := source.Parse("source "+c.Name, []byte(c.Text))
		if err != nil {
			return nil, err
		}
		grants, err := grantsOf(w.Tx, c.Name)
		if err != nil {
			return nil, err
		}
		if calls, who := Widened(was, d, grants); len(calls) > 0 {
			holders := make([]string, len(who))
			for i, g := range who {
				holders[i] = g.Holder()
			}
			reads = append(reads, fmt.Sprintf("%s of %s for %s", andList(calls), c.Name, andList(holders)))
		}
	}
	var out []AskWeight
	if len(files) > 0 {
		out = append(out, weighs(WeightCode, "the command file "+andList(files)))
	}
	if len(reads) > 0 {
		out = append(out, weighs(WeightReads, "a grant of read widens to "+strings.Join(reads, "; ")))
	}
	return out, nil
}

// updatesOf are the calls of calls that the declaration d says change
// something in the service (source.Update).
func updatesOf(d *source.Declaration, calls []string) []string {
	var out []string
	for _, what := range calls {
		if c, ok := d.Call(what); ok && c.Kind == source.Update {
			out = append(out, what)
		}
	}
	return out
}

// anUpdate is how a weight's line names update calls after their names.
func anUpdate(calls []string) string {
	if len(calls) == 1 {
		return "an update"
	}
	return "updates"
}

// setupWeight is a setup ask's weight: it takes the account, so it gives
// a credential; outside where a preset grants an update call, whichever
// preset the pick takes; and what its carried sources bring.
func (w *wtx) setupWeight(s AskSetup) ([]AskWeight, error) {
	out := []AskWeight{weighs(WeightCredential, fmt.Sprintf("the account %s of pack %s", strings.Join(s.Account, " and "), s.Pack))}
	decls := map[string]*source.Declaration{}
	for _, c := range s.Carried {
		d, err := source.Parse("pack source "+c.Name, []byte(c.Text))
		if err != nil {
			return nil, err
		}
		decls[c.Name] = d
	}
	var outside []string
	for _, p := range s.Presets {
		for _, g := range p.Grants {
			d := decls[g.Source]
			if d == nil {
				continue
			}
			if ups := updatesOf(d, g.Calls); len(ups) > 0 {
				outside = append(outside, fmt.Sprintf("the preset %s lets %s call %s of %s (%s)", p.Name, granteesWord(s.To), andList(ups), g.Source, anUpdate(ups)))
			}
		}
	}
	if len(outside) > 0 {
		out = append(out, weighs(WeightOutside, strings.Join(outside, "; ")))
	}
	carried, err := w.carriedWeight(s.Carried)
	if err != nil {
		return nil, err
	}
	// A setup brings the pack's command files as part of the whole setup,
	// even where their bytes match the copies that stand already. Source
	// add and pack apply weigh only files that are new or changed.
	hasFiles := false
	for _, c := range s.Carried {
		if len(c.Files) > 0 {
			hasFiles = true
			break
		}
	}
	if hasFiles && !slices.ContainsFunc(carried, func(w AskWeight) bool { return w.Class == WeightCode }) {
		var files []string
		for _, c := range s.Carried {
			for _, f := range c.Files {
				files = append(files, fmt.Sprintf("%s of %s", f.Program, c.Name))
			}
		}
		carried = append(carried, weighs(WeightCode, "the command file "+andList(files)))
	}
	return append(out, carried...), nil
}

// peerAllowWeight weighs a peer allow: trust where it adds a kind the link
// carries either way, or raises the mail the link takes. Stopping a kind
// is routine.
func peerAllowWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	al := peerAllowOf(run.Argv)
	p, err := loadPeer(w.Tx, al.name)
	if err != nil {
		return nil, err
	}
	var says []string
	if has := p.MailsAnHour(); al.hasLimit && al.limit > has {
		says = append(says, fmt.Sprintf("the link with %s takes %d mails and asks an hour from it, %d now", al.name, al.limit, has))
	}
	added := func(have, add, drop []string) []string {
		return slices.DeleteFunc(changeList(have, add, drop), func(k string) bool { return slices.Contains(have, k) })
	}
	if kinds := added(p.Send, al.send, al.nosend); len(kinds) > 0 {
		says = append(says, fmt.Sprintf("the link with %s carries %s to it", al.name, andList(kinds)))
	}
	if kinds := added(p.Receive, al.receive, al.noreceive); len(kinds) > 0 {
		says = append(says, fmt.Sprintf("the link with %s takes %s from it", al.name, andList(kinds)))
	}
	if len(says) == 0 {
		return nil, nil
	}
	return []AskWeight{weighs(WeightTrust, strings.Join(says, "; "))}, nil
}

// sourceLimitWeight weighs a source set of a limit: outside where it
// raises the updates a caller can make in an hour, which is the lower of
// the two caps, since each update is one of the calls too. Raising the
// calls past the updates' cap lets no more updates through.
func sourceLimitWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	name, key, n := sourceLimitOf(run.Argv)
	src, _, err := sourceIn(w.Tx, name)
	if err != nil {
		return nil, err
	}
	calls, updates := src.CallLimit(), src.UpdateLimit()
	before := min(calls, updates)
	if key == SourceUpdatesPerHour {
		updates = n
	} else {
		calls = n
	}
	if after := min(calls, updates); after > before {
		return []AskWeight{weighs(WeightOutside, fmt.Sprintf("each caller may make %d updates an hour of the source %s, %d now", after, name, before))}, nil
	}
	return nil, nil
}

// grantWeight weighs a source grant: outside where a call it grants, or
// one of its preset's, is an update; as-user where the calls are made as
// a user. A plain grant of read calls is routine.
func grantWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	name, what, preset, to, as := grantOf(run.Argv)
	d, err := declared(w.Tx, name)
	if err != nil {
		return nil, err
	}
	calls := []string{what}
	if preset != "" {
		pr, err := w.presetOf(name, preset)
		if err != nil {
			return nil, err
		}
		calls = pr.Calls
	}
	holders := make([]string, len(to))
	for i, t := range to {
		holders[i] = t.String()
	}
	who := andList(holders)
	var out []AskWeight
	if ups := updatesOf(d, calls); len(ups) > 0 {
		out = append(out, weighs(WeightOutside, fmt.Sprintf("%s may call %s of %s (%s)", who, andList(ups), name, anUpdate(ups))))
	}
	if as != "" {
		out = append(out, weighs(WeightAsUser, fmt.Sprintf("%s calls %s as %s, with %s's credential", who, name, as, as)))
	}
	return out, nil
}

// carriedRunWeight weighs a row that carries sources (source add, pack
// apply) by what it carries.
func carriedRunWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	return w.carriedWeight(run.Carried)
}

// hookAddWeight weighs a hook add: an endpoint that takes outside input is
// always a widening.
func hookAddWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	name, src := hookAddOf(run.Argv)
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("POST /hooks/%s takes deliveries from outside the yard for the source %s", name, src))}, nil
}

// userKeyWeight weighs a user key: it gives the yard a user's address.
func userKeyWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	user, src := userKeyOf(run.Argv)
	return []AskWeight{weighs(WeightCredential, fmt.Sprintf("%s's own address in the source %s", user, src))}, nil
}

// userAddWeight weighs a user add: the yard takes a new person's word.
func userAddWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("a new user, %s, whose answers and approvals the yard takes", run.Argv[2]))}, nil
}

// crewGrantWeight weighs a crew set of grants: trust where its peers=
// adds a peer whose crew the member may mail. What it lets the member do
// in a source, and what it takes away, are routine.
func crewGrantWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	c := crewGrantOf(run.Argv)
	var have CrewMember
	if ok, err := get(w.Tx, bCrew, c.name, &have); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("crew member %s: %w", c.name, ErrNotFound)
	}
	want, err := crewGrantWant(have, c)
	if err != nil {
		return nil, err
	}
	peers := slices.DeleteFunc(slices.Clone(want.MayMail()), func(p string) bool { return slices.Contains(have.MayMail(), p) })
	if len(peers) == 0 {
		return nil, nil
	}
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("crew member %s may mail crew members of %s", c.name, andList(peers)))}, nil
}

// userIdentityWeight weighs a user identity: what the id or key says in
// the source then counts as the user's.
func userIdentityWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	user, src, id, key := userIdentityOf(run.Argv)
	who := identitySays(id, foldIdentityKey(w.Tx, src, key))
	return []AskWeight{weighs(WeightAsUser, fmt.Sprintf("%s in %s counts as the user %s", who, src, user))}, nil
}
