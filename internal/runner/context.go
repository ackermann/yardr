package runner

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
)

// Context is the context the session's agent holds, in tokens: the last
// use its own session file holds, by the kind's context source
// (agents.ContextSource). ok is false when the kind names no source, or
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
func lastUsage(pattern string, pick func(line []byte) (int, bool)) (int, bool, error) {
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return 0, false, err
	}
	f, err := os.Open(files[0])
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	// A line holds a whole message, tool output included: no fixed buffer.
	used, ok := 0, false
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if n, has := pick(line); has {
			used, ok = n, true
		}
		if err == io.EOF {
			return used, ok, nil
		}
		if err != nil {
			return 0, false, err
		}
	}
}
