package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// weighs is the classes the yard gives the command a card would propose,
// with what the card would carry beside it, weighed as the row weighs it
// in the write that stores the ask and in the approval's.
func (y *askYard) weighs(t *testing.T, run AskRun) []string {
	t.Helper()
	c, argv, err := parseRow(run.Argv)
	if err != nil {
		t.Fatal(err)
	}
	run.Argv = argv
	var ws []AskWeight
	if err := y.write(func(w *wtx) (err error) {
		ws, err = w.weighRun(c, run)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return weightClasses(ws)
}

func weightClasses(ws []AskWeight) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Class)
	}
	return out
}

// askUnweighed puts ask on the bead as an ask written before cards were
// weighed: as the yard stored it, without its weight.
func (y *askYard) askUnweighed(t *testing.T, ask Ask, actor string) uint64 {
	t.Helper()
	scratch, err := y.CreateBead(y.ctx, NewBead{Title: "scratch"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.AddAsk(y.ctx, scratch.ID, ask, actor)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := y.Notes(y.ctx, scratch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return y.putUnweighed(t, y.bead, storedAsk(t, notes, seq), actor)
}

// putUnweighed writes ask on the bead with no weight, as a note from
// before cards were weighed.
func (y *askYard) putUnweighed(t *testing.T, bead string, ask Ask, actor string) uint64 {
	t.Helper()
	ask.Weight = nil
	text, err := json.Marshal(ask)
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	if err := y.write(func(w *wtx) error {
		row, err := w.loadBead(bead)
		if err != nil {
			return err
		}
		n, err := w.addNoted(row, NoteAsk, string(text), actor)
		if err != nil {
			return err
		}
		seq = n.Seq
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seq
}

func storedAsk(t *testing.T, notes []Note, seq uint64) Ask {
	t.Helper()
	for _, n := range notes {
		if n.Seq == seq && n.Kind == NoteAsk {
			a, err := ParseAsk(n)
			if err != nil {
				t.Fatal(err)
			}
			return a
		}
	}
	t.Fatalf("no ask %d", seq)
	return Ask{}
}

// crmWith is runCRM with one more read call, which a standing grant of
// read would reach once the declaration replaces crm's.
const crmWith = runCRM + `  list: { kind: read, request: { method: GET, path: /lists } }
`

// Each row of the table of design yardr-ekmx 3.1 weighs as the table
// says, and a narrowing form of each is routine: the yard weighs the
// command against its records, never the asker's words.
func TestWeightOfEachCard(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := presetYard(t, presetStandard())
	y.Store.SetPresetReader((&presetFake{pack: "crm-pack", label: "embedded in yardr test1",
		calls: map[string][]string{"standard": {"read", "note"}, "reader": {"read", "get"}}}).read)
	if _, err := y.AllowPeer(y.ctx, "airy", PeerAllow{Send: []string{PeerAsk}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	src := y.source(t)
	file := func(prog, text string) AskFile {
		return AskFile{Program: prog, Text: text, SHA256: sha256Hex(text)}
	}
	newSource := `name: fresh
base: https://fresh.example.com
thread: { argv: [./thread.sh] }
calls:
  find: { kind: read, request: { method: GET, path: /f } }
`
	for _, c := range []struct {
		name string
		prep func()
		run  AskRun
		want []string
	}{
		{name: "peer allow raising the mail limit", run: AskRun{Argv: []string{"peer allow airy --mail-limit 300"}}, want: []string{WeightTrust}},
		{name: "peer allow lowering it", run: AskRun{Argv: []string{"peer allow airy --mail-limit 10"}}},
		{name: "peer allow adding a kind", run: AskRun{Argv: []string{"peer allow airy --receive ask"}}, want: []string{WeightTrust}},
		{name: "peer allow stopping one", run: AskRun{Argv: []string{"peer allow airy --no-send ask"}}},
		{name: "source set raising updates_per_hour", run: AskRun{Argv: []string{"source set crm updates_per_hour=20"}}, want: []string{WeightOutside}},
		{name: "source set lowering it", run: AskRun{Argv: []string{"source set crm updates_per_hour=5"}}},
		{name: "source grant of an update call", run: AskRun{Argv: []string{"source grant crm note --crew yardmaster"}}, want: []string{WeightOutside}},
		{name: "source grant of read", run: AskRun{Argv: []string{"source grant crm read --crew yardmaster"}}},
		{name: "source grant of a read call", run: AskRun{Argv: []string{"source grant crm find --group builders"}}},
		{name: "source grant of a preset with an update", run: AskRun{Argv: []string{"source grant crm --preset standard --crew yardmaster"}}, want: []string{WeightOutside}},
		{name: "source grant of a preset of reads", run: AskRun{Argv: []string{"source grant crm --preset reader --crew yardmaster"}}},
		{name: "source grant as a user", run: AskRun{Argv: []string{"source grant crm read --crew yardmaster --as ada"}}, want: []string{WeightAsUser}},
		{name: "source grant of an update as a user", run: AskRun{Argv: []string{"source grant crm note --crew yardmaster --as ada"}}, want: []string{WeightOutside, WeightAsUser}},
		{name: "source add with a command file", run: AskRun{Argv: []string{"source add fresh.yaml"},
			Carried: []AskSource{{Name: "fresh", Text: newSource, Files: []AskFile{file("./thread.sh", "#!/bin/sh\n")}}}}, want: []string{WeightCode}},
		{name: "source add of no command", run: AskRun{Argv: []string{"source add crm.yaml"},
			Carried: []AskSource{{Name: "fresh", Text: strings.Replace(newSource, "thread: { argv: [./thread.sh] }\n", "", 1)}}}},
		{name: "pack apply changing a command file", run: AskRun{Argv: []string{"pack apply crm-pack"},
			Carried: []AskSource{{Name: "crm", Text: src.Text, Files: []AskFile{file("./thread.sh", "#!/bin/sh\nnew\n")},
				Inventory: []AskInventory{{Path: "./thread.sh", SHA256: sha256Hex("#!/bin/sh\n")}}}}}, want: []string{WeightCode}},
		{name: "pack apply of the copies that stand", run: AskRun{Argv: []string{"pack apply crm-pack"},
			Carried: []AskSource{{Name: "crm", Text: src.Text, Files: []AskFile{file("./thread.sh", "#!/bin/sh\n")},
				Inventory: []AskInventory{{Path: "./thread.sh", SHA256: sha256Hex("#!/bin/sh\n")}}}}}},
		{name: "pack apply widening a grant of read", prep: func() {
			if _, err := y.GrantSource(y.ctx, "crm", GrantRead, Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
				t.Fatal(err)
			}
		}, run: AskRun{Argv: []string{"pack apply crm-pack"}, Carried: []AskSource{{Name: "crm", Text: crmWith}}}, want: []string{WeightReads}},
		{name: "pack apply dropping a read call", run: AskRun{Argv: []string{"pack apply crm-pack"},
			Carried: []AskSource{{Name: "crm", Text: strings.Replace(runCRM, "  find: { kind: read, request: { method: GET, path: /contacts } }\n", "", 1)}}}},
		{name: "hook add", run: AskRun{Argv: []string{"hook add bot --source crm"}}, want: []string{WeightTrust}},
		{name: "user key", run: AskRun{Argv: []string{"user key ada crm"}}, want: []string{WeightCredential}},
		{name: "user add", run: AskRun{Argv: []string{"user add eve"}}, want: []string{WeightTrust}},
		{name: "user rm", run: AskRun{Argv: []string{"user rm ada"}}},
		{name: "crew set adding a peer", run: AskRun{Argv: []string{"crew set yardmaster peers=airy"}}, want: []string{WeightTrust}},
		{name: "crew set of threads", run: AskRun{Argv: []string{"crew set yardmaster open_threads=crm"}}},
		{name: "crew set taking the peer away", prep: func() {
			y.crew(t, map[string]any{"kind": "claude", MailPeers: []any{"airy"}})
		}, run: AskRun{Argv: []string{"crew set yardmaster --unset peers"}}},
		{name: "user identity", run: AskRun{Argv: []string{"user identity ada --source crm --id 7"}}, want: []string{WeightAsUser}},
		{name: "user set tell", run: AskRun{Argv: []string{"user set ada tell=asks"}}},
		{name: "timer rm", run: AskRun{Argv: []string{"timer rm t1"}}},
	} {
		if c.prep != nil {
			c.prep()
		}
		if got := y.weighs(t, c.run); !slices.Equal(got, c.want) {
			t.Errorf("%s weighs %v, want %v", c.name, got, c.want)
		}
	}

	// Secret asks give a value whatever their form; one for a user's own
	// credential lets a call made as them read it.
	if got := weightClasses(secretWeight(AskSecret{Source: "crm", Env: []string{"CRM_TOKEN"}, Host: "https://crm.example.com"})); !slices.Equal(got, []string{WeightCredential}) {
		t.Errorf("a source's credential weighs %v", got)
	}
	if got := weightClasses(secretWeight(AskSecret{Source: "crm", Env: []string{"CRM_TOKEN"}, As: "ada"})); !slices.Equal(got, []string{WeightCredential, WeightAsUser}) {
		t.Errorf("a user's own credential weighs %v", got)
	}
	// A mapping card's picks write a user's identity.
	if got := weightClasses(mappingWeight(AskIdentity{Source: "talk", Sender: "U1"})); !slices.Equal(got, []string{WeightAsUser}) {
		t.Errorf("a mapping card weighs %v", got)
	}
	// A setup takes the account, is outside where a preset grants an
	// update, and brings what its pack carries; one of reads alone and no
	// commands gives the credential only.
	carried := AskSource{Name: "crm", Text: runCRM}
	setup := func(presets ...AskPreset) []string {
		var ws []AskWeight
		if err := y.write(func(w *wtx) (err error) {
			ws, err = w.setupWeight(AskSetup{Pack: "crm-pack", Account: []string{"CRM_TOKEN"}, To: []Grantee{{Crew: "yardmaster"}},
				Carried: []AskSource{carried}, Presets: presets})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return weightClasses(ws)
	}
	if got := setup(AskPreset{Name: "reader", Grants: []AskPresetGrant{{Source: "crm", Calls: []string{GrantRead}}}},
		AskPreset{Name: "writer", Grants: []AskPresetGrant{{Source: "crm", Calls: []string{GrantRead, "note"}}}}); !slices.Equal(got, []string{WeightCredential, WeightOutside}) {
		t.Errorf("a setup with an update preset weighs %v", got)
	}
	if got := setup(AskPreset{Name: "reader", Grants: []AskPresetGrant{{Source: "crm", Calls: []string{GrantRead}}}}); !slices.Equal(got, []string{WeightCredential}) {
		t.Errorf("a setup of reads weighs %v", got)
	}
	carried.Files = []AskFile{file("./thread.sh", "#!/bin/sh\n")}
	if got := setup(AskPreset{Name: "reader", Grants: []AskPresetGrant{{Source: "crm", Calls: []string{GrantRead}}}}); !slices.Equal(got, []string{WeightCredential, WeightCode}) {
		t.Errorf("a setup whose pack brings a command weighs %v", got)
	}
	carried.Inventory = []AskInventory{{Path: "./thread.sh", SHA256: sha256Hex("#!/bin/sh\n")}}
	if got := setup(AskPreset{Name: "reader", Grants: []AskPresetGrant{{Source: "crm", Calls: []string{GrantRead}}}}); !slices.Equal(got, []string{WeightCredential, WeightCode}) {
		t.Errorf("a setup whose pack brings the copy that stands weighs %v", got)
	}

	// Every other row is routine whatever it is asked.
	weighty := []string{"peer allow", "source set", "source grant", "source add", "pack apply", "hook add", "user key", "user add", "crew set", "user identity"}
	for _, c := range runList {
		if has := c.weigh != nil; has != slices.Contains(weighty, strings.Join(c.words, " ")) {
			t.Errorf("the row %s weighs: %v", strings.Join(c.words, " "), has)
		}
	}
}

// Raising calls_per_hour is weighty exactly when it raises the lower of
// the two caps, since each update is one of the calls too.
func TestWeightOfCallsPerHour(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	for _, c := range []struct {
		calls, updates string // as set before; "" leaves it unset
		to             string
		want           []string
	}{
		{calls: "2", to: "calls_per_hour=10", want: []string{WeightOutside}}, // 2 -> 10, updates unset at 10
		{calls: "2", to: "calls_per_hour=30", want: []string{WeightOutside}}, // 2 -> 10
		{calls: "60", to: "calls_per_hour=100"},                              // updates stay 10
		{calls: "2", updates: "1", to: "calls_per_hour=10"},                  // updates stay 1
		{calls: "20", to: "calls_per_hour=5"},                                // lowers
		{calls: "5", updates: "3", to: "updates_per_hour=8", want: []string{WeightOutside}},
		{calls: "5", updates: "3", to: "updates_per_hour=2"}, // lowers
		{calls: "5", updates: "5", to: "updates_per_hour=9"}, // calls stay 5
	} {
		for key, v := range map[string]string{SourceCallsPerHour: c.calls, SourceUpdatesPerHour: c.updates} {
			if _, err := y.SetSource(y.ctx, "crm", key, v, "mckean"); err != nil {
				t.Fatal(err)
			}
		}
		if got := y.weighs(t, AskRun{Argv: []string{"source set crm " + c.to}}); !slices.Equal(got, c.want) {
			t.Errorf("calls %s, updates %s, %s weighs %v, want %v", c.calls, or(c.updates, "unset"), c.to, got, c.want)
		}
	}
}

// The weight is the yard's: what a writer sends for it is thrown away, a
// routine ask is stored with none, and a weighty one with the yard's.
func TestWeightIsTheYards(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	forged := []AskWeight{{Class: WeightCode, Line: "nothing to see"}}
	plain := Ask{Question: "Which colour?", To: "mckean", Context: []string{"the door."}, Weight: forged}
	notes := func() []Note {
		n, err := y.Notes(y.ctx, y.bead)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	seq := y.ask(t, plain, "yardmaster")
	if got := storedAsk(t, notes(), seq).Weight; got != nil {
		t.Fatalf("a plain ask is stored weighing %+v", got)
	}
	ask := listAsk("mckean", "source grant crm note --crew yardmaster")
	ask.Weight = forged
	seq = y.ask(t, ask, "yardmaster")
	got := storedAsk(t, notes(), seq).Weight
	want := []AskWeight{{Class: WeightOutside, Line: "lets the yard change something outside it: crew member yardmaster may call note of crm (an update)"}}
	if !slices.Equal(got, want) {
		t.Fatalf("a grant of an update is stored weighing %+v, want %+v", got, want)
	}
	routine := listAsk("mckean", "source set crm calls_per_hour=30")
	routine.Weight = forged
	seq = y.ask(t, routine, "yardmaster")
	if got := storedAsk(t, notes(), seq).Weight; got != nil {
		t.Fatalf("a routine run ask is stored weighing %+v", got)
	}
}

// An approval weighs the card again: where the weight is not the one the
// card showed, or the card showed none and weighs as weighty now, it is
// stale, nothing runs and the ask stays open. A routine card written
// without weight is approved as before.
func TestWeightIsHeldAtApproval(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)

	// Written without weight, weighty now.
	seq := y.askUnweighed(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	y.refusedStale(t, seq, "ask "+itoa(seq)+" was written without its weight, and the yard weighs it weighty (outside) now")
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("a stale approval granted %v", got)
	}
	// The client's word that its person saw the weight is no stand-in for
	// a card that never showed it: the refusal is the stale one still.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err == nil || !strings.Contains(err.Error(), "was written without its weight") {
		t.Fatalf("approving a card written without weight, with the word = %v", err)
	}

	// Written without weight, routine now: approved as before.
	seq = y.askUnweighed(t, listAsk("mckean", "source set crm calls_per_hour=30"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err != nil {
		t.Fatalf("approving a routine ask written without weight = %v", err)
	}

	// Weighed when written, and the weight moved since while the card's
	// sentences did not: raising calls from 2 to 10 lets 8 more updates
	// through, until updates are held at 1.
	if _, err := y.SetSource(y.ctx, "crm", SourceCallsPerHour, "2", "mckean"); err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, listAsk("mckean", "source set crm calls_per_hour=10"), "yardmaster")
	if got := weightClasses(y.asked(t, seq).Ask.Weight); !slices.Equal(got, []string{WeightOutside}) {
		t.Fatalf("raising calls from 2 to 10 weighs %v", got)
	}
	if _, err := y.SetSource(y.ctx, "crm", SourceUpdatesPerHour, "1", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "ask "+itoa(seq)+" was weighed weighty (outside) when it was written, and the yard weighs it routine now")
	if got := y.source(t).CallLimit(); got != 2 {
		t.Fatalf("a stale approval set calls_per_hour to %d", got)
	}

	// A weighty card that showed its weight is approved only by a client
	// that says its person saw it (weighed): without the word the approval
	// is refused in the words of design yardr-ekmx 3.3, changes nothing,
	// and leaves the ask open; with it the same ask is approved.
	seq = y.ask(t, listAsk("mckean", "source grant crm note --crew yardmaster"), "yardmaster")
	if got := weightClasses(y.asked(t, seq).Ask.Weight); !slices.Equal(got, []string{WeightOutside}) {
		t.Fatalf("the grant weighs %v", got)
	}
	want := "ask " + itoa(seq) + ` is weighty (outside): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false); err == nil || err.Error() != want {
		t.Fatalf("approving a weighty card without the word = %v, want %q", err, want)
	}
	if got := y.grants(t); len(got) != 0 {
		t.Fatalf("an approval without the word granted %v", got)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", r.State)
	}
	if answers := answerNotes(t, y); len(answers) != 1 {
		t.Fatalf("a refused approval left answers %+v, want the one routine approval above", answers)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err != nil {
		t.Fatalf("approving the same card with the word = %v", err)
	}
	if got := y.grants(t); len(got) != 1 {
		t.Fatalf("the approval with the word granted %v", got)
	}
}

// A secret written without weight is given no more: every secret is
// weighty.
func TestWeightIsHeldAtGiving(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, file := secretYard(t)
	seq := y.askUnweighed(t, secretAsk("crm"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "tok"}, "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "was written without its weight, and the yard weighs it weighty (credential) now") ||
		!strings.HasSuffix(err.Error(), nothingRan) {
		t.Fatalf("giving a secret written without weight = %v", err)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("a stale giving left answers %+v", answers)
	}
	// Written now, it carries its weight and is given.
	seq = y.ask(t, secretAsk("crm"), "yardmaster")
	if got := weightClasses(y.asked(t, seq).Ask.Weight); !slices.Equal(got, []string{WeightCredential}) {
		t.Fatalf("a secret ask weighs %v", got)
	}
	// Without the client's word that its person saw the weight (weighed),
	// the giving is refused in the words of design yardr-ekmx 3.3 before
	// anything is written: no file, no answer, and the ask stays open.
	want := "ask " + itoa(seq) + ` is weighty (credential): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "tok"}, "mckean", false); err == nil || err.Error() != want {
		t.Fatalf("giving a secret without the word = %v, want %q", err, want)
	} else if strings.Contains(err.Error(), "tok") {
		t.Fatalf("the refusal repeats the value: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote %s (%v)", file, err)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("a refused giving left answers %+v", answers)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", r.State)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq, map[string]string{"CRM_TOKEN": "tok"}, "mckean", true); err != nil {
		t.Fatal(err)
	}
	if text, err := os.ReadFile(file); err != nil || string(text) != "CRM_TOKEN=tok\n" {
		t.Fatalf("the file holds %q (%v)", text, err)
	}
}

// A mapping card is written weighty, and one from before cards were
// weighed writes no identity on a pick.
func TestWeightIsHeldAtAMappingPick(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := mappingYard(t)
	card := claimCard(t, y, "U1")
	if got := weightClasses(card.Weight); !slices.Equal(got, []string{WeightAsUser}) {
		t.Fatalf("a mapping card weighs %v", got)
	}
	notes, err := y.Notes(y.ctx, card.Bead)
	if err != nil {
		t.Fatal(err)
	}
	ask := storedAsk(t, notes, card.Ask)
	if ask.Options[0].Label != "ada" {
		t.Fatalf("the card's first pick is %q, want ada", ask.Options[0].Label)
	}
	seq := y.putUnweighed(t, card.Bead, ask, stageAskAuthor)
	err = y.AddAnswer(y.ctx, card.Bead, Answer{Ask: seq, Option: 1}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "was written without its weight, and the yard weighs it weighty (as-user) now") {
		t.Fatalf("a pick on a mapping card written without weight = %v", err)
	}
	u, err := y.GetUser(y.ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := u.Identities["talk"]; ok {
		t.Fatalf("a stale pick wrote ada's identity %+v", u.Identities)
	}

	// A pick that writes an identity is a weighty approval: it takes the
	// client's word that its person saw the weight (weighed), and without
	// it, through AnswerAsk or through AddAnswer, which carries none, it is
	// refused in the words of design yardr-ekmx 3.3 and the card stays open.
	want := "ask " + itoa(card.Ask) + ` is weighty (as-user): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, 1, "", "mckean", false); err == nil || err.Error() != want {
		t.Fatalf("a pick without the word = %v, want %q", err, want)
	}
	if err := y.AddAnswer(y.ctx, card.Bead, Answer{Ask: card.Ask, Option: 1}, "mckean"); err == nil || err.Error() != want {
		t.Fatalf("a pick through AddAnswer = %v, want %q", err, want)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || len(u.Identities) != 0 {
		t.Fatalf("a refused pick wrote ada's identity %+v (%v)", u.Identities, err)
	}
	wantCardOpen(t, y, card.Bead)
	// Nobody and You decide write no identity, so they ask for no word.
	later := claimCard(t, y, "U2")
	if err := y.AnswerAsk(y.ctx, later.Bead, later.Ask, pickFor(t, y, later.Bead, YouDecide), "", "mckean", false); err != nil {
		t.Fatalf("you decide without the word = %v", err)
	}
	if err := y.AnswerAsk(y.ctx, later.Bead, later.Ask, pickFor(t, y, later.Bead, MappingNobody), "", "mckean", false); err != nil {
		t.Fatalf("nobody without the word = %v", err)
	}
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, 1, "", "mckean", true); err != nil {
		t.Fatalf("the same pick with the word = %v", err)
	}
	if u, err := y.GetUser(y.ctx, "ada"); err != nil || !u.IsIn("talk", "U1") {
		t.Fatalf("the pick with the word left ada's identities %+v (%v)", u.Identities, err)
	}
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
