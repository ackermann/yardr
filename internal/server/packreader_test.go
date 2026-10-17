package server

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
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
	r, err := readEmbeddedPack(context.Background(), "telegram", st, t.TempDir())
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
	r, err = readEmbeddedPack(context.Background(), "pushover", st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) != 1 || len(r.Sources[0].Files) != 0 {
		t.Fatalf("the pushover reading = %+v", r.Sources)
	}
	if _, err := readEmbeddedPack(context.Background(), "nosuchpack", st, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "yardr embeds no pack of that name") {
		t.Fatalf("a name yardr does not embed = %v", err)
	}
}

// A depot pack with no standing import, and then with one, is read
// against the record's depot with a plan.
func TestReadEmbeddedDepotPack(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	if _, err := readEmbeddedPack(ctx, "starter", st, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "no standing import") {
		t.Fatalf("a first install = %v", err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yardr", Path: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "yardr", Source: "embedded in yardr test"}, false, "mckean"); err != nil {
		t.Fatal(err)
	}
	r, err := readEmbeddedPack(ctx, "starter", st, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.Depot != "yardr" || r.Plan == nil || len(r.Plan.Objects) == 0 {
		t.Fatalf("the starter reading = depot %q, plan %+v", r.Depot, r.Plan)
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
		"of kind source, into the yard: it adds 1 source and copies 2 command files (4 commands)",
		"POST /bot{credential}/sendMessage",
		"TELEGRAM_BOT_TOKEN",
		"in the request path",
		"command file telegram/thread:",
		"command file telegram/normalize:",
		"still owed after it: the credential, by a secret ask for telegram",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the telegram card lacks %q:\n%s", want, joined)
		}
	}
	done, err := st.ApproveAsk(ctx, b.ID, telegramSeq, "mckean")
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

// Each google pack applies by one card: mail, drive, and docs with
// slides each fit the card's line limit, where the one google pack's
// 75 lines did not. The count per pack is pinned, so a declaration
// that grows past the limit fails here and not at a person's card.
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
		{"google-mail", []string{"google-mail"}, 21},
		{"google-drive", []string{"google-drive"}, 29},
		{"google-docs", []string{"google-docs", "google-slides"}, 33},
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
