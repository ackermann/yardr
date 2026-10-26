package hooks

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Token accepts any sender holding the hook's shared token, for tests and
// ad hoc senders (a script, a cron job):
//
//	Authorization: Bearer <token>
//	Yard-Delivery: <id>   required; repeats of an id are dropped
//
// A JSON object body may set type, action, text, url and actor {id, name};
// any other body becomes a "message" whose text is the body. Every other
// top-level string of the body rides as a field, for a makes line to match
// on and a standalone bead to carry as data; numbers and objects stay out,
// since a request's fields are strings, as do the names the dispatcher
// reads for routing (store.TokenReservedFields), which a sender must not
// steer.
//
// The id is required because nothing else tells a resend from a new
// delivery: the token signs no time, so a body that comes again may be a
// replay or a cron job saying "backup ok" once more, and only the sender
// can say which. The receiver refuses a delivery with no id, as it does of
// any source whose bodies repeat (Receiver.serve), so Verify returns the
// header as it is.
type Token struct{}

func (Token) Verify(r *http.Request, body, secret []byte, now time.Time) (string, error) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), secret) != 1 {
		return "", unauthorized("bad or missing bearer token")
	}
	return r.Header.Get("Yard-Delivery"), nil
}

// Bodies repeat: a token sender may send the same body again on
// purpose, and only a repeated Yard-Delivery marks a redelivery.
func (Token) Bodies() BodyScope { return BodiesRepeat }

func (Token) Normalize(_ http.Header, body []byte) (*Envelope, error) {
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
	// The sender's own fields ride beside the five known keys, each
	// truncated as text is. No allowlist holds them: they are the
	// sender's request data, whatever the sender names them. The routing
	// names stay out: which train, repository, number and head an event
	// belongs to is the dispatcher's to read, never the sender's to say,
	// so a token delivery is about no bead but the one its line makes.
	var rest map[string]any
	if json.Unmarshal(body, &rest) != nil {
		return e, nil
	}
	for k, v := range rest {
		s, ok := v.(string)
		switch {
		case !ok:
			continue
		case k == "type" || k == "action" || k == "text" || k == "url" || k == "actor":
			continue
		case slices.Contains(store.TokenReservedFields, k):
			continue
		}
		if e.Fields == nil {
			e.Fields = map[string][]string{}
		}
		e.Fields[k] = []string{truncate(s)}
	}
	return e, nil
}
