package store

import (
	"context"
	"time"
)

type Event struct {
	ID    string         `json:"id"`
	Bead  *string        `json:"bead,omitempty"`
	Kind  string         `json:"kind"`
	Actor string         `json:"actor"`
	Data  map[string]any `json:"data,omitempty"`
	At    time.Time      `json:"at"`
}

const eventFields = `record::id(id) AS id, IF bead THEN record::id(bead) END AS bead, kind, actor, data, at`

// Events returns the most recent events, oldest first; bead filters if set.
func (s *Store) Events(ctx context.Context, bead string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT * FROM (SELECT ` + eventFields + ` FROM event`
	vars := map[string]any{"limit": limit}
	if bead != "" {
		q += ` WHERE bead = type::record('bead', $bead)`
		vars["bead"] = bead
	}
	q += ` ORDER BY at DESC LIMIT $limit) ORDER BY at`
	return queryOne[[]Event](ctx, s, q, vars, 0)
}

// LogEvent records an event that has no accompanying state change, such as
// a dispatcher decision.
func (s *Store) LogEvent(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	q := `CREATE event SET kind = $kind, actor = $actor, data = $data`
	vars := map[string]any{"kind": kind, "actor": actor, "data": data}
	if bead != "" {
		q += `, bead = type::record('bead', $bead)`
		vars["bead"] = bead
	}
	_, err := s.exec(ctx, q, vars)
	return err
}
