package rpc

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

// get is GET path on the socket: the status, the type and the body.
func get(t *testing.T, hc *http.Client, path string) (int, string, []byte) {
	t.Helper()
	resp, err := hc.Get("http://yard" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), body
}

// An image goes in with PutBlob and comes back from GET /blobs/<hash>, the
// bytes it was with the type they say; a hash the yard does not hold is
// 404, as is a name that is no hash, and mail that names one is refused.
func TestBlobsOverTheSocket(t *testing.T) {
	hc := overSocket(apiYard(t))
	jpeg := []byte("\xff\xd8\xff\xe0 the fence")
	reply := string(post(t, hc, "PutBlob", `{"data":"`+base64.StdEncoding.EncodeToString(jpeg)+`"}`))
	hash, _, _ := strings.Cut(strings.TrimPrefix(reply, `{"result":{"hash":"`), `"`)
	if !strings.Contains(reply, `"type":"image/jpeg","bytes":14`) || len(hash) != 64 {
		t.Fatalf("PutBlob = %s", reply)
	}
	if status, typ, body := get(t, hc, "/blobs/"+hash); status != http.StatusOK || typ != "image/jpeg" || !bytes.Equal(body, jpeg) {
		t.Errorf("GET /blobs/%s = %d, %q, %q", hash, status, typ, body)
	}
	if status, typ, body := get(t, hc, "/blobs/"+apiPNGHash); status != http.StatusOK || typ != "image/png" ||
		base64.StdEncoding.EncodeToString(body) != apiPNG {
		t.Errorf("GET of the PNG = %d, %q, %d bytes", status, typ, len(body))
	}
	unknown := strings.Repeat("0", 64)
	for _, name := range []string{unknown, "nope", "..%2Fyard.db"} {
		if status, _, body := get(t, hc, "/blobs/"+name); status != http.StatusNotFound {
			t.Errorf("GET /blobs/%s = %d, %q, want 404", name, status, body)
		}
	}

	for _, c := range []struct{ method, body, want string }{
		{"PutBlob", `{"data":"` + base64.StdEncoding.EncodeToString([]byte("GIF89a it moves")) + `"}`, `"message":"the file is image/gif: the yard keeps only image/png and image/jpeg and text/html; charset=utf-8"`},
		{"Mail", `{"to":"yardmaster","text":"look","actor":"jo","files":["` + hash + `"]}`, `{}`},
		{"Mail", `{"to":"yardmaster","text":"look","actor":"jo","files":["` + unknown + `"]}`,
			`{"error":{"code":"not-found","message":"mail to yardmaster: image ` + unknown + `: not found: the yard does not hold it; put it first (PutBlob, or yardr mail --file)"}}`},
		{"MailPeer", `{"to":"clerk","peer":"north","text":"look","actor":"jo","files":["` + hash + `"]}`,
			`{"error":{"code":"error","message":"mail to clerk@north: peer north: the link does not carry file out, so mail to it carries no image: the operator allows it with yardr peer allow north --send file"}}`},
		// What the mail carried is in the conversation.
		{"Conversation", `{"a":"jo","b":"yardmaster"}`, `"text":"look","read":false,"files":[{"hash":"` + hash + `","type":"image/jpeg","bytes":14}]`},
	} {
		if got := strings.TrimSpace(string(post(t, hc, c.method, c.body))); !strings.Contains(got, c.want) {
			t.Errorf("%s %s = %s, want %s", c.method, c.body, got, c.want)
		}
	}
}

// The client puts an image and sends mail that names it, and the mail
// event the stream carries has the list.
func TestClientMailWithFiles(t *testing.T) {
	sock := apiYard(t)
	c := NewClient(sock)
	ctx := t.Context()
	events, err := c.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := c.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed"))
	if err != nil || ref.Type != "image/png" || ref.Bytes != 17 {
		t.Fatalf("PutBlob = %+v, %v", ref, err)
	}
	if err := c.Mail(ctx, "yardmaster", "from the client", "jo", ref.Hash); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail(ctx, "yardmaster", "and one without", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MailPeer(ctx, "clerk", "north", "look", "jo", ref.Hash); err == nil || !strings.Contains(err.Error(), "the link does not carry file out") {
		t.Errorf("MailPeer with a file = %v", err)
	}
	for e := range events {
		text, _ := e.Data["text"].(string)
		switch files, has := e.Data["files"]; text {
		case "from the client":
			list, _ := files.([]any)
			if len(list) != 1 {
				t.Fatalf("the mail event's files = %v", files)
			}
			if f, _ := list[0].(map[string]any); f["hash"] != ref.Hash || f["type"] != "image/png" || f["bytes"] != float64(17) {
				t.Errorf("the mail event's file = %v", list[0])
			}
		case "and one without":
			if has {
				t.Errorf("mail without a file has files in its event: %v", files)
			}
			return
		}
	}
	t.Fatal("the stream ended before the mail came")
}
