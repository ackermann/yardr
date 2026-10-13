package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// yardSession is the herdr session the yard names (its herdr_session
// setting), as UseSession was last told; "" while the yard names none.
// store is the yard's, for a process that was given one (FollowSetting),
// given is how often it was, and read is when the setting was last read
// from it.
var yardSession struct {
	sync.RWMutex
	name  string
	store store.API
	given int
	read  time.Time
}

// UseSession sends every call this process makes to herdr, the API's and
// the program's, to the herdr session name; "" to the one the process's
// environment names, as before the yard could name one. A process of the
// yard is told what the yard's herdr_session setting says (FollowSetting,
// Herdr.followSetting).
func UseSession(name string) {
	yardSession.Lock()
	defer yardSession.Unlock()
	yardSession.name = name
}

// FollowSetting has this process's calls to herdr go where the
// herdr_session setting of st's yard says, read at the process's next call
// to herdr and again once it is settingAge old: a command is run from any
// shell, in any herdr session or none, and the panes it asks herdr about
// are the yard's. A command that never calls herdr never reads it. Until it
// is read the process is in no session the yard named: st may be another
// yard's than the last.
func FollowSetting(st store.API) {
	yardSession.Lock()
	defer yardSession.Unlock()
	yardSession.store, yardSession.name, yardSession.read = st, "", time.Time{}
	yardSession.given++
}

// settingAge is how long a process that follows a yard's setting
// (FollowSetting) goes by what it read: a command's whole life, and little
// of a view's that stays open.
const settingAge = 5 * time.Second

// followYard reads the setting of the yard this process follows, if it
// follows one and what it read is old.
func followYard(ctx context.Context) error {
	yardSession.RLock()
	st, given, fresh := yardSession.store, yardSession.given, time.Since(yardSession.read) < settingAge
	yardSession.RUnlock()
	if st == nil || fresh {
		return nil
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return dispatch.Runtime(fmt.Errorf("herdr: the yard's %s setting could not be read: %w", store.HerdrSessionSetting, err))
	}
	yardSession.Lock()
	defer yardSession.Unlock()
	if yardSession.given == given {
		yardSession.name, yardSession.read = all[store.HerdrSessionSetting], time.Now()
	}
	return nil
}

func usedSession() string {
	yardSession.RLock()
	defer yardSession.RUnlock()
	return yardSession.name
}

// herdrConfigDir is where herdr keeps its sessions.
func herdrConfigDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "herdr")
}

// SessionSocket is the API socket of the herdr session name, where herdr
// 0.9 puts it (herdr session list): the session named default has herdr's
// first socket, every other its own directory under sessions.
func SessionSocket(name string) string {
	if name == "" || name == "default" {
		return filepath.Join(herdrConfigDir(), "herdr.sock")
	}
	return filepath.Join(herdrConfigDir(), "sessions", name, "herdr.sock")
}

// Where the herdr session a process uses is from (HerdrSession.From).
const (
	SessionSet       = "set"       // the yard's herdr_session setting
	SessionInherited = "inherited" // the environment of the process
)

// HerdrSession is the herdr session a process's calls go to.
type HerdrSession struct {
	// Name is "" for an inherited socket whose session the environment
	// does not name: HERDR_SOCKET_PATH without HERDR_SESSION, away from
	// the default's.
	Name   string `json:"name,omitempty"`
	Socket string `json:"socket"`
	From   string `json:"from"`
}

// String is the session in words, for a report.
func (s HerdrSession) String() string {
	from := "this shell's, since the yard's " + store.HerdrSessionSetting + " setting is unset"
	if s.From == SessionSet {
		from = "the yard's " + store.HerdrSessionSetting + " setting"
	}
	name := "session " + s.Name
	if s.Name == "" {
		name = "a session HERDR_SOCKET_PATH names"
	}
	return fmt.Sprintf("%s, socket %s (%s)", name, s.Socket, from)
}

// SessionInUse is the herdr session this process's calls go to now: the
// one the yard names, as last read or told (UseSession), else what its
// environment says, by herdr's own order (HERDR_SOCKET_PATH before
// HERDR_SESSION before the default).
func SessionInUse() HerdrSession {
	if name := usedSession(); name != "" {
		return HerdrSession{Name: name, Socket: SessionSocket(name), From: SessionSet}
	}
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		// Herdr 0.9 selects HERDR_SOCKET_PATH before HERDR_SESSION. When
		// both are set but disagree, report the session the socket reaches.
		return HerdrSession{Name: sessionForSocket(p), Socket: p, From: SessionInherited}
	}
	name := os.Getenv("HERDR_SESSION")
	if name == "" {
		name = "default"
	}
	return HerdrSession{Name: name, Socket: SessionSocket(name), From: SessionInherited}
}

// sessionForSocket identifies sockets in herdr's standard layout. A custom
// socket path has no session name yardr can infer, so its report leaves the
// name empty rather than trusting HERDR_SESSION, which herdr ignores when a
// socket path is set.
func sessionForSocket(socket string) string {
	clean := filepath.Clean(socket)
	if clean == filepath.Clean(SessionSocket("")) {
		return "default"
	}
	rel, err := filepath.Rel(filepath.Join(herdrConfigDir(), "sessions"), clean)
	if err != nil || filepath.Dir(rel) == "." || filepath.Base(rel) != "herdr.sock" {
		return ""
	}
	name := filepath.Dir(rel)
	if strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		return ""
	}
	for _, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return ""
		}
	}
	return name
}

// HerdrCommand is the herdr program bin with args, sent to the session the
// yard names when it names one: by the session's socket, the one way the
// API is sent there too (SocketPath), which herdr takes before the
// HERDR_SESSION of the shell this process was started from. Every run of
// the herdr program is made here. A session herdr does not have is not
// made by it: herdr creates one only when it is attached. A setting that
// cannot be read is the command's error when it is run.
func HerdrCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	err := followYard(ctx)
	cmd := herdrCommand(ctx, bin, args...)
	if err != nil {
		cmd.Err = err
	}
	return cmd
}

// herdrCommand is HerdrCommand for a caller that has read the setting.
func herdrCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	if name := usedSession(); name != "" {
		cmd.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+SessionSocket(name), "HERDR_SESSION="+name)
	}
	return cmd
}

// sessionDown is err, herdr not answering, with the session the yard names
// in front: herdr's own words name a socket, and `herdr` as what starts it,
// which starts the default session and not the yard's.
func sessionDown(err error) error {
	if name := usedSession(); name != "" {
		return fmt.Errorf("herdr session %q, which the yard's %s setting names, is not running or herdr has none of that name "+
			"(herdr session list; herdr session attach %s starts it): %w", name, store.HerdrSessionSetting, name, err)
	}
	return err
}

// followSetting has this process use the herdr session the yard's setting
// names now, read from the store h was given before every call: a daemon
// outlives a change of the setting, and is started from whatever shell.
// Without a store (the CLI's own short calls) it is the yard the process
// follows (FollowSetting), if any. A setting that cannot be read fails the
// call as herdr being down does: the session of the environment may be
// another one, in which every pane of the yard would look gone.
func (h *Herdr) followSetting(ctx context.Context) error {
	if h.Store == nil {
		return followYard(ctx)
	}
	all, err := h.Store.Settings(ctx)
	if err != nil {
		return dispatch.Runtime(fmt.Errorf("herdr: the yard's %s setting could not be read: %w", store.HerdrSessionSetting, err))
	}
	UseSession(all[store.HerdrSessionSetting])
	return nil
}

// sessionCheck is how often an open event stream asks whether the yard
// still names the session it is open to; tests shorten it.
var sessionCheck = 30 * time.Second

// leaveOnChange ends the event stream to the socket at path, through
// leave, once the yard names another session: herdr keeps the old one's
// stream open, and nothing of the yard's would ever be told on it. The
// interval is passed in: the caller reads sessionCheck before the goroutine
// starts, so no goroutine started by a test reads the global after the
// test's cleanup restores it.
func (h *Herdr) leaveOnChange(ctx context.Context, path string, interval time.Duration, leave func()) {
	if h.Store == nil || h.Socket != "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if h.followSetting(ctx) == nil && h.SocketPath() != path {
				leave()
				return
			}
		}
	}
}
