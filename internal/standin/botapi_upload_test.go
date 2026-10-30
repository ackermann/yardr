package standin

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A declared document call, as the telegram pack's, against the stand-in's
// bot API: the upload it sends is one the stand-in takes, with the chat,
// the caption and the file's name, type and bytes.
func TestBotAPITakesADeclaredDocumentUpload(t *testing.T) {
	dir := t.TempDir()
	s := &Server{BotToken: "123:ABC", BotDir: filepath.Join(dir, "bot")}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	const file = `name: tg
base: https://api.telegram.org
credential: { env: TELEGRAM_BOT_TOKEN, in_path: true }
calls:
  document:
    kind: update
    params:
      chat: { type: string, pattern: "^-?[0-9]+$" }
      path: { type: file, max: 52428800 }
      caption: { type: string, max: 1024, optional: true }
    request:
      method: POST
      path: /bot{credential}/sendDocument
      body: { chat_id: "{chat}", document: "{path}", caption: "{caption}" }
`
	d, err := source.Parse("tg.yaml", []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	d.Base = srv.URL
	upload := filepath.Join(dir, "shed.txt")
	if err := os.WriteFile(upload, []byte("the shed, measured\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := source.Do(context.Background(), d, "document", map[string]string{"chat": "987654321", "path": upload, "caption": "tonight's build"}, "123:ABC", store.ReadSecretFile)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("result = %+v", res)
	}
	reqs := s.BotRequests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %+v", reqs)
	}
	r := reqs[0]
	if r.Method != "sendDocument" || r.Chat != "987654321" || r.Caption != "tonight's build" || r.Name != "shed.txt" ||
		r.Type != "text/plain; charset=utf-8" || r.Bytes != 19 || r.Refusal != "" || r.Answer != 200 {
		t.Fatalf("req = %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "bot", "1")); err != nil || string(data) != "the shed, measured\n" {
		t.Fatalf("kept bytes = %q, %v", data, err)
	}
}
