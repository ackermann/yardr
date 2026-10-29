package yards

// SetMaxLargeReads replaces how many large request bodies are read at once.
// With none, every body past maxRequest is refused. A test pins the
// refusal with it; without it the bound takes four concurrent large
// uploads to reach.
func (rc *Receiver) SetMaxLargeReads(n int) {
	rc.mu.Lock()
	rc.maxLargeReads = n
	rc.mu.Unlock()
}
