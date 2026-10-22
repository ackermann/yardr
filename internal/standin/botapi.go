package standin

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif" // photoRefusal reads a picture's size from its header
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The bot API is a chat service's: a bot sends a person a message, a photo
// or a document with a POST to /bot<token>/<method>, the token in the path,
// and is answered {"ok":true,"result":{...}} or {"ok":false,"error_code":N,
// "description":"..."}. The stand-in takes the three sends yardr's commands
// make and keeps what each held, for a trial to read at GET /requests: see
// BotRequest. It is written from the service's API page (read 22 October
// 2026) and enforces the one rule of it that bites a picture, photoRefusal.
// Not enforced: the 4096 characters a text may hold, the 1024 of a caption,
// the 50 MB of a document.

const (
	// photoMax is the most a photo may hold, 10 MB as the API page says.
	photoMax = 10 << 20
	// photoSides is the most a photo's width and height may come to
	// together, photoRatio the most one may be of the other.
	photoSides = 10000
	photoRatio = 20
)

// BotRequest is a request the bot API was sent, taken or refused: a text by
// sendMessage, or a photo or a document by sendPhoto and sendDocument. N is
// its place in the list, from 1, and names the file in BotDir that holds the
// bytes of its upload as they came. The list a trial reads leaves them out.
type BotRequest struct {
	N      int    `json:"n"`
	Method string `json:"method"`
	// Chat is the chat_id, a string of digits for a person.
	Chat    string `json:"chat"`
	Text    string `json:"text,omitempty"`
	Caption string `json:"caption,omitempty"`
	// Name and Type are the file part's file name and content type as it
	// came, Bytes and SHA256 its size and hash.
	Name   string `json:"filename,omitempty"`
	Type   string `json:"type,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// Width and Height are a picture's size, where the stand-in could read it.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Answer is the status it was answered with, Refusal the service's
	// words in the answer of a 400 or a 401.
	Answer  int    `json:"answer"`
	Refusal string `json:"refusal,omitempty"`
}

// BotRequests are the requests the bot API was sent, in order: those it took
// and those it refused, among them the unauthorized and the unknown. The
// webhook calls are answered and not listed. The bytes are in BotDir.
func (s *Server) BotRequests() []BotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]BotRequest{}, s.bot...)
}

// botAPI serves the bot API, and a 404 for any other path as the mux would.
// Without a BotDir to keep the files in it serves nothing.
func (s *Server) botAPI(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	token, method, found := strings.Cut(rest, "/")
	if !ok || !found || s.BotDir == "" {
		http.NotFound(w, r)
		return
	}
	answer := func(status int, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	// A refusal is kept and answered with its words. The body of a request
	// refused before it was read is drained, or a client still sending a
	// file is cut off before it hears why.
	refuse := func(req BotRequest, status int, words string) {
		_, _ = io.Copy(io.Discard, r.Body)
		req.Answer, req.Refusal = status, words
		_, _ = s.keepBot(req, nil)
		answer(status, map[string]any{"ok": false, "error_code": status, "description": words})
	}
	switch {
	case s.BotToken != "" && token != s.BotToken:
		refuse(BotRequest{Method: method}, http.StatusUnauthorized, "Unauthorized")
	case method == "getWebhookInfo":
		answer(http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"url": "", "pending_update_count": 0}})
	case method == "setWebhook" || method == "deleteWebhook":
		answer(http.StatusOK, map[string]any{"ok": true, "result": true, "description": "stand-in: no webhook is kept"})
	case method == "sendMessage" || method == "sendPhoto" || method == "sendDocument":
		s.send(w, r, method, answer)
	default:
		refuse(BotRequest{Method: method}, http.StatusNotFound, "Not Found")
	}
}

// send takes one send: a JSON object for sendMessage, a multipart form with
// the file in the part photo or document for the others. A request that
// cannot be taken is kept with its refusal, a 400.
func (s *Server) send(w http.ResponseWriter, r *http.Request, method string, answer func(int, map[string]any)) {
	req := BotRequest{Method: method}
	var data []byte
	if method == "sendMessage" {
		readBotMessage(r, &req)
	} else {
		data = readBotUpload(r, &req)
	}
	req.Answer = http.StatusOK
	if req.Refusal != "" {
		req.Answer = http.StatusBadRequest
	}
	n, err := s.keepBot(req, data)
	switch {
	case err != nil:
		answer(http.StatusInternalServerError, map[string]any{"ok": false, "error_code": http.StatusInternalServerError, "description": "stand-in: " + err.Error()})
	case req.Refusal != "":
		answer(req.Answer, map[string]any{"ok": false, "error_code": req.Answer, "description": req.Refusal})
	default:
		answer(http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": n}})
	}
}

// keepBot lists req and writes its upload, if it has one, to its file in
// BotDir: refused or not, so that a trial can compare the bytes of a picture
// the service would not take. It returns the request's number.
func (s *Server) keepBot(req BotRequest, data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req.N = len(s.bot) + 1
	if data != nil {
		if err := os.MkdirAll(s.BotDir, 0o700); err != nil {
			return 0, err
		}
		if err := os.WriteFile(filepath.Join(s.BotDir, strconv.Itoa(req.N)), data, 0o600); err != nil {
			return 0, err
		}
	}
	s.bot = append(s.bot, req)
	return req.N, nil
}

// readBotMessage reads a sendMessage: a JSON object with a chat_id, which
// the service takes as a string or as a number, and a text.
func readBotMessage(r *http.Request, req *BotRequest) {
	var in struct {
		Chat json.RawMessage `json:"chat_id"`
		Text string          `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		req.Refusal = "Bad Request: the body is no JSON object"
		return
	}
	var chat string
	if json.Unmarshal(in.Chat, &chat) != nil {
		chat = string(in.Chat)
	}
	req.Chat, req.Text = chat, in.Text
	switch {
	case req.Chat == "":
		req.Refusal = "Bad Request: chat_id is empty"
	case req.Text == "":
		req.Refusal = "Bad Request: message text is empty"
	}
}

// readBotUpload reads a sendPhoto or a sendDocument and returns the file's
// bytes. A part with no file name is a field to the multipart reader, not a
// file, and so is none to the service either.
func readBotUpload(r *http.Request, req *BotRequest) []byte {
	field := "photo"
	if req.Method == "sendDocument" {
		field = "document"
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		req.Refusal = "Bad Request: the body is no multipart form"
		return nil
	}
	req.Chat, req.Caption = r.FormValue("chat_id"), r.FormValue("caption")
	f, head, err := r.FormFile(field)
	if err != nil {
		req.Refusal = "Bad Request: there is no " + field + " in the request"
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		req.Refusal = "Bad Request: the " + field + " cannot be read"
		return nil
	}
	req.Name, req.Type = head.Filename, head.Header.Get("Content-Type")
	req.Bytes, req.SHA256 = len(data), fmt.Sprintf("%x", sha256.Sum256(data))
	switch {
	case req.Chat == "":
		req.Refusal = "Bad Request: chat_id is empty"
	case req.Method == "sendPhoto":
		req.Width, req.Height, req.Refusal = photoRefusal(data)
	}
	return data
}

// photoRefusal is the service's rule for a photo, from its API page: at most
// 10 MB; width and height at most 10000 in total; their ratio at most 20.
// It returns the picture's size where it could be read and the words of the
// 400 the service answers, "" for a photo it takes. A file that is no
// picture is IMAGE_PROCESS_FAILED. WebP and BMP, which a pack may send as a
// photo and the standard library cannot read, are taken unmeasured.
func photoRefusal(data []byte) (width, height int, refusal string) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err == nil {
		width, height = cfg.Width, cfg.Height
	}
	switch {
	case len(data) > photoMax:
		return width, height, "Bad Request: file is too big"
	case err != nil && !unreadablePicture(data):
		return 0, 0, "Bad Request: IMAGE_PROCESS_FAILED"
	case width+height > photoSides || width > photoRatio*height || height > photoRatio*width:
		return width, height, "Bad Request: PHOTO_INVALID_DIMENSIONS"
	}
	return width, height, ""
}

// unreadablePicture is whether data is a kind of picture the standard
// library has no reader for.
func unreadablePicture(data []byte) bool {
	switch http.DetectContentType(data) {
	case "image/webp", "image/bmp":
		return true
	}
	return false
}

// botFile serves GET /requests/<n>/file: the bytes the nth request carried,
// as they came, for a trial to compare with the file it sent. They are
// served as a picture only when they are one of the kinds a browser shows
// without running anything, and otherwise as bytes.
func (s *Server) botFile(w http.ResponseWriter, r *http.Request) {
	num, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/requests/"), "/file")
	n, err := strconv.Atoi(num)
	reqs := s.BotRequests()
	if !ok || err != nil || n < 1 || n > len(reqs) || reqs[n-1].SHA256 == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.BotDir, strconv.Itoa(n)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	kind := "application/octet-stream"
	switch sniffed := http.DetectContentType(data); sniffed {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		kind = sniffed
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
