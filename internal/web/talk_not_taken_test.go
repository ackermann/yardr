package web

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

type talkNotTakenBlobStore struct{ store.API }

func (talkNotTakenBlobStore) MailBlob(context.Context, string, string) (*store.BlobRef, error) {
	return nil, store.ErrNotFound
}

func (talkNotTakenBlobStore) NoteBlob(context.Context, string) (*store.BlobRef, error) {
	return nil, store.ErrNotFound
}

// A peer file explicitly marked NotTaken was never held by this yard, so
// talk keeps the peer's reason instead of calling it a file the yard lost.
func TestTalkNotTakenFile(t *testing.T) {
	t.Parallel()
	s, err := NewServer(nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.st = talkNotTakenBlobStore{}
	letter := store.Letter{From: "north@yard", Files: []store.BlobRef{{
		Hash: strings.Repeat("a", 64), Type: "image/png", NotTaken: "it did not arrive",
	}}}

	shown, err := s.talkLetters(context.Background(), "jo", "yardmaster", []store.Letter{letter})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[0].Missing != nil {
		t.Fatalf("talkLetters = %+v, want the peer's not-taken file without a missing marker", shown)
	}
	var body bytes.Buffer
	if err := s.pages["talk"].ExecuteTemplate(&body, "letter", shown[0]); err != nil {
		t.Fatal(err)
	}
	if want := "image aaaaaaaa: not taken: it did not arrive"; !strings.Contains(body.String(), want) {
		t.Errorf("talk letter lacks %q: %s", want, body.String())
	}
	if strings.Contains(body.String(), "the yard no longer has it") || strings.Contains(body.String(), "<img") {
		t.Errorf("talk letter misstates or draws a file this yard did not take: %s", body.String())
	}
}
