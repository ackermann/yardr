package runner

import "context"

// Context is the context the session's agent holds, in tokens: the last
// use its own session file holds, by the kind's context source
// (store.ContextSource). ok is false when the kind names no source, or
// the file says no use yet (no reply, or none with numbers). The file is
// found as the model source's is (see Model).
func (h *Herdr) Context(ctx context.Context, handle map[string]any) (used int, ok bool, err error) {
	kind, id, err := h.sessionKind(ctx, handle)
	if err != nil || id == "" {
		return 0, false, err
	}
	if kind.Model == nil || kind.Context == nil {
		return 0, false, nil
	}
	return lastUsage(kind.Model.Pattern(h.agentHome(kind), id), kind.Context.UsageIn)
}

// lastUsage reads the session file matching pattern line by line and
// returns the use the last line pick reads holds; ok is false when there
// is no file yet or no line holds one.
func lastUsage(pattern string, pick func(line []byte) (int, bool)) (used int, ok bool, err error) {
	path, err := sessionFile(pattern)
	if path == "" || err != nil {
		return 0, false, err
	}
	err = eachLine(path, func(line []byte) bool {
		if n, has := pick(line); has {
			used, ok = n, true
		}
		return false
	})
	if err != nil {
		return 0, false, err
	}
	return used, ok, nil
}
