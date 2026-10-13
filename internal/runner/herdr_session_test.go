package runner

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// herdrSessions is a machine with herdr's config directory of its own, in
// which the test starts what sessions it needs (sessionListens), and a
// process started from a pane of the session named other: its environment
// has that session's socket and name, as herdr gives a pane. What the test
// leaves the process following is forgotten after it.
func herdrSessions(t *testing.T) {
	t.Helper()
	// Not under t.TempDir: see fakeHerdr on the socket path limit.
	dir, err := os.MkdirTemp("", "yh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HERDR_SOCKET_PATH", SessionSocket("other"))
	t.Setenv("HERDR_SESSION", "other")
	t.Cleanup(func() { FollowSetting(nil); UseSession("") })
	FollowSetting(nil)
	UseSession("")
}

// sessionListens starts the herdr session name: its socket answers an
// event subscription, and anything else with the session's name as result.
// Each connection is sent on the channel.
func sessionListens(t *testing.T, name string) <-chan net.Conn {
	t.Helper()
	sock := SessionSocket(name)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ch := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			if strings.Contains(line, `"events.subscribe"`) {
				c.Write([]byte(`{"id":"yard-events","result":{"type":"subscription_started"}}` + "\n"))
			} else {
				c.Write([]byte(`{"id":"x","result":"` + name + `"}` + "\n"))
			}
			ch <- c
		}
	}()
	return ch
}

// sessionBin is a herdr program that answers with the session its
// environment sends it to, as herdr 0.9 chooses: the socket before the
// name. A socket nothing listens on is answered as herdr does a session
// that is not running.
func sessionBin(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "herdr")
	scripttest.Write(t, p, `#!/bin/sh
sock="${HERDR_SOCKET_PATH:-$XDG_CONFIG_HOME/herdr/sessions/$HERDR_SESSION/herdr.sock}"
if [ ! -S "$sock" ]; then
  echo "{\"id\":\"x\",\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running at $sock; run herdr to start or attach it\"}}" >&2
  exit 1
fi
echo "{\"id\":\"x\",\"result\":\"$sock $HERDR_SESSION\"}"
`)
	return p
}

func yardNaming(t *testing.T, session string) store.API {
	t.Helper()
	st := storetest.New(t)
	if err := st.SetSetting(context.Background(), store.HerdrSessionSetting, session, "me"); err != nil {
		t.Fatal(err)
	}
	return st
}

// With herdr_session set, a daemon started from a pane of another herdr
// session (its HERDR_SOCKET_PATH and HERDR_SESSION are that one's) reaches
// the session the yard names: the herdr program, the API socket and the
// event stream. So does one started with no herdr variable at all, as a
// login service is.
func TestHerdrSessionSettingIsReachedFromAnotherSessionsShell(t *testing.T) {
	for _, service := range []bool{false, true} {
		herdrSessions(t)
		if service {
			os.Unsetenv("HERDR_SOCKET_PATH")
			os.Unsetenv("HERDR_SESSION")
		}
		yard, other := sessionListens(t, "gc"), sessionListens(t, "other")
		ctx, cancel := context.WithCancel(context.Background())
		h := &Herdr{Bin: sessionBin(t), Store: yardNaming(t, "gc")}

		out, err := h.run(ctx, "agent", "list")
		if want := `"` + SessionSocket("gc") + ` gc"`; err != nil || string(out) != want {
			t.Fatalf("the herdr program was sent to %s, %v; want %s", out, err, want)
		}
		if out, err := h.herdrPaneRead(ctx, "w1:p1", paneScreen, 5); err != nil || !strings.Contains(out, SessionSocket("gc")) {
			t.Fatalf("pane read was sent to %s, %v", out, err)
		}
		if out, err := h.call(ctx, "tab.create", nil); err != nil || string(out) != `"gc"` {
			t.Fatalf("the API call reached %s, %v", out, err)
		}
		<-yard
		if _, err := h.Wake(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case <-yard:
		case <-time.After(2 * time.Second):
			t.Fatal("the event stream did not reach the yard's session")
		}
		select {
		case <-other:
			t.Fatal("the shell's own session was asked")
		default:
		}
		if got := SessionInUse(); got.Name != "gc" || got.Socket != SessionSocket("gc") || got.From != SessionSet {
			t.Fatalf("SessionInUse = %+v", got)
		}
		cancel()
	}
}

// Unset, the yard does as before: the session of the environment, by its
// socket, else herdr's default.
func TestHerdrSessionUnsetIsTheEnvironments(t *testing.T) {
	herdrSessions(t)
	ctx := context.Background()
	other := sessionListens(t, "other")
	h := &Herdr{Bin: sessionBin(t), Store: storetest.New(t)}
	if out, err := h.run(ctx, "agent", "list"); err != nil || string(out) != `"`+SessionSocket("other")+` other"` {
		t.Fatalf("the herdr program was sent to %s, %v", out, err)
	}
	if out, err := h.call(ctx, "tab.create", nil); err != nil || string(out) != `"other"` {
		t.Fatalf("the API call reached %s, %v", out, err)
	}
	<-other
	if cmd := HerdrCommand(ctx, h.Bin, "agent", "list"); cmd.Env != nil {
		t.Fatalf("the program's environment is not the process's own: %v", cmd.Env)
	}
	if got := SessionInUse(); got != (HerdrSession{Name: "other", Socket: SessionSocket("other"), From: SessionInherited}) {
		t.Fatalf("SessionInUse = %+v", got)
	}
	t.Setenv("HERDR_SESSION", "")
	if got := SessionInUse(); got.Name != "" || got.Socket != SessionSocket("other") {
		t.Fatalf("a socket nothing names: %+v", got)
	}
	t.Setenv("HERDR_SOCKET_PATH", "")
	if got, want := SessionInUse(), (HerdrSession{Name: "default", Socket: filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "herdr", "herdr.sock"), From: SessionInherited}); got != want {
		t.Fatalf("no herdr variable: %+v, want %+v", got, want)
	}
	if got := h.SocketPath(); got != SessionSocket("default") {
		t.Fatalf("SocketPath = %s", got)
	}
}

// A command of the CLI has no runner of the daemon's: it follows the yard
// it connected to, and reads the setting when it first calls herdr, from
// whatever session its shell is in.
func TestCommandFollowsTheYardsHerdrSession(t *testing.T) {
	herdrSessions(t)
	ctx := context.Background()
	sessionListens(t, "gc")
	sessionListens(t, "other")
	bin := sessionBin(t)
	FollowSetting(yardNaming(t, "gc"))
	if got := SessionInUse(); got.From != SessionInherited {
		t.Fatalf("before the first call the setting is not read: %+v", got)
	}
	out, err := HerdrCommand(ctx, bin, "agent", "focus", "a").Output()
	if err != nil || !strings.Contains(string(out), SessionSocket("gc")+" gc") {
		t.Fatalf("herdr agent focus was sent to %s, %v", out, err)
	}
	if out, err := (&Herdr{Bin: bin}).run(ctx, "agent", "list"); err != nil || !strings.Contains(string(out), SessionSocket("gc")) {
		t.Fatalf("a runner without a store was sent to %s, %v", out, err)
	}
	// Another yard's command in the same process is not left in this one's.
	FollowSetting(storetest.New(t))
	if out, err := HerdrCommand(ctx, bin, "agent", "list").Output(); err != nil || !strings.Contains(string(out), SessionSocket("other")) {
		t.Fatalf("a yard that names none was sent to %s, %v", out, err)
	}
}

// A session herdr does not have, or one that is stopped, is herdr being
// down, told with the session's name: nothing is started and nothing
// created for it, and the shell's own session, which runs, is not used in
// its place.
func TestHerdrSessionThatIsNotRunning(t *testing.T) {
	herdrSessions(t)
	ctx := context.Background()
	other := sessionListens(t, "other")
	h := &Herdr{Bin: sessionBin(t), Store: yardNaming(t, "nope")}

	down, err := h.ServerUp(ctx)
	var rt *dispatch.RuntimeError
	if !down || !errors.As(err, &rt) {
		t.Fatalf("ServerUp = %v, %v; want herdr down, the runtime's failure", down, err)
	}
	for _, want := range []string{`herdr session "nope"`, "herdr_session", "server_not_running", SessionSocket("nope")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ServerUp = %v, want %q in it", err, want)
		}
	}
	_, err = h.call(ctx, "tab.create", nil)
	if !errors.As(err, &rt) || !strings.Contains(err.Error(), `herdr session "nope"`) {
		t.Fatalf("the API call = %v", err)
	}
	if err := h.stream(ctx, func() {}, func() {}); err == nil || !strings.Contains(err.Error(), `herdr session "nope"`) {
		t.Fatalf("the event stream = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(SessionSocket("nope"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was made for the session: %v", err)
	}
	select {
	case <-other:
		t.Fatal("the shell's own session was asked instead")
	default:
	}
}

// A setting that cannot be read fails the call: the environment's session
// may be another, in which every pane of the yard would look gone.
func TestHerdrIsNotAskedWhenTheSettingCannotBeRead(t *testing.T) {
	herdrSessions(t)
	other := sessionListens(t, "other")
	h := &Herdr{Bin: sessionBin(t), Store: noSettings{storetest.New(t)}}
	_, err := h.run(context.Background(), "agent", "list")
	var rt *dispatch.RuntimeError
	if !errors.As(err, &rt) || !strings.Contains(err.Error(), "herdr_session setting could not be read") {
		t.Fatalf("run = %v", err)
	}
	if _, err := h.call(context.Background(), "tab.create", nil); err == nil {
		t.Fatal("the API was called")
	}
	select {
	case <-other:
		t.Fatal("the shell's own session was asked")
	default:
	}
}

type noSettings struct{ store.API }

func (noSettings) Settings(context.Context) (map[string]string, error) {
	return nil, errors.New("server gone")
}

// A daemon outlives a change of the setting: its next call goes to the new
// session, and the event stream it holds open to the old one is left for
// one to the new.
func TestDaemonFollowsAChangeOfTheHerdrSession(t *testing.T) {
	herdrSessions(t)
	was := sessionCheck
	sessionCheck = 20 * time.Millisecond
	t.Cleanup(func() { sessionCheck = was })
	first, second := sessionListens(t, "gc"), sessionListens(t, "gc2")
	st := yardNaming(t, "gc")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &Herdr{Bin: sessionBin(t), Store: st}
	w, err := h.Wake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-first
	if err := st.SetSetting(ctx, store.HerdrSessionSetting, "gc2", "me"); err != nil {
		t.Fatal(err)
	}
	if out, err := h.run(ctx, "agent", "list"); err != nil || !strings.Contains(string(out), SessionSocket("gc2")) {
		t.Fatalf("after the change the herdr program was sent to %s, %v", out, err)
	}
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream stayed with the old session")
	}
	select {
	case <-w: // reconnected: exits may have been missed
	case <-time.After(2 * time.Second):
		t.Fatal("no wake after the stream moved")
	}
}
