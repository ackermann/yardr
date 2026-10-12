package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// settings get printed what is stored, so an unset timer_limit read "-" and
// not the 5 the yard goes by, and YARDR_HOOK_ADDR, which is used instead
// of the setting, was not shown. It prints the value in effect and where
// that is from (yardr-3wt9).
func TestSettingsGetPrintsTheValueInEffect(t *testing.T) {
	t.Setenv("YARDR_HOOK_ADDR", "")
	st := userYard(t)
	row := func(out, key string) string {
		t.Helper()
		for _, l := range strings.Split(out, "\n") {
			if f := strings.Fields(l); len(f) > 2 && f[0] == key {
				return strings.Join(f[:3], " ")
			}
		}
		t.Fatalf("settings get has no %s:\n%s", key, out)
		return ""
	}
	out := mustAs(t, st, "jo", "settings", "get")
	for _, want := range []string{"operator jo set", "default_kind claude set", "timer_limit 5 default",
		"timer_min_every 1h default", "yardmaster yardmaster set", "hook_addr - -", "operator_note - -", "name y default"} {
		if key, _, _ := strings.Cut(want, " "); row(out, key) != want {
			t.Errorf("settings get: %s, want %s", row(out, key), want)
		}
	}
	if !strings.HasPrefix(out, "KEY") || !strings.Contains(strings.SplitN(out, "\n", 2)[0], "FROM") {
		t.Errorf("settings get has no FROM column:\n%s", out)
	}

	mustAs(t, st, "jo", "settings", "set", "hook_addr", "127.0.0.1:9000")
	if got := row(mustAs(t, st, "jo", "settings", "get"), "hook_addr"); got != "hook_addr 127.0.0.1:9000 set" {
		t.Errorf("set: %s", got)
	}
	t.Setenv("YARDR_HOOK_ADDR", "127.0.0.1:9001")
	out = mustAs(t, st, "jo", "settings", "get")
	if got := row(out, "hook_addr"); got != "hook_addr 127.0.0.1:9001 environment" {
		t.Errorf("under YARDR_HOOK_ADDR: %s", got)
	}

	// One key prints its value alone, as scripts read it; --json says where
	// it is from. A setting with no value is still an error.
	if out := mustAs(t, st, "jo", "settings", "get", "hook_addr"); out != "127.0.0.1:9001\n" {
		t.Errorf("settings get hook_addr = %q", out)
	}
	if out := mustAs(t, st, "jo", "settings", "get", "timer_limit"); out != "5\n" {
		t.Errorf("settings get timer_limit = %q", out)
	}
	var one settingRow
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "settings", "get", "hook_addr", "--json")), &one); err != nil ||
		one != (settingRow{"hook_addr", "127.0.0.1:9001", store.FromEnvironment}) {
		t.Errorf("settings get hook_addr --json = %+v, %v", one, err)
	}
	var all []settingRow
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "settings", "get", "--json")), &all); err != nil || len(all) != len(store.KnownSettings) {
		t.Errorf("settings get --json = %+v, %v", all, err)
	}
	refusedAs(t, st, "jo", "operator_note is not set: not found", "settings", "get", "operator_note")
	refusedAs(t, st, "jo", `no setting "hook_adr"; there is: blob_max_bytes, default_kind, default_pack, hook_addr,`, "settings", "get", "hook_adr")
	refusedAs(t, st, "jo", `no setting "hook_adr"; there is: blob_max_bytes, default_kind, default_pack, hook_addr,`, "settings", "set", "hook_adr", "3")
}

// A yard set up before the store refused them may hold a config key or a
// setting nothing reads. It opens and runs as it did, and doctor notes
// each with how it is removed: notes, no faults (yardr-3wt9).
func TestDoctorNotesWhatNothingReads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	userYardOn(t, old)
	mustAs(t, old, "jo", "group", "add", "agents", "--runner", "herdr", "--set", "kind=claude")
	mustAs(t, old, "jo", "crew", "add", "clerk", "--set", "kind=claude")
	clean, _ := doctorJSON(t, old)
	if len(clean.State.Config) != 0 {
		t.Fatalf("a yard the store checked has config notes: %v", clean.State.Config)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.OldConfig(t, path, "groups", "agents", "idel", "5m")
	storetest.OldConfig(t, path, "groups", "agents", "cleanup", "no")
	storetest.OldConfig(t, path, "crew", "clerk", "max-age", "6h")
	storetest.OldConfig(t, path, "settings", "", "hook_adr", "4")
	st := storetest.Open(t, path)

	rep, _ := doctorJSON(t, st)
	// The commands as they are typed for this home.
	y := yardrAt(os.Getenv("YARDR_HOME"))
	want := []string{
		`group agents: herdr runner needs config.cleanup as true or false, got the JSON string "no". Change it: yardr group set agents <key>=<value>, or --unset <key>`,
		`group agents: unknown config key "idel": nothing reads it. A group of the herdr runner takes: args, cleanup, env_file, grace, idle, keep, kind, label, max_age, model, prompt, provider, stall, trust, worktrees. Change it: yardr group set agents <key>=<value>, or --unset <key>`,
		`crew member clerk: unknown config key "max-age": nothing reads it. A crew member of the herdr runner takes: args, env_command, env_file, greet, kind, model, open_threads, peers, prompt, provider, set_state, trust. Change it: yardr crew set clerk <key>=<value>, or --unset <key>`,
		`setting hook_adr is stored and nothing reads it (there is: blob_max_bytes, default_kind, default_pack, hook_addr, max_sessions, name, operator, operator_note, operator_style, peer_file_max_bytes, timer_limit, timer_min_every, yardmaster). Remove it: yardr settings set hook_adr ""`,
	}
	for i := range want {
		want[i] = strings.Replace(want[i], ": yardr ", ": "+y+" ", 1)
	}
	if strings.Join(rep.State.Config, "\n") != strings.Join(want, "\n") {
		t.Fatalf("doctor's config notes:\n%s\nwant:\n%s", strings.Join(rep.State.Config, "\n"), strings.Join(want, "\n"))
	}
	if len(rep.State.Failing) != len(clean.State.Failing) {
		t.Errorf("a key nothing reads made a crew member fail: %+v", rep.State.Failing)
	}
	out, _ := as(t, st, "jo", "doctor")
	for _, w := range want {
		if !strings.Contains(out, "note   config") || !strings.Contains(out, w) {
			t.Fatalf("doctor has no note %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "FAULT  config") {
		t.Fatalf("doctor makes a fault of a key nothing reads:\n%s", out)
	}
	// settings get lists the setting nothing reads, and says how it goes.
	if out := mustAs(t, st, "jo", "settings", "get"); !strings.Contains(out, `nothing reads it. Remove it: yardr settings set hook_adr ""`) {
		t.Fatalf("settings get does not list the setting nothing reads:\n%s", out)
	}
	// What the notes say removes them does.
	mustAs(t, st, "jo", "group", "set", "agents", "--unset", "idel", "--unset", "cleanup")
	mustAs(t, st, "jo", "crew", "set", "clerk", "--unset", "max-age")
	mustAs(t, st, "jo", "settings", "set", "hook_adr", "")
	if rep, _ := doctorJSON(t, st); len(rep.State.Config) != 0 {
		t.Fatalf("after the removals: %v", rep.State.Config)
	}
}

// The group form keeps the config keys it does not edit (Other) and stores
// the group itself, with no check of the CLI's before it: the store's is
// the one it meets.
func TestGroupFormMeetsTheStoresCheck(t *testing.T) {
	st := userYard(t)
	g := groupAnswers{Name: "agents", Runner: "herdr", Kind: "claude", Limit: "1", Other: map[string]any{"idel": "5m"}}.group()
	if err := st.PutGroup(context.Background(), g, "jo"); err == nil || !strings.Contains(err.Error(), `group agents: unknown config key "idel"`) {
		t.Fatalf("the form's group with a key nothing reads: %v", err)
	}
}
