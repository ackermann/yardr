package standin

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The stand-in takes a message with the application's token, refuses one
// with another as Pushover does, and answers what it is told to while it
// is to fail. It keeps the messages and never the token.
func TestMessages(t *testing.T) {
	s := &Server{PushToken: "tok"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL+"/1/messages.json", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(`{"token":"tok","user":"u1","title":"mail from jo","message":"hello"}`); got != 200 {
		t.Fatalf("a message: %d", got)
	}
	if got := post(`{"token":"other","user":"u1","message":"hello"}`); got != 400 {
		t.Fatalf("another token: %d", got)
	}
	if got := post(`{"token":"tok","user":"u1"}`); got != 400 {
		t.Fatalf("no text: %d", got)
	}
	if _, err := http.PostForm(srv.URL+"/messages/answer", url.Values{"status": {"500"}}); err != nil {
		t.Fatal(err)
	}
	if got := post(`{"token":"tok","user":"u1","message":"lost"}`); got != 500 {
		t.Fatalf("while it fails: %d", got)
	}
	s.Answer(200)
	if got := post(`{"token":"tok","user":"u1","message":"again"}`); got != 200 {
		t.Fatalf("after it was told to take them again: %d", got)
	}
	ms := s.Messages()
	if len(ms) != 2 || ms[0] != (Message{User: "u1", Title: "mail from jo", Text: "hello"}) || ms[1].Text != "again" || s.Asked() != 5 {
		t.Fatalf("messages = %+v, asked %d", ms, s.Asked())
	}

	// A multipart form takes the same names and a picture as the part
	// attachment, sent as a client's upload is: with a file name and a
	// content type. Its bytes are kept as they came.
	type part struct {
		filename, ctype string
		data            []byte
	}
	upload := func(fields map[string]string, p *part) int {
		t.Helper()
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range fields {
			if err := w.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
		if p != nil {
			h := textproto.MIMEHeader{}
			disposition := `form-data; name="attachment"`
			if p.filename != "" {
				disposition += `; filename="` + p.filename + `"`
			}
			h.Set("Content-Disposition", disposition)
			if p.ctype != "" {
				h.Set("Content-Type", p.ctype)
			}
			pw, err := w.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			pw.Write(p.data)
		}
		w.Close()
		resp, err := http.Post(srv.URL+"/1/messages.json", w.FormDataContentType(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	fields := map[string]string{"token": "tok", "user": "u1", "title": "mail from jo", "message": "the shed"}
	picture := []byte("\x89PNG\r\n\x1a\nthe shed from the lane")
	if got := upload(fields, &part{"shed.png", "image/png", picture}); got != 200 {
		t.Fatalf("a message with a picture: %d", got)
	}
	if got := upload(fields, nil); got != 200 {
		t.Fatalf("a form without a picture: %d", got)
	}
	ms = s.Messages()
	a := ms[2].Attachment
	if len(ms) != 4 || ms[2].User != "u1" || ms[2].Title != "mail from jo" || ms[2].Text != "the shed" || ms[3].Attachment != nil ||
		a == nil || a.Name != "shed.png" || a.Type != "image/png" || !bytes.Equal(a.Data, picture) || a.Bytes != len(picture) ||
		a.SHA256 != fmt.Sprintf("%x", sha256.Sum256(picture)) {
		t.Fatalf("messages = %+v, picture %+v", ms, a)
	}
	// The picture the service takes is at most 5,242,880 bytes, to the
	// byte, and one over is refused and kept nowhere.
	if got := upload(fields, &part{"big.png", "image/png", make([]byte, 5242880)}); got != 200 {
		t.Fatalf("a picture of 5242880 bytes: %d", got)
	}
	if got := upload(fields, &part{"big.png", "image/png", make([]byte, 5242881)}); got != 400 {
		t.Fatalf("a picture of 5242881 bytes: %d", got)
	}
	// It comes with its file name and its content type, or it is refused.
	if got := upload(fields, &part{"", "image/png", picture}); got != 400 {
		t.Fatalf("a picture without a file name: %d", got)
	}
	if got := upload(fields, &part{"shed.png", "", picture}); got != 400 {
		t.Fatalf("a picture without a content type: %d", got)
	}
	wrong := map[string]string{"token": "other", "user": "u1", "message": "the shed"}
	if got := upload(wrong, &part{"shed.png", "image/png", picture}); got != 400 {
		t.Fatalf("another token in a form: %d", got)
	}
	if got := upload(map[string]string{"token": "tok", "user": "u1"}, &part{"shed.png", "image/png", picture}); got != 400 {
		t.Fatalf("a form with no text: %d", got)
	}
	// JSON carries no picture here: a field of the part's name is dropped.
	if got := post(`{"token":"tok","user":"u1","message":"json","attachment":{"name":"x","data":"eA=="}}`); got != 200 {
		t.Fatalf("JSON with an attachment field: %d", got)
	}
	ms = s.Messages()
	if len(ms) != 6 || ms[4].Attachment == nil || ms[4].Attachment.Bytes != 5242880 || ms[5].Text != "json" || ms[5].Attachment != nil || s.Asked() != 14 {
		t.Fatalf("messages after the forms = %d, asked %d", len(ms), s.Asked())
	}

	// A trial that lists them is told of a picture by its name, type, size
	// and hash, not by 5 MB of its bytes.
	resp, err := http.Get(srv.URL + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var listed struct {
		Messages []struct {
			Attachment map[string]any `json:"attachment"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"bytes":%d,"name":"shed.png","sha256":"%x","type":"image/png"}`, len(picture), sha256.Sum256(picture))
	got, _ := json.Marshal(listed.Messages[2].Attachment)
	if string(got) != want || listed.Messages[3].Attachment != nil || len(raw) > 4096 {
		t.Fatalf("GET /messages listed the picture as %s (%d bytes), want %s", got, len(raw), want)
	}
}

// pngHeader is a PNG of the given size that holds a header and no pixels,
// which is all the stand-in reads of a picture: one of 1280x9000 costs 45
// bytes, and a test pays for no encoding.
func pngHeader(width, height int) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		body := append([]byte(kind), data...)
		b.Write(body)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(body))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(height))
	ihdr[8], ihdr[9] = 8, 2 // 8 bits a colour, RGB
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return b.Bytes()
}

// The bot API takes a text, a photo and a document as the service does,
// answers what it refuses with the service's words, keeps every request with
// the bytes it carried (a refused picture's too), and lists them for a trial
// without the bytes, which it serves one at a time.
func TestBotAPI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bot") // made by the first upload
	s := &Server{BotToken: "123:tok", BotDir: dir}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	type answer struct {
		OK          bool           `json:"ok"`
		Code        int            `json:"error_code"`
		Description string         `json:"description"`
		Result      map[string]any `json:"result"`
	}
	call := func(token, method, ctype string, body io.Reader) (int, answer) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/bot"+token+"/"+method, ctype, body)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var a answer
		_ = json.NewDecoder(resp.Body).Decode(&a)
		return resp.StatusCode, a
	}
	message := func(body string) (int, answer) {
		t.Helper()
		return call("123:tok", "sendMessage", "application/json", strings.NewReader(body))
	}
	type file struct {
		name, ctype string
		data        []byte
	}
	// upload sends a form with the fields and, as the part called field, the
	// file: with its name and type as a client's upload has them, or with
	// neither when it has no name, which makes it a field.
	upload := func(method, field string, fields map[string]string, f *file) (int, answer) {
		t.Helper()
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range fields {
			if err := w.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
		if f != nil {
			h := textproto.MIMEHeader{}
			params := map[string]string{"name": field}
			if f.name != "" {
				params["filename"] = f.name
			}
			h.Set("Content-Disposition", mime.FormatMediaType("form-data", params))
			if f.ctype != "" {
				h.Set("Content-Type", f.ctype)
			}
			part, err := w.CreatePart(h)
			if err != nil {
				t.Fatal(err)
			}
			part.Write(f.data)
		}
		w.Close()
		return call("123:tok", method, w.FormDataContentType(), &buf)
	}
	sum := func(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

	// want is the list the stand-in should hold, in order.
	var want []BotRequest
	expect := func(r BotRequest) {
		r.N = len(want) + 1
		want = append(want, r)
	}
	check := func(what string, gotStatus int, got answer, status int, words string) {
		t.Helper()
		if gotStatus != status || got.Description != words || got.OK != (status == 200) {
			t.Fatalf("%s: %d %+v, want %d %q", what, gotStatus, got, status, words)
		}
	}

	// A text, to a chat given as a string or as a number.
	status, got := message(`{"chat_id":"424242","text":"hello"}`)
	if status != 200 || !got.OK || got.Result["message_id"] != float64(1) {
		t.Fatalf("a text: %d %+v", status, got)
	}
	expect(BotRequest{Method: "sendMessage", Chat: "424242", Text: "hello", Answer: 200})
	status, got = message(`{"chat_id":-1001234567890,"text":"to a group"}`)
	if status != 200 || got.Result["message_id"] != float64(2) {
		t.Fatalf("a text to a chat given as a number: %d %+v", status, got)
	}
	expect(BotRequest{Method: "sendMessage", Chat: "-1001234567890", Text: "to a group", Answer: 200})
	// What the service refuses, and what it does not know.
	status, got = call("other", "sendMessage", "application/json", strings.NewReader(`{"chat_id":"1","text":"x"}`))
	check("another token", status, got, 401, "Unauthorized")
	expect(BotRequest{Method: "sendMessage", Answer: 401, Refusal: "Unauthorized"})
	status, got = call("123:tok", "getMe", "application/json", strings.NewReader(`{}`))
	check("a method it does not know", status, got, 404, "Not Found")
	expect(BotRequest{Method: "getMe", Answer: 404, Refusal: "Not Found"})
	status, got = message(`{"chat_id":"1","text":""}`)
	check("an empty text", status, got, 400, "Bad Request: message text is empty")
	expect(BotRequest{Method: "sendMessage", Chat: "1", Answer: 400, Refusal: "Bad Request: message text is empty"})
	status, got = message(`{"text":"no chat"}`)
	check("no chat", status, got, 400, "Bad Request: chat_id is empty")
	expect(BotRequest{Method: "sendMessage", Text: "no chat", Answer: 400, Refusal: "Bad Request: chat_id is empty"})
	status, got = message(`not json`)
	check("a body that is no JSON", status, got, 400, "Bad Request: the body is no JSON object")
	expect(BotRequest{Method: "sendMessage", Answer: 400, Refusal: "Bad Request: the body is no JSON object"})
	// The webhook calls are answered and not listed.
	for _, method := range []string{"getWebhookInfo", "setWebhook", "deleteWebhook"} {
		if status, got = call("123:tok", method, "application/json", strings.NewReader(`{}`)); status != 200 || !got.OK {
			t.Fatalf("%s: %d %+v", method, status, got)
		}
	}

	// A photo with its caption, and a document whose name holds what is
	// special in a header, are kept with their name, type and hash.
	reply := pngHeader(1280, 800)
	status, got = upload("sendPhoto", "photo", map[string]string{"chat_id": "424242", "caption": "the shed, as built"}, &file{"reply.png", "image/png", reply})
	if status != 200 || got.Result["message_id"] != float64(len(want)+1) {
		t.Fatalf("a photo: %d %+v", status, got)
	}
	expect(BotRequest{Method: "sendPhoto", Chat: "424242", Caption: "the shed, as built", Name: "reply.png", Type: "image/png",
		Bytes: len(reply), SHA256: sum(reply), Width: 1280, Height: 800, Answer: 200})
	log := []byte("shed log: three rows\n")
	odd := `shed;log, "v2".txt`
	status, got = upload("sendDocument", "document", map[string]string{"chat_id": "424242"}, &file{odd, "text/plain", log})
	if status != 200 {
		t.Fatalf("a document: %d %+v", status, got)
	}
	expect(BotRequest{Method: "sendDocument", Chat: "424242", Name: odd, Type: "text/plain", Bytes: len(log), SHA256: sum(log), Answer: 200})
	// The photo rule is a photo's: a document of the same size is taken.
	tall := pngHeader(1280, 9000)
	status, got = upload("sendDocument", "document", map[string]string{"chat_id": "424242"}, &file{"tall.png", "image/png", tall})
	if status != 200 {
		t.Fatalf("a tall picture as a document: %d %+v", status, got)
	}
	expect(BotRequest{Method: "sendDocument", Chat: "424242", Name: "tall.png", Type: "image/png", Bytes: len(tall), SHA256: sum(tall), Answer: 200})

	// The photo's rule, to the byte and the pixel: at most 10 MB, width and
	// height at most 10000 in total, a ratio of at most 20, and a picture.
	padded := func(n int) []byte { return append(pngHeader(10, 10), make([]byte, n-len(pngHeader(10, 10)))...) }
	var smallGIF bytes.Buffer
	if err := gif.Encode(&smallGIF, image.NewPaletted(image.Rect(0, 0, 30, 10), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	const (
		tooBig  = "Bad Request: file is too big"
		badSize = "Bad Request: PHOTO_INVALID_DIMENSIONS"
		noPhoto = "Bad Request: IMAGE_PROCESS_FAILED"
	)
	for _, c := range []struct {
		what    string
		data    []byte
		w, h    int
		refusal string
	}{
		{"10 MB to the byte", padded(10 << 20), 10, 10, ""},
		{"one byte over 10 MB", padded(10<<20 + 1), 10, 10, tooBig},
		{"5000x5000, 10000 in total", pngHeader(5000, 5000), 5000, 5000, ""},
		{"5001x5000", pngHeader(5001, 5000), 5001, 5000, badSize},
		{"a page of 1280x9000", tall, 1280, 9000, badSize},
		{"a ratio of 20", pngHeader(2000, 100), 2000, 100, ""},
		{"a ratio over 20, wide", pngHeader(2100, 100), 2100, 100, badSize},
		{"a ratio over 20, tall", pngHeader(100, 2100), 100, 2100, badSize},
		{"a GIF, which it reads", smallGIF.Bytes(), 30, 10, ""},
		{"a WebP, which it cannot measure", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), 0, 0, ""},
		{"a BMP, which it cannot measure", []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), 0, 0, ""},
		{"a file that is no picture", []byte("the shed, in words"), 0, 0, noPhoto},
		{"a PNG torn after its signature", []byte("\x89PNG\r\n\x1a\nthe shed from the lane"), 0, 0, noPhoto},
	} {
		status, got = upload("sendPhoto", "photo", map[string]string{"chat_id": "7"}, &file{"p.bin", "application/octet-stream", c.data})
		r := BotRequest{Method: "sendPhoto", Chat: "7", Name: "p.bin", Type: "application/octet-stream", Bytes: len(c.data), SHA256: sum(c.data),
			Width: c.w, Height: c.h, Answer: 200}
		if c.refusal != "" {
			r.Answer, r.Refusal = 400, c.refusal
			check(c.what, status, got, 400, c.refusal)
		} else if status != 200 || !got.OK {
			t.Fatalf("%s: %d %+v", c.what, status, got)
		}
		expect(r)
	}

	// A photo or a document the request does not hold, or no chat to send it to.
	status, got = upload("sendPhoto", "photo", map[string]string{"chat_id": "7"}, nil)
	check("no photo part", status, got, 400, "Bad Request: there is no photo in the request")
	expect(BotRequest{Method: "sendPhoto", Chat: "7", Answer: 400, Refusal: "Bad Request: there is no photo in the request"})
	status, got = upload("sendDocument", "document", map[string]string{"chat_id": "7"}, &file{"", "text/plain", log})
	check("a document part with no file name", status, got, 400, "Bad Request: there is no document in the request")
	expect(BotRequest{Method: "sendDocument", Chat: "7", Answer: 400, Refusal: "Bad Request: there is no document in the request"})
	status, got = upload("sendDocument", "document", nil, &file{"shed-log.txt", "text/plain", log})
	check("no chat", status, got, 400, "Bad Request: chat_id is empty")
	expect(BotRequest{Method: "sendDocument", Name: "shed-log.txt", Type: "text/plain", Bytes: len(log), SHA256: sum(log), Answer: 400, Refusal: "Bad Request: chat_id is empty"})
	status, got = call("123:tok", "sendPhoto", "text/plain", strings.NewReader("no form"))
	check("a body that is no form", status, got, 400, "Bad Request: the body is no multipart form")
	expect(BotRequest{Method: "sendPhoto", Answer: 400, Refusal: "Bad Request: the body is no multipart form"})

	if got := s.BotRequests(); !reflect.DeepEqual(got, want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("request %d = %+v, want %+v", i+1, got[i], want[i])
			}
		}
		t.Fatalf("the stand-in holds %d requests, want %d", len(got), len(want))
	}

	// A trial reads them as JSON, without the bytes, and fetches an upload's
	// by its number: every one that carried a file, refused or not, as it came.
	resp, err := http.Get(srv.URL + "/requests")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var listed struct {
		Requests []BotRequest `json:"requests"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil || !reflect.DeepEqual(listed.Requests, want) || len(raw) > 16<<10 {
		t.Fatalf("GET /requests = %d bytes, %v, listed %d requests, want the %d and under 16 KiB", len(raw), err, len(listed.Requests), len(want))
	}
	for _, r := range want {
		resp, err := http.Get(fmt.Sprintf("%s/requests/%d/file", srv.URL, r.N))
		if err != nil {
			t.Fatal(err)
		}
		served, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		kept, _ := os.ReadFile(filepath.Join(dir, fmt.Sprint(r.N)))
		if r.SHA256 == "" {
			if resp.StatusCode != 404 || len(kept) != 0 {
				t.Errorf("request %d held no file, yet /file is %d and %d bytes are kept", r.N, resp.StatusCode, len(kept))
			}
			continue
		}
		if resp.StatusCode != 200 || sum(served) != r.SHA256 || len(served) != r.Bytes || sum(kept) != r.SHA256 {
			t.Errorf("request %d (%s): /file is %d with %d bytes, %d kept, want %d bytes of %s", r.N, r.Name, resp.StatusCode, len(served), len(kept), r.Bytes, r.SHA256[:12])
		}
		wantType := "application/octet-stream"
		if strings.HasPrefix(http.DetectContentType(served), "image/") {
			wantType = http.DetectContentType(served)
		}
		if got := resp.Header.Get("Content-Type"); got != wantType {
			t.Errorf("request %d is served as %q, want %q", r.N, got, wantType)
		}
	}
	for _, path := range []string{"/requests/0/file", fmt.Sprintf("/requests/%d/file", len(want)+1), "/requests/x/file", "/requests/1"} {
		if resp, err := http.Get(srv.URL + path); err != nil || resp.StatusCode != 404 {
			t.Errorf("GET %s = %v, %v, want 404", path, resp, err)
		}
	}

	// Without a directory to keep files in it serves nothing; without a
	// token it takes any.
	bare := httptest.NewServer((&Server{BotToken: "t"}).Handler())
	defer bare.Close()
	if resp, err := http.Post(bare.URL+"/bott/sendMessage", "application/json", strings.NewReader(`{"chat_id":"1","text":"x"}`)); err != nil || resp.StatusCode != 404 {
		t.Errorf("a bot API with no directory = %v, %v, want 404", resp, err)
	}
	anyToken := &Server{BotDir: filepath.Join(t.TempDir(), "bot")}
	open := httptest.NewServer(anyToken.Handler())
	defer open.Close()
	if resp, err := http.Post(open.URL+"/botwhatever/sendMessage", "application/json", strings.NewReader(`{"chat_id":"1","text":"x"}`)); err != nil || resp.StatusCode != 200 || len(anyToken.BotRequests()) != 1 {
		t.Errorf("a bot API with no token = %v, %v, %d requests, want 200 and 1", resp, err, len(anyToken.BotRequests()))
	}
}

// The customer system takes a note on a customer under the token for
// updates, once for each key: a request with the key of a note it has is
// answered with that note. A held note is written and never answered.
func TestCustomerNotes(t *testing.T) {
	s := &Server{CRMToken: "read", CRMUpdateToken: "write"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(client *http.Client, token, key, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	c := http.DefaultClient
	const notes = "/crm/customers/7/notes"
	if got, _ := post(c, "read", "", notes, `{"text":"a"}`); got != 403 {
		t.Fatalf("a note with the token of the reads: %d", got)
	}
	if got, _ := post(c, "write", "", notes, `{}`); got != 400 {
		t.Fatalf("a note without a text: %d", got)
	}
	if got, _ := post(c, "write", "", "/crm/customers/x/notes", `{"text":"a"}`); got != 404 {
		t.Fatalf("a note on no customer: %d", got)
	}
	status, first := post(c, "write", "k1", notes, `{"text":"called"}`)
	if status != 201 || !strings.Contains(first, `"id":"n1"`) {
		t.Fatalf("a note: %d %s", status, first)
	}
	if status, again := post(c, "write", "k1", notes, `{"text":"called"}`); status != 201 || again != first {
		t.Fatalf("the key again: %d %s, first %s", status, again, first)
	}
	// Without a key, and with another, each is a note of its own.
	post(c, "write", "", notes, `{"text":"called"}`)
	post(c, "write", "k2", notes, `{"text":"called"}`)

	if _, err := http.Post(srv.URL+"/notes/hold", "", nil); err != nil {
		t.Fatal(err)
	}
	if status, why := post(&http.Client{Timeout: 200 * time.Millisecond}, "write", "k3", notes, `{"text":"held"}`); status != 0 {
		t.Fatalf("a held note was answered: %d %s", status, why)
	}
	if status, body := post(c, "write", "k3", notes, `{"text":"held"}`); status != 201 || !strings.Contains(body, `"id":"n4"`) {
		t.Fatalf("the held note's key again: %d %s", status, body)
	}
	got := s.CustomerNotes()
	if len(got) != 4 || got[0] != (CustomerNote{ID: "n1", Customer: "7", Text: "called", Key: "k1"}) || got[3].Text != "held" || s.NotesAsked() != 9 {
		t.Fatalf("notes = %+v, asked %d", got, s.NotesAsked())
	}
	resp, err := http.Get(srv.URL + "/notes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if raw, _ := io.ReadAll(resp.Body); !strings.Contains(string(raw), `"asked":9`) || !strings.Contains(string(raw), `"key":"k3"`) {
		t.Fatalf("GET /notes = %s", raw)
	}
}

// A pull request's head and base, what its base branch requires from its
// protection and its rulesets, and the check runs and commit statuses on
// a commit: what the pack's pr and ci commands read. An issue is no pull
// request, and a branch or commit the stand-in never heard of is 404, as
// GitHub answers them.
func TestGitHubPullAndCI(t *testing.T) {
	s := &Server{GitHubToken: "gh-test"}
	s.Add(Issue{Source: "github", Key: "acme/webapp#7", Title: "Flow", Status: "open", Pull: true,
		Head: "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b", Base: "main",
		Required: []string{"build"}, Rules: []string{"lint"},
		Runs:     []CheckRun{{Name: "build", Status: "completed", Conclusion: "success", URL: "https://ci.test/runs/5"}},
		Statuses: []CommitStatus{{Context: "deploy", State: "pending"}}})
	s.Add(Issue{Source: "github", Key: "acme/webapp#8", Title: "Bug", Status: "open"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer gh-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := get("/repos/acme/webapp/pulls/7"); code != 200 ||
		!strings.Contains(body, `"sha":"4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"`) ||
		!strings.Contains(body, `"ref":"main"`) || !strings.Contains(body, `"state":"open"`) {
		t.Fatalf("pulls/7 = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/pulls/8"); code != 404 {
		t.Fatalf("an issue as a pull request: %d", code)
	}
	if code, _ := get("/repos/acme/webapp/pulls/9"); code != 404 {
		t.Fatalf("no such pull request: %d", code)
	}
	if code, body := get("/repos/acme/webapp/branches/main"); code != 200 ||
		!strings.Contains(body, `"contexts":["build"]`) {
		t.Fatalf("branches/main = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/branches/other"); code != 404 {
		t.Fatalf("no such branch: %d", code)
	}
	if code, body := get("/repos/acme/webapp/rules/branches/main"); code != 200 ||
		!strings.Contains(body, `"type":"required_status_checks"`) || !strings.Contains(body, `"context":"lint"`) {
		t.Fatalf("rules/branches/main = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/rules/branches/other"); code != 404 {
		t.Fatalf("no such branch's rules: %d", code)
	}
	const sha = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	if code, body := get("/repos/acme/webapp/commits/" + sha + "/check-runs"); code != 200 ||
		!strings.Contains(body, `"name":"build"`) || !strings.Contains(body, `"conclusion":"success"`) {
		t.Fatalf("check-runs = %d %s", code, body)
	}
	if code, body := get("/repos/acme/webapp/commits/" + sha + "/status"); code != 200 ||
		!strings.Contains(body, `"state":"pending"`) || !strings.Contains(body, `"context":"deploy"`) {
		t.Fatalf("status = %d %s", code, body)
	}
	if code, _ := get("/repos/acme/webapp/commits/0000000000000000000000000000000000000000/check-runs"); code != 404 {
		t.Fatalf("no such commit's runs: %d", code)
	}
	want := []string{"github pull acme/webapp#7", "github branch acme/webapp/main",
		"github branch acme/webapp/other", "github rules acme/webapp/main", "github rules acme/webapp/other",
		"github check-runs acme/webapp#7", "github status acme/webapp#7"}
	if got := s.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
