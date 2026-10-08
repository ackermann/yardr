package dispatch

import (
	"time"

	"yardr.dev/yardr/internal/store"
)

// ListedSession is a session as `yardr session list` and the web view's
// /sessions print it: with what it waits for when its agent stopped on a
// usage limit.
type ListedSession struct {
	store.Session
	ResetWait *ResetWaiting `json:"reset_wait,omitempty"`
}

// ResetWaiting is the wait of a session whose agent stopped on a usage
// limit, as its handle has it (ResetWait): until when the daemon waits for
// the window, and the model its group starts no further session with
// meanwhile, "" for the agent's own.
type ResetWaiting struct {
	Until  time.Time `json:"until"`
	Window string    `json:"window,omitempty"`
	Model  string    `json:"model"`
}

// Listed is ss as a list of sessions shows them. An ended session has no
// wait.
func Listed(ss []store.Session) []ListedSession {
	listed := make([]ListedSession, len(ss))
	for i, s := range ss {
		listed[i].Session = s
		if until, window, model, ok := ResetWait(s.Handle); ok && s.EndedAt == nil {
			listed[i].ResetWait = &ResetWaiting{Until: until, Window: window, Model: model}
		}
	}
	return listed
}
