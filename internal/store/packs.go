package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bPacks holds what the yard imported, <depot>\x00<pack> -> PackImport: one
// record per pack and depot, replaced by the next import of that pack there.
// A pack imported into the yard itself, and into no depot, is under
// "\x00<pack>": no depot's name is empty, so removing one never takes it.
var bPacks = []byte("packs")

func init() { allBuckets = append(allBuckets, bPacks) }

// PackImport is the yard's record of a pack imported into a depot: where it
// came from, who imported it and when, how the pack's names map to the
// yard's, and the objects that are the pack's, so a later import can tell
// what the pack has dropped since. What a pack is, and how it maps to the
// yard, is package pack's business; the store keeps the record.
type PackImport struct {
	Pack string `json:"pack"`
	// Depot is the depot the pack was imported into, "" for a pack that
	// was imported into the yard itself, and into no depot.
	Depot string `json:"depot"`
	// Source is the directory the pack was read from; Commit its git commit
	// when it is (in) a repository, Dirty whether it then had changes the
	// commit does not hold. A pack yardr embeds is in no directory: Source
	// then says embedded and the build, and there is no commit.
	Source string `json:"source"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Flow is the pack's flow the depot follows, by the pack's name for it.
	Flow string `json:"flow,omitempty"`
	// Setup says the pack carried setup notes, its author's words about
	// what a setup takes (pack.yaml's setup): a crew member's prime names
	// them by the command that prints them (yardr pack show <pack>).
	Setup bool `json:"setup,omitempty"`
	// DepotRoles says the import was told to leave a group or crew member its
	// depot's own role (--depot-roles); otherwise the pack's roles were set, the
	// default. The next import repeats the choice unless told anew.
	DepotRoles bool      `json:"depot_roles,omitempty"`
	At         time.Time `json:"at"`
	By         string    `json:"by"`
	// Groups and Crew members map the pack's names to the yard's.
	Groups  map[string]string `json:"groups"`
	Crew    map[string]string `json:"crew"`
	Objects []PackObject      `json:"objects"`
	// OpenThreads are the answers to what the pack asked that its crew
	// may open threads in: the yard's crew member -> source -> whether the
	// operator said yes. A source nobody was asked about is not in it, so a
	// later import asks.
	OpenThreads map[string]map[string]bool `json:"open_threads,omitempty"`
	// Calls are the answers to what the pack asked that its groups'
	// sessions and its crew may call of a declared source: the grantee, as
	// Grantee.String says it of the yard's group or crew member ("group
	// web-builders") -> "<source>:<read or a call's name>" -> whether the
	// operator said yes. What nobody was asked about is not in it, so a
	// later import asks.
	Calls map[string]map[string]bool `json:"calls,omitempty"`
	// Expose are the answers to whether a crew member the pack asks it for
	// (expose_to_peers) is exposed to a peer: the yard's crew member -> the
	// peer's key -> whether the operator said yes. By key, since a peer's
	// name is the yard's to change and free again once the peer is removed:
	// a no to one yard is none to the next of that name. A peer nobody was
	// asked about is not in it, so a later import asks.
	Expose map[string]map[string]bool `json:"expose_to_peers,omitempty"`
}

// PackObject is one object of the yard that came from a pack, with what it
// takes to find it again.
type PackObject struct {
	Kind string `json:"kind"` // file group flow pointer route subscription map source
	// Name is the file's path, or the group's, flow's or declared source's
	// name.
	Name string `json:"name,omitempty"`
	// SHA256 is a file's content as the import installed it: a copy that
	// differs was edited by hand since. Records older than the field lack it.
	// Of a declared source it is the declaration's text as the import added
	// it: one added by hand since is the yard's.
	SHA256 string `json:"sha256,omitempty"`
	// A route's stage, type and group; a pointer's type and flow.
	Stage string `json:"stage,omitempty"`
	Type  string `json:"type,omitempty"`
	Group string `json:"group,omitempty"`
	Flow  string `json:"flow,omitempty"`
	// A subscription and the crew member it is of.
	CrewMember   string        `json:"crew,omitempty"`
	Subscription *Subscription `json:"subscription,omitempty"`
	// A stage map (kind map): Name is the hook it was set on, CrewMember the
	// one the hook acts as, and Map the map as the import left it, so a map
	// changed by hand since is not taken for the pack's. Says is the hook's
	// table of what the source's events say (Hook's Says), and Makes the
	// one of what they make (Hook's Makes), likewise. A record holds the
	// parts the pack carried: no CrewMember when it had no map, no Says or
	// Makes when it had no such table.
	Map   map[string]string `json:"map,omitempty"`
	Says  []Say             `json:"says,omitempty"`
	Makes []Make            `json:"makes,omitempty"`
}

// Key identifies the object among those of one import, by what stays the
// same when the pack changes it: a route by its stage and type whatever
// group it leads to, a pointer by its type, a subscription by its source
// and filters whatever its scope.
func (o PackObject) Key() string {
	switch o.Kind {
	case "route":
		return join(o.Kind, o.Stage, o.Type)
	case "pointer":
		return join(o.Kind, o.Type)
	case "subscription":
		s := Subscription{}
		if o.Subscription != nil {
			s = Subscription{Source: o.Subscription.Source, Where: o.Subscription.Where}
		}
		return join(o.Kind, o.CrewMember, s.String())
	}
	return join(o.Kind, o.Name)
}

// same reports whether two records say the same of the yard: when and by
// whom, and where the pack was read from, aside.
func (p PackImport) same(o PackImport) bool {
	p.At, p.By, p.Source, p.Commit, p.Dirty = o.At, o.By, o.Source, o.Commit, o.Dirty
	return reflect.DeepEqual(normalPack(p), normalPack(o))
}

// normalPack is p as it reads back from the store: empty maps and lists
// are stored, and decoded, as such.
func normalPack(p PackImport) PackImport {
	if p.Groups == nil {
		p.Groups = map[string]string{}
	}
	if p.Crew == nil {
		p.Crew = map[string]string{}
	}
	p.Objects = slices.Clone(p.Objects)
	if p.Objects == nil {
		p.Objects = []PackObject{}
	}
	for i, o := range p.Objects {
		if o.Subscription != nil && len(o.Subscription.Where) == 0 {
			s := *o.Subscription
			s.Where = nil
			p.Objects[i].Subscription = &s
		}
	}
	p.OpenThreads, p.Calls, p.Expose = answered(p.OpenThreads), answered(p.Calls), answered(p.Expose)
	return p
}

// answered is a record's answers as they are stored: whoever has none is
// left out, and so is a record without any.
func answered(by map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for name, said := range by {
		if len(said) > 0 {
			out[name] = said
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RecordPack ends an import of rec.Pack into rec.Depot, "" for a pack
// imported into the yard itself, and into no depot. changed says whether
// the import wrote anything. One that did, or whose record differs from the
// stored one, is recorded under the time and actor of this call and logged
// as pack_applied. One that changed nothing leaves the record as it is and
// logs pack_unchanged, the only trace such an import leaves. It reports
// whether the record was written.
func (s *Store) RecordPack(ctx context.Context, rec PackImport, changed bool, actor string) (bool, error) {
	if rec.Pack == "" {
		return false, fmt.Errorf("pack record: pack is required")
	}
	rec = normalPack(rec)
	written := false
	err := s.write(func(w *wtx) error {
		written = false
		if rec.Depot != "" && w.Bucket(bDepots).Get([]byte(rec.Depot)) == nil {
			return fmt.Errorf("depot %s: %w", rec.Depot, ErrNotFound)
		}
		key := join(rec.Depot, rec.Pack)
		var old PackImport
		had, err := get(w.Tx, bPacks, key, &old)
		if err != nil {
			return err
		}
		data := map[string]any{"pack": rec.Pack, "depot": rec.Depot, "source": rec.Source}
		if rec.Commit != "" {
			data["commit"] = rec.Commit
		}
		if had && !changed && old.same(rec) {
			return w.event("", "pack_unchanged", actor, data)
		}
		rec.At, rec.By = now(), actor
		if err := put(w.Tx, bPacks, key, rec); err != nil {
			return err
		}
		written = true
		return w.event("", "pack_applied", actor, data)
	})
	return written, err
}

// Packs lists what the yard imported, by depot and pack.
func (s *Store) Packs(ctx context.Context) ([]PackImport, error) {
	out := []PackImport{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPacks, "", func(_ []byte, p *PackImport) error {
			out = append(out, normalPack(*p))
			return nil
		})
	})
	return out, err
}

// RemoveSubscription drops exactly sub from the crew member (or manual group)
// name: the one with its source, scope and filters, and no other.
// RemoveSubscriptions without filters takes every subscription of the
// source and scope, filtered ones too.
func (s *Store) RemoveSubscription(ctx context.Context, name string, sub Subscription, actor string) error {
	return s.removeOneSubscription(name, "", sub, actor)
}

// RemoveSubscriptionKind is RemoveSubscription for the subscriber of one
// stated kind ("crew", "user" or "group") called name, and no other.
func (s *Store) RemoveSubscriptionKind(ctx context.Context, name, kind string, sub Subscription, actor string) error {
	return s.removeOneSubscription(name, kind, sub, actor)
}

func (s *Store) removeOneSubscription(name, kind string, sub Subscription, actor string) error {
	return s.write(func(w *wtx) error {
		change := func(name, actor string, fn func([]Subscription) ([]Subscription, string, map[string]any, error)) error {
			if kind == "" {
				return w.changeSubscriptions(name, actor, fn)
			}
			return w.changeSubscriptionsKind(name, kind, actor, fn)
		}
		return change(name, actor, func(subs []Subscription) ([]Subscription, string, map[string]any, error) {
			i := slices.IndexFunc(subs, sub.same)
			if i < 0 {
				return nil, "", nil, fmt.Errorf("%s has no subscription %s: %w", name, sub, ErrNotFound)
			}
			return slices.Delete(slices.Clone(subs), i, i+1), "unsubscribed", map[string]any{"subscriptions": []string{sub.String()}}, nil
		})
	})
}
