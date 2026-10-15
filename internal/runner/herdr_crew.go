package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/otel"
	"yardr.dev/yardr/internal/store"
)

// yardName is what tells this yard's crew from another yard's in
// herdr, which lists every yard on the machine's agents together: the
// `name` setting, else the home's directory name (~/.yardr reads as yardr).
func (h *Herdr) yardName(ctx context.Context) string {
	var all map[string]string
	if h.Store != nil {
		all, _ = h.Store.Settings(ctx)
	}
	return YardName(h.YardHome, all)
}

// YardName is the yard's name from its settings and its home (see
// yardName), for whoever has to name a crew member's agent or workspace as
// herdr lists it without a runner at hand.
func YardName(home string, settings map[string]string) string {
	if settings["name"] != "" {
		return settings["name"]
	}
	return strings.TrimLeft(filepath.Base(home), ".")
}

// crewDir is where a crew member's session runs: its dir, else the yard's
// home.
func (h *Herdr) crewDir(r store.CrewMember) string {
	if r.Dir != "" {
		return r.Dir
	}
	return h.YardHome
}

// LiveCrew looks through herdr's agents for one that already is the line
// session ("") or the conversation session for user of r without being
// the session yardr recorded for it (see dispatch.CrewFinder): an agent
// under the session's herdr name, or one under its bare name in its
// directory, which is what a pane started by hand before the crew member
// was defined looks like. The recorded session itself (its handle's agent,
// in its pane) is not counted: after a stop for a handoff herdr may list
// it a moment longer.
func (h *Herdr) LiveCrew(ctx context.Context, r store.CrewMember, user string) (*dispatch.LiveAgent, error) {
	raw, err := h.run(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var list struct {
		Agents []struct {
			Name string `json:"name"`
			Cwd  string `json:"cwd"`
			Pane string `json:"pane_id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("agent list: %w", err)
	}
	yard := h.yardName(ctx)
	full, bare := CrewAgentName(yard, r.Name), agentName(r.Name)
	if user != "" {
		full, bare = CrewConversationAgentName(yard, r.Name, user), agentName(r.Name+"-"+user)
	}
	dir := realPath(h.crewDir(r))
	st := r.Session(user)
	own, _ := st.Handle["agent"].(string)
	ownPane, _ := st.Handle["pane"].(string)
	for _, a := range list.Agents {
		if a.Name == "" || (a.Name == own && a.Pane == ownPane) {
			continue
		}
		if a.Name == full || (a.Name == bare && dir != "" && realPath(a.Cwd) == dir) {
			return &dispatch.LiveAgent{Name: a.Name, Pane: a.Pane}, nil
		}
	}
	return nil, nil
}

// realPath is path with its symlinks resolved, as herdr reports a pane's
// cwd (/tmp is /private/tmp on macOS); a path that cannot be resolved is
// only cleaned.
func realPath(path string) string {
	if path == "" {
		return ""
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

// CrewPromptLead and CrewPromptEnd are how a crew member's first prompt
// begins and ends, for whoever looks in its pane for what the agent printed
// after it. What a prompt says beyond its prime (the greeting) goes between
// the two.
const (
	CrewPromptLead = "You are the yardr crew member"
	CrewPromptEnd  = "and follow it."
)

// GreetKey is the crew member config key that names the user its first session
// greets by mail. yardr init sets it on the yardmaster it adds, to the
// operator: the onboarding ends in yardr chat, which shows mail and no pane,
// so the yardmaster's first word has to be mail.
const GreetKey = "greet"

// Greets is the user r's next session is asked to greet: the one its config
// names, for its first session only. A session after that (a rotation, a
// restart) is in a conversation that has begun, or was never answered.
func Greets(r store.CrewMember) string {
	if r.StartedAt != nil {
		return ""
	}
	return str(r.Config, GreetKey, "")
}

// Greeted reports whether r's running session is the one that was asked to
// greet (Greets, before it started).
func Greeted(r store.CrewMember) bool {
	return r.Live() && r.PrevStartedAt == nil && str(r.Config, GreetKey, "") != ""
}

// crewPrompt is a crew member's first prompt: who it is and where its
// prime is, and for a line session that greets (Greets) whom to write to
// first. A conversation session never greets: its user talks first, and the
// greeting is the setup's. It is told whose it is and whose slice it
// reads instead, and primes with --for. It begins and ends as every crew
// member's does: the start is what a pane is searched for to tell that
// the prompt arrived (showsPrompt), the end what the agent's answer is
// looked for after.
func crewPrompt(yardr string, r store.CrewMember, user string) string {
	who, run := fmt.Sprintf("%q", r.Name), "Run"
	prime := fmt.Sprintf("`%s prime --crew %s`", yardr, r.Name)
	if user != "" {
		who = fmt.Sprintf("%q, talking with %q", r.Name, user)
		run = fmt.Sprintf("Your inbox is %s's slice alone. Run", user)
		prime = fmt.Sprintf("`%s prime --crew %s --for %s`", yardr, r.Name, user)
	} else if greets := Greets(r); greets != "" {
		run = fmt.Sprintf("%s set this yard up a moment ago and waits for your first message in `%s chat`, which shows "+
			"them your mail and not your pane. Once you have read your prime, greet them with `%s mail %s \"<text>\"` and say "+
			"what you need from them for the next setup step; go on with them by mail. First run", greets, yardr, yardr, greets)
	}
	return fmt.Sprintf(CrewPromptLead+" %s. %s %s "+CrewPromptEnd, who, run, prime)
}

// StartCrew opens a herdr workspace in the crew member's directory, with
// its identity in the environment, starts its agent and points it at its
// prime: the line session ("") or the conversation session for user, on
// that session's env. The agent and the workspace are named after the yard
// and the crew member (CrewAgentName), with the user for a conversation.
func (h *Herdr) StartCrew(ctx context.Context, r store.CrewMember, user string) (map[string]any, error) {
	dir := h.crewDir(r)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("crew member directory %q does not exist", dir)
	}
	secrets, err := h.crewSessionEnv(ctx, r, dir, user)
	if err != nil {
		return nil, err
	}
	yard := h.yardName(ctx)
	name, label := CrewAgentName(yard, r.Name), CrewLabel(yard, r.Name)
	if user != "" {
		name, label = CrewConversationAgentName(yard, r.Name, user), CrewConversationLabel(yard, r.Name, user)
	}
	// The settings of the yard's server are blanked, as for a bead's agent
	// (agentEnv).
	env := slices.Concat(config.Env("CREW", r.Name), config.Env("ACTOR", r.Name), config.BlankServerSettings())
	if user != "" {
		// How the session's commands read their slice: prime, inbox and
		// the rest resolve the session from it.
		env = append(env, config.Env(CrewForEnv, user)...)
	}
	if h.YardHome != "" {
		env = append(env, config.Env("HOME", h.YardHome)...)
	}
	// From here until its pane is recorded, a setting change could make the
	// workspace we are about to create use a different herdr session from
	// the panes already running. Keep this start visible with no session
	// recorded yet; shellOn fills in the pane and its session below. What
	// a failed start left on record stays beside it: a pane the daemon
	// could not close is the crew member's while its shell runs
	// (store.paneCrew), also when this start opens none.
	if h.Store != nil {
		starting := maps.Clone(r.Session(user).Starting)
		if starting == nil {
			starting = map[string]any{}
		}
		starting[store.HandleHerdrSession] = ""
		if err := h.Store.CrewStartingFor(ctx, r.Name, user, starting); err != nil {
			return nil, err
		}
	}
	// The environment goes over the socket, never on a command line (see
	// call). The workspace is not focused: a crew member starts in the
	// background, under the daemon, not in front of whoever is at herdr.
	raw, err := h.call(ctx, "workspace.create", map[string]any{
		"cwd": dir, "label": label, "focus": false,
		"env": envMap(withEnvFile(env, secrets)),
	})
	if err != nil {
		return nil, fmt.Errorf("workspace for %s: %w", r.Name, err)
	}
	var ws herdrResult
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("workspace result: %w", err)
	}
	handle := map[string]any{"agent": name, "workspace": ws.Workspace.ID, "tab": ws.Tab.ID,
		"pane": ws.RootPane.ID, "dir": dir}
	if user != "" {
		handle["for"] = user
	}
	// As for a bead's session: the store then knows a command run in the
	// crew member's pane as the crew member's, whatever actor it names, which is
	// what keeps a crew member from releasing a peer's bead as a user
	// (store.paneCrew).
	h.shellOn(ctx, ws.RootPane.ID, handle)
	if err := h.shellKnown(handle, func(handle map[string]any) error {
		return h.Store.CrewStartingFor(ctx, r.Name, user, handle)
	}); err != nil {
		return handle, err
	}

	kind := agents.Kind(r.Config)
	handle[harnessKind] = kind
	start := []string{"agent", "start", name, "--kind", kind, "--pane", ws.RootPane.ID}
	if extra := agentArgs(r.Config, "", h.YardHome, dir); len(extra) > 0 {
		start = append(append(start, "--"), extra...)
	}
	prompt := crewPrompt(h.yard(), r, user)
	handle[firstPrompt] = prompt
	return h.startAgent(ctx, start, name, prompt, handle, trustEntry(r.Config))
}

// CrewForEnv is the pane env name (YARDR_CREW_FOR) carrying the user of a
// conversation session: how its commands read their slice.
const CrewForEnv = "CREW_FOR"

// crewSessionEnv is what the session's settings add to its pane
// environment: the line session's env_file and env_command (crewEnv), or a
// conversation session's env_for_ file alone, held to the env_file rule. A
// conversation never inherits the line's env: its file carries its user's
// login, and the line's command output winning over it could burn the
// wrong login. Either source refused is Blocked, as a group's env_file is.
func (h *Herdr) crewSessionEnv(ctx context.Context, r store.CrewMember, dir, user string) ([]string, error) {
	if user == "" {
		return h.crewEnv(ctx, r, dir)
	}
	who := "crew member " + r.Name + " for " + user
	f, err := config.ReadEnvFile(r.EnvFor(user))
	if err != nil {
		return nil, dispatch.Blocked(fmt.Errorf("%s: %w", who, err))
	}
	if h.Log != nil {
		h.Log.Debug("env_file loaded", "for", who, "path", f.Path, "keys", f.Keys())
	}
	return f.Env, nil
}

// envFile reads the env_file cfg names, if any, for the pane environment of
// who ("crew member yardmaster", "group builders"); see envFile.
func (h *Herdr) envFile(cfg map[string]any, who string) ([]string, error) {
	return envFile(h.Log, cfg, who)
}

// envFile reads the env_file cfg names, if any, for the environment of a
// session of who, a herdr pane's or an exec command's. A file that is
// missing, not a regular file or open to others is refused as Blocked: the
// session must not start without its credentials, nor with credentials
// others can read. Only key names are logged.
func envFile(log *slog.Logger, cfg map[string]any, who string) ([]string, error) {
	path := str(cfg, "env_file", "")
	if path == "" {
		return nil, nil
	}
	f, err := config.ReadEnvFile(path)
	if err != nil {
		return nil, dispatch.Blocked(fmt.Errorf("%s: %w", who, err))
	}
	if log != nil {
		log.Debug("env_file loaded", "for", who, "path", f.Path, "keys", f.Keys())
	}
	return f.Env, nil
}

// crewEnv is what r's settings add to its pane environment: its
// env_file's entries and those its env_command prints (see
// config.ReadCrewEnv), asked for at every start. Either one refused (a
// file open to others, a command that fails, hangs or prints a line that is
// no KEY=VALUE) is Blocked, as a group's env_file is: the crew member must not
// start without its credentials. Only key names are logged.
func (h *Herdr) crewEnv(ctx context.Context, r store.CrewMember, dir string) ([]string, error) {
	env, err := config.ReadCrewEnv(ctx, r.Name, dir, h.YardHome, r.Config)
	if err != nil {
		if ctx.Err() != nil {
			// The daemon is stopping: nothing a human has to fix.
			return nil, err
		}
		return nil, dispatch.Blocked(fmt.Errorf("crew member %s: %w", r.Name, err))
	}
	if h.Log != nil && env.Set() {
		h.Log.Debug("crew member environment loaded", "for", "crew member "+r.Name, "from", env.From(), "keys", env.Keys())
	}
	return env.Env, nil
}

// withEnvFile is env with the env file's entries added. A key yardr sets
// itself (the session's identity, the yard's home) is left out of the file's
// entries, so a credentials file cannot make an agent someone else. The one
// such key that is a list, OTEL_RESOURCE_ATTRIBUTES, keeps the file's
// attributes in front of yardr's: a group's env file is where an operator
// turns a harness's telemetry on, and may name attributes of its own.
func withEnvFile(env, file []string) []string {
	set := make(map[string]int, len(env))
	out := slices.Clone(env)
	for i, e := range out {
		k, _, _ := strings.Cut(e, "=")
		set[k] = i
	}
	for _, e := range file {
		k, v, _ := strings.Cut(e, "=")
		i, ours := set[k]
		switch {
		case !ours:
			out = append(out, e)
		case k == otel.ResourceVar:
			if theirs := otel.ResourceAttributes(v); theirs != "" {
				_, mine, _ := strings.Cut(out[i], "=")
				out[i] = k + "=" + theirs + "," + mine
			}
		}
	}
	return out
}
