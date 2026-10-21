// The rows of the list after the first five (runList): a user added and
// removed, a crew member's grants, a crew member's identity in a source, a
// setting, a peer renamed, a bead's originator, and another's timer
// removed. Each has what a row has: how
// its words are read into one stored form, the command's own check as
// the asker, the card the yard writes from its records, why an approval
// of an older card is refused, and the store function that makes the
// change.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// parseUserAdd reads the words of user add <name>.
func parseUserAdd(rest []string) ([]string, error) {
	const use = "yardr user add <name>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one user, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// checkUserAdd is adding a user's own check as the asker.
func checkUserAdd(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "add a user", Askable)
}

// userAddCard is the card of user add <name>: what adding holds is what
// AddUser refuses, in its words.
func (w *wtx) userAddCard(argv []string, asking bool) (AskRun, error) {
	name := argv[2]
	if err := checkUserName(name); err != nil {
		return AskRun{}, err
	}
	if name == OriginatorAny {
		return AskRun{}, fmt.Errorf("user %s: that is a route keyword, not a user, and mail to a name reaches one party: choose another", name)
	}
	switch {
	case w.Bucket(bUsers).Get([]byte(name)) != nil:
		return AskRun{}, fmt.Errorf("%w: there is a user %s already", ErrConflict, name)
	case w.Bucket(bCrew).Get([]byte(name)) != nil:
		return AskRun{}, fmt.Errorf("user %s: that is a crew member's name, and mail to a name reaches one party: choose another", name)
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{fmt.Sprintf("adds the user %s: they read mail from now on, and are told nothing from before.", name)},
		Back:   runCommandLine([]string{"user", "rm", name}),
		BackBy: "only the operator",
		Seen:   map[string]string{},
	}, nil
}

// userAddStale is never told apart: the card names nothing the yard
// holds, and a user added since is refused by the card itself.
func (w *wtx) userAddStale(seq uint64, was, now AskRun) error {
	return staleCard(seq, was, now)
}

// runUserAdd adds the user as actor through insertUser, and answers what
// the command prints at a terminal.
func runUserAdd(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	u, err := w.insertUser(ctx, argv[2], actor, by)
	if err != nil {
		return "", err
	}
	return "user " + u.Name + " added", nil
}

// parseUserRm reads the words of user rm <name>.
func parseUserRm(rest []string) ([]string, error) {
	const use = "yardr user rm <name>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one user, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// checkUserRm is removing a user's own check as the asker.
func checkUserRm(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "remove a user", Askable)
}

// userRmCard is the card of user rm <name>: who goes is named with what
// goes with them, as DeleteUser drops it.
func (w *wtx) userRmCard(argv []string, asking bool) (AskRun, error) {
	name := argv[2]
	u, err := getUser(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	if operator, err := operatorIn(w.Tx); err != nil {
		return AskRun{}, err
	} else if operator == name {
		return AskRun{}, fmt.Errorf("user %s is the yard's operator and is not removed; name another operator first (yardr settings set %s <name>)", name, OperatorSetting)
	}
	devices := keys(w.Tx, bDevices, name+"\x00")
	var exposed, trusted []string
	if err := each(w.Tx, bPeers, "", func(_ []byte, p *Peer) error {
		if slices.Contains(p.Expose, name) {
			exposed = append(exposed, p.Name)
		}
		for _, as := range p.Trust {
			if as == name {
				trusted = append(trusted, p.Name)
				break
			}
		}
		return nil
	}); err != nil {
		return AskRun{}, err
	}
	does := fmt.Sprintf("removes the user %s: their %s and %s go, and what waited to be told them; the mail stays in the log.", name, countNoun(len(u.Subscriptions), "subscription"), countNoun(len(devices), "device"))
	if u.Notify != "" {
		does = fmt.Sprintf("removes the user %s: their %s, %s and their telling outside the yard go, and what waited to be told them; the mail stays in the log.", name, countNoun(len(u.Subscriptions), "subscription"), countNoun(len(devices), "device"))
	}
	sentences := []string{does}
	if len(exposed) > 0 {
		sentences = append(sentences, fmt.Sprintf("%s see them no more.", strings.Join(exposed, ", ")))
	}
	if len(trusted) > 0 {
		sentences = append(sentences, fmt.Sprintf("mail from %s no longer counts as them.", strings.Join(trusted, ", ")))
	}
	sentences = append(sentences, "Adding them again starts them fresh: no earlier mail, no subscriptions, no devices.")
	return AskRun{
		Argv:   argv,
		Does:   sentences,
		Back:   runCommandLine([]string{"user", "add", name}),
		BackBy: "only the operator",
		Seen:   map[string]string{"user " + name: seen(u.CreatedAt, u.CreatedBy)},
	}, nil
}

// countNoun is n with the noun after it, plural as it is.
func countNoun(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// userRmStale says why the ask seq for user rm <name> is approved no
// more: the user was removed and added again since, or what goes with
// them is not what the card says.
func (w *wtx) userRmStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if key := "user " + name; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "user_added", naming("user", name))
		return fmt.Errorf("ask %d was written when the user %s was another: they were removed and added again since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runUserRm removes the user as actor through deleteUser, and answers
// what the command prints at a terminal.
func runUserRm(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	if err := w.deleteUser(ctx, argv[2], actor, by); err != nil {
		return "", err
	}
	return "user " + argv[2] + " removed", nil
}

// crewGrantKeys are the crew settings a run ask takes: the grants. The
// rest of a definition is set directly, and a definition whole is the
// operator's alone.
var crewGrantKeys = []string{OpenThreads, SetState, MailPeers}

// crewGrantUse is how a refusal names the crew set a run ask takes.
const crewGrantUse = "yardr crew set <name> open_threads=<sources> | set_state=<sources> | peers=<peers>"

// crewGrantChange is a parsed crew set of grants: the member, the grants
// set whole, and the grants removed.
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

// parseCrewGrant reads the words of crew set <name> with grants. What is
// no grant refuses the command with where it is run: set directly, as
// saving it checks nothing.
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
		} else if !slices.Contains(crewGrantKeys, k) {
			return nil, fmt.Errorf("--run takes %s: %s is no grant of a crew member, and is set directly", crewGrantUse, k)
		}
	}
	var unsets []string
	for _, k := range values["--unset"] {
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
		return w.operatorOnly(ctx, actor, added[0], Askable)
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

// parseCrewIdentity reads the words of crew identity <name> with its
// --source and --id and --key, one of the two at least. What is stored is
// the name, the source and each of the two given, each once, in that
// order. Listing (--source left out) and removing (--rm) are no run ask:
// one needs no approval, and the other is the operator's to type.
func parseCrewIdentity(rest []string) ([]string, error) {
	const use = "yardr crew identity <name> --source <source> [--id <id>] [--key <key>]"
	args, values, err := runWords(use, "crew identity", "the operator's", rest, map[string]bool{"--source": true, "--id": true, "--key": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) != 1:
		return nil, fmt.Errorf("--run takes %s: one crew member, not %d words", use, len(args))
	case len(values["--source"]) != 1:
		return nil, fmt.Errorf("--run takes %s: one --source, not %d", use, len(values["--source"]))
	case len(values["--id"]) > 1:
		return nil, fmt.Errorf("--run takes %s: --id once, not %d times", use, len(values["--id"]))
	case len(values["--key"]) > 1:
		return nil, fmt.Errorf("--run takes %s: --key once, not %d times", use, len(values["--key"]))
	case len(values["--id"]) == 0 && len(values["--key"]) == 0:
		return nil, fmt.Errorf("--run takes %s: --id and/or --key", use)
	}
	out := []string{args[0], "--source", values["--source"][0]}
	for _, flag := range []string{"--id", "--key"} {
		if len(values[flag]) > 0 {
			out = append(out, flag, values[flag][0])
		}
	}
	return out, nil
}

// crewIdentityOf is the crew member, the source and the id and key of a
// parsed crew identity, each "" when the command leaves it out.
func crewIdentityOf(argv []string) (name, source, id, key string) {
	name = argv[2]
	for i := 3; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--source":
			source = argv[i+1]
		case "--id":
			id = argv[i+1]
		case "--key":
			key = argv[i+1]
		}
	}
	return name, source, id, key
}

// checkCrewIdentity is saying who a crew member is in a source's own
// check as the asker.
func checkCrewIdentity(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "say who a crew member is in a source", Askable)
}

// identitySays is the id and/or key as a card names them.
func identitySays(id, key string) string {
	switch {
	case id != "" && key != "":
		return id + " / " + key
	case id != "":
		return id
	default:
		return key
	}
}

// crewIdentityCard is the card of crew identity: which crew member and
// which source, that mentions and assignments of the id or key there are
// then addressed to it, and what stood. Asking refuses an identity that
// would change nothing.
func (w *wtx) crewIdentityCard(argv []string, asking bool) (AskRun, error) {
	name, source, id, key := crewIdentityOf(argv)
	src, _, err := sourceIn(w.Tx, source)
	if err != nil {
		return AskRun{}, err
	}
	var r CrewMember
	if ok, err := get(w.Tx, bCrew, name, &r); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("crew member %s: %w", name, ErrNotFound)
	}
	unfolded, folded := key, foldIdentityKey(w.Tx, source, key)
	key = folded
	who := identitySays(id, key)
	cur, had := r.Identities[source]
	if asking && had && cur.ID == id && cur.Key == key {
		return AskRun{}, fmt.Errorf("crew member %s is %s in %s already: the command would change nothing", name, who, source)
	}
	does := []string{fmt.Sprintf("says the crew member %s is %s in %s: the yard treats mentions and assignments of %s there as addressed to it.", name, who, source, who)}
	if unfolded != folded {
		does = append(does, fmt.Sprintf("%s carries keys lowercase, so %s is stored as %s.", source, unfolded, folded))
	}
	if had {
		does = append(does, fmt.Sprintf("It is %s in %s now.", identitySays(cur.ID, cur.Key), source))
	} else {
		does = append(does, fmt.Sprintf("It has no identity in %s now.", source))
	}
	back := []string{"crew", "identity", name, "--source", source}
	if had {
		if cur.ID != "" {
			back = append(back, "--id", cur.ID)
		}
		if cur.Key != "" {
			back = append(back, "--key", cur.Key)
		}
	} else {
		back = append(back, "--rm")
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: "only the operator",
		Seen:   map[string]string{"crew " + name: seen(cur.ID, cur.Key), "source " + source: fingerprint},
	}, nil
}

// crewIdentityStale says why the ask seq for crew identity is approved no
// more: the source is another under its name, or the crew member holds
// another identity there than the card says.
func (w *wtx) crewIdentityStale(seq uint64, was, now AskRun) error {
	name, source, _, _ := crewIdentityOf(was.Argv)
	if err := w.staleSource(seq, source, was, now); err != nil {
		return err
	}
	if key := "crew " + name; was.Seen[key] != now.Seen[key] {
		match := func(e Event) bool { return e.Data["crew"] == name && e.Data["source"] == source }
		set, removed := lastEvent(w.Tx, "identity_set", match), lastEvent(w.Tx, "identity_removed", match)
		switch {
		case set != nil && (removed == nil || set.Seq > removed.Seq):
			return fmt.Errorf("ask %d was written when the crew member %s held another identity in %s: it was set since%s. %s", seq, name, source, since(set), nothingRan)
		case removed != nil:
			return fmt.Errorf("ask %d was written when the crew member %s held another identity in %s: it was removed since%s. %s", seq, name, source, since(removed), nothingRan)
		}
		return fmt.Errorf("ask %d was written when the crew member %s held another identity in %s. %s", seq, name, source, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runCrewIdentity says who the crew member is in the source as actor
// through setCrewIdentity, and answers the identity as it is stored.
func runCrewIdentity(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, source, id, key := crewIdentityOf(argv)
	if err := w.setCrewIdentity(ctx, name, source, Identity{ID: id, Key: key}, actor, by); err != nil {
		return "", err
	}
	return fmt.Sprintf("crew member %s is %s in %s", name, identitySays(id, foldIdentityKey(w.Tx, source, key)), source), nil
}

// parseSettingSet reads the words of settings set <key> <value>. What is
// no setting refuses the command as setting itself refuses it. One word
// is refused apart: unsetting one takes an empty value, which no split
// line holds.
func parseSettingSet(rest []string) ([]string, error) {
	const use = "yardr settings set <key> <value>"
	if len(rest) == 1 {
		return nil, fmt.Errorf("--run takes %s: one key and one value; unsetting one takes an empty value, and is typed in a terminal", use)
	}
	if len(rest) != 2 {
		return nil, fmt.Errorf("--run takes %s: one key and one value, not %d words", use, len(rest))
	}
	if _, ok := SettingOf(rest[0]); !ok {
		return nil, fmt.Errorf("no setting %q; there is: %s", rest[0], strings.Join(SettingKeys(), ", "))
	}
	return []string{rest[0], rest[1]}, nil
}

// checkSettingSet is setting a setting's own check as the asker.
func checkSettingSet(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "change a setting", settingAsk(argv[2]))
}

// settingSetCard is the card of settings set <key> <value>: the value it
// sets, against what is stored now. The value is checked as setting
// checks it.
func (w *wtx) settingSetCard(argv []string, asking bool) (AskRun, error) {
	key, value := argv[2], argv[3]
	set, ok := SettingOf(key)
	if !ok {
		return AskRun{}, fmt.Errorf("no setting %q; there is: %s", key, strings.Join(SettingKeys(), ", "))
	}
	if value != "" {
		if err := set.Check(value); err != nil {
			return AskRun{}, err
		}
	}
	stored, has := w.Bucket(bSettings).Get([]byte(key)), w.Bucket(bSettings).Get([]byte(key)) != nil
	if asking && has && string(stored) == value {
		return AskRun{}, fmt.Errorf("setting %s is %q already, so the command would change nothing", key, value)
	}
	now := "unset"
	if has {
		now = string(stored)
	} else if set.Default != "" {
		now = "unset (default " + set.Default + ")"
	}
	back := runCommandLine([]string{"settings", "set", key, string(stored)})
	if !has {
		back = runCommandLine([]string{"settings", "set", key, `""`})
	}
	does := []string{fmt.Sprintf("setting %s becomes %s (now: %s).", key, value, now)}
	if key == HookPublicURLSetting {
		// The address's one effect is the URL a register call sends
		// to a service, so the card names it whole: what the operator
		// approves is where hooks are registered. Trimmed as the
		// hook's URL is (hookPublicURL), so the card shows it.
		does = append(does, fmt.Sprintf("a hook whose source names a register call is registered at this address when its card is approved: the URL of hook <name> is %s/<name>.", strings.TrimRight(value, "/")))
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   back,
		BackBy: "only the operator",
		Seen:   map[string]string{"setting " + key: seen(has, string(stored))},
	}, nil
}

// settingSetStale says why the ask seq for settings set <key> <value> is
// approved no more: the setting was changed since.
func (w *wtx) settingSetStale(seq uint64, was, now AskRun) error {
	key := was.Argv[2]
	if seen := "setting " + key; was.Seen[seen] != now.Seen[seen] {
		e := lastEvent(w.Tx, "setting_changed", naming("key", key))
		return fmt.Errorf("ask %d was written when setting %s was another: it was changed since%s. %s", seq, key, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runSettingSet sets the setting as actor through setSetting, and
// answers what the command prints at a terminal.
func runSettingSet(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	key, value := argv[2], argv[3]
	if set, ok := SettingOf(key); ok && value != "" {
		if err := set.Check(value); err != nil {
			return "", err
		}
	}
	if err := w.setSetting(ctx, key, value, true, actor, by); err != nil {
		return "", err
	}
	return key + " = " + value, nil
}

// parsePeerRename reads the words of peer rename <old> <new>.
func parsePeerRename(rest []string) ([]string, error) {
	const use = "yardr peer rename <old> <new>"
	if len(rest) != 2 {
		return nil, fmt.Errorf("--run takes %s: two names, not %d words", use, len(rest))
	}
	return []string{rest[0], rest[1]}, nil
}

// checkPeerRename is renaming a peer's own check as the asker.
func checkPeerRename(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "rename a peer", Askable)
}

// peerRenameCard is the card of peer rename <old> <new>: what follows
// the name is named as renaming moves it, in the store's own words for
// what it refuses.
func (w *wtx) peerRenameCard(argv []string, asking bool) (AskRun, error) {
	name, to := argv[2], argv[3]
	p, err := loadPeer(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	if err := checkName("peer", to); err != nil {
		return AskRun{}, err
	}
	switch {
	case to == name:
		return AskRun{}, fmt.Errorf("peer %s has that name already", name)
	case w.Bucket(bPeers).Get([]byte(to)) != nil:
		return AskRun{}, fmt.Errorf("peer %s is added already: two links cannot share a name. Rename or remove that one first", to)
	}
	if err := outboxOf(w.Tx, p); err != nil {
		return AskRun{}, err
	}
	var may []string
	if err := each(w.Tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if slices.Contains(r.MayMail(), name) {
			may = append(may, r.Name)
		}
		return nil
	}); err != nil {
		return AskRun{}, err
	}
	sentences := []string{fmt.Sprintf("the link with %s (%s) becomes %s: its key, its address and what it carries stay the same.", name, peerAs(p), to)}
	if p.Waiting > 0 {
		sentences = append(sentences, fmt.Sprintf("%d messages that wait for it move with it, and are delivered.", p.Waiting))
	}
	if len(may) > 0 {
		sentences = append(sentences, fmt.Sprintf("%s may mail it as before, and get mail from the yard that says so.", strings.Join(may, ", ")))
	}
	return AskRun{
		Argv:   argv,
		Does:   sentences,
		Back:   runCommandLine([]string{"peer", "rename", to, name}),
		BackBy: "only the operator",
		Seen:   map[string]string{"peer " + name: p.Key},
	}, nil
}

// peerRenameStale says why the ask seq for peer rename <old> <new> is
// approved no more: the link is another one under the same name, or what
// follows it is not what the card says.
func (w *wtx) peerRenameStale(seq uint64, was, now AskRun) error {
	if err := w.peerKeyStale(seq, was.Argv[2], was, now); err != nil {
		return err
	}
	return staleCard(seq, was, now)
}

// runPeerRename renames the peer as actor through renamePeer, and
// answers what the command prints at a terminal.
func runPeerRename(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, to := argv[2], argv[3]
	var may []string
	if err := each(w.Tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if slices.Contains(r.MayMail(), name) {
			may = append(may, r.Name)
		}
		return nil
	}); err != nil {
		return "", err
	}
	p, err := w.renamePeer(ctx, name, to, actor, by)
	if err != nil {
		return "", err
	}
	out := fmt.Sprintf("peer %s is %s now: its crew are <name>@%s, and nothing is reached as <name>@%s", name, p.Name, p.Name, name)
	if p.Waiting > 0 {
		out += fmt.Sprintf("\n%d messages that waited for it still wait, and are delivered", p.Waiting)
	}
	if len(may) > 0 {
		out += fmt.Sprintf("\n%s may mail it as before, and got mail from the yard that says so", strings.Join(may, ", "))
	}
	return out, nil
}

// parseOriginator reads the words of bead update <bead> --originator
// <user>: the originator and nothing else, as the rest of an update is
// the asker's to type.
func parseOriginator(rest []string) ([]string, error) {
	const use = "yardr bead update <bead> --originator <user>"
	args, values, err := runWords(use, "bead update", "the asker's", rest, map[string]bool{"--originator": true})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("--run takes %s: it names no bead", use)
		}
		return nil, fmt.Errorf("--run takes %s: one bead, not %s", use, strings.Join(args, " and "))
	}
	if len(values["--originator"]) != 1 {
		if len(values["--originator"]) == 0 {
			return nil, fmt.Errorf("--run takes %s: it names no --originator", use)
		}
		return nil, fmt.Errorf("--run takes %s: --originator once", use)
	}
	return []string{args[0], "--originator", values["--originator"][0]}, nil
}

// checkOriginator is changing a bead's originator's own check as the
// asker.
func checkOriginator(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.operatorOnly(ctx, actor, "change a bead's originator", Askable)
}

// originatorCard is the card of bead update <bead> --originator <user>:
// whose the bead becomes for, against whose it is for now. A bead for
// no one has no command that takes it back to none: the originator is
// only ever changed.
func (w *wtx) originatorCard(argv []string, asking bool) (AskRun, error) {
	id, to := argv[2], argv[4]
	row, err := w.loadBead(id)
	if err != nil {
		return AskRun{}, err
	}
	if to == OriginatorAny {
		return AskRun{}, fmt.Errorf("--originator %s: an originator is a person, not the route keyword %q", to, OriginatorAny)
	}
	if _, err := getUser(w.Tx, to); err != nil {
		return AskRun{}, fmt.Errorf("--originator: %w", err)
	}
	if asking && to == row.Originator {
		return AskRun{}, fmt.Errorf("bead %s is for %s already, so the command would change nothing", row.ID, to)
	}
	back := runCommandLine([]string{"bead", "update", row.ID, "--originator", row.Originator})
	backBy := "only the operator"
	if row.Originator == "" {
		back = "the bead can only be for another user after"
		backBy = "nobody"
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{fmt.Sprintf("bead %s is for %s now (was: %s).", row.ID, to, or(row.Originator, "no one"))},
		Back:   back,
		BackBy: backBy,
		Seen:   map[string]string{"bead " + row.ID: seen(row.Originator)},
	}, nil
}

// originatorStale says why the ask seq for bead update <bead>
// --originator <user> is approved no more: the bead is for another
// already.
func (w *wtx) originatorStale(seq uint64, was, now AskRun) error {
	id, to := was.Argv[2], was.Argv[4]
	row, err := w.loadBead(id)
	if err != nil {
		return staleCard(seq, was, now)
	}
	if key := "bead " + row.ID; was.Seen[key] != now.Seen[key] {
		// The originator the card was written at is in the command that
		// takes it back, which names it; a bead for no one has none.
		had := ""
		if back := strings.Fields(was.Back); len(back) == 6 && back[4] == "--originator" {
			had = back[5]
		}
		if row.Originator == to {
			return fmt.Errorf("ask %d says the bead %s is for %s now, and it is for %s already: nothing is left to change. %s", seq, row.ID, or(had, "no one"), to, nothingRan)
		}
		return fmt.Errorf("ask %d says the bead %s is for %s now, and it is for %s: the card is from before that changed. %s", seq, row.ID, or(had, "no one"), or(row.Originator, "no one"), nothingRan)
	}
	return staleCard(seq, was, now)
}

// parseTimerRm reads the words of timer rm <id>.
func parseTimerRm(rest []string) ([]string, error) {
	const use = "yardr timer rm <id>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one timer, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// checkTimerRm is removing a timer's own check as the asker: one's own
// is anyone's to remove, and anyone else's is the operator's.
func checkTimerRm(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.checkTimerRemove(ctx, argv[2], actor)
}

// timerRmCard is the card of timer rm <id>: what the timer would have
// told, and when. Taking it back re-adds it by hand: what it would have
// told is told anew, and a time past is given anew.
func (w *wtx) timerRmCard(argv []string, asking bool) (AskRun, error) {
	id := argv[2]
	t := new(Timer)
	if ok, err := get(w.Tx, bTimers, id, t); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("timer %s: %w", id, ErrNotFound)
	}
	when := "once"
	if t.Every != "" {
		when = "every " + t.Every
	}
	unless := ""
	if t.Unless != nil {
		unless = ", unless bead " + t.Unless.Bead
		if t.Unless.Stage != "" {
			unless += ", stage " + t.Unless.Stage
		}
	}
	backBy := timerAddBackBy(w, t)
	backLine := "the timer cannot be added again because its recipient is no longer a user or crew member of the yard"
	var back []string
	if backBy != "nobody" {
		back = []string{"timer", "add", "--to", t.To}
	}
	if t.Every != "" {
		if backBy != "nobody" {
			back = append(back, "--every", t.Every)
		}
	} else {
		if backBy != "nobody" {
			back = append(back, "--at", t.Due.Format("2006-01-02T15:04:05Z07:00"))
		}
	}
	if t.Unless != nil && backBy != "nobody" {
		cond := "bead=" + t.Unless.Bead
		if t.Unless.Stage != "" {
			cond += ",stage=" + t.Unless.Stage
		}
		back = append(back, "--unless", cond)
	}
	if reason, ok := timerReasonArg(t.Reason); ok && backBy != "nobody" {
		back = append(back, reason)
		backLine = runCommandLine(back)
	} else if backBy != "nobody" {
		backLine = "the timer can be added again by hand"
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{fmt.Sprintf("removes the timer %s set by %s for %s: %s (%s%s).", t.ID, t.By, t.To, strconv.Quote(t.Reason), when, unless)},
		Back:   backLine,
		BackBy: backBy,
		Seen:   map[string]string{"timer " + t.ID: seen(t.By, t.To, t.Reason, t.Every, t.Ends, t.Unless, t.SetAt.Unix())},
	}, nil
}

// timerAddBackBy is who can add a timer back for this recipient now. A
// user sets one for themselves; a crew member may set one for a user or
// for themselves. When the recipient is gone, there is no take-back.
func timerAddBackBy(w *wtx, t *Timer) string {
	if w.Bucket(bUsers).Get([]byte(t.To)) != nil {
		if len(keys(w.Tx, bCrew, "")) > 0 {
			return t.To + " or a crew member"
		}
		return t.To
	}
	if w.Bucket(bCrew).Get([]byte(t.To)) != nil {
		return t.To
	}
	return "nobody"
}

// timerReasonArg is the timer's reason as one safe shell word for a card's
// take-back command. Control characters cannot be shown there without
// breaking the card into lines, so those reasons get a manual take-back.
func timerReasonArg(reason string) (string, bool) {
	if strings.IndexFunc(reason, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) >= 0 {
		return "", false
	}
	return "'" + strings.ReplaceAll(reason, "'", "'\\''") + "'", true
}

// timerRmStale is never told apart past the sentences: a timer is set
// and removed, never changed, and one gone since is refused by the card
// itself.
func (w *wtx) timerRmStale(seq uint64, was, now AskRun) error {
	return staleCard(seq, was, now)
}

// runTimerRm removes the timer as actor through removeTimer, and answers
// what the command prints at a terminal.
func runTimerRm(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	t, err := w.removeTimer(ctx, argv[2], actor, by)
	if err != nil {
		return "", err
	}
	return "timer " + t.ID + " removed", nil
}
