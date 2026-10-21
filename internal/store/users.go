package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

// A user is a person as a party of the yard: a name and an inbox, as a
// crew member has. The operator (the name in the operator setting) is one of
// them. Mail to a user is a mail event like any other, marked to_user and
// indexed in the inbox bucket under the name, so the log's pruning and the
// rebuilt indexes treat it as they treat a crew member's. What differs is the
// reading: nothing nudges a person, and nothing of theirs holds the log
// back.
//
// Crew members and users share one space of names, since mail to a name must
// reach one party.

// OperatorSetting is the setting naming the yard's operator.
const OperatorSetting = "operator"

var bUsers = []byte("users")

func init() { allBuckets = append(allBuckets, bUsers) }

// User is a person with an inbox. The record is the place for what a
// person is elsewhere (an origin in another yard) once there is such a
// thing; today it is the name, what the user subscribes to, how far each
// part of its inbox is read, which source tells them outside the yard,
// and who they are in a source.
type User struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Cursor    uint64    `json:"cursor"` // mail acknowledged through this event seq
	// NotificationCursor is the same for the notifications: the two parts
	// of the inbox are acknowledged apart.
	NotificationCursor uint64 `json:"notification_cursor"`
	// Subscriptions make matching events notifications in the inbox (see
	// Notification). The user and the operator change them.
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	// Notify is the source that tells the user outside the yard, a declared
	// one with a notify key or an adapter that can; "" for none. EnvFile is
	// the path of the file that holds the user's own address in it (and
	// whatever else of theirs the source needs), read at each send under
	// the rule of every env_file (config.ReadEnvFile). The store has the
	// path and never what is in the file. See SetUser.
	Notify  string `json:"notify,omitempty"`
	EnvFile string `json:"env_file,omitempty"`
	// Tell is which parts the source tells them of, of TellParts and in
	// their order; none is all of them. See Tells.
	Tell []string `json:"tell,omitempty"`
	// Identities is who the user is in a source, by source, as a crew
	// member's is (SetIdentity). It is whom a thread opened there for
	// them is assigned to, and who a hook delivery's author is matched
	// to: a delivery from a linked author bills that user's login when a
	// herdr group and a route for the made bead use that user's login.
	Identities map[string]Identity `json:"identities,omitempty"`
	// LastPush is how the last send through that source ended, and Pushed
	// when the last messages it took were sent, the newest PushesPerHour of
	// them: what the bound on an hour's messages is counted from. See Push.
	LastPush *PushResult `json:"last_push,omitempty"`
	Pushed   []time.Time `json:"pushed,omitempty"`
}

// Reaction is one party's emoji on a letter: who put it and which of the
// fixed reactions it is.
type Reaction struct {
	From  string `json:"from"`
	Emoji string `json:"emoji"`
}

// Letter is one mail as a conversation and a user's inbox show it.
type Letter struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	From string    `json:"from"`
	To   string    `json:"to"`
	Text string    `json:"text"`
	// Read is whether the receiver acknowledged it (yardr inbox --ack <seq>).
	// Mail to a party that is gone was never read as far as the store
	// knows.
	Read bool `json:"read"`
	// Reactions are the parties' emoji on the mail, by who put them; absent
	// on mail without any.
	Reactions []Reaction `json:"reactions,omitempty"`
	// Timer is the id of the timer the yard sent it for, "" for mail its
	// sender wrote: the text then begins "timer:".
	Timer string `json:"timer,omitempty"`
	// Delivery is set on mail to a crew member of another yard, whose reading
	// of it this yard never learns of: MailQueued while it waits here for
	// the peer, MailSent once the peer has it, MailFailed when it was
	// given up.
	Delivery string `json:"delivery,omitempty"`
	// As is the user of this yard the mail speaks as: set on mail from a
	// name of a linked yard that the link trusted as that user when the
	// mail came (Peer.Trust), "" on any other.
	As string `json:"as,omitempty"`
	// Via is the device the mail was written through (WithVia), "" for mail
	// written on the yard's own machine.
	Via string `json:"via,omitempty"`
	// Files are the blobs the mail carries (PutBlob), absent on mail
	// without any: each is read with GET /blobs/<hash>, and is the file of
	// that name in the home's blobs directory.
	Files []BlobRef `json:"files,omitempty"`
}

// Mailbox is a user's mail: what they have not acknowledged, and the
// latest of what they have.
type Mailbox struct {
	// Mail is the unread, oldest first, then the read, newest first; at
	// most the limit in all.
	Mail   []Letter `json:"mail"`
	Unread int      `json:"unread"` // all unread, shown or not
	Last   uint64   `json:"last"`   // seq of the newest unread
	// From is who the unread are from, in the order they first wrote.
	From []string `json:"from"`
}

// operatorIn is the operator setting, "" when unset.
func operatorIn(tx *bolt.Tx) (string, error) {
	var v string
	_, err := get(tx, bSettings, OperatorSetting, &v)
	return v, err
}

// notOperator says whether actor acts as anyone but the operator: this is
// then how a refusal names them, and operator how it names the operator.
// It is the one place the rule is read, inside the write it guards, so
// every client of the store is held to it. From a session's or a crew
// member's pane the actor is the pane's (paneAuthor) before the rule is
// read, so a pane can never pass an admin write as the operator by
// --actor alone; from anywhere else it is the caller's word (YARDR_ACTOR,
// --actor): every agent runs as one OS user, so this is a line against
// mistakes, not a wall, and moves when yardr hosts more than one person.
// A yard that names no operator has nobody to hold it to, and takes the
// change from anyone who is no agent of its own, as maySource reads it:
// no crew member, and no call from the pane of a bead's session or of a
// crew member.
func notOperator(ctx context.Context, tx *bolt.Tx, actor string) (operator, this string, err error) {
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return "", "", err
	}
	actor = resolved
	name, err := operatorIn(tx)
	switch {
	case err != nil:
		return "", "", err
	case name != "" && actor == name:
		return "", "", nil
	case name != "":
		return "the operator (" + name + ")", or(actor, "nobody"), nil
	case tx.Bucket(bCrew).Get([]byte(actor)) != nil:
		return "the operator", "the crew member " + actor, nil
	}
	if sess, err := paneSession(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if sess != nil && !sess.Manual() {
		return "the operator", "the session " + sess.ID + " of a bead's agent", nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if r != nil {
		return "the operator", "the crew member " + r.Name, nil
	}
	return "", "", nil
}

// AskClass says whether an operator-only action may be proposed as a run
// ask. Alone is the zero value: a new operator-only check lands alone
// until its site marks it Askable, so nothing becomes proposable by
// omission. Alone is for what defines what the yard runs or whom it
// trusts, takes a file path, or reaches another yard: the operator types
// it in a terminal, and a run ask proposing it is refused with that
// reason.
type AskClass int

const (
	Alone AskClass = iota
	Askable
)

// OperatorOnlyError refuses what only the operator may do, in the same
// words whatever is asked and whoever asks, with whether a run ask may
// propose it. Both operatorOnly and maySource refuse with it.
type OperatorOnlyError struct {
	msg string
	ask AskClass
}

func (e *OperatorOnlyError) Error() string { return e.msg }

// IsOperatorOnly reports whether err refuses an operator-only action, and
// whether a run ask may propose it.
func IsOperatorOnly(err error) (AskClass, bool) {
	var o *OperatorOnlyError
	if errors.As(err, &o) {
		return o.ask, true
	}
	return Alone, false
}

// operatorOnly refuses what only the operator may do (does, as in "add a
// peer") when actor is anyone else (notOperator), in the same words
// whatever is asked and whoever asks. ask marks whether a run ask may
// propose it, beside the refusal it marks.
func operatorOnly(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("only %s may %s; this is %s", operator, does, this), ask: ask}
}

func (w *wtx) operatorOnly(ctx context.Context, actor, does string, ask AskClass) error {
	return operatorOnly(ctx, w.Tx, actor, does, ask)
}

// operatorOrYardmaster passes the operator's call (notOperator) or the
// crew member's the yardmaster setting names, and refuses anyone else in
// words naming both. It covers undoing what the yardmaster may already
// add (crew, groups, flows, routes: adding each is anyone's, attributed)
// and restarting crew: removing a crew member, a group, a flow or a
// route, enabling a crew member, and asking one's session to restart.
// ask marks whether a run ask may propose it, as with operatorOnly.
func operatorOrYardmaster(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	operator, _, err := notOperator(ctx, tx, actor)
	if err != nil || operator == "" {
		return err
	}
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	var ym string
	if _, err := get(tx, bSettings, YardmasterSetting, &ym); err != nil {
		return err
	}
	if ym == "" {
		ym = "yardmaster"
	}
	// The setting names a crew member; a matching user name or stale
	// setting is not a delegation.
	if resolved == ym && tx.Bucket(bCrew).Get([]byte(ym)) != nil {
		return nil
	}
	return &OperatorOnlyError{msg: fmt.Sprintf("only %s or the yardmaster (%s) may %s", operator, ym, does), ask: ask}
}

func (w *wtx) operatorOrYardmaster(ctx context.Context, actor, does string, ask AskClass) error {
	return operatorOrYardmaster(ctx, w.Tx, actor, does, ask)
}

// operatorOnly is the rule for a change that is no write of the database
// (a pairing, which lives in the server's memory).
func (s *Store) operatorOnly(ctx context.Context, actor, does string, ask AskClass) error {
	return s.read(func(tx *bolt.Tx) error { return operatorOnly(ctx, tx, actor, does, ask) })
}

// daemonOrOperator passes the daemon's own call (daemonOnly) or the
// operator's (operatorOnly, does): a rotation restart and the automatic
// prune run in-process as "yard", and anyone else asking must be the
// operator. Over the socket only the daemon-lock holder passes the
// daemon leg, so actor "yard" from a pane still faces operatorOnly; the
// daemon leg's refusal is then swallowed in favour of the operator's
// words.
func (s *Store) daemonOrOperator(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	if err := s.daemonOnly(ctx, does); err == nil {
		return nil
	}
	return operatorOnly(ctx, tx, actor, does, ask)
}

// daemonOrYardmaster passes the daemon's own call (daemonOnly) or the
// operator's or the yardmaster's (operatorOrYardmaster, does): a rotation
// restart runs in-process as "yard", and anyone else asking must be the
// operator or the yardmaster. Over the socket only the daemon-lock holder
// passes the daemon leg, so actor "yard" from a pane still faces
// operatorOrYardmaster; the daemon leg's refusal is then swallowed in
// favour of the operator's words.
func (s *Store) daemonOrYardmaster(ctx context.Context, tx *bolt.Tx, actor, does string, ask AskClass) error {
	if err := s.daemonOnly(ctx, does); err == nil {
		return nil
	}
	return operatorOrYardmaster(ctx, tx, actor, does, ask)
}

// daemonOnly refuses a session-lifecycle write from any caller but the
// daemon: method is the daemon's alone to make. The pane checks trust
// what these writes record (paneSession, paneCrew), so a pane that could
// make them could pass as another session or rewrite who a note is stored
// as. A call with no caller (the owning process: tests, in-process use)
// is served; over the socket the caller is the pid that holds the daemon
// lock, verified by pid and start time as a stop verifies it (never a
// bare pid), or the call is refused.
func (s *Store) daemonOnly(ctx context.Context, method string) error {
	pid := callerOf(ctx)
	if pid <= 0 {
		return nil
	}
	lock := config.At(s.home).DaemonLock()
	holder := config.DaemonPID(lock)
	start, recorded := config.DaemonStart(lock)
	if holder != pid || !recorded || !peer.Alive(pid, start) {
		return fmt.Errorf("%s is the daemon's alone", method)
	}
	return nil
}

// maySpeakAs refuses to speak as the crew member name to anyone but the
// crew member itself and the operator: a bead's agent, or another crew
// member, would otherwise speak in its name, with its credential or the
// operator's. From a session's or a crew member's pane the actor is the
// pane's (paneAuthor) before the rule is read, so a pane can never pass
// as the crew member or the operator by --actor alone; the pane yardr
// started it in still speaks as it when a command there names another.
// Like operatorOnly it is a line, not a wall.
func maySpeakAs(ctx context.Context, tx *bolt.Tx, name, actor string) error {
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	if actor == name {
		return nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return err
	} else if r != nil && r.Name == name {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s's own session or %s may speak as %s; this is %s", name, operator, name, this)
}

// addUser writes a new user who reads mail from now on: mail an earlier
// user of the name left is not theirs, nor are its notifications.
func (w *wtx) addUser(name, actor string, by *approvedOn) (*User, error) {
	u := &User{Name: name, CreatedAt: now(), CreatedBy: actor}
	if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
		u.Cursor = binary.BigEndian.Uint64(k)
		u.NotificationCursor = u.Cursor
	}
	if err := dropNotifications(w.Tx, name); err != nil {
		return nil, err
	}
	if err := put(w.Tx, bUsers, name, u); err != nil {
		return nil, err
	}
	data := map[string]any{"user": name}
	if by != nil {
		data["approved"] = by
	}
	return u, w.event("", "user_added", actor, data)
}

// ensureOperator gives the operator a user when there is none of the name.
// A crew member of the name keeps it and no user is made: yardr doctor says
// which of the two to rename. A user it makes carries the automatic
// install's failure subscription; one already there is left as it is.
func (w *wtx) ensureOperator(actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil || operator == "" {
		return err
	}
	if w.Bucket(bUsers).Get([]byte(operator)) != nil || w.Bucket(bCrew).Get([]byte(operator)) != nil {
		return nil
	}
	u, err := w.addUser(operator, actor, nil)
	if err != nil {
		return err
	}
	u.Subscriptions = append(u.Subscriptions, upgradeFailureSub())
	return put(w.Tx, bUsers, operator, u)
}

// subscribeUpgradeFailures gives the operator's user the automatic
// install's failure subscription, unless it holds it already: setting
// upgrade_from subscribes them again in the same write, so the automatic
// path never runs untold even where it was removed before.
func (w *wtx) subscribeUpgradeFailures() error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	u, err := getUser(w.Tx, operator)
	if err != nil {
		return err
	}
	want := upgradeFailureSub()
	if slices.ContainsFunc(u.Subscriptions, want.same) {
		return nil
	}
	u.Subscriptions = append(u.Subscriptions, want)
	return put(w.Tx, bUsers, operator, u)
}

// migrateOperator is ensureOperator for a yard from before users, run when
// the store opens.
func migrateOperator(tx *bolt.Tx) error {
	return (&wtx{Tx: tx}).ensureOperator("yardr")
}

// ValidUserName reports whether a user may have the name: what a caller
// asks before it proposes one (a login name as the operator, say).
func ValidUserName(name string) bool { return checkUserName(name) == nil }

func checkUserName(name string) error {
	if !peerMailName.MatchString(name) {
		return fmt.Errorf("user %q: use letters, digits, '.', '_', '-' or '/' (starting with a letter or digit)", name)
	}
	return nil
}

// AddUser adds the user name. It refuses a name a crew member or a user has,
// and the route keyword any, as a crew member's name is refused: an
// originator is a person.
// Who is a party of the yard is the operator's to say (operatorOnly): a
// crew member's session adding one would make itself a second voice.
func (s *Store) AddUser(ctx context.Context, name, actor string) (*User, error) {
	if err := checkUserName(name); err != nil {
		return nil, err
	}
	if name == OriginatorAny {
		return nil, fmt.Errorf("user %s: that is a route keyword, not a user, and mail to a name reaches one party: choose another", name)
	}
	var out *User
	err := s.write(func(w *wtx) error {
		var err error
		out, err = w.insertUser(ctx, name, actor, nil)
		return err
	})
	return out, err
}

// insertUser is AddUser inside the transaction, so that an approved run
// ask adds the user in the write that records its approval: by is then
// the ask it was approved on, which the event carries.
func (w *wtx) insertUser(ctx context.Context, name, actor string, by *approvedOn) (*User, error) {
	if err := w.operatorOnly(ctx, actor, "add a user", Askable); err != nil {
		return nil, err
	}
	switch {
	case w.Bucket(bUsers).Get([]byte(name)) != nil:
		return nil, fmt.Errorf("%w: there is a user %s already", ErrConflict, name)
	case w.Bucket(bCrew).Get([]byte(name)) != nil:
		return nil, fmt.Errorf("user %s: that is a crew member's name, and mail to a name reaches one party: choose another", name)
	}
	return w.addUser(name, actor, by)
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	out := []User{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bUsers, "", func(_ []byte, u *User) error {
			out = append(out, *u)
			return nil
		})
	})
	return out, err
}

func getUser(tx *bolt.Tx, name string) (*User, error) {
	u := new(User)
	if ok, err := get(tx, bUsers, name, u); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("user %s: %w", name, ErrNotFound)
	}
	return u, nil
}

func (s *Store) GetUser(ctx context.Context, name string) (*User, error) {
	var u *User
	err := s.read(func(tx *bolt.Tx) (err error) {
		u, err = getUser(tx, name)
		return err
	})
	return u, err
}

// DeleteUser removes the user, with their subscriptions and notifications,
// their devices and what was still to be told them outside the yard; the
// mail to and from them stays in the log. It also removes every grant of
// a call made as them and clears their credential from their env file, so
// a user added again under the name finds neither waiting: a name is
// reusable, and the first's access must not be the second's.
// The operator's user is not removed: the setting names someone else
// first. Like adding one, it is the operator's to do (operatorOnly).
func (s *Store) DeleteUser(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		return w.deleteUser(ctx, name, actor, nil)
	})
}

// effectiveEnvFile is the file a user's credential and address are read
// from: the one their record names, or the yard's default for their name
// (userEnvFile); no file for a name the yard chooses none for.
func effectiveEnvFile(home string, u *User) (string, bool) {
	if u.EnvFile != "" {
		return u.EnvFile, true
	}
	file, err := userEnvFile(home, u.Name)
	if err != nil {
		return "", false
	}
	return file, true
}

// resolvedEnvFile is a path as the one file it names: a leading ~/ made
// the home and the result made absolute and cleaned (config.ExpandPath),
// with a symbolic link evaluated where the file stands, so its spellings
// and a link to it count as the one file. The comparison is on the
// records, whether the file stands today or not.
func resolvedEnvFile(path string) (string, error) {
	p, err := config.ExpandPath(path)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	}
	return p, nil
}

// userCredentialNames are every name a user's env file holds for a
// source: each declared source's credential names (its static ones and
// every name under its mint) and the address name of each notify key,
// across the yard's sources. Sorted, each once.
func userCredentialNames(tx *bolt.Tx) ([]string, error) {
	set := map[string]bool{}
	err := each(tx, bSources, "", func(_ []byte, src *Source) error {
		d, err := src.Declaration()
		if err != nil {
			// One this build does not read names no name it can clear.
			return nil
		}
		if d.Credential != nil {
			for _, n := range d.Credential.Variables() {
				set[n] = true
			}
		}
		if d.Notify != nil && d.Notify.AddressEnv != "" {
			set[d.Notify.AddressEnv] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// clearUserCredential takes the user name's credential out of their
// effective file, before their record goes: the yard's own default file
// is removed outright, a file a person keeps elsewhere has every
// credential and address name struck from it by the whole-file write an
// env file is written by, its other lines and comments kept. A file
// already gone is no error; one that cannot be cleared refuses the
// removal whole, with the record, the grants and the file as they were.
func (w *wtx) clearUserCredential(name string, u *User) error {
	file, has := effectiveEnvFile(w.home, u)
	if !has {
		return nil
	}
	resolved, err := resolvedEnvFile(file)
	if err != nil {
		return fmt.Errorf("user %s is not removed: %w", name, err)
	}
	if def, err := userEnvFile(w.home, name); err == nil {
		if defResolved, err := resolvedEnvFile(def); err == nil && defResolved == resolved {
			// Remove the path the yard chose, not its resolved target: if
			// the default name has become a symlink, following it here would
			// delete a file outside that name. The credential writer refuses
			// symlinks, so there is no valid default credential to preserve.
			st, err := os.Lstat(file)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("user %s is not removed: their credential file %s is not removed: %w", name, file, err)
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("user %s is not removed: their credential file %s is a symbolic link, and the yard removes none", name, file)
			}
			if !st.Mode().IsRegular() {
				return fmt.Errorf("user %s is not removed: their credential file %s is not a regular file", name, file)
			}
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("user %s is not removed: their credential file %s is not removed: %w", name, file, err)
			}
			return nil
		}
	}
	names, err := userCredentialNames(w.Tx)
	if err != nil {
		return err
	}
	if _, _, err := config.RemoveEnvNames(file, names); err != nil {
		return fmt.Errorf("user %s is not removed: their credential is not cleared from %s: %w", name, resolved, err)
	}
	return nil
}

// deleteUser is DeleteUser inside the transaction, so that an approved run
// ask removes the user in the write that records its approval: by is then
// the ask it was approved on, which the event carries.
func (w *wtx) deleteUser(ctx context.Context, name, actor string, by *approvedOn) error {
	if err := w.operatorOnly(ctx, actor, "remove a user", Askable); err != nil {
		return err
	}
	u, err := getUser(w.Tx, name)
	if err != nil {
		return err
	}
	if operator, err := operatorIn(w.Tx); err != nil {
		return err
	} else if operator == name {
		return fmt.Errorf("user %s is the yard's operator and is not removed; name another operator first (yardr settings set %s <name>)", name, OperatorSetting)
	}
	// Before any grant is deleted and before the file is touched: where
	// another user's effective file is the same file, clearing it would
	// take their credential or address with this one's, so the removal
	// is refused whole, with the move that frees it named.
	if file, has := effectiveEnvFile(w.home, u); has {
		resolved, err := resolvedEnvFile(file)
		if err != nil {
			return fmt.Errorf("user %s is not removed: %w", name, err)
		}
		var shared string
		err = each(w.Tx, bUsers, "", func(_ []byte, other *User) error {
			if other.Name == name || shared != "" {
				return nil
			}
			otherFile, ok := effectiveEnvFile(w.home, other)
			if !ok {
				return nil
			}
			otherResolved, err := resolvedEnvFile(otherFile)
			if err != nil {
				return err
			}
			if otherResolved == resolved {
				shared = other.Name
			}
			return nil
		})
		if err != nil {
			return err
		}
		if shared != "" {
			return fmt.Errorf("user %s is not removed: %s's env_file is the same file (%s); move one of them first (yardr user set %s env_file=<file>) and remove %s again", name, shared, resolved, shared, name)
		}
	}
	// The grants of calls made as them, in the same write as the record.
	var asKeys []string
	if err := each(w.Tx, bSourceGrants, "", func(k []byte, g *SourceGrant) error {
		if g.As == name {
			asKeys = append(asKeys, string(k))
		}
		return nil
	}); err != nil {
		return err
	}
	if err := w.clearUserCredential(name, u); err != nil {
		return err
	}
	for _, k := range asKeys {
		if err := w.Bucket(bSourceGrants).Delete([]byte(k)); err != nil {
			return err
		}
	}
	if err := w.Bucket(bUsers).Delete([]byte(name)); err != nil {
		return err
	}
	if err := dropNotifications(w.Tx, name); err != nil {
		return err
	}
	if err := dropPushes(w.Tx, name); err != nil {
		return err
	}
	data := map[string]any{"user": name}
	if len(asKeys) > 0 {
		data["as_grants"] = len(asKeys)
	}
	if err := dropDevices(w.Tx, name, data); err != nil {
		return err
	}
	if err := unexpose(w.Tx, name, data); err != nil {
		return err
	}
	if err := untrust(w.Tx, name, data); err != nil {
		return err
	}
	if by != nil {
		data["approved"] = by
	}
	return w.event("", "user_removed", actor, data)
}

// toUser reports whether a mail event is for a user, not a crew member.
func toUser(e Event) bool {
	u, _ := e.Data["to_user"].(bool)
	return e.Kind == "mail" && u
}

// MailReceiver is whose inbox a mail event of this yard is in, crew member or
// user: "" for mail that left for a peer's crew member, and for what is no
// mail. The listener reads it too, to send a device its own mail alone.
func MailReceiver(e Event) string {
	if e.Kind != "mail" {
		return ""
	}
	if _, left := e.Data["to_peer"]; left {
		return ""
	}
	to, _ := e.Data["to"].(string)
	return to
}

// mailKeys are the event keys after seq in name's inbox index, in order.
func mailKeys(tx *bolt.Tx, name string, after uint64) []string {
	var out []string
	c := tx.Bucket(bInbox).Cursor()
	p := []byte(name + "\x00")
	for k, _ := c.Seek([]byte(join(name, seqKey(after+1)))); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
		out = append(out, string(k[len(p):]))
	}
	return out
}

// readers answers whether a mail's receiver acknowledged it, reading each
// crew member or user once, and what reactions its parties put on it,
// reading the reaction events once.
type readers struct {
	tx            *bolt.Tx
	cursors       map[string]uint64
	reactions     map[uint64][]Reaction
	reactionsDone bool
}

// reactionsFor are the reactions on the mail seq: the last event wins per
// party, and a clear leaves none. The reaction events are read once, on the
// first letter asked of.
func (rd *readers) reactionsFor(seq uint64) ([]Reaction, error) {
	if !rd.reactionsDone {
		m, err := allReactions(rd.tx)
		if err != nil {
			return nil, err
		}
		rd.reactions, rd.reactionsDone = m, true
	}
	return rd.reactions[seq], nil
}

// allReactions are the reactions the log holds, by the seq of the mail they
// are on: one party's latest emoji, or none after a clear. The events arrive
// in seq order, so each overwrites the one before it.
func allReactions(tx *bolt.Tx) (map[uint64][]Reaction, error) {
	last := map[uint64]map[string]string{}
	c := tx.Bucket(bEventsKd).Cursor()
	prefix := []byte(join("reaction", ""))
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		var e Event
		if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		target, ok := reactionTarget(e)
		if !ok {
			continue
		}
		emoji, _ := e.Data["emoji"].(string)
		if last[target] == nil {
			last[target] = map[string]string{}
		}
		last[target][e.Actor] = emoji
	}
	out := map[uint64][]Reaction{}
	for target, byFrom := range last {
		for from, emoji := range byFrom {
			if emoji == "" {
				continue
			}
			out[target] = append(out[target], Reaction{From: from, Emoji: emoji})
		}
		slices.SortFunc(out[target], func(a, b Reaction) int { return strings.Compare(a.From, b.From) })
		if len(out[target]) == 0 {
			delete(out, target)
		}
	}
	return out, nil
}

// reactionTarget is the seq of the mail the reaction event e is on. A number
// of the log reads back as a float64; the write holds the seq it was given.
func reactionTarget(e Event) (uint64, bool) {
	switch n := e.Data["seq"].(type) {
	case float64:
		return uint64(n), n >= 0 && n == float64(uint64(n))
	case uint64:
		return n, true
	case int:
		return uint64(n), n >= 0
	case int64:
		return uint64(n), n >= 0
	}
	return 0, false
}

func (rd *readers) letter(e Event) (Letter, error) {
	to := MailReceiver(e)
	key, bucket, toCrew := "r\x00"+to, bCrew, true
	if toUser(e) {
		key, bucket, toCrew = "u\x00"+to, bUsers, false
	} else {
		// Mail to a crew member is read in the sender's slice, so the
		// cursor that says read depends on who sent it.
		key += "\x00" + e.Actor
	}
	cursor, ok := rd.cursors[key]
	if !ok {
		// CrewMember and User both keep it as "cursor".
		var c struct {
			Cursor        uint64         `json:"cursor"`
			Config        map[string]any `json:"config"`
			Conversations map[string]struct {
				Cursor uint64 `json:"cursor"`
			} `json:"conversations"`
		}
		if _, err := get(rd.tx, bucket, to, &c); err != nil {
			return Letter{}, err
		}
		cursor = c.Cursor
		// Mail to a crew member from a user with a conversation is read
		// in that slice: its cursor says whether it was read, not the
		// line's, which acks past it without seeing it. A conversation
		// whose row the daemon has not made yet reads at the line cursor,
		// as its slice does.
		if toCrew && configServes(c.Config, e.Actor) {
			if path, _ := c.Config[EnvForPrefix+e.Actor].(string); path != "" {
				if cs, ok := c.Conversations[e.Actor]; ok {
					cursor = cs.Cursor
				}
			}
		}
		if rd.cursors == nil {
			rd.cursors = map[string]uint64{}
		}
		rd.cursors[key] = cursor
	}
	text, _ := e.Data["text"].(string)
	reactions, err := rd.reactionsFor(e.Seq)
	if err != nil {
		return Letter{}, err
	}
	return Letter{Seq: e.Seq, At: e.At, From: e.Actor, To: to, Text: text, Read: e.Seq <= cursor, Reactions: reactions, Timer: timerOf(e), As: SpeaksAs(e), Via: Via(e), Files: EventFiles(e)}, nil
}

// UserInbox returns the mail to the user name: all that is unread counted,
// and up to limit shown (0 shows all), the unread first.
func (s *Store) UserInbox(ctx context.Context, name string, limit int) (*Mailbox, error) {
	box := &Mailbox{Mail: []Letter{}, From: []string{}}
	err := s.read(func(tx *bolt.Tx) error {
		if _, err := getUser(tx, name); err != nil {
			return err
		}
		rd := &readers{tx: tx}
		var evs []Event
		for _, k := range mailKeys(tx, name, 0) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if ok && toUser(e) && MailReceiver(e) == name {
				evs = append(evs, e)
			}
		}
		// A linked yard may have been renamed since it wrote one of these.
		// Give the user the same current sender address as crew inboxes do,
		// so chat can match the mail to the linked yard's row.
		if err := peersToday(tx, evs); err != nil {
			return err
		}
		var read []Letter
		for _, e := range evs {
			l, err := rd.letter(e)
			if err != nil {
				return err
			}
			if l.Read {
				read = append(read, l)
				continue
			}
			box.Unread++
			box.Last = l.Seq
			if !slices.Contains(box.From, l.From) {
				box.From = append(box.From, l.From)
			}
			if limit <= 0 || len(box.Mail) < limit {
				box.Mail = append(box.Mail, l)
			}
		}
		for i := len(read) - 1; i >= 0 && (limit <= 0 || len(box.Mail) < limit); i-- {
			box.Mail = append(box.Mail, read[i])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return box, nil
}

// AckUserInbox marks the user's mail read through seq.
func (s *Store) AckUserInbox(ctx context.Context, name string, through uint64) error {
	return s.writeVia(ctx, func(w *wtx) error {
		u, err := getUser(w.Tx, name)
		if err != nil {
			return err
		}
		was := u.Cursor
		u.Cursor = max(u.Cursor, through)
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		return w.mailRead(name, was, u.Cursor)
	})
}

// mailRead logs that name, a crew member or a user, acknowledged its inbox
// from after was through seq, when mail is in between: the event mail_read
// with the reader and how far. It is what tells a view that follows the
// stream that mail it shows as sent has been read; an acknowledgement
// that passes no mail is not an event.
func (w *wtx) mailRead(name string, was, through uint64) error {
	read := false
	for _, k := range mailKeys(w.Tx, name, was) {
		if binary.BigEndian.Uint64([]byte(k)) > through {
			break
		}
		var e Event
		if ok, err := get(w.Tx, bEvents, k, &e); err != nil {
			return err
		} else if ok && MailReceiver(e) == name {
			read = true
			break
		}
	}
	if !read {
		return nil
	}
	return w.event("", "mail_read", name, map[string]any{"reader": name, "through": through})
}

// crewMailRead is mailRead for one crew session's slice. A positional ack
// can pass mail belonging to another slice, so it counts only mail this
// session actually reads and names the session on the receipt.
func (w *wtx) crewMailRead(r *CrewMember, session string, was, through uint64) error {
	read := false
	for _, k := range mailKeys(w.Tx, r.Name, was) {
		if binary.BigEndian.Uint64([]byte(k)) > through {
			break
		}
		var e Event
		if ok, err := get(w.Tx, bEvents, k, &e); err != nil {
			return err
		} else if ok && MailReceiver(e) == r.Name && forSlice(r, session, e) {
			read = true
			break
		}
	}
	if !read {
		return nil
	}
	return w.event("", "mail_read", r.Name, map[string]any{
		"reader": r.Name, "through": through, "session": session,
	})
}

// Conversation returns the mail between a and b within this yard, both
// directions, oldest first, with whether its receiver has read each; after
// leaves out what is not newer than that seq. Either may be a crew member or
// a user. It reads the inbox index of the two and nothing else of the log,
// so asking again with the last seq seen costs what is new.
//
// before leaves out what is not older than that seq (0: nothing), and limit
// answers at most that many (0: all): the newest of what after and before
// leave, so a long conversation is read from its newest end, a page at a
// time, each with the seq of the oldest letter of the last as before. The
// bool is whether the limit left older mail out. Only the letters answered,
// and one more, are read from the log.
//
// b may also be <crew member>@<peer>, a crew member of a linked yard: see
// peerConversation.
//
// A party that is a user of this yard has one history, whatever name they
// wrote under: the conversation has also the mail between the other party
// and each name of a linked yard that spoke as that user (history), in the
// same order and the same pages. Such a letter says how it went, by the
// address it is from or to and by As. An address as b is that address
// alone, as before.
func (s *Store) Conversation(ctx context.Context, a, b string, after uint64, limit int, before uint64) ([]Letter, bool, error) {
	out, more := []Letter{}, false
	err := s.read(func(tx *bolt.Tx) (err error) {
		if strings.Contains(b, "@") {
			out, more, err = peerConversation(tx, a, b, after, limit, before)
			return err
		}
		h, err := historyOf(tx, a, b)
		if err != nil {
			return err
		}
		keys := mailKeys(tx, a, after)
		if b != a {
			keys = append(keys, mailKeys(tx, b, after)...)
		}
		if h.wrote() {
			// What was written to a name of another yard is in no inbox of
			// this one: it is found among all the mail of the log, as
			// peerConversation finds it.
			keys = append(keys, mailAfter(tx, after)...)
		}
		slices.Sort(keys) // big-endian seqs sort as numbers
		keys = slices.Compact(keys)
		if before > 0 {
			n, _ := slices.BinarySearch(keys, seqKey(before))
			keys = keys[:n]
		}
		rd := &readers{tx: tx}
		out, more, err = newest(keys, limit, func(k string) (Letter, bool, error) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil || !ok {
				return Letter{}, false, err
			}
			to := MailReceiver(e)
			if to == "" || !(e.Actor == a && to == b || e.Actor == b && to == a) {
				return h.letter(e, rd)
			}
			l, err := rd.letter(e)
			return l, err == nil, err
		})
		return err
	})
	return out, more, err
}

// mailAfter are the keys of the mail events after seq, oldest first, by
// the kind index.
func mailAfter(tx *bolt.Tx, after uint64) []string {
	var out []string
	c := tx.Bucket(bEventsKd).Cursor()
	prefix := []byte(join("mail", ""))
	for k, _ := c.Seek([]byte(join("mail", seqKey(after+1)))); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		out = append(out, string(k[len(prefix):]))
	}
	return out
}

// history is what a conversation has beyond the mail between its two
// parties as named: for each of them that is a user of this yard, the mail
// between the other and a name of a linked yard that spoke as that user.
// The trusted name is how the user spoke, not another party (peertrust.go),
// so what they wrote under it and what was written to it is their word and
// their mail, with whoever the other is.
//
// Mail is of it by what the link said of the name when it was logged. Mail
// that came was marked as the user's on arrival (SpeaksAs), and is theirs
// whatever the link trusts today. Mail without the mark, which is all that
// was written to the name and what came before the grant, goes by the
// grant: the one that stands, or one that ended after the mail
// (Peer.spokeAs). So trust taken back keeps the mail of its time, and
// takes none of what follows. Mail of a yard that is linked no more is not
// of it: its grants went with the link, and nothing reaches the address.
//
// None of it is in the user's inbox here. What came is in the inbox of
// whom it was for, and read as they read it; what was written to the name
// left for the other yard, and has a delivery and no reading (Letter). So
// UserInbox counts none of it, and an acknowledgement passes none.
type history struct {
	tx *bolt.Tx
	ps peerNames
	// of are the parties that are users, each with the other one.
	of []spoken
	// sent is what is known of the mail written to each peer, by its name,
	// read when the first such letter is answered.
	sent map[string]*sent
}

// spoken is one side of a history: the mail of the names that spoke as
// user, with other.
type spoken struct{ user, other string }

// historyOf is the history of the conversation of a and b; nil, which adds
// nothing, when neither is a user.
func historyOf(tx *bolt.Tx, a, b string) (*history, error) {
	h := &history{tx: tx, sent: map[string]*sent{}}
	for _, p := range [][2]string{{a, b}, {b, a}} {
		if tx.Bucket(bUsers).Get([]byte(p[0])) != nil && (len(h.of) == 0 || a != b) {
			h.of = append(h.of, spoken{p[0], p[1]})
		}
	}
	if len(h.of) == 0 {
		return nil, nil
	}
	var err error
	h.ps, err = peersIn(tx)
	return h, err
}

// wrote is whether mail written to a name of another yard can be of the
// history: a link trusts a name as one of the users, or did.
func (h *history) wrote() bool {
	if h == nil {
		return false
	}
	for _, p := range h.ps {
		for _, of := range h.of {
			for _, as := range p.Trust {
				if as == of.user {
					return true
				}
			}
			if slices.ContainsFunc(p.Spoke, func(s PeerSpoke) bool { return s.As == of.user }) {
				return true
			}
		}
	}
	return false
}

// letter is the mail e as a letter of the history, false for mail that is
// not of it. The address in it has the name its peer has now.
func (h *history) letter(e Event, rd *readers) (Letter, bool, error) {
	if h == nil || e.Kind != "mail" {
		return Letter{}, false, nil
	}
	to, _ := e.Data["to"].(string)
	if left, _ := e.Data["to_peer"].(string); left != "" {
		p := h.ps.peer(left, e.Seq)
		if p == nil {
			return Letter{}, false, nil
		}
		if !slices.Contains(h.of, spoken{p.spokeAs(to, e.Seq), e.Actor}) {
			return Letter{}, false, nil
		}
		if h.sent[p.Name] == nil {
			s, err := sentTo(h.tx, p)
			if err != nil {
				return Letter{}, false, err
			}
			h.sent[p.Name] = s
		}
		l := h.sent[p.Name].letter(e, to+"@"+p.Name)
		reactions, err := rd.reactionsFor(e.Seq)
		if err != nil {
			return Letter{}, false, err
		}
		l.Reactions = reactions
		return l, true, nil
	}
	came, _ := e.Data["from_peer"].(string)
	from, _ := e.Data["from"].(string)
	p := h.ps.peer(came, e.Seq)
	if p == nil {
		return Letter{}, false, nil
	}
	user, _ := e.Data["as"].(string)
	if user == "" {
		user = p.spokeAs(from, e.Seq)
	}
	if !slices.Contains(h.of, spoken{user, to}) {
		return Letter{}, false, nil
	}
	l, err := rd.letter(e)
	l.From = from + "@" + p.Name
	return l, err == nil, err
}

// newest is the letters of a conversation among keys, the seqs of mail
// oldest first: letter answers the one of a key, false for mail that is not
// of the conversation. It reads from the newest end and stops at limit
// letters (0: at none), and says whether one more was there. Oldest first.
func newest(keys []string, limit int, letter func(k string) (Letter, bool, error)) ([]Letter, bool, error) {
	out, more := []Letter{}, false
	for i := len(keys) - 1; i >= 0 && !more; i-- {
		l, ok, err := letter(keys[i])
		switch {
		case err != nil:
			return nil, false, err
		case !ok:
		case limit > 0 && len(out) == limit:
			more = true
		default:
			out = append(out, l)
		}
	}
	slices.Reverse(out)
	return out, more, nil
}
