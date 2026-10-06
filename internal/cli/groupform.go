package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// groupAnswers is the form's state; group() and answersFrom() convert to
// and from the stored group, so editing prefills every field.
type groupAnswers struct {
	Name     string
	Runner   string // herdr, exec, manual, peer
	Kind     string // herdr agent kind
	Model    string // "" = the agent's default
	PermArgs []string
	Extra    string // further agent args, space-separated
	Prompt   string
	Cmd, Dir string // exec
	Notify   bool   // manual: herdr notification when a bead arrives
	Members  string // manual: comma-separated actors who may move beads on
	Peer     string // peer: the linked yard that works the group's beads
	Limit    string
	Other    map[string]any // config keys the form does not edit, kept as is
	// For goes into the titles of the agent questions when they are asked
	// outside the group form, which has named its group already.
	For string
}

func (a groupAnswers) group() store.Group {
	cfg := map[string]any{}
	for k, v := range a.Other {
		cfg[k] = v
	}
	switch a.Runner {
	case "herdr":
		cfg["kind"] = a.Kind
		if a.Model != "" {
			cfg["model"] = a.Model
		}
		if a.Prompt != "" {
			cfg["prompt"] = a.Prompt
		}
		args := append(slices.Clone(a.PermArgs), strings.Fields(a.Extra)...)
		if len(args) > 0 {
			list := make([]any, len(args))
			for i, s := range args {
				list[i] = s
			}
			cfg["args"] = list
		}
	case "exec":
		cfg["cmd"] = a.Cmd
		if a.Dir != "" {
			cfg["dir"] = a.Dir
		}
	case "manual":
		if a.Notify {
			cfg["notify"] = true
		}
	case store.PeerRunner:
		cfg[store.GroupPeer] = a.Peer
	}
	var members []string
	if a.Runner == "manual" {
		members = splitMembers(a.Members)
	}
	limit, _ := strconv.Atoi(a.Limit)
	return store.Group{Name: a.Name, Runner: a.Runner, Config: cfg, Limit: max(limit, 1), Members: members}
}

func answersFrom(g store.Group) groupAnswers {
	a := groupAnswers{Name: g.Name, Runner: g.Runner, Limit: strconv.Itoa(max(g.Limit, 1)), Other: map[string]any{}}
	str := func(k string) string { s, _ := g.Config[k].(string); return s }
	for k, v := range g.Config {
		switch k {
		case "kind", "model", "prompt", "args", "cmd", "dir", "notify":
		case store.GroupPeer:
			// The form's own only for a peer group; any other keeps the key.
			if g.Runner != store.PeerRunner {
				a.Other[k] = v
			}
		default:
			a.Other[k] = v
		}
	}
	a.Kind, a.Model, a.Prompt, a.Cmd, a.Dir = str("kind"), str("model"), str("prompt"), str("cmd"), str("dir")
	a.Notify = g.Config["notify"] == true || str("notify") == "true"
	a.Peer = g.AtPeer()
	a.Members = strings.Join(g.Members, ", ")
	var args []string
	if list, ok := g.Config["args"].([]any); ok {
		for _, v := range list {
			args = append(args, fmt.Sprint(v))
		}
	}
	// A --model inside args becomes the model field.
	for i := 0; i < len(args); i++ {
		if args[i] == "--model" && i+1 < len(args) {
			if a.Model == "" {
				a.Model = args[i+1]
			}
			args = slices.Delete(args, i, i+2)
			i--
		}
	}
	// A preset's permission flags, if present, become the permission field.
	if p, ok := agents.Lookup(a.Kind); ok {
		for _, perm := range p.Permissions {
			if i := indexOf(args, perm.Args); i >= 0 {
				a.PermArgs = perm.Args
				args = slices.Delete(args, i, i+len(perm.Args))
				break
			}
		}
	}
	a.Extra = strings.Join(args, " ")
	return a
}

// indexOf finds sub as a contiguous run in s.
func indexOf(s, sub []string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if slices.Equal(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

const (
	newGroup   = "\x00new"
	custom     = "\x00custom"
	otherKind  = "\x00other"
	noPerm     = "\x00none"
	customPerm = "\x00customperm"
)

// groupForm walks through creating or editing a group interactively.
func (a *app) groupForm(ctx context.Context, st store.API) error {
	groups, err := st.Groups(ctx)
	if err != nil {
		return err
	}
	ans := groupAnswers{Runner: "herdr", Kind: or(defaultKind(ctx, st), "claude"), Limit: "1", Other: map[string]any{}}
	editing := false

	if len(groups) > 0 {
		pick := newGroup
		opts := []huh.Option[string]{huh.NewOption("+ New group", newGroup)}
		for _, g := range groups {
			opts = append(opts, huh.NewOption(g.Name+"  ("+describe(g)+")", g.Name))
		}
		if err := ask(huh.NewSelect[string]().Title("Group").Options(opts...).Value(&pick)); err != nil {
			return err
		}
		if pick != newGroup {
			for _, g := range groups {
				if g.Name == pick {
					ans, editing = answersFrom(g), true
				}
			}
		}
	}
	g, err := a.askGroup(ctx, st, groups, ans, editing, nil)
	if err != nil || g == nil {
		return err
	}
	if err := st.PutGroup(ctx, *g); err != nil {
		return err
	}
	fmt.Printf("group %s saved (%s)\n", g.Name, describe(*g))
	return nil
}

// askGroup asks the group form's questions, each starting at what ans holds,
// and returns the group to save: nil when the last page, "Save this group?",
// was answered no. It saves nothing itself. editing is a group that exists,
// whose name is not asked again. note, when given, adds lines to the last
// page for the group as answered (the guided setup: its command line).
func (a *app) askGroup(ctx context.Context, st store.API, groups []store.Group, ans groupAnswers, editing bool, note func(store.Group) string) (*store.Group, error) {
	if !editing {
		if err := ask(huh.NewInput().Title("Name").Placeholder("webapp/builders").Value(&ans.Name).
			Validate(func(s string) error {
				switch {
				case !groupName.MatchString(s):
					return errors.New("letters, digits, '.', '_', '-', '/'")
				case slices.ContainsFunc(groups, func(g store.Group) bool { return g.Name == s }):
					return errors.New("a group with that name exists; pick it from the list instead")
				}
				return nil
			})); err != nil {
			return nil, err
		}
	}
	oldRunner := ans.Runner

	if err := ask(huh.NewSelect[string]().Title("Runner").Value(&ans.Runner).Options(
		huh.NewOption("herdr   — a coding agent in its own worktree", "herdr"),
		huh.NewOption("exec    — a shell command (checks, merges)", "exec"),
		huh.NewOption("manual  — a human; nothing is started", "manual"),
		huh.NewOption("peer    — a linked yard works the bead and returns its outcome", store.PeerRunner),
	)); err != nil {
		return nil, err
	}

	switch ans.Runner {
	case "herdr":
		if err := a.agentFields(ctx, &ans); err != nil {
			return nil, err
		}
	case "exec":
		if err := ask(
			huh.NewText().Title("Command").Description("Runs with sh -c in the rig, with $YARDR_BEAD set. It should advance or close the bead.").
				Value(&ans.Cmd).Validate(nonEmpty),
			huh.NewInput().Title("Directory").Description("Optional; defaults to the bead's rig.").Value(&ans.Dir),
		); err != nil {
			return nil, err
		}
	case "manual":
		if err := ask(huh.NewConfirm().Title("Notify in herdr when a bead arrives?").
			Description("Once per bead and stage. Leave off for placeholder stages such as backlog.").
			Value(&ans.Notify),
			huh.NewInput().Title("Members").Placeholder("mckean, mayor").
				Description("Optional, comma-separated actors (YARDR_ACTOR). If set, only they may advance beads out of this group's stages.").
				Value(&ans.Members)); err != nil {
			return nil, err
		}
	case store.PeerRunner:
		if err := ask(huh.NewInput().Title("Yard").
			Description("The linked yard, by its name in yardr peer list. Its link must carry bead out: yardr peer allow <name> --send bead.").
			Value(&ans.Peer).Validate(nonEmpty)); err != nil {
			return nil, err
		}
	}
	if err := ask(huh.NewInput().Title("Limit").Description("How many beads this group works at once.").
		Value(&ans.Limit).Validate(func(s string) error {
		if n, err := strconv.Atoi(s); err != nil || n < 1 {
			return errors.New("a whole number, at least 1")
		}
		return nil
	})); err != nil {
		return nil, err
	}

	g := ans.group()
	summary := fmt.Sprintf("%s: %s, limit %d\n%s", g.Name, g.Runner, g.Limit, fmtConfig(g.Config))
	if editing && oldRunner != ans.Runner {
		if live, _ := st.Sessions(ctx, false); slices.ContainsFunc(live, func(s store.Session) bool { return s.Group == g.Name }) {
			summary += fmt.Sprintf("\n\nThis group has running sessions; they keep their %s runner. "+
				"`yardr bead release <bead>` hands one to the new runner.", oldRunner)
		}
	}
	if note != nil {
		summary += "\n\n" + note(g)
	}
	save := true
	if err := ask(huh.NewConfirm().Title("Save this group?").Description(summary).Value(&save)); err != nil {
		return nil, err
	}
	if !save {
		fmt.Println("not saved")
		return nil, nil
	}
	return &g, nil
}

// agentFields asks for the agent kind, model, permissions, prompt and
// extra arguments of a herdr group.
func (a *app) agentFields(ctx context.Context, ans *groupAnswers) error {
	if err := a.agentKindModel(ctx, ans); err != nil {
		return err
	}
	if err := a.agentPermissions(ans); err != nil {
		return err
	}
	return ask(
		huh.NewInput().Title("Prompt file").Description("Optional; roles/builder.md reads the rig's .yardr/roles/builder.md, else ~/.yardr/roles/.").
			Value(&ans.Prompt),
		huh.NewInput().Title("Extra agent arguments").Description("Optional, space-separated.").Value(&ans.Extra),
	)
}

// agentPermissions asks which of the agent's own permission flags it runs
// with, ans.PermArgs preselected: the list a pack's import asks from too.
func (a *app) agentPermissions(ans *groupAnswers) error {
	preset, _ := agents.Lookup(ans.Kind)
	permPick := noPerm
	popts := []huh.Option[string]{huh.NewOption("None — the agent asks as usual", noPerm)}
	for _, p := range preset.Permissions {
		popts = append(popts, huh.NewOption(p.Label+" — "+p.Note, strings.Join(p.Args, " ")))
		if slices.Equal(p.Args, ans.PermArgs) {
			permPick = strings.Join(p.Args, " ")
		}
	}
	popts = append(popts, huh.NewOption("Custom flags…", customPerm))
	if err := ask(huh.NewSelect[string]().Title("Permissions" + ans.For).
		Description("What the agent may do without asking. Unattended agents stop at every prompt.").
		Options(popts...).Value(&permPick)); err != nil {
		return err
	}
	switch permPick {
	case noPerm:
		ans.PermArgs = nil
	case customPerm:
		flags := strings.Join(ans.PermArgs, " ")
		if err := ask(huh.NewInput().Title("Permission flags").Value(&flags)); err != nil {
			return err
		}
		ans.PermArgs = strings.Fields(flags)
	default:
		ans.PermArgs = strings.Fields(permPick)
	}
	return nil
}

// agentKindModel asks for an agent kind and, from the list the installed
// agent gives, its model: the binding a pack's import asks for too.
func (a *app) agentKindModel(ctx context.Context, ans *groupAnswers) error {
	kindPick := ans.Kind
	if _, ok := agents.Lookup(ans.Kind); !ok && ans.Kind != "" {
		kindPick = otherKind
	}
	opts := []huh.Option[string]{}
	for _, p := range agents.Presets {
		opts = append(opts, huh.NewOption(p.Label, p.Kind))
	}
	opts = append(opts, huh.NewOption("Other herdr agent…", otherKind))
	if err := ask(huh.NewSelect[string]().Title("Agent" + ans.For).Options(opts...).Value(&kindPick)); err != nil {
		return err
	}
	if kindPick == otherKind {
		other := []huh.Option[string]{}
		for _, k := range agents.HerdrKinds {
			if _, ok := agents.Lookup(k); !ok {
				other = append(other, huh.NewOption(k, k))
			}
		}
		kindPick = ans.Kind
		if err := ask(huh.NewSelect[string]().Title("Agent kind").Options(other...).
			Value(&kindPick)); err != nil {
			return err
		}
	}
	if kindPick != ans.Kind {
		ans.Model, ans.PermArgs = "", nil // a new agent: its own models and flags
	}
	ans.Kind = kindPick

	// Model: read live from the installed agent.
	models, listErr := listModels(ctx, ans.Kind)
	modelPick := ans.Model
	mopts := []huh.Option[string]{huh.NewOption("Agent's default", "")}
	for _, m := range models {
		mopts = append(mopts, huh.NewOption(m, m))
	}
	if ans.Model != "" && !slices.Contains(models, ans.Model) {
		mopts = append(mopts, huh.NewOption(ans.Model+" (current)", ans.Model))
	}
	mopts = append(mopts, huh.NewOption("Custom…", custom))
	desc := fmt.Sprintf("%d from %s; press / to search. A bead's --meta model=… overrides this.", len(models), ans.Kind)
	if listErr != nil || len(models) == 0 {
		desc = fmt.Sprintf("Could not list %s's models (%v); pick the default or type one.", ans.Kind, or(fmt.Sprint(listErr), "none listed"))
	}
	if err := ask(huh.NewSelect[string]().Title("Model" + ans.For).Description(desc).Options(mopts...).
		Height(min(len(mopts)+2, 14)).Value(&modelPick)); err != nil {
		return err
	}
	if modelPick == custom {
		modelPick = ans.Model
		if err := ask(huh.NewInput().Title("Model").Description("Passed as --model.").Value(&modelPick).
			Validate(nonEmpty)); err != nil {
			return err
		}
	}
	ans.Model = modelPick
	return nil
}

// listModels asks the installed agent of kind for its models, behind a
// spinner: it runs the agent. Tests replace it.
var listModels = func(ctx context.Context, kind string) (models []string, err error) {
	preset, _ := agents.Lookup(kind)
	_ = spinner.New().Title("Asking " + kind + " for its models…").
		Action(func() { models, err = preset.Models(ctx) }).Run()
	return models, err
}

// ask runs fields as one form page. Tests replace it to answer forms.
var ask = func(fields ...huh.Field) error {
	return huh.NewForm(huh.NewGroup(fields...)).Run()
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	return nil
}

// splitMembers reads a comma- or space-separated member list; none is nil.
func splitMembers(s string) []string {
	if out := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }); len(out) > 0 {
		return out
	}
	return nil
}

// describe is a one-line summary of a group for lists.
func describe(g store.Group) string {
	parts := []string{g.Runner}
	if k, _ := g.Config["kind"].(string); k != "" {
		parts = append(parts, k)
	}
	if m, _ := g.Config["model"].(string); m != "" {
		parts = append(parts, m)
	}
	if yard := g.AtPeer(); yard != "" {
		parts = append(parts, "yard "+yard)
	}
	if len(g.Members) > 0 {
		parts = append(parts, "members "+strings.Join(g.Members, ","))
	}
	for _, sub := range g.Subscriptions {
		parts = append(parts, "subscribed "+sub.String())
	}
	return strings.Join(parts, " · ") + fmt.Sprintf(" · limit %d", g.Limit)
}
