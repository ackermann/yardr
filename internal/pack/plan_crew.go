package pack

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

func (b *planner) crew() error {
	have, err := b.st.Crew(b.ctx)
	if err != nil {
		return err
	}
	settings, err := b.st.Settings(b.ctx)
	if err != nil {
		return err
	}
	var subs []Object
	for _, pr := range b.p.Crew {
		name, told := b.plan.Crew[pr.Name], b.told[pr.Name]
		i := slices.IndexFunc(have, func(r store.CrewMember) bool { return r.Name == name })
		exists := i >= 0
		var old store.CrewMember
		if exists {
			old = have[i]
		}
		op := &CrewOp{Pack: pr.Name, CrewMember: name, On: slices.Clone(pr.Events)}
		if op.On == nil {
			op.On = []string{}
		}
		o := Object{Kind: "crew", Name: name, CrewMember: op}
		var changes []Diff
		if pr.Role != "" {
			op.Prompt = b.installed(pr.Role)
			prompt, _ := old.Config["prompt"].(string)
			own, err := b.depotRole(pr.Role)
			if err != nil {
				return err
			}
			switch {
			case own == "":
			case !b.plan.DepotRoles:
				o.Shadows = append(o.Shadows, own)
			case prompt != "" && prompt != op.Prompt:
				// A crew member is the yard's, not the depot's, so only one that
				// has a prompt of its own keeps it; a new one gets the pack's.
				op.Prompt = prompt
				o.Keeps = append(o.Keeps, fmt.Sprintf("prompt %s: the depot has its own %s", prompt, own))
			}
			if exists && prompt != op.Prompt {
				changes = append(changes, Diff{"prompt", prompt, op.Prompt})
			}
		}
		if exists && !sameSet(old.On, op.On) {
			changes = append(changes, Diff{"on", old.On, op.On})
		}
		if !exists {
			if !told {
				names := make([]string, len(have))
				for i, r := range have {
					names[i] = r.Name
				}
				ask := "which crew member of the yard takes the pack's " + pr.Name + " (--crew " + pr.Name + "=<name>)?"
				if len(names) > 0 {
					ask += " The yard has: " + strings.Join(names, ", ") + "."
				}
				b.ask(&o, "crew", ask, "a new crew member "+name)
			}
			b.ask(&o, "kind", "which agent runs crew member "+name+" (claude, codex, ...)?", settings["default_kind"])
			b.ask(&o, "model", "which model does crew member "+name+" run (empty: the agent's own default)?", "")
			b.ask(&o, "args", "which permission arguments does crew member "+name+"'s agent run with (empty: none, and it stops at every permission prompt)?", "")
			b.ask(&o, "dir", "which directory does crew member "+name+" work in?", "")
		}
		o.Action, o.Changes = action(exists, changes), changes
		b.add(o)

		var want []store.Subscription
		for _, s := range pr.Stages {
			want = append(want, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": b.o.Depot, "stage": s}})
		}
		if pr.Done {
			want = append(want, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"depot": b.o.Depot, "state": store.StateDone}})
		}
		// The sources the yard hears nothing from yet: without one the
		// subscription is skipped, and the next apply sets it once it is
		// there.
		skipped := map[string]Need{}
		for _, s := range pr.Subscribe {
			need, skip, err := b.subSkip(s.Name)
			if err != nil {
				return err
			}
			if skip {
				skipped[s.Name] = need
				continue
			}
			want = append(want, store.Subscription{Source: s.Name, Scope: s.Scope})
		}
		taken := make([]bool, len(old.Subscriptions))
		hears := len(changes) > 0
		for _, w := range want {
			so := Object{Kind: "subscription", Name: name + " " + w.String(), Action: Create, Subscription: &SubscriptionOp{CrewMember: name, Add: &w}}
			if j := slices.IndexFunc(old.Subscriptions, func(s store.Subscription) bool { return reflect.DeepEqual(s.Where, w.Where) && s.Source == w.Source }); j >= 0 {
				// The same source and filters: it is this subscription,
				// whatever its scope is now.
				taken[j] = true
				so.Action = Unchanged
				if have := old.Subscriptions[j]; have.Scope != w.Scope {
					so.Action, so.Changes, so.Subscription.Remove = Change, []Diff{{"scope", have.Scope, w.Scope}}, &have
				}
			}
			hears = hears || so.Action != Unchanged
			subs = append(subs, so)
		}
		if old.Live() && hears {
			op.Restart = true
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("crew member %s is running and its role or what it hears changes: the import does not restart it, so restart it afterwards (yardr crew restart %s)", name, name))
		}
		for j, s := range old.Subscriptions {
			if !taken[j] {
				subs = append(subs, Object{Kind: "subscription", Name: name + " " + s.String(), Action: NotInPack, Subscription: &SubscriptionOp{CrewMember: name}})
			}
		}
		for _, s := range pr.Subscribe {
			if need, ok := skipped[s.Name]; ok {
				b.plan.Sources = append(b.plan.Sources, SourceNeeds{Source: s.Name, CrewMember: name, Needs: []Need{need}})
				continue
			}
			// Nothing of the yard's is needed to hear a source of yardr's own.
			if ownSource(s.Name) {
				continue
			}
			needs, err := b.needs(s.Name, name, exists, old)
			if err != nil {
				return err
			}
			b.plan.Sources = append(b.plan.Sources, SourceNeeds{Source: s.Name, CrewMember: name, Needs: needs})
		}
		if len(pr.OpenThreads) > 0 {
			ot, err := b.openThreads(pr, name, exists, old)
			if err != nil {
				return err
			}
			b.plan.OpenThreads = append(b.plan.OpenThreads, ot)
		}
		if pr.ExposeToPeers {
			ex, err := b.expose(pr, name)
			if err != nil {
				return err
			}
			b.plan.Expose = append(b.plan.Expose, ex)
		}
	}
	for _, o := range subs {
		b.add(o)
	}
	return nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// ownSource reports whether source is yardr's own: the yard's bead events
// or what the yards it is linked with say of its beads. No hook receives
// either, no crew member has an identity in it and nothing is answered in
// it with a credential, so a subscription to it needs nothing of the yard
// (as yardr subscribe takes both without asking for a hook).
func ownSource(source string) bool {
	return source == store.SourceYardr || source == store.SourcePeer
}

// subSkip says why no subscription to source is planned: the yard neither
// has it in Go nor declares it with a hook key, so nothing could be heard
// packFor is " (yardr pack apply <source> brings it)" when the binary
// carries a pack of that name, else "": what a yard without its source
// is told.
func packFor(source string) string {
	if packs.Carries(source) {
		return fmt.Sprintf(" (yardr pack apply %s brings it)", source)
	}
	return ""
}

// from it. ok is false when the subscription is planned.
func (b *planner) subSkip(source string) (need Need, skip bool, err error) {
	if ownSource(source) {
		return Need{}, false, nil
	}
	decl, _, _, err := b.sourceAfter(source)
	if err != nil {
		return Need{}, false, err
	}
	if _, ok := hooks.AdapterFor(source, decl, ""); ok {
		return Need{}, false, nil
	}
	if decl == nil {
		return Need{"source", "missing", fmt.Sprintf("the yard has no source %s%s; the subscription is skipped, and the next apply sets it once it is there", source, packFor(source))}, true, nil
	}
	whose := "the yard's"
	if slices.ContainsFunc(b.p.Declared, func(d Declared) bool { return d.Name == source }) {
		whose = "the pack's"
	}
	return Need{"hook", "missing", fmt.Sprintf("source %s receives no webhooks: %s declaration of %s has no hook key; the subscription is skipped", source, whose, source)}, true, nil
}

// needs is what hearing source, and answering in it, takes of the yard:
// the hook that receives its deliveries and the file holding that hook's
// secret, who the crew member is in the source, the credential it answers
// with, and the server's hook listener. Only names are read, never a
// secret's value.
func (b *planner) needs(source, member string, exists bool, r store.CrewMember) ([]Need, error) {
	const ok, missing, unknown = "ok", "missing", "unknown"
	var out []Need
	hs, err := b.st.Hooks(b.ctx)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(hs, func(h store.Hook) bool { return h.Source == source })
	if i < 0 {
		out = append(out,
			Need{"hook", missing, fmt.Sprintf("no hook receives %s: yardr hook add %s --source %s --secret-file <file>", source, source, source)},
			Need{"secret_file", missing, "the hook names it"})
	} else {
		h := hs[i]
		out = append(out, Need{"hook", ok, fmt.Sprintf("hook %s (POST /hooks/%s)", h.Name, h.Name)})
		// The rule the receiver holds the file to on every delivery.
		if _, _, err := config.ReadSecretFile(h.SecretFile); err != nil {
			out = append(out, Need{"secret_file", missing, err.Error()})
		} else {
			out = append(out, Need{"secret_file", ok, h.SecretFile})
		}
	}
	if id := r.Identities[source]; id.ID != "" || id.Key != "" {
		out = append(out, Need{"identity", ok, "crew member " + member + " has an identity in " + source})
	} else {
		out = append(out, Need{"identity", missing, fmt.Sprintf("yardr crew identity %s --source %s --id <id> --key <key>", member, source)})
	}
	// The names a source's reply credential goes by in a crew member's
	// environment, its env_file's and its env_command's
	// (hooks.CredentialWays): the variable its declaration names, or for
	// a minted one the App's two names or its token.
	decl, _, _, err := b.sourceAfter(source)
	if err != nil {
		return nil, err
	}
	if len(hooks.CredentialWays(source, decl)) > 0 {
		names := hooks.CredentialNames(source, decl)
		if strings.Contains(names, ",") {
			names += "," // APP_ID and ..., or TOKEN, in crew member ...
		}
		n := Need{"credential", missing, fmt.Sprintf("%s in crew member %s's env_file or from its env_command, to answer in %s", names, member, source)}
		if exists {
			env, err := config.ReadCrewEnv(b.ctx, r.Name, r.Dir, b.o.Home, r.Config)
			way, set := hooks.CredentialSet(source, env.Keys(), decl)
			switch {
			case !env.Set():
				n.Detail += ": it has neither"
			case err != nil:
				n.Detail += ": " + err.Error()
			case !set:
				n.Detail += ": " + env.From() + " does not set it"
			default:
				verb := " is set in "
				if len(way.Names) > 1 {
					verb = " are set in "
				}
				n = Need{"credential", ok, strings.Join(way.Names, " and ") + verb + env.From()}
			}
		}
		out = append(out, n)
	}
	// What the server of the yard in Home said when it started; a plan made
	// without one (no home, or no server on it) cannot tell.
	settings, err := b.st.Settings(b.ctx)
	if err != nil {
		return nil, err
	}
	l, known := hooks.ReadListener(&config.Config{Home: b.o.Home})
	listening, text := hooks.Listens(l, known, settings[hooks.FromSetting])
	switch {
	case !known:
		out = append(out, Need{"listener", unknown, text})
	case listening:
		out = append(out, Need{"listener", ok, text})
	default:
		out = append(out, Need{"listener", missing, text})
	}
	return out, nil
}

// expose sorts the yard's peers by what an import does about exposing the
// pack's crew member pr, the yard's name, to each. An earlier yes the
// crew member no longer has (taken away with --no-expose) is asked again,
// never given back unasked; a no stays a no until the operator exposes it
// by hand.
func (b *planner) expose(pr CrewMember, name string) (Expose, error) {
	ex := Expose{Pack: pr.Name, CrewMember: name, Has: []string{}, Declined: []string{}, Asks: []string{}, Keys: map[string]string{}}
	peers, err := b.st.Peers(b.ctx)
	if err != nil {
		return ex, err
	}
	var recorded map[string]bool
	if b.rec != nil {
		recorded = b.rec.Expose[name]
	}
	for _, p := range peers {
		ex.Keys[p.Name] = p.Key
		yes, answered := recorded[p.Key]
		if answered {
			if ex.Recorded == nil {
				ex.Recorded = map[string]bool{}
			}
			ex.Recorded[p.Name] = yes
		}
		switch {
		case slices.Contains(p.Expose, name):
			ex.Has = append(ex.Has, p.Name)
		case answered && !yes:
			ex.Declined = append(ex.Declined, p.Name)
		default:
			ex.Asks = append(ex.Asks, p.Name)
		}
	}
	return ex, nil
}

// openThreads sorts the sources the pack asks that its crew member pr may open
// threads in by what an import does about each. A source is asked about
// only when the yard has a hook and a credential for it, read as needs
// reads them for the Sources table: letting a crew member speak first where
// the yard cannot hear or answer is nothing an operator can judge yet. An
// earlier yes the crew member no longer has (the setting was taken away by
// hand) is asked again, never given back unasked.
func (b *planner) openThreads(pr CrewMember, name string, exists bool, r store.CrewMember) (OpenThreads, error) {
	ot := OpenThreads{Pack: pr.Name, CrewMember: name, Wants: slices.Clone(pr.OpenThreads),
		Has: []string{}, Lacks: []Lack{}, Declined: []string{}, Asks: []string{}}
	var recorded map[string]bool
	if b.rec != nil {
		recorded = b.rec.OpenThreads[name]
	}
	may := r.MayOpen()
	for _, src := range ot.Wants {
		yes, answered := recorded[src]
		if answered {
			if ot.Recorded == nil {
				ot.Recorded = map[string]bool{}
			}
			ot.Recorded[src] = yes
		}
		if slices.Contains(may, src) {
			ot.Has = append(ot.Has, src)
			continue
		}
		// A source the yard has nothing to open a thread with: without
		// one the import asks nothing, and the next one asks once it is
		// there.
		if need, skip, err := b.openSkip(src); err != nil {
			return ot, err
		} else if skip {
			ot.Lacks = append(ot.Lacks, Lack{Source: src, Missing: []Need{need}})
			continue
		}
		needs, err := b.needs(src, name, exists, r)
		if err != nil {
			return ot, err
		}
		var missing []Need
		for _, n := range needs {
			if (n.What == "hook" || n.What == "credential") && n.Status != "ok" {
				missing = append(missing, n)
			}
		}
		switch {
		case len(missing) > 0:
			ot.Lacks = append(ot.Lacks, Lack{Source: src, Missing: missing})
		case answered && !yes:
			ot.Declined = append(ot.Declined, src)
		default:
			ot.Asks = append(ot.Asks, src)
		}
	}
	return ot, nil
}

// openSkip says why the import asks nothing about opening threads in
// source: the yard neither has it in Go nor declares it with the keys
// opening takes. ok is false when the source is asked about.
func (b *planner) openSkip(source string) (need Need, skip bool, err error) {
	decl, _, _, err := b.sourceAfter(source)
	if err != nil {
		return Need{}, false, err
	}
	if _, ok := hooks.OpenerFor(source, decl, "", ""); ok {
		return Need{}, false, nil
	}
	if decl == nil {
		return Need{"source", "missing", fmt.Sprintf("the yard has no source %s%s; the next apply asks once it is there", source, packFor(source))}, true, nil
	}
	whose := "the yard's"
	if slices.ContainsFunc(b.p.Declared, func(d Declared) bool { return d.Name == source }) {
		whose = "the pack's"
	}
	return Need{"source", "missing", fmt.Sprintf("source %s opens no thread: %s declaration of %s lacks the keys opening takes", source, whose, source)}, true, nil
}
