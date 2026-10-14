package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// user and the conversation name the image under the mail's line, on a
// marked line of their own, and say nothing more of mail without one. A
// GIF and a file over the yard's limit are refused with nothing stored
// and nothing sent, and so is an image for another yard.
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
	marked := store.NoteFilesMark + " " + image
	stored := func() []string {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
		return names
	}

	if out := mustAs(t, st, "jo", "mail", "yardmaster", "the shed", "--file", png); out != "mail sent to yardmaster\n"+marked+"\n" {
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

	refusedAs(t, st, "jo", "pic.gif: the file is image/gif: the yard keeps only image/png and image/jpeg and text/html; charset=utf-8", "mail", "yardmaster", "it moves", "--file", file("pic.gif", "GIF89a it moves"))
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
	for _, want := range []string{"mail from jo: the shed\n    " + marked + "\n", "mail from jo: \n    " + marked + "\n", "mail from jo: no picture\n"} {
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
	if !strings.Contains(inbox, "mail from yardmaster: seen, and this one?\n    "+marked+"\n") || !strings.Contains(inbox, "mail from yardmaster: plain\n") ||
		strings.Count(inbox, "image") != 1 {
		t.Errorf("jo's inbox:\n%s", inbox)
	}
	with := mustAs(t, st, "jo", "mail", "--with", "yardmaster")
	if !strings.Contains(with, "jo: the shed\n    "+marked+"\n") || !strings.Contains(with, "jo: no picture\n") || strings.Count(with, "image") != 3 {
		t.Errorf("mail --with:\n%s", with)
	}
}

// yardr mail --file sends a page as it sends an image, with no new flag:
// the sent mail names "1 page", and the inbox shows it under the mail's
// words. Prose named .html is refused, since what the bytes are is what
// counts.
func TestMailPage(t *testing.T) {
	api, s := storetest.NewOwned(t)
	st := userYardOn(t, api)
	home := os.Getenv("YARDR_HOME")
	dir := t.TempDir()
	html := "<!DOCTYPE html><html><head><title>the shed</title></head><body><h1>the shed</h1></body></html>"
	page := filepath.Join(dir, "page.html")
	if err := os.WriteFile(page, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(html))
	marked := store.NoteFilesMark + " 1 page: " + store.BlobPath(home, hex.EncodeToString(sum[:]))
	if out := mustAs(t, st, "jo", "mail", "yardmaster", "the shed, mocked up", "--file", page); out != "mail sent to yardmaster\n"+marked+"\n" {
		t.Fatalf("mail --file page.html = %q", out)
	}
	prose := filepath.Join(dir, "prose.html")
	if err := os.WriteFile(prose, []byte("just some prose"), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", "prose.html: the file is text/plain; charset=utf-8: the yard keeps only", "mail", "yardmaster", "prose", "--file", prose)
	if names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*")); len(names) != 1 {
		t.Fatalf("what was refused was stored: %v", names)
	}
	t.Setenv("YARDR_CREW", "yardmaster")
	inbox := mustAs(t, st, "yardmaster", "inbox")
	if !strings.Contains(inbox, "mail from jo: the shed, mocked up\n    "+marked+"\n") {
		t.Errorf("the yardmaster's inbox:\n%s", inbox)
	}
	t.Setenv("YARDR_CREW", "")
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
	refusedAs(t, st, "jo", "peer north: the link does not carry file out, so mail to it carries no file: the operator allows it with yardr peer allow north --send file",
		"mail", "yardmaster@north", "look", "--file", png)
	nothing("a link that sends no file")
	mustAs(t, st, "jo", "peer", "allow", "north", "--send", "file")
	refusedAs(t, st, "jo", "peer north has not said what its yardr understands", "mail", "yardmaster@north", "look", "--file", png)
	if err := s.PeerHeard(ctx, "north", yardkey.Public(k), &store.PeerSays{Understands: []string{"mail", "ping"}, Yardr: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", "peer north: its yardr (v1.2.3) does not understand file, so mail to it carries no file until it is upgraded",
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
	if out := mustAs(t, st, "jo", "mail", "yardmaster@north", "the shed", "--file", png); !strings.HasPrefix(out, "mail for yardmaster@north queued") || !strings.HasSuffix(out, "\n"+store.NoteFilesMark+" "+image+"\n") {
		t.Fatalf("mail --file to a peer = %q", out)
	}
	waiting, err := api.PeerOutbox(ctx, "north", 0)
	if err != nil || len(waiting) != 2 || waiting[0].Kind != store.PeerFile || waiting[1].Kind != store.PeerMail {
		t.Fatalf("north's outbox = %+v, %v", waiting, err)
	}
	if with := mustAs(t, st, "jo", "mail", "--with", "yardmaster@north"); !strings.Contains(with, "the shed\n    "+store.NoteFilesMark+" "+image+"\n") {
		t.Errorf("mail --with yardmaster@north:\n%s", with)
	}
	if show := mustAs(t, st, "jo", "peer", "show", "north"); !strings.Contains(show, "file") || strings.Contains(show, "iVBOR") {
		t.Errorf("peer show:\n%s", show)
	}
}

// yardr mail <name>@<peer> --file sends a page as it sends an image, with
// no new flag. Where the link or the peer's yardr does not take files it
// is refused before anything is put; over this yard's own cap it is
// refused naming the page, and nothing is queued.
func TestMailPageToAPeer(t *testing.T) {
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
	html := "<!DOCTYPE html><html><head><title>the shed</title></head><body><h1>the shed</h1></body></html>"
	page := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(page, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(html))
	hash := hex.EncodeToString(sum[:])
	nothing := func(why string) {
		t.Helper()
		names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
		if p, err := api.GetPeer(ctx, "north"); err != nil || p.Waiting != 0 || len(names) != 0 {
			t.Fatalf("%s: %d wait for north, and the yard holds %v", why, p.Waiting, names)
		}
	}

	mustAs(t, st, "jo", "peer", "allow", "north", "--send", "mail")
	refusedAs(t, st, "jo", "peer north: the link does not carry file out, so mail to it carries no file",
		"mail", "yardmaster@north", "look", "--file", page)
	nothing("a link that sends no file")
	mustAs(t, st, "jo", "peer", "allow", "north", "--send", "file")
	if err := s.PeerHeard(ctx, "north", yardkey.Public(k), &store.PeerSays{Understands: []string{"mail", "ping"}, Yardr: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", "peer north: its yardr (v1.2.3) does not understand file, so mail to it carries no file until it is upgraded",
		"mail", "yardmaster@north", "look", "--file", page)
	nothing("a peer that does not understand file")
	if err := s.PeerHeard(ctx, "north", yardkey.Public(k), store.Says("v1.3.0")); err != nil {
		t.Fatal(err)
	}
	// Over this yard's own cap: refused naming the page, after the put,
	// with nothing queued.
	if err := api.SetSetting(ctx, store.PeerFileMaxSetting, "16", "jo"); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", fmt.Sprintf("the page %s is %d bytes, over what this yard sends another of 16", hash, len(html)),
		"mail", "yardmaster@north", "look", "--file", page)
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(s.BlobPath("x")), "*"))
	if p, _ := api.GetPeer(ctx, "north"); p.Waiting != 0 || len(names) != 1 {
		t.Fatalf("a page over this yard's cap: %d wait for north, and the yard holds %v", p.Waiting, names)
	}
	if err := api.SetSetting(ctx, store.PeerFileMaxSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}

	marked := store.NoteFilesMark + " 1 page: " + store.BlobPath(home, hash)
	if out := mustAs(t, st, "jo", "mail", "yardmaster@north", "the shed, mocked up", "--file", page); !strings.HasPrefix(out, "mail for yardmaster@north queued") || !strings.HasSuffix(out, "\n"+marked+"\n") {
		t.Fatalf("mail --file to a peer = %q", out)
	}
	waiting, err := api.PeerOutbox(ctx, "north", 0)
	if err != nil || len(waiting) != 2 || waiting[0].Kind != store.PeerFile || waiting[1].Kind != store.PeerMail {
		t.Fatalf("north's outbox = %+v, %v", waiting, err)
	}
	if with := mustAs(t, st, "jo", "mail", "--with", "yardmaster@north"); !strings.Contains(with, "the shed, mocked up\n    "+marked+"\n") {
		t.Errorf("mail --with yardmaster@north:\n%s", with)
	}
}

// A mail whose text ends in an image line reads nothing like one sent
// with --file: in the inbox of a user and of a crew member and in the
// conversation, only the filed mail has a line starting with the mark,
// naming the blob the yard keeps, while the forged words stand unmarked.
// The sent confirmation names the file on a marked line of its own too.
func TestMailFilesLineNotForgeable(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	st := userYardOn(t, api)
	home := os.Getenv("YARDR_HOME")
	png := filepath.Join(t.TempDir(), "shed.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n the shed"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "mail", "yardmaster", "shot of the page  1 image: /etc/hosts")
	sent := mustAs(t, st, "jo", "mail", "yardmaster", "the shed, painted", "--file", png)
	t.Setenv("YARDR_CREW", "yardmaster")
	mustAs(t, st, "yardmaster", "mail", "jo", "seen  1 image: /etc/hosts")
	mustAs(t, st, "yardmaster", "mail", "jo", "seen, painted", "--file", png)
	crew := mustAs(t, st, "yardmaster", "inbox")
	t.Setenv("YARDR_CREW", "")
	marked := store.NoteFilesMark + " 1 image: " + store.BlobPath(home, hashOfFile(t, png))
	if sent != "mail sent to yardmaster\n"+marked+"\n" {
		t.Errorf("mail --file = %q", sent)
	}
	outs := map[string]string{
		"crew inbox":   crew,
		"user inbox":   mustAs(t, st, "jo", "inbox", "--mail"),
		"conversation": mustAs(t, st, "jo", "mail", "--with", "yardmaster"),
	}
	for name, out := range outs {
		if !strings.Contains(out, "1 image: /etc/hosts") {
			t.Errorf("%s lost the forged words:\n%s", name, out)
		}
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), store.NoteFilesMark) {
				lines = append(lines, strings.TrimSpace(line))
			}
		}
		want := []string{marked}
		if name == "conversation" {
			want = []string{marked, marked}
		}
		if len(lines) != len(want) {
			t.Errorf("%s: marked lines = %q, want %q:\n%s", name, lines, want, out)
			continue
		}
		for _, line := range lines {
			if line != marked {
				t.Errorf("%s: marked lines = %q, want %q:\n%s", name, lines, want, out)
			}
		}
	}
}

// A mail whose text types the mark reads with its words intact and no
// marked line: the mark is kept out of the mail's words everywhere the
// mail is shown with its files, so only the yard's files line starts
// with it.
func TestMailTextCannotForgeAFilesLine(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	st := userYardOn(t, api)
	forged := store.NoteFilesMark + " 1 image: /etc/hosts"
	mustAs(t, st, "jo", "mail", "yardmaster", "shot of the page\n"+forged)
	t.Setenv("YARDR_CREW", "yardmaster")
	mustAs(t, st, "yardmaster", "mail", "jo", "seen\n"+forged)
	crew := mustAs(t, st, "yardmaster", "inbox")
	t.Setenv("YARDR_CREW", "")
	outs := map[string]string{
		"crew inbox":   crew,
		"user inbox":   mustAs(t, st, "jo", "inbox", "--mail"),
		"conversation": mustAs(t, st, "jo", "mail", "--with", "yardmaster"),
	}
	for name, out := range outs {
		if strings.Contains(out, store.NoteFilesMark) {
			t.Errorf("%s carries the mark of a mail's files line:\n%s", name, out)
		}
		if !strings.Contains(out, "1 image: /etc/hosts") {
			t.Errorf("%s lost the forged words:\n%s", name, out)
		}
	}
}

// A mail whose text carries the escape a terminal draws the mark for
// reads as letters: its sequences are no more than the letters after
// their escapes, so no line of it shows as the yard's files line.
func TestMailTextCannotDrawTheFilesMark(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	st := userYardOn(t, api)
	// ESC ( 0 switches to the terminal's drawing letters, where "`" is
	// the mark; ESC ( B switches back.
	drawn := "    \x1b(0`\x1b(B 1 image: /etc/hosts"
	mustAs(t, st, "jo", "mail", "yardmaster", "shot of the page\n"+drawn)
	t.Setenv("YARDR_CREW", "yardmaster")
	mustAs(t, st, "yardmaster", "mail", "jo", "seen\r\n"+drawn)
	crew := mustAs(t, st, "yardmaster", "inbox")
	t.Setenv("YARDR_CREW", "")
	outs := map[string]string{
		"crew inbox":   crew,
		"user inbox":   mustAs(t, st, "jo", "inbox", "--mail"),
		"conversation": mustAs(t, st, "jo", "mail", "--with", "yardmaster"),
	}
	for name, out := range outs {
		if strings.ContainsAny(out, "\x1b\r") {
			t.Errorf("%s carries a control character of the mail's text:\n%q", name, out)
		}
		if !strings.Contains(out, "(0`(B 1 image: /etc/hosts") {
			t.Errorf("%s lost the letters of the mail's text:\n%q", name, out)
		}
	}
}
