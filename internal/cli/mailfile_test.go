package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
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
	png := file("pic.txt", "\x89PNG\r\n\x1a\n the shed")
	sum := sha256.Sum256([]byte("\x89PNG\r\n\x1a\n the shed"))
	image := "1 image: " + store.BlobPath(home, hex.EncodeToString(sum[:]))
	stored := func() []string {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
		return names
	}

	if out := mustAs(t, st, "jo", "mail", "yardmaster", "the shed", "--file", png); out != "mail sent to yardmaster  "+image+"\n" {
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
		file("big.png", "\x89PNG\r\n\x1a\n the shed"+strings.Repeat("\x00", 9<<20)))
	refusedAs(t, st, "jo", "no such file", "mail", "yardmaster", "gone", "--file", filepath.Join(dir, "none.png"))
	// The good one before the bad is put, and the mail is not sent.
	refusedAs(t, st, "jo", "pic.gif", "mail", "yardmaster", "both", "--file", png, "--file", filepath.Join(dir, "pic.gif"))
	// To a yard no link names: refused before the image is put.
	refusedAs(t, st, "jo", "peer airy: not found", "mail", "yardmaster@airy", "look", "--file", file("other.png", "\x89PNG\r\n\x1a\n the fence"))
	refusedAs(t, st, "jo", "accepts 2 arg(s)", "mail", "yardmaster")
	if got := stored(); len(got) != 1 {
		t.Fatalf("what was refused was stored: %v", got)
	}

	t.Setenv("YARDR_CREW", "yardmaster")
	inbox := mustAs(t, st, "yardmaster", "inbox")
	for _, want := range []string{"mail from jo: the shed  " + image + "\n", "mail from jo: " + image + "\n", "mail from jo: no picture\n"} {
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
	if !strings.Contains(with, "jo: the shed  "+image+"\n") || !strings.Contains(with, "jo: no picture\n") || strings.Count(with, "image") != 3 {
		t.Errorf("mail --with:\n%s", with)
	}
}

// yardr mail <name>@<peer> --file sends the image with the mail when the
// link carries file out and the peer's yardr understands it. Where either
// does not hold it is refused at once: no image is put and nothing queued.
func TestMailFileToAPeer(t *testing.T) {
	api, s := storetest.NewOwned(t)
	st := userYardOn(t, api)
	home := os.Getenv("YARDR_HOME")
	ctx := t.Context()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(k), URL: "http://north.test"}, "jo"); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(t.TempDir(), "shed.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n the shed"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("\x89PNG\r\n\x1a\n the shed"))
	image := "1 image: " + store.BlobPath(home, hex.EncodeToString(sum[:]))
	nothing := func(why string) {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
		if p, err := api.GetPeer(ctx, "north"); err != nil || p.Waiting != 0 || len(names) != 0 {
			t.Fatalf("%s: %d wait for north, and the yard holds %v", why, p.Waiting, names)
		}
	}

	mustAs(t, st, "jo", "peer", "allow", "north", "--send", "mail")
	refusedAs(t, st, "jo", "peer north: the link does not carry file out, so mail to it carries no image: the operator allows it with yardr peer allow north --send file",
		"mail", "yardmaster@north", "look", "--file", png)
	nothing("a link that sends no file")
	mustAs(t, st, "jo", "peer", "allow", "north", "--send", "file")
	refusedAs(t, st, "jo", "peer north has not said what its yardr understands", "mail", "yardmaster@north", "look", "--file", png)
	if err := s.PeerHeard(ctx, "north", yardkey.Public(k), &store.PeerSays{Understands: []string{"mail", "ping"}, Yardr: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", "peer north: its yardr (v1.2.3) does not understand file, so mail to it carries no image until it is upgraded",
		"mail", "yardmaster@north", "look", "--file", png)
	nothing("a peer that does not understand file")
	// Doctor says it of a link that sends files, and of no other.
	cfg := &config.Config{Home: home}
	if part, err := peerPart(ctx, cfg, st); err != nil || !strings.Contains(strings.Join(part.Notes, "\n"), "the link sends files, and north: its yardr (v1.2.3) does not understand file") {
		t.Fatalf("doctor's peers = %+v, %v", part, err)
	}

	if err := s.PeerHeard(ctx, "north", yardkey.Public(k), store.Says("v1.3.0")); err != nil {
		t.Fatal(err)
	}
	if part, _ := peerPart(ctx, cfg, st); strings.Contains(strings.Join(part.Notes, "\n"), "file") {
		t.Fatalf("doctor's peers still say %+v", part.Notes)
	}
	if out := mustAs(t, st, "jo", "mail", "yardmaster@north", "the shed", "--file", png); !strings.HasPrefix(out, "mail for yardmaster@north queued") || !strings.HasSuffix(out, "  "+image+"\n") {
		t.Fatalf("mail --file to a peer = %q", out)
	}
	waiting, err := api.PeerOutbox(ctx, "north", 0)
	if err != nil || len(waiting) != 2 || waiting[0].Kind != store.PeerFile || waiting[1].Kind != store.PeerMail {
		t.Fatalf("north's outbox = %+v, %v", waiting, err)
	}
	if with := mustAs(t, st, "jo", "mail", "--with", "yardmaster@north"); !strings.Contains(with, "the shed  "+image+"\n") {
		t.Errorf("mail --with yardmaster@north:\n%s", with)
	}
	if show := mustAs(t, st, "jo", "peer", "show", "north"); !strings.Contains(show, "file") || strings.Contains(show, "iVBOR") {
		t.Errorf("peer show:\n%s", show)
	}
}
