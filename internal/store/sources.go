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
// it one caller makes in an hour (SourceCallLimit when unset), how many
// of those are updates (SourceUpdateLimit), and which of the declaration's
// region bases its calls go to (the declaration's own base when unset).
const (
	SourceEnvFile        = "env_file"
	SourceMintCommand    = "mint_command"
	SourceCallsPerHour   = "calls_per_hour"
	SourceUpdatesPerHour = "updates_per_hour"
	SourceBase           = "base"
)

// SourceSettings are the settings a source has, as yardr source set names
// them.
var SourceSettings = []string{SourceEnvFile, SourceMintCommand, SourceCallsPerHour, SourceUpdatesPerHour, SourceBase}

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
	// operator sets it; PutSource keeps what the source has, and a pack
	// never sets it: a pack brings its own command in the declaration
	// (credential.mint_command), which this setting wins over.
	MintCommand string `json:"mint_command,omitempty"`
	// Mint is the command a mint of the source runs, as
	// Declaration.MintArgv has it: MintCommand where it is set, else the
	// copy under <home>/sources/<name>/ of the one the declaration
	// brings. GetSource and Sources say it, from the home the store
	// stands in; it is never stored, so a client reads the server's
	// answer and needs no home of its own.
	Mint []string `json:"mint,omitempty"`
	// CallsPerHour is how many calls of the source one session or crew
	// member makes within SourceCallWindow; 0 for SourceCallLimit.
	CallsPerHour int `json:"calls_per_hour,omitempty"`
	// UpdatesPerHour is how many of those calls are updates; 0 for
	// SourceUpdateLimit.
	UpdatesPerHour int `json:"updates_per_hour,omitempty"`
	// Base is the name of the region's base the source's calls go to,
	// one of the declaration's bases; "" for the declaration's own
	// base. Empty until the operator sets it; PutSource keeps what the
	// source has.
	Base string `json:"base,omitempty"`
	// Ignored are the sender ids in this source the operator has
	// answered for in particular: a delivery from one is dropped
	// without a card under any hook word, until the entry is removed
	// (UnignoreSource) or the id is mapped to a user, which clears it
	// in the same write. Entries are added only by picking "nobody"
	// on a mapping card, and are read only for a source that hears by
	// identity. PutSource keeps what the source has.
	Ignored []string  `json:"ignored,omitempty"`
	AddedAt time.Time `json:"added_at"`
	AddedBy string    `json:"added_by"`
	// Builtin is whether the source is answered by the yard itself, in
	// process, with no record: the machine. It is never stored; the
	// commands that list a source beside the declared ones set it, so
	// that --json tells the built-in from a declared source of its name.
	Builtin bool `json:"builtin,omitempty"`
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

// CalledAt is where the source's calls go under its declaration d, as
// yardr source add says it: the address, and beside it the name of the
// region base the source is set to where that names one of d's bases (a
// setting a replacement keeps). "" for a declaration that makes no call.
func (s Source) CalledAt(d *source.Declaration) string {
	addr := d.WithBase(s.Base).Address()
	if _, ok := d.BaseURL(s.Base); addr != "" && s.Base != "" && ok {
		addr += " (base " + s.Base + ")"
	}
	return addr
}

// PutSource adds the source text declares, under the name the text gives
// it, or replaces the one of that name, which keeps its settings. file is
// how an error names the text. A declaration that source.Parse refuses,
// or whose hear.subscribe names a key no subscription takes, is not kept.
func (s *Store) PutSource(ctx context.Context, file, text, actor string) (*Source, error) {
	d, err := source.Parse(file, []byte(text))
	if err != nil {
		return nil, err
	}
	if err := hearSubscribeKeys(d); err != nil {
		return nil, err
	}
	var src *Source
	err = s.write(func(w *wtx) error {
		// Alone: it reads a file that says where a credential is sent and
		// may name a command the yard runs.
		if err := w.may(ctx, actor, ActAddSource, Object{Kind: KindSource, Name: d.Name}); err != nil {
			return err
		}
		var err error
		src, err = w.putSource(d, text, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return src, nil
}

// putSource stores the source text declares as d, inside the write, under
// d's name, or replaces the one of that name, which keeps its settings and
// its ignored senders (the grants stand beside it). It is what PutSource
// writes and what an approved run ask writes (putCarriedSource), so the
// two cannot differ: by names the ask the add was approved on, for the
// event. The caller has asked who may.
func (w *wtx) putSource(d *source.Declaration, text, actor string, by *approvedOn) (*Source, error) {
	src := Source{Name: d.Name, Text: text, AddedAt: now(), AddedBy: actor}
	var was Source
	replaced, err := get(w.Tx, bSources, d.Name, &was)
	if err != nil {
		return nil, err
	}
	src.EnvFile, src.MintCommand, src.CallsPerHour, src.UpdatesPerHour, src.Base = was.EnvFile, was.MintCommand, was.CallsPerHour, was.UpdatesPerHour, was.Base
	src.Ignored = was.Ignored
	if d.Limits != nil {
		if err := limitsKindsFree(w.Tx, d); err != nil {
			return nil, err
		}
	}
	if err := put(w.Tx, bSources, d.Name, src); err != nil {
		return nil, err
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
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "source_added", actor, data); err != nil {
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
			src.Mint = src.mintIn(s.home)
			out = append(out, *src)
			return nil
		})
	})
	return out, err
}

// mintIn is the command a mint of the source runs for a yard whose home is
// home (Source.Mint): nil for a declaration this build does not read.
func (s *Source) mintIn(home string) []string {
	d, err := s.Declaration()
	if err != nil {
		return nil
	}
	return d.MintArgv(home, s.MintCommand)
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
	src.Mint = src.mintIn(s.home)
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
// mint, or to "" for none; SourceCallsPerHour or SourceUpdatesPerHour,
// to a whole number from 1, or to "" for SourceCallLimit and
// SourceUpdateLimit; or SourceBase, to the name of one of the
// declaration's bases, or to "" for the declaration's own base. The path
// and the command are kept as given: whoever calls resolves them, since
// the server may run in another directory.
func (s *Store) SetSource(ctx context.Context, name, key, value, actor string) (*Source, error) {
	perHour, err := sourceSetting(name, key, value)
	if err != nil {
		return nil, err
	}
	var src *Source
	err = s.write(func(w *wtx) (err error) {
		src, err = w.setSource(ctx, name, key, value, perHour, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return src, nil
}

// sourceSetting checks a setting of the source name as SetSource takes it,
// and answers the number of one that is a limit, 0 for none. A base names
// one of the declaration's bases, which takes the stored source to check:
// sourceSetCheck holds it, with the source in hand.
func sourceSetting(name, key, value string) (perHour int, err error) {
	switch key {
	case SourceEnvFile, SourceMintCommand, SourceBase:
	case SourceCallsPerHour, SourceUpdatesPerHour:
		if value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				unset := SourceCallLimit
				if key == SourceUpdatesPerHour {
					unset = SourceUpdateLimit
				}
				return 0, fmt.Errorf("source %s: %s=%q: want a whole number from 1, or nothing for %d", name, key, value, unset)
			}
			perHour = n
		}
	default:
		return 0, fmt.Errorf("source %s: %s is no setting of a source (it has: %s)", name, key, strings.Join(SourceSettings, ", "))
	}
	return perHour, nil
}

// setSource is SetSource inside the transaction, for a setting that
// sourceSetting has checked. by is the ask the change was approved on, nil
// for one typed at a terminal: the event then names it (ApproveAsk).
// sourceSetAsk is whether setting key of a source may be proposed as a
// run ask: the two limits may, and where a credential comes from, and
// where it is sent, is the operator's alone.
func sourceSetAsk(key string) AskClass {
	if key == SourceCallsPerHour || key == SourceUpdatesPerHour {
		return Askable
	}
	return Alone
}

func (w *wtx) setSource(ctx context.Context, name, key, value string, perHour int, actor string, by *approvedOn) (*Source, error) {
	src, err := w.sourceSetCheck(ctx, name, key, actor)
	if err != nil {
		return nil, err
	}
	if key == SourceBase {
		d, err := src.Declaration()
		if err != nil {
			return nil, err
		}
		if err := d.CheckBase(value); err != nil {
			return nil, err
		}
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
	case SourceBase:
		src.Base = value
	}
	if err := put(w.Tx, bSources, name, src); err != nil {
		return nil, err
	}
	data := map[string]any{"source": name, key: value}
	if by != nil {
		data["approved"] = by
	}
	return src, w.event("", "source_set", actor, data)
}

// sourceSetCheck is the check SetSource makes inside the write. A run
// ask uses the same permission and existence check for both its asker
// and the operator it would run as.
func (w *wtx) sourceSetCheck(ctx context.Context, name, key, actor string) (*Source, error) {
	if err := w.may(ctx, actor, ActSetSourceKey, Object{Kind: KindSource, Name: name, Ask: sourceSetAsk(key)}); err != nil {
		return nil, err
	}
	src := new(Source)
	if ok, err := get(w.Tx, bSources, name, src); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("source %s: %w", name, ErrNotFound)
	}
	return src, nil
}

// ignoredIn reports whether the source name ignores the sender id: its
// record names it among Ignored. A source the yard declares not, or
// whose declaration hears by no identity, ignores nothing: the entries
// of a declaration that lost its hearing lie inert until it hears again.
func ignoredIn(tx *bolt.Tx, name, sender string) bool {
	src := new(Source)
	if ok, err := get(tx, bSources, name, src); err != nil || !ok {
		return false
	}
	d, err := src.Declaration()
	if err != nil || !d.HearsByIdentity() {
		return false
	}
	return slices.Contains(src.Ignored, sender)
}

// ignoreSender records the sender id on the source's ignore list, so its
// later deliveries are dropped without a second card. It is the
// operator's alone to say (maySource), refused from an agent pane, and
// an id already on the list is kept once. Adding happens only by picking
// "nobody" on a mapping card, never by a command. It ends the mapping
// cards for the sender (retireMappingCards), also where the id was on
// the list already: the sender is settled either way.
func (w *wtx) ignoreSender(ctx context.Context, name, sender, actor string) error {
	if err := w.may(ctx, actor, ActIgnoreSender, Object{Kind: KindSource, Name: name, Ask: Askable}); err != nil {
		return err
	}
	src := new(Source)
	if ok, err := get(w.Tx, bSources, name, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("source %s: %w", name, ErrNotFound)
	}
	if !slices.Contains(src.Ignored, sender) {
		src.Ignored = append(src.Ignored, sender)
		slices.Sort(src.Ignored)
		if err := put(w.Tx, bSources, name, src); err != nil {
			return err
		}
	}
	return w.retireMappingCards(name, func(card string) bool { return card == sender }, "ignored", actor)
}

// unignoreSender removes the sender id from the source's ignore list,
// so its next delivery follows the hook's word again. It is the
// operator's alone to say (maySource), refused from an agent pane, as
// ignoring is; an id the list does not name is refused. by is the ask
// the change was approved on, nil for one typed at a terminal.
func (w *wtx) unignoreSender(ctx context.Context, name, sender, actor string, by *approvedOn) error {
	if err := w.may(ctx, actor, ActUnignoreSender, Object{Kind: KindSource, Name: name, Ask: Askable}); err != nil {
		return err
	}
	src := new(Source)
	if ok, err := get(w.Tx, bSources, name, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("source %s: %w", name, ErrNotFound)
	}
	if !slices.Contains(src.Ignored, sender) {
		return fmt.Errorf("source %s: %s is not ignored: %w", name, sender, ErrNotFound)
	}
	src.Ignored = slices.DeleteFunc(src.Ignored, func(id string) bool { return id == sender })
	if err := put(w.Tx, bSources, name, src); err != nil {
		return err
	}
	data := map[string]any{"source": name, "sender": sender}
	if by != nil {
		data["approved"] = by
	}
	return w.event("", "sender_unignored", actor, data)
}

// UnignoreSource removes the sender id from the source's ignore list
// (yardr source ignore <source> --rm <id>), so its next delivery is
// heard again: under unknown a fresh mapping card is offered, under
// allow it is heard as an outsider. It is the operator's alone, refused
// from an agent pane, and the same write an approval of the row's card
// runs.
func (s *Store) UnignoreSource(ctx context.Context, name, sender, actor string) error {
	return s.write(func(w *wtx) error {
		return w.unignoreSender(ctx, name, sender, actor, nil)
	})
}

// LimitsLine is how many calls of the source one caller makes in an hour,
// as yardr source set prints it after a limit was set.
func (s Source) LimitsLine() string {
	return fmt.Sprintf("%d calls an hour for each caller, %d of them updates at most", s.CallLimit(), s.UpdateLimit())
}

// SourceKeyed is what KeySource, KeyUser, KeyUserCredential or GiveSecret
// did: the names it wrote, the file they are in, and what the yard can say
// of those names without showing a value. A source credential has the state
// yardr source show reports; a hook signing secret says it is set; an
// address, credential for a user or crew, or harness key says which names
// are set. A giving to a waiting login holds the login's reference instead
// of a file.
type SourceKeyed struct {
	Source *Source  `json:"source"`
	Given  []string `json:"given"`
	File   string   `json:"file"`
	// Hook is the hook whose signing secret was given; set instead of a
	// credential's state, with File the hook's secret file.
	Hook string `json:"hook,omitempty"`
	// Login is the waiting login the value was handed to; set instead of a
	// file, and "" for a source's credential or a user's address.
	Login string `json:"login,omitempty"`
	// User is whose own address in the source was given, and whose file
	// File is; with Harness, whose login's key file File is. It is "" for
	// a source's credential and for a user's own credential (As). Next is
	// then the command that comes after it, in
	// a line: the one that tells the user through the source, or once
	// that is said the one that tries it.
	User string `json:"user,omitempty"`
	// Crew is whose own credential in a source was given, and
	// whose file File is; "" for a source's credential, a harness API key,
	// and a user's address. Next is then when the value takes effect, in a
	// line: the running session does not see it, so it is used when the next
	// one starts.
	Crew string `json:"crew,omitempty"`
	// Harness is the kind whose API key was given, and User then whose
	// login's key file of the kind File is (HarnessKeyFile). Next is when
	// it takes effect, in a line: as each session on that login next
	// starts.
	Harness string `json:"harness,omitempty"`
	// As is whose own credential in the source was given, and whose file
	// File is; "" for a source's credential and for a user's address.
	// Next is then the command that tries it, in a line: the source's
	// check, made as that user.
	As   string `json:"as,omitempty"`
	Next string `json:"next,omitempty"`
	// State is the yard's description of what the giving set: CredentialState
	// for a source credential, a setting sentence for another file-backed
	// secret. Missing is what a source still wants when the names given are
	// not all it needs, in the words of its error.
	State   string `json:"credential,omitempty"`
	Missing string `json:"missing,omitempty"`
}

// SecretNoAnswer is what every client says of a giving that ended without
// the yard's answer (ErrNoAnswer): the connection broke, and on which side
// of the file's write is not known. It is never said to be sent.
const SecretNoAnswer = "the yard did not answer. The value may be written. Give it again."

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
	return k.keyed(s.home), nil
}

// keyed is what a giving that was committed did, with the credential's
// state read as a call would read it, after the write: the words never
// hold a value.
func (k *sourceKey) keyed(home string) *SourceKeyed {
	out := &SourceKeyed{Source: k.src, Given: k.names, File: k.file, Hook: k.hook}
	if k.hook != "" {
		out.State = fmt.Sprintf("the signing secret for the hook %s is set in %s", k.hook, k.file)
		return out
	}
	if u := k.user; u != nil {
		if k.as {
			out.As = u.Name
			out.State = fmt.Sprintf("%s is set in %s", strings.Join(k.names, " and "), k.file)
			out.Next = fmt.Sprintf("yardr source check %s --as %s", k.src.Name, u.Name)
			return out
		}
		out.User = u.Name
		out.State = fmt.Sprintf("%s is set in %s", strings.Join(k.names, " and "), k.file)
		out.Next = fmt.Sprintf("yardr source check %s --user %s sends one test message", k.src.Name, u.Name)
		if u.Notify != k.src.Name {
			out.Next = fmt.Sprintf("yardr user set %s %s=%s tells %s through it", u.Name, UserNotify, k.src.Name, u.Name)
		}
		return out
	}
	if h := k.harness; h != nil {
		out.Harness, out.User = h.Kind, k.login
		label := h.Kind
		if h.Label != "" {
			label = h.Label
		}
		out.State = fmt.Sprintf("%s is set in %s", strings.Join(k.names, " and "), k.file)
		out.Next = fmt.Sprintf("every %s session that burns %s's login reads it from its next start", label, k.login)
		return out
	}
	if c := k.crew; c != nil {
		out.Crew = c.Name
		out.State = fmt.Sprintf("%s is set in %s", strings.Join(k.names, " and "), k.file)
		out.Next = fmt.Sprintf("it takes effect when %s next starts (rotation or yardr crew restart %s)", c.Name, c.Name)
		return out
	}
	if d, err := k.src.Declaration(); err == nil {
		out.State, err = d.CredentialState(k.src.EnvFile, d.MintArgv(home, k.src.MintCommand))
		if err != nil {
			out.Missing = err.Error()
		}
	}
	return out
}

// sourceKey is one giving of a source's credential, as keySource left it,
// of a user's own address in the source, as keyUser did (user is then
// set), of a user's own credential in it, as keyUser did for one (user
// and as are then set), of a crew member's own credential in a source
// (crew is then set), or of a harness API key (harness is then set). It
// outlives the transaction: a batch may run any of them twice, and renamed
// stays true once any run renamed the file.
type sourceKey struct {
	src  *Source
	user *User
	// as is set for a user's own credential in the source rather than
	// their address: keyed then says whose credential and the command
	// that tries it.
	as   bool
	crew *CrewMember
	// harness is the kind whose API key was given, and login the user
	// whose key file of the kind File is; nil for any other giving.
	harness *Harness
	login   string
	// hook is the hook whose signing secret was given, "" for a
	// credential: File is then the hook's secret file.
	hook    string
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
// and the file, and never a value. What is left then is the yard's own
// file failing under a committed write, marked (OperationalError), not a
// refusal of what was sent.
func (k *sourceKey) failed(err error) error {
	if !k.renamed {
		return err
	}
	return &config.OperationalError{Err: &KeyNotRecorded{Names: k.names, File: k.file, Err: err}}
}

// afterKeyRename fails a transaction after keySource renamed the file, for
// a test of what that leaves; nil outside one.
var afterKeyRename func() error

// checkURLCredential holds a value given for a credential that is the
// address to what a base is, refusing it where it is given instead of at
// the first call, which for a webhook is also the test. The refusal is
// phrased as the call's (doKeyed): what is wrong, and none of the value.
// A credential the declaration does not mark url is not an address and
// passes unchecked, as hook secrets do.
func checkURLCredential(d *source.Declaration, name string, values map[string]string) error {
	c := d.Credential
	if c == nil || !c.URL {
		return nil
	}
	names := []string{c.Env}
	if c.UpdateEnv != "" {
		names = append(names, c.UpdateEnv)
	}
	for _, n := range names {
		v, ok := values[n]
		if !ok {
			continue
		}
		if words := source.AddressWords(v); words != "" {
			return fmt.Errorf("source %s: the value of %s %s", name, n, words)
		}
	}
	return nil
}

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
	if err := w.may(ctx, actor, ActSetSourceKey, Object{Kind: KindSource, Name: name}); err != nil {
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
	if err := checkURLCredential(d, name, values); err != nil {
		return err
	}
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
		return w.deleteSource(ctx, name, actor)
	})
}

// deleteSource is DeleteSource inside the transaction, so that an
// approved run ask removes the source in the write that records its
// approval.
func (w *wtx) deleteSource(ctx context.Context, name, actor string) error {
	// Alone: removing a source also removes the files of its commands
	// from disk, which no write of the store takes back.
	if err := w.may(ctx, actor, ActRemoveSource, Object{Kind: KindSource, Name: name}); err != nil {
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
	return nil
}
