package store

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// The settings of a user: the source that tells them outside the yard of
// what lands in their inbox, and the file their own address in it is read
// from.
const (
	UserNotify  = "notify"
	UserEnvFile = "env_file"
)

// notifyAdapters lists the sources yardr has in Go that can tell a user.
// Until NotifyAdapters sets it there is none.
var notifyAdapters = func() []string { return nil }

// NotifyAdapters sets the function that lists the sources yardr has in Go
// that can tell a user outside the yard, so that a user's notify takes one
// as it takes a declared source with a notify key. They are in
// internal/hooks, which registers the function from its init, as it does
// IssueForm.
func NotifyAdapters(f func() []string) { notifyAdapters = f }

// notifySources are the sources a user can be told through, sorted: the
// declared ones whose file has a notify key, and the adapters that can.
func notifySources(tx *bolt.Tx) ([]string, error) {
	out := slices.Clone(notifyAdapters())
	err := each(tx, bSources, "", func(_ []byte, src *Source) error {
		if d, err := src.Declaration(); err == nil && d.Notify != nil {
			out = append(out, src.Name)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// mayTell refuses a change of where the user name is told outside the yard
// by anyone but that user and the operator. What lands in a person's inbox
// goes where these settings point, so whoever sets them can send it out of
// the yard, to an address of their own. yardr checks nobody's name, so who
// asks is told by the store's own records before the caller's word, as for
// a declared source (maySource): a call from the pane of a bead's session
// or of a crew member is that session's or that crew member's, whatever
// actor it names.
func (w *wtx) mayTell(ctx context.Context, name, actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	why := "where " + name + " is told outside the yard is set by " + name
	if operator != "" && operator != name {
		why += ", or by the operator (" + operator + ")"
	}
	why += ": what lands in their inbox goes there"
	if who, err := agentPane(ctx, w.Tx); err != nil {
		return err
	} else if who != "" {
		return fmt.Errorf("%s; this is %s", why, who)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != name && (operator == "" || actor != operator) {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// SetUser sets settings of the user name: UserNotify to the source that
// tells them ("" for none), UserEnvFile to the path of the file their
// address in it is read from ("" for none). A source that cannot tell a
// user is refused with the ones that can. The path is kept as given:
// whoever calls resolves it, since the server may run in another directory,
// and reads the file, which the store never does.
func (s *Store) SetUser(ctx context.Context, name string, set map[string]string, actor string) (*User, error) {
	keys := make([]string, 0, len(set))
	for k := range set {
		if k != UserNotify && k != UserEnvFile {
			return nil, fmt.Errorf("user %s: %s is no setting of a user (it has: %s, %s)", name, k, UserNotify, UserEnvFile)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("user %s: nothing to set (%s=<source>, %s=<file>)", name, UserNotify, UserEnvFile)
	}
	sort.Strings(keys)
	var u *User
	err := s.write(func(w *wtx) (err error) {
		if u, err = getUser(w.Tx, name); err != nil {
			return err
		}
		if err := w.mayTell(ctx, name, actor); err != nil {
			return err
		}
		data := map[string]any{"user": name}
		for _, k := range keys {
			v := set[k]
			switch k {
			case UserNotify:
				if err := checkNotify(w.Tx, v); err != nil {
					return fmt.Errorf("user %s: %w", name, err)
				}
				u.Notify = v
				if v == "" {
					// Told nowhere: what waited to be told is not kept for
					// a source named later.
					if err := dropPushes(w.Tx, name); err != nil {
						return err
					}
				}
			case UserEnvFile:
				u.EnvFile = v
			}
			data[k] = v
		}
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		// The names and the path, as a source's settings are logged: what
		// the file holds is never read here.
		return w.event("", "user_set", actor, data)
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// checkNotify refuses a source no user can be told through.
func checkNotify(tx *bolt.Tx, name string) error {
	if name == "" {
		return nil
	}
	can, err := notifySources(tx)
	if err != nil || slices.Contains(can, name) {
		return err
	}
	have := "No source of this yard can yet: a declared source can when its file has a notify key (yardr source --help)"
	if len(can) > 0 {
		have = "A user can be told through: " + strings.Join(can, ", ")
	}
	var src Source
	if ok, err := get(tx, bSources, name, &src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s is no source a user can be told through. %s", name, have)
	}
	if _, err := src.Declaration(); err != nil {
		return fmt.Errorf("the file of the source %s is not read by this yardr (yardr source show %s). %s", name, name, have)
	}
	return fmt.Errorf("the source %s has no notify key, so its file names no call that tells a person. %s", name, have)
}
