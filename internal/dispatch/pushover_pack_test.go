package dispatch_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

const (
	// An application token and a user key of the shape Pushover gives
	// them: 30 letters and digits.
	packToken = "aqmK3fVxN7Hz2PdW9sLc4TyRbJe6Uo"
	packKey   = "uQ8nXw5ZrEa1CtYvMk0LgHs7DbFj3P"

	// A 1x1 picture of each kind the pack's command is told apart by.
	packPNG = "89504e470d0a1a0a0000000d4948445200000001000000010802000000907753de0000000c4944415478da6360000000020001e221bc330000000049454e44ae426082a6"
	packGIF = "47494638396101000100800000000000ffffff21f90401000000002c00000000010001000002024401003b"
)

// withPushoverPack has ada told as pack apply leaves her: through the
// pushover pack's own declaration with its base at a stand-in for the
// service, its image command copied under the yard's home, the
// application's token in the source's env file and her user key in hers.
// It returns the stand-in, which keeps what it is sent.
func (y *pushYard) withPushoverPack(t *testing.T) *standin.Server {
	t.Helper()
	for _, tool := range []string{"jq", "curl", "file"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the pack's command is written with it", tool)
		}
	}
	ctx := context.Background()
	s := &standin.Server{PushToken: packToken}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	// The pack as it is, pointed at the stand-in.
	dir := filepath.Join("..", "..", "packs", "pushover")
	text, err := os.ReadFile(filepath.Join(dir, "pushover.yaml"))
	must(t, err)
	text = []byte(strings.Replace(string(text), "base: https://api.pushover.net/1", "base: "+srv.URL+"/1", 1))
	d, err := source.Parse(filepath.Join(dir, "pushover.yaml"), text)
	must(t, err)
	must(t, d.CopyCommandFiles(dir, y.home))
	scripttest.Relink(t, filepath.Join(y.home, "sources", d.Name))
	_, err = y.api.PutSource(ctx, "pushover.yaml", string(text), "jo")
	must(t, err)
	file := func(name, text string) string {
		f := filepath.Join(t.TempDir(), name)
		must(t, os.WriteFile(f, []byte(text), 0o600))
		return f
	}
	_, err = y.api.SetSource(ctx, "pushover", store.SourceEnvFile, file("pushover.env", "PUSHOVER_TOKEN="+packToken+"\n"), "jo")
	must(t, err)
	_, err = y.api.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: file("ada.env", "PUSHOVER_USER="+packKey+"\n")}, "ada")
	must(t, err)
	y.secrets = append(y.secrets, packToken, packKey)
	return s
}

// settle has the daemon's clock go past the grace of the newest push in the
// queue and has it pass once. The store stamps a push with the time the
// test's own clock reads, so a pass of a fixed length after the mail waits
// on how long the test took to set up: this one waits on the push.
func (y *pushYard) settle(t *testing.T) {
	t.Helper()
	var newest time.Time
	for _, p := range y.queued(t) {
		if p.At.After(newest) {
			newest = p.At
		}
	}
	if at := newest.Add(store.PushGrace + time.Second); at.After(y.now) {
		y.now = at
	}
	y.pass(t, 0)
}

// doneBead closes a bead of the yard whose last note before it carries
// shot, and has ada subscribed to what is done, so that the message about
// it carries the picture.
func (y *pushYard) doneBead(t *testing.T, id string, shot store.BlobFile) {
	t.Helper()
	ctx := context.Background()
	must(t, y.api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"))
	flow, err := store.ParseFlow(pushFlow)
	must(t, err)
	flow.Name = "yard"
	_, err = y.api.PutFlow(ctx, *flow, "jo")
	must(t, err)
	must(t, y.api.SetFlowPointer(ctx, "yard", "", "yard", "jo"))
	must(t, y.api.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"state": store.StateDone}}, "ada"))
	_, err = y.api.CreateBead(ctx, store.NewBead{ID: id, Title: "The shed", Depot: "yard", Stage: "new"}, "jo")
	must(t, err)
	must(t, y.api.AddNote(ctx, id, "the shed, as built", "builder", shot))
	_, err = y.api.CloseBead(ctx, id, "landed", store.Expect{}, "builder")
	must(t, err)
}

// Through the pushover pack and a stand-in for the service: a mail with a
// picture and a done bead with a screenshot arrive with the picture as the
// message's attachment, its bytes the file's own, and a picture the service
// takes none so large is told as the words alone, with the reason in the
// daemon's log.
func testPushoverPackPictures(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	service := y.withPushoverPack(t)
	ctx := context.Background()
	png, err := hex.DecodeString(packPNG)
	must(t, err)
	gif, err := hex.DecodeString(packGIF)
	must(t, err)

	must(t, y.api.Mail(ctx, "ada", "the shed lands Monday", "yardmaster", y.putBlob(t, png, "shed.png")))
	y.settle(t)
	msgs := service.Messages()
	if len(msgs) != 1 || msgs[0].User != packKey || msgs[0].Title != "mail from yardmaster" || msgs[0].Text != "the shed lands Monday" ||
		msgs[0].Attachment == nil || !bytes.Equal(msgs[0].Attachment.Data, png) ||
		msgs[0].Attachment.Name != "photo.png" || msgs[0].Attachment.Type != "image/png" {
		t.Fatalf("a mail with a picture: %+v", msgs)
	}

	y.doneBead(t, "yard-1", y.putBlob(t, gif, "shot.gif"))
	y.settle(t)
	msgs = service.Messages()
	if len(msgs) != 2 || msgs[1].Title != "yard-1 closed" || msgs[1].Attachment == nil || !bytes.Equal(msgs[1].Attachment.Data, gif) ||
		msgs[1].Attachment.Name != "photo.gif" || msgs[1].Attachment.Type != "image/gif" {
		t.Fatalf("a done bead with a screenshot: %+v", msgs)
	}

	// One byte over what the service takes: the command refuses before it
	// sends anything, and the words go alone, at once, in the same pass.
	huge := append(bytes.Clone(png), make([]byte, 5242881-len(png))...)
	must(t, y.api.Mail(ctx, "ada", "the whole plan, as one picture", "yardmaster", y.putBlob(t, huge, "plan.png")))
	asked := service.Asked()
	y.settle(t)
	msgs = service.Messages()
	if len(msgs) != 3 || service.Asked() != asked+1 || msgs[2].Title != "mail from yardmaster" || msgs[2].Text != "the whole plan, as one picture" || msgs[2].Attachment != nil {
		t.Fatalf("a picture over the limit: %+v, asked %d, was %d", msgs, service.Asked(), asked)
	}
	want(t, "left in the queue", len(y.queued(t)), 0)
	want(t, "push_failed events", y.failed(t), []string{})
	if l := y.last(t); !l.OK() || l.Tries != 1 {
		t.Fatalf("last push = %+v", l)
	}
	if logged := y.log.String(); !strings.Contains(logged, "user told without the picture") || !strings.Contains(logged, "the picture is 5242881 bytes") {
		t.Fatalf("the daemon's log: %s", logged)
	}
}
