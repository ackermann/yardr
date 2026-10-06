package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"yardr.dev/yardr/internal/dispatch"
)

// call sends one request to herdr's API socket and returns its result.
// Start and StartCrew use it for what carries a pane's environment
// (tab.create, workspace.create): the values travel in the request body on
// the socket, which only this user can open, instead of as --env arguments
// of the herdr CLI, where ps showed them (an env_file's tokens among them)
// to every local user. The protocol is the one Wake speaks: one JSON object
// per line, {"id","method","params"} answered by {"id","result"} or
// {"id","error"}. Errors are classified as run classifies the CLI's: an
// error object herdr sends is the request's failure, a socket that cannot
// be opened or answers nothing is the runtime's.
func (h *Herdr) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", h.SocketPath())
	if err != nil {
		return nil, dispatch.Runtime(fmt.Errorf("herdr %s: %w", method, err))
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	req, err := json.Marshal(map[string]any{"id": "yard-" + method, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, dispatch.Runtime(fmt.Errorf("herdr %s: %w", method, err))
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *herdrError     `json:"error"`
		}
		// Anything that is not an answer (an event, a line that is not
		// JSON) is skipped: the answer carries a result or an error.
		if json.Unmarshal(sc.Bytes(), &resp) != nil || (resp.Result == nil && resp.Error == nil) {
			continue
		}
		if resp.Error != nil {
			if resp.Error.outage() {
				return nil, dispatch.Runtime(resp.Error)
			}
			return nil, resp.Error
		}
		return resp.Result, nil
	}
	err = sc.Err()
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	return nil, dispatch.Runtime(fmt.Errorf("herdr %s: no answer: %w", method, err))
}

// envMap is env (KEY=VALUE, as the runners build it) in the shape herdr's
// API takes a pane's environment.
func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		out[k] = v
	}
	return out
}
