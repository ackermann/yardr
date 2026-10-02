package store

import (
	"context"
	"fmt"
	"time"
)

// Group is whoever works a stage. Runner names the implementation (herdr,
// exec, fake, ...); Config is runner-specific and opaque to the engine.
type Group struct {
	Name   string         `json:"name"`
	Runner string         `json:"runner"`
	Config map[string]any `json:"config"`
	Limit  int            `json:"limit"`
}

const groupFields = `record::id(id) AS name, runner, config, limit`

func (s *Store) PutGroup(ctx context.Context, g Group) error {
	if g.Config == nil {
		g.Config = map[string]any{}
	}
	if g.Limit < 1 {
		g.Limit = 1
	}
	_, err := s.exec(ctx, `UPSERT type::record('group', $name) SET runner = $runner, config = $config, limit = $limit`,
		map[string]any{"name": g.Name, "runner": g.Runner, "config": g.Config, "limit": g.Limit})
	return err
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	return queryOne[[]Group](ctx, s, `SELECT `+groupFields+` FROM group ORDER BY name`, nil, 0)
}

func (s *Store) GetGroup(ctx context.Context, name string) (*Group, error) {
	gs, err := queryOne[[]Group](ctx, s, `SELECT `+groupFields+` FROM type::record('group', $name)`,
		map[string]any{"name": name}, 0)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, fmt.Errorf("group %s: %w", name, ErrNotFound)
	}
	return &gs[0], nil
}

func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	_, err := s.exec(ctx, `DELETE route WHERE group = type::record('group', $name);
		DELETE type::record('group', $name)`, map[string]any{"name": name})
	return err
}

// Route sends open, unblocked beads in Stage to Group. Nil fields match any.
type Route struct {
	ID       string  `json:"id"`
	Stage    string  `json:"stage"`
	Type     *string `json:"type,omitempty"`
	Rig      *string `json:"rig,omitempty"`
	Label    *string `json:"label,omitempty"`
	Group    string  `json:"group"`
	Priority int     `json:"priority"`
}

const routeFields = `record::id(id) AS id, stage, type, IF rig THEN record::id(rig) END AS rig,
	label, record::id(group) AS group, priority`

func (s *Store) AddRoute(ctx context.Context, r Route) (*Route, error) {
	set := "stage = $stage, group = type::record('group', $group), priority = $priority"
	vars := map[string]any{"stage": r.Stage, "group": r.Group, "priority": r.Priority}
	if r.Type != nil {
		set += ", type = $type"
		vars["type"] = *r.Type
	}
	if r.Rig != nil {
		set += ", rig = type::record('rig', $rig)"
		vars["rig"] = *r.Rig
	}
	if r.Label != nil {
		set += ", label = $label"
		vars["label"] = *r.Label
	}
	q := `BEGIN;
	IF !record::exists(type::record('group', $group)) { THROW '` + throwNotFound + `' };
	RETURN (CREATE route SET ` + set + ` RETURN VALUE record::id(id))[0];
	COMMIT;`
	id, err := queryOne[string](ctx, s, q, vars, -1)
	if err != nil {
		return nil, err
	}
	r.ID = id
	return &r, nil
}

func (s *Store) Routes(ctx context.Context) ([]Route, error) {
	return queryOne[[]Route](ctx, s, `SELECT `+routeFields+` FROM route ORDER BY stage, priority DESC`, nil, 0)
}

func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE type::record('route', $id)`, map[string]any{"id": id})
	return err
}

type Session struct {
	ID        string         `json:"id"`
	Bead      string         `json:"bead"`
	Group     string         `json:"group"`
	Stage     string         `json:"stage"`
	State     string         `json:"state"`
	Handle    map[string]any `json:"handle,omitempty"`
	Error     *string        `json:"error,omitempty"`
	Stopped   bool           `json:"stopped"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
}

const sessionFields = `record::id(id) AS id, record::id(bead) AS bead, record::id(group) AS group,
	stage, state, handle, error, stopped, started_at, ended_at`

// Claim atomically assigns an unowned bead to group and opens a session.
// It returns ErrTaken if the bead was claimed, changed stage, or became
// blocked or held since the caller read it.
func (s *Store) Claim(ctx context.Context, beadID, group, stage, actor string) (*Session, error) {
	q := `BEGIN;
	LET $bead = type::record('bead', $id);
	LET $sess = (CREATE session SET bead = $bead, group = type::record('group', $group), stage = $stage)[0];
	LET $won = (UPDATE $bead SET group = type::record('group', $group), session = $sess.id
		WHERE group = NONE AND stage = $stage AND ` + readyWhere + ` RETURN AFTER);
	IF array::len($won) = 0 { THROW '` + throwTaken + `' };
	fn::mint($bead, $actor);
	CREATE event SET bead = $bead, kind = 'claimed', actor = $actor,
		data = { group: $group, stage: $stage, session: record::id($sess.id) };
	RETURN record::id($sess.id);
	COMMIT;`
	id, err := queryOne[string](ctx, s, q, map[string]any{
		"id": beadID, "group": group, "stage": stage, "actor": actor}, -1)
	if err != nil {
		return nil, err
	}
	return s.GetSession(ctx, id)
}

// MarkRunning records the runner handle once the runner has started.
func (s *Store) MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error {
	q := `BEGIN;
	LET $s = (UPDATE type::record('session', $id) SET state = 'running', handle = $handle
		WHERE state = 'starting' RETURN AFTER)[0];
	IF $s { CREATE event SET bead = $s.bead, kind = 'started', actor = 'yard',
		data = { session: $id, handle: $handle } };
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": sessionID, "handle": handle})
	return err
}

// EndSession ends a live session as 'died' or 'failed' and, if the bead is
// still owned by it, returns the bead for routing.
func (s *Store) EndSession(ctx context.Context, sessionID, state, reason string) error {
	if state != "died" && state != "failed" {
		return fmt.Errorf("EndSession: state %q not allowed", state)
	}
	q := `BEGIN;
	LET $s = (UPDATE type::record('session', $id) SET state = $state, error = $reason, ended_at = time::now()
		WHERE state IN ['starting', 'running'] RETURN AFTER)[0];
	IF $s {
		LET $d = (UPDATE $s.bead SET group = NONE, session = NONE WHERE session = $s.id RETURN DIFF);
		IF ` + diffNonEmpty + ` { fn::mint($s.bead, 'yard') };
		CREATE event SET bead = $s.bead, kind = $kind, actor = 'yard', data = { session: $id, reason: $reason };
	};
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": sessionID, "state": state, "reason": reason,
		"kind": "session_" + state})
	return err
}

func (s *Store) MarkStopped(ctx context.Context, sessionID string) error {
	_, err := s.exec(ctx, `UPDATE type::record('session', $id) SET stopped = true`,
		map[string]any{"id": sessionID})
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	ss, err := queryOne[[]Session](ctx, s, `SELECT `+sessionFields+` FROM type::record('session', $id)`,
		map[string]any{"id": id}, 0)
	if err != nil {
		return nil, err
	}
	if len(ss) == 0 {
		return nil, fmt.Errorf("session %s: %w", id, ErrNotFound)
	}
	return &ss[0], nil
}

// Sessions lists live sessions, or every session when all is set.
func (s *Store) Sessions(ctx context.Context, all bool) ([]Session, error) {
	q := `SELECT ` + sessionFields + ` FROM session`
	if !all {
		q += ` WHERE state IN ['starting', 'running']`
	}
	q += ` ORDER BY started_at`
	return queryOne[[]Session](ctx, s, q, nil, 0)
}

// Unstopped lists ended sessions whose runner resources are still up.
func (s *Store) Unstopped(ctx context.Context) ([]Session, error) {
	return queryOne[[]Session](ctx, s, `SELECT `+sessionFields+` FROM session
		WHERE state NOT IN ['starting', 'running'] AND stopped = false`, nil, 0)
}

// SetHandle records runner resources on a session without changing state,
// so teardown can clean up after a failed start.
func (s *Store) SetHandle(ctx context.Context, sessionID string, handle map[string]any) error {
	_, err := s.exec(ctx, `UPDATE type::record('session', $id) SET handle = $handle`,
		map[string]any{"id": sessionID, "handle": handle})
	return err
}

// FailureCount counts failed or died sessions for bead in stage since a
// human last un-held it, so un-holding grants fresh attempts.
func (s *Store) FailureCount(ctx context.Context, bead, stage string) (int, error) {
	q := `LET $b = type::record('bead', $bead);
	LET $since = (SELECT VALUE at FROM event WHERE bead = $b AND kind = 'unheld' ORDER BY at DESC)[0]
		OR d'1970-01-01T00:00:00Z';
	RETURN count(SELECT id FROM session WHERE bead = $b AND stage = $stage
		AND state IN ['died', 'failed'] AND started_at > $since);`
	return queryOne[int](ctx, s, q, map[string]any{"bead": bead, "stage": stage}, -1)
}

// ClosedWithWorkspace lists stopped sessions of closed beads whose runner
// left something on disk (handle.worktree) that has not been cleaned up.
func (s *Store) ClosedWithWorkspace(ctx context.Context) ([]Session, error) {
	return queryOne[[]Session](ctx, s, `SELECT `+sessionFields+` FROM session
		WHERE cleaned = false AND stopped = true AND bead.status = 'closed'
		AND handle.worktree != NONE`, nil, 0)
}

// MarkCleaned records the cleanup outcome so it is attempted once.
func (s *Store) MarkCleaned(ctx context.Context, sessionID string) error {
	_, err := s.exec(ctx, `UPDATE type::record('session', $id) SET cleaned = true`,
		map[string]any{"id": sessionID})
	return err
}
