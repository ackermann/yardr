package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Bead struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Body        string         `json:"body,omitempty"`
	Type        string         `json:"type"`
	Stage       string         `json:"stage"`
	Status      string         `json:"status"`
	Priority    int            `json:"priority"`
	Rig         *string        `json:"rig,omitempty"`
	Labels      []string       `json:"labels"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Hold        bool           `json:"hold,omitempty"`
	Group       *string        `json:"group,omitempty"`
	Session     *string        `json:"session,omitempty"`
	CloseReason *string        `json:"close_reason,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ClosedAt    *time.Time     `json:"closed_at,omitempty"`
	CreatedBy   *string        `json:"created_by,omitempty"`
	// Revision is an opaque token that changes on every real state change.
	Revision string `json:"revision"`
}

// beadFields projects record links to plain ids and sets to arrays so callers
// never see SurrealDB types (a set decodes to an empty Go slice otherwise).
const beadFields = `record::id(id) AS id, title, body, type, stage, status, priority,
	IF rig THEN record::id(rig) END AS rig, <array>labels AS labels, metadata, hold,
	IF group THEN record::id(group) END AS group,
	IF session THEN record::id(session) END AS session,
	close_reason, created_at, updated_at, closed_at, created_by, revision`

type NewBead struct {
	Title    string
	Body     string
	Type     string
	Stage    string
	Priority int
	Rig      string
	Labels   []string
	Prefix   string // id prefix; defaults to the rig name, else "yd"
	ID       string // explicit id (imports); must be unused
}

// CreateBead mints a short id (<prefix>-<n>) and creates the bead.
func (s *Store) CreateBead(ctx context.Context, nb NewBead, actor string) (*Bead, error) {
	prefix := nb.Prefix
	if prefix == "" {
		prefix = nb.Rig
	}
	if prefix == "" {
		prefix = "yd"
	}
	set := []string{"title = $title", "body = $body", "priority = $priority", "labels = <set>$labels",
		"created_by = $actor"}
	vars := map[string]any{
		"prefix": prefix, "title": nb.Title, "body": nb.Body,
		"priority": nb.Priority, "labels": nonNil(nb.Labels), "actor": actor,
	}
	if nb.Type != "" {
		set = append(set, "type = $type")
		vars["type"] = nb.Type
	}
	if nb.Stage != "" {
		set = append(set, "stage = $stage")
		vars["stage"] = nb.Stage
	}
	if nb.Rig != "" {
		set = append(set, "rig = type::record('rig', $rig)")
		vars["rig"] = nb.Rig
	}
	mint := `LET $n = (UPSERT type::record('counter', $prefix) SET n += 1 RETURN VALUE n)[0];
	LET $id = string::concat($prefix, '-', <string>$n);`
	if nb.ID != "" {
		if !validID.MatchString(nb.ID) {
			return nil, fmt.Errorf("bead id %q: use letters, digits, '.', '_' or '-'", nb.ID)
		}
		vars["id"] = nb.ID
		mint = ``
	}
	// CREATE fails if the id exists, so an explicit id is never reused.
	q := `BEGIN;
	` + mint + `
	CREATE type::record('bead', $id) SET ` + strings.Join(set, ", ") + `;
	fn::mint(type::record('bead', $id), $actor);
	CREATE event SET bead = type::record('bead', $id), kind = 'created', actor = $actor,
		data = { title: $title };
	RETURN $id;
	COMMIT;`
	id, err := queryOne[string](ctx, s, q, vars, -1)
	if err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

func (s *Store) GetBead(ctx context.Context, id string) (*Bead, error) {
	bs, err := queryOne[[]Bead](ctx, s, `SELECT `+beadFields+` FROM type::record('bead', $id)`,
		map[string]any{"id": id}, 0)
	if err != nil {
		return nil, err
	}
	if len(bs) == 0 {
		return nil, fmt.Errorf("bead %s: %w", id, ErrNotFound)
	}
	return &bs[0], nil
}

type BeadFilter struct {
	All   bool // include closed
	Stage string
	Type  string
	Rig   string
	Label string
	Group string
}

func (s *Store) ListBeads(ctx context.Context, f BeadFilter) ([]Bead, error) {
	var where []string
	vars := map[string]any{}
	if !f.All {
		where = append(where, "status = 'open'")
	}
	if f.Stage != "" {
		where = append(where, "stage = $stage")
		vars["stage"] = f.Stage
	}
	if f.Type != "" {
		where = append(where, "type = $type")
		vars["type"] = f.Type
	}
	if f.Rig != "" {
		where = append(where, "rig = type::record('rig', $rig)")
		vars["rig"] = f.Rig
	}
	if f.Label != "" {
		where = append(where, "labels CONTAINS $label")
		vars["label"] = f.Label
	}
	if f.Group != "" {
		where = append(where, "group = type::record('group', $group)")
		vars["group"] = f.Group
	}
	q := `SELECT ` + beadFields + ` FROM bead`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY priority, created_at`
	return queryOne[[]Bead](ctx, s, q, vars, 0)
}

// readyWhere selects open, unheld, unblocked beads.
const readyWhere = `status = 'open' AND hold = false
	AND count(<-dep[WHERE kind = 'blocks']<-bead[WHERE status != 'closed']) = 0`

// Ready lists open, unheld beads whose blockers are all closed, assigned or not.
func (s *Store) Ready(ctx context.Context) ([]Bead, error) {
	return queryOne[[]Bead](ctx, s,
		`SELECT `+beadFields+` FROM bead WHERE `+readyWhere+` ORDER BY priority, created_at`, nil, 0)
}

// Dispatchable lists ready beads that no group owns yet.
func (s *Store) Dispatchable(ctx context.Context) ([]Bead, error) {
	return queryOne[[]Bead](ctx, s,
		`SELECT `+beadFields+` FROM bead WHERE group = NONE AND `+readyWhere+
			` ORDER BY priority, created_at`, nil, 0)
}

type BeadPatch struct {
	Title       *string
	Body        *string
	Type        *string
	Priority    *int
	Rig         *string
	AddLabels   []string
	RemoveLabel []string
	Metadata    map[string]any // merged
}

func (s *Store) UpdateBead(ctx context.Context, id string, p BeadPatch, actor string) (*Bead, error) {
	var set []string
	vars := map[string]any{"id": id, "actor": actor}
	changed := map[string]any{}
	if p.Title != nil {
		set, vars["title"], changed["title"] = append(set, "title = $title"), *p.Title, *p.Title
	}
	if p.Body != nil {
		set, vars["body"], changed["body"] = append(set, "body = $body"), *p.Body, true
	}
	if p.Type != nil {
		set, vars["type"], changed["type"] = append(set, "type = $type"), *p.Type, *p.Type
	}
	if p.Priority != nil {
		set, vars["priority"], changed["priority"] = append(set, "priority = $priority"), *p.Priority, *p.Priority
	}
	if p.Rig != nil {
		set, vars["rig"], changed["rig"] = append(set, "rig = type::record('rig', $rig)"), *p.Rig, *p.Rig
	}
	// SET clauses all read the original row, so label edits are one expression.
	if len(p.AddLabels) > 0 || len(p.RemoveLabel) > 0 {
		vars["add"], vars["rm"] = nonNil(p.AddLabels), nonNil(p.RemoveLabel)
		changed["labels_added"], changed["labels_removed"] = p.AddLabels, p.RemoveLabel
		set = append(set, "labels = labels.union(<set>$add).complement(<set>$rm)")
	}
	if len(p.Metadata) > 0 {
		set, vars["meta"], changed["metadata"] = append(set, "metadata = object::extend(metadata, $meta)"), p.Metadata, p.Metadata
	}
	if len(set) == 0 {
		return s.GetBead(ctx, id)
	}
	vars["changed"] = changed
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };
	LET $d = (UPDATE type::record('bead', $id) SET ` + strings.Join(set, ", ") + ` RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = 'updated', actor = $actor, data = $changed;
	};
	COMMIT;`
	if _, err := s.exec(ctx, q, vars); err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

// diffNonEmpty is true when the preceding `LET $d = (UPDATE ... RETURN DIFF)`
// altered anything; only then is a revision minted and an event written.
const diffNonEmpty = `array::len(array::flatten($d)) > 0`

// finishSession marks the bead's current session done and clears ownership.
// It is shared by Advance, Close and Release.
const finishSession = `
	LET $b = (SELECT session FROM ONLY type::record('bead', $id));
	IF $b.session { UPDATE $b.session SET state = $sstate, ended_at = time::now() WHERE state IN ['starting', 'running'] };`

// Advance moves a bead to the next stage and frees it for routing.
func (s *Store) Advance(ctx context.Context, id, stage, actor string) (*Bead, error) {
	q := `BEGIN;
	LET $old = (SELECT stage FROM ONLY type::record('bead', $id));
	IF !$old { THROW '` + throwNotFound + `' };` + finishSession + `
	LET $d = (UPDATE type::record('bead', $id) SET stage = $stage, group = NONE, session = NONE RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = 'advanced', actor = $actor,
			data = { from: $old.stage, to: $stage };
	};
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": id, "stage": stage, "actor": actor, "sstate": "done"})
	if err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

func (s *Store) CloseBead(ctx context.Context, id, reason, actor string) (*Bead, error) {
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };` + finishSession + `
	LET $d = (UPDATE type::record('bead', $id) SET status = 'closed', close_reason = $reason,
		closed_at = closed_at OR time::now(), group = NONE, session = NONE RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = 'closed', actor = $actor, data = { reason: $reason };
	};
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": id, "reason": reason, "actor": actor, "sstate": "done"})
	if err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

func (s *Store) Reopen(ctx context.Context, id, actor string) (*Bead, error) {
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };
	LET $d = (UPDATE type::record('bead', $id) SET status = 'open', close_reason = NONE, closed_at = NONE RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = 'reopened', actor = $actor;
	};
	COMMIT;`
	if _, err := s.exec(ctx, q, map[string]any{"id": id, "actor": actor}); err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

// SetHold pauses (or resumes) routing for a bead. Holding does not stop a
// session already working it.
func (s *Store) SetHold(ctx context.Context, id string, hold bool, actor string) (*Bead, error) {
	kind := "held"
	if !hold {
		kind = "unheld"
	}
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };
	LET $d = (UPDATE type::record('bead', $id) SET hold = $hold RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = $kind, actor = $actor;
	};
	COMMIT;`
	if _, err := s.exec(ctx, q, map[string]any{"id": id, "hold": hold, "kind": kind, "actor": actor}); err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

// Release takes a bead away from its group so it is routed again, ending
// the current session as 'released'.
func (s *Store) Release(ctx context.Context, id, reason, actor string) (*Bead, error) {
	q := `BEGIN;
	IF !record::exists(type::record('bead', $id)) { THROW '` + throwNotFound + `' };` + finishSession + `
	LET $d = (UPDATE type::record('bead', $id) SET group = NONE, session = NONE RETURN DIFF);
	IF ` + diffNonEmpty + ` {
		fn::mint(type::record('bead', $id), $actor);
		CREATE event SET bead = type::record('bead', $id), kind = 'released', actor = $actor, data = { reason: $reason };
	};
	COMMIT;`
	_, err := s.exec(ctx, q, map[string]any{"id": id, "reason": reason, "actor": actor, "sstate": "released"})
	if err != nil {
		return nil, err
	}
	return s.GetBead(ctx, id)
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
