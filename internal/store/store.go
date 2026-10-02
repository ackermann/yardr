// Package store is the only package that talks to SurrealDB. Every mutation
// writes its event row in the same transaction as the change, so the event
// log explains every step.
package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	surrealdb "github.com/surrealdb/surrealdb.go"
	"github.com/surrealdb/surrealdb.go/pkg/connection"
	"github.com/surrealdb/surrealdb.go/pkg/connection/gorillaws"
	"github.com/surrealdb/surrealdb.go/pkg/logger"
	"github.com/surrealdb/surrealdb.go/pkg/models"
	"github.com/surrealdb/surrealdb.go/surrealcbor"
)

//go:embed schema.surql
var schema string

var (
	ErrNotFound = errors.New("not found")
	// ErrTaken means another session already owns the bead.
	ErrTaken = errors.New("bead already claimed")
)

// Sentinels THROWn from inside transactions and mapped back to Go errors.
const (
	throwTaken    = "yard:taken"
	throwNotFound = "yard:not-found"
)

type Store struct {
	db *surrealdb.DB
	// done is closed by Close; live goroutines kill their queries and exit
	// before the connection goes away (the SDK panics on a closed one).
	done chan struct{}
	wg   sync.WaitGroup
}

type Options struct {
	Endpoint, User, Pass, Namespace, Database string
}

// Open connects, signs in, selects the database and applies the schema.
func Open(ctx context.Context, o Options) (*Store, error) {
	u, err := url.ParseRequestURI(o.Endpoint)
	if err != nil {
		return nil, err
	}
	// The SDK's default logger writes to stdout, which would corrupt --json
	// output; route it to stderr and drop the noise live-query kills make.
	quiet := logger.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 4}))
	conf := connection.NewConfig(u)
	conf.Logger = quiet
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	// The websocket connection keeps its own stdout logger unless told.
	db, err := surrealdb.FromConnection(ctx, gorillaws.New(conf).Logger(quiet))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", o.Endpoint, err)
	}
	if _, err := db.SignIn(ctx, map[string]any{"user": o.User, "pass": o.Pass}); err != nil {
		db.Close(ctx)
		return nil, fmt.Errorf("sign in: %w", err)
	}
	// USE creates the namespace and database on first use; concurrent first
	// uses conflict, so it is retried like any other write.
	if _, err := retry(ctx, func() (struct{}, error) {
		return struct{}{}, db.Use(ctx, o.Namespace, o.Database)
	}); err != nil {
		db.Close(ctx)
		return nil, err
	}
	s := &Store{db: db, done: make(chan struct{})}
	if err := s.migrate(ctx); err != nil {
		db.Close(ctx)
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return s, nil
}

// migrate applies the schema only when its hash differs from the one last
// applied. Every CLI call opens the store, and concurrent DEFINE ... OVERWRITE
// statements conflict with each other.
//
// When several processes race on a fresh database, the losers' DEFINEs
// conflict; each loser re-checks the hash, sees the winner's, and stops.
func (s *Store) migrate(ctx context.Context) error {
	sum := sha256.Sum256([]byte(schema))
	hash := hex.EncodeToString(sum[:8])
	var lastErr error
	for attempt := 0; attempt < 40; attempt++ {
		cur, err := queryOne[[]string](ctx, s, `DEFINE TABLE IF NOT EXISTS meta SCHEMALESS;
			SELECT VALUE hash FROM meta:schema;`, nil, -1)
		if err == nil && len(cur) == 1 && cur[0] == hash {
			return nil
		}
		if err == nil {
			if _, err = s.exec(ctx, schema, nil); err == nil {
				_, err = s.exec(ctx, `UPSERT meta:schema SET hash = $hash, at = time::now()`,
					map[string]any{"hash": hash})
			}
			if err == nil {
				return nil
			}
		}
		if !strings.Contains(err.Error(), "can be retried") {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(10+rand.IntN(40)) * time.Millisecond):
		}
	}
	return lastErr
}

func (s *Store) Close(ctx context.Context) error {
	close(s.done)
	s.wg.Wait()
	return s.db.Close(ctx)
}

// exec runs a (possibly multi-statement) query and fails on any statement error.
func (s *Store) exec(ctx context.Context, q string, vars map[string]any) (*[]surrealdb.QueryResult[any], error) {
	return retry(ctx, func() (*[]surrealdb.QueryResult[any], error) {
		res, err := surrealdb.Query[any](ctx, s.db, q, vars)
		return res, mapErr(err)
	})
}

// retry re-runs fn while SurrealDB reports a retryable transaction conflict.
// Every query here is a single statement or one BEGIN/COMMIT block, so a
// retry re-runs the whole unit.
func retry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	delay := 5 * time.Millisecond
	for attempt := 0; ; attempt++ {
		v, err := fn()
		if err == nil || attempt == 8 || !strings.Contains(err.Error(), "can be retried") {
			return v, err
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(delay + time.Duration(rand.Int64N(int64(delay)))):
		}
		delay *= 2
	}
}

// queryOne runs q and decodes the result of statement idx into T. Negative
// idx counts from the end, ignoring a trailing COMMIT. Results arrive as raw
// CBOR because statements in one query return different shapes.
func queryOne[T any](ctx context.Context, s *Store, q string, vars map[string]any, idx int) (T, error) {
	var zero T
	res, err := retry(ctx, func() (*[]surrealdb.QueryResult[cbor.RawMessage], error) {
		res, err := surrealdb.Query[cbor.RawMessage](ctx, s.db, q, vars)
		return res, mapErr(err)
	})
	if err != nil {
		return zero, err
	}
	n := len(*res)
	if idx < 0 {
		if strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(q), ";")), "COMMIT") {
			n--
		}
		idx = n + idx
	}
	if idx < 0 || idx >= n {
		return zero, fmt.Errorf("query returned %d results, want index %d", n, idx)
	}
	var out T
	if err := surrealcbor.Unmarshal((*res)[idx].Result, &out); err != nil {
		return zero, fmt.Errorf("decode result %d: %w", idx, err)
	}
	return out, nil
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, throwTaken):
		return ErrTaken
	case strings.Contains(msg, throwNotFound):
		return ErrNotFound
	}
	return err
}

// Wake returns a channel that receives (coalesced) whenever beads, edges or
// sessions change. Notifications are a wake-up signal only: callers must
// re-read state, because live queries are not replayed after a disconnect.
func (s *Store) Wake(ctx context.Context) (<-chan struct{}, error) {
	out := make(chan struct{}, 1)
	for _, table := range []string{"bead", "dep", "session"} {
		ch, kill, err := s.live(ctx, table)
		if err != nil {
			return nil, err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer kill()
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.done:
					return
				case _, ok := <-ch:
					if !ok {
						return
					}
					select {
					case out <- struct{}{}:
					default:
					}
				}
			}
		}()
	}
	return out, nil
}

// TailEvents streams newly written events until ctx is cancelled.
func (s *Store) TailEvents(ctx context.Context) (<-chan Event, error) {
	ch, kill, err := s.live(ctx, "event")
	if err != nil {
		return nil, err
	}
	out := make(chan Event)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(out)
		defer kill()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case n, ok := <-ch:
				if !ok {
					return
				}
				if n.Action != connection.CreateAction {
					continue
				}
				m, ok := n.Result.(map[string]any)
				if !ok {
					continue
				}
				// Re-read through the normal projection so callers get the
				// same shape as Events().
				evs, err := queryOne[[]Event](ctx, s, `SELECT `+eventFields+` FROM $id`,
					map[string]any{"id": m["id"]}, 0)
				if err != nil || len(evs) == 0 {
					continue
				}
				select {
				case out <- evs[0]:
				case <-ctx.Done():
					return
				case <-s.done:
					return
				}
			}
		}
	}()
	return out, nil
}

func (s *Store) live(ctx context.Context, table string) (chan connection.Notification, func(), error) {
	id, err := surrealdb.Live(ctx, s.db, models.Table(table), false)
	if err != nil {
		return nil, nil, fmt.Errorf("live %s: %w", table, err)
	}
	ch, err := s.db.LiveNotifications(id.String())
	if err != nil {
		return nil, nil, err
	}
	kill := func() {
		kctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = surrealdb.Kill(kctx, s.db, id.String())
	}
	return ch, kill, nil
}
