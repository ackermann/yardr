package config

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGetenv(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "new")
	if got := Getenv("ACTOR"); got != "new" {
		t.Fatalf("Getenv(ACTOR) = %q, want new", got)
	}
	if got, err := Actor(); err != nil || got != "new" {
		t.Fatalf("Actor = %q, %v, want new", got, err)
	}
	t.Setenv("YARDR_ACTOR", "")
	if got := Getenv("ACTOR"); got != "" {
		t.Fatalf("unset: %q", got)
	}
}

// Without YARDR_ACTOR the actor is the login name: $USER, else the OS
// user's, else "unknown". The --actor flag says so ("default $YARDR_ACTOR,
// else $USER"), and a login name that is nobody's is refused with both
// ways to name one (yardr-sang).
func TestActorFallsBackToLogin(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "kloni")
	if got, err := Actor(); err != nil || got != "kloni" {
		t.Fatalf("USER=kloni: Actor = %q, %v, want kloni", got, err)
	}
	t.Setenv("USER", "")
	if got, err := Actor(); err != nil || got == "" || got == "kloni" {
		t.Fatalf("no USER: Actor = %q, %v", got, err)
	}
}

// yardr-tbm4: with neither YARDR_ACTOR nor USER the actor comes from the
// OS, and under Codex's sandbox that lookup hangs past signals. Actor
// bounds it at 2 s and fails with how to name one instead of hanging; a
// named actor never reaches the lookup.
func TestActorLookupTimeout(t *testing.T) {
	if UserLookupTimeout != 2*time.Second {
		t.Fatalf("UserLookupTimeout = %v, want 2s", UserLookupTimeout)
	}
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "")
	release := make(chan struct{})
	t.Cleanup(func() {
		currentUser = user.Current
		UserLookupTimeout = 2 * time.Second
		close(release)
	})
	currentUser = func() (*user.User, error) {
		<-release
		return nil, errors.New("released")
	}
	UserLookupTimeout = 50 * time.Millisecond
	start := time.Now()
	got, err := Actor()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Actor with a blocked lookup took %v, want the 50ms bound", elapsed)
	}
	want := "no actor: set YARDR_ACTOR or USER; the system's user lookup did not answer"
	if err == nil || err.Error() != want {
		t.Fatalf("Actor with a blocked lookup = %q, %v, want the error", got, err)
	}
}

// A lookup that answers with no user is "unknown", as before the bound;
// LookupUsername hands the caller the lookup's error.
func TestActorLookupError(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "")
	t.Cleanup(func() { currentUser = user.Current })
	lookupErr := errors.New("user: unknown userid 99")
	currentUser = func() (*user.User, error) { return nil, lookupErr }
	if name, err := LookupUsername(currentUser); name != "" || !errors.Is(err, lookupErr) {
		t.Fatalf("LookupUsername = %q, %v, want the lookup's error", name, err)
	}
	if got, err := Actor(); err != nil || got != "unknown" {
		t.Fatalf("Actor = %q, %v, want unknown", got, err)
	}
}

func TestActorNamedSkipsLookup(t *testing.T) {
	t.Cleanup(func() { currentUser = user.Current })
	currentUser = func() (*user.User, error) {
		t.Error("a named actor reached the OS lookup")
		return nil, errors.New("looked up")
	}
	t.Setenv("YARDR_ACTOR", "jo")
	t.Setenv("USER", "")
	if got, err := Actor(); err != nil || got != "jo" {
		t.Fatalf("YARDR_ACTOR=jo: Actor = %q, %v", got, err)
	}
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("USER", "kloni")
	if got, err := Actor(); err != nil || got != "kloni" {
		t.Fatalf("USER=kloni: Actor = %q, %v", got, err)
	}
}

func TestEnv(t *testing.T) {
	if got := Env("BEAD", "web-3"); !slices.Equal(got, []string{"YARDR_BEAD=web-3"}) {
		t.Fatalf("Env = %v", got)
	}
}

func TestIsVar(t *testing.T) {
	for key, want := range map[string]bool{"YARDR_BEAD": true, "YARDS_BEAD": false, "YARDR_SESSION": false,
		"BEAD": false, "YARDR_": false, "PATH": false} {
		if got := IsVar(key, "BEAD", "HOME"); got != want {
			t.Errorf("IsVar(%q, BEAD, HOME) = %v", key, got)
		}
	}
	if !IsVar("YARDR_ANYTHING") || IsVar("YARDS_X") {
		t.Error("IsVar with no names: any yardr variable")
	}
}

func TestDefaultHome(t *testing.T) {
	u := t.TempDir()
	if got := DefaultHome(u); got != filepath.Join(u, ".yardr") {
		t.Fatalf("neither exists: %s", got)
	}
	if err := os.Mkdir(filepath.Join(u, ".yard"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := DefaultHome(u); got != filepath.Join(u, ".yardr") {
		t.Fatalf("only ~/.yard exists: %s", got)
	}
}

func TestLoadHome(t *testing.T) {
	u := t.TempDir()
	t.Setenv("HOME", u)
	t.Setenv("YARDR_HOME", filepath.Join(u, "new"))
	c, err := Load()
	if err != nil || c.Home != filepath.Join(u, "new") {
		t.Fatalf("YARDR_HOME: %+v %v", c, err)
	}

	// The home yard had is not read: a ~/.yard left behind is not the yard.
	t.Setenv("YARDR_HOME", "")
	os.Mkdir(filepath.Join(u, ".yard"), 0o700)
	if c, err = Load(); err != nil || c.Home != filepath.Join(u, ".yardr") {
		t.Fatalf("default home: %+v %v", c, err)
	}
}

// LoadWith, LoadReadOnlyWith and ActorWith resolve from a lookup instead
// of the process: two maps name two homes and two actors, one actor by
// YARDR_ACTOR and the other by USER, without touching the process
// environment. LoadWith still creates the home it resolves.
func TestWithResolvesFromMaps(t *testing.T) {
	// A short directory: the socket path must fit MaxSocket.
	base, err := os.MkdirTemp("", "ycw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	homes := []string{filepath.Join(base, "h1"), filepath.Join(base, "h2")}
	maps := []map[string]string{
		{"YARDR_HOME": homes[0], "YARDR_ACTOR": "ada", "YARDR_WEB_ADDR": "127.0.0.1:9101"},
		{"YARDR_HOME": homes[1], "USER": "kloni", "YARDR_BDP_ADDR": "127.0.0.1:9102"},
	}
	for i, m := range maps {
		short := func(name string) string { return m[prefix+name] }
		c, err := LoadWith(short)
		if err != nil || c.Home != homes[i] {
			t.Fatalf("map %d: LoadWith = %+v, %v; want %s", i, c, err, homes[i])
		}
		if st, err := os.Stat(homes[i]); err != nil || !st.IsDir() {
			t.Fatalf("map %d: the resolved home was not created: %v", i, err)
		}
		full := func(key string) string { return m[key] }
		got, err := ActorWith(full)
		if err != nil {
			t.Fatalf("map %d: ActorWith: %v", i, err)
		}
		want := []string{"ada", "kloni"}[i]
		if got != want {
			t.Fatalf("map %d: ActorWith = %q, want %q", i, got, want)
		}
	}
	if c, err := LoadWith(func(name string) string { return maps[0][prefix+name] }); err != nil || c.WebAddr != "127.0.0.1:9101" {
		t.Fatalf("address override from the map: %+v, %v", c, err)
	}
	if c, err := LoadWith(func(name string) string { return maps[1][prefix+name] }); err != nil || c.BDPAddr != "127.0.0.1:9102" {
		t.Fatalf("address override from the map: %+v, %v", c, err)
	}
}

// LoadReadOnlyWith resolves two homes from two maps and, as
// LoadReadOnly, creates neither.
func TestLoadReadOnlyWithCreatesNothing(t *testing.T) {
	base, err := os.MkdirTemp("", "ycr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	for i, home := range []string{filepath.Join(base, "r1"), filepath.Join(base, "r2")} {
		m := map[string]string{"YARDR_HOME": home}
		c, err := LoadReadOnlyWith(func(name string) string { return m[prefix+name] })
		if err != nil || c.Home != home {
			t.Fatalf("map %d: LoadReadOnlyWith = %+v, %v; want %s", i, c, err, home)
		}
		if _, err := os.Stat(home); err == nil {
			t.Fatalf("map %d: the read-only home was created", i)
		}
	}
}

// LoadReadOnlyWith takes only YARDR_* values from get: an empty HOME
// still defaults from the process's user home, and a relative HOME is
// still resolved from its working directory.
func TestLoadReadOnlyWithUsesProcessHomeAndDirectory(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	get := func(string) string { return "" }
	c, err := LoadReadOnlyWith(get)
	want, absErr := filepath.Abs(DefaultHome(userHome))
	if err != nil || absErr != nil || c.Home != want {
		t.Fatalf("empty HOME: LoadReadOnlyWith = %+v, %v; want %s (abs: %v)", c, err, want, absErr)
	}

	relativeHome := "yardr-config-relative-home"
	c, err = LoadReadOnlyWith(func(name string) string {
		if name == "HOME" {
			return relativeHome
		}
		return ""
	})
	want, absErr = filepath.Abs(relativeHome)
	if err != nil || absErr != nil || c.Home != want {
		t.Fatalf("relative HOME: LoadReadOnlyWith = %+v, %v; want %s (abs: %v)", c, err, want, absErr)
	}
}

// LoadReadOnly resolves the home as Load does but creates nothing, neither
// a named home nor the default one: for commands that must leave a home
// with no yard as they found it.
func TestLoadReadOnlyCreatesNothing(t *testing.T) {
	// A short directory: the socket path must fit MaxSocket.
	u, err := os.MkdirTemp("", "ycr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(u) })
	t.Setenv("HOME", u)
	t.Setenv("YARDR_HOME", filepath.Join(u, "new"))
	c, err := LoadReadOnly()
	if err != nil || c.Home != filepath.Join(u, "new") {
		t.Fatalf("YARDR_HOME: %+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(u, "new")); err == nil {
		t.Fatal("the named home was created")
	}
	t.Setenv("YARDR_HOME", "")
	if c, err = LoadReadOnly(); err != nil || c.Home != filepath.Join(u, ".yardr") {
		t.Fatalf("default home: %+v %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(u, ".yardr")); err == nil {
		t.Fatal("the default home was created")
	}
}

func TestLoadServiceAddressOverrides(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "yardr-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("YARDR_HOME", filepath.Join(dir, "home"))
	t.Setenv("YARDR_WEB_ADDR", "127.0.0.1:9001")
	t.Setenv("YARDR_BDP_ADDR", "127.0.0.1:9002")
	t.Setenv("YARDR_WEB_PAGES_ADDR", "127.0.0.1:9003")
	t.Setenv("YARDR_WEB_PAGES_HOST", "pages.example.net")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebAddr != "127.0.0.1:9001" || c.BDPAddr != "127.0.0.1:9002" {
		t.Fatalf("Load service address overrides = web %q, bdp %q", c.WebAddr, c.BDPAddr)
	}
	if c.WebPagesAddr != "127.0.0.1:9003" || c.WebPagesHost != "pages.example.net" {
		t.Fatalf("Load sandbox origin overrides = addr %q, host %q", c.WebPagesAddr, c.WebPagesHost)
	}
}

func TestParseRetention(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour, "off": 0, "0": 0, "0d": 0,
	} {
		if got, err := ParseRetention(in); err != nil || got != want {
			t.Errorf("ParseRetention(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "-1d", "-5h", "1.5d"} {
		if _, err := ParseRetention(in); err == nil {
			t.Errorf("ParseRetention(%q) accepted", in)
		}
	}
	t.Setenv("YARDR_EVENT_RETENTION", "")
	if got, err := Retention("EVENT_RETENTION", time.Hour); err != nil || got != time.Hour {
		t.Fatalf("unset: %v %v", got, err)
	}
	t.Setenv("YARDR_EVENT_RETENTION", "7d")
	if got, err := Retention("EVENT_RETENTION", time.Hour); err != nil || got != 7*24*time.Hour {
		t.Fatalf("7d: %v %v", got, err)
	}
	t.Setenv("YARDR_EVENT_RETENTION", "soon")
	if _, err := Retention("EVENT_RETENTION", time.Hour); err == nil {
		t.Fatal("bad value accepted")
	}
}

// A home whose socket would not fit a unix socket path is refused, and
// nothing is created for it.
// A relative YARDR_HOME is made absolute at load, once: the server, the
// agents and the scripts are started with it and run elsewhere, where the
// relative path would name another directory (or none: exit 2).
func TestLoadMakesHomeAbsolute(t *testing.T) {
	// A short directory: the absolute socket path must fit MaxSocket.
	dir, err := os.MkdirTemp("", "ych")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(dir)
	t.Setenv("YARDR_HOME", "y1/")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "y1"); c.Home != want {
		t.Fatalf("Home = %q, want %q", c.Home, want)
	}
	if st, err := os.Stat(filepath.Join(dir, "y1")); err != nil || !st.IsDir() {
		t.Fatalf("home not created where it was named: %v", err)
	}
	if !filepath.IsAbs(c.Socket()) {
		t.Fatalf("Socket = %q", c.Socket())
	}
}

func TestLoadRefusesALongHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), strings.Repeat("h", MaxSocket))
	t.Setenv("YARDR_HOME", home)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), "YARDR_HOME") || !strings.Contains(err.Error(), "--home") {
		t.Fatalf("Load = %v", err)
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("the refused home was created")
	}
	// Exactly at the bound is fine.
	short := t.TempDir()
	home = short + "/" + strings.Repeat("h", MaxSocket-len(short)-len("/yard.sock")-1)
	t.Setenv("YARDR_HOME", home)
	if c, err := Load(); err != nil || len(c.Socket()) != MaxSocket {
		t.Fatalf("Load at the bound: %v %v", c, err)
	}
}

// The default home is compared resolved: a trailing slash and a symlink to
// it count (yard-0wys). The ~/.yard it was before the rename does not.
func TestIsDefaultHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	legacy := filepath.Join(user, ".yard")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(user, ".yardr")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{home, home + "/", link, filepath.Join(user, "x", "..", ".yardr")} {
		if !IsDefaultHome(h) {
			t.Errorf("%s is not the default home", h)
		}
	}
	for _, h := range []string{legacy, filepath.Join(t.TempDir(), "y3"), "/tmp/y3"} {
		if IsDefaultHome(h) {
			t.Errorf("%s is the default home", h)
		}
	}
}

// UseHome, the --home flag, wins over the variable, is
// made absolute once, and is what a child's environment then says.
func TestUseHome(t *testing.T) {
	dir, err := os.MkdirTemp("", "ych")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Chdir(dir)
	t.Setenv("YARDR_HOME", filepath.Join(dir, "variable"))
	if err := UseHome("y1/"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "y1")
	if os.Getenv("YARDR_HOME") != want {
		t.Fatalf("YARDR_HOME=%s, want %s", os.Getenv("YARDR_HOME"), want)
	}
	if c, err := Load(); err != nil || c.Home != want {
		t.Fatalf("Load = %+v, %v; want %s", c, err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "variable")); err == nil {
		t.Fatal("the variable's home was created")
	}
	if err := UseHome(""); err == nil || os.Getenv("YARDR_HOME") != want {
		t.Fatalf("UseHome(\"\") = %v, home %s", err, os.Getenv("YARDR_HOME"))
	}
}

// What a yard starts for work is started without the settings of the
// yard's server, and with everything else it had;
// where the environment can only be added to, they are set to nothing,
// which Getenv reads as unset.
func TestWithoutServerSettings(t *testing.T) {
	environ := []string{"PATH=/bin", "YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_WEB_ADDR=127.0.0.1:2", "YARDR_WEB_AS=jo", "YARDR_BDP_ADDR=127.0.0.1:3", "YARDR_BEAD=web-3",
		"YARDR_EVENT_RETENTION=1d", "YARDR_HOOK_PAYLOAD_RETENTION=3d", "YARDR_WEB_PAGES_ADDR=127.0.0.1:4", "YARDR_WEB_PAGES_HOST=pages.example.net",
		"YARDR_INBOX_RETENTION=5d", "YARDR_HOOK_ADDRESS=x"}
	before := slices.Clone(environ)
	got := WithoutServerSettings(environ)
	if want := []string{"PATH=/bin", "YARDR_BEAD=web-3", "YARDR_INBOX_RETENTION=5d", "YARDR_HOOK_ADDRESS=x"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutServerSettings = %v, want %v", got, want)
	}
	if !slices.Equal(environ, before) {
		t.Fatalf("the caller's environ was changed: %v", environ)
	}

	blank := BlankServerSettings()
	if len(blank) != len(ServerSettings) {
		t.Fatalf("BlankServerSettings = %v", blank)
	}
	for _, kv := range blank {
		k, v, _ := strings.Cut(kv, "=")
		if !IsVar(k, ServerSettings...) || v != "" {
			t.Errorf("BlankServerSettings has %q", kv)
		}
	}
	for _, name := range ServerSettings {
		t.Setenv(prefix+name, "")
		if got := Getenv(name); got != "" {
			t.Errorf("a blank %s reads as %q", name, got)
		}
	}
}

// DaemonPID is the pid the lock file names while it is held, and DaemonStart
// the start time beside it; an unheld lock, and a file from before starts
// were recorded, say none.
func TestDaemonPIDAndStart(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")
	if DaemonRunning(lock) || DaemonPID(lock) != 0 {
		t.Fatal("a missing lock reads as held")
	}
	if _, ok := DaemonStart(lock); ok {
		t.Fatal("a missing lock reads a start time")
	}
	f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := f.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte(s), 0); err != nil {
			t.Fatal(err)
		}
	}
	write("123 456\n")
	if !DaemonRunning(lock) || DaemonPID(lock) != 123 {
		t.Fatalf("held: running %v pid %d", DaemonRunning(lock), DaemonPID(lock))
	}
	if start, ok := DaemonStart(lock); !ok || start != 456 {
		t.Fatalf("held: start %d, %v", start, ok)
	}
	write("123\n")
	if DaemonPID(lock) != 123 {
		t.Fatalf("no start recorded: pid %d", DaemonPID(lock))
	}
	if _, ok := DaemonStart(lock); ok {
		t.Fatal("no start recorded: a start time reads")
	}
	write("123 bogus\n")
	if _, ok := DaemonStart(lock); ok {
		t.Fatal("a bogus start time reads")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if DaemonRunning(lock) || DaemonPID(lock) != 0 {
		t.Fatal("a released lock reads as held")
	}
	if _, ok := DaemonStart(lock); ok {
		t.Fatal("a released lock reads a start time")
	}
}
