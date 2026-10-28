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

// CardedUnder is the head of a stale-build refusal: the build the ask was
// carded under and the one the yard runs now. A card written before cards
// kept the build names none: it was carded under an older yardr, and its
// refusal says to decline it and ask again, so old cards drain.
func CardedUnder(seq uint64, wasBuild, nowBuild string) string {
	return fmt.Sprintf("ask %d was carded under %s and this yard now runs %s", seq, buildWords(wasBuild), buildWords(nowBuild))
}

// buildWords names a build in a stale-build refusal: "yardr <build>", or
// "an older yardr" where the card kept none. The build is carried
// structurally, never trimmed out of the label that holds it beside its
// provenance.
func buildWords(build string) string {
	if build == "" {
		return "an older yardr"
	}
	return "yardr " + build
}

// LabelWithoutBuild is a pack's label with its build left out: "embedded in
// yardr", whichever build embeds it. An approval is held to the pack's bytes
// and to who embeds them; the build is provenance, which the card keeps in
// Seen and the head of a refusal names (CardedUnder), and a build that moved
// under the same bytes changes nothing the approval runs. A card that kept
// no build has none to leave out, so its label stands whole and differs from
// every label that is stripped: it was carded by an older yardr, and it
// drains.
func LabelWithoutBuild(label, build string) string {
	if build == "" {
		return label
	}
	return strings.TrimSpace(strings.TrimSuffix(label, build))
}

// seen is what a run ask keeps of a thing its command names: a SHA-256 over
// the parts of it the command's effect rests on, whole. The yard does not
// judge which change of them matters: any makes the ask stale.
func seen(parts ...any) string {
	text, _ := json.Marshal(parts)
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// sourceSeen is what a run ask keeps of a source: its declaration text,
// where its credential comes from and where its calls go, and the whole
// destination inventory of its command copies (inventoryOf). A replaced
// file may keep a call's name, method and path and send another body, and
// every sentence then reads as before; a copy changed, added, removed or
// chmodded under it reads as nothing on the card, and is caught here alone.
func (w *wtx) sourceSeen(src *Source) (string, error) {
	home := ""
	if w.store != nil {
		home = w.store.home
	}
	return sourceFingerprint(src, home)
}

func sourceFingerprint(src *Source, home string) (string, error) {
	inv, _, err := sourceInventory(home, src.Name, false)
	if err != nil {
		return "", err
	}
	if len(inv) > 0 {
		return seen(src.Text, src.EnvFile, src.MintCommand, src.Base, inv), nil
	}
	return seen(src.Text, src.EnvFile, src.MintCommand, src.Base), nil
}

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
		_, base := e.Data[SourceBase]
		return e.Data["source"] == name && (file || mint || base)
	})
	switch {
	case set != nil && (added == nil || set.Seq > added.Seq):
		if _, base := set.Data[SourceBase]; base {
			return fmt.Errorf("ask %d was written when the source %s used another base: its base was set since%s. %s", seq, name, since(set), nothingRan)
		}
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

// doesWithoutBuild is a card's sentences with the build left out of the
// label they name the pack by. The sentence that opens a pack or a setup
// card names the label ("pack x (embedded in yardr <build>), of kind ..."),
// and the card is compared sentence for sentence, so without this a build
// that moved under the same bytes would still read as another card.
func doesWithoutBuild(does []string, label, build string) []string {
	base := LabelWithoutBuild(label, build)
	if label == "" || base == label {
		return does
	}
	out := make([]string, len(does))
	for i, line := range does {
		out[i] = strings.ReplaceAll(line, label, base)
	}
	return out
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

// parseSourceLimit reads the words of source set <source> <setting>=<n>.
// Where the credential comes from, and where it is sent, is read too,
// and refused as the operator's alone by the check, not here: a path, a
// program and a base are no words of a command that is approved from a
// card.
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
	case key != SourceCallsPerHour && key != SourceUpdatesPerHour && key != SourceEnvFile && key != SourceMintCommand && key != SourceBase:
		return nil, fmt.Errorf("source %s: %s is no setting of a source (it has: %s)", name, key, strings.Join(SourceSettings, ", "))
	case value == "":
		return nil, fmt.Errorf("--run takes %s, and source set with %s= and nothing after it is the operator's to type in a terminal", use, key)
	}
	if key == SourceEnvFile || key == SourceMintCommand || key == SourceBase {
		return []string{name, args[1]}, nil
	}
	n, err := sourceSetting(name, key, value)
	if err != nil {
		return nil, err
	}
	return []string{name, key + "=" + strconv.Itoa(n)}, nil
}

// checkSourceLimit is the source set's own check as the asker: the two
// limits may be proposed, and where the credential comes from, and where
// it is sent, is the operator's alone.
func checkSourceLimit(ctx context.Context, w *wtx, argv []string, actor string) error {
	name, key, _ := sourceLimitOf(argv)
	_, err := w.sourceSetCheck(ctx, name, key, actor)
	return err
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
	d = d.WithBase(src.Base)
	has := limitOf(src, key)
	if asking && n == has {
		return AskRun{}, fmt.Errorf("source %s: %s is %d already, so the command would change nothing", name, key, has)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	return AskRun{
		Argv:   argv,
		Does:   []string{limitSays(key, fmt.Sprintf("the source %s (%s)", name, sourceHost(d)), n, has)},
		Back:   runCommandLine([]string{"source", "set", name, key + "=" + strconv.Itoa(has)}),
		BackBy: "only the operator",
		Seen:   map[string]string{"source " + name: fingerprint},
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
// --group and --crew, each as often as it is given, and its --as <user>
// where the grant is of a call made as that user. --preset <name> stands
// in for read or the call: the preset's calls of the source, from the
// embedded pack that declares it. The grantees are stored once each, the
// groups before the crew members and each in the order of their names,
// and the user last.
func parseGrant(rest []string) ([]string, error) {
	const use = "yardr source grant <source> read|<call>|--preset <preset> --group <group> or --crew <crew member> [--as <user>]"
	args, values, err := runWords(use, "source grant", "the operator's", rest, map[string]bool{"--group": true, "--crew": true, "--as": true, "--preset": true})
	if err != nil {
		return nil, err
	}
	presets := values["--preset"]
	switch {
	case len(presets) > 1:
		return nil, fmt.Errorf("--run takes %s: one --preset <name>, not %d", use, len(presets))
	case len(presets) == 1 && len(args) == 2:
		return nil, fmt.Errorf("--run takes %s: %s and --preset %s together name two grants: name read, one call or --preset <preset>", use, args[1], presets[0])
	case len(presets) == 1 && len(args) != 1:
		return nil, fmt.Errorf("--run takes %s: one source and --preset <preset>, not %d words", use, len(args))
	case len(presets) == 0 && len(args) != 2:
		return nil, fmt.Errorf("--run takes %s: one source and read or one call, not %d words", use, len(args))
	case len(values["--group"])+len(values["--crew"]) == 0:
		return nil, fmt.Errorf("--run takes %s: it names nobody the grant is for", use)
	case len(values["--as"]) > 1:
		return nil, fmt.Errorf("--run takes %s: one --as <user>, not %d", use, len(values["--as"]))
	}
	var out []string
	if len(presets) == 1 {
		out = []string{args[0], "--preset", presets[0]}
	} else {
		out = []string{args[0], args[1]}
	}
	for _, flag := range []string{"--group", "--crew"} {
		names := slices.Clone(values[flag])
		sort.Strings(names)
		for _, name := range slices.Compact(names) {
			out = append(out, flag, name)
		}
	}
	if as := values["--as"]; len(as) > 0 {
		out = append(out, "--as", as[0])
	}
	return out, nil
}

// grantOf is the source, the grant, the grantees and the user called as
// of a parsed source grant, in the order they are written in. A preset
// grant names its preset instead of the grant: what is then empty and
// preset holds the name.
func grantOf(argv []string) (src, what, preset string, to []Grantee, as string) {
	i := 4
	if argv[3] == "--preset" {
		preset = argv[4]
		i = 5
	} else {
		what = argv[3]
	}
	for ; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--group":
			to = append(to, Grantee{Group: argv[i+1]})
		case "--crew":
			to = append(to, Grantee{Crew: argv[i+1]})
		case "--as":
			as = argv[i+1]
		}
	}
	return argv[2], what, preset, to, as
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

// PresetReading is what the server's reading of one preset hands the
// store: the pack declaring the source, the preset's name, the build
// label it is embedded in, and the preset's calls of the source, in file
// order. Nothing of it is the asker's.
type PresetReading struct {
	Pack   string
	Preset string
	Label  string // "embedded in yardr <build>"
	Build  string // the <build> of Label, kept apart so refusals name it
	Calls  []string
}

// PresetReader resolves the preset of the source against the packs the
// owning process embeds, for a preset grant ask: the one pack of kind
// source declaring the source, and its preset of that name. An error
// refuses the ask, storing nothing, in the reader's words.
type PresetReader func(source, preset string) (*PresetReading, error)

// SetPresetReader tells the store how its owning process reads the
// presets of the packs yardr embeds, as SetPackReader tells it how it
// reads the packs. A store no server owns has none, and a preset grant
// ask of it is refused naming the server.
func (s *Store) SetPresetReader(r PresetReader) { s.presetReader = r }

// presetReaderOf is the reading registered on the store this write
// belongs to, nil where there is none.
func (w *wtx) presetReaderOf() PresetReader {
	if w.store == nil {
		return nil
	}
	return w.store.presetReader
}

// presetOf resolves the preset of the source through the server's reading
// of the packs yardr embeds, and holds its calls to the yard's
// declaration: each must be grantable, since the card renders and the
// approval grants each by name. A preset whose calls the yard's
// declaration no longer has is granted by name in a terminal.
func (w *wtx) presetOf(name, preset string) (*PresetReading, error) {
	read := w.presetReaderOf()
	if read == nil {
		return nil, fmt.Errorf("source %s: no server has registered its reading of the packs yardr embeds on this store, so no preset can be resolved from the binary that runs the yard: a preset grant is proposed from a yard its server runs", name)
	}
	pr, err := read(name, preset)
	if err != nil {
		return nil, err
	}
	if pr == nil || pr.Pack == "" || len(pr.Calls) == 0 {
		return nil, fmt.Errorf("source %s: the server's reading of the preset %s holds no pack or calls: nothing was stored", name, preset)
	}
	d, err := declared(w.Tx, name)
	if err != nil {
		return nil, err
	}
	for _, what := range pr.Calls {
		if err := grantable(d, what); err != nil {
			return nil, fmt.Errorf("the preset %s of pack %s grants %s of the source %s, and the yard's declaration has no such call (it has: %s): grant by name in a terminal (yardr source grant %s read|<call> --group <group> | --crew <crew member>)", preset, pr.Pack, what, name, or(strings.Join(callNames(d), ", "), "none"), name)
		}
	}
	return pr, nil
}

// presetHeld reports whether the grant g holds every call of calls of the
// declaration d as the user as: a call by its name, or a read call
// through a grant of read, as the grant row holds one.
func presetHeld(g SourceGrant, d *source.Declaration, calls []string, as string) bool {
	for _, what := range calls {
		if slices.Contains(g.Calls, what) {
			continue
		}
		if c, ok := d.Call(what); ok && g.Covers(c, as) {
			continue
		}
		return false
	}
	return true
}

// grantCallMay is what a grant card says of one call: of read which calls
// that is today with the later-reads line, of an update call that it
// changes something there, each with method and path.
func grantCallMay(d *source.Declaration, of, what string) string {
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
		return fmt.Sprintf("may make every read call %s: %s, and it covers any read call the operator later gives the source.", of, today)
	} else if ok && c.Kind == source.Update {
		return fmt.Sprintf("may make the call %s %s, which changes something there.", callSays(*c), of)
	} else if ok {
		return fmt.Sprintf("may make the call %s %s, which the declaration says reads there and changes nothing.", callSays(*c), of)
	}
	return ""
}

// grantCard is the card of source grant: a sentence for each grantee, which
// names the calls the grant is of with their methods and paths, says of
// read which calls that is today and that it covers what the source is
// later given, and of an update call that it changes something there. A
// preset grant names the preset and the pack beside the grantee, and
// renders each of the preset's calls as a grant of it reads. The
// approval is held to the declaration the card was written from, to the
// preset's bytes with the build where one is named, and to each group
// and crew member as it was (Seen): GrantSource checks a grant
// against the declaration as it is when it runs. Asking refuses a grantee
// that holds the grant already, and an approval runs past what one was
// granted since unless it holds the grant already (grantStale).
func (w *wtx) grantCard(argv []string, asking bool) (AskRun, error) {
	name, what, preset, to, as := grantOf(argv)
	src, d, err := sourceIn(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	d = d.WithBase(src.Base)
	var pack, label, build string
	calls := []string{what}
	if preset != "" {
		pr, err := w.presetOf(name, preset)
		if err != nil {
			return AskRun{}, err
		}
		pack, label, build, calls = pr.Pack, pr.Label, pr.Build, pr.Calls
	} else if err := grantable(d, what); err != nil {
		return AskRun{}, err
	}
	of := fmt.Sprintf("of the source %s (%s)", name, sourceHost(d))
	switch {
	case as != "":
		of += fmt.Sprintf(" as %s, with the credential in %s's env file", as, as)
	case d.Credential != nil:
		of += ", with the credential the yard holds for it"
	}
	mays := make([]string, len(calls))
	for i, call := range calls {
		mays[i] = grantCallMay(d, of, call)
	}
	may, with := strings.Join(mays, " "), ""
	if preset != "" {
		// A space and no comma before it: a group's sentence ends in
		// one already ("and so each session it starts,").
		with = fmt.Sprintf(" with the preset %s of pack %s,", preset, pack)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	card := AskRun{Argv: argv, Seen: map[string]string{"source " + name: fingerprint}}
	if preset != "" {
		card.Seen["pack "+pack] = label
		card.Seen["pack "+pack+" build"] = build
		card.Seen["preset "+pack+" "+preset] = seen(calls)
	}
	if as != "" {
		u, err := getUser(w.Tx, as)
		if err != nil {
			return AskRun{}, err
		}
		card.Seen["user "+as] = seen(u.Name, u.EnvFile, u.CreatedAt)
	}
	var flags []string
	for _, t := range to {
		who, fingerprint, err := w.granteeSeen(t)
		if err != nil {
			return AskRun{}, err
		}
		var g SourceGrant
		if _, err := get(w.Tx, bSourceGrants, grantKey(name, t, as), &g); err != nil {
			return AskRun{}, err
		}
		holder := (&SourceGrant{Grantee: t, As: as}).Holder()
		if preset != "" {
			if asking && presetHeld(g, d, calls, as) {
				return AskRun{}, fmt.Errorf("%s holds every call of the preset %s of pack %s already: leave it out of the command, which would change nothing for it", holder, preset, pack)
			}
		} else {
			c, isCall := d.Call(what)
			switch {
			case !asking:
			case slices.Contains(g.Calls, what):
				return AskRun{}, fmt.Errorf("%s is granted %s of the source %s already: leave it out of the command, which would change nothing for it", holder, what, name)
			case isCall && g.Covers(c, as):
				return AskRun{}, fmt.Errorf("%s may make %s of the source %s already, through its grant of %s: leave it out of the command, which would change nothing for it", holder, what, name, GrantRead)
			}
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
		heldOf := name
		if as != "" {
			heldOf = name + " as " + as
		}
		card.Does = append(card.Does, fmt.Sprintf("%s%s %s It is granted %s of %s now.", who, with, may, held, heldOf))
		card.Seen[t.seenKey()] = fingerprint
		if t.Crew != "" {
			flags = append(flags, "--crew", t.Crew)
		} else {
			flags = append(flags, "--group", t.Group)
		}
	}
	if as != "" {
		flags = append(flags, "--as", as)
	}
	backs := make([]string, len(calls))
	for i, call := range calls {
		backs[i] = runCommandLine(append([]string{"source", "revoke", name, call}, flags...))
	}
	card.Back = strings.Join(backs, "; ")
	return card, nil
}

// grantAction is what a grant card's Does line says the approval would
// do, without what the grantee holds now: the line up to the "It is
// granted ... now." sentence grantCard ends it with.
func grantAction(line string) string {
	action, _, _ := strings.Cut(line, " It is granted ")
	return action
}

// sameGrantAction reports whether the grant cards was and now say the
// approval would do the same: the member, the call, and the source's
// call as declared. What a grantee holds now is no part of it.
func sameGrantAction(was, now []string) bool {
	if len(was) != len(now) {
		return false
	}
	for i := range was {
		if grantAction(was[i]) != grantAction(now[i]) {
			return false
		}
	}
	return true
}

// grantedMatch matches the event of the grant of what of the source name
// to the grantee t as the user as.
func grantedMatch(name string, t Grantee, as, what string) func(Event) bool {
	return func(e Event) bool {
		if e.Data["source"] != name || e.Data["what"] != what || e.Data["as"] != as {
			return false
		}
		if t.Crew != "" {
			return e.Data["crew"] == t.Crew
		}
		return e.Data["group"] == t.Group
	}
}

// grantedPresetMatch matches the event of the grant of one of calls of
// the source name to the grantee t as the user as.
func grantedPresetMatch(name string, t Grantee, as string, calls []string) func(Event) bool {
	return func(e Event) bool {
		what, _ := e.Data["what"].(string)
		if e.Data["source"] != name || e.Data["as"] != as || !slices.Contains(calls, what) {
			return false
		}
		if t.Crew != "" {
			return e.Data["crew"] == t.Crew
		}
		return e.Data["group"] == t.Group
	}
}

// presetBytesStale says why a preset grant approval is refused for the
// preset: the source's preset is declared by another pack now, or its calls
// differ from the card's. A build that moved under unchanged calls is no
// reason: the approval grants those calls (LabelWithoutBuild). Only the
// preset's calls of this source are held: what it grants of another source
// reads nowhere on this card.
func (w *wtx) presetBytesStale(seq uint64, name, preset string, was, now AskRun) error {
	wasPack, nowPack := presetPackOf(was.Seen), presetPackOf(now.Seen)
	if wasPack == "" || nowPack == "" {
		return staleCard(seq, was, now)
	}
	if wasPack != nowPack {
		return fmt.Errorf("ask %d was written from the preset %s of pack %s: the source %s is declared by pack %s now. %s", seq, preset, wasPack, name, nowPack, nothingRan)
	}
	wasBuild, nowBuild := was.Seen["pack "+wasPack+" build"], now.Seen["pack "+nowPack+" build"]
	wasLabel := LabelWithoutBuild(was.Seen["pack "+wasPack], wasBuild)
	nowLabel := LabelWithoutBuild(now.Seen["pack "+nowPack], nowBuild)
	key := "preset " + wasPack + " " + preset
	if was.Seen[key] != now.Seen[key] || wasLabel != nowLabel {
		return fmt.Errorf("%s, whose preset differs. %s", CardedUnder(seq, wasBuild, nowBuild), nothingRan)
	}
	return nil
}

// presetPackOf is the pack a preset grant card saw its preset in: the
// "pack <pack>" key the card keeps, beside its "pack <pack> build". A
// pack's name holds no space, so a remainder with one is the build key.
func presetPackOf(seen map[string]string) string {
	for key := range seen {
		pack, ok := strings.CutPrefix(key, "pack ")
		if !ok || strings.Contains(pack, " ") {
			continue
		}
		return pack
	}
	return ""
}

// grantStale says why the ask seq for source grant is approved no more:
// the source, a group or a crew member is another under its name, the
// user called as is another, the preset's bytes moved where one is
// named, what the approval would do reads otherwise than the card
// says, or a grantee holds the grant already. What a grantee holds
// beside the grant is no part of it: a grant landed since is no reason
// to ask again, and nil carries the approval on.
func (w *wtx) grantStale(seq uint64, was, now AskRun) error {
	name, what, preset, to, as := grantOf(was.Argv)
	if err := w.staleSource(seq, name, was, now); err != nil {
		return err
	}
	if as != "" && was.Seen["user "+as] != now.Seen["user "+as] {
		e := lastEvent(w.Tx, "user_set", naming("user", as))
		return fmt.Errorf("ask %d was written when the user %s was another: what a source needs of them was set since%s. %s", seq, as, since(e), nothingRan)
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
	if preset != "" {
		if err := w.presetBytesStale(seq, name, preset, was, now); err != nil {
			return err
		}
	}
	if !sameGrantAction(was.Does, now.Does) {
		return staleCard(seq, was, now)
	}
	d, err := declared(w.Tx, name)
	if err != nil {
		return err
	}
	if preset != "" {
		pr, err := w.presetOf(name, preset)
		if err != nil {
			// The card the approval compares against resolved it in
			// this same write, so this names what moved instead.
			return staleCard(seq, was, now)
		}
		for _, t := range to {
			var g SourceGrant
			if _, err := get(w.Tx, bSourceGrants, grantKey(name, t, as), &g); err != nil {
				return err
			}
			if presetHeld(g, d, pr.Calls, as) {
				holder := (&SourceGrant{Grantee: t, As: as}).Holder()
				e := lastEvent(w.Tx, "source_granted", grantedPresetMatch(name, t, as, pr.Calls))
				return fmt.Errorf("ask %d would grant the preset %s of pack %s of the source %s to %s, and %s holds it already: it was granted since%s. %s", seq, preset, pr.Pack, name, holder, holder, since(e), nothingRan)
			}
		}
		return nil
	}
	for _, t := range to {
		var g SourceGrant
		if _, err := get(w.Tx, bSourceGrants, grantKey(name, t, as), &g); err != nil {
			return err
		}
		holder := (&SourceGrant{Grantee: t, As: as}).Holder()
		c, isCall := d.Call(what)
		switch {
		case slices.Contains(g.Calls, what):
			e := lastEvent(w.Tx, "source_granted", grantedMatch(name, t, as, what))
			return fmt.Errorf("ask %d would grant %s of the source %s to %s, and %s is granted it already: it was granted since%s. %s", seq, what, name, holder, holder, since(e), nothingRan)
		case isCall && g.Covers(c, as):
			e := lastEvent(w.Tx, "source_granted", grantedMatch(name, t, as, GrantRead))
			return fmt.Errorf("ask %d would grant %s of the source %s to %s, and %s may make it already through its grant of read: read was granted since%s. %s", seq, what, name, holder, holder, since(e), nothingRan)
		}
	}
	return nil
}

// runGrant gives the grant of a parsed source grant to each of its
// grantees, in this one write, and answers a line for each as yardr source
// grant prints it.
func checkGrant(ctx context.Context, w *wtx, argv []string, actor string) error {
	name, what, preset, to, as := grantOf(argv)
	if preset != "" {
		// The check the grant typed at a terminal has: a grant as a
		// user is theirs alone to give, and the card it suggests
		// names the preset, not one of its calls.
		what = "--preset " + preset
	}
	var first Grantee
	if len(to) > 0 {
		first = to[0]
	}
	return w.checkGrantSource(ctx, name, what, first, as, actor)
}

func runGrant(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, what, preset, to, as := grantOf(argv)
	if preset != "" {
		pr, err := w.presetOf(name, preset)
		if err != nil {
			return "", err
		}
		d, err := declared(w.Tx, name)
		if err != nil {
			return "", err
		}
		var lines []string
		for _, t := range to {
			g := &SourceGrant{}
			if _, err := get(w.Tx, bSourceGrants, grantKey(name, t, as), g); err != nil {
				return "", err
			}
			for _, call := range pr.Calls {
				// A read call named by the preset may already be covered
				// by a read grant that landed after the card. Carry out
				// only what is still missing, as the grant row does.
				if call == GrantRead && slices.Contains(g.Calls, GrantRead) {
					continue
				}
				if c, ok := d.Call(call); ok && g.Covers(c, as) {
					continue
				}
				g, err = w.grantSource(ctx, name, call, t, as, actor, by)
				if err != nil {
					return "", err
				}
			}
			lines = append(lines, fmt.Sprintf("%s may call %s: %s", g.Holder(), g.Source, g.Words(d)))
		}
		return strings.Join(lines, "\n"), nil
	}
	d, err := declared(w.Tx, name)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, t := range to {
		g, err := w.grantSource(ctx, name, what, t, as, actor, by)
		if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s may call %s: %s", g.Holder(), g.Source, g.Words(d)))
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
	if err := w.bindSubscription(name, "user", &sub); err != nil {
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
func runSubscribe(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	name, sub := subscribeOf(argv)
	if err := w.bindSubscription(name, "user", &sub); err != nil {
		return "", err
	}
	if err := w.addSubscription(ctx, name, sub, actor, by); err != nil {
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
