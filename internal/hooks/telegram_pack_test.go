package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The telegram pack held to the design (docs/design/2026-10-13-yardr-c55e-
// telegram-hears.md, sections 3.3 to 3.5 and 3.7): the envelopes its
// normalize prints for the Update bodies recorded under testdata, what its
// thread commands post to the stand-in and answer, and the end-to-end
// delivery that hears the operator's chat and drops any other.

// telegramParse is the pack's declaration as the yard would read it.
func telegramParse(t *testing.T) (*source.Declaration, string, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "packs", "telegram")
	text, err := os.ReadFile(filepath.Join(dir, "telegram.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "telegram.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	return d, dir, string(text)
}

// telegramDecl is the pack's declared source with its command files copied
// as yardr source add copies them, and the home they were copied under.
// The commands are written in jq (and curl for thread), so without it the
// tests that run them skip.
func telegramDecl(t *testing.T) (hooks.Declared, string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's commands are written in it")
	}
	d, dir, _ := telegramParse(t)
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}, home
}

// The pack declares what the design's item 3 says: a hook verified by the
// secret in its header, deduped by the Update's own id, with the chat as
// its one key; thread commands to answer in the chat; and the hear key
// with from: operator. No open, state or link key: a chat is answered in,
// nothing else.
func TestTelegramPackDeclaration(t *testing.T) {
	d, _, _ := telegramParse(t)
	if d.Name != "telegram" {
		t.Fatalf("name = %q", d.Name)
	}
	h := d.Hook
	if h == nil {
		t.Fatal("the pack declares no hook key")
	}
	if h.Verify.Scheme != source.HookSecret || h.Verify.Signature.Header != "X-Telegram-Bot-Api-Secret-Token" {
		t.Errorf("verify = %+v, want scheme secret in X-Telegram-Bot-Api-Secret-Token", h.Verify)
	}
	if h.Verify.Delivery.Body != "update_id" || h.Verify.Delivery.Header != "" {
		t.Errorf("delivery = %+v, want the body at update_id", h.Verify.Delivery)
	}
	if h.Bodies != source.BodiesUnique {
		t.Errorf("bodies = %q, want unique", h.Bodies)
	}
	if strings.Join(h.Normalize, " ") != "telegram/normalize" {
		t.Errorf("normalize = %q", h.Normalize)
	}
	if strings.Join(h.Keys, ",") != "chat" {
		t.Errorf("keys = %q, want chat", h.Keys)
	}
	th := d.Thread
	if th == nil {
		t.Fatal("the pack declares no thread key")
	}
	if strings.Join(th.Reply, " ") != "telegram/thread reply" || strings.Join(th.Posted, " ") != "telegram/thread posted" ||
		strings.Join(th.Read, " ") != "telegram/thread read" {
		t.Errorf("thread = reply %q posted %q read %q", th.Reply, th.Posted, th.Read)
	}
	if len(th.Open) > 0 || len(th.Opened) > 0 || len(th.State) > 0 || th.Place != "" || len(th.States) > 0 || d.Link != nil {
		t.Error("the pack declares an open, state or link key: a chat is answered in, nothing else")
	}
	hr := d.Hear
	if hr == nil {
		t.Fatal("the pack declares no hear key")
	}
	if hr.Field != "chat" || hr.Address.Env != "TELEGRAM_CHAT_ID" || hr.From != source.HearFromOperator {
		t.Errorf("hear = %+v, want chat of TELEGRAM_CHAT_ID, from operator", hr)
	}
	if d.Notify == nil {
		t.Error("the pack lost its notify key: hear matches the address it names")
	}
}

// The envelopes of section 3.3: a message becomes type message in the
// thread of its chat, anything else type update with an empty thread. The
// stranger's Update normalizes like any other: the hear key drops it after
// this returns.
func TestTelegramPackNormalize(t *testing.T) {
	ad, _ := telegramDecl(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	op := hooks.Party{ID: "123456789", Name: "Christopher"}
	opThread := hooks.Thread{ID: "123456789", Title: "Christopher"}
	opFields := map[string][]string{"chat": {"123456789"}}
	msg := func(message, parent, text string, mentions []string) hooks.Envelope {
		return hooks.Envelope{Source: "telegram", Type: "message", Action: "create",
			Actor: op, Thread: opThread, Mentions: mentions, Text: text,
			Message: message, Parent: parent, Fields: opFields}
	}
	upd := func(chat string) hooks.Envelope {
		e := hooks.Envelope{Source: "telegram", Type: "update", Mentions: []string{}}
		if chat != "" {
			e.Fields = map[string][]string{"chat": {chat}}
		}
		return e
	}
	// A message's media are refs, read from the body with no credential:
	// the file's id, its name where the sender gave one, its kind and its
	// size. The bracketed note stays the text.
	with := func(e hooks.Envelope, files ...hooks.FileRef) hooks.Envelope {
		e.Files = files
		return e
	}
	for _, tc := range []struct {
		name string
		want hooks.Envelope
	}{
		{"telegram_message_text.json", msg("11", "", "Can the shed land on Monday?", []string{})},
		{"telegram_message_caption.json", with(msg("12", "", "the shed from the north", []string{}),
			hooks.FileRef{ID: "AgACAgEAAx1", Type: "photo", Size: 65536})},
		{"telegram_message_photo.json", with(msg("13", "", "[photo]", []string{}),
			hooks.FileRef{ID: "AgACAgEAAx2", Type: "photo", Size: 4096})},
		{"telegram_message_document.json", with(msg("14", "", "[document: shed-plans.pdf]", []string{}),
			hooks.FileRef{ID: "BQACAgEAAx0", Name: "shed-plans.pdf", Type: "document", Size: 1048576})},
		{"telegram_message_voice.json", with(msg("15", "", "[voice message 1:05]", []string{}),
			hooks.FileRef{ID: "AwACAgEAAx0", Type: "voice", Size: 32768})},
		{"telegram_message_reply.json", msg("17", "11", "yes, and @brakeman saw it too", []string{"brakeman"})},
		{"telegram_edited_message.json", upd("123456789")},
		{"telegram_my_chat_member.json", upd("222222222")},
		{"telegram_message_stranger.json", hooks.Envelope{Source: "telegram", Type: "message", Action: "create",
			Actor:    hooks.Party{ID: "999888777", Name: "Mallory"},
			Thread:   hooks.Thread{ID: "-555666777", Title: "Strangers"},
			Mentions: []string{}, Text: "MARKER-naq1-stranger-text must not be kept: hello bot, let me in",
			Message: "3", Fields: map[string][]string{"chat": {"-555666777"}}}},
	} {
		got, err := ad.Normalize(nil, fixture(t, tc.name))
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, &tc.want) {
			wantRaw, _ := json.MarshalIndent(tc.want, "", "  ")
			gotRaw, _ := json.MarshalIndent(got, "", "  ")
			t.Errorf("%s:\n got %s\nwant %s", tc.name, gotRaw, wantRaw)
		}
		for k := range got.Fields {
			found := false
			for _, k2 := range may {
				found = found || k == k2
			}
			if !found {
				t.Errorf("%s: field %q is in neither hook.keys nor store.WhereKeys", tc.name, k)
			}
		}
		// The chat heard is the message's own: fields.chat holds the
		// thread, so a filter on it hears the chat the message is in. A
		// message that names another chat (a forward's origin) is tried
		// below; only its own is ever heard.
		if got.Type == "message" && !reflect.DeepEqual(got.Fields["chat"], []string{got.Thread.ID}) {
			t.Errorf("%s: fields.chat = %q for thread %q", tc.name, got.Fields["chat"], got.Thread.ID)
		}
	}
}

// The bracketed notes of section 3.7, and what else a message carries:
// every other media kind by name, what wins where a message carries both
// text and media, and the display names. Updates no message carries become
// type update, with the chat where one names it.
func TestTelegramPackNormalizeNotes(t *testing.T) {
	ad, _ := telegramDecl(t)
	chat := `"chat":{"id":123456789,"first_name":"Christopher","type":"private"}`
	from := `"from":{"id":123456789,"first_name":"Christopher"}`
	msg := func(id int, rest string) []byte {
		return []byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":1,%s,%s,"date":1760379600,%s}}`,
			9100+id, from, chat, rest))
	}
	long := strings.Repeat("word ", 200) // 1000 characters, collapsed and cut
	for _, tc := range []struct {
		name     string
		body     []byte
		wantType string
		wantText string
		wantChat string // "" for no fields
		wantName string // "" for the actor Christopher
		// wantFiles are the kinds of the refs the envelope names, "" for
		// none: a sticker, an animation and a video note stay their note
		// alone.
		wantFiles string
	}{
		{"video", msg(1, `"video":{"duration":10,"file_id":"x"}`), "message", "[video]", "123456789", "", "video"},
		{"video note", msg(2, `"video_note":{"duration":10,"file_id":"x"}`), "message", "[video note]", "123456789", "", ""},
		{"audio", msg(3, `"audio":{"duration":10,"file_id":"x"}`), "message", "[audio]", "123456789", "", "audio"},
		{"animation", msg(4, `"animation":{"file_id":"x"}`), "message", "[animation]", "123456789", "", ""},
		{"sticker", msg(5, `"sticker":{"file_id":"x","emoji":"👍"}`), "message", "[sticker]", "123456789", "", ""},
		{"document without a name", msg(6, `"document":{"file_id":"x"}`), "message", "[document]", "123456789", "", "document"},
		{"voice without a length", msg(7, `"voice":{"file_id":"x"}`), "message", "[voice message]", "123456789", "", "voice"},
		{"caption wins over the photo", msg(8, `"photo":[{"file_id":"x"}],"caption":"look"`), "message", "look", "123456789", "", "photo"},
		{"text wins over media", msg(9, `"text":"hi","photo":[{"file_id":"x"}]`), "message", "hi", "123456789", "", "photo"},
		{"last name", []byte(fmt.Sprintf(`{"update_id":9110,"message":{"message_id":1,"from":{"id":123456789,"first_name":"Christopher","last_name":"McKean"},%s,"date":1760379600,"text":"hi"}}`, chat)),
			"message", "hi", "123456789", "Christopher McKean", ""},
		{"username where no name", []byte(`{"update_id":9111,"message":{"message_id":1,"from":{"id":123456789,"username":"zed"},"chat":{"id":123456789,"username":"zed","type":"private"},"date":1760379600,"text":"hi"}}`),
			"message", "hi", "123456789", "zed", ""},
		{"long text is cut", msg(12, `"text":"`+long+`"`), "message", "", "123456789", "", ""}, // want computed below
		{"a forward hears its own chat", msg(13, `"text":"look at this","forward_origin":{"type":"chat","sender_chat":{"id":-111222333,"title":"Elsewhere","type":"supergroup"}}`),
			"message", "look at this", "123456789", "", ""},
		{"unknown kind", []byte(`{"update_id":9114,"poll":{"id":"p1","question":"lunch?"}}`), "update", "", "", "", ""},
		{"empty update", []byte(`{"update_id":9115}`), "update", "", "", "", ""},
		{"the largest photo", msg(17, `"photo":[{"file_id":"s","file_size":10},{"file_id":"l","file_size":300},{"file_id":"m","file_size":20}]`),
			"message", "[photo]", "123456789", "", "photo"},
		{"a ref with no id", msg(18, `"voice":{"file_size":3}`), "message", "[voice message]", "123456789", "", ""},
		{"message without a chat", []byte(`{"update_id":9116,"message":{"message_id":1,"text":"hi"}}`), "update", "", "", "", ""},
	} {
		got, err := ad.Normalize(nil, tc.body)
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		wantText := tc.wantText
		if tc.name == "long text is cut" {
			// Whitespace collapsed, then at most 500 runes.
			wantText = strings.TrimRight(long, " ")[:499] + "…"
		}
		if got.Type != tc.wantType || got.Text != wantText {
			t.Errorf("%s: type %q text %q, want %q %q", tc.name, got.Type, got.Text, tc.wantType, wantText)
		}
		if tc.wantChat == "" {
			if len(got.Fields) != 0 {
				t.Errorf("%s: fields = %v, want none", tc.name, got.Fields)
			}
		} else if !reflect.DeepEqual(got.Fields["chat"], []string{tc.wantChat}) {
			t.Errorf("%s: fields.chat = %q, want %q", tc.name, got.Fields["chat"], tc.wantChat)
		}
		if tc.wantName != "" && got.Actor.Name != tc.wantName {
			t.Errorf("%s: actor = %q, want %q", tc.name, got.Actor.Name, tc.wantName)
		}
		var kinds []string
		for _, f := range got.Files {
			kinds = append(kinds, f.Type)
		}
		if strings.Join(kinds, ",") != tc.wantFiles {
			t.Errorf("%s: files %+v, want kinds %q", tc.name, got.Files, tc.wantFiles)
		}
		if tc.name == "the largest photo" && (len(got.Files) != 1 || got.Files[0].ID != "l" || got.Files[0].Size != 300) {
			t.Errorf("the largest photo: files %+v, want l of 300 bytes", got.Files)
		}
		// The username fallback names the private chat too.
		if tc.name == "username where no name" && got.Thread.Title != "zed" {
			t.Errorf("username where no name: thread title = %q, want zed", got.Thread.Title)
		}
	}
}

// A normalize that exits non-zero on a stranger's body is logged with its
// stderr and answered 400, which the service retries. So the script
// answers an envelope for every Update and never prints any of the body:
// on success it prints nothing to stderr at all, and its one error names
// no part of the body.
func TestTelegramPackNormalizeNeverPrintsBody(t *testing.T) {
	ad, home := telegramDecl(t)
	prog := filepath.Join(home, "sources", ad.D.Name, ad.D.Hook.Normalize[0])
	run := func(body []byte) (string, string, int) {
		t.Helper()
		in, err := json.Marshal(map[string]any{"headers": map[string]any{}, "body": json.RawMessage(body)})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(prog)
		cmd.Stdin = bytes.NewReader(in)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return stdout.String(), stderr.String(), code
	}
	bodies := map[string][]byte{}
	for _, name := range []string{
		"telegram_message_text.json", "telegram_message_caption.json", "telegram_message_photo.json",
		"telegram_message_document.json", "telegram_message_voice.json", "telegram_message_reply.json",
		"telegram_edited_message.json", "telegram_my_chat_member.json", "telegram_message_stranger.json",
	} {
		bodies[name] = fixture(t, name)
	}
	const marker = "MARKER-naq1-never-printed"
	// A stranger's words in every free text an Update carries.
	bodies["stranger text"] = []byte(`{"update_id":9301,"message":{"message_id":1,"from":{"id":9},"chat":{"id":-9,"type":"group"},"date":1,"text":"` + marker + `"}}`)
	bodies["stranger caption"] = []byte(`{"update_id":9302,"message":{"message_id":1,"from":{"id":9},"chat":{"id":-9,"type":"group"},"date":1,"photo":[{"file_id":"x"}],"caption":"` + marker + `"}}`)
	for name, body := range bodies {
		stdout, stderr, code := run(body)
		if code != 0 {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
			continue
		}
		if stderr != "" {
			t.Errorf("%s: stderr = %q, want nothing", name, stderr)
		}
		var e hooks.Envelope
		if err := json.Unmarshal([]byte(stdout), &e); err != nil || e.Type == "" {
			t.Errorf("%s: stdout is no envelope: %q, %v", name, stdout, err)
		}
	}
	// What is no Update is refused with one fixed line, holding none of it.
	stdout, stderr, code := run([]byte(`"` + marker + `"`))
	if code == 0 {
		t.Fatalf("a body that is no Update exited 0: %q", stdout)
	}
	if !strings.Contains(stderr, "telegram Update is not an object") {
		t.Errorf("stderr = %q, want the one fixed line", stderr)
	}
	if strings.Contains(stderr, marker) {
		t.Errorf("stderr carries the body's text: %q", stderr)
	}
}

// The hook is verified by the secret in its header, compared as it
// arrives, and a repeated Update is told by its own id: a retry shares
// the first attempt's update_id however it re-serializes the body.
func TestTelegramPackVerify(t *testing.T) {
	d, _, _ := telegramParse(t)
	ad := hooks.Declared{D: d}
	body := fixture(t, "telegram_message_text.json")
	verify := func(body []byte, header string) (string, error) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/telegram", bytes.NewReader(body))
		if header != "" {
			req.Header.Set("X-Telegram-Bot-Api-Secret-Token", header)
		}
		return ad.Verify(req, body, []byte("s3cret"), time.Now())
	}
	if id, err := verify(body, "s3cret"); err != nil || id != "9001" {
		t.Fatalf("accept: id %q, err %v", id, err)
	}
	for _, tc := range []struct{ name, header string }{
		{"wrong secret", "nope"},
		{"missing header", ""},
	} {
		if _, err := verify(body, tc.header); !errors.Is(err, hooks.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want unauthorized", tc.name, err)
		}
	}
	// The same Update re-serialized: other bytes, the same id.
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	reserialized, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reserialized, body) {
		t.Fatal("re-serializing left the bytes alone: the retry below proves nothing")
	}
	if id, err := verify(reserialized, "s3cret"); err != nil || id != "9001" {
		t.Fatalf("re-serialized retry: id %q, err %v", id, err)
	}
	if id, err := verify([]byte(`{"message":{"message_id":1}}`), "s3cret"); err != nil || id != "" {
		t.Fatalf("an Update with no id: id %q, err %v", id, err)
	}
	if ad.Bodies() != hooks.BodiesUniquePerSource {
		t.Errorf("bodies = %v, want unique per source", ad.Bodies())
	}
}

// Answering in the chat: thread.reply posts sendMessage to the chat the
// delivery came from and answers the sent message's id. The stand-in stands
// in for the service; nothing here reaches it. Posted and read answer what
// there is to find, nothing, so a reply under --to is refused: with no
// history no conversation can be found.
func TestTelegramPackThread(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	ad, _ := telegramDecl(t)
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		if gotBody["chat_id"] == "0" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":4242,"chat":{"id":123456789},"text":"hi"}}`)
	}))
	defer srv.Close()
	ad.D.Base = srv.URL
	ctx := context.Background()
	const cred = "bot-token-for-test"
	env := hooks.Envelope{Source: "telegram", Type: "message", Thread: hooks.Thread{ID: "123456789", Title: "Christopher"}}
	m, err := ad.Reply(ctx, env, "", "the shed lands Monday", "key-1", cred)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "4242" || m.Body != "the shed lands Monday" {
		t.Fatalf("reply = %+v", m)
	}
	if gotPath != "/bot"+cred+"/sendMessage" {
		t.Error("the reply went somewhere other than the token's sendMessage")
	}
	if !reflect.DeepEqual(gotBody, map[string]any{"chat_id": "123456789", "text": "the shed lands Monday"}) {
		t.Errorf("the service was sent %v", gotBody)
	}
	// The parent and the key go nowhere: a chat is one conversation, and
	// the service takes no idempotency key.
	if _, err := ad.Reply(ctx, env, "", "again", "key-2", cred); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotBody, map[string]any{"chat_id": "123456789", "text": "again"}) {
		t.Errorf("the service was sent %v", gotBody)
	}
	if found, err := ad.Posted(ctx, env, "key-1", cred); err != nil || found != nil {
		t.Fatalf("posted: %+v, %v", found, err)
	}
	if ms, err := ad.Thread(ctx, env, cred); err != nil || len(ms) != 0 {
		t.Fatalf("read: %+v, %v", ms, err)
	}
	bad := hooks.Envelope{Source: "telegram", Type: "message", Thread: hooks.Thread{ID: "0"}}
	if _, err := ad.Reply(ctx, bad, "", "hello?", "key-3", cred); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("a refused chat: %v", err)
	}
	if _, err := ad.Reply(ctx, env, "9", "hello?", "key-4", cred); err == nil || !strings.Contains(err.Error(), "is not on") {
		t.Errorf("a reply under --to: %v", err)
	}
	if _, err := ad.Reply(ctx, env, "", "hello?", "key-5", ""); err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("a reply with no credential: %v", err)
	}
}

// The bot's token is part of the address the reply is posted to, and of
// both a file fetch asks, and an argument of a running command can be read
// by every process on the machine: curl is handed the address on its
// stdin, never as an argument. The stand-in curl here records its
// arguments and runs the real one.
func TestTelegramPackThreadKeepsTheTokenOutOfArguments(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	ad, _ := telegramDecl(t)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getFile"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_path":"photos/x.jpg","file_size":3}}`)
		case strings.HasPrefix(r.URL.Path, "/file/"):
			_, _ = io.WriteString(w, "abc")
		default:
			gotPath = r.URL.Path
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
		}
	}))
	defer srv.Close()
	ad.D.Base = srv.URL
	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$@\" >>'" + args + "'\nexec '" + curl + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	const cred = "123456:MARKER-naq1-token"
	env := hooks.Envelope{Source: "telegram", Type: "message", Thread: hooks.Thread{ID: "123456789"}}
	if _, err := ad.Reply(context.Background(), env, "", "hello", "key-1", cred); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot"+cred+"/sendMessage" {
		t.Error("the reply went somewhere other than the token's sendMessage")
	}
	if data, err := ad.File(context.Background(), hooks.FileRef{ID: "x"}, t.TempDir(), 10, cred); err != nil || string(data) != "abc" {
		t.Fatalf("file: %q, %v", data, err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("the stand-in curl was not run: %v", err)
	}
	if strings.Contains(string(got), "MARKER-naq1-token") {
		t.Error("the bot's token was an argument of curl")
	}
}

// telegramDepot is a depot whose receiver serves the pack's declared source
// with hook telegram: the delivery path as the yard runs it.
func telegramDepot(t *testing.T) *depot {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	home := t.TempDir()
	r := newDepot(t)
	r.rc = hooks.NewReceiver(r.own, home, nil)
	r.rc.SetClock(func() time.Time { return r.now })
	r.srv.Config.Handler = r.rc.Handler()
	ctx := context.Background()
	d, dir, text := telegramParse(t)
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "telegram.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "telegram.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "telegram", Source: "telegram", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// telegramPost is one Update delivered to the telegram hook: its status and
// its answer.
func telegramPost(t *testing.T, r *depot, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/telegram", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// tellTelegram tells the user through telegram at the chat: their env file
// sets TELEGRAM_CHAT_ID to it. The file is what the hear key reads.
func tellTelegram(t *testing.T, r *depot, user, chat string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), user+".env")
	if err := os.WriteFile(file, []byte("TELEGRAM_CHAT_ID="+chat+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetUser(context.Background(), user,
		map[string]string{store.UserNotify: "telegram", store.UserEnvFile: file}, user); err != nil {
		t.Fatal(err)
	}
}

// strangerUpdate is the Update with the operator's chat replaced by a
// stranger's and every word of its own replaced by the marker, under a new
// update_id: a stranger's Update of the kind.
func strangerUpdate(t *testing.T, body []byte, updateID int, chat int64, marker string) []byte {
	t.Helper()
	var u map[string]any
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatal(err)
	}
	u["update_id"] = updateID
	for _, key := range []string{"message", "edited_message", "channel_post", "my_chat_member", "chat_member"} {
		m, _ := u[key].(map[string]any)
		if m == nil {
			continue
		}
		if c, ok := m["chat"].(map[string]any); ok {
			c["id"] = chat
		}
		if _, ok := m["text"].(string); ok {
			m["text"] = marker
		}
		if _, ok := m["caption"].(string); ok {
			m["caption"] = marker
		}
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// hearPushover is a source that tells a user and hears nothing back: the
// operator's notify in the test below, so the test pins that a chat is
// heard by its address, whatever the user is told through.
const hearPushover = `name: pushover
base: https://api.pushover.net/1
credential: { env: PUSHOVER_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string } }
    request: { method: POST, path: /messages.json, body: { user: "{user}", text: "{text}" } }
notify:
  call: message
  address: { param: user, env: PUSHOVER_USER }
  text: text
`

// A chat is heard by its address, not by notify: the operator, told
// through pushover with TELEGRAM_CHAT_ID in their env file, is heard on
// telegram; another user whose file sets the address is dropped under
// from: operator, and a chat no file sets is dropped as a stranger's.
func TestTelegramPackHearsAddressWhateverNotify(t *testing.T) {
	r := telegramDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutSource(ctx, "pushover.yaml", hearPushover, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddUser(ctx, "zed", "op"); err != nil {
		t.Fatal(err)
	}
	tellTelegram(t, r, "op", "123456789")
	tellTelegram(t, r, "zed", "222222222")
	// The operator is told through pushover: hearing follows the address.
	if _, err := r.st.SetUser(ctx, "op", map[string]string{store.UserNotify: "pushover"}, "op"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	// The operator's line is heard, though told through pushover.
	if code, ans := telegramPost(t, r, fixture(t, "telegram_message_text.json")); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the operator's chat answered %d %s", code, ans)
	}
	// Another user's address is dropped under from: operator, and a chat
	// no env file sets is dropped as a stranger's.
	zed := []byte(`{"update_id":9401,"message":{"message_id":1,"from":{"id":222222222,"first_name":"Zed"},"chat":{"id":222222222,"first_name":"Zed","type":"private"},"date":1760379600,"text":"zed is heard nowhere"}}`)
	if code, ans := telegramPost(t, r, zed); code != http.StatusOK || !strings.Contains(ans, `"dropped"`) {
		t.Fatalf("another user's chat answered %d %s", code, ans)
	}
	if code, ans := telegramPost(t, r, fixture(t, "telegram_message_stranger.json")); code != http.StatusOK || !strings.Contains(ans, `"dropped"`) {
		t.Fatalf("a chat no file sets answered %d %s", code, ans)
	}
	if n := len(r.hookEvents(t)); n != 1 {
		t.Fatalf("want 1 hook event, got %d", n)
	}
	if got := r.rc.Rejected()["stranger"]; got != 2 {
		t.Fatalf("stranger count %d, want 2", got)
	}
}

// A line from the operator's chat reaches the yardmaster's inbox as a
// thread delivery; the chat of another user whose env file sets the
// address, and a stranger's, are dropped by the hear key: answered 200,
// recorded nowhere, with none of their text kept. Every recorded kind is
// tried as a stranger's, and a retry of the operator's Update arrives
// once.
func TestTelegramPackHearDropsStrangersEndToEnd(t *testing.T) {
	r := telegramDepot(t)
	ctx := context.Background()
	const kindMarker = "MARKER-naq1-kind-text-must-not-be-kept"
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AddUser(ctx, "zed", "op"); err != nil {
		t.Fatal(err)
	}
	tellTelegram(t, r, "op", "123456789")
	tellTelegram(t, r, "zed", "222222222")
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "telegram",
		Scope: store.ScopeAll, Where: map[string]string{"type": "message", "chat": "123456789"}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	r.now = time.Now()
	events := func() int { return len(r.hookEvents(t)) }
	inbox := func() int {
		t.Helper()
		page, err := r.st.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	// The operator's line is recorded and in the inbox.
	op := fixture(t, "telegram_message_text.json")
	if code, ans := telegramPost(t, r, op); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the operator's chat answered %d %s", code, ans)
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the operator's line: %d hook events, inbox %d", events(), inbox())
	}
	// A retry of it, re-serialized, arrives once: the update_id dedupes it.
	var v any
	if err := json.Unmarshal(op, &v); err != nil {
		t.Fatal(err)
	}
	reserialized, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reserialized, op) {
		t.Fatal("re-serializing left the bytes alone: the retry below proves nothing")
	}
	if code, ans := telegramPost(t, r, reserialized); code != http.StatusOK || !strings.Contains(ans, `"duplicate"`) {
		t.Fatalf("the retry answered %d %s", code, ans)
	}
	if events() != 1 {
		t.Fatalf("the retry recorded: %d hook events", events())
	}
	// Another user's chat is dropped like a stranger's.
	zed := []byte(`{"update_id":9401,"message":{"message_id":1,"from":{"id":222222222,"first_name":"Zed"},"chat":{"id":222222222,"first_name":"Zed","type":"private"},"date":1760379600,"text":"MARKER-naq1-zed-text-must-not-be-kept"}}`)
	if code, ans := telegramPost(t, r, zed); code != http.StatusOK || !strings.Contains(ans, `"dropped"`) {
		t.Fatalf("another user's chat answered %d %s", code, ans)
	}
	// A stranger's Update of every recorded kind is dropped.
	kinds := []string{
		"telegram_message_text.json", "telegram_message_caption.json", "telegram_message_photo.json",
		"telegram_message_document.json", "telegram_message_voice.json", "telegram_message_reply.json",
		"telegram_edited_message.json", "telegram_my_chat_member.json",
	}
	for i, name := range kinds {
		body := strangerUpdate(t, fixture(t, name), 9500+i, -555666777, kindMarker)
		if code, ans := telegramPost(t, r, body); code != http.StatusOK || !strings.Contains(ans, `"dropped"`) {
			t.Fatalf("%s as a stranger's answered %d %s", name, code, ans)
		}
	}
	if code, ans := telegramPost(t, r, fixture(t, "telegram_message_stranger.json")); code != http.StatusOK || !strings.Contains(ans, `"dropped"`) {
		t.Fatalf("the stranger's chat answered %d %s", code, ans)
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the drops: %d hook events, inbox %d", events(), inbox())
	}
	// The operator's edit is recorded, and nobody's inbox: the setup hears
	// type message alone.
	if code, ans := telegramPost(t, r, fixture(t, "telegram_edited_message.json")); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the operator's edit answered %d %s", code, ans)
	}
	if events() != 2 || inbox() != 1 {
		t.Fatalf("after the operator's edit: %d hook events, inbox %d", events(), inbox())
	}
	// A dropped delivery leaves no text of the stranger's behind: no
	// event, no payload, and no sum carries it. The operator's own line is
	// there, so the scan would find kept text.
	evs, err := r.st.Events(ctx, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, e := range evs {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), kindMarker) || strings.Contains(string(raw), "MARKER-naq1-stranger-text") ||
			strings.Contains(string(raw), "MARKER-naq1-zed-text") {
			t.Fatalf("%s event carries a stranger's text: %s", e.Kind, raw)
		}
		seen = seen || strings.Contains(string(raw), "Can the shed land on Monday?")
	}
	if !seen {
		t.Fatal("the operator's line is in no event: the scan above proves nothing")
	}
	for _, e := range r.hookEvents(t) {
		raw, err := r.st.HookPayload(ctx, e.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), kindMarker) || strings.Contains(string(raw), "MARKER-naq1-stranger-text") ||
			strings.Contains(string(raw), "MARKER-naq1-zed-text") {
			t.Fatalf("payload of %d carries a stranger's text: %s", e.Seq, raw)
		}
	}
	if got := r.rc.Rejected()["stranger"]; got != 10 {
		t.Fatalf("stranger count %d, want 10", got)
	}
	if h, err := r.st.GetHook(ctx, "telegram"); err != nil || h.RefusedFor != "stranger" {
		t.Fatalf("hook refused for %+v %v, want stranger", h, err)
	}
}

// telegramFiles stands in for the Bot API's two halves of a file: getFile,
// which answers the file's path and size for its id, and the file's own
// address under /file/bot<token>/. Each file is its id's bytes; one under
// an id the map lacks is refused as the service refuses one. lie is an id
// whose getFile says fewer bytes than its file has. It counts every
// request it is sent, by path.
type telegramFiles struct {
	token string
	files map[string][]byte
	lie   string

	mu   sync.Mutex
	seen []string
}

func (s *telegramFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, r.URL.Path)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/bot"+s.token+"/getFile" {
		var in struct {
			FileID string `json:"file_id"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &in)
		data, ok := s.files[in.FileID]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: invalid file_id"}`)
			return
		}
		size := len(data)
		if in.FileID == s.lie {
			size = 4
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"file_id":%q,"file_size":%d,"file_path":"photos/%s.jpg"}}`, in.FileID, size, in.FileID)
		return
	}
	if id, ok := strings.CutPrefix(r.URL.Path, "/file/bot"+s.token+"/photos/"); ok {
		if data, ok := s.files[strings.TrimSuffix(id, ".jpg")]; ok {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *telegramFiles) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// A file is fetched in two calls, getFile and then the file's address,
// into the directory the core made, and answered as its path and size.
// One over the bound is refused, whether getFile says so or the bytes do
// while they come, and leaves nothing in the directory; an id the service
// refuses is refused with the service's words.
func TestTelegramPackFile(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	ad, _ := telegramDecl(t)
	const cred = "bot-token-for-test"
	stub := &telegramFiles{token: cred, lie: "lying", files: map[string][]byte{
		"small": []byte("\x89PNG\r\n\x1a\nsmall"),
		"big":   bytes.Repeat([]byte("b"), 100),
		"lying": bytes.Repeat([]byte("l"), 100),
	}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	ad.D.Base = srv.URL
	ctx := context.Background()
	get := func(id string, max int64) ([]byte, string, error) {
		t.Helper()
		dir := t.TempDir()
		data, err := ad.File(ctx, hooks.FileRef{ID: id, Type: "photo"}, dir, max, cred)
		left, _ := os.ReadDir(dir)
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		return data, strings.Join(names, ","), err
	}
	data, _, err := get("small", 50)
	if err != nil || !bytes.Equal(data, stub.files["small"]) {
		t.Fatalf("small: %q, %v", data, err)
	}
	if got := stub.requests(); !slices.Equal(got, []string{"/bot" + cred + "/getFile", "/file/bot" + cred + "/photos/small.jpg"}) {
		t.Fatalf("requests = %q", got)
	}
	before := len(stub.requests())
	if _, left, err := get("big", 50); err == nil || !strings.Contains(err.Error(), "100 bytes, over the limit of 50") || left != "" {
		t.Fatalf("big: left %q, err %v", left, err)
	}
	if got := stub.requests()[before:]; len(got) != 1 {
		t.Fatalf("a file getFile says is over the bound was downloaded: %q", got)
	}
	if _, left, err := get("lying", 50); err == nil || !strings.Contains(err.Error(), "over the limit of 50") || left != "" {
		t.Fatalf("lying: left %q, err %v", left, err)
	}
	if _, _, err := get("nobody", 50); err == nil || !strings.Contains(err.Error(), "invalid file_id") {
		t.Fatalf("an id the service refuses: %v", err)
	}
	// The script's own bound holds where the yard's is higher.
	stub.files["huge"] = bytes.Repeat([]byte("h"), 8<<20+1)
	if _, _, err := get("huge", 64<<20); err == nil || !strings.Contains(err.Error(), "over the limit of 8388608") {
		t.Fatalf("over TELEGRAM_MAX_BYTES: %v", err)
	}
}

// telegramSeq is the seq a kept delivery's answer names.
func telegramSeq(t *testing.T, ans string) uint64 {
	t.Helper()
	var a struct {
		Seq uint64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(ans), &a); err != nil || a.Seq == 0 {
		t.Fatalf("answer %q names no seq: %v", ans, err)
	}
	return a.Seq
}

// A kept delivery's files are fetched after it is answered, with the
// source's own token, into the blob store, and recorded beside the event:
// each a blob, or the reason it was not taken, with its size. A delivery
// whose source has no token set keeps its refs alone; a stranger's is
// dropped before anything is fetched, and a redelivery fetches nothing
// twice.
func TestTelegramPackFetchesFiles(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	r := telegramDepot(t)
	ctx := context.Background()
	const cred = "123456:MARKER-mnc8-token"
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("p"), 100)...)
	stub := &telegramFiles{token: cred, files: map[string][]byte{
		"AgACAgEAAx2": png,                          // the photo's id
		"AwACAgEAAx0": bytes.Repeat([]byte("v"), 9), // the voice note's
	}}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	_, _, text := telegramParse(t)
	if _, err := r.st.PutSource(ctx, "telegram.yaml", strings.Replace(text, "base: https://api.telegram.org", "base: "+srv.URL, 1), "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	tellTelegram(t, r, "op", "123456789")
	r.now = time.Now()
	post := func(body []byte) uint64 {
		t.Helper()
		code, ans := telegramPost(t, r, body)
		if code != http.StatusOK {
			t.Fatalf("answered %d %s", code, ans)
		}
		r.rc.Wait()
		if strings.Contains(ans, `"dropped"`) || strings.Contains(ans, `"duplicate"`) {
			return 0
		}
		return telegramSeq(t, ans)
	}
	files := func(seq uint64) []store.BlobRef {
		t.Helper()
		got, err := r.st.DeliveryFiles(ctx, seq)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// No token set for the source: the voice note keeps its ref alone, and
	// the service is asked nothing.
	voice := post(fixture(t, "telegram_message_voice.json"))
	if got := files(voice); got != nil {
		t.Fatalf("fetched without a token: %+v", got)
	}
	if got := stub.requests(); len(got) != 0 {
		t.Fatalf("the service was asked %q without a token", got)
	}
	env := filepath.Join(t.TempDir(), "telegram.env")
	if err := os.WriteFile(env, []byte("TELEGRAM_BOT_TOKEN="+cred+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.SetSource(ctx, "telegram", store.SourceEnvFile, env, "op"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetSetting(ctx, store.BlobMaxSetting, "8192", "op"); err != nil {
		t.Fatal(err)
	}
	// The photo is fetched, kept as a blob and recorded on its delivery.
	photo := post(fixture(t, "telegram_message_photo.json"))
	got := files(photo)
	if len(got) != 1 || got[0].NotTaken != "" || got[0].Kind() != "image" || got[0].Bytes != int64(len(png)) {
		t.Fatalf("the photo's files = %+v", got)
	}
	if kept, err := os.ReadFile(r.own.BlobPath(got[0].Hash)); err != nil || !bytes.Equal(kept, png) {
		t.Fatalf("the photo's blob: %q, %v", kept, err)
	}
	if blob, err := r.st.DeliveryBlob(ctx, got[0].Hash); err != nil || blob.Bytes != int64(len(png)) {
		t.Fatalf("DeliveryBlob = %+v, %v", blob, err)
	}
	asked := len(stub.requests())
	if asked != 2 {
		t.Fatalf("the photo took %d requests: %q", asked, stub.requests())
	}
	// The document is over the yard's limit by the size the Update says:
	// its reason says it, and it is never downloaded.
	doc := post(fixture(t, "telegram_message_document.json"))
	got = files(doc)
	if len(got) != 1 || got[0].Hash != "" || got[0].Name != "shed-plans.pdf" || got[0].Bytes != 1048576 ||
		!strings.Contains(got[0].NotTaken, "document of 1048576 bytes, over the yard's limit of 8192") {
		t.Fatalf("the document's files = %+v", got)
	}
	if len(stub.requests()) != asked {
		t.Fatalf("the document over the limit was asked for: %q", stub.requests()[asked:])
	}
	// A stranger's photo is dropped before it is kept: the service is
	// asked nothing of it.
	if seq := post(strangerUpdate(t, fixture(t, "telegram_message_photo.json"), 9600, -555666777, "MARKER-mnc8-stranger")); seq != 0 {
		t.Fatalf("a stranger's photo was kept as %d", seq)
	}
	// A redelivery of the photo is a duplicate, and fetches nothing again.
	if seq := post(fixture(t, "telegram_message_photo.json")); seq != 0 {
		t.Fatalf("a redelivery was kept as %d", seq)
	}
	if len(stub.requests()) != asked {
		t.Fatalf("a stranger's photo or a redelivery was fetched: %q", stub.requests()[asked:])
	}
	// Nothing is left where the files were fetched to.
	if left, _ := os.ReadDir(filepath.Join(r.home, "tmp")); len(left) != 0 {
		t.Fatalf("left behind under tmp: %v", left)
	}
}
