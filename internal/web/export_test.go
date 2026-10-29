package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// SetDescribeServer replaces the server probe GET /health reads, for tests;
// it returns what restores the real one.
func SetDescribeServer(fn func(context.Context, *config.Config) (server.Info, bool)) (restore func()) {
	prev := describeServer
	describeServer = fn
	return func() { describeServer = prev }
}

// CSRF is the value the forms of a view that acts carry, for a test that
// sends a write the way a browser does.
func (s *Server) CSRF() string { return s.csrfValue() }

// Streams is how many /events streams are open.
func (s *Server) Streams() int { return int(s.streams.Load()) }

// SetStreamWrite sets how long a stream waits on a client that takes
// nothing before it drops it.
func (s *Server) SetStreamWrite(d time.Duration) { s.streamWrite = d }

// SetStreamLimits sets how many streams are open at once: max from off
// loopback and again from loopback, perAddr from one address off loopback.
func (s *Server) SetStreamLimits(max, perAddr int) { s.streamMax, s.streamPerAddr = max, perAddr }

// StreamKey is what a stream from remote is counted under.
func StreamKey(remote string) string { return streamKey(remote) }

// SetLimitsEvery sets how long one ask of the limits source stands.
func (s *Server) SetLimitsEvery(d time.Duration) { s.limitsEvery = d }

// Age is how the pages say a duration.
func Age(d time.Duration) string { return age(d) }

// AgeJWTKeys moves the JWT key cache's fetch and attempt back by d, for
// tests of the hourly refresh.
func (s *Server) AgeJWTKeys(d time.Duration) {
	s.jwt.mu.Lock()
	defer s.jwt.mu.Unlock()
	s.jwt.fetched = s.jwt.fetched.Add(-d)
	s.jwt.attempted = s.jwt.attempted.Add(-d)
}

// DropJWTKeys empties the JWT key cache, for tests of the answer with no
// usable keys.
func (s *Server) DropJWTKeys() {
	s.jwt.mu.Lock()
	defer s.jwt.mu.Unlock()
	s.jwt.keys = map[string]any{}
}

// FreshSession verifies the request's gateway token as a request to the
// view would and asks the freshness question of what it verified: the status
// a refusal answered with (200 when the token verified), and the answer. On
// a view that acts as a user (ActAs) the request holds the view's cookie, so
// that it acts as that user and the question has to be answered without a
// gateway.
func (s *Server) FreshSession(r *http.Request, since time.Time) (status int, fresh bool) {
	w := httptest.NewRecorder()
	r, ok := s.verifyJWT(w, r)
	if !ok {
		return w.Code, false
	}
	if s.token != nil {
		r = r.WithContext(context.WithValue(r.Context(), tokenHeldKey{}, true))
	}
	return http.StatusOK, s.freshSession(r.Context(), since)
}

// Unified is old and new as the history's diff.
func Unified(old, new string) string { return unified(old, new) }

// Updated is what an update's event says it changed.
func Updated(data map[string]any) string { return updated(data) }

// ClosedToday are the beads that closed on now's date, the newest first,
// at most ten.
func ClosedToday(beads []store.Bead, now time.Time) []HomeClosed { return closedToday(beads, now) }

// ProviderOf is the subscription a herdr group or crew member with cfg
// burns, resolved against as without asking.
func ProviderOf(runner string, cfg map[string]any, as []limits.Answerer) string {
	return providerOf(runner, cfg, as)
}
