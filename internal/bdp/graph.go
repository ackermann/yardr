// Package bdp serves yard over the Beads Protocol (BDP) Read profile.
//
// The mapping from yard to BDP:
//
//	bead           Bead  <scope>beads/<id>; properties carry yard's fields
//	dep (a -k-> b) Link  <scope>links/<dep-id>, source b, target a: for kind
//	               blocks, "b depends on a", the direction bd and BDP's
//	               readiness clients follow (outbound blocks -> blockers)
//	bead type      https://work.example/types/<t> for BDP's reference types,
//	dep kind       else <scope>types/<t>, as BDP's bd adapter does
//
// Bead properties are bd's JSON row (id, title, status, priority,
// issue_type, created_at, created_by, updated_at, dependencies and their
// counts) plus yard's own fields, so clients written for bd read yard too.
// Revisions are the store's: opaque ULIDs minted on every real change.
package bdp

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
)

const workTypes = "https://work.example/types/"

// BDP's reference vocabulary (packages/protocol reference-domain). Like the
// bd adapter, yard publishes these without owned-link declarations.
var referenceBeadTypes = map[string]string{
	"tracked-item": "Tracked Item", "work-item": "Work Item", "task": "Task", "bug": "Bug",
	"feature": "Feature", "chore": "Chore", "epic": "Epic", "decision": "Decision",
}

var referenceLinkTypes = map[string]string{
	"relationship": "Relationship", "dependency": "Dependency", "blocks": "Blocks",
	"relates": "Relates", "cites": "Cites",
}

// referenceConforms is the reference conformsTo graph.
var referenceConforms = map[string][]string{
	workTypes + "task":       {workTypes + "work-item"},
	workTypes + "bug":        {workTypes + "work-item"},
	workTypes + "feature":    {workTypes + "work-item"},
	workTypes + "chore":      {workTypes + "work-item"},
	workTypes + "epic":       {workTypes + "work-item"},
	workTypes + "work-item":  {workTypes + "tracked-item"},
	workTypes + "blocks":     {workTypes + "dependency"},
	workTypes + "dependency": {workTypes + "relationship"},
}

// Record is a BDP Bead or Link as served.
type Record struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Revision    string         `json:"revision"`
	Attribution *Attribution   `json:"attribution,omitempty"`
	Source      string         `json:"source,omitempty"`
	Target      string         `json:"target,omitempty"`
	Properties  map[string]any `json:"properties"`

	link bool
}

type Attribution struct {
	Principal string `json:"principal"`
	Status    string `json:"status"`
}

// TypeDescriptor describes a Scope-local Type.
type TypeDescriptor struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Describes   string        `json:"describes"`
	ConformsTo  []string      `json:"conformsTo"`
	Source      *EndpointRule `json:"source,omitempty"`
	Target      *EndpointRule `json:"target,omitempty"`
}

type EndpointRule struct {
	ConformsTo []string `json:"conformsTo"`
}

type TypeSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Describes string `json:"describes"`
}

// Graph is one consistent view of the yard as BDP resources, each list in
// canonical-uri order.
type Graph struct {
	Scope string
	Beads []*Record
	Links []*Record
	Types []TypeSummary

	byID        map[string]*Record
	descriptors map[string]*TypeDescriptor // Scope-local only
	conforms    map[string][]string        // direct conformsTo, all known types
}

func (g *Graph) beadURL(id string) string { return g.Scope + "beads/" + EncodeSegment(id) }

// BuildGraph maps a store snapshot onto BDP resources.
func BuildGraph(scope string, snap *store.Snapshot) *Graph {
	g := &Graph{Scope: scope, byID: map[string]*Record{}, descriptors: map[string]*TypeDescriptor{},
		conforms: map[string][]string{}}
	for k, v := range referenceConforms {
		g.conforms[k] = v
	}
	// The inventory offers BDP's whole reference vocabulary, as bd's server
	// does, plus whatever Scope-local types the yard uses.
	types := map[string]TypeSummary{}
	for t, name := range referenceBeadTypes {
		types[workTypes+t] = TypeSummary{ID: workTypes + t, Name: name, Describes: "bead"}
	}
	for t, name := range referenceLinkTypes {
		types[workTypes+t] = TypeSummary{ID: workTypes + t, Name: name, Describes: "link"}
	}

	// bd lists a bead's dependencies (its outbound edges) in its row.
	deps := map[string][]any{}
	dependents := map[string]int{}
	for i := range snap.Deps {
		d := &snap.Deps[i]
		deps[d.To] = append(deps[d.To], map[string]any{
			"issue_id": d.To, "depends_on_id": d.From, "type": d.Kind,
			"created_at": instant(d.CreatedAt), "created_by": deref(d.CreatedBy), "metadata": "{}",
		})
		dependents[d.From]++
	}
	for _, list := range deps {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i].(map[string]any), list[j].(map[string]any)
			if a["created_at"] != b["created_at"] {
				return a["created_at"].(string) < b["created_at"].(string)
			}
			return a["depends_on_id"].(string) < b["depends_on_id"].(string)
		})
	}

	for i := range snap.Beads {
		b := &snap.Beads[i]
		props := beadProperties(&b.Bead)
		props["dependencies"] = nonNilAny(deps[b.ID])
		props["dependency_count"] = len(deps[b.ID])
		props["dependent_count"] = dependents[b.ID]
		props["comment_count"] = b.Notes
		r := &Record{
			ID:          g.beadURL(b.ID),
			Type:        g.beadType(b.Type, types),
			Revision:    b.Revision,
			Attribution: attribution(b.LastActor),
			Properties:  props,
		}
		g.Beads = append(g.Beads, r)
		g.byID[r.ID] = r
	}
	for i := range snap.Deps {
		d := &snap.Deps[i]
		r := &Record{
			ID:          g.Scope + "links/" + EncodeSegment(d.ID),
			Type:        g.linkType(d.Kind, types),
			Revision:    d.Revision,
			Attribution: attribution(d.LastActor),
			Source:      g.beadURL(d.To),
			Target:      g.beadURL(d.From),
			Properties:  map[string]any{},
			link:        true,
		}
		g.Links = append(g.Links, r)
		g.byID[r.ID] = r
	}
	for _, t := range types {
		g.Types = append(g.Types, t)
	}
	sortRecords(g.Beads)
	sortRecords(g.Links)
	sort.Slice(g.Types, func(i, j int) bool { return CompareIDs(g.Types[i].ID, g.Types[j].ID) < 0 })
	return g
}

func (g *Graph) beadType(t string, seen map[string]TypeSummary) string {
	if name, ok := referenceBeadTypes[t]; ok {
		id := workTypes + t
		seen[id] = TypeSummary{ID: id, Name: name, Describes: "bead"}
		return id
	}
	id := g.Scope + "types/" + EncodeSegment(t)
	if _, ok := g.descriptors[id]; !ok {
		// Like bd's adapter, a minted type claims no conformance.
		conforms := []string{}
		g.descriptors[id] = &TypeDescriptor{ID: id, Name: titleCase(t), Describes: "bead", ConformsTo: conforms,
			Description: "yardr bead type " + t}
		g.conforms[id] = conforms
	}
	seen[id] = TypeSummary{ID: id, Name: titleCase(t), Describes: "bead"}
	return id
}

func (g *Graph) linkType(kind string, seen map[string]TypeSummary) string {
	if name, ok := referenceLinkTypes[kind]; ok {
		id := workTypes + kind
		seen[id] = TypeSummary{ID: id, Name: name, Describes: "link"}
		return id
	}
	id := g.Scope + "types/" + EncodeSegment(kind)
	if _, ok := g.descriptors[id]; !ok {
		conforms := []string{}
		g.descriptors[id] = &TypeDescriptor{ID: id, Name: titleCase(kind), Describes: "link", ConformsTo: conforms,
			Description: "yardr dependency kind " + kind,
			Source:      &EndpointRule{ConformsTo: []string{}}, Target: &EndpointRule{ConformsTo: []string{}}}
		g.conforms[id] = conforms
	}
	seen[id] = TypeSummary{ID: id, Name: titleCase(kind), Describes: "link"}
	return id
}

// ConformsTo reports whether type t is c or conforms to it transitively.
func (g *Graph) ConformsTo(t, c string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(x string) bool {
		if x == c {
			return true
		}
		if seen[x] {
			return false
		}
		seen[x] = true
		for _, p := range g.conforms[x] {
			if walk(p) {
				return true
			}
		}
		return false
	}
	return walk(t)
}

// beadProperties is bd's row shape. A bead's BDP id is its handle, which
// never changes and is unique in the Scope; its UUID rides along.
func beadProperties(b *store.Bead) map[string]any {
	p := map[string]any{
		"id":         b.ID,
		"uuid":       b.UUID,
		"title":      b.Title,
		"status":     status(b),
		"priority":   b.Priority,
		"issue_type": b.Type,
		"stage":      b.Stage,
		"labels":     nonNil(b.Labels),
		"hold":       b.Hold,
		"created_at": instant(b.CreatedAt),
		"created_by": deref(b.CreatedBy),
		"updated_at": instant(b.UpdatedAt),
	}
	if b.Body != "" {
		p["description"] = b.Body
	}
	if b.Rig != nil {
		p["rig"] = *b.Rig
	}
	if b.Group != nil {
		p["assignee"] = *b.Group
	}
	if b.ClosedAt != nil {
		p["closed_at"] = instant(*b.ClosedAt)
	}
	if b.CloseReason != nil {
		p["close_reason"] = *b.CloseReason
	}
	if len(b.Metadata) > 0 {
		p["metadata"] = b.Metadata
	}
	return p
}

// status is bd's vocabulary: a held open bead is "deferred", not workable
// now, so BDP readiness clients skip it just as `yardr ready` does.
func status(b *store.Bead) string {
	if b.Status == "open" && b.Hold {
		return "deferred"
	}
	return b.Status
}

func attribution(actor *string) *Attribution {
	if actor == nil || *actor == "" {
		return nil
	}
	return &Attribution{Principal: *actor, Status: "claimed"}
}

// instant renders RFC 3339 in UTC with an uppercase Z, as BDP requires.
func instant(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func sortRecords(rs []*Record) {
	sort.Slice(rs, func(i, j int) bool { return CompareIDs(rs[i].ID, rs[j].ID) < 0 })
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nonNilAny(s []any) []any {
	if s == nil {
		return []any{}
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func titleCase(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	if len(words) == 0 {
		return s
	}
	return strings.Join(words, " ")
}

// Source is the store as CachedLoader reads it.
type Source interface {
	Head(ctx context.Context) (uint64, error)
	Snapshot(ctx context.Context) (*store.Snapshot, error)
}

// CachedLoader is a load function for NewServer that builds the Graph from
// a snapshot only when the store's head has moved since the last build.
// Every write that changes a bead or an edge logs an event in the same
// transaction, so an unmoved head means an unchanged graph, and a request
// then costs one small Head call instead of a full snapshot over RPC.
// A Graph is never changed once built, so requests may share it.
func CachedLoader(scope string, src Source) func(ctx context.Context) (*Graph, error) {
	var (
		mu    sync.Mutex
		head  uint64
		graph *Graph
	)
	return func(ctx context.Context) (*Graph, error) {
		mu.Lock()
		defer mu.Unlock()
		// The head is read before the snapshot: a write in between leaves
		// the graph newer than its key, and the next request rebuilds.
		h, err := src.Head(ctx)
		if err != nil {
			return nil, err
		}
		if graph != nil && h == head {
			return graph, nil
		}
		snap, err := src.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		head, graph = h, BuildGraph(scope, snap)
		return graph, nil
	}
}
