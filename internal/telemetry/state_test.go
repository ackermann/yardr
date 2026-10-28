package telemetry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMintID(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first, err := MintID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := MintID()
	if err != nil {
		t.Fatal(err)
	}
	if !shape.MatchString(first) {
		t.Errorf("minted id = %q", first)
	}
	if first == second {
		t.Error("two minted ids are equal")
	}
}

func TestDeriveUUID(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a := DeriveUUID("id", "yardr_daily", "2026-10-28")
	b := DeriveUUID("id", "yardr_daily", "2026-10-28")
	if a != b || !shape.MatchString(a) {
		t.Errorf("derived uuid = %q, %q", a, b)
	}
	for _, other := range []string{
		DeriveUUID("other", "yardr_daily", "2026-10-28"),
		DeriveUUID("id", "yardr_daily", "2026-10-29"),
		DeriveUUID("id", "yardr_daily", "2026-10-28", "claude"),
	} {
		if other == a {
			t.Errorf("derived uuid does not vary: %q", other)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "telemetry.json")
	if state, err := LoadState(path); err != nil || state != nil {
		t.Fatalf("missing state = %+v, %v", state, err)
	}
	want := &State{
		InstallID: "id", Minted: "2026-10-26",
		Grants: []string{"2026-10-26T09:30:00Z"}, Withdrawals: []string{"2026-10-27T09:30:00Z"},
		EligibleFrom: "2026-10-27", LastAttemptDay: "2026-10-28", LastDaySent: "2026-10-27",
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, %v", fi.Mode(), err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallID != want.InstallID || len(got.Grants) != 1 || len(got.Withdrawals) != 1 ||
		got.EligibleFrom != want.EligibleFrom || got.LastAttemptDay != want.LastAttemptDay ||
		got.LastDaySent != want.LastDaySent || got.Minted != want.Minted {
		t.Errorf("round trip = %+v", got)
	}
	stale := path + ".tmp"
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(stale)
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("replaced state file mode = %v, %v", fi.Mode(), err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Error("a corrupt state file loads")
	} else if got := err.Error(); !strings.Contains(got, path) {
		t.Errorf("corrupt state error = %q", got)
	}
}
