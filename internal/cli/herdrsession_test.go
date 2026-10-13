package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// herdrBySocket is a herdr whose server runs in the session gc alone: it
// answers by the socket its environment sends it to, as herdr 0.9 does
// before the session's name.
const herdrBySocket = `#!/bin/sh
case "$HERDR_SOCKET_PATH" in
  */sessions/gc/herdr.sock) echo '{"id":"cli:x","result":{"agents":[]}}' ;;
  *) echo "{\"id\":\"cli:x\",\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running at $HERDR_SOCKET_PATH\"}}" >&2; exit 1 ;;
esac
`

// otherShell is a shell in a pane of the herdr session other, on a machine
// with a herdr config directory of its own; it returns the directory.
func otherShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "herdr", "sessions", "other", "herdr.sock"))
	t.Setenv("HERDR_SESSION", "other")
	t.Cleanup(func() { runner.FollowSetting(nil); runner.UseSession("") })
	return dir
}

// doctor names the herdr session it asked and its socket: this shell's
// while the yard names none, and the yard's once herdr_session is set,
// whatever session the shell doctor is run in is. A session that is not
// running is a fault that names it and how it is started.
func TestDoctorNamesTheHerdrSession(t *testing.T) {
	newHome(t)
	dir := otherShell(t)
	stub(t, &herdrServer, askHerdrServer)
	st := storetest.New(t)
	tools := fakeTools(t, map[string]string{"git": toolAnswers, "herdr": herdrBySocket, "claude": toolAnswers})
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	herdr := filepath.Join(tools, "herdr")
	sock := func(name string) string { return filepath.Join(dir, "herdr", "sessions", name, "herdr.sock") }

	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  herdr       "+herdr+": on PATH, but its server is not running",
		"Start it: run herdr in a terminal",
		"; session other, socket "+sock("other")+" (this shell's, since the yard's herdr_session setting is unset)")

	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "settings", "set", "herdr_session", "gc"); err != nil {
		t.Fatal(err)
	}
	out, _ = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "ok     herdr       "+herdr+", its server is running; session gc, socket "+sock("gc")+" (the yard's herdr_session setting)")
	rep, _ := doctorJSON(t, st)
	for _, n := range rep.Machine {
		if n.Program != "herdr" {
			continue
		}
		if s := n.Server.Session; s == nil || *s != (runner.HerdrSession{Name: "gc", Socket: sock("gc"), From: runner.SessionSet}) {
			t.Fatalf("doctor --json: herdr session = %+v", s)
		}
	}

	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "settings", "set", "herdr_session", "nope"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  herdr       "+herdr+": on PATH, but its server is not running (herdr session \"nope\", which the yard's herdr_session setting names, is not running",
		"Start it: run herdr session attach nope in a terminal",
		"; session nope, socket "+sock("nope")+" (the yard's herdr_session setting)")
	if _, err := os.Stat(filepath.Join(dir, "herdr")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor made something for the session: %v", err)
	}
}

// status names the herdr session and socket the yard's panes are asked for
// in: the shell's own, said to be that, while herdr_session is unset, and
// the yard's once it is set.
func TestStatusNamesTheHerdrSession(t *testing.T) {
	dir := otherShell(t)
	_, st, a := statusYard(t, "abcdef123456")
	sock := func(name string) string { return filepath.Join(dir, "herdr", "sessions", name, "herdr.sock") }

	out, err := run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "herdr      session other, socket "+sock("other")+" (this shell's, since the yard's herdr_session setting is unset)\n",
		"           that is this shell's: a daemon started from another uses its own. Name the yard's: "+here("yardr settings set herdr_session <name>")+"\n")

	if err := st.SetSetting(context.Background(), store.HerdrSessionSetting, "gc", "Jo"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a().rootCmd(), "status")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "herdr      session gc, socket "+sock("gc")+" (the yard's herdr_session setting)\n")
	if strings.Contains(out, "this shell's") {
		t.Errorf("a set session is not the shell's:\n%s", out)
	}
	if got := statusJSON(t, a()).Herdr; got == nil || *got != (runner.HerdrSession{Name: "gc", Socket: sock("gc"), From: runner.SessionSet}) {
		t.Fatalf("status --json: herdr = %+v", got)
	}
}

// yardr settings set herdr_session is refused while a crew member runs,
// which it lists, and yardr settings shows the setting with the others.
func TestSettingsSetHerdrSessionWhileCrewRuns(t *testing.T) {
	_, st, a := statusYard(t, "abcdef123456")
	ctx := context.Background()
	if err := st.CrewStarted(ctx, "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "settings", "set", "herdr_session", "gc", "--actor", "Jo")
	if err == nil || !strings.Contains(err.Error(), "crew member yardmaster") {
		t.Fatalf("settings set herdr_session = %v\n%s", err, out)
	}
	if err := st.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a().rootCmd(), "settings", "set", "herdr_session", "gc", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a().rootCmd(), "settings", "get")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "herdr_session", "gc")
}

// yardr daemon install, run in a shell with no herdr variable, writes a
// service that reaches the session the yard names: the file carries no
// socket, and the daemon it starts, with the file's environment and no
// more, reads the setting and is sent to that session.
func TestDaemonServiceReachesTheHerdrSessionSet(t *testing.T) {
	home := newHome(t)
	dir, _ := stubServices(t, "darwin")
	stubLookPath(t, "git", "herdr")
	t.Setenv("HERDR_SESSION", "")
	t.Cleanup(func() { runner.FollowSetting(nil); runner.UseSession("") })
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a().rootCmd(), "settings", "set", "herdr_session", "gc"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a().rootCmd(), "daemon", "install"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "dev.yardr.daemon."+homeID(home)+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	// stubServices leaves HERDR_SOCKET_PATH empty, which is written empty.
	if strings.Contains(string(body), "herdr.sock") || strings.Contains(string(body), "HERDR_SESSION") {
		t.Fatalf("the service names a herdr socket or session of the installer's:\n%s", body)
	}
	// The daemon of that service: HOME and no herdr variable, as here.
	bin := filepath.Join(t.TempDir(), "herdr")
	scripttest.Write(t, bin, herdrBySocket)
	h := &runner.Herdr{Bin: bin, Store: st}
	if down, err := h.ServerUp(context.Background()); err != nil {
		t.Fatalf("the daemon did not reach the session gc: down=%v, %v", down, err)
	}
	if got, want := h.SocketPath(), "/Users/jo/.config/herdr/sessions/gc/herdr.sock"; got != want {
		t.Fatalf("the daemon's API socket = %s, want %s", got, want)
	}
}
