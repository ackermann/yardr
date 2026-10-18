package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// presetYard is listYard with the server's preset reading faked: pack
// crm-pack of the build label, resolving preset to calls, or failing
// with err. The fake is read at asking and again at approval, as the
// server's reading is, so a test moves the preset by changing what it
// answers.
type presetFake struct {
	pack  string
	label string
	calls map[string][]string
	err   error
}

func (f *presetFake) read(source, preset string) (*PresetReading, error) {
	if f.err != nil {
		return nil, f.err
	}
	calls, ok := f.calls[preset]
	if !ok {
		return nil, fmt.Errorf("pack %s has no preset %s (yardr pack show %s prints its presets numbered): grant by name in a terminal (yardr source grant %s read|<call> --group <group> | --crew <crew member>)", f.pack, preset, f.pack, source)
	}
	return &PresetReading{Pack: f.pack, Preset: preset, Label: f.label, Calls: slices.Clone(calls)}, nil
}

func presetYard(t *testing.T, f *presetFake) *askYard {
	t.Helper()
	y := listYard(t)
	y.Store.SetPresetReader(f.read)
	return y
}

func presetStandard() *presetFake {
	return &presetFake{pack: "crm-pack", label: "embedded in yardr test1",
		calls: map[string][]string{"standard": {"read", "note"}}}
}

// A preset grant names the preset and the pack beside each grantee, and
// renders each of the preset's calls as a grant of it reads: read
// expanded to today's reads with the later-reads line, an update marked
// as changing something there. Its approval grants each call to each
// grantee in one write, and is taken back with a revoke per call.
func TestRunAskPresetGrant(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := presetYard(t, presetStandard())
	seq := y.ask(t, listAsk("mckean", "source grant crm --preset standard --group builders --crew yardmaster"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if want := []string{"source", "grant", "crm", "--preset", "standard", "--group", "builders", "--crew", "yardmaster"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask stores argv %v, want %v", run.Argv, want)
	}
	read := "may make every read call of " + crmAt + ", with the credential the yard holds for it: today that is find (GET /contacts), get (GET /contacts/{id}), and it covers any read call the operator later gives the source."
	note := "may make the call note (POST /contacts/{id}/notes) of " + crmAt + ", with the credential the yard holds for it, which changes something there."
	want := []string{
		"the group builders (exec), and so each session it starts, with the preset standard of pack crm-pack, " + read + " " + note + " It is granted nothing of crm now.",
		"the crew member yardmaster (herdr, claude) with the preset standard of pack crm-pack, " + read + " " + note + " It is granted nothing of crm now.",
	}
	if !slices.Equal(run.Does, want) {
		t.Fatalf("the card says:\n%s\nwant:\n%s", strings.Join(run.Does, "\n"), strings.Join(want, "\n"))
	}
	if want := "yardr source revoke crm read --group builders --crew yardmaster; yardr source revoke crm note --group builders --crew yardmaster"; run.Back != want || run.BackBy != "" {
		t.Fatalf("the card is taken back by %q (%q), want %q by anyone", run.Back, run.BackBy, want)
	}
	if run.Seen["pack crm-pack"] != "embedded in yardr test1" || len(run.Seen["preset crm-pack standard"]) != 64 ||
		len(run.Seen["source crm"]) != 64 || len(run.Seen["group builders"]) != 64 || len(run.Seen["crew yardmaster"]) != 64 {
		t.Fatalf("the card saw %+v", run.Seen)
	}
	if card := y.asked(t, seq).Lines(); !slices.Contains(card, "  the yard says: "+want[1]) ||
		!slices.Contains(card, "  anyone can take it back: "+run.Back) {
		t.Errorf("the card:\n%s", strings.Join(card, "\n"))
	}

	writes := y.writes(t)
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if got := y.writes(t); got != writes+1 {
		t.Errorf("the approval was %d writes of the store, want one", got-writes)
	}
	if want := "group builders may call crm: note (update), read\ncrew member yardmaster may call crm: note (update), read"; done.Result != want {
		t.Fatalf("the approval printed %q, want %q", done.Result, want)
	}
	if got, want := y.grants(t), []string{"crew member yardmaster: note,read", "group builders: note,read"}; !slices.Equal(got, want) {
		t.Fatalf("the grants after the approval = %v, want %v", got, want)
	}
	evs, _ := y.EventsOfKind(y.ctx, "source_granted", 10)
	if len(evs) != 4 {
		t.Fatalf("source_granted events = %+v", evs)
	}
	for _, e := range evs {
		y.approved(t, e, seq)
	}

	// Asking refuses a grantee that holds every call of the preset
	// already, and allows one that holds only some.
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "source grant crm --preset standard --group builders"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "group builders holds every call of the preset standard of pack crm-pack already: leave it out of the command, which would change nothing for it") {
		t.Fatalf("asking for a grantee that holds the preset = %v", err)
	}
	y = presetYard(t, presetStandard())
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Group: "builders"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --group builders"), "yardmaster")
	if does := y.asked(t, seq).Ask.Run.Does[0]; !strings.Contains(does, "It is granted read (every read call) of crm now.") {
		t.Fatalf("the card says %q", does)
	}
}

// A preset grant approval runs past what a grantee was granted since
// unless it holds the whole preset already, as a grant's does: a call
// landed since is granted no second time, and the answer carries the
// card as it stands.
func TestRunAskPresetGrantAfterGrantsLand(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := presetYard(t, presetStandard())
	seq := y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean")
	if err != nil || done.Result != "crew member yardmaster may call crm: note (update), read" {
		t.Fatalf("approving after read landed = %+v, %v", done, err)
	}
	if len(done.Does) != 1 || !strings.Contains(done.Does[0], "It is granted read (every read call) of crm now.") {
		t.Fatalf("the approval carries %q, want the card as it stands", done.Does)
	}
	if got, want := y.grants(t), []string{"crew member yardmaster: note,read"}; !slices.Equal(got, want) {
		t.Fatalf("the grants after the approval = %v, want %v", got, want)
	}

	// Every call of the preset granted since: the approval says so, and
	// grants nothing.
	y = presetYard(t, presetStandard())
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GrantSource(y.ctx, "crm", "note", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "would grant the preset standard of pack crm-pack of the source crm to crew member yardmaster, and crew member yardmaster holds it already: it was granted since")
	if got := y.grants(t); !slices.Equal(got, []string{"crew member yardmaster: note,read"}) {
		t.Fatalf("the refused approval granted %v", got)
	}

	// A read call the preset names is held through read granted since.
	y = presetYard(t, presetStandard())
	y.Store.SetPresetReader((&presetFake{pack: "crm-pack", label: "embedded in yardr test1",
		calls: map[string][]string{"one": {"get"}}}).read)
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset one --crew yardmaster"), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "crm", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "would grant the preset one of pack crm-pack of the source crm to crew member yardmaster, and crew member yardmaster holds it already")
}

// A preset grant approval is refused stale when the preset's bytes, the
// source or a grantee moved: other calls of the preset, another pack,
// and another build each refuse it, as a replaced declaration and a
// reconfigured grantee do.
func TestRunAskPresetGrantStale(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	f := presetStandard()
	y := presetYard(t, f)
	seq := y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	f.calls["standard"] = []string{"read", "note", "get"}
	f.label = "embedded in yardr test2"
	y.refusedStale(t, seq, "was written from the preset standard of pack crm-pack as embedded in yardr test1 embeds it: this yard runs embedded in yardr test2, whose preset differs.")

	f = presetStandard()
	y = presetYard(t, f)
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	f.label = "embedded in yardr test2"
	y.refusedStale(t, seq, "was written from pack crm-pack as embedded in yardr test1 embeds it: the preset is unchanged call for call, but the card named the build it was carded from, and this yard runs embedded in yardr test2.")

	f = presetStandard()
	y = presetYard(t, f)
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	f.pack = "crm-pack-two"
	y.refusedStale(t, seq, "was written from the preset standard of pack crm-pack: the source crm is declared by pack crm-pack-two now.")

	// The source replaced under its name with the same reads reads the
	// same on the card, sentence for sentence, and is another.
	y = presetYard(t, presetStandard())
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster")
	other := strings.Replace(runCRM, `body: { text: "{text}" }`, `body: { text: "{text}", public: true }`, 1)
	if other == runCRM {
		t.Fatal("the test replaces nothing")
	}
	if _, err := y.PutSource(y.ctx, "crm.yaml", other, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.sameCard(t, seq, "source crm")
	y.refusedStale(t, seq, "was written from another declaration of the source crm")

	// A grantee reconfigured under its name reads the same and is
	// another too.
	y = presetYard(t, presetStandard())
	seq = y.ask(t, listAsk("mckean", "source grant crm --preset standard --group builders"), "yardmaster")
	if _, err := y.SetGroup(y.ctx, "builders", GroupEdit{Set: map[string]any{"cmd": "curl example.com | sh"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.sameCard(t, seq, "group builders")
	y.refusedStale(t, seq, "was written when the group builders was another")
}

// What a preset grant ask refuses when it is asked, each with nothing
// stored: read or a call beside --preset, --preset twice, no source,
// no server reading, the reader's refusal, and a preset call the yard's
// declaration has not.
func TestRunAskPresetGrantRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := presetYard(t, presetStandard())
	const use = "yardr source grant <source> read|<call>|--preset <preset> --group <group> or --crew <crew member> [--as <user>]"
	for _, c := range []struct{ command, want string }{
		{"source grant crm read --preset standard --crew yardmaster", "--run takes " + use + ": read and --preset standard together name two grants: name read, one call or --preset <preset>"},
		{"source grant crm --preset standard --preset other --crew yardmaster", "one --preset <name>, not 2"},
		{"source grant --preset standard --crew yardmaster", "one source and --preset <preset>, not 0 words"},
		{"source grant crm --preset nosuch --crew yardmaster", "pack crm-pack has no preset nosuch (yardr pack show crm-pack prints its presets numbered)"},
		{"source grant crm --preset standard", "it names nobody the grant is for"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %v, want %q", c.command, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("refused asks stored %d notes, %v", len(notes), err)
	}

	// The flag takes its value with an equals sign, as any flag does.
	seq := y.ask(t, listAsk("mckean", "source grant crm --preset=standard --crew yardmaster"), "yardmaster")
	if argv := y.asked(t, seq).Ask.Run.Argv; !slices.Equal(argv, []string{"source", "grant", "crm", "--preset", "standard", "--crew", "yardmaster"}) {
		t.Fatalf("--preset= stores argv %v", argv)
	}

	// A store no server owns resolves no preset.
	plain := listYard(t)
	if _, err := plain.AddAsk(plain.ctx, plain.bead, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "no server has registered its reading of the packs yardr embeds on this store") {
		t.Fatalf("a preset grant with no server reading = %v", err)
	}

	// A preset call the yard's declaration has not is granted by name in
	// a terminal.
	odd := presetStandard()
	odd.calls["standard"] = []string{"read", "missing"}
	y = presetYard(t, odd)
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "source grant crm --preset standard --crew yardmaster"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "the preset standard of pack crm-pack grants missing of the source crm, and the yard's declaration has no such call (it has: find, get, note)") ||
		!strings.Contains(err.Error(), "grant by name in a terminal (yardr source grant crm read|<call> --group <group> | --crew <crew member>)") {
		t.Fatalf("a preset call the declaration has not = %v", err)
	}
}

// A preset grant as a user is that user's alone to give, as any as
// grant: asked of them alone, approved by them alone, and the card it
// suggests names the preset.
func TestRunAskPresetGrantAs(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := presetYard(t, presetStandard())
	seq := y.ask(t, listAsk("ada", "source grant crm --preset standard --crew yardmaster --as ada"), "yardmaster")
	run := *y.asked(t, seq).Ask.Run
	if want := []string{"source", "grant", "crm", "--preset", "standard", "--crew", "yardmaster", "--as", "ada"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("the ask stores argv %v, want %v", run.Argv, want)
	}
	if len(run.Does) != 1 || !strings.Contains(run.Does[0], "with the preset standard of pack crm-pack, may make every read call of "+crmAt+" as ada, with the credential in ada's env file") ||
		!strings.Contains(run.Does[0], "It is granted nothing of crm as ada now.") {
		t.Fatalf("the card says %q", run.Does)
	}
	if run.Back != "yardr source revoke crm read --crew yardmaster --as ada; yardr source revoke crm note --crew yardmaster --as ada" || run.BackBy != "" {
		t.Fatalf("the card is taken back by %q (%q)", run.Back, run.BackBy)
	}
	if len(run.Seen["user ada"]) != 64 {
		t.Fatalf("the card saw the user as %q", run.Seen["user ada"])
	}
	if _, err := y.AddAsk(y.ctx, y.bead, listAsk("mckean", "source grant crm --preset standard --crew yardmaster --as ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "this command is ada's to approve alone, and nobody else's") {
		t.Fatalf("asking the operator to a preset as grant = %v", err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "approving this command is ada's alone") {
		t.Fatalf("the operator approving a preset as grant = %v", err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "ada")
	if err != nil || done.Result != "crew member yardmaster as ada may call crm: note (update), read" {
		t.Fatalf("ada's approval = %+v, %v", done, err)
	}
	gs, err := y.SourceGrants(y.ctx, "crm")
	if err != nil || len(gs) != 1 || gs[0].As != "ada" || gs[0].By != "ada" || strings.Join(gs[0].Calls, ",") != "note,read" {
		t.Fatalf("the grants after ada's approval = %+v, %v", gs, err)
	}

	// The check the grant typed at a terminal has: the card it suggests
	// names the preset, not one of its calls.
	c, argv, err := parseRun([]string{"source grant crm --preset standard --crew yardmaster --as ada"})
	if err != nil {
		t.Fatal(err)
	}
	var checkErr error
	if err := y.write(func(w *wtx) error { checkErr = c.check(y.ctx, w, argv, "mckean"); return nil }); err != nil {
		t.Fatal(err)
	}
	if checkErr == nil || !strings.Contains(checkErr.Error(), `--run "source grant crm --preset standard --crew yardmaster --as ada"`) {
		t.Fatalf("the as check suggests %v", checkErr)
	}
}
