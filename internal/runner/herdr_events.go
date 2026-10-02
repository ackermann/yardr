package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// herdrWakeEvents are the herdr events after which a session's agent may be
// gone. They only wake the dispatcher; Alive decides what actually ended.
var herdrWakeEvents = []string{"pane.exited", "pane.closed", "tab.closed", "pane.agent_detected"}

// SocketPath is herdr's API socket: $HERDR_SOCKET_PATH, else the default
// under the config directory.
func (h *Herdr) SocketPath() string {
	if h.Socket != "" {
		return h.Socket
	}
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "herdr", "herdr.sock")
}

// Wake subscribes to herdr's event stream and signals whenever a pane, tab
// or agent goes away. It reconnects with backoff while herdr is down and
// signals after every reconnect, since events may have been missed.
func (h *Herdr) Wake(ctx context.Context) (<-chan struct{}, error) {
	out := make(chan struct{}, 1)
	signal := func() {
		select {
		case out <- struct{}{}:
		default:
		}
	}
	go func() {
		backoff := time.Second
		first := true
		for ctx.Err() == nil {
			err := h.stream(ctx, func() {
				backoff = time.Second
				if !first {
					signal() // reconnected: we may have missed exits
				}
				first = false
			}, signal)
			if ctx.Err() != nil {
				return
			}
			if h.OnError != nil {
				h.OnError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
	return out, nil
}

// stream holds one subscription until it fails or ctx ends.
func (h *Herdr) stream(ctx context.Context, started, event func()) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", h.SocketPath())
	if err != nil {
		return fmt.Errorf("herdr events: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	subs := make([]map[string]string, len(herdrWakeEvents))
	for i, t := range herdrWakeEvents {
		subs[i] = map[string]string{"type": t}
	}
	req, _ := json.Marshal(map[string]any{
		"id": "yard-events", "method": "events.subscribe",
		"params": map[string]any{"subscriptions": subs},
	})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("herdr events: subscribe: %w", err)
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			Result *struct {
				Type string `json:"type"`
			} `json:"result"`
			Error *herdrError `json:"error"`
			Event string      `json:"event"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		switch {
		case msg.Error != nil:
			return fmt.Errorf("herdr events: %w", msg.Error)
		case msg.Result != nil && msg.Result.Type == "subscription_started":
			started()
		case msg.Event != "":
			event()
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("herdr events: %w", err)
	}
	return fmt.Errorf("herdr events: connection closed")
}
