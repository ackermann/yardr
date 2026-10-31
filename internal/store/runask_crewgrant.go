// The crew set row of the run list (runList): a crew member's grants.
// The rest of the rows after the first five are in runask_more.go.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type crewGrantChange struct {
	name  string
	sets  []string
	unset []string
}

// crewGrantOf reads a crew set in its stored form: the member, then each
// setting, then each removal.
func crewGrantOf(argv []string) crewGrantChange {
	c := crewGrantChange{name: argv[2]}
	for i := 3; i < len(argv); i++ {
		if argv[i] == "--unset" && i+1 < len(argv) {
			i++
			c.unset = append(c.unset, argv[i])
			continue
		}
		c.sets = append(c.sets, argv[i])
	}
	return c
}

// crewGrantLink is the peers a crew set must not grant on a peer's own
// word: the peers its peers= setting names. The setting is written whole,
// so a peer the member mailed already is named too, and an approval through
// its reach is refused with the rest: the card's argv does not say which the
// member had, and the refusal sends the approval to the yard, where it runs.
// A set with no peers=, or an unset, names none: taking standing away
// needs no guard.
func crewGrantLink(argv []string) ([]string, string) {
	var peers []string
	for _, set := range crewGrantOf(argv).sets {
		k, v, ok := strings.Cut(set, "=")
		if !ok || k != MailPeers {
			continue
		}
		for _, p := range strings.Split(v, ",") {
			if p != "" {
				peers = append(peers, p)
			}
		}
	}
	return peers, "let a crew member mail its own crew"
}

// crewAloneKeys are the crew settings a run ask never proposes: the same
// settings the store holds to the operator in checkCrewDefine.
var crewAloneKeys = []string{"env_file", "env_command", "kind", "args", "prompt"}

// parseCrewGrant reads the words of crew set <name> with grants. What is
// no grant refuses with where it is run: set directly, but env_file,
// env_command, kind, args and prompt as the operator's alone
// (checkCrewDefine).
func parseCrewGrant(rest []string) ([]string, error) {
	args, values, err := runWords(crewGrantUse, "crew set", "the asker's", rest, map[string]bool{"--unset": true})
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("--run takes %s: it names no crew member", crewGrantUse)
	}
	for _, kv := range args[1:] {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return nil, fmt.Errorf("%q: want key=value (--unset <key> removes a setting)", kv)
		} else if slices.Contains(crewAloneKeys, k) {
			return nil, fmt.Errorf("--run takes %s: %s is the operator's alone, typed in a terminal and never proposed as a run ask", crewGrantUse, k)
		} else if !slices.Contains(crewGrantKeys, k) {
			return nil, fmt.Errorf("--run takes %s: %s is no grant of a crew member, and is set directly", crewGrantUse, k)
		}
	}
	var unsets []string
	for _, k := range values["--unset"] {
		if slices.Contains(crewAloneKeys, k) {
			return nil, fmt.Errorf("--run takes %s: %s is the operator's alone, typed in a terminal and never proposed as a run ask", crewGrantUse, k)
		}
		if !slices.Contains(crewGrantKeys, k) {
			return nil, fmt.Errorf("--run takes %s: %s is no grant of a crew member, and is unset directly", crewGrantUse, k)
		}
		unsets = append(unsets, k)
	}
	sets := map[string]string{}
	for _, kv := range args[1:] {
		k, v, _ := strings.Cut(kv, "=")
		if slices.Contains(unsets, k) {
			return nil, fmt.Errorf("%s is both set and unset", k)
		}
		sets[k] = v
	}
	if len(sets) == 0 && len(unsets) == 0 {
		return nil, fmt.Errorf("--run takes %s: nothing to set", crewGrantUse)
	}
	out := []string{args[0]}
	for _, k := range slices.Sorted(maps.Keys(sets)) {
		out = append(out, k+"="+sets[k])
	}
	slices.Sort(unsets)
	for _, k := range slices.Compact(unsets) {
		out = append(out, "--unset", k)
	}
	return out, nil
}

// crewGrantWant is the definition the parsed change makes of have: its
// config with the sets written over it and the unsets removed.
func crewGrantWant(have CrewMember, c crewGrantChange) (CrewMember, error) {
	sets, err := ParseSets(c.sets)
	if err != nil {
		return CrewMember{}, err
	}
	config, err := MergeConfig(have.Name, have.Config, sets, c.unset)
	if err != nil {
		return CrewMember{}, err
	}
	want := have
	want.Config = config
	return want, nil
}

// checkCrewGrant is the crew set's own check as the asker: what it adds
// to what the member may do outside the yard is the operator's, and
// anything else is the asker's to run.
func checkCrewGrant(ctx context.Context, w *wtx, argv []string, actor string) error {
	c := crewGrantOf(argv)
	var have CrewMember
	if ok, err := get(w.Tx, bCrew, c.name, &have); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("crew member %s: %w", c.name, ErrNotFound)
	}
	want, err := crewGrantWant(have, c)
	if err != nil {
		return err
	}
	if added := crewGrantsAdded(have, want); len(added) > 0 {
		return w.may(ctx, actor, ActGrantCrew, Object{Kind: KindCrew, Name: c.name, Does: added[0], Ask: Askable})
	}
	return nil
}

// grantMay is what the grant key reads of a crew member, and how a card
// names giving and taking it.
type grantMay struct {
	may     func(CrewMember) []string
	give    string
	take    string
	sources bool
}

func grantMays() map[string]grantMay {
	return map[string]grantMay{
		OpenThreads: {CrewMember.MayOpen, "may open threads in", "no longer opens threads in", true},
		SetState:    {CrewMember.MaySetState, "may set the status of threads in", "no longer sets the status of threads in", true},
		MailPeers:   {CrewMember.MayMail, "may mail crew members of", "no longer mails crew members of", false},
	}
}

// crewGrantCard is the card of crew set <name> with grants: what the
// member may newly do outside the yard, and what it may no more. What
// each names must be there: a source for threads, a peer for mail. May
// is permission, not what the other end allows: whether the yard can
// open threads somewhere is the command's own check at a terminal.
func (w *wtx) crewGrantCard(argv []string, asking bool) (AskRun, error) {
	c := crewGrantOf(argv)
	var have CrewMember
	if ok, err := get(w.Tx, bCrew, c.name, &have); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("crew member %s: %w", c.name, ErrNotFound)
	}
	want, err := crewGrantWant(have, c)
	if err != nil {
		return AskRun{}, err
	}
	var does []string
	var back []string
	backBy := ""
	for _, key := range crewGrantKeys {
		g := grantMays()[key]
		before, after := g.may(have), g.may(want)
		added := slices.DeleteFunc(slices.Clone(after), func(s string) bool { return slices.Contains(before, s) })
		removed := slices.DeleteFunc(slices.Clone(before), func(s string) bool { return slices.Contains(after, s) })
		for _, name := range added {
			if g.sources {
				if _, _, err := sourceIn(w.Tx, name); err != nil {
					return AskRun{}, err
				}
			} else if _, err := loadPeer(w.Tx, name); err != nil {
				return AskRun{}, err
			}
		}
		if len(added) > 0 {
			does = append(does, fmt.Sprintf("%s %s %s (now: %s).", have.Name, g.give, strings.Join(added, ", "), or(strings.Join(before, ", "), "none")))
		}
		if len(removed) > 0 {
			does = append(does, fmt.Sprintf("%s %s %s.", have.Name, g.take, strings.Join(removed, ", ")))
			backBy = "only the operator"
		}
		if len(added)+len(removed) == 0 {
			continue
		}
		if len(before) == 0 {
			back = append(back, "--unset", key)
		} else {
			back = append(back, key+"="+strings.Join(before, ","))
		}
	}
	if asking && len(does) == 0 {
		return AskRun{}, fmt.Errorf("crew member %s holds those grants already, so the command would change nothing", have.Name)
	}
	_, fingerprint, err := w.granteeSeen(Grantee{Crew: have.Name})
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(append([]string{"crew", "set", have.Name}, back...)),
		BackBy: backBy,
		Seen:   map[string]string{"crew " + have.Name: fingerprint},
	}, nil
}

// crewGrantStale says why the ask seq for crew set <name> with grants is
// approved no more: the member's definition was set since, or what it
// may do is not what the card says.
func (w *wtx) crewGrantStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if key := "crew " + name; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "crew_defined", naming("crew", name))
		return fmt.Errorf("ask %d was written when the crew member %s was another: its definition was set since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runCrewGrant saves the grants as actor through putCrew, and answers
// the lines the command prints for what changed.
func runCrewGrant(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	c := crewGrantOf(argv)
	var have CrewMember
	if ok, err := get(w.Tx, bCrew, c.name, &have); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("crew member %s: %w", c.name, ErrNotFound)
	}
	def, err := crewGrantWant(have, c)
	if err != nil {
		return "", err
	}
	def.Revision = have.Revision
	if _, err := w.putCrew(ctx, def, actor, by); err != nil {
		return "", err
	}
	var lines []string
	for _, key := range crewGrantKeys {
		was, had := have.Config[key]
		is, has := def.Config[key]
		a, b := grantSettingText(was, had), grantSettingText(is, has)
		if had != has || a != b {
			lines = append(lines, fmt.Sprintf("  %s: %s -> %s", key, or(a, "(not set)"), or(b, "(dropped)")))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// grantSettingText is a config value as a change prints it, as the
// command prints it at a terminal: a string as it is, anything else as
// JSON.
func grantSettingText(v any, set bool) string {
	if !set {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
