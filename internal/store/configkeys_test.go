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
	t.Parallel()
	s, ctx := storetest.New(t), context.Background()
	put := func(runner string, cfg map[string]any) error {
		return s.PutGroup(ctx, store.Group{Name: "g", Runner: runner, Config: cfg}, "me")
	}
	for want, err := range map[string]error{
		`group g: unknown config key "idel": nothing reads it. A group of the herdr runner takes: args, cleanup, env_file, grace, idle, keep, kind, label, login_for, max_age, model, nice, prompt, provider, stall, still, trust, worktrees`: put("herdr", map[string]any{"kind": "claude", "idel": "5m"}),
		`group g: unknown config key "max-age": nothing reads it. A group of the exec runner takes: cleanup, cmd, dir, env_file, grace, idle, keep, log, max_age, nice, stall, still, timeout, worktrees`:                                     put("exec", map[string]any{"cmd": "true", "max-age": "6h"}),
		// A key of another runner, or of a crew member, is read by nothing here.
		`group g: unknown config key "kind": nothing reads it. A group of the manual runner takes: cleanup, fold, fold_after, fold_max, grace, idle, keep, max_age, notify, stall, still, wait`: put("manual", map[string]any{"kind": "claude"}),
		`group g: unknown config key "greet"`:       put("herdr", map[string]any{"greet": "jo"}),
		`group g: unknown config key "env_command"`: put("herdr", map[string]any{"env_command": "op inject"}),
		// A value its reader would take as not set.
		`group g: herdr runner needs config.idle as a duration such as 30m, a number of seconds, or off, got the JSON string "soon"`:  put("herdr", map[string]any{"idle": "soon"}),
		`group g: herdr runner needs config.cleanup as true or false, got the JSON string "no"`:                                       put("herdr", map[string]any{"cleanup": "no"}),
		`group g: manual runner needs config.notify as true or false (or the word true, 1, yes, false, 0, no), got the JSON number 2`: put("manual", map[string]any{"notify": 2}),
		`group g: manual runner needs config.wait as a duration such as 40m, 12h or 7d, got the JSON string "off"`:                    put("manual", map[string]any{"wait": "off"}),
		`group g: manual runner needs config.wait as a duration such as 40m, 12h or 7d, got the JSON number 3600`:                     put("manual", map[string]any{"wait": 3600}),
		`group g: exec runner needs config.timeout as a duration such as 40m, 12h or 7d, got the JSON string "soon"`:                  put("exec", map[string]any{"cmd": "true", "timeout": "soon"}),
		`group g: exec runner needs config.timeout as a duration such as 40m, 12h or 7d, got the JSON number 3600`:                    put("exec", map[string]any{"cmd": "true", "timeout": 3600}),
		// A manual group's alone: nothing waits for a person in another.
		`group g: unknown config key "wait"`: put("herdr", map[string]any{"kind": "claude", "wait": "1h"}),
		// An exec group's alone: nothing else runs a script to bound.
		`group g: unknown config key "timeout"`: put("herdr", map[string]any{"kind": "claude", "timeout": "1h"}),
		// So are the keys that make it a siding: no other group has beads
		// wait in a stage to be folded.
		`group g: unknown config key "fold"`:       put("herdr", map[string]any{"kind": "claude", "fold": 4}),
		`group g: unknown config key "fold_after"`: put("exec", map[string]any{"cmd": "true", "fold_after": "8h"}),
		`group g: unknown config key "fold_max"`:   put("exec", map[string]any{"cmd": "true", "fold_max": 6}),
		// A siding folds two beads or more, and weighs two points or more.
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON number 1`:                 put("manual", map[string]any{"fold": 1}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON number 0`:                 put("manual", map[string]any{"fold": 0}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON number -4`:                put("manual", map[string]any{"fold": -4}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON number 2.5`:               put("manual", map[string]any{"fold": 2.5}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON string "four"`:            put("manual", map[string]any{"fold": "four"}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON boolean true`:             put("manual", map[string]any{"fold": true}),
		`group g: manual runner needs config.fold as a whole number of 2 or more, got the JSON number 1e+30`:             put("manual", map[string]any{"fold": 1e30}),
		`group g: manual runner needs config.fold_max as a whole number of 2 or more, got the JSON number 1`:             put("manual", map[string]any{"fold_max": 1}),
		`group g: manual runner needs config.fold_max as a whole number of 2 or more, got the JSON string "6"`:           put("manual", map[string]any{"fold_max": "6"}),
		`group g: manual runner needs config.fold_after as a duration such as 40m, 12h or 7d, got the JSON string "off"`: put("manual", map[string]any{"fold_after": "off"}),
		`group g: manual runner needs config.fold_after as a duration such as 40m, 12h or 7d, got the JSON number 3600`:  put("manual", map[string]any{"fold_after": 3600}),
		`group g: exec runner needs config.cmd as a string, got the JSON boolean true; write --set 'cmd="true"'`:         put("exec", map[string]any{"cmd": true}),
		"group g: exec runner needs config.cmd": put("exec", nil),
		// A niceness is 0 to 19: a session cannot ask for more of the CPU
		// than the process that starts it has.
		`group g: herdr runner needs config.nice as a whole number from 0 to 19, got the JSON number 20`:    put("herdr", map[string]any{"kind": "claude", "nice": 20}),
		`group g: exec runner needs config.nice as a whole number from 0 to 19, got the JSON number -1`:     put("exec", map[string]any{"cmd": "true", "nice": -1}),
		`group g: exec runner needs config.nice as a whole number from 0 to 19, got the JSON number 2.5`:    put("exec", map[string]any{"cmd": "true", "nice": 2.5}),
		`group g: herdr runner needs config.nice as a whole number from 0 to 19, got the JSON string "10"`:  put("herdr", map[string]any{"kind": "claude", "nice": "10"}),
		`group g: herdr runner needs config.nice as a whole number from 0 to 19, got the JSON boolean true`: put("herdr", map[string]any{"kind": "claude", "nice": true}),
		// A session of a manual group runs nothing to be nice, a peer's runs at the other yard.
		`group g: unknown config key "nice"`: put("manual", map[string]any{"nice": 10}),
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
			"stall": "2m", "keep": true, "cleanup": false, "trust": false, "label": "builders", "worktrees": "/srv/wt", "nice": 19},
		"manual": {"notify": "yes", "max_age": 90.5, "wait": "1d", "fold": 4, "fold_after": "1d", "fold_max": 6},
		"exec":   {"cmd": "make", "dir": "/srv", "log": "/srv/log", "idle": "10m", "env_file": "/srv/deploy.env", "timeout": "2h", "nice": 0},
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
		`crew member clerk: unknown config key "max_age": nothing reads it. A crew member of the herdr runner takes: args, env_command, env_file, for, greet, kind, model, open_threads, peers, prompt, provider, set_state, trust`: crew(map[string]any{"max_age": "6h"}),
		`crew member clerk: herdr runner needs config.env_command as a string or a list, got the JSON boolean true; write --set 'env_command="true"'`:                                                                               crew(map[string]any{"env_command": true}),
		`crew member clerk: herdr runner needs config.set_state as names separated by commas, or a list, got the JSON boolean true`:                                                                                                 crew(map[string]any{"set_state": true}),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PutCrew = %v, want %s", err, want)
		}
	}
	if _, err := s.GetCrew(ctx, "clerk"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused crew member was stored: %v", err)
	}
	execCrew := store.CrewMember{Name: "script", Runner: "exec", Config: map[string]any{"env_file": "/srv/deploy.env"}}
	if _, err := s.PutCrew(ctx, execCrew, "me"); err == nil ||
		!strings.Contains(err.Error(), `unknown config key "env_file"`) {
		t.Errorf("PutCrew with exec env_file = %v, want env_file refused", err)
	}
	if _, err := s.GetCrew(ctx, "script"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an exec crew member with env_file was stored: %v", err)
	}
	if err := crew(map[string]any{"kind": "claude", "env_command": []string{"op", "inject"}, "greet": "jo",
		"open_threads": "linear,github", "peers": []string{"north"}}); err != nil {
		t.Errorf("PutCrew = %v", err)
	}
}

// The niceness a group's sessions run at is its nice setting, as a Go caller
// builds it and as JSON stores it. A value the store refuses to write (see
// above) reads as none, so a group stored before the key was checked starts
// its sessions as it did.
func TestGroupNice(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		cfg  map[string]any
		want int
	}{
		{nil, 0},
		{map[string]any{"cmd": "true"}, 0},
		{map[string]any{"nice": 10}, 10},
		{map[string]any{"nice": 10.0}, 10},
		{map[string]any{"nice": 0}, 0},
		{map[string]any{"nice": 19}, 19},
		{map[string]any{"nice": 20}, 0},
		{map[string]any{"nice": -3}, 0},
		{map[string]any{"nice": 2.5}, 0},
		{map[string]any{"nice": "10"}, 0},
		{map[string]any{"nice": true}, 0},
	} {
		if got := (store.Group{Config: c.cfg}).Nice(); got != c.want {
			t.Errorf("Group{Config: %v}.Nice() = %d, want %d", c.cfg, got, c.want)
		}
	}
}

// A herdr group may say which subscription it burns: provider is a shared
// herdr key, named as a source's limits kinds name it (yardr-368s). Any
// other runner refuses it with the keys it takes.
func TestStoreTakesProviderOnHerdr(t *testing.T) {
	t.Parallel()
	s, ctx := storetest.New(t), context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "herdr", Config: map[string]any{"kind": "pi", "provider": "zai"}}, "me"); err != nil {
		t.Fatalf("PutGroup with provider = %v", err)
	}
	ch, err := s.SetGroup(ctx, "g", store.GroupEdit{Set: map[string]any{"provider": "openai"}}, "me")
	if err != nil || ch.Is.Config["provider"] != "openai" || ch.Is.Config["kind"] != "pi" {
		t.Fatalf("SetGroup provider = %+v, %v", ch, err)
	}
	if ch, err = s.SetGroup(ctx, "g", store.GroupEdit{Unset: []string{"provider"}}, "me"); err != nil {
		t.Fatalf("unsetting provider: %v", err)
	} else if _, kept := ch.Is.Config["provider"]; kept {
		t.Fatalf("provider unset and kept: %+v", ch.Is.Config)
	}
	// A crew member's too: the key is shared, as kind and model are.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{"kind": "pi", "provider": "zai"}}, "me"); err != nil {
		t.Fatalf("PutCrew with provider = %v", err)
	}
	put := func(runner string, cfg map[string]any) error {
		return s.PutGroup(ctx, store.Group{Name: "o", Runner: runner, Config: cfg}, "me")
	}
	for want, err := range map[string]error{
		`group o: unknown config key "provider": nothing reads it. A group of the exec runner takes: cleanup, cmd, dir, env_file, grace, idle, keep, log, max_age, nice, stall, still, timeout, worktrees`: put("exec", map[string]any{"cmd": "true", "provider": "zai"}),
		`group o: unknown config key "provider": nothing reads it. A group of the manual runner takes: cleanup, fold, fold_after, fold_max, grace, idle, keep, max_age, notify, stall, still, wait`:        put("manual", map[string]any{"provider": "zai"}),
		`group o: unknown config key "provider": nothing reads it. A group of the peer runner takes: cleanup, grace, idle, keep, max_age, peer, remote, stall, still, tries`:                               put("peer", map[string]any{"peer": "north", "provider": "zai"}),
		`group o: herdr runner needs config.provider as a string, got the JSON boolean true; write --set 'provider="true"'`:                                                                                put("herdr", map[string]any{"provider": true}),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PutGroup = %v, want %s", err, want)
		}
	}
}

// A yard set up before the store checked config keys holds what it would
// now refuse. It still opens, and a write that leaves such a key as it is
// stored is taken: the group can be changed one key at a time. Adding the
// key anew, or changing it, is refused.
func TestStoreKeepsAConfigKeyItWouldRefuse(t *testing.T) {
	t.Parallel()
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
	storetest.OldConfig(t, path, "settings", "", "hook_adr", "4")
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
	if err != nil || all["hook_adr"] != "4" {
		t.Fatalf("settings = %v, %v", all, err)
	}
	if err := s.SetSetting(ctx, "hook_adr", "5", "me"); err == nil {
		t.Fatal("a stored setting nothing reads was set again")
	}
	if err := s.SetSetting(ctx, "hook_adr", "", "me"); err != nil {
		t.Fatalf("removing it: %v", err)
	}
	if err := s.SetSetting(ctx, "hook_adr", "", "me"); err == nil {
		t.Fatal("removing a setting that is neither known nor stored said nothing")
	}
}

// SetSetting knows which settings there are and what each takes: it was
// the CLI's table, so init, the pairing form and any other caller of the
// store wrote what they liked.
func TestStoreChecksSettings(t *testing.T) {
	// Sequential: process environment.
	s, ctx := storetest.New(t), context.Background()
	for want, kv := range map[string][2]string{
		`no setting "hook_adr"; there is: backup_dir, backup_every, backup_keep, bdp_addr, blob_max_bytes, default_kind, default_pack, disk_keep_gb, edge, files_max, hearer, herdr_session, hook_addr, hook_public_url, hosted, max_sessions, name, operator, operator_note, operator_style, peer_file_max_bytes, permissions_preset, preview_idle, provider_terms, quiet_landings, router_addr, telemetry, timer_limit, timer_min_every, upgrade_from, upgrade_restore, web_addr, web_as, web_jwt_audience, web_jwt_header, web_jwt_issuer, web_jwt_jwks, web_jwt_users, web_pages_addr, web_pages_host, yardmaster`: {"hook_adr", "4"},
		`default_kind "nope": herdr starts`:                                                                                 {"default_kind", "nope"},
		`operator_style "loud": there is guide, show, work, report`:                                                         {"operator_style", "loud"},
		`hook address "8790": want host:port`:                                                                               {"hook_addr", "8790"},
		`router_addr "router": the router's address as an IP address`:                                                       {"router_addr", "router"},
		`router_addr "172.18.0.2:8790": the router's address as an IP address`:                                              {"router_addr", "172.18.0.2:8790"},
		`hosted "yes": true or false`:                                                                                       {"hosted", "yes"},
		`hosted "1": true or false`:                                                                                         {"hosted", "1"},
		`bdp_addr: --addr 8765: address 8765: missing port in address`:                                                      {"bdp_addr", "8765"},
		`bdp_addr: --addr 0.0.0.0:8765 refused: 0.0.0.0 is not a loopback address, and yardr bdp has no authentication yet`: {"bdp_addr", "0.0.0.0:8765"},
		`bdp_addr: --addr :8765 refused: an address with no host listens on every interface`:                                {"bdp_addr", ":8765"},
		`web_addr: --addr 0.0.0.0:8791 refused: 0.0.0.0 is not a loopback address, and yardr web has no authentication yet`: {"web_addr", "0.0.0.0:8791"},
		`web_addr: --addr :8791 refused: an address with no host listens on every interface`:                                {"web_addr", ":8791"},
		`web_addr: --addr 8791: address 8791: missing port in address`:                                                      {"web_addr", "8791"},
		`web_as: user "a b": use letters, digits, '.', '_', '-' or '/'`:                                                     {"web_as", "a b"},
		`web_jwt_issuer: an issuer "team": a URL`:                                                                           {"web_jwt_issuer", "team"},
		`web_jwt_issuer: an issuer "ftp://team/x": a URL`:                                                                   {"web_jwt_issuer", "ftp://team/x"},
		`web_jwt_audience "two words": an audience is one word`:                                                             {"web_jwt_audience", "two words"},
		`web_jwt_jwks: the keys' URL "team/certs": a URL`:                                                                   {"web_jwt_jwks", "team/certs"},
		`web_jwt_jwks "http://keys.example.net/x": http is for a test issuer on loopback`:                                   {"web_jwt_jwks", "http://keys.example.net/x"},
		`web_jwt_jwks "http://keys.example.com/x": http is for a test issuer on loopback or the router`:                     {"web_jwt_jwks", "http://keys.example.com/x"},
		`web_jwt_jwks "http://gateway/jwks.json": http is for a test issuer on loopback or the router`:                      {"web_jwt_jwks", "http://gateway/jwks.json"},
		`web_jwt_jwks "http://router.example.com/x": http is for a test issuer on loopback or the router`:                   {"web_jwt_jwks", "http://router.example.com/x"},
		`web_jwt_header "a b": a header's name`:                                                                             {"web_jwt_header", "a b"},
		`web_jwt_users: "mckean": email=user pairs separated by commas`:                                                     {"web_jwt_users", "mckean"},
		`web_jwt_users: "mckean@example.net": email=user pairs separated by commas`:                                         {"web_jwt_users", "mckean@example.net"},
		`web_jwt_users: email "no-at-sign": an address with an @`:                                                           {"web_jwt_users", "no-at-sign=jo"},
		`web_jwt_users: user "a b": use letters, digits, '.', '_', '-' or '/'`:                                              {"web_jwt_users", "a@b.example=a b"},
		`web_jwt_users: email "A@B.EXAMPLE" twice`:                                                                          {"web_jwt_users", "a@b.example=jo,A@B.EXAMPLE=ann"},
		`timer_limit "0": a whole number, 1 or more`:                                                                        {"timer_limit", "0"},
		`max_sessions "-1": a whole number, 0 or more`:                                                                      {"max_sessions", "-1"},
		`max_sessions "many": a whole number, 0 or more`:                                                                    {"max_sessions", "many"},
		`files_max is answered by the yard and not set`:                                                                     {"files_max", "32"},
		`default_pack "packs/x": a directory is given as an absolute path`:                                                  {"default_pack", "packs/x"},
		`permissions_preset "frobnicate": no harness has a permission preset`:                                               {"permissions_preset", "frobnicate"},
		`yard name "a b"`: {"name", "a b"},
	} {
		if err := s.SetSetting(ctx, kv[0], kv[1], "me"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SetSetting(%s, %s) = %v, want %s", kv[0], kv[1], err, want)
		}
	}
	if all, err := s.Settings(ctx); err != nil || len(all) != 1 || all[store.FilesMaxSetting] != "64" {
		t.Fatalf("a refused setting was stored: %v, %v", all, err)
	}
	for k, v := range map[string]string{"default_kind": "codex", "permissions_preset": "bypass", "operator_style": "report", "hook_addr": "127.0.0.1:8790", "router_addr": "172.18.0.2", "hosted": "true",
		"timer_limit": "9", "timer_min_every": "30m", "default_pack": "research", "name": "north", "yardmaster": "boss", "operator_note": "reads Go",
		"web_as": "jo", "web_jwt_issuer": "https://team.cloudflareaccess.com", "web_jwt_audience": "aud-tag",
		"web_jwt_jwks": "https://team.cloudflareaccess.com/cdn-cgi/access/certs", "web_jwt_header": "Authorization",
		"web_jwt_users": "mckean@example.net=mckean, Jo@Example.NET=jo", "max_sessions": "0"} {
		if err := s.SetSetting(ctx, k, v, "me"); err != nil {
			t.Errorf("SetSetting(%s, %s) = %v", k, v, err)
		}
	}
	// The router's address takes either form of IP.
	if err := s.SetSetting(ctx, "router_addr", "fd00::2", "me"); err != nil {
		t.Errorf("SetSetting(router_addr, fd00::2) = %v", err)
	}
	// The keys' URL over http serves a test issuer on loopback.
	if err := s.SetSetting(ctx, "web_jwt_jwks", "http://127.0.0.1:9/certs", "me"); err != nil {
		t.Errorf("SetSetting(web_jwt_jwks, loopback http) = %v", err)
	}
	if err := s.SetSetting(ctx, "web_jwt_jwks", "http://localhost:9/certs", "me"); err != nil {
		t.Errorf("SetSetting(web_jwt_jwks, localhost http) = %v", err)
	}
	// The keys' URL over http serves a hosted yard's router, that host
	// alone, in any case.
	if err := s.SetSetting(ctx, "web_jwt_jwks", "http://router:8080/jwks.json", "me"); err != nil {
		t.Errorf("SetSetting(web_jwt_jwks, router http) = %v", err)
	}
	if err := s.SetSetting(ctx, "web_jwt_jwks", "http://Router:8080/jwks.json", "me"); err != nil {
		t.Errorf("SetSetting(web_jwt_jwks, Router http) = %v", err)
	}
	// Unset is unchecked: "" is no value of any setting.
	if err := s.SetSetting(ctx, "timer_limit", "", "me"); err != nil {
		t.Fatal(err)
	}

	// What a setting is read as, and where that is from.
	timers, _ := store.SettingOf("timer_limit")
	if v, from := timers.Effective(nil); v != "5" || from != store.FromDefault {
		t.Errorf("unset timer_limit = %q from %q", v, from)
	}
	if v, from := timers.Effective(map[string]string{"timer_limit": "9"}); v != "9" || from != store.FromSet {
		t.Errorf("set timer_limit = %q from %q", v, from)
	}
	// A yard is not hosted until the provisioner says so, and no variable
	// says it instead.
	hosted, _ := store.SettingOf("hosted")
	if v, from := hosted.Effective(nil); v != "false" || from != store.FromDefault {
		t.Errorf("unset hosted = %q from %q", v, from)
	}
	if v, from := hosted.Effective(map[string]string{"hosted": "true"}); v != "true" || from != store.FromSet {
		t.Errorf("set hosted = %q from %q", v, from)
	}
	t.Setenv("YARDR_HOOK_ADDR", "")
	addr, _ := store.SettingOf("hook_addr")
	if v, from := addr.Effective(map[string]string{"hook_addr": "127.0.0.1:8790"}); v != "127.0.0.1:8790" || from != store.FromSet {
		t.Errorf("set hook_addr = %q from %q", v, from)
	}
	t.Setenv("YARDR_HOOK_ADDR", "127.0.0.1:9001")
	if v, from := addr.Effective(map[string]string{"hook_addr": "127.0.0.1:8790"}); v != "127.0.0.1:9001" || from != store.FromEnvironment {
		t.Errorf("hook_addr under YARDR_HOOK_ADDR = %q from %q", v, from)
	}
	for _, tc := range []struct{ key, env, setting, value string }{
		{"web_addr", "YARDR_WEB_ADDR", "127.0.0.1:9100", "127.0.0.1:9200"},
		{"bdp_addr", "YARDR_BDP_ADDR", "127.0.0.1:9200", "127.0.0.1:9300"},
		{"web_as", "YARDR_WEB_AS", "jo", "ann"},
	} {
		t.Setenv(tc.env, "")
		setting, ok := store.SettingOf(tc.key)
		if !ok {
			t.Fatalf("no setting %s", tc.key)
		}
		if v, from := setting.Effective(map[string]string{tc.key: tc.setting}); v != tc.setting || from != store.FromSet {
			t.Errorf("set %s = %q from %q", tc.key, v, from)
		}
		t.Setenv(tc.env, tc.value)
		if v, from := setting.Effective(map[string]string{tc.key: tc.setting}); v != tc.value || from != store.FromEnvironment {
			t.Errorf("%s under %s = %q from %q", tc.key, tc.env, v, from)
		}
	}
	note, _ := store.SettingOf("operator_note")
	if v, from := note.Effective(nil); v != "" || from != "" {
		t.Errorf("unset operator_note = %q from %q", v, from)
	}
}

// MaxSessions reads the setting as the dispatcher holds it: unset caps
// nothing, "0" caps everything (paused), and a number caps at it.
func TestMaxSessions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		settings map[string]string
		max      int
		capped   bool
	}{
		{nil, 0, false},
		{map[string]string{}, 0, false},
		{map[string]string{store.MaxSessionsSetting: "0"}, 0, true},
		{map[string]string{store.MaxSessionsSetting: "1"}, 1, true},
		{map[string]string{store.MaxSessionsSetting: "4"}, 4, true},
	} {
		if max, capped := store.MaxSessions(tc.settings); max != tc.max || capped != tc.capped {
			t.Errorf("MaxSessions(%v) = (%d, %v), want (%d, %v)",
				tc.settings, max, capped, tc.max, tc.capped)
		}
	}
}

// ParseWebJWTUsers reads the users table as the web view matches it:
// emails in lower case, pairs trimmed.
func TestParseWebJWTUsers(t *testing.T) {
	t.Parallel()
	got, err := store.ParseWebJWTUsers(" McKean@Example.NET = mckean , jo@example.net=jo ")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"mckean@example.net": "mckean", "jo@example.net": "jo"}
	if len(got) != len(want) {
		t.Fatalf("ParseWebJWTUsers = %v, want %v", got, want)
	}
	for email, user := range want {
		if got[email] != user {
			t.Fatalf("ParseWebJWTUsers = %v, want %v", got, want)
		}
	}
}
