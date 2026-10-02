package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LinearWindow is how far webhookTimestamp may be from our clock. Linear
// recommends a minute; anything older is treated as a replay.
const LinearWindow = time.Minute

// Linear verifies and normalizes Linear webhooks
// (https://linear.app/developers/webhooks):
//
//	Linear-Signature  hex HMAC-SHA256 of the raw body with the signing secret
//	Linear-Delivery   UUID of this delivery
//	webhookTimestamp  (body) unix milliseconds when it was sent
type Linear struct{}

func (Linear) Verify(r *http.Request, body, secret []byte, now time.Time) (string, error) {
	sig, err := hex.DecodeString(strings.TrimSpace(r.Header.Get("Linear-Signature")))
	if err != nil || len(sig) == 0 {
		return "", unauthorized("missing or malformed Linear-Signature")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", unauthorized("Linear-Signature does not match")
	}
	// The timestamp is checked only after the signature, so it is one the
	// sender signed.
	var p struct {
		WebhookTimestamp json.Number `json:"webhookTimestamp"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", bad("body is not JSON: %v", err)
	}
	ms, err := p.WebhookTimestamp.Int64()
	if err != nil {
		return "", unauthorized("missing webhookTimestamp")
	}
	if d := now.Sub(time.UnixMilli(ms)); d > LinearWindow || d < -LinearWindow {
		return "", unauthorized("webhookTimestamp is %s away from now", d.Round(time.Second))
	}
	return r.Header.Get("Linear-Delivery"), nil
}

// linearPayload is the part of a Linear data-change webhook we read.
type linearPayload struct {
	Action      string          `json:"action"`
	Type        string          `json:"type"`
	Actor       *linearUser     `json:"actor"`
	URL         string          `json:"url"`
	Data        linearData      `json:"data"`
	UpdatedFrom json.RawMessage `json:"updatedFrom"`
}

type linearUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type linearData struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Identifier  string          `json:"identifier"`
	URL         string          `json:"url"`
	Body        string          `json:"body"`
	BodyData    json.RawMessage `json:"bodyData"`
	Description string          `json:"description"`
	UserID      string          `json:"userId"`
	User        *linearUser     `json:"user"`
	IssueID     string          `json:"issueId"`
	Issue       *linearData     `json:"issue"`
	AssigneeID  string          `json:"assigneeId"`
	Assignee    *linearUser     `json:"assignee"`
	Team        *linearNamed    `json:"team"`
	Project     *linearNamed    `json:"project"`
	Labels      []linearNamed   `json:"labels"`
}

type linearNamed struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (Linear) Normalize(body []byte) (*Envelope, error) {
	var p linearPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, bad("linear payload: %v", err)
	}
	if p.Type == "" {
		return nil, bad("linear payload has no type")
	}
	d := p.Data
	e := &Envelope{Source: "linear", Type: strings.ToLower(p.Type), Action: p.Action, URL: p.URL}
	switch {
	case p.Actor != nil:
		e.Actor = Party{ID: p.Actor.ID, Name: p.Actor.Name}
	case d.User != nil:
		e.Actor = Party{ID: d.User.ID, Name: d.User.Name}
	default:
		e.Actor = Party{ID: d.UserID}
	}
	switch e.Type {
	case "comment":
		if d.Issue != nil {
			e.Fields = linearFields(d.Issue)
		}
		e.Thread = Thread{ID: d.IssueID, URL: stripFragment(p.URL)}
		if d.Issue != nil {
			e.Thread = Thread{ID: d.Issue.ID, Title: d.Issue.Title, URL: d.Issue.URL}
			if e.Thread.URL == "" {
				e.Thread.URL = stripFragment(p.URL)
			}
		}
		if e.Thread.ID == "" {
			e.Thread.ID = d.ID // a comment on something other than an issue
		}
		e.Text = truncate(d.Body)
		e.Mentions = linearMentions(d.Body, d.BodyData)
	case "issue":
		e.Fields = linearFields(&d)
		e.Thread = Thread{ID: d.ID, Title: d.Title, URL: firstOf(d.URL, p.URL)}
		e.Assignee = d.AssigneeID
		if e.Assignee == "" && d.Assignee != nil {
			e.Assignee = d.Assignee.ID
		}
		if p.Action == "update" {
			e.Text = changed(p.UpdatedFrom)
		} else {
			e.Text = truncate(d.Description)
		}
		e.Mentions = linearMentions(d.Description, nil)
	default:
		// Other entities pass through with what every payload has, so a
		// subscription on them needs no adapter change.
		e.Thread = Thread{ID: d.ID, Title: d.Title, URL: p.URL}
	}
	if e.Mentions == nil {
		e.Mentions = []string{}
	}
	return e, nil
}

// linearFields exposes an issue's team (key and name), project and labels
// for subscription filters. Payloads only carry them when Linear includes
// the nested objects; ids alone are not useful to filter on by hand.
func linearFields(d *linearData) map[string][]string {
	f := map[string][]string{}
	if d.Team != nil {
		for _, v := range []string{d.Team.Key, d.Team.Name} {
			if v != "" {
				f["team"] = append(f["team"], v)
			}
		}
	}
	if d.Project != nil && d.Project.Name != "" {
		f["project"] = []string{d.Project.Name}
	}
	for _, l := range d.Labels {
		if l.Name != "" {
			f["label"] = append(f["label"], l.Name)
		}
	}
	if len(f) == 0 {
		return nil
	}
	return f
}

// linearProfile matches a mention in Linear markdown: mentions are written
// as the user's profile URL, which Linear renders as @name.
var linearProfile = regexp.MustCompile(`https://linear\.app/[^/\s]+/profiles/([A-Za-z0-9][A-Za-z0-9._-]*)`)

// linearMentions lists who a body mentions: user ids from the rich-text
// document (bodyData, when the payload has it) and profile keys from
// markdown profile URLs (the url key, not the id: the markdown has no ids).
func linearMentions(md string, doc json.RawMessage) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, id := range docMentions(doc) {
		add(id)
	}
	for _, m := range linearProfile.FindAllStringSubmatch(md, -1) {
		add(strings.TrimRight(m[1], ".")) // a sentence may end right after it
	}
	return out
}

// docMentions walks a ProseMirror document (an object, or a JSON string of
// one, as the GraphQL API returns it) for user mention nodes.
func docMentions(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	type node struct {
		Type    string            `json:"type"`
		Attrs   map[string]any    `json:"attrs"`
		Content []json.RawMessage `json:"content"`
	}
	var out []string
	var walk func(json.RawMessage, int)
	walk = func(r json.RawMessage, depth int) {
		var n node
		if depth > 64 || json.Unmarshal(r, &n) != nil {
			return
		}
		if n.Type == "suggestion_userMentions" {
			if id, _ := n.Attrs["id"].(string); id != "" {
				out = append(out, id)
			}
		}
		for _, c := range n.Content {
			walk(c, depth+1)
		}
	}
	walk(raw, 0)
	return out
}

// changed summarizes an update by the fields it changed.
func changed(updatedFrom json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(updatedFrom, &m) != nil || len(m) == 0 {
		return ""
	}
	var keys []string
	for k := range m {
		if k != "updatedAt" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return truncate("changed " + strings.Join(keys, ", "))
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
