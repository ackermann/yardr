package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
)

// bSettings holds the yard's own defaults, key -> value (a JSON string),
// such as default_kind, the agent kind a herdr group or crew member gets when
// its config names none. Which keys exist and what each accepts is
// Settings' table below, which SetSetting holds a write to.
var bSettings = []byte("settings")

func init() { allBuckets = append(allBuckets, bSettings) }

// YardmasterSetting names the crew member that coordinates the yard, whom the
// role prompts tell agents to mail.
const YardmasterSetting = "yardmaster"

// The settings whose readers live in packages that import this one: named
// here so that the table can list them, and there under their own names.
const (
	// StyleSetting is how agents should work with the operator, one of
	// OperatorStyles, and NoteSetting one line they should know about them.
	StyleSetting = "operator_style"
	NoteSetting  = "operator_note"
	// DefaultKindSetting is the agent kind of herdr groups and crew
	// members whose config names none.
	DefaultKindSetting = "default_kind"
	// DefaultPackSetting names the pack a depot is set up from when depot
	// init is given none.
	DefaultPackSetting = "default_pack"
	// HookAddrSetting is the address the server takes webhooks on.
	HookAddrSetting = "hook_addr"
	// NameSetting is the yard's name.
	NameSetting = "name"
	// MaxSessionsSetting caps the sessions that run on this machine at
	// once, over all groups (MaxSessions).
	MaxSessionsSetting = "max_sessions"
	// HerdrSessionSetting names the herdr session the yard's panes are in.
	HerdrSessionSetting = "herdr_session"
)

// herdrSessionName is what herdr 0.9 takes as a session's name.
var herdrSessionName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// MaxSessions is the max_sessions of settings: the most sessions that run
// something on this machine (RunsHere) the dispatcher has going at once,
// whatever their groups' limits allow; 0, as when it is unset, is no cap.
func MaxSessions(settings map[string]string) int {
	n, _ := strconv.Atoi(settings[MaxSessionsSetting])
	return max(n, 0)
}

// OperatorStyles are the values of operator_style, from the most explaining
// to the least. What each means is the CLI's to say (its form and primes).
var OperatorStyles = []string{"guide", "show", "work", "report"}

// packFile is the file that makes a directory a pack (pack.File, which
// imports this package).
const packFile = "pack.yaml"

// Setting is one of the yard's settings: what it means, what it accepts,
// and what the yard goes by while it is unset.
type Setting struct {
	Key  string
	Help string
	// Default is the value an unset setting reads as; "" when there is
	// none, or when it is no one value (Help says what it is then).
	Default string
	// Env is the environment variable that is used instead of the setting
	// where it is set, without its YARDR_ prefix (config.Getenv).
	Env string
	// Check refuses a value the setting's readers would not take.
	Check func(string) error
}

// partyName is what names a crew member, a group or the yard.
var partyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// KnownSettings are the yard's settings. A key that is not here is read by
// nothing, and SetSetting refuses it.
var KnownSettings = []Setting{
	{Key: OperatorSetting, Help: "who the role prompts name, the person agents bring decisions to (a pack's roles are installed naming them); default: your login name",
		Check: func(string) error { return nil }},
	{Key: YardmasterSetting, Help: "the yardmaster crew member's name, whom the role prompts tell agents to mail", Default: "yardmaster",
		Check: func(v string) error {
			if !partyName.MatchString(v) {
				return fmt.Errorf("yardmaster name %q: letters, digits, '.', '_', '-', '/'", v)
			}
			return nil
		}},
	{Key: StyleSetting, Help: "how agents should work with the operator, which every session reads when it starts (yardr prime): " +
		strings.Join(OperatorStyles, ", "),
		Check: func(v string) error {
			if !slices.Contains(OperatorStyles, v) {
				return fmt.Errorf("%s %q: there is %s", StyleSetting, v, strings.Join(OperatorStyles, ", "))
			}
			return nil
		}},
	{Key: NoteSetting, Help: "one line agents should know about the operator, read with operator_style",
		Check: func(v string) error {
			if strings.ContainsAny(v, "\n\r") {
				return errors.New(NoteSetting + ": one line")
			}
			return nil
		}},
	{Key: DefaultKindSetting, Help: "the agent kind of herdr groups and crew members whose config names none", Default: "claude",
		Check: func(v string) error {
			if !agents.IsKind(v) {
				return fmt.Errorf("%s %q: herdr starts %s", DefaultKindSetting, v, strings.Join(agents.Kinds(), ", "))
			}
			return nil
		}},
	{Key: DefaultPackSetting, Help: "the pack a depot is set up from when yardr depot init names none, and the pack yardr init takes the yardmaster's role from: " +
		"a pack's name (one the yard has imported, or one yardr carries), its directory, absolute, or a pack's URL; default: starter, and dir for a directory depot",
		Default: "starter", Check: checkDefaultPack},
	{Key: HookAddrSetting, Help: "the address the server takes webhooks on, such as 127.0.0.1:8790, when YARDR_HOOK_ADDR is not set where it starts; " +
		"the server reads it when it starts", Env: "HOOK_ADDR", Check: CheckHookAddr},
	{Key: TimerLimitSetting, Help: "how many live timers one crew member or user may have set (yardr timer)",
		Default: strconv.Itoa(DefaultTimerLimit), Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return fmt.Errorf("%s %q: a whole number, 1 or more", TimerLimitSetting, v)
			}
			return nil
		}},
	{Key: TimerMinEverySetting, Help: "the shortest period of a recurring timer (yardr timer add --every), such as 30m or 1d",
		Default: Span(DefaultTimerMinEvery), Check: func(v string) error {
			if _, err := ParseSpan(v); err != nil {
				return fmt.Errorf("%s: %w", TimerMinEverySetting, err)
			}
			return nil
		}},
	{Key: BlobMaxSetting, Help: "the most bytes one image in mail may have (yardr mail --file)",
		Default: strconv.Itoa(DefaultBlobMax), Check: func(v string) error {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 1 {
				return fmt.Errorf("%s %q: a whole number of bytes, 1 or more", BlobMaxSetting, v)
			}
			return nil
		}},
	{Key: PeerFileMaxSetting, Help: fmt.Sprintf("the most bytes one image sent to a linked yard, or taken from one, may have (yardr peer allow --send/--receive file), at most %d; default: %s", MaxPeerFile, BlobMaxSetting),
		Check: func(v string) error {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 1 || n > MaxPeerFile {
				return fmt.Errorf("%s %q: a whole number of bytes, 1 to %d", PeerFileMaxSetting, v, MaxPeerFile)
			}
			return nil
		}},
	{Key: MaxSessionsSetting, Help: "the most sessions that run an agent or a script on this machine at once, over all groups, " +
		"beside each group's limit; the daemon reads it on every pass; default: no cap, as 0",
		Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return fmt.Errorf("%s %q: a whole number, 0 (no cap) or more", MaxSessionsSetting, v)
			}
			return nil
		}},
	{Key: HerdrSessionSetting, Help: "the herdr session the yard's agents run in (herdr session list), which every call the yard makes to herdr goes to, " +
		"whatever shell the daemon or a command was started from; it is changed while no crew member and no session of a bead runs; " +
		"default: the session of the environment each is started in (HERDR_SOCKET_PATH, HERDR_SESSION, else herdr's default)",
		Check: func(v string) error {
			if !herdrSessionName.MatchString(v) || v == "." || v == ".." {
				return fmt.Errorf("%s %q: a herdr session's name has letters, digits, '.', '_' and '-' (herdr session list)", HerdrSessionSetting, v)
			}
			return nil
		}},
	{Key: NameSetting, Help: "the yard's name, which its crew's herdr agents and workspaces carry (<name>-yardmaster); default: the home's directory name",
		Check: func(v string) error {
			if !partyName.MatchString(v) {
				return fmt.Errorf("yard name %q: letters, digits, '.', '_', '-', '/'", v)
			}
			return nil
		}},
}

// SettingOf is the setting key names.
func SettingOf(key string) (Setting, bool) {
	for _, s := range KnownSettings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// SettingKeys are the keys of KnownSettings, sorted.
func SettingKeys() []string {
	out := make([]string, len(KnownSettings))
	for i, s := range KnownSettings {
		out[i] = s.Key
	}
	slices.Sort(out)
	return out
}

// Where the value a setting is read as comes from (Setting.Effective).
const (
	FromSet         = "set"
	FromDefault     = "default"
	FromEnvironment = "environment"
)

// Effective is the value the setting is read as, given what is stored, and
// where it is from: the environment variable that stands in for it, in the
// environment of whoever asks (a server reads its own when it starts), else
// what is set, else the default. Both are "" for a setting that is unset
// and has no default.
func (s Setting) Effective(stored map[string]string) (value, from string) {
	if s.Env != "" {
		if v := config.Getenv(s.Env); v != "" {
			return v, FromEnvironment
		}
	}
	if v := stored[s.Key]; v != "" {
		return v, FromSet
	}
	if s.Default != "" {
		return s.Default, FromDefault
	}
	return "", ""
}

// checkDefaultPack takes what names a pack wherever a later command runs:
// a name, a directory written absolute, or a pack's URL. A relative
// directory would mean another pack in every directory depot init is run
// from.
func checkDefaultPack(v string) error {
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return nil
	}
	if !strings.ContainsRune(v, filepath.Separator) && !strings.Contains(v, "/") && v != "." && v != ".." {
		if !partyName.MatchString(v) {
			return fmt.Errorf("%s %q: a pack's name, or its directory as an absolute path", DefaultPackSetting, v)
		}
		return nil
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("%s %q: a directory is given as an absolute path, since the setting is read wherever yardr depot init is run", DefaultPackSetting, v)
	}
	if _, err := os.Stat(filepath.Join(v, packFile)); err != nil {
		return fmt.Errorf("%s %q: no pack there: %w", DefaultPackSetting, v, err)
	}
	return nil
}

// CheckHookAddr refuses what the webhook receiver could not listen on: an
// address that is no host:port.
func CheckHookAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("hook address %q: want host:port, such as 127.0.0.1:8790 (%v)", addr, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("hook address %q: the port is a number from 1 to 65535, as in 127.0.0.1:8790", addr)
	}
	return nil
}

// Settings returns every setting.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bSettings, "", func(k []byte, v *string) error {
			out[string(k)] = *v
			return nil
		})
	})
	return out, err
}

// SetSetting sets key to value; an empty value removes it. A key that is
// not in KnownSettings is refused with the keys there are, and so is a
// value the key's check does not take: whoever writes (the CLI, init, the
// pairing form) is told a misspelt key instead of having it stored and
// never read. One the yard has stored already can still be removed.
//
// Settings are the operator's alone to change (operatorOnly): they say who
// the operator is, where the server listens and what every later session
// is told, so no agent sets one, and reading them stays anyone's. A yard
// that names no operator yet takes one from whoever is no agent of its own
// (notOperator), which is how its first operator is named.
//
// The operator is
// a user (see User), so naming one adds the user when there is none. A
// crew member's name is refused: mail to a name reaches one party. A yard that
// has a crew member named as its operator already keeps its setting as it is.
func (s *Store) SetSetting(ctx context.Context, key, value, actor string) error {
	if err := checkName("setting", key); err != nil {
		return err
	}
	set, known := SettingOf(key)
	if known && value != "" {
		if err := set.Check(value); err != nil {
			return err
		}
	}
	if key == OperatorSetting && value != "" {
		if err := checkUserName(value); err != nil {
			return fmt.Errorf("operator: %w", err)
		}
	}
	return s.write(func(w *wtx) error {
		if err := w.operatorOnly(ctx, actor, "change a setting"); err != nil {
			return err
		}
		if !known && (value != "" || w.Bucket(bSettings).Get([]byte(key)) == nil) {
			return fmt.Errorf("no setting %q; there is: %s", key, strings.Join(SettingKeys(), ", "))
		}
		if key == OperatorSetting && value != "" && w.Bucket(bCrew).Get([]byte(value)) != nil {
			if was, err := operatorIn(w.Tx); err != nil {
				return err
			} else if was != value {
				return fmt.Errorf("%s %q: that is a crew member's name, and mail to a name reaches one party: "+
					"choose another name for the operator, or remove the crew member first (yardr crew rm %s)", key, value, value)
			}
		}
		if key == HerdrSessionSetting {
			if err := s.herdrSessionFree(w.Tx, value); err != nil {
				return err
			}
		}
		var err error
		if value == "" {
			err = w.Bucket(bSettings).Delete([]byte(key))
		} else {
			err = put(w.Tx, bSettings, key, value)
		}
		if err != nil {
			return err
		}
		if key == OperatorSetting {
			if err := w.ensureOperator(actor); err != nil {
				return err
			}
		}
		return w.event("", "setting_changed", actor, map[string]any{"key": key, "value": value})
	})
}

// herdrSessionFree refuses a change of herdr_session to value while
// anything of the yard runs in a pane: a crew member that runs or is being
// started, or a live session of a bead that herdr runs. Their panes are in
// the session the yard used until now, and pane ids are a session's own, so
// in another one the yard would take them for gone, or another pane of the
// same id for theirs. Nothing on a handle says which session its pane is
// in, so they are stopped first; the refusal lists them.
func (s *Store) herdrSessionFree(tx *bolt.Tx, value string) error {
	var was string
	if _, err := get(tx, bSettings, HerdrSessionSetting, &was); err != nil {
		return err
	}
	if was == value {
		return nil
	}
	var running []string
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if r.Live() || r.Starting != nil {
			running = append(running, "crew member "+r.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	live, err := s.liveSessions(tx, "")
	if err != nil {
		return err
	}
	for _, sess := range live {
		if sess.RunnerName() == "herdr" {
			running = append(running, fmt.Sprintf("session %s (%s)", sess.ID, sess.Group))
		}
	}
	if len(running) == 0 {
		return nil
	}
	slices.Sort(running)
	return fmt.Errorf("%s is changed while nothing of the yard runs in a pane, since the panes of what runs are in the herdr session used until now "+
		"and are not moved: %s. Stop the daemon so that it starts nothing more (yardr daemon stop), stop the crew (yardr crew stop <name>) "+
		"and let the sessions end (yardr session list); then set it and start them again",
		HerdrSessionSetting, strings.Join(running, ", "))
}
