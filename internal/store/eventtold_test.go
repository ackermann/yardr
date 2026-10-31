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
	approved := map[string]any{"bead": "yd-4x1k", "ask": uint64(3)}
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
		{"a depot update tells what approved it and holds the path", store.Event{Kind: "depot_updated",
			Data: map[string]any{"depot": "yardr", "kind": "git", "base": "main",
				"path": "/home/jo/projects/yardr", "test": "sh .yardr/test", "approved": approved}},
			map[string]any{"depot": "yardr", "kind": "git", "base": "main", "approved": approved}, true},
		{"a peer add tells what approved it and holds the URL", store.Event{Kind: "peer_added",
			Data: map[string]any{"peer": "north", "key": "k1", "url": "https://north.example.com", "approved": approved}},
			map[string]any{"peer": "north", "key": "k1", "approved": approved}, true},
		{"a peer url set tells what approved it and holds the addresses", store.Event{Kind: "peer_url_set",
			Data: map[string]any{"peer": "north", "url": "https://north.example.com", "was": "https://old.example.com", "approved": approved}},
			map[string]any{"peer": "north", "approved": approved}, true},
		{"a peer removal tells what approved it and holds the identities", store.Event{Kind: "peer_removed",
			Data: map[string]any{"peer": "north", "asks": 2, "identities": []any{"crm:ada"}, "approved": approved}},
			map[string]any{"peer": "north", "asks": 2, "approved": approved}, true},
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

// EventFor is the event as viewer is told it: mail, a read receipt and a
// reaction to their parties alone and whole, nobody party to none; a
// setting_changed with its value for a told setting alone; every other
// kind whole for an operator and held to its row otherwise. Whether the
// viewer may see the event's bead is the caller's to ask, not told here.
func TestEventFor(t *testing.T) {
	t.Parallel()
	setting := func(key, value string) store.Event {
		return store.Event{Kind: "setting_changed", Actor: "ada", Data: map[string]any{"key": key, "value": value}}
	}
	for _, c := range []struct {
		name     string
		viewer   string
		operator bool
		e        store.Event
		// want is the data told; nil drops the event.
		want map[string]any
	}{
		{"mail to the viewer", "ada", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "ada", "text": "hi"}},
			map[string]any{"to": "ada", "text": "hi"}},
		{"mail from the viewer", "bob", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "ada", "text": "hi"}},
			map[string]any{"to": "ada", "text": "hi"}},
		{"mail to others", "sam", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "ada", "text": "hi"}}, nil},
		{"mail to nobody", "", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "ada", "text": "hi"}}, nil},
		{"mail that left is its sender's alone", "bob", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "zed", "to_peer": "north", "text": "hi"}},
			map[string]any{"to": "zed", "to_peer": "north", "text": "hi"}},
		{"mail that left is not nobody's", "", false,
			store.Event{Kind: "mail", Actor: "bob", Data: map[string]any{"to": "zed", "to_peer": "north", "text": "hi"}}, nil},
		{"mail with no actor is not nobody's", "", false,
			store.Event{Kind: "mail", Data: map[string]any{"to": "ada", "text": "hi"}}, nil},
		{"a read receipt to its reader", "ada", false,
			store.Event{Kind: "mail_read", Actor: "bob", Data: map[string]any{"reader": "ada"}},
			map[string]any{"reader": "ada"}},
		{"a read receipt to another", "sam", false,
			store.Event{Kind: "mail_read", Actor: "bob", Data: map[string]any{"reader": "ada"}}, nil},
		{"a read receipt with no reader named is its actor's", "bob", false,
			store.Event{Kind: "mail_read", Actor: "bob", Data: map[string]any{}},
			map[string]any{}},
		{"a reaction to its reactor", "ada", false,
			store.Event{Kind: "reaction", Actor: "ada", Data: map[string]any{"to": "bob", "text": "hi", "emoji": "+1"}},
			map[string]any{"to": "bob", "text": "hi", "emoji": "+1"}},
		{"a reaction to the mail's other party", "bob", false,
			store.Event{Kind: "reaction", Actor: "ada", Data: map[string]any{"to": "bob", "text": "hi", "emoji": "+1"}},
			map[string]any{"to": "bob", "text": "hi", "emoji": "+1"}},
		{"a reaction to another", "sam", false,
			store.Event{Kind: "reaction", Actor: "ada", Data: map[string]any{"to": "bob", "text": "hi", "emoji": "+1"}}, nil},
		{"a setting no device is told carries its key alone, for a member", "sam", false,
			setting(store.WebJWTUsersSetting, "ada@example.net=ada"),
			map[string]any{"key": store.WebJWTUsersSetting}},
		{"a setting no device is told carries its key alone, for an operator", "ada", true,
			setting(store.WebJWTUsersSetting, "ada@example.net=ada"),
			map[string]any{"key": store.WebJWTUsersSetting}},
		{"a told setting carries its value to a member", "sam", false,
			setting(store.OperatorSetting, "ada"),
			map[string]any{"key": store.OperatorSetting, "value": "ada"}},
		{"a cap carries its value to a member", "sam", false,
			setting(store.FilesMaxSetting, "512"),
			map[string]any{"key": store.FilesMaxSetting, "value": "512"}},
		{"the operator's words carry their value to an operator", "ada", true,
			setting(store.StyleSetting, "guide"),
			map[string]any{"key": store.StyleSetting, "value": "guide"}},
		{"the operator's words carry no value to a member", "sam", false,
			setting(store.NoteSetting, "early shift"),
			map[string]any{"key": store.NoteSetting}},
		{"a refusal whole for an operator", "ada", true,
			store.Event{Kind: "move_refused", Actor: "yard", Data: map[string]any{"session": "s1", "reason": "not_allowed",
				"command": "yardr bead done yd-1 --outcome shipped", "error": "flow has no outcome shipped"}},
			map[string]any{"session": "s1", "reason": "not_allowed",
				"command": "yardr bead done yd-1 --outcome shipped", "error": "flow has no outcome shipped"}},
		{"a refusal held to its row for a member", "sam", false,
			store.Event{Kind: "move_refused", Actor: "yard", Data: map[string]any{"session": "s1", "reason": "not_allowed",
				"command": "yardr bead done yd-1 --outcome shipped", "error": "flow has no outcome shipped"}},
			map[string]any{"session": "s1", "reason": "not_allowed"}},
		{"a kind with no row tells nobody its data but the keys every kind tells", "sam", false,
			store.Event{Kind: "a_kind_added_later", Actor: "yard",
				Data: map[string]any{"via": "phone", "notified": map[string]any{"ada": "all"}, "dir": "/home/jo"}},
			map[string]any{"via": "phone", "notified": map[string]any{"ada": "all"}}},
		{"a kind with no row is whole for an operator", "ada", true,
			store.Event{Kind: "a_kind_added_later", Actor: "yard",
				Data: map[string]any{"via": "phone", "dir": "/home/jo"}},
			map[string]any{"via": "phone", "dir": "/home/jo"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := store.EventFor(c.viewer, c.operator, c.e)
			if c.want == nil {
				if ok {
					t.Fatalf("told %+v, want the event dropped", got.Data)
				}
				return
			}
			if !ok {
				t.Fatal("the event is dropped, want it told")
			}
			if !reflect.DeepEqual(got.Data, c.want) {
				t.Errorf("the data told is %v, want %v", got.Data, c.want)
			}
			if got.Kind != c.e.Kind || got.Seq != c.e.Seq || got.Actor != c.e.Actor || got.Bead != c.e.Bead {
				t.Errorf("the event told is %+v, want all but the data kept", got)
			}
		})
	}
}
