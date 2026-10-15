package yards_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// What a type is read from is the first bytes, so this is an image as far
// as a yard asks.
var shedPNG = []byte("\x89PNG\r\n\x1a\n" + "a picture of the shed")

// And this is a page: HTML bytes, which cross as files do.
var shedPage = []byte("<!DOCTYPE html><html><head><title>the shed</title></head><body><p>the shed, painted</p></body></html>")

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var mailAndFile = []string{"file", "mail"}

// fileYards are south, which writes, and north, whose yardmaster is
// written to. South calls north when url is set, and north asks south when
// it is not. Each has heard what the other's yardr understands.
func fileYards(t *testing.T, url bool, southSends, northTakes []string) (south, north *yard, toNorth store.Peer) {
	t.Helper()
	south, north = newYard(t), newYard(t)
	ctx := context.Background()
	toNorth = south.add(t, "north", north, url, southSends, nil)
	north.add(t, "south", south, !url, nil, northTakes)
	if _, err := north.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	north.expose(t, "south", "yardmaster")
	if err := south.own.PeerHeard(ctx, "north", north.pub(), yards.Says()); err != nil {
		t.Fatal(err)
	}
	if err := north.own.PeerHeard(ctx, "south", south.pub(), yards.Says()); err != nil {
		t.Fatal(err)
	}
	return south, north, toNorth
}

// post does what the daemon's pass does with what waits in y for p: each
// message is posted, oldest first, and ended as sent or as refused.
func (y *yard) post(t *testing.T, p store.Peer) {
	t.Helper()
	ctx := context.Background()
	msgs, err := y.api.PeerOutbox(ctx, p.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		switch err := y.caller().Push(ctx, p, m.PeerMessage); {
		case err == nil:
			err = y.api.PeerDelivered(ctx, p.Name, m.Seq)
			if err != nil {
				t.Fatal(err)
			}
		case errors.Is(err, yards.ErrRefused):
			var over *yards.Refused
			if errors.As(err, &over) {
				err = y.api.PeerRefusedRate(ctx, p.Name, m.Seq, over.Limit, over.Until)
			} else {
				err = y.api.PeerRefused(ctx, p.Name, m.Seq, err.Error())
			}
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("posting %s %s: %v", m.Kind, m.ID, err)
		}
	}
}

// mailed is the one mail in the inbox of north's yardmaster, with its
// files.
func mailed(t *testing.T, north *yard) (store.Event, []store.BlobRef) {
	t.Helper()
	page, err := north.api.Inbox(context.Background(), "yardmaster", 0)
	if err != nil {
		t.Fatal(err)
	}
	var mail []store.Event
	for _, e := range page.Events {
		if e.Kind == "mail" {
			mail = append(mail, e)
		}
	}
	if len(mail) != 1 {
		t.Fatalf("the inbox of north's yardmaster = %+v", mail)
	}
	return mail[0], store.MailFiles(mail[0])
}

func blobs(t *testing.T, y *yard) []string {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(y.own.BlobPath("x")), "*"))
	return names
}

// With file allowed both ways, mail with an image arrives with the blob in
// the other yard's blobs, under the same hash, and its line names the file
// there; the conversation shows it. The file crosses before the mail that
// names it, and the same image sent again is one blob still.
func TestMailWithAnImageCrosses(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed, painted", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	out, _ := south.api.PeerOutbox(ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "file" || out[1].Kind != "mail" {
		t.Fatalf("south's outbox = %+v", out)
	}
	south.post(t, toNorth)

	path := north.own.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, shedPNG) {
		t.Fatalf("north's blob = %q, %v", got, err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0] != *ref || e.Data["text"] != "the shed, painted" || e.Actor != "jo@south" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
	home := filepath.Dir(filepath.Dir(path))
	if line := store.FilesLine(home, files); line != "1 image: "+path {
		t.Fatalf("the file line = %q", line)
	}
	talk, _, err := north.api.Conversation(ctx, "yardmaster", "jo@south", 0, 0, 0)
	if err != nil || len(talk) != 1 || len(talk[0].Files) != 1 || talk[0].Files[0] != *ref {
		t.Fatalf("north's conversation = %+v, %v", talk, err)
	}
	// South's own mail event names the image too, and no event of either
	// yard has its bytes.
	if sent := south.events(t, "mail"); len(sent) != 1 || len(store.MailFiles(sent[0])) != 1 {
		t.Fatalf("south's mail event = %+v", sent)
	}
	for _, e := range append(north.events(t, "peer_message_received"), south.events(t, "peer_message_sent")...) {
		if raw, _ := json.Marshal(e); len(raw) > 512 {
			t.Fatalf("an event carries the body: %s", raw)
		}
	}

	// Again, without a text: taken, and the yard holds one blob.
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "", "jo", store.BlobFile{Hash: ref.Hash}, store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	if out, _ := south.api.PeerOutbox(ctx, "north", 0); len(out) != 2 {
		t.Fatalf("an image named twice is queued as %+v", out)
	}
	south.post(t, toNorth)
	if got := blobs(t, north); len(got) != 1 || len(north.events(t, "peer_message_refused")) != 0 || len(south.events(t, "peer_message_failed")) != 0 {
		t.Fatalf("after the second mail north holds %v", got)
	}
}

// With file allowed both ways, mail with a page arrives with the blob in
// the other yard's blobs, under the same hash and the type its bytes say,
// and its line names the page there; the conversation shows it. The type
// the sender claimed is not trusted: bytes that say HTML land as a page
// whatever they came under. Nothing here opens it: a mailed page stays
// unopenable until mail viewing is decided (yardr-k5j6).
func TestMailWithAPageCrosses(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPage)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Type != "text/html; charset=utf-8" || !ref.IsPage() {
		t.Fatalf("south's blob = %+v", ref)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed, mocked up", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	out, _ := south.api.PeerOutbox(ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "file" || out[1].Kind != "mail" {
		t.Fatalf("south's outbox = %+v", out)
	}
	south.post(t, toNorth)

	path := north.own.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, shedPage) {
		t.Fatalf("north's blob = %q, %v", got, err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0] != *ref || e.Data["text"] != "the shed, mocked up" || e.Actor != "jo@south" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
	home := filepath.Dir(filepath.Dir(path))
	if line := store.FilesLine(home, files); line != "1 page: "+path {
		t.Fatalf("the file line = %q", line)
	}
	talk, _, err := north.api.Conversation(ctx, "yardmaster", "jo@south", 0, 0, 0)
	if err != nil || len(talk) != 1 || len(talk[0].Files) != 1 || talk[0].Files[0] != *ref {
		t.Fatalf("north's conversation = %+v, %v", talk, err)
	}

	// The receiver reads the type itself, as for a file put there: page
	// bytes claimed as an image land as a page.
	if err := north.api.AckInbox(ctx, "yardmaster", e.Seq); err != nil {
		t.Fatal(err)
	}
	other := append(bytes.Clone(shedPage), "\n<!-- again -->"...)
	claimed := store.BlobRef{Hash: hashOf(other), Type: "image/png", Bytes: int64(len(other))}
	fileBody, _ := json.Marshal(peerFileForTest(claimed, other))
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "f9", Kind: "file", Body: fileBody}); err != nil {
		t.Fatal(err)
	}
	mailBody, _ := json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "again", "files": []store.BlobRef{claimed}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m9", Kind: "mail", Body: mailBody}); err != nil {
		t.Fatal(err)
	}
	if _, files := mailed(t, north); len(files) != 1 || !files[0].IsPage() || files[0].Type != "text/html; charset=utf-8" {
		t.Fatalf("mail that claims an image of page bytes has %+v", files)
	}
}

// With file allowed both ways, mail with a file of any kind arrives with
// the blob in the other yard's blobs and the name the mail gives it, and
// its line names both there.
func TestMailWithAFileCrosses(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	report := []byte("%PDF-1.4 a report, as far as the yard asks")
	ref, err := south.api.PutBlob(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind() != "file" {
		t.Fatalf("south's blob = %+v", ref)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the report", "jo", store.BlobFile{Hash: ref.Hash, Name: "report.pdf"}); err != nil {
		t.Fatal(err)
	}
	out, _ := south.api.PeerOutbox(ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "file" || out[1].Kind != "mail" {
		t.Fatalf("south's outbox = %+v", out)
	}
	south.post(t, toNorth)

	path := north.own.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, report) {
		t.Fatalf("north's blob = %q, %v", got, err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0].Hash != ref.Hash || files[0].Name != "report.pdf" || files[0].NotTaken != "" ||
		e.Data["text"] != "the report" || e.Actor != "jo@south" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
	home := filepath.Dir(filepath.Dir(path))
	if line := store.FilesLine(home, files); line != "1 file: "+path+" (report.pdf)" {
		t.Fatalf("the file line = %q", line)
	}
	talk, _, err := north.api.Conversation(ctx, "yardmaster", "jo@south", 0, 0, 0)
	if err != nil || len(talk) != 1 || len(talk[0].Files) != 1 || talk[0].Files[0].Name != "report.pdf" {
		t.Fatalf("north's conversation = %+v, %v", talk, err)
	}
}

// An empty file is still a file: its hash names the empty bytes, which
// cross and arrive under the name the mail gives them.
func TestEmptyFileCrosses(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Bytes != 0 || ref.Kind() != "file" {
		t.Fatalf("south's empty blob = %+v", ref)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the empty file", "jo", store.BlobFile{Hash: ref.Hash, Name: "empty.dat"}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)

	path := north.own.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || len(got) != 0 {
		t.Fatalf("north's empty blob = %q, %v", got, err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0].Hash != ref.Hash || files[0].Name != "empty.dat" || files[0].NotTaken != "" ||
		e.Data["text"] != "the empty file" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
}

// The longest allowed name crosses even when JSON expands each character.
func TestMaxLengthEscapedNameCrosses(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	name := strings.Repeat("<", store.MaxBlobName)
	if err := store.CheckBlobName(name); err != nil {
		t.Fatalf("the %d-byte name was refused: %v", len(name), err)
	}
	ref, err := south.api.PutBlob(ctx, []byte("%PDF-1.4 the report"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the report", "jo", store.BlobFile{Hash: ref.Hash, Name: name}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)
	_, files := mailed(t, north)
	if len(files) != 1 || files[0].Hash != ref.Hash || files[0].Name != name || files[0].NotTaken != "" {
		t.Fatalf("north's mail file = %+v", files)
	}
}

// A page over the receiver's cap for a peer's file is refused with the
// size, as an image is, and the mail says it.
func TestPageRefusedForWhatItIs(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "64", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, shedPage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)
	_, files := mailed(t, north)
	want := fmt.Sprintf("it is %d bytes, over this yard's limit of 64 for a file from another yard", len(shedPage))
	if len(files) != 1 || files[0].NotTaken != want {
		t.Fatalf("north's mail has %+v", files)
	}
	if line := store.FilesLine("/home", files); line != "1 page: not taken: "+want {
		t.Fatalf("the file line = %q", line)
	}
	refused := north.events(t, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedTooLarge || refused[0].Data["bytes"] != float64(len(shedPage)) || refused[0].Data["limit"] != float64(64) {
		t.Fatalf("north's peer_message_refused = %+v", refused)
	}
	if failed := south.events(t, "peer_message_failed"); len(failed) != 1 || !strings.HasSuffix(failed[0].Data["reason"].(string), "the file is larger than it takes from another yard") {
		t.Fatalf("south's peer_message_failed = %+v", failed)
	}
}

// Who wrote mail with a page the other yard refused is told so by mail
// from the yard, naming the page, and their conversation shows it as not
// taken where the mail itself was delivered. A second mail with an image
// and a page, of which the link then takes the image but not the page,
// names each by kind.
func TestWriterIsToldOfARefusedPage(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, []string{"mail"})
	ctx := context.Background()
	if _, err := south.api.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, shedPage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)

	const why = "refused by the peer: its link does not carry file in"
	box, err := south.api.UserInbox(ctx, "jo", 0)
	if err != nil || len(box.Mail) != 1 || box.Mail[0].From != "yard" ||
		!strings.HasPrefix(box.Mail[0].Text, "the page of your mail to yardmaster@north of ") || !strings.HasSuffix(box.Mail[0].Text, " was not taken: "+why) {
		t.Fatalf("jo's mail = %+v, %v", box, err)
	}
	talk, _, err := south.api.Conversation(ctx, "jo", "yardmaster@north", 0, 0, 0)
	if err != nil || len(talk) != 1 || talk[0].Delivery != store.MailSent || len(talk[0].Files) != 1 || talk[0].Files[0].NotTaken != why {
		t.Fatalf("south's conversation = %+v, %v", talk, err)
	}
	if line := store.FilesLine("/home", talk[0].Files); line != "1 page: not taken: "+why {
		t.Fatalf("the file line = %q", line)
	}
	first, files := mailed(t, north)
	if len(files) != 1 || files[0].NotTaken != "the link does not take files" {
		t.Fatalf("north's mail has %+v", files)
	}
	if err := north.api.AckInbox(ctx, "yardmaster", first.Seq); err != nil {
		t.Fatal(err)
	}

	// The link takes files now, but within a cap the image meets and the
	// page does not.
	if _, err := north.api.AllowPeer(ctx, "south", store.PeerAllow{Receive: []string{"file"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "64", "jo"); err != nil {
		t.Fatal(err)
	}
	image, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "both", "jo", store.BlobFile{Hash: image.Hash}, store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)
	_, files = mailed(t, north)
	over := fmt.Sprintf("it is %d bytes, over this yard's limit of 64 for a file from another yard", len(shedPage))
	if len(files) != 2 || files[0] != *image || files[1].NotTaken != over {
		t.Fatalf("north's second mail has %+v", files)
	}
	home := filepath.Dir(filepath.Dir(north.own.BlobPath(image.Hash)))
	if line := store.FilesLine(home, files); line != "1 image, 1 page: "+north.own.BlobPath(image.Hash)+"; not taken: "+over {
		t.Fatalf("the file line = %q", line)
	}
	box, err = south.api.UserInbox(ctx, "jo", 0)
	if err != nil || len(box.Mail) != 2 {
		t.Fatalf("jo's mail = %+v, %v", box, err)
	}
	var told string
	for _, m := range box.Mail {
		if strings.Contains(m.Text, "page 2 of 2") {
			told = m.Text
		}
	}
	if !strings.HasPrefix(told, "page 2 of 2 of your mail to yardmaster@north of ") ||
		!strings.HasSuffix(told, " was not taken: refused by the peer: the file is larger than it takes from another yard") {
		t.Fatalf("jo's mail = %+v", box.Mail)
	}
}

// A yard whose link does not take files takes no bytes: the file is
// refused 403 with the reason and logged, the sender gives it up and logs
// that, and the mail still arrives, saying so where the path would be.
func TestFileRefusedWhereTheLinkTakesNone(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, []string{"mail"})
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)

	_, files := mailed(t, north)
	if len(files) != 1 || files[0].NotTaken != "the link does not take files" || files[0].Hash != ref.Hash {
		t.Fatalf("north's mail has %+v", files)
	}
	if line := store.FilesLine("/home", files); line != "1 image: not taken: the link does not take files" {
		t.Fatalf("the file line = %q", line)
	}
	if got := blobs(t, north); len(got) != 0 {
		t.Fatalf("north took bytes: %v", got)
	}
	ref0 := north.events(t, "peer_message_refused")
	if len(ref0) != 1 || ref0[0].Data["kind"] != "file" || ref0[0].Data["reason"] != store.RefusedKind || ref0[0].Data["direction"] != "in" {
		t.Fatalf("north's peer_message_refused = %+v", ref0)
	}
	failed := south.events(t, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["kind"] != "file" || !strings.HasSuffix(failed[0].Data["reason"].(string), "refused by the peer: its link does not carry file in") {
		t.Fatalf("south's peer_message_failed = %+v", failed)
	}
	// What was given up stays without its bytes, and nothing waits.
	p, _ := south.api.GetPeer(ctx, "north")
	if p.Waiting != 0 || p.Failed != 1 {
		t.Fatalf("south's outbox for north = %+v", p)
	}
}

// A file over the receiver's cap for a peer's file is refused with the
// size, and the mail says it. Bytes that are not the hash's are refused
// and not stored, and mail that names a file which never came says that.
// Any bytes go: a GIF is taken.
func TestFileRefusedForWhatItIs(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "16", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)
	first, files := mailed(t, north)
	if len(files) != 1 || files[0].NotTaken != "it is 29 bytes, over this yard's limit of 16 for a file from another yard" {
		t.Fatalf("north's mail has %+v", files)
	}
	refused := north.events(t, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedTooLarge || refused[0].Data["bytes"] != float64(29) || refused[0].Data["limit"] != float64(16) {
		t.Fatalf("north's peer_message_refused = %+v", refused)
	}
	if failed := south.events(t, "peer_message_failed"); len(failed) != 1 || !strings.HasSuffix(failed[0].Data["reason"].(string), "the file is larger than it takes from another yard") {
		t.Fatalf("south's peer_message_failed = %+v", failed)
	}
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}

	file := func(id, hash string, data []byte) error {
		body, _ := json.Marshal(map[string]any{"hash": hash, "type": "image/png", "bytes": len(data), "data": data})
		return south.caller().Push(ctx, toNorth, store.PeerMessage{ID: id, Kind: "file", Body: body})
	}
	for name, err := range map[string]error{
		"bytes of another hash":       file("f1", hashOf([]byte("another")), shedPNG),
		"empty bytes with wrong hash": file("f3", hashOf([]byte("not empty")), nil),
		"a path for a hash":           file("f4", "../../yard.db", shedPNG),
	} {
		if !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "it does not read the file, or its bytes are not what it was named by") {
			t.Errorf("%s = %v", name, err)
		}
	}
	if got := blobs(t, north); len(got) != 0 {
		t.Fatalf("north stored what it refused: %v", got)
	}
	// A GIF is taken, as any bytes are.
	gif := []byte("GIF89a" + "one that moves")
	if err := file("f2", hashOf(gif), gif); err != nil {
		t.Fatalf("a GIF = %v", err)
	}
	if got := blobs(t, north); len(got) != 1 {
		t.Fatalf("north holds %v, want the GIF", got)
	}
	for _, name := range blobs(t, north) {
		os.Remove(name)
	}
	// The mail that names the first of them is delivered all the same.
	if err := north.api.AckInbox(ctx, "yardmaster", first.Seq); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "look",
		"files": []store.BlobRef{{Hash: hashOf([]byte("another")), Type: "text/html", Bytes: 7}}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m1", Kind: "mail", Body: body}); err != nil {
		t.Fatal(err)
	}
	if _, files := mailed(t, north); len(files) != 1 || files[0].NotTaken != "it did not arrive" || files[0].Type != "text/html" {
		t.Fatalf("mail that names a file which did not come has %+v", files)
	}
	// And the good file is taken at last, under the type its bytes say.
	if err := file("f5", ref.Hash, shedPNG); err != nil {
		t.Fatal(err)
	}
	if got, err := north.own.Blob(ref.Hash); err != nil || *got != *ref {
		t.Fatalf("north's blob = %+v, %v", got, err)
	}
	// Mail that names what no mail may is no mail.
	body, _ = json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "look", "files": []store.BlobRef{{Hash: "../x"}}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m2", Kind: "mail", Body: body}); !errors.Is(err, yards.ErrRefused) {
		t.Fatalf("mail with a path for a file's hash = %v", err)
	}
}

// Mail with an image is refused at once, with nothing queued, for a peer
// whose yardr does not say it understands file, for one that has not said,
// over a link that does not carry file out, and for an image over this
// yard's own cap.
func TestMailWithAnImageRefusedBeforeItIsQueued(t *testing.T) {
	south, north, _ := fileYards(t, true, []string{"mail"}, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(want string) {
		t.Helper()
		if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("MailPeer with an image = %v, want %q", err, want)
		}
		if p, _ := south.api.GetPeer(ctx, "north"); p.Waiting != 0 || len(south.events(t, "mail")) != 0 {
			t.Fatalf("refused mail was queued: %+v", p)
		}
	}
	refused("the link does not carry file out, so mail to it carries no file: the operator allows it with yardr peer allow north --send file")
	if _, err := south.api.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"file"}}, "jo"); err != nil {
		t.Fatal(err)
	}

	old := &store.PeerSays{Understands: []string{"mail", "ping"}, Yardr: "v1.2.3"}
	if err := south.own.PeerHeard(ctx, "north", north.pub(), old); err != nil {
		t.Fatal(err)
	}
	refused("peer north: its yardr (v1.2.3) does not understand file, so mail to it carries no file until it is upgraded")
	if err := south.own.PeerHeard(ctx, "north", north.pub(), nil); err != nil {
		t.Fatal(err)
	}
	refused("peer north has not said what its yardr understands")
	if err := south.own.PeerHeard(ctx, "north", north.pub(), yards.Says()); err != nil {
		t.Fatal(err)
	}

	if err := south.api.SetSetting(ctx, store.PeerFileMaxSetting, "16", "jo"); err != nil {
		t.Fatal(err)
	}
	refused("is 29 bytes, over what this yard sends another of 16 (yardr settings set peer_file_max_bytes")
	if err := south.api.SetSetting(ctx, store.PeerFileMaxSetting, "64", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	// A file is not sent by hand, and the setting has its ceiling.
	if _, err := south.api.PeerSend(ctx, "north", "file", nil, "jo"); err == nil || !strings.Contains(err.Error(), "yardr mail <crew member>@north --file") {
		t.Fatalf("PeerSend of kind file = %v", err)
	}
	if err := south.api.SetSetting(ctx, store.PeerFileMaxSetting, "33554433", "jo"); err == nil || !strings.Contains(err.Error(), "1 to 33554432") {
		t.Fatalf("a cap over what any yard reads = %v", err)
	}
}

// A yard that cannot be called gets a file by asking: one of 2 MiB, larger
// than a page of messages otherwise is, is handed over whole in one pull,
// on its own, and the mail that names it with the next.
func TestFileIsPulled(t *testing.T) {
	south, north, _ := fileYards(t, false, mailAndFile, mailAndFile)
	ctx := context.Background()
	big := append(bytes.Clone(shedPNG), bytes.Repeat([]byte{0x5a}, 2<<20)...)
	ref, err := south.api.PutBlob(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed, large", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	pull := func() *store.PeerPulled {
		t.Helper()
		toSouth, err := north.api.GetPeer(ctx, "south")
		if err != nil {
			t.Fatal(err)
		}
		batch, err := north.caller().Pull(ctx, *toSouth, toSouth.Pulled, 0)
		if err != nil {
			t.Fatal(err)
		}
		res, err := north.api.PeerPulled(ctx, "south", batch)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := pull(); len(res.Taken) != 1 || res.Taken[0].Refused != "" {
		t.Fatalf("the first pull = %+v", res)
	}
	if got, err := os.ReadFile(north.own.BlobPath(ref.Hash)); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("north's blob after one pull: %d bytes, %v", len(got), err)
	}
	if res := pull(); len(res.Taken) != 1 || res.Taken[0].Refused != "" {
		t.Fatalf("the second pull = %+v", res)
	}
	if _, files := mailed(t, north); len(files) != 1 || files[0] != *ref {
		t.Fatalf("north's mail has %+v", files)
	}
	pull()
	if sent := south.events(t, "peer_message_sent"); len(sent) != 2 || sent[0].Data["kind"] != "file" || sent[0].Data["way"] != "pull" {
		t.Fatalf("south's peer_message_sent = %+v", sent)
	}
}

// A file a yard asked for and does not take is told with its next
// question, and given up there as one posted is.
func TestPulledFileRefused(t *testing.T) {
	south, north, _ := fileYards(t, false, mailAndFile, []string{"mail"})
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		toSouth, _ := north.api.GetPeer(ctx, "south")
		batch, err := north.caller().Pull(ctx, *toSouth, toSouth.Pulled, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := north.api.PeerPulled(ctx, "south", batch); err != nil {
			t.Fatal(err)
		}
	}
	if _, files := mailed(t, north); len(files) != 1 || files[0].NotTaken != "the link does not take files" || len(blobs(t, north)) != 0 {
		t.Fatalf("north's mail has %+v", files)
	}
	failed := south.events(t, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["kind"] != "file" || failed[0].Data["reason"] != "refused by the peer: its link does not carry file in" {
		t.Fatalf("south's peer_message_failed = %+v", failed)
	}
}

// A yard counts the files a peer sends in the hour its mail is counted in:
// past as many as that hour's mail could name, a file is refused as
// rate_limited and no byte of it kept, and the refusals of the hour log
// once. A message taken before is dropped by its id, whatever bytes it has
// now: none are kept, and it counts for nothing.
func TestFilesPastTheHourAreRefused(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	file := func(id string, n int) error {
		data := append(bytes.Clone(shedPNG), byte(n>>8), byte(n))
		body, _ := json.Marshal(peerFileForTest(store.BlobRef{Hash: hashOf(data), Type: "image/png", Bytes: int64(len(data))}, data))
		return south.caller().Push(ctx, toNorth, store.PeerMessage{ID: id, Kind: "file", Body: body})
	}
	for i := 0; i < store.PeerFileLimit-1; i++ {
		if err := file("f"+strconv.Itoa(i), i); err != nil {
			t.Fatalf("file %d of the hour: %v", i+1, err)
		}
		if i > 0 {
			continue
		}
		if err := file("f0", store.PeerFileLimit+7); err != nil || len(blobs(t, north)) != 1 {
			t.Fatalf("a file sent again under its id: %v, and north holds %v", err, blobs(t, north))
		}
	}
	// Simultaneous batches at the boundary must not both write a blob based
	// on the same pre-write quota snapshot.
	const concurrent = 8
	start := make(chan struct{})
	errs := make([]error, concurrent)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = file("edge"+strconv.Itoa(i), store.PeerFileLimit+i)
		}(i)
	}
	close(start)
	wg.Wait()
	taken, refusedCount := 0, 0
	for _, err := range errs {
		if err == nil {
			taken++
		} else if errors.Is(err, yards.ErrRefused) {
			refusedCount++
		} else {
			t.Fatalf("a concurrent file at the limit: %v", err)
		}
	}
	if taken != 1 || refusedCount != concurrent-1 {
		t.Fatalf("concurrent files: %d taken, %d refused", taken, refusedCount)
	}
	if got := blobs(t, north); len(got) != store.PeerFileLimit {
		t.Fatalf("north holds %d blobs, want %d", len(got), store.PeerFileLimit)
	}
	for i := range 2 {
		err := file("over"+strconv.Itoa(i), store.PeerFileLimit+i)
		if !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "it has taken as many files from this yard as it takes in an hour") {
			t.Fatalf("a file past the limit = %v", err)
		}
	}
	if got := blobs(t, north); len(got) != store.PeerFileLimit {
		t.Fatalf("north kept a file past the limit: %d blobs", len(got))
	}
	refused := north.events(t, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedRate || refused[0].Data["kind"] != "file" || refused[0].Data["limit"] != float64(store.PeerFileLimit) || refused[0].Data["until"] == nil {
		t.Fatalf("north's peer_message_refused = %+v", refused)
	}
	// The files are counted apart from the mail: mail is taken still.
	body, _ := json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "many pictures"})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m1", Kind: "mail", Body: body}); err != nil {
		t.Fatalf("mail after the files: %v", err)
	}
}

// Who wrote mail with an image the other yard refused is told so by mail
// from the yard, with that yard's reason, and their conversation shows the
// image as not taken where the mail itself was delivered. A second mail
// with the same image, which the link then takes, shows it as sent.
func TestWriterIsToldOfARefusedImage(t *testing.T) {
	for _, way := range []string{"push", "pull"} {
		t.Run(way, func(t *testing.T) {
			south, north, toNorth := fileYards(t, way == "push", mailAndFile, []string{"mail"})
			ctx := context.Background()
			if _, err := south.api.AddUser(ctx, "jo", "jo"); err != nil {
				t.Fatal(err)
			}
			cross := func() {
				t.Helper()
				if way == "push" {
					south.post(t, toNorth)
					return
				}
				for range 2 {
					toSouth, _ := north.api.GetPeer(ctx, "south")
					batch, err := north.caller().Pull(ctx, *toSouth, toSouth.Pulled, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := north.api.PeerPulled(ctx, "south", batch); err != nil {
						t.Fatal(err)
					}
				}
			}
			ref, err := south.api.PutBlob(ctx, shedPNG)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
				t.Fatal(err)
			}
			talk, _, err := south.api.Conversation(ctx, "jo", "yardmaster@north", 0, 0, 0)
			if err != nil || len(talk) != 1 || talk[0].Delivery != store.MailQueued || talk[0].Files[0].NotTaken != "" {
				t.Fatalf("south's conversation before the mail crossed = %+v, %v", talk, err)
			}
			cross()

			const why = "refused by the peer: its link does not carry file in"
			failed := south.events(t, "peer_message_failed")
			sent := south.events(t, "mail")
			if len(failed) != 1 || failed[0].Data["kind"] != "file" || failed[0].Data["hash"] != ref.Hash || failed[0].Data["mail"] != sent[0].Data["id"] {
				t.Fatalf("south's peer_message_failed = %+v", failed)
			}
			box, err := south.api.UserInbox(ctx, "jo", 0)
			if err != nil || len(box.Mail) != 1 || box.Mail[0].From != "yard" ||
				!strings.HasPrefix(box.Mail[0].Text, "the image of your mail to yardmaster@north of ") || !strings.HasSuffix(box.Mail[0].Text, " was not taken: "+why) {
				t.Fatalf("jo's mail = %+v, %v", box, err)
			}
			talk, _, err = south.api.Conversation(ctx, "jo", "yardmaster@north", 0, 0, 0)
			if err != nil || len(talk) != 1 || talk[0].Delivery != store.MailSent || len(talk[0].Files) != 1 || talk[0].Files[0].NotTaken != why {
				t.Fatalf("south's conversation = %+v, %v", talk, err)
			}
			if line := store.FilesLine("/home", talk[0].Files); line != "1 image: not taken: "+why {
				t.Fatalf("the file line = %q", line)
			}

			if _, err := north.api.AllowPeer(ctx, "south", store.PeerAllow{Receive: []string{"file"}}, "jo"); err != nil {
				t.Fatal(err)
			}
			if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "again", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
				t.Fatal(err)
			}
			cross()
			talk, _, err = south.api.Conversation(ctx, "jo", "yardmaster@north", 0, 0, 0)
			if err != nil || len(talk) != 2 || talk[0].Files[0].NotTaken != why || talk[1].Delivery != store.MailSent || talk[1].Files[0] != *ref {
				t.Fatalf("south's conversation after the second mail = %+v, %v", talk, err)
			}
			if box, _ := south.api.UserInbox(ctx, "jo", 0); len(box.Mail) != 1 {
				t.Fatalf("jo was mailed again: %+v", box.Mail)
			}
		})
	}
}

// Mail metadata is the sender's word. Once the file message has been
// accepted, the verified blob metadata wins even if the mail claims a
// different size or type.
func TestTakenFileUsesVerifiedMetadata(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "64", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	fileBody, _ := json.Marshal(peerFileForTest(*ref, shedPNG))
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "f1", Kind: "file", Body: fileBody}); err != nil {
		t.Fatal(err)
	}
	claimed := *ref
	claimed.Bytes = 1000
	claimed.Type = "text/html"
	mailBody, _ := json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "look", "files": []store.BlobRef{claimed}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m1", Kind: "mail", Body: mailBody}); err != nil {
		t.Fatal(err)
	}
	_, files := mailed(t, north)
	if len(files) != 1 || files[0] != *ref {
		t.Fatalf("north's mail files = %+v, want verified blob %+v", files, *ref)
	}
}

func peerFileForTest(ref store.BlobRef, data []byte) map[string]any {
	return map[string]any{"hash": ref.Hash, "type": ref.Type, "bytes": ref.Bytes, "data": data}
}

// A file the receiving yard cannot keep, for no fault of the file (here its
// blobs cannot be written; a full disk is the same), is answered 500 in
// the yard's name, not 400: the sender does not take it as refused, its
// queue keeps the file and the mail behind it, and the next post, once the
// yard can write again, is taken.
func TestFileNotKeptStaysQueued(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the shed", "jo", store.BlobFile{Hash: ref.Hash}); err != nil {
		t.Fatal(err)
	}
	// A file where north's blobs would be: no blob can be written there.
	dir := filepath.Dir(north.own.BlobPath(ref.Hash))
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := south.api.PeerOutbox(ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "file" {
		t.Fatalf("south's outbox = %+v", out)
	}
	body, _ := json.Marshal(out[0].PeerMessage)
	r := signed(south, north, "POST", yards.Path, body)
	resp, answer := r.send(t, north)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a file north cannot keep is answered %d (%s), want 500", resp.StatusCode, answer)
	}
	if err := yardkey.VerifyResponse(north.pub(), resp.StatusCode, r.sig, resp.Header.Get(yardkey.HeaderSig), answer); err != nil {
		t.Fatalf("the 500 is not north's: %v", err)
	}
	if strings.Contains(string(answer), dir) {
		t.Fatalf("the answer names a path of the yard: %s", answer)
	}
	// As the daemon posts it: an error that is no refusal, so it tries again.
	err = south.caller().Push(ctx, toNorth, out[0].PeerMessage)
	if err == nil || errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "answered 500") {
		t.Fatalf("the post of a file north cannot keep = %v", err)
	}
	if out, _ := south.api.PeerOutbox(ctx, "north", 0); len(out) != 2 || out[0].Failed != nil {
		t.Fatalf("south's outbox after the 500 = %+v", out)
	}
	if n := len(south.events(t, "peer_message_failed")) + len(north.events(t, "peer_message_received")) + len(north.events(t, "peer_message_refused")); n != 0 || north.rc.Rejected()["store"] != 2 {
		t.Fatalf("%d events of a file not kept; north rejected %v", n, north.rc.Rejected())
	}
	// What is no message is still 400, and refused for good.
	bad := out[0].PeerMessage
	bad.ID = "not an id"
	if err := south.caller().Push(ctx, toNorth, bad); !errors.Is(err, yards.ErrRefused) {
		t.Fatalf("the post of what is no message = %v", err)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	south.post(t, toNorth)
	if _, files := mailed(t, north); len(files) != 1 || files[0] != *ref {
		t.Fatalf("north's mail, posted again, has %+v", files)
	}
	if out, _ := south.api.PeerOutbox(ctx, "north", 0); len(out) != 0 || len(south.events(t, "peer_message_failed")) != 0 {
		t.Fatalf("south's outbox at the end = %+v", out)
	}
}

type receiveError struct {
	yards.Store
	err error
}

func (s receiveError) PeerReceive(context.Context, string, string, []store.PeerMessage) ([]store.PeerTaken, error) {
	return nil, s.err
}

func TestReceiverTreatsMessageNotFoundAsStoreError(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	north.srv.Close()
	north.rc = yards.NewReceiver(receiveError{Store: north.own, err: store.ErrNotFound}, north.keyFile, nil)
	north.rc.SetClock(func() time.Time { return north.now })
	north.srv = httptest.NewServer(north.rc.Handler())
	t.Cleanup(north.srv.Close)
	toNorth.URL = north.srv.URL

	m := store.PeerMessage{ID: "message-1", Kind: store.PeerMail, Body: json.RawMessage(`{"to":"yardmaster","from":"jo","text":"hello"}`)}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	resp, answer := signed(south, north, "POST", yards.Path, body).send(t, north)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a message processing ErrNotFound is answered %d (%s), want 500", resp.StatusCode, answer)
	}
	if north.rc.Rejected()["store"] != 1 {
		t.Fatalf("store rejection count = %v, want one", north.rc.Rejected())
	}
	if err := south.caller().Push(context.Background(), toNorth, m); err == nil || errors.Is(err, yards.ErrRefused) {
		t.Fatalf("sender treats store ErrNotFound as a permanent refusal: %v", err)
	}
}

// A name arriving from a peer that no letter may carry is kept empty,
// the file still taken: the mail arrives, and its line names no name.
func TestMailWithABadNameArrivesNameless(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, shedPNG)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(peerFileForTest(store.BlobRef{Hash: ref.Hash, Type: "image/png", Bytes: int64(len(shedPNG))}, shedPNG))
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "f1", Kind: "file", Body: body}); err != nil {
		t.Fatal(err)
	}
	mailBody, _ := json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "look",
		"files": []store.BlobRef{{Hash: ref.Hash, Type: "image/png", Bytes: int64(len(shedPNG)), Name: "../yard.db"}}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m1", Kind: "mail", Body: mailBody}); err != nil {
		t.Fatalf("mail with a bad name = %v, want taken", err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0].Hash != ref.Hash || files[0].Name != "" || files[0].NotTaken != "" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
	if line := store.FilesLine("/home", files); line != "1 image: "+store.BlobPath("/home", ref.Hash) {
		t.Fatalf("the file line = %q", line)
	}
	// A control character in the name goes the same way: nothing
	// unvalidated reaches the line.
	mailBody, _ = json.Marshal(map[string]any{"to": "yardmaster", "from": "jo", "text": "look again",
		"files": []store.BlobRef{{Hash: ref.Hash, Type: "image/png", Bytes: int64(len(shedPNG)), Name: "a\nb.png"}}})
	if err := south.caller().Push(ctx, toNorth, store.PeerMessage{ID: "m2", Kind: "mail", Body: mailBody}); err != nil {
		t.Fatalf("mail with a control character = %v, want taken", err)
	}
	page, err := north.api.Inbox(ctx, "yardmaster", 0)
	if err != nil {
		t.Fatal(err)
	}
	var mails []store.Event
	for _, e := range page.Events {
		if e.Kind == "mail" {
			mails = append(mails, e)
		}
	}
	if len(mails) != 2 {
		t.Fatalf("north's inbox = %+v", page.Events)
	}
	if got := store.MailFiles(mails[1]); len(got) != 1 || got[0].Name != "" {
		t.Fatalf("the second mail's files = %+v", got)
	}
}
