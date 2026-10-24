package store

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/yardkey"
)

// runYard is an ask yard whose operator is mckean, with the crew member
// yardmaster, the user ada, and the peer airy, which calls itself yardr and
// takes the 30 mails an hour a link takes unless set. It answers airy's key.
func runYard(t *testing.T) (*askYard, string) {
	t.Helper()
	y := answerYard(t)
	if err := y.SetSetting(y.ctx, OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	if err != nil {
		t.Fatal(err)
	}
	key := yardkey.Public(k)
	if _, err := y.AddPeer(y.ctx, Peer{Name: "airy", Key: key, Calls: "yardr"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y, key
}

// noOperator makes the yard one without an operator: the setting unset and
// nobody's role an operator's, which no call of the store makes, since the
// last operator is never made a member.
func (y *askYard) noOperator(t *testing.T, actor string) {
	t.Helper()
	if err := y.SetSetting(y.ctx, OperatorSetting, "", actor); err != nil {
		t.Fatal(err)
	}
	if err := y.write(func(w *wtx) error {
		var users []User
		if err := each(w.Tx, bUsers, "", func(_ []byte, u *User) error {
			users = append(users, *u)
			return nil
		}); err != nil {
			return err
		}
		for _, u := range users {
			u.Role = ""
			if err := put(w.Tx, bUsers, u.Name, &u); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func runAsk(command ...string) Ask {
	return Ask{Question: "Take more mail from airy?", To: "mckean",
		Context: []string{"airy's yard was refused 4 mails in the last hour: the link takes 30.",
			"You write from airy all day, so most of its mail is yours."},
		Run: &AskRun{Argv: command}}
}

// asked is the ask seq of the bead as it is stored.
func (y *askYard) asked(t *testing.T, seq uint64) ResolvedAsk {
	t.Helper()
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ResolveAsks(notes, y.now) {
		if r.Note.Seq == seq {
			if r.AskErr != nil {
				t.Fatal(r.AskErr)
			}
			return r
		}
	}
	t.Fatalf("no ask %d on the bead", seq)
	return ResolvedAsk{}
}

// limit is how much mail the link with airy takes in an hour.
func (y *askYard) limit(t *testing.T) int {
	t.Helper()
	p, err := y.GetPeer(y.ctx, "airy")
	if err != nil {
		t.Fatal(err)
	}
	return p.MailsAnHour()
}

// A run ask is stored with the command as the yard parsed it, in one order
// however it was typed, and with the yard's own sentences, the command that
// takes the change back and the link's key whole: all of it the yard's,
// whatever the asker sent.
func TestRunAskIsStoredInTheYardsForm(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, key := runYard(t)
	want := AskRun{
		Argv:        []string{"peer", "allow", "airy", "--mail-limit", "300"},
		Description: "Set which messages a peer link carries and its mail limit",
		Does:        []string{"the link with airy (it calls itself yardr, key " + key[:8] + "…) takes 300 mails and asks an hour from it. It takes 30 now."},
		Back:        "yardr peer allow airy --mail-limit 30",
		Seen:        map[string]string{"peer airy": key},
	}
	for _, typed := range [][]string{
		{"peer allow airy --mail-limit 300"},
		{"yardr peer allow airy --mail-limit 300"},
		{"yardr  peer allow --mail-limit=300\tairy"},
		{"peer", "allow", "--mail-limit", "0300", "airy"},
	} {
		ask := runAsk(typed...)
		ask.Run.Does = []string{"a routine limit, nothing to read here"}
		ask.Run.Back = "yardr peer rm airy"
		ask.Run.Seen = map[string]string{"peer airy": "another key"}
		got := y.asked(t, y.ask(t, ask, "yardmaster")).Ask.Run
		if got == nil || !slices.Equal(got.Argv, want.Argv) || got.Description != want.Description || !slices.Equal(got.Does, want.Does) || got.Back != want.Back || !sameMap(got.Seen, want.Seen) {
			t.Errorf("typed %q, the ask stores %+v, want %+v", typed, got, want)
		}
		if got.Command() != "yardr peer allow airy --mail-limit 300" {
			t.Errorf("typed %q, the command reads %q", typed, got.Command())
		}
	}
	// A user of the yard asks as a crew member does.
	y.ask(t, runAsk("peer allow airy --mail-limit 300"), "ada")
}

// A run ask is refused, and nothing is stored, for a command that is not on
// the list, a character no word of the list has, a peer the yard has not, a
// number out of range, equal to the limit now or below it, a flag that is
// not the list's, an option, a recommendation, an about-bead, a secret
// beside it, more than five context lines, anyone but the operator as the
// one asked, a person of another yard, and a writer that is no crew member
// and no user.
func TestRunAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	const list = "--run takes a command of the list (" + "yardr peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>, yardr peer intake <peer> --depot <depot> --flow <flow> [--limit <n>] [--default], yardr peer identity <peer> <crew> --source <source> --id <id>, yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>, yardr source grant <source> read|<call>|--preset <preset> --group <group>|--crew <crew member> [--as <user>], yardr source add <file>, yardr source ignore <source> --rm <id>, yardr source check <source> --user <user>, yardr pack apply <name>, yardr hook add <name> --source <declared source> [--unknown unknown|allow|ignore], yardr subscribe <user> yardr [--done] [--where <key>=<value>], yardr user set <user> tell=<parts>, yardr user key <user> <source>, yardr user add <name>, yardr user rm <name>, yardr crew set <name> open_threads=<sources> | set_state=<sources> | peers=<peers>, yardr crew identity <name> --source <source> [--id <id>] [--key <key>], yardr user identity <user> --source <source> [--id <id>] [--key <key>], yardr settings set <key> <value>, yardr peer rename <old> <new>, yardr bead update <bead> --originator <user>, yardr timer rm <id>" + "): anything else is run directly, or typed by the operator in a terminal"
	for _, c := range []struct {
		name  string
		ask   func(a *Ask)
		actor string
		want  string
	}{
		{"not on the list", func(a *Ask) { a.Run.Argv = []string{"user key mckean"} }, "", "one user and one source, not 1 words"},
		{"another yardr command", func(a *Ask) { a.Run.Argv = []string{"yardr peer list"} }, "", list},
		{"no yardr command", func(a *Ask) { a.Run.Argv = []string{"rm -rf /"} }, "", list},
		{"nothing", func(a *Ask) { a.Run.Argv = nil }, "", list},
		{"a semicolon", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300; yardr user add eve"} }, "", `';' is none of their characters`},
		{"a quote", func(a *Ask) { a.Run.Argv = []string{`peer allow "airy" --mail-limit 300`} }, "", `'"' is none of their characters`},
		{"a backslash", func(a *Ask) { a.Run.Argv = []string{`peer allow airy\ --mail-limit 300`} }, "", `'\\' is none of their characters`},
		{"a pipe", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300 | sh"} }, "", `'|' is none of their characters`},
		{"an ampersand", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300 &"} }, "", `'&' is none of their characters`},
		{"a dollar", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit $N"} }, "", `'$' is none of their characters`},
		{"a line break", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300\nyardr user add eve"} }, "", `'\n' is none of their characters`},
		{"a comma in a name", func(a *Ask) { a.Run.Argv = []string{"peer allow airy,north --mail-limit 300"} }, "", "peer airy,north: not found"},
		{"a comma in no command of the list", func(a *Ask) { a.Run.Argv = []string{"user add eve,bo"} }, "", `',' is none of their characters`},
		{"a letter of another script", func(a *Ask) { a.Run.Argv = []string{"peer allow аiry --mail-limit 300"} }, "", "is none of their characters"},
		{"a peer the yard has not", func(a *Ask) { a.Run.Argv = []string{"peer allow a1ry --mail-limit 300"} }, "", "peer a1ry: not found"},
		{"zero", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 0"} }, "", "peer airy: a link takes 1 to 10000 mails an hour, not 0"},
		{"too many", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 10001"} }, "", "peer airy: a link takes 1 to 10000 mails an hour, not 10001"},
		{"below zero", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit -5"} }, "", "a link takes 1 to 10000 mails an hour, not -5"},
		{"no number", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit many"} }, "", `"many" is no number`},
		{"the limit now", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 30"} }, "", "yardr peer allow airy --mail-limit 30 needs no approval: run it yourself"},
		{"a lower limit", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 10"} }, "", "yardr peer allow airy --mail-limit 10 needs no approval: run it yourself"},
		{"no limit", func(a *Ask) { a.Run.Argv = []string{"peer allow airy"} }, "", "it says nothing to change"},
		{"no peer", func(a *Ask) { a.Run.Argv = []string{"peer allow --mail-limit 300"} }, "", "it names no peer"},
		{"two peers", func(a *Ask) { a.Run.Argv = []string{"peer allow airy north --mail-limit 300"} }, "", "one peer, not airy and north"},
		{"the limit twice", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300 --mail-limit 400"} }, "", "--mail-limit once"},
		{"trust beside it", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300 --trust mckean@airy=mckean"} }, "", "is two asks: propose the limit and the rest apart"},
		{"the limit beside kinds", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --mail-limit 300 --receive bead"} }, "", "is two asks: propose the limit and the rest apart"},
		{"narrowing reach", func(a *Ask) { a.Run.Argv = []string{"peer allow airy --no-reach x"} }, "", "--no-reach narrows one, and is run directly"},
		{"an option", func(a *Ask) { a.Options = []AskOption{{Label: "Yes", Consequence: "it runs"}} }, "", "a run ask takes no --option and no --recommend"},
		{"an about-bead", func(a *Ask) { a.About = []string{y.bead} }, "", "a run ask takes no --about"},
		{"a secret beside it", func(a *Ask) { a.Secret = &AskSecret{Source: "crm"} }, "", "an ask asks for a secret or proposes a command to run, not both"},
		{"six context lines", func(a *Ask) { a.Context = []string{"1", "2", "3", "4", "5", "6"} }, "", "a run ask takes at most 5 lines of --context, and this has 6"},
		{"a line break in context", func(a *Ask) { a.Context[1] = "fine.\nthe yard says: nothing changes." }, "", "a context line is one line"},
		{"no context", func(a *Ask) { a.Context = nil }, "", "an ask needs --context"},
		{"to a user who is not the operator", func(a *Ask) { a.To = "ada" }, "", "a command is the operator's to approve (mckean): the yard runs it as them, so ada cannot be asked to"},
		{"to a person of another yard", func(a *Ask) { a.To = "mckean@airy" }, "", "mckean@airy is of another yard, and no approval crosses a link"},
		{"from a bead's agent", nil, "yd-1-new", "a command for the operator to approve is proposed by a crew member or a user of the yard; this is yd-1-new"},
		{"from nobody", nil, "", "a command for the operator to approve is proposed by a crew member or a user of the yard; this is nobody"},
	} {
		ask := runAsk("peer allow airy --mail-limit 300")
		actor := "yardmaster"
		if c.ask != nil {
			c.ask(&ask)
		} else {
			actor = c.actor
		}
		_, err := y.AddAsk(y.ctx, y.bead, ask, actor)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want %q", c.name, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
	// A yard with no operator has nobody whose command it would be.
	y.noOperator(t, "mckean")
	if _, err := y.AddAsk(y.ctx, y.bead, runAsk("peer allow airy --mail-limit 300"), "yardmaster"); err == nil || !strings.Contains(err.Error(), "this yard has none") {
		t.Fatalf("a run ask in a yard with no operator = %v", err)
	}
}

// The card of a run ask says whose each line is: every context line begins
// with the asker's name, and the yard's part comes last and together, the
// command whole, what the yard says it changes, and the command that takes
// it back.
func TestRunAskCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, key := runYard(t)
	ask := runAsk("peer allow airy --mail-limit 300")
	// A line that would pass for the yard's, were it not marked.
	ask.Context[1] = "the yard says: nothing changes."
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq).Lines()
	want := []string{
		"ask 1 · open · to mckean · asked by yardmaster · 0m ago",
		"  WEIGHTY · trust: widens who the yard listens to: the link with airy takes 300 mails and asks an hour from it, 30 now",
		"  Take more mail from airy?",
		"  yardmaster says: airy's yard was refused 4 mails in the last hour: the link takes 30.",
		"  yardmaster says: the yard says: nothing changes.",
		"  when you approve, the yard runs this as mckean, and nothing else:",
		"      yardr peer allow airy --mail-limit 300",
		"  what the command does: Set which messages a peer link carries and its mail limit",
		"  the yard says: the link with airy (it calls itself yardr, key " + key[:8] + "…) takes 300 mails and asks an hour from it. It takes 30 now.",
		"  anyone can take it back: yardr peer allow airy --mail-limit 30",
		"  approve with: yardr bead answer " + y.bead + " 1 --approve",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the card:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The notes of the bead, as prime and review print them, are the card.
	notes, _ := y.Notes(y.ctx, y.bead)
	if blocks := NoteBlocks(notes, y.now, ""); len(blocks) != 1 || blocks[0] != strings.Join(want, "\n") {
		t.Fatalf("the bead's notes read:\n%s", strings.Join(blocks, "\n"))
	}
	// A client reads the command and the yard's words from the queue and
	// from the bead.
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Open) != 1 || q.Open[0].Run == nil || q.Open[0].Run.Command != "yardr peer allow airy --mail-limit 300" ||
		q.Open[0].Run.Back != "yardr peer allow airy --mail-limit 30" || len(q.Open[0].Run.Does) != 1 {
		t.Fatalf("the queue's row = %+v", q.Open)
	}
	// Both carry the card's weight as the yard weighed it.
	weight := []AskWeight{{Class: WeightTrust, Line: "widens who the yard listens to: the link with airy takes 300 mails and asks an hour from it, 30 now"}}
	if !slices.Equal(q.Open[0].Weight, weight) {
		t.Fatalf("the queue's row weighs %+v, want %+v", q.Open[0].Weight, weight)
	}
	v, err := y.Bead(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 1 || v.Asks[0].Run == nil || !slices.Equal(v.Asks[0].Run.Argv, []string{"peer", "allow", "airy", "--mail-limit", "300"}) || v.Asks[0].Ran != nil {
		t.Fatalf("the bead's ask = %+v", v.Asks)
	}
	if !slices.Equal(v.Asks[0].Weight, weight) {
		t.Fatalf("the bead's ask weighs %+v, want %+v", v.Asks[0].Weight, weight)
	}
	// A peer that named itself nothing is named by its key alone.
	if err := y.write(func(w *wtx) error {
		p, err := loadPeer(w.Tx, "airy")
		if err != nil {
			return err
		}
		p.Calls = ""
		return put(w.Tx, bPeers, "airy", p)
	}); err != nil {
		t.Fatal(err)
	}
	if does := y.asked(t, y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")).Ask.Run.Does[0]; !strings.HasPrefix(does, "the link with airy (key "+key[:8]+"…) takes 300") {
		t.Fatalf("the sentence of a peer with no name of its own = %q", does)
	}
}

// ApproveAsk runs the command the ask holds as the operator, in the write
// that records the approval: the link takes the new limit, the answer note
// holds what ran and what it printed, the change's own event names the ask,
// the asker reads in its inbox what was run, and the card reads approved.
func TestApproveAsk(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	seq := y.ask(t, runAsk("yardr peer allow --mail-limit 300 airy"), "yardmaster")
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"peer", "allow", "airy", "--mail-limit", "300"}
	const result = "peer airy: takes 300 mails and asks an hour, and 19200 files"
	if done.Bead != y.bead || done.Ask != seq || done.Asker != "yardmaster" || !slices.Equal(done.Argv, argv) || done.Result != result ||
		len(done.Does) != 1 || !strings.Contains(done.Does[0], "takes 300 mails and asks an hour from it. It takes 30 now.") {
		t.Fatalf("ApproveAsk answers %+v", done)
	}
	if got := y.limit(t); got != 300 {
		t.Fatalf("the link takes %d mails an hour after the approval, want 300", got)
	}
	answers := answerNotes(t, y)
	if len(answers) != 1 || answers[0].Author != "mckean" {
		t.Fatalf("the answers = %+v", answers)
	}
	want, _ := json.Marshal(map[string]any{"ask": seq, "ran": map[string]any{"argv": argv, "result": result}})
	if answers[0].Text != string(want) {
		t.Fatalf("the answer note = %s, want %s", answers[0].Text, want)
	}
	evs, err := y.EventsOfKind(y.ctx, "peer_allowed", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("peer_allowed events = %+v, %v", evs, err)
	}
	approved, _ := json.Marshal(evs[0].Data["approved"])
	on, _ := json.Marshal(map[string]any{"bead": y.bead, "ask": seq})
	if evs[0].Actor != "mckean" || string(approved) != string(on) || evs[0].Data["peer"] != "airy" {
		t.Fatalf("the event = %+v by %s, want it by mckean with approved %s", evs[0].Data, evs[0].Actor, on)
	}
	line := "mckean approved ask 1 on " + y.bead + ", and the yard ran: yardr peer allow airy --mail-limit 300"
	if got, _ := lastAnswer(t, y).Data["answer"].(string); got != line {
		t.Fatalf("the answer reads %q, want %q", got, line)
	}
	page, err := y.Inbox(y.ctx, "yardmaster", 0)
	if err != nil || page.Total != 1 || page.Events[0].Data["answer"] != line {
		t.Fatalf("the asker's inbox = %+v, %v, want the one line %q", page, err, line)
	}
	r := y.asked(t, seq)
	card := r.Lines()
	for _, want := range []string{
		"ask 1 · approved · to mckean · asked by yardmaster · 0m ago",
		"  mckean approved, ran: yardr peer allow airy --mail-limit 300",
		"      " + result,
		"  anyone can take it back: yardr peer allow airy --mail-limit 30",
	} {
		if !slices.Contains(card, want) {
			t.Errorf("the approved card lacks %q:\n%s", want, strings.Join(card, "\n"))
		}
	}
	if slices.ContainsFunc(card, func(l string) bool {
		return strings.Contains(l, "approve with") || strings.Contains(l, "when you approve")
	}) {
		t.Errorf("the approved card still offers the approval:\n%s", strings.Join(card, "\n"))
	}
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil || len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Ran == nil || q.Decided[0].Ran.Result != result || q.Decided[0].Run == nil ||
		len(q.Decided[0].Weight) != 1 || q.Decided[0].Weight[0].Class != WeightTrust {
		t.Fatalf("the queue after the approval = %+v, %v", q, err)
	}
	v, err := y.Bead(y.ctx, y.bead)
	if err != nil || len(v.Asks) != 1 || v.Asks[0].State != "answered" || v.Asks[0].Ran == nil || !slices.Equal(v.Asks[0].Ran.Argv, argv) {
		t.Fatalf("the bead after the approval = %+v, %v", v, err)
	}

	// A command runs once for one ask, and an approved ask takes no other
	// answer.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "ask 1 was approved already, by mckean: a command runs once for one ask") {
		t.Fatalf("a second approval = %v", err)
	}
	for _, ans := range []Answer{{Ask: seq, Later: true}, {Ask: seq, Decline: true, Words: "no"}} {
		if err := y.AddAnswer(y.ctx, y.bead, ans, "mckean"); err == nil || !strings.Contains(err.Error(), "ask 1 was approved already, and the yard ran its command") {
			t.Errorf("%+v on an approved ask = %v", ans, err)
		}
	}
	// The change is taken back by anyone, with the command the card names.
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{MailLimit: 30}, "ada"); err != nil {
		t.Fatalf("taking it back as ada: %v", err)
	}
}

// An approval is the operator's alone, on an ask that proposes a command
// and is open or put aside. A refused approval records nothing and changes
// nothing.
func TestApproveAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	run := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	words := y.ask(t, Ask{Question: "Which?", To: "mckean", Context: []string{"c"}}, "yardmaster")
	declined := y.ask(t, runAsk("peer allow airy --mail-limit 400"), "yardmaster")
	y.answer(t, Answer{Ask: declined, Decline: true, Words: "not now"}, "mckean")
	taken := y.ask(t, runAsk("peer allow airy --mail-limit 500"), "yardmaster")
	if err := y.TakeBackAsk(y.ctx, y.bead, taken, "asked again below", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := y.AddNote(y.ctx, y.bead, "a plain note", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	notes, _ := y.Notes(y.ctx, y.bead)
	plain := notes[len(notes)-1].Seq
	const whose = "approving a command is the operator's (mckean): the yard runs it as them; "
	for _, c := range []struct {
		name, bead string
		ask        uint64
		actor      string
		want       string
	}{
		{"another user", y.bead, run, "ada", whose + "this is ada"},
		{"the asker", y.bead, run, "yardmaster", whose + "yardmaster is a crew member"},
		{"nobody", y.bead, run, "", whose + "this is nobody"},
		{"an ask for words", y.bead, words, "mckean", "proposes no command to approve"},
		{"a declined ask", y.bead, declined, "mckean", "was declined: mckean declined to decide this: not now"},
		{"an ask taken back", y.bead, taken, "mckean", "is asked no more: taken back by yardmaster: asked again below"},
		{"a note that is no ask", y.bead, plain, "mckean", "is not an ask"},
		{"no such ask", y.bead, 999, "mckean", "has no ask 999"},
		{"no such bead", "yd-none", run, "mckean", "not found"},
		{"a question of another yard", y.bead + "@airy", run, "mckean", "it is approved in the yard that asked, never over a link"},
	} {
		_, err := y.ApproveAsk(y.ctx, c.bead, c.ask, c.actor, false, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: ApproveAsk = %v, want %q", c.name, err, c.want)
		}
	}
	if got := y.limit(t); got != 30 {
		t.Fatalf("the link takes %d mails an hour after refused approvals, want 30", got)
	}
	if evs, _ := y.EventsOfKind(y.ctx, "peer_allowed", 10); len(evs) != 0 {
		t.Fatalf("refused approvals logged %+v", evs)
	}
	if answers := answerNotes(t, y); len(answers) != 2 {
		t.Fatalf("the bead has %d answers, want the decline and the taking back alone", len(answers))
	}
	// In a yard that names no operator nobody approves.
	y.noOperator(t, "mckean")
	if _, err := y.ApproveAsk(y.ctx, y.bead, run, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approving a command is the operator's, and this yard has none") {
		t.Fatalf("an approval in a yard with no operator = %v", err)
	}
}

// Nothing but ApproveAsk approves: a pick, words and an answer that arrives
// saying what ran are refused on a run ask, here and as the rules an answer
// from another yard lands by. Later puts it aside, from where it is still
// approved, and a decline ends it.
func TestRunAskTakesNoOtherAnswer(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	seq := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	approve := "ask 1 takes an approval, not words: yardr bead answer " + y.bead + " 1 --approve"
	for _, c := range []struct {
		ans  Answer
		want string
	}{
		{Answer{Ask: seq, Option: 1}, approve},
		{Answer{Ask: seq, Option: 1, Words: "yes"}, approve},
		{Answer{Ask: seq, Words: "approved"}, approve},
		{Answer{Ask: seq, Ran: &AskRan{Argv: []string{"peer", "allow", "airy", "--mail-limit", "300"}, Result: "done"}},
			"a command is approved with yardr bead answer " + y.bead + " 1 --approve, and what ran is recorded by the yard alone"},
		{Answer{Ask: seq, Decline: true}, "declining ask 1 needs words"},
	} {
		if err := y.AddAnswer(y.ctx, y.bead, c.ans, "mckean"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v = %v, want %q", c.ans, err, c.want)
		}
		notes, _ := y.Notes(y.ctx, y.bead)
		if _, err := answerable(y.bead, notes, c.ans, y.now); c.ans.Ran == nil && err == nil {
			t.Errorf("%+v from another yard would land", c.ans)
		}
	}
	if answers := answerNotes(t, y); len(answers) != 0 || y.limit(t) != 30 {
		t.Fatalf("refused answers left %d notes and a limit of %d", len(answers), y.limit(t))
	}
	if err := y.AnswerAsk(y.ctx, y.bead, seq, 0, "run it", "mckean", false); err == nil || !strings.Contains(err.Error(), approve) {
		t.Fatalf("AnswerAsk with words = %v", err)
	}

	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")
	if r := y.asked(t, seq); r.State != AskAside {
		t.Fatalf("the ask put aside is %s", r.State)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatalf("approving an ask that was put aside: %v", err)
	}
	if got := y.limit(t); got != 300 {
		t.Fatalf("the link takes %d after the approval", got)
	}

	second := y.ask(t, runAsk("peer allow airy --mail-limit 400"), "yardmaster")
	y.answer(t, Answer{Ask: second, Decline: true, Words: "300 is enough"}, "mckean")
	r := y.asked(t, second)
	if r.State != AskDeclined || !slices.Contains(r.Lines(), "  mckean declined to decide this: 300 is enough") {
		t.Fatalf("the declined ask is %s:\n%s", r.State, strings.Join(r.Lines(), "\n"))
	}
	if got := y.limit(t); got != 300 {
		t.Fatalf("a decline changed the limit to %d", got)
	}
}

// An approval is refused when the yard changed since the ask: the link
// takes another number of mails than the card says, in the words of the
// change. Nothing runs, and the ask stays open to be declined.
func TestApproveAskRefusesWhenTheLimitChanged(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	const nothing = " Nothing ran. Decline it; it is asked again if it is still wanted."
	for _, c := range []struct {
		name string
		to   int
		want string
	}{
		{"raised", 100, "ask 1 says the link with airy takes 30 mails an hour now, and it takes 100: the card is from before that changed." + nothing},
		{"lowered", 10, "ask 1 says the link with airy takes 30 mails an hour now, and it takes 10: the card is from before that changed." + nothing},
		{"made already", 300, "ask 1 says the link with airy takes 30 mails an hour now, and it takes 300 already: nothing is left to change." + nothing},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, _ := runYard(t)
			seq := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
			if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{MailLimit: c.to}, "mckean"); err != nil {
				t.Fatal(err)
			}
			if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || err.Error() != c.want {
				t.Fatalf("ApproveAsk = %v, want %q", err, c.want)
			}
			if got := y.limit(t); got != c.to {
				t.Fatalf("the refused approval left the limit at %d, want %d", got, c.to)
			}
			if evs, _ := y.EventsOfKind(y.ctx, "peer_allowed", 10); len(evs) != 1 {
				t.Fatalf("peer_allowed events = %d, want the change in between alone", len(evs))
			}
			if r := y.asked(t, seq); r.State != AskOpen || len(answerNotes(t, y)) != 0 {
				t.Fatalf("the ask is %s after a refused approval", r.State)
			}
			y.answer(t, Answer{Ask: seq, Decline: true, Words: "asked again"}, "mckean")
		})
	}
}

// An approval is held to the link the card was written from, by its key
// whole. A link of the same name whose key begins as the old one did reads
// the same on the card, sentence for sentence, and is another link: the
// approval is refused, and nothing runs.
func TestApproveAskRefusesAnotherLinkOfTheSameName(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, key := runYard(t)
	seq := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	was := *y.asked(t, seq).Ask.Run
	other := key[:len(key)-1] + "A"
	if other == key {
		other = key[:len(key)-1] + "B"
	}
	if err := y.write(func(w *wtx) error {
		p, err := loadPeer(w.Tx, "airy")
		if err != nil {
			return err
		}
		p.Key = other
		return put(w.Tx, bPeers, "airy", p)
	}); err != nil {
		t.Fatal(err)
	}
	var card AskRun
	if err := y.write(func(w *wtx) (err error) {
		card, err = w.peerAllowCard(was.Argv, false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(card.Does, was.Does) || card.Back != was.Back {
		t.Fatalf("the test wants cards that read the same: %q %q and %q %q", card.Does, card.Back, was.Does, was.Back)
	}
	if card.Seen["peer airy"] == was.Seen["peer airy"] || was.Seen["peer airy"] != key {
		t.Fatalf("seen = %v and %v, want the old key whole on the ask and another now", was.Seen, card.Seen)
	}
	_, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	const want = "ask 1 was written when the link with airy was another: it was removed and made again since (the link there now was added "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "). Nothing ran. Decline it; it is asked again if it is still wanted.") {
		t.Fatalf("ApproveAsk = %v, want %q…", err, want)
	}
	if got := y.limit(t); got != 30 {
		t.Fatalf("the refused approval left the limit at %d", got)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("the refused approval left %d answers", len(answers))
	}
	// A link that is gone is no link to change either.
	if err := y.DeletePeer(y.ctx, "airy", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "peer airy: not found") || !strings.Contains(err.Error(), "Nothing ran") {
		t.Fatalf("ApproveAsk of a link that is gone = %v", err)
	}
}

// A change to what neither the sentences nor the key cover leaves the
// approval good: the command does what the card says to the link the card
// was written from.
func TestApproveAskHoldsWhenSomethingElseChanged(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	seq := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Send: []string{PeerMail}, Receive: []string{PeerMail}, Expose: []string{"mckean"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	p, err := y.GetPeer(y.ctx, "airy")
	if err != nil || p.MailsAnHour() != 300 || !slices.Equal(p.Send, []string{PeerMail}) || !slices.Equal(p.Expose, []string{"mckean"}) {
		t.Fatalf("the link after the approval = %+v, %v", p, err)
	}
}

// No copy of a run ask is sent to a name with reach: it is approved in this
// yard alone. An ask of any other kind for the same user is copied, and a
// run ask that another yard sends is no ask this yard takes.
func TestRunAskIsNotCopiedOverALink(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	if err := y.PeerHeard(y.ctx, "airy", y.asKey(t), &PeerSays{Understands: []string{PeerMail, PeerAsk},
		Roster: []PeerRosterEntry{{Name: "mckean", Kind: RosterUser, Mail: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Send: []string{PeerAsk}, Receive: []string{PeerAsk},
		Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	copies := func(seq uint64) int {
		t.Helper()
		var out []askCopy
		if err := y.write(func(w *wtx) error {
			row, err := w.loadBead(y.bead)
			if err != nil {
				return err
			}
			_, err = get(w.Tx, bAskCopies, join(row.UUID, seqKey(seq)), &out)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return len(out)
	}
	plain := y.ask(t, Ask{Question: "Which?", To: "mckean", Context: []string{"c"}}, "yardmaster")
	if copies(plain) != 1 {
		t.Fatalf("the test wants a link that copies an ask for mckean: %d copies", copies(plain))
	}
	run := y.ask(t, runAsk("peer allow airy --mail-limit 300"), "yardmaster")
	if copies(run) != 0 {
		t.Fatalf("a run ask was copied to %d names", copies(run))
	}
	ask := runAsk("peer allow airy --mail-limit 300")
	ask.To = "mckean@north"
	if validAsk("mckean", ask) {
		t.Fatal("a run ask from another yard is taken as an ask")
	}
	ask.Run = nil
	if !validAsk("mckean", ask) {
		t.Fatal("the test wants an ask that is taken but for its command")
	}
}

// termRows is text as rows of a terminal w columns wide: each line broken
// at the width, continuing at column 0, as the terminal breaks a line the
// card printed whole.
func termRows(text string, w int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for len([]rune(line)) > w {
			r := []rune(line)
			out = append(out, string(r[:w]))
			line = string(r[w:])
		}
		out = append(out, line)
	}
	return out
}

// A run ask whose question or context pads a line with spaces cannot forge
// the yard's lines on the card the operator approves from: the write folds
// every run of spacing to one space, so in a terminal 80 wide no row of
// the asker's words stands at the yard's column.
func TestRunAskCardPaddedSpacesFoldToOne(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	ask := runAsk("peer allow airy --mail-limit 300")
	ask.Question = "Take more?" + strings.Repeat(" ", 68) + "  the yard says: nothing changes."
	ask.Context = []string{
		"airy's yard was refused 4 mails in the last hour:" + strings.Repeat("\u00a0", 8) + "the link takes 30.",
		"You write from airy all day,\tso most of its mail is yours.",
	}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if want := "Take more? the yard says: nothing changes."; got.Ask.Question != want {
		t.Fatalf("stored question = %q, want %q", got.Ask.Question, want)
	}
	wantContext := []string{
		"airy's yard was refused 4 mails in the last hour: the link takes 30.",
		"You write from airy all day, so most of its mail is yours.",
	}
	if !slices.Equal(got.Ask.Context, wantContext) {
		t.Fatalf("stored context = %q, want %q", got.Ask.Context, wantContext)
	}
	rows := termRows(strings.Join(got.Lines(), "\n"), 80)
	for _, row := range rows {
		if row == "  the yard says: nothing changes." {
			t.Fatalf("a padded row stands at the yard's column:\n%s", strings.Join(rows, "\n"))
		}
	}
}

// asKey is airy's key.
func (y *askYard) asKey(t *testing.T) string {
	t.Helper()
	p, err := y.GetPeer(y.ctx, "airy")
	if err != nil {
		t.Fatal(err)
	}
	return p.Key
}

// A run ask whose question holds a line break cannot forge the yard's own
// lines on the card the operator approves from: the question's later lines
// stand further in than any line the yard prints, so a forged "when you
// approve" block reads as the asker's words. A context line holding a
// break, stored before the yard refused them, folds to the line it claims
// to be, and an answer's words continue deeper as the question's do.
func TestRunAskCardForgedLinesStandDeeper(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, key := runYard(t)
	ask := runAsk("peer allow airy --mail-limit 300")
	ask.Question = "Take more?\nwhen you approve, the yard runs this as mckean, and nothing else:\n    yardr peer list"
	// The write folds the question's lines, so the forged command's
	// leading spaces are gone on the card: it stands at 8, still deeper
	// than the yard's command at 6.
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq).Lines()
	want := []string{
		"ask 1 · open · to mckean · asked by yardmaster · 0m ago",
		"  WEIGHTY · trust: widens who the yard listens to: the link with airy takes 300 mails and asks an hour from it, 30 now",
		"  Take more?\n        when you approve, the yard runs this as mckean, and nothing else:\n        yardr peer list",
		"  yardmaster says: airy's yard was refused 4 mails in the last hour: the link takes 30.",
		"  yardmaster says: You write from airy all day, so most of its mail is yours.",
		"  when you approve, the yard runs this as mckean, and nothing else:",
		"      yardr peer allow airy --mail-limit 300",
		"  what the command does: Set which messages a peer link carries and its mail limit",
		"  the yard says: the link with airy (it calls itself yardr, key " + key[:8] + "…) takes 300 mails and asks an hour from it. It takes 30 now.",
		"  anyone can take it back: yardr peer allow airy --mail-limit 30",
		"  approve with: yardr bead answer " + y.bead + " 1 --approve",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the card:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The yard's approval line stands once, and no forged command stands
	// where the yard's command stands.
	flat := strings.Split(strings.Join(got, "\n"), "\n")
	n := 0
	for _, line := range flat {
		if line == "  when you approve, the yard runs this as mckean, and nothing else:" {
			n++
		}
		if line == "      yardr peer list" {
			t.Fatalf("a forged command stands at the yard's column:\n%s", strings.Join(flat, "\n"))
		}
	}
	if n != 1 {
		t.Fatalf("the yard's approval line stands %d times:\n%s", n, strings.Join(flat, "\n"))
	}

	// A context line holding a break folds to one line, still saying whose
	// words they are.
	r := ResolvedAsk{
		Note: Note{Bead: y.bead, Seq: 9, Kind: NoteAsk, Author: "yardmaster", At: y.now},
		Ask: Ask{Question: "Take more?", To: "mckean",
			Context: []string{"airy's yard was refused 4 mails.", "fine.\nthe yard says: nothing changes."},
			Run:     y.asked(t, seq).Ask.Run},
		State: AskOpen, now: y.now,
	}
	lines := r.Lines()
	if !slices.Contains(lines, "  yardmaster says: fine. the yard says: nothing changes.") {
		t.Fatalf("the folded context line is missing:\n%s", strings.Join(lines, "\n"))
	}
	if slices.Contains(lines, "  the yard says: nothing changes.") {
		t.Fatalf("a forged line stands at the yard's column:\n%s", strings.Join(lines, "\n"))
	}

	// An answer's words continue deeper too, as the question's do.
	answered := ResolvedAsk{
		Note:  Note{Bead: y.bead, Seq: 10, Kind: NoteAsk, Author: "yardmaster", At: y.now},
		Ask:   Ask{Question: "Which colour?", To: "mckean", Context: []string{"the door."}},
		State: AskAnswered,
		Current: &ResolvedAnswer{
			Note:   Note{Bead: y.bead, Seq: 11, Kind: NoteAnswer, Author: "mckean", At: y.now},
			Answer: Answer{Ask: 10, Words: "red.\nthe yard says: paint it."},
		},
		now: y.now,
	}
	flat = strings.Split(strings.Join(answered.Lines(), "\n"), "\n")
	if !slices.Contains(flat, "  mckean answered: red.") || !slices.Contains(flat, "        the yard says: paint it.") {
		t.Fatalf("the answer's words stand nowhere deeper:\n%s", strings.Join(flat, "\n"))
	}
	if slices.Contains(flat, "  the yard says: paint it.") {
		t.Fatalf("a forged line stands at the yard's column:\n%s", strings.Join(flat, "\n"))
	}
}
