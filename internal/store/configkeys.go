package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// ConfigOf says whose config a key is read from: a group's or a crew
// member's. It is how an error names the owner ("group tick", "crew member
// clerk").
type ConfigOf string

const (
	GroupConfig ConfigOf = "group"
	CrewConfig  ConfigOf = "crew member"
)

// What a config value may be stored as. A --set value is read as JSON
// first, so `cmd=true` is the boolean and `model=5` the number.
const (
	// ConfigString is a string.
	ConfigString = "a string"
	// ConfigWords is a string, split at spaces, or a list.
	ConfigWords = "a string or a list"
	// ConfigBool is true or false.
	ConfigBool = "true or false"
	// ConfigSwitch is true or false, or one of the words a manual group's
	// notify is read as.
	ConfigSwitch = "true or false (or the word true, 1, yes, false, 0, no)"
	// ConfigLimit is a duration such as 30m, a number of seconds, or off.
	ConfigLimit = "a duration such as 30m, a number of seconds, or off"
	// ConfigSpan is a duration with whole days, as a timer's is written
	// (ParseSpan). A limit's 0 or off has no meaning for it: a key of this
	// type is turned off by not being set.
	ConfigSpan = "a duration such as 40m, 12h or 7d"
	// ConfigNames is names separated by commas, or a list of them.
	ConfigNames = "names separated by commas, or a list"
	// ConfigWord is one word: a string, or the number or boolean --set
	// reads a word like 7 as. What the word may be is its reader's to say
	// (checkPeerGroup).
	ConfigWord = "a word"
)

// ConfigKey is one key of a group's or a crew member's config: what its
// value is stored as, who reads it and what it means.
type ConfigKey struct {
	Key  string
	Type string
	// Runners are the runners that read the key; none means every runner.
	Runners []string
	// Of is whose config it is read from; "" means both.
	Of   ConfigOf
	Help string
}

// ConfigKeys are the keys yardr reads from a group's or a crew member's
// config. A key that is not here is read by nothing: PutGroup, SetGroup and
// PutCrew refuse to add one (ConfigError), so a misspelt key is told where
// it is written instead of being stored and ignored.
var ConfigKeys = []ConfigKey{
	{"cmd", ConfigString, []string{"exec"}, GroupConfig, "the command a session runs, through sh -c (required)"},
	{"dir", ConfigString, []string{"exec"}, GroupConfig, "where the command runs; default: the depot"},
	{"log", ConfigString, []string{"exec"}, GroupConfig, "the file the command's output is appended to"},
	{"worktrees", ConfigString, []string{"exec", "herdr"}, GroupConfig, "the directory a bead's worktree is made under; default: the home's worktrees"},
	{"kind", ConfigString, []string{"herdr"}, "", "the agent kind herdr starts; default: the yard's default_kind"},
	{"model", ConfigString, []string{"herdr"}, "", "the agent's model; default: the harness's own"},
	{"args", ConfigWords, []string{"herdr"}, "", "arguments the agent is started with"},
	{"prompt", ConfigString, []string{"herdr"}, "", "the role prompt file a session's brief starts with"},
	{"env_file", ConfigString, []string{"herdr"}, "", "a file of KEY=VALUE lines (mode 0600) that join the session's environment"},
	{"env_command", ConfigWords, []string{"herdr"}, CrewConfig, "a command that prints KEY=VALUE lines for the session's environment, run at each start"},
	{"trust", ConfigBool, []string{"herdr"}, "", "false: a claude agent is not told to trust the directory it starts in"},
	{"label", ConfigString, []string{"herdr"}, GroupConfig, "the label of the herdr workspace the group's sessions open in"},
	{"greet", ConfigString, []string{"herdr"}, CrewConfig, "the user the crew member's first session greets"},
	{"notify", ConfigSwitch, []string{"manual"}, GroupConfig, "true: a herdr notification says a bead is waiting"},
	{"wait", ConfigSpan, []string{"manual"}, GroupConfig, "how long a bead may be in a stage of the group before a waited event, once per stay; unset: never"},
	{GroupPeer, ConfigWord, []string{PeerRunner}, GroupConfig, "the linked yard that works the group's beads (required)"},
	{GroupTries, ConfigWord, []string{PeerRunner}, GroupConfig, fmt.Sprintf("how often a bead is posted to the peer before it is given up; default %d", PeerBeadTries)},
	{GroupRemote, ConfigWord, []string{PeerRunner}, GroupConfig, "the remote a branch the peer returns is fetched from; default " + PeerRemote},
	{"idle", ConfigLimit, nil, GroupConfig, "how long an agent may wait for input before a session_idle event"},
	{"max_age", ConfigLimit, nil, GroupConfig, "how long a session may run before a session_aged event"},
	{"grace", ConfigLimit, nil, GroupConfig, "how long after its event a session still idle or running is stopped"},
	{"stall", ConfigLimit, nil, GroupConfig, "how long an agent may sit idle after its first prompt before a session_stalled event"},
	{"keep", ConfigBool, nil, GroupConfig, "true: a session that ended is not stopped, so what it ran in stays to be looked at"},
	{"cleanup", ConfigBool, nil, GroupConfig, "false: what a closed bead's sessions left behind (its worktree) is kept"},
	{OpenThreads, ConfigNames, nil, CrewConfig, "the sources the crew member may open threads in"},
	{SetState, ConfigNames, nil, CrewConfig, "the sources the crew member may set a thread's status in"},
	{MailPeers, ConfigNames, nil, CrewConfig, "the linked yards whose crew the crew member may mail"},
}

// configRunners are the runners ConfigKeys speaks of. Of any other (a
// test's, one a later build adds) it knows no key, and says nothing.
var configRunners = []string{"exec", "herdr", "manual", PeerRunner}

// ErrUnknownConfigKey is what ConfigError's refusal of a key no code reads
// wraps, for a caller that goes on without the key (Exec.Start: the group
// was stored before keys were checked).
var ErrUnknownConfigKey = errors.New("unknown config key")

// reads reports whether the key is read from the config of of with the
// named runner.
func (k ConfigKey) reads(of ConfigOf, runner string) bool {
	return (k.Of == "" || k.Of == of) && (len(k.Runners) == 0 || slices.Contains(k.Runners, runner))
}

// ConfigKeysOf are the keys read from the config of of with the named
// runner, sorted.
func ConfigKeysOf(of ConfigOf, runner string) []string {
	var out []string
	for _, k := range ConfigKeys {
		if k.reads(of, runner) {
			out = append(out, k.Key)
		}
	}
	slices.Sort(out)
	return out
}

// ConfigError reports why the config cfg of name, a group or a crew member
// of the named runner, cannot be stored: the first of ConfigProblems.
func ConfigError(of ConfigOf, name, runner string, cfg map[string]any) error {
	if ps := ConfigProblems(of, name, runner, cfg); len(ps) > 0 {
		return ps[0]
	}
	return nil
}

// ConfigProblems is everything wrong with the config cfg of name, a group
// or a crew member of the named runner: a value stored as something its
// reader takes as not set (`--set cmd=true` stores the JSON boolean, so the
// error says what was stored and how to write the string), then a key
// nothing reads (ErrUnknownConfigKey, with the keys there are), then a
// setting the runner must have and has not. PutGroup, SetGroup and PutCrew
// ask it of what they are about to write, and yardr doctor of what is
// stored. A runner it does not know has nothing to say.
func ConfigProblems(of ConfigOf, name, runner string, cfg map[string]any) []error {
	return configProblems(of, name, runner, cfg, nil)
}

// configProblems is ConfigProblems for a write over a stored config, old:
// a key the write leaves as it is stored is not the write's to refuse, so
// a yard set up before keys were checked can still be changed, one key at
// a time. A setting the runner must have is asked for all the same.
func configProblems(of ConfigOf, name, runner string, cfg, old map[string]any) []error {
	if !slices.Contains(configRunners, runner) {
		return nil
	}
	// Judged as stored: a list is []any and a number float64 whatever Go
	// type the caller built the config from.
	cfg, old = asStored(cfg), asStored(old)
	subject := string(of) + " " + name
	var typed, unknown []error
	for _, key := range sortedConfig(cfg) {
		v := cfg[key]
		if was, kept := old[key]; kept && reflect.DeepEqual(was, v) {
			continue
		}
		i := slices.IndexFunc(ConfigKeys, func(k ConfigKey) bool { return k.Key == key && k.reads(of, runner) })
		if i < 0 {
			unknown = append(unknown, fmt.Errorf("%s: %w %q: nothing reads it. A %s of the %s runner takes: %s",
				subject, ErrUnknownConfigKey, key, of, runner, strings.Join(ConfigKeysOf(of, runner), ", ")))
			continue
		}
		if k := ConfigKeys[i]; !configTyped(k.Type, v) {
			// How to write a string is said where a string is wanted.
			hint := ""
			if _, isString := v.(string); !isString && slices.Contains([]string{ConfigString, ConfigWords, ConfigNames}, k.Type) {
				hint = fmt.Sprintf("; write --set '%s=%s'", key, quoted(v))
			}
			typed = append(typed, fmt.Errorf("%s: %s runner needs config.%s as %s, got the JSON %s%s",
				subject, runner, key, k.Type, jsonKind(v), hint))
		}
	}
	out := append(typed, unknown...)
	if cmd, _ := cfg["cmd"].(string); runner == "exec" && of == GroupConfig && cmd == "" && len(typed) == 0 {
		out = append(out, fmt.Errorf("%s: exec runner needs config.cmd", subject))
	}
	return out
}

func sortedConfig(cfg map[string]any) []string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// asStored is cfg as it reads once it has been through JSON, which is how
// the store keeps it.
func asStored(cfg map[string]any) map[string]any {
	b, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return cfg
	}
	return out
}

// configTyped reports whether v, a decoded JSON value, is what a key of
// type want is read as.
func configTyped(want string, v any) bool {
	s, isString := v.(string)
	_, isList := v.([]any)
	_, isBool := v.(bool)
	n, isNumber := v.(float64)
	switch want {
	case ConfigString:
		return isString
	case ConfigWords, ConfigNames:
		return isString || isList
	case ConfigBool:
		return isBool
	case ConfigSwitch:
		return isBool || isString && slices.Contains([]string{"true", "1", "yes", "false", "0", "no"}, s)
	case ConfigLimit:
		if isString && s != "off" && s != "0" {
			d, err := time.ParseDuration(s)
			return err == nil && d >= 0
		}
		return isString || isNumber && n >= 0
	case ConfigSpan:
		_, err := ParseSpan(s)
		return isString && err == nil
	case ConfigWord:
		return isString || isBool || isNumber
	}
	return false
}

// jsonKind names a decoded JSON value, with the value where it is short
// enough to say: "boolean true", "number 3", "null".
func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprintf("boolean %v", v)
	case float64:
		return "number " + jsonText(v)
	case string:
		return "string " + jsonText(v)
	case []any:
		return "list " + jsonText(v)
	}
	return "object " + jsonText(v)
}

// quoted is v as the JSON string --set stores as a string: "true" for the
// boolean true, which is what was typed. A list or an object was not meant
// as one word, so its place is left to fill in.
func quoted(v any) string {
	switch v.(type) {
	case nil, bool, float64:
		return jsonText(jsonText(v))
	}
	return `"..."`
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
