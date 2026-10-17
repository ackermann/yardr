package packs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/packs"
)

// The google packs are four declared sources, one per host, read here
// from the embedded packs, as the yard reads them: Gmail, Drive, and
// Docs with Slides. No test calls Google.
func TestGooglePacksDeclareTheirSources(t *testing.T) {
	for _, tc := range []struct {
		pack string
		want []string
	}{
		{"google-mail", []string{"google-mail"}},
		{"google-drive", []string{"google-drive"}},
		{"google-docs", []string{"google-docs", "google-slides"}},
	} {
		t.Run(tc.pack, func(t *testing.T) {
			sub, err := fs.Sub(packs.FS, tc.pack)
			if err != nil {
				t.Fatal(err)
			}
			p, err := pack.Load(sub, packs.Embedded)
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != "source" {
				t.Fatalf("kind %q, want source", p.Kind)
			}
			var names []string
			for _, d := range p.Declared {
				names = append(names, d.Name)
			}
			if !reflect.DeepEqual(names, tc.want) {
				t.Fatalf("sources = %v, want %v", names, tc.want)
			}
		})
	}
}

// googleDeclaration is the embedded declaration file, parsed, with its
// base pointed at the stand-in. The base line is replaced whole: the
// drive file cites its discovery document, on the same host, in a
// comment above it. The rewrite is asserted: a declaration still
// pointing at Google would call it.
func googleDeclaration(t *testing.T, file, base, standin string) *source.Declaration {
	t.Helper()
	raw, err := fs.ReadFile(packs.FS, googlePackOf(file)+"/"+file)
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(file, []byte(strings.Replace(string(raw), "base: "+base, "base: "+standin, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != standin {
		t.Fatalf("%s: base %s, want the stand-in", file, d.Base)
	}
	return d
}

// googlePackOf is the embedded pack a Google declaration file ships
// in: mail and drive each alone, docs with slides.
func googlePackOf(file string) string {
	switch file {
	case "google-mail.yaml":
		return "google-mail"
	case "google-drive.yaml":
		return "google-drive"
	default:
		return "google-docs"
	}
}

// Each declaration mints from GOOGLE_ACCOUNT, names no idempotency key
// (no Google API below takes one), and has its calls with their kinds,
// as yardr source show lists them. Only drive names a check call: every
// other read needs a parameter.
func TestGoogleDeclarations(t *testing.T) {
	bases := map[string]string{
		"google-mail.yaml":   "https://gmail.googleapis.com",
		"google-drive.yaml":  "https://www.googleapis.com",
		"google-docs.yaml":   "https://docs.googleapis.com",
		"google-slides.yaml": "https://slides.googleapis.com",
	}
	calls := map[string][]source.Call{
		"google-mail.yaml": {
			{Name: "search", Kind: source.Read}, {Name: "message", Kind: source.Read},
			{Name: "thread", Kind: source.Read}, {Name: "draft", Kind: source.Update},
		},
		"google-drive.yaml": {
			{Name: "files", Kind: source.Read}, {Name: "file", Kind: source.Read},
			{Name: "folder", Kind: source.Update}, {Name: "doc", Kind: source.Update},
			{Name: "slides", Kind: source.Update}, {Name: "share", Kind: source.Update},
		},
		"google-docs.yaml": {
			{Name: "doc", Kind: source.Read}, {Name: "write", Kind: source.Update},
		},
		"google-slides.yaml": {
			{Name: "deck", Kind: source.Read}, {Name: "slide", Kind: source.Update},
		},
	}
	for file, base := range bases {
		raw, err := fs.ReadFile(packs.FS, googlePackOf(file)+"/"+file)
		if err != nil {
			t.Fatal(err)
		}
		d, err := source.Parse(file, raw)
		if err != nil {
			t.Fatal(err)
		}
		if d.Base != base {
			t.Errorf("%s: base %s, want %s", file, d.Base, base)
		}
		if d.Credential == nil || d.Credential.Env != "GOOGLE_TOKEN" ||
			d.Credential.Header != "Authorization: Bearer {credential}" ||
			!reflect.DeepEqual(d.Credential.Mint, []string{"GOOGLE_ACCOUNT"}) {
			t.Errorf("%s: credential = %+v", file, d.Credential)
		}
		if d.Idempotency != "" {
			t.Errorf("%s: idempotency %q: no call below takes a key", file, d.Idempotency)
		}
		var got []source.Call
		for _, c := range d.Calls {
			got = append(got, source.Call{Name: c.Name, Kind: c.Kind})
		}
		if !reflect.DeepEqual(got, calls[file]) {
			t.Errorf("%s: calls = %+v", file, got)
		}
		check := ""
		if file == "google-drive.yaml" {
			check = "files"
		}
		if d.Check != check {
			t.Errorf("%s: check %q, want %q", file, d.Check, check)
		}
	}
}

// googleStandin answers as Google's APIs do for the calls' recorded
// shapes, and keeps the last request it was sent.
type googleStandin struct {
	t    *testing.T
	mu   sync.Mutex
	last googleRequest
	// answered are the recorded answers by "METHOD /path". vary answers
	// from the request instead: a search's page from its q, a created
	// file echoing the name and type it was made with.
	answered map[string]string
	vary     map[string]func(url.Values, []byte) string
}

// googleRequest is the last request the stand-in was sent.
type googleRequest struct {
	method, path string
	query        url.Values
	body         []byte
}

func (s *googleStandin) handler(w http.ResponseWriter, r *http.Request) {
	if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
		s.t.Errorf("%s %s: Authorization %q, want a Bearer token", r.Method, r.URL.Path, auth)
	}
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.last = googleRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), body: body}
	s.mu.Unlock()
	if f, ok := s.vary[key]; ok {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f(r.URL.Query(), body))
		return
	}
	out, ok := s.answered[key]
	if !ok {
		s.t.Errorf("the stand-in has no answer for %s", key)
		http.Error(w, "no such recorded shape", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, out)
}

func (s *googleStandin) request() googleRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Every call of the four declarations, against the stand-in: the request
// it sends (method, path, query, body) and its select on a recorded
// answer. Keys the select drops are in the recordings, so the test
// would fail were they kept.
func TestGoogleCallsAgainstTheStandin(t *testing.T) {
	msgID := "18c4a1b2c3d4e5f6"
	driveID := "1AbC2dE3fH4jK5lM6nO7pQ8rStUv"
	docID := "1DoCxYz0123456789abcdefghijklmnop"
	deckID := "1DeCk0123456789abcdefghijklmnopqr"
	searchPage1 := `{"messages":[{"id":"18c4a1b2c3d4e5f6","threadId":"18c4a1b2c3d4e5f6"},{"id":"18c4a1b2c3d4e5f7","threadId":"18c4a1b2c3d4e5f6"}],"nextPageToken":"CAEQDBgBEg9OZXh0IFBhZ2U","resultSizeEstimate":2}`
	searchPage2 := `{"messages":[{"id":"18c4a1b2c3d4e5f8","threadId":"18c4a1b2c3d4e5f8"}],"resultSizeEstimate":3}`
	standin := &googleStandin{t: t, answered: map[string]string{
		"GET /gmail/v1/users/me/messages/" + msgID:          `{"id":"` + msgID + `","threadId":"` + msgID + `","labelIds":["INBOX","UNREAD"],"snippet":"Lunch on Friday?","historyId":"987654","internalDate":"1760620800000","sizeEstimate":1234,"payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"ada@example.com"},{"name":"Subject","value":"Lunch"}]}}`,
		"GET /gmail/v1/users/me/threads/" + msgID:           `{"id":"` + msgID + `","historyId":"987655","snippet":"Lunch on Friday?","messages":[{"id":"` + msgID + `","threadId":"` + msgID + `","snippet":"Lunch on Friday?"}]}`,
		"POST /gmail/v1/users/me/drafts":                    `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`,
		"GET /drive/v3/files/" + driveID:                    `{"kind":"drive#file","id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["1PaReNt0123456789abcdefghijklm"],"owners":[{"displayName":"Ada"}],"modifiedTime":"2026-10-16T15:00:00.000Z","size":"1024","trashed":false,"webViewLink":"https://docs.google.com/document/d/` + driveID + `","version":"9"}`,
		"GET /v1/documents/" + docID:                        `{"documentId":"` + docID + `","title":"lunch","revisionId":"9","documentStyle":{"pageSize":{"width":{"magnitude":612}}},"body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Lunch places"}}]},"startIndex":1,"endIndex":13}]}}`,
		"POST /v1/documents/" + docID + ":batchUpdate":      `{"documentId":"` + docID + `","replies":[{},{"updateParagraphStyle":{"objectId":"kix.1"}}],"writeControl":{"requiredRevisionId":"9"}}`,
		"GET /v1/presentations/" + deckID:                   `{"presentationId":"` + deckID + `","title":"friday","pageSize":{"width":{"magnitude":9144000}},"slides":[{"objectId":"friday_1"}]}`,
		"POST /v1/presentations/" + deckID + ":batchUpdate": `{"presentationId":"` + deckID + `","replies":[{"createSlide":{"objectId":"friday_1"}},{},{}],"writeControl":{"requiredRevisionId":"9"}}`,
	}, vary: map[string]func(url.Values, []byte) string{
		"GET /gmail/v1/users/me/messages": func(q url.Values, _ []byte) string {
			if q.Get("q") == "last" {
				return searchPage2
			}
			return searchPage1
		},
		"GET /drive/v3/files": func(q url.Values, _ []byte) string {
			if q.Get("q") == "" {
				return `{"kind":"drive#fileList","incompleteSearch":false,"files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`
			}
			return `{"kind":"drive#fileList","incompleteSearch":false,"nextPageToken":"next","files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`
		},
		"POST /drive/v3/files": func(_ url.Values, body []byte) string {
			var made struct {
				Name, MimeType string
				Parents        []string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			out, _ := json.Marshal(map[string]any{"kind": "drive#file", "id": "1NeWFiLe0123456789abcdefghijklm", "name": made.Name, "mimeType": made.MimeType, "parents": made.Parents})
			return string(out)
		},
		"POST /drive/v3/files/" + driveID + "/permissions": func(_ url.Values, body []byte) string {
			var made struct {
				Type, Role, EmailAddress string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			out, _ := json.Marshal(map[string]any{"kind": "drive#permission", "id": "00362519876543210987", "type": made.Type, "role": made.Role, "emailAddress": made.EmailAddress})
			return string(out)
		},
	}}
	srv := httptest.NewServer(http.HandlerFunc(standin.handler))
	defer srv.Close()
	mail := googleDeclaration(t, "google-mail.yaml", "https://gmail.googleapis.com", srv.URL)
	drive := googleDeclaration(t, "google-drive.yaml", "https://www.googleapis.com", srv.URL)
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	slides := googleDeclaration(t, "google-slides.yaml", "https://slides.googleapis.com", srv.URL)
	parent := "1PaReNt0123456789abcdefghijklm"
	for _, tc := range []struct {
		name       string
		d          *source.Declaration
		call       string
		params     map[string]string
		method     string
		path       string
		query      map[string]string
		body       string
		wantAnswer string
	}{
		{name: "search", d: mail, call: "search", params: map[string]string{"q": "from:me", "maxResults": "5"},
			method: "GET", path: "/gmail/v1/users/me/messages", query: map[string]string{"q": "from:me", "maxResults": "5"}, wantAnswer: searchPage1},
		{name: "search without q", d: mail, call: "search", params: map[string]string{"maxResults": "50"},
			method: "GET", path: "/gmail/v1/users/me/messages", query: map[string]string{"maxResults": "50"}, wantAnswer: searchPage1},
		{name: "search last page", d: mail, call: "search", params: map[string]string{"q": "last", "maxResults": "5"},
			method: "GET", path: "/gmail/v1/users/me/messages", query: map[string]string{"q": "last", "maxResults": "5"}, wantAnswer: searchPage2},
		{name: "message", d: mail, call: "message", params: map[string]string{"id": msgID, "format": "metadata"},
			method: "GET", path: "/gmail/v1/users/me/messages/" + msgID, query: map[string]string{"format": "metadata"},
			wantAnswer: `{"id":"` + msgID + `","labelIds":["INBOX","UNREAD"],"payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"ada@example.com"},{"name":"Subject","value":"Lunch"}]},"snippet":"Lunch on Friday?","threadId":"` + msgID + `"}`},
		{name: "thread", d: mail, call: "thread", params: map[string]string{"id": msgID, "format": "full"},
			method: "GET", path: "/gmail/v1/users/me/threads/" + msgID, query: map[string]string{"format": "full"},
			wantAnswer: `{"id":"` + msgID + `","messages":[{"id":"` + msgID + `","threadId":"` + msgID + `","snippet":"Lunch on Friday?"}]}`},
		{name: "draft padded", d: mail, call: "draft", params: map[string]string{"raw": "RnJvbTptZQ=="},
			method: "POST", path: "/gmail/v1/users/me/drafts", body: `{"message":{"raw":"RnJvbTptZQ=="}}`,
			wantAnswer: `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`},
		{name: "draft unpadded", d: mail, call: "draft", params: map[string]string{"raw": "RnJvbTptZQ"},
			method: "POST", path: "/gmail/v1/users/me/drafts", body: `{"message":{"raw":"RnJvbTptZQ"}}`,
			wantAnswer: `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`},
		{name: "files", d: drive, call: "files", params: nil,
			method: "GET", path: "/drive/v3/files",
			wantAnswer: `{"incompleteSearch":false,"files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`},
		{name: "files with q", d: drive, call: "files", params: map[string]string{"q": "name contains 'x'"},
			method: "GET", path: "/drive/v3/files", query: map[string]string{"q": "name contains 'x'"},
			wantAnswer: `{"incompleteSearch":false,"nextPageToken":"next","files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`},
		{name: "file", d: drive, call: "file", params: map[string]string{"id": driveID},
			method: "GET", path: "/drive/v3/files/" + driveID,
			wantAnswer: `{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["1PaReNt0123456789abcdefghijklm"],"modifiedTime":"2026-10-16T15:00:00.000Z","size":"1024","trashed":false}`},
		{name: "folder", d: drive, call: "folder", params: map[string]string{"name": "notes", "parent": parent},
			method: "POST", path: "/drive/v3/files", body: `{"name":"notes","mimeType":"application/vnd.google-apps.folder","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.folder","name":"notes","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "drive doc", d: drive, call: "doc", params: map[string]string{"name": "lunch", "parent": parent},
			method: "POST", path: "/drive/v3/files", body: `{"name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.document","name":"lunch","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "drive slides", d: drive, call: "slides", params: map[string]string{"name": "friday", "parent": parent},
			method: "POST", path: "/drive/v3/files", body: `{"name":"friday","mimeType":"application/vnd.google-apps.presentation","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.presentation","name":"friday","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "share quiet by default", d: drive, call: "share", params: map[string]string{"fileId": driveID, "email": "bob@example.com", "role": "reader"},
			method: "POST", path: "/drive/v3/files/" + driveID + "/permissions", query: map[string]string{"sendNotificationEmail": "false"},
			body:       `{"type":"user","role":"reader","emailAddress":"bob@example.com"}`,
			wantAnswer: `{"id":"00362519876543210987","type":"user","role":"reader"}`},
		{name: "share notified commenter", d: drive, call: "share", params: map[string]string{"fileId": driveID, "email": "ada@example.com", "role": "commenter", "notify": "true"},
			method: "POST", path: "/drive/v3/files/" + driveID + "/permissions", query: map[string]string{"sendNotificationEmail": "true"},
			body:       `{"type":"user","role":"commenter","emailAddress":"ada@example.com"}`,
			wantAnswer: `{"id":"00362519876543210987","type":"user","role":"commenter"}`},
		{name: "docs doc", d: docs, call: "doc", params: map[string]string{"docId": docID},
			method: "GET", path: "/v1/documents/" + docID,
			wantAnswer: `{"body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Lunch places"}}]},"startIndex":1,"endIndex":13}]},"title":"lunch"}`},
		{name: "write", d: docs, call: "write", params: map[string]string{"docId": docID, "text": "Lunch places", "style": "HEADING_1", "start": "1", "end": "2"},
			method: "POST", path: "/v1/documents/" + docID + ":batchUpdate",
			body:       `{"requests":[{"insertText":{"endOfSegmentLocation":{},"text":"Lunch places"}},{"updateParagraphStyle":{"range":{"startIndex":1,"endIndex":2},"paragraphStyle":{"namedStyleType":"HEADING_1"},"fields":"namedStyleType"}}]}`,
			wantAnswer: `{"documentId":"` + docID + `","replies":[{},{"updateParagraphStyle":{"objectId":"kix.1"}}]}`},
		{name: "deck", d: slides, call: "deck", params: map[string]string{"id": deckID},
			method: "GET", path: "/v1/presentations/" + deckID,
			wantAnswer: `{"slides":[{"objectId":"friday_1"}],"title":"friday"}`},
		{name: "slide", d: slides, call: "slide", params: map[string]string{"id": deckID, "slide": "friday_1", "title": "Friday", "body": "We shipped."},
			method: "POST", path: "/v1/presentations/" + deckID + ":batchUpdate",
			body:       `{"requests":[{"createSlide":{"objectId":"friday_1","slideLayoutReference":{"predefinedLayout":"TITLE_AND_BODY"},"placeholderIdMappings":[{"layoutPlaceholder":{"type":"TITLE","index":0},"objectId":"friday_1_title"},{"layoutPlaceholder":{"type":"BODY","index":0},"objectId":"friday_1_body"}]}},{"insertText":{"objectId":"friday_1_title","text":"Friday"}},{"insertText":{"objectId":"friday_1_body","text":"We shipped."}}]}`,
			wantAnswer: `{"presentationId":"` + deckID + `","replies":[{"createSlide":{"objectId":"friday_1"}},{},{}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := source.Do(context.Background(), tc.d, tc.call, tc.params, "tok")
			if err != nil {
				t.Fatal(err)
			}
			gotReq := standin.request()
			if gotReq.method != tc.method || gotReq.path != tc.path {
				t.Fatalf("request = %s %s, want %s %s", gotReq.method, gotReq.path, tc.method, tc.path)
			}
			if len(gotReq.query) != len(tc.query) {
				t.Fatalf("query = %v, want %v", gotReq.query, tc.query)
			}
			for k, want := range tc.query {
				if got := gotReq.query.Get(k); got != want {
					t.Fatalf("query = %v, want %v", gotReq.query, tc.query)
				}
			}
			if tc.body == "" {
				if len(gotReq.body) != 0 {
					t.Fatalf("body = %s, want none", gotReq.body)
				}
			} else {
				var got, want any
				if err := json.Unmarshal(gotReq.body, &got); err != nil {
					t.Fatalf("body = %s: %v", gotReq.body, err)
				}
				if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("body = %s, want %s", gotReq.body, tc.body)
				}
			}
			var gotAnswer, wantAnswer any
			if err := json.Unmarshal(res.Answer, &gotAnswer); err != nil {
				t.Fatalf("answer = %s: %v", res.Answer, err)
			}
			if err := json.Unmarshal([]byte(tc.wantAnswer), &wantAnswer); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotAnswer, wantAnswer) {
				t.Fatalf("answer = %s, want %s", res.Answer, tc.wantAnswer)
			}
		})
	}
}

// A parameter outside what a call allows is refused before anything is
// sent: the stand-in hears nothing of these.
func TestGoogleCallParamsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the stand-in was sent %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	mail := googleDeclaration(t, "google-mail.yaml", "https://gmail.googleapis.com", srv.URL)
	drive := googleDeclaration(t, "google-drive.yaml", "https://www.googleapis.com", srv.URL)
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	slides := googleDeclaration(t, "google-slides.yaml", "https://slides.googleapis.com", srv.URL)
	for _, tc := range []struct {
		name   string
		d      *source.Declaration
		call   string
		params map[string]string
		want   string
	}{
		{"search without maxResults", mail, "search", map[string]string{"q": "x"}, "parameter maxResults is missing"},
		{"search 51", mail, "search", map[string]string{"maxResults": "51"}, "parameter maxResults does not match its pattern"},
		{"search 0", mail, "search", map[string]string{"maxResults": "0"}, "parameter maxResults does not match its pattern"},
		{"search long q", mail, "search", map[string]string{"q": strings.Repeat("x", 1001), "maxResults": "5"}, "parameter q is 1001 characters, and at most 1000"},
		{"search unknown", mail, "search", map[string]string{"maxResults": "5", "page": "2"}, `"page" is no parameter`},
		{"message uppercase id", mail, "message", map[string]string{"id": "ABCDEF", "format": "full"}, "parameter id does not match its pattern"},
		{"message raw", mail, "message", map[string]string{"id": "abcdef", "format": "raw"}, "parameter format is one of metadata, full"},
		{"message without format", mail, "message", map[string]string{"id": "abcdef"}, "parameter format is missing"},
		{"thread empty id", mail, "thread", map[string]string{"id": "", "format": "full"}, "parameter id does not match its pattern"},
		{"draft impossible length", mail, "draft", map[string]string{"raw": "abcde"}, "parameter raw does not match its pattern"},
		{"draft invalid padding", mail, "draft", map[string]string{"raw": "abc=="}, "parameter raw does not match its pattern"},
		{"draft spaced", mail, "draft", map[string]string{"raw": "ab c"}, "parameter raw does not match its pattern"},
		{"draft long", mail, "draft", map[string]string{"raw": strings.Repeat("a", 1000001)}, "parameter raw is 1000001 characters, and at most 1000000"},
		{"files long q", drive, "files", map[string]string{"q": strings.Repeat("x", 1001)}, "parameter q is 1001 characters, and at most 1000"},
		{"file slashed id", drive, "file", map[string]string{"id": "a/b"}, "parameter id does not match its pattern"},
		{"folder long name", drive, "folder", map[string]string{"name": strings.Repeat("x", 201), "parent": "abc"}, "parameter name is 201 characters, and at most 200"},
		{"share slashed fileId", drive, "share", map[string]string{"fileId": "a/b", "email": "bob@example.com", "role": "reader", "notify": "false"}, "parameter fileId does not match its pattern"},
		{"share bare email", drive, "share", map[string]string{"fileId": "abc", "email": "bob", "role": "reader", "notify": "false"}, "parameter email does not match its pattern"},
		{"share spaced email", drive, "share", map[string]string{"fileId": "abc", "email": "bob @example.com", "role": "reader", "notify": "false"}, "parameter email does not match its pattern"},
		{"share writer", drive, "share", map[string]string{"fileId": "abc", "email": "bob@example.com", "role": "writer", "notify": "false"}, "parameter role is one of reader, commenter"},
		{"share notify yes", drive, "share", map[string]string{"fileId": "abc", "email": "bob@example.com", "role": "reader", "notify": "yes"}, "parameter notify is true or false"},
		{"doc dots", docs, "doc", map[string]string{"docId": ".."}, "parameter docId does not match its pattern"},
		{"write heading 3", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "HEADING_3", "start": "1", "end": "2"}, "parameter style is one of NORMAL_TEXT, TITLE, HEADING_1, HEADING_2"},
		{"write fractional start", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "TITLE", "start": "1.5", "end": "2"}, "parameter start is no whole number"},
		{"write without end", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "TITLE", "start": "1"}, "parameter end is missing"},
		{"slide short id", slides, "slide", map[string]string{"id": "abc", "slide": "ab", "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
		{"slide dashed id", slides, "slide", map[string]string{"id": "abc", "slide": "-abcde", "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
		{"slide long id", slides, "slide", map[string]string{"id": "abc", "slide": strings.Repeat("a", 45), "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failed *source.CallError
			if _, err := source.Do(context.Background(), tc.d, tc.call, tc.params, "tok"); !errors.As(err, &failed) || failed.Reason != source.BadParam {
				t.Fatalf("err = %v, want %s", err, source.BadParam)
			} else if !strings.Contains(failed.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", failed, tc.want)
			}
		})
	}
}
