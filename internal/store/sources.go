package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// bSources holds the declared sources, <name>: services outside the yard
// that were added by a file (see internal/source). An older build never
// opens the bucket and no other record names a source yet, so it came
// without a new schema version.
var bSources = []byte("sources")

func init() { allBuckets = append(allBuckets, bSources) }

// The settings of a declared source: the file its credential is read from,
// the command that mints it where the file names a mint, how many calls of
// it one caller makes in an hour (SourceCallLimit when unset), and how many
// of those are updates (SourceUpdateLimit).
const (
	SourceEnvFile        = "env_file"
	SourceMintCommand    = "mint_command"
	SourceCallsPerHour   = "calls_per_hour"
	SourceUpdatesPerHour = "updates_per_hour"
)

// SourceSettings are the settings a source has, as yardr source set names
// them.
var SourceSettings = []string{SourceEnvFile, SourceMintCommand, SourceCallsPerHour, SourceUpdatesPerHour}

// Source is a declared source: its file as it was added, and where the
// yard keeps the credential the file names. The file holds no secret and
// the credential's file is named by its path alone, so the record is as
// free to show and to copy as the rest of the store.
type Source struct {
	Name string `json:"name"`
	// Text is the declaration file, whole, comments and the keys this build
	// does not read included: a later build reads them from here.
	Text string `json:"declaration"`
	// EnvFile is the path of the file that sets the variable the
	// declaration names (credential.env), read at each call under the rule
	// of every env_file (config.ReadEnvFile). Empty until the operator
	// sets it; PutSource keeps what the source has.
	EnvFile string `json:"env_file,omitempty"`
	// EnvFileUnset is the file at the path KeySource writes to for a
	// source without an env_file, when one stands there and none is set:
	// what a giving left that was written and not recorded
	// (KeyNotRecorded), or a file put there by hand. No call reads it.
	// GetSource says it from the disk; it is never stored.
	EnvFileUnset string `json:"env_file_unset,omitempty"`
	// MintCommand is the operator's command that turns the mint's inputs
	// into a short-lived token, one line split on spaces without a shell,
	// the program absolute or on PATH. It is stored to be shown, as a crew
	// member's env_command is, so it holds no secret. Empty until the
	// operator sets it; PutSource keeps what the source has, and no pack
	// brings one.
	MintCommand string `json:"mint_command,omitempty"`
	// CallsPerHour is how many calls of the source one session or crew
	// member makes within SourceCallWindow; 0 for SourceCallLimit.
	CallsPerHour int `json:"calls_per_hour,omitempty"`
	// UpdatesPerHour is how many of those calls are updates; 0 for
	// SourceUpdateLimit.
	UpdatesPerHour int       `json:"updates_per_hour,omitempty"`
	AddedAt        time.Time `json:"added_at"`
	AddedBy        string    `json:"added_by"`
}

// Declaration reads the source's file. It was read when it was added, so
// an error here is a build that reads less than the one that added it.
func (s Source) Declaration() (*source.Declaration, error) {
	return source.Parse("source "+s.Name, []byte(s.Text))
}

// CallLimit is how many calls of the source one caller makes in an hour.
func (s Source) CallLimit() int {
	if s.CallsPerHour > 0 {
		return s.CallsPerHour
	}
	return SourceCallLimit
}

// UpdateLimit is how many update calls of the source one caller makes in
// an hour, each of which is one of its CallLimit too.
func (s Source) UpdateLimit() int {
	if s.UpdatesPerHour > 0 {
		return s.UpdatesPerHour
	}
	return SourceUpdateLimit
}

// maySource refuses a change to the declared sources by anyone but the
// operator. A source is where a credential of the yard is sent: whoever
// may replace its file can point the next call, and the credential with
// it, at an address of their own. And its file may name a command the yard
// runs (limits), as the user the daemon runs as. Who asks is told by the store's own
// records before the caller's word, as for a move out of a stage whose
// group has members (checkMembers): a call from the pane of a bead's session or of a crew
// member is that session's or that crew member's, whatever actor it
// names. A yard without an operator has nobody to hold it to, and takes
// the change from anyone who is no agent of its own.
func (w *wtx) maySource(ctx context.Context, actor, does string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	why := does + " is the operator's"
	if operator != "" {
		why += " (" + operator + ")"
	}
	why += ": a source is where a credential of the yard is sent, and its file may name a command the yard runs"
	sess, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() {
		return fmt.Errorf("%s; this is the session %s of a bead's agent", why, sess.ID)
	}
	r, err := paneCrew(w.Tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if r != nil {
		return fmt.Errorf("%s; this is the crew member %s", why, r.Name)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if operator != "" && actor != operator {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// PutSource adds the source text declares, under the name the text gives
// it, or replaces the one of that name, which keeps its settings. file is
// how an error names the text. A declaration that source.Parse refuses is
// not kept.
func (s *Store) PutSource(ctx context.Context, file, text, actor string) (*Source, error) {
	d, err := source.Parse(file, []byte(text))
	if err != nil {
		return nil, err
	}
	src := Source{Name: d.Name, Text: text, AddedAt: now(), AddedBy: actor}
	err = s.write(func(w *wtx) error {
		if err := w.maySource(ctx, actor, "adding a source"); err != nil {
			return err
		}
		var was Source
		replaced, err := get(w.Tx, bSources, d.Name, &was)
		if err != nil {
			return err
		}
		src.EnvFile, src.MintCommand, src.CallsPerHour, src.UpdatesPerHour = was.EnvFile, was.MintCommand, was.CallsPerHour, was.UpdatesPerHour
		if d.Limits != nil {
			if err := limitsKindsFree(w.Tx, d); err != nil {
				return err
			}
		}
		if err := put(w.Tx, bSources, d.Name, src); err != nil {
			return err
		}
		calls := make([]string, len(d.Calls))
		for i, c := range d.Calls {
			calls[i] = c.Name
		}
		data := map[string]any{"source": d.Name, "base": d.Base, "calls": calls}
		if d.Limits != nil {
			data["limits"] = d.Limits.KindNames()
		}
		if replaced {
			data["replaced"] = true
		}
		return w.event("", "source_added", actor, data)
	})
	if err != nil {
		return nil, err
	}
	return &src, nil
}

// limitsKindsFree refuses d when another source of the yard already says
// the limits of a harness kind d maps: one source answers for a kind, so
// that which one is asked does not depend on the order they are read in.
func limitsKindsFree(tx *bolt.Tx, d *source.Declaration) error {
	return each(tx, bSources, "", func(_ []byte, other *Source) error {
		if other.Name == d.Name {
			return nil
		}
		// One this build does not read maps nothing this build would ask.
		od, err := other.Declaration()
		if err != nil || od.Limits == nil {
			return nil
		}
		for _, kind := range d.Limits.KindNames() {
			if _, taken := od.Limits.Kinds[kind]; taken {
				return fmt.Errorf("source %s: the source %s already says the limits of %s: one source answers for a harness kind. Take %s out of one of the two (limits.kinds)",
					d.Name, od.Name, kind, kind)
			}
		}
		return nil
	})
}

func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	out := []Source{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bSources, "", func(_ []byte, src *Source) error {
			out = append(out, *src)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetSource(ctx context.Context, name string) (*Source, error) {
	src := new(Source)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bSources, name, src)
		if err == nil && !ok {
			err = fmt.Errorf("source %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if src.EnvFile == "" {
		if f := source.EnvFile(s.home, src.Name); fileStands(f) {
			src.EnvFileUnset = f
		}
	}
	return src, nil
}

// fileStands says whether anything is at path, a link that points nowhere
// included.
func fileStands(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// SetSource sets a setting of the source name: SourceEnvFile, to the path
// of the file its credential is read from, or to "" for none;
// SourceMintCommand, to the command that mints it where the file names a
// mint, or to "" for none; or SourceCallsPerHour or SourceUpdatesPerHour,
// to a whole number from 1, or to "" for SourceCallLimit and
// SourceUpdateLimit. The path and the command are kept as given: whoever
// calls resolves them, since the server may run in another directory.
func (s *Store) SetSource(ctx context.Context, name, key, value, actor string) (*Source, error) {
	perHour := 0
	switch key {
	case SourceEnvFile, SourceMintCommand:
	case SourceCallsPerHour, SourceUpdatesPerHour:
		if value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				unset := SourceCallLimit
				if key == SourceUpdatesPerHour {
					unset = SourceUpdateLimit
				}
				return nil, fmt.Errorf("source %s: %s=%q: want a whole number from 1, or nothing for %d", name, key, value, unset)
			}
			perHour = n
		}
	default:
		return nil, fmt.Errorf("source %s: %s is no setting of a source (it has: %s)", name, key, strings.Join(SourceSettings, ", "))
	}
	src := new(Source)
	err := s.write(func(w *wtx) error {
		if err := w.maySource(ctx, actor, "setting a source's credential"); err != nil {
			return err
		}
		if ok, err := get(w.Tx, bSources, name, src); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("source %s: %w", name, ErrNotFound)
		}
		switch key {
		case SourceEnvFile:
			src.EnvFile = value
		case SourceMintCommand:
			src.MintCommand = value
		case SourceCallsPerHour:
			src.CallsPerHour = perHour
		case SourceUpdatesPerHour:
			src.UpdatesPerHour = perHour
		}
		if err := put(w.Tx, bSources, name, src); err != nil {
			return err
		}
		return w.event("", "source_set", actor, map[string]any{"source": name, key: value})
	})
	if err != nil {
		return nil, err
	}
	return src, nil
}

// SourceKeyed is what KeySource or GiveSecret did: the names it wrote, the
// file they are in, and the credential's state in the words yardr source show has for
// it. None of it holds a value.
type SourceKeyed struct {
	Source *Source  `json:"source"`
	Given  []string `json:"given"`
	File   string   `json:"file"`
	// State is where every variable of the credential is set now
	// (CredentialState); Missing is what it still wants, in the words of
	// its error, when the names given are not all it needs.
	State   string `json:"credential,omitempty"`
	Missing string `json:"missing,omitempty"`
}

// KeySource writes values, names of the source's credential to the values
// they take, into the file the source's credential is read from: its
// env_file, or with none set <home>/sources/<name>.env, which is then set
// as SetSource sets it. It is how a value reaches a file without a person
// writing the file by hand, and a value is kept nowhere else: not in the
// store, not in an event, not in an error. A source with an env_file gets
// no write to the store and no event, as a file edited by hand has none.
//
// The file and the store are two writes. See keySource for their order and
// *KeyNotRecorded for what is left when the second fails.
func (s *Store) KeySource(ctx context.Context, name string, values map[string]string, actor string) (*SourceKeyed, error) {
	var k sourceKey
	err := s.write(func(w *wtx) error { return w.keySource(ctx, s.home, name, values, actor, &k) })
	if err != nil {
		return nil, k.failed(err)
	}
	return k.keyed(), nil
}

// keyed is what a giving that was committed did, with the credential's
// state read as a call would read it, after the write: the words never
// hold a value.
func (k *sourceKey) keyed() *SourceKeyed {
	out := &SourceKeyed{Source: k.src, Given: k.names, File: k.file}
	if d, err := k.src.Declaration(); err == nil {
		out.State, err = d.CredentialState(k.src.EnvFile, k.src.MintCommand)
		if err != nil {
			out.Missing = err.Error()
		}
	}
	return out
}

// sourceKey is one giving of a source's credential, as keySource left it.
// It outlives the transaction: a batch may run keySource twice, and renamed
// stays true once any run renamed the file.
type sourceKey struct {
	src     *Source
	names   []string
	file    string
	renamed bool
}

// KeyNotRecorded is the error of a credential that is written to its file
// and that the store could not record: the rename was done and the
// transaction around it failed. The file keeps the value, since putting
// the old one back would be a third write that can fail as the second did.
// A source that had an env_file uses the new value from its next call; one
// that had none has none still, and the file stands at the default path
// with no setting that names it. Giving the same names again ends as one
// giving that worked.
type KeyNotRecorded struct {
	Names []string
	File  string
	Err   error
}

func (e *KeyNotRecorded) Error() string {
	verb := "is"
	if len(e.Names) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s %s written to %s, but the yard could not record it: %v. Give it again.", strings.Join(e.Names, " and "), verb, e.File, e.Err)
}

func (e *KeyNotRecorded) Unwrap() error { return e.Err }

// failed is err as the caller is told it: after the rename, with the names
// and the file, and never a value.
func (k *sourceKey) failed(err error) error {
	if !k.renamed {
		return err
	}
	return &KeyNotRecorded{Names: k.names, File: k.file, Err: err}
}

// afterKeyRename fails a transaction after keySource renamed the file, for
// a test of what that leaves; nil outside one.
var afterKeyRename func() error

// keySource is KeySource inside the transaction, in the order that makes
// one thing hold whatever fails: the store never says more than the file
// holds. First the checks; then the setting and its event, put and not
// committed; last the file, written beside itself and renamed into place
// (config.WriteEnvFile). So an error up to the rename rolls the store back
// with the file as it was, and an env_file this call set is committed only
// after the file with the names' lines is in place. The other way round is
// not held: a commit that fails after the rename leaves the value in the
// file and nothing in the store (KeyNotRecorded).
//
// The store has one write transaction at a time, so two of these never
// write one file at once. A batch may run this twice for one call (write):
// the second run finds the lines and writes the same content.
//
// Whoever has more to record of the giving puts it before calling this:
// nothing may follow the rename but the commit.
func (w *wtx) keySource(ctx context.Context, home, name string, values map[string]string, actor string, k *sourceKey) error {
	if err := w.maySource(ctx, actor, "setting a source's credential"); err != nil {
		return err
	}
	src := new(Source)
	if ok, err := get(w.Tx, bSources, name, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("source %s: %w", name, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	if d.Credential == nil {
		return fmt.Errorf("source %s: its declaration names no credential, so there is nothing to give it", name)
	}
	if len(values) == 0 {
		return fmt.Errorf("source %s: no value was given (its declaration names %s)", name, d.Credential.Names())
	}
	has := d.Credential.Variables()
	names := make([]string, 0, len(values))
	for n := range values {
		if !slices.Contains(has, n) {
			return fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", name, n, d.Credential.Names())
		}
		names = append(names, n)
	}
	sort.Strings(names)
	file := src.EnvFile
	if file == "" {
		// The server chooses the path: nothing a caller sends is one.
		file = source.EnvFile(home, name)
		src.EnvFile = file
		if err := put(w.Tx, bSources, name, src); err != nil {
			return err
		}
		if err := w.event("", "source_set", actor, map[string]any{"source": name, SourceEnvFile: file}); err != nil {
			return err
		}
	}
	k.src, k.names = src, names
	resolved, renamed, err := config.WriteEnvFile(file, values)
	k.file = resolved
	k.renamed = k.renamed || renamed
	if err != nil {
		return fmt.Errorf("source %s: %w", name, err)
	}
	if afterKeyRename != nil {
		return afterKeyRename()
	}
	return nil
}

// DeleteSource removes the source, and with it who was granted its calls.
// The file its credential is in is the operator's and stays.
func (s *Store) DeleteSource(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		if err := w.maySource(ctx, actor, "removing a source"); err != nil {
			return err
		}
		if w.Bucket(bSources).Get([]byte(name)) == nil {
			return fmt.Errorf("source %s: %w", name, ErrNotFound)
		}
		if err := w.Bucket(bSources).Delete([]byte(name)); err != nil {
			return err
		}
		if err := dropSourceRecords(w.Tx, name); err != nil {
			return err
		}
		return w.event("", "source_removed", actor, map[string]any{"source": name})
	})
}
