package store

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// What a person who is no operator is told of an event. An event's data
// is open: the kinds are many, they are written in many places, and a key a
// writer adds says nothing of whether its value is a file of an operator's, a
// command, an address or the words of an error that names one. So the data is
// told by an allow-list, as a device's settings are (rpc.deviceSettings):
// eventDataTold names, for each kind, the keys of its data the person is told,
// and every other key is held, as is every key of a kind that has no row. A
// kind added later tells no data until its author lists it, and rpc's
// TestEveryKindIsDecided fails until they do.
//
// What a row lists is what says which thing the event is about and what became
// of it: the names (a crew member, a user, a peer, a source, a hook, a depot,
// a flow, a group), the numbers, the words of a fixed vocabulary (a status, an
// outcome, a reason the yard names itself), and what the stream's contract
// promises (docs/api.md). What a row leaves out is a path on the yard's
// machine, a command, a URL, a pane's screen, the handle of a session, and any
// text that is made of an error or a command's output: a path with words
// around it is still the path, and nothing here scrubs one.
//
// A key whose value is a record can be told in part: a name with a dot, as
// login.groups, lists a field of it, and the key is then told with the fields
// the row lists and no others, so a field the record gains later is held until
// its author lists it, as a key is.
//
// An operator is told every event whole. That is the caller's to decide,
// since only it knows who is asking: the listener reads the role of a device's
// user (rpc.UserServer.deviceEvent), and the store reads it for each user a
// notification is written for (notify). The socket, the terminal, the agents
// and the web view read the event whole from the store.
var eventDataTold = map[string][]string{
	// Beads, and what is done to them.
	"created":            {"title", "stage", "type", "depot", "originator"},
	"advanced":           {"from", "to", "group", "outcome", "rounds", "max", "instead_of", "peer"},
	"rounds_reached":     {"stage", "outcome", "rounds", "to", "then"},
	"closed":             {"reason", "group", "originator"},
	"reopened":           {},
	"updated":            {"title", "body", "depot", "type", "priority", "labels_added", "labels_removed"},
	"originator_changed": {"from", "to", "approved"},
	"held":               {"session", "peer", "outcome"},
	"unheld":             {},
	"released":           {"reason"},
	"unblocked":          {"blockers"},
	"reblocked":          {"blockers"},
	"folded":             {"members", "left", "from", "to", "trigger", "points"},
	"dep_added":          {"from", "kind"},
	"dep_removed":        {"from", "kind"},
	"linked":             {"kind", "source", "thread", "moved_from", "moved_to", "moved_source_from", "replaced"},
	"unlinked":           {"kind", "source", "thread", "moved_source_to", "replaced_by"},
	"noted":              {"note", "ask", "session", "answer", "ask_session"},
	"journal":            {"note", "session"},
	"handoff":            {"note", "session"},
	"note_told":          {"note", "session", "delivery"},
	"note_untold":        {"note", "session"},
	"bad_template":       {"stage"},
	"ask_unreachable":    {"member", "stage"},
	"move_refused":       {"agent", "group", "reason", "session", "session_bead", "stage"},

	// Sessions: the claim of a bead, and what the daemon found out about the
	// agent that works it. The handle, the worktree, the pane and what its
	// screen shows are held, with the hints, which name commands.
	"claimed":                  {"group", "kind", "model", "route", "session", "stage", "was"},
	"started":                  {"session"},
	"session_died":             {"session"},
	"session_failed":           {"session"},
	"session_aborted":          {"session"},
	"session_blocked":          {"session", "stage"},
	"session_unblocked":        {"session", "stage"},
	"session_idle":             {"group", "idle", "limit", "session", "stage", "stop_after"},
	"session_aged":             {"group", "age", "limit", "session", "stage", "stop_after"},
	"session_stalled":          {"group", "idle", "limit", "model", "prompt_look", "session", "stage"},
	"session_idle_nudge":       {"delivery", "session", "stage"},
	"session_snoozed":          {"duration", "session"},
	"session_model_mismatch":   {"asked", "group", "reported", "session", "stage"},
	"session_prompted":         {"crew", "user", "deliveries", "n", "session", "shown", "stage", "submits"},
	"session_prompt_lost":      {"crew", "user", "delivery", "n", "of", "session", "stage"},
	"session_prompt_typed":     {"crew", "user", "n", "of", "session", "stage", "submit", "times"},
	"session_prompt_gave_up":   {"agent", "crew", "user", "deliveries", "session", "submits", "typed"},
	"session_harness_starting": {"crew", "user", "session"},
	"session_harness_dialog":   {"crew", "user", "group", "kind", "prompt_look", "session", "stage", "version"},
	"session_harness_error":    {"crew", "user", "group", "kind", "model", "prompt_shown", "reason", "resets_at", "session", "stage", "tries", "wakes_at", "window"},
	"session_harness_retry":    {"crew", "user", "group", "kind", "of", "reason", "session", "stage", "try", "window"},
	"workspace_kept":           {"session"},
	"workspace_removed":        {"session"},
	"human_notified":           {"group", "session", "stage"},
	"nudge_held":               {"crew", "user", "session", "waited"},
	"login_led":                {"ask", "crew", "kind", "method", "proved", "restarted", "step"},
	"train_branched":           {"branch", "child", "commit", "from"},

	// What a stopped session's agent used: the counts, the time they were read,
	// whether they fall short, and the reason there is none (toldUsage). The
	// store keeps the reading as the daemon gives it and reads none of it, so it
	// is told field by field. A crew session is named by its member and the user
	// of a conversation. The id of its agent's own session is held: it names the
	// agent's file on the yard's machine, and no one who is told the count needs
	// it.
	"session_usage": slices.Concat([]string{"group", "kind", "session", "stage"}, toldUsage),
	"crew_usage":    slices.Concat([]string{"kind", "member", "user"}, toldUsage),

	// Routing: why a bead waits. The reasons are made of the errors of git,
	// the flows' files and the limits' commands, so none is told but the
	// stage.
	"gave_up":             {"failures", "stage"},
	"backoff":             {"stage"},
	"capped":              {"stage"},
	"unrouted":            {"stage"},
	"no_runner":           {"stage"},
	"bad_depot":           {"stage"},
	"bad_flow":            {"stage"},
	"bad_train":           {"stage"},
	"wagon_without_train": {"stage"},
	"stranded":            {"stage", "flow", "source"},
	"quota_wait":          {"stage", "route", "resets_at"},
	"group_wait":          {"stage", "route"},
	"waited":              {"group", "since", "stage", "waited"},
	"hook_rule":           {"action", "bead", "depot", "event", "hook", "linked_author", "originator_rule", "outcome", "route", "seq", "stage", "train", "worker"},
	"make_refused":        {"action", "bead", "depot", "event", "hook", "seq", "stage"},

	// Crew: who runs, and in what state. The handle of a session is the
	// directory it started in and the yardr it was told to run.
	"crew_defined":           {"approved", "crew", "runner"},
	"crew_deleted":           {"crew", "unexposed"},
	"crew_enabled":           {"crew"},
	"crew_disabled":          {"crew"},
	"crew_started":           {"crew", "user"},
	"crew_status":            {"crew", "status", "was", "user"},
	"crew_login":             {"crew", "login_led", "login_screen", "user"},
	"crew_stopped":           {"crew", "user"},
	"crew_start_failed":      {"crew", "failures", "user"},
	"crew_gave_up":           {"crew", "failures", "user"},
	"crew_start_tail":        {"crew", "user"},
	"crew_blocked":           {"agent", "crew", "user"},
	"crew_restart_requested": {"crew", "user"},
	"crew_rotation_asked":    {"crew", "user"},
	"crew_context_asked":     {"crew", "pct", "window", "user"},
	"open_threads_granted":   {"crew", "pack", "sources"},
	"inbox_expired":          {"crew", "events"},
	"thread_opened":          {"crew", "in", "seq", "source", "thread", "title"},
	"replied":                {"crew", "in", "seq", "source", "thread", "title"},
	"thread_state":           {"cause", "crew", "hook", "instead", "source", "stage", "state", "status", "thread", "title", "was"},
	"thread_state_skipped":   {"attempts", "cause", "crew", "hook", "reason", "source", "stage", "state", "status", "thread", "title"},

	// Users and their devices. The file a user's address is read from is the
	// operator's configuration, though the user is the one told through it, and
	// so are the files the login of a removed user named, which stay on disk
	// when the user goes: the names of what went with them are the yard's news.
	"user_added":          {"user", "approved"},
	"user_removed":        {"user", "approved", "as_grants", "devices", "unexposed", "unreached", "untrusted", "login.groups", "login.routes", "login.crew"},
	"user_set":            {"user", "approved", "notify", "tell"},
	"device_added":        {"device", "user", "pairing"},
	"device_removed":      {"device", "user"},
	"device_pair_opened":  {"device", "user", "pairing", "expires", "wrong_codes"},
	"device_pair_refused": {"device", "user", "pairing", "reason", "wrong_codes"},
	"identity_set":        {"user", "crew", "source", "id", "key", "approved"},
	"identity_removed":    {"user", "crew", "source", "id"},
	"subscribed":          {"crew", "group", "user", "subscription", "approved"},
	"unsubscribed":        {"crew", "group", "user", "subscriptions"},
	"sender_unignored":    {"approved", "sender", "source"},
	"notifications_read":  {"reader", "through"},
	"push_failed":         {"reason", "seqs", "source", "status", "tries", "user"},

	// Mail goes whole to its parties, as the listener decides who they are
	// (rpc.UserServer.deviceEvent), and to no one else.
	"mail":      {toldWhole},
	"mail_read": {toldWhole},
	"reaction":  {toldWhole},

	// Sources, hooks and what they carry. The file a source reads its
	// credential from, the command it mints a token with, the base of its
	// calls, the file a hook's secret is read from and the values a call was
	// made with are an operator's.
	"source_added":         {"source", "calls", "limits", "replaced", "approved"},
	"source_set":           {"source", "approved", "calls_per_hour", "updates_per_hour"},
	"source_granted":       {"approved", "source", "what", "as", "crew", "group"},
	"source_call":          {"as", "attempt", "by", "bytes", "call", "crew", "group", "kind", "ms", "params", "session", "source", "status"},
	"hook":                 {"action", "actor", "assignee", "delegate", "delivery", "entity", "fields", "files", "held", "hook", "mentions", "message", "new_mentions", "parent", "source", "text", "there", "thread", "type", "url"},
	"hook_added":           {"hook", "source", "as", "approved", "unknown"},
	"hook_removed":         {"hook", "issue_moves_dropped"},
	"hook_blocked":         {"hook"},
	"hook_rejected":        {"count", "hook", "last", "reason", "status"},
	"hook_files":           {"seq"},
	"hook_quiet":           {"gap", "hook", "last", "source"},
	"hook_listener_failed": {},
	"pack_applied":         {"approved", "auth", "commit", "depot", "pack"},
	"pack_unchanged":       {"approved", "auth", "commit", "depot", "pack"},

	// The yard's own arrangement: depots, flows, groups and routes. A depot's
	// path and test command, a route's note and the conditions of its groups
	// are held.
	"depot_updated":        {"depot", "kind", "base"},
	"depot_removed":        {"depot", "flow_pointers", "routes"},
	"flow_set":             {"flow"},
	"flow_removed":         {"flow"},
	"flow_pointer_set":     {"depot", "flow", "type"},
	"flow_pointer_cleared": {"depot", "type"},
	"group_set":            {"limit", "members", "name", "runner"},
	"group_removed":        {"name", "released", "routes"},
	"route_added":          {"id", "stage", "group", "priority", "label", "labels", "bead_priority", "type", "depot", "originator"},
	"route_removed":        {"id", "stage", "group", "priority", "label", "labels", "bead_priority", "type", "depot", "originator"},
	"route_set":            {},
	"setting_changed":      {"key", "approved", "reason"},

	// Peers: the yards this one is linked with. A link's URL, the git remote
	// a bead's branch travels by and the words of an error are held.
	"peer_added":            {"peer", "key"},
	"peer_allowed":          {"approved", "beads_away", "expose", "mail_limit", "peer", "reach", "receive", "send", "trust"},
	"peer_removed":          {"asks", "beads", "beads_away", "crew", "messages_dropped", "peer"},
	"peer_renamed":          {"approved", "crew", "from", "groups", "hooks", "messages", "peer", "subscriptions", "to"},
	"peer_url_set":          {"peer"},
	"peer_paired":           {"key", "pairing", "peer", "role"},
	"peer_pair_opened":      {"expires", "pairing", "role"},
	"peer_pair_refused":     {"key", "pairing", "peer", "reason", "role", "wrong_codes"},
	"peer_identity_set":     {"approved", "crew", "id", "peer", "source", "was"},
	"peer_identity_removed": {"crew", "id", "peer", "source"},
	"peer_intake":           {"approved", "default", "depot", "flow", "limit", "none", "peer"},
	"peer_trust_dropped":    {"names", "peer", "reason"},
	"peer_reach_dropped":    {"names", "peer", "reason"},
	"peer_message_sent":     {"attempts", "id", "kind", "peer", "re", "way"},
	"peer_message_received": {"id", "kind", "peer", "re", "way"},
	"peer_message_failed":   {"attempts", "id", "kind", "peer", "re", "way"},
	"peer_message_refused":  {"ask", "bead", "direction", "here", "id", "kind", "known", "limit", "open", "peer", "re", "reason", "seq", "stage", "to", "until", "v", "way", "yards"},
	"peer_bead_arrived":     {"depot", "flow", "from", "id", "notes_left_out", "peer", "stage", "way"},
	"peer_bead_sent":        {"group", "id", "notes", "notes_left_out", "peer", "session", "stage", "tries", "yards_before"},
	"peer_bead_returned":    {"branch", "commit", "from", "id", "notes", "notes_left_out", "outcome", "peer", "re", "stage", "stale", "to", "way"},
	"peer_bead_event":       {"event", "from", "held", "id", "left", "peer", "re", "stage", "stale", "way"},
	"peer_bead_noted":       {"direction", "from", "id", "peer", "re", "to", "way"},
	"peer_bead_read":        {"answer", "from", "held", "id", "notes", "notes_left_out", "peer", "re", "stage", "stale", "to", "way"},
	"peer_bead_fetched":     {"attempts", "branch", "commit", "into", "peer"},
	"peer_bead_not_fetched": {"attempts", "branch", "commit", "held", "peer"},
	"peer_bead_refused":     {"id", "outcome", "peer", "sent", "stage"},
	"peer_bead_undelivered": {"attempts", "id", "outcome", "peer", "sent", "stage"},

	// Timers: when they are due and for whom. What a timer says is mail to
	// its user, in the person's own words, and so is not told.
	"timer_set":     {"timer", "to", "due", "ends", "every"},
	"timer":         {"by", "last", "late", "set_at", "timer", "to", "to_user"},
	"timer_dropped": {"by", "timer", "to"},
	"timer_removed": {"approved", "by", "timer", "to"},

	// The yard: its daemon, its server, its disk and its upgrades. The
	// directories of a backup, of the build cache and of a yard's home, and
	// the words of what failed, are held.
	"daemon_started":    {"to"},
	"daemon_replaced":   {"from", "to"},
	"server_replaced":   {"from", "to"},
	"replace_skipped":   {"candidate", "process", "running"},
	"runtime_down":      {"group", "runner"},
	"runtime_up":        {"group", "runner"},
	"backup_failed":     {"every"},
	"disk_kept":         {"free_after", "free_before", "gave_up"},
	"store_pruned":      {"blobs", "events", "expired", "hook_payloads", "peers", "sessions"},
	"install_failed":    {"beads", "from", "restored", "revision", "stage", "to", "told"},
	"rolled_back":       {"beads", "from", "restored", "revision", "stage", "to", "told"},
	"base_diverged":     {"base", "depot", "local", "remote", "upstream"},
	"quiet_started":     {"base", "depot", "landings"},
	"quiet_passed":      {"base", "commit", "depot", "landings", "load", "wall_s"},
	"quiet_failed":      {"base", "commit", "depot", "landings", "load", "wall_s"},
	"user_refused":      {"count", "last", "reason", "status"},
	"edge_link_up":      {},
	"edge_link_down":    {},
	"depots_migrated":   {"from"},
	"flows_migrated":    {"flows", "from"},
	"settings_migrated": {"default_kind", "from", "hearer", "yardmaster"},
}

// toldWhole stands in the row of a kind that is told whole to those it goes
// to: mail, whose parties the listener decides (rpc.UserServer.deviceEvent). A
// row that lists it lists no key besides.
const toldWhole = "*"

// toldEveryKind are the keys every kind tells: the device a call was written
// through, which every event a device's call writes carries (docs/api.md,
// "What it writes says how it came"), and the users an event became a
// notification for, which tells a client to read its notifications again.
var toldEveryKind = []string{"via", "notified"}

// toldUsage are the fields of a count that the kinds which carry one tell
// (session_usage and crew_usage). The daemon makes the record of both with one
// function, so one list says what of it is told and the two cannot differ. The
// reasons for a missing count are the runner's own words, never a path.
var toldUsage = []string{"usage.input", "usage.fresh_input", "usage.output", "usage.cache_read",
	"usage.cache_write", "usage.partial", "usage.kept", "usage.read_at", "usage.missing"}

// EventDataToldKinds are the kinds eventDataTold has a row for, sorted: those
// whose data a decision was made for.
func EventDataToldKinds() []string { return slices.Sorted(maps.Keys(eventDataTold)) }

// EventDataTold are the keys of the data of an event of kind that a person
// who is no operator is told, beside the keys every kind tells, and
// whether the kind has a row at all. A field of a record is listed as
// key.field.
func EventDataTold(kind string) ([]string, bool) {
	keys, ok := eventDataTold[kind]
	return slices.Clone(keys), ok
}

// WithoutHeld is the event as a person who is no operator is told it, and
// whether anything may have been held: its data held to the keys its kind's row
// lists and the keys every kind tells, and a key the row lists fields of held
// to those fields (which counts as held even where the record has no other
// field). A kind with no row tells no data. The event is copied, not changed:
// the store hands one event, and one map of its data, to every stream. An event
// that has nothing to hold comes back as it is.
func (e Event) WithoutHeld() (Event, bool) {
	keys := eventDataTold[e.Kind]
	if slices.Contains(keys, toldWhole) {
		return e, false
	}
	told := func(k string) bool { return slices.Contains(keys, k) || slices.Contains(toldEveryKind, k) }
	held := false
	for k := range e.Data {
		if !told(k) {
			held = true
			break
		}
	}
	if !held {
		return e, false
	}
	data := make(map[string]any, len(e.Data))
	for k, v := range e.Data {
		if told(k) {
			data[k] = v
		} else if fields := fieldsTold(keys, k); len(fields) > 0 {
			if record, ok := recordHeldTo(v, fields); ok {
				data[k] = record
			}
		}
	}
	e.Data = data
	return e, true
}

// fieldsTold are the fields of the key that a row lists as key.field.
func fieldsTold(row []string, key string) []string {
	var fields []string
	for _, entry := range row {
		if name, field, ok := strings.Cut(entry, "."); ok && name == key {
			fields = append(fields, field)
		}
	}
	return fields
}

// recordHeldTo is the record v with the fields listed and no others. The
// record of an event just written is the store's own type and one read back
// from the log is a decoded object, so a type is taken through its JSON, which
// is how the log keeps it. A value that is no record is held whole.
func recordHeldTo(v any, fields []string) (map[string]any, bool) {
	record, ok := v.(map[string]any)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil || json.Unmarshal(b, &record) != nil || record == nil {
			return nil, false
		}
	}
	told := make(map[string]any, len(fields))
	for _, f := range fields {
		if x, ok := record[f]; ok {
			told[f] = x
		}
	}
	return told, true
}
