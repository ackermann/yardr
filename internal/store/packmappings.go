package store

import (
	"context"
	"fmt"
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
