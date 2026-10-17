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

// The google packs are five declared sources, one per host, read here
// from the embedded packs, as the yard reads them: Gmail, Drive,
// Calendar, and Docs with Slides. No test calls Google.
func TestGooglePacksDeclareTheirSources(t *testing.T) {
	for _, tc := range []struct {
		pack string
		want []string
	}{
		{"google-mail", []string{"google-mail"}},
		{"google-drive", []string{"google-drive"}},
		{"google-docs", []string{"google-docs", "google-slides"}},
		{"google-calendar", []string{"google-calendar"}},
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
// in: mail, drive and calendar each alone, docs with slides.
func googlePackOf(file string) string {
	switch file {
	case "google-mail.yaml":
		return "google-mail"
	case "google-drive.yaml":
		return "google-drive"
	case "google-calendar.yaml":
		return "google-calendar"
	default:
		return "google-docs"
	}
}

// Each declaration mints from GOOGLE_ACCOUNT, names no idempotency key
// (no Google API below takes one), and has its calls with their kinds,
// as yardr source show lists them. Drive and calendar name a check
// call each: every other read needs a parameter.
func TestGoogleDeclarations(t *testing.T) {
	bases := map[string]string{
		"google-mail.yaml":     "https://gmail.googleapis.com",
		"google-drive.yaml":    "https://www.googleapis.com",
		"google-docs.yaml":     "https://docs.googleapis.com",
		"google-slides.yaml":   "https://slides.googleapis.com",
		"google-calendar.yaml": "https://www.googleapis.com",
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
			{Name: "comments", Kind: source.Read}, {Name: "reply", Kind: source.Update},
		},
		"google-docs.yaml": {
			{Name: "doc", Kind: source.Read}, {Name: "write", Kind: source.Update},
		},
		"google-slides.yaml": {
			{Name: "deck", Kind: source.Read}, {Name: "slide", Kind: source.Update},
		},
		"google-calendar.yaml": {
			{Name: "events", Kind: source.Read}, {Name: "event", Kind: source.Read},
			{Name: "calendars", Kind: source.Read}, {Name: "create", Kind: source.Update},
			{Name: "edit", Kind: source.Update},
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
		} else if file == "google-calendar.yaml" {
			check = "calendars"
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

// Every call of the five declarations, against the stand-in: the request
// it sends (method, path, query, body) and its select on a recorded
// answer. Keys the select drops are in the recordings, so the test
// would fail were they kept.
func TestGoogleCallsAgainstTheStandin(t *testing.T) {
	msgID := "18c4a1b2c3d4e5f6"
	driveID := "1AbC2dE3fH4jK5lM6nO7pQ8rStUv"
	commentID := "cOmMeNt0123456789abcdef"
	shutID := "cOmMeNt0123456789abcdeg"
	madeReplyID := "rEpLy0123456789abcdef01"
	docID := "1DoCxYz0123456789abcdefghijklmnop"
	deckID := "1DeCk0123456789abcdefghijklmnopqr"
	eventID := "6coj8gpl6oo30e9i8gr3cba18"
	otherEventID := "7d1k9hqm7pp41f9j9hs4dcb29"
	madeEventID := "8e2l0irn0pq52g0k0it5edb30"
	eventsFields := "items(id,summary,start,end,location,attendees(email,responseStatus),htmlLink),nextPageToken"
	eventFields := "id,status,summary,description,location,start,end,attendees(email,responseStatus),htmlLink"
	calendarsFields := "items(id,summary,primary,accessRole),nextPageToken"
	writeFields := "id,summary,start,end,htmlLink"
	searchPage1 := `{"messages":[{"id":"18c4a1b2c3d4e5f6","threadId":"18c4a1b2c3d4e5f6"},{"id":"18c4a1b2c3d4e5f7","threadId":"18c4a1b2c3d4e5f6"}],"nextPageToken":"CAEQDBgBEg9OZXh0IFBhZ2U","resultSizeEstimate":2}`
	searchPage2 := `{"messages":[{"id":"18c4a1b2c3d4e5f8","threadId":"18c4a1b2c3d4e5f8"}],"resultSizeEstimate":3}`
	// Drive lists resolved and open comments alike, each marked by its
	// own resolved flag: one of each, the open one with a reply.
	openComment := `{"kind":"drive#comment","id":"` + commentID + `","createdTime":"2026-10-17T10:00:00.000Z","modifiedTime":"2026-10-17T11:00:00.000Z","author":{"kind":"drive#user","displayName":"Ada","me":false},"htmlContent":"What about <b>lunch</b>?","content":"What about lunch?","deleted":false,"resolved":false,"quotedFileContent":{"mimeType":"text/plain","value":"Lunch places"},"replies":[{"kind":"drive#reply","id":"rEpLy0123456789abcdef00","content":"Friday works.","author":{"displayName":"Bob"},"createdTime":"2026-10-17T10:30:00.000Z","deleted":false}]}`
	shutComment := `{"kind":"drive#comment","id":"` + shutID + `","createdTime":"2026-10-16T09:00:00.000Z","modifiedTime":"2026-10-16T09:30:00.000Z","author":{"kind":"drive#user","displayName":"Bob","me":false},"htmlContent":"Fixed.","content":"Fixed.","deleted":false,"resolved":true,"quotedFileContent":{"mimeType":"text/plain","value":"Lunch places"},"replies":[]}`
	// The list's field mask keeps these details. The event read also asks
	// for its description and status.
	listLunch := `{"id":"` + eventID + `","summary":"Lunch with Ada","htmlLink":"https://www.google.com/calendar/event?eid=` + eventID + `","location":"Cafe Roma","start":{"dateTime":"2026-10-24T12:00:00+02:00","timeZone":"Europe/Berlin"},"end":{"dateTime":"2026-10-24T13:00:00+02:00","timeZone":"Europe/Berlin"},"attendees":[{"email":"ada@example.com","responseStatus":"accepted"},{"email":"bob@example.com","responseStatus":"needsAction"}]}`
	listDentist := `{"id":"` + otherEventID + `","summary":"Dentist","htmlLink":"https://www.google.com/calendar/event?eid=` + otherEventID + `","start":{"dateTime":"2026-10-26T09:00:00+02:00"},"end":{"dateTime":"2026-10-26T09:30:00+02:00"}}`
	readLunch := `{"id":"` + eventID + `","status":"confirmed","htmlLink":"https://www.google.com/calendar/event?eid=` + eventID + `","summary":"Lunch with Ada","description":"Try the new place.","location":"Cafe Roma","start":{"dateTime":"2026-10-24T12:00:00+02:00","timeZone":"Europe/Berlin"},"end":{"dateTime":"2026-10-24T13:00:00+02:00","timeZone":"Europe/Berlin"},"attendees":[{"email":"ada@example.com","responseStatus":"accepted"},{"email":"bob@example.com","responseStatus":"needsAction"}]}`
	// The account's own calendar and a holidays calendar it only reads:
	// with only the four fields the calendar list asks for.
	ownCalendar := `{"id":"mckean@example.com","summary":"Christopher","accessRole":"owner","primary":true}`
	holidayCalendar := `{"id":"en.usa#holiday@group.v.calendar.google.com","summary":"Holidays in United States","accessRole":"reader"}`
	eventsPage := `{"nextPageToken":"CigKGDN1cGVyIExvbmdQYWdl","items":[` + listLunch + `,` + listDentist + `]}`
	otherCalendarPage := `{"items":[` + listDentist + `]}`
	calendarListPage := `{"nextPageToken":"CgljYWxlbmRhckxpc3Q","items":[` + ownCalendar + `,` + holidayCalendar + `]}`
	standin := &googleStandin{t: t, answered: map[string]string{
		"GET /gmail/v1/users/me/messages/" + msgID:             `{"id":"` + msgID + `","threadId":"` + msgID + `","labelIds":["INBOX","UNREAD"],"snippet":"Lunch on Friday?","historyId":"987654","internalDate":"1760620800000","sizeEstimate":1234,"payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"ada@example.com"},{"name":"Subject","value":"Lunch"}]}}`,
		"GET /gmail/v1/users/me/threads/" + msgID:              `{"id":"` + msgID + `","historyId":"987655","snippet":"Lunch on Friday?","messages":[{"id":"` + msgID + `","threadId":"` + msgID + `","snippet":"Lunch on Friday?"}]}`,
		"POST /gmail/v1/users/me/drafts":                       `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`,
		"GET /drive/v3/files/" + driveID:                       `{"kind":"drive#file","id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["1PaReNt0123456789abcdefghijklm"],"owners":[{"displayName":"Ada"}],"modifiedTime":"2026-10-16T15:00:00.000Z","size":"1024","trashed":false,"webViewLink":"https://docs.google.com/document/d/` + driveID + `","version":"9"}`,
		"GET /drive/v3/files/" + driveID + "/comments":         `{"kind":"drive#commentList","nextPageToken":"next","comments":[` + openComment + `,` + shutComment + `]}`,
		"GET /v1/documents/" + docID:                           `{"documentId":"` + docID + `","title":"lunch","revisionId":"9","documentStyle":{"pageSize":{"width":{"magnitude":612}}},"body":{"content":[{"paragraph":{"elements":[{"textRun":{"content":"Lunch places"}}]},"startIndex":1,"endIndex":13}]}}`,
		"POST /v1/documents/" + docID + ":batchUpdate":         `{"documentId":"` + docID + `","replies":[{},{"updateParagraphStyle":{"objectId":"kix.1"}}],"writeControl":{"requiredRevisionId":"9"}}`,
		"GET /v1/presentations/" + deckID:                      `{"presentationId":"` + deckID + `","title":"friday","pageSize":{"width":{"magnitude":9144000}},"slides":[{"objectId":"friday_1"}]}`,
		"POST /v1/presentations/" + deckID + ":batchUpdate":    `{"presentationId":"` + deckID + `","replies":[{"createSlide":{"objectId":"friday_1"}},{},{}],"writeControl":{"requiredRevisionId":"9"}}`,
		"GET /calendar/v3/calendars/primary/events":            eventsPage,
		"GET /calendar/v3/calendars/ada@example.com/events":    otherCalendarPage,
		"GET /calendar/v3/calendars/primary/events/" + eventID: readLunch,
		"GET /calendar/v3/users/me/calendarList":               calendarListPage,
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
		"POST /drive/v3/files/" + driveID + "/comments/" + commentID + "/replies": func(_ url.Values, body []byte) string {
			var made struct {
				Content string
				Action  string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			m := map[string]any{"kind": "drive#reply", "id": madeReplyID, "content": made.Content,
				"author": map[string]any{"displayName": "Ada"}, "createdTime": "2026-10-17T12:00:00.000Z",
				"htmlContent": made.Content, "deleted": false}
			if made.Action != "" {
				m["action"] = made.Action
			}
			out, _ := json.Marshal(m)
			return string(out)
		},
		"POST /calendar/v3/calendars/primary/events": func(_ url.Values, body []byte) string {
			var made struct {
				Summary     string
				Start       map[string]string
				End         map[string]string
				Description *string
				Location    *string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			m := map[string]any{"kind": "calendar#event", "etag": `"3414"`, "id": madeEventID,
				"status": "confirmed", "summary": made.Summary,
				"start": made.Start, "end": made.End,
				"htmlLink": "https://www.google.com/calendar/event?eid=" + madeEventID,
				"created":  "2026-10-17T12:00:00.000Z", "iCalUID": madeEventID + "@google.com"}
			if made.Description != nil {
				m["description"] = *made.Description
			}
			if made.Location != nil {
				m["location"] = *made.Location
			}
			out, _ := json.Marshal(m)
			return string(out)
		},
		"PATCH /calendar/v3/calendars/primary/events/" + eventID: func(_ url.Values, body []byte) string {
			// What the patch did not give stays as the lunch is: an
			// omitted start or end arrives as {}, which the patch
			// reads as no change there.
			var made struct {
				Summary     *string
				Start       map[string]string
				End         map[string]string
				Description *string
				Location    *string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			summary := "Lunch with Ada"
			if made.Summary != nil {
				summary = *made.Summary
			}
			start := map[string]string{"dateTime": "2026-10-24T12:00:00+02:00", "timeZone": "Europe/Berlin"}
			if len(made.Start) > 0 {
				start = made.Start
			}
			end := map[string]string{"dateTime": "2026-10-24T13:00:00+02:00", "timeZone": "Europe/Berlin"}
			if len(made.End) > 0 {
				end = made.End
			}
			m := map[string]any{"kind": "calendar#event", "etag": `"3415"`, "id": eventID,
				"status": "confirmed", "summary": summary,
				"start": start, "end": end,
				"htmlLink": "https://www.google.com/calendar/event?eid=" + eventID,
				"updated":  "2026-10-17T13:00:00.000Z", "iCalUID": eventID + "@google.com"}
			if made.Description != nil {
				m["description"] = *made.Description
			}
			if made.Location != nil {
				m["location"] = *made.Location
			}
			out, _ := json.Marshal(m)
			return string(out)
		},
	}}
	srv := httptest.NewServer(http.HandlerFunc(standin.handler))
	defer srv.Close()
	mail := googleDeclaration(t, "google-mail.yaml", "https://gmail.googleapis.com", srv.URL)
	drive := googleDeclaration(t, "google-drive.yaml", "https://www.googleapis.com", srv.URL)
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	slides := googleDeclaration(t, "google-slides.yaml", "https://slides.googleapis.com", srv.URL)
	cal := googleDeclaration(t, "google-calendar.yaml", "https://www.googleapis.com", srv.URL)
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
		{name: "comments", d: drive, call: "comments", params: map[string]string{"fileId": driveID},
			method: "GET", path: "/drive/v3/files/" + driveID + "/comments",
			query:      map[string]string{"fields": "comments(id,author,content,quotedFileContent,resolved,replies),nextPageToken"},
			wantAnswer: `{"nextPageToken":"next","comments":[` + openComment + `,` + shutComment + `]}`},
		{name: "reply", d: drive, call: "reply", params: map[string]string{"fileId": driveID, "commentId": commentID, "content": "Friday works."},
			method: "POST", path: "/drive/v3/files/" + driveID + "/comments/" + commentID + "/replies",
			body:       `{"content":"Friday works."}`,
			wantAnswer: `{"id":"` + madeReplyID + `","content":"Friday works."}`},
		{name: "reply resolving", d: drive, call: "reply", params: map[string]string{"fileId": driveID, "commentId": commentID, "content": "Fixed.", "action": "resolve"},
			method: "POST", path: "/drive/v3/files/" + driveID + "/comments/" + commentID + "/replies",
			body:       `{"content":"Fixed.","action":"resolve"}`,
			wantAnswer: `{"id":"` + madeReplyID + `","content":"Fixed.","action":"resolve"}`},
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
		{name: "events", d: cal, call: "events", params: map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10"},
			method: "GET", path: "/calendar/v3/calendars/primary/events",
			query:      map[string]string{"timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10", "singleEvents": "true", "orderBy": "startTime", "fields": eventsFields},
			wantAnswer: eventsPage},
		{name: "events with q", d: cal, call: "events", params: map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "q": "lunch", "maxResults": "10"},
			method: "GET", path: "/calendar/v3/calendars/primary/events",
			query:      map[string]string{"timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "q": "lunch", "maxResults": "10", "singleEvents": "true", "orderBy": "startTime", "fields": eventsFields},
			wantAnswer: eventsPage},
		{name: "events on another calendar", d: cal, call: "events", params: map[string]string{"calendarId": "ada@example.com", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "50"},
			method: "GET", path: "/calendar/v3/calendars/ada@example.com/events",
			query:      map[string]string{"timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "50", "singleEvents": "true", "orderBy": "startTime", "fields": eventsFields},
			wantAnswer: otherCalendarPage},
		{name: "event", d: cal, call: "event", params: map[string]string{"calendarId": "primary", "eventId": eventID},
			method: "GET", path: "/calendar/v3/calendars/primary/events/" + eventID,
			query:      map[string]string{"fields": eventFields},
			wantAnswer: readLunch},
		{name: "calendars", d: cal, call: "calendars", params: nil,
			method: "GET", path: "/calendar/v3/users/me/calendarList",
			query:      map[string]string{"fields": calendarsFields},
			wantAnswer: `{"nextPageToken":"CgljYWxlbmRhckxpc3Q","items":[` + ownCalendar + `,` + holidayCalendar + `]}`},
		{name: "create", d: cal, call: "create", params: map[string]string{"calendarId": "primary", "summary": "Lunch with Ada", "start": "2026-10-24T12:00:00+02:00", "end": "2026-10-24T13:00:00+02:00", "description": "Try the new place.", "location": "Cafe Roma"},
			method: "POST", path: "/calendar/v3/calendars/primary/events", query: map[string]string{"sendUpdates": "none", "fields": writeFields},
			body:       `{"summary":"Lunch with Ada","start":{"dateTime":"2026-10-24T12:00:00+02:00"},"end":{"dateTime":"2026-10-24T13:00:00+02:00"},"description":"Try the new place.","location":"Cafe Roma"}`,
			wantAnswer: `{"id":"` + madeEventID + `","summary":"Lunch with Ada","start":{"dateTime":"2026-10-24T12:00:00+02:00"},"end":{"dateTime":"2026-10-24T13:00:00+02:00"},"htmlLink":"https://www.google.com/calendar/event?eid=` + madeEventID + `"}`},
		{name: "create bare", d: cal, call: "create", params: map[string]string{"calendarId": "primary", "summary": "Focus", "start": "2026-10-25T09:00:00Z", "end": "2026-10-25T10:00:00Z"},
			method: "POST", path: "/calendar/v3/calendars/primary/events", query: map[string]string{"sendUpdates": "none", "fields": writeFields},
			body:       `{"summary":"Focus","start":{"dateTime":"2026-10-25T09:00:00Z"},"end":{"dateTime":"2026-10-25T10:00:00Z"}}`,
			wantAnswer: `{"id":"` + madeEventID + `","summary":"Focus","start":{"dateTime":"2026-10-25T09:00:00Z"},"end":{"dateTime":"2026-10-25T10:00:00Z"},"htmlLink":"https://www.google.com/calendar/event?eid=` + madeEventID + `"}`},
		{name: "edit one field", d: cal, call: "edit", params: map[string]string{"calendarId": "primary", "eventId": eventID, "location": "Cafe Novo"},
			method: "PATCH", path: "/calendar/v3/calendars/primary/events/" + eventID, query: map[string]string{"sendUpdates": "none", "fields": writeFields},
			body:       `{"start":{},"end":{},"location":"Cafe Novo"}`,
			wantAnswer: `{"id":"` + eventID + `","summary":"Lunch with Ada","start":{"dateTime":"2026-10-24T12:00:00+02:00","timeZone":"Europe/Berlin"},"end":{"dateTime":"2026-10-24T13:00:00+02:00","timeZone":"Europe/Berlin"},"htmlLink":"https://www.google.com/calendar/event?eid=` + eventID + `"}`},
		{name: "edit timed", d: cal, call: "edit", params: map[string]string{"calendarId": "primary", "eventId": eventID, "summary": "Lunch with Bob", "start": "2026-10-25T12:00:00+02:00", "end": "2026-10-25T13:00:00+02:00"},
			method: "PATCH", path: "/calendar/v3/calendars/primary/events/" + eventID, query: map[string]string{"sendUpdates": "none", "fields": writeFields},
			body:       `{"summary":"Lunch with Bob","start":{"dateTime":"2026-10-25T12:00:00+02:00"},"end":{"dateTime":"2026-10-25T13:00:00+02:00"}}`,
			wantAnswer: `{"id":"` + eventID + `","summary":"Lunch with Bob","start":{"dateTime":"2026-10-25T12:00:00+02:00"},"end":{"dateTime":"2026-10-25T13:00:00+02:00"},"htmlLink":"https://www.google.com/calendar/event?eid=` + eventID + `"}`},
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
	cal := googleDeclaration(t, "google-calendar.yaml", "https://www.googleapis.com", srv.URL)
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
		{"comments slashed fileId", drive, "comments", map[string]string{"fileId": "a/b"}, "parameter fileId does not match its pattern"},
		{"reply slashed fileId", drive, "reply", map[string]string{"fileId": "a/b", "commentId": "abc", "content": "x"}, "parameter fileId does not match its pattern"},
		{"reply slashed commentId", drive, "reply", map[string]string{"fileId": "abc", "commentId": "a/b", "content": "x"}, "parameter commentId does not match its pattern"},
		{"reply without content", drive, "reply", map[string]string{"fileId": "abc", "commentId": "abc"}, "parameter content is missing"},
		{"reply long content", drive, "reply", map[string]string{"fileId": "abc", "commentId": "abc", "content": strings.Repeat("x", 10001)}, "parameter content is 10001 characters, and at most 10000"},
		{"reply reopen", drive, "reply", map[string]string{"fileId": "abc", "commentId": "abc", "content": "x", "action": "reopen"}, "parameter action is one of resolve"},
		{"doc dots", docs, "doc", map[string]string{"docId": ".."}, "parameter docId does not match its pattern"},
		{"write heading 3", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "HEADING_3", "start": "1", "end": "2"}, "parameter style is one of NORMAL_TEXT, TITLE, HEADING_1, HEADING_2"},
		{"write fractional start", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "TITLE", "start": "1.5", "end": "2"}, "parameter start is no whole number"},
		{"write without end", docs, "write", map[string]string{"docId": "abc", "text": "x", "style": "TITLE", "start": "1"}, "parameter end is missing"},
		{"slide short id", slides, "slide", map[string]string{"id": "abc", "slide": "ab", "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
		{"slide dashed id", slides, "slide", map[string]string{"id": "abc", "slide": "-abcde", "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
		{"slide long id", slides, "slide", map[string]string{"id": "abc", "slide": strings.Repeat("a", 45), "title": "t", "body": "b"}, "parameter slide does not match its pattern"},
		{"events without maxResults", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z"}, "parameter maxResults is missing"},
		{"events 51", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "51"}, "parameter maxResults does not match its pattern"},
		{"events bare calendarId", cal, "events", map[string]string{"calendarId": "not an id", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10"}, "parameter calendarId does not match its pattern"},
		{"events empty calendarId", cal, "events", map[string]string{"calendarId": "", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10"}, "parameter calendarId does not match its pattern"},
		{"events date timeMin", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10"}, "parameter timeMin does not match its pattern"},
		{"events zoneless timeMax", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00", "maxResults": "10"}, "parameter timeMax does not match its pattern"},
		{"events long q", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "q": strings.Repeat("x", 1001), "maxResults": "10"}, "parameter q is 1001 characters, and at most 1000"},
		{"events unknown", cal, "events", map[string]string{"calendarId": "primary", "timeMin": "2026-10-20T00:00:00Z", "timeMax": "2026-10-27T00:00:00Z", "maxResults": "10", "pageToken": "abc"}, `"pageToken" is no parameter`},
		{"event slashed eventId", cal, "event", map[string]string{"calendarId": "primary", "eventId": "a/b"}, "parameter eventId does not match its pattern"},
		{"event short eventId", cal, "event", map[string]string{"calendarId": "primary", "eventId": "abcd"}, "parameter eventId does not match its pattern"},
		{"event uppercase eventId", cal, "event", map[string]string{"calendarId": "primary", "eventId": "ABCDE"}, "parameter eventId does not match its pattern"},
		{"event without calendarId", cal, "event", map[string]string{"eventId": "abc"}, "parameter calendarId is missing"},
		{"calendars with a param", cal, "calendars", map[string]string{"maxResults": "5"}, `"maxResults" is no parameter`},
		{"create without summary", cal, "create", map[string]string{"calendarId": "primary", "start": "2026-10-24T12:00:00Z", "end": "2026-10-24T13:00:00Z"}, "parameter summary is missing"},
		{"create date start", cal, "create", map[string]string{"calendarId": "primary", "summary": "Lunch", "start": "2026-10-24", "end": "2026-10-24T13:00:00Z"}, "parameter start does not match its pattern"},
		{"create long summary", cal, "create", map[string]string{"calendarId": "primary", "summary": strings.Repeat("x", 501), "start": "2026-10-24T12:00:00Z", "end": "2026-10-24T13:00:00Z"}, "parameter summary is 501 characters, and at most 500"},
		{"create long description", cal, "create", map[string]string{"calendarId": "primary", "summary": "Lunch", "start": "2026-10-24T12:00:00Z", "end": "2026-10-24T13:00:00Z", "description": strings.Repeat("x", 10001)}, "parameter description is 10001 characters, and at most 10000"},
		{"edit without eventId", cal, "edit", map[string]string{"calendarId": "primary", "summary": "Lunch"}, "parameter eventId is missing"},
		{"edit date start", cal, "edit", map[string]string{"calendarId": "primary", "eventId": "abcde", "start": "2026-10-24"}, "parameter start does not match its pattern"},
		{"edit long location", cal, "edit", map[string]string{"calendarId": "primary", "eventId": "abcde", "location": strings.Repeat("x", 501)}, "parameter location is 501 characters, and at most 500"},
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
