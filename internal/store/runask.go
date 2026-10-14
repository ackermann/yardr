// A run ask is an ask whose answer is no words either: it proposes one
// yardr command that is the operator's to give, and the operator's approval
// (ApproveAsk) runs it. No shell line is ever run: the command is one of a
// fixed list (runList), parsed and checked when the ask is written, and run
// through the store function the command itself calls, under that
// function's own check, in the write that records the approval.
//
// The ask holds the command as the yard parsed it and the yard's own
// sentences for what it changes, made from the yard's records and never
// from the asker's words. An approval is held to the yard the card was
// written from: when what the sentences say, or what the command names, is
// no longer what the yard holds, nothing runs and the ask is asked again.
// Nothing else an ask has approves: a pick and words are refused on a run
// ask (answerable), and so is an answer that arrives saying what ran
// (answerShape).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxRunContext is how many lines of context a run ask takes: more would
// push the yard's part of the card out of sight.
const MaxRunContext = 5

// runContextLines is how many lines the context holds. A context line is
// one line, and the limit counts lines, not entries.
func runContextLines(context []string) int {
	n := 0
	for _, line := range context {
		n += strings.Count(line, "\n") + 1
	}
	return n
}

// AskRun is the command a run ask proposes. Its writer sends Argv alone,
// the command as it is typed in a terminal, in one string or in words; the
// rest is the yard's, written in the write that stores the ask (askedRun)
// whatever the writer sent for it.
type AskRun struct {
	// Argv is the command as the yard parsed it, without the leading yardr
	// and in an order of the yard's own, so that two ways of typing one
	// command are stored as one.
	Argv []string `json:"argv"`
	// Does are the yard's sentences for what the command changes and what
	// is there now, and Back the command that takes the change back. They
	// are kept on the ask because the views render an ask from its note
	// alone.
	Does []string `json:"does,omitempty"`
	Back string   `json:"back,omitempty"`
	// Seen holds, for each thing the command names, what the yard held of
	// it when it wrote the sentences, whole: an approval is held to it,
	// since a sentence names a thing and cannot say all of it. For a peer
	// it is the link's key; for a source, a group, a crew member and a user
	// a SHA-256 over the part of it the command's effect rests on (seen).
	Seen map[string]string `json:"seen,omitempty"`
	// BackBy is who may run Back, in the yard's words, where that is not
	// anyone: to narrow what the yard holds is mostly anyone's, and a
	// source's limits and a user's subscriptions are not.
	BackBy string `json:"back_by,omitempty"`
}

// TakenBack is the card's line for the command that takes the change back,
// with who may run it.
func (r AskRun) TakenBack() string { return or(r.BackBy, "anyone") + " can take it back: " + r.Back }

// Command is the command whole, as it is typed in a terminal. No view
// shortens it.
func (r AskRun) Command() string { return runCommandLine(r.Argv) }

// AskRan is what an approved run ask ran: the command, which is the one the
// ask stored, and what it prints at a terminal, made by the server.
type AskRan struct {
	Argv   []string `json:"argv"`
	Result string   `json:"result"`
}

// Command is the command that ran, whole.
func (r AskRan) Command() string { return runCommandLine(r.Argv) }

func runCommandLine(argv []string) string { return "yardr " + strings.Join(argv, " ") }

// RunAsked is the command a run ask proposes, as a client shows it: the
// command whole, the yard's sentences for what it changes, and the command
// that takes it back. All of it is the yard's, and none of it the asker's.
type RunAsked struct {
	Argv    []string `json:"argv"`
	Command string   `json:"command"`
	Does    []string `json:"does"`
	Back    string   `json:"back"`
	// BackBy is who may run Back, in words that stand before "can take it
	// back"; absent where that is anyone.
	BackBy string `json:"back_by,omitempty"`
}

// proposed is the command of the ask a as a client shows it, nil for an
// ask that proposes none.
func (a Ask) proposed() *RunAsked {
	if a.Run == nil {
		return nil
	}
	return &RunAsked{Argv: a.Run.Argv, Command: a.Run.Command(), Does: a.Run.Does, Back: a.Run.Back, BackBy: a.Run.BackBy}
}

// AskApproved is what ApproveAsk did: the ask and who asked it, the command
// as it ran with the yard's sentences for it, and what it printed.
type AskApproved struct {
	Bead   string   `json:"bead"`
	Ask    uint64   `json:"ask"`
	Asker  string   `json:"asker"`
	Argv   []string `json:"argv"`
	Does   []string `json:"does"`
	Result string   `json:"result"`
}

// approvedOn is the ask a change was approved on, as the change's own event
// carries it, so that the events tie the change to what proposed it.
type approvedOn struct {
	Bead string `json:"bead"`
	Ask  uint64 `json:"ask"`
}

// runCommand is one command of the list.
type runCommand struct {
	// words begin the command, and use is how a refusal names it.
	words []string
	use   string
	// parse reads the words after words, in whatever order they were typed,
	// into the command's one stored form. It reads no record of the yard.
	parse func(rest []string) ([]string, error)
	// card writes what the yard says of the parsed command from its records
	// as they are now: the sentences, the command that takes it back, and
	// what it saw of each thing the command names. asking is the write that
	// stores the ask, which refuses a command that would change nothing; an
	// approval reads the card again without that, and says in its own words
	// what changed (stale).
	card func(w *wtx, argv []string, asking bool) (AskRun, error)
	// stale says why an approval of the ask seq is refused, when the card it
	// was written with (was) is not the one the yard would write now.
	stale func(w *wtx, seq uint64, was, now AskRun) error
	// run makes the change as actor through the command's own store
	// function, and answers what the command prints at a terminal.
	run func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error)
	// whose is the user whose own the command is, where the store function
	// takes it from that user as it does from the operator: the ask is then
	// for one of the two, and the one asked approves. nil for a command
	// that is the operator's alone.
	whose func(argv []string) string
	// commas says a word of the command is a list with commas between, as
	// the parts of tell= are. No other command takes the character.
	commas bool
}

// runList are the commands a run ask may name. A command is here only when
// it is one write of the store through a function that exists, every word
// of it is a name the yard holds, a number or a flag, it starts no process
// and sends nothing, it adds nobody who may speak, answer or act as a
// person of the yard, and anyone can take it back with one command (design
// yardr-x3g8, section 3.4). A test holds each row to the command of
// internal/cli it names.
var runList = []runCommand{{
	words: []string{"peer", "allow"},
	use:   "yardr peer allow <peer> --mail-limit <n>",
	parse: parseMailLimit,
	card:  (*wtx).mailLimitCard,
	stale: (*wtx).mailLimitStale,
	run: func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
		name, n := mailLimitOf(argv)
		p, err := w.allowPeer(ctx, name, PeerAllow{MailLimit: n}, actor, by)
		if err != nil {
			return "", err
		}
		return "peer " + p.Name + ": " + p.MailTakes(), nil
	},
}, {
	words: []string{"source", "set"},
	use:   "yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>",
	parse: parseSourceLimit,
	card:  (*wtx).sourceLimitCard,
	stale: (*wtx).sourceLimitStale,
	run: func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
		name, key, n := sourceLimitOf(argv)
		src, err := w.setSource(ctx, name, key, strconv.Itoa(n), n, actor, by)
		if err != nil {
			return "", err
		}
		return "source " + src.Name + ": " + src.LimitsLine(), nil
	},
}, {
	words: []string{"source", "grant"},
	use:   "yardr source grant <source> read|<call> --group <group>|--crew <crew member>",
	parse: parseGrant,
	card:  (*wtx).grantCard,
	stale: (*wtx).grantStale,
	run:   runGrant,
}, {
	words: []string{"subscribe"},
	use:   "yardr subscribe <user> yardr [--done] [--where <key>=<value>]",
	parse: parseSubscribe,
	card:  (*wtx).subscribeCard,
	stale: (*wtx).subscribeStale,
	run:   runSubscribe,
	whose: func(argv []string) string { return argv[1] },
}, {
	words:  []string{"user", "set"},
	use:    "yardr user set <user> tell=<parts>",
	parse:  parseUserTell,
	card:   (*wtx).userTellCard,
	stale:  (*wtx).userTellStale,
	run:    runUserTell,
	whose:  func(argv []string) string { return argv[2] },
	commas: true,
}}

// RunCommands are the commands of the list, each as a refusal and the help
// of yardr bead ask name it.
func RunCommands() []string {
	var uses []string
	for _, c := range runList {
		uses = append(uses, c.use)
	}
	return uses
}

// parseRun reads the command a run ask names, as its writer sent it, and
// answers its row of the list with the command in its stored form. The
// line is split at white space and nowhere else, and is read by nothing
// after this: a character that no name, number or flag of the list has is
// refused, so there is no quoting, and no second reading of the line.
func parseRun(sent []string) (*runCommand, []string, error) {
	line := strings.Join(sent, " ")
	none := func(c rune) error {
		return fmt.Errorf("--run takes one yardr command of names, numbers and flags, split at spaces: %q is none of their characters, and nothing is quoted. A command that needs it is the operator's to type in a terminal", c)
	}
	for _, c := range line {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune(" \t._/=-", c):
		case c == ',':
			// Held to the command that takes a list, below.
		default:
			return nil, nil, none(c)
		}
	}
	comma := strings.ContainsRune(line, ',')
	words := strings.Fields(line)
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	for i := range runList {
		c := &runList[i]
		if len(words) < len(c.words) || !slices.Equal(words[:len(c.words)], c.words) {
			continue
		}
		if comma && !c.commas {
			return nil, nil, none(',')
		}
		rest, err := c.parse(words[len(c.words):])
		if err != nil {
			return nil, nil, err
		}
		return c, append(slices.Clone(c.words), rest...), nil
	}
	if comma {
		return nil, nil, none(',')
	}
	return nil, nil, fmt.Errorf("--run takes a command of the list (%s): anything else is the operator's to type in a terminal", strings.Join(RunCommands(), ", "))
}

// parseMailLimit reads the words of peer allow <peer> --mail-limit <n>. A
// flag that is not --mail-limit refuses the command whole: what else peer
// allow does is not on the list.
func parseMailLimit(rest []string) ([]string, error) {
	const use = "--run takes yardr peer allow <peer> --mail-limit <n>"
	var peer, limit string
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		value, set := strings.CutPrefix(word, "--mail-limit=")
		switch {
		case word == "--mail-limit" && i+1 < len(rest):
			i++
			value, set = rest[i], true
		case word == "--mail-limit":
			return nil, fmt.Errorf("%s: --mail-limit needs its number", use)
		case set:
		case strings.HasPrefix(word, "-"):
			return nil, fmt.Errorf("%s, and peer allow with %s is the operator's to type in a terminal", use, word)
		case peer != "":
			return nil, fmt.Errorf("%s: one peer, not %s and %s", use, peer, word)
		default:
			peer = word
			continue
		}
		if limit != "" {
			return nil, fmt.Errorf("%s: --mail-limit once", use)
		}
		limit = value
	}
	switch {
	case peer == "":
		return nil, fmt.Errorf("%s: it names no peer", use)
	case limit == "":
		return nil, fmt.Errorf("%s: it names no --mail-limit", use)
	}
	n, err := strconv.Atoi(limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %q is no number", use, limit)
	}
	if n < 1 || n > MaxPeerMailLimit {
		return nil, fmt.Errorf("peer %s: a link takes 1 to %d mails an hour, not %d", peer, MaxPeerMailLimit, n)
	}
	return []string{peer, "--mail-limit", strconv.Itoa(n)}, nil
}

// mailLimitOf is the peer and the limit of a parsed peer allow <peer>
// --mail-limit <n>.
func mailLimitOf(argv []string) (string, int) {
	n, _ := strconv.Atoi(argv[4])
	return argv[2], n
}

// mailLimitCard is the card of peer allow <peer> --mail-limit <n>. The peer
// is named as the yard knows it, with what it calls itself and the start of
// its key, since two peers may have names that look alike; the approval is
// held to the key whole (Seen). Asking refuses a limit the link has
// already, and one below it: to take less mail is anyone's, and needs no
// approval.
func (w *wtx) mailLimitCard(argv []string, asking bool) (AskRun, error) {
	name, n := mailLimitOf(argv)
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	has := p.MailsAnHour()
	switch {
	case !asking:
	case n == has:
		return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour already, so the command would change nothing", name, has)
	case n < has:
		return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour, and to take less needs no approval: anyone runs yardr peer allow %s --mail-limit %d", name, has, name, n)
	}
	as := "key " + keyStart(p.Key)
	if p.Calls != "" {
		as = "it calls itself " + p.Calls + ", " + as
	}
	return AskRun{
		Argv: argv,
		Does: []string{fmt.Sprintf("the link with %s (%s) takes %d mails and asks an hour from it. It takes %d now.", name, as, n, has)},
		Back: runCommandLine([]string{"peer", "allow", name, "--mail-limit", strconv.Itoa(has)}),
		Seen: map[string]string{"peer " + name: p.Key},
	}, nil
}

// keyStart is the start of a key as a card shows it. It names the key to a
// person and proves nothing: what is compared is the key whole.
func keyStart(key string) string {
	if len(key) > 8 {
		return key[:8] + "…"
	}
	return key
}

// mailLimitStale says why the ask seq for peer allow <peer> --mail-limit
// <n> is approved no more: the link is another one under the same name, or
// it takes another number of mails than the card says.
func (w *wtx) mailLimitStale(seq uint64, was, now AskRun) error {
	name, n := mailLimitOf(was.Argv)
	const nothing = "Nothing ran. Decline it; it is asked again if it is still wanted."
	seen := "peer " + name
	if was.Seen[seen] != now.Seen[seen] {
		added := ""
		if p, err := loadPeer(w.Tx, name); err == nil {
			added = " (the link there now was added " + p.AddedAt.Local().Format("2 Jan 15:04") + ")"
		}
		return fmt.Errorf("ask %d was written when the link with %s was another: it was removed and made again since%s. %s", seq, name, added, nothing)
	}
	// The limit the card was written at is in the command that takes it
	// back, which is the same command with that number.
	_, had := mailLimitOf(strings.Fields(was.Back)[1:])
	_, has := mailLimitOf(strings.Fields(now.Back)[1:])
	switch {
	case had == has:
		return fmt.Errorf("ask %d says %q, and the yard would say %q now: the card is from before that changed. %s", seq, strings.Join(was.Does, " "), strings.Join(now.Does, " "), nothing)
	case has == n:
		return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d already: nothing is left to change. %s", seq, name, had, has, nothing)
	}
	return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d: the card is from before that changed. %s", seq, name, had, has, nothing)
}

// askedRun checks the command a run ask for the user to proposes, written
// by actor, and answers it as the ask stores it: the command in the yard's
// own form, with the yard's sentences, the command that takes it back and
// what the yard saw of what it names, whatever the writer sent for them.
//
// It refuses a command that is not on the list, one the yard would refuse
// or that would change nothing, anyone but the operator as the one asked
// (for a command that is a user's own, anyone but that user and the
// operator: runCommand.whose), and a writer that is no crew member and no user of the yard: a bead's
// agent reads text from outside the yard and is the one most likely to be
// talked into proposing something, so the store knows it by its pane,
// whatever actor it names. Asking gives the asker nothing: the ask is words
// until the operator approves it.
func (w *wtx) askedRun(ctx context.Context, run AskRun, to, actor string) (*AskRun, error) {
	c, argv, err := parseRun(run.Argv)
	if err != nil {
		return nil, err
	}
	const whose = "a command for the operator to approve is proposed by a crew member or a user of the yard"
	sess, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return nil, err
	}
	if sess != nil && !sess.Manual() {
		return nil, fmt.Errorf("%s; this is the session %s of a bead's agent: say what is needed in a note on the bead (yardr bead note)", whose, sess.ID)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) == nil && w.Bucket(bUsers).Get([]byte(actor)) == nil {
		return nil, fmt.Errorf("%s; this is %s: say what is needed in a note on the bead (yardr bead note)", whose, or(actor, "nobody"))
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return nil, err
	}
	user := ""
	if c.whose != nil {
		user = c.whose(argv)
	}
	switch {
	case user != "" && to == user:
	case user != "" && to != operator:
		whose := user + "'s to approve"
		if operator != "" && operator != user {
			whose += ", or the operator's (" + operator + ") for them"
		}
		return nil, fmt.Errorf("this command is %s: the yard runs it as the one who approves, so %s cannot be asked to", whose, to)
	case operator == "":
		return nil, fmt.Errorf("a command is the operator's to approve, and this yard has none: yardr settings set operator <name>")
	case to != operator:
		return nil, fmt.Errorf("a command is the operator's to approve (%s): the yard runs it as them, so %s cannot be asked to", operator, to)
	}
	card, err := c.card(w, argv, true)
	if err != nil {
		return nil, err
	}
	return &card, nil
}

// ApproveAsk approves the run ask ask on the bead id as actor, and the yard
// runs the command the ask holds: in one write it checks the ask and who
// approves, holds the approval to the yard the card was written from, makes
// the change through the command's own store function as the operator, and
// records an answer that holds the command and what it printed.
//
// It refuses an ask that is not on the bead, proposes no command, or was
// declined, taken back or approved already: a command runs once for one
// ask. It refuses anyone but the operator, and every call from the pane of
// an agent whatever actor it names (mayApprove), also for a command whose
// own check goes by the name alone, so that an agent cannot approve its own
// ask by naming the operator. A command that is a user's own, as that
// user's subscriptions and what their phone is told of are, is approved by
// the one it was asked of, that
// user or the operator, and by nobody else (mayApproveOwn). It refuses when
// the yard would not write the
// same card now: what the command names was replaced under its name (Seen),
// or what it changes is not what the card says it is (Does). And what the
// command's own function refuses is refused here, in its words. A refused
// approval records nothing and changes nothing, and leaves the ask open.
//
// It is served on the yard's socket, and on the listener to a device of the
// operator and to no other caller (rpc.OperatorCalls): the answer and the
// events of an approval that came so say how it came, as a device's other
// writes do (writeVia). No answer that comes over a link approves
// (answerable).
func (s *Store) ApproveAsk(ctx context.Context, id string, ask uint64, actor string) (*AskApproved, error) {
	// A bead's handle never holds an "@" (validID).
	if strings.Contains(id, "@") {
		return nil, fmt.Errorf("ask %d on %s is a command of another yard's operator: it is approved in the yard that asked, never over a link", ask, id)
	}
	var out *AskApproved
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		out, err = w.approveAsk(ctx, id, ask, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// approveAsk is ApproveAsk inside the transaction. A batch may run it twice
// for one call (write): it reads the yard's records anew each time and ends
// the same.
func (w *wtx) approveAsk(ctx context.Context, id string, seq uint64, actor string) (*AskApproved, error) {
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
	case r.Ask.Run == nil:
		return nil, fmt.Errorf("ask %d proposes no command to approve: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, row.ID, seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	case r.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a command runs once for one ask", seq, r.Current.Note.Author)
	}
	was := *r.Ask.Run
	c, argv, err := parseRun(was.Argv)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w", seq, err)
	}
	// Before anything is written: an agent's approval changes nothing, not
	// even what the transaction would take back.
	if c.whose != nil {
		err = w.mayApproveOwn(ctx, actor, r.Ask.To, c.whose(argv))
	} else {
		err = w.mayApprove(ctx, actor)
	}
	if err != nil {
		return nil, err
	}
	if !slices.Equal(argv, was.Argv) {
		return nil, fmt.Errorf("ask %d holds a command the yard did not write (%s): nothing ran", seq, was.Command())
	}
	card, err := c.card(w, argv, false)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. Nothing ran. Decline it; it is asked again if it is still wanted", seq, err)
	}
	if !sameMap(card.Seen, was.Seen) || !slices.Equal(card.Does, was.Does) || card.Back != was.Back || card.BackBy != was.BackBy {
		return nil, c.stale(w, seq, was, card)
	}
	result, err := c.run(ctx, w, argv, actor, &approvedOn{Bead: row.ID, Ask: seq})
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(Answer{Ask: seq, Ran: &AskRan{Argv: argv, Result: result}})
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return nil, err
	}
	return &AskApproved{Bead: row.ID, Ask: seq, Asker: r.Note.Author, Argv: argv, Does: was.Does, Result: result}, nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// mayApprove refuses an approval by anyone but the operator. It is the
// firmest check the store has, as maySource is: who asks is told by the
// store's own record of the calling process before the caller's word, so a
// call from the pane of a bead's session or of a crew member is refused
// whatever actor it names. A yard without an operator has nobody whose
// command it would be.
func (w *wtx) mayApprove(ctx context.Context, actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if operator == "" {
		return fmt.Errorf("approving a command is the operator's, and this yard has none: yardr settings set operator <name>")
	}
	why := "approving a command is the operator's (" + operator + "): the yard runs it as them"
	pane, err := agentPane(ctx, w.Tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != operator {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// mayApproveOwn refuses an approval of a command that is the user whose own
// to give, as it is the operator's for them, by anyone but the one the ask
// was put to, who is one of the two: the card says the yard runs the
// command as the one asked, and so it does. An agent's pane is refused
// first, whatever actor the call names, as mayApprove refuses it. Whether
// the one asked may still make the change is the command's own check,
// which runs after this one.
func (w *wtx) mayApproveOwn(ctx context.Context, actor, to, whose string) error {
	why := "approving this command is " + to + "'s, whom it was asked of: the yard runs it as them"
	pane, err := agentPane(ctx, w.Tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != to {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if to != whose && to != operator {
		return fmt.Errorf("this command is %s's own to approve, or the operator's for them, and %s is neither now", whose, to)
	}
	return nil
}
