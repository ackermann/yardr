package standin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
}
