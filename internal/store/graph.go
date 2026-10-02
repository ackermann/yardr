package store

import (
	"context"
	"fmt"
	"time"
)

type Dep struct {
	From      string    `json:"from"` // must close first (for kind 'blocks')
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

const depFields = `record::id(in) AS from, record::id(out) AS to, kind, created_at`

// AddDep relates from -> to. For kind 'blocks', `to` is not ready until
// `from` closes. The edge gets its own revision.
func (s *Store) AddDep(ctx context.Context, from, to, kind, actor string) error {
	if from == to {
		return fmt.Errorf("a bead cannot depend on itself")
	}
	q := `BEGIN;
	LET $e = (RELATE (type::record('bead', $from))->dep->(type::record('bead', $to))
		SET kind = $kind, created_by = $actor)[0];
	fn::mint($e.id, $actor);
	-- Both beads' representations list or count the edge.
	fn::mint(type::record('bead', $from), $actor);
	fn::mint(type::record('bead', $to), $actor);
	CREATE event SET bead = type::record('bead', $to), kind = 'dep_added', actor = $actor,
		data = { from: $from, kind: $kind };
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"from": from, "to": to, "kind": kind, "actor": actor})
	return err
}

// RemoveDep deletes the edge, leaving a tombstone version that cites its
// final revision. Removing an edge that does not exist is a no-op.
func (s *Store) RemoveDep(ctx context.Context, from, to, kind, actor string) error {
	q := `BEGIN;
	LET $gone = (DELETE dep WHERE in = type::record('bead', $from) AND out = type::record('bead', $to)
		AND kind = $kind RETURN BEFORE);
	FOR $e IN $gone { fn::tombstone($e.id, $e, $actor) };
	IF array::len($gone) > 0 {
		fn::mint(type::record('bead', $from), $actor);
		fn::mint(type::record('bead', $to), $actor);
		CREATE event SET bead = type::record('bead', $to), kind = 'dep_removed', actor = $actor,
			data = { from: $from, kind: $kind };
	};
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"from": from, "to": to, "kind": kind, "actor": actor})
	return err
}

// Deps returns every edge touching the bead, in either direction.
func (s *Store) Deps(ctx context.Context, id string) ([]Dep, error) {
	return queryOne[[]Dep](ctx, s, `SELECT `+depFields+` FROM dep
		WHERE in = type::record('bead', $id) OR out = type::record('bead', $id)
		ORDER BY created_at`, map[string]any{"id": id}, 0)
}

// OpenBlockers lists open beads that block id.
func (s *Store) OpenBlockers(ctx context.Context, id string) ([]string, error) {
	return queryOne[[]string](ctx, s, `SELECT VALUE record::id(in) FROM dep
		WHERE out = type::record('bead', $id) AND kind = 'blocks' AND in.status = 'open'`,
		map[string]any{"id": id}, 0)
}

type Note struct {
	Bead   string    `json:"bead"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

func (s *Store) AddNote(ctx context.Context, id, text, actor string) error {
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };
	CREATE note SET bead = type::record('bead', $id), author = $actor, text = $text;
	-- The bead's representation counts its notes (bd's comment_count).
	fn::mint(type::record('bead', $id), $actor);
	CREATE event SET bead = type::record('bead', $id), kind = 'noted', actor = $actor;
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": id, "text": text, "actor": actor})
	return err
}

func (s *Store) Notes(ctx context.Context, id string) ([]Note, error) {
	return queryOne[[]Note](ctx, s, `SELECT record::id(bead) AS bead, author, text, at FROM note
		WHERE bead = type::record('bead', $id) ORDER BY at`, map[string]any{"id": id}, 0)
}

type Rig struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Base string `json:"base"`
}

func (s *Store) PutRig(ctx context.Context, r Rig) error {
	_, err := s.exec(ctx, `UPSERT type::record('rig', $name) SET path = $path, base = $base`,
		map[string]any{"name": r.Name, "path": r.Path, "base": r.Base})
	return err
}

func (s *Store) Rigs(ctx context.Context) ([]Rig, error) {
	return queryOne[[]Rig](ctx, s, `SELECT record::id(id) AS name, path, base FROM rig ORDER BY name`, nil, 0)
}

func (s *Store) GetRig(ctx context.Context, name string) (*Rig, error) {
	rs, err := queryOne[[]Rig](ctx, s, `SELECT record::id(id) AS name, path, base FROM type::record('rig', $name)`,
		map[string]any{"name": name}, 0)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, fmt.Errorf("rig %s: %w", name, ErrNotFound)
	}
	return &rs[0], nil
}

// SnapBead and SnapDep are the full records the BDP server serves, with the
// actor of their latest version for attribution.
type SnapBead struct {
	Bead
	LastActor *string `json:"last_actor"`
	Notes     int     `json:"notes"`
}

type SnapDep struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy *string   `json:"created_by"`
	LastActor *string   `json:"last_actor"`
}

type Snapshot struct {
	Beads []SnapBead
	Deps  []SnapDep
}

// lastActor is the actor of a record's newest version. No LIMIT: on
// SurrealDB 3.2 a LIMIT inside a per-row subquery only yields rows for the
// first outer row (TestLastActorOnEveryRow).
const lastActor = `(SELECT actor, at FROM version WHERE target = $parent.id ORDER BY at DESC)[0].actor AS last_actor`

// Snapshot reads every bead and edge in one transaction, so a reader sees a
// single consistent state of the yard.
func (s *Store) Snapshot(ctx context.Context) (*Snapshot, error) {
	type both struct {
		Beads []SnapBead `json:"beads"`
		Deps  []SnapDep  `json:"deps"`
	}
	q := `BEGIN;
	LET $b = (SELECT ` + beadFields + `, ` + lastActor + `,
		(SELECT count() FROM note WHERE bead = $parent.id GROUP ALL)[0].count OR 0 AS notes FROM bead);
	LET $d = (SELECT record::id(id) AS id, record::id(in) AS from, record::id(out) AS to, kind,
		revision, created_at, created_by, ` + lastActor + ` FROM dep);
	RETURN { beads: $b, deps: $d };
	COMMIT;`
	r, err := queryOne[both](ctx, s, q, nil, -1)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Beads: r.Beads, Deps: r.Deps}, nil
}
