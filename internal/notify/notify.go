// Package notify tells a user outside the yard: one message to the person,
// through the source their record names (yardr user set <name>
// notify=<source>). Send is the whole of it for a caller, which decides
// that something is to be told and what, and does not know how it goes.
//
// Two kinds of source can tell a person: a declared one, by the notify key
// of its file (internal/source), and one yardr has in Go, by
// hooks.Notifier. Both are a provider here, and a third way to reach a
// person (a command the operator sets, say) would be one more.
//
// Nothing is written by this package: no log, no event. The address of a
// person and the credential of a source are read from their files for one
// send and are in no error it returns.
package notify

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Message is what is told. Link is "" where there is nothing a person
// could open.
type Message struct {
	Title, Text, Link string
}

// Sent is a send the source answered.
type Sent struct {
	User   string `json:"user"`
	Source string `json:"source"`
	// Status is the source's answer. One that is no success is a Sent too:
	// the caller decides whether it is worth another try.
	Status int           `json:"status"`
	Took   time.Duration `json:"-"`
}

// OK reports whether the source took the message.
func (s *Sent) OK() bool { return s.Status >= 200 && s.Status < 300 }

// NotSetUp is a send that was not tried because of how the user or their
// source is set up: no source named, a source that is gone or cannot tell
// anyone, an env file that is missing, open to others or does not set a
// name. Trying again changes nothing until someone changes that.
type NotSetUp struct{ msg string }

func (e *NotSetUp) Error() string { return e.msg }

func notSetUp(format string, args ...any) error {
	return &NotSetUp{msg: fmt.Sprintf(format, args...)}
}

// Store is what of the yard's store a send reads.
type Store interface {
	GetUser(ctx context.Context, name string) (*store.User, error)
	GetSource(ctx context.Context, name string) (*store.Source, error)
}

// provider is one way a person is reached.
type provider interface {
	// names are what the user's env file must set for it.
	names() []string
	// send tells the person env is of, with env the values of names. An
	// error is a send that was not answered.
	send(ctx context.Context, env map[string]string, m Message) (status int, err error)
}

// Send tells the user m through the source their record names.
func Send(ctx context.Context, st Store, user string, m Message) (*Sent, error) {
	u, err := st.GetUser(ctx, user)
	if err != nil {
		return nil, err
	}
	if u.Notify == "" {
		return nil, notSetUp("user %s names no source that tells them: yardr user set %s %s=<source>", u.Name, u.Name, store.UserNotify)
	}
	return Through(ctx, st, u.Notify, u, m)
}

// Through tells the user m through the source name, whichever their record
// names: what yardr source check <name> --user does, to try a source
// before a user is given it.
func Through(ctx context.Context, st Store, name string, u *store.User, m Message) (*Sent, error) {
	p, err := providerOf(ctx, st, name)
	if err != nil {
		return nil, err
	}
	env, _, err := userEnv(u, p.names())
	if err != nil {
		return nil, err
	}
	start := time.Now()
	status, err := p.send(ctx, env, m)
	if err != nil {
		return nil, err
	}
	return &Sent{User: u.Name, Source: name, Status: status, Took: time.Since(start)}, nil
}

// Names are the names a user's env file sets to be told through the source
// name.
func Names(ctx context.Context, st Store, name string) ([]string, error) {
	p, err := providerOf(ctx, st, name)
	if err != nil {
		return nil, err
	}
	return p.names(), nil
}

// Ready reports whether u could be told as its record stands, without
// sending anything: state says in words what is set where, and never holds
// a value.
func Ready(ctx context.Context, st Store, u *store.User) (state string, err error) {
	if u.Notify == "" {
		return "", notSetUp("user %s names no source that tells them: yardr user set %s %s=<source>", u.Name, u.Name, store.UserNotify)
	}
	p, err := providerOf(ctx, st, u.Notify)
	if err != nil {
		return "", err
	}
	_, state, err = userEnv(u, p.names())
	return state, err
}

func providerOf(ctx context.Context, st Store, name string) (provider, error) {
	// An adapter first: a declared source cannot have the name of one.
	if n, ok := hooks.NotifierFor(name); ok {
		return compiled{n}, nil
	}
	src, err := st.GetSource(ctx, name)
	if err != nil {
		return nil, notSetUp("the source %s is not there to tell anyone (yardr source list): %v", name, err)
	}
	d, err := src.Declaration()
	if err != nil {
		return nil, notSetUp("the file of the source %s is not read by this yardr: yardr source show %s", name, name)
	}
	if d.Notify == nil {
		return nil, notSetUp("the source %s has no notify key: its file names no call that tells a person", name)
	}
	return declared{src: src, d: d}, nil
}

// userEnv reads from the user's env file the values of names, each of
// which it must set.
func userEnv(u *store.User, names []string) (env map[string]string, state string, err error) {
	if u.EnvFile == "" {
		return nil, "", notSetUp("user %s has no %s: yardr user set %s %s=<a file of mode 0600 that sets %s>",
			u.Name, store.UserEnvFile, u.Name, store.UserEnvFile, strings.Join(names, " and "))
	}
	f, err := config.ReadEnvFile(u.EnvFile)
	if err != nil {
		return nil, "", notSetUp("user %s: %v", u.Name, err)
	}
	env = map[string]string{}
	for _, n := range names {
		if env[n] = source.Lookup(f.Env, n); env[n] == "" {
			return nil, "", notSetUp("user %s: env_file %s does not set %s", u.Name, f.Path, n)
		}
	}
	return env, strings.Join(names, " and ") + isAre(names) + "set in " + f.Path, nil
}

func isAre(names []string) string {
	if len(names) == 1 {
		return " is "
	}
	return " are "
}

// declared tells through a declared source: the call its notify key names,
// an update, with the source's own credential for one.
type declared struct {
	src *store.Source
	d   *source.Declaration
}

func (p declared) names() []string { return []string{p.d.Notify.AddressEnv} }

func (p declared) send(ctx context.Context, env map[string]string, m Message) (int, error) {
	cred, _, err := p.d.ReadCredential(p.src.EnvFile, source.Update)
	if err != nil {
		return 0, notSetUp("source %s: %v", p.d.Name, err)
	}
	n := p.d.Notify
	c, _ := p.d.Call(n.Call)
	params := map[string]string{n.Address: env[n.AddressEnv]}
	// set gives value to the parameter name, where the call has one for it.
	// A value there is none of is not given to a parameter that may be left
	// out; a required one gets it empty, which is the call's to refuse.
	set := func(name, value string, cut bool) {
		for _, par := range c.Params {
			if par.Name != name {
				continue
			}
			if par.Max > 0 && utf8.RuneCountInString(value) > par.Max {
				if !cut {
					// Half a link opens nothing, or something else.
					value = ""
				} else {
					value = string([]rune(value)[:par.Max-1]) + "…"
				}
			}
			if value != "" || !par.Optional {
				params[name] = value
			}
		}
	}
	set(n.Title, m.Title, true)
	set(n.Text, m.Text, true)
	set(n.Link, m.Link, false)
	res, err := source.Do(ctx, p.d, n.Call, params, cred)
	if err != nil {
		return 0, err
	}
	return res.Status, nil
}

// compiled tells through a source yardr has in Go.
type compiled struct{ n hooks.Notifier }

func (p compiled) names() []string { return p.n.NotifyEnv() }

func (p compiled) send(ctx context.Context, env map[string]string, m Message) (int, error) {
	return p.n.Notify(ctx, env, m.Title, m.Text, m.Link)
}
