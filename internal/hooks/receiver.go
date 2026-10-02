package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Remembering delivery ids: longer than any timestamp window, so a replay
// is either stale or remembered; bounded so a flood cannot grow it.
const (
	DedupeTTL  = 10 * time.Minute
	DedupeKeep = 100_000
)

// Store is what the receiver needs of the yard.
type Store interface {
	GetHook(ctx context.Context, name string) (*store.Hook, error)
	RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte) (uint64, error)
}

// Receiver serves POST /hooks/<name> and nothing else. It is meant for its
// own listener, apart from the store socket and BDP.
type Receiver struct {
	st  Store
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	seen     map[string]time.Time
	order    []seenID // insertion order, for expiry and the bound
	rejected map[string]int
}

type seenID struct {
	key string
	at  time.Time
}

func NewReceiver(st Store, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Receiver{st: st, log: log, now: time.Now, seen: map[string]time.Time{}, rejected: map[string]int{}}
}

// SetClock replaces the clock (tests).
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

// Rejected counts refused (and duplicate) deliveries by reason.
func (rc *Receiver) Rejected() map[string]int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make(map[string]int, len(rc.rejected))
	for k, v := range rc.rejected {
		out[k] = v
	}
	return out
}

func (rc *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{name}", rc.serve)
	return mux
}

func (rc *Receiver) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h, err := rc.st.GetHook(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		rc.reject(w, name, "unknown_hook", http.StatusNotFound, err)
		return
	} else if err != nil {
		rc.reject(w, name, "store", http.StatusInternalServerError, err)
		return
	}
	ad, ok := Adapters[h.Source]
	if !ok {
		rc.reject(w, name, "unknown_source", http.StatusInternalServerError, errors.New("no adapter for source "+h.Source))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, store.MaxHookPayload))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		rc.reject(w, name, "too_large", http.StatusRequestEntityTooLarge, err)
		return
	} else if err != nil {
		rc.reject(w, name, "read", http.StatusBadRequest, err)
		return
	}
	secret, err := os.ReadFile(h.SecretFile)
	secret = bytes.TrimSpace(secret)
	if err == nil && len(secret) == 0 {
		// An empty key would verify anyone's signature.
		err = errors.New("secret file is empty")
	}
	if err != nil {
		rc.reject(w, name, "secret", http.StatusInternalServerError, err)
		return
	}
	delivery, err := ad.Verify(r, body, secret, rc.now())
	if err != nil {
		code, reason := http.StatusUnauthorized, "unauthorized"
		if errors.Is(err, ErrBadRequest) {
			code, reason = http.StatusBadRequest, "bad_request"
		}
		rc.reject(w, name, reason, code, err)
		return
	}
	// Dedupe after Verify, so only the source can claim a delivery id.
	key := name + "\x00" + delivery
	if delivery != "" && !rc.remember(key) {
		rc.count("duplicate")
		rc.log.Info("hook delivery dropped as duplicate", "hook", name, "delivery", delivery)
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true})
		return
	}
	env, err := ad.Normalize(body)
	if err != nil {
		rc.forget(key)
		rc.reject(w, name, "bad_request", http.StatusBadRequest, err)
		return
	}
	env.Delivery = delivery
	seq, err := rc.st.RecordHook(r.Context(), name, env.Map(), body)
	if err != nil {
		rc.forget(key) // not recorded, so a retry must get through
		rc.reject(w, name, "store", http.StatusInternalServerError, err)
		return
	}
	// Linear treats any non-200 response as a failed delivery and retries it.
	// The event is already durable here, so acknowledge it with 200.
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq})
}

func (rc *Receiver) reject(w http.ResponseWriter, name, reason string, code int, err error) {
	rc.count(reason)
	rc.log.Warn("hook delivery rejected", "hook", name, "reason", reason, "status", code, "err", err)
	http.Error(w, http.StatusText(code), code)
}

func (rc *Receiver) count(reason string) {
	rc.mu.Lock()
	rc.rejected[reason]++
	rc.mu.Unlock()
}

// remember records key and reports whether it was new.
func (rc *Receiver) remember(key string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	t := rc.now()
	for len(rc.order) > 0 && (len(rc.order) >= DedupeKeep || t.Sub(rc.order[0].at) > DedupeTTL) {
		old := rc.order[0]
		rc.order = rc.order[1:]
		if rc.seen[old.key] == old.at {
			delete(rc.seen, old.key)
		}
	}
	if _, dup := rc.seen[key]; dup {
		return false
	}
	rc.seen[key] = t
	rc.order = append(rc.order, seenID{key, t})
	return true
}

func (rc *Receiver) forget(key string) {
	rc.mu.Lock()
	delete(rc.seen, key)
	rc.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
