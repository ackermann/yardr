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
	"maps"
	"slices"
	"strings"
	"time"
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

// WithoutWeightWords is a card's weight as a device that is no operator's
// is told it where the command's words are held (RunArgvHeld): each class
// with the yard's line for it, and nothing of what this card does, since
// those particulars quote the held words and the plan behind them.
func WithoutWeightWords(ws []AskWeight) []AskWeight {
	out := make([]AskWeight, len(ws))
	for i, w := range ws {
		out[i] = AskWeight{Class: w.Class, Line: weightSays[w.Class]}
	}
	return out
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
// byte-identical. Whitespace between two marks saves neither: the second
// would otherwise stand one space over and still read as the yard's.
// Residual: an asker's mark at a line's first non-space position still
// drops. That is the safe direction (it is exactly the forgery shape),
// vanishingly rare, and the stored bytes are untouched, so any future
// escape re-renders them.
func WithoutWeightMark(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		rest := trimmed
		for strings.HasPrefix(rest, WeightMark) {
			rest = rest[len(WeightMark):]
			if next := strings.TrimLeftFunc(rest, unicode.IsSpace); strings.HasPrefix(next, WeightMark) {
				rest = next
			}
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

// StrongWeight reports whether the card's stored weight makes it a strong
// card (design yardr-2nb1 5.2): trust or code, one class enough. Identity
// cards (as-user) are not strong. The web reads the same predicate before
// it starts a strong card's sign-in, never its own.
func StrongWeight(weight []AskWeight) bool {
	return slices.ContainsFunc(weight, func(w AskWeight) bool {
		return w.Class == WeightTrust || w.Class == WeightCode
	})
}

// StrongWords names a strong card's weight in a refusal: strong with the
// trust and code classes it carries. The web names it the same way before
// it starts a strong card's sign-in.
func StrongWords(ws []AskWeight) string {
	var classes []string
	for _, w := range ws {
		if w.Class == WeightTrust || w.Class == WeightCode {
			classes = append(classes, w.Class)
		}
	}
	return "strong (" + strings.Join(classes, ", ") + ")"
}

// gateRefusal is a run or setup approval's gate behind the stale check:
// the weighed refusal first, the fresh witness's second (weighedRefusal,
// freshRefusal). No door is refused for being the one it is: a paired
// device of the operator approves a strong card as it approves any card,
// and the card's own gate is that its person saw its weight. The
// approvals of a strong card differ in how they are witnessed
// (witnessStrong), not in whether they are taken.
func (w *wtx) gateRefusal(seq uint64, weight, stored []AskWeight, actor string, weighed bool) error {
	if err := weighedRefusal(seq, weight, weighed); err != nil {
		return err
	}
	return w.freshRefusal(seq, stored, actor)
}

// freshSkew is how far ahead of the yard's clock a fresh sign-in's time
// may be: the gateway verifier's minute of clock skew (internal/web
// jwtLeeway), so a gateway clock running ahead does not refuse every
// sign-in, and past it no time counts.
const freshSkew = time.Minute

// freshRefusal says why the approval of the ask seq is refused when it
// carries a fresh sign-in that does not witness it: nil for a routine
// card, which asks for no witness, and for an approval that carries none,
// which the socket witnesses as a terminal's (witnessStrong). A strong
// card approved in the hosted web carries the request's: one made as
// another user, one with no authentication time, or one outside the
// gateway's privileged-session window refuses, and the ask stays open.
// Whether the sign-in answered a strong card's challenge is the web's to
// have checked before it stated the witness; the store takes the
// statement and checks whom and when.
func (w *wtx) freshRefusal(seq uint64, stored []AskWeight, actor string) error {
	if !w.hasFresh || !StrongWeight(stored) {
		return nil
	}
	switch fw := w.fresh; {
	case fw.User != actor:
		return fmt.Errorf("ask %d is %s: this approval's fresh sign-in was made as %s, not as %s. Nothing ran, and the ask stays open", seq, StrongWords(stored), fw.User, actor)
	case fw.AuthTime.IsZero():
		return fmt.Errorf("ask %d is %s: this approval carries a fresh sign-in with no authentication time, and a strong card is approved in the hosted web only under one. Nothing ran, and the ask stays open", seq, StrongWords(stored))
	default:
		window, err := ParseSpan(w.settingValue(WebJWTPrivilegedSessionMaxAgeSetting))
		if err != nil || window <= 0 || fw.AuthTime.Before(now().Add(-window)) || fw.AuthTime.After(now().Add(freshSkew)) {
			return fmt.Errorf("ask %d is %s: this approval's fresh sign-in is outside the gateway's window, and a strong card is approved in the hosted web only under one inside it. Nothing ran, and the ask stays open", seq, StrongWords(stored))
		}
		return nil
	}
}

// weighAsk weighs the card the ask a holds, as the yard wrote it: a secret,
// a run, a setup, a depot-push or a mapping ask by its own rules, and
// any other ask as routine.
func (w *wtx) weighAsk(a Ask) ([]AskWeight, error) {
	switch {
	case a.Secret != nil:
		return secretWeight(*a.Secret), nil
	case a.DepotPush != nil:
		return depotPushWeight(*a.DepotPush), nil
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
	// A login's input goes to the login that waits, and no call reads it.
	if sec.As != "" && sec.Login == "" {
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

// programsOf are the update calls among calls that send a file, which a
// card of the call weighs as code (sourceCallWeight). A grant of one is
// that card's approval given for good: its holder makes the call with no
// card, so the grant weighs code as the card does, and is as strong a card
// as the one it stands in for.
func programsOf(d *source.Declaration, calls []string) []string {
	var out []string
	for _, name := range updatesOf(d, calls) {
		c, _ := d.Call(name)
		if slices.ContainsFunc(c.Params, func(p source.Param) bool { return p.Type == "file" }) {
			out = append(out, name)
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
	var outside, programs []string
	for _, p := range s.Presets {
		for _, g := range p.Grants {
			d := decls[g.Source]
			if d == nil {
				continue
			}
			if ups := updatesOf(d, g.Calls); len(ups) > 0 {
				outside = append(outside, fmt.Sprintf("the preset %s lets %s call %s of %s (%s)", p.Name, granteesWord(s.To), andList(ups), g.Source, anUpdate(ups)))
			}
			if ups := programsOf(d, g.Calls); len(ups) > 0 {
				programs = append(programs, fmt.Sprintf("the preset %s lets %s send a file to %s through %s with no card, a program it will run", p.Name, granteesWord(s.To), g.Source, andList(ups)))
			}
		}
	}
	if len(outside) > 0 {
		out = append(out, weighs(WeightOutside, strings.Join(outside, "; ")))
	}
	if len(programs) > 0 {
		out = append(out, weighs(WeightCode, strings.Join(programs, "; ")))
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
// carries either way, exposes someone new, trusts a name new or as another
// user, sends asks on new or to another user, or raises the mail the link
// takes. Stopping a kind, and narrowing trust, is routine.
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
	if names := added(p.Expose, al.expose, nil); len(names) > 0 {
		says = append(says, fmt.Sprintf("the link with %s exposes %s to it", al.name, andList(names)))
	}
	trust, err := runAllowTrust(al.name, "trust", al.trust)
	if err != nil {
		return nil, err
	}
	reach, err := runAllowTrust(al.name, "reach", al.reach)
	if err != nil {
		return nil, err
	}
	var trusted, reached []string
	for _, name := range slices.Sorted(maps.Keys(trust)) {
		if p.Trust[name] != trust[name] {
			trusted = append(trusted, name+"@"+al.name+" as "+trust[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(reach)) {
		if p.Reach[name] != reach[name] {
			reached = append(reached, name+"@"+al.name+" for "+reach[name])
		}
	}
	if len(trusted) > 0 {
		says = append(says, fmt.Sprintf("the link with %s trusts %s", al.name, andList(trusted)))
	}
	if len(reached) > 0 {
		says = append(says, fmt.Sprintf("the link with %s sends asks to %s", al.name, andList(reached)))
	}
	if len(says) == 0 {
		return nil, nil
	}
	return []AskWeight{weighs(WeightTrust, strings.Join(says, "; "))}, nil
}

// peerAddWeight weighs a peer add: linking a yard is always trust, since
// its calls and mail then cross as the invite says.
func peerAddWeight(_ *wtx, run AskRun) ([]AskWeight, error) {
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("a new peer, %s, whose calls and mail then cross as the invite says", run.Argv[2]))}, nil
}

// peerSetWeight weighs a peer set: moving where a peer is called is always
// trust, since the yard's calls and mail for it then go to the new address,
// whoever answers there.
func peerSetWeight(_ *wtx, run AskRun) ([]AskWeight, error) {
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("the calls and mail for the peer %s then go to %s", run.Argv[2], run.Argv[4]))}, nil
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
	if ups := programsOf(d, calls); len(ups) > 0 {
		out = append(out, weighs(WeightCode, fmt.Sprintf("%s may send a file to %s through %s with no card, a program it will run", who, name, andList(ups))))
	}
	if as != "" {
		out = append(out, weighs(WeightAsUser, fmt.Sprintf("%s calls %s as %s, with %s's credential", who, name, as, as)))
	}
	return out, nil
}

// carriedRunWeight weighs a row that carries sources by what it carries.
func carriedRunWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	return w.carriedWeight(run.Carried)
}

// packApplyWeight weighs a pack apply by what it carries and, where it
// names a depot, by what its mappings give, in the classes the grant and
// setup cards use: a call of an update is outside, and one that sends a
// file is code; opening a thread reaches outside as an update does; a bind
// says which agents the yard runs and with which permissions, so it is
// code; a member is a person whose word the group takes, and no members
// is anyone's, so trust. A first
// install is code where its plan makes what the yard will run: files and
// the groups and crew that work them. A credential alone weighs nothing
// more, since a depot pack's declaration brings no value.
func packApplyWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	out, err := w.carriedWeight(run.Carried)
	if err != nil {
		return nil, err
	}
	_, depot, m, err := packApplyWords(run.Argv[2:])
	if err != nil || depot == "" || run.Pack == nil {
		return out, err
	}
	declaredIn := func(name string) (*source.Declaration, error) {
		for _, c := range run.Carried {
			if c.Name == name {
				return source.Parse("pack source "+c.Name, []byte(c.Text))
			}
		}
		d, err := declared(w.Tx, name)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return d, err
	}
	var outside, code, trust []string
	first, err := w.packFirstInstall(run.Pack.Name, depot)
	if err != nil {
		return nil, err
	}
	if first && run.Pack.Plan != nil {
		var made []string
		for _, o := range run.Pack.Plan.Objects {
			if (o.Action == "create" || o.Action == "change") && slices.Contains([]string{"file", "group", "crew"}, o.Kind) {
				made = append(made, o.Kind+" "+o.Name)
			}
		}
		if len(made) > 0 {
			code = append(code, fmt.Sprintf("the first install of pack %s into depot %s makes %s", run.Pack.Name, depot, andList(made)))
		}
	}
	for _, g := range m.callGrants() {
		d, err := declaredIn(g.Source)
		if err != nil {
			return nil, err
		}
		if d == nil {
			continue
		}
		if ups := updatesOf(d, g.Calls); len(ups) > 0 {
			outside = append(outside, fmt.Sprintf("%s may call %s of %s (%s)", g.Who, andList(ups), g.Source, anUpdate(ups)))
		}
		if ups := programsOf(d, g.Calls); len(ups) > 0 {
			code = append(code, fmt.Sprintf("%s may send a file to %s through %s with no card, a program it will run", g.Who, g.Source, andList(ups)))
		}
	}
	for _, g := range m.threadGrants() {
		outside = append(outside, fmt.Sprintf("crew member %s may open threads in %s", g.Crew, andList(g.Sources)))
	}
	for _, kv := range m.Binds {
		code = append(code, "the agents of "+kv)
	}
	for _, kv := range m.Members {
		// No names is the widest answer there is: anyone, as the flag says.
		group, names, _ := strings.Cut(kv, "=")
		who := "anyone"
		if names != "" {
			who = andList(strings.Split(names, ","))
		}
		trust = append(trust, fmt.Sprintf("the group %s takes the word of %s", group, who))
	}
	for _, add := range []struct {
		class string
		says  []string
	}{{WeightOutside, outside}, {WeightCode, code}, {WeightTrust, trust}} {
		if len(add.says) == 0 {
			continue
		}
		says := strings.Join(add.says, "; ")
		if i := slices.IndexFunc(out, func(a AskWeight) bool { return a.Class == add.class }); i >= 0 {
			out[i].Line += "; " + oneLine(says)
			continue
		}
		out = append(out, weighs(add.class, says))
	}
	return out, nil
}

// sourceAddWeight weighs a source add by what it carries, and a source it
// replaces as code whatever its files: the source keeps the credential it
// has and every grant of it, so a declaration with no file to change can
// still move where that credential is sent and which programs the yard
// runs for it. A source that is new weighs as carriedRunWeight has it.
func sourceAddWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	out, err := w.carriedWeight(run.Carried)
	if err != nil || slices.ContainsFunc(out, func(a AskWeight) bool { return a.Class == WeightCode }) {
		return out, err
	}
	for _, c := range run.Carried {
		if w.Bucket(bSources).Get([]byte(c.Name)) != nil {
			return slices.Insert(out, 0, weighs(WeightCode, fmt.Sprintf("the declaration of %s replaces the one that stands, and says where the credential it keeps is sent and which programs the yard runs", c.Name))), nil
		}
	}
	return out, nil
}

// hookAddWeight weighs a hook add: an endpoint that takes outside input is
// always a widening.
func hookAddWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	name, src := hookAddOf(run.Argv)
	return []AskWeight{weighs(WeightTrust, fmt.Sprintf("POST /hooks/%s takes deliveries from outside the yard for the source %s", name, src))}, nil
}

// depotTestWeight weighs a depot test: the command the yard will run as
// the depot's test, wherever a script group's command gets it and a
// builder's prime prints it.
func depotTestWeight(w *wtx, run AskRun) ([]AskWeight, error) {
	return []AskWeight{weighs(WeightCode, fmt.Sprintf("the test command of the depot %s: a bead's prime prints it, and a script group's command gets it as YARDR_TEST", run.Argv[2]))}, nil
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
