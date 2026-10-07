package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/source"
)

// bSources holds the declared sources, <name>: services outside the yard
// that were added by a file (see internal/source). An older build never
// opens the bucket and no other record names a source yet, so it came
// without a new schema version.
var bSources = []byte("sources")

func init() { allBuckets = append(allBuckets, bSources) }

// The settings of a declared source: the file its credential is read from,
// and how many calls of it one caller makes in an hour (SourceCallLimit
// when unset).
const (
	SourceEnvFile      = "env_file"
	SourceCallsPerHour = "calls_per_hour"
)

// SourceSettings are the settings a source has, as yardr source set names
// them.
var SourceSettings = []string{SourceEnvFile, SourceCallsPerHour}

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
	// CallsPerHour is how many calls of the source one session or crew
	// member makes within SourceCallWindow; 0 for SourceCallLimit.
	CallsPerHour int       `json:"calls_per_hour,omitempty"`
	AddedAt      time.Time `json:"added_at"`
	AddedBy      string    `json:"added_by"`
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

// maySource refuses a change to the declared sources by anyone but the
// operator. A source is where a credential of the yard is sent: whoever
// may replace its file can point the next call, and the credential with
// it, at an address of their own. And its file may name a command the yard
// runs (limits), as the user the daemon runs as. Who asks is told by the store's own
// records before the caller's word, as for the release of a peer's bead
// (mayRelease): a call from the pane of a bead's session or of a crew
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
		src.EnvFile, src.CallsPerHour = was.EnvFile, was.CallsPerHour
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
	return src, nil
}

// SetSource sets a setting of the source name: SourceEnvFile, to the path
// of the file its credential is read from, or to "" for none; or
// SourceCallsPerHour, to a whole number from 1, or to "" for
// SourceCallLimit. The path is kept as given: whoever calls resolves it,
// since the server may run in another directory.
func (s *Store) SetSource(ctx context.Context, name, key, value, actor string) (*Source, error) {
	perHour := 0
	switch key {
	case SourceEnvFile:
	case SourceCallsPerHour:
		if value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("source %s: %s=%q: want a whole number from 1, or nothing for %d", name, key, value, SourceCallLimit)
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
		if key == SourceEnvFile {
			src.EnvFile = value
		} else {
			src.CallsPerHour = perHour
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
