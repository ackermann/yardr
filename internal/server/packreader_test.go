package server

import (
	"context"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

func packReaderYard(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/y.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	return st
}

// The server's reading of an embedded source pack holds its declared
// sources' texts and command files from the binary itself.
func TestReadEmbeddedSourcePack(t *testing.T) {
	st := packReaderYard(t)
	r, err := readEmbeddedPack(context.Background(), "telegram", "", store.Mappings{}, st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "source" || len(r.Sources) != 1 || r.Sources[0].Name != "telegram" {
		t.Fatalf("the telegram reading = %+v", r)
	}
	if !strings.HasPrefix(r.Label, "embedded in yardr ") {
		t.Fatalf("its label = %q", r.Label)
	}
	progs := map[string]bool{}
	for _, f := range r.Sources[0].Files {
		progs[f.Program] = true
		if f.SHA256 == "" || f.Text == "" {
			t.Fatalf("command file %+v holds no bytes or hash", f)
		}
	}
	if !progs["telegram/thread"] || !progs["telegram/normalize"] {
		t.Fatalf("its command files = %v", progs)
	}
	r, err = readEmbeddedPack(context.Background(), "pushover", "", store.Mappings{}, st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) != 1 || len(r.Sources[0].Files) != 1 || r.Sources[0].Files[0].Program != "pushover/notify" ||
		r.Sources[0].Files[0].SHA256 == "" || r.Sources[0].Files[0].Text == "" {
		t.Fatalf("the pushover reading = %+v", r.Sources)
	}
	if _, err := readEmbeddedPack(context.Background(), "nosuchpack", "", store.Mappings{}, st, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "yardr embeds no pack of that name") {
		t.Fatalf("a name yardr does not embed = %v", err)
	}
}

// The server's reading carries the pack's presets with their grants, in
// file order, for the setup card to count as pack show prints them.
func TestReadEmbeddedPackCarriesPresets(t *testing.T) {
	st := packReaderYard(t)
	r, err := readEmbeddedPack(context.Background(), "google-mail", "", store.Mappings{}, st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Presets) != 3 || r.Presets[0].Name != "read" || r.Presets[1].Name != "drafts" || r.Presets[2].Name != "everything" {
		t.Fatalf("the google-mail presets = %+v", r.Presets)
	}
	want := [][]string{{"read"}, {"read", "draft"}, {"read", "draft", "draft_edit"}}
	for i, p := range r.Presets {
		if len(p.Grants) != 1 || p.Grants[0].Source != "google-mail" {
			t.Fatalf("preset %s grants %+v", p.Name, p.Grants)
		}
		if strings.Join(p.Grants[0].Calls, ",") != strings.Join(want[i], ",") {
			t.Errorf("preset %s grants %v, want %v", p.Name, p.Grants[0].Calls, want[i])
		}
	}
}

// The server's preset reading resolves by source against the packs the
// binary embeds: the one pack of kind source declaring the source, with
// the build it is embedded in and the preset's calls of that source.
// An unknown preset and a source no pack declares are refused with the
// terminal named; two packs declaring one source is refused where it is
// resolved (internal/pack), which no two embedded packs do.
func TestPresetReaderFor(t *testing.T) {
	read := presetReaderFor()
	pr, err := read("google-mail", "drafts")
	if err != nil || pr.Pack != "google-mail" || pr.Preset != "drafts" ||
		!strings.HasPrefix(pr.Label, "embedded in yardr ") ||
		strings.Join(pr.Calls, ",") != "read,draft" {
		t.Fatalf("google-mail drafts = %+v, %v", pr, err)
	}
	// A pack of two sources answers each source's calls alone.
	pr, err = read("google-slides", "append")
	if err != nil || pr.Pack != "google-docs" || strings.Join(pr.Calls, ",") != "read,slide" {
		t.Fatalf("google-slides append = %+v, %v", pr, err)
	}
	if _, err := read("google-mail", "nosuch"); err == nil ||
		!strings.Contains(err.Error(), "pack google-mail has no preset nosuch") ||
		!strings.Contains(err.Error(), "yardr pack show google-mail") ||
		!strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("an unknown preset = %v", err)
	}
	if _, err := read("crm", "read"); err == nil ||
		!strings.Contains(err.Error(), "source crm: no embedded pack declares it") ||
		!strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("a source no embedded pack declares = %v", err)
	}
}

// A depot pack with no standing import, and then with one, is read
// against the record's depot with a plan.
func TestReadEmbeddedDepotPack(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	if _, err := readEmbeddedPack(ctx, "starter", "", store.Mappings{}, st, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "no standing import") {
		t.Fatalf("a first install = %v", err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Path: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "yardr", Source: "embedded in yardr test"}, false, "mckean"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	r, err := readEmbeddedPack(ctx, "starter", "", store.Mappings{}, st, home)
	if err != nil {
		t.Fatal(err)
	}
	if r.Depot != "yardr" || r.Plan == nil || len(r.Plan.Objects) == 0 {
		t.Fatalf("the starter reading = depot %q, plan %+v", r.Depot, r.Plan)
	}
	// The group the import would create for the assembly starts its cmd with
	// the pack's env, which the card carries.
	if i := slices.IndexFunc(r.Plan.Objects, func(o store.AskPlanObject) bool { return o.Kind == "group" && o.Name == "yardr-assembly" }); i < 0 ||
		r.Plan.Objects[i].Action != "create" || !slices.Equal(r.Plan.Objects[i].Env, []string{"YARDR_LAND=squash"}) {
		t.Fatalf("the starter reading's assembly = %+v, want a create with YARDR_LAND=squash", r.Plan.Objects)
	}

	// The plan an approval compares whole holds nothing of the label the
	// pack was loaded under: the same pack embedded by another build plans
	// the same objects, so a build that moved alone moves none of them.
	sub, err := fs.Sub(packs.FS, "starter")
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Load(sub, packs.Embedded+" in yardr another-build")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pack.Build(ctx, st, p, pack.Options{Depot: "yardr", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	other := askPlanOf(sub, plan, pack.Answers{}, nil, nil)
	if len(other.Objects) != len(r.Plan.Objects) {
		t.Fatalf("under another build's label the plan has %d objects, not %d", len(other.Objects), len(r.Plan.Objects))
	}
	for i, o := range r.Plan.Objects {
		if !reflect.DeepEqual(o, other.Objects[i]) {
			t.Fatalf("under another build's label the plan's object %s %s reads otherwise:\n%+v\nnot\n%+v", o.Kind, o.Name, other.Objects[i], o)
		}
	}
	if !reflect.DeepEqual(other, r.Plan) {
		t.Fatalf("under another build's label the plan reads otherwise beyond its objects:\n%+v\nnot\n%+v", other, r.Plan)
	}

	// Named to a depot that does not hold the pack, the reading builds
	// its plan against that depot: a first install can be read at all.
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	first, err := readEmbeddedPack(ctx, "starter", "web", store.Mappings{}, st, home)
	if err != nil {
		t.Fatal(err)
	}
	if first.Depot != "web" || first.Plan == nil || len(first.Plan.Objects) == 0 {
		t.Fatalf("the first-install reading = depot %q, plan %+v", first.Depot, first.Plan)
	}
	if i := slices.IndexFunc(first.Plan.Objects, func(o store.AskPlanObject) bool { return o.Kind == "group" && o.Name == "web-builders" }); i < 0 ||
		first.Plan.Objects[i].Action != "create" {
		t.Fatalf("the first-install reading's builders = %+v, want a create in depot web", first.Plan.Objects)
	}

	// The mappings answer the asks they name: a group the yard has takes
	// the pack's builders, and a bind answers the reviewers' agent.
	if err := st.PutGroup(ctx, store.Group{Name: "takers", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	mapped, err := readEmbeddedPack(ctx, "starter", "web", store.Mappings{
		Groups: map[string]string{"builders": "takers"},
		Binds:  []string{"reviewers=claude:opus"},
	}, st, home)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Depot != "web" || mapped.Mappings.Groups["builders"] != "takers" {
		t.Fatalf("the mapped reading = depot %q, mappings %+v", mapped.Depot, mapped.Mappings)
	}
	if i := slices.IndexFunc(mapped.Plan.Objects, func(o store.AskPlanObject) bool { return o.Kind == "group" && o.Name == "takers" }); i < 0 ||
		mapped.Plan.Objects[i].Action != "takes" {
		t.Fatalf("the mapped reading's builders = %+v, want takers to take them", mapped.Plan.Objects)
	}
	for _, answered := range []string{"group.web-reviewers.kind", "group.web-reviewers.model", "group.web-reviewers.args"} {
		for _, missing := range mapped.Plan.Missing {
			if strings.HasPrefix(missing, answered+":") {
				t.Errorf("the bind left %q still asking:\n%s", answered, strings.Join(mapped.Plan.Missing, "\n"))
			}
		}
	}
	// What no mapping names still asks: the crew member the pack wants.
	foundCrewAsk := false
	for _, missing := range mapped.Plan.Missing {
		if strings.HasPrefix(missing, "crew.yardmaster.") {
			foundCrewAsk = true
		}
	}
	if !foundCrewAsk {
		t.Errorf("the mapped reading asks nothing of the crew:\n%s", strings.Join(mapped.Plan.Missing, "\n"))
	}
}

// The real telegram pack, read by the server and carded by the store:
// the card shows the credential in its path and each command file's
// hash whole, and the approval records it to be carried out.
func TestPackApplyTelegramCard(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	st.SetPackReader(packReaderFor(t.TempDir()))
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	var telegramSeq uint64
	for _, name := range []string{"linear", "github", "pushover", "telegram"} {
		seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Add the " + name + " source?", To: "mckean",
			Context: []string{"the yard has no source that tells a user outside the yard"},
			Run:     &store.AskRun{Argv: []string{"pack apply " + name}}}, "yardmaster")
		if err != nil {
			t.Fatalf("ask for embedded source pack %s: %v", name, err)
		}
		if name == "telegram" {
			telegramSeq = seq
		}
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var joined string
	for _, r := range store.ResolveAsks(notes, notes[0].At) {
		if r.Note.Seq == telegramSeq && r.Ask.Run != nil {
			joined = strings.Join(r.Ask.Run.Does, "\n")
		}
	}
	for _, want := range []string{
		"pack telegram (embedded in yardr ",
		"of kind source, into the yard: it adds 1 source and copies 2 command files (6 commands)",
		"POST /bot{credential}/sendMessage",
		"TELEGRAM_BOT_TOKEN",
		"in the request path",
		"command file telegram/thread:",
		"[telegram/thread, tell] (notify.image, handed the credential)",
		"command file telegram/normalize:",
		"still owed after it: the credential, by a secret ask for telegram",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the telegram card lacks %q:\n%s", want, joined)
		}
	}
	done, err := st.ApproveAsk(ctx, b.ID, telegramSeq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "approved, to be carried out" {
		t.Fatalf("the approval recorded %q", done.Result)
	}
	if _, err := st.GetSource(ctx, "telegram"); err == nil {
		t.Fatalf("a pack approval applied the pack in its write")
	}
}

// Each google pack applies by one card: mail, drive, docs with slides
// and calendar each fit the card's line limit, where the one google
// pack's 75 lines did not. The count per pack is pinned, so a
// declaration that grows past the limit fails here and not at a
// person's card.
func TestPackApplyGoogleCardsFit(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	st.SetPackReader(packReaderFor(t.TempDir()))
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pack    string
		sources []string
		lines   int
	}{
		{"google-mail", []string{"google-mail"}, 25},
		{"google-drive", []string{"google-drive"}, 31},
		{"google-docs", []string{"google-docs", "google-slides"}, 36},
		{"google-calendar", []string{"google-calendar"}, 23},
	} {
		t.Run(tc.pack, func(t *testing.T) {
			seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Add the " + tc.pack + " sources?", To: "mckean",
				Context: []string{"the yard has no source for this part of Google"},
				Run:     &store.AskRun{Argv: []string{"pack apply " + tc.pack}}}, "yardmaster")
			if err != nil {
				t.Fatalf("ask for embedded source pack %s: %v", tc.pack, err)
			}
			notes, err := st.Notes(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var does []string
			for _, r := range store.ResolveAsks(notes, notes[0].At) {
				if r.Note.Seq == seq && r.Ask.Run != nil {
					does = r.Ask.Run.Does
				}
			}
			if len(does) == 0 {
				t.Fatalf("the %s card holds no lines", tc.pack)
			}
			if len(does) > store.MaxRunSourceCardLines {
				t.Fatalf("the %s card runs to %d lines, over the %d a card holds", tc.pack, len(does), store.MaxRunSourceCardLines)
			}
			if len(does) != tc.lines {
				t.Errorf("the %s card runs to %d lines, want the pinned %d", tc.pack, len(does), tc.lines)
			}
			joined := strings.Join(does, "\n")
			for _, name := range tc.sources {
				if !strings.Contains(joined, "source "+name) {
					t.Errorf("the %s card names no source %s:\n%s", tc.pack, name, joined)
				}
			}
		})
	}
}

// googleDocsPreBuildDecl is the google-docs declaration as ee8a23ae
// carried it: write with its static body, before the build key, and no
// replace call. A yard that applied the pack then re-applies today's
// pack against this declaration.
const googleDocsPreBuildDecl = `# Google Docs as a declared source: a document read, and appended to at
# the end with a style for the paragraph. One source per host, so Docs
# is its own; the empty file it fills is made by the drive source's doc
# call. No other batchUpdate request is declared.
#
# Checked against the discovery document
# (https://www.googleapis.com/discovery/v1/apis/docs/v1/rest) on 16
# October 2026: documents.get answers the document with title and body;
# documents.batchUpdate takes {requests} and answers {documentId,
# replies}. InsertText takes endOfSegmentLocation, which appends at the
# end of the body with no index; UpdateParagraphStyle takes a range, a
# paragraphStyle with namedStyleType (NORMAL_TEXT, TITLE, HEADING_1
# through HEADING_6) and fields, and styles the paragraphs the range
# overlaps.
#
# The credential mints like google-mail's: GOOGLE_ACCOUNT names the
# account (an address, no secret) and the operator sets mint_command to
# contrib/google-mint, which no pack brings. Docs takes no idempotency
# key, so the file names none: a write tried again after a lost answer
# is appended twice.
name: google-docs
about: Google Docs read and appended to, with a style for the paragraph.
base: https://docs.googleapis.com
credential:
  env: GOOGLE_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [GOOGLE_ACCOUNT]
calls:
  doc:
    kind: read
    about: Read one document, title and text
    params:
      docId: { type: string, pattern: "^[A-Za-z0-9_-]+$", max: 200, log: true }
    request:
      method: GET
      path: /v1/documents/{docId}
    answer: { select: [title, body], max: 262144 }   # the text is in body's paragraphs
  write:
    kind: update
    about: Append text at the end, styling a paragraph
    params:
      docId: { type: string, pattern: "^[A-Za-z0-9_-]+$", max: 200, log: true }
      # Free text: its value is in no event. Appended at the end of the
      # body; to start a new paragraph, begin it with a blank line.
      text: { type: string, max: 50000 }
      style: { type: enum, values: [NORMAL_TEXT, TITLE, HEADING_1, HEADING_2] }   # NORMAL_TEXT writes plain
      # The range the style applies to, from a doc read: the paragraphs
      # it overlaps are styled. Text appended to the last paragraph read
      # at endIndex E styles with start=E-1, end=E; a new paragraph begun
      # with a blank line starts at E, so start=E, end=E+1.
      start: { type: int }
      end: { type: int }
    request:
      method: POST
      path: /v1/documents/{docId}:batchUpdate
      body:
        requests:
          - insertText: { endOfSegmentLocation: {}, text: "{text}" }
          - updateParagraphStyle: { range: { startIndex: "{start}", endIndex: "{end}" }, paragraphStyle: { namedStyleType: "{style}" }, fields: "namedStyleType" }
    answer: { select: [documentId, replies] }
`

// A yard that applied google-docs before the build key re-applies
// today's pack on one card: the changed declaration cards its calls
// change and each build file's change beside the unchanged slides,
// inside the card's line limit. The count is pinned, as the
// first-install counts are above, so a declaration that grows past the
// limit fails here and not at a person's card.
func TestPackApplyGoogleDocsChangeCardFits(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	home := t.TempDir()
	st.SetPackReader(packReaderFor(home))
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "google-docs.yaml", googleDocsPreBuildDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	reading, err := readEmbeddedPack(ctx, "google-docs", "", store.Mappings{}, st, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range reading.Sources {
		if s.Name == "google-slides" {
			if _, err := st.PutSource(ctx, "google-slides.yaml", s.Text, "mckean"); err != nil {
				t.Fatal(err)
			}
		}
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.AddAsk(ctx, b.ID, store.Ask{Question: "Apply the google-docs update?", To: "mckean",
		Context: []string{"the yard's google-docs predates the build key"},
		Run:     &store.AskRun{Argv: []string{"pack apply google-docs"}}}, "yardmaster")
	if err != nil {
		t.Fatalf("ask for the google-docs re-apply: %v", err)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var does []string
	for _, r := range store.ResolveAsks(notes, notes[0].At) {
		if r.Note.Seq == seq && r.Ask.Run != nil {
			does = r.Ask.Run.Does
		}
	}
	if len(does) == 0 {
		t.Fatalf("the google-docs change card holds no lines")
	}
	if len(does) > store.MaxRunSourceCardLines {
		t.Fatalf("the google-docs change card runs to %d lines, over the %d a card holds", len(does), store.MaxRunSourceCardLines)
	}
	// 40, the most a card holds: the credential line of the change
	// says the mint command the pack now brings (yardr-66qd).
	if len(does) != 40 {
		t.Errorf("the google-docs change card runs to %d lines, want the pinned 40", len(does))
	}
	joined := strings.Join(does, "\n")
	for _, want := range []string{
		"replaces 2 sources",
		"it would replace the source google-docs the yard has under that name.",
		"source google-docs calls: ",
		"call replace (update)",
		"command file google-docs/build-write: [google-docs/build-write] (call.write.build, handed the credential): sha256 (none) -> sha256 ",
		"command file google-docs/build-replace: [google-docs/build-replace] (call.replace.build, handed the credential): sha256 (none) -> sha256 ",
		"source google-slides",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the google-docs change card lacks %q:\n%s", want, joined)
		}
	}
}
