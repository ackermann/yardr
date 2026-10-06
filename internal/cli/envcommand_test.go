package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A crew member whose only credential source is a provider (env_command):
// yardr reply, in a pane with the environment a start gives it, answers as
// the GitHub App the provider names, and crew show says where the
// environment comes from, with the names and no value. A provider that
// fails is said there too, with nothing it printed.
func TestCrewAnswersWithAProvidersCredential(t *testing.T) {
	y := newAppYard(t)
	ctx := context.Background()
	provider := filepath.Join(t.TempDir(), "provider")
	body := func(more string) {
		t.Helper()
		script := "#!/bin/sh\necho GITHUB_APP_ID=4242\necho GITHUB_APP_PRIVATE_KEY_FILE=" + y.keyFile + "\n" + more + "\n"
		if err := os.WriteFile(provider, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body("")
	def := store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"env_command": provider + " --env", store.SetState: "github", store.OpenThreads: "github"}}
	if _, err := y.st.PutCrew(ctx, def, "jo"); err != nil {
		t.Fatal(err)
	}
	// The pane's environment: nothing of newAppYard's, and what the runner
	// puts there at a start (Herdr.crewEnv reads it with this call;
	// runner's TestStartCrewLoadsEnvCommand has it reach the pane).
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "")
	env, err := config.ReadCrewEnv(ctx, "yardmaster", "", "", def.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env.Env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	out, err := run(t, y.a.replyCmd(), y.seqs["installed"], "On it.")
	if err != nil || !strings.HasPrefix(out, "https://github.com/acme/webapp/issues/7#issuecomment-") {
		t.Fatalf("reply: %q, %v", out, err)
	}
	if got := y.src.Calls(); !strings.Contains(strings.Join(got, "\n"), "github comment acme/webapp#7 by yardr[bot]") {
		t.Fatalf("calls: %v", got)
	}

	show, err := run(t, y.a.crewCmd(), "show", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, show, "  env_command "+provider+" --env (GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE)\n",
		"  github credential: GitHub App (GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE)\n")
	if strings.Contains(show, "env_file") {
		t.Errorf("show names an env_file the crew member has not:\n%s", show)
	}
	y.clean(t, out, show)

	body("echo GITHUB_TOKEN=gh-pr0vided; echo 'vault is locked' >&2; exit 3")
	if show, err = run(t, y.a.crewCmd(), "show", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	wantText(t, show, "  env_command "+provider+" --env: env_command "+provider+" --env: exit status 3; its stderr ended: vault is locked\n")
	if strings.Contains(show, "gh-pr0vided") || strings.Contains(show, "github credential: GitHub App") {
		t.Errorf("show of a provider that fails:\n%s", show)
	}
}

// doctor says which crew member's env_command names a program it does not
// find, and runs none to tell.
func TestDoctorNotesAMissingEnvCommand(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	ran := filepath.Join(t.TempDir(), "ran")
	there := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(there, []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, cmd := range map[string]string{"clerk": there, "scribe": "yardr-no-such-provider --env"} {
		if _, err := st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", Config: map[string]any{"env_command": cmd}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := st.Crew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err := readState(ctx, &config.Config{Home: t.TempDir()}, st, rs)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.EnvCommands) != 1 || !strings.Contains(state.EnvCommands[0], "crew member scribe does not start without its environment: env_command yardr-no-such-provider --env: ") {
		t.Fatalf("notes %q", state.EnvCommands)
	}
	if _, err := os.Stat(ran); !errors.Is(err, os.ErrNotExist) {
		t.Error("doctor ran a provider")
	}
}
