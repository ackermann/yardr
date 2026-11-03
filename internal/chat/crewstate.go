package chat

import (
	"fmt"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// How a crew member stands, as the view names it: the state it is listed
// with, whether it waits in its pane for a person, and where mail sent to it
// stands because of both.

// The states a crew member is listed with.
const (
	NotRunning = "not running"
	Working    = "working"
	Idle       = "idle"
	Blocked    = "blocked"
	Failed     = "failed to start"
	// NotLoggedIn is a session at its harness's login screen: the daemon
	// saw it there and Crew says so (LoginScreen), whatever the runtime
	// says the agent does.
	NotLoggedIn = "not logged in"
	// Running is a session of which the daemon has not said more yet, or
	// whose runtime cannot tell.
	Running = "running"
)

// StateOf is the state r is listed with: whether its session runs, and
// what the daemon last saw it do. A crew member whose last start failed is
// failed to start, parked for good or waiting for the next try.
func StateOf(r store.CrewMember) string {
	switch {
	case r.State == "failed" || (!r.Live() && r.Enabled && r.Failures > 0):
		return Failed
	case !r.Live():
		return NotRunning
	case r.LoginScreen:
		return NotLoggedIn
	}
	switch r.Status {
	case "working":
		return Working
	case "idle":
		return Idle
	case "blocked":
		return Blocked
	}
	return Running
}

// loginExe is the command that logs r's harness in: the kind the runner
// started it as (the handle's "kind", as the herdr runner writes it), or
// the kind its config names, and that kind's executable.
func loginExe(r store.CrewMember) (kind, exe string) {
	kind, _ = r.Handle["kind"].(string)
	if kind == "" {
		kind, _ = r.Config["kind"].(string)
	}
	if kind == "" {
		kind = agents.DefaultKind
	}
	return kind, store.Executable(kind)
}

// YardLeadsLogin is what is said of a crew member at its login screen
// whose login the yard leads (LoginLed): the operator answers it at asks,
// with no terminal.
const YardLeadsLogin = "the yard leads its login, and waits for the operator at its asks (yardr asks)"

// PaneKey is the key that goes to the selected crew member's pane, as the
// view names it in a sentence.
const PaneKey = "ctrl+g"

// Waits reports whether r's session waits in its pane for a person: its
// runtime reads it blocked, or its first prompt is still to be delivered
// and it is not working on one (a harness at a login or a dialog takes no
// prompt, and a runtime may read it idle there). Such a crew member reads no
// mail until someone answered it.
func Waits(r store.CrewMember) bool {
	if !r.Live() {
		return false
	}
	return r.Status == "blocked" || (r.Handle[store.PendingPrompt] != nil && r.Status != "working")
}

// MayWaitAt is what a harness that does not go on may wait at, as far as
// yardr can tell without reading its pane. One text for the onboarding's
// diagnosis and the view's notice.
const MayWaitAt = "may wait at a login, a question or a system dialog"

// NotReady is what is known of a session whose harness, of the kind
// named, did not take its first prompt.
func NotReady(kind string) string {
	return fmt.Sprintf("%s was started in the pane but is not ready for its first prompt: it %s", kind, MayWaitAt)
}

// AtPrompt says that the crew member named waits in its pane: the start of
// the line under a mail it cannot read, and of the notice above the input.
func AtPrompt(name string) string { return name + " waits at a prompt in its pane" }

// Known is the one line of what yardr knows about a crew member that waits
// in its pane (Waits).
func Known(r store.CrewMember) string {
	kind, _ := r.Config["kind"].(string)
	if kind == "" {
		kind = "its harness"
	}
	if r.LoginLed {
		return fmt.Sprintf("%s is not logged in: %s; it starts again once the login is made", r.Name, YardLeadsLogin)
	}
	if r.LoginScreen {
		_, exe := loginExe(r)
		return fmt.Sprintf("%s is not logged in: run %s in another terminal and log in there; "+
			"then answer the login in its pane or start it fresh (`yardr crew restart %s`)", r.Name, exe, r.Name)
	}
	if r.Status != "blocked" {
		return NotReady(kind)
	}
	return fmt.Sprintf("%s: %s %s", AtPrompt(r.Name), kind, MayWaitAt)
}

// Standing is where a mail the user sent stands: read, or what its
// receiver's state means for it. r is nil for a receiver the yard no
// longer has, and for one of a linked yard, whose mail stands as its
// delivery there does (Away).
func Standing(l store.Letter, r *store.CrewMember, yardr string) string {
	switch {
	case l.Delivery != "":
		return Away(l, yardr)
	case l.Read:
		return "read"
	case r == nil:
		return "sent"
	}
	n := r.Name
	if r.LoginLed {
		return fmt.Sprintf("%s is not logged in and cannot read mail: %s", n, YardLeadsLogin)
	}
	if r.LoginScreen {
		_, exe := loginExe(*r)
		return fmt.Sprintf("%s is not logged in and cannot read mail (run %s in another terminal and log in there)", n, exe)
	}
	if Waits(*r) {
		return fmt.Sprintf("%s and cannot read mail (%s goes there)", AtPrompt(n), PaneKey)
	}
	switch StateOf(*r) {
	case Working:
		return fmt.Sprintf("sent; %s is working and reads mail when idle", n)
	case Idle:
		return fmt.Sprintf("sent; %s is idle", n)
	case NotRunning:
		return fmt.Sprintf("%s is not running: %s crew start %s", n, yardr, n)
	case Failed:
		return fmt.Sprintf("%s failed to start: %s crew show %s", n, yardr, n)
	}
	return "sent"
}
