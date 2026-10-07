package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// Where a receiver's address comes from, as Listener.From names it.
const (
	FromVariable = "YARDR_HOOK_ADDR"
	FromSetting  = store.HookAddrSetting
)

// Listener is what a yard's server says about its webhook receiver once it
// has started: the address it was given, where that came from, and why it
// does not listen there when it does not. Only the server knows, so it
// writes this next to its pidfile for doctor and the pack plan to read.
type Listener struct {
	// Addr is the address the server was given; "" when it was given none
	// and runs no receiver.
	Addr string `json:"addr,omitempty"`
	// From is FromVariable or FromSetting; "" without an address.
	From string `json:"from,omitempty"`
	// Error is why the server could not listen on Addr.
	Error string `json:"error,omitempty"`
	// PID is the server that wrote this.
	PID int `json:"pid"`
}

// Listening reports whether the receiver takes deliveries.
func (l Listener) Listening() bool { return l.Addr != "" && l.Error == "" }

// Source is where the address came from, in words.
func (l Listener) Source() string {
	if l.From == FromSetting {
		return "the hook_addr setting"
	}
	return FromVariable + ", set where the server started"
}

// WriteListener records l as the state of this process's receiver, by
// renaming a finished file over the last one, as the pidfile is written.
func WriteListener(c *config.Config, l Listener) error {
	l.PID = os.Getpid()
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := c.HookState() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.HookState()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ReadListener is what the running server said about its receiver; false
// when it has said nothing: there is no state, or it is that of a server
// that has gone (the pidfile names another), or of a build before the
// server wrote it.
func ReadListener(c *config.Config) (Listener, bool) {
	var l Listener
	b, err := os.ReadFile(c.HookState())
	if err != nil || json.Unmarshal(b, &l) != nil {
		return Listener{}, false
	}
	pid, err := os.ReadFile(c.PIDFile())
	if err != nil || strings.TrimSpace(string(pid)) != strconv.Itoa(l.PID) {
		return Listener{}, false
	}
	return l, true
}

// CheckAddr refuses an address a receiver could not listen on: it wants
// host:port with a port number, the host empty for every interface.
func CheckAddr(addr string) error { return store.CheckHookAddr(addr) }

// restart is how a server comes to read its address again, as a command
// for the default home; a caller for another home puts the home in.
const restart = "yardr server stop (the next command starts it again)"

// Listens says whether the yard's server takes webhooks, and on what, from
// what the server said (l, when known) and the hook_addr setting as it is
// now. Without known, nobody can tell: no server of this build has started
// on the home. The text names what to change where the receiver does not
// listen, with commands as they are typed for the default home.
func Listens(l Listener, known bool, setting string) (listening bool, text string) {
	switch {
	case !known:
		return false, "the server has not said what it takes webhooks on"
	case l.Listening():
		text = fmt.Sprintf("the server takes webhooks on %s (%s)", l.Addr, l.Source())
		switch {
		case l.From == FromVariable && setting != "" && setting != l.Addr:
			text += fmt.Sprintf("; the hook_addr setting (%s) is not used while the variable is set there", setting)
		case l.From == FromSetting && setting != l.Addr:
			text += fmt.Sprintf("; hook_addr is %s now, which it takes at its next start: %s", orNone(setting), restart)
		}
		return true, text
	case l.Error != "":
		text = fmt.Sprintf("the server could not listen on %s (%s): %s. Another yard or program may have the address; ", l.Addr, l.Source(), l.Error)
		switch {
		case l.From == FromVariable:
			text += "change the variable where the server starts, or unset it there and give this yard its own with " +
				"yardr settings set hook_addr <host:port>, then " + restart
		case setting != l.Addr:
			text += fmt.Sprintf("hook_addr is %s now, which the server takes at its next start: %s", orNone(setting), restart)
		default:
			text += "give this yard its own with yardr settings set hook_addr <host:port>, then " + restart
		}
		return false, text
	case setting != "":
		return false, fmt.Sprintf("the server takes no webhooks yet: hook_addr is %s, which it takes at its next start: %s", setting, restart)
	}
	return false, "the server has no address to take webhooks on: yardr settings set hook_addr 127.0.0.1:8790, then " + restart
}

func orNone(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}
