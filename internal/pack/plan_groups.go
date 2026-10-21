package pack

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// groupName is the yard's group for the pack's g, and whether that group
// takes g as it is instead of being set by the pack: the one a flag names,
// else the one the last import recorded for it, else GroupName, the depot's
// own. A group the last import mapped g to and did not record among the
// pack's objects took it then, and does again while it still fits.
func (b *planner) groupName(g Group, have []store.Group) (name string, takes bool, err error) {
	own := GroupName(b.o.Depot, g)
	has := func(name string) *store.Group {
		if i := slices.IndexFunc(have, func(h store.Group) bool { return h.Name == name }); i >= 0 {
			return &have[i]
		}
		return nil
	}
	if to, told := b.o.Groups[g.Name]; told {
		// The import's own name for it is how a flag says no group takes
		// it: the depot gets its own group, which ends an earlier --group.
		// But a group the yard has under that name is the yard's, whatever
		// the flag names it for: it takes the pack's as it is, unless the
		// last import made it the pack's own.
		if to == own && (has(own) == nil || b.owned(own)) {
			return own, false, nil
		}
		h := has(to)
		switch {
		case h == nil:
			return "", false, fmt.Errorf("--group %s=%s: the yard has no group %s (it has, to take %s: %s)", g.Name, to, to, groupKind(g.runner()), or(strings.Join(fitting(g, have), ", "), "none"))
		case !Fits(g, *h):
			return "", false, fmt.Errorf("--group %s=%s: the pack's %s is %s, and %s is %s", g.Name, to, g.Name, groupKind(g.runner()), to, groupKind(h.Runner))
		}
		return to, true, nil
	}
	if b.rec != nil {
		if name := b.rec.Groups[g.Name]; name != "" && has(name) != nil {
			if b.owned(name) {
				return name, false, nil
			}
			if Fits(g, *has(name)) {
				return name, true, nil
			}
		}
	}
	return own, false, nil
}

// owned reports whether the last import of the pack into the depot made
// name the pack's own group, rather than one of the yard's taking it.
func (b *planner) owned(name string) bool {
	return b.rec != nil && slices.ContainsFunc(b.rec.Objects, func(po store.PackObject) bool { return po.Kind == "group" && po.Name == name })
}

// fitting are the names of the yard's groups that may take the pack's g.
func fitting(g Group, have []store.Group) []string {
	var out []string
	for _, h := range have {
		if Fits(g, h) {
			out = append(out, h.Name)
		}
	}
	return out
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// taken is the plan's object for old, a group of the yard that takes the
// pack's g: the group as it stays, and under Keeps what the pack carries
// for g and does not set on it.
func (b *planner) taken(g Group, old store.Group) Object {
	o := Object{Kind: "group", Name: old.Name, Action: Takes, Group: &old}
	const why = ": the group is the yard's, and the pack's %s is not set on it"
	switch {
	case g.Agent != "":
		if prompt, _ := old.Config["prompt"].(string); prompt != b.installed(g.Agent) {
			o.Keeps = append(o.Keeps, fmt.Sprintf("prompt %s"+why, or(prompt, "(none)"), "role "+g.Agent))
		}
	case g.Run != "":
		if cmd, _ := old.Config["cmd"].(string); cmd != setScript(cmd, b.installed(g.Run)) {
			o.Keeps = append(o.Keeps, fmt.Sprintf("cmd %s"+why, or(cmd, "(none)"), "script "+g.Run))
		}
	}
	if g.Limit > 0 && g.Limit != old.Limit {
		o.Keeps = append(o.Keeps, fmt.Sprintf("limit %d"+why, old.Limit, fmt.Sprintf("limit %d", g.Limit)))
	}
	return o
}

// bindsAs settles a new agent group that binds as another group of the
// pack (Group.BindsAs) and names the yard's group it takes the agent from,
// or "" when it is asked about as any new agent group is: it binds as no
// other, the caller binds it itself (Options.Bound), or the group it binds
// as is no agent group of the yard. A group the yard has gives its agent
// kind, model and arguments as they are, copied into want; one this import
// makes is asked about, and Apply copies what the caller answers.
func (b *planner) bindsAs(g Group, have []store.Group, want *store.Group) string {
	if g.BindsAs == "" || slices.Contains(b.o.Bound, g.Name) {
		return ""
	}
	// Groups are planned in the pack's order, and the reader holds a group
	// to binding as one before it.
	name := b.plan.Groups[g.BindsAs]
	if i := slices.IndexFunc(have, func(h store.Group) bool { return h.Name == name }); i >= 0 {
		if have[i].Runner != "herdr" {
			return ""
		}
		for _, k := range []string{"kind", "model", "args"} {
			if v, ok := have[i].Config[k]; ok {
				want.Config[k] = v
			}
		}
		return name
	}
	if !slices.ContainsFunc(b.plan.Objects, func(o Object) bool {
		return o.Kind == "group" && o.Name == name && o.Group != nil && o.Group.Runner == "herdr"
	}) {
		return ""
	}
	return name
}

func (b *planner) groups() error {
	have, err := b.st.Groups(b.ctx)
	if err != nil {
		return err
	}
	settings, err := b.st.Settings(b.ctx)
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, g := range b.p.Groups {
		name, takes, err := b.groupName(g, have)
		if err != nil {
			return err
		}
		named[name] = true
		b.plan.Groups[g.Name] = name
		if takes {
			b.add(b.taken(g, have[slices.IndexFunc(have, func(h store.Group) bool { return h.Name == name })]))
			continue
		}
		want := store.Group{Name: name, Config: map[string]any{}, Limit: 1}
		i := slices.IndexFunc(have, func(h store.Group) bool { return h.Name == name })
		exists := i >= 0
		var old store.Group
		if exists {
			// What the pack does not carry stays as the yard has it.
			old = have[i]
			want.Limit, want.Members, want.Subscriptions = old.Limit, slices.Clone(old.Members), old.Subscriptions
			for k, v := range old.Config {
				want.Config[k] = v
			}
		}
		if g.Limit > 0 {
			want.Limit = g.Limit
		}
		o := Object{Kind: "group", Name: name}
		// A new group may be one the yard has instead. Not a script group:
		// its command is the pack's script, and nothing is asked for one.
		// Nor a group that binds as another, which carries no question
		// unless the caller binds it itself: --group names the one that
		// takes it.
		binds := g.BindsAs != "" && !slices.Contains(b.o.Bound, g.Name)
		if _, told := b.o.Groups[g.Name]; !exists && !told && g.Run == "" && !binds {
			if fit := fitting(g, have); len(fit) > 0 {
				b.ask(&o, "group", fmt.Sprintf("which group of the yard takes the pack's %s (--group %s=<name>)? The yard has: %s.", g.Name, g.Name, strings.Join(fit, ", ")), "a new group "+name)
			}
		}
		var field string
		switch {
		case g.Agent != "":
			want.Runner, field = "herdr", "prompt"
			want.Config[field] = b.installed(g.Agent)
			own, err := b.depotRole(g.Agent)
			if err != nil {
				return err
			}
			switch {
			case own == "":
			case b.plan.DepotRoles:
				// Told to keep the depot's own role: an existing prompt stays,
				// and a new group, or one the pack's copy was set on, gets
				// the role by the name the depot's file is found under.
				prompt, _ := old.Config[field].(string)
				if prompt == "" || prompt == b.installed(g.Agent) {
					prompt = g.Agent
				}
				want.Config[field] = prompt
				o.Keeps = append(o.Keeps, fmt.Sprintf("prompt %s: the depot has its own %s", prompt, own))
			default:
				o.Shadows = append(o.Shadows, own)
			}
			if !exists {
				if o.Binds = b.bindsAs(g, have, &want); o.Binds == "" {
					b.ask(&o, "kind", "which agent runs group "+name+" (claude, codex, ...)?", settings["default_kind"])
					b.ask(&o, "model", "which model does group "+name+" run (empty: the agent's own default)?", "")
					b.ask(&o, "args", "which permission arguments does group "+name+"'s agent run with (empty: none, and it stops at every permission prompt)?", "")
				}
			}
		case g.Run != "":
			want.Runner, field = "exec", "cmd"
			cmd, _ := old.Config["cmd"].(string)
			want.Config[field] = setScript(cmd, b.installed(g.Run))
		default:
			want.Runner = "manual"
			if !exists {
				b.ask(&o, "members", "who is in group "+name+" (actors who may move a bead out of its stages; empty: anyone)?", "")
			}
		}
		if want.Runner != "manual" {
			// Only people groups have members; one that becomes an agent
			// or script group loses them, or the store refuses it.
			want.Members = nil
		}
		var changes []Diff
		if exists {
			if old.Runner != want.Runner {
				changes = append(changes, Diff{"runner", old.Runner, want.Runner})
			}
			if field != "" && !reflect.DeepEqual(old.Config[field], want.Config[field]) {
				changes = append(changes, Diff{field, old.Config[field], want.Config[field]})
			}
			if old.Limit != want.Limit {
				changes = append(changes, Diff{"limit", old.Limit, want.Limit})
			}
			if len(old.Members) > 0 && want.Members == nil {
				changes = append(changes, Diff{"members", old.Members, []string{}})
			}
		}
		o.Action, o.Changes, o.Group = action(exists, changes), changes, &want
		// A group the yard has keeps the last import's binding in the
		// record while the pack still says binds_as: the import binds
		// nothing anew, so the record must not change either, or no
		// re-import is ever unchanged.
		if exists && o.Binds == "" && g.BindsAs != "" && !slices.Contains(b.o.Bound, g.Name) && b.rec != nil {
			if i := slices.IndexFunc(b.rec.Objects, func(po store.PackObject) bool { return po.Kind == "group" && po.Name == name }); i >= 0 {
				o.bindsKept = b.rec.Objects[i].Binds
			}
		}
		b.add(o)
	}
	// The depot's other groups, by the name an import gives a depot's groups.
	for _, h := range have {
		if !named[h.Name] && strings.HasPrefix(h.Name, b.o.Depot+"-") {
			b.add(Object{Kind: "group", Name: h.Name, Action: NotInPack})
		}
	}
	return nil
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// setScript puts script in place of the command a group's cmd runs and
// keeps the rest: the variable assignments before it, which is how a
// script's options are set (YARDR_PUSH=origin /path/to/merge-local), and
// any arguments after it. An empty cmd becomes the script alone.
func setScript(cmd, script string) string {
	script = shellQuote(script)
	i := 0
	for i < len(cmd) {
		for i < len(cmd) && (cmd[i] == ' ' || cmd[i] == '\t') {
			i++
		}
		end := wordEnd(cmd, i)
		if i == end {
			break
		}
		if !assignment.MatchString(cmd[i:end]) {
			return cmd[:i] + script + cmd[end:]
		}
		i = end
	}
	if strings.TrimSpace(cmd) == "" {
		return script
	}
	return strings.TrimRight(cmd, " \t") + " " + script
}

// wordEnd is where the shell word starting at i ends: at the first blank
// outside quotes.
func wordEnd(s string, i int) int {
	var quote byte
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == 0 && (c == ' ' || c == '\t'):
			return i
		case c == '\\' && quote != '\'' && i+1 < len(s):
			i++
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		}
	}
	return i
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
