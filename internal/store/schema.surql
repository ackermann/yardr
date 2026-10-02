-- yard schema. Applied on every connect; OVERWRITE keeps definitions current.

-- Bookkeeping: meta:schema holds the hash of the schema last applied.
DEFINE TABLE OVERWRITE meta SCHEMALESS;

-- A rig is a repository that beads can be worked in.
DEFINE TABLE OVERWRITE rig SCHEMAFULL;
DEFINE FIELD OVERWRITE path ON rig TYPE string;
DEFINE FIELD OVERWRITE base ON rig TYPE string DEFAULT 'origin/main';

-- A bead is one durable unit of work. The engine owns status (open/closed)
-- and assignment; stage is a free-form string that only routes interpret.
DEFINE TABLE OVERWRITE bead SCHEMAFULL CHANGEFEED 7d;
DEFINE FIELD OVERWRITE title        ON bead TYPE string;
DEFINE FIELD OVERWRITE body         ON bead TYPE string DEFAULT '';
DEFINE FIELD OVERWRITE type         ON bead TYPE string DEFAULT 'task';
DEFINE FIELD OVERWRITE stage        ON bead TYPE string DEFAULT 'new';
DEFINE FIELD OVERWRITE status       ON bead TYPE string DEFAULT 'open'
    ASSERT $value IN ['open', 'closed'];
DEFINE FIELD OVERWRITE priority     ON bead TYPE int DEFAULT 2;
DEFINE FIELD OVERWRITE rig          ON bead TYPE option<record<rig>>;
DEFINE FIELD OVERWRITE labels       ON bead TYPE set<string> DEFAULT <set>[];
DEFINE FIELD OVERWRITE metadata     ON bead TYPE object FLEXIBLE DEFAULT {};
DEFINE FIELD OVERWRITE hold         ON bead TYPE bool DEFAULT false;
DEFINE FIELD OVERWRITE group        ON bead TYPE option<record<group>>;
DEFINE FIELD OVERWRITE session      ON bead TYPE option<record<session>>;
DEFINE FIELD OVERWRITE close_reason ON bead TYPE option<string>;
DEFINE FIELD OVERWRITE created_at   ON bead TYPE datetime VALUE $before OR time::now();
DEFINE FIELD OVERWRITE created_by   ON bead TYPE option<string>;
-- updated_at and revision change together, only when the bead's state does
-- (fn::mint); a no-op write leaves both alone.
DEFINE FIELD OVERWRITE updated_at   ON bead TYPE datetime DEFAULT time::now();
DEFINE FIELD OVERWRITE revision     ON bead TYPE string DEFAULT '';
DEFINE FIELD OVERWRITE closed_at    ON bead TYPE option<datetime>;
DEFINE INDEX OVERWRITE bead_open ON bead FIELDS status, stage;

-- Typed edges between beads. Only kind 'blocks' affects readiness:
-- bead:a->dep->bead:b with kind 'blocks' means a must close before b is ready.
DEFINE TABLE OVERWRITE dep TYPE RELATION IN bead OUT bead ENFORCED SCHEMAFULL;
DEFINE FIELD OVERWRITE kind       ON dep TYPE string DEFAULT 'blocks';
DEFINE FIELD OVERWRITE created_at ON dep TYPE datetime VALUE $before OR time::now();
DEFINE FIELD OVERWRITE updated_at ON dep TYPE datetime DEFAULT time::now();
DEFINE FIELD OVERWRITE created_by ON dep TYPE option<string>;
DEFINE FIELD OVERWRITE revision   ON dep TYPE string DEFAULT '';
DEFINE INDEX OVERWRITE dep_unique ON dep FIELDS in, out, kind UNIQUE;

-- Notes are the evidence trail a bead carries.
DEFINE TABLE OVERWRITE note SCHEMAFULL;
DEFINE FIELD OVERWRITE bead   ON note TYPE record<bead>;
DEFINE FIELD OVERWRITE author ON note TYPE string;
DEFINE FIELD OVERWRITE text   ON note TYPE string;
DEFINE FIELD OVERWRITE at     ON note TYPE datetime VALUE $before OR time::now();
DEFINE INDEX OVERWRITE note_bead ON note FIELDS bead;

-- A group is whoever works a stage: an agent pool, a code handler or a human.
DEFINE TABLE OVERWRITE group SCHEMAFULL;
DEFINE FIELD OVERWRITE runner ON group TYPE string;
DEFINE FIELD OVERWRITE config ON group TYPE object FLEXIBLE DEFAULT {};
DEFINE FIELD OVERWRITE limit  ON group TYPE int DEFAULT 1;

-- A route sends open, unblocked beads in a stage to a group. NONE matches any.
DEFINE TABLE OVERWRITE route SCHEMAFULL;
DEFINE FIELD OVERWRITE stage    ON route TYPE string;
DEFINE FIELD OVERWRITE type     ON route TYPE option<string>;
DEFINE FIELD OVERWRITE rig      ON route TYPE option<record<rig>>;
DEFINE FIELD OVERWRITE label    ON route TYPE option<string>;
DEFINE FIELD OVERWRITE group    ON route TYPE record<group>;
DEFINE FIELD OVERWRITE priority ON route TYPE int DEFAULT 0;

-- A session is one runner working one bead for one group.
DEFINE TABLE OVERWRITE session SCHEMAFULL;
DEFINE FIELD OVERWRITE bead       ON session TYPE record<bead>;
DEFINE FIELD OVERWRITE group      ON session TYPE record<group>;
DEFINE FIELD OVERWRITE stage      ON session TYPE string;
DEFINE FIELD OVERWRITE state      ON session TYPE string DEFAULT 'starting'
    ASSERT $value IN ['starting', 'running', 'done', 'released', 'died', 'failed'];
DEFINE FIELD OVERWRITE handle     ON session TYPE object FLEXIBLE DEFAULT {};
DEFINE FIELD OVERWRITE error      ON session TYPE option<string>;
-- stopped: the runner's resources (pane, process) were torn down.
DEFINE FIELD OVERWRITE stopped    ON session TYPE bool DEFAULT false;
-- cleaned: workspace resources (worktree) handled after the bead closed.
DEFINE FIELD OVERWRITE cleaned    ON session TYPE bool DEFAULT false;
DEFINE FIELD OVERWRITE started_at ON session TYPE datetime VALUE $before OR time::now();
DEFINE FIELD OVERWRITE ended_at   ON session TYPE option<datetime>;
DEFINE INDEX OVERWRITE session_state ON session FIELDS state;

-- Append-only log; every state change writes one row in the same transaction.
DEFINE TABLE OVERWRITE event SCHEMAFULL CHANGEFEED 7d
    PERMISSIONS FOR update, delete NONE;
DEFINE FIELD OVERWRITE bead  ON event TYPE option<record<bead>>;
DEFINE FIELD OVERWRITE kind  ON event TYPE string;
DEFINE FIELD OVERWRITE actor ON event TYPE string;
DEFINE FIELD OVERWRITE data  ON event TYPE object FLEXIBLE DEFAULT {};
DEFINE FIELD OVERWRITE at    ON event TYPE datetime VALUE $before OR time::now();
DEFINE INDEX OVERWRITE event_bead ON event FIELDS bead;

-- Every state a bead or dep has had, keyed by its revision. A revision is an
-- opaque token (a ULID) minted on each real change; a no-op mints none, and
-- A -> B -> A yields three distinct revisions. Deletions leave a tombstone.
DEFINE TABLE OVERWRITE version SCHEMAFULL;
DEFINE FIELD OVERWRITE target   ON version TYPE record<bead | dep>;
DEFINE FIELD OVERWRITE revision ON version TYPE string;
DEFINE FIELD OVERWRITE actor    ON version TYPE string;
DEFINE FIELD OVERWRITE deleted  ON version TYPE bool DEFAULT false;
DEFINE FIELD OVERWRITE snapshot ON version TYPE object FLEXIBLE DEFAULT {};
DEFINE FIELD OVERWRITE at       ON version TYPE datetime VALUE $before OR time::now();
DEFINE INDEX OVERWRITE version_target ON version FIELDS target, at;
DEFINE INDEX OVERWRITE version_revision ON version FIELDS revision UNIQUE;

-- fn::mint gives $rec a fresh revision and records the resulting state.
-- Callers invoke it only when a write actually changed something.
DEFINE FUNCTION OVERWRITE fn::mint($rec: record, $actor: string) {
    LET $r = rand::ulid();
    LET $after = (UPDATE ONLY $rec SET revision = $r, updated_at = time::now());
    CREATE version SET target = $rec, revision = $r, actor = $actor, snapshot = $after;
    RETURN $r;
};

-- fn::tombstone records that $rec (already deleted) is gone, citing its
-- final live revision.
DEFINE FUNCTION OVERWRITE fn::tombstone($rec: record, $last: object, $actor: string) {
    CREATE version SET target = $rec, revision = rand::ulid(), actor = $actor,
        deleted = true, snapshot = $last;
};

-- Monotonic per-prefix counters for short bead ids.
DEFINE TABLE OVERWRITE counter SCHEMAFULL;
DEFINE FIELD OVERWRITE n ON counter TYPE int DEFAULT 0;

-- Backfill: beads and deps from before created_by take it from their first
-- version; the change mints a revision like any other.
LET $uncredited = array::concat((SELECT VALUE id FROM bead WHERE created_by = NONE AND revision != ''),
    (SELECT VALUE id FROM dep WHERE created_by = NONE AND revision != ''));
FOR $r IN $uncredited {
    UPDATE $r SET created_by = (SELECT actor, at FROM version WHERE target = $r ORDER BY at)[0].actor OR 'yard';
    fn::mint($r, 'yard');
};

-- Backfill: records created before revisions existed get their first one.
-- (FOR over an inline subquery fails on 3.2; iterate a LET instead.)
LET $unminted = array::concat((SELECT VALUE id FROM bead WHERE revision = ''),
    (SELECT VALUE id FROM dep WHERE revision = ''));
FOR $r IN $unminted { fn::mint($r, 'yard') };
