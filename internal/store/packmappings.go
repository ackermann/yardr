package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Mappings are the mappings an import of a pack is shaped and answered
// by, as the terminal's flags name them: the flow the depot follows, which
// crew member and group of the yard take the pack's, the binds and members
// that answer a new group or crew member, and what a crew member may open
// threads in and what calls a group or crew member may make. A card holds
// them beside its depot, and the carrying out builds and applies with
// exactly them, so what approval runs is the plan the card showed. The
// zero mappings shape and answer nothing, as a card written before them
// does.
type Mappings struct {
	Flow        string            `json:"flow,omitempty"`
	Crew        map[string]string `json:"crew,omitempty"`
	Groups      map[string]string `json:"groups,omitempty"`
	Binds       []string          `json:"binds,omitempty"`
	Members     []string          `json:"members,omitempty"`
	OpenThreads []string          `json:"open_threads,omitempty"`
	Calls       []string          `json:"calls,omitempty"`
}

// IsZero reports whether m names no mapping at all.
func (m Mappings) IsZero() bool {
	return m.Flow == "" && len(m.Crew) == 0 && len(m.Groups) == 0 &&
		len(m.Binds) == 0 && len(m.Members) == 0 && len(m.OpenThreads) == 0 && len(m.Calls) == 0
}

// Bound are the pack's groups the binds bind themselves, as
// pack.Options.Bound names them: a bind for a crew member binds no group.
func (m Mappings) Bound() []string {
	var out []string
	for _, kv := range m.Binds {
		if name, _, _ := strings.Cut(kv, "="); name != "" && !strings.HasPrefix(name, "crew:") {
			out = append(out, name)
		}
	}
	return out
}

// PackApplyUse is how a refusal names the row.
const PackApplyUse = "yardr pack apply <name> [--depot <depot> [--flow <flow>] [--crew <pack>=<yard>] [--group <pack>=<yard>] [--bind <spec>] [--members <group>=<a,b>] [--open-threads <crew>=<sources>] [--calls <who>=<source>:<calls>]]"

// packApplyFlags are the words a card carries: the depot, and the
// mappings a plan's questions are answered by. Every other flag of the
// terminal's shapes the import in a way a card holds no word for.
var packApplyFlags = map[string]bool{"--depot": true, "--flow": true, "--crew": true, "--group": true,
	"--bind": true, "--members": true, "--open-threads": true, "--calls": true}

// parsePackApply reads the words of pack apply into the row's one stored
// form: the name, the depot, then the mappings in packMappingFlags' order.
func parsePackApply(rest []string) ([]string, error) {
	name, depot, m, err := packApplyWords(rest)
	if err != nil {
		return nil, err
	}
	out := []string{name}
	if depot != "" {
		out = append(out, "--depot", depot)
	}
	for _, flag := range packMappingFlags(m) {
		word, value, _ := strings.Cut(flag, " ")
		out = append(out, word, value)
	}
	return out, nil
}

// packApplyWords reads the pack's name, the depot and the mappings out of
// the words of pack apply. A mapping answers a plan for a depot, so it
// needs --depot; with none the words are a bare name, a re-apply. Each
// value stands on the card as it was typed, and draws no line of its own
// there: parseRow has held the line to names, numbers and flags.
func packApplyWords(rest []string) (string, string, Mappings, error) {
	var m Mappings
	for _, word := range rest {
		flag, _, _ := strings.Cut(word, "=")
		if strings.HasPrefix(word, "-") && !packApplyFlags[flag] {
			return "", "", m, fmt.Errorf("--run takes %s, and pack apply with %s shapes the import, which a card answers none of: it is the operator's to type in a terminal (yardr pack check %s shows its plan there)", PackApplyUse, flag, "<name>")
		}
	}
	args, values, err := runWords(PackApplyUse, "pack apply", "the operator's", rest, packApplyFlags)
	if err != nil {
		return "", "", m, err
	}
	if len(args) != 1 {
		return "", "", m, fmt.Errorf("--run takes %s: one pack's name, not %d words", PackApplyUse, len(args))
	}
	once := func(flag string) (string, error) {
		if len(values[flag]) > 1 {
			return "", fmt.Errorf("--run takes %s: one %s, not %d", PackApplyUse, flag, len(values[flag]))
		}
		return strings.Join(values[flag], ""), nil
	}
	takes := func(flag string) (map[string]string, error) {
		var out map[string]string
		for _, kv := range values[flag] {
			pack, yard, ok := strings.Cut(kv, "=")
			if !ok || pack == "" || yard == "" {
				return nil, fmt.Errorf("--run takes %s: %s %s is not <pack>=<yard>", PackApplyUse, flag, kv)
			}
			if _, twice := out[pack]; twice {
				return nil, fmt.Errorf("--run takes %s: %s names %s twice", PackApplyUse, flag, pack)
			}
			if out == nil {
				out = map[string]string{}
			}
			out[pack] = yard
		}
		return out, nil
	}
	depot, err := once("--depot")
	if err != nil {
		return "", "", m, err
	}
	if m.Flow, err = once("--flow"); err != nil {
		return "", "", m, err
	}
	if m.Crew, err = takes("--crew"); err != nil {
		return "", "", m, err
	}
	if m.Groups, err = takes("--group"); err != nil {
		return "", "", m, err
	}
	m.Binds, m.Members, m.OpenThreads, m.Calls = values["--bind"], values["--members"], values["--open-threads"], values["--calls"]
	if depot == "" && !m.IsZero() {
		return "", "", m, fmt.Errorf("--run takes %s: a mapping answers the plan for a named depot, so it needs --depot", PackApplyUse)
	}
	return args[0], depot, m, nil
}

// packTargetKey starts each Seen key a named depot's card holds for what
// its command names beside the pack (packTargetsSeen).
const packTargetKey = "target "

// packFirstInstall says whether the depot holds no import of the pack.
func (w *wtx) packFirstInstall(name, depot string) (bool, error) {
	var rec PackImport
	had, err := get(w.Tx, bPacks, join(depot, name), &rec)
	return !had, err
}

// packTargetsSeen keeps in seenMap what a named depot's card rests on
// beside the plan: the depot, its record of the pack, and each thing of
// the yard a mapping names, so a change to any of them stales the
// approval (packApplyStale) even where the plan reads alike. A target
// that does not stand is kept as that, and stales when it comes to. The
// selected flow is a name in the embedded pack, already held by the
// reading and its plan, not a flow record in the yard.
func (w *wtx) packTargetsSeen(seenMap map[string]string, name, depot string, m Mappings) error {
	var d Depot
	if _, err := get(w.Tx, bDepots, depot, &d); err != nil {
		return err
	}
	seenMap[packTargetKey+"depot "+depot] = seen(d.Path, d.Kind, d.Base, d.Test)
	var rec PackImport
	if _, err := get(w.Tx, bPacks, join(depot, name), &rec); err != nil {
		return err
	}
	seenMap[packTargetKey+"import of pack "+name+" in depot "+depot] = seen(rec)
	for _, yard := range m.Crew {
		var c CrewMember
		if _, err := get(w.Tx, bCrew, yard, &c); err != nil {
			return err
		}
		seenMap[packTargetKey+"crew member "+yard] = seen(c.Runner, c.Config, c.Dir)
	}
	for _, yard := range m.Groups {
		var g Group
		if _, err := get(w.Tx, bGroups, yard, &g); err != nil {
			return err
		}
		seenMap[packTargetKey+"group "+yard] = seen(g.Runner, g.Config, g.Members)
	}
	for _, kv := range m.Members {
		_, names, _ := strings.Cut(kv, "=")
		for _, member := range strings.Split(names, ",") {
			stands := w.Bucket(bUsers).Get([]byte(member)) != nil || w.Bucket(bCrew).Get([]byte(member)) != nil
			seenMap[packTargetKey+"member "+member] = seen(stands)
		}
	}
	for _, src := range mappedSources(m) {
		fingerprint := ""
		if got, _, err := sourceIn(w.Tx, src); err == nil {
			if fingerprint, err = w.sourceSeen(got); err != nil {
				return err
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		seenMap[packTargetKey+"source "+src] = fingerprint
	}
	return nil
}

// mappedSources are the sources the mappings' grants name.
func mappedSources(m Mappings) []string {
	var out []string
	for _, g := range m.threadGrants() {
		out = append(out, g.Sources...)
	}
	for _, g := range m.callGrants() {
		out = append(out, g.Source)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// threadGrant is one --open-threads word: the pack's crew member, and the
// sources it may open threads in.
type threadGrant struct {
	Crew    string
	Sources []string
}

// callGrant is one --calls word: who, as the flag names them, the source,
// and the calls.
type callGrant struct {
	Who    string
	Source string
	Calls  []string
}

// grantWords are the comma-separated words of a mapping's value, without
// the none that says the mapping grants nothing.
func grantWords(v string) []string {
	var out []string
	for word := range strings.SplitSeq(v, ",") {
		if word = strings.TrimSpace(word); word != "" && word != "none" {
			out = append(out, word)
		}
	}
	return out
}

// threadGrants are the open-threads the mappings grant.
func (m Mappings) threadGrants() []threadGrant {
	var out []threadGrant
	for _, kv := range m.OpenThreads {
		crew, v, _ := strings.Cut(kv, "=")
		if sources := grantWords(v); len(sources) > 0 {
			out = append(out, threadGrant{Crew: crew, Sources: sources})
		}
	}
	return out
}

// callGrants are the calls the mappings grant.
func (m Mappings) callGrants() []callGrant {
	var out []callGrant
	for _, kv := range m.Calls {
		who, v, _ := strings.Cut(kv, "=")
		src, words, _ := strings.Cut(v, ":")
		if calls := grantWords(words); len(calls) > 0 {
			out = append(out, callGrant{Who: who, Source: src, Calls: calls})
		}
	}
	return out
}

// packMappingFlags are the import's mappings in the flags that name
// them, in a stable order, so the card says what its plan uses.
func packMappingFlags(m Mappings) []string {
	var out []string
	if m.Flow != "" {
		out = append(out, "--flow "+m.Flow)
	}
	for _, name := range sortedMappingKeys(m.Crew) {
		out = append(out, "--crew "+name+"="+m.Crew[name])
	}
	for _, name := range sortedMappingKeys(m.Groups) {
		out = append(out, "--group "+name+"="+m.Groups[name])
	}
	for _, kv := range m.Binds {
		out = append(out, "--bind "+kv)
	}
	for _, kv := range m.Members {
		out = append(out, "--members "+kv)
	}
	for _, kv := range m.OpenThreads {
		out = append(out, "--open-threads "+kv)
	}
	for _, kv := range m.Calls {
		out = append(out, "--calls "+kv)
	}
	return out
}

func sortedMappingKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// AskPack is a pack apply ask's pack as the yard's server read it: its
// name, its kind, the depot a depot kind would be re-applied to, and
// the build label the pack is embedded in. A depot kind carries the
// plan the server built inside the write that stored the ask.
type AskPack struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind,omitempty"`
	Depot    string   `json:"depot,omitempty"`
	Mappings Mappings `json:"mappings,omitzero"`
	Label    string   `json:"label"`
	Plan     *AskPlan `json:"plan,omitempty"`
}

// PackReading is what the server's reading of one embedded pack hands
// the store: the pack's name and kind, the build label it is embedded
// in, each declared source's whole text with its command files' bytes,
// the pack's presets with their grants, and, for a depot kind, the depot
// it would be re-applied to and the plan built against the yard. Nothing
// of it is the asker's.
type PackReading struct {
	Name     string
	Kind     string // pack.KindSource ("source") for a yard pack, else a depot kind
	Depot    string
	Mappings Mappings // the mappings the plan was built with, echoed for the card
	Label    string   // "embedded in yardr <build>"
	Build    string   // the <build> of Label, kept apart so refusals name it
	Sources  []AskSource
	Presets  []AskPreset
	Plan     *AskPlan
}

// PackReader reads the embedded pack of a name for a pack apply ask,
// for the named depot with the named mappings: the yard is the store's
// own API, so a depot plan is built from the yard's records as the write
// that stores the ask holds them. No depot and no mappings read a depot
// pack against the depot that already holds it, as before mappings were
// carried. An error refuses the ask, storing nothing, in the reader's
// words.
type PackReader func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error)

// SetPackReader tells the store how its owning process reads the packs
// yardr embeds, as SetYard tells it what only that process knows. A
// store no server owns has none, and a pack ask of it is refused naming
// the server.
func (s *Store) SetPackReader(r PackReader) { s.packReader = r }

// ReadPack reads the embedded pack of a name through the registered
// reading, for the server's carrying out of an approved pack apply:
// the same reading the approval was held to, taken again, for the depot
// and with the mappings the card holds.
func (s *Store) ReadPack(ctx context.Context, name string, depot string, mappings Mappings) (*PackReading, error) {
	if s.packReader == nil {
		return nil, fmt.Errorf("pack %s: no server has registered its reading of the packs yardr embeds on this store", name)
	}
	return s.packReader(ctx, name, depot, mappings, s)
}

// packReaderOf is the reading registered on the store this write
// belongs to, nil where there is none.
func (w *wtx) packReaderOf() PackReader {
	if w.store == nil {
		return nil
	}
	return w.store.packReader
}
