package hooks

import (
	"context"

	"yardr.dev/yardr/internal/store"
)

// Telling a person outside the yard is a capability of a source like
// answering in one: a user names the source that tells them of what lands
// in their inbox (yardr user set <name> notify=<source>). A declared source
// has it by the notify key of its file (internal/source); a source yardr
// has in Go has it by this interface. internal/notify sends through either
// and is the one place that knows there are two.

// Notifier is what an Adapter whose source can tell a person adds. No
// adapter has it yet.
type Notifier interface {
	// NotifyEnv are the names a user's env_file sets for the source to
	// reach them: where the person is, and a credential of their own
	// where the source wants one. The names, never the values.
	NotifyEnv() []string
	// Notify tells the person env is of, with env the values of
	// NotifyEnv as their file sets them: title, text and link, "" for one
	// there is none of. It returns the status the source answered with;
	// an error is a call that was not answered, and names no value of env.
	Notify(ctx context.Context, env map[string]string, title, text, link string) (status int, err error)
}

// Imager is what a Notifier whose source takes a picture with the
// message adds.
type Imager interface {
	Notifier
	// NotifyImage tells the person env is of, with env the values of
	// NotifyEnv as their file sets them: title, text and link, "" for
	// one there is none of, with the picture at the local path image,
	// read by the call. It returns the status the source answered with;
	// an error is a call that was not answered, and names no value of
	// env.
	NotifyImage(ctx context.Context, env map[string]string, title, text, link, image string) (status int, err error)
}

// NotifierFor is the source's Notifier, if it can tell a person.
func NotifierFor(source string) (Notifier, bool) {
	n, ok := Adapters[source].(Notifier)
	return n, ok
}

// Notifying lists the sources yardr has in Go that can tell a person,
// sorted.
func Notifying() []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := NotifierFor(s); ok {
			out = append(out, s)
		}
	}
	return out
}

func init() { store.NotifyAdapters(Notifying) }
