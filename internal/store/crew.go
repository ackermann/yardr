package store

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bCrew = []byte("crew")

func init() { allBuckets = append(allBuckets, bCrew) }

// Bounds that keep a fresh session's starting context small.
const (
	MaxHandoffBytes = 16 << 10
	MaxJournalBytes = 4 << 10
	MaxMailBytes    = 4 << 10
	PrimeEntries    = 20 // journal entries since the latest handoff
	PrimeInbox      = 20
	PrimeFallback   = 15 // recent events when the last session left no handoff
	PrimeMemories   = 20 // memory titles, the most recently written
	PrimeAsks       = 20 // open asks the crew member wrote, the oldest
	MaxStartFails   = 5  // consecutive failed starts before a crew member is parked
)

// MinSessionLife is how long a crew member's session has to live for its start
// to count as one that worked. A session gone sooner (a harness that exits
// on a bad flag or an expired login) is a failed start, or the daemon would
// start the next one on every pass and never give up.
const MinSessionLife = 60 * time.Second

// CrewDir is the directory of a yard's home where the operator keeps what a
// crew member runs with (an env_file, a key): a convention of the docs,
// which yardr makes nowhere.
const CrewDir = "crew"

// oldKinds are the kinds earlier releases logged, each with the kind new
// events carry, by the rename that replaced them: a train was an epic and a
// wagon a part until schema 10, the crew were the residents until schema
// 11, and a depot was a rig until schema 12. Events already in the log keep
// the old kind, so what reads the log goes by these (EventKind,
// kindSpellings) until the log is rewritten or pruned. Nothing else reads
// an old word: a kind given in a filter or an --on list is today's.
var (
	oldTrainKinds = map[string]string{
		"epic_branched": "train_branched", "bad_epic": "bad_train", "part_without_epic": "wagon_without_train",
	}
	oldCrewKinds = map[string]string{
		"resident_defined": "crew_defined", "resident_deleted": "crew_deleted",
		"resident_enabled": "crew_enabled", "resident_disabled": "crew_disabled",
		"resident_started": "crew_started", "resident_status": "crew_status", "resident_stopped": "crew_stopped",
		"resident_start_failed": "crew_start_failed", "resident_gave_up": "crew_gave_up",
		"resident_restart_requested": "crew_restart_requested", "resident_rotation_asked": "crew_rotation_asked",
		"resident_blocked": "crew_blocked",
	}
	oldDepotKinds = map[string]string{
		"rig_updated": "depot_updated", "rig_removed": "depot_removed", "bad_rig": "bad_depot",
	}
	oldKinds = []map[string]string{oldDepotKinds, oldCrewKinds, oldTrainKinds}
)

// EventKind is the kind new events carry for the kind of an event in the
// log: kind itself, or the one that replaced it when an earlier release
// logged it (oldKinds). What reads the log by kind goes by it, so an event
// from before the upgrade is read as one from after.
func EventKind(kind string) string {
	for _, olds := range oldKinds {
		if k := olds[kind]; k != "" {
			return k
		}
	}
	return kind
}

// kindSpellings are the kinds an event of kind is in the log under: kind
// as new events carry it, and what an earlier release called it.
func kindSpellings(kind string) []string {
	out := []string{kind}
	for _, olds := range oldKinds {
		for old, k := range olds {
			if k == kind {
				out = append(out, old)
			}
		}
	}
	return out
}

// CrewMember is one of the crew: an always-on session not tied to a bead,
// such as a yardmaster.
// The daemon keeps it running, delivers its inbox when it is idle, and
// rotates it through handoffs written to its journal bead.
type CrewMember struct {
	Name         string         `json:"name"`
	Runner       string         `json:"runner"`
	Config       map[string]any `json:"config"`
	Dir          string         `json:"dir"`
	On           []string       `json:"on"`            // event kinds delivered to the inbox (mail always is), as if subscribed to yardr all kind=<k>
	MaxAge       string         `json:"max_age"`       // e.g. "6h"; "" never rotates
	HandoffGrace string         `json:"handoff_grace"` // how long a requested handoff may take
	Enabled      bool           `json:"enabled"`
	Journal      string         `json:"journal"` // journal bead handle
	// Revision changes on every write to the crew member. Given to PutCrew
	// (or as rev to the writes that take one) it is the revision the caller
	// read, and the write is refused with ErrConflict if it moved since.
	Revision string `json:"revision,omitempty"`

	// Who it is in each source (by source name), and which hook events it
	// hears about. Kept when the definition is replaced.
	Identities    map[string]Identity `json:"identities,omitempty"`
	Subscriptions []Subscription      `json:"subscriptions,omitempty"`

	// Runtime of the line session, maintained by the daemon.
	CrewSession

	// Conversations is the runtime of each conversation session, by user:
	// one session per served user whose env_for_<user> is set, on that
	// env. The daemon makes each row when it first keeps the session,
	// starting its cursors at the line session's.
	Conversations map[string]CrewSession `json:"conversations,omitempty"`
}

// CrewSession is the runtime state of one session of a crew member: the
// line session's lives in the CrewMember itself, and each conversation
// session's under Conversations by its user. Every session has its own
// inbox slice (Cursor), nudge position (Notified) and handoff, over the
// one inbox and the one journal.
type CrewSession struct {
	State string `json:"state"` // stopped running failed
	// Status is what the running session is doing, as the daemon last saw
	// it (CrewStatus): working, idle, blocked (it waits at a prompt
	// only a person in its pane can answer) or unknown; "" when none runs
	// or the daemon has not looked yet.
	Status string         `json:"status,omitempty"`
	Handle map[string]any `json:"handle,omitempty"`
	// Starting is the handle, as far as it is known, of a session the
	// daemon is starting (CrewStarting): its agent can act before the
	// start is recorded, and its pane is the crew member's from then.
	Starting           map[string]any `json:"starting,omitempty"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	PrevStartedAt      *time.Time     `json:"prev_started_at,omitempty"`
	Failures           int            `json:"failures,omitempty"`
	RetryAfter         *time.Time     `json:"retry_after,omitempty"`
	RestartRequested   string         `json:"restart_requested,omitempty"`
	RestartRequestedAt *time.Time     `json:"restart_requested_at,omitempty"`
	RotationAskedAt    *time.Time     `json:"rotation_asked_at,omitempty"`
	LastHandoffAt      *time.Time     `json:"last_handoff_at,omitempty"`
	Cursor             uint64         `json:"cursor"`   // inbox slice acknowledged through this event seq
	Notified           uint64         `json:"notified"` // slice nudged about events through this seq

	// Left are the handles of sessions that were gone when the daemon
	// looked (CrewGone), oldest first: what they ran in is still open
	// in the runtime, and the handle is how the daemon knows it as its own
	// when it closes all but the latest (CrewLeft).
	Left []map[string]any `json:"left,omitempty"`
}

func (s CrewSession) Live() bool { return s.State == "running" }

// CrewForKey is the crew config key naming the users a crew member serves;
// EnvForPrefix starts the per-user env keys (env_for_<user>).
const (
	CrewForKey   = "for"
	EnvForPrefix = "env_for_"
)

// EnvForUser is the user an env_for_<user> config key carries the env of,
// "" for any other key.
func EnvForUser(key string) string {
	if !IsEnvForKey(key) {
		return ""
	}
	return strings.TrimPrefix(key, EnvForPrefix)
}

// IsEnvForKey reports whether key is an env_for_<user> config key.
func IsEnvForKey(key string) bool {
	return len(key) > len(EnvForPrefix) && strings.HasPrefix(key, EnvForPrefix)
}

// Serves reports whether r serves user: everyone when its for= is unset
// or empty, else the users it names.
func (r CrewMember) Serves(user string) bool {
	return configServes(r.Config, user)
}

// configServes is Serves of a config: everyone when for= is unset or
// empty, else membership. The read receipt (readers.letter) asks it of a
// config it decoded for cursors alone.
func configServes(cfg map[string]any, user string) bool {
	list := servedList(cfg)
	return len(list) == 0 || slices.Contains(list, user)
}

// servedList is the users r's for= names, sorted, nil when unset or empty.
func servedList(cfg map[string]any) []string {
	v, ok := cfg[CrewForKey]
	if !ok {
		return nil
	}
	var out []string
	switch v := v.(type) {
	case string:
		for _, u := range strings.Split(v, ",") {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
	case []string:
		for _, u := range v {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
	case []any:
		for _, x := range v {
			if u, ok := x.(string); ok {
				if u = strings.TrimSpace(u); u != "" {
					out = append(out, u)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Served is the users r serves explicitly, sorted: nil when it serves
// everyone. It is how a refusal names whom the crew member serves.
func (r CrewMember) Served() []string { return servedList(r.Config) }

// EnvFor is the env file carrying user's conversation session: the
// env_for_<user> setting, "" where it names none.
func (r CrewMember) EnvFor(user string) string {
	path, _ := r.Config[EnvForPrefix+user].(string)
	return path
}

// HasConversation reports whether r keeps a conversation session for
// user: served, with its env_for_ key set.
func (r CrewMember) HasConversation(user string) bool {
	return user != "" && r.Serves(user) && r.EnvFor(user) != ""
}

// sliced reports whether r reads a slice for user: a conversation the
// config declares whose row the daemon made. Until the row exists the
// user's mail falls back to the line slice, which holds it: the line's
// positional ack would otherwise strand it before the slice is made.
func (r CrewMember) sliced(user string) bool {
	if !r.HasConversation(user) {
		return false
	}
	_, ok := r.Conversations[user]
	return ok
}

// ConversationUsers are the users r keeps a conversation session for,
// sorted.
func (r CrewMember) ConversationUsers() []string {
	var out []string
	for key := range r.Config {
		if user := EnvForUser(key); user != "" && r.Serves(user) && r.EnvFor(user) != "" {
			out = append(out, user)
		}
	}
	slices.Sort(out)
	return out
}

// Session is the runtime state of the line session ("") or of the
// conversation session for user. A user with no conversation reads zero.
func (r CrewMember) Session(user string) CrewSession {
	if user == "" {
		return r.CrewSession
	}
	return r.Conversations[user]
}

// ensureConversation makes the row for user's conversation session where
// missing, starting its cursors at the line session's, and answers it.
// The caller stores back what it changes.
func (r *CrewMember) ensureConversation(user string) CrewSession {
	if cs, ok := r.Conversations[user]; ok {
		return cs
	}
	cs := CrewSession{Cursor: r.Cursor, Notified: r.Notified}
	if r.Conversations == nil {
		r.Conversations = map[string]CrewSession{}
	}
	r.Conversations[user] = cs
	return cs
}

func journalID(name string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(name) + "-journal"
}

// grants are the crew member settings that let it act outside the yard,
// each with what it lets it do, as a refusal says it.
var grants = []struct {
	does string
	may  func(CrewMember) []string
}{
	{"open threads in", CrewMember.MayOpen},
	{"set the status of threads in", CrewMember.MaySetState},
	{"mail crew members of", CrewMember.MayMail},
}

// PutCrew creates a crew member (with its journal bead) or replaces its
// definition, keeping its runtime state, journal and inbox position. A
// definition that lets its crew member do outside the yard what it could
// not before (grants) is the operator's to save (operatorOnly): a crew
// member's session granting it to itself, or to another, would make the
// setting mean nothing. One that keeps or narrows what was allowed is
// anyone's, as the rest of it is.
// crewGrantsAdded answers what want lets its crew member do outside the
// yard that have does not, a phrase for each grant that grew, in the
// order PutCrew refuses them: "let <name> open threads in ...".
func crewGrantsAdded(have, want CrewMember) []string {
	var out []string
	for _, g := range grants {
		before := g.may(have)
		added := slices.DeleteFunc(g.may(want), func(s string) bool { return slices.Contains(before, s) })
		if len(added) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("let %s %s %s", want.Name, g.does, strings.Join(added, ", ")))
	}
	return out
}

func (s *Store) PutCrew(ctx context.Context, def CrewMember, actor string) (*CrewMember, error) {
	var out *CrewMember
	err := s.write(func(w *wtx) error {
		var err error
		out, err = w.putCrew(ctx, def, actor, nil)
		return err
	})
	return out, err
}

// putCrew is PutCrew inside the transaction, so that an approved run ask
// saves the definition in the write that records its approval: by is then
// the ask it was approved on, which the event carries.
func (w *wtx) putCrew(ctx context.Context, def CrewMember, actor string, by *approvedOn) (*CrewMember, error) {
	var r CrewMember
	ok, err := get(w.Tx, bCrew, def.Name, &r)
	if err != nil {
		return nil, err
	}
	if err := checkCrewRev(def.Name, r.Revision, def.Revision); err != nil {
		return nil, err
	}
	if !ok {
		// One name is one party: mail to it must reach one of the two.
		if w.Bucket(bUsers).Get([]byte(def.Name)) != nil {
			return nil, fmt.Errorf("crew member %s: that is a user's name, and mail to a name reaches one party: choose another", def.Name)
		}
		// A new crew member starts reading the inbox from now, not history.
		head := uint64(0)
		if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
			head = binary.BigEndian.Uint64(k)
		}
		r = CrewMember{Name: def.Name, State: "stopped", Enabled: true, Cursor: head, Notified: head,
			Journal: journalID(def.Name)}
		if w.Bucket(bHandles).Get([]byte(r.Journal)) == nil {
			// The crew's journal is the yard's own work, the
			// operator's as a crew-created bead is.
			operator, err := operatorIn(w.Tx)
			if err != nil {
				return nil, err
			}
			jr := &beadRow{Bead: Bead{Title: "Journal of " + def.Name, Type: TypeJournal,
				Stage: "journal", Status: "open", Labels: []string{"crew:" + def.Name},
				CreatedAt: now(), CreatedBy: &actor, Originator: operator}}
			if err := w.insertBead(jr, "", r.Journal, actor); err != nil {
				return nil, err
			}
			if err := w.event(r.Journal, "created", actor, createdData(&jr.Bead)); err != nil {
				return nil, err
			}
		}
	}
	// A grant on a crew member that is there may be proposed; one
	// on a member being defined is the operator's alone with the
	// definition, which says what the yard runs.
	if added := crewGrantsAdded(r, def); len(added) > 0 {
		ask := Askable
		if !ok {
			ask = Alone
		}
		if err := w.operatorOnly(ctx, actor, added[0], ask); err != nil {
			return nil, err
		}
	}
	// Held to ConfigKeys as a group's config is (putGroup).
	kept := r.Config
	if r.Runner != def.Runner {
		kept = nil
	}
	if ps := configProblems(CrewConfig, def.Name, def.Runner, def.Config, kept); len(ps) > 0 {
		return nil, ps[0]
	}
	if err := checkCrewSessionSettings(ctx, w.Tx, def.Name, r.Config, def.Config, actor); err != nil {
		return nil, err
	}
	if err := checkCrewUsers(w.Tx, &def); err != nil {
		return nil, err
	}
	r.Runner, r.Config, r.Dir, r.MaxAge, r.HandoffGrace =
		def.Runner, def.Config, def.Dir, def.MaxAge, def.HandoffGrace
	r.On = def.On
	if r.Config == nil {
		r.Config = map[string]any{}
	}
	for _, d := range []string{r.MaxAge, r.HandoffGrace} {
		if d != "" {
			if _, err := time.ParseDuration(d); err != nil {
				return nil, fmt.Errorf("duration %q: %w", d, err)
			}
		}
	}
	r.Revision = newRevision()
	if err := put(w.Tx, bCrew, r.Name, r); err != nil {
		return nil, err
	}
	data := map[string]any{"crew": r.Name, "runner": r.Runner}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event(r.Journal, "crew_defined", actor, data); err != nil {
		return nil, err
	}
	return &r, nil
}

// checkCrewSessionSettings keeps the conversation logins and the serving
// list in the operator's hands: they decide which user's login the daemon
// starts. Other crew settings keep PutCrew's existing actor rules.
func checkCrewSessionSettings(ctx context.Context, tx *bolt.Tx, name string, before, after map[string]any, actor string) error {
	keys := map[string]bool{}
	for key := range before {
		if key == CrewForKey || IsEnvForKey(key) {
			keys[key] = true
		}
	}
	for key := range after {
		if key == CrewForKey || IsEnvForKey(key) {
			keys[key] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.Sort(ordered)
	for _, key := range ordered {
		was, had := before[key]
		is, set := after[key]
		if had == set && reflect.DeepEqual(was, is) {
			continue
		}
		if err := operatorOnly(ctx, tx, actor, fmt.Sprintf("change %s on crew member %s", key, name), Alone); err != nil {
			return err
		}
	}
	return nil
}

// checkCrewUsers refuses a crew member whose for= or env_for_<user> names
// no user that exists, in the style of checkLoginFor: a serving list or a
// conversation env for a typo'd name would strand the session silently.
func checkCrewUsers(tx *bolt.Tx, def *CrewMember) error {
	for _, who := range def.Served() {
		if _, err := getUser(tx, who); err != nil {
			return fmt.Errorf("crew member %s: for %s: %w", def.Name, who, err)
		}
	}
	var envFor []string
	for key := range def.Config {
		if user := EnvForUser(key); user != "" {
			envFor = append(envFor, user)
		}
	}
	slices.Sort(envFor)
	for _, who := range envFor {
		if _, err := getUser(tx, who); err != nil {
			return fmt.Errorf("crew member %s: env_for_%s: %w", def.Name, who, err)
		}
	}
	return nil
}

func (s *Store) Crew(ctx context.Context) ([]CrewMember, error) {
	out := []CrewMember{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
			out = append(out, *r)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetCrew(ctx context.Context, name string) (*CrewMember, error) {
	r := new(CrewMember)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bCrew, name, r)
		if err == nil && !ok {
			err = fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteCrew removes the crew member; its journal bead is kept as history.
// A rev other than "" must be the crew member's current revision.
// Removing a crew member is the operator's to do (operatorOnly).
func (s *Store) DeleteCrew(ctx context.Context, name, rev, actor string) error {
	return s.changeCrewAt(name, rev, func(w *wtx, r *CrewMember) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		if err := w.operatorOnly(ctx, actor, "remove a crew member", Alone); err != nil {
			return err
		}
		if err := w.Bucket(bCrew).Delete([]byte(name)); err != nil {
			return err
		}
		if err := dropThreads(w.Tx, name); err != nil {
			return err
		}
		data := map[string]any{"crew": name}
		if err := unexpose(w.Tx, name, data); err != nil {
			return err
		}
		return w.event(r.Journal, "crew_deleted", actor, data)
	}, true)
}

// changeCrew applies fn to the stored crew member and saves it under a
// fresh revision, unless deleted is set (fn removed it). The daemon's own
// bookkeeping writes through it unchecked.
func (s *Store) changeCrew(name string, fn func(*wtx, *CrewMember) error, deleted ...bool) error {
	return s.changeCrewAt(name, "", fn, deleted...)
}

// changeCrewAt is changeCrew refusing with ErrConflict unless the
// crew member is at rev ("" expects nothing).
func (s *Store) changeCrewAt(name, rev string, fn func(*wtx, *CrewMember) error, deleted ...bool) error {
	return s.write(func(w *wtx) error {
		var r CrewMember
		ok, err := get(w.Tx, bCrew, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		if err := checkCrewRev(name, r.Revision, rev); err != nil {
			return err
		}
		if err := fn(w, &r); err != nil {
			return err
		}
		if len(deleted) > 0 && deleted[0] {
			return nil
		}
		r.Revision = newRevision()
		return put(w.Tx, bCrew, name, r)
	})
}

// checkCrewRev refuses a write that expected revision want of a
// crew member now at have. A crew member stored before revisions existed has
// none, so any expectation of it is stale: the caller read something else.
func checkCrewRev(name, have, want string) error {
	if want == "" || want == have {
		return nil
	}
	if have == "" {
		have = "none"
	}
	return fmt.Errorf("%w: crew member %s is at revision %s, not %s; read it again", ErrConflict, name, have, want)
}

// SetCrewEnabled starts (true) or stops (false) keeping it alive. Enabling
// also clears every session's failure streak. A rev other than "" must be
// the crew member's current revision. Enabling one is the operator's to do
// (operatorOnly): it starts agents. Disabling one is anyone's, as narrowing
// what is allowed is; from a session's or a crew member's pane the write is
// recorded as that session's actor or that crew member, whatever actor
// says, and from anywhere else as given (paneAuthor).
func (s *Store) SetCrewEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error {
	return s.changeCrewAt(name, rev, func(w *wtx, r *CrewMember) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		if enabled {
			if err := w.operatorOnly(ctx, actor, "enable a crew member", Alone); err != nil {
				return err
			}
		}
		r.Enabled = enabled
		if enabled {
			r.Failures, r.RetryAfter = 0, nil
			if r.State == "failed" {
				r.State = "stopped"
			}
			for user, cs := range r.Conversations {
				cs.Failures, cs.RetryAfter = 0, nil
				if cs.State == "failed" {
					cs.State = "stopped"
				}
				r.Conversations[user] = cs
			}
		}
		kind := map[bool]string{true: "crew_enabled", false: "crew_disabled"}[enabled]
		return w.event(r.Journal, kind, actor, map[string]any{"crew": name})
	})
}

// CrewStarting records what is known of a session of the crew member being
// started, before its agent is: a runner can first record that a start is
// underway, then its pane, before the agent takes its first prompt seconds
// before the start is recorded (CrewStarted). A command it runs in them is
// the crew member's by this handle, whatever actor it names (paneCrew). The
// recorded start takes it off again; after a failed one it stays while its
// shell runs (CrewStartFailed).
func (s *Store) CrewStarting(ctx context.Context, name string, handle map[string]any) error {
	return s.CrewStartingFor(ctx, name, "", handle)
}

// CrewStarted records a fresh session. The failure streak stays: only a
// session that lives (CrewSettled) ends it.
func (s *Store) CrewStarted(ctx context.Context, name string, handle map[string]any) error {
	return s.CrewStartedFor(ctx, name, "", handle)
}

// CrewSettled ends the failure streak: the session has lived
// MinSessionLife, so its start worked.
func (s *Store) CrewSettled(ctx context.Context, name string) error {
	return s.CrewSettledFor(ctx, name, "")
}

// CrewStatuses are what CrewStatus takes: a runtime's word for
// what a session is doing, "done" being a turn that ended, which is idle.
var CrewStatuses = []string{"working", "idle", "blocked", "unknown"}

// CrewStatus records what the crew member's running session is doing, as
// the daemon saw it on its pass. A change is an event, crew_status
// with the status and what it was, so a view of the crew follows it
// from the stream; the same status again writes nothing. The event is on
// no bead: a session's turns are not its journal's history.
func (s *Store) CrewStatus(ctx context.Context, name, status string) error {
	return s.CrewStatusFor(ctx, name, "", status)
}

// SetCrewHandle replaces the running session's handle (e.g. once its
// pending first prompt was delivered).
func (s *Store) SetCrewHandle(ctx context.Context, name string, handle map[string]any) error {
	return s.SetCrewHandleFor(ctx, name, "", handle)
}

// CrewStopped records that the session ended (stopped by yard, or died).
func (s *Store) CrewStopped(ctx context.Context, name, reason string) error {
	return s.CrewStoppedFor(ctx, name, "", reason)
}

// CrewGone is CrewStopped for a session that ended by itself in a
// runtime that keeps what it ran in open: its handle goes to the end of
// Left, in the same write, so no session's leavings are lost track of.
func (s *Store) CrewGone(ctx context.Context, name, reason string) error {
	return s.CrewGoneFor(ctx, name, "", reason)
}

// CrewLeft replaces Left, once the daemon has closed some of what it
// lists.
func (s *Store) CrewLeft(ctx context.Context, name string, left []map[string]any) error {
	return s.CrewLeftFor(ctx, name, "", left)
}

// CrewStartFailed counts a failed start (one that did not happen, or a
// session gone within MinSessionLife); after MaxStartFails in a row the
// crew member is parked (state failed) until someone starts it again.
func (s *Store) CrewStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error {
	return s.CrewStartFailedFor(ctx, name, "", reason, backoff)
}

// RequestRestart asks the daemon for a fresh session once the current one
// is idle (or the handoff grace has passed). A rev other than "" must be
// the crew member's current revision. It asks for the line session, through
// RequestRestartFor, whose gate covers both.
func (s *Store) RequestRestart(ctx context.Context, name, reason, rev, actor string) error {
	return s.RequestRestartFor(ctx, name, "", reason, rev, actor)
}

// MarkRotationAsked records that the crew member was asked to hand off.
func (s *Store) MarkRotationAsked(ctx context.Context, name string) error {
	return s.MarkRotationAskedFor(ctx, name, "")
}

// MarkNotified records that the crew member was nudged about events through seq.
func (s *Store) MarkNotified(ctx context.Context, name string, seq uint64) error {
	return s.MarkNotifiedFor(ctx, name, "", seq)
}

// Journal writes an entry on the crew member's journal bead. A handoff also
// requests a fresh session. The entry is tagged with the crew session that
// wrote it (paneCrewSession), so each session reads back its own handoff;
// a write from no crew pane is the line's. From a session's or a crew
// member's pane the entry is recorded as that session's actor or that crew
// member, whatever actor says; from anywhere else, as given (paneAuthor).
func (s *Store) Journal(ctx context.Context, name, kind, text, actor string) error {
	limit := map[string]int{NoteJournal: MaxJournalBytes, NoteHandoff: MaxHandoffBytes}[kind]
	if limit == 0 {
		return fmt.Errorf("journal kind %q: use journal or handoff", kind)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty %s entry", kind)
	}
	if len(text) > limit {
		return fmt.Errorf("%s entry is %d bytes, over the %d-byte limit: summarize it", kind, len(text), limit)
	}
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		// The entry is tagged with the session that wrote it: the handoff
		// is per session, and the journal shared. A write from no crew
		// pane is the line's, as every entry was before sessions.
		_, session, err := paneCrewSession(w.Tx, callerOf(ctx))
		if err != nil {
			return err
		}
		if err := w.addNoteSession(r.Journal, kind, text, actor, session); err != nil {
			return err
		}
		if kind == NoteHandoff {
			t := now()
			if session == "" {
				r.LastHandoffAt = &t
				r.RestartRequested, r.RestartRequestedAt = "handoff", &t
				return nil
			}
			cs := r.ensureConversation(session)
			cs.LastHandoffAt = &t
			cs.RestartRequested, cs.RestartRequestedAt = "handoff", &t
			r.Conversations[session] = cs
		}
		return nil
	})
}

// Mail sends a message to the inbox of a crew member or of a user. files
// are the blobs it carries, each as its hash as PutBlob answered it with
// the name the sender gives it: the mail event names them (MailFiles),
// and mail with a file needs no text. From a session's or a crew member's
// pane the mail is recorded as that session's actor or that crew member,
// whatever actor says; from anywhere else, as given (paneAuthor).
func (s *Store) Mail(ctx context.Context, to, text, actor string, files ...BlobFile) error {
	if strings.TrimSpace(text) == "" && len(files) == 0 || len(text) > MaxMailBytes {
		return fmt.Errorf("mail must be 1..%d bytes", MaxMailBytes)
	}
	refs, err := s.mailFiles(files)
	if err != nil {
		return fmt.Errorf("mail to %s: %w", to, err)
	}
	return s.writeVia(ctx, func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		data := map[string]any{"to": to, "text": text}
		if len(refs) > 0 {
			data["files"] = refs
		}
		switch {
		case w.Bucket(bCrew).Get([]byte(to)) != nil:
			var r CrewMember
			if ok, err := get(w.Tx, bCrew, to, &r); err != nil {
				return err
			} else if ok && w.Bucket(bUsers).Get([]byte(actor)) != nil && !r.Serves(actor) {
				// Mail from a user it does not serve is refused at write,
				// naming whom it serves; anything else (the yard, another
				// crew member, a bead's agent) still lands, for the line
				// slice, or waits in the log where no line session runs.
				return fmt.Errorf("mail to %s from %s: %s serves %s", to, actor, to, strings.Join(r.Served(), ", "))
			}
		case w.Bucket(bUsers).Get([]byte(to)) != nil:
			data["to_user"] = true
		default:
			return fmt.Errorf("crew member %s: %w (nor is there a user of that name)", to, ErrNotFound)
		}
		return w.event("", "mail", actor, data)
	})
}

// forCrew reports whether e belongs in r's inbox: mail to it, a hook
// or bead event its subscriptions delivered to it when logged, or a kind
// it hears of through --on, never its own actions nor yardr telling of
// itself (aboutItself). --on works as implicit
// "yardr all kind=<k>" subscriptions, checked when the inbox is read, and
// also covers events not about a bead. A word of --on may be a class
// (KindAttention): what it holds is looked up here, each time.
func forCrew(r *CrewMember, e Event) bool {
	if e.Actor == r.Name || (e.Bead != nil && aboutItself(r, *e.Bead, e.Kind)) {
		return false
	}
	switch e.Kind {
	case "mail":
		return mailTo(e) == r.Name
	case "hook":
		return Reason(e, r.Name) != ""
	}
	return Reason(e, r.Name) != "" || slices.ContainsFunc(kindWords(EventKind(e.Kind)), func(word string) bool {
		return slices.Contains(r.On, word)
	})
}

// KindAttention names a class of kinds where one kind is given: in a crew
// member's --on, and as kind= in the filter of a yardr subscription. It is
// what a new crew member's --on holds, so the member hears the class as it
// is in this release and not as it was when the member was made.
const KindAttention = "attention"

// AttentionKinds are the kinds of the class KindAttention, the events that
// need someone: work that needs a decision, anything that went wrong,
// beads held (the roles tell a stuck agent to hold its bead for the
// yardmaster, so the hold must reach it), and beads that waited longer
// for a manual group than the group allows. nudge_held is of them because an
// agent whose input is held is told nothing until someone clears it or
// answers its harness's dialog there, backup_failed because a yard that
// asked for copies of its store and gets none finds out when it needs one,
// install_failed and rolled_back because a failed install needs someone
// told whether the yard answers or not, and hook_quiet because a webhook
// that stops arriving is otherwise found only by asking why nothing came.
// A kind added here reaches every crew member and subscription that names
// the class, whenever it was made: the name is what is stored, and it is
// resolved when an event is matched.
var AttentionKinds = []string{"gave_up", "session_blocked", "unrouted", "stranded", "no_runner", "workspace_kept",
	"crew_gave_up", "runtime_down", "session_idle", "session_aged", "session_stalled",
	"session_harness_error", "session_model_mismatch", "session_prompt_gave_up", "move_refused", "held", "waited", "nudge_held", "backup_failed",
	"note_untold", "install_failed", "rolled_back", HookQuiet}

// KindsOf are the kinds word names where a kind is given: those of the
// class when it is the class's name, else the one kind.
func KindsOf(word string) []string {
	if word == KindAttention {
		return AttentionKinds
	}
	return []string{word}
}

// kindWords are the words that ask for an event of kind: the kind, and the
// name of the class it is in. A list or a filter that holds any of them
// has the event, by the exact match it always was.
func kindWords(kind string) []string {
	if slices.Contains(AttentionKinds, kind) {
		return []string{kind, KindAttention}
	}
	return []string{kind}
}

// InboxPage is the start of a crew member's unacknowledged events.
type InboxPage struct {
	Events []Event `json:"events"` // oldest first, at most the limit
	Total  int     `json:"total"`  // all unacknowledged events
	Last   uint64  `json:"last"`   // seq of the newest unacknowledged event
}

// Inbox returns up to limit unacknowledged events for the crew member. It
// reads only the crew member's own index entries and those of its --on kinds
// after its cursor, never the rest of the log.
func (s *Store) Inbox(ctx context.Context, name string, limit int) (*InboxPage, error) {
	return s.InboxFor(ctx, name, "", limit)
}

// InboxFor is Inbox for one session's slice: the line session's ("") or
// the conversation session's for user. A user with no conversation is
// refused: its mail falls back to the line slice, which reads it.
func (s *Store) InboxFor(ctx context.Context, name, user string, limit int) (*InboxPage, error) {
	page := &InboxPage{Events: []Event{}}
	err := s.read(func(tx *bolt.Tx) error {
		r := new(CrewMember)
		if ok, err := get(tx, bCrew, name, r); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		cursor, err := sliceCursor(r, user)
		if err != nil {
			return err
		}
		var evs []Event
		for _, k := range inboxKeysFrom(tx, r, cursor) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if ok {
				evs = append(evs, e)
			}
		}
		s.inboxReads.Add(int64(len(evs)))
		named(tx, evs) // forCrew compares handles
		if err := peersToday(tx, evs); err != nil {
			return err
		}
		for _, e := range evs {
			if forSlice(r, user, e) {
				page.Total++
				page.Last = e.Seq
				if limit <= 0 || len(page.Events) < limit {
					page.Events = append(page.Events, e)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// sliceCursor is the inbox position of the slice user ("": the line
// session's). A conversation whose row the daemon has not made yet reads
// at the line cursor: the position its slice starts at when made.
func sliceCursor(r *CrewMember, user string) (uint64, error) {
	if user == "" {
		return r.Cursor, nil
	}
	if !r.HasConversation(user) {
		return 0, fmt.Errorf("crew member %s has no conversation session for %s", r.Name, user)
	}
	if cs, ok := r.Conversations[user]; ok {
		return cs.Cursor, nil
	}
	return r.Cursor, nil
}

// AskSessionOf is the crew session that asked the ask an answer event
// answers: the ask_session the answer was logged with, "" for the line's
// and for every answer from before sessions.
func AskSessionOf(e Event) string {
	who, _ := e.Data["ask_session"].(string)
	return who
}

// forSlice reports whether e belongs in the inbox slice of session user
// ("": the line session's) of r's one inbox. Mail from a user with a
// conversation goes to that slice alone; an ask's answer to the session
// that asked, falling back to the line where that conversation is gone
// or its row not made yet; everything else (events, mail from people
// with no conversation) to the line. Acking in one slice never hides
// what another has not seen: each slice acks through its own cursor.
func forSlice(r *CrewMember, user string, e Event) bool {
	if !forCrew(r, e) {
		return false
	}
	switch {
	case e.Kind == "mail":
		who := ""
		if r.sliced(e.Actor) {
			who = e.Actor
		}
		return user == who
	case Reason(e, r.Name) == ReasonAnswered:
		who := AskSessionOf(e)
		if !r.sliced(who) {
			who = ""
		}
		return user == who
	default:
		return user == ""
	}
}

// inboxKeys are the event keys after r's cursor that may be in its inbox,
// in seq order: its inbox index entries and those of the kinds it hears of
// through --on.
func inboxKeys(tx *bolt.Tx, r *CrewMember) []string {
	return inboxKeysFrom(tx, r, r.Cursor)
}

// inboxKeysFrom is inboxKeys after cursor: one session's slice of the
// same index entries.
func inboxKeysFrom(tx *bolt.Tx, r *CrewMember, cursor uint64) []string {
	from := seqKey(cursor + 1)
	seen := map[string]bool{}
	var out []string
	scan := func(b []byte, prefix string) {
		c := tx.Bucket(b).Cursor()
		p := []byte(prefix + "\x00")
		for k, _ := c.Seek([]byte(prefix + "\x00" + from)); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
			if sk := string(k[len(p):]); !seen[sk] {
				seen[sk] = true
				out = append(out, sk)
			}
		}
	}
	scan(bInbox, r.Name)
	for _, word := range r.On {
		for _, kind := range KindsOf(word) {
			// An event logged before the upgrade has the kind's old word.
			for _, k := range kindSpellings(kind) {
				scan(bEventsKd, k)
			}
		}
	}
	slices.Sort(out) // big-endian seqs sort as numbers
	return out
}

// AckInbox marks the inbox read through seq.
func (s *Store) AckInbox(ctx context.Context, name string, through uint64) error {
	return s.AckInboxFor(ctx, name, "", through)
}

// Prime is what a fresh crew member session reads first, bounded in size.
type Prime struct {
	CrewMember CrewMember `json:"crew"`
	// Session is the user of the conversation session primed, "" for the
	// line session's.
	Session         string         `json:"session,omitempty"`
	Handoff         *Note          `json:"handoff,omitempty"`
	Since           []Note         `json:"since"`   // journal entries after the handoff, newest last
	Omitted         int            `json:"omitted"` // entries after the handoff not shown
	Inbox           []Event        `json:"inbox"`
	InboxTotal      int            `json:"inbox_total"`
	Memories        []MemoryInfo   `json:"memories"`         // titles only, newest first
	MemoriesOmitted int            `json:"memories_omitted"` // memories not shown
	Timers          []Timer        `json:"timers"`           // the live ones it set or is told by
	Asks            []WriterAsk    `json:"asks"`             // its open asks, oldest first
	AsksOmitted     int            `json:"asks_omitted"`     // open asks not shown
	NoHandoff       bool           `json:"no_handoff"`
	EventCounts     map[string]int `json:"event_counts,omitempty"` // since the last handoff, when there was none at the end
	Recent          []Event        `json:"recent,omitempty"`
}

// CrewPrime gathers what the next session needs, and only that.
func (s *Store) CrewPrime(ctx context.Context, name string) (*Prime, error) {
	return s.CrewPrimeFor(ctx, name, "")
}

// CrewPrimeFor is CrewPrime for one session: the line session ("") or the
// conversation session for user. The handoff, the asks and the inbox are
// the session's own; the journal entries since, the memories and the
// timers are shared. A user with no conversation is refused.
func (s *Store) CrewPrimeFor(ctx context.Context, name, user string) (*Prime, error) {
	r, err := s.GetCrew(ctx, name)
	if err != nil {
		return nil, err
	}
	if user != "" && !r.HasConversation(user) {
		return nil, fmt.Errorf("crew member %s has no conversation session for %s", name, user)
	}
	st := r.Session(user)
	p := &Prime{CrewMember: *r, Session: user, Since: []Note{}}
	notes, err := s.Notes(ctx, r.Journal)
	if err != nil {
		return nil, err
	}
	var after []Note
	for i := len(notes) - 1; i >= 0; i-- {
		// The handoff is per session, over the one shared journal: each
		// session reads back the latest its own session wrote.
		if notes[i].Kind == NoteHandoff && notes[i].Session == user {
			h := notes[i]
			p.Handoff, after = &h, notes[i+1:]
			break
		}
	}
	if p.Handoff == nil {
		after = notes
	}
	if len(after) > PrimeEntries {
		p.Omitted, after = len(after)-PrimeEntries, after[len(after)-PrimeEntries:]
	}
	p.Since = append(p.Since, after...)

	inbox, err := s.InboxFor(ctx, name, user, PrimeInbox)
	if err != nil {
		return nil, err
	}
	p.Inbox, p.InboxTotal = inbox.Events, inbox.Total
	if p.Memories, err = s.Recall(ctx, "crew:"+name, ""); err != nil {
		return nil, err
	}
	// The prime cannot grow with the yard: the most recently written
	// titles, newest first, and how many it leaves out (yardr recall
	// finds them). Reads leave no trace, so what was read but not
	// written ages out like the rest.
	slices.SortFunc(p.Memories, func(a, b MemoryInfo) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(a.ID, b.ID))
	})
	if len(p.Memories) > PrimeMemories {
		p.MemoriesOmitted, p.Memories = len(p.Memories)-PrimeMemories, p.Memories[:PrimeMemories]
	}
	if p.Timers, err = s.Timers(ctx, name); err != nil {
		return nil, err
	}
	// What it still waits on from people, from the yard's records, so its
	// handoff need not carry that list as prose: the oldest first, and how
	// many the prime leaves out (bead list --asked-by lists them).
	asks, err := s.WriterAsks(ctx, name)
	if err != nil {
		return nil, err
	}
	// An ask's answer goes to the session that asked: each session primes
	// with the asks its own session wrote.
	p.Asks = []WriterAsk{}
	for _, a := range asks {
		if a.Session == user {
			p.Asks = append(p.Asks, a)
		}
	}
	if len(p.Asks) > PrimeAsks {
		p.AsksOmitted, p.Asks = len(p.Asks)-PrimeAsks, p.Asks[:PrimeAsks]
	}

	// The previous session ended without writing a handoff (it crashed, or
	// was rotated by force): summarize what happened since the last handoff,
	// or since that session started, so the new one can reconstruct.
	if prev := st.PrevStartedAt; prev != nil && (st.LastHandoffAt == nil || st.LastHandoffAt.Before(*prev)) {
		from := *prev
		if st.LastHandoffAt != nil {
			from = *st.LastHandoffAt
		}
		p.NoHandoff, p.EventCounts = true, map[string]int{}
		// Newest first, back to where that session began: the log before
		// it is never read.
		err := s.read(func(tx *bolt.Tx) error {
			c := tx.Bucket(bEvents).Cursor()
			for k, raw := c.Last(); k != nil; k, raw = c.Prev() {
				var e Event
				if err := json.Unmarshal(raw, &e); err != nil {
					return err
				}
				if !e.At.After(from) {
					break
				}
				p.EventCounts[e.Kind]++
				if len(p.Recent) < PrimeFallback {
					p.Recent = append(p.Recent, e)
				}
			}
			slices.Reverse(p.Recent)
			named(tx, p.Recent)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

// MemoryInfo is a memory as listed: enough to decide whether to read it.
type MemoryInfo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Scope     string     `json:"scope"`
	Expires   *time.Time `json:"expires,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type NewMemory struct {
	Title   string
	Body    string
	Scope   string // "all" or "crew:<name>" (or any label)
	Expires *time.Time
}

// Remember stores a piece of knowledge as a memory bead.
func (s *Store) Remember(ctx context.Context, m NewMemory, actor string) (*Bead, error) {
	if m.Scope == "" {
		m.Scope = "all"
	}
	meta := map[string]any{"scope": m.Scope}
	if m.Expires != nil {
		meta["expires_at"] = m.Expires.UTC().Format(time.RFC3339)
	}
	return s.CreateBead(ctx, NewBead{Title: m.Title, Body: m.Body, Type: TypeMemory, Stage: "memory",
		Prefix: "mem", Labels: []string{"scope:" + m.Scope}, Metadata: meta}, actor)
}

// Recall lists live memories visible in scope (its own and "all") whose
// title or body contains query (case-insensitive), titles only.
func (s *Store) Recall(ctx context.Context, scope, query string) ([]MemoryInfo, error) {
	q := strings.ToLower(query)
	beads, err := s.beadsWhere(true, time.Time{}, 0, func(_ *bolt.Tx, b *Bead) bool {
		if b.Type != TypeMemory || !b.open() {
			return false
		}
		sc, _ := b.Metadata["scope"].(string)
		if scope != "" && sc != "all" && sc != scope {
			return false
		}
		return q == "" || strings.Contains(strings.ToLower(b.Title+"\n"+b.Body), q)
	})
	if err != nil {
		return nil, err
	}
	out := []MemoryInfo{}
	for _, b := range beads {
		mi := MemoryInfo{ID: b.ID, Title: b.Title, Expires: expiry(&b), UpdatedAt: b.UpdatedAt}
		mi.Scope, _ = b.Metadata["scope"].(string)
		out = append(out, mi)
	}
	return out, nil
}

// JournalHit is one entry of a crew member's journal that a recall found.
type JournalHit struct {
	Crew string `json:"crew"`
	Note Note   `json:"note"`
}

// RecallJournal lists the entries of crew journals whose text contains
// query (case-insensitive), newest first: the journal read back, which
// the prime's titles and `crew show`'s last lines are not. With no query
// there is nothing to find. scope keeps one crew member's journal
// ("crew:<name>"); "" and "all" search every crew member's.
func (s *Store) RecallJournal(ctx context.Context, scope, query string) ([]JournalHit, error) {
	out := []JournalHit{}
	if strings.TrimSpace(query) == "" {
		return out, nil
	}
	q := strings.ToLower(query)
	crew, err := s.Crew(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range crew {
		if scope != "" && scope != "all" && scope != "crew:"+r.Name {
			continue
		}
		notes, err := s.Notes(ctx, r.Journal)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			if strings.Contains(strings.ToLower(n.Text), q) {
				out = append(out, JournalHit{Crew: r.Name, Note: n})
			}
		}
	}
	// Newest first, by the notes' store-wide order.
	slices.SortFunc(out, func(a, b JournalHit) int { return cmp.Compare(b.Note.Seq, a.Note.Seq) })
	return out, nil
}

// expiry is when the memory b expires (metadata expires_at), nil for never.
func expiry(b *Bead) *time.Time {
	if e, ok := b.Metadata["expires_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			return &t
		}
	}
	return nil
}

// ExpireMemories closes memories past their expiry as "expired" (retired,
// kept as history) and reports how many. The dispatcher asks on every pass,
// so it reads the open beads as the pass's other questions do (openBeads),
// and nothing of the memories that stay.
func (s *Store) ExpireMemories(ctx context.Context) (int, error) {
	var due []string
	at := time.Now()
	err := s.read(func(tx *bolt.Tx) error {
		rows, err := s.openBeads(tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.Type != TypeMemory {
				continue
			}
			if t := expiry(&r.Bead); t != nil && at.After(*t) {
				due = append(due, r.ID)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range due {
		if _, err := s.CloseBead(ctx, id, "expired", Expect{}, "yard"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
