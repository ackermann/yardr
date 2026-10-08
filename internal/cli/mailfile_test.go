package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardr mail --file puts the image in the yard, once however often it is
// sent, and sends mail that names it; the inbox of a crew member and of a
// user and the conversation end the mail's line with where the image is,
// and say nothing more of mail without one. A GIF and a file over the
// yard's limit are refused with nothing stored and nothing sent, and so is
// an image for another yard.
func TestMailFile(t *testing.T) {
	api, s := storetest.NewOwned(t)
	st := userYardOn(t, api)
	home := os.Getenv("YARDR_HOME")
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
	png := file("pic.txt", "\x89PNG\r\n\x1a\n the gate")
	sum := sha256.Sum256([]byte("\x89PNG\r\n\x1a\n the gate"))
	image := "1 image: " + store.BlobPath(home, hex.EncodeToString(sum[:]))
	stored := func() []string {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
		return names
	}

	if out := mustAs(t, st, "jo", "mail", "yardmaster", "the gate", "--file", png); out != "mail sent to yardmaster  "+image+"\n" {
		t.Fatalf("mail --file = %q", out)
	}
	if got := stored(); len(got) != 1 || got[0] != s.BlobPath(hex.EncodeToString(sum[:])) {
		t.Fatalf("the yard holds %v", got)
	}
	// Again, and without a text: the same blob, not a second.
	mustAs(t, st, "jo", "mail", "yardmaster", "--file", png)
	mustAs(t, st, "jo", "mail", "yardmaster", "no picture")
	if got := stored(); len(got) != 1 {
		t.Fatalf("after a second send the yard holds %v", got)
	}

	refusedAs(t, st, "jo", "pic.gif: the file is image/gif: mail carries only image/png and image/jpeg", "mail", "yardmaster", "it moves", "--file", file("pic.gif", "GIF89a it moves"))
	refusedAs(t, st, "jo", "big.png: the file is 9437201 bytes, over the yard's limit of 8388608", "mail", "yardmaster", "big", "--file",
		file("big.png", "\x89PNG\r\n\x1a\n the gate"+strings.Repeat("\x00", 9<<20)))
	refusedAs(t, st, "jo", "no such file", "mail", "yardmaster", "gone", "--file", filepath.Join(dir, "none.png"))
	// The good one before the bad is put, and the mail is not sent.
	refusedAs(t, st, "jo", "pic.gif", "mail", "yardmaster", "both", "--file", png, "--file", filepath.Join(dir, "pic.gif"))
	refusedAs(t, st, "jo", "images do not cross the link yet", "mail", "yardmaster@airy", "look", "--file", png)
	refusedAs(t, st, "jo", "accepts 2 arg(s)", "mail", "yardmaster")
	if got := stored(); len(got) != 1 {
		t.Fatalf("what was refused was stored: %v", got)
	}

	t.Setenv("YARDR_CREW", "yardmaster")
	inbox := mustAs(t, st, "yardmaster", "inbox")
	for _, want := range []string{"mail from jo: the gate  " + image + "\n", "mail from jo: " + image + "\n", "mail from jo: no picture\n"} {
		if !strings.Contains(inbox, want) {
			t.Errorf("the yardmaster's inbox has no %q:\n%s", want, inbox)
		}
	}
	if strings.Count(inbox, "mail from jo") != 3 || strings.Count(inbox, "image") != 2 {
		t.Errorf("the yardmaster's inbox:\n%s", inbox)
	}
	mustAs(t, st, "yardmaster", "mail", "jo", "seen, and this one?", "--file", png)
	mustAs(t, st, "yardmaster", "mail", "jo", "plain")
	t.Setenv("YARDR_CREW", "")
	inbox = mustAs(t, st, "jo", "inbox", "--mail")
	if !strings.Contains(inbox, "mail from yardmaster: seen, and this one?  "+image+"\n") || !strings.Contains(inbox, "mail from yardmaster: plain\n") ||
		strings.Count(inbox, "image") != 1 {
		t.Errorf("jo's inbox:\n%s", inbox)
	}
	with := mustAs(t, st, "jo", "mail", "--with", "yardmaster")
	if !strings.Contains(with, "jo: the gate  "+image+"\n") || !strings.Contains(with, "jo: no picture\n") || strings.Count(with, "image") != 3 {
		t.Errorf("mail --with:\n%s", with)
	}
}
