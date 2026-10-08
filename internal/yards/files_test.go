package yards_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yards"
)

// What a type is read from is the first bytes, so this is an image as far
// as a yard asks.
var gatePNG = []byte("\x89PNG\r\n\x1a\n" + "a picture of the gate")

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
			if err := y.api.PeerRefused(ctx, p.Name, m.Seq, err.Error()); err != nil {
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
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate, painted", "jo", ref.Hash); err != nil {
		t.Fatal(err)
	}
	out, _ := south.api.PeerOutbox(ctx, "north", 0)
	if len(out) != 2 || out[0].Kind != "file" || out[1].Kind != "mail" {
		t.Fatalf("south's outbox = %+v", out)
	}
	south.post(t, toNorth)

	path := north.own.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, gatePNG) {
		t.Fatalf("north's blob = %q, %v", got, err)
	}
	e, files := mailed(t, north)
	if len(files) != 1 || files[0] != *ref || e.Data["text"] != "the gate, painted" || e.Actor != "jo@south" {
		t.Fatalf("north's mail = %+v, files %+v", e, files)
	}
	home := filepath.Dir(filepath.Dir(path))
	if line := store.FilesLine(home, files); line != "1 image: "+path {
		t.Fatalf("the file line = %q", line)
	}
	talk, err := north.api.Conversation(ctx, "yardmaster", "jo@south", 0)
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
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "", "jo", ref.Hash, ref.Hash); err != nil {
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

// A yard whose link does not take files takes no bytes: the file is
// refused 403 with the reason and logged, the sender gives it up and logs
// that, and the mail still arrives, saying so where the path would be.
func TestFileRefusedWhereTheLinkTakesNone(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, []string{"mail"})
	ctx := context.Background()
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate", "jo", ref.Hash); err != nil {
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
// size, and the mail says it. Bytes that are not the hash's, or no image,
// are refused and not stored, and mail that names a file which never came
// says that.
func TestFileRefusedForWhatItIs(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "16", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate", "jo", ref.Hash); err != nil {
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
	gif := []byte("GIF89a" + "one that moves")
	for name, err := range map[string]error{
		"bytes of another hash": file("f1", hashOf([]byte("another")), gatePNG),
		"a GIF":                 file("f2", hashOf(gif), gif),
		"no bytes":              file("f3", hashOf(nil), nil),
		"a path for a hash":     file("f4", "../../yard.db", gatePNG),
	} {
		if !errors.Is(err, yards.ErrRefused) || !strings.HasSuffix(err.Error(), "it does not read the file, or its bytes are not what it was named by") {
			t.Errorf("%s = %v", name, err)
		}
	}
	if got := blobs(t, north); len(got) != 0 {
		t.Fatalf("north stored what it refused: %v", got)
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
	if _, files := mailed(t, north); len(files) != 1 || files[0].NotTaken != "it did not arrive" || files[0].Type != "" {
		t.Fatalf("mail that names a file which did not come has %+v", files)
	}
	// And the good file is taken at last, under the type its bytes say.
	if err := file("f5", ref.Hash, gatePNG); err != nil {
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
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(want string) {
		t.Helper()
		if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate", "jo", ref.Hash); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("MailPeer with an image = %v, want %q", err, want)
		}
		if p, _ := south.api.GetPeer(ctx, "north"); p.Waiting != 0 || len(south.events(t, "mail")) != 0 {
			t.Fatalf("refused mail was queued: %+v", p)
		}
	}
	refused("the link does not carry file out, so mail to it carries no image: the operator allows it with yardr peer allow north --send file")
	if _, err := south.api.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"file"}}, "jo"); err != nil {
		t.Fatal(err)
	}

	old := &store.PeerSays{Understands: []string{"mail", "ping"}, Yardr: "v1.2.3"}
	if err := south.own.PeerHeard(ctx, "north", north.pub(), old); err != nil {
		t.Fatal(err)
	}
	refused("peer north: its yardr (v1.2.3) does not understand file, so mail to it carries no image until it is upgraded")
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
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate", "jo", ref.Hash); err != nil {
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
	big := append(bytes.Clone(gatePNG), bytes.Repeat([]byte{0x5a}, 2<<20)...)
	ref, err := south.api.PutBlob(ctx, big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate, large", "jo", ref.Hash); err != nil {
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
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := south.api.MailPeer(ctx, "yardmaster", "north", "the gate", "jo", ref.Hash); err != nil {
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

// Mail metadata is the sender's word. Once the file message has been
// accepted, the verified blob metadata wins even if the mail claims a
// different size or type.
func TestTakenFileUsesVerifiedMetadata(t *testing.T) {
	south, north, toNorth := fileYards(t, true, mailAndFile, mailAndFile)
	ctx := context.Background()
	if err := north.api.SetSetting(ctx, store.PeerFileMaxSetting, "64", "jo"); err != nil {
		t.Fatal(err)
	}
	ref, err := south.api.PutBlob(ctx, gatePNG)
	if err != nil {
		t.Fatal(err)
	}
	fileBody, _ := json.Marshal(peerFileForTest(*ref, gatePNG))
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
