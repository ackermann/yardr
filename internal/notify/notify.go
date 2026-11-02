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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Message is what is told. Link is "" where there is nothing a person
// could open, and Image the local path of the picture it carries, "" for
// a message without one. A message carries one picture at most.
type Message struct {
	Title, Text, Link string
	Image             string
}

// Sent is a send the source answered.
type Sent struct {
	User   string `json:"user"`
	Source string `json:"source"`
	// Status is the source's answer. One that is no success is a Sent too:
	// the caller decides whether it is worth another try.
	Status int `json:"status"`
	// Unpictured is why the message went without the picture it carried,
	// when the source did not take the picture and the words were told on
	// their own; "" when the picture went, and when there was none.
	Unpictured string        `json:"unpictured,omitempty"`
	Took       time.Duration `json:"-"`
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
	// send tells the person env is of, with env the values of names. A
	// message with a picture goes with it where the source takes one,
	// and without where it takes none. An error is a send that was not
	// answered.
	send(ctx context.Context, home string, env map[string]string, m Message) (status int, err error)
	// pictured reports whether send carries a message's picture, which
	// only a source that takes one does.
	pictured() bool
}

// Send tells the user m through the source their record names. home is
// the yard's home, which a relative notify image command runs from.
func Send(ctx context.Context, st Store, home, user string, m Message) (*Sent, error) {
	u, err := st.GetUser(ctx, user)
	if err != nil {
		return nil, err
	}
	if u.Notify == "" {
		return nil, notSetUp("user %s names no source that tells them: yardr user set %s %s=<source>", u.Name, u.Name, store.UserNotify)
	}
	return Through(ctx, st, home, u.Notify, u, m)
}

// fallbackTimeout is the time the words alone get when the picture they
// carried was not taken. It is a budget of their own, since the picture may
// have used all of the caller's. The daemon, which sends the pictures, gives
// a tell PushTimeout (the Dispatcher's, in internal/dispatch), 10 seconds
// too by default; this package cannot name it, since dispatch imports this
// one.
const fallbackTimeout = 10 * time.Second

// Through tells the user m through the source name, whichever their record
// names: what yardr source check <name> --user does, to try a source
// before a user is given it. home is the yard's home, which a relative
// notify image command runs from.
func Through(ctx context.Context, st Store, home, name string, u *store.User, m Message) (*Sent, error) {
	p, err := providerOf(ctx, st, name)
	if err != nil {
		return nil, err
	}
	env, _, err := userEnv(u, name, p.names())
	if err != nil {
		return nil, err
	}
	start := time.Now()
	status, err := p.send(ctx, home, env, m)
	var unpictured string
	if m.Image != "" && p.pictured() && (err != nil || status < 200 || status >= 300) {
		// The picture is an addition to the words, and one the source will
		// not take (too large, too tall, a kind it cannot show) must not
		// cost them, nor have them tried again with it until they are
		// given up: they go on their own, as they went before pictures
		// were told, and the Sent says why.
		if errors.Is(ctx.Err(), context.Canceled) {
			// The caller gave up on the send (the daemon is stopping). The
			// words would go, but the daemon could not record that, and
			// they would go again after the restart.
			return nil, ctx.Err()
		}
		if err != nil {
			unpictured = err.Error()
		} else {
			unpictured = fmt.Sprintf("the source answered %d", status)
		}
		m.Image = ""
		// A picture that is slow rather than refused can use all of ctx.
		// The words would then start past its deadline and fail at once,
		// and the push would try again, slow picture first, until it is
		// given up. They get a budget of their own.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fallbackTimeout)
		status, err = p.send(fctx, home, env, m)
		cancel()
	}
	if err != nil {
		return nil, err
	}
	return &Sent{User: u.Name, Source: name, Status: status, Unpictured: unpictured, Took: time.Since(start)}, nil
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
	_, state, err = userEnv(u, u.Notify, p.names())
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
// which the source needs of them and it must set.
func userEnv(u *store.User, through string, names []string) (env map[string]string, state string, err error) {
	if u.EnvFile == "" {
		// The command that writes the file, so that nobody does by hand.
		return nil, "", notSetUp("user %s has no %s: yardr user key %s %s gives %s",
			u.Name, store.UserEnvFile, u.Name, through, strings.Join(names, " and "))
	}
	f, err := store.ReadEnvFile(u.EnvFile)
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

func (p declared) pictured() bool { return len(p.d.Notify.Image) > 0 }

func (p declared) send(ctx context.Context, home string, env map[string]string, m Message) (int, error) {
	// The telling goes to the source's chosen base, the call and the
	// image command alike.
	p.d = p.d.WithBase(p.src.Base)
	// The source's own credential goes only to the host it was given for. A
	// telling that would send it elsewhere is not tried, and the image command
	// is not run with it: it is reached from here, after the credential is
	// read. The caller records it as a source that is not set up, as it does
	// for a credential that is not.
	if held := p.src.CredentialHeld(p.d); held != "" {
		return 0, notSetUp("source %s: %s", p.d.Name, held)
	}
	cred, _, err := p.d.ReadCredential(ctx, p.src.EnvFile, p.src.Mint, source.Update, store.ReadEnvFileEntries)
	if err != nil {
		return 0, notSetUp("source %s: %v", p.d.Name, err)
	}
	if m.Image != "" && p.pictured() {
		return p.sendImage(ctx, home, env, m, cred)
	}
	n := p.d.Notify
	res, err := source.Do(ctx, p.d, n.Call, p.d.TellParams(env[n.AddressEnv], m.Title, m.Text, m.Link), cred, store.ReadSecretFile)
	if err != nil {
		return 0, err
	}
	return res.Status, nil
}

// sendImage tells with the picture m carries, by the notify key's image
// command: in {address, title, text, image}, out {status}. Telling sends
// JSON, which carries no picture, so the command reads the file at image
// and sends it with the words. A message without a picture, and a file
// without the key, go through the call above, unadorned.
func (p declared) sendImage(ctx context.Context, home string, env map[string]string, m Message, cred string) (int, error) {
	n := p.d.Notify
	in := map[string]any{"address": env[n.AddressEnv], "title": m.Title, "text": m.Text, "image": m.Image}
	argv := slices.Clone(n.Image)
	argv[0] = source.Command{Key: "notify.image", Argv: n.Image}.Program(home, p.d.Name)
	var status int
	err := source.Run(ctx, "notify.image of source "+p.d.Name, argv, in, p.d.CommandEnvOf(source.Update, cred), home, 0, func(out []byte) error {
		var o struct {
			Status int `json:"status"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {status}: %v", err)
		}
		if o.Status == 0 {
			return fmt.Errorf("its answer is no {status}")
		}
		status = o.Status
		return nil
	})
	if err != nil {
		return 0, err
	}
	return status, nil
}

// compiled tells through a source yardr has in Go.
type compiled struct{ n hooks.Notifier }

func (p compiled) names() []string { return p.n.NotifyEnv() }

func (p compiled) pictured() bool {
	_, ok := p.n.(hooks.Imager)
	return ok
}

func (p compiled) send(ctx context.Context, _ string, env map[string]string, m Message) (int, error) {
	if m.Image != "" {
		if im, ok := p.n.(hooks.Imager); ok {
			return im.NotifyImage(ctx, env, m.Title, m.Text, m.Link, m.Image)
		}
	}
	return p.n.Notify(ctx, env, m.Title, m.Text, m.Link)
}
