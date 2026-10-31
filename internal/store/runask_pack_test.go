package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// packReaderOf returns a reader of one fake embedded pack, wired, whose
// label and files the test moves between asking and approving.
type fakePack struct {
	label    string
	build    string
	files    map[string]string
	plan     *AskPlan
	kind     string
	depot    string
	mappings Mappings
	digest   string
	programs []string
	// noSources reads a pack that declares none, as the four large
	// packs are.
	noSources bool
	// What the last reading was asked for.
	readDepot    string
	readMappings Mappings
}

func (f *fakePack) reading() *PackReading {
	kind := f.kind
	if kind == "" {
		kind = "source"
	}
	files := f.files
	if files == nil {
		files = wiredFiles
	}
	// In the declaration's command order, as a server reading builds it.
	var srcs []AskSource
	_ = srcs
	r := &PackReading{Name: "wired", Kind: kind, Depot: f.depot, Mappings: f.mappings, Label: f.label, Build: f.build,
		Plan: f.plan, Digest: f.digest, Programs: f.programs}
	if !f.noSources {
		r.Sources = []AskSource{{Name: "wired", Text: wiredDecl, Files: askFilesOrdered(files)}}
	}
	return r
}

func askFilesOrdered(files map[string]string) []AskFile {
	var out []AskFile
	for _, prog := range []string{"wired/limits", "wired/thread", "wired/normalize", "wired/events"} {
		body := files[prog]
		sum := sha256.Sum256([]byte(body))
		out = append(out, AskFile{Program: prog, Text: body, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

func (y *askYard) setPack(f *fakePack) {
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		if name != "wired" && name != "starter" {
			return nil, errFakeNoPack(name)
		}
		f.readDepot, f.readMappings = depot, mappings
		r := f.reading()
		r.Name = name
		return r, nil
	})
}

func errFakeNoPack(name string) error {
	return &fakeNoPack{name}
}

type fakeNoPack struct{ name string }

func (e *fakeNoPack) Error() string {
	return "pack " + e.name + ": yardr embeds no pack of that name: it is the operator's to apply in a terminal (yardr pack check " + e.name + ")"
}

func packAsk() Ask {
	return Ask{Question: "Add the wired source from its pack?", To: "mckean",
		Context: []string{"the yard has no source that speaks in threads"},
		Run:     &AskRun{Argv: []string{"pack apply wired"}}}
}

// A pack ask is stored with the server's own bytes, whatever the writer
// sent for them, and its approval records it to be carried out.
func TestPackApplyAskStoresServerBytes(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	ask := packAsk()
	ask.Run.Carried = []AskSource{{Name: "wired", Text: "name: forged\n", Files: []AskFile{{Program: "x", Text: "forged", SHA256: "0"}}}}
	ask.Run.Does = []string{"a small apply, nothing to read here"}
	ask.Run.Seen = map[string]string{"pack wired": "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run == nil || got.Ask.Run.Pack == nil || got.Ask.Run.Pack.Label != "embedded in yardr test1" {
		t.Fatalf("the stored ask's pack = %+v", got.Ask.Run)
	}
	if len(got.Ask.Run.Carried) != 1 || got.Ask.Run.Carried[0].Text != wiredDecl {
		t.Fatalf("the stored ask carries %+v, want the server's declaration", got.Ask.Run.Carried)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"pack wired (embedded in yardr test1), of kind source, into the yard: it adds 1 source",
		"source wired: its calls go to https://wired.example.com/v1",
		"call ping (read): GET /me",
		"command file wired/thread:",
		"still owed after it: the credential, by a secret ask for wired",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "nothing to read here") || strings.Contains(joined, "forged") {
		t.Errorf("the writer's words stood:\n%s", joined)
	}
	if got.Ask.Run.Description != "Import a pack into the yard: set the yard to what the pack says" {
		t.Errorf("the card's description = %q", got.Ask.Run.Description)
	}
	// The queue and the bead carry the yard's sentences whole.
	q, err := y.UserAsks(y.ctx, "mckean", "mckean")
	if err != nil || len(q.Open) != 1 || q.Open[0].Run == nil || q.Open[0].Run.Pack == nil ||
		!strings.Contains(strings.Join(q.Open[0].Run.Does, "\n"), "pack wired (embedded in yardr test1)") {
		t.Fatalf("the queue's row = %+v, %v", q, err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "approved, to be carried out" {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatalf("a pack approval applied the pack in its write: carrying out is the server's after it")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "was approved already") {
		t.Fatalf("a second approval = %v", err)
	}
}

// A pack ask of a store with no reading registered is refused naming
// the server, and flags and other names are refused to a terminal.
func TestPackApplyRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "no server has registered") {
		t.Fatalf("a pack ask with no reading = %v", err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	for _, c := range []struct{ argv, want string }{
		{"pack apply wired --yes", "pack apply with --yes"},
		{"pack apply wired --auth=x", "pack apply with --auth"},
		{"pack apply wired --sha256 abc", "pack apply with --sha256"},
		{"pack apply wired --prune", "pack apply with --prune"},
		{"pack apply wired --pack-roles", "pack apply with --pack-roles"},
		{"pack apply wired --depot-roles", "pack apply with --depot-roles"},
		{"pack apply wired --source-env a=b", "pack apply with --source-env"},
		{"pack apply wired --source-base a=b", "pack apply with --source-base"},
		{"pack apply wired --notify a=b", "pack apply with --notify"},
		{"pack apply wired --group a=b", "needs --depot"},
		{"pack apply wired --depot a --depot b", "one --depot, not 2"},
		{"pack apply wired --depot a --flow b --flow c", "one --flow, not 2"},
		{"pack apply wired --depot a --crew b", "is not <pack>=<yard>"},
		{"pack apply wired --depot a --group b=c --group b=d", "names b twice"},
		{"pack apply wired --depot a --bind b=\u202ec", "is none of their characters"},
		{"pack apply wired --depot a --bind b=c\nd=e", "is none of their characters"},
		{"pack apply wired --depot nosuch", "depot nosuch"},
		{"pack apply wired extra", "one pack's name"},
		{"pack apply nosuch", "yardr embeds no pack of that name"},
	} {
		ask := packAsk()
		ask.Run.Argv = []string{c.argv}
		if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want %q", c.argv, err, c.want)
		}
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// A pack of kind source is never installed through this row with a depot
// or mappings: the setup card is its one route.
func TestPackApplySourceKindPointsToSetup(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	y.firstInstallDepot(t)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply wired --depot web"}
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "--setup wired") {
		t.Fatalf("a source pack to a depot: AddAsk = %v", err)
	}
}

func (y *askYard) firstInstallDepot(t *testing.T) {
	t.Helper()
	if err := y.PutDepot(y.ctx, Depot{Name: "web", Path: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
}

// firstInstallPlan is a depot plan that asks nothing, with what a first
// install makes.
func firstInstallPlan() *AskPlan {
	return &AskPlan{Objects: []AskPlanObject{
		{Kind: "file", Name: "roles/a.md", Action: "create", FileNewSHA: strings.Repeat("b", 64), Text: "role\n", Diff: "+role\n"},
		{Kind: "group", Name: "builders", Action: "create"},
		{Kind: "route", Name: "builders", Action: "create"},
	}}
}

// A first install is asked with its depot and mappings as words: the card
// is built from them whatever the writer sent, shows them whole, weighs
// strong, and is approved only weighed. Each thing it names stales it.
func TestPackApplyFirstInstallCardAndStale(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	if err := y.PutGroup(y.ctx, Group{Name: "takers", Runner: "manual"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	plan := firstInstallPlan()
	plan.Objects = append(plan.Objects, AskPlanObject{Kind: "flow", Name: "starter.fresh", Action: "create"})
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", plan: plan}
	y.setPack(f)
	ask := packAsk()
	// Typed out of order, and with a forged depot beside argv.
	ask.Run.Argv = []string{"pack apply starter --bind reviewers=claude:opus --flow fresh --group builders=takers --depot web"}
	ask.Run.Pack = &AskPack{Name: "starter", Depot: "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	run := got.Ask.Run
	wantArgv := "pack apply starter --depot web --flow fresh --group builders=takers --bind reviewers=claude:opus"
	if strings.Join(run.Argv, " ") != wantArgv {
		t.Fatalf("the stored argv = %q, want %q", run.Argv, wantArgv)
	}
	if f.readDepot != "web" || f.readMappings.Flow != "fresh" || f.readMappings.Groups["builders"] != "takers" {
		t.Fatalf("the reading was asked for depot %q, mappings %+v", f.readDepot, f.readMappings)
	}
	if run.Pack.Depot != "web" || run.Pack.Mappings.Groups["builders"] != "takers" || run.Pack.Plan == nil {
		t.Fatalf("the stored pack = %+v", run.Pack)
	}
	joined := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"into depot web: 4 to create",
		"this is a first install: depot web holds no import of pack starter.",
		"the plan uses mapping --flow fresh.",
		"the plan uses mapping --group builders=takers.",
		"the plan uses mapping --bind reviewers=claude:opus.",
		"file roles/a.md: create",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if !StrongWeight(got.Ask.Weight) || !strings.Contains(WeightWords(got.Ask.Weight), "code") {
		t.Fatalf("the card's weight = %+v", got.Ask.Weight)
	}
	if !RunArgvHeld(run.Argv) || RunArgvHeld([]string{"pack", "apply", "starter"}) {
		t.Errorf("RunArgvHeld holds the wrong pack apply")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approve it where its weight is shown") {
		t.Fatalf("an unweighed approval = %v", err)
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("the weighed approval = %+v, %v", done, err)
	}
	for _, c := range []struct {
		name, want string
		move       func() error
	}{
		{"the depot", "the depot web", func() error {
			d, err := y.GetDepot(y.ctx, "web")
			if err != nil {
				return err
			}
			d.Base = "trunk"
			return y.PutDepot(y.ctx, *d, "mckean")
		}},
		{"the mapping's target's members", "the group takers", func() error {
			g, err := y.GetGroup(y.ctx, "takers")
			if err != nil {
				return err
			}
			g.Members = []string{"ada"}
			return y.PutGroup(y.ctx, *g, "mckean")
		}},
		{"the mapping's target", "the group takers", func() error {
			return y.PutGroup(y.ctx, Group{Name: "takers", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean")
		}},
		{"the depot's pack record", "the import of pack starter in depot web", func() error {
			_, err := y.RecordPack(y.ctx, PackImport{Pack: "starter", Depot: "web", Source: "x"}, true, "mckean")
			return err
		}},
	} {
		seq := y.ask(t, ask, "yardmaster")
		if err := c.move(); err != nil {
			t.Fatal(err)
		}
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), nothingRan) {
			t.Errorf("%s moved: ApproveAsk = %v", c.name, err)
		}
	}
	// The depot holds the pack now: naming it is a re-apply, no first install.
	seq = y.ask(t, ask, "yardmaster")
	if joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n"); strings.Contains(joined, "first install") {
		t.Errorf("a re-apply to a named depot reads as a first install:\n%s", joined)
	}
	if ws := y.asked(t, seq).Ask.Weight; len(ws) != 1 || !strings.Contains(ws[0].Line, "the agents of reviewers=claude:opus") || strings.Contains(ws[0].Line, "first install") {
		t.Errorf("a re-apply's weight = %+v", ws)
	}
}

// A plan the mappings leave asking, or that refuses or would overwrite, is
// refused at the ask naming what it asks, and an over-cap plan by its cap.
func TestPackApplyFirstInstallRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter --depot web"}
	for _, c := range []struct {
		name string
		plan AskPlan
		want string
	}{
		{"an unmapped group", AskPlan{Missing: []string{"group.web-builders.kind: which agent?"}}, "group.web-builders.kind: which agent?"},
		{"an expose question", AskPlan{Asks: []string{"expose hal to the peer north"}}, "expose hal to the peer north"},
		{"a refused plan", AskPlan{Refused: []string{"a route stands"}}, "a route stands"},
		{"an edited file", AskPlan{Edited: []string{"roles/a.md"}}, "roles/a.md"},
		{"an over-cap plan", AskPlan{Objects: []AskPlanObject{{Kind: "file", Name: "big", Action: "create", Text: strings.Repeat("a", MaxRunPackFiles+1)}}}, "a card holds at most"},
	} {
		y.setPack(&fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", plan: &c.plan})
		if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want %q", c.name, err, c.want)
		}
	}
	// A crew member may propose it; applying it directly stays refused.
	err := y.write(func(w *wtx) error {
		return w.may(y.ctx, "yardmaster", ActApplyPack, Object{Kind: KindPack, Name: "starter"})
	})
	if err == nil {
		t.Fatalf("a crew member's direct first install was allowed")
	}
}

// Each mapping that gives access weighs in the class the grant and setup
// cards give it, and a declaration with a credential alone adds no strength.
func TestPackApplyMappingWeights(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	const decl = `name: crm
base: https://crm.example.com/v1
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  look: { kind: read, request: { method: GET, path: /look } }
  note: { kind: update, request: { method: POST, path: /notes } }
  upload:
    kind: update
    params: { file: { type: file } }
    request: { method: POST, path: /files }
`
	if _, err := y.PutSource(y.ctx, "crm.yaml", decl, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		return &PackReading{Name: name, Kind: "git", Depot: depot, Label: "embedded in yardr test1", Plan: &AskPlan{}}, nil
	})
	for _, c := range []struct {
		words  string
		class  string
		says   string
		strong bool
	}{
		{"--calls builders=crm:read", "", "", false},
		{"--calls builders=crm:none", "", "", false},
		{"--calls builders=crm:look,note", WeightOutside, "builders may call note of crm (an update)", false},
		{"--calls crew:hal=crm:upload", WeightCode, "crew:hal may send a file to crm through upload with no card", true},
		{"--open-threads hal=crm", WeightOutside, "crew member hal may open threads in crm", false},
		{"--open-threads hal=none", "", "", false},
		{"--bind builders=claude:opus,yolo", WeightCode, "the agents of builders=claude:opus,yolo", true},
		{"--members decisions=ada,hal --bind x=claude --calls builders=crm:note", WeightOutside, "builders may call note of crm", true},
		{"--members decisions=ada,hal", WeightTrust, "the group decisions takes the word of ada and hal", true},
		{"--members decisions=", WeightTrust, "the group decisions takes the word of anyone", true},
	} {
		ask := packAsk()
		ask.Run.Argv = []string{"pack apply starter --depot web " + c.words}
		seq := y.ask(t, ask, "yardmaster")
		ws := y.asked(t, seq).Ask.Weight
		if c.class == "" {
			if len(ws) != 0 {
				t.Errorf("%s: weight = %+v, want none", c.words, ws)
			}
			continue
		}
		i := slices.IndexFunc(ws, func(a AskWeight) bool { return a.Class == c.class })
		if i < 0 || !strings.Contains(ws[i].Line, c.says) {
			t.Errorf("%s: weight = %+v, want %s saying %q", c.words, ws, c.class, c.says)
		}
		if StrongWeight(ws) != c.strong {
			t.Errorf("%s: strong = %v, want %v", c.words, StrongWeight(ws), c.strong)
		}
	}
	// A depot pack carrying a credentialed declaration, and nothing to run.
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		return &PackReading{Name: name, Kind: "git", Depot: depot, Label: "embedded in yardr test1", Plan: &AskPlan{},
			Sources: []AskSource{{Name: "phone", Text: "name: phone\nbase: https://phone.example.com/v1\ncredential: { env: PHONE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"}}}, nil
	})
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter --depot web"}
	if ws := y.asked(t, y.ask(t, ask, "yardmaster")).Ask.Weight; StrongWeight(ws) {
		t.Errorf("a credential alone is strong: %+v", ws)
	}
}

// An approval of a pack carded under another build runs when only the build
// moved: the pack is the same file for file, and the build is provenance
// the card keeps. It is refused when the bytes moved, when the label without
// the build did (here, a reading with no label), and for a card written
// before cards kept the build.
func TestPackApplyStaleBuild(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := &fakePack{label: "embedded in yardr one", build: "one"}
	y.setPack(f)
	seq := y.ask(t, packAsk(), "yardmaster")
	f.label, f.build = "embedded in yardr two", "two"
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("a moved build: ApproveAsk = %+v, %v", done, err)
	}
	// The approved card still names the build it was carded under: the
	// carrying out holds the reading to it.
	if run := y.asked(t, seq).Ask.Run; run.Seen["pack wired build"] != "one" || run.Pack.Label != "embedded in yardr one" {
		t.Fatalf("the approved card names the build %q and the label %q, want the ones it was carded under", run.Seen["pack wired build"], run.Pack.Label)
	}
	answers := len(answerNotes(t, y))

	seq2 := y.ask(t, packAsk(), "yardmaster")
	changed := map[string]string{}
	for k, v := range wiredFiles {
		changed[k] = v
	}
	changed["wired/thread"] = "#!/bin/sh\n# thread, changed in the new build\n"
	f.files = changed
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq2, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "whose pack differs") {
		t.Fatalf("changed bytes: ApproveAsk = %v", err)
	}

	// A reading that comes back with no label has moved the label without
	// the build, whatever bytes it still holds. The server's reader fails
	// before this for a pack the new build no longer embeds; the comparison
	// refuses either way.
	f.files = nil
	seq3 := y.ask(t, packAsk(), "yardmaster")
	f.label, f.build = "", ""
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq3, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "whose pack differs") {
		t.Fatalf("a reading with no label: ApproveAsk = %v", err)
	}
	for _, open := range []uint64{seq2, seq3} {
		if r := y.asked(t, open); r.State != AskOpen {
			t.Fatalf("ask %d is %s after a refused approval", open, r.State)
		}
	}
	if got := len(answerNotes(t, y)); got != answers {
		t.Fatalf("refused approvals recorded %d answers", got-answers)
	}

	// A card written before it kept the build has no build key to leave out
	// of its label, so a moved label refuses it, naming an older yardr, and
	// still tells the operator to ask again.
	oldYard, _ := runYard(t)
	oldPack := &fakePack{label: "embedded in yardr one"}
	oldYard.setPack(oldPack)
	oldSeq := oldYard.ask(t, packAsk(), "yardmaster")
	oldPack.label, oldPack.build = "embedded in yardr two", "two"
	if _, err := oldYard.ApproveAsk(oldYard.ctx, oldYard.bead, oldSeq, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "was carded under an older yardr and this yard now runs yardr two, whose pack differs") ||
		!strings.Contains(err.Error(), "Nothing ran. Decline it; it is asked again if it is still wanted.") {
		t.Fatalf("an older card: ApproveAsk = %v", err)
	}
}

// The last comparison of a pack apply's and a setup's approval holds the
// card's sentences and how it is taken back, with the build struck from the
// label the first sentence names: a build that moved alone reads as the card
// it was, and a sentence that moved with it is named as itself, not as the
// build. Neither stale function reads the yard when no source is carried.
func TestStaleCardsReadAlikeWithoutBuild(t *testing.T) {
	t.Parallel()
	w := &wtx{}
	cardOf := func(build string) (AskRun, AskSetup) {
		seen := map[string]string{"pack wired": "embedded in yardr " + build, "pack wired build": build,
			"pack wired bytes": "b", "pack wired presets": "p"}
		does := []string{"pack wired (embedded in yardr " + build + "), of kind source, into the yard: it adds 1 source.",
			"source wired: its calls go to https://wired.example.com/v1."}
		run := AskRun{Argv: []string{"pack", "apply", "wired"}, Does: does, Back: "yardr source rm wired", BackBy: "the operator", Seen: seen}
		return run, AskSetup{Pack: "wired", Does: does, Back: run.Back, BackBy: run.BackBy, Seen: seen}
	}
	for _, c := range []struct {
		name   string
		change func(does []string, back *string)
		want   string // what the refusal says, "" where there is none
	}{
		{"nothing else", func([]string, *string) {}, ""},
		{"a sentence", func(does []string, _ *string) {
			does[1] = "source wired: its calls go to https://other.example.com/v1."
		},
			`ask 1 says "source wired: its calls go to https://wired.example.com/v1.", and the yard would say "source wired: its calls go to https://other.example.com/v1." now: the card is from before that changed.`},
		{"how it is taken back", func(_ []string, back *string) { *back = "yardr source rm other" },
			"the card is from before that changed."},
	} {
		for _, nowBuild := range []string{"one", "two"} {
			wasRun, wasSetup := cardOf("one")
			nowRun, nowSetup := cardOf(nowBuild)
			c.change(nowRun.Does, &nowRun.Back)
			nowSetup.Back = nowRun.Back
			for which, err := range map[string]error{
				"pack apply": w.packApplyStale(1, wasRun, nowRun),
				"setup":      w.setupStale(1, wasSetup, nowSetup),
			} {
				switch {
				case c.want == "" && err != nil:
					t.Errorf("%s, %s, build %s: refused: %v", which, c.name, nowBuild, err)
				case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
					t.Errorf("%s, %s, build %s: = %v, want it to say %q", which, c.name, nowBuild, err, c.want)
				}
			}
		}
	}
}

// plantWired puts the wired source in the yard with its copies on disk
// and one entry neither declaration names.
func plantWired(t *testing.T, y *askYard) string {
	t.Helper()
	if _, err := y.PutSource(y.ctx, "wired.yaml", wiredDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(y.Store.home, "sources", "wired")
	for prog, body := range wiredFiles {
		p := filepath.Join(dir, filepath.FromSlash(prog))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A pack that would replace a source cards each command file's change on
// the file's own command line, with its hashes whole and its diff, and
// names each entry neither declaration names as removed.
func TestPackApplyReplacementCard(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	// The standing copy of wired/thread differs from the pack's.
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# thread, as the yard has it\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	carried := got.Ask.Run.Carried[0]
	var diff *AskFileDiff
	for i := range carried.Old {
		if carried.Old[i].Program == "wired/thread" {
			diff = &carried.Old[i]
		}
	}
	if diff == nil || diff.OldSHA256 == diff.NewSHA256 || !strings.Contains(diff.Diff, "-# thread, as the yard has it") || !strings.Contains(diff.Diff, "+# thread") {
		t.Fatalf("the stored change = %+v", carried.Old)
	}
	if len(carried.Removed) != 1 || carried.Removed[0].Path != "extra.txt" || carried.Removed[0].Text != "left by hand\n" {
		t.Fatalf("the stored removed entries = %+v", carried.Removed)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"it would replace the source wired the yard has under that name.",
		"command file wired/thread: [wired/thread, reply] (thread.reply, handed the credential): sha256 " + diff.OldSHA256 + " -> sha256 " + diff.NewSHA256 + ", mode 0700 (1 insertions, 1 deletions).",
		"the copy would remove extra.txt, which neither declaration names: sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	lines := strings.Join(got.Lines(), "\n")
	if !strings.Contains(lines, "-# thread, as the yard has it") {
		t.Errorf("the terminal card lacks the diff whole:\n%s", lines)
	}
	checkReplacementCardChangedCallGrants(t, "pack apply")
}

// A file the pack's declaration drops has no command line to carry its
// change, so it keeps a line of its own, with its old hash whole.
func TestPackApplyDroppedFileKeepsItsLine(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	plantWired(t, y)
	dropped := strings.Replace(wiredDecl, "thread:\n  reply: [wired/thread, reply]\n", "", 1)
	var files []AskFile
	for _, f := range askFilesOrdered(wiredFiles) {
		if f.Program != "wired/thread" {
			files = append(files, f)
		}
	}
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		if name != "wired" {
			return nil, errFakeNoPack(name)
		}
		return &PackReading{Name: "wired", Kind: "source", Label: "embedded in yardr test1",
			Sources: []AskSource{{Name: "wired", Text: dropped, Files: files}}}, nil
	})
	seq := y.ask(t, packAsk(), "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Run.Does, "\n")
	sum := sha256.Sum256([]byte(wiredFiles["wired/thread"]))
	want := "command file wired/thread would change: sha256 " + hex.EncodeToString(sum[:]) + " -> sha256 (none)"
	if !strings.Contains(joined, want) {
		t.Errorf("the card lacks %q:\n%s", want, joined)
	}
	if strings.Contains(joined, "command file wired/thread:") {
		t.Errorf("the dropped file kept its line and grew a command line:\n%s", joined)
	}
}

// A previous source removal may leave its command-copy directory behind.
// A pack that adds that source must card and hold to the files it will
// replace or remove even though no source record stands.
func TestPackApplyCardsOrphanCopyDirectory(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := filepath.Join(y.Store.home, "sources", "wired")
	if err := os.MkdirAll(filepath.Join(dir, "wired"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# left by a removed source\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	carried := got.Ask.Run.Carried[0]
	if len(carried.Inventory) != 2 || len(carried.Old) != 1 || len(carried.Removed) != 1 {
		t.Fatalf("the orphan copy inventory = %+v, old %+v, removed %+v", carried.Inventory, carried.Old, carried.Removed)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"the source wired is not in the yard, but its command copy directory has entries this pack would replace or remove.",
		"command file wired/thread: [wired/thread, reply] (thread.reply, handed the credential): sha256 ",
		"the copy would remove extra.txt, which neither declaration names: sha256 ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(strings.Join(got.Lines(), "\n"), "left by hand") {
		t.Fatalf("the terminal card omits the orphan file's text:\n%s", strings.Join(got.Lines(), "\n"))
	}
	if _, err := y.GetSource(y.ctx, "wired"); err == nil {
		t.Fatal("the orphan directory had a source record")
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("changed after the ask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "extra.txt") {
		t.Fatalf("a changed orphan copy: ApproveAsk = %v", err)
	}
	seq = y.ask(t, packAsk(), "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "added-after-ask.txt"), []byte("also left by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "added-after-ask.txt") {
		t.Fatalf("an orphan copy added after the ask: ApproveAsk = %v", err)
	}
}

func TestPackApplyCarriesRemovedTextUpToThePackPayloadBound(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	text := strings.Repeat("a removed line\n", 800) // larger than the loose-file limit
	if err := os.WriteFile(filepath.Join(dir, "large-extra.txt"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	for _, rem := range got.Ask.Run.Carried[0].Removed {
		if rem.Path == "large-extra.txt" {
			if rem.Text != text {
				t.Fatalf("carried removed text has %d bytes, want %d", len(rem.Text), len(text))
			}
			return
		}
	}
	t.Fatalf("the pack card omitted the removed text: %+v", got.Ask.Run.Carried[0].Removed)
}

func TestPackReplacementDiffShowsWholeDeclarationKeys(t *testing.T) {
	t.Parallel()
	oldCalls, err := source.Parse("old", []byte(tgDecl))
	if err != nil {
		t.Fatal(err)
	}
	newText := strings.Replace(tgDecl, `text: "hi {text}"`, `text: "hello {text}"`, 1)
	newCalls, err := source.Parse("new", []byte(newText))
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(packDeclDiffLines("tg", oldCalls, newCalls), "\n")
	for _, want := range []string{`"text":"hi {text}"`, `"text":"hello {text}"`, `"chat_id":"{chat}"`} {
		if !strings.Contains(calls, want) {
			t.Errorf("the calls change omits %q:\n%s", want, calls)
		}
	}

	oldHook, err := source.Parse("old", []byte(wiredDecl))
	if err != nil {
		t.Fatal(err)
	}
	newText = strings.Replace(wiredDecl, "X-Wired-Secret", "X-New-Secret", 1)
	newHook, err := source.Parse("new", []byte(newText))
	if err != nil {
		t.Fatal(err)
	}
	hook := strings.Join(packDeclDiffLines("wired", oldHook, newHook), "\n")
	if !strings.Contains(hook, "X-Wired-Secret") || !strings.Contains(hook, "X-New-Secret") {
		t.Errorf("the hook change omits its old or new verification header:\n%s", hook)
	}

	oldGit, err := source.Parse("old", []byte(wiredDecl))
	if err != nil {
		t.Fatal(err)
	}
	newGit, err := source.Parse("new", []byte(wiredDecl+"git:\n  credential: [wired/git-credential]\n"))
	if err != nil {
		t.Fatal(err)
	}
	git := strings.Join(packDeclDiffLines("wired", oldGit, newGit), "\n")
	if !strings.Contains(git, `source wired git: null -> {"credential":["wired/git-credential"]}.`) {
		t.Errorf("the git change is not on the card:\n%s", git)
	}

	// Region bases decide where the credential goes, so a change of them
	// alone is one line; no regions is "none" however the map was built.
	noBases, err := source.Parse("old", []byte(wiredDecl))
	if err != nil {
		t.Fatal(err)
	}
	eu := wiredDecl + "bases:\n  eu: https://eu.wired.example.com/v1\n"
	euBases, err := source.Parse("new", []byte(eu))
	if err != nil {
		t.Fatal(err)
	}
	movedBases, err := source.Parse("new", []byte(strings.Replace(eu, "eu.wired", "eu2.wired", 1)))
	if err != nil {
		t.Fatal(err)
	}
	emptyBases := *noBases
	emptyBases.Bases = map[string]string{}
	for _, c := range []struct {
		name     string
		was, now *source.Declaration
		want     string
	}{
		{"added", noBases, euBases, `source wired bases: none -> {"eu":"https://eu.wired.example.com/v1"}.`},
		{"dropped", euBases, noBases, `source wired bases: {"eu":"https://eu.wired.example.com/v1"} -> none.`},
		{"a region moved", euBases, movedBases, `source wired bases: {"eu":"https://eu.wired.example.com/v1"} -> {"eu":"https://eu2.wired.example.com/v1"}.`},
		{"empty against absent", &emptyBases, noBases, ""},
		{"absent against empty", noBases, &emptyBases, ""},
		{"the same", euBases, euBases, ""},
	} {
		if got := strings.Join(packDeclDiffLines("wired", c.was, c.now), "\n"); got != c.want {
			t.Errorf("bases %s: card lines = %q, want %q", c.name, got, c.want)
		}
	}
}

// A chmod of a command copy after the ask makes the approval stale, and
// so does a changed copy and a changed entry neither declaration names:
// the refusal names the file that moved.
func TestPackApplyStaleCopy(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	y.setPack(&fakePack{label: "embedded in yardr test1"})

	seq := y.ask(t, packAsk(), "yardmaster")
	if err := os.Chmod(filepath.Join(dir, "wired", "thread"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "wired/thread") || !strings.Contains(err.Error(), "chmod") {
		t.Fatalf("a chmod after the ask: ApproveAsk = %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "wired", "thread"), 0o700); err != nil {
		t.Fatal(err)
	}

	seq = y.ask(t, packAsk(), "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "wired", "normalize"), []byte("#!/bin/sh\n# changed by hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "wired/normalize") {
		t.Fatalf("a changed copy after the ask: ApproveAsk = %v", err)
	}

	seq = y.ask(t, packAsk(), "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("changed by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "extra.txt") {
		t.Fatalf("a changed removed entry after the ask: ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after refused approvals", r.State)
	}
}

func TestPackApplyReplacementCardsSymlinksWithoutFollowingThem(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	target := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(target, []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "outside.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	var removed *AskRemoved
	for i := range got.Ask.Run.Carried[0].Removed {
		if got.Ask.Run.Carried[0].Removed[i].Path == "outside.txt" {
			removed = &got.Ask.Run.Carried[0].Removed[i]
		}
	}
	if removed == nil || !removed.Symlink || removed.Text != "" {
		t.Fatalf("the symbolic link inventory = %+v", got.Ask.Run.Carried[0].Removed)
	}
	if strings.Contains(strings.Join(got.Lines(), "\n"), "outside secret") {
		t.Fatalf("the card read the link target outside the source copies:\n%s", strings.Join(got.Lines(), "\n"))
	}

	other := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(other, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "outside.txt") {
		t.Fatalf("a moved symbolic link: ApproveAsk = %v", err)
	}
}

func TestPackApplyRefusesChangedCommandSymlink(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	target := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(dir, "wired", "thread")
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, command); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("a command copy symbolic link = %v", err)
	}
}

// A pending source set and a pending source grant go stale when a
// command copy of their source changes after the ask.
func TestSourceRowsStaleWhenCopyChanges(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	dir := plantWired(t, y)
	if err := y.PutGroup(y.ctx, Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	set := Ask{Question: "More calls?", To: "mckean", Context: []string{"c"},
		Run: &AskRun{Argv: []string{"source set wired calls_per_hour=200"}}}
	seq := y.ask(t, set, "yardmaster")
	grant := Ask{Question: "Grant it?", To: "mckean", Context: []string{"c"},
		Run: &AskRun{Argv: []string{"source grant wired read --group builders"}}}
	gseq := y.ask(t, grant, "yardmaster")
	if err := os.WriteFile(filepath.Join(dir, "wired", "thread"), []byte("#!/bin/sh\n# changed by hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil {
		t.Fatalf("a source set approved over a changed copy")
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, gseq, "mckean", false, nil); err == nil {
		t.Fatalf("a source grant approved over a changed copy")
	}
}

// A depot pack ask stores the server-built plan with each changed
// file's diff, throws a forged plan away, and its approval is held to
// the plan rebuilt.
func TestPackApplyDepotCardAndStale(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	diff := "--- was\n+++ is\n@@ -1,1 +1,1 @@\n-old role\n+new role\n"
	plan := &AskPlan{
		Objects: []AskPlanObject{
			{Kind: "file", Name: "roles/a.md", Action: "change", FileOldSHA: strings.Repeat("a", 64), FileNewSHA: strings.Repeat("b", 64), Text: "new role\n", Diff: diff},
			{Kind: "group", Name: "builders", Action: "create", Changes: []AskChange{{Field: "runner", Old: "", New: "exec"}}, Env: []string{"YARDR_LAND=squash"}},
			{Kind: "file", Name: "roles/old.md", Action: "dropped"},
		},
		Governs: []AskGovern{{Flow: "default", Now: "other"}},
	}
	mappings := Mappings{
		Flow:        "default",
		Crew:        map[string]string{"yardmaster": "mayor"},
		Groups:      map[string]string{"builders": "takers"},
		Binds:       []string{"reviewers=claude:opus"},
		Members:     []string{"decisions=mckean"},
		OpenThreads: []string{"yardmaster=linear"},
		Calls:       []string{"builders=crm:read"},
	}
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "yardr", plan: plan, mappings: mappings}
	y.setPack(f)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Run.Pack == nil || got.Ask.Run.Pack.Plan == nil || len(got.Ask.Run.Pack.Plan.Objects) != 3 {
		t.Fatalf("the stored plan = %+v", got.Ask.Run.Pack)
	}
	joined := strings.Join(got.Ask.Run.Does, "\n")
	for _, want := range []string{
		"pack starter (embedded in yardr test1), of kind git, into depot yardr: 1 to create, 1 to change, 0 unchanged, and 1 dropped",
		"file roles/a.md: change, sha256 " + strings.Repeat("a", 64) + " -> sha256 " + strings.Repeat("b", 64),
		"group builders: create (runner: none -> exec)",
		// What the pack sets in the cmd of a group it creates is on the
		// card: the approval has the yard write it.
		"group builders: cmd sets YARDR_LAND=squash before the script.",
		"file roles/old.md: dropped by the pack, left where it stands.",
		"depot yardr would follow flow default; it follows other now.",
		"the plan uses mapping --flow default.",
		"the plan uses mapping --crew yardmaster=mayor.",
		"the plan uses mapping --group builders=takers.",
		"the plan uses mapping --bind reviewers=claude:opus.",
		"the plan uses mapping --members decisions=mckean.",
		"the plan uses mapping --open-threads yardmaster=linear.",
		"the plan uses mapping --calls builders=crm:read.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if got.Ask.Run.Description != "Import a pack into depot yardr: set the yard to what the pack says" {
		t.Errorf("the card's description = %q", got.Ask.Run.Description)
	}
	if lines := strings.Join(got.Lines(), "\n"); !strings.Contains(lines, "-old role") || !strings.Contains(lines, "+new role") {
		t.Errorf("the terminal card lacks the file's diff whole:\n%s", lines)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("the approval = %+v, %v", done, err)
	}
	// A mapping that moves under a second ask stales its approval too,
	// even when its depot objects have not changed.
	seq2 := y.ask(t, ask, "yardmaster")
	f.mappings.Calls = []string{"builders=crm:none"}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq2, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "card is from before that changed") {
		t.Fatalf("a moved mapping: ApproveAsk = %v", err)
	}
	f.mappings = mappings
	seqPlan := y.ask(t, ask, "yardmaster")
	plan.Objects[1].Changes[0].New = "herdr"
	if _, err := y.ApproveAsk(y.ctx, y.bead, seqPlan, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "the plan") {
		t.Fatalf("a moved plan: ApproveAsk = %v", err)
	}
	// So does an env that moves: the card showed the other.
	plan.Objects[1].Changes[0].New = "exec"
	seq3 := y.ask(t, ask, "yardmaster")
	plan.Objects[1].Env = []string{"YARDR_LAND=rebase"}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq3, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "the object group builders") {
		t.Fatalf("a moved env: ApproveAsk = %v", err)
	}
}

// A depot ask is refused where the plan asks, where there is no single
// depot, and where its card or payload would run past the bound.
func TestPackApplyDepotRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter"}
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "yardr",
		plan: &AskPlan{Missing: []string{"group.building.agent: which agent?"}}}
	y.setPack(f)
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "yardr pack check starter --depot yardr") {
		t.Fatalf("a plan that asks: AddAsk = %v", err)
	}
	// With no single depot, the refusal says a first install is asked with its
	// depot named, and that the terminal is for a plan that still asks.
	f.plan, f.depot = &AskPlan{}, ""
	_, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster")
	if err == nil {
		t.Fatal("no single depot: AddAsk was accepted")
	}
	for _, want := range []string{
		"no single depot",
		`--run "pack apply starter --depot <depot>"`,
		"a plan that still asks is the operator's to type in a terminal",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no single depot: AddAsk = %v, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "a first install, and one of several depots, is the operator's to type in a terminal") {
		t.Errorf("no single depot: a first install is called a terminal's: %v", err)
	}
	if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
		t.Fatalf("the refused asks left %d notes, %v", len(notes), err)
	}
}

// The store refuses a pack source whose parse names another source, a
// card past forty lines, and a payload past the bound.
func TestPackApplyStoreRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		return &PackReading{Name: name, Kind: "source", Label: "embedded in yardr test1",
			Sources: []AskSource{{Name: "other", Text: wiredDecl}}}, nil
	})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "its declaration names wired") {
		t.Fatalf("a carried name the parse does not give: AddAsk = %v", err)
	}
	// Past the line bound: many sources, each with its own card lines.
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		var srcs []AskSource
		for i := 0; i < 6; i++ {
			text := strings.Replace(wiredDecl, "name: wired", "name: wiredx", 1)
			text = strings.Replace(text, "wiredx", "w"+strings.Repeat("x", i+1), 1)
			srcs = append(srcs, AskSource{Name: "w" + strings.Repeat("x", i+1), Text: text, Files: askFiles(wiredFiles)})
		}
		return &PackReading{Name: name, Kind: "source", Label: "embedded in yardr test1", Sources: srcs}, nil
	})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "card would run to") {
		t.Fatalf("a card past the bound: AddAsk = %v", err)
	}
	// Past the payload bound: one command file too large together.
	big := map[string]string{}
	for k := range wiredFiles {
		big[k] = wiredFiles[k]
	}
	big["wired/limits"] = strings.Repeat("a", 70000)
	y.setPack(&fakePack{label: "embedded in yardr test1", files: big})
	if _, err := y.AddAsk(y.ctx, y.bead, packAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "a card holds at most 65536") {
		t.Fatalf("a payload past the bound: AddAsk = %v", err)
	}
}

func askFiles(files map[string]string) []AskFile {
	var out []AskFile
	for prog, body := range files {
		sum := sha256.Sum256([]byte(body))
		out = append(out, AskFile{Program: prog, Text: body, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

// An approval is refused when a source the pack would add was added
// since the ask.
func TestPackApplyStaleSourceAdded(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	if _, err := y.PutSource(y.ctx, "wired.yaml", wiredDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil ||
		!strings.Contains(err.Error(), "was not in the yard: it was added since") {
		t.Fatalf("ApproveAsk = %v", err)
	}
	if r := y.asked(t, seq); r.State != AskOpen {
		t.Fatalf("the ask is %s after a refused approval", r.State)
	}
}

// largePlan is a depot plan of n created files of size bytes each, whose
// texts pass the whole card's bound together, with the objects of a first
// install beside them.
func largePlan(n, size int) *AskPlan {
	plan := firstInstallPlan()
	plan.Objects = []AskPlanObject{}
	for i := range n {
		plan.Objects = append(plan.Objects, AskPlanObject{Kind: "file", Name: "roles/" + itoa(uint64(i)) + ".md", Action: "create",
			FileNewSHA: strings.Repeat("b", 64), Text: strings.Repeat("r", size) + "\n", Diff: "--- was\n+++ is\n@@ -0,0 +1,1 @@\n+role\n"})
	}
	plan.Objects = append(plan.Objects,
		AskPlanObject{Kind: "group", Name: "web-builders", Action: "create"},
		AskPlanObject{Kind: "group", Name: "web-assembly", Action: "create", Env: []string{"YARDR_LAND=squash"}},
		AskPlanObject{Kind: "crew", Name: "yardmaster", Action: "change", Changes: []AskChange{{Field: "on", Old: "a", New: "b"}}},
		AskPlanObject{Kind: "route", Name: "stage new", Action: "create"},
		AskPlanObject{Kind: "route", Name: "stage review", Action: "unchanged"},
		AskPlanObject{Kind: "file", Name: "roles/old.md", Action: "dropped"})
	plan.Governs = []AskGovern{{Flow: "starter.default", Now: "default"}}
	return plan
}

var summaryDigest = strings.Repeat("d", 64)

// summaryAsk is a first install of the fake starter into depot web.
func summaryAsk() Ask {
	ask := packAsk()
	ask.Run.Argv = []string{"pack apply starter --depot web"}
	return ask
}

// A depot pack named with its depot past the whole card's bounds is a
// summary card: its lines name the pack by its digest and each file by its
// hash, a client reaches each file's text from its line, the texts stay
// carried, and the approval is gated and held as a whole card's is.
func TestPackApplySummaryCard(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", digest: summaryDigest,
		programs: []string{"scripts/merge-local"}, plan: largePlan(20, 5000)}
	y.setPack(f)
	seq := y.ask(t, summaryAsk(), "yardmaster")
	got := y.asked(t, seq)
	run := got.Ask.Run
	if !run.Pack.Summary || run.Pack.Digest != summaryDigest || run.Seen[packDigestKey("starter")] != summaryDigest {
		t.Fatalf("the stored pack = %+v, seen %v", run.Pack, run.Seen)
	}
	if len(run.Does) > MaxRunSourceCardLines {
		t.Fatalf("the summary runs to %d lines", len(run.Does))
	}
	joined := strings.Join(run.Does, "\n")
	for _, want := range []string{
		"pack starter (embedded in yardr test1), of kind git, into depot web: 23 to create, 1 to change, 1 unchanged, and 1 dropped and left where they stand, digest sha256:" + summaryDigest + ". It asks nothing. It runs 1 program of the pack: scripts/merge-local.",
		"this is a first install: depot web holds no import of pack starter.",
		"file roles/0.md: create, sha256 (none) -> sha256 " + strings.Repeat("b", 64) + ", 5001 bytes (1 insertions, 0 deletions).",
		"groups to create: web-builders, web-assembly.",
		"group web-assembly: cmd sets YARDR_LAND=squash before the script.",
		"crew yardmaster: change (on: a -> b).",
		"routes to create: stage new.",
		"file roles/old.md: dropped by the pack, left where it stands.",
		"depot web would follow flow starter.default; it follows default now.",
		"source wired: added, its calls go to https://wired.example.com/v1, with command files wired/limits sha256 ",
		"in all: 23 to create, 1 to change, 1 unchanged, 0 not in the pack, 1 no longer in it.",
		"the full text stands behind this card, on this bead's page: 25 files, ",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the summary lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "more, ") {
		t.Errorf("a summary that fits folded files:\n%s", joined)
	}
	if len(run.Pack.Files) != 20 {
		t.Fatalf("the card ties %d files to lines, want 20", len(run.Pack.Files))
	}
	for _, file := range run.Pack.Files {
		if file.Line < 0 || !strings.HasPrefix(run.Does[file.Line], "file "+file.Name+":") {
			t.Errorf("file %s is tied to line %d: %q", file.Name, file.Line, run.Does[max(file.Line, 0)])
		}
		if !slices.ContainsFunc(run.Pack.Plan.Objects, func(o AskPlanObject) bool {
			return o.Kind == "file" && o.Name == file.Name && o.Text != "" && o.FileNewSHA != ""
		}) {
			t.Errorf("file %s has no plan object with its text and hash", file.Name)
		}
	}
	// A terminal reads the texts behind the card, as behind a whole one.
	if lines := strings.Join(got.Lines(), "\n"); !strings.Contains(lines, "the file roles/19.md as the pack would install it") {
		t.Errorf("the terminal card lacks a file's text")
	}
	if !StrongWeight(got.Ask.Weight) {
		t.Fatalf("the summary's weight = %+v, want the whole card's strong", got.Ask.Weight)
	}
	if !slices.ContainsFunc(got.Ask.Weight, func(a AskWeight) bool { return strings.Contains(a.Line, "file roles/19.md") }) {
		t.Errorf("the weight lost a file past the card's lines: %+v", got.Ask.Weight)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "approve it where its weight is shown") {
		t.Fatalf("an unweighed approval = %v", err)
	}
	if done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("the weighed approval = %+v, %v", done, err)
	}
	// A paired device approves a summary as it does a whole card: the weight
	// is the whole card's, strong, and the word that its person saw it is all
	// the gate asks of a device.
	if done, err := y.ApproveAsk(WithVia(y.ctx, "mckean", "phone"), y.bead, y.ask(t, summaryAsk(), "yardmaster"), "mckean", true, nil); err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("a device's approval = %+v, %v", done, err)
	}
	if ev := lastAnswer(t, y); Via(ev) != "phone" || Witness(ev) != "" {
		t.Fatalf("the device's approval is via %q witnessed %q, want phone with no witness", Via(ev), Witness(ev))
	}

	for _, c := range []struct {
		name, want string
		move       func()
		back       func()
	}{
		{"the digest alone, for a file no plan object shows", "whose pack differs",
			func() { f.digest = strings.Repeat("e", 64) }, func() { f.digest = summaryDigest }},
		{"a plan object", "the object file roles/0.md",
			func() { f.plan.Objects[0].Text += "more" }, func() { f.plan.Objects[0].Text = strings.TrimSuffix(f.plan.Objects[0].Text, "more") }},
		{"the pack's label", "whose pack differs",
			func() { f.label = "embedded in yardr other" }, func() { f.label = "embedded in yardr test1" }},
	} {
		seq := y.ask(t, summaryAsk(), "yardmaster")
		c.move()
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), nothingRan) {
			t.Errorf("%s moved: ApproveAsk = %v", c.name, err)
		}
		c.back()
	}
	seq = y.ask(t, summaryAsk(), "yardmaster")
	d, err := y.GetDepot(y.ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	d.Base = "trunk"
	if err := y.PutDepot(y.ctx, *d, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "the depot web") {
		t.Errorf("the depot moved: ApproveAsk = %v", err)
	}
}

// An ask stored before cards held a digest is not stale for lacking one.
func TestPackApplyStaleKeepsADigestlessAsk(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", plan: firstInstallPlan()}
	y.setPack(f)
	seq := y.ask(t, summaryAsk(), "yardmaster")
	f.digest = summaryDigest
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatalf("the digestless ask = %v", err)
	}
}

// The files take what room the owed lines leave of the bound: past it the
// last room goes to one closing line naming how many files and bytes are
// folded, each folded file stays tied to its plan object, and a card whose
// owed lines alone pass the bound is refused. A card that fits stays whole.
func TestPackApplySummaryFold(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", digest: summaryDigest, noSources: true}
	y.setPack(f)
	// The head is 2 lines, the owed object lines 7, the full text's 1.
	const room = 30
	const changedDiff = "--- old\n+++ new\n@@ -1 +1 @@\n-old\n+new\n"
	for _, c := range []struct {
		files, folded, lines int
	}{
		{room, 0, MaxRunSourceCardLines},
		{room + 1, 2, MaxRunSourceCardLines},
		{60, 31, MaxRunSourceCardLines},
	} {
		f.plan = largePlan(c.files, 3000)
		for i := range c.files {
			if i != 0 && (c.folded == 0 || i < c.files-c.folded) {
				continue
			}
			o := &f.plan.Objects[i]
			o.Action, o.FileOldSHA, o.Diff = "change", strings.Repeat("a", 64), changedDiff
		}
		run := y.asked(t, y.ask(t, summaryAsk(), "yardmaster")).Ask.Run
		if !run.Pack.Summary || len(run.Does) != c.lines {
			t.Fatalf("%d files: summary %v, %d lines:\n%s", c.files, run.Pack.Summary, len(run.Does), strings.Join(run.Does, "\n"))
		}
		var folded []AskPackFile
		for _, file := range run.Pack.Files {
			if file.Line == -1 {
				folded = append(folded, file)
			}
		}
		if len(run.Pack.Files) != c.files || len(folded) != c.folded {
			t.Fatalf("%d files: tied %d, folded %d, want %d", c.files, len(run.Pack.Files), len(folded), c.folded)
		}
		line := ""
		for _, l := range run.Does {
			if strings.Contains(l, "without a line of their own") {
				line = l
			}
		}
		if c.folded == 0 {
			if line != "" {
				t.Errorf("%d files fit and were folded: %s", c.files, line)
			}
			continue
		}
		want := countNoun(c.folded, "more file") + ", " + itoa(uint64(c.folded*(3001+len(changedDiff)))) + " bytes"
		if !strings.Contains(line, want) || !strings.Contains(line, "under digest sha256:"+summaryDigest) {
			t.Errorf("%d files: the closing line %q lacks %q", c.files, line, want)
		}
		lastFile := run.Pack.Files[c.files-c.folded-1].Line
		groups := slices.IndexFunc(run.Does, func(line string) bool { return strings.HasPrefix(line, "groups to create:") })
		counts := slices.IndexFunc(run.Does, func(line string) bool { return strings.HasPrefix(line, "in all:") })
		fullText := slices.IndexFunc(run.Does, func(line string) bool { return strings.HasPrefix(line, "the full text stands behind this card") })
		foldLine := slices.Index(run.Does, line)
		if !(lastFile < foldLine && foldLine < groups && groups < counts && counts < fullText) {
			t.Errorf("%d files: summary sentences are out of order: file=%d fold=%d groups=%d counts=%d full text=%d",
				c.files, lastFile, foldLine, groups, counts, fullText)
		}
		fullBytes := c.files*3001 + (1+c.folded)*len(changedDiff)
		wantFullText := "the full text stands behind this card, on this bead's page: " + countNoun(c.files, "file") + ", " + itoa(uint64(fullBytes)) + " bytes."
		if !slices.Contains(run.Does, wantFullText) {
			t.Errorf("%d files: full text line lacks changed-file diffs: want %q", c.files, wantFullText)
		}
	}

	f.plan = largePlan(60, 3000)
	for i := range MaxRunSourceCardLines {
		f.plan.Objects = append(f.plan.Objects, AskPlanObject{Kind: "file", Name: "roles/gone" + itoa(uint64(i)) + ".md", Action: "dropped"})
	}
	if _, err := y.AddAsk(y.ctx, y.bead, summaryAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "lines: a card is read whole") {
		t.Fatalf("owed lines past the bound: AddAsk = %v", err)
	}

	f.plan = largePlan(3, 100)
	run := y.asked(t, y.ask(t, summaryAsk(), "yardmaster")).Ask.Run
	if run.Pack.Summary || run.Pack.Files != nil || run.Pack.Digest != summaryDigest {
		t.Fatalf("a pack that fits = %+v", run.Pack)
	}
	if joined := strings.Join(run.Does, "\n"); strings.Contains(joined, "digest sha256") || !strings.Contains(joined, "group web-builders: create.") {
		t.Errorf("a pack that fits lost its whole card:\n%s", joined)
	}
}

// A summary is written for a named depot and an addition alone: what it
// cannot bind still refuses to a terminal in the whole card's words.
func TestPackApplySummaryRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", digest: summaryDigest, plan: largePlan(20, 5000)}
	home := y.Store.home
	y.setPack(f)
	wholeWords := "a card holds at most 65536 together, so this is the operator's to type in a terminal"

	f.digest = ""
	if _, err := y.AddAsk(y.ctx, y.bead, summaryAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), wholeWords) {
		t.Errorf("no digest: AddAsk = %v", err)
	}
	f.digest = summaryDigest

	bare := packAsk()
	bare.Run.Argv = []string{"pack apply starter"}
	if _, err := y.AddAsk(y.ctx, y.bead, bare, "yardmaster"); err == nil || !strings.Contains(err.Error(), wholeWords) {
		t.Errorf("a bare over-cap re-apply: AddAsk = %v", err)
	}

	f.plan = largePlan(30, 40000)
	if _, err := y.AddAsk(y.ctx, y.bead, summaryAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "a summary card carries at most 1048576 together, so this is the operator's to type in a terminal") {
		t.Errorf("past the carried bound: AddAsk = %v", err)
	}
	f.plan = largePlan(20, 5000)

	// Copies a removed source left under its name: no record stands, and
	// the import would still replace them.
	left := filepath.Join(home, "sources", "wired", "wired", "limits")
	if err := os.MkdirAll(filepath.Dir(left), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(left, []byte("left\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, summaryAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), wholeWords) {
		t.Errorf("copies left under the name: AddAsk = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(home, "sources", "wired")); err != nil {
		t.Fatal(err)
	}
	plantWired(t, y)
	if _, err := y.AddAsk(y.ctx, y.bead, summaryAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), wholeWords) {
		t.Errorf("a standing source: AddAsk = %v", err)
	}
}

// A re-apply to a named depot is a summary too, with the weight its whole
// card would have: where that is neither trust nor code the unchanged gate
// lets a device approve it, and a mapping's weight still stands on it.
func TestPackApplySummaryReapply(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.firstInstallDepot(t)
	if _, err := y.RecordPack(y.ctx, PackImport{Pack: "starter", Depot: "web", Source: "x"}, true, "mckean"); err != nil {
		t.Fatal(err)
	}
	f := &fakePack{label: "embedded in yardr test1", kind: "git", depot: "web", digest: summaryDigest, noSources: true, plan: largePlan(20, 5000)}
	y.setPack(f)
	seq := y.ask(t, summaryAsk(), "yardmaster")
	got := y.asked(t, seq)
	if !got.Ask.Run.Pack.Summary || strings.Contains(strings.Join(got.Ask.Run.Does, "\n"), "first install") {
		t.Fatalf("the re-apply's card = %+v:\n%s", got.Ask.Run.Pack, strings.Join(got.Ask.Run.Does, "\n"))
	}
	if StrongWeight(got.Ask.Weight) {
		t.Fatalf("the re-apply's weight = %+v, want the whole card's, not strong", got.Ask.Weight)
	}
	if done, err := y.ApproveAsk(WithVia(y.ctx, "mckean", "phone"), y.bead, seq, "mckean", true, nil); err != nil || done.Result != "approved, to be carried out" {
		t.Fatalf("a device's approval of a routine re-apply = %+v, %v", done, err)
	}
	ask := summaryAsk()
	ask.Run.Argv = []string{"pack apply starter --depot web --bind reviewers=claude:opus"}
	bound := y.asked(t, y.ask(t, ask, "yardmaster"))
	if !bound.Ask.Run.Pack.Summary || !StrongWeight(bound.Ask.Weight) {
		t.Fatalf("a re-apply with a bind = %+v, weight %+v", bound.Ask.Run.Pack, bound.Ask.Weight)
	}
}
