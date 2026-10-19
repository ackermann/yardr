package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bHooks        = []byte("hooks")
	bHookPayloads = []byte("hook_payloads") // <event seq>: the raw body as received
	bHookSeen     = []byte("hook_seen")     // <dedupe key>: seq of the event that recorded it
	bHookMentions = []byte("hook_mentions") // <source> 0 <entity>: who its text mentioned last
)

func init() { allBuckets = append(allBuckets, bHooks, bHookPayloads, bHookSeen, bHookMentions) }

// MaxHookPayload bounds a stored delivery; the receiver rejects larger
// bodies before they reach the store.
const MaxHookPayload = 1 << 20

// hookName is what may appear in /hooks/<name>.
var hookName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// HookSecretFile is the file the yard owns for the hook name's signing
// secret: <home>/hooks/<name>.secret. A hook made from a card names it,
// and a secret ask for the hook writes it (writeHookSecret); a hook made
// in a terminal names a file of the operator's instead.
func HookSecretFile(home, name string) string {
	return filepath.Join(home, "hooks", name+".secret")
}

// writeHookSecret writes the hook name's signing secret to its file
// under home (HookSecretFile), mode 0600, as WriteEnvFile writes an env
// file: whole to a second file beside it, synced and renamed into
// place, so a reader sees the old file or the new one. A file that is
// there is held to the rule it is read under and refused, not repaired,
// when it breaks it; so is a symbolic link. renamed says the rename was
// done. Errors name the hook and the file, never the value.
func writeHookSecret(home, name, value string) (file string, renamed bool, err error) {
	file = HookSecretFile(home, name)
	switch {
	case value == "":
		return file, false, fmt.Errorf("hook %s: the signing secret is empty, and nothing is written", name)
	case strings.ContainsAny(value, "\r\n"):
		return file, false, fmt.Errorf("hook %s: the signing secret has a line break in it, and nothing is written", name)
	case strings.TrimSpace(value) != value:
		return file, false, fmt.Errorf("hook %s: the signing secret begins or ends with white space, and nothing is written", name)
	}
	if st, err := os.Lstat(file); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return file, false, fmt.Errorf("hook %s: the secret file %s is a symbolic link, and the yard writes through none", name, file)
		}
		if !st.Mode().IsRegular() {
			return file, false, fmt.Errorf("hook %s: the secret file %s is not a regular file", name, file)
		}
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			return file, false, fmt.Errorf("hook %s: the secret file %s is open to others (mode %04o); chmod 600 it", name, file, perm)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return file, false, fmt.Errorf("hook %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return file, false, fmt.Errorf("hook %s: %w", name, err)
	}
	tmp := filepath.Join(filepath.Dir(file), "."+filepath.Base(file)+".new")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return file, false, fmt.Errorf("hook %s: %w", name, err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return file, false, fmt.Errorf("hook %s: %w", name, err)
	}
	err = func() error {
		defer f.Close()
		if err := f.Chmod(0o600); err != nil {
			return err
		}
		if _, err := f.WriteString(value); err != nil {
			return err
		}
		return f.Sync()
	}()
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
		return file, false, fmt.Errorf("hook %s: %w", name, err)
	}
	dir, err := os.Open(filepath.Dir(file))
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return file, true, fmt.Errorf("hook %s: %w", name, err)
	}
	return file, true, nil
}

// Hook is a named webhook endpoint. The secret stays in SecretFile, read
// when a delivery is verified, so it never enters the store, the event log
// or a command line.
type Hook struct {
	Name       string `json:"name"`
	Source     string `json:"source"` // token or a declared source
	SecretFile string `json:"secret_file"`
	// Blocked is why the receiver refuses this hook's deliveries (its
	// secret file is missing or open to others), empty while it takes them.
	// Only the receiver sets it, through BlockHook.
	Blocked string `json:"blocked,omitempty"`
	// Map says which status an issue of the source goes to when the bead
	// tracking it enters a stage: "<bead type>:<stage>" -> started, review
	// or done (see IssueMove). As is the crew member the change is made as:
	// with its credential, under its identity in the source, and only where
	// its definition allows it (SetState). As is also who the hook's says
	// outcomes are reported as (Store.Done by the dispatcher), so the
	// operator's own mapping crosses a stage of people the member belongs
	// to; without one the move is the yard's, which such a stage refuses.
	As  string            `json:"as,omitempty"`
	Map map[string]string `json:"map,omitempty"`
	// Says is the other direction: what the source's events say of the bead
	// they are about, as an outcome of it (see Say). The first line an event
	// matches is the one that counts; a hook without lines moves no bead.
	Says []Say `json:"says,omitempty"`
	// Makes is what the source's events make: a bead for a thread no open
	// bead tracks (see Make). The first line an event matches is the one
	// that counts; a hook without lines makes none.
	Makes []Make `json:"makes,omitempty"`
	// Accepted is when the hook last took a delivery (RecordHook), and
	// Refused when the receiver last refused one and logged it, with the
	// reason in RefusedFor (RejectHook). Refusals are logged at most once per
	// reason in a while, so Refused can trail the last refusal by that long.
	// Only the receiver's process sets them: PutHook keeps what the hook has.
	Accepted   *time.Time `json:"accepted,omitempty"`
	Refused    *time.Time `json:"refused,omitempty"`
	RefusedFor string     `json:"refused_for,omitempty"`
	// Unknown is what a hook whose source hears by identity does with a
	// sender no user maps: HookUnknownUnknown, HookUnknownAllow or
	// HookUnknownIgnore. It is refused for a source with no identity
	// hearing, and a hook for an identity-hearing source made without
	// it stores HookUnknownUnknown. Empty in a hook made before the
	// word existed reads as HookUnknownUnknown (UnknownPolicy).
	Unknown string `json:"unknown,omitempty"`
}

// The words a hook's Unknown takes, in the order a refusal names them.
const (
	HookUnknownUnknown = "unknown"
	HookUnknownAllow   = "allow"
	HookUnknownIgnore  = "ignore"
)

// HookUnknowns are the words a hook's Unknown takes.
var HookUnknowns = []string{HookUnknownUnknown, HookUnknownAllow, HookUnknownIgnore}

// ValidHookUnknown reports whether word is one a hook's Unknown takes.
func ValidHookUnknown(word string) bool { return slices.Contains(HookUnknowns, word) }

// SpeakerOutside is the speaker a kept delivery from an unmapped sender
// carries under HookUnknownAllow: fields.speaker holds it in place of
// a yard user's name.
const SpeakerOutside = "outside"

// SpeakerKey is the derived filter key hearing by identity adds beside
// a declaration's hook.keys: a subscription may filter on it for a
// source hearing by identity alone.
const SpeakerKey = "speaker"

// UnknownPolicy is the hook's Unknown word as the receiver reads it:
// empty, in a hook made before the word existed, is HookUnknownUnknown.
func (h Hook) UnknownPolicy() string {
	if h.Unknown != "" {
		return h.Unknown
	}
	return HookUnknownUnknown
}

// LastRefused reports whether the hook's last delivery the yard knows of
// was refused: it refused one and has accepted none since.
func (h Hook) LastRefused() bool {
	return h.Refused != nil && (h.Accepted == nil || h.Refused.After(*h.Accepted))
}

// HookRejected is the kind of the event a refused delivery is logged as
// (RejectHook).
const HookRejected = "hook_rejected"

// ThreadStates are the words a thread's status is asked for by, in the
// order work passes through them (hooks.States, which a test holds equal).
var ThreadStates = []string{"started", "review", "done"}

// ThreadLabelPrefix begins a word that is no state but a label:
// label:<name> asks for the label <name> on the thread, in a source whose
// threads take labels.
const ThreadLabelPrefix = "label:"

// ThreadLabel is the label a word asks for, and whether it is such a word:
// label:<name> with a name, which is kept as it is spelled.
func ThreadLabel(word string) (string, bool) {
	name, ok := strings.CutPrefix(word, ThreadLabelPrefix)
	if !ok || name == "" || name != strings.TrimSpace(name) {
		return "", false
	}
	return name, true
}

// MapKey is the key of a Hook's Map for beads of type typ entering stage.
func MapKey(typ, stage string) string { return typ + ":" + stage }

// checkMap refuses a hook whose map could never be acted on: one without
// the crew member to act as, a line that names no bead type and stage, or a
// word that is neither a state nor a label. Whether the hook's source has a
// status for the word is its adapter's to say (hooks.Forwarder's Places).
func (h Hook) checkMap() error {
	if len(h.Map) > 0 && h.As == "" {
		return fmt.Errorf("hook %s: a map needs the crew member it acts as (--as): the issue is moved with that crew member's credential, under its identity", h.Name)
	}
	for k, word := range h.Map {
		typ, stage, ok := strings.Cut(k, ":")
		if !ok {
			return fmt.Errorf("hook %s: map %q: want <bead type>:<stage>=<state>", h.Name, k+"="+word)
		}
		if err := checkName("hook "+h.Name+": map "+k+": bead type", typ); err != nil {
			return err
		}
		if err := checkName("hook "+h.Name+": map "+k+": stage", stage); err != nil {
			return err
		}
		if _, label := ThreadLabel(word); !label && !slices.Contains(ThreadStates, word) {
			return fmt.Errorf("hook %s: map %s=%s: %q is no state: want %s", h.Name, k, word, word, strings.Join(ThreadStates, ", "))
		}
	}
	return nil
}

// Say is one line of a hook's table of what its source's events say (Hook's
// Says): an event the filter Where matches reports Outcome for the bead it
// is about, as a session would with yardr bead done --outcome, and the
// bead's flow routes it. Note begins the note the move adds.
//
// Where is a subscription's filter (Subscription's Where: exact matches,
// ANDed, a key with several values matching any) over the fields of the
// source's envelope, and two more keys: stage, the stage the bead is in
// when the event is judged, for a line that holds in some stages only,
// and type, the bead's type, for a line that holds for some types only
// (a train, or the bead that tracks the thread). The event's own type is
// named with event there.
type Say struct {
	Where   map[string]string `json:"where"`
	Outcome string            `json:"outcome"`
	Note    string            `json:"note,omitempty"`
}

// SayStage is the key of a Say's filter that the bead's stage is matched
// on. SayType is the key the bead's type is matched on; the event's type
// is named with event. The others are the source's own (whereKeys).
const SayStage = "stage"

// SayType is the key of a Say's filter that the bead's type is matched on.
const SayType = "type"

// String is the line as yardr hook add --says takes it (ParseSay).
func (s Say) String() string {
	out := whereString(s.Where) + " -> " + s.Outcome
	if s.Note != "" {
		out += ": " + s.Note
	}
	return out
}

// same reports whether o is the same line.
func (s Say) same(o Say) bool {
	return maps.Equal(s.Where, o.Where) && s.Outcome == o.Outcome && s.Note == o.Note
}

// SameSays reports whether two tables hold the same lines in the same
// order, which is part of what a table says: the first match counts.
func SameSays(a, b []Say) bool { return slices.EqualFunc(a, b, Say.same) }

// SayLines are a table's lines as Say's String writes them.
func SayLines(says []Say) []string {
	out := make([]string, len(says))
	for i, s := range says {
		out[i] = s.String()
	}
	return out
}

// ParseSay reads a line written "<key>=<value> ... -> <outcome>", with
// ": <note>" after it when it has one.
func ParseSay(line string) (Say, error) {
	where, rest, ok := strings.Cut(line, "->")
	if !ok {
		return Say{}, fmt.Errorf("%q: want <key>=<value> ... -> <outcome>, e.g. event=pull_request action=closed merged=true -> merged", line)
	}
	s := Say{}
	s.Outcome, s.Note, _ = strings.Cut(strings.TrimSpace(rest), ":")
	s.Outcome, s.Note = strings.TrimSpace(s.Outcome), strings.TrimSpace(s.Note)
	var err error
	if s.Where, err = ParseWhere(where); err != nil {
		return Say{}, err
	}
	return s, nil
}

// Make is one line of a hook's table of what its source's events make
// (Hook's Makes): an event the filter Where matches, about a thread no open
// bead tracks, makes a bead of Type in Stage that tracks the thread, so that
// what comes from outside enters the yard and the thread's later events find
// it (Say). Depot is where it is made; without one it is the depot whose
// repository the event is in.
//
// Where is a subscription's filter over the fields of the source's
// envelope, as a Say's is, without stage and without type: there is no bead
// yet to be in one or of one, and the line's bead already says the type it
// makes. The event's own type is named with event there, as in a Say's.
type Make struct {
	Where map[string]string `json:"where"`
	Type  string            `json:"type"`
	Stage string            `json:"stage"`
	Depot string            `json:"depot,omitempty"`
}

// String is the line as yardr hook add --makes takes it (ParseMake).
func (m Make) String() string {
	out := whereString(m.Where) + " -> " + m.Type + ":" + m.Stage
	if m.Depot != "" {
		out += "@" + m.Depot
	}
	return out
}

// same reports whether o is the same line.
func (m Make) same(o Make) bool {
	return maps.Equal(m.Where, o.Where) && m.Type == o.Type && m.Stage == o.Stage && m.Depot == o.Depot
}

// SameMakes reports whether two tables hold the same lines in the same
// order, which is part of what a table says: the first match counts.
func SameMakes(a, b []Make) bool { return slices.EqualFunc(a, b, Make.same) }

// MakeLines are a table's lines as Make's String writes them.
func MakeLines(makes []Make) []string {
	out := make([]string, len(makes))
	for i, m := range makes {
		out[i] = m.String()
	}
	return out
}

// ParseMake reads a line written "<key>=<value> ... -> <type>:<stage>", with
// "@<depot>" after it when it names one.
func ParseMake(line string) (Make, error) {
	where, rest, ok := strings.Cut(line, "->")
	bead, depot, named := strings.Cut(strings.TrimSpace(rest), "@")
	typ, stage, has := strings.Cut(bead, ":")
	typ, stage, depot = strings.TrimSpace(typ), strings.TrimSpace(stage), strings.TrimSpace(depot)
	if !ok || !has || typ == "" || stage == "" || (named && depot == "") {
		return Make{}, fmt.Errorf("%q: want <key>=<value> ... -> <type>:<stage>[@<depot>], e.g. event=issues action=opened -> issue:triage", line)
	}
	m := Make{Type: typ, Stage: stage, Depot: depot}
	var err error
	if m.Where, err = ParseWhere(where); err != nil {
		return Make{}, err
	}
	return m, nil
}

// Unstaged says why no bead could be made in the line's stage under a flow
// with the stages given, the non-terminal ones of the flow the line's type
// follows in a depot: nil when the stage is among them. A line that fails
// this would be refused at every event it matches.
func (m Make) Unstaged(stages []string) error {
	if slices.Contains(stages, m.Stage) {
		return nil
	}
	return fmt.Errorf("no stage %s a bead can be made in (stages: %s)", m.Stage, or(strings.Join(stages, ", "), "none"))
}

// CheckMakes refuses a line that could match nothing or everything, or make
// nothing: a filter that is empty or names a key the source's envelope has
// not, and a type, stage or depot that is no name. Whether the type's flow
// has the stage is the caller's to ask (Make's Unstaged): the flows are the
// depots', and may change after the hook is saved.
func (h Hook) CheckMakes() error { return h.checkMakes(whereKeys(h.Source)) }

// CheckMakesKeys is CheckMakes against keys: for a caller that knows the
// source's keys itself, a declared source's hook keys beside the common
// ones, without a hook to check them on yet.
func (h Hook) CheckMakesKeys(keys []string) error { return h.checkMakes(keys) }

// checkMakes is CheckMakes against keys: the store passes the keys of a
// declared source's hook beside them.
func (h Hook) checkMakes(keys []string) error {
	for _, m := range h.Makes {
		if len(m.Where) == 0 {
			return fmt.Errorf("hook %s: makes -> %s:%s: no filter: a line says which events it is for (event=issues action=opened)", h.Name, m.Type, m.Stage)
		}
		if typ, ok := m.Where[SayType]; ok {
			return fmt.Errorf("hook %s: makes %s: type=%s is redundant with the bead the line makes (-> %s:%s): the line makes a bead of that type, so it needs no filter on it; the event's own type is named with event", h.Name, m, WhereValues(typ)[0], m.Type, m.Stage)
		}
		for _, k := range slices.Sorted(maps.Keys(m.Where)) {
			if !slices.Contains(keys, k) {
				return fmt.Errorf("hook %s: makes %s: cannot filter %s on %q: use %s", h.Name, m, h.Source, k, strings.Join(keys, ", "))
			}
			if slices.Contains(WhereValues(m.Where[k]), "") {
				return fmt.Errorf("hook %s: makes %s: empty value for %s", h.Name, m, k)
			}
		}
		what := "hook " + h.Name + ": makes " + m.String() + ": "
		if err := checkName(what+"bead type", m.Type); err != nil {
			return err
		}
		if NotWork[m.Type] {
			return fmt.Errorf("%sa bead of type %s follows no flow, so it has no stage to be made in", what, m.Type)
		}
		if err := checkName(what+"stage", m.Stage); err != nil {
			return err
		}
		if m.Depot != "" {
			if err := checkName(what+"depot", m.Depot); err != nil {
				return err
			}
		}
	}
	return nil
}

// Made is the first line of the hook's table of what its events make that
// the event with the envelope data (a hook event's Data) matches, nil when
// none does.
func (h Hook) Made(data map[string]any) *Make {
	if len(h.Makes) == 0 {
		return nil
	}
	fields := hookFields(data)
	for i, m := range h.Makes {
		if match(Subscription{Scope: ScopeAll, Where: m.Where}, fields, given(false), given(false), given(false)) != "" {
			return &h.Makes[i]
		}
	}
	return nil
}

// Unrouted says why no bead could take the line's outcome under flows whose
// stages have the outcomes given (stage -> its outcomes), nil when one
// could: the stage the line names has the outcome, or, for a line that
// names none, some stage has. A line that fails this would be refused by
// the flow at every event it matches.
func (s Say) Unrouted(stages map[string][]string) error {
	if named, ok := s.Where[SayStage]; ok {
		for _, stage := range WhereValues(named) {
			outs, has := stages[stage]
			if !has {
				return fmt.Errorf("no stage %s (stages: %s)", stage, strings.Join(slices.Sorted(maps.Keys(stages)), ", "))
			}
			if !slices.Contains(outs, s.Outcome) {
				return fmt.Errorf("stage %s has no outcome %s (it has: %s)", stage, s.Outcome, or(strings.Join(outs, ", "), "none"))
			}
		}
		return nil
	}
	for _, outs := range stages {
		if slices.Contains(outs, s.Outcome) {
			return nil
		}
	}
	return fmt.Errorf("no stage has an outcome %s", s.Outcome)
}

// CheckSays refuses a line that could match nothing or everything, or
// report nothing: a filter that is empty or names a key the source's
// envelope has not, and an outcome that is no outcome's name. Whether a
// flow routes the outcome is the caller's to ask (Say's Unrouted): the
// flows are the depots', and may change after the hook is saved.
func (h Hook) CheckSays() error { return h.checkSays(append(whereKeys(h.Source), SayStage)) }

// CheckSaysKeys is CheckSays against keys: for a caller that knows the
// source's keys itself, with SayStage among them.
func (h Hook) CheckSaysKeys(keys []string) error { return h.checkSays(keys) }

// checkSays is CheckSays against keys: the store passes the keys of a
// declared source's hook beside them.
func (h Hook) checkSays(keys []string) error {
	for _, s := range h.Says {
		if len(s.Where) == 0 {
			return fmt.Errorf("hook %s: says -> %s: no filter: a line says which events it is for (event=pull_request action=closed)", h.Name, s.Outcome)
		}
		for _, k := range slices.Sorted(maps.Keys(s.Where)) {
			if !slices.Contains(keys, k) {
				return fmt.Errorf("hook %s: says %s: cannot filter %s on %q: use %s", h.Name, s, h.Source, k, strings.Join(keys, ", "))
			}
			if slices.Contains(WhereValues(s.Where[k]), "") {
				return fmt.Errorf("hook %s: says %s: empty value for %s", h.Name, s, k)
			}
		}
		if typ, ok := s.Where[SayType]; ok {
			for _, v := range WhereValues(typ) {
				if err := checkName("hook "+h.Name+": says "+s.String()+": bead type", v); err != nil {
					return err
				}
			}
		}
		if !validOutcome.MatchString(s.Outcome) {
			return fmt.Errorf("hook %s: says %s: outcome %q: use letters, digits, '.', '_' or '-'", h.Name, s, s.Outcome)
		}
	}
	return nil
}

// checkCI refuses a hook whose table reads CI as state (an event=ci line)
// of a declared source without what the read runs with: the source's ci
// command, and the crew member it acts as, whose credential reads. A hook
// of a source the yard does not declare is not refused here: the read
// fails with the setup named, as every other read of it does.
func (h Hook) checkCI(tx *bolt.Tx) error {
	reads := false
	for _, s := range h.Says {
		if slices.Contains(WhereValues(s.Where["event"]), "ci") {
			reads = true
			break
		}
	}
	if !reads {
		return nil
	}
	var src Source
	ok, err := get(tx, bSources, h.Source, &src)
	if err != nil || !ok {
		return nil
	}
	d, err := src.Declaration()
	if err != nil {
		return nil // unreadable: the read fails with why, as every other read of it does
	}
	if d.Events == nil || len(d.Events.CI) == 0 {
		return fmt.Errorf("hook %s: its event=ci line reads CI through source %s's ci command, and it declares none (events.ci in the declaration)", h.Name, h.Source)
	}
	if h.As == "" {
		return fmt.Errorf("hook %s: its event=ci line is read with the crew member the hook acts as, and it acts as none (--as)", h.Name)
	}
	return nil
}

// Said is the first line of the hook's table that the event with the
// envelope data (a hook event's Data) matches for a bead of beadType in
// stage, nil when none does. A line with type matches only such beads.
func (h Hook) Said(data map[string]any, stage, beadType string) *Say {
	if len(h.Says) == 0 {
		return nil
	}
	fields := hookFields(data)
	fields[SayStage] = []string{stage}
	fields[SayType] = []string{beadType}
	for i, s := range h.Says {
		if match(Subscription{Scope: ScopeAll, Where: s.Where}, fields, given(false), given(false), given(false)) != "" {
			return &h.Says[i]
		}
	}
	return nil
}

// PutHook creates or replaces a hook. A replaced hook starts unblocked: the
// receiver finds out again on the next delivery. It keeps when it last
// accepted and refused a delivery, which the caller cannot set. A hook that acts as a
// crew member (As) is refused when there is no such crew member, or when it has
// no identity in the hook's source: the source's delivery of a change made
// as the crew member is kept from the crew member's inbox by that identity alone.
// Adding a hook, or changing one's source or secret file, is the
// operator's to do (operatorOnly); what one says, makes, maps or acts as is
// anyone's to change, recorded from a session's or a crew member's pane as
// that session's actor or that crew member, whatever actor says, and from
// anywhere else as given (paneAuthor). A hook that acts as a crew member
// still speaks as it (maySpeakAs).
func (s *Store) PutHook(ctx context.Context, h Hook, actor string) (*Hook, error) {
	unlock := s.askLocks.lock(hookLockKey(h.Name))
	defer unlock()
	var out *Hook
	err := s.write(func(w *wtx) error {
		var err error
		out, err = w.putHook(ctx, h, actor, nil, Alone)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// putHook is PutHook inside the transaction, so that an approved run
// ask adds the hook in the write that records its approval: by is then
// the ask it was approved on, which the event carries, and add is the
// mark its new-hook check carries (Alone from a terminal, Askable from
// a card: a new hook a crew member may propose, a replacement never).
func (w *wtx) putHook(ctx context.Context, h Hook, actor string, by *approvedOn, add AskClass) (*Hook, error) {
	h.Blocked = ""
	if !hookName.MatchString(h.Name) {
		return nil, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", h.Name)
	}
	if h.Source == "" || h.SecretFile == "" {
		return nil, fmt.Errorf("hook %s: source and secret file are required", h.Name)
	}
	if err := h.checkMap(); err != nil {
		return nil, err
	}
	if len(h.Says) == 0 {
		h.Says = nil
	}
	if len(h.Makes) == 0 {
		h.Makes = nil
	}
	{
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return nil, err
		}
		actor = resolved
		var was Hook
		had, err := get(w.Tx, bHooks, h.Name, &was)
		if err != nil {
			return nil, err
		}
		switch {
		case !had:
			if err := w.operatorOnly(ctx, actor, "add a hook", add); err != nil {
				return nil, err
			}
		case was.Source != h.Source || was.SecretFile != h.SecretFile:
			if err := w.operatorOnly(ctx, actor, "change a hook's source or secret file", Alone); err != nil {
				return nil, err
			}
		}
		// The unknown word holds only for a source hearing by identity:
		// there is a sender to be unknown there, and nowhere else.
		identityHearing := false
		if src, d, err := sourceIn(w.Tx, h.Source); err == nil && src != nil {
			_ = src
			identityHearing = d.HearsByIdentity()
		}
		if h.Unknown != "" && !ValidHookUnknown(h.Unknown) {
			return nil, fmt.Errorf("hook %s: --unknown %q: want %s", h.Name, h.Unknown, strings.Join(HookUnknowns, ", "))
		}
		if h.Unknown != "" && !identityHearing {
			return nil, fmt.Errorf("hook %s: --unknown %s for a source with no identity hearing: source %s hears no sender by identity, so there is no unmapped sender to name a policy for", h.Name, h.Unknown, h.Source)
		}
		if identityHearing && h.Unknown == "" {
			h.Unknown = HookUnknownUnknown
		}
		keys := whereKeysTx(w.Tx, h.Source)
		if err := h.checkSays(append(slices.Clone(keys), SayStage)); err != nil {
			return nil, err
		}
		if err := h.checkMakes(keys); err != nil {
			return nil, err
		}
		if err := h.checkCI(w.Tx); err != nil {
			return nil, err
		}
		data := map[string]any{"hook": h.Name, "source": h.Source}
		if identityHearing {
			data["unknown"] = h.Unknown
		}
		if by != nil {
			data["approved"] = by
		}
		if h.As != "" {
			var r CrewMember
			if ok, err := get(w.Tx, bCrew, h.As, &r); err != nil {
				return nil, err
			} else if !ok {
				return nil, fmt.Errorf("hook %s: --as %s: crew member %s: %w", h.Name, h.As, h.As, ErrNotFound)
			}
			// A hook that acts as a crew member speaks as it.
			if err := maySpeakAs(ctx, w.Tx, h.As, actor); err != nil {
				return nil, fmt.Errorf("hook %s: %w", h.Name, err)
			}
			if r.Identities[h.Source].zero() {
				return nil, fmt.Errorf("hook %s: --as %s: crew member %s has no identity in %s, so the delivery of a change made as it would reach its own inbox: yardr crew identity %s --source %s --id <its id there>",
					h.Name, h.As, h.As, h.Source, h.As, h.Source)
			}
			data["as"] = h.As
		}
		if len(h.Map) > 0 {
			data["map"] = h.Map
		}
		if len(h.Says) > 0 {
			data["says"] = SayLines(h.Says)
		}
		if len(h.Makes) > 0 {
			data["makes"] = MakeLines(h.Makes)
		}
		h.Accepted, h.Refused, h.RefusedFor = was.Accepted, was.Refused, was.RefusedFor
		if err := put(w.Tx, bHooks, h.Name, h); err != nil {
			return nil, err
		}
		if err := w.event("", "hook_added", actor, data); err != nil {
			return nil, err
		}
		return &h, nil
	}
}

func (s *Store) Hooks(ctx context.Context) ([]Hook, error) {
	out := []Hook{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bHooks, "", func(_ []byte, h *Hook) error {
			out = append(out, *h)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetHook(ctx context.Context, name string) (*Hook, error) {
	h := new(Hook)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bHooks, name, h)
		if err == nil && !ok {
			err = fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// DeleteHook removes the hook; deliveries it already accepted stay. The
// issue moves it had still to make (IssueMoves) are dropped with it, and
// the hook_removed event says how many. Removing a hook is the operator's
// to do (operatorOnly).
func (s *Store) DeleteHook(ctx context.Context, name, actor string) error {
	unlock := s.askLocks.lock(hookLockKey(name))
	defer unlock()
	return s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		if err := w.operatorOnly(ctx, actor, "remove a hook", Alone); err != nil {
			return err
		}
		if w.Bucket(bHooks).Get([]byte(name)) == nil {
			return fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		if err := w.Bucket(bHooks).Delete([]byte(name)); err != nil {
			return err
		}
		data := map[string]any{"hook": name}
		// What the hook had still to do is not done behind the back of
		// whoever removed it.
		dropped, err := dropIssueMoves(w.Tx, name)
		if err != nil {
			return err
		}
		if dropped > 0 {
			data["issue_moves_dropped"] = dropped
		}
		return w.event("", "hook_removed", actor, data)
	})
}

// BlockHook records that the receiver refuses the hook's deliveries, and
// why, or with an empty reason that it takes them again. One `hook_blocked`
// event is logged when the state begins, not for every delivery refused
// while it lasts; it carries the hook, the path and the reason, never what
// the file holds.
//
// Like RecordHook it is not on the API: only the receiver sees the file as
// the deliveries do.
func (s *Store) BlockHook(ctx context.Context, name, reason string) error {
	return s.write(func(w *wtx) error {
		var h Hook
		if ok, err := get(w.Tx, bHooks, name, &h); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		if h.Blocked == reason {
			return nil
		}
		began := h.Blocked == ""
		h.Blocked = reason
		if err := put(w.Tx, bHooks, name, h); err != nil {
			return err
		}
		if !began {
			return nil
		}
		return w.event("", "hook_blocked", "hook:"+name, map[string]any{"hook": name, "path": h.SecretFile, "reason": reason})
	})
}

// RejectHook logs deliveries the receiver refused as one `hook_rejected`
// event: the name they were sent to (hook), why (reason, the receiver's
// word), the status answered, how many were refused since the last such
// event for that name and reason (count) and when the last of them was
// (last). It never carries a body, a header or the secret. A name that is a
// hook's has the time and the reason kept on the hook (Refused), for `yardr
// hook list` and doctor; a name that is no hook's is logged all the same,
// since a source sending to a mistyped URL is what the event is for.
//
// The receiver bounds how often it calls this (hooks.RejectWindow), so a
// scanner cannot grow the log. Like RecordHook it is not on the API: no
// client can log a refusal that did not happen.
func (s *Store) RejectHook(ctx context.Context, name, reason string, status, count int, last time.Time) error {
	return s.write(func(w *wtx) error {
		last := last.UTC().Truncate(time.Microsecond)
		actor := "yard"
		var h Hook
		if ok, err := get(w.Tx, bHooks, name, &h); err != nil {
			return err
		} else if ok {
			actor = "hook:" + name
			if h.Refused == nil || last.After(*h.Refused) {
				h.Refused, h.RefusedFor = &last, reason
				if err := put(w.Tx, bHooks, name, h); err != nil {
					return err
				}
			}
		}
		return w.event("", HookRejected, actor, map[string]any{
			"hook": name, "reason": reason, "status": status, "count": count, "last": last.Format(time.RFC3339)})
	})
}

// RecordHook logs an accepted delivery as one `hook` event carrying the
// normalized envelope, and keeps the raw payload beside it under the
// event's seq. The envelope is what routing reads; the payload is there
// for `yardr hook show` when the envelope is not enough. Which crew members it
// reaches, and why, is decided here against their subscriptions as they
// are now, and kept on the event as "deliveries"; the users it reaches are
// kept as "notified" and each gets a notification. A crew member that is the
// delivery's actor becomes a participant in its thread from then on.
//
// dedupe are the keys that identify this delivery (see hooks.DedupeKeys).
// When any of them was recorded before, nothing is written and RecordHook
// returns that delivery's seq with duplicate set. The check and the write
// share one transaction, so of two concurrent redeliveries exactly one is
// recorded.
//
// When the envelope names an entity (an issue whose description persists
// across deliveries), only the mentions that entity's text did not already
// carry count as mentions for routing; the full list stays on the event.
//
// RecordHook is not on the API: only the webhook receiver, in the process
// that owns the store, records deliveries, so no client can forge one.
func (s *Store) RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte, dedupe []string) (seq uint64, duplicate bool, err error) {
	if len(payload) > MaxHookPayload {
		return 0, false, fmt.Errorf("hook %s: payload of %d bytes exceeds %d", name, len(payload), MaxHookPayload)
	}
	err = s.write(func(w *wtx) error {
		seq, duplicate = 0, false // a batched write may run this twice
		seen := w.Bucket(bHookSeen)
		for _, k := range dedupe {
			if v := seen.Get([]byte(k)); v != nil {
				seq, duplicate = binary.BigEndian.Uint64(v), true
				return nil
			}
		}
		data := map[string]any{"hook": name}
		for k, v := range envelope {
			data[k] = v
		}
		var err error
		if seq, err = w.delivered("hook:"+name, data); err != nil {
			return err
		}
		for _, k := range dedupe {
			if err := seen.Put([]byte(k), []byte(seqKey(seq))); err != nil {
				return err
			}
		}
		// A delivery under a name that is no hook's (a test's, a hook removed
		// meanwhile) has no record to say so on.
		var h Hook
		if ok, err := get(w.Tx, bHooks, name, &h); err != nil {
			return err
		} else if ok {
			at := now()
			h.Accepted = &at
			if err := put(w.Tx, bHooks, name, h); err != nil {
				return err
			}
		}
		return w.Bucket(bHookPayloads).Put([]byte(seqKey(seq)), payload)
	})
	return seq, duplicate, err
}

// delivered logs the delivery with the envelope data as one hook event by
// actor and returns its seq: the routing every delivery goes through, a
// hook's (RecordHook) and one of the source peer (peerDelivered), which no
// hook received and whose data so names none. Whom it reaches is decided
// against the subscriptions as they are now and kept on the event; the
// users among them each get a notification.
func (w *wtx) delivered(actor string, data map[string]any) (uint64, error) {
	if err := freshMentions(w.Tx, data); err != nil {
		return 0, err
	}
	deliveries, told, err := hookDeliveries(w.Tx, data)
	if err != nil {
		return 0, err
	}
	data["deliveries"] = deliveries
	if len(told) > 0 {
		data["notified"] = told
	}
	if err := recordParticipation(w.Tx, data); err != nil {
		return 0, err
	}
	if err := w.event("", "hook", actor, data); err != nil {
		return 0, err
	}
	seq := w.Bucket(bEvents).Sequence()
	if len(told) > 0 {
		var e Event
		if _, err := get(w.Tx, bEvents, seqKey(seq), &e); err != nil {
			return 0, err
		}
		if err := w.notify(hookNotification(seq, e.At, data), told); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// freshMentions diffs the mentions of a delivery that names an entity
// against those the entity's text carried last time, and keeps the new set.
// Routing then reads "new_mentions", so a crew member mentioned in an issue's
// description, or in a comment, is told once, not again on every status
// or label change nor on every edit of the comment.
func freshMentions(tx *bolt.Tx, data map[string]any) error {
	entity, _ := data["entity"].(string)
	source, _ := data["source"].(string)
	if entity == "" {
		return nil
	}
	key := []byte(join(source, entity))
	b := tx.Bucket(bHookMentions)
	var prev []string
	if raw := b.Get(key); raw != nil {
		if err := json.Unmarshal(raw, &prev); err != nil {
			return err
		}
	}
	now := strs(data["mentions"])
	fresh := []string{}
	for _, m := range now {
		if !slices.Contains(prev, m) {
			fresh = append(fresh, m)
		}
	}
	data["new_mentions"] = fresh
	// The entity is gone (remove, deleted): forget what
	// it mentioned, so nothing is held against an id that comes back.
	if action, _ := data["action"].(string); action == "remove" || action == "deleted" {
		return b.Delete(key)
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return err
	}
	return b.Put(key, raw)
}

// HookPayload returns the raw body of the delivery logged as event seq.
func (s *Store) HookPayload(ctx context.Context, seq uint64) ([]byte, error) {
	var out []byte
	err := s.read(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bHookPayloads).Get([]byte(seqKey(seq)))
		if raw == nil {
			return fmt.Errorf("hook payload for event %d: %w", seq, ErrNotFound)
		}
		out = append([]byte{}, raw...)
		return nil
	})
	return out, err
}
