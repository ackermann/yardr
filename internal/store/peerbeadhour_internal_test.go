package store

import (
	"runtime"
	"testing"
	"time"
)

// One link's batches with beads run one at a time, from the read of its hour
// to the write that counts it: another link's, and a batch with no bead,
// wait for nothing. A batch with a file holds the file lock, as before the
// beads had a lock, and a batch with both holds both, with nothing left
// behind.
func TestLockPeerBatch(t *testing.T) {
	t.Parallel()
	var s Store
	mail, bead, file := PeerMessage{Kind: PeerMail}, PeerMessage{Kind: PeerBead}, PeerMessage{Kind: PeerFile}
	first := s.lockPeerBatch("north", []PeerMessage{mail, bead})
	held, done := make(chan struct{}), make(chan struct{})
	go func() {
		unlock := s.lockPeerBatch("north", []PeerMessage{bead})
		close(held)
		unlock()
		close(done)
	}()
	// Once it has queued behind the first it is waiting for it: the lock
	// counts the callers it has before they wait.
	for fuse := time.Now().Add(time.Minute); ; runtime.Gosched() {
		s.peerBeadLocks.mu.Lock()
		l := s.peerBeadLocks.locks["north"]
		queued := l != nil && l.refs == 2
		s.peerBeadLocks.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(fuse) {
			t.Fatal("the second batch of the link's beads never queued behind the first")
		}
	}
	select {
	case <-held:
		t.Fatal("a second batch of the link's beads was taken while the first held the link")
	default:
	}
	s.lockPeerBatch("south", []PeerMessage{bead})()
	s.lockPeerBatch("north", []PeerMessage{mail})()
	first()
	<-held
	<-done

	for name, c := range map[string]struct {
		msgs []PeerMessage
		file bool
	}{
		"a bead":             {[]PeerMessage{bead}, false},
		"a file":             {[]PeerMessage{file}, true},
		"a file and a bead":  {[]PeerMessage{bead, file}, true},
		"mail":               {[]PeerMessage{mail}, false},
		"a file, mail, bead": {[]PeerMessage{file, mail, bead}, true},
	} {
		unlock := s.lockPeerBatch("north", c.msgs)
		if free := s.peerFileMu.TryLock(); free == c.file {
			t.Fatalf("a batch with %s: the file lock was free = %v, want %v", name, free, !c.file)
		} else if free {
			s.peerFileMu.Unlock()
		}
		unlock()
		if !s.peerFileMu.TryLock() {
			t.Fatalf("a batch with %s kept the file lock", name)
		}
		s.peerFileMu.Unlock()
	}
	s.peerBeadLocks.mu.Lock()
	left := len(s.peerBeadLocks.locks)
	s.peerBeadLocks.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d locks of links left behind", left)
	}
}
