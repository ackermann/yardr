package pack

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/depotfile"
	"yardr.dev/yardr/internal/store"
)

// depotRole names the depot's own role file that stands in for the pack's role
// rel (roles/builder.md -> .yardr/roles/builder.md), or "" when the depot has
// none or the pack's file is no role by yardr's naming (roles/<name>.md).
// The pack's role is set over it unless the import keeps the depot's roles.
// The depot's roles are read together on the first asked for, instead of
// starting git per role.
func (b *planner) depotRole(rel string) (string, error) {
	dir, name := filepath.Split(filepath.FromSlash(rel))
	if filepath.Clean(dir) != "roles" || filepath.Ext(name) != ".md" {
		return "", nil
	}
	if !b.rolesRead {
		b.rolesRead = true
		var names []string
		seen := map[string]bool{}
		add := func(rel string) {
			d, n := filepath.Split(filepath.FromSlash(rel))
			if filepath.Clean(d) == "roles" && filepath.Ext(n) == ".md" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		for _, pr := range b.p.Crew {
			if pr.Role != "" {
				add(pr.Role)
			}
		}
		for _, g := range b.p.Groups {
			if g.Agent != "" {
				add(g.Agent)
			}
		}
		rels := make([]string, len(names))
		for i, n := range names {
			rels[i] = filepath.Join(".yardr", "roles", n)
		}
		files, _, err := depotfile.ReadDepotFiles(b.depot, rels...)
		if err != nil {
			b.rolesErr = err
		} else {
			b.roles = map[string]depotfile.File{}
			for i, n := range names {
				b.roles[n] = files[i]
			}
		}
	}
	if b.rolesErr != nil {
		return "", fmt.Errorf("depot %s: looking for its own role %s: %w", b.depot.Name, name, b.rolesErr)
	}
	if f := b.roles[name]; f.Err != nil {
		return "", fmt.Errorf("depot %s: looking for its own role %s: %w", b.depot.Name, name, f.Err)
	} else if !f.Found {
		return "", nil
	}
	return ".yardr/roles/" + name, nil
}

// Ident is the record of an object the pack carries: what a later import
// finds it by. ok is false for what is not the pack's to own: a crew member
// and a group that takes the pack's, which stay the yard's, and an object
// the pack does not carry.
func Ident(o Object) (po store.PackObject, ok bool) {
	if o.Action == Dropped {
		if o.Owned == nil {
			return po, false
		}
		return *o.Owned, true
	}
	if o.Action == NotInPack || o.Action == Takes {
		return po, false
	}
	switch {
	case o.Kind == "file" && o.File != nil:
		return store.PackObject{Kind: o.Kind, Name: o.File.To, SHA256: o.File.SHA256}, true
	case o.Kind == "group":
		// What this import binds, or what the last one did and still stands.
		binds := o.bindsKept
		if o.Binds != "" {
			binds = o.Binds
		}
		return store.PackObject{Kind: o.Kind, Name: o.Name, Binds: binds}, true
	case o.Kind == "flow":
		return store.PackObject{Kind: o.Kind, Name: o.Name}, true
	case o.Kind == "pointer" && o.Pointer != nil:
		return store.PackObject{Kind: o.Kind, Type: o.Pointer.Type, Flow: o.Pointer.Flow}, true
	case o.Kind == "route" && o.Route != nil:
		return store.PackObject{Kind: o.Kind, Stage: o.Route.Stage, Type: o.Route.Type, Group: o.Route.Group}, true
	case o.Kind == "subscription" && o.Subscription != nil && o.Subscription.Add != nil:
		return store.PackObject{Kind: o.Kind, CrewMember: o.Subscription.CrewMember, Subscription: o.Subscription.Add}, true
	case o.Kind == "map" && o.Map != nil:
		return store.PackObject{Kind: o.Kind, Name: o.Map.Hook, CrewMember: o.Map.As, Map: o.Map.Map, Says: o.Map.Says, Makes: o.Map.Makes}, true
	case o.Kind == "source" && o.Source != nil:
		return store.PackObject{Kind: o.Kind, Name: o.Source.Name, SHA256: sum([]byte(o.Source.Text))}, true
	}
	return po, false
}

// dropped adds what the last import of this pack into the depot brought and
// the pack no longer carries, as far as the yard still has it: a group the
// pack dropped, the routes and subscriptions of a stage it lost, a file
// nothing names any more. Each takes the place of the object's "not in the
// pack" entry, if it had one. A prune removes them, except those held.
func (b *planner) dropped() error {
	if b.rec == nil {
		return nil
	}
	carried := map[string]bool{}
	for _, o := range b.plan.Objects {
		if po, ok := Ident(o); ok {
			carried[po.Key()] = true
		}
	}
	for pk, name := range b.rec.Crew {
		if now, ok := b.plan.Crew[pk]; !ok || now != name {
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("crew member %s took the pack's %s at the last import and no longer does: it is the yard's, so its role and the event kinds it hears stay as that import left them", name, pk))
		}
	}
	for _, po := range b.rec.Objects {
		if carried[po.Key()] {
			continue
		}
		o, err := b.gone(po)
		if err != nil {
			return err
		}
		if o == nil {
			continue // the yard no longer has it
		}
		o.Action, o.Owned = Dropped, &po
		b.plan.Objects = slices.DeleteFunc(b.plan.Objects, func(h Object) bool {
			return h.Action == NotInPack && h.Kind == o.Kind && h.Name == o.Name && (o.File == nil || h.File == nil || h.File.To == o.File.To)
		})
		b.add(*o)
	}
	// A type-flow pointer held to protect its beads keeps the flow it points
	// at too; otherwise pruning would report a store error for that flow.
	for _, pointer := range b.plan.Objects {
		if pointer.Kind != "pointer" || pointer.Action != Dropped || pointer.Held == "" || pointer.Pointer == nil {
			continue
		}
		for i := range b.plan.Objects {
			flow := &b.plan.Objects[i]
			if flow.Kind == "flow" && flow.Action == Dropped && flow.Name == pointer.Pointer.Flow {
				flow.Held = "a type-flow pointer is kept: " + pointer.Held
			}
		}
	}
	// A flow that stays keeps its stage files: whoever still follows it
	// reads them.
	for i := range b.plan.Objects {
		file := &b.plan.Objects[i]
		if file.Kind != "file" || file.Action != Dropped || file.File == nil || file.File.Flow == "" {
			continue
		}
		for _, flow := range b.plan.Objects {
			if flow.Kind == "flow" && flow.Action == Dropped && flow.Held != "" && flow.Name == file.File.Flow {
				file.Held = "flow " + flow.Name + " is kept: " + flow.Held
			}
		}
	}
	return nil
}

// gone is the object an earlier import recorded as po, as the yard has it
// now and with what removing it takes; nil when the yard no longer has it,
// or has it in a state that import did not leave (a pointer since set by
// hand to another flow).
func (b *planner) gone(po store.PackObject) (*Object, error) {
	switch po.Kind {
	case "file":
		// Never a file outside the pack's own directory and its flows' stage
		// files. A stage file is named by where it is in the home: the pack
		// has no path for it any more.
		rel, err := filepath.Rel(b.plan.Install, po.Name)
		file := &FileOp{To: po.Name, From: joinName(b.p.Dir, filepath.ToSlash(rel))}
		if err != nil || !filepath.IsLocal(rel) {
			flow, ok := b.plan.stageFlow(po.Name)
			if !ok {
				return nil, nil
			}
			rel = stageFileName(flow, filepath.Base(po.Name))
			file = &FileOp{To: po.Name, From: b.p.Dir, Flow: flow}
		}
		if info, err := os.Lstat(po.Name); err != nil || !info.Mode().IsRegular() {
			return nil, nil
		}
		var edited bool
		if have, err := os.ReadFile(po.Name); err == nil {
			was := b.installedSums(po.Name)
			edited = len(was) > 0 && !slices.Contains(was, sum(have))
		}
		return &Object{Kind: po.Kind, Name: filepath.ToSlash(rel), File: file, Edited: edited}, nil
	case "group":
		if _, err := b.st.GetGroup(b.ctx, po.Name); errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		o := &Object{Kind: po.Kind, Name: po.Name}
		// A people group is the yard's, shared by its depots: it stays while
		// another import or another depot's route still uses it.
		for _, r := range b.all {
			if (r.Pack != b.p.Name || r.Depot != b.o.Depot) && slices.ContainsFunc(r.Objects, func(x store.PackObject) bool { return x.Kind == "group" && x.Name == po.Name }) {
				o.Held = fmt.Sprintf("pack %s in %s names it too", r.Pack, inDepot(r.Depot))
			}
		}
		routes, err := b.st.Routes(b.ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			if o.Held == "" && r.Names(po.Name) && (r.Depot == nil || *r.Depot != b.o.Depot) {
				o.Held = "a route outside depot " + b.o.Depot + " leads to it"
			}
		}
		return o, nil
	case "flow":
		if _, err := b.st.GetFlow(b.ctx, po.Name); errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		o := &Object{Kind: po.Kind, Name: po.Name}
		ptrs, err := b.st.FlowPointers(b.ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range ptrs {
			if p.Flow == po.Name && p.Depot != b.o.Depot {
				o.Held = p.String() + " follows it"
			}
		}
		return o, nil
	case "pointer":
		ptrs, err := b.st.FlowPointers(b.ctx)
		if err != nil {
			return nil, err
		}
		p := store.FlowPointer{Depot: b.o.Depot, Type: po.Type, Flow: po.Flow}
		if !slices.Contains(ptrs, p) {
			return nil, nil
		}
		held := ""
		if p.Type != "" {
			if held, err = b.strandedTypePointer(&p); err != nil {
				return nil, err
			}
		}
		return &Object{Kind: po.Kind, Name: p.String() + " -> " + p.Flow, Pointer: &p, Held: held}, nil
	case "route":
		routes, err := b.st.Routes(b.ctx)
		if err != nil {
			return nil, err
		}
		op := &RouteOp{Stage: po.Stage, Depot: b.o.Depot, Type: po.Type, Group: po.Group}
		for _, r := range routes {
			if r.Stage == po.Stage && r.Depot != nil && *r.Depot == b.o.Depot && !r.ByBead() && deref(r.Type) == po.Type && !r.Quotaed() && r.Worker() == po.Group {
				op.Delete = append(op.Delete, r.ID)
				op.Priority = r.Priority
			}
		}
		if len(op.Delete) == 0 {
			return nil, nil
		}
		name := "depot " + b.o.Depot + " stage " + po.Stage
		if po.Type != "" {
			name += " type " + po.Type
		}
		return &Object{Kind: po.Kind, Name: name + " -> " + po.Group, Route: op}, nil
	case "subscription":
		if po.Subscription == nil {
			return nil, nil
		}
		r, err := b.st.GetCrew(b.ctx, po.CrewMember)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		sub := *po.Subscription
		if !slices.ContainsFunc(r.Subscriptions, func(s store.Subscription) bool { return s.String() == sub.String() }) {
			return nil, nil
		}
		return &Object{Kind: po.Kind, Name: po.CrewMember + " " + sub.String(), Subscription: &SubscriptionOp{CrewMember: po.CrewMember, Remove: &sub}}, nil
	case "map":
		h, err := b.st.GetHook(b.ctx, po.Name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		// A source the pack still carries is not dropped, though nothing can
		// be set for it now; and a map set by hand since is the yard's. The
		// tables hold other lines too, so only the pack's own are the
		// import's to remove: those the hook still has, and no other pack's
		// last import lists (a line taken off by hand since is gone). The
		// crew member and the map are the hook's whole, so another pack
		// acting as one on it keeps them.
		other := b.others(h.Name)
		says, makes := leftOf(h.Says, po.Says, nil, other.says), leftOf(h.Makes, po.Makes, nil, other.makes)
		as, mp := po.CrewMember, po.Map
		if other.as {
			as, mp = "", nil
		}
		byHand := as != "" && (h.As != as || !maps.Equal(h.Map, mp))
		if slices.ContainsFunc(b.p.Sources, func(m StageMap) bool { return m.Source == h.Source }) || byHand ||
			(as == "" && len(says)+len(makes) == 0) {
			return nil, nil
		}
		return &Object{Kind: po.Kind, Name: mapName(h.Name, po.CrewMember), Map: &MapOp{Hook: h.Name, Source: h.Source, As: as, Map: mp, Says: says, Makes: makes}}, nil
	case "source":
		src, err := b.st.GetSource(b.ctx, po.Name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		// A declaration added by hand since is the yard's.
		if sum([]byte(src.Text)) != po.SHA256 {
			return nil, nil
		}
		o := &Object{Kind: po.Kind, Name: po.Name, Source: &SourceOp{Name: po.Name, Text: src.Text}}
		// A source is the yard's, shared by its depots, and a user who is
		// told through it would be told nowhere without a word.
		for _, r := range b.all {
			if (r.Pack != b.p.Name || r.Depot != b.o.Depot) && slices.ContainsFunc(r.Objects, func(x store.PackObject) bool { return x.Kind == "source" && x.Name == po.Name }) {
				o.Held = fmt.Sprintf("pack %s in %s names it too", r.Pack, inDepot(r.Depot))
			}
		}
		users, err := b.st.Users(b.ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if o.Held == "" && u.Notify == po.Name {
				o.Held = fmt.Sprintf("user %s is told through it (yardr user set %s %s= ends that)", u.Name, u.Name, store.UserNotify)
			}
		}
		return o, nil
	}
	return nil, nil
}

// strandedTypePointer keeps a dropped type-flow pointer when clearing
// it would make the type's open beads follow the depot's newly chosen default
// flow at stages that flow lacks. Clearing a pointer does not itself check
// for stranded beads, so the import has to protect them before pruning.
func (b *planner) strandedTypePointer(p *store.FlowPointer) (string, error) {
	var fallback string
	for _, g := range b.plan.Governs {
		if g.Type == "" {
			fallback = g.Flow
			break
		}
	}
	if fallback == "" {
		return "", nil
	}
	f, err := b.st.GetFlow(b.ctx, fallback)
	if err != nil {
		return "", err
	}
	beads, err := b.st.ListBeads(b.ctx, store.BeadFilter{})
	if err != nil {
		return "", err
	}
	var stranded []string
	for _, bead := range beads {
		if bead.Status == "closed" || bead.Depot == nil || *bead.Depot != p.Depot || bead.Type != p.Type || store.PinnedFlow(&bead) != "" {
			continue
		}
		if !slices.Contains(f.Stages, bead.Stage) {
			stranded = append(stranded, fmt.Sprintf("%s (%s)", bead.ID, bead.Stage))
		}
	}
	if len(stranded) > 0 {
		return fmt.Sprintf("clearing it would strand open beads under flow %s: %s", fallback, strings.Join(stranded, ", ")), nil
	}
	return "", nil
}
