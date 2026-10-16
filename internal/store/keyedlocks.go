package store

import (
	"fmt"
	"sync"
)

// keyedLocks serializes operations on one named object without blocking
// unrelated asks while a hook registration waits on the source service.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

func askLockKey(id string, seq uint64) string { return fmt.Sprintf("ask\x00%s\x00%d", id, seq) }

func hookLockKey(name string) string { return "hook\x00" + name }
