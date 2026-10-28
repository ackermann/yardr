package telemetry

import (
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/docs"
)

// The user doc's event list matches the builder property for property:
// every event the builder emits is named there with every property it
// carries, and the builder emits nothing the doc does not name. A
// property added to the builder without its row in docs/telemetry.md
// fails here, and so does one the builder no longer emits.
func TestDocMatchesTheBuilder(t *testing.T) {
	t.Parallel()
	const sharedProfile = "$process_person_profile"
	want := map[string][]string{
		DailyEvent:   {"os", "telemetry_age_days", "version"},
		HarnessEvent: {"hosted", "kind", "model", "sessions_claimed"},
		CrewEvent:    {"hosted", "kind", "model", "running_crew_sessions"},
		FailureEvent: {"count", "hosted", "kind"},
	}
	seen := map[string]map[string]bool{}
	for _, e := range Build(fixtureInput()) {
		if seen[e.Name] == nil {
			seen[e.Name] = map[string]bool{}
		}
		for p := range e.Properties {
			seen[e.Name][p] = true
		}
	}
	for name := range seen {
		if _, ok := want[name]; !ok {
			t.Errorf("the builder emits %s, which the doc does not name", name)
		}
	}
	for name, props := range want {
		var got []string
		for p := range seen[name] {
			got = append(got, p)
		}
		slices.Sort(got)
		builderWant := append([]string{sharedProfile}, props...)
		if !slices.Equal(got, builderWant) {
			t.Errorf("%s carries %q, want %q", name, got, builderWant)
		}
	}
	text, ok := docs.Read("telemetry")
	if !ok {
		t.Fatal("docs.Read(telemetry) finds no page")
	}
	if !strings.Contains(text, "Every event sets `"+sharedProfile+"`") {
		t.Errorf("the doc does not describe the property shared by every event: %s", sharedProfile)
	}
	for name, props := range want {
		got := docProperties(text, name)
		if len(got) == 0 {
			t.Errorf("the doc has no property table for `%s`", name)
			continue
		}
		slices.Sort(got)
		if !slices.Equal(got, props) {
			t.Errorf("the doc gives `%s` properties %q, builder carries %q", name, got, props)
		}
	}
}

// docProperties reads the property table immediately following an event's
// description, so a property listed under another event cannot satisfy the
// builder-to-doc check.
func docProperties(text, event string) []string {
	marker := "`" + event + "`"
	start := strings.Index(text, marker)
	if start < 0 {
		return nil
	}
	text = text[start+len(marker):]
	if next := strings.Index(text, "\n`yardr_"); next >= 0 {
		text = text[:next]
	}
	table := strings.Index(text, "| property | value |")
	if table < 0 {
		return nil
	}
	var properties []string
	for _, line := range strings.Split(text[table:], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		property := strings.TrimSpace(cells[1])
		if property == "property" || strings.Trim(property, "- ") == "" {
			continue
		}
		properties = append(properties, strings.Trim(property, "`"))
	}
	return properties
}
