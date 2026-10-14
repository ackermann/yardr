// Package rpc carries the store API over a local unix socket, so the one
// process that owns the store file serves every other yard process.
//
//	POST /call/<Method>   body: JSON array of the method's arguments (no ctx),
//	                      or for a call of Calls or OperatorCalls an object of
//	                      them by name
//	                      reply: {"result": ...} or {"error": {code, message}}
//	GET  /events?after=N  NDJSON stream of events with seq > N, then live
//	GET  /blobs/<hash>    the bytes of a blob (store.PutBlob), with their type
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

// method is one method of store.API as /call serves it.
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
	a := newAPI(st, writeTimeout)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /call/{method}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.methods[r.PathValue("method")]; !ok {
			writeJSON(w, noSuchMethod(r.PathValue("method")))
			return
		}
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, reply{Error: toWire(fmt.Errorf("arguments: %w", err))})
			return
		}
		a.call(w, r, r.PathValue("method"), body)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) { a.events(w, r, nil, 0) })
	mux.HandleFunc("GET /blobs/{hash}", a.blob)
	mux.HandleFunc("GET /wake", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := st.Wake(r.Context())
		stream(w, r, writeTimeout, func() (any, bool) {
			_, ok := <-ch
			return true, ok
		})
	})
	return mux
}

// api is the calls, the stream and the blobs of a store as they are served:
// on the socket (handler), and to a person's device on the yard's listener
// (UserServer), which lets in fewer calls and serves them no other way.
type api struct {
	st           *store.Store
	methods      map[string]method
	writeTimeout time.Duration
}

func newAPI(st *store.Store, writeTimeout time.Duration) *api {
	t := reflect.TypeOf((*store.API)(nil)).Elem()
	methods := map[string]method{}
	v := reflect.ValueOf(st)
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		if !notCallable[name] {
			methods[name] = method{name, v.MethodByName(name)}
		}
	}
	return &api{st: st, methods: methods, writeTimeout: writeTimeout}
}

// call makes the call name with the arguments body, an array of them or an
// object of them by name, and answers it.
func (a *api) call(w http.ResponseWriter, r *http.Request, name string, body json.RawMessage) {
	m, ok := a.methods[name]
	if !ok {
		writeJSON(w, noSuchMethod(name))
		return
	}
	ctx := r.Context()
	if id := r.Header.Get(SessionHeader); id != "" {
		ctx = store.WithSession(ctx, id)
	}
	result, more, err := invoke(ctx, m, body)
	if err != nil {
		writeJSON(w, reply{Error: toWire(err)})
		return
	}
	writeJSON(w, reply{Result: result, More: more})
}

func noSuchMethod(name string) reply {
	return reply{Error: &wireError{Code: "error", Message: "no such method " + name}}
}

// events streams the events after ?after=. still, when given, is asked
// every `every` and on an event that takes a way in away (endsAccess),
// before that event is sent: the stream ends when it says no. The socket
// gives none: whoever opened it can open it again.
func (a *api) events(w http.ResponseWriter, r *http.Request, still func() bool, every time.Duration) {
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil && r.URL.Query().Get("after") != "" {
		http.Error(w, "bad after", http.StatusBadRequest)
		return
	}
	ch, err := a.st.Subscribe(r.Context(), after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var tick <-chan time.Time
	if still != nil {
		t := time.NewTicker(every)
		defer t.Stop()
		tick = t.C
	}
	stream(w, r, a.writeTimeout, func() (any, bool) {
		for {
			select {
			case e, ok := <-ch:
				if ok && still != nil && endsAccess(e) && !still() {
					return nil, false
				}
				return e, ok
			case <-tick:
				if !still() {
					return nil, false
				}
			}
		}
	})
}

// endsAccess reports whether e may have taken a caller's way in away: a
// device removed, or a user with theirs. A stream replays the log, so the
// event may be of a device long since added again: it is a reason to ask,
// not the answer.
func endsAccess(e store.Event) bool {
	return e.Kind == "device_removed" || e.Kind == "user_removed"
}

func (a *api) blob(w http.ResponseWriter, r *http.Request) {
	ref, err := a.st.Blob(r.PathValue("hash"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A blob never changes, so its name is all a cache needs to know.
	w.Header().Set("Content-Type", ref.Type)
	w.Header().Set("ETag", `"`+ref.Hash+`"`)
	http.ServeFile(w, r, a.st.BlobPath(ref.Hash))
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

// invoke makes the call m with the arguments body and returns its result.
// A method that answers a list in parts (Conversation) returns, after the
// list, whether there is more of it: that is more, beside the result.
func invoke(ctx context.Context, m method, body json.RawMessage) (result any, more bool, err error) {
	t := m.fn.Type()
	var raw []json.RawMessage
	if len(body) > 0 && body[0] == '{' {
		var err error
		if raw, err = named(m.name, body); err != nil {
			return nil, false, err
		}
	} else if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("arguments: %w", err)
	}
	// An argument a later build added is one a client of before leaves out
	// (Calls): it is the zero value by position as it is by name, so a
	// yardr left running from before the upgrade still makes the call.
	if _, stable := argNames(m.name); stable {
		for len(raw) < t.NumIn()-1 {
			raw = append(raw, json.RawMessage("null"))
		}
	}
	// A list a later build added to the end of a call (Claim's via) is one
	// a daemon of before leaves out: it is the empty list, so the daemon
	// still claims until it has replaced itself.
	if t.IsVariadic() && len(raw) == t.NumIn()-2 {
		raw = append(raw, json.RawMessage("null"))
	}
	if len(raw) != t.NumIn()-1 {
		return nil, false, fmt.Errorf("want %d arguments, got %d", t.NumIn()-1, len(raw))
	}
	args := []reflect.Value{reflect.ValueOf(ctx)}
	for i, a := range raw {
		p := reflect.New(t.In(i + 1))
		if err := json.Unmarshal(a, p.Interface()); err != nil {
			if names, _ := argNames(m.name); len(body) > 0 && body[0] == '{' {
				return nil, false, fmt.Errorf("argument %q: %w", names[i], err)
			}
			return nil, false, fmt.Errorf("argument %d: %w", i+1, err)
		}
		args = append(args, p.Elem())
	}
	// The last argument of a variadic method came as its list.
	call := m.fn.Call
	if t.IsVariadic() {
		call = m.fn.CallSlice
	}
	out := call(args)
	if err, _ := out[len(out)-1].Interface().(error); err != nil {
		return nil, false, err
	}
	switch len(out) {
	case 2:
		return out[0].Interface(), false, nil
	case 3:
		return out[0].Interface(), out[1].Bool(), nil
	}
	return nil, false, nil
}

type reply struct {
	Result any `json:"result,omitempty"`
	// More is beside the result of a call that was asked for a part of a
	// list: there is more of it than the limit let through.
	More  bool       `json:"more,omitempty"`
	Error *wireError `json:"error,omitempty"`
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
