package web

import (
	"context"
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
