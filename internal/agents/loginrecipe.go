package agents

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// LoginRecipe is how a program leads a harness's login for a person who has
// no terminal: the harness's own login command, the ways to log in it has
// flags for, and the lines it prints while it waits (yardr harness login
// <kind> --json plays it; docs/doctor.md, "A harness's login led by a
// program"). The command runs on pipes, not in a terminal: a recipe is for
// a login command that prints its lines and reads its answer without one.
//
// A recipe is read from what one version of the harness printed, and the
// harness may print something else tomorrow: a pattern that matches nothing
// then leaves the login waiting, and the recipe is read again by a bead
// that says which version it was read from (Verified).
type LoginRecipe struct {
	// Verified is the command the recipe was read from, with the harness's
	// version, and the day it was run.
	Verified Verified `yaml:"verified" json:"verified"`
	// Argv are the arguments the harness is started with to log in.
	Argv []string `yaml:"argv" json:"argv"`
	// Methods are the ways to log in, each the arguments added to Argv for
	// it: what a harness that asks for the method in a menu on its login
	// screen takes as a flag on its login command. With none the command is
	// run as Argv has it.
	Methods []LoginMethod `yaml:"methods" json:"methods,omitempty"`
	// OpenURL is a regular expression a whole line the command prints
	// matches, without the white space around it, when it names the page to
	// sign in on: its one group is the address.
	OpenURL string `yaml:"open_url" json:"open_url"`
	// Input is a regular expression the line the command waits on matches,
	// which need not have ended: what the page gave the person is typed
	// there, as one line. "" for a login that takes nothing back.
	Input string `yaml:"input" json:"input,omitempty"`

	openURL, input *regexp.Regexp
}

// LoginMethod is one way to log in: what a person is offered, and the
// arguments the login command takes for it.
type LoginMethod struct {
	Label string   `yaml:"label" json:"label"`
	Args  []string `yaml:"args" json:"args"`
}

// check finds what r says that a login could not be led by, and compiles
// its patterns.
func (r *LoginRecipe) check() error {
	if r.Verified.From == "" {
		return fmt.Errorf("verified.from: the command and version this was read from")
	}
	if _, err := time.Parse(time.DateOnly, r.Verified.On); err != nil {
		return fmt.Errorf("verified.on %q is no day (2006-01-02)", r.Verified.On)
	}
	if len(r.Argv) == 0 {
		return fmt.Errorf("no argv: the harness's own interface is no login command")
	}
	seen := map[string]bool{}
	for _, m := range r.Methods {
		if m.Label == "" || len(m.Args) == 0 || seen[m.Label] {
			return fmt.Errorf("methods: one without label or args, or a label twice (%q)", m.Label)
		}
		seen[m.Label] = true
	}
	re, err := regexp.Compile(r.OpenURL)
	if err != nil || r.OpenURL == "" || re.NumSubexp() != 1 {
		return fmt.Errorf("open_url: pattern %q needs one group, the address: %v", r.OpenURL, err)
	}
	r.openURL = re
	if r.Input != "" {
		if r.input, err = regexp.Compile(r.Input); err != nil {
			return fmt.Errorf("input: pattern %q: %v", r.Input, err)
		}
	}
	return nil
}

// Method is the method of label, if r has it.
func (r *LoginRecipe) Method(label string) (LoginMethod, bool) {
	for _, m := range r.Methods {
		if m.Label == label {
			return m, true
		}
	}
	return LoginMethod{}, false
}

// Page is the address to sign in on that line names, "" when it names
// none. Only an https address is one: what a program opens for a person is
// never anything else a harness printed.
func (r *LoginRecipe) Page(line string) string {
	m := r.openURL.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return ""
	}
	if u, err := url.Parse(m[1]); err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return m[1]
}

// Waits reports whether line is the one the login command takes its input
// on.
func (r *LoginRecipe) Waits(line string) bool {
	return r.input != nil && r.input.MatchString(strings.TrimSpace(line))
}
