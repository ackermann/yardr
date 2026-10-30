package dispatch

import "errors"

// The kinds of failure a runner's Start can report, and how the
// dispatcher tells them apart (see route in dispatch.go): the
// runtime's own, a session's configuration refusing it, and one
// bead's alone.

// RuntimeError marks a Start error as the runtime's (herdr missing or down,
// a workspace it could not create), not the bead's. Every bead of the group
// would fail the same way, so the dispatcher does not count it against the
// bead: it reports the group's runtime down and backs off instead.
type RuntimeError struct{ Err error }

func (e *RuntimeError) Error() string { return e.Err.Error() }
func (e *RuntimeError) Unwrap() error { return e.Err }

// Runtime wraps err as a RuntimeError; nil stays nil.
func Runtime(err error) error {
	if err == nil {
		return nil
	}
	return &RuntimeError{Err: err}
}

// IsRuntime reports whether err is, or wraps, a RuntimeError.
func IsRuntime(err error) bool {
	var re *RuntimeError
	return errors.As(err, &re)
}

// BlockedError marks a Start error as the session's own configuration
// refusing it (an env_file that is missing or open to others): no retry
// fixes it until a human does, and every session of the same group or
// crew member would fail alike, so it is reported rather than counted against
// a bead.
type BlockedError struct{ Err error }

func (e *BlockedError) Error() string { return e.Err.Error() }
func (e *BlockedError) Unwrap() error { return e.Err }

// Blocked wraps err as a BlockedError; nil stays nil.
func Blocked(err error) error {
	if err == nil {
		return nil
	}
	return &BlockedError{Err: err}
}

// IsBlocked reports whether err is, or wraps, a BlockedError.
func IsBlocked(err error) bool {
	var be *BlockedError
	return errors.As(err, &be)
}

// BeadBlockedError marks a Start failure that needs this bead to wait for a
// person, rather than another retry or holding every bead in the group. The
// dispatcher ends the unstarted session, records session_blocked with the
// reason, and holds the bead until it is released.
type BeadBlockedError struct{ Err error }

func (e *BeadBlockedError) Error() string { return e.Err.Error() }
func (e *BeadBlockedError) Unwrap() error { return e.Err }

// BeadBlocked wraps err as a bead-specific blocked Start error; nil stays nil.
func BeadBlocked(err error) error {
	if err == nil {
		return nil
	}
	return &BeadBlockedError{Err: err}
}

func isBeadBlocked(err error) bool {
	var be *BeadBlockedError
	return errors.As(err, &be)
}
