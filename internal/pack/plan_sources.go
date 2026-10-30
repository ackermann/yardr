package pack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// yardSource is the yard's source of name: builtin when yardr has it in
// Go, decl the yard's declaration of it, nil when it declares none.
// unreadable reports a declaration this yardr does not read.
func (b *planner) yardSource(name string) (builtin bool, decl *source.Declaration, unreadable bool, err error) {
	_, builtin = hooks.Adapters[name]
	src, err := b.st.GetSource(b.ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return builtin, nil, false, nil
	}
	if err != nil {
		return false, nil, false, err
	}
	decl, err = src.Declaration()
	if err != nil {
		return builtin, nil, true, nil
	}
	return builtin, decl, false, nil
}

// checkSourceLines holds a pack's lines for a source to the yard's source
// of the name: a status hears names is one the source has, and a filter
// names keys its events carry. builtin is a source yardr has in Go, decl
// the yard's declaration of it.
func checkSourceLines(sm StageMap, builtin bool, decl *source.Declaration) error {
	if sm.As != "" {
		f, ok := hooks.ForwarderFor(sm.Source, decl, "", "")
		if !ok {
			if builtin {
				return fmt.Errorf("yardr cannot set a status in %s", sm.Source)
			}
			return fmt.Errorf("source %s moves no issue: its declaration lacks the keys moving takes (a credential, and thread.reply, posted, read and state with states, and link)", sm.Source)
		}
		// Sorted, so one plan names the same word.
		for _, k := range slices.Sorted(maps.Keys(sm.Map)) {
			if err := f.Places(sm.Map[k]); err != nil {
				return fmt.Errorf("hears %s=%s: %v", k, sm.Map[k], err)
			}
		}
	}
	if len(sm.Says) == 0 && len(sm.Makes) == 0 {
		return nil
	}
	h := store.Hook{Name: sm.Source, Source: sm.Source, Says: sm.Says, Makes: sm.Makes}
	// Without the hook's name, which the yard has not named yet: what the
	// line says is said about the source.
	unhooked := func(err error) error {
		s, _ := strings.CutPrefix(err.Error(), "hook "+sm.Source+": ")
		return errors.New(s)
	}
	if builtin {
		if err := h.CheckSays(); err != nil {
			return unhooked(err)
		}
		if err := h.CheckMakes(); err != nil {
			return unhooked(err)
		}
		return nil
	}
	keys := slices.Clone(store.WhereKeys)
	if decl.Hook != nil {
		for _, k := range decl.Hook.Keys {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	if err := h.CheckSaysKeys(append(slices.Clone(keys), store.SayStage)); err != nil {
		return unhooked(err)
	}
	if err := h.CheckMakesKeys(keys); err != nil {
		return unhooked(err)
	}
	return nil
}

// declared plans the sources the pack brings by a declaration: each is added
// under its name, and one the yard has under that name is replaced, with
// what changes of it shown, since a source is where a credential of the
// yard is sent. The yard's own settings of the source stay.
func (b *planner) declared() error {
	const ok, missing = "ok", "missing"
	if len(b.p.Declared) == 0 {
		return nil
	}
	users, err := b.st.Users(b.ctx)
	if err != nil {
		return err
	}
	for _, d := range b.p.Declared {
		decl := d.Declaration
		dp := DeclaredPlan{Source: d.Name, File: d.File, Base: decl.Address(), Calls: []string{}, Needs: []Need{}}
		for _, c := range decl.Calls {
			dp.Calls = append(dp.Calls, c.Name+" ("+c.Kind+")")
		}
		commands, err := b.sourceCommands(d)
		if err != nil {
			return err
		}
		dp.Commands = commands
		have, err := b.st.GetSource(b.ctx, d.Name)
		exists := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var changes []Diff
		envFile, mintCommand := "", ""
		if exists {
			envFile, mintCommand = have.EnvFile, have.MintCommand
			var was *source.Declaration
			if have.Text != d.Text {
				was, _ = have.Declaration()
				changes = declarationDiff(was, decl)
				// A grant of read covers the read calls a later file adds:
				// who gets one that way is shown, since nobody is asked.
				grants, err := b.st.SourceGrants(b.ctx, d.Name)
				if err != nil {
					return err
				}
				if calls, who := store.Widened(was, decl, grants); len(calls) > 0 {
					holders := make([]ReachHolder, len(who))
					for i, g := range who {
						holders[i] = ReachHolder{Grantee: g.Grantee, As: g.As}
					}
					dp.Reaches = &Reach{Calls: calls, Who: holders}
				}
				dp.StillGranted = store.StillGranted(was, decl, grants)
			}
			changes = append(changes, b.commandDiffs(d, was, commands)...)
			if have.Text != d.Text && len(changes) == 0 {
				// Another text that declares the same: comments, an
				// about, order.
				changes = []Diff{{Field: "declaration", Old: "another text", New: "the pack's, which declares the same calls"}}
			}
		}
		dp.Action = action(exists, changes)
		if n, has := credentialNeed(decl, envFile, decl.MintArgv(b.o.Home, mintCommand)); has {
			// A file nobody needs is not a need: with no grant or
			// notification the server makes no call of the source,
			// and a crew member holds the key its commands run with.
			// Only its check call wants the source's own file then.
			quiet := n.Status == "missing" && envFile == ""
			if quiet {
				grants, err := b.st.SourceGrants(b.ctx, d.Name)
				if err != nil {
					return err
				}
				told := slices.ContainsFunc(users, func(u store.User) bool { return u.Notify == d.Name })
				holder, _ := KeyHolder(b.ctx, b.st, decl)
				quiet = len(grants) == 0 && !told && holder != ""
			}
			if !quiet {
				dp.Needs = append(dp.Needs, n)
			}
		}
		if n := decl.Notify; n != nil {
			var told []string
			for _, u := range users {
				if u.Notify == d.Name {
					told = append(told, u.Name)
				}
			}
			if len(told) > 0 {
				dp.Needs = append(dp.Needs, Need{store.UserNotify, ok, "tells " + strings.Join(told, ", ")})
			} else {
				dp.Needs = append(dp.Needs, Need{store.UserNotify, missing, fmt.Sprintf("it can tell a user outside the yard and no user names it. The import names none: yardr user key <user> %s gives their %s, then yardr user set <user> %s=%s", d.Name, n.AddressEnv, store.UserNotify, d.Name)})
			}
		}
		op := &SourceOp{Name: d.Name, From: joinName(b.p.Dir, d.File), Text: d.Text, DeclDir: declDir(d.File)}
		// The files the import copies: every command's, since an absolute
		// program is refused above.
		op.Commands = append(op.Commands, commands...)
		b.add(Object{Kind: "source", Name: d.Name, Action: dp.Action, Changes: changes, Source: op})
		b.plan.Declared = append(b.plan.Declared, dp)
	}
	return nil
}

// Reach is what a replaced declaration widens without a new grant: the
// read calls it adds or changes, and the holders that get them through
// their grant of store.GrantRead, which was given before the call was as it
// is (store.Widened). A plan shows it, since nobody is asked about it.
type Reach struct {
	Calls []string      `json:"calls"`
	Who   []ReachHolder `json:"who"`
}

// ReachHolder is a grantee reached through read, with As set when that
// grant makes calls with a user's credential. Embedding Grantee keeps the
// existing plain-holder JSON shape and adds only the as field where needed.
type ReachHolder struct {
	store.Grantee
	As string `json:"as,omitempty"`
}

func (h ReachHolder) Holder() string {
	return (store.SourceGrant{Grantee: h.Grantee, As: h.As}).Holder()
}

// declDir is the declaration file's directory in the pack, a slash path,
// "" for the pack's root.
func declDir(file string) string {
	if dir := path.Dir(file); dir != "." {
		return dir
	}
	return ""
}

// sourceCommands are the declaration's commands as the pack has them: each
// command, its program and the sha256 of the pack's file. An absolute
// program is refused: every program a pack's declaration runs with a
// credential is one the yard copies and hashes, so a loose declaration's
// absolute program (the operator's own) is never a pack's to bring.
func (b *planner) sourceCommands(d Declared) ([]CommandPlan, error) {
	var out []CommandPlan
	for _, c := range d.Declaration.Commands() {
		if !c.Relative() {
			return nil, fmt.Errorf("declaration %s: %s: the program %q is absolute: a command a declaration in a pack brings is a file beside the declaration's file that the yard copies and hashes", d.File, c.Key, c.Argv[0])
		}
		data, err := b.p.read(path.Join(declDir(d.File), c.Argv[0]))
		if err != nil {
			return nil, fmt.Errorf("declaration %s names the command file %s: %v", d.File, c.Argv[0], err)
		}
		out = append(out, CommandPlan{Key: c.Key, Program: c.Argv[0], SHA256: sum(data)})
	}
	return out, nil
}

// commandDiffs are what the pack's command files change of the copies the
// yard runs, under <home>/sources/<name>/: a file that is new, gone or
// different. commands are the pack's, as sourceCommands has them; was is
// the yard's declaration of the source, nil when the pack's text is the
// yard's or this yardr does not read the yard's.
func (b *planner) commandDiffs(d Declared, was *source.Declaration, commands []CommandPlan) []Diff {
	var out []Diff
	// The yard's copies, by program, "" where the copy is not there.
	installed := map[string]string{}
	for _, f := range d.Declaration.CommandFiles(b.o.Home) {
		if f.Declared {
			installed[f.Program] = f.SHA256
		}
	}
	seen := map[string]bool{}
	for _, c := range commands {
		if seen[c.Program] {
			continue
		}
		seen[c.Program] = true
		if installed[c.Program] != c.SHA256 {
			out = append(out, Diff{"file " + c.Program, shortSHA(installed[c.Program]), "sha256 " + c.SHA256[:12]})
		}
	}
	if was != nil {
		for _, f := range was.CommandFiles(b.o.Home) {
			if !f.Declared || seen[f.Program] {
				continue
			}
			seen[f.Program] = true
			if f.SHA256 != "" {
				out = append(out, Diff{"file " + f.Program, "sha256 " + f.SHA256[:12], ""})
			}
		}
	}
	return out
}

// shortSHA is a sha256 as a changed file's value shows it: "" for a copy
// that is not there, which the plan prints as (none).
func shortSHA(sum string) string {
	if sum == "" {
		return ""
	}
	return "sha256 " + sum[:12]
}

// KeyHolder is the crew member whose env_file sets the variables the
// credential of decl is read from, and the names it sets for them: "" when
// none does. Only env files are read, and only the names they set: an
// env_command is never run to tell, so a crew member that holds the key
// that way alone is not named.
func KeyHolder(ctx context.Context, st store.API, decl *source.Declaration) (string, []string) {
	if decl == nil || decl.Credential == nil {
		return "", nil
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return "", nil
	}
	for _, r := range crew {
		path, _ := r.Config["env_file"].(string)
		if path == "" {
			continue
		}
		f, err := store.ReadEnvFile(path)
		if err != nil {
			continue
		}
		if way, ok := hooks.CredentialSet(decl.Name, f.Keys(), decl); ok {
			return r.Name, way.Names
		}
	}
	return "", nil
}

// credentialNeed is whether the file the credential of decl is read from
// is set and sets what decl names, and where it names a mint, whether a
// command mints it: mint is the one that will (Declaration.MintArgv), the
// operator's setting or the one decl brings. has is false for a
// declaration that names no credential.
func credentialNeed(decl *source.Declaration, envFile string, mint []string) (n Need, has bool) {
	if decl.Credential == nil {
		return n, false
	}
	state, err := decl.CredentialState(envFile, mint, store.ReadEnvFileEntries)
	switch {
	case err == nil:
		return Need{store.SourceEnvFile, "ok", state}, true
	case envFile == "":
		// The command that writes the file first, so that nobody does by
		// hand; the file the operator keeps themselves is named instead.
		// An agent reads this from pack check, where source key is refused
		// from a pane, so its way on is named too: this names no caller.
		return Need{store.SourceEnvFile, "missing", fmt.Sprintf("no env_file is set for it, and a pack neither carries nor names one: yardr source key %s gives %s, or yardr source set %s env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret %s --context \"<where to get it>\"", decl.Name, decl.Credential.Names(), decl.Name, decl.Name)}, true
	}
	var noCommand *source.NoMintCommand
	if errors.As(err, &noCommand) {
		return Need{store.SourceMintCommand, "missing", state + ", and the pack's declaration brings none (credential.mint_command): " + err.Error()}, true
	}
	return Need{store.SourceEnvFile, "missing", err.Error()}, true
}

// calls sorts what the pack asks that its groups' sessions and its crew may
// call by what an import does about each, as openThreads does for the
// sources a crew member may open threads in. A call is asked about only
// when the yard will have the source, the source the call, and the file its
// credential is read from is set: a grant of a call nobody can make yet is
// nothing an operator can judge. So an import into a yard that has not the
// source adds it and asks nothing, and the one after the operator set its
// env_file asks. An earlier yes the grantee no longer has (the grant was
// revoked) is asked again, never given back unasked.
func (b *planner) calls() error {
	type asker struct {
		pack  string
		to    store.Grantee
		asked []CallsAsked
	}
	var askers []asker
	for _, g := range b.p.Groups {
		askers = append(askers, asker{g.Name, store.Grantee{Group: b.plan.Groups[g.Name]}, g.Calls})
	}
	for _, r := range b.p.Crew {
		askers = append(askers, asker{r.Name, store.Grantee{Crew: b.plan.Crew[r.Name]}, r.Calls})
	}
	if !slices.ContainsFunc(askers, func(a asker) bool { return len(a.asked) > 0 }) {
		return nil
	}
	grants, err := b.st.SourceGrants(b.ctx, "")
	if err != nil {
		return err
	}
	for _, a := range askers {
		for _, asked := range a.asked {
			c := Calls{Pack: a.pack, Group: a.to.Group, CrewMember: a.to.Crew, Source: asked.Source, Wants: slices.Clone(asked.Calls),
				Updates: []string{}, Has: []string{}, Lacks: []string{}, Missing: []Need{}, Declined: []string{}, Asks: []string{}}
			decl, envFile, mintCommand, err := b.sourceAfter(asked.Source)
			if err != nil {
				return err
			}
			var recorded map[string]bool
			if b.rec != nil {
				recorded = b.rec.Calls[a.to.String()]
			}
			var has store.SourceGrant
			// A pack asks for plain calls alone: a grant as a user is
			// that user's to give and no pack carries one.
			if i := slices.IndexFunc(grants, func(g store.SourceGrant) bool {
				return g.Source == asked.Source && g.Grantee == a.to && g.As == ""
			}); i >= 0 {
				has = grants[i]
			}
			// unready is why nothing of the source is asked about yet.
			unready := true
			if decl == nil {
				detail := fmt.Sprintf("the yard has no source %s, and the pack declares none (sources.%s: declaration): yardr source add <its file>, then apply the pack again", asked.Source, asked.Source)
				if packs.Carries(asked.Source) {
					detail = fmt.Sprintf("the yard has no source %s, and the pack declares none: yardr pack apply %s brings it, then apply the pack again", asked.Source, asked.Source)
				}
				c.Missing = append(c.Missing, Need{"source", "missing", detail})
			} else if n, named := credentialNeed(decl, envFile, decl.MintArgv(b.o.Home, mintCommand)); named && n.Status != "ok" {
				c.Missing = append(c.Missing, Need{n.What, n.Status, n.Detail + ", then apply the pack again"})
			} else {
				unready = false
			}
			for _, w := range c.Wants {
				yes, answered := recorded[CallKey(asked.Source, w)]
				if answered {
					if c.Recorded == nil {
						c.Recorded = map[string]bool{}
					}
					c.Recorded[w] = yes
				}
				var call *source.Call
				exists := w == store.GrantRead
				if decl != nil && !exists {
					if call, exists = decl.Call(w); exists && call.Kind == source.Update {
						c.Updates = append(c.Updates, w)
					}
				}
				switch {
				case decl != nil && !exists:
					// Of a source the pack does not declare: the reader holds
					// the pack's own to its file.
					names := make([]string, len(decl.Calls))
					for i, call := range decl.Calls {
						names[i] = call.Name
					}
					c.Lacks = append(c.Lacks, w)
					c.Missing = append(c.Missing, Need{"call", "missing", fmt.Sprintf("the source %s has no call %s (it has: %s)", asked.Source, w, strings.Join(names, ", "))})
				case w == store.GrantRead && slices.Contains(has.Calls, w), call != nil && has.Covers(call, ""):
					// What the grantee has is said as it is, whatever is missing.
					c.Has = append(c.Has, w)
				case unready:
					c.Lacks = append(c.Lacks, w)
				case answered && !yes:
					c.Declined = append(c.Declined, w)
				default:
					c.Asks = append(c.Asks, w)
				}
			}
			b.plan.Calls = append(b.plan.Calls, c)
		}
	}
	return nil
}

// sourceAfter is the declaration the yard has under name once the plan is
// applied, the pack's where it brings one, and the file the yard reads its
// credential from; nil for a source neither has, or one this build does not
// read.
func (b *planner) sourceAfter(name string) (decl *source.Declaration, envFile, mintCommand string, err error) {
	have, err := b.st.GetSource(b.ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, "", "", err
	}
	if err == nil {
		envFile, mintCommand = have.EnvFile, have.MintCommand
		decl, _ = have.Declaration()
	}
	if i := slices.IndexFunc(b.p.Declared, func(d Declared) bool { return d.Name == name }); i >= 0 {
		decl = b.p.Declared[i].Declaration
	}
	return decl, envFile, mintCommand, nil
}

// declarationDiff is what the declaration now changes of the one the yard
// has, was: where calls go, what they send the credential in, and each call.
// was is nil for one this build does not read. Empty when the two declare
// the same: the caller says what that means.
func declarationDiff(was, now *source.Declaration) []Diff {
	var out []Diff
	if was == nil {
		return []Diff{{"declaration", "one this yardr does not read", "the pack's"}}
	}
	credential := func(d *source.Declaration) string {
		if d.Credential == nil {
			return ""
		}
		out := strings.TrimSpace(strings.Join(d.Credential.Envs(), ", for an update ") + " " + d.Credential.Header)
		if d.Credential.Minted() {
			out = "minted from " + strings.Join(d.Credential.Mint, " and ") + "; " + out
		}
		if d.Credential.URL {
			out += ", the address every call goes to"
		}
		return out
	}
	notify := func(d *source.Declaration) string {
		if d.Notify == nil {
			return ""
		}
		return "the call " + d.Notify.Call + ", at " + d.Notify.AddressEnv
	}
	// The regions' bases are compact JSON, as the setup card's
	// declarationKey writes them: none, absent or empty, reads as no value.
	bases := func(d *source.Declaration) string {
		if len(d.Bases) == 0 {
			return ""
		}
		text, _ := json.Marshal(d.Bases) // a map of strings always marshals
		return string(text)
	}
	line := func(c *source.Call) string { return "(" + c.Kind + ") " + c.Request.Line() }
	for _, f := range []struct{ field, old, new string }{
		{"base", was.Address(), now.Address()}, {"bases", bases(was), bases(now)}, {"credential", credential(was), credential(now)}, {"check", was.Check, now.Check}, {"idempotency", was.Idempotency, now.Idempotency}, {"notify", notify(was), notify(now)},
	} {
		if f.old != f.new {
			out = append(out, Diff{f.field, f.old, f.new})
		}
	}
	for i := range now.Calls {
		c := &now.Calls[i]
		old, had := was.Call(c.Name)
		switch {
		case !had:
			out = append(out, Diff{"call " + c.Name, "", line(c)})
		case !reflect.DeepEqual(asRead(*old), asRead(*c)):
			// The same line on both sides says the change is in the call's
			// parameters, body, build or answer.
			out = append(out, Diff{"call " + c.Name, line(old), line(c)})
		}
	}
	for i := range was.Calls {
		if _, has := now.Call(was.Calls[i].Name); !has {
			out = append(out, Diff{"call " + was.Calls[i].Name, line(&was.Calls[i]), ""})
		}
	}
	return out
}

// asRead is a call as two readings of it compare: by what the file says,
// which is all its JSON holds.
func asRead(c source.Call) any {
	var v any
	text, _ := json.Marshal(c)
	_ = json.Unmarshal(text, &v)
	return v
}

// inDepot is where a pack was imported: its depot, or the yard itself for
// a pack of kind source, which names no depot.
func inDepot(depot string) string {
	if depot == "" {
		return "the yard"
	}
	return "depot " + depot
}

// mapName is what a plan calls the stage map on hook, acted on as crew member.
func mapName(hook, as string) string {
	if as == "" {
		return "hook " + hook
	}
	return "hook " + hook + " as " + as
}
