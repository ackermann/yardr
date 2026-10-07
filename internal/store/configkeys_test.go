package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A config key nothing reads was stored and ignored: `max-age=6h` or
// `idel=5m` set no limit and said nothing. The store refuses one now, with
// the keys there are, whoever writes: PutGroup is what the CLI, the group
// form and a pack's import all call, and PutCrew likewise (yardr-3wt9).
func TestStoreRefusesAConfigKeyNothingReads(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	put := func(runner string, cfg map[string]any) error {
		return s.PutGroup(ctx, store.Group{Name: "g", Runner: runner, Config: cfg}, "me")
	}
	for want, err := range map[string]error{
		`group g: unknown config key "idel": nothing reads it. A group of the herdr runner takes: args, cleanup, env_file, grace, idle, keep, kind, label, max_age, model, prompt, stall, trust, worktrees`: put("herdr", map[string]any{"kind": "claude", "idel": "5m"}),
		`group g: unknown config key "max-age": nothing reads it. A group of the exec runner takes: cleanup, cmd, dir, grace, idle, keep, log, max_age, stall, worktrees`:                                   put("exec", map[string]any{"cmd": "true", "max-age": "6h"}),
		// A key of another runner, or of a crew member, is read by nothing here.
		`group g: unknown config key "kind": nothing reads it. A group of the manual runner takes: cleanup, grace, idle, keep, max_age, notify, stall`: put("manual", map[string]any{"kind": "claude"}),
		`group g: unknown config key "greet"`:       put("herdr", map[string]any{"greet": "jo"}),
		`group g: unknown config key "env_command"`: put("herdr", map[string]any{"env_command": "op inject"}),
		// A value its reader would take as not set.
		`group g: herdr runner needs config.idle as a duration such as 30m, a number of seconds, or off, got the JSON string "soon"`:  put("herdr", map[string]any{"idle": "soon"}),
		`group g: herdr runner needs config.cleanup as true or false, got the JSON string "no"`:                                       put("herdr", map[string]any{"cleanup": "no"}),
		`group g: manual runner needs config.notify as true or false (or the word true, 1, yes, false, 0, no), got the JSON number 2`: put("manual", map[string]any{"notify": 2}),
		`group g: exec runner needs config.cmd as a string, got the JSON boolean true; write --set 'cmd="true"'`:                      put("exec", map[string]any{"cmd": true}),
		"group g: exec runner needs config.cmd": put("exec", nil),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PutGroup = %v, want %s", err, want)
		}
	}
	if gs, err := s.Groups(ctx); err != nil || len(gs) != 0 {
		t.Fatalf("a refused group was stored: %+v, %v", gs, err)
	}

	// Every type a reader takes, as a Go caller builds a config and as
	// JSON stores it.
	for runner, cfg := range map[string]map[string]any{
		"herdr": {"kind": "claude", "args": []string{"--model", "opus"}, "idle": "off", "max_age": 3600, "grace": "0",
			"stall": "2m", "keep": true, "cleanup": false, "trust": false, "label": "builders", "worktrees": "/srv/wt"},
		"manual": {"notify": "yes", "max_age": 90.5},
		"exec":   {"cmd": "make", "dir": "/srv", "log": "/srv/log", "idle": "10m"},
		// A runner the table does not know is asked nothing.
		"fake": {"whatever": 1},
	} {
		if err := put(runner, cfg); err != nil {
			t.Errorf("PutGroup(%s, %v) = %v", runner, cfg, err)
		}
	}

	// SetGroup holds the group to the same.
	if err := put("herdr", map[string]any{"kind": "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGroup(ctx, "g", store.GroupEdit{Set: map[string]any{"idel": "5m"}}, "me"); err == nil || !strings.Contains(err.Error(), `unknown config key "idel"`) {
		t.Errorf("SetGroup = %v", err)
	}

	crew := func(cfg map[string]any) error {
		_, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: cfg}, "me")
		return err
	}
	for want, err := range map[string]error{
		`crew member clerk: unknown config key "max_age": nothing reads it. A crew member of the herdr runner takes: args, env_command, env_file, greet, kind, model, open_threads, peers, prompt, release, set_state, trust`: crew(map[string]any{"max_age": "6h"}),
		`crew member clerk: herdr runner needs config.env_command as a string or a list, got the JSON boolean true; write --set 'env_command="true"'`:                                                                         crew(map[string]any{"env_command": true}),
		`crew member clerk: herdr runner needs config.set_state as names separated by commas, or a list, got the JSON boolean true`:                                                                                           crew(map[string]any{"set_state": true}),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PutCrew = %v, want %s", err, want)
		}
	}
	if _, err := s.GetCrew(ctx, "clerk"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused crew member was stored: %v", err)
	}
	if err := crew(map[string]any{"kind": "claude", "env_command": []string{"op", "inject"}, "greet": "jo",
		"open_threads": "linear,github", "peers": []string{"north"}}); err != nil {
		t.Errorf("PutCrew = %v", err)
	}
}

// A yard set up before the store checked config keys holds what it would
// now refuse. It still opens, and a write that leaves such a key as it is
// stored is taken: the group can be changed one key at a time. Adding the
// key anew, or changing it, is refused.
func TestStoreKeepsAConfigKeyItWouldRefuse(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.PutGroup(ctx, store.Group{Name: "agents", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := old.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.OldConfig(t, path, "groups", "agents", "idel", "5m")
	storetest.OldConfig(t, path, "groups", "agents", "cleanup", "no")
	storetest.OldConfig(t, path, "crew", "clerk", "max-age", "6h")
	storetest.OldConfig(t, path, "settings", "", "gate_slot", "4")
	s := storetest.Open(t, path)

	g, err := s.GetGroup(ctx, "agents")
	if err != nil || g.Config["idel"] != "5m" {
		t.Fatalf("the group as stored: %+v, %v", g, err)
	}
	if ps := store.ConfigProblems(store.GroupConfig, g.Name, g.Runner, g.Config); len(ps) != 2 ||
		!strings.Contains(ps[0].Error(), "config.cleanup as true or false") || !errors.Is(ps[1], store.ErrUnknownConfigKey) {
		t.Fatalf("ConfigProblems = %v", ps)
	}
	limit := 3
	ch, err := s.SetGroup(ctx, "agents", store.GroupEdit{Set: map[string]any{"model": "opus"}, Limit: &limit}, "me")
	if err != nil || ch.Is.Config["idel"] != "5m" || ch.Is.Config["model"] != "opus" || ch.Is.Limit != 3 {
		t.Fatalf("a set of another key: %+v, %v", ch, err)
	}
	if err := s.PutGroup(ctx, ch.Is, "me"); err != nil {
		t.Fatalf("the group put back as it is: %v", err)
	}
	if _, err := s.SetGroup(ctx, "agents", store.GroupEdit{Set: map[string]any{"idel": "10m"}}, "me"); err == nil || !errors.Is(err, store.ErrUnknownConfigKey) && !strings.Contains(err.Error(), `unknown config key "idel"`) {
		t.Fatalf("a changed key nothing reads: %v", err)
	}
	// Under another runner the keys are judged anew.
	other := ch.Is
	other.Runner, other.Config = "exec", map[string]any{"cmd": "true", "idel": "5m"}
	if err := s.PutGroup(ctx, other, "me"); err == nil || !strings.Contains(err.Error(), `unknown config key "idel"`) {
		t.Fatalf("the key under another runner: %v", err)
	}
	if _, err := s.SetGroup(ctx, "agents", store.GroupEdit{Unset: []string{"idel", "cleanup"}}, "me"); err != nil {
		t.Fatalf("removing them: %v", err)
	}

	r, err := s.GetCrew(ctx, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	r.Config["model"] = "sonnet"
	if r, err = s.PutCrew(ctx, *r, "me"); err != nil || r.Config["max-age"] != "6h" {
		t.Fatalf("a crew member changed beside its old key: %+v, %v", r, err)
	}
	r.Config["max-age"] = "8h"
	if _, err := s.PutCrew(ctx, *r, "me"); err == nil || !strings.Contains(err.Error(), `unknown config key "max-age"`) {
		t.Fatalf("a changed crew key nothing reads: %v", err)
	}

	// A setting nothing reads is listed, cannot be set, and can be removed.
	all, err := s.Settings(ctx)
	if err != nil || all["gate_slot"] != "4" {
		t.Fatalf("settings = %v, %v", all, err)
	}
	if err := s.SetSetting(ctx, "gate_slot", "5", "me"); err == nil {
		t.Fatal("a stored setting nothing reads was set again")
	}
	if err := s.SetSetting(ctx, "gate_slot", "", "me"); err != nil {
		t.Fatalf("removing it: %v", err)
	}
	if err := s.SetSetting(ctx, "gate_slot", "", "me"); err == nil {
		t.Fatal("removing a setting that is neither known nor stored said nothing")
	}
}

// SetSetting knows which settings there are and what each takes: it was
// the CLI's table, so init, the pairing form and any other caller of the
// store wrote what they liked.
func TestStoreChecksSettings(t *testing.T) {
	s, ctx := storetest.New(t), context.Background()
	for want, kv := range map[string][2]string{
		`no setting "gate_slot"; there is: default_kind, default_pack, gate_slots, hook_addr, name, operator, operator_note, operator_style, timer_limit, timer_min_every, yardmaster`: {"gate_slot", "4"},
		`gate_slots "many": a whole number of gates`:                       {"gate_slots", "many"},
		`default_kind "nope": herdr starts`:                                {"default_kind", "nope"},
		`operator_style "loud": there is guide, show, work, report`:        {"operator_style", "loud"},
		`hook address "8790": want host:port`:                              {"hook_addr", "8790"},
		`timer_limit "0": a whole number, 1 or more`:                       {"timer_limit", "0"},
		`default_pack "packs/x": a directory is given as an absolute path`: {"default_pack", "packs/x"},
		`yard name "a b"`: {"name", "a b"},
	} {
		if err := s.SetSetting(ctx, kv[0], kv[1], "me"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SetSetting(%s, %s) = %v, want %s", kv[0], kv[1], err, want)
		}
	}
	if all, err := s.Settings(ctx); err != nil || len(all) != 0 {
		t.Fatalf("a refused setting was stored: %v, %v", all, err)
	}
	for k, v := range map[string]string{"gate_slots": "0", "default_kind": "codex", "operator_style": "report", "hook_addr": "127.0.0.1:8790",
		"timer_limit": "9", "timer_min_every": "30m", "default_pack": "research", "name": "north", "mayor": "boss", "operator_note": "reads Go"} {
		if err := s.SetSetting(ctx, k, v, "me"); err != nil {
			t.Errorf("SetSetting(%s, %s) = %v", k, v, err)
		}
	}
	// Unset is unchecked: "" is no value of any setting.
	if err := s.SetSetting(ctx, "gate_slots", "", "me"); err != nil {
		t.Fatal(err)
	}

	// What a setting is read as, and where that is from.
	t.Setenv("YARDR_GATE_SLOTS", "")
	t.Setenv("YARD_GATE_SLOTS", "")
	slots, _ := store.SettingOf("gate_slots")
	if v, from := slots.Effective(nil); v != "2" || from != store.FromDefault {
		t.Errorf("unset gate_slots = %q from %q", v, from)
	}
	if v, from := slots.Effective(map[string]string{"gate_slots": "4"}); v != "4" || from != store.FromSet {
		t.Errorf("set gate_slots = %q from %q", v, from)
	}
	t.Setenv("YARDR_GATE_SLOTS", "7")
	if v, from := slots.Effective(map[string]string{"gate_slots": "4"}); v != "7" || from != store.FromEnvironment {
		t.Errorf("gate_slots under YARDR_GATE_SLOTS = %q from %q", v, from)
	}
	note, _ := store.SettingOf("operator_note")
	if v, from := note.Effective(nil); v != "" || from != "" {
		t.Errorf("unset operator_note = %q from %q", v, from)
	}
}
