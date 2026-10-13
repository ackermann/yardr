package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardr bead note --file puts the image in the yard and writes a note that
// names it; bead show prints the image line under the note and names it in
// --json, and prime ends the note's block with it. A GIF, a file over the
// yard's limit and a missing file are refused with nothing written, as are
// --ack with --file and a note without words.
func TestBeadNoteFile(t *testing.T) {
	api, s := storetest.NewOwned(t)
	st := userYardOn(t, api)
	home := os.Getenv("YARDR_HOME")
	ctx := context.Background()
	b, err := api.CreateBead(ctx, store.NewBead{Title: "the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// The name says nothing: what the bytes are is what counts.
	png := file("shot.txt", "\x89PNG\r\n\x1a\n the shed")
	hash := hashOfFile(t, png)
	image := "1 image: " + store.BlobPath(home, hash)

	out := mustAs(t, st, "jo", "bead", "note", b.ID, "the shed, painted", "--file", png)
	if want := " on " + b.ID + "  " + image + "\n"; !strings.HasPrefix(out, "note ") || !strings.HasSuffix(out, want) {
		t.Fatalf("bead note --file = %q", out)
	}
	if got, err := os.ReadFile(s.BlobPath(hash)); err != nil || string(got) != "\x89PNG\r\n\x1a\n the shed" {
		t.Fatalf("the yard holds %q, %v", got, err)
	}
	// bead show prints the image line under the note.
	show := mustAs(t, st, "jo", "bead", "show", b.ID)
	if !strings.Contains(show, "    the shed, painted\n    "+image+"\n") {
		t.Errorf("bead show:\n%s", show)
	}
	// --json names the files.
	var shown struct {
		Notes []store.Note `json:"notes"`
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "bead", "show", b.ID, "--json")), &shown); err != nil ||
		len(shown.Notes) != 1 || len(shown.Notes[0].Files) != 1 || shown.Notes[0].Files[0].Hash != hash {
		t.Fatalf("bead show --json = %+v, %v", shown, err)
	}
	// prime ends the note's block with it.
	prime := mustAs(t, st, "jo", "prime", "--bead", b.ID)
	if !strings.Contains(prime, "the shed, painted  "+image) {
		t.Errorf("prime:\n%s", prime)
	}
	// A second note without files reads as before.
	mustAs(t, st, "jo", "bead", "note", b.ID, "plain")
	if show := mustAs(t, st, "jo", "bead", "show", b.ID); strings.Count(show, "image") != 1 {
		t.Errorf("bead show after a plain note:\n%s", show)
	}

	refusedAs(t, st, "jo", "pic.gif: the file is image/gif: the yard keeps only image/png and image/jpeg",
		"bead", "note", b.ID, "it moves", "--file", file("pic.gif", "GIF89a it moves"))
	refusedAs(t, st, "jo", "big.png: the file is 9437201 bytes, over the yard's limit of 8388608",
		"bead", "note", b.ID, "big", "--file", file("big.png", "\x89PNG\r\n\x1a\n the shed"+strings.Repeat("\x00", 9<<20)))
	refusedAs(t, st, "jo", "no such file", "bead", "note", b.ID, "gone", "--file", filepath.Join(dir, "none.png"))
	refusedAs(t, st, "jo", "note --ack records that this session read the note: it takes no --file",
		"bead", "note", b.ID, "--ack", "1", "--file", png)
	refusedAs(t, st, "jo", "accepts 2 arg(s)", "bead", "note", b.ID, "--file", png)
	if notes, err := api.Notes(ctx, b.ID); err != nil || len(notes) != 2 {
		t.Errorf("what was refused was written: %+v, %v", notes, err)
	}
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
	if len(names) != 1 {
		t.Errorf("what was refused was stored: %v", names)
	}
}

// hashOfFile is the SHA-256 of the file's bytes, in hex: the name its blob
// goes by.
func hashOfFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
