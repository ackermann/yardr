package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func mailTextYard(t *testing.T) store.API {
	t.Helper()
	s := storetest.New(t)
	if _, err := s.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return s
}

// A 10 KB text is three mails, marked (1/3)..(3/3) in order, each within a
// mail; a text of one mail's size is one mail, as it is.
func TestMailTextSplits(t *testing.T) {
	t.Parallel()
	s := mailTextYard(t)
	ctx := context.Background()
	text := strings.Repeat("x", 10<<10)
	if err := store.MailText(s, ctx, "yardmaster", text, "jo"); err != nil {
		t.Fatal(err)
	}
	letters, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 3 {
		t.Fatalf("%d mails, %v", len(letters), err)
	}
	for i, l := range letters {
		if head, body, _ := strings.Cut(l.Text, "\n"); head != fmt.Sprintf("(%d/3)", i+1) ||
			len(l.Text) > store.MaxMailBytes || strings.Trim(body, "x") != "" {
			t.Fatalf("mail %d has %d bytes and begins %q", i+1, len(l.Text), head)
		}
	}
	if bodies := letters[0].Text[6:] + letters[1].Text[6:] + letters[2].Text[6:]; bodies != text {
		t.Fatal("the mails do not make the text again")
	}
}

// A text past the most mails one text is sent as is refused whole, naming
// the file it goes as instead, and nothing is sent.
func TestMailTextTooLong(t *testing.T) {
	t.Parallel()
	s := mailTextYard(t)
	ctx := context.Background()
	text := strings.Repeat("x", store.MaxMailParts*store.MaxMailBytes+1)
	if err := store.MailText(s, ctx, "yardmaster", text, "jo"); !errors.Is(err, store.ErrMailTooLong) ||
		!strings.Contains(err.Error(), "--file") {
		t.Fatalf("a text of %d parts = %v", len(store.SplitMail(text)), err)
	}
	letters, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 0 {
		t.Fatalf("%d mails sent of a refused text, %v", len(letters), err)
	}
}

// Files attached to a long text go with the first part only.
func TestMailTextFilesFirstOnly(t *testing.T) {
	t.Parallel()
	api, _ := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := api.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MailText(api, ctx, "yardmaster", strings.Repeat("x", 10<<10), "jo",
		store.BlobFile{Hash: ref.Hash, Name: "shed.png"}); err != nil {
		t.Fatal(err)
	}
	letters, _, err := api.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 3 {
		t.Fatalf("%d mails, %v", len(letters), err)
	}
	if len(letters[0].Files) != 1 || len(letters[1].Files) != 0 || len(letters[2].Files) != 0 {
		t.Fatalf("the files went %d/%d/%d, want 1/0/0",
			len(letters[0].Files), len(letters[1].Files), len(letters[2].Files))
	}
}

// failingMail is a yard that takes no mail from the nth on.
type failingMail struct {
	store.API
	n int
}

func (f *failingMail) Mail(ctx context.Context, to, text, actor string, files ...store.BlobFile) error {
	if f.n--; f.n < 0 {
		return errors.New("the store is away")
	}
	return f.API.Mail(ctx, to, text, actor, files...)
}

// A part that is not taken ends the send: the error says how many went, and
// no later part arrives without it.
func TestMailTextStopsAtRefusal(t *testing.T) {
	t.Parallel()
	s := &failingMail{API: mailTextYard(t), n: 1}
	if err := store.MailText(s, context.Background(), "yardmaster", strings.Repeat("x", 10<<10), "jo"); err == nil ||
		!strings.Contains(err.Error(), "1 of 3 mails went to yardmaster") {
		t.Fatalf("after one part = %v", err)
	}
	letters, _, err := s.Conversation(context.Background(), "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 1 || !strings.HasPrefix(letters[0].Text, "(1/3)\n") {
		t.Fatalf("%d mails after the failure, %v", len(letters), err)
	}
}
