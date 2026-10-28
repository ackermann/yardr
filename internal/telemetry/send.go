package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Timeout bounds one batch request, from the first byte out to the last in.
// The daemon posts synchronously in its pass, so an endpoint that hangs
// holds the pass for this long, once a day, and no longer.
const Timeout = 10 * time.Second

// The classes of a batch that was not taken: a fixed vocabulary, so a
// warning says why in a word and never in the words of an error that might
// carry the request or the answer.
const (
	ClassTimeout = "timeout"       // no answer in time
	ClassNetwork = "network"       // the endpoint was not reached
	ClassStatus  = "status"        // answered with a status that is not 2xx
	ClassQuota   = "quota_limited" // a 2xx whose answer names quota-limited entries
	ClassRequest = "request"       // the request could not be made
)

// Refused is a batch the endpoint did not take. Status is the HTTP status
// that answered, 0 when none did, and Class says why in a word. Its text
// is those two and nothing else: not the request, the answer, the install
// id or the key, so a log line made from it carries none of them.
type Refused struct {
	Status int
	Class  string
}

func (r *Refused) Error() string {
	if r.Status == 0 {
		return "telemetry batch not accepted: " + r.Class
	}
	return fmt.Sprintf("telemetry batch not accepted: %s (HTTP %d)", r.Class, r.Status)
}

// answerMax bounds how much of the answer is read: it is looked at for
// quota_limited and for nothing else.
const answerMax = 64 << 10

// Post sends req to url in one request and returns nil when the endpoint
// took it: a 2xx whose answer names no quota-limited entries. Anything else
// is a *Refused. The url is the compiled Endpoint but in a test. A redirect
// is not followed, since the endpoint is compiled in and the batch goes
// there and nowhere else; it is a refusal with its status.
func Post(ctx context.Context, url string, req *Request, timeout time.Duration) error {
	body, err := json.Marshal(req)
	if err != nil {
		return &Refused{Class: ClassRequest}
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &Refused{Class: ClassRequest}
	}
	hreq.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(hreq)
	if err != nil {
		return &Refused{Class: classOf(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &Refused{Status: resp.StatusCode, Class: ClassStatus}
	}
	// A 2xx can still say the project dropped the events for want of
	// quota: those are not accepted, and the cursor must not pass them.
	// A quota_limited of any other shape than a list is held to the same,
	// since a batch the project dropped is not one to forget.
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, answerMax))
	var limited struct {
		QuotaLimited json.RawMessage `json:"quota_limited"`
	}
	if err := json.Unmarshal(answer, &limited); err == nil && len(limited.QuotaLimited) > 0 {
		value := bytes.TrimSpace(limited.QuotaLimited)
		var entries []json.RawMessage
		if len(value) == 0 || value[0] != '[' || json.Unmarshal(value, &entries) != nil || len(entries) > 0 {
			return &Refused{Status: resp.StatusCode, Class: ClassQuota}
		}
	}
	return nil
}

// classOf is the class of an error from the request itself.
func classOf(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return ClassTimeout
	}
	return ClassNetwork
}
