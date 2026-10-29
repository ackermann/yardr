package packs_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/scripttest"
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
			{Name: "thread", Kind: source.Read}, {Name: "drafts", Kind: source.Read},
			{Name: "draft", Kind: source.Update}, {Name: "draft_edit", Kind: source.Update},
		},
		"google-drive.yaml": {
			{Name: "files", Kind: source.Read}, {Name: "file", Kind: source.Read},
			{Name: "folder", Kind: source.Update}, {Name: "doc", Kind: source.Update},
			{Name: "slides", Kind: source.Update}, {Name: "copy", Kind: source.Update},
			{Name: "share", Kind: source.Update}, {Name: "comments", Kind: source.Read},
			{Name: "reply", Kind: source.Update},
		},
		"google-docs.yaml": {
			{Name: "doc", Kind: source.Read}, {Name: "write", Kind: source.Update},
			{Name: "replace", Kind: source.Update},
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
	auth         string
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
	copyID := "1CoPy0123456789abcdefghijklmnopq"
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
	copyFields := "id,name,webViewLink"
	fileFields := "id,name,mimeType,parents,modifiedTime,size,trashed"
	madeFields := "id,name,mimeType,parents"
	searchPage1 := `{"messages":[{"id":"18c4a1b2c3d4e5f6","threadId":"18c4a1b2c3d4e5f6"},{"id":"18c4a1b2c3d4e5f7","threadId":"18c4a1b2c3d4e5f6"}],"nextPageToken":"CAEQDBgBEg9OZXh0IFBhZ2U","resultSizeEstimate":2}`
	searchPage2 := `{"messages":[{"id":"18c4a1b2c3d4e5f8","threadId":"18c4a1b2c3d4e5f8"}],"resultSizeEstimate":3}`
	draftsPage := `{"drafts":[{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9"}},{"id":"r123457","message":{"id":"18c4a1b2c3d4e5fa","threadId":"18c4a1b2c3d4e5fa"}}],"nextPageToken":"CAEQDBgBEg9EcmFmdHM","resultSizeEstimate":2}`
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
		"GET /gmail/v1/users/me/drafts":                        draftsPage,
		"POST /gmail/v1/users/me/drafts":                       `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`,
		"PUT /gmail/v1/users/me/drafts/r123456":                `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`,
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
		// A copy answers with the name it was given and its own id, not the
		// original's. The recording carries keys the call's select drops.
		"POST /drive/v3/files/" + driveID + "/copy": func(_ url.Values, body []byte) string {
			var made struct {
				Name    string
				Parents []string
			}
			if err := json.Unmarshal(body, &made); err != nil {
				t.Fatalf("the stand-in was sent %s: %v", body, err)
			}
			out, _ := json.Marshal(map[string]any{"kind": "drive#file", "id": copyID, "name": made.Name, "mimeType": "application/vnd.google-apps.document", "parents": made.Parents, "webViewLink": "https://docs.google.com/document/d/" + copyID, "version": "1"})
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
		{name: "drafts", d: mail, call: "drafts", params: map[string]string{"q": "from:me", "maxResults": "5"},
			method: "GET", path: "/gmail/v1/users/me/drafts", query: map[string]string{"q": "from:me", "maxResults": "5"}, wantAnswer: draftsPage},
		// source.Do sends the template and runs no build: the draft's body
		// comes from its script (TestGoogleMailDraftBuild below), so the
		// template path sends none.
		{name: "draft template sends no body", d: mail, call: "draft", params: map[string]string{"to": "someone@example.com", "subject": "hello", "body": "The body."},
			method: "POST", path: "/gmail/v1/users/me/drafts",
			wantAnswer: `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`},
		{name: "draft_edit template sends no body", d: mail, call: "draft_edit", params: map[string]string{"id": "r123456", "to": "someone@example.com", "subject": "hello", "body": "The body."},
			method: "PUT", path: "/gmail/v1/users/me/drafts/r123456",
			wantAnswer: `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f9","labelIds":["DRAFT"]}}`},
		{name: "files", d: drive, call: "files", params: nil,
			method: "GET", path: "/drive/v3/files",
			wantAnswer: `{"incompleteSearch":false,"files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`},
		{name: "files with q", d: drive, call: "files", params: map[string]string{"q": "name contains 'x'"},
			method: "GET", path: "/drive/v3/files", query: map[string]string{"q": "name contains 'x'"},
			wantAnswer: `{"incompleteSearch":false,"nextPageToken":"next","files":[{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document"}]}`},
		{name: "file", d: drive, call: "file", params: map[string]string{"id": driveID},
			method: "GET", path: "/drive/v3/files/" + driveID, query: map[string]string{"fields": fileFields},
			wantAnswer: `{"id":"` + driveID + `","name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["1PaReNt0123456789abcdefghijklm"],"modifiedTime":"2026-10-16T15:00:00.000Z","size":"1024","trashed":false}`},
		{name: "folder", d: drive, call: "folder", params: map[string]string{"name": "notes", "parent": parent},
			method: "POST", path: "/drive/v3/files", query: map[string]string{"fields": madeFields},
			body:       `{"name":"notes","mimeType":"application/vnd.google-apps.folder","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.folder","name":"notes","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "drive doc", d: drive, call: "doc", params: map[string]string{"name": "lunch", "parent": parent},
			method: "POST", path: "/drive/v3/files", query: map[string]string{"fields": madeFields},
			body:       `{"name":"lunch","mimeType":"application/vnd.google-apps.document","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.document","name":"lunch","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "drive slides", d: drive, call: "slides", params: map[string]string{"name": "friday", "parent": parent},
			method: "POST", path: "/drive/v3/files", query: map[string]string{"fields": madeFields},
			body:       `{"name":"friday","mimeType":"application/vnd.google-apps.presentation","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"1NeWFiLe0123456789abcdefghijklm","mimeType":"application/vnd.google-apps.presentation","name":"friday","parents":["1PaReNt0123456789abcdefghijklm"]}`},
		{name: "copy", d: drive, call: "copy", params: map[string]string{"id": driveID, "name": "lunch, again", "parent": parent},
			method: "POST", path: "/drive/v3/files/" + driveID + "/copy", query: map[string]string{"fields": copyFields},
			body:       `{"name":"lunch, again","parents":["` + parent + `"]}`,
			wantAnswer: `{"id":"` + copyID + `","name":"lunch, again","webViewLink":"https://docs.google.com/document/d/` + copyID + `"}`},
		// The parent left out drops the whole "{parent}" element of the list,
		// and the key stays: the body sends an empty parents list.
		{name: "copy without parent", d: drive, call: "copy", params: map[string]string{"id": driveID, "name": "lunch, again"},
			method: "POST", path: "/drive/v3/files/" + driveID + "/copy", query: map[string]string{"fields": copyFields},
			body:       `{"name":"lunch, again","parents":[]}`,
			wantAnswer: `{"id":"` + copyID + `","name":"lunch, again","webViewLink":"https://docs.google.com/document/d/` + copyID + `"}`},
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
		// The write call builds its body per call; its goldens and round
		// trip are TestGoogleDocsWriteBuilds and TestGoogleDocsWriteRoundTrip.
		// So does the replace call; its are TestGoogleDocsReplaceBuilds and
		// TestGoogleDocsReplaceRoundTrip.
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
		{"draft spaced to", mail, "draft", map[string]string{"to": "bob @example.com"}, "parameter to does not match its pattern"},
		{"draft tab to", mail, "draft", map[string]string{"to": "bob\t@example.com"}, "parameter to does not match its pattern"},
		{"draft long to", mail, "draft", map[string]string{"to": "a@" + strings.Repeat("b", 300) + ".com"}, "parameter to is 306 characters, and at most 254"},
		{"draft bare cc", mail, "draft", map[string]string{"cc": "bob"}, "parameter cc does not match its pattern"},
		{"draft long subject", mail, "draft", map[string]string{"subject": strings.Repeat("x", 501)}, "parameter subject is 501 characters, and at most 500"},
		{"draft long body", mail, "draft", map[string]string{"body": strings.Repeat("x", 100001)}, "parameter body is 100001 characters, and at most 100000"},
		{"draft uppercase thread", mail, "draft", map[string]string{"thread": "ABCDEF"}, "parameter thread does not match its pattern"},
		{"draft_edit without id", mail, "draft_edit", map[string]string{"to": "someone@example.com", "subject": "hello", "body": "The body."}, "parameter id is missing"},
		{"files long q", drive, "files", map[string]string{"q": strings.Repeat("x", 1001)}, "parameter q is 1001 characters, and at most 1000"},
		{"file slashed id", drive, "file", map[string]string{"id": "a/b"}, "parameter id does not match its pattern"},
		{"folder long name", drive, "folder", map[string]string{"name": strings.Repeat("x", 201), "parent": "abc"}, "parameter name is 201 characters, and at most 200"},
		{"copy slashed id", drive, "copy", map[string]string{"id": "a/b", "name": "x"}, "parameter id does not match its pattern"},
		{"copy slashed parent", drive, "copy", map[string]string{"id": "abc", "name": "x", "parent": "a/b"}, "parameter parent does not match its pattern"},
		{"copy empty name", drive, "copy", map[string]string{"id": "abc", "name": ""}, "parameter name does not match its pattern"},
		{"copy name with a newline", drive, "copy", map[string]string{"id": "abc", "name": "a\nb"}, "parameter name does not match its pattern"},
		{"copy name ending in a newline", drive, "copy", map[string]string{"id": "abc", "name": "ab\n"}, "parameter name does not match its pattern"},
		{"copy long name", drive, "copy", map[string]string{"id": "abc", "name": strings.Repeat("x", 201)}, "parameter name is 201 characters, and at most 200"},
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
		{"write without paragraphs", docs, "write", map[string]string{"docId": "abc"}, "parameter paragraphs is missing"},
		{"write long paragraphs", docs, "write", map[string]string{"docId": "abc", "paragraphs": strings.Repeat("x", 60001)}, "parameter paragraphs is 60001 characters, and at most 60000"},
		{"write text", docs, "write", map[string]string{"docId": "abc", "paragraphs": "[]", "text": "x"}, `"text" is no parameter`},
		{"replace without find", docs, "replace", map[string]string{"docId": "abc", "with": "x"}, "parameter find is missing"},
		{"replace without with", docs, "replace", map[string]string{"docId": "abc", "find": "x"}, "parameter with is missing"},
		{"replace long find", docs, "replace", map[string]string{"docId": "abc", "find": strings.Repeat("x", 1001), "with": "x"}, "parameter find is 1001 characters, and at most 1000"},
		{"replace long with", docs, "replace", map[string]string{"docId": "abc", "find": "x", "with": strings.Repeat("x", 60001)}, "parameter with is 60001 characters, and at most 60000"},
		{"replace all maybe", docs, "replace", map[string]string{"docId": "abc", "find": "x", "with": "x", "all": "maybe"}, "parameter all is true or false"},
		{"replace dots", docs, "replace", map[string]string{"docId": "..", "find": "x", "with": "x"}, "parameter docId does not match its pattern"},
		{"replace text", docs, "replace", map[string]string{"docId": "abc", "find": "x", "with": "x", "text": "y"}, `"text" is no parameter`},
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

// The draft call builds its body with the pack's script: the headers of a
// composed draft, a UTF-8 subject and body, a reply's threadId and reply
// headers, the raw passthrough, and the refusal of a shape that is neither.
// The draft_edit call builds with the same script, carrying the id of the
// draft it replaces beside either shape. The script runs from an installed
// copy, as the yard runs it; the stand-in answers the thread's messages
// and receives the built draft. The non-ASCII subject is an escape, not a
// literal, so the golden holds exactly the bytes the script was given.
func TestGoogleMailDraftBuild(t *testing.T) {
	for _, tool := range []string{"curl", "jq", "base64"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the draft's build command is written with it", tool)
		}
	}
	threadID := "18c4a1b2c3d4e5f6"
	draftID := "r123456"
	lastID := "18c4a1b2c3d4e5f7"
	parent := "<abc123@mail.gmail.com>"
	emptyThreadID := "18c4a1b2c3d4e000"
	missingThreadID := "18c4a1b2c3d4e404"
	noidThreadID := "18c4a1b2c3d4e111"
	noidLastID := "18c4a1b2c3d4e112"
	badLastThreadID := "18c4a1b2c3d4e222"
	badHeaderThreadID := "18c4a1b2c3d4e333"
	badHeaderLastID := "18c4a1b2c3d4e334"
	escapedToken := "tok\"\\word"
	type draftRequest struct{ method, path, query string }
	var mu sync.Mutex
	var got []draftRequest
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" && auth != "Bearer "+escapedToken {
			t.Errorf("%s %s: Authorization %q, want a Bearer tok", r.Method, r.URL.Path, auth)
		}
		var requestBody string
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read draft body: %v", err)
			}
			requestBody = string(b)
		}
		mu.Lock()
		got = append(got, draftRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery})
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			writes = append(writes, requestBody)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		format := r.URL.Query().Get("format")
		switch r.Method + " " + r.URL.Path {
		case "GET /gmail/v1/users/me/threads/" + threadID:
			if format != "minimal" {
				t.Errorf("threads.get was sent format %q, want minimal", format)
			}
			io.WriteString(w, `{"id":"`+threadID+`","messages":[{"id":"`+threadID+`","threadId":"`+threadID+`"},{"id":"`+lastID+`","threadId":"`+threadID+`"}]}`)
		case "GET /gmail/v1/users/me/threads/" + emptyThreadID:
			io.WriteString(w, `{"id":"`+emptyThreadID+`","messages":[]}`)
		case "GET /gmail/v1/users/me/threads/" + missingThreadID:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`)
		case "GET /gmail/v1/users/me/threads/" + noidThreadID:
			io.WriteString(w, `{"id":"`+noidThreadID+`","messages":[{"id":"`+noidLastID+`","threadId":"`+noidThreadID+`"}]}`)
		case "GET /gmail/v1/users/me/threads/" + badLastThreadID:
			io.WriteString(w, `{"id":"`+badLastThreadID+`","messages":[{"id":"bad\"\nurl = \"https://attacker.example\"","threadId":"`+badLastThreadID+`"}]}`)
		case "GET /gmail/v1/users/me/threads/" + badHeaderThreadID:
			io.WriteString(w, `{"id":"`+badHeaderThreadID+`","messages":[{"id":"`+badHeaderLastID+`","threadId":"`+badHeaderThreadID+`"}]}`)
		case "GET /gmail/v1/users/me/messages/" + lastID:
			if format != "metadata" {
				t.Errorf("messages.get was sent format %q, want metadata", format)
			}
			io.WriteString(w, `{"id":"`+lastID+`","threadId":"`+threadID+`","labelIds":["INBOX","UNREAD"],"snippet":"Lunch on Friday?","payload":{"mimeType":"multipart/alternative","headers":[{"name":"From","value":"ada@example.com"},{"name":"Subject","value":"Lunch"},{"name":"Message-ID","value":"`+parent+`"}]}}`)
		case "GET /gmail/v1/users/me/messages/" + noidLastID:
			io.WriteString(w, `{"id":"`+noidLastID+`","threadId":"`+noidThreadID+`","payload":{"mimeType":"text/plain","headers":[{"name":"From","value":"ada@example.com"}]}}`)
		case "GET /gmail/v1/users/me/messages/" + badHeaderLastID:
			io.WriteString(w, `{"id":"`+badHeaderLastID+`","threadId":"`+badHeaderThreadID+`","payload":{"mimeType":"text/plain","headers":[{"name":"Message-ID","value":"<abc123@mail.gmail.com>\nBcc: attacker@example.com"}]}}`)
		case "POST /gmail/v1/users/me/drafts":
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"r123456","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f6","labelIds":["DRAFT"]}}`)
		case "PUT /gmail/v1/users/me/drafts/" + draftID:
			io.WriteString(w, `{"id":"`+draftID+`","message":{"id":"18c4a1b2c3d4e5f9","threadId":"18c4a1b2c3d4e5f6","labelIds":["DRAFT"]}}`)
		default:
			t.Errorf("the stand-in has no answer for %s %s", r.Method, r.URL.Path)
			http.Error(w, "no such recorded shape", 404)
		}
	}))
	defer srv.Close()
	d := googleDeclaration(t, "google-mail.yaml", "https://gmail.googleapis.com", srv.URL)
	home := t.TempDir()
	sub, err := fs.Sub(packs.FS, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CopyCommandFilesFS(sub, ".", home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", "google-mail"))
	folded := strings.Repeat("\u00e9", 23)
	for _, tc := range []struct {
		name         string
		call         string // "" means the draft call
		params       map[string]string
		credential   string
		wantMIME     string // the decoded raw; "" with wantRaw for the passthrough
		wantRaw      string
		wantThread   string // "" means the answer holds no threadId
		wantID       string // "" means the answer holds no draft id
		wantRefusal  string // "" means the build succeeds
		wantRequests []draftRequest
	}{
		{name: "headers", params: map[string]string{"to": "someone@example.com", "subject": "hello", "body": "The body."},
			wantMIME: "To: someone@example.com\nSubject: hello\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHku\n"},
		{name: "cc", params: map[string]string{"to": "someone@example.com", "cc": "boss@example.com", "subject": "hello", "body": "The body."},
			wantMIME: "To: someone@example.com\nCc: boss@example.com\nSubject: hello\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHku\n"},
		{name: "utf-8 subject", params: map[string]string{"to": "someone@example.com", "subject": "Caf\u00e9 Friday", "body": "The body."},
			wantMIME: "To: someone@example.com\nSubject: =?UTF-8?B?Q2Fmw6kgRnJpZGF5?=\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHku\n"},
		{name: "long utf-8 subject folds whole characters", params: map[string]string{"to": "someone@example.com", "subject": folded, "body": "The body."},
			wantMIME: "To: someone@example.com\nSubject: =?UTF-8?B?w6nDqcOpw6nDqcOpw6nDqcOpw6nDqcOpw6nDqcOpw6nDqcOpw6nDqcOpw6k=?=\n =?UTF-8?B?w6k=?=\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHku\n"},
		{name: "reply", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": threadID},
			credential: escapedToken,
			wantMIME:   "To: ada@example.com\nSubject: Re: Lunch\nIn-Reply-To: <abc123@mail.gmail.com>\nReferences: <abc123@mail.gmail.com>\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nRnJpZGF5IHdvcmtzLg==\n",
			wantThread: threadID,
			wantRequests: []draftRequest{
				{"GET", "/gmail/v1/users/me/threads/" + threadID, "format=minimal"},
				{"GET", "/gmail/v1/users/me/messages/" + lastID, "format=metadata"},
			}},
		{name: "body keeps trailing newlines", params: map[string]string{"to": "someone@example.com", "subject": "hello", "body": "The body.\n\n"},
			wantMIME: "To: someone@example.com\nSubject: hello\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHkuCgo=\n"},
		{name: "utf-8 body", params: map[string]string{"to": "someone@example.com", "subject": "hello", "body": "Caf\u00e9 \u2615"},
			wantMIME: "To: someone@example.com\nSubject: hello\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nQ2Fmw6kg4piV\n"},
		{name: "raw passthrough", params: map[string]string{"raw": "RnJvbTptZQ"}, wantRaw: "RnJvbTptZQ"},
		{name: "padded raw passthrough", params: map[string]string{"raw": "RnJvbTptZQ=="}, wantRaw: "RnJvbTptZQ=="},
		{name: "edit headers", call: "draft_edit", params: map[string]string{"id": draftID, "to": "someone@example.com", "subject": "hello", "body": "The body."},
			wantMIME: "To: someone@example.com\nSubject: hello\nMIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: base64\n\nVGhlIGJvZHku\n",
			wantID:   draftID},
		{name: "edit raw passthrough", call: "draft_edit", params: map[string]string{"id": draftID, "raw": "RnJvbTptZQ"}, wantRaw: "RnJvbTptZQ", wantID: draftID},
		{name: "mixed raw and to", params: map[string]string{"raw": "RnJvbTptZQ", "to": "someone@example.com"},
			wantRefusal: "raw was given with to: pass raw alone, or to, subject and body"},
		{name: "mixed raw and thread", params: map[string]string{"raw": "RnJvbTptZQ", "thread": threadID},
			wantRefusal: "raw was given with thread: pass raw alone, or to, subject and body"},
		{name: "missing subject and body", params: map[string]string{"to": "someone@example.com"},
			wantRefusal: "a draft is to, subject and body, or raw alone; missing: subject, body"},
		{name: "subject line break", params: map[string]string{"to": "someone@example.com", "subject": "a\nb", "body": "The body."},
			wantRefusal: "subject holds a line break: a header is one line"},
		{name: "subject trailing line break", params: map[string]string{"to": "someone@example.com", "subject": "hello\n", "body": "The body."},
			wantRefusal: "subject holds a line break: a header is one line"},
		{name: "to outside ascii", params: map[string]string{"to": "m\u00fcll@example.com", "subject": "hello", "body": "The body."},
			wantRefusal: "to holds a character outside ASCII: addresses are ASCII"},
		{name: "thread gone", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": missingThreadID},
			wantRefusal:  "thread " + missingThreadID + " could not be read: Requested entity was not found.",
			wantRequests: []draftRequest{{"GET", "/gmail/v1/users/me/threads/" + missingThreadID, "format=minimal"}}},
		{name: "thread empty", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": emptyThreadID},
			wantRefusal:  "thread " + emptyThreadID + " holds no message to answer",
			wantRequests: []draftRequest{{"GET", "/gmail/v1/users/me/threads/" + emptyThreadID, "format=minimal"}}},
		{name: "last message without id", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": noidThreadID},
			wantRefusal: "message " + noidLastID + " has no Message-ID header to answer",
			wantRequests: []draftRequest{
				{"GET", "/gmail/v1/users/me/threads/" + noidThreadID, "format=minimal"},
				{"GET", "/gmail/v1/users/me/messages/" + noidLastID, "format=metadata"},
			}},
		{name: "invalid last message id", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": badLastThreadID},
			wantRefusal:  "thread " + badLastThreadID + " holds an invalid message id",
			wantRequests: []draftRequest{{"GET", "/gmail/v1/users/me/threads/" + badLastThreadID, "format=minimal"}}},
		{name: "message id line break", params: map[string]string{"to": "ada@example.com", "subject": "Re: Lunch", "body": "Friday works.", "thread": badHeaderThreadID},
			wantRefusal: "message " + badHeaderLastID + " Message-ID holds a line break: a header is one line",
			wantRequests: []draftRequest{
				{"GET", "/gmail/v1/users/me/threads/" + badHeaderThreadID, "format=minimal"},
				{"GET", "/gmail/v1/users/me/messages/" + badHeaderLastID, "format=metadata"},
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			got = nil
			writes = nil
			mu.Unlock()
			credential := tc.credential
			if credential == "" {
				credential = "tok"
			}
			call := tc.call
			if call == "" {
				call = "draft"
			}
			body, err := source.BuildBody(context.Background(), d, call, tc.params, credential, home)
			if tc.wantRefusal != "" {
				var failed *source.CallError
				if !errors.As(err, &failed) || failed.Reason != source.BuildRefused {
					t.Fatalf("err = %v (%s), want %s", err, body, source.BuildRefused)
				} else if !strings.Contains(failed.Error(), tc.wantRefusal) {
					t.Fatalf("err = %v, want %q", failed, tc.wantRefusal)
				}
			} else {
				if err != nil {
					t.Fatalf("BuildBody = %v", err)
				}
				var out struct {
					ID      string `json:"id"`
					Message struct {
						Raw      string `json:"raw"`
						ThreadID string `json:"threadId"`
					} `json:"message"`
				}
				if err := json.Unmarshal(body, &out); err != nil {
					t.Fatalf("answer = %s: %v", body, err)
				}
				if out.ID != tc.wantID {
					t.Fatalf("id = %q, want %q", out.ID, tc.wantID)
				}
				if tc.wantRaw != "" {
					if out.Message.Raw != tc.wantRaw || out.Message.ThreadID != "" {
						t.Fatalf("answer = %s, want the raw wrapped untouched", body)
					}
				} else {
					raw, err := base64.RawURLEncoding.DecodeString(out.Message.Raw)
					if err != nil {
						t.Fatalf("raw = %q: %v", out.Message.Raw, err)
					}
					if string(raw) != tc.wantMIME {
						t.Fatalf("mime = %q, want %q", raw, tc.wantMIME)
					}
					if out.Message.ThreadID != tc.wantThread {
						t.Fatalf("threadId = %q, want %q", out.Message.ThreadID, tc.wantThread)
					}
				}
				res, err := source.DoKeyedBody(context.Background(), d, call, tc.params, credential, "", body)
				wantStatus, wantWrite := http.StatusCreated, draftRequest{"POST", "/gmail/v1/users/me/drafts", ""}
				if call == "draft_edit" {
					wantStatus, wantWrite = http.StatusOK, draftRequest{"PUT", "/gmail/v1/users/me/drafts/" + draftID, ""}
				}
				if err != nil || res.Status != wantStatus {
					t.Fatalf("DoKeyedBody = %+v, %v; want the draft written", res, err)
				}
				tc.wantRequests = append(tc.wantRequests, wantWrite)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(got, tc.wantRequests) {
				t.Fatalf("the stand-in heard %v, want %v", got, tc.wantRequests)
			}
			wantWrites := []string(nil)
			if tc.wantRefusal == "" {
				wantWrites = []string{string(body)}
			}
			if !reflect.DeepEqual(writes, wantWrites) {
				t.Fatalf("draft write bodies = %q, want %q", writes, wantWrites)
			}
		})
	}
}

// The write call builds its batchUpdate per call: its build script reads the
// document for its body's end index, works out each paragraph's range
// itself, and prints the body the yard posts. The first appended character
// sits at E-1, before the body's last newline, and each paragraph starts
// past the newline before it. The write is pinned to the revision read.
// Indices are UTF-16 code units, so an astral character counts double. No
// test calls Google.
func TestGoogleDocsWriteBuilds(t *testing.T) {
	paras := `[{"text":"Lunch places","style":"TITLE"},{"text":"Friday","style":"HEADING_1"},` +
		`{"text":"We shipped the condiments.","style":"NORMAL_TEXT"},{"text":"Ada picks the place.","style":"NORMAL_TEXT"}]`
	insert := `{"endOfSegmentLocation":{},"text":"Lunch places\nFriday\nWe shipped the condiments.\nAda picks the place."}`
	style := func(start, end int, named string) string {
		return `{"range":{"startIndex":` + strconv.Itoa(start) + `,"endIndex":` + strconv.Itoa(end) +
			`},"paragraphStyle":{"namedStyleType":"` + named + `"},"fields":"namedStyleType"}`
	}
	for _, tc := range []struct {
		name   string
		doc    string // the stand-in's doc: an empty one, or one with text
		paras  string
		insert string
		styles []string
	}{
		{name: "title heading bodies on an empty doc", doc: "empty", paras: paras, insert: insert,
			styles: []string{style(1, 13, "TITLE"), style(14, 20, "HEADING_1"), style(21, 47, "NORMAL_TEXT"), style(48, 68, "NORMAL_TEXT")}},
		{name: "title heading bodies on a doc with text", doc: "full", paras: paras, insert: insert,
			styles: []string{style(12, 24, "TITLE"), style(25, 31, "HEADING_1"), style(32, 58, "NORMAL_TEXT"), style(59, 79, "NORMAL_TEXT")}},
		{name: "an emoji counts double", doc: "full",
			paras:  `[{"text":"Lunch 🍜 places","style":"HEADING_1"},{"text":"Friday it is.","style":"NORMAL_TEXT"}]`,
			insert: `{"endOfSegmentLocation":{},"text":"Lunch 🍜 places\nFriday it is."}`,
			styles: []string{style(12, 27, "HEADING_1"), style(28, 41, "NORMAL_TEXT")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			standin, srv := writeStandin(t)
			defer srv.Close()
			docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
			body, err := source.BuildBody(context.Background(), docs, "write",
				map[string]string{"docId": standin.doc(tc.doc), "paragraphs": tc.paras}, "tok", writeHome(t))
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("built body = %s: %v", body, err)
			}
			golden := `{"requests":[{"insertText":` + tc.insert + `}`
			for _, s := range tc.styles {
				golden += `,{"updateParagraphStyle":` + s + `}`
			}
			revision := map[string]string{"empty": "rev-empty", "full": "rev-full"}[tc.doc]
			golden += `],"writeControl":{"requiredRevisionId":"` + revision + `"}}`
			if err := json.Unmarshal([]byte(golden), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("built body = %s, want %s", body, golden)
			}
			// The pre-read the ranges came from: the doc, read with the
			// call's credential, asking only for its revision and end indices.
			gotReqs := standin.requests()
			if len(gotReqs) != 1 {
				t.Fatalf("the stand-in heard %d requests, want the one pre-read", len(gotReqs))
			}
			pre := gotReqs[0]
			if pre.method != "GET" || pre.path != "/v1/documents/"+standin.doc(tc.doc) {
				t.Fatalf("pre-read = %s %s", pre.method, pre.path)
			}
			if pre.auth != "Bearer tok" {
				t.Fatalf("pre-read Authorization = %q, want the call's credential", pre.auth)
			}
			if pre.query.Get("fields") != "revisionId,body(content(endIndex))" {
				t.Fatalf("pre-read fields = %q", pre.query.Get("fields"))
			}
		})
	}
}

// What the write call refuses to build: nothing is posted, and the caller
// reads the script's own words. The stand-in hears the pre-read alone, or
// nothing where the paragraphs never parsed.
func TestGoogleDocsWriteRefusals(t *testing.T) {
	standin, srv := writeStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	home := writeHome(t)
	for _, tc := range []struct {
		name  string
		doc   string
		paras string
		words string
		pre   bool // the refusal came after the pre-read
	}{
		{"no JSON", "full", "nope", "paragraphs is no JSON array of text/style pairs", false},
		{"no array", "full", `{"text":"x"}`, "paragraphs is no JSON array of text/style pairs", false},
		{"no paragraphs", "full", "[]", "paragraphs holds no paragraph to append", false},
		{"no pair", "full", `["x"]`, "paragraph 1 is no text/style pair", false},
		{"no text", "full", `[{"style":"TITLE"}]`, "paragraph 1 names no text", false},
		{"no style", "full", `[{"text":"x","style":"SNACK"}]`, "paragraph 1 styles with SNACK, which is no style: one of NORMAL_TEXT, TITLE, HEADING_1, HEADING_2", false},
		{"a line break", "full", `[{"text":"Lunch places","style":"TITLE"},{"text":"on\nFriday","style":"NORMAL_TEXT"}]`, "paragraph 2 holds a line break: one pair is one paragraph", false},
		{"a stripped control", "full", `[{"text":"a\u0001b","style":"TITLE"}]`, "paragraph 1 holds a control or private-use character Google Docs strips", false},
		{"a stripped private-use character", "full", `[{"text":"a\ue000b","style":"TITLE"}]`, "paragraph 1 holds a control or private-use character Google Docs strips", false},
		{"no text at all", "full", `[{"text":"","style":"TITLE"}]`, "paragraphs holds no text to append", false},
		{"no such doc", "missing", `[{"text":"x","style":"TITLE"}]`, "could not be read", true},
		{"no end index", "empty-answered", `[{"text":"x","style":"TITLE"}]`, "answered no end index", true},
		{"no revision id", "no-revision", `[{"text":"x","style":"TITLE"}]`, "answered no revision id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(standin.requests())
			_, err := source.BuildBody(context.Background(), docs, "write",
				map[string]string{"docId": standin.doc(tc.doc), "paragraphs": tc.paras}, "tok", home)
			var refused *source.CallError
			if !errors.As(err, &refused) || refused.Reason != source.BuildRefused || !strings.Contains(err.Error(), tc.words) {
				t.Fatalf("err = %v, want %s containing %q", err, source.BuildRefused, tc.words)
			}
			want := 0
			if tc.pre {
				want = 1
			}
			if n := len(standin.requests()) - before; n != want {
				t.Fatalf("the stand-in heard %d requests past the %d before, want %d", n, before, want)
			}
		})
	}
}

// The built body posted: one write appends its paragraphs in one batchUpdate,
// and the answer passes through the call's select.
func TestGoogleDocsWriteRoundTrip(t *testing.T) {
	standin, srv := writeStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	paras := `[{"text":"Lunch places","style":"TITLE"},{"text":"Friday","style":"HEADING_1"},` +
		`{"text":"We shipped the condiments.","style":"NORMAL_TEXT"},{"text":"Ada picks the place.","style":"NORMAL_TEXT"}]`
	body, err := source.BuildBody(context.Background(), docs, "write",
		map[string]string{"docId": standin.doc("empty"), "paragraphs": paras}, "tok", writeHome(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := source.DoKeyedBody(context.Background(), docs, "write",
		map[string]string{"docId": standin.doc("empty"), "paragraphs": paras}, "tok", "", body)
	if err != nil {
		t.Fatal(err)
	}
	gotReqs := standin.requests()
	if len(gotReqs) != 2 {
		t.Fatalf("the stand-in heard %d requests, want the pre-read and the batchUpdate", len(gotReqs))
	}
	post := gotReqs[1]
	if post.method != "POST" || post.path != "/v1/documents/"+standin.doc("empty")+":batchUpdate" {
		t.Fatalf("post = %s %s", post.method, post.path)
	}
	if post.auth != "Bearer tok" {
		t.Fatalf("post Authorization = %q", post.auth)
	}
	var got, want any
	if err := json.Unmarshal(post.body, &got); err != nil {
		t.Fatalf("posted body = %s: %v", post.body, err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("posted body = %s, want the built %s", post.body, body)
	}
	if want := `{"documentId":"` + standin.doc("empty") + `","replies":[{},{},{},{}]}`; string(res.Answer) != want {
		t.Fatalf("answer = %s, want %s", res.Answer, want)
	}
}

// The pre-read's credential is no argument of curl's: an argument is
// readable by every process on the machine while curl runs, so the script
// hands the header to curl as a config line on its stdin, as the other
// packs' scripts hand theirs over. A stand-in curl first on PATH records
// its arguments and delegates to the real one.
func TestGoogleDocsWriteHandsCurlNoToken(t *testing.T) {
	real, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed, and the write build script reads the doc with it")
	}
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	scripttest.Write(t, filepath.Join(bin, "curl"),
		"printf '%s\n' \"$@\" > \""+argv+"\"\nexec \""+real+"\" \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	standin, srv := writeStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	if _, err := source.BuildBody(context.Background(), docs, "write",
		map[string]string{"docId": standin.doc("empty"), "paragraphs": `[{"text":"x","style":"TITLE"}]`},
		"the-secret-token", writeHome(t)); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), "the-secret-token") {
		t.Fatalf("curl was called with the credential in an argument: %s", recorded)
	}
	if got := standin.requests()[0].auth; got != "Bearer the-secret-token" {
		t.Fatalf("pre-read Authorization = %q, want the credential, handed over out of the arguments", got)
	}
}

// writeHome installs the write call's build script from the embedded pack,
// as a source add copies it, and returns the home it stands under. The
// script reads docs with curl and counts with jq.
func writeHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the write build script reads the doc with it")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the write build script counts with it")
	}
	script, err := fs.ReadFile(packs.FS, "google-docs/google-docs/build-write")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := source.WriteCopies(home, "google-docs", []source.FileCopy{{Program: "google-docs/build-write", Data: script}}); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", "google-docs"))
	return home
}

// writeDoc is one doc the write stand-in serves: its id, and the end indices
// its body answers with.
type writeDoc struct {
	id       string
	ends     []int
	revision string
}

// docsStandin answers documents.get for an empty doc and a doc with text,
// and batchUpdate with stock replies, keeping every request it was sent.
type docsStandin struct {
	t    *testing.T
	mu   sync.Mutex
	got  []googleRequest
	docs map[string]writeDoc
}

func writeStandin(t *testing.T) (*docsStandin, *httptest.Server) {
	t.Helper()
	s := &docsStandin{t: t, docs: map[string]writeDoc{
		"empty":          {id: "1Empty0123456789abcdefghijklmnop", ends: []int{2}, revision: "rev-empty"},
		"full":           {id: "1FullDoc0123456789abcdefghijklm", ends: []int{7, 13}, revision: "rev-full"},
		"empty-answered": {id: "1NoEnds0123456789abcdefghijklmno", ends: nil, revision: "rev-no-ends"},
		"no-revision":    {id: "1NoRevision0123456789abcdefghij", ends: []int{5}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got = append(s.got, googleRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization"), body: body})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		for _, d := range s.docs {
			switch {
			case r.Method == "GET" && r.URL.Path == "/v1/documents/"+d.id:
				if d.ends == nil {
					io.WriteString(w, `{"revisionId":"`+d.revision+`"}`)
					return
				}
				var ends []string
				for _, e := range d.ends {
					ends = append(ends, `{"endIndex":`+strconv.Itoa(e)+`}`)
				}
				io.WriteString(w, `{"revisionId":"`+d.revision+`","body":{"content":[`+strings.Join(ends, ",")+`]}}`)
				return
			case r.Method == "POST" && r.URL.Path == "/v1/documents/"+d.id+":batchUpdate":
				io.WriteString(w, `{"documentId":"`+d.id+`","replies":[{},{},{},{}],"writeControl":{"requiredRevisionId":"9"}}`)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":404,"message":"no such doc"}}`)
	}))
	return s, srv
}

// doc is the id of the write stand-in's named doc; "missing" is one it has
// no answer for.
func (s *docsStandin) doc(name string) string {
	if d, ok := s.docs[name]; ok {
		return d.id
	}
	return "1Missing0123456789abcdefghijklmno"
}

func (s *docsStandin) requests() []googleRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]googleRequest(nil), s.got...)
}

// The replace call builds its batchUpdate per call: its build script reads
// the document for its text, counts the occurrences of find itself, and
// prints the replaceAllText its guard allows. The match is literal and
// case-sensitive, as matchCase asks; a match in a table, header, footnote
// or another tab counts like one in the body. Zero matches still sends, and
// the answer's occurrencesChanged of 0 says so. No test calls Google.
func TestGoogleDocsReplaceBuilds(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string // the stand-in's doc: one, none, many, a table, a header, a
		// footnote, or several tabs.
		find string
		with string
		all  string // "" leaves all out
	}{
		{name: "one match", doc: "one", find: "Lunch", with: "Dinner"},
		{name: "no match still builds", doc: "none", find: "Lunch", with: "Dinner"},
		{name: "many with all", doc: "many", find: "Lunch", with: "Dinner", all: "true"},
		{name: "many across tabs with all", doc: "multitab", find: "Lunch", with: "Dinner", all: "true"},
		{name: "trailing newlines survive", doc: "tail", find: "Lunch\n", with: "Dinner\n"},
		{name: "a match in a table counts", doc: "cells", find: "rota", with: "menu"},
		{name: "a match in a header counts", doc: "headed", find: "Lunch", with: "Dinner"},
		{name: "a match in a footnote counts", doc: "noted", find: "Lunch", with: "Dinner"},
		{name: "quotes survive", doc: "one", find: "Lunch", with: `Dinner "chez Ada"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			standin, srv := replaceStandin(t)
			defer srv.Close()
			docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
			params := map[string]string{"docId": standin.doc(tc.doc), "find": tc.find, "with": tc.with}
			if tc.all != "" {
				params["all"] = tc.all
			}
			body, err := source.BuildBody(context.Background(), docs, "replace", params, "tok", replaceHome(t))
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("built body = %s: %v", body, err)
			}
			revision := map[string]string{
				"one": "rev-one", "none": "rev-none", "many": "rev-many",
				"multitab": "rev-multitab", "tail": "rev-tail",
				"cells": "rev-cells", "headed": "rev-headed", "noted": "rev-noted",
			}[tc.doc]
			golden := `{"requests":[{"replaceAllText":{"containsText":{"text":` + strconv.Quote(tc.find) +
				`,"matchCase":true},"replaceText":` + strconv.Quote(tc.with) + `}}],` +
				`"writeControl":{"requiredRevisionId":"` + revision + `"}}`
			if err := json.Unmarshal([]byte(golden), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("built body = %s, want %s", body, golden)
			}
			// The pre-read the count came from: the doc, read with the
			// call's credential, asking for its revision and its text.
			gotReqs := standin.requests()
			if len(gotReqs) != 1 {
				t.Fatalf("the stand-in heard %d requests, want the one pre-read", len(gotReqs))
			}
			pre := gotReqs[0]
			if pre.method != "GET" || pre.path != "/v1/documents/"+standin.doc(tc.doc) {
				t.Fatalf("pre-read = %s %s", pre.method, pre.path)
			}
			if pre.auth != "Bearer tok" {
				t.Fatalf("pre-read Authorization = %q, want the call's credential", pre.auth)
			}
			if pre.query.Get("fields") != "revisionId,tabs" || pre.query.Get("includeTabsContent") != "true" {
				t.Fatalf("pre-read fields = %q", pre.query.Get("fields"))
			}
		})
	}
}

// What the replace call refuses to build: nothing is posted, and the caller
// reads the script's own words. The stand-in hears the pre-read the count
// needed, and no batchUpdate; where the find never parsed, it hears nothing
// at all.
func TestGoogleDocsReplaceRefusals(t *testing.T) {
	standin, srv := replaceStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	home := replaceHome(t)
	for _, tc := range []struct {
		name  string
		doc   string
		find  string
		with  string
		all   string
		words string
		pre   bool // the refusal came after the pre-read
	}{
		{"many refused", "many", "Lunch", "Dinner", "", "3 matches; pass all=true to replace them all", true},
		{"many refused with all false", "many", "Lunch", "Dinner", "false", "3 matches; pass all=true to replace them all", true},
		{"many across tabs refused", "multitab", "Lunch", "Dinner", "", "2 matches; pass all=true to replace them all", true},
		{"find with NUL", "one", "Lu\x00nch", "Dinner", "", "contains a NUL character", false},
		{"replacement with NUL", "one", "Lunch", "Di\x00nner", "", "contains a NUL character", false},
		{"no find", "many", "", "Dinner", "", "find names no text to replace", false},
		{"no such doc", "missing", "Lunch", "Dinner", "", "could not be read", true},
		{"no revision id", "no-revision", "Lunch", "Dinner", "", "answered no revision id", true},
		{"no tabs", "no-tabs", "Lunch", "Dinner", "", "answered no tabs", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(standin.requests())
			params := map[string]string{"docId": standin.doc(tc.doc), "find": tc.find, "with": tc.with}
			if tc.all != "" {
				params["all"] = tc.all
			}
			_, err := source.BuildBody(context.Background(), docs, "replace", params, "tok", home)
			var refused *source.CallError
			if !errors.As(err, &refused) || refused.Reason != source.BuildRefused || !strings.Contains(err.Error(), tc.words) {
				t.Fatalf("err = %v, want %s containing %q", err, source.BuildRefused, tc.words)
			}
			want := 0
			if tc.pre {
				want = 1
			}
			past := standin.requests()[before:]
			if len(past) != want {
				t.Fatalf("the stand-in heard %d requests past the %d before, want %d", len(past), before, want)
			}
			for _, r := range past {
				if r.method == "POST" {
					t.Fatalf("the stand-in was sent %s %s: a refusal sends nothing", r.method, r.path)
				}
			}
		})
	}
}

// The built body posted: a replace sends its replaceAllText in one
// batchUpdate, and the answer's occurrencesChanged says how many changed,
// none where the find matched nothing.
func TestGoogleDocsReplaceRoundTrip(t *testing.T) {
	standin, srv := replaceStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	home := replaceHome(t)
	for _, tc := range []struct {
		doc     string
		changed string
	}{
		{"one", "1"},
		{"none", "0"},
	} {
		before := len(standin.requests())
		params := map[string]string{"docId": standin.doc(tc.doc), "find": "Lunch", "with": "Dinner"}
		body, err := source.BuildBody(context.Background(), docs, "replace", params, "tok", home)
		if err != nil {
			t.Fatal(err)
		}
		res, err := source.DoKeyedBody(context.Background(), docs, "replace", params, "tok", "", body)
		if err != nil {
			t.Fatal(err)
		}
		gotReqs := standin.requests()[before:]
		if len(gotReqs) != 2 {
			t.Fatalf("the stand-in heard %d requests, want the pre-read and the batchUpdate", len(gotReqs))
		}
		post := gotReqs[1]
		if post.method != "POST" || post.path != "/v1/documents/"+standin.doc(tc.doc)+":batchUpdate" {
			t.Fatalf("post = %s %s", post.method, post.path)
		}
		if post.auth != "Bearer tok" {
			t.Fatalf("post Authorization = %q", post.auth)
		}
		var got, want any
		if err := json.Unmarshal(post.body, &got); err != nil {
			t.Fatalf("posted body = %s: %v", post.body, err)
		}
		if err := json.Unmarshal(body, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("posted body = %s, want the built %s", post.body, body)
		}
		if want := `{"documentId":"` + standin.doc(tc.doc) + `","replies":[{"replaceAllText":{"occurrencesChanged":` + tc.changed + `}}]}`; string(res.Answer) != want {
			t.Fatalf("answer = %s, want %s", res.Answer, want)
		}
	}
}

// The pre-read's credential is no argument of curl's: an argument is
// readable by every process on the machine while curl runs, so the script
// hands the header to curl as a config line on its stdin, as the other
// packs' scripts hand theirs over. A stand-in curl first on PATH records
// its arguments and delegates to the real one.
func TestGoogleDocsReplaceHandsCurlNoToken(t *testing.T) {
	real, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed, and the replace build script reads the doc with it")
	}
	bin := t.TempDir()
	argv := filepath.Join(bin, "argv")
	scripttest.Write(t, filepath.Join(bin, "curl"),
		"printf '%s\n' \"$@\" > \""+argv+"\"\nexec \""+real+"\" \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	standin, srv := replaceStandin(t)
	defer srv.Close()
	docs := googleDeclaration(t, "google-docs.yaml", "https://docs.googleapis.com", srv.URL)
	if _, err := source.BuildBody(context.Background(), docs, "replace",
		map[string]string{"docId": standin.doc("one"), "find": "Lunch", "with": "Dinner"},
		"the-secret-token", replaceHome(t)); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), "the-secret-token") {
		t.Fatalf("curl was called with the credential in an argument: %s", recorded)
	}
	if got := standin.requests()[0].auth; got != "Bearer the-secret-token" {
		t.Fatalf("pre-read Authorization = %q, want the credential, handed over out of the arguments", got)
	}
}

// replaceHome installs the replace call's build script from the embedded pack,
// as a source add copies it, and returns the home it stands under. The
// script reads docs with curl and lifts their text with jq.
func replaceHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed, and the replace build script reads the doc with it")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the replace build script counts with it")
	}
	script, err := fs.ReadFile(packs.FS, "google-docs/google-docs/build-replace")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := source.WriteCopies(home, "google-docs", []source.FileCopy{{Program: "google-docs/build-replace", Data: script}}); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", "google-docs"))
	return home
}

// replaceDoc is one doc the replace stand-in serves: its id, the
// documents.get answer it is read with, and what its batchUpdate says
// changed.
type replaceDoc struct {
	id      string
	get     string
	changed int
}

// replaceDocs answers documents.get for text-bearing docs, and batchUpdate
// with the occurrences its recorded shape changed, keeping every request
// it was sent.
type replaceDocs struct {
	t    *testing.T
	mu   sync.Mutex
	got  []googleRequest
	docs map[string]replaceDoc
}

func replaceStandin(t *testing.T) (*replaceDocs, *httptest.Server) {
	t.Helper()
	para := func(runs ...string) string {
		var els []string
		for _, r := range runs {
			els = append(els, `{"textRun":{"content":`+strconv.Quote(r)+`}}`)
		}
		return `{"paragraph":{"elements":[` + strings.Join(els, ",") + `]}}`
	}
	tab := func(id, content string) string {
		return `{"tabProperties":{"tabId":` + strconv.Quote(id) + `},"documentTab":` + content + `}`
	}
	one := `{"revisionId":"rev-one","tabs":[` + tab("tab-one", `{"body":{"content":[`+
		para("Lunch", " places\n")+`,`+para("Friday we ship.\n")+`]}}`) + `]}`
	none := `{"revisionId":"rev-none","tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Dinner spots\n")+`]}}`) + `]}`
	many := `{"revisionId":"rev-many","tabs":[` + tab("tab-one", `{"body":{"content":[`+
		para("Lunch places\n")+`,`+para("Lunch menus\n")+`,`+para("Lunch specials\n")+`]}}`) + `]}`
	multitab := `{"revisionId":"rev-multitab","tabs":[` +
		tab("tab-one", `{"body":{"content":[`+para("Lunch in the first tab\n")+`]}}`) + `,` +
		tab("tab-two", `{"body":{"content":[`+para("Lunch in the second tab\n")+`]}}`) + `]}`
	tail := `{"revisionId":"rev-tail","tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Lunch\n")+`]}}`) + `]}`
	cells := `{"revisionId":"rev-cells","tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Notes\n")+
		`,{"table":{"tableRows":[{"tableCells":[{"content":[`+para("rota for Friday\n")+`]}]}]}}]}}`) + `]}`
	headed := `{"revisionId":"rev-headed","tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Notes\n")+
		`]},"headers":{"h1":{"content":[`+para("Lunch header\n")+`]}}}`) + `]}`
	noted := `{"revisionId":"rev-noted","tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Notes\n")+
		`]},"footnotes":{"f1":{"content":[`+para("Lunch footnote\n")+`]}}}`) + `]}`
	norev := `{"tabs":[` + tab("tab-one", `{"body":{"content":[`+para("Lunch\n")+`]}}`) + `]}`
	notabs := `{"revisionId":"rev-no-tabs"}`
	s := &replaceDocs{t: t, docs: map[string]replaceDoc{
		"one":         {id: "1One0123456789abcdefghijklmnopq", get: one, changed: 1},
		"none":        {id: "1None0123456789abcdefghijklmnop", get: none, changed: 0},
		"many":        {id: "1Many0123456789abcdefghijklmnop", get: many, changed: 3},
		"multitab":    {id: "1MultiTab0123456789abcdefghijklmn", get: multitab, changed: 2},
		"tail":        {id: "1Tail0123456789abcdefghijklmnopqr", get: tail, changed: 1},
		"cells":       {id: "1Cells0123456789abcdefghijklmno", get: cells, changed: 1},
		"headed":      {id: "1Headed0123456789abcdefghijklm", get: headed, changed: 1},
		"noted":       {id: "1Noted0123456789abcdefghijklmn", get: noted, changed: 1},
		"no-revision": {id: "1NoRev0123456789abcdefghijklmno", get: norev, changed: 0},
		"no-tabs":     {id: "1NoTabs0123456789abcdefghijklmn", get: notabs, changed: 0},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got = append(s.got, googleRequest{method: r.Method, path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization"), body: body})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		for _, d := range s.docs {
			switch {
			case r.Method == "GET" && r.URL.Path == "/v1/documents/"+d.id:
				io.WriteString(w, d.get)
				return
			case r.Method == "POST" && r.URL.Path == "/v1/documents/"+d.id+":batchUpdate":
				io.WriteString(w, `{"documentId":"`+d.id+`","replies":[{"replaceAllText":{"occurrencesChanged":`+strconv.Itoa(d.changed)+`}}],"writeControl":{"requiredRevisionId":"9"}}`)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":404,"message":"no such doc"}}`)
	}))
	return s, srv
}

// doc is the id of the replace stand-in's named doc; "missing" is one it has
// no answer for.
func (s *replaceDocs) doc(name string) string {
	if d, ok := s.docs[name]; ok {
		return d.id
	}
	return "1Missing0123456789abcdefghijklmno"
}

func (s *replaceDocs) requests() []googleRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]googleRequest(nil), s.got...)
}
