package yards

import "io"

// ReadHead reads the head of a request body as the receiver does, so a test
// pins that a small body holds near what it sent, not the whole head.
func ReadHead(r io.Reader) ([]byte, error) { return readHead(r) }

// SetMaxLargeReads replaces how many bodies past maxRequest are read at
// once before their signatures are checked. With none, every such body is
// refused; a smaller one takes no slot. A test pins the refusal with it;
// without it the bound takes four concurrent uploads to reach.
func (rc *Receiver) SetMaxLargeReads(n int) {
	rc.mu.Lock()
	rc.maxLargeReads = n
	rc.mu.Unlock()
}
