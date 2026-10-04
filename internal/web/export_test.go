package web

import "time"

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

// Age is how the pages say a duration.
func Age(d time.Duration) string { return age(d) }
