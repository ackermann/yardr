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
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The posthog pack held to PostHog's source: the envelope its normalize
// prints for the issue-created body recorded under testdata, the shapes
// around it, and the end-to-end delivery that records a signed one and
// refuses an unsigned one. The body is the HTTP Webhook destination's
// default {event, person} with the project the setup adds beside it
// (nodejs/src/cdp/templates/_destinations/webhook/webhook.template.ts,
// posthog.com/docs/cdp/destinations/customizing-destinations); the event
// is what the issue-created workflow emits
// (products/error_tracking/backend/temporal/lifecycle/side_effects.py,
// products/cdp/backend/internal_events.py).

// posthogParse is the pack's declaration as the yard would read it.
func posthogParse(t *testing.T) (*source.Declaration, string, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "packs", "posthog")
	text, err := os.ReadFile(filepath.Join(dir, "posthog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "posthog.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	return d, dir, string(text)
}

// posthogDecl is the pack's declared source with its command files copied
// as yardr source add copies them. The command is written in jq, so
// without it the tests that run it skip.
func posthogDecl(t *testing.T) hooks.Declared {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	d, dir, _ := posthogParse(t)
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return hooks.Declared{D: d, Home: home}
}

// The pack declares what the setup needs: a hook verified by the secret
// in its header, deduped by the event's own id, with the project, the
// session and the replay link as its keys, and the survey, the email,
// the footplate version and the OS a feedback subscription filters on,
// and registered by the yard itself on the hook card. No hear key:
// every verified delivery is kept. No thread, link or notify key: the
// destination call is the one thing the yard writes to PostHog.
func TestPostHogPackDeclaration(t *testing.T) {
	d, dir, _ := posthogParse(t)
	if d.Name != "posthog" {
		t.Fatalf("name = %q", d.Name)
	}
	h := d.Hook
	if h == nil {
		t.Fatal("the pack declares no hook key")
	}
	if h.Verify.Scheme != source.HookSecret || h.Verify.Signature.Header != "X-Yardr-Secret" {
		t.Errorf("verify = %+v, want scheme secret in X-Yardr-Secret", h.Verify)
	}
	if h.Verify.Delivery.Body != "event.uuid" || h.Verify.Delivery.Header != "" {
		t.Errorf("delivery = %+v, want the body at event.uuid", h.Verify.Delivery)
	}
	if h.Bodies != source.BodiesUnique {
		t.Errorf("bodies = %q, want unique", h.Bodies)
	}
	if strings.Join(h.Normalize, " ") != "posthog/normalize" {
		t.Errorf("normalize = %q", h.Normalize)
	}
	if strings.Join(h.Keys, ",") != "project,session,replay,survey,email,footplate_version,os" {
		t.Errorf("keys = %q, want project,session,replay,survey,email,footplate_version,os", h.Keys)
	}
	if h.Register == nil {
		t.Fatal("the pack declares no register key: the yard registers the destination itself on the hook card")
	}
	if h.Register.Call != "destination" || h.Register.URL != "url" || h.Register.Secret != "secret" ||
		len(h.Register.Ask) != 1 || h.Register.Ask[0] != "project" {
		t.Errorf("register = %+v, want the destination call with url, secret and ask [project]", h.Register)
	}
	if ex := h.Register.Existing; ex == nil || ex.Call != "destinations" || ex.In != "results" || ex.Field != "inputs.url.value" {
		t.Errorf("existing = %+v, want the destinations call with results and inputs.url.value", h.Register.Existing)
	}
	if d.Hear != nil || d.Thread != nil || d.Link != nil || d.Notify != nil {
		t.Error("the pack declares a hear, thread, link or notify key: it reads PostHog and hears a new error issue, and only its destination call writes")
	}
	var update []string
	for _, c := range d.Calls {
		if c.Kind == source.Update {
			update = append(update, c.Name)
		}
	}
	if strings.Join(update, ",") != "destination" {
		t.Errorf("update calls = %q, want the destination alone", update)
	}
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatalf("the pack does not load: %v", err)
	}
	for _, pr := range p.Presets {
		for _, g := range pr.Grants {
			for _, call := range g.Calls {
				if call == "destination" {
					t.Errorf("preset %s grants the destination: only the hook card calls it", pr.Name)
				}
			}
		}
	}
}

// The recorded issue and feedback bodies normalize to their full
// envelopes, with fields limited to the hook's declared keys.
func TestPostHogPackNormalize(t *testing.T) {
	ad := posthogDecl(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	const id = "019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e"
	const url = "https://us.posthog.com/project/48291/error-tracking/" + id
	const survey = "019f99f0-1234-7abc-8def-0123456789ab"
	for _, tc := range []struct {
		name, fixture string
		want          hooks.Envelope
	}{
		{
			name: "issue-created", fixture: "posthog_issue_created.json",
			want: hooks.Envelope{Source: "posthog", Type: "error_issue", Action: "created",
				Thread:   hooks.Thread{ID: id, Title: "TypeError: Cannot read properties of undefined", URL: url},
				Mentions: []string{}, Text: "TypeError: Cannot read properties of undefined (reading 'id')",
				URL: url, Entity: id, Fields: map[string][]string{"project": {"48291"}}},
		},
		{
			name: "bare feedback", fixture: "posthog_survey_sent.json",
			want: hooks.Envelope{Source: "posthog", Type: "feedback",
				Thread:   hooks.Thread{ID: "019f9a01-b1c2-7d3e-8f4a-9b0c1d2e3f4a"},
				Mentions: []string{}, Text: "The rail hides my longest depot name; please let it wrap.",
				Fields: map[string][]string{"project": {"48291"}, "survey": {survey},
					"email": {"ada@example.com"}, "footplate_version": {"0.9.3"}, "os": {"macos"}}},
		},
		{
			name: "keyed feedback", fixture: "posthog_survey_sent_keyed.json",
			want: hooks.Envelope{Source: "posthog", Type: "feedback",
				Thread:   hooks.Thread{ID: "019f9a02-c2d3-7e4f-9a5b-8c1d2e3f4a5b"},
				Mentions: []string{}, Text: "Dark mode, please.",
				Fields: map[string][]string{"project": {"48291"}, "survey": {survey},
					"footplate_version": {"0.9.3"}, "os": {"linux"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ad.Normalize(nil, fixture(t, tc.fixture))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if !reflect.DeepEqual(got, &tc.want) {
				wantRaw, _ := json.MarshalIndent(tc.want, "", "  ")
				gotRaw, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("got %s\nwant %s", gotRaw, wantRaw)
			}
			for k := range got.Fields {
				found := false
				for _, k2 := range may {
					found = found || k == k2
				}
				if !found {
					t.Errorf("field %q is in neither hook.keys nor store.WhereKeys", k)
				}
			}
		})
	}
}

// The envelope for issue-created bodies that carry a session: the
// triggering exception's $session_id, which the destination's {event}
// renders under exception_props (the issue-created workflow puts the
// exception's properties there), becomes the session field, and the
// replay link beside the project. This holds both a recorded issue and
// the event the setup asks the operator to paste into PostHog's Testing
// globals editor. Their issue id, name, description and URL follow the
// same normalization path.
func TestPostHogPackNormalizeSession(t *testing.T) {
	ad := posthogDecl(t)
	may := append(append([]string{}, store.WhereKeys...), ad.D.Hook.Keys...)
	for _, tc := range []struct {
		name, fixture, id, session, title, text string
	}{
		{
			name: "recorded issue", fixture: "posthog_issue_created_with_session.json",
			id: "019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e", session: "019f7c3b-4d5e-7a1b-8c2d-9e0f1a2b3c4d",
			title: "TypeError: Cannot read properties of undefined", text: "TypeError: Cannot read properties of undefined (reading 'id')",
		},
		{
			name: "destination test globals", fixture: "posthog_destination_test.json",
			id: "019f8d01-aaaa-7bbb-8ccc-abcabcabcabc", session: "019f8d01-dddd-7eee-8fff-defdefdefdef",
			title: "PostHog destination test issue", text: "Sent from the destination's Testing panel; not a real issue.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := "https://us.posthog.com/project/48291/error-tracking/" + tc.id
			replay := "https://us.posthog.com/project/48291/replay/" + tc.session
			want := hooks.Envelope{Source: "posthog", Type: "error_issue", Action: "created",
				Thread:   hooks.Thread{ID: tc.id, Title: tc.title, URL: url},
				Mentions: []string{}, Text: tc.text,
				URL: url, Entity: tc.id, Fields: map[string][]string{"project": {"48291"}, "session": {tc.session}, "replay": {replay}}}
			got, err := ad.Normalize(nil, fixture(t, tc.fixture))
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if !reflect.DeepEqual(got, &want) {
				wantRaw, _ := json.MarshalIndent(want, "", "  ")
				gotRaw, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("got %s\nwant %s", gotRaw, wantRaw)
			}
			for k := range got.Fields {
				found := false
				for _, k2 := range may {
					found = found || k == k2
				}
				if !found {
					t.Errorf("field %q is in neither hook.keys nor store.WhereKeys", k)
				}
			}
		})
	}
}

// The issue and feedback shapes include the destination's default body,
// fields that may be absent or malformed, truncation, and unmatched
// events. Issue links are built only from guarded project and issue
// values; feedback carries neither a link nor a mention.
func TestPostHogPackNormalizeShapes(t *testing.T) {
	ad := posthogDecl(t)
	event := `"event":"$error_tracking_issue_created","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192","distinct_id":"019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e","timestamp":"2026-10-20T14:32:02.000000+00:00"`
	project := `"project":{"id":48291,"name":"signalbox","url":"https://us.posthog.com/project/48291"}`
	created := func(props, rest string) []byte {
		return []byte(`{"event":{` + event + `,"properties":{` + props + `}}` + rest + `}`)
	}
	const feedbackUUID = "019f9a01-b1c2-7d3e-8f4a-9b0c1d2e3f4a"
	const survey = "019f99f0-1234-7abc-8def-0123456789ab"
	feedbackBase := `"$survey_id":"` + survey + `","footplate_version":"0.9.3","os":"macos"`
	feedbackProject := `,"project":{"id":48291,"name":"signalbox","url":"https://eu.posthog.com/project/48291"}`
	feedback := func(props string) []byte {
		return []byte(`{"event":{"event":"survey sent","uuid":"` + feedbackUUID + `","properties":{` + props + `}}` + feedbackProject + `}`)
	}
	feedbackFields := func(withProject, withEmail bool) map[string][]string {
		fields := map[string][]string{"survey": {survey}, "footplate_version": {"0.9.3"}, "os": {"macos"}}
		if withProject {
			fields["project"] = []string{"48291"}
		}
		if withEmail {
			fields["email"] = []string{"ada@example.com"}
		}
		return fields
	}
	long := strings.Repeat("word ", 200) // 1000 characters, collapsed and cut
	const id = "019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e"
	const session = "019f7c3b-4d5e-7a1b-8c2d-9e0f1a2b3c4d"
	const url = "https://us.posthog.com/project/48291/error-tracking/" + id
	const replay = "https://us.posthog.com/project/48291/replay/" + session
	for _, tc := range []struct {
		name       string
		body       []byte
		wantType   string
		wantAction string
		wantText   string
		wantThread hooks.Thread
		wantFields map[string][]string // nil for none
		wantURL    string
	}{
		{"no project",
			created(`"name":"boom","description":"it went boom"`, ""),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom"}, nil, ""},
		{"null name and description",
			created(`"name":null,"description":null`, ","+project),
			"error_issue", "created", "",
			hooks.Thread{ID: id, Title: "", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"long description is cut",
			created(`"name":"boom","description":"`+long+`"`, ","+project),
			"error_issue", "created", strings.TrimRight(long, " ")[:499] + "…",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"no distinct_id falls back to the properties",
			[]byte(`{"event":{"event":"$error_tracking_issue_created","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192","properties":{"name":"boom","description":"it went boom","$exception_issue_id":"` + id + `"}},` + project + `}`),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"a trailing slash leaves the URL",
			[]byte(`{"event":{` + event + `,"properties":{"name":"boom","description":"it went boom"}},"project":{"id":48291,"url":"https://us.posthog.com/project/48291/"}}`),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"a person is ignored",
			[]byte(`{"event":{` + event + `,"properties":{"name":"boom","description":"it went boom"}},"person":{"id":"1","name":"Ada"},` + project + `}`),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"a session under exception_props",
			created(`"name":"boom","description":"it went boom","exception_props":{"$session_id":"`+session+`"}`, ","+project),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}, "session": {session}, "replay": {replay}}, url},
		{"a message too large carries no session",
			created(`"name":"boom","description":"it went boom","message_was_too_large":true`, ","+project),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"a session id that is no string",
			created(`"name":"boom","description":"it went boom","exception_props":{"$session_id":42}`, ","+project),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom", URL: url}, map[string][]string{"project": {"48291"}}, url},
		{"a session without a project is heard with no link",
			created(`"name":"boom","description":"it went boom","exception_props":{"$session_id":"`+session+`"}`, ""),
			"error_issue", "created", "it went boom",
			hooks.Thread{ID: id, Title: "boom"}, map[string][]string{"session": {session}}, ""},
		{"another event is type event",
			[]byte(`{"event":{"event":"$error_tracking_issue_reopened","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708193","distinct_id":"` + id + `","properties":{"name":"boom"}},` + project + `}`),
			"event", "", "",
			hooks.Thread{}, map[string][]string{"project": {"48291"}}, ""},
		{"another event without a project",
			[]byte(`{"event":{"event":"test","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708194"}}`),
			"event", "", "",
			hooks.Thread{}, nil, ""},
		{"bare feedback with every field",
			feedback(feedbackBase + `,"$survey_response":"please let the rail wrap","email":"ada@example.com"`),
			"feedback", "", "please let the rail wrap",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, true), ""},
		{"keyed feedback without email",
			feedback(feedbackBase + `,"$survey_response_019f99f1-2345-7bcd-9efa-123456789abc":"Dark mode, please."`),
			"feedback", "", "Dark mode, please.",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"bare feedback response wins over keyed",
			feedback(feedbackBase + `,"$survey_response":"the bare answer","$survey_response_019f99f1-2345-7bcd-9efa-123456789abc":"the keyed answer"`),
			"feedback", "", "the bare answer",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"empty bare feedback response falls back to keyed",
			feedback(feedbackBase + `,"$survey_response":"","$survey_response_019f99f1-2345-7bcd-9efa-123456789abc":"the keyed answer"`),
			"feedback", "", "the keyed answer",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"several keyed feedback responses use the first key",
			feedback(feedbackBase + `,"$survey_response_b":"second","$survey_response_a":"first"`),
			"feedback", "", "first",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"long feedback response is cut",
			feedback(feedbackBase + `,"$survey_response":"` + long + `"`),
			"feedback", "", strings.TrimRight(long, " ")[:499] + "…",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"feedback without an answer is still recorded",
			feedback(feedbackBase),
			"feedback", "", "",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"numeric feedback response reads as text",
			feedback(feedbackBase + `,"$survey_response":5`),
			"feedback", "", "5",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"feedback text naming @word and a URL makes no mention or link",
			feedback(feedbackBase + `,"$survey_response":"@ada see https://example.com/x for the wrap"`),
			"feedback", "", "@ada see https://example.com/x for the wrap",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"feedback event without a uuid",
			[]byte(`{"event":{"event":"survey sent","properties":{` + feedbackBase + `,"$survey_response":"no uuid"}}` + feedbackProject + `}`),
			"feedback", "", "no uuid",
			hooks.Thread{}, feedbackFields(true, false), ""},
		{"non-string feedback field is dropped",
			feedback(feedbackBase + `,"$survey_response":"hi","email":42`),
			"feedback", "", "hi",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(true, false), ""},
		{"feedback without a project",
			[]byte(`{"event":{"event":"survey sent","uuid":"` + feedbackUUID + `","properties":{` + feedbackBase + `,"$survey_response":"hi"}}}`),
			"feedback", "", "hi",
			hooks.Thread{ID: feedbackUUID}, feedbackFields(false, false), ""},
	} {
		got, err := ad.Normalize(nil, tc.body)
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if got.Source != "posthog" || got.Type != tc.wantType || got.Action != tc.wantAction ||
			got.Text != tc.wantText || got.Thread != tc.wantThread || got.URL != tc.wantURL ||
			!reflect.DeepEqual(map[string][]string(got.Fields), tc.wantFields) {
			t.Errorf("%s: got %+v, want type %q action %q text %q thread %+v url %q fields %v",
				tc.name, got, tc.wantType, tc.wantAction, tc.wantText, tc.wantThread, tc.wantURL, tc.wantFields)
		}
		if len(got.Mentions) != 0 {
			t.Errorf("%s: mentions = %q, want none", tc.name, got.Mentions)
		}
		if tc.wantType == "error_issue" && got.Entity != id {
			t.Errorf("%s: entity = %q, want the issue", tc.name, got.Entity)
		}
		if tc.wantType != "error_issue" && got.Entity != "" {
			t.Errorf("%s: entity = %q, want none", tc.name, got.Entity)
		}
	}
}

// The link in an envelope is built from the body's own project.url and
// issue id, which are a verified sender's data, not the yard's: only an
// https base and a UUID issue id, as PostHog sends them, make a link.
// Anything else is still an error_issue, with no link for a bead or an
// inbox to carry a reader somewhere the body chose.
func TestPostHogPackNormalizeLinks(t *testing.T) {
	ad := posthogDecl(t)
	const id = "019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e"
	body := func(distinct, projectURL string) []byte {
		return []byte(`{"event":{"event":"$error_tracking_issue_created","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192","distinct_id":"` + distinct +
			`","properties":{"name":"boom","description":"it went boom"}},"project":{"id":48291,"url":"` + projectURL + `"}}`)
	}
	for _, tc := range []struct {
		name       string
		body       []byte
		wantURL    string
		wantThread string // issue id the thread still carries
	}{
		{"an https project and a UUID issue",
			body(id, "https://us.posthog.com/project/48291"),
			"https://us.posthog.com/project/48291/error-tracking/" + id, id},
		{"a script scheme in the project url",
			body(id, "javascript:alert(1)//evil"),
			"", id},
		{"a plain http project url",
			body(id, "http://us.posthog.com/project/48291"),
			"", id},
		{"an issue id that leaves its segment",
			body("abc/../../x?y=1#frag", "https://us.posthog.com/project/48291"),
			"", "abc/../../x?y=1#frag"},
	} {
		got, err := ad.Normalize(nil, tc.body)
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if got.Type != "error_issue" || got.URL != tc.wantURL || got.Thread.URL != tc.wantURL || got.Thread.ID != tc.wantThread {
			t.Errorf("%s: got type %q url %q thread %+v", tc.name, got.Type, got.URL, got.Thread)
		}
	}
}

// The replay link is built from the same body's project.url and session
// id, under the same guard as the issue link: only an https base and a
// UUID session, as PostHog sends them, make a link. Anything else is
// still an error_issue, heard as the session, with no link for a bead or
// an inbox to carry a reader somewhere the body chose.
func TestPostHogPackNormalizeReplay(t *testing.T) {
	ad := posthogDecl(t)
	const session = "019f7c3b-4d5e-7a1b-8c2d-9e0f1a2b3c4d"
	body := func(sess, projectURL string) []byte {
		return []byte(`{"event":{"event":"$error_tracking_issue_created","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192","distinct_id":"019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e","properties":{"name":"boom","description":"it went boom","exception_props":{"$session_id":"` + sess + `"}}},"project":{"id":48291,"url":"` + projectURL + `"}}`)
	}
	for _, tc := range []struct {
		name        string
		body        []byte
		wantReplay  string
		wantSession string // the session the fields still carry
	}{
		{"an https project and a UUID session",
			body(session, "https://us.posthog.com/project/48291"),
			"https://us.posthog.com/project/48291/replay/" + session, session},
		{"a script scheme in the project url",
			body(session, "javascript:alert(1)//evil"),
			"", session},
		{"a plain http project url",
			body(session, "http://us.posthog.com/project/48291"),
			"", session},
		{"a session id that leaves its segment",
			body("abc/../../x?y=1#frag", "https://us.posthog.com/project/48291"),
			"", "abc/../../x?y=1#frag"},
	} {
		got, err := ad.Normalize(nil, tc.body)
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		var replay, sess string
		if len(got.Fields["replay"]) > 0 {
			replay = got.Fields["replay"][0]
		}
		if len(got.Fields["session"]) > 0 {
			sess = got.Fields["session"][0]
		}
		if got.Type != "error_issue" || replay != tc.wantReplay || sess != tc.wantSession {
			t.Errorf("%s: got type %q replay %q session %q", tc.name, got.Type, replay, sess)
		}
	}
}

// A normalize that exits non-zero is answered 400, which the service
// retries. So the script answers an envelope for every object body and
// never prints any of it: on success it prints nothing to stderr at all,
// and its one error names no part of the body.
func TestPostHogPackNormalizeNeverPrintsBody(t *testing.T) {
	ad := posthogDecl(t)
	home := ad.Home
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
	const marker = "MARKER-zz07-never-printed"
	bodies := map[string][]byte{
		"recorded":                fixture(t, "posthog_issue_created.json"),
		"recorded with session":   fixture(t, "posthog_issue_created_with_session.json"),
		"recorded feedback":       fixture(t, "posthog_survey_sent.json"),
		"recorded keyed feedback": fixture(t, "posthog_survey_sent_keyed.json"),
	}
	bodies["free text"] = []byte(`{"event":{"event":"$error_tracking_issue_created","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192","properties":{"name":"` + marker + `","description":"` + marker + `"}},"project":{"id":1,"url":"https://us.posthog.com/project/1"}}`)
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
	// What is no object is refused with one fixed line, holding none of it.
	stdout, stderr, code := run([]byte(`"` + marker + `"`))
	if code == 0 {
		t.Fatalf("a body that is no object exited 0: %q", stdout)
	}
	if !strings.Contains(stderr, "posthog webhook body is not an object") {
		t.Errorf("stderr = %q, want the one fixed line", stderr)
	}
	if strings.Contains(stderr, marker) {
		t.Errorf("stderr carries the body's text: %q", stderr)
	}
}

// The hook is verified by the secret in its header, compared as it
// arrives, and a repeated delivery is told by the event's own id: a retry
// shares the first attempt's uuid however it re-serializes the body.
func TestPostHogPackVerify(t *testing.T) {
	d, _, _ := posthogParse(t)
	ad := hooks.Declared{D: d}
	body := fixture(t, "posthog_issue_created.json")
	verify := func(body []byte, header string) (string, error) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/posthog", bytes.NewReader(body))
		if header != "" {
			req.Header.Set("X-Yardr-Secret", header)
		}
		return ad.Verify(req, body, []byte("s3cret"), time.Now())
	}
	if id, err := verify(body, "s3cret"); err != nil || id != "019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192" {
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
	// The same delivery re-serialized: other bytes, the same id.
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
	if id, err := verify(reserialized, "s3cret"); err != nil || id != "019f7c3a-1b2d-7e4f-9a1c-3d5e6f708192" {
		t.Fatalf("re-serialized retry: id %q, err %v", id, err)
	}
	if id, err := verify([]byte(`{"event":{"event":"$error_tracking_issue_created"}}`), "s3cret"); err != nil || id != "" {
		t.Fatalf("a delivery with no id: id %q, err %v", id, err)
	}
	if ad.Bodies() != hooks.BodiesUniquePerSource {
		t.Errorf("bodies = %v, want unique per source", ad.Bodies())
	}
}

// posthogDepot is a depot whose receiver serves the pack's declared source
// with hook posthog: the delivery path as the yard runs it.
func posthogDepot(t *testing.T) *depot {
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
	d, dir, text := posthogParse(t)
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	if _, err := r.st.PutSource(ctx, "posthog.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(home, "posthog.secret")
	if err := os.WriteFile(secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.PutHook(ctx, store.Hook{Name: "posthog", Source: "posthog", SecretFile: secretFile}, "me"); err != nil {
		t.Fatal(err)
	}
	return r
}

// posthogPost is one body delivered to the posthog hook, signed unless
// the header says otherwise: its status and its answer.
func posthogPost(t *testing.T, r *depot, body []byte, header string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.srv.URL+"/hooks/posthog", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if header != "" {
		req.Header.Set("X-Yardr-Secret", header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A signed issue-created delivery is recorded and routed to a
// subscription on its type; its retry arrives once; an unsigned one is
// refused, and recorded nowhere. A signed `survey sent` delivery is
// recorded as feedback and routed to a subscription on its type, with
// the answer and the survey in the event.
func TestPostHogPackEndToEnd(t *testing.T) {
	r := posthogDepot(t)
	ctx := context.Background()
	if _, err := r.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "posthog",
		Scope: store.ScopeAll, Where: map[string]string{"type": "error_issue"}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := r.st.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "posthog",
		Scope: store.ScopeAll, Where: map[string]string{"type": "feedback"}}, "yardmaster"); err != nil {
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
	body := fixture(t, "posthog_issue_created.json")
	// The signed delivery is recorded and in the inbox.
	if code, ans := posthogPost(t, r, body, secret); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the signed delivery answered %d %s", code, ans)
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the delivery: %d hook events, inbox %d", events(), inbox())
	}
	// A retry of it, re-serialized, arrives once: the uuid dedupes it.
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
	if code, ans := posthogPost(t, r, reserialized, secret); code != http.StatusOK || !strings.Contains(ans, `"duplicate"`) {
		t.Fatalf("the retry answered %d %s", code, ans)
	}
	if events() != 1 {
		t.Fatalf("the retry recorded: %d hook events", events())
	}
	// Without the header, and with a wrong secret, the same body is
	// refused, and recorded nowhere.
	for _, tc := range []struct{ name, header string }{{"unsigned", ""}, {"wrong secret", "nope"}} {
		if code, _ := posthogPost(t, r, body, tc.header); code != http.StatusUnauthorized {
			t.Fatalf("%s: answered %d, want 401", tc.name, code)
		}
	}
	if events() != 1 || inbox() != 1 {
		t.Fatalf("after the refusals: %d hook events, inbox %d", events(), inbox())
	}
	if got := r.refused(t, "unauthorized"); got != 2 {
		t.Fatalf("unauthorized count %d, want 2", got)
	}
	// Another event is recorded, and nobody's inbox: the setup hears
	// type error_issue and type feedback alone.
	other := []byte(`{"event":{"event":"$error_tracking_issue_reopened","uuid":"019f7c3a-1b2d-7e4f-9a1c-3d5e6f708193","distinct_id":"019f7c39-8a4b-7c2d-b3e5-1f2a3b4c5d6e","properties":{"name":"boom"}},"project":{"id":48291,"url":"https://us.posthog.com/project/48291"}}`)
	if code, ans := posthogPost(t, r, other, secret); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("another event answered %d %s", code, ans)
	}
	if events() != 2 || inbox() != 1 {
		t.Fatalf("after another event: %d hook events, inbox %d", events(), inbox())
	}
	// A signed survey sent delivery is recorded as feedback and routed
	// to the feedback subscription, with the answer and the survey.
	feedback := fixture(t, "posthog_survey_sent.json")
	if code, ans := posthogPost(t, r, feedback, secret); code != http.StatusOK || !strings.Contains(ans, `"seq"`) {
		t.Fatalf("the feedback delivery answered %d %s", code, ans)
	}
	if events() != 3 || inbox() != 2 {
		t.Fatalf("after the feedback delivery: %d hook events, inbox %d", events(), inbox())
	}
	var found bool
	for _, e := range r.hookEvents(t) {
		if e.Data["type"] != "feedback" {
			continue
		}
		found = true
		fields, _ := e.Data["fields"].(map[string]any)
		if e.Data["text"] != "The rail hides my longest depot name; please let it wrap." ||
			fmt.Sprint(fields["survey"]) != "[019f99f0-1234-7abc-8def-0123456789ab]" ||
			fmt.Sprint(fields["email"]) != "[ada@example.com]" {
			t.Fatalf("feedback event: %+v", e.Data)
		}
	}
	if !found {
		t.Fatal("no feedback event was recorded")
	}
}
