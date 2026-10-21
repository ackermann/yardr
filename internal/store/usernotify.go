package store

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
)

// The settings of a user: the source that tells them outside the yard of
// what lands in their inbox, the file their own address in it is read
// from, and which parts they are told of.
const (
	UserNotify  = "notify"
	UserEnvFile = "env_file"
	UserTell    = "tell"
)

// TellParts are the parts a user can be told of outside the yard, as
// UserTell names them and in the order it is kept in: the notifications of
// their subscriptions, their mail, and the asks for them.
var TellParts = []string{"notifications", "mail", "asks"}

// tellPart is the name in TellParts of what a push is for.
var tellPart = map[string]string{PushNotification: "notifications", PushMail: "mail", PushAsk: "asks"}

// Tells reports whether the user is told outside the yard of the part a
// push is for (PushMail, PushNotification, PushAsk): of every part when
// they name none.
func (u *User) Tells(part string) bool {
	return len(u.Tell) == 0 || slices.Contains(u.Tell, tellPart[part])
}

// Told says in a line where the user is told outside the yard, and of which
// parts when they name some: what yardr user set prints of it, without
// whether the source is set up, which is read from a file. "" for a user
// who is told nowhere and has nothing set for it.
func (u *User) Told() string {
	if u.Notify == "" {
		var set []string
		if u.EnvFile != "" {
			set = append(set, UserEnvFile+" "+u.EnvFile)
		}
		if len(u.Tell) > 0 {
			set = append(set, UserTell+" "+strings.Join(u.Tell, ","))
		}
		if len(set) == 0 {
			return ""
		}
		return "nowhere (" + strings.Join(set, " and ") + " is set, and no notify)"
	}
	if len(u.Tell) > 0 {
		return "through " + u.Notify + ", of " + strings.Join(u.Tell, ", ") + " only"
	}
	return "through " + u.Notify
}

// toldOf are the parts the user is told of outside the yard, in the order
// of TellParts: all of them for a user who names none.
func (u *User) toldOf() []string {
	if len(u.Tell) == 0 {
		return TellParts
	}
	return u.Tell
}

// parseTell reads a value of UserTell, a list of TellParts with commas
// between: each once and in their order, whatever was written, and none
// for "". A name that is no part is refused with the three.
func parseTell(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	said := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if !slices.Contains(TellParts, part) {
			return nil, fmt.Errorf("%q is no part a user is told of outside the yard: %s=<a list of %s>, or nothing for all of them", part, UserTell, strings.Join(TellParts, ", "))
		}
		said[part] = true
	}
	var out []string
	for _, part := range TellParts {
		if said[part] {
			out = append(out, part)
		}
	}
	return out, nil
}

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
// address in it is read from ("" for none), UserTell to the parts they are
// told of, a list of TellParts with commas between ("" for all of them). A
// source that cannot tell a user is refused with the ones that can, and a
// part that is none with the three. The path is kept as given: whoever
// calls resolves it, since the server may run in another directory, and
// reads the file, which the store never does.
func (s *Store) SetUser(ctx context.Context, name string, set map[string]string, actor string) (*User, error) {
	keys := make([]string, 0, len(set))
	for k := range set {
		if k != UserNotify && k != UserEnvFile && k != UserTell {
			return nil, fmt.Errorf("user %s: %s is no setting of a user (it has: %s, %s, %s)", name, k, UserNotify, UserEnvFile, UserTell)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("user %s: nothing to set (%s=<source>, %s=<file>, %s=<parts>)", name, UserNotify, UserEnvFile, UserTell)
	}
	sort.Strings(keys)
	var u *User
	err := s.write(func(w *wtx) (err error) {
		u, err = w.setUser(ctx, name, keys, set, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// setUser is SetUser inside the transaction, for the settings keys of set
// in their order. by is the ask the change was approved on, nil for one
// that was typed.
func (w *wtx) setUser(ctx context.Context, name string, keys []string, set map[string]string, actor string, by *approvedOn) (*User, error) {
	u, err := getUser(w.Tx, name)
	if err != nil {
		return nil, err
	}
	if err := w.mayTell(ctx, name, actor); err != nil {
		return nil, err
	}
	data := map[string]any{"user": name}
	for _, k := range keys {
		v := set[k]
		switch k {
		case UserNotify:
			if err := checkNotify(w.Tx, v); err != nil {
				return nil, fmt.Errorf("user %s: %w", name, err)
			}
			u.Notify = v
			if v == "" {
				// Told nowhere: what waited to be told is not kept for
				// a source named later.
				if err := dropPushes(w.Tx, name); err != nil {
					return nil, err
				}
			}
		case UserEnvFile:
			u.EnvFile = v
		case UserTell:
			if u.Tell, err = parseTell(v); err != nil {
				return nil, fmt.Errorf("user %s: %w", name, err)
			}
			// A part they are not told of any more: what waited to
			// be told of it goes, as all of it does with notify=.
			if err := dropUntold(w.Tx, u); err != nil {
				return nil, err
			}
			v = strings.Join(u.Tell, ",")
		}
		data[k] = v
	}
	if err := put(w.Tx, bUsers, name, u); err != nil {
		return nil, err
	}
	if by != nil {
		data["approved"] = by
	}
	// The names and the path, as a source's settings are logged: what
	// the file holds is never read here.
	return u, w.event("", "user_set", actor, data)
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

// userEnvFile is where the yard keeps what a source needs of the user name
// when nobody named a file for them: <home>/users/<name>.env, beside the
// store and not in it. A user's name may hold path separators and dots
// (peerMailName), and one that does could name a file outside that directory
// or another user's, so the yard chooses no file for it.
func userEnvFile(home, name string) (string, error) {
	if strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("user %s has no %s, and the yard chooses no file for a name with a path separator in it: yardr user set %s %s=<file> first", name, UserEnvFile, name, UserEnvFile)
	}
	return filepath.Join(home, "users", name+".env"), nil
}

// KeyUser writes the user name's own address in the source src into the
// file it is read from at each send: values is the one variable the
// source's notify key names (notify.address.env) to the value it takes. The
// file is the user's env_file, or with none set <home>/users/<name>.env,
// which is then set as SetUser sets it. It is KeySource for what a source
// needs of one person, by the same writer and in the same order (keyUser),
// and a value is kept nowhere else.
//
// It is the user's own to give, or the operator's for them (mayTell), and
// it does not say the user is told through the source: notify stays as it
// is, and what it answers names the command that sets it.
func (s *Store) KeyUser(ctx context.Context, name, src string, values map[string]string, actor string) (*SourceKeyed, error) {
	var k sourceKey
	err := s.write(func(w *wtx) error {
		if err := w.keyUser(ctx, s.home, name, src, values, actor, false, &k); err != nil {
			return err
		}
		// The address given settles the claim: its card's bead closes and
		// its waiting ask is taken back, as a mapping card's is when its
		// sender is settled another way (retireClaimCards). After the
		// file's rename, as the approval's subscription is (runUserKey).
		// values holds the one address keyUser wrote.
		var addr string
		for _, addr = range values {
		}
		return w.retireClaimCards(name, src, addr, actor)
	})
	if err != nil {
		return nil, k.failed(err)
	}
	return k.keyed(s.home), nil
}

// KeyUserCredential writes the user name's own credential in the source
// src into the file a call made as them reads: values is variables of the
// source's credential to the values they take. The file is the user's
// env_file, or with none set <home>/users/<name>.env, which is then set
// as SetUser sets it. It is KeyUser for a credential instead of an
// address, by the same writer and in the same order (keyUser), and a value
// is kept nowhere else. The source needs no notify key: a source that is
// called as a user need tell nobody.
//
// It is the user's own to give, or the operator's for them (mayTell), and
// what it answers names the command that tries it as them.
func (s *Store) KeyUserCredential(ctx context.Context, name, src string, values map[string]string, actor string) (*SourceKeyed, error) {
	var k sourceKey
	err := s.write(func(w *wtx) error { return w.keyUser(ctx, s.home, name, src, values, actor, true, &k) })
	if err != nil {
		return nil, k.failed(err)
	}
	return k.keyed(s.home), nil
}

// keyUser is KeyUser inside the transaction, in keySource's order and for
// its reason: the checks; the setting and its event, put and not committed;
// last the file, after whose rename nothing may follow but the commit. So
// the store never says more than the file holds, and what a commit that
// fails after the rename leaves is what it leaves of a source's
// (KeyNotRecorded): the value in the file, and for a user who had no
// env_file none set still.
//
// With credential it is KeyUserCredential instead: values are variables of
// the source's credential, held to the declaration as KeySource holds
// them, and no notify key is needed, since a source that is called as a
// user need tell nobody. Without it, it refuses a source with no notify
// key, since such a source reads nothing of a user's file, and any name
// but the one that key names, without saying back what was sent.
func (w *wtx) keyUser(ctx context.Context, home, name, srcName string, values map[string]string, actor string, credential bool, k *sourceKey) error {
	u, err := getUser(w.Tx, name)
	if err != nil {
		return err
	}
	if err := w.mayTell(ctx, name, actor); err != nil {
		return err
	}
	src := new(Source)
	if ok, err := get(w.Tx, bSources, srcName, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("source %s: %w", srcName, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	var names []string
	if credential {
		if d.Credential == nil {
			return fmt.Errorf("source %s: its declaration names no credential, so there is no credential of %s's to give", srcName, name)
		}
		if len(values) == 0 {
			return fmt.Errorf("source %s: no value was given (its declaration names %s)", srcName, d.Credential.Names())
		}
		has := d.Credential.Variables()
		names = make([]string, 0, len(values))
		for n := range values {
			if !slices.Contains(has, n) {
				return fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", srcName, n, d.Credential.Names())
			}
			names = append(names, n)
		}
		sort.Strings(names)
	} else {
		if d.Notify == nil {
			return fmt.Errorf("source %s: its declaration has no notify key, so it reaches no person and reads no address of %s's", srcName, name)
		}
		env := d.Notify.AddressEnv
		if _, ok := values[env]; !ok || len(values) != 1 {
			return fmt.Errorf("user %s: the source %s reads %s of a user's file: that name is given, and no other", name, srcName, env)
		}
		names = []string{env}
	}
	file := u.EnvFile
	if file == "" {
		// The server chooses the path: nothing a caller sends is one.
		if file, err = userEnvFile(home, name); err != nil {
			return err
		}
		u.EnvFile = file
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		if err := w.event("", "user_set", actor, map[string]any{"user": name, UserEnvFile: file}); err != nil {
			return err
		}
	}
	k.src, k.user, k.as, k.names = src, u, credential, names
	resolved, renamed, err := config.WriteEnvFile(file, values)
	k.file = resolved
	k.renamed = k.renamed || renamed
	if err != nil {
		return fmt.Errorf("user %s: %w", name, err)
	}
	if afterKeyRename != nil {
		return afterKeyRename()
	}
	return nil
}
