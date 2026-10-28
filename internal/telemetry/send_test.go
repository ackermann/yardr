package telemetry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sendFixture is a request with an install id and key whose words a failure
// must never repeat.
func sendFixture() *Request {
	return &Request{APIKey: "phc_SENTINEL-key", Batch: []Event{{
		Name: DailyEvent, DistinctID: "SENTINEL-install-id", Timestamp: "2026-10-28T00:00:00Z",
		UUID: "0e3c8b2a-1432-4a51-9f39-1bc7a1b1d001", Properties: map[string]any{"version": "development"},
	}}}
}

// Post takes a 2xx that names nothing quota-limited, sends the request as
// JSON, and refuses everything else with a status and a class in a word.
// What the refusal says carries nothing of the request or of the answer.
func TestPost(t *testing.T) {
	t.Parallel()
	const answerWords = "SENTINEL-answer-words"
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   *Refused // nil: taken
	}{
		{"taken", 200, `{"status":"Ok"}`, nil},
		{"taken with an empty list", 200, `{"status":"Ok","quota_limited":[]}`, nil},
		{"taken, the old answer", 200, `{"status":1}`, nil},
		{"taken, an answer that is no object", 200, `[1,2]`, nil},
		{"taken with no answer", 200, ``, nil},
		{"taken with a page", 200, `<html>` + answerWords + `</html>`, nil},
		{"quota limited", 200, `{"status":"Ok","quota_limited":["events"]}`, &Refused{200, ClassQuota}},
		{"quota limited, null is not a list", 200, `{"status":"Ok","quota_limited":null}`, &Refused{200, ClassQuota}},
		{"quota limited, not as a list", 200, `{"quota_limited":true}`, &Refused{200, ClassQuota}},
		{"bad request", 400, answerWords, &Refused{400, ClassStatus}},
		{"unauthorized", 401, `{"detail":"` + answerWords + `"}`, &Refused{401, ClassStatus}},
		{"too many requests", 429, answerWords, &Refused{429, ClassStatus}},
		{"unavailable", 503, answerWords, &Refused{503, ClassStatus}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			type received struct {
				method, contentType string
				body                []byte
			}
			seen := make(chan received, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seen <- received{r.Method, r.Header.Get("Content-Type"), body}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			req := sendFixture()
			err := Post(t.Context(), srv.URL, req, time.Minute)
			want, _ := json.Marshal(req)
			if got := <-seen; got.method != http.MethodPost || got.contentType != "application/json" || string(got.body) != string(want) {
				t.Errorf("the listener received %s as %q with %q, want a POST of %q as application/json", got.method, got.contentType, got.body, want)
			}
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Post = %v, want it taken", err)
				}
				return
			}
			var refused *Refused
			if !errors.As(err, &refused) || *refused != *tc.want {
				t.Fatalf("Post = %#v, want %#v", err, tc.want)
			}
			for _, secret := range []string{answerWords, req.APIKey, req.Batch[0].DistinctID} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal %q carries %q", err, secret)
				}
			}
		})
	}
}

// A redirect is not followed: the batch goes to the endpoint the build
// compiles in and nowhere else, so another address that a redirect names
// receives nothing.
func TestPostDoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	err := Post(t.Context(), srv.URL, sendFixture(), time.Minute)
	var refused *Refused
	if !errors.As(err, &refused) || refused.Status != http.StatusTemporaryRedirect || refused.Class != ClassStatus {
		t.Fatalf("Post = %#v", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the redirect target received %d requests", n)
	}
}

// An endpoint that is not there is the network's, one that does not answer
// in time the timeout's, and neither names the address it tried.
func TestPostClassifiesWhatHappenedBeforeAnAnswer(t *testing.T) {
	t.Parallel()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	err := Post(t.Context(), gone.URL, sendFixture(), time.Minute)
	var refused *Refused
	if !errors.As(err, &refused) || *refused != (Refused{0, ClassNetwork}) {
		t.Fatalf("Post to a closed listener = %#v", err)
	}
	if strings.Contains(err.Error(), gone.URL) {
		t.Errorf("the refusal %q names the address", err)
	}

	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)
	err = Post(t.Context(), hung.URL, sendFixture(), 50*time.Millisecond)
	if !errors.As(err, &refused) || *refused != (Refused{0, ClassTimeout}) {
		t.Fatalf("Post to a listener that never answers = %#v", err)
	}
}
