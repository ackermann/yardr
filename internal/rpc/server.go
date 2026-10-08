// Package rpc carries the store API over a local unix socket, so the one
// process that owns the store file serves every other yard process.
//
//	POST /call/<Method>   body: JSON array of the method's arguments (no ctx),
//	                      or for a call of Calls an object of them by name
//	                      reply: {"result": ...} or {"error": {code, message}}
//	GET  /events?after=N  NDJSON stream of events with seq > N, then live
//	GET  /wake            a newline after every committed write
//	GET  /ping            liveness
//
// Requests carry the caller's build revision and replies the server's, plus
// its pid, so a server can replace itself for a newer client and a client
// can explain a server it cannot talk to (see internal/version).
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// StreamWriteTimeout bounds each write on an event or wake stream. A reader
// that has stopped taking (a watcher paused for good) fills the socket and
// would otherwise hold the handler in Write forever: past a drain's settle,
// so the store would close under calls that are still answered. The same
// as the settle, so a stalled stream is gone by the time the settle gives up.
const StreamWriteTimeout = 10 * time.Second

// PIDHeader carries the server's pid on every reply.
const PIDHeader = "X-Yardr-Pid"

// ConnContext is for the http.Server that serves Handler: it records on
// every connection's context the pid of the process that opened it, so the
// store can check a bead write as the session whose pane it came from (see
// store.Expect). A connection whose peer the OS does not tell is served
// with no caller.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if pid, err := peer.PID(c); err == nil {
		return store.WithCaller(ctx, pid)
	}
	return ctx
}

// WriteReplacing refuses a call without running it because the server is
// about to exec a newer build; the client retries against that build.
func WriteReplacing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(reply{Error: &wireError{Code: "replacing", Message: "yardr server is replacing itself"}})
}

// streams and lifecycle methods are not callable through /call.
var notCallable = map[string]bool{"Subscribe": true, "TailEvents": true, "Wake": true, "Close": true}

// oldMethods are the names a method of store.API had until this release,
// when the crew were the residents and a depot was a rig; none for one that
// had no other. A client started before the upgrade that does not replace
// itself (the web view, a chat left open) still calls them, and gets what
// the method of today gives.
func oldMethods(name string) []string {
	switch {
	case name == "Crew":
		return []string{"Residents"}
	case strings.Contains(name, "Crew"):
		return []string{strings.Replace(name, "Crew", "Resident", 1)}
	case strings.Contains(name, "Depot"):
		return []string{strings.Replace(name, "Depot", "Rig", 1)}
	}
	return nil
}

// method is one method of store.API as /call serves it: under name, which
// is the one it has today whatever the request called it.
type method struct {
	name string
	fn   reflect.Value
}

// Handler serves st.
func Handler(st *store.Store, log *slog.Logger) http.Handler {
	return handler(st, log, StreamWriteTimeout)
}

// handler is Handler with the stream write timeout given, for tests.
func handler(st *store.Store, log *slog.Logger, writeTimeout time.Duration) http.Handler {
	api := reflect.TypeOf((*store.API)(nil)).Elem()
	methods := map[string]method{}
	v := reflect.ValueOf(st)
	for i := 0; i < api.NumMethod(); i++ {
		name := api.Method(i).Name
		if !notCallable[name] {
			methods[name] = method{name, v.MethodByName(name)}
			for _, old := range oldMethods(name) {
				methods[old] = methods[name]
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /call/{method}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := methods[r.PathValue("method")]
		if !ok {
			writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method " + r.PathValue("method")}})
			return
		}
		result, err := invoke(r.Context(), m, r)
		if err != nil {
			writeJSON(w, reply{Error: toWire(err)})
			return
		}
		writeJSON(w, reply{Result: result})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
		if err != nil && r.URL.Query().Get("after") != "" {
			http.Error(w, "bad after", http.StatusBadRequest)
			return
		}
		ch, err := st.Subscribe(r.Context(), after)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stream(w, r, writeTimeout, func() (any, bool) {
			e, ok := <-ch
			return e, ok
		})
	})
	mux.HandleFunc("GET /wake", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := st.Wake(r.Context())
		stream(w, r, writeTimeout, func() (any, bool) {
			_, ok := <-ch
			return true, ok
		})
	})
	return mux
}

// stream writes what next yields as NDJSON until next reports false, the
// client goes away, or a write waits longer than writeTimeout on a reader
// that has stopped taking. The deadline is set per write, once next has
// yielded: a stream that is merely quiet is never cut off.
func stream(w http.ResponseWriter, r *http.Request, writeTimeout time.Duration, next func() (any, bool)) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	enc := json.NewEncoder(w)
	for r.Context().Err() == nil {
		v, ok := next()
		if !ok {
			return
		}
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if enc.Encode(v) != nil || rc.Flush() != nil {
			return
		}
	}
}

func invoke(ctx context.Context, m method, r *http.Request) (any, error) {
	t := m.fn.Type()
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	var raw []json.RawMessage
	if len(body) > 0 && body[0] == '{' {
		var err error
		if raw, err = named(m.name, body); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	if len(raw) != t.NumIn()-1 {
		return nil, fmt.Errorf("want %d arguments, got %d", t.NumIn()-1, len(raw))
	}
	args := []reflect.Value{reflect.ValueOf(ctx)}
	for i, a := range raw {
		p := reflect.New(t.In(i + 1))
		if err := json.Unmarshal(a, p.Interface()); err != nil {
			if names := Calls[m.name]; len(body) > 0 && body[0] == '{' {
				return nil, fmt.Errorf("argument %q: %w", names[i], err)
			}
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}
		args = append(args, p.Elem())
	}
	out := m.fn.Call(args)
	if err, _ := out[len(out)-1].Interface().(error); err != nil {
		return nil, err
	}
	if len(out) == 2 {
		return out[0].Interface(), nil
	}
	return nil, nil
}

type reply struct {
	Result any        `json:"result,omitempty"`
	Error  *wireError `json:"error,omitempty"`
}

type wireError struct {
	Code    string `json:"code"` // not-found, taken, at-limit, conflict, ambiguous, replacing, error
	Message string `json:"message"`
}

func toWire(err error) *wireError {
	code := "error"
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = "not-found"
	case errors.Is(err, store.ErrTaken):
		code = "taken"
	case errors.Is(err, store.ErrAtLimit):
		code = "at-limit"
	case errors.Is(err, store.ErrConflict):
		code = "conflict"
	case errors.Is(err, store.ErrAmbiguous):
		code = "ambiguous"
	}
	return &wireError{Code: code, Message: err.Error()}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
