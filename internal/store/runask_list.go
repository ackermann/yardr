// The rows of the list after the first (runList): a source's calls an hour,
// a grant of a source's calls, a user's own subscription to the yard's
// events, and which parts a user is told of outside the yard. Each has what a row has: how its words are read into one stored
// form, the card the yard writes from its records, why an approval of an
// older card is refused, and the store function that makes the change.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/source"
)

// nothingRan ends the refusal of an approval that is held to another yard
// than the one that stands.
const nothingRan = "Nothing ran. Decline it; it is asked again if it is still wanted."

// seen is what a run ask keeps of a thing its command names: a SHA-256 over
// the parts of it the command's effect rests on, whole. The yard does not
// judge which change of them matters: any makes the ask stale.
func seen(parts ...any) string {
	text, _ := json.Marshal(parts)
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// sourceSeen is what a run ask keeps of a source: its declaration text, and
// where its credential comes from. A replaced file may keep a call's name,
// method and path and send another body, and every sentence then reads as
// before.
func sourceSeen(src *Source) string { return seen(src.Text, src.EnvFile, src.MintCommand) }

// lastEvent is the newest event of the kind kind that match takes, among
// the newest 200 of the kind, nil for none: what a refusal names as the
// change an ask is older than.
func lastEvent(tx *bolt.Tx, kind string, match func(Event) bool) *Event {
	prefix := []byte(kind + "\x00")
	c := tx.Bucket(bEventsKd).Cursor()
	// The key after the kind's last: the one before it is that last.
	k, _ := c.Seek([]byte(kind + "\x01"))
	if k == nil {
		k, _ = c.Last()
	} else {
		k, _ = c.Prev()
	}
	for n := 0; k != nil && bytes.HasPrefix(k, prefix) && n < 200; k, _ = c.Prev() {
		n++
		var e Event
		if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err == nil && ok && match(e) {
			return &e
		}
	}
	return nil
}

// since names the event e in a refusal, " (group_set, 12 Oct 23:10, by
// yardmaster)", and is nothing for no event: the log is pruned.
func since(e *Event) string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(" (%s, %s, by %s)", e.Kind, e.At.Local().Format("2 Jan 15:04"), e.Actor)
}

// naming matches the events whose data names name under key.
func naming(key, name string) func(Event) bool {
	return func(e Event) bool { return e.Data[key] == name }
}

// staleSource says why the ask seq, written about the source name, is
// approved no more when the source is not the one the card was written
// from, and nil when it is.
func (w *wtx) staleSource(seq uint64, name string, was, now AskRun) error {
	if key := "source " + name; was.Seen[key] == now.Seen[key] {
		return nil
	}
	added := lastEvent(w.Tx, "source_added", naming("source", name))
	set := lastEvent(w.Tx, "source_set", func(e Event) bool {
		_, file := e.Data[SourceEnvFile]
		_, mint := e.Data[SourceMintCommand]
		return e.Data["source"] == name && (file || mint)
	})
	switch {
	case set != nil && (added == nil || set.Seq > added.Seq):
		what := SourceEnvFile
		if _, mint := set.Data[SourceMintCommand]; mint {
			what = SourceMintCommand
		}
		return fmt.Errorf("ask %d was written when the source %s took its credential from elsewhere: its %s was set since%s. %s", seq, name, what, since(set), nothingRan)
	case added != nil:
		return fmt.Errorf("ask %d was written from another declaration of the source %s: it was replaced since%s. %s", seq, name, since(added), nothingRan)
	}
	return fmt.Errorf("ask %d was written when the source %s was another: its declaration, its %s or its %s was changed since. %s", seq, name, SourceEnvFile, SourceMintCommand, nothingRan)
}

// staleCard says of the ask seq that the yard would not write its card as
// it stands, by the first sentence that reads otherwise now.
func staleCard(seq uint64, was, now AskRun) error {
	said, says := strings.Join(was.Does, " "), strings.Join(now.Does, " ")
	for i := range was.Does {
		if i < len(now.Does) && was.Does[i] != now.Does[i] {
			said, says = was.Does[i], now.Does[i]
			break
		}
	}
	return fmt.Errorf("ask %d says %q, and the yard would say %q now: the card is from before that changed. %s", seq, said, says, nothingRan)
}

// runWords reads the words after a command's own into those that are no
// flag, and the values of the flags it takes, each in the order typed.
// flags says of each whether it takes a value, which follows it or an "=".
// Any other flag refuses the command whole, in the words of use: what else
// the command does is not on the list, and is typed by whose.
func runWords(use, command, whose string, rest []string, flags map[string]bool) (args []string, values map[string][]string, err error) {
	values = map[string][]string{}
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		if !strings.HasPrefix(word, "-") {
			args = append(args, word)
			continue
		}
		flag, value, set := strings.Cut(word, "=")
		takes, ok := flags[flag]
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("--run takes %s, and %s with %s is %s to type in a terminal", use, command, flag, whose)
		case !takes && set:
			return nil, nil, fmt.Errorf("--run takes %s: %s takes no value", use, flag)
		case !takes:
			value = "set"
		case !set && i+1 < len(rest):
			i++
			value = rest[i]
		case !set:
			return nil, nil, fmt.Errorf("--run takes %s: %s needs its value", use, flag)
		}
		if value == "" {
			return nil, nil, fmt.Errorf("--run takes %s: %s needs its value", use, flag)
		}
		values[flag] = append(values[flag], value)
	}
	return args, values, nil
}

// andList joins words as a sentence lists them.
func andList(words []string) string {
	if len(words) < 3 {
		return strings.Join(words, " and ")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// parseSourceLimit reads the words of source set <source>
// calls_per_hour=<n> or updates_per_hour=<n>. The other settings of a
// source refuse the command: a path and a program are no words of a command
// that is approved from a card.
func parseSourceLimit(rest []string) ([]string, error) {
	const use = "yardr source set <source> calls_per_hour=<n> or updates_per_hour=<n>"
	args, _, err := runWords(use, "source set", "the operator's", rest, nil)
	if err != nil {
		return nil, err
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("--run takes %s: one source and one setting, not %d words", use, len(args))
	}
	name := args[0]
	key, value, ok := strings.Cut(args[1], "=")
	switch {
	case !ok:
		return nil, fmt.Errorf("--run takes %s: %q is no setting", use, args[1])
	case key != SourceCallsPerHour && key != SourceUpdatesPerHour:
		return nil, fmt.Errorf("--run takes %s, and source set with %s= is the operator's to type in a terminal: where a credential comes from is no number", use, key)
	case value == "":
		return nil, fmt.Errorf("--run takes %s, and source set with %s= and nothing after it is the operator's to type in a terminal", use, key)
	}
	n, err := sourceSetting(name, key, value)
	if err != nil {
		return nil, err
	}
	return []string{name, key + "=" + strconv.Itoa(n)}, nil
}

// sourceLimitOf is the source, the setting and the number of a parsed
// source set <source> <setting>=<n>.
func sourceLimitOf(argv []string) (name, key string, n int) {
	key, value, _ := strings.Cut(argv[3], "=")
	n, _ = strconv.Atoi(value)
	return argv[2], key, n
}

// limitOf is the limit of the source that the setting key sets, as it
// stands: the number set, or the one a source has without.
func limitOf(src *Source, key string) int {
	if key == SourceUpdatesPerHour {
		return src.UpdateLimit()
	}
	return src.CallLimit()
}

// limitSays is what a card says of the limit n of the source named as, and
// of the limit has that stands, for the setting key.
func limitSays(key, as string, n, has int) string {
	if key == SourceUpdatesPerHour {
		return fmt.Sprintf("%s may be sent %d calls an hour that change something there (updates) by each session or crew member that may make them. It may be sent %d now.", as, n, has)
	}
	return fmt.Sprintf("%s may be called %d times an hour by each session or crew member that may call it. It may be called %d times now.", as, n, has)
}

// sourceLimitCard is the card of source set <source> <setting>=<n>, for the
// two settings that are limits. The source is named with the host its calls
// go to, and the approval is held to its file and to where its credential
// comes from (Seen). A lower number is asked as a higher one is: every
// setting of a source is the operator's (SetSource), so the card says that
// only the operator takes the change back.
func (w *wtx) sourceLimitCard(argv []string, asking bool) (AskRun, error) {
	name, key, n := sourceLimitOf(argv)
	src, d, err := sourceIn(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	has := limitOf(src, key)
	if asking && n == has {
		return AskRun{}, fmt.Errorf("source %s: %s is %d already, so the command would change nothing", name, key, has)
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{limitSays(key, fmt.Sprintf("the source %s (%s)", name, sourceHost(d)), n, has)},
		Back:   runCommandLine([]string{"source", "set", name, key + "=" + strconv.Itoa(has)}),
		BackBy: "only the operator",
		Seen:   map[string]string{"source " + name: sourceSeen(src)},
	}, nil
}

// sourceLimitStale says why the ask seq for source set <source>
// <setting>=<n> is approved no more: the source is another under its name,
// or the limit is not the one the card says it is.
func (w *wtx) sourceLimitStale(seq uint64, was, now AskRun) error {
	name, key, n := sourceLimitOf(was.Argv)
	if err := w.staleSource(seq, name, was, now); err != nil {
		return err
	}
	// The limit the card was written at is in the command that takes it
	// back, which is the same command with that number.
	_, _, had := sourceLimitOf(strings.Fields(was.Back)[1:])
	_, _, has := sourceLimitOf(strings.Fields(now.Back)[1:])
	switch {
	case had == has:
		return staleCard(seq, was, now)
	case has == n:
		return fmt.Errorf("ask %d says %s of the source %s is %d now, and it is %d already: nothing is left to change. %s", seq, key, name, had, has, nothingRan)
	}
	return fmt.Errorf("ask %d says %s of the source %s is %d now, and it is %d: the card is from before that changed. %s", seq, key, name, had, has, nothingRan)
}

// parseGrant reads the words of source grant <source> read|<call> with its
// --group and --crew, each as often as it is given. The grantees are stored
// once each, the groups before the crew members and each in the order of
// their names.
func parseGrant(rest []string) ([]string, error) {
	const use = "yardr source grant <source> read|<call> --group <group> or --crew <crew member>"
	args, values, err := runWords(use, "source grant", "the operator's", rest, map[string]bool{"--group": true, "--crew": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) != 2:
		return nil, fmt.Errorf("--run takes %s: one source and read or one call, not %d words", use, len(args))
	case len(values) == 0:
		return nil, fmt.Errorf("--run takes %s: it names nobody the grant is for", use)
	}
	out := []string{args[0], args[1]}
	for _, flag := range []string{"--group", "--crew"} {
		names := slices.Clone(values[flag])
		sort.Strings(names)
		for _, name := range slices.Compact(names) {
			out = append(out, flag, name)
		}
	}
	return out, nil
}

// grantOf is the source, the grant and the grantees of a parsed source
// grant, in the order they are written in.
func grantOf(argv []string) (src, what string, to []Grantee) {
	for i := 4; i+1 < len(argv); i += 2 {
		if argv[i] == "--group" {
			to = append(to, Grantee{Group: argv[i+1]})
		} else {
			to = append(to, Grantee{Crew: argv[i+1]})
		}
	}
	return argv[2], argv[3], to
}

// seenKey is the name a run ask keeps what it saw of the grantee under.
func (g Grantee) seenKey() string {
	if g.Crew != "" {
		return "crew " + g.Crew
	}
	return "group " + g.Group
}

// granteeSeen names the grantee to as a card does, by its name and its
// runner, with the agent's kind for one herdr starts, and answers what a
// run ask keeps of it: for a group a SHA-256 over its runner and its
// config, for a crew member one over its runner, its config and its
// directory. They are what a session that calls on the grant is: the
// command an exec group runs, the kind, model, arguments, prompt and
// env_file of a herdr one. PutGroup, SetGroup and PutCrew replace them
// under the same name, so without it the calls would go to sessions the
// card never described. What says how many sessions run, who is a member,
// and what the daemon writes as a crew member runs is no part of it.
func (w *wtx) granteeSeen(to Grantee) (as, fingerprint string, err error) {
	if err := grantee(w.Tx, to); err != nil {
		return "", "", err
	}
	// A runner and a kind are kept as they were written, by anyone who
	// defines a group or a crew member, and stand here inside the yard's own
	// sentence: one that is no plain name is quoted, so that it cannot end
	// the sentence or begin a line of the card.
	word := func(s string) string {
		if peerMailName.MatchString(s) {
			return s
		}
		return strconv.Quote(s)
	}
	kind := func(runner string, config map[string]any) string {
		if k, _ := config["kind"].(string); runner == "herdr" && k != "" {
			return runner + ", " + word(k)
		}
		return word(runner)
	}
	if to.Crew != "" {
		var r CrewMember
		if _, err := get(w.Tx, bCrew, to.Crew, &r); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("the crew member %s (%s)", r.Name, kind(r.Runner, r.Config)), seen(r.Runner, r.Config, r.Dir), nil
	}
	var g Group
	if _, err := get(w.Tx, bGroups, to.Group, &g); err != nil {
		return "", "", err
	}
	as = fmt.Sprintf("the group %s (%s)", g.Name, kind(g.Runner, g.Config))
	if g.Runner != "manual" {
		as += ", and so each session it starts,"
	}
	return as, seen(g.Runner, g.Config), nil
}

// callSays names a call as a card does: by its name, its method and its
// path as the declaration has them, since the name alone says nothing of
// what the call does.
func callSays(c source.Call) string {
	return oneLine(fmt.Sprintf("%s (%s %s)", c.Name, c.Request.Method, c.Request.Path))
}

// grantCard is the card of source grant: a sentence for each grantee, which
// names the calls the grant is of with their methods and paths, says of
// read which calls that is today and that it covers what the source is
// later given, and of an update call that it changes something there. The
// approval is held to the declaration the card was written from, and to
// each group and crew member as it was (Seen): GrantSource checks a grant
// against the declaration as it is when it runs. Asking refuses a grantee
// that holds the grant already.
func (w *wtx) grantCard(argv []string, asking bool) (AskRun, error) {
	name, what, to := grantOf(argv)
	src, d, err := sourceIn(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	if err := grantable(d, what); err != nil {
		return AskRun{}, err
	}
	of := fmt.Sprintf("of the source %s (%s)", name, sourceHost(d))
	if d.Credential != nil {
		of += ", with the credential the yard holds for it"
	}
	may := ""
	if c, ok := d.Call(what); what == GrantRead {
		var reads []string
		for _, c := range d.Calls {
			if c.Kind == source.Read {
				reads = append(reads, callSays(c))
			}
		}
		today := "today that is " + strings.Join(reads, ", ")
		if len(reads) == 0 {
			today = "today the source has none"
		}
		may = fmt.Sprintf("may make every read call %s: %s, and it covers any read call the operator later gives the source.", of, today)
	} else if ok && c.Kind == source.Update {
		may = fmt.Sprintf("may make the call %s %s, which changes something there.", callSays(*c), of)
	} else if ok {
		may = fmt.Sprintf("may make the call %s %s, which the declaration says reads there and changes nothing.", callSays(*c), of)
	}
	card := AskRun{Argv: argv, Seen: map[string]string{"source " + name: sourceSeen(src)}}
	back := []string{"source", "revoke", name, what}
	for _, t := range to {
		as, fingerprint, err := w.granteeSeen(t)
		if err != nil {
			return AskRun{}, err
		}
		var g SourceGrant
		if _, err := get(w.Tx, bSourceGrants, t.key(name), &g); err != nil {
			return AskRun{}, err
		}
		c, isCall := d.Call(what)
		switch {
		case !asking:
		case slices.Contains(g.Calls, what):
			return AskRun{}, fmt.Errorf("%s is granted %s of the source %s already: leave it out of the command, which would change nothing for it", t, what, name)
		case isCall && g.Covers(c):
			return AskRun{}, fmt.Errorf("%s may make %s of the source %s already, through its grant of %s: leave it out of the command, which would change nothing for it", t, what, name, GrantRead)
		}
		held := "nothing"
		if len(g.Calls) > 0 {
			words := slices.Clone(g.Calls)
			for i, word := range words {
				if word == GrantRead {
					words[i] = GrantRead + " (every read call)"
				}
			}
			held = andList(words)
		}
		card.Does = append(card.Does, fmt.Sprintf("%s %s It is granted %s of %s now.", as, may, held, name))
		card.Seen[t.seenKey()] = fingerprint
		if t.Crew != "" {
			back = append(back, "--crew", t.Crew)
		} else {
			back = append(back, "--group", t.Group)
		}
	}
	card.Back = runCommandLine(back)
	return card, nil
}

// grantStale says why the ask seq for source grant is approved no more: the
// source, a group or a crew member is another under its name, or a grantee
// holds another grant than the card says.
func (w *wtx) grantStale(seq uint64, was, now AskRun) error {
	name, _, to := grantOf(was.Argv)
	if err := w.staleSource(seq, name, was, now); err != nil {
		return err
	}
	for _, t := range to {
		if key := t.seenKey(); was.Seen[key] == now.Seen[key] {
			continue
		}
		if t.Crew != "" {
			e := lastEvent(w.Tx, "crew_defined", naming("crew", t.Crew))
			return fmt.Errorf("ask %d was written when the crew member %s was another: its config was changed since%s. %s", seq, t.Crew, since(e), nothingRan)
		}
		e := lastEvent(w.Tx, "group_set", naming("name", t.Group))
		return fmt.Errorf("ask %d was written when the group %s was another: its config was changed since%s. %s", seq, t.Group, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runGrant gives the grant of a parsed source grant to each of its
// grantees, in this one write, and answers a line for each as yardr source
// grant prints it.
func runGrant(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, what, to := grantOf(argv)
	d, err := declared(w.Tx, name)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, t := range to {
		g, err := w.grantSource(ctx, name, what, t, actor, by)
		if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s may call %s: %s", g.Grantee, g.Source, g.Words(d)))
	}
	return strings.Join(lines, "\n"), nil
}

// parseSubscribe reads the words of subscribe <user> yardr with --done and
// --where. The row is of the yard's own events alone: what a hook source
// sends is not the yard's to say, and whether a name is one the store
// cannot tell. A subscription to everything is refused, since the command
// that takes one back names its filters.
func parseSubscribe(rest []string) ([]string, error) {
	const use = "yardr subscribe <user> yardr [--done] [--where <key>=<value>]"
	const whose = "the user's own or the operator's"
	args, values, err := runWords(use, "subscribe", whose, rest, map[string]bool{"--done": false, "--where": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) != 2:
		return nil, fmt.Errorf("--run takes %s: one user and the source yardr, not %d words", use, len(args))
	case args[1] != SourceYardr:
		return nil, fmt.Errorf("--run takes %s: it subscribes a user to the yard's own events, and a subscription to %s is %s to type in a terminal", use, args[1], whose)
	}
	out := []string{args[0], SourceYardr}
	done := len(values["--done"]) > 0
	var where []string
	for _, kv := range values["--where"] {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok || k == "" || v == "":
			return nil, fmt.Errorf("--run takes %s: --where %q: want key=value", use, kv)
		case k == "state" && v == StateDone:
			done = true
		case k == "state":
			return nil, fmt.Errorf("subscribe: state %q: only %s", v, StateDone)
		case !slices.Contains(where, kv):
			where = append(where, kv)
		}
	}
	if done {
		out = append(out, "--done")
	}
	// The keys in their order, the values of one key as they were typed:
	// each is tried in turn, and the first to match is the reason given.
	sort.SliceStable(where, func(i, j int) bool {
		a, _, _ := strings.Cut(where[i], "=")
		b, _, _ := strings.Cut(where[j], "=")
		return a < b
	})
	for _, kv := range where {
		out = append(out, "--where", kv)
	}
	if len(out) == 2 {
		return nil, fmt.Errorf("--run takes %s: with neither it is every event of every bead, and the command that takes a subscription back names its filters", use)
	}
	if _, sub := subscribeOf(append([]string{"subscribe"}, out...)); sub.check() != nil {
		return nil, sub.check()
	}
	return out, nil
}

// subscribeOf is the user and the subscription of a parsed subscribe, as
// yardr subscribe makes it of the same words: every event the filters take.
func subscribeOf(argv []string) (user string, sub Subscription) {
	vals := map[string][]string{}
	for i := 3; i < len(argv); i++ {
		if argv[i] == "--done" {
			vals["state"] = []string{StateDone}
			continue
		}
		i++
		k, v, _ := strings.Cut(argv[i], "=")
		vals[k] = append(vals[k], v)
	}
	sub = Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{}}
	for k, vs := range vals {
		sub.Where[k] = JoinWhere(vs)
	}
	return argv[1], sub
}

// subscribeCard is the card of subscribe <user> yardr: what will then land
// among the notifications of the user's inbox, in the yard's words and with
// the subscription as it is stored, and whether each is sent on to the
// user outside the yard. The approval is held to where the user is told
// (Seen): what lands in the inbox goes there. Asking refuses a subscription
// the user has already.
func (w *wtx) subscribeCard(argv []string, asking bool) (AskRun, error) {
	name, sub := subscribeOf(argv)
	var u User
	if ok, err := get(w.Tx, bUsers, name, &u); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("a run ask subscribes a user of the yard (yardr user list), and %s is none: what a crew member or a manual group hears is set with yardr subscribe, by anyone", name)
	}
	if err := w.bindSubscription(name, &sub); err != nil {
		return AskRun{}, err
	}
	has := slices.ContainsFunc(u.Subscriptions, sub.same)
	if asking && has {
		return AskRun{}, fmt.Errorf("%s is subscribed to %s already, so the command would change nothing", name, sub)
	}
	what := "every event of a bead"
	if sub.Where["state"] == StateDone {
		what = "each bead that becomes done"
	}
	var filters []string
	back := []string{"unsubscribe", name, SourceYardr}
	for _, kv := range strings.Fields(whereString(sub.Where)) {
		back = append(back, "--where", kv)
	}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range sub.Where {
			if k != "state" && !yield(k) {
				return
			}
		}
	}) {
		filters = append(filters, k+" is "+strings.Join(WhereValues(sub.Where[k]), " or "))
	}
	if len(filters) > 0 {
		what += " where " + strings.Join(filters, ", and ")
	}
	now := "They have no such subscription now."
	if has {
		now = "They have that subscription already."
	}
	out := fmt.Sprintf("Nothing sends it on outside the yard: no source tells %s of notifications there.", name)
	if u.Notify != "" && u.Tells(PushNotification) {
		out = fmt.Sprintf("Each is also sent to %s outside the yard, through the source %s: it reaches their phone, or wherever %s tells them.", name, u.Notify, u.Notify)
	}
	return AskRun{
		Argv: argv,
		Does: []string{
			fmt.Sprintf("%s gets a line among the notifications of their inbox for %s (the subscription %s). %s", name, what, sub, now),
			out,
		},
		Back:   runCommandLine(back),
		BackBy: name + " or the operator",
		Seen:   map[string]string{"user " + name: seen(u.Notify, u.EnvFile, u.Tell)},
	}, nil
}

// subscribeStale says why the ask seq for subscribe is approved no more:
// the user is told outside the yard another way than when the card was
// written, or the card would read otherwise now.
func (w *wtx) subscribeStale(seq uint64, was, now AskRun) error {
	name, _ := subscribeOf(was.Argv)
	return w.staleTold(seq, name, was, now)
}

// staleTold says why the ask seq, written about the user name, is approved
// no more: where the user is told outside the yard, or of which parts, was
// set since, or the card would read otherwise now.
func (w *wtx) staleTold(seq uint64, name string, was, now AskRun) error {
	if key := "user " + name; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "user_set", naming("user", name))
		return fmt.Errorf("ask %d was written when %s was told outside the yard another way: that was set since%s. %s", seq, name, since(e), nothingRan)
	}
	return staleCard(seq, was, now)
}

// runSubscribe subscribes the user of a parsed subscribe as actor, under
// the check a subscription typed at a terminal has (maySubscribe): the user
// subscribes themselves, or the operator does it for them. It answers the
// line yardr subscribe prints.
func runSubscribe(_ context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, sub := subscribeOf(argv)
	if err := w.bindSubscription(name, &sub); err != nil {
		return "", err
	}
	if err := w.addSubscription(name, sub, actor, by); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s subscribed: %s", name, sub), nil
}

// parseUserTell reads the words of user set <user> tell=<parts>: the parts
// each once and in the order the yard keeps them in, whatever was typed,
// and none for all of them. The other settings of a user refuse the
// command: they decide where a person's inbox goes and with which
// credential, and a path is no word of a command that is approved from a
// card.
func parseUserTell(rest []string) ([]string, error) {
	const use = "yardr user set <user> tell=<parts>"
	args, _, err := runWords(use, "user set", "the operator's", rest, nil)
	if err != nil {
		return nil, err
	}
	for _, arg := range args {
		if key, _, ok := strings.Cut(arg, "="); ok && (key == UserNotify || key == UserEnvFile) {
			return nil, fmt.Errorf("--run takes %s, and user set with %s= is typed by the operator, or by the user themselves, in a terminal: it decides where a person's inbox goes, and with which credential", use, key)
		}
	}
	if len(args) != 2 {
		return nil, fmt.Errorf("--run takes %s: one user and one %s=, not %d words. Anything more of user set is typed by the operator, or by the user themselves, in a terminal", use, UserTell, len(args))
	}
	name := args[0]
	key, value, ok := strings.Cut(args[1], "=")
	if !ok || key != UserTell {
		return nil, fmt.Errorf("--run takes %s: %q is no %s=", use, args[1], UserTell)
	}
	parts, err := parseTell(value)
	if err != nil {
		return nil, fmt.Errorf("user %s: %w", name, err)
	}
	return []string{name, UserTell + "=" + strings.Join(parts, ",")}, nil
}

// userTellOf is the user and the value of tell of a parsed user set <user>
// tell=<parts>, as SetUser takes it.
func userTellOf(argv []string) (name, tell string) {
	_, tell, _ = strings.Cut(argv[3], "=")
	return argv[2], tell
}

// userTellCard is the card of user set <user> tell=<parts>: of which parts
// the user is then told outside the yard and of which now, through which
// source, and that what waited to be told of a part that is dropped goes
// with it. A user no source tells is told nothing by the change, and the
// card says so. The approval is held to where the user is told and of what
// (Seen), and the command that takes it back sets the parts that stand.
// Asking refuses the parts the user is told of already.
func (w *wtx) userTellCard(argv []string, asking bool) (AskRun, error) {
	name, tell := userTellOf(argv)
	var u User
	if ok, err := get(w.Tx, bUsers, name, &u); err != nil {
		return AskRun{}, err
	} else if !ok {
		return AskRun{}, fmt.Errorf("a run ask sets what a user of the yard is told of (yardr user list), and %s is none", name)
	}
	parts, err := parseTell(tell)
	if err != nil {
		return AskRun{}, fmt.Errorf("user %s: %w", name, err)
	}
	has, then := u.toldOf(), (&User{Tell: parts}).toldOf()
	if asking && slices.Equal(has, then) {
		return AskRun{}, fmt.Errorf("%s is told of %s outside the yard already, so the command would change nothing", name, andList(has))
	}
	does := []string{fmt.Sprintf("the source %s tells %s outside the yard of %s: on their phone, or wherever %s tells them. It tells them of %s now.", u.Notify, name, andList(then), u.Notify, andList(has))}
	if u.Notify == "" {
		does = []string{fmt.Sprintf("%s has no notify source, so nothing tells them outside the yard, and the change tells them nothing until one is set (yardr user set %s %s=<source>). A source would then tell them of %s; it would tell them of %s as things stand.", name, name, UserNotify, andList(then), andList(has))}
	}
	if dropped := slices.DeleteFunc(slices.Clone(has), func(part string) bool { return slices.Contains(then, part) }); len(dropped) > 0 {
		does = append(does, fmt.Sprintf("What still waited to be told of %s is dropped, and is not told later. Their inbox and their queue of decisions in the yard are filled as before.", andList(dropped)))
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine([]string{"user", "set", name, UserTell + "=" + strings.Join(u.Tell, ",")}),
		BackBy: name + " or the operator",
		Seen:   map[string]string{"user " + name: seen(u.Notify, u.EnvFile, u.Tell)},
	}, nil
}

// userTellStale says why the ask seq for user set <user> tell=<parts> is
// approved no more: the user's tell or notify was set since the card was
// written.
func (w *wtx) userTellStale(seq uint64, was, now AskRun) error {
	name, _ := userTellOf(was.Argv)
	return w.staleTold(seq, name, was, now)
}

// runUserTell sets the parts of a parsed user set <user> tell=<parts> as
// actor, under the check the setting typed at a terminal has (mayTell): the
// user sets their own, or the operator does it for them. It answers the
// line yardr user set prints, without whether the source is set up.
func runUserTell(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, tell := userTellOf(argv)
	u, err := w.setUser(ctx, name, []string{UserTell}, map[string]string{UserTell: tell}, actor, by)
	if err != nil {
		return "", err
	}
	return "user " + u.Name + ": told outside the yard: " + or(u.Told(), "nowhere"), nil
}
