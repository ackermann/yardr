package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const envSecret = "lin_api_s3cret"

func envFileT(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "yardmaster.env")
	body := "# outbound Linear\nLINEAR_API_KEY=" + envSecret + "\nYARDR_ACTOR=someone-else\n"
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// envHerdr is a fake herdr logging every call to the returned file.
func envHerdr(t *testing.T, wt string) (*Herdr, string, *bytes.Buffer) {
	calls := filepath.Join(t.TempDir(), "calls")
	var logs bytes.Buffer
	h := &Herdr{YardHome: "/y", YardBin: "/bin/yardr",
		Log:    slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Socket: fakeHerdrAPI(t, calls),
		Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, calls, &logs
}

func readCalls(t *testing.T, path string) string {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// checkEnvStart asserts the env file's entry reached the socket request
// that opens the pane (paneCall: workspace.create or tab.create) and
// nothing that went through the herdr binary, where a command line shows
// in ps; that the identity was not overridden by the file; and that the
// value shows in neither the handle nor the logs.
func checkEnvStart(t *testing.T, calls, paneCall, actor string, handle map[string]any, logs string) {
	t.Helper()
	env := envOf(apiCall(t, calls, paneCall))
	if env["LINEAR_API_KEY"] != envSecret {
		t.Errorf("%s lacks the env file's entry: %v", paneCall, env)
	}
	if env["YARDR_ACTOR"] != actor {
		t.Errorf("env file overrode the identity: %v", env)
	}
	if cli := cliCalls(calls); strings.Contains(cli, "--env") || strings.Contains(cli, envSecret) || strings.Contains(cli, "LINEAR_API_KEY") {
		t.Errorf("the pane environment went on a command line:\n%s", cli)
	}
	if h := fmt.Sprint(handle); strings.Contains(h, envSecret) {
		t.Errorf("handle holds the value: %s", h)
	}
	if strings.Contains(logs, envSecret) {
		t.Errorf("logs show the value:\n%s", logs)
	}
	if !strings.Contains(logs, "LINEAR_API_KEY") {
		t.Errorf("debug log lacks the key names:\n%s", logs)
	}
}

func TestStartCrewLoadsEnvFile(t *testing.T) {
	t.Parallel()
	h, calls, logs := envHerdr(t, "")
	r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: map[string]any{"env_file": envFileT(t, 0o600)}}
	handle, err := h.StartCrew(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	checkEnvStart(t, readCalls(t, calls), "workspace.create", "yardmaster", handle, logs.String())
}

func TestHerdrStartLoadsGroupEnvFile(t *testing.T) {
	t.Parallel()
	wt := seedEmptyRepo(t)
	h, calls, logs := envHerdr(t, wt)
	h.YardHome = t.TempDir()
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Stage: "review"},
		Group:   store.Group{Name: "yard-review", Config: map[string]any{"env_file": envFileT(t, 0o600)}},
		Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	handle, err := h.Start(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	checkEnvStart(t, readCalls(t, calls), "tab.create", "yard-13-review", handle, logs.String())
}

// Kimi 2.2.0 skips its trust dialog with KIMI_CODE_TRUST_WORKSPACE=1, so
// the runner sets it only when automatic trust is allowed. Its env_file
// cannot override the harness setting or turn the bypass on when trust=false.
func TestHerdrStartSetsTrustEnv(t *testing.T) {
	t.Parallel()
	cleared := func(env map[string]string) bool {
		v, ok := env["KIMI_CODE_TRUST_WORKSPACE"]
		return ok && v == ""
	}
	start := func(t *testing.T, cfg map[string]any) map[string]string {
		t.Helper()
		wt := seedEmptyRepo(t)
		h, calls, _ := envHerdr(t, wt)
		h.YardHome = t.TempDir()
		job := dispatch.Job{
			Bead:    store.Bead{ID: "yard-13", Stage: "build"},
			Group:   store.Group{Name: "g", Config: cfg},
			Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
			Session: store.Session{ID: "s1"},
		}
		if _, err := h.Start(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		return envOf(apiCall(t, readCalls(t, calls), "tab.create"))
	}
	if env := start(t, map[string]any{"kind": "kimi"}); env["KIMI_CODE_TRUST_WORKSPACE"] != "1" {
		t.Errorf("kimi tab.create lacks the harness entry: %v", env)
	}
	over := filepath.Join(t.TempDir(), "g.env")
	if err := os.WriteFile(over, []byte("KIMI_CODE_TRUST_WORKSPACE=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := start(t, map[string]any{"kind": "kimi", "env_file": over}); env["KIMI_CODE_TRUST_WORKSPACE"] != "1" {
		t.Errorf("env_file overrode the harness entry: %v", env)
	}
	if env := start(t, map[string]any{"kind": "kimi", "trust": false}); !cleared(env) {
		t.Errorf("trust=false does not clear Kimi's dialog bypass: %v", env)
	}
	trustOver := filepath.Join(t.TempDir(), "trust.env")
	if err := os.WriteFile(trustOver, []byte("KIMI_CODE_TRUST_WORKSPACE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := start(t, map[string]any{"kind": "kimi", "trust": false, "env_file": trustOver}); !cleared(env) {
		t.Errorf("env_file bypassed trust=false: %v", env)
	}
	if env := start(t, map[string]any{"kind": "claude"}); env["KIMI_CODE_TRUST_WORKSPACE"] != "" {
		t.Errorf("claude tab.create carries kimi's entry: %v", env)
	}
}

func TestStartCrewSetsTrustEnv(t *testing.T) {
	t.Parallel()
	h, calls, _ := envHerdr(t, "")
	r := store.CrewMember{Name: "kimi-security", Dir: t.TempDir(), Config: map[string]any{"kind": "kimi"}}
	if _, err := h.StartCrew(context.Background(), r, ""); err != nil {
		t.Fatal(err)
	}
	env := envOf(apiCall(t, readCalls(t, calls), "workspace.create"))
	if env["KIMI_CODE_TRUST_WORKSPACE"] != "1" {
		t.Errorf("kimi workspace.create lacks the harness entry: %v", env)
	}
	trustOver := filepath.Join(t.TempDir(), "trust.env")
	if err := os.WriteFile(trustOver, []byte("KIMI_CODE_TRUST_WORKSPACE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, calls, _ = envHerdr(t, "")
	r.Config = map[string]any{"kind": "kimi", "trust": false, "env_file": trustOver}
	if _, err := h.StartCrew(context.Background(), r, ""); err != nil {
		t.Fatal(err)
	}
	env = envOf(apiCall(t, readCalls(t, calls), "workspace.create"))
	if v, ok := env["KIMI_CODE_TRUST_WORKSPACE"]; !ok || v != "" {
		t.Errorf("crew env_file bypassed trust=false: %v", env)
	}
}

// A file others can read, or none at all, refuses the start before herdr is
// asked for anything, as Blocked, and the error shows no value.
func TestEnvFileRefusedBlocksStart(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.env")
	for name, path := range map[string]string{"0644": envFileT(t, 0o644), "0640": envFileT(t, 0o640),
		"missing": missing, "directory": t.TempDir()} {
		h, calls, logs := envHerdr(t, "")
		cfg := map[string]any{"env_file": path}
		_, rerr := h.StartCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: cfg}, "")
		h.YardHome = t.TempDir()
		_, gerr := h.Start(context.Background(), dispatch.Job{Bead: store.Bead{ID: "yard-13", Stage: "new"},
			Group: store.Group{Name: "g", Config: cfg}, Depot: &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"}})
		for who, err := range map[string]error{"crew": rerr, "group": gerr} {
			if !dispatch.IsBlocked(err) {
				t.Errorf("%s %s: err = %v, want Blocked", name, who, err)
			} else if strings.Contains(err.Error(), envSecret) || !strings.Contains(err.Error(), "env_file") {
				t.Errorf("%s %s: error %q", name, who, err)
			}
		}
		if c := readCalls(t, calls); c != "" {
			t.Errorf("%s: herdr called despite the refusal:\n%s", name, c)
		}
		if strings.Contains(logs.String(), envSecret) {
			t.Errorf("%s: logs show the value", name)
		}
	}
}

// A crew member's herdr agent and workspace carry the yard's name, so two
// yards on one machine (herdr lists their agents together, and refuses a
// second "yardmaster") are told apart: the `name` setting, else the home's
// directory name; a yard with neither names them after the crew member alone.
func TestStartCrewNamesTheYard(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		home, setting, agent, label string
	}{
		{"/Users/jo/.yardr", "", "yardr-yardmaster", "yardr yardmaster"},
		{"/tmp/y1", "", "y1-yardmaster", "y1 yardmaster"},
		{"/tmp/y1", "Prod/Web", "prod-web-yardmaster", "Prod/Web yardmaster"},
		{"", "", "yardmaster", "yardmaster"},
	} {
		h, calls, _ := envHerdr(t, "")
		h.YardHome = tc.home
		if tc.setting != "" {
			st := storetest.New(t)
			if err := st.SetSetting(context.Background(), "name", tc.setting, "me"); err != nil {
				t.Fatal(err)
			}
			h.Store = st
			if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
case "$1 $2" in
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p1","shell_pid":`+fmt.Sprint(os.Getpid())+`}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
		}
		handle, err := h.StartCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: t.TempDir()}, "")
		if err != nil {
			t.Fatal(err)
		}
		got := readCalls(t, calls)
		ws := apiCall(t, got, "workspace.create")
		if ws["label"] != tc.label || ws["focus"] != false || !strings.Contains(got, "\nagent start "+tc.agent+" --kind claude") ||
			handle["agent"] != tc.agent {
			t.Errorf("home %q, name %q: agent %v, calls:\n%s", tc.home, tc.setting, handle["agent"], got)
		}
	}
}

// listHerdr is envHerdr whose `agent list` answers with the content of the
// returned file: one agent per line, "<name> <cwd> <pane>".
func listHerdr(t *testing.T) (h *Herdr, calls, agents string) {
	h, calls, _ = envHerdr(t, "")
	agents = filepath.Join(t.TempDir(), "agents")
	h.YardHome = "/tmp/y1"
	h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
if [ "$1 $2" = "agent list" ]; then
  printf '{"result":{"type":"agent_list","agents":['
  sep=
  while read -r name cwd pane; do
    printf '%s{"name":"%s","cwd":"%s","pane_id":"%s","agent_status":"idle"}' "$sep" "$name" "$cwd" "$pane"
    sep=,
  done < `+agents+`
  echo ']}}'
else
  echo '{"result":{"type":"ok"}}'
fi`)
	setAgents(t, agents)
	return h, calls, agents
}

func setAgents(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(append(lines, ""), "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Which of herdr's agents already is a crew member: one under the crew member's
// herdr name wherever it runs, or one under its bare name in its directory
// (herdr reports a cwd with its symlinks resolved). Not the session the
// store recorded for it, not an agent of another name or of the bare name
// somewhere else, and not an agent herdr detected but nobody named.
func TestLiveCrew(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	dir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, dir); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]any{"agent": "y1-yardmaster", "pane": "w1:p1"}
	for name, tc := range map[string]struct {
		agents []string
		handle map[string]any
		want   string // "<name> <pane>", "" for none
	}{
		"none":                         {nil, nil, ""},
		"herdr name":                   {[]string{"y1-yardmaster /elsewhere w3:p1"}, nil, "y1-yardmaster w3:p1"},
		"bare name in its dir":         {[]string{"other /x w2:p1", "yardmaster " + real + " w3:p1"}, nil, "yardmaster w3:p1"},
		"bare name elsewhere":          {[]string{"yardmaster /elsewhere w3:p1"}, nil, ""},
		"another yard's":               {[]string{"y2-yardmaster " + real + " w3:p1"}, nil, ""},
		"unnamed":                      {[]string{" " + real + " w3:p1"}, nil, ""},
		"its own recorded session":     {[]string{"y1-yardmaster " + real + " w1:p1"}, own, ""},
		"its name in another pane":     {[]string{"y1-yardmaster " + real + " w9:p1"}, own, "y1-yardmaster w9:p1"},
		"bare name beside its own":     {[]string{"y1-yardmaster " + real + " w1:p1", "yardmaster " + real + " w3:p1"}, own, "yardmaster w3:p1"},
		"bare name, a stopped handle":  {[]string{"yardmaster " + real + " w3:p1"}, own, "yardmaster w3:p1"},
		"herdr name, no handle's pane": {[]string{"y1-yardmaster " + real + " w3:p1"}, map[string]any{"agent": "y1-yardmaster"}, "y1-yardmaster w3:p1"},
	} {
		h, calls, agents := listHerdr(t)
		setAgents(t, agents, tc.agents...)
		got, err := h.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: dir, Handle: tc.handle}, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found := ""
		if got != nil {
			found = got.Name + " " + got.Pane
		}
		if found != tc.want {
			t.Errorf("%s: found %q, want %q", name, found, tc.want)
		}
		if c := readCalls(t, calls); strings.TrimSpace(c) != "agent list" {
			t.Errorf("%s: looking did more than list agents:\n%s", name, c)
		}
	}
	// A crew member with no dir runs in the yard's home.
	h, _, agents := listHerdr(t)
	h.YardHome = dir
	setAgents(t, agents, "yardmaster "+real+" w3:p1")
	if got, err := h.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster"}, ""); err != nil || got == nil {
		t.Errorf("bare name in the yard's home: %v %v", got, err)
	}
	// herdr down: an error, never "none".
	down := &Herdr{Bin: fakeHerdrBin(t, `echo '{"error":{"code":"server_not_running","message":"no herdr server"}}' >&2; exit 1`)}
	if got, err := down.LiveCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: dir}, ""); got != nil || !dispatch.IsRuntime(err) {
		t.Errorf("herdr down: %v %v, want a runtime error", got, err)
	}
}

// Found live (2026-10-03): the daemon started yardr-yardmaster beside a
// pane that had been the yardmaster since noon. Through the dispatcher,
// under either name: the crew member is refused without a workspace being
// created or an agent started, one crew_blocked names agent and pane
// however often it is tried, and the crew member starts once the agent is
// gone.
func TestCrewNotStartedBesideALiveHerdrAgent(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []string{"yardmaster " + dir + " w7:p1", "y1-yardmaster /elsewhere w7:p1"} {
		agent, _, _ := strings.Cut(foreign, " ")
		ctx := context.Background()
		h, calls, agents := listHerdr(t)
		st := storetest.New(t)
		if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: dir}, "me"); err != nil {
			t.Fatal(err)
		}
		d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"herdr": h}}
		setAgents(t, agents, foreign)
		for range 3 {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		r, _ := st.GetCrew(ctx, "yardmaster")
		got := readCalls(t, calls)
		if r.Live() || r.Failures != 0 || strings.Contains(got, "workspace.create") || strings.Contains(got, "agent start") {
			t.Fatalf("%s: started beside it: %+v\n%s", agent, r, got)
		}
		var reasons []string
		evs, _ := st.Events(ctx, r.Journal, 100)
		for _, e := range evs {
			if e.Kind == "crew_blocked" {
				reasons = append(reasons, fmt.Sprint(e.Data["reason"]))
			}
		}
		if len(reasons) != 1 || !strings.Contains(reasons[0], `"`+agent+`"`) || !strings.Contains(reasons[0], "w7:p1") {
			t.Fatalf("%s: crew_blocked = %q, want one naming agent and pane", agent, reasons)
		}
		setAgents(t, agents)
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		r, _ = st.GetCrew(ctx, "yardmaster")
		if got := readCalls(t, calls); !r.Live() || r.Handle["agent"] != "y1-yardmaster" || !strings.Contains(got, "workspace.create") {
			t.Fatalf("%s: not started once it was gone: %+v\n%s", agent, r, got)
		}
	}
}

// serverSettingNames are the settings of the yard's server.
var serverSettingNames = []string{"YARDR_HOOK_ADDR", "YARDR_WEB_ADDR", "YARDR_BDP_ADDR", "YARDR_EVENT_RETENTION", "YARDR_HOOK_PAYLOAD_RETENTION"}

// A herdr session never has the settings of the yard's server: not from the
// daemon's environment, not from an env file, and not from herdr's own,
// which a pane starts with and yardr can only set over. So each is on the
// request that opens the pane, set to nothing, which reads as unset: an
// agent's trial server, or a test it runs, does not listen on this yard's
// webhook address (yardr-t9n1).
func TestHerdrSessionsGetNoServerSettings(t *testing.T) {
	// Serial: sets the daemon's environment.
	for _, n := range serverSettingNames {
		t.Setenv(n, "the-daemons")
	}
	envFile := filepath.Join(t.TempDir(), "group.env")
	if err := os.WriteFile(envFile, []byte("LINEAR_API_KEY="+envSecret+"\nYARDR_HOOK_ADDR=127.0.0.1:1\nYARDR_WEB_ADDR=127.0.0.1:2\nYARDR_BDP_ADDR=127.0.0.1:3\nYARDR_EVENT_RETENTION=1d\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(calls, paneCall string) {
		t.Helper()
		env := envOf(apiCall(t, calls, paneCall))
		for _, n := range serverSettingNames {
			if v, set := env[n]; !set || v != "" {
				t.Errorf("%s env has %s=%q (set %v), want it set to nothing: %v", paneCall, n, v, set, env)
			}
		}
		if env["LINEAR_API_KEY"] != envSecret {
			t.Errorf("%s lost the env file's other entry: %v", paneCall, env)
		}
	}

	wt := seedEmptyRepo(t)
	h, calls, _ := envHerdr(t, wt)
	h.YardHome = t.TempDir()
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Stage: "new"},
		Group:   store.Group{Name: "yard-builders", Config: map[string]any{"env_file": envFile}},
		Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	if _, err := h.Start(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	check(readCalls(t, calls), "tab.create")

	h, calls, _ = envHerdr(t, "")
	r := store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: map[string]any{"env_file": envFile}}
	if _, err := h.StartCrew(context.Background(), r, ""); err != nil {
		t.Fatal(err)
	}
	check(readCalls(t, calls), "workspace.create")
}

// An exec session's command inherits the daemon's environment, but not the
// settings of the yard's server: the assembly's tests ran with the daemon's
// YARDR_HOOK_ADDR, and a server its tests started listened there
// (yardr-t9n1). A dir depot's bead, whose inherited environment is cut
// further, is no exception.
func TestExecSessionsGetNoServerSettings(t *testing.T) {
	// Serial: sets the daemon's environment.
	for _, n := range serverSettingNames {
		t.Setenv(n, "the-daemons")
	}
	t.Setenv("YARDR_T9N1_KEPT", "kept")
	dir := t.TempDir()
	for name, depot := range map[string]*store.Depot{
		"git": {Name: "web", Path: dir, Base: "main"},
		"dir": {Name: "docs", Path: dir, Kind: store.DepotDir},
	} {
		out := filepath.Join(dir, name+".env")
		job := dispatch.Job{
			Bead: store.Bead{ID: "yd-7", Stage: "approved"},
			Group: store.Group{Name: "assembly", Config: map[string]any{"cmd": "env > " + out + ".tmp && mv " + out + ".tmp " + out,
				"log": filepath.Join(dir, "log")}},
			Depot:   depot,
			Session: store.Session{ID: "s1"},
		}
		if _, err := (&Exec{Home: t.TempDir(), Bin: "/bin/yardr"}).Start(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		var got string
		for deadline := time.Now().Add(5 * time.Second); got == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			b, _ := os.ReadFile(out)
			got = string(b)
		}
		if !strings.Contains(got, "YARDR_T9N1_KEPT=kept\n") || !strings.Contains(got, "YARDR_BEAD=yd-7\n") {
			t.Fatalf("%s depot: the command's environment lacks what it should have:\n%s", name, got)
		}
		for _, n := range serverSettingNames {
			if strings.Contains(got, n+"=") {
				t.Errorf("%s depot: the command got %s:\n%s", name, n, got)
			}
		}
	}
}

// execEnvJob is a job of an exec group whose command writes its environment
// to the returned file, whole once it is there.
func execEnvJob(t *testing.T, depot *store.Depot, envFile string) (dispatch.Job, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	return dispatch.Job{
		Bead: store.Bead{ID: "yd-7", Stage: "deploy"},
		Group: store.Group{Name: "deployers", Config: map[string]any{"cmd": "env > " + out + ".tmp && mv " + out + ".tmp " + out,
			"log": filepath.Join(dir, "log"), "env_file": envFile}},
		Depot:   depot,
		Session: store.Session{ID: "s1"},
	}, out
}

// An exec group's command gets its env_file's entries, as a herdr group's
// agent does: over what the daemon was itself started with, but not over a
// key yardr sets, and only the key names reach the log.
func TestExecStartLoadsGroupEnvFile(t *testing.T) {
	// Serial: sets the daemon's environment.
	t.Setenv("LINEAR_API_KEY", "the-daemons")
	var logs bytes.Buffer
	e := &Exec{Home: t.TempDir(), Bin: "/bin/yardr",
		Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	job, out := execEnvJob(t, &store.Depot{Name: "web", Path: t.TempDir(), Base: "main"}, envFileT(t, 0o600))
	handle, err := e.Start(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	got := waitFile(t, out)
	if !strings.Contains(got, "LINEAR_API_KEY="+envSecret+"\n") || strings.Contains(got, "the-daemons") {
		t.Errorf("the command lacks the env file's entry, or has the daemon's:\n%s", got)
	}
	if !strings.Contains(got, "YARDR_ACTOR=deployers\n") || strings.Contains(got, "someone-else") {
		t.Errorf("env file overrode the identity:\n%s", got)
	}
	if h := fmt.Sprint(handle); strings.Contains(h, envSecret) {
		t.Errorf("handle holds the value: %s", h)
	}
	if l := logs.String(); strings.Contains(l, envSecret) || !strings.Contains(l, "LINEAR_API_KEY") || !strings.Contains(l, "group deployers") {
		t.Errorf("the log shows the value, or lacks the key names:\n%s", l)
	}
	if raw, _ := os.ReadFile(job.Group.Config["log"].(string)); strings.Contains(string(raw), envSecret) {
		t.Errorf("the session's log shows the value:\n%s", raw)
	}
}

// An env_file gives an exec command nothing the daemon's own environment
// may not: no setting of the yard's server, no test command for a depot with
// none, and for a dir depot's bead no base, branch or worktree.
func TestExecEnvFileGivesNoWithheldVariable(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "deploy.env")
	body := "DOKKU_KEY_FILE=/k\nYARDR_TEST=rm -rf\nYARDR_BASE=theirs\nYARDR_BRANCH=theirs\nYARDR_WORKTREE=/theirs\n"
	for _, n := range serverSettingNames {
		body += n + "=theirs\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	job, out := execEnvJob(t, &store.Depot{Name: "docs", Path: t.TempDir(), Kind: store.DepotDir}, p)
	if _, err := (&Exec{Home: t.TempDir(), Bin: "/bin/yardr"}).Start(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	got := waitFile(t, out)
	if !strings.Contains(got, "DOKKU_KEY_FILE=/k\n") {
		t.Fatalf("the command lacks the env file's entry:\n%s", got)
	}
	if strings.Contains(got, "theirs") || strings.Contains(got, "YARDR_TEST=") {
		t.Errorf("the env file gave a variable the command must not have:\n%s", got)
	}
}

// A file others can read, or none at all, refuses an exec group's start as
// Blocked, as a herdr group's is, before the command runs or its log is
// made, and the error shows no value.
func TestExecEnvFileRefusedBlocksStart(t *testing.T) {
	t.Parallel()
	for name, path := range map[string]string{"0644": envFileT(t, 0o644), "0640": envFileT(t, 0o640),
		"missing": filepath.Join(t.TempDir(), "nope.env"), "directory": t.TempDir()} {
		var logs bytes.Buffer
		e := &Exec{Home: t.TempDir(), Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
		job, out := execEnvJob(t, &store.Depot{Name: "web", Path: t.TempDir(), Base: "main"}, path)
		handle, err := e.Start(context.Background(), job)
		if !dispatch.IsBlocked(err) {
			t.Errorf("%s: err = %v, want Blocked", name, err)
		} else if strings.Contains(err.Error(), envSecret) || !strings.Contains(err.Error(), "env_file") ||
			!strings.Contains(err.Error(), "group deployers") {
			t.Errorf("%s: error %q", name, err)
		}
		if handle != nil {
			t.Errorf("%s: a refused start left a handle: %v", name, handle)
		}
		// The command would have written out at once; give it a moment.
		time.Sleep(50 * time.Millisecond)
		for _, f := range []string{out, job.Group.Config["log"].(string)} {
			if _, err := os.Stat(f); err == nil {
				t.Errorf("%s: %s was made despite the refusal", name, f)
			}
		}
		if strings.Contains(logs.String(), envSecret) {
			t.Errorf("%s: logs show the value", name)
		}
	}
}

// waitFile is what an exec command wrote to path, waited for.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(path); len(b) > 0 {
			return string(b)
		}
	}
	t.Fatalf("%s was never written", path)
	return ""
}

// A crew member's handle names the shell of its pane, as a bead session's
// does: by it the store knows a command run in the pane as the crew member's,
// whatever actor the command names (store's release of a peer's bead).
// A herdr that does not tell leaves the handle without it.
func TestStartCrewRecordsItsShell(t *testing.T) {
	t.Parallel()
	h, calls, _ := envHerdr(t, "")
	h.Bin = fakeHerdrBin(t, `printf '%s\n' "$*" >> `+calls+`
case "$1 $2" in
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p1","shell_pid":`+fmt.Sprint(os.Getpid())+`}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
	handle, err := h.StartCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: t.TempDir()}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := readCalls(t, calls)
	if handle["shell_pid"] != os.Getpid() || handle["shell_start"] == nil || !strings.Contains(got, "pane process-info --pane ") {
		t.Fatalf("shell_pid = %v, shell_start = %v, want this process %d:\n%s", handle["shell_pid"], handle["shell_start"], os.Getpid(), got)
	}
	// The shell is asked for before the agent is started in it.
	if strings.Index(got, "pane process-info") > strings.Index(got, "agent start") {
		t.Fatalf("the shell was asked for after the agent started:\n%s", got)
	}
	plain, _, _ := envHerdr(t, "")
	if handle, err := plain.StartCrew(context.Background(), store.CrewMember{Name: "yardmaster", Dir: t.TempDir()}, ""); err != nil || handle["shell_pid"] != nil {
		t.Fatalf("a herdr that tells no shell: handle %v, %v", handle, err)
	}
}
