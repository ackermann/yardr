// Package rpc carries the store API over a local unix socket, so the one
// process that owns the store file serves every other yard process.
//
//	POST /call/<Method>   body: JSON array of the method's arguments (no ctx)
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
	"net/http"
	"reflect"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// PIDHeader carries the server's pid on every reply.
const PIDHeader = "X-Yardr-Pid"

// WriteReplacing refuses a call without running it because the server is
// about to exec a newer build; the client retries against that build.
func WriteReplacing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(reply{Error: &wireError{Code: "replacing", Message: "yardr server is replacing itself"}})
}

// streams and lifecycle methods are not callable through /call.
var notCallable = map[string]bool{"Subscribe": true, "TailEvents": true, "Wake": true, "Close": true}

// Handler serves st.
func Handler(st *store.Store, log *slog.Logger) http.Handler {
	api := reflect.TypeOf((*store.API)(nil)).Elem()
	methods := map[string]reflect.Value{}
	v := reflect.ValueOf(st)
	for i := 0; i < api.NumMethod(); i++ {
		name := api.Method(i).Name
		if !notCallable[name] {
			methods[name] = v.MethodByName(name)
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
		stream(w, r, func(enc *json.Encoder) bool {
			e, ok := <-ch
			return ok && enc.Encode(e) == nil
		})
	})
	mux.HandleFunc("GET /wake", func(w http.ResponseWriter, r *http.Request) {
		ch, _ := st.Wake(r.Context())
		stream(w, r, func(enc *json.Encoder) bool {
			_, ok := <-ch
			return ok && enc.Encode(true) == nil
		})
	})
	return mux
}

// stream writes NDJSON until next reports false or the client goes away.
func stream(w http.ResponseWriter, r *http.Request, next func(*json.Encoder) bool) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	enc := json.NewEncoder(w)
	for r.Context().Err() == nil && next(enc) {
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func invoke(ctx context.Context, m reflect.Value, r *http.Request) (any, error) {
	t := m.Type()
	var raw []json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	if len(raw) != t.NumIn()-1 {
		return nil, fmt.Errorf("want %d arguments, got %d", t.NumIn()-1, len(raw))
	}
	args := []reflect.Value{reflect.ValueOf(ctx)}
	for i, a := range raw {
		p := reflect.New(t.In(i + 1))
		if err := json.Unmarshal(a, p.Interface()); err != nil {
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}
		args = append(args, p.Elem())
	}
	out := m.Call(args)
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
	Code    string `json:"code"` // not-found, taken, ambiguous, replacing, error
	Message string `json:"message"`
}

func toWire(err error) *wireError {
	code := "error"
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = "not-found"
	case errors.Is(err, store.ErrTaken):
		code = "taken"
	case errors.Is(err, store.ErrAmbiguous):
		code = "ambiguous"
	}
	return &wireError{Code: code, Message: err.Error()}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
