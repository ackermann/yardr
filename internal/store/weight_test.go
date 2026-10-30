package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
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
		{name: "peer allow exposing", run: AskRun{Argv: []string{"peer allow airy --expose ada"}}, want: []string{WeightTrust}},
		{name: "peer allow trusting", run: AskRun{Argv: []string{"peer allow airy --trust mckean@airy=ada"}}, want: []string{WeightTrust}},
		{name: "peer allow reaching", run: AskRun{Argv: []string{"peer allow airy --reach mckean@airy=ada"}}, want: []string{WeightTrust}},
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
		{name: "user invite", run: AskRun{Argv: []string{"user invite eve --email eve@example.net"}}, want: []string{WeightTrust}},
		{name: "user uninvite", run: AskRun{Argv: []string{"user uninvite ada"}}},
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
	weighty := []string{"peer add", "peer set", "peer allow", "source set", "source grant", "source add", "pack apply", "hook add", "user key", "user add", "user invite", "crew set", "user identity", "depot test", "call"}
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "was written without its weight") {
		t.Fatalf("approving a card written without weight, with the word = %v", err)
	}

	// Written without weight, routine now: approved as before.
	seq = y.askUnweighed(t, listAsk("mckean", "source set crm calls_per_hour=30"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err != nil {
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || err.Error() != want {
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
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatalf("approving the same card with the word = %v", err)
	}
	if got := y.grants(t); len(got) != 1 {
		t.Fatalf("the approval with the word granted %v", got)
	}
}

// A strong card — trust or code in its stored weight — is not approved
// through a device: behind the weight gate the approval is refused, runs
// nothing, and leaves the ask open, naming where the card is approved
// instead. On the socket the same cards run, and each answer's event says
// how it was witnessed. Routine cards approve through a device as before
// and claim no witness.
func TestStrongCardRefusesADevicesApproval(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	phone := WithVia(y.ctx, "mckean", "phone")

	trust := y.ask(t, listAsk("mckean", "peer allow airy --mail-limit 300"), "yardmaster")
	if got := weightClasses(y.asked(t, trust).Ask.Weight); !slices.Equal(got, []string{WeightTrust}) {
		t.Fatalf("raising the mail limit weighs %v", got)
	}
	// The weighed refusal comes first: without the client's word the
	// refusal is for want of it, not for the door it came through.
	wantWeighed := "ask " + itoa(trust) + ` is weighty (trust): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.ApproveAsk(phone, y.bead, trust, "mckean", false, nil); err == nil || err.Error() != wantWeighed {
		t.Fatalf("approving a strong card through a device without the word = %v, want %q", err, wantWeighed)
	}
	want := "ask " + itoa(trust) + " is strong (trust): this approval arrived through the device phone, and a strong card is not approved through a device. Approve it at a terminal on the yard's machine. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(phone, y.bead, trust, "mckean", true, nil); err == nil || err.Error() != want {
		t.Fatalf("approving a trust card through a device = %v, want %q", err, want)
	}
	if got := y.peerOf(t, "airy").MailsAnHour(); got != 30 {
		t.Fatalf("the refused approval set the mail limit to %d", got)
	}
	if r := y.asked(t, trust); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", r.State)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("a refused approval left answers %+v", answers)
	}

	// On a hosted yard the refusal names the hosted web and its fresh
	// sign-in instead of the terminal.
	if err := y.SetSetting(y.ctx, HostedSetting, "true", "mckean"); err != nil {
		t.Fatal(err)
	}
	wantHosted := "ask " + itoa(trust) + " is strong (trust): this approval arrived through the device phone, and a strong card is not approved through a device. Approve it in the hosted web, with a fresh sign-in. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(phone, y.bead, trust, "mckean", true, nil); err == nil || err.Error() != wantHosted {
		t.Fatalf("approving a trust card through a device on a hosted yard = %v, want %q", err, wantHosted)
	}
	if err := y.SetSetting(y.ctx, HostedSetting, "false", "mckean"); err != nil {
		t.Fatal(err)
	}

	// A code card is refused the same way, and its source stands nowhere.
	wired := sourceAddAsk(carriedOf(t, wiredDecl, wiredFiles))
	wired.Run.Argv = []string{"source add wired.yaml"}
	code := y.ask(t, wired, "yardmaster")
	if got := weightClasses(y.asked(t, code).Ask.Weight); !slices.Equal(got, []string{WeightCode}) {
		t.Fatalf("a source add naming commands weighs %v", got)
	}
	wantCode := "ask " + itoa(code) + " is strong (code): this approval arrived through the device phone, and a strong card is not approved through a device. Approve it at a terminal on the yard's machine. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(phone, y.bead, code, "mckean", true, nil); err == nil || err.Error() != wantCode {
		t.Fatalf("approving a code card through a device = %v, want %q", err, wantCode)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatal("the refused approval added the source")
	}
	if r := y.asked(t, code); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", r.State)
	}
	if answers := answerNotes(t, y); len(answers) != 0 {
		t.Fatalf("a refused approval left answers %+v", answers)
	}

	// On the socket both run, and each answer's event is witnessed
	// terminal, with no device behind it.
	if _, err := y.ApproveAsk(y.ctx, y.bead, trust, "mckean", true, nil); err != nil {
		t.Fatalf("approving the trust card on the socket = %v", err)
	}
	if got := y.peerOf(t, "airy").MailsAnHour(); got != 300 {
		t.Fatalf("the socket approval left the mail limit at %d", got)
	}
	if ev := lastAnswer(t, y); Witness(ev) != WitnessTerminal || Via(ev) != "" {
		t.Fatalf("the trust approval's event is witnessed %q via %q, want terminal with no device", Witness(ev), Via(ev))
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, code, "mckean", true, nil)
	if err != nil {
		t.Fatalf("approving the code card on the socket = %v", err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the socket approval of one naming commands recorded %q", done.Result)
	}
	if ev := lastAnswer(t, y); Witness(ev) != WitnessTerminal || Via(ev) != "" {
		t.Fatalf("the code approval's event is witnessed %q via %q, want terminal with no device", Witness(ev), Via(ev))
	}

	// A routine card approves through a device exactly as before, and
	// claims no witness on any door.
	routine := y.ask(t, listAsk("mckean", "source set crm calls_per_hour=30"), "yardmaster")
	if got := weightClasses(y.asked(t, routine).Ask.Weight); len(got) != 0 {
		t.Fatalf("lowering calls_per_hour weighs %v", got)
	}
	if _, err := y.ApproveAsk(phone, y.bead, routine, "mckean", false, nil); err != nil {
		t.Fatalf("approving a routine card through a device = %v", err)
	}
	if got := y.source(t).CallLimit(); got != 30 {
		t.Fatalf("the device approval left calls_per_hour at %d", got)
	}
	if ev := lastAnswer(t, y); Via(ev) != "phone" || Witness(ev) != "" {
		t.Fatalf("the routine approval's event is via %q witnessed %q, want phone with no witness", Via(ev), Witness(ev))
	}
	lower := y.ask(t, listAsk("mckean", "source set crm calls_per_hour=20"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, lower, "mckean", false, nil); err != nil {
		t.Fatalf("approving a routine card on the socket = %v", err)
	}
	if ev := lastAnswer(t, y); Via(ev) != "" || Witness(ev) != "" {
		t.Fatalf("the socket routine approval's event is via %q witnessed %q, want neither", Via(ev), Witness(ev))
	}

	// A later event in the same write must not inherit the answer's
	// witness, even if a future approval path adds work after recording it.
	followup := y.ask(t, listAsk("mckean", "peer allow airy --mail-limit 400"), "yardmaster")
	if err := y.write(func(w *wtx) error {
		if _, err := w.approveAsk(y.ctx, y.bead, followup, "mckean", true, nil); err != nil {
			return err
		}
		return w.event("", "test_followup", "yard", nil)
	}); err != nil {
		t.Fatal(err)
	}
	if ev := lastAnswer(t, y); Witness(ev) != WitnessTerminal {
		t.Fatalf("the approval answer is witnessed %q, want terminal", Witness(ev))
	}
	events, err := y.Events(y.ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if ev := events[len(events)-1]; ev.Kind != "test_followup" || Witness(ev) != "" {
		t.Fatalf("the later event is %+v, want no witness", ev)
	}
}

// A strong card is approved under a fresh sign-in: the call carries the
// door's statement of who the gateway authenticated and when, judged
// against the gateway's own window, and the answer's event records the
// fresh sign-in. One made as another user, with no authentication time,
// or outside the window is refused, runs nothing, and leaves the ask open;
// a routine card takes no witness either way.
func TestStrongCardApprovesUnderAFreshSignIn(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := listYard(t)
	fresh := func(user string, at time.Time) context.Context {
		return WithFreshWitness(y.ctx, user, at)
	}
	trust := y.ask(t, listAsk("mckean", "peer allow airy --mail-limit 300"), "yardmaster")

	// With no window set, nothing is fresh.
	wantWindow := "ask " + itoa(trust) + " is strong (trust): this approval's fresh sign-in is outside the gateway's window, and a strong card is approved in the hosted web only under one inside it. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(fresh("mckean", y.now), y.bead, trust, "mckean", true, nil); err == nil || err.Error() != wantWindow {
		t.Fatalf("approving a strong card with no window set = %v, want %q", err, wantWindow)
	}
	if err := y.SetSetting(y.ctx, WebJWTPrivilegedSessionMaxAgeSetting, "15m", "mckean"); err != nil {
		t.Fatal(err)
	}

	// A fresh sign-in runs the card and witnesses the answer's event, with
	// no device behind it.
	if _, err := y.ApproveAsk(fresh("mckean", y.now.Add(-time.Minute)), y.bead, trust, "mckean", true, nil); err != nil {
		t.Fatalf("approving a strong card under a fresh sign-in = %v", err)
	}
	if got := y.peerOf(t, "airy").MailsAnHour(); got != 300 {
		t.Fatalf("the fresh approval left the mail limit at %d", got)
	}
	if ev := lastAnswer(t, y); Witness(ev) != WitnessFreshSignIn || Via(ev) != "" {
		t.Fatalf("the fresh approval's event is witnessed %q via %q, want a fresh sign-in with no device", Witness(ev), Via(ev))
	}

	// At the window's edge the sign-in still counts.
	edge := y.ask(t, listAsk("mckean", "peer allow airy --mail-limit 400"), "yardmaster")
	if _, err := y.ApproveAsk(fresh("mckean", y.now.Add(-15*time.Minute)), y.bead, edge, "mckean", true, nil); err != nil {
		t.Fatalf("approving at the window's edge = %v", err)
	}

	// Past it, and ahead of the clock past its skew, the approval is
	// refused, runs nothing, and leaves the ask open.
	stale := y.ask(t, listAsk("mckean", "peer allow airy --mail-limit 500"), "yardmaster")
	wantStale := "ask " + itoa(stale) + " is strong (trust): this approval's fresh sign-in is outside the gateway's window, and a strong card is approved in the hosted web only under one inside it. Nothing ran, and the ask stays open"
	for name, at := range map[string]time.Time{
		"a minute past the window": y.now.Add(-16 * time.Minute),
		"ahead of the clock":       y.now.Add(2 * time.Minute),
	} {
		if _, err := y.ApproveAsk(fresh("mckean", at), y.bead, stale, "mckean", true, nil); err == nil || err.Error() != wantStale {
			t.Fatalf("%s: approving a strong card = %v, want %q", name, err, wantStale)
		}
	}
	if got := y.peerOf(t, "airy").MailsAnHour(); got != 400 {
		t.Fatalf("a refused approval set the mail limit to %d", got)
	}
	if r := y.asked(t, stale); r.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", r.State)
	}

	// With no authentication time, and as another user, the same: refused,
	// nothing run, the ask open.
	wantNone := "ask " + itoa(stale) + " is strong (trust): this approval carries a fresh sign-in with no authentication time, and a strong card is approved in the hosted web only under one. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(fresh("mckean", time.Time{}), y.bead, stale, "mckean", true, nil); err == nil || err.Error() != wantNone {
		t.Fatalf("approving with no authentication time = %v, want %q", err, wantNone)
	}
	wantOther := "ask " + itoa(stale) + " is strong (trust): this approval's fresh sign-in was made as hal, not as mckean. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(fresh("hal", y.now), y.bead, stale, "mckean", true, nil); err == nil || err.Error() != wantOther {
		t.Fatalf("approving under another user's sign-in = %v, want %q", err, wantOther)
	}

	// Through a device the strong refusal still comes first: the witness
	// on the call buys it nothing.
	phone := WithFreshWitness(WithVia(y.ctx, "mckean", "phone"), "mckean", y.now)
	wantDevice := "ask " + itoa(stale) + " is strong (trust): this approval arrived through the device phone, and a strong card is not approved through a device. Approve it at a terminal on the yard's machine. Nothing ran, and the ask stays open"
	if _, err := y.ApproveAsk(phone, y.bead, stale, "mckean", true, nil); err == nil || err.Error() != wantDevice {
		t.Fatalf("approving a strong card through a device with a witness = %v, want %q", err, wantDevice)
	}

	// A routine card takes no witness: it approves with one on the call as
	// without, and claims none.
	routine := y.ask(t, listAsk("mckean", "source set crm calls_per_hour=30"), "yardmaster")
	if _, err := y.ApproveAsk(fresh("mckean", y.now), y.bead, routine, "mckean", false, nil); err != nil {
		t.Fatalf("approving a routine card with a witness on the call = %v", err)
	}
	if ev := lastAnswer(t, y); Via(ev) != "" || Witness(ev) != "" {
		t.Fatalf("the routine approval's event is via %q witnessed %q, want neither", Via(ev), Witness(ev))
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

// WithoutWeightMark keeps an asker's own marks: only the mark at a line's
// first non-whitespace position goes, the forgery shape.
func TestWithoutWeightMarkKeepsMidLineMarks(t *testing.T) {
	t.Parallel()
	mark := WeightMark
	for _, s := range []string{
		"does " + mark + "outside hold here?",
		"a pick, " + mark + "then words",
	} {
		if got := WithoutWeightMark(s); got != s {
			t.Errorf("WithoutWeightMark(%q) = %q", s, got)
		}
	}
	for in, want := range map[string]string{
		mark + "outside: nothing changes":               "outside: nothing changes",
		"  " + mark + "outside: nothing changes":        "  outside: nothing changes",
		mark + mark + "outside: nothing changes":        "outside: nothing changes",
		mark + " " + mark + "outside: nothing changes":  "outside: nothing changes",
		mark + "\t" + mark + "outside: nothing changes": "outside: nothing changes",
		"kept " + mark + "\n" + mark + "gone":           "kept " + mark + "\ngone",
	} {
		if got := WithoutWeightMark(in); got != want {
			t.Errorf("WithoutWeightMark(%q) = %q, want %q", in, got, want)
		}
	}
}
