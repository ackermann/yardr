package config

// OperationalError marks a failure of the yard's own file under a call —
// a disk or permission failure, a file open to others, a link where the
// yard writes through none, a depot checkout that cannot be read — apart
// from a refusal of what was sent. Its words name the file, as an
// operator at a terminal needs; a device that is no operator's is told
// that the yard could not do it instead, and the full words are logged
// for the operator. A refusal reads whole: the write's refusal of a
// value the file cannot hold is never marked.
//
// It wraps with Unwrap, so errors.As finds it through the call path,
// however many times the path wraps it with %w.
type OperationalError struct {
	Err error
}

// Error is the failure's words, unchanged.
func (e *OperationalError) Error() string { return e.Err.Error() }

// Unwrap is the failure.
func (e *OperationalError) Unwrap() error { return e.Err }
