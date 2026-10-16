package store

import "testing"

// TestMatch is the one matcher over both kinds of event: a hook envelope,
// flattened (hookFields), and a bead event's fields as beadFields makes
// them. Each scope is asked with each way its holder may stand to the
// event, and the filter with the keys of each.
func TestMatch(t *testing.T) {
	t.Parallel()
	hook := hookFields(map[string]any{
		"source": "linear", "type": "Comment", "action": "create",
		"fields": map[string]any{"team": []any{"ENG"}, "label": []any{"bug", "p1"}, "type": []any{"shadowed"}},
	})
	bead := map[string][]string{
		"bead": {"web-1"}, "type": {"task"}, "label": {"bug", "p1"}, "kind": {"closed", KindAttention},
		"actor": {"amy"}, "depot": {"web"}, "state": {StateDone},
	}
	type standing struct{ mentioned, assigned, participant bool }
	var (
		nobody      = standing{}
		mentioned   = standing{mentioned: true}
		assigned    = standing{assigned: true}
		participant = standing{participant: true}
		everything  = standing{true, true, true}
	)
	for _, ev := range []struct {
		name   string
		source string
		fields map[string][]string
		hit    map[string]string // a filter the event passes
		miss   map[string]string // one it does not
	}{
		{"hook", "linear", hook,
			map[string]string{"type": "Comment", "action": "create", "team": "ENG", "label": JoinWhere([]string{"p1", "p9"})},
			map[string]string{"type": "Comment", "team": "OPS"}},
		{"yardr", SourceYardr, bead,
			map[string]string{"depot": "web", "kind": KindAttention, "label": JoinWhere([]string{"p1", "p9"}), "state": StateDone},
			map[string]string{"depot": "web", "bead": "web-2"}},
	} {
		for _, tc := range []struct {
			scope string
			who   standing
			want  string
		}{
			{ScopeAll, nobody, ReasonAll},
			{ScopeAll, participant, ReasonParticipant},
			{ScopeAll, assigned, ReasonAssigned},
			{ScopeAll, mentioned, ReasonMentioned},
			{ScopeAll, everything, ReasonMentioned},
			{ScopeMentions, nobody, ""},
			{ScopeMentions, participant, ""},
			{ScopeMentions, assigned, ""},
			{ScopeMentions, mentioned, ReasonMentioned},
			{ScopeMentions, everything, ReasonMentioned},
			{ScopeInvolved, nobody, ""},
			{ScopeInvolved, participant, ReasonParticipant},
			{ScopeInvolved, assigned, ReasonAssigned},
			{ScopeInvolved, mentioned, ReasonMentioned},
			{ScopeInvolved, standing{assigned: true, participant: true}, ReasonAssigned},
		} {
			ask := func(where map[string]string) string {
				return match(Subscription{Source: ev.source, Scope: tc.scope, Where: where}, ev.fields,
					given(tc.who.mentioned), given(tc.who.assigned), given(tc.who.participant))
			}
			if got := ask(nil); got != tc.want {
				t.Errorf("%s, scope %s, %+v: reason %q, want %q", ev.name, tc.scope, tc.who, got, tc.want)
			}
			if got := ask(ev.hit); got != tc.want {
				t.Errorf("%s, scope %s, %+v, filter passed: reason %q, want %q", ev.name, tc.scope, tc.who, got, tc.want)
			}
			if got := ask(ev.miss); got != "" {
				t.Errorf("%s, scope %s, %+v, filter not passed: reason %q, want none", ev.name, tc.scope, tc.who, got)
			}
		}
	}
}

// A hook's type and action are the envelope's: a field its adapter called
// the same does not stand in for them, and one that is no list of strings
// matches nothing.
func TestHookFields(t *testing.T) {
	t.Parallel()
	built := hookFields(map[string]any{"type": "Issue", "fields": map[string][]string{"team": {"ENG"}, "action": {"create"}}})
	decoded := hookFields(map[string]any{"type": "Issue", "fields": map[string]any{"team": []any{"ENG"}, "action": []any{"create"}, "repo": "a/b"}})
	for name, f := range map[string]map[string][]string{"built in process": built, "decoded": decoded} {
		all := Subscription{Scope: ScopeAll}
		for _, tc := range []struct {
			where map[string]string
			want  string
		}{
			{map[string]string{"type": "Issue", "team": "ENG"}, ReasonAll},
			{map[string]string{"action": "create"}, ""},
			{map[string]string{"repo": "a/b"}, ""},
		} {
			all.Where = tc.where
			if got := match(all, f, given(false), given(false), given(false)); got != tc.want {
				t.Errorf("%s, where %v: reason %q, want %q", name, tc.where, got, tc.want)
			}
		}
	}
}

// What reads the store to say how a holder stands to an event is asked
// only when the answer can change the reason: not when the filter fails,
// and a mentions subscription never asks who was assigned or took part.
func TestMatchAsksOnlyWhatItNeeds(t *testing.T) {
	t.Parallel()
	asked := ""
	ask := func(name string, v bool) func() bool {
		return func() bool { asked += name; return v }
	}
	fields := map[string][]string{"depot": {"web"}}
	for _, tc := range []struct {
		sub   Subscription
		asked string
	}{
		{Subscription{Scope: ScopeInvolved, Where: map[string]string{"depot": "api"}}, ""},
		{Subscription{Scope: ScopeMentions}, "m"},
		{Subscription{Scope: ScopeInvolved}, "map"},
		{Subscription{Scope: ScopeAll, Where: map[string]string{"depot": "web"}}, "map"},
	} {
		asked = ""
		match(tc.sub, fields, ask("m", false), ask("a", false), ask("p", false))
		if asked != tc.asked {
			t.Errorf("%s: asked %q, want %q", tc.sub, asked, tc.asked)
		}
	}
}
