// Package hooks receives webhooks from the tools humans use and turns each
// delivery into one `hook` event carrying a source-neutral Envelope.
//
// yard only receives here: nothing is routed, answered or linked. Each
// source has an Adapter that proves a delivery came from that source and
// normalizes it; adding a source (Jira) is adding an Adapter.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Rejections. The receiver answers ErrUnauthorized with 401 and
// ErrBadRequest with 400; neither leaves an event.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrBadRequest   = errors.New("bad request")
)

// MaxText bounds Envelope.Text: enough to decide whether to look closer,
// never the whole body (that is in the stored payload).
const MaxText = 500

// Envelope is a delivery in the shape routing reads, whatever the source.
type Envelope struct {
	Source   string   `json:"source"`
	Type     string   `json:"type"`             // comment, issue, ... (lowercase)
	Action   string   `json:"action,omitempty"` // create, update, remove, ...
	Actor    Party    `json:"actor"`
	Thread   Thread   `json:"thread"`
	Mentions []string `json:"mentions"` // ids (or profile keys) of people mentioned
	Assignee string   `json:"assignee,omitempty"`
	Text     string   `json:"text,omitempty"`
	URL      string   `json:"url,omitempty"`
	Delivery string   `json:"delivery,omitempty"`
	// Entity is the id of what the delivery's text belongs to when that
	// text persists across deliveries (an issue's description), so routing
	// counts only the mentions the entity did not carry before. Empty when
	// every delivery is new text (a comment, a message).
	Entity string `json:"entity,omitempty"`
	// Fields are what the source says about where the delivery belongs,
	// for subscriptions to filter on (--where team=ENG): team, project,
	// repo, label. A field may hold several values (labels, or a team's
	// key and name); a filter matches any of them.
	Fields map[string][]string `json:"fields,omitempty"`
}

type Party struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Thread is what a delivery belongs to: the issue a comment is on, or the
// issue itself.
type Thread struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Map is the envelope as event data.
func (e *Envelope) Map() map[string]any {
	if e.Mentions == nil {
		e.Mentions = []string{}
	}
	raw, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// Adapter is one source.
type Adapter interface {
	// Verify proves the delivery came from the source, from r's headers and
	// the raw body, using the hook's secret. It returns the source's
	// delivery id, used to drop redeliveries ("" when the source has none).
	// Errors wrap ErrUnauthorized or ErrBadRequest.
	Verify(r *http.Request, body, secret []byte, now time.Time) (delivery string, err error)
	// Normalize turns a verified body into an Envelope. The headers are
	// those of the delivery: some sources name the event only there
	// (X-GitHub-Event).
	Normalize(h http.Header, body []byte) (*Envelope, error)
	// UniqueBodies reports whether no two deliveries from the source share
	// a body (Linear signs a timestamp into each), so a body seen before is
	// a redelivery. A source whose senders may repeat a body on purpose
	// (a cron job's "backup ok") dedupes on delivery ids alone.
	UniqueBodies() bool
}

// DedupeKeys identify a verified delivery to hook name from src: the
// sha256 of its body, under the source, when bodies are unique; and the
// source's delivery id, under the hook, when it sent one (ids are chosen
// per endpoint by token senders). The store drops a delivery any of whose
// keys it has recorded.
func DedupeKeys(name, src string, ad Adapter, body []byte, delivery string) []string {
	var keys []string
	if ad.UniqueBodies() {
		sum := sha256.Sum256(body)
		keys = append(keys, "body\x00"+src+"\x00"+hex.EncodeToString(sum[:]))
	}
	if delivery != "" {
		keys = append(keys, "delivery\x00"+name+"\x00"+delivery)
	}
	return keys
}

// Adapters by source name.
var Adapters = map[string]Adapter{
	"github": GitHub{},
	"linear": Linear{},
	"token":  Token{},
}

// Sources lists the source names, sorted.
func Sources() []string {
	out := make([]string, 0, len(Adapters))
	for k := range Adapters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// truncate collapses whitespace and shortens s to MaxText runes.
func truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= MaxText {
		return s
	}
	return string(r[:MaxText-1]) + "…"
}

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, fmt.Sprintf(format, args...))
}

func unauthorized(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnauthorized, fmt.Sprintf(format, args...))
}
