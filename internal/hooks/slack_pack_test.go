package hooks_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The slack pack held to the design (docs/design/2026-10-17-yardr-pcxd-
// slack.md, sections 3.1 and 3.6): the envelopes its normalize prints for
// the Events API bodies recorded under testdata, and what its thread
// commands post to the stand-in and answer. The fixtures are the events
// the setup subscribes to in its step 6 (an app_mention in a channel, an
// app_mention in a group direct message, a one-to-one direct message, a
// threaded reply among those, the bot's own message where Slack sends
// one — with its user, and with only its bot id — a message with no
// plain text) and what message.im carries beside new messages: an edit,
// pinned as ignored, since an edit in a direct message reaches the yard
// and must match no subscription. There is no fixture for an unmentioned
// channel message or an unmentioned group message, since no subscription
// brings either to the yard in a first version, and none pins room
// group, which no event can supply.

// slackParse is the pack's declaration as the yard would read it.
func slackParse(t *testing.T) (*source.Declaration, string, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "packs", "slack")
	text, err := os.ReadFile(filepath.Join(dir, "slack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "slack.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	return d, dir, string(text)
}

// slackDecl is the pack's declared source with its command files copied
// as yardr source add copies them, and the home they were copied under.
// The commands are written in jq (and curl for thread), so without it the
// tests that run them skip.
func slackDecl(t *testing.T) (hooks.Declared, string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's commands are written in it")
	}
	d, dir, _ := slackParse(t)
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}, home
}

// The pack declares what section 3.1 says: the bot token as a Bearer
// header, the whoami check call with its select and the post call, a hook
// verified over v0:{timestamp}:{body} with the url_verification challenge
// answered after verification, the channel, team, sender and room keys,
// hearing by identity on the sender field, and thread commands to answer
// in the thread. No notify key: a first version cannot tell through Slack.
func TestSlackPackDeclaration(t *testing.T) {
	d, _, _ := slackParse(t)
	if d.Name != "slack" {
		t.Fatalf("name = %q", d.Name)
	}
	if d.Base != "https://slack.com/api" {
		t.Errorf("base = %q", d.Base)
	}
	if c := d.Credential; c == nil || c.Env != "SLACK_BOT_TOKEN" || c.Header != "Authorization: Bearer {credential}" {
		t.Fatalf("credential = %+v", d.Credential)
	}
	whoami, ok := d.Call("whoami")
	if !ok || whoami.Kind != source.Read || whoami.Request.Line() != "POST /auth.test" || len(whoami.Params) != 0 {
		t.Fatalf("whoami call = %+v", whoami)
	}
	if !reflect.DeepEqual(whoami.Answer.Select, []string{"user_id", "team_id", "bot_id"}) {
		t.Fatalf("whoami select = %q", whoami.Answer.Select)
	}
	if d.Check != "whoami" {
		t.Fatalf("check = %q, want whoami", d.Check)
	}
	post, ok := d.Call("post")
	if !ok || post.Kind != source.Update || post.Request.Line() != "POST /chat.postMessage" {
		t.Fatalf("post call = %+v", post)
	}
	if len(d.Calls) != 2 {
		t.Fatalf("calls = %d, want whoami and post", len(d.Calls))
	}
	h := d.Hook
	if h == nil {
		t.Fatal("the pack declares no hook key")
	}
	v := h.Verify
	if v.Scheme != source.HookScheme || v.Signed != "v0:{timestamp}:{body}" ||
		v.Signature.Header != "X-Slack-Signature" || v.Signature.Prefix != "v0=" ||
		v.Delivery.Body != "event_id" || v.Delivery.Header != "" ||
		v.Timestamp.Header != "X-Slack-Request-Timestamp" || v.Timestamp.Unit != "s" || v.Timestamp.Window != "5m" {
		t.Errorf("verify = %+v", v)
	}
	if h.Bodies != source.BodiesRepeat {
		t.Errorf("bodies = %q, want repeat", h.Bodies)
	}
	if c := h.Challenge; c == nil || c.Body != "type" || c.Value != "url_verification" || c.Answer != "challenge" {
		t.Errorf("challenge = %+v", h.Challenge)
	}
	if strings.Join(h.Normalize, " ") != "slack/normalize" {
		t.Errorf("normalize = %q", h.Normalize)
	}
	if strings.Join(h.Keys, ",") != "channel,team,sender,room" {
		t.Errorf("keys = %q, want channel,team,sender,room", h.Keys)
	}
	th := d.Thread
	if th == nil {
		t.Fatal("the pack declares no thread key")
	}
	if strings.Join(th.Reply, " ") != "slack/thread reply" || strings.Join(th.Posted, " ") != "slack/thread posted" ||
		strings.Join(th.Read, " ") != "slack/thread read" {
		t.Errorf("thread = reply %q posted %q read %q", th.Reply, th.Posted, th.Read)
	}
	if len(th.Open) > 0 || len(th.Opened) > 0 || len(th.State) > 0 || th.Place != "" || len(th.States) > 0 || d.Link != nil {
		t.Error("the pack declares an open, state or link key: a thread is answered in, nothing else")
	}
	if d.Notify != nil {
		t.Errorf("the pack declares a notify key: a first version cannot tell through Slack: %+v", d.Notify)
	}
	if h := d.Hear; h == nil || h.Field != "sender" || h.By != source.HearByIdentity || h.Address.Env != "" || h.From != "" {
		t.Errorf("hear = %+v, want field sender by identity", d.Hear)
	}
}

// The envelopes of sections 3.1 and 3.6: every event the setup
// subscribes to becomes type message in the thread of <channel>:<parent
// ts>, with the mentions as member ids and the channel, team, sender and
// room fields. The group mention reads room channel like a channel
// mention: no mark in an app_mention reliably tells a group from a
// channel. The threaded reply carries its thread's parent, the bot's own
// message names the bot as actor — by its user where Slack sends one,
// by its bot id alone where the echo carries no user — and the message
// with no plain text is noted as a file.
func TestSlackPackNormalize(t *testing.T) {
	ad, _ := slackDecl(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	msg := func(actor, thread, message, parent, text string, mentions []string, fields map[string][]string) hooks.Envelope {
		return hooks.Envelope{Source: "slack", Type: "message", Action: "create",
			Actor: hooks.Party{ID: actor}, Thread: hooks.Thread{ID: thread},
			Mentions: mentions, Text: text, Message: message, Parent: parent, Fields: fields}
	}
	for _, tc := range []struct {
		name string
		want hooks.Envelope
	}{
		{"slack_app_mention_channel.json", msg("U012AB3CD", "C012AB3CD:1729156800.000100",
			"1729156800.000100", "1729156800.000100", "<@U999BOT99> can the shed land on Monday?", []string{"U999BOT99"},
			map[string][]string{"channel": {"C012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"U012AB3CD"}, "room": {"channel"}})},
		{"slack_app_mention_group.json", msg("U034EF56G", "G098MN456:1729156850.000150",
			"1729156850.000150", "1729156850.000150", "<@U999BOT99> and <@U012AB3CD>, standup in five? <@U999BOT99>",
			[]string{"U999BOT99", "U012AB3CD"},
			map[string][]string{"channel": {"G098MN456"}, "team": {"T012AB3CD"}, "sender": {"U034EF56G"}, "room": {"channel"}})},
		{"slack_message_dm.json", msg("U012AB3CD", "D012AB3CD:1729156900.000200",
			"1729156900.000200", "1729156900.000200", "morning — did the shed land?", []string{},
			map[string][]string{"channel": {"D012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"U012AB3CD"}, "room": {"dm"}})},
		{"slack_app_mention_thread.json", msg("U078GH90J", "C012AB3CD:1729156800.000100",
			"1729157000.000300", "1729156800.000100", "<@U999BOT99> yes, and loop in <#C012AB3CD|general>?", []string{"U999BOT99"},
			map[string][]string{"channel": {"C012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"U078GH90J"}, "room": {"channel"}})},
		{"slack_bot_own_message.json", msg("U999BOT99", "D012AB3CD:1729157100.000400",
			"1729157100.000400", "1729157100.000400", "Noted; I will post here when it lands.", []string{},
			map[string][]string{"channel": {"D012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"U999BOT99"}, "room": {"dm"}})},
		{"slack_bot_own_message_bot_id_only.json", msg("B012AB3CD", "D012AB3CD:1729157100.000400",
			"1729157100.000400", "1729157100.000400", "Noted; I will post here when it lands.", []string{},
			map[string][]string{"channel": {"D012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"B012AB3CD"}, "room": {"dm"}})},
		{"slack_message_file.json", msg("U012AB3CD", "D012AB3CD:1729157200.000500",
			"1729157200.000500", "1729157200.000500", "[file]", []string{},
			map[string][]string{"channel": {"D012AB3CD"}, "team": {"T012AB3CD"}, "sender": {"U012AB3CD"}, "room": {"dm"}})},
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
		// room has no group word: a message is in a one-to-one direct
		// message or it is not.
		if room := got.Fields["room"]; !reflect.DeepEqual(room, []string{"dm"}) && !reflect.DeepEqual(room, []string{"channel"}) {
			t.Errorf("%s: room = %q, want dm or channel", tc.name, room)
		}
	}
}

// What else a subscribed event carries: a label after a mention is not
// part of the id, channel and link references mention nobody, an
// app_mention in a D channel with no channel_type is a direct message by
// its first letter, and a message with no plain text and no files is
// noted as a message. An event the yard never subscribes to is answered
// as type event and matches no subscription, as an edit or a delete in
// a direct message is: both arrive through message.im, carrying no
// top-level user or text, and are ignored rather than delivered.
func TestSlackPackNormalizeNotes(t *testing.T) {
	ad, _ := slackDecl(t)
	event := func(ev string) []byte {
		return []byte(`{"type":"event_callback","team_id":"T012AB3CD","event_id":"Ev9","event":` + ev + `}`)
	}
	long := strings.Repeat("word ", 200) // 1000 characters, collapsed and cut
	for _, tc := range []struct {
		name         string
		body         []byte
		wantType     string
		wantText     string
		wantMentions []string
		wantRoom     string
		wantThread   string
	}{
		{"a label is not the id", event(`{"type":"app_mention","user":"U012AB3CD","text":"<@U999BOT99|yardr> ship it","ts":"1729156800.000100","channel":"C012AB3CD"}`),
			"message", "<@U999BOT99|yardr> ship it", []string{"U999BOT99"}, "channel", "C012AB3CD:1729156800.000100"},
		{"channel and link references mention nobody", event(`{"type":"app_mention","user":"U012AB3CD","text":"see <#C012AB3CD|general> and <https://example.com|here>","ts":"1729156800.000100","channel":"C012AB3CD"}`),
			"message", "see <#C012AB3CD|general> and <https://example.com|here>", []string{}, "channel", "C012AB3CD:1729156800.000100"},
		{"a D channel with no channel_type is a dm", event(`{"type":"app_mention","user":"U012AB3CD","text":"<@U999BOT99> hi","ts":"1729156800.000100","channel":"D012AB3CD"}`),
			"message", "<@U999BOT99> hi", []string{"U999BOT99"}, "dm", "D012AB3CD:1729156800.000100"},
		{"empty text without files", event(`{"type":"message","channel_type":"im","user":"U012AB3CD","text":"","ts":"1729156800.000100","channel":"D012AB3CD"}`),
			"message", "[message]", []string{}, "dm", "D012AB3CD:1729156800.000100"},
		{"long text is cut", event(`{"type":"message","channel_type":"im","user":"U012AB3CD","text":"` + long + `","ts":"1729156800.000100","channel":"D012AB3CD"}`),
			"message", "", []string{}, "dm", "D012AB3CD:1729156800.000100"}, // want computed below
		{"an event never subscribed to", event(`{"type":"channel_created","channel":{"id":"C099NEWCH","name":"new"}}`),
			"event", "", []string{}, "", ""},
		{"an edit in a direct message", fixture(t, "slack_message_changed.json"),
			"event", "", []string{}, "", ""},
		{"a delete in a direct message", event(`{"type":"message","subtype":"message_deleted","hidden":true,"channel":"D012AB3CD","channel_type":"im","ts":"1729156960.000260","deleted_ts":"1729156900.000200","previous_message":{"type":"message","user":"U012AB3CD","text":"morning — did the shed land?","ts":"1729156900.000200"}}`),
			"event", "", []string{}, "", ""},
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
		if got.Type != tc.wantType || got.Text != wantText || !reflect.DeepEqual(got.Mentions, tc.wantMentions) {
			t.Errorf("%s: type %q text %q mentions %q, want %q %q %q",
				tc.name, got.Type, got.Text, got.Mentions, tc.wantType, wantText, tc.wantMentions)
		}
		if tc.wantRoom == "" {
			if len(got.Fields) != 0 {
				t.Errorf("%s: fields = %v, want none", tc.name, got.Fields)
			}
			continue
		}
		if !reflect.DeepEqual(got.Fields["room"], []string{tc.wantRoom}) {
			t.Errorf("%s: room = %q, want %q", tc.name, got.Fields["room"], tc.wantRoom)
		}
		if got.Thread.ID != tc.wantThread {
			t.Errorf("%s: thread = %q, want %q", tc.name, got.Thread.ID, tc.wantThread)
		}
	}
}

// A normalize that exits non-zero is logged with its stderr and answered
// 400, which the service retries. So the script answers an envelope for
// every body and never prints any of it: on success it prints nothing to
// stderr at all, and its one error names no part of the body.
func TestSlackPackNormalizeNeverPrintsBody(t *testing.T) {
	ad, home := slackDecl(t)
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
		"slack_app_mention_channel.json", "slack_app_mention_group.json", "slack_message_dm.json",
		"slack_app_mention_thread.json", "slack_bot_own_message.json", "slack_bot_own_message_bot_id_only.json",
		"slack_message_file.json", "slack_message_changed.json",
	} {
		bodies[name] = fixture(t, name)
	}
	const marker = "MARKER-q2jq-never-printed"
	bodies["stranger text"] = []byte(`{"type":"event_callback","team_id":"T1","event_id":"Ev9","event":{"type":"app_mention","user":"U9","text":"` + marker + `","ts":"1.0","channel":"C9"}}`)
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
	// What is no event is refused with one fixed line, holding none of it.
	stdout, stderr, code := run([]byte(`"` + marker + `"`))
	if code == 0 {
		t.Fatalf("a body that is no event exited 0: %q", stdout)
	}
	if !strings.Contains(stderr, "slack event is not an object") {
		t.Errorf("stderr = %q, want the one fixed line", stderr)
	}
	if strings.Contains(stderr, marker) {
		t.Errorf("stderr carries the body's text: %q", stderr)
	}
}

// slackSign is Slack's signature: the hex HMAC-SHA256 with the signing
// secret over the literal basestring v0:<timestamp>:<raw body>.
func slackSign(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("v0:" + ts + ":"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// The hook is verified over the timestamp and the body together, past the
// v0= prefix, and a request more than five minutes from the receiver's
// clock is refused. A retry repeats the event's own id however it
// re-serializes the body, and bodies otherwise repeat.
func TestSlackPackVerify(t *testing.T) {
	d, _, _ := slackParse(t)
	ad := hooks.Declared{D: d}
	body := fixture(t, "slack_app_mention_channel.json")
	now := time.Now()
	verify := func(body []byte, ts, sig string) (string, error) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/slack", bytes.NewReader(body))
		if ts != "" {
			req.Header.Set("X-Slack-Request-Timestamp", ts)
		}
		if sig != "" {
			req.Header.Set("X-Slack-Signature", sig)
		}
		return ad.Verify(req, body, []byte("s3cret"), now)
	}
	sec := strconv.FormatInt(now.Unix(), 10)
	if id, err := verify(body, sec, "v0="+slackSign("s3cret", sec, body)); err != nil || id != "Ev012AB3CD01" {
		t.Fatalf("accept: id %q, err %v", id, err)
	}
	stale := strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10)
	for _, tc := range []struct {
		name string
		ts   string
		sig  string
	}{
		{"a signature over the body alone", sec, "v0=" + sign("s3cret", body)},
		{"a bad signature", sec, "v0=" + strings.Repeat("0", 64)},
		{"a stale timestamp", stale, "v0=" + slackSign("s3cret", stale, body)},
		{"a missing signature", sec, ""},
		{"a missing timestamp", "", "v0=" + slackSign("s3cret", sec, body)},
	} {
		if _, err := verify(body, tc.ts, tc.sig); !errors.Is(err, hooks.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want unauthorized", tc.name, err)
		}
	}
	// The same event re-serialized: other bytes, the same id.
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
	if id, err := verify(reserialized, sec, "v0="+slackSign("s3cret", sec, reserialized)); err != nil || id != "Ev012AB3CD01" {
		t.Fatalf("re-serialized retry: id %q, err %v", id, err)
	}
	if ad.Bodies() != hooks.BodiesRepeat {
		t.Errorf("bodies = %v, want repeat", ad.Bodies())
	}
}

// slackThread is one message the stand-in's conversations.replies serves:
// its ts, its thread's parent ts ("" for the thread's first message,
// which carries no thread_ts of its own), its sender and its text.
type slackThread struct {
	ts, parent, user, text string
}

// Answering in the thread: thread.reply posts chat.postMessage to the
// thread's channel with its thread_ts and answers the posted ts, for a
// channel, a group-direct-message and a one-to-one direct-message thread
// alike. thread.read follows conversations.replies across two pages —
// the first answer's has_more with a next_cursor, the second completing
// the thread — and maps the concatenated messages oldest first, for all
// three kinds alike; a --to naming a message that stands only on the
// second page is found there. Where Slack refuses the read the script
// surfaces its reason, never an empty thread. Posted answers what there
// is to find, nothing. The stand-in stands in for the service; nothing
// here reaches it.
func TestSlackPackThread(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	ad, _ := slackDecl(t)
	threads := map[string][]slackThread{
		"C012AB3CD": {
			{"1729156800.000100", "", "U012AB3CD", "can the shed land on Monday?"},
			{"1729156900.000200", "1729156800.000100", "U999BOT99", "on it"},
			{"1729157000.000300", "1729156800.000100", "U078GH90J", "and the gate?"},
		},
		"G098MN456": {
			{"1729156810.000110", "", "U034EF56G", "standup in five?"},
			{"1729156910.000210", "1729156810.000110", "U999BOT99", "noted"},
			{"1729157010.000310", "1729156810.000110", "U012AB3CD", "bring the numbers"},
		},
		"D012AB3CD": {
			{"1729156820.000120", "", "U012AB3CD", "morning"},
			{"1729156920.000220", "1729156820.000120", "U999BOT99", "morning — did the shed land?"},
			{"1729157020.000320", "1729156820.000120", "U012AB3CD", "it did"},
		},
	}
	var posts, reads []map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "chat.postMessage":
			auth = r.Header.Get("Authorization")
			posts = append(posts, body)
			if body["text"] == "refuse this" {
				_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"ts":"1729157300.000900"}`)
		case "conversations.replies":
			reads = append(reads, body)
			channel, _ := body["channel"].(string)
			switch channel {
			case "C000NOPE":
				_, _ = io.WriteString(w, `{"ok":false,"error":"not_in_channel"}`)
				return
			case "G000SCOPE":
				_, _ = io.WriteString(w, `{"ok":false,"error":"missing_scope"}`)
				return
			}
			msgs, ok := threads[channel]
			if !ok {
				_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
				return
			}
			// Two pages: the parent and its first reply, then the rest.
			// The first answer carries has_more with a next_cursor, the
			// second completes the thread.
			page := msgs[:2]
			more := true
			if cursor, _ := body["cursor"].(string); cursor != "" {
				if cursor != "next-"+channel {
					t.Errorf("conversations.replies was sent cursor %q, want %q", cursor, "next-"+channel)
				}
				page, more = msgs[2:], false
			}
			out := map[string]any{"ok": true, "has_more": more, "messages": []map[string]any{}}
			if more {
				out["response_metadata"] = map[string]any{"next_cursor": "next-" + channel}
			}
			for _, m := range page {
				msg := map[string]any{"type": "message", "user": m.user, "text": m.text, "ts": m.ts}
				if m.parent != "" {
					msg["thread_ts"] = m.parent
				}
				out["messages"] = append(out["messages"].([]map[string]any), msg)
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
		}
	}))
	defer srv.Close()
	ad.D.Base = srv.URL
	ctx := context.Background()
	const cred = "xoxb-test-token"
	env := func(channel string) hooks.Envelope {
		return hooks.Envelope{Source: "slack", Type: "message", Thread: hooks.Thread{ID: channel + ":" + threads[channel][0].ts}}
	}
	want := func(channel string) []hooks.Message {
		var out []hooks.Message
		for _, m := range threads[channel] {
			sec, _ := strconv.ParseInt(strings.SplitN(m.ts, ".", 2)[0], 10, 64)
			parent := m.parent
			if parent == "" {
				parent = m.ts
			}
			out = append(out, hooks.Message{ID: m.ts, Parent: parent,
				Author: hooks.Party{ID: m.user}, At: time.Unix(sec, 0).UTC(), Body: m.text})
		}
		return out
	}
	for _, channel := range []string{"C012AB3CD", "G098MN456", "D012AB3CD"} {
		m, err := ad.Reply(ctx, env(channel), "", "the shed lands Monday", "key-"+channel, cred)
		if err != nil {
			t.Fatal(err)
		}
		if m.ID != "1729157300.000900" || m.Body != "the shed lands Monday" {
			t.Fatalf("%s: reply = %+v", channel, m)
		}
	}
	if auth != "Bearer "+cred {
		t.Errorf("the service was called with Authorization %q", auth)
	}
	for i, channel := range []string{"C012AB3CD", "G098MN456", "D012AB3CD"} {
		if !reflect.DeepEqual(posts[i], map[string]any{"channel": channel, "text": "the shed lands Monday", "thread_ts": threads[channel][0].ts}) {
			t.Errorf("%s: the service was sent %v", channel, posts[i])
		}
	}
	for _, channel := range []string{"C012AB3CD", "G098MN456", "D012AB3CD"} {
		before := len(reads)
		ms, err := ad.Thread(ctx, env(channel), cred)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ms, want(channel)) {
			wantRaw, _ := json.MarshalIndent(want(channel), "", "  ")
			gotRaw, _ := json.MarshalIndent(ms, "", "  ")
			t.Errorf("%s:\n got %s\nwant %s", channel, gotRaw, wantRaw)
		}
		got := reads[before:]
		if len(got) != 2 {
			t.Fatalf("%s: the thread was read in %d calls, want 2", channel, len(got))
		}
		if _, ok := got[0]["cursor"]; ok {
			t.Errorf("%s: the first read was sent a cursor: %v", channel, got[0])
		}
		if got[1]["cursor"] != "next-"+channel {
			t.Errorf("%s: the second read was sent %v", channel, got[1])
		}
	}
	// A --to naming a message that stands only on the second page is
	// found there: without the cursor the read would end at the first
	// page and the reply would be refused as not on the thread.
	target := threads["C012AB3CD"][2].ts
	m, err := ad.Reply(ctx, env("C012AB3CD"), target, "the gate too", "key-to", cred)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "1729157300.000900" {
		t.Fatalf("reply --to: %+v", m)
	}
	if last := posts[len(posts)-1]; last["thread_ts"] != threads["C012AB3CD"][0].ts {
		t.Errorf("reply --to was sent %v", last)
	}
	if found, err := ad.Posted(ctx, env("C012AB3CD"), "key-1", cred); err != nil || found != nil {
		t.Fatalf("posted: %+v, %v", found, err)
	}
	// Where Slack refuses the read, its reason surfaces: the thread is
	// not read as empty.
	bad := hooks.Envelope{Source: "slack", Type: "message", Thread: hooks.Thread{ID: "C000NOPE:1729156800.000100"}}
	if _, err := ad.Thread(ctx, bad, cred); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Errorf("a bot no longer in the conversation: %v", err)
	}
	scoped := hooks.Envelope{Source: "slack", Type: "message", Thread: hooks.Thread{ID: "G000SCOPE:1729156810.000110"}}
	if _, err := ad.Thread(ctx, scoped, cred); err == nil || !strings.Contains(err.Error(), "missing_scope") {
		t.Errorf("a token missing the history scope: %v", err)
	}
	if _, err := ad.Reply(ctx, env("C012AB3CD"), "", "refuse this", "key-bad", cred); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("a refused post: %v", err)
	}
	if _, err := ad.Reply(ctx, env("C012AB3CD"), "", "hello?", "key-nocred", ""); err == nil || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Errorf("a reply with no credential: %v", err)
	}
}

// The bot's token is sent as an Authorization header, and an argument of
// a running command can be read by every process on the machine: curl is
// handed the header on its stdin, never as an argument. The stand-in curl
// here records its arguments and runs the real one.
func TestSlackPackThreadKeepsTheTokenOutOfArguments(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed, and the pack's thread command calls the service with it")
	}
	ad, _ := slackDecl(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "chat.postMessage":
			_, _ = io.WriteString(w, `{"ok":true,"ts":"1729157300.000900"}`)
		case "conversations.replies":
			_, _ = io.WriteString(w, `{"ok":true,"has_more":false,"messages":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
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
	const cred = "xoxb-MARKER-q2jq-token"
	env := hooks.Envelope{Source: "slack", Type: "message", Thread: hooks.Thread{ID: "C012AB3CD:1729156800.000100"}}
	if _, err := ad.Reply(context.Background(), env, "", "hello", "key-1", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Thread(context.Background(), env, cred); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("the stand-in curl was not run: %v", err)
	}
	if strings.Contains(string(got), "MARKER-q2jq-token") {
		t.Error("the bot's token was an argument of curl")
	}
}
