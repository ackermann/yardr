package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/telemetry"
)

// telemetryYard is an app on a throwaway store in a home of its own,
// where the telemetry state file lands.
func telemetryYard(t *testing.T) *app {
	t.Helper()
	a := testApp(t, storetest.New(t), newHomeDir(t))
	a.actor = "me"
	return a
}

// installIDOf reads the install id an on, off or show printed.
func installIDOf(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.Lines(strings.TrimSpace(out)) {
		if id, ok := strings.CutPrefix(line, "install id: "); ok {
			return strings.TrimSpace(id)
		}
	}
	t.Fatalf("no install id in:\n%s", out)
	return ""
}

// yardr telemetry on mints the install id and switches the setting;
// show prints the id beside the current request, a daily alone on a
// fresh yard; off stops it, keeping the id.
func TestTelemetryOnOffShow(t *testing.T) {
	t.Parallel()
	a := telemetryYard(t)

	on, err := run(t, a.telemetryCmd(), "on")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(on, "telemetry: on (since ") || !strings.Contains(on, telemetry.Endpoint) {
		t.Errorf("on printed:\n%s", on)
	}
	if !strings.Contains(on, telemetry.IDSentence) {
		t.Errorf("on carries no pseudonymous sentence:\n%s", on)
	}
	id := installIDOf(t, on)

	show, err := run(t, a.telemetryCmd(), "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"install id: " + id, telemetry.IDSentence,
		"telemetry: on (since ", "next request: sending day ", "(UTC); no eligible source days",
		`"api_key": "` + telemetry.APIKey + `"`,
		`"event": "yardr_daily"`, `"telemetry_age_days": 0`,
		"To have this yard's data deleted, send the project this install id.",
	} {
		if !strings.Contains(show, want) {
			t.Errorf("show has no %q:\n%s", want, show)
		}
	}

	off, err := run(t, a.telemetryCmd(), "off")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(off, "telemetry: off — nothing is sent") || !strings.Contains(off, "install id: "+id) {
		t.Errorf("off printed:\n%s", off)
	}
	dark, err := run(t, a.telemetryCmd(), "show")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"telemetry: off — nothing is sent",
		"no batch available until telemetry is enabled",
		"install id: " + id,
	} {
		if !strings.Contains(dark, want) {
			t.Errorf("show while off has no %q:\n%s", want, dark)
		}
	}
	if strings.Contains(dark, "yardr_daily") {
		t.Errorf("show while off previews a batch:\n%s", dark)
	}
}

// --json prints the same change and preview the text renders.
func TestTelemetryJSON(t *testing.T) {
	t.Parallel()
	a := telemetryYard(t)
	a.json = true

	var change telemetry.Change
	if out, err := run(t, a.telemetryCmd(), "on"); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal([]byte(out), &change); err != nil || change.Switch != "on" || change.InstallID == "" {
		t.Fatalf("on --json = %s, %v", out, err)
	}
	var preview telemetry.Preview
	if out, err := run(t, a.telemetryCmd(), "show"); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatalf("show --json = %s, %v", out, err)
	}
	if preview.InstallID != change.InstallID || preview.Request == nil || preview.Request.APIKey != telemetry.APIKey {
		t.Fatalf("show --json = %+v", preview)
	}
	if len(preview.Request.Batch) != 1 || preview.Request.Batch[0].Name != telemetry.DailyEvent {
		t.Fatalf("show --json batch = %+v", preview.Request.Batch)
	}
}

// The generic settings set refuses telemetry with the hint, while
// settings get reads it as every setting.
func TestSettingsSetTelemetryRefused(t *testing.T) {
	t.Parallel()
	a := telemetryYard(t)

	for _, value := range []string{"true", "false", ""} {
		if out, err := run(t, a.settingsCmd(), "set", "telemetry", value); err == nil ||
			!strings.Contains(err.Error(), "yardr telemetry on and yardr telemetry off") {
			t.Fatalf("settings set telemetry %q = %v, want the hint\n%s", value, err, out)
		}
	}
	if out, err := run(t, a.settingsCmd(), "get", "telemetry"); err != nil || strings.TrimSpace(out) != "false" {
		t.Fatalf("settings get telemetry = %q, %v", out, err)
	}
	if _, err := run(t, a.telemetryCmd(), "on"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.settingsCmd(), "get", "telemetry"); err != nil || strings.TrimSpace(out) != "true" {
		t.Fatalf("settings get telemetry = %q, %v", out, err)
	}
}

// The on and off help describe the four questions and the id-only
// PostHog profile used for deletion.
func TestTelemetryHelpStatesTheQuestions(t *testing.T) {
	t.Parallel()
	a := telemetryYard(t)
	for _, sub := range a.telemetryCmd().Commands() {
		if sub.Name() != "on" && sub.Name() != "off" {
			continue
		}
		if !strings.Contains(sub.Long, "four questions") {
			t.Errorf("%s help names no four questions", sub.Name())
		}
		if !strings.Contains(sub.Long, "id-only person profile") {
			t.Errorf("%s help names no id-only profile", sub.Name())
		}
	}
}
