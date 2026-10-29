package store_test

import (
	"reflect"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// An event told to a person who is not the operator keeps the keys of its
// kind's row and the two every kind tells, and no other: a kind with no row
// tells no data but those. A key whose row lists fields of it is told with
// those fields, from the record of an event just written (the store's own type)
// as from the one the log reads back. The event is a copy where something is
// held, since the store hands one event, and one map of its data, to every
// stream and every subscriber; where nothing is held it is the event itself,
// which a replay of a long log is spared a copy of.
func TestEventWithoutHeld(t *testing.T) {
	t.Parallel()
	crew := map[string]any{"crew": "yardmaster", "user": "sam", "via": "phone", "handle": map[string]any{"dir": "/home/jo/crew"}, "reason": "gone"}
	login := store.UserLogin{Groups: []string{"ada-builders"}, EnvFiles: []string{"/home/jo/ada.env"}, Routes: []string{"r1"}, Crew: []string{"ada-chat"}}
	told := map[string]any{"groups": []any{"ada-builders"}, "routes": []any{"r1"}, "crew": []any{"ada-chat"}}
	for _, c := range []struct {
		name string
		e    store.Event
		want map[string]any
		held bool // something was held; if not, the event comes back as it is, its data the same map
	}{
		{"a kind with a row", store.Event{Kind: "crew_stopped", Data: crew},
			map[string]any{"crew": "yardmaster", "user": "sam", "via": "phone"}, true},
		{"a kind with no row", store.Event{Kind: "a_kind_added_later", Data: map[string]any{"via": "phone", "notified": map[string]any{"ada": "all"}, "dir": "/home/jo"}},
			map[string]any{"via": "phone", "notified": map[string]any{"ada": "all"}}, true},
		{"a pack event tells the credential's name and holds the URL", store.Event{Kind: "pack_applied",
			Data: map[string]any{"pack": "starter", "depot": "web", "source": "https://packs.example.com/starter/", "auth": "ci-red"}},
			map[string]any{"pack": "starter", "depot": "web", "auth": "ci-red"}, true},
		{"data that is all told", store.Event{Kind: "crew_enabled", Data: map[string]any{"crew": "yardmaster"}},
			map[string]any{"crew": "yardmaster"}, false},
		{"a kind told whole", store.Event{Kind: "mail", Data: map[string]any{"to": "jo", "text": "see /home/jo/notes"}},
			map[string]any{"to": "jo", "text": "see /home/jo/notes"}, false},
		{"no data", store.Event{Kind: "crew_stopped"}, nil, false},
		{"a record just written, told by the fields its row lists", store.Event{Kind: "user_removed", Data: map[string]any{"user": "ada", "login": login}},
			map[string]any{"user": "ada", "login": told}, true},
		{"a record read back from the log", store.Event{Kind: "user_removed", Data: map[string]any{"user": "ada", "login": map[string]any{
			"groups": []any{"ada-builders"}, "env_files": []any{"/home/jo/ada.env"}, "routes": []any{"r1"}, "crew": []any{"ada-chat"}, "folder": "/home/jo/ada"}}},
			map[string]any{"user": "ada", "login": told}, true},
		{"a field the record gains later", store.Event{Kind: "user_removed", Data: map[string]any{"user": "ada", "login": map[string]any{"groups": []any{"ada-builders"}, "token_file": "/home/jo/t"}}},
			map[string]any{"user": "ada", "login": map[string]any{"groups": []any{"ada-builders"}}}, true},
		{"a count read from a session's file", store.Event{Kind: "session_usage", Data: map[string]any{"session": "s1", "group": "builders", "stage": "new", "kind": "claude",
			"usage": map[string]any{"input": 10, "fresh_input": 4, "output": 5, "cache_read": 6, "cache_write": 0, "read_at": "2026-10-22T08:05:00Z", "partial": true, "file": "/home/jo/.claude/s1.jsonl"}}},
			map[string]any{"session": "s1", "group": "builders", "stage": "new", "kind": "claude",
				"usage": map[string]any{"input": 10, "fresh_input": 4, "output": 5, "cache_read": 6, "cache_write": 0, "read_at": "2026-10-22T08:05:00Z", "partial": true}}, true},
		// A crew session's count is told as a bead session's is, by its member and
		// the user of a conversation; the id of its agent's own session is held.
		{"a count read from a crew session's file", store.Event{Kind: "crew_usage", Data: map[string]any{"member": "yardmaster", "user": "sam", "kind": "claude", "agent_session": "a2",
			"usage": map[string]any{"input": 10, "fresh_input": 4, "output": 5, "cache_read": 6, "cache_write": 0, "read_at": "2026-10-22T08:05:00Z", "partial": true, "file": "/home/jo/.claude/a2.jsonl"}}},
			map[string]any{"member": "yardmaster", "user": "sam", "kind": "claude",
				"usage": map[string]any{"input": 10, "fresh_input": 4, "output": 5, "cache_read": 6, "cache_write": 0, "read_at": "2026-10-22T08:05:00Z", "partial": true}}, true},
		{"a crew session's count that has none", store.Event{Kind: "crew_usage", Data: map[string]any{"member": "yardmaster", "kind": "muse", "agent_session": "",
			"usage": map[string]any{"missing": "no session id", "read_at": "2026-10-22T08:05:00Z"}}},
			map[string]any{"member": "yardmaster", "kind": "muse", "usage": map[string]any{"missing": "no session id", "read_at": "2026-10-22T08:05:00Z"}}, true},
		{"a value that is no record", store.Event{Kind: "user_removed", Data: map[string]any{"user": "ada", "login": "/home/jo/ada.env"}},
			map[string]any{"user": "ada"}, true},
		{"no value for the record", store.Event{Kind: "user_removed", Data: map[string]any{"user": "ada", "login": nil}},
			map[string]any{"user": "ada"}, true},
		{"a marked takeback tells no sentence, though its kind lists one", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": `yard took back yd-4x1k ask 2 "code is not logged in: how should the yard log in its harness?": the login ended: harness login: exit status 1`, "answer_held": true}},
			map[string]any{"note": uint64(7)}, true},
		{"an old login takeback, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": `yard took back yd-4x1k ask 2 "code is not logged in: how should the yard log in its harness?": the login ended: harness login: exit status 1`}},
			map[string]any{"note": uint64(7)}, true},
		{"an old takeback by a person, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": `mckean took back yd-4x1k ask 3 "May the night shift cover the queue?": the night shift covers it`}},
			map[string]any{"note": uint64(7),
				"answer": `mckean took back yd-4x1k ask 3 "May the night shift cover the queue?": the night shift covers it`}, false},
		{"an old pick whose words name the login's end", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": `mckean picked 1 (After Friday) on yd-4x1k ask 6 "when?": the login ended: friday works`}},
			map[string]any{"note": uint64(7),
				"answer": `mckean picked 1 (After Friday) on yd-4x1k ask 6 "when?": the login ended: friday works`}, false},
		{"a marked approval tells no sentence, though its kind lists one", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 3 on yd-4x1k, and the yard ran: yardr peer allow airy --mail-limit 300", "answer_held": true}},
			map[string]any{"note": uint64(7)}, true},
		{"a witnessed approval tells how it was seen, even when its answer is held", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 3 on yd-4x1k, and the yard ran: yardr peer allow airy --mail-limit 300", "answer_held": true, "witness": "terminal"}},
			map[string]any{"note": uint64(7), "witness": "terminal"}, true},
		{"an old approval of a setting, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 3 on yd-4x1k, and the yard ran: yardr settings set operator_note early-shift"}},
			map[string]any{"note": uint64(7)}, true},
		{"an old approval of a source add, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 4 on yd-4x1k, and the yard ran: yardr source add phone.yaml"}},
			map[string]any{"note": uint64(7)}, true},
		{"an old depot-push approval, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 5 on yd-4x1k, and the yard ran: yardr depot-push web"}},
			map[string]any{"note": uint64(7)}, true},
		{"an old approval of a setting whose value holds the run wording, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 3 on yd-4x1k, and the yard ran: yardr settings set operator_note early, and the yard ran: yardr peer allow airy"}},
			map[string]any{"note": uint64(7)}, true},
		{"an old approval of a told row, unmarked", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": "mckean approved ask 5 on yd-4x1k, and the yard ran: yardr peer allow airy --mail-limit 300"}},
			map[string]any{"note": uint64(7),
				"answer": "mckean approved ask 5 on yd-4x1k, and the yard ran: yardr peer allow airy --mail-limit 300"}, false},
		{"an old answer that is no approval", store.Event{Kind: "noted", Data: map[string]any{"note": uint64(7),
			"answer": `mckean picked 1 (After Friday) on yd-4x1k ask 6 "when?"`}},
			map[string]any{"note": uint64(7),
				"answer": `mckean picked 1 (After Friday) on yd-4x1k ask 6 "when?"`}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := map[string]any{}
			for k, v := range c.e.Data {
				before[k] = v
			}
			got, held := c.e.WithoutHeld()
			if !reflect.DeepEqual(got.Data, c.want) {
				t.Errorf("the data told is %v, want %v", got.Data, c.want)
			}
			if held != c.held {
				t.Errorf("something was held: %v, want %v", held, c.held)
			}
			if same := reflect.ValueOf(got.Data).Pointer() == reflect.ValueOf(c.e.Data).Pointer(); same == c.held {
				t.Errorf("the data is the event's own map: %v, want %v", same, !c.held)
			}
			if !reflect.DeepEqual(c.e.Data, before) && len(before) > 0 {
				t.Errorf("the event is now %v, want it as it was: %v", c.e.Data, before)
			}
			if got.Kind != c.e.Kind || got.Seq != c.e.Seq || got.Actor != c.e.Actor {
				t.Errorf("the event told is %+v, want the kind, the seq and the actor kept", got)
			}
		})
	}
	// The row a caller reads is its own.
	keys, ok := store.EventDataTold("crew_stopped")
	if !ok || len(keys) == 0 {
		t.Fatalf("crew_stopped has no row: %v", keys)
	}
	keys[0] = "dir"
	if again, _ := store.EventDataTold("crew_stopped"); again[0] == "dir" {
		t.Error("changing the row EventDataTold answered changed the table")
	}
}
