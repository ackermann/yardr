package web

import "time"

// Streams is how many /events streams the server is writing to.
func (s *Server) Streams() int { return int(s.streams.Load()) }

// SetStreamWrite sets how long a stream waits on a client that takes
// nothing before it drops it.
func (s *Server) SetStreamWrite(d time.Duration) { s.streamWrite = d }

// Age is how the pages say a duration.
func Age(d time.Duration) string { return age(d) }
