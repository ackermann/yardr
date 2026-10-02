package hooks

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Token accepts any sender holding the hook's shared token, for tests and
// ad hoc senders (a script, a cron job):
//
//	Authorization: Bearer <token>
//	Yard-Delivery: <id>   optional; repeats of an id are dropped
//
// A JSON object body may set type, action, text, url and actor {id, name};
// any other body becomes a "message" whose text is the body.
type Token struct{}

func (Token) Verify(r *http.Request, body, secret []byte, now time.Time) (string, error) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), secret) != 1 {
		return "", unauthorized("bad or missing bearer token")
	}
	return r.Header.Get("Yard-Delivery"), nil
}

// UniqueBodies is false: a token sender may send the same body again on
// purpose, and only a repeated Yard-Delivery marks a redelivery.
func (Token) UniqueBodies() bool { return false }

func (Token) Normalize(body []byte) (*Envelope, error) {
	e := &Envelope{Source: "token", Type: "message", Mentions: []string{}}
	var p struct {
		Type   string `json:"type"`
		Action string `json:"action"`
		Text   string `json:"text"`
		URL    string `json:"url"`
		Actor  Party  `json:"actor"`
	}
	if json.Unmarshal(body, &p) != nil {
		e.Text = truncate(string(body))
		return e, nil
	}
	e.Type = strings.ToLower(firstOf(p.Type, e.Type))
	e.Action, e.Text, e.URL, e.Actor = p.Action, truncate(p.Text), p.URL, p.Actor
	return e, nil
}
