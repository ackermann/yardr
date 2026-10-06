package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// matrixYard is a yard with two crew members, a user (jo) and a manual
// group that share one subscription and differ in the others. clerk has
// the shared one through the group only, and yardmaster has held both in
// --on and as a subscription.
func matrixYard(t *testing.T) (text string, m subMatrix) {
	t.Helper()
	st := userYard(t)
	// init made the yardmaster, with the default --on.
	mustAs(t, st, "jo", "crew", "set", "yardmaster", "--on", "gave_up,held")
	mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "kind=claude", "--on", "held")
	mustAs(t, st, "jo", "group", "add", "approvers", "--runner", "manual", "--members", "jo,clerk")
	for _, args := range [][]string{
		{"yardmaster", "yardr", "--done"},
		{"jo", "yardr", "--done"},
		{"approvers", "yardr", "--done"},
		{"yardmaster", "linear"},
		{"yardmaster", "yardr", "--where", "kind=held"},
		{"jo", "yardr", "--where", "kind=move_refused"},
		{"approvers", "yardr", "--where", "stage=approved"},
	} {
		mustAs(t, st, "jo", append([]string{"subscribe"}, args...)...)
	}
	text = mustAs(t, st, "jo", "subscribe")
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "subscribe", "--json")), &m); err != nil {
		t.Fatalf("subscribe --json: %v", err)
	}
	return text, m
}

// Bare yardr subscribe lays every subscriber's --on kinds and subscriptions
// side by side: the row all of them have is marked in each column and
// counted, a row one of them has is marked once, and a kind a crew member
// has both ways says so (yardr-a3s2).
func TestSubscribeMatrix(t *testing.T) {
	text, m := matrixYard(t)
	row := func(sub string) subRow {
		t.Helper()
		for _, r := range m.Rows {
			if r.Subscription.String() == sub {
				return r
			}
		}
		t.Fatalf("no row %q in:\n%s", sub, text)
		return subRow{}
	}
	marks := func(r subRow) string {
		var out []string
		for _, name := range sortedKeys(r.Marks) {
			out = append(out, name+"="+strings.Join(r.Marks[name], "+"))
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct{ sub, marks, reaches string }{
		{"yardr all state=done", "approvers=sub clerk=via approvers jo=sub yardmaster=sub", "clerk,jo,yardmaster"},
		{"linear involved", "yardmaster=sub", "yardmaster"},
		{"yardr all stage=approved", "approvers=sub clerk=via approvers", "clerk"},
		{"yardr all kind=held", "clerk=on yardmaster=on+sub", "clerk,yardmaster"},
		{"yardr all kind=gave_up", "yardmaster=on", "yardmaster"},
		{"yardr all kind=move_refused", "jo=sub", "jo"},
	} {
		r := row(tc.sub)
		if got := marks(r); got != tc.marks {
			t.Errorf("%s: marks %q, want %q", tc.sub, got, tc.marks)
		}
		if got := strings.Join(r.Reaches, ","); got != tc.reaches {
			t.Errorf("%s: reaches %q, want %q", tc.sub, got, tc.reaches)
		}
	}
	// The group's column says whose inbox it is: its members that are crew
	// members, so not jo.
	var cols []string
	for _, c := range m.Subscribers {
		cols = append(cols, c.String())
		if c.Name == "approvers" && !slices.Equal(c.Reaches, []string{"clerk"}) {
			t.Errorf("approvers reaches %v, want clerk", c.Reaches)
		}
	}
	if got := strings.Join(cols, ","); got != "clerk,yardmaster,jo (user),approvers (group)" {
		t.Errorf("columns %q", got)
	}
	// The text is the same data: the shared row with its count and a mark
	// in every column, in the order of the header.
	lines := map[string][]string{}
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Split(l, "  "); len(f) > 1 {
			f = slices.DeleteFunc(f, func(s string) bool { return strings.TrimSpace(s) == "" })
			for i := range f {
				f[i] = strings.TrimSpace(f[i])
			}
			lines[f[0]] = f[1:]
		}
	}
	for sub, want := range map[string]string{
		"SUBSCRIPTION":             "REACHES|clerk|yardmaster|jo (user)|approvers (group)",
		"yardr all state=done":     "3|via approvers|sub|sub|sub",
		"linear involved":          "1|-|sub|-|-",
		"yardr all kind=held":      "2|on|on+sub|-|-",
		"yardr all kind=gave_up":   "1|-|on|-|-",
		"yardr all stage=approved": "1|via approvers|-|-|sub",
	} {
		if got := strings.Join(lines[sub], "|"); got != want {
			t.Errorf("%s: %q, want %q in:\n%s", sub, got, want, text)
		}
	}
	if !strings.Contains(text, "approvers (group) reaches: clerk\n") {
		t.Errorf("no line for the group's crew members:\n%s", text)
	}
}

// The default --on kinds nobody has are listed as reaching no one: --on
// replaces the list, so a crew member given two kinds lost the rest. A kind
// someone has as a subscription instead is not among them (yardr-a3s2).
func TestSubscribeMatrixReachesNoOne(t *testing.T) {
	text, m := matrixYard(t)
	var want []string
	for _, k := range defaultOn {
		if !slices.Contains([]string{"gave_up", "held", "move_refused"}, k) {
			want = append(want, k)
		}
	}
	if !slices.Equal(m.ReachesNoOne, want) {
		t.Errorf("reaches no one = %v, want %v", m.ReachesNoOne, want)
	}
	if !strings.Contains(text, "): "+strings.Join(want, ", ")+"\n") || !strings.Contains(text, "reaches no one (") {
		t.Errorf("text does not list them:\n%s", text)
	}
}

// A subscription typed without its subscriber is refused, not answered with
// the view as if it had been made.
func TestSubscribeBareTakesNoFlags(t *testing.T) {
	st := userYard(t)
	for _, args := range [][]string{{"--done"}, {"--source", "yardr"}, {"--where", "kind=held"}, {"--scope", "all"}} {
		refusedAs(t, st, "jo", "name who subscribes", append([]string{"subscribe"}, args...)...)
	}
}
