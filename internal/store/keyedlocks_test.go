package store

import (
	"runtime"
	"testing"
)

func TestKeyedLocksSerializeOnlyTheSameKey(t *testing.T) {
	t.Parallel()
	var locks keyedLocks
	first := locks.lock("ask one")
	started := make(chan struct{})
	secondHeld := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		close(started)
		unlock := locks.lock("ask one")
		close(secondHeld)
		unlock()
		close(secondDone)
	}()
	<-started
	for {
		locks.mu.Lock()
		refs := locks.locks["ask one"].refs
		locks.mu.Unlock()
		if refs == 2 {
			break
		}
		runtime.Gosched()
	}
	select {
	case <-secondHeld:
		t.Fatal("the second operation entered while the first held the key")
	default:
	}
	other := locks.lock("ask two")
	other()
	first()
	<-secondDone
	locks.mu.Lock()
	remaining := len(locks.locks)
	locks.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("key locks left behind: %d", remaining)
	}
}
