package pack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// What a plan says of one object.
const (
	Create    = "create"
	Change    = "change"
	Unchanged = "unchanged"
	// NotInPack is an object of the yard the pack does not carry: an
	// import leaves it alone.
	NotInPack = "not_in_pack"
	// Dropped is an object that came from this pack in an earlier import
	// and is no longer in it: an import reports it, and removes it only
	// when told to prune.
	Dropped = "dropped"
)

// Options say where a pack would be imported.
type Options struct {
	Rig string
	// Home is the yard's home; the pack's files would go under
	// <Home>/packs/<pack>.
	Home string
	// Flow names the pack's flow the rig would follow; "" is the first
	// flow without a type.
	Flow string
	// Residents says which resident of the yard takes each of the pack's:
	// pack name -> yard name.
	Residents map[string]string
	// RigRoles leaves a group or resident its rig's own role where the rig
	// has one of that name; PackRoles sets the pack's role everywhere, which
	// is the default. With neither, the choice is the last import's.
	RigRoles  bool
	PackRoles bool
	// Operator is who the pack's roles and stage files name where they say
	// {{operator}}; "" is the yard's operator setting.
	Operator string
	// HookAddr is YARDR_HOOK_ADDR as this process sees it. The server's
	// own value is not known to its clients.
	HookAddr string
}

// Plan is what importing a pack into a rig of a yard would do, object by
// object and in the order an import would do it: files, groups, flows,
// pointers, routes, residents, subscriptions. Each object carries the state
// the import would leave it in, so applying a plan needs no second look at
// the pack; what the yard must still be asked is under Questions, by the id
// the object refers to.
type Plan struct {
	Pack string `json:"pack"`
	Dir  string `json:"dir"`
	Rig  string `json:"rig"`
	// Flow is the pack's flow the rig would follow, by the pack's name.
	Flow string `json:"flow,omitempty"`
	// RigRoles says a rig's own role is kept where it has one, instead of
	// the pack's being set.
	RigRoles bool `json:"rig_roles"`
	// Groups and Residents map the pack's names to the yard's.
	Groups    map[string]string `json:"groups"`
	Residents map[string]string `json:"residents"`
	// Fill is what the import puts where a role or stage file of the pack
	// has a placeholder: operator -> the yard's operator, and each of the
	// pack's residents -> the yard's resident that takes it.
	Fill map[string]string `json:"fill"`
	// Install is where the pack's roles and scripts would be copied.
	Install string `json:"install"`
	// StageFiles is where the yard keeps the stage files of its store flows,
	// <home>/flows: the pack's go to <StageFiles>/<pack>.<flow>/<stage>.md.
	StageFiles string     `json:"stage_files"`
	Objects    []Object   `json:"objects"`
	Questions  []Question `json:"questions"`
	// Governs says which flow would govern the rig's beads afterwards.
	Governs []Governs `json:"governs"`
	// Sources says, for each outside source a resident would subscribe to,
	// what the yard has and lacks to hear it and answer in it.
	Sources []SourceNeeds `json:"sources"`
	Notes   []string      `json:"notes"`
	// Refused is why this plan must not be applied as it is; empty when it
	// may be.
	Refused []string `json:"refused"`
}

// Object is one thing in the yard and what the import would do to it. One
// of File, Group, Flow, Pointer, Route, Resident and Subscription is set,
// by Kind.
type Object struct {
	Kind    string   `json:"kind"` // file group flow pointer route resident subscription
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Changes []Diff   `json:"changes,omitempty"`
	Asks    []string `json:"asks,omitempty"` // question ids
	// Keeps says what the import leaves as the yard has it though the pack
	// carries it, and why: a prompt, where the rig has its own role and the
	// import was told to keep those.
	Keeps []string `json:"keeps,omitempty"`
	// Shadows are the rig's files that are not used while the object is as
	// the pack sets it: the rig's own role, under a prompt from the pack.
	Shadows []string `json:"shadows,omitempty"`
	// Edited says an installed file was changed by hand since the last
	// import and this one would overwrite it.
	Edited bool `json:"edited,omitempty"`
	// Of a dropped object: Owned is the earlier import's record of it, and
	// Held, when set, why not even a prune removes it.
	Owned *store.PackObject `json:"owned,omitempty"`
	Held  string            `json:"held,omitempty"`

	File         *FileOp            `json:"file,omitempty"`
	Group        *store.Group       `json:"group,omitempty"`
	Flow         *store.Flow        `json:"flow,omitempty"`
	Pointer      *store.FlowPointer `json:"pointer,omitempty"`
	Route        *RouteOp           `json:"route,omitempty"`
	Resident     *ResidentOp        `json:"resident,omitempty"`
	Subscription *SubscriptionOp    `json:"subscription,omitempty"`
}

// Diff is one field of an object an import would change.
type Diff struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}

// FileOp copies a pack's file under the yard's home. Flow and Stage are set
// on a stage file: the store flow it belongs to, where that flow's stage
// files are read from, and the stage it is named after there. Fill says the
// copy has the plan's names in place of the file's placeholders, as a role
// and a stage file do; SHA256 is of the copy, so of the filled text then.
type FileOp struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Mode   string `json:"mode"` // 0644, or 0755 for a script
	SHA256 string `json:"sha256"`
	Fill   bool   `json:"fill,omitempty"`
	Flow   string `json:"flow,omitempty"`
	Stage  string `json:"stage,omitempty"`
}

// RouteOp is the route state a rig's stage (and type) would have. The store
// mints an id per added route, so a route that changes is added anew and
// the ones it stands in for, listed under Delete, are removed. An empty
// Group with Delete means the pack leaves that stage without a route.
type RouteOp struct {
	Stage    string   `json:"stage"`
	Rig      string   `json:"rig"`
	Type     string   `json:"type,omitempty"`
	Group    string   `json:"group"`
	Priority int      `json:"priority"`
	Delete   []string `json:"delete,omitempty"`
}

// ResidentOp is what the pack sets on a resident: its role and the event
// kinds it hears. Everything else of the resident is the yard's.
type ResidentOp struct {
	Pack     string   `json:"pack"`     // the pack's name for it
	Resident string   `json:"resident"` // the yard's
	Prompt   string   `json:"prompt,omitempty"`
	On       []string `json:"on"`
	// Restart says the resident is running and its role or what it hears
	// would change: an import does not restart it.
	Restart bool `json:"restart,omitempty"`
}

// SubscriptionOp subscribes a resident; Remove is the subscription it
// replaces, when a source's scope changes.
type SubscriptionOp struct {
	Resident string              `json:"resident"`
	Add      *store.Subscription `json:"add,omitempty"`
	Remove   *store.Subscription `json:"remove,omitempty"`
}

// Question is something an import must be told, since a pack names no
// agent, model, permission arguments, person or directory.
type Question struct {
	ID      string `json:"id"`
	Object  string `json:"object"` // "<kind> <name>"
	Field   string `json:"field"`
	Ask     string `json:"ask"`
	Default string `json:"default,omitempty"`
}

// Governs is the flow the rig's beads (of Type, when set) would follow
// once the plan is applied, and the one they follow now.
type Governs struct {
	Type   string `json:"type,omitempty"`
	Flow   string `json:"flow"`
	Source string `json:"source"`
	Now    string `json:"now"`
	InPack bool   `json:"in_pack"`
}

// SourceNeeds is what a resident's subscription to a source takes.
type SourceNeeds struct {
	Source   string `json:"source"`
	Resident string `json:"resident"`
	Needs    []Need `json:"needs"`
}

// Need is one of them: ok, missing, or unknown when this process cannot
// tell.
type Need struct {
	What   string `json:"what"` // hook secret_file identity credential listener
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// credentialKeys are the names a source's reply credential goes by in a
// resident's env_file (see hooks: each adapter's Credential).
var credentialKeys = map[string]string{"linear": "LINEAR_API_KEY", "github": "GITHUB_TOKEN"}

// FlowName is the name a pack's flow has in the store. Flow names are the
// yard's, shared by its rigs, so the pack's name goes in front: two packs
// may each have a flow called default.
func FlowName(pack, flow string) string { return pack + "." + flow }

// FlowQuestion is the id of the question a plan asks when the pack has
// several flows a rig could follow and nothing says which.
const FlowQuestion = "pack.flow"

// GroupName is the name a pack's group has in the yard: agent and script
// groups are the rig's, people groups the yard's.
func GroupName(rig string, g Group) string {
	if g.People {
		return g.Name
	}
	return rig + "-" + g.Name
}

type planner struct {
	ctx  context.Context
	st   store.API
	p    *Pack
	o    Options
	plan *Plan
	rig  *store.Rig
	// rec is the record of this pack's last import into the rig, nil when
	// there was none; all are the records of every import.
	rec *store.PackImport
	all []store.PackImport
	// told are the pack's residents whose taker a flag or the last import
	// named; the others go by their own name.
	told map[string]bool
}

// Build plans the import of p. It only reads: the store, the pack and the
// files under the yard's home.
func Build(ctx context.Context, st store.API, p *Pack, o Options) (*Plan, error) {
	rig, err := st.GetRig(ctx, o.Rig)
	if err != nil {
		return nil, err
	}
	b := &planner{ctx: ctx, st: st, p: p, o: o, rig: rig, plan: &Plan{
		Pack: p.Name, Dir: p.Dir, Rig: o.Rig, Install: filepath.Join(o.Home, "packs", p.Name), StageFiles: filepath.Join(o.Home, "flows"),
		Groups: map[string]string{}, Residents: map[string]string{}, Fill: map[string]string{},
		Objects: []Object{}, Questions: []Question{}, Governs: []Governs{}, Sources: []SourceNeeds{}, Notes: []string{}, Refused: []string{},
	}}
	if b.all, err = st.Packs(ctx); err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(b.all, func(r store.PackImport) bool { return r.Pack == p.Name && r.Rig == o.Rig }); i >= 0 {
		b.rec = &b.all[i]
	}
	if o.RigRoles && o.PackRoles {
		return nil, errors.New("--rig-roles and --pack-roles are opposite choices: give one")
	}
	// Whose roles, as at the last import unless told anew.
	b.plan.RigRoles = o.RigRoles || (!o.PackRoles && b.rec != nil && b.rec.RigRoles)
	for name := range o.Residents {
		if !slices.ContainsFunc(p.Residents, func(r Resident) bool { return r.Name == name }) {
			return nil, fmt.Errorf("--resident %s=%s: the pack has no resident %s", name, o.Residents[name], name)
		}
	}
	flows, err := b.chosen()
	if err != nil {
		return nil, err
	}
	if err := b.names(); err != nil {
		return nil, err
	}
	for _, step := range []func() error{
		func() error { return b.files(flows) },
		b.groups,
		func() error { return b.flows(flows) },
		func() error { return b.routes(flows) },
		b.residents,
		b.dropped,
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.plan, nil
}

// names settles which resident of the yard takes each of the pack's and who
// the operator is, before the files: a role or stage file is installed with
// those names in it.
func (b *planner) names() error {
	b.told = map[string]bool{}
	for _, pr := range b.p.Residents {
		name, told := b.o.Residents[pr.Name]
		if !told && b.rec != nil {
			// Whoever took it at the last import, unless told anew.
			name, told = b.rec.Residents[pr.Name]
		}
		if !told {
			name = pr.Name
		}
		b.plan.Residents[pr.Name], b.told[pr.Name] = name, told
		b.plan.Fill[pr.Name] = name
	}
	operator := b.o.Operator
	if operator == "" {
		settings, err := b.st.Settings(b.ctx)
		if err != nil {
			return err
		}
		if operator = settings["operator"]; operator == "" {
			operator = "the operator"
		}
	}
	// Last: {{operator}} is the operator also in a pack with a resident of
	// that name.
	b.plan.Fill[Operator] = operator
	return nil
}

// installed is where a pack's file would be in the yard: an absolute path
// under the home, which no bead's branch can change.
func (b *planner) installed(rel string) string {
	return filepath.Join(b.plan.Install, filepath.FromSlash(rel))
}

func (b *planner) add(o Object) { b.plan.Objects = append(b.plan.Objects, o) }

func (b *planner) ask(o *Object, field, ask, def string) {
	q := Question{ID: o.Kind + "." + o.Name + "." + field, Object: o.Kind + " " + o.Name, Field: field, Ask: ask, Default: def}
	b.plan.Questions = append(b.plan.Questions, q)
	o.Asks = append(o.Asks, q.ID)
}

// action is create for an object the yard lacks, else change or unchanged
// by whether anything differs.
func action(exists bool, changes []Diff) string {
	switch {
	case !exists:
		return Create
	case len(changes) > 0:
		return Change
	}
	return Unchanged
}

// chosen are the pack's flows an import into the rig would use: the one
// the rig follows (Options.Flow, else the first without a type) and every
// flow for a type. Another flow a rig could follow is not imported: it has
// no rig to point at it, and its stages would get no routes.
func (b *planner) chosen() ([]Flow, error) {
	var out, own []Flow
	for _, f := range b.p.Flows {
		if f.Type == "" {
			own = append(own, f)
		}
	}
	if b.o.Flow != "" {
		i := slices.IndexFunc(own, func(f Flow) bool { return f.Name == b.o.Flow })
		if i < 0 {
			names := make([]string, len(own))
			for i, f := range own {
				names[i] = f.Name
			}
			return nil, fmt.Errorf("--flow %s: the pack has no such flow for a rig (it has: %s)", b.o.Flow, strings.Join(names, ", "))
		}
		own[0], own[i] = own[i], own[0]
	} else if i := slices.IndexFunc(own, func(f Flow) bool { return b.rec != nil && f.Name == b.rec.Flow }); i >= 0 {
		// The flow the rig was given by the last import, unless told anew.
		own[0], own[i] = own[i], own[0]
	} else if len(own) > 1 {
		names := make([]string, len(own))
		for i, f := range own {
			names[i] = f.Name
		}
		b.plan.Questions = append(b.plan.Questions, Question{ID: FlowQuestion, Object: "pack " + b.p.Name, Field: "flow",
			Ask: fmt.Sprintf("which of the pack's flows does rig %s follow (--flow <name>)? It has: %s.", b.o.Rig, strings.Join(names, ", ")), Default: own[0].Name})
	}
	if len(own) > 0 {
		b.plan.Flow = own[0].Name
		out = append(out, own[0])
		for _, f := range own[1:] {
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("flow %s of the pack is not imported: rig %s would follow %s (--flow %s picks this one)", f.Name, b.o.Rig, own[0].Name, f.Name))
		}
	}
	for _, f := range b.p.Flows {
		if f.Type != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// files are the pack's roles and scripts, copied under Install, and the
// stage files of the flows the import uses, copied to where the yard reads
// a store flow's: <home>/flows/<pack>.<flow>/<stage>.md.
func (b *planner) files(flows []Flow) error {
	want := map[string]bool{}
	for _, rel := range b.p.Files() {
		op := &FileOp{To: b.installed(rel), Fill: b.p.Filled(rel)}
		want[op.To] = true
		if err := b.file(rel, op); err != nil {
			return err
		}
	}
	// What an earlier import left there and this pack no longer names.
	b.otherFiles(b.plan.Install, "", want, "")
	for _, f := range flows {
		name := FlowName(b.p.Name, f.Name)
		dir := store.HomeStageDir(b.o.Home, name)
		for _, s := range f.Stages {
			if s.File == "" {
				continue
			}
			op := &FileOp{To: filepath.Join(dir, s.Name+".md"), Flow: name, Stage: s.Name, Fill: b.p.Filled(s.File)}
			want[op.To] = true
			if err := b.file(s.File, op); err != nil {
				return err
			}
		}
		// The flow's other stage files: written by hand for a stage the pack
		// has no file for, or left by an earlier import.
		b.otherFiles(dir, stageFileName(name, ""), want, name)
	}
	return nil
}

// stageFileName is what a plan calls a stage file of store flow that the
// pack does not carry: its path under the yard's home, since no path in the
// pack names it.
func stageFileName(flow, rel string) string {
	return "flows/" + flow + "/" + filepath.ToSlash(rel)
}

// file plans the copy of the pack's file rel to op.To. What is compared
// with the installed copy, and hashed for the record, is the text as it
// would be installed: filled, for a role or stage file. So an import just
// made shows no change, and a copy counts as edited by hand only when it
// differs from what an import wrote.
func (b *planner) file(rel string, op *FileOp) error {
	op.From = filepath.Join(b.p.Dir, filepath.FromSlash(rel))
	text, err := os.ReadFile(op.From)
	if err != nil {
		return err
	}
	if op.Fill {
		text = Fill(text, b.plan.Fill)
	}
	info, err := os.Stat(op.From)
	if err != nil {
		return err
	}
	op.Mode, op.SHA256 = "0644", sum(text)
	if info.Mode().Perm()&0o111 != 0 {
		op.Mode = "0755"
	}
	var changes []Diff
	have, err := os.ReadFile(op.To)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	edited := false
	if exists {
		if !bytes.Equal(have, text) {
			changes = append(changes, Diff{"content", "sha256 " + sum(have)[:12], "sha256 " + op.SHA256[:12]})
			// Not what the last import installed either: someone edited
			// the copy, and the import would undo that.
			was := b.installedSums(op.To)
			edited = len(was) > 0 && !slices.Contains(was, sum(have))
		}
		if hi, err := os.Stat(op.To); err == nil && (hi.Mode().Perm()&0o111 != 0) != (op.Mode == "0755") {
			changes = append(changes, Diff{"mode", fmt.Sprintf("%04o", hi.Mode().Perm()), op.Mode})
		}
	}
	b.add(Object{Kind: "file", Name: rel, Action: action(exists, changes), Changes: changes, File: op, Edited: edited})
	return nil
}

// otherFiles adds the files under dir that the import does not write, as not
// in the pack, named prefix and their path under dir; flow is the store flow
// whose stage files dir holds, "" for the pack's own directory.
func (b *planner) otherFiles(dir, prefix string, want map[string]bool, flow string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || want[path] {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		b.add(Object{Kind: "file", Name: prefix + filepath.ToSlash(rel), Action: NotInPack, File: &FileOp{To: path, Flow: flow}})
		return nil
	})
}

// stageFlow names the store flow whose stage file path is, when path is
// where an import of this pack puts one: <StageFiles>/<pack>.<flow>/<stage>.md.
func (p *Plan) stageFlow(path string) (flow string, ok bool) {
	if p.StageFiles == "" {
		return "", false
	}
	rel, err := filepath.Rel(p.StageFiles, path)
	if err != nil || !filepath.IsLocal(rel) || filepath.Ext(rel) != ".md" {
		return "", false
	}
	flow = filepath.Dir(rel)
	if flow != filepath.Base(flow) || !strings.HasPrefix(flow, p.Pack+".") {
		return "", false
	}
	return flow, true
}

// Removable reports whether path is one a prune may remove: a file under
// the pack's own directory in the home, or a stage file of one of its
// flows. The record says which files an import brought; this says where an
// import could have put one, so a record naming a file elsewhere removes
// nothing.
func (p *Plan) Removable(path string) bool {
	if rel, err := filepath.Rel(p.Install, path); err == nil && filepath.IsLocal(rel) {
		return true
	}
	_, ok := p.stageFlow(path)
	return ok
}

// installedSums are the contents the imports of this pack recorded for the
// installed file to, one per rig it was imported into: the rigs share the
// copy, so the last import into any of them may have written it. Empty when
// no record says, as those older than the field do not.
func (b *planner) installedSums(to string) []string {
	var out []string
	for _, r := range b.all {
		if r.Pack != b.p.Name {
			continue
		}
		for _, po := range r.Objects {
			if po.Kind == "file" && po.Name == to && po.SHA256 != "" {
				out = append(out, po.SHA256)
			}
		}
	}
	return out
}

// EditedFiles are the installed files the plan would overwrite though they
// were edited by hand since the last import, each with why that is refused.
func (p *Plan) EditedFiles() []string {
	var out []string
	for _, o := range p.Objects {
		if o.Kind == "file" && o.Edited && o.File != nil {
			verb := "overwrite"
			if o.Action == Dropped {
				verb = "remove"
			}
			out = append(out, fmt.Sprintf("%s was edited by hand since the last import, and the import would %s it: the edit belongs in the pack's source (%s)", o.File.To, verb, o.File.From))
		}
	}
	return out
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
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
		name := GroupName(b.o.Rig, g)
		named[name] = true
		b.plan.Groups[g.Name] = name
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
		var field string
		switch {
		case g.Agent != "":
			want.Runner, field = "herdr", "prompt"
			want.Config[field] = b.installed(g.Agent)
			own, err := b.rigRole(g.Agent)
			if err != nil {
				return err
			}
			switch {
			case own == "":
			case b.plan.RigRoles:
				// Told to keep the rig's own role: an existing prompt stays,
				// and a new group, or one the pack's copy was set on, gets
				// the role by the name the rig's file is found under.
				prompt, _ := old.Config[field].(string)
				if prompt == "" || prompt == b.installed(g.Agent) {
					prompt = g.Agent
				}
				want.Config[field] = prompt
				o.Keeps = append(o.Keeps, fmt.Sprintf("prompt %s: the rig has its own %s", prompt, own))
			default:
				o.Shadows = append(o.Shadows, own)
			}
			if !exists {
				b.ask(&o, "kind", "which agent runs group "+name+" (claude, codex, ...)?", settings["default_kind"])
				b.ask(&o, "model", "which model does group "+name+" run (empty: the agent's own default)?", "")
				b.ask(&o, "args", "which permission arguments does group "+name+"'s agent run with (empty: none, and it stops at every permission prompt)?", "")
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
		b.add(o)
	}
	// The rig's other groups, by the name an import gives a rig's groups.
	for _, h := range have {
		if !named[h.Name] && strings.HasPrefix(h.Name, b.o.Rig+"-") {
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

func (b *planner) flows(flows []Flow) error {
	have, err := b.st.Flows(b.ctx)
	if err != nil {
		return err
	}
	pointers, err := b.st.FlowPointers(b.ctx)
	if err != nil {
		return err
	}
	// The flows that govern the rig now. A checkout git cannot read has
	// none to show; the import does not need it, so it is a note.
	now := map[string]string{}
	rigFlows, err := b.st.RigFlows(b.ctx, b.o.Rig)
	if err != nil {
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("the flows governing rig %s now could not be read: %v", b.o.Rig, err))
	}
	for _, rf := range rigFlows {
		now[rf.Type] = rf.Flow.Name + " (" + rf.Source.String() + ")"
	}

	want := map[string]store.Flow{} // by store name
	after := slices.Clone(pointers)
	var ptrObjects []Object
	for _, f := range flows {
		sf := f.StoreFlow(FlowName(b.p.Name, f.Name))
		want[sf.Name] = sf
		var changes []Diff
		i := slices.IndexFunc(have, func(h store.Flow) bool { return h.Name == sf.Name })
		if i >= 0 {
			changes = flowDiff(have[i], sf)
		}
		b.add(Object{Kind: "flow", Name: sf.Name, Action: action(i >= 0, changes), Changes: changes, Flow: &sf})

		ptr := store.FlowPointer{Rig: b.o.Rig, Type: f.Type, Flow: sf.Name}
		changes = nil
		j := slices.IndexFunc(after, func(p store.FlowPointer) bool { return p.Rig == ptr.Rig && p.Type == ptr.Type })
		if j >= 0 {
			if after[j].Flow != ptr.Flow {
				changes = []Diff{{"flow", after[j].Flow, ptr.Flow}}
			}
			after[j] = ptr
		} else {
			after = append(after, ptr)
		}
		ptrObjects = append(ptrObjects, Object{Kind: "pointer", Name: ptr.String() + " -> " + ptr.Flow, Action: action(j >= 0, changes), Changes: changes, Pointer: &ptr})
		g := Governs{Type: f.Type, Flow: sf.Name, Source: "store", Now: now[f.Type], InPack: true}
		if g.Now == "" {
			g.Now = "none"
			if f.Type != "" {
				g.Now = "the rig's flow"
			}
		}
		b.plan.Governs = append(b.plan.Governs, g)
	}
	for _, o := range ptrObjects {
		b.add(o)
	}
	for _, p := range pointers {
		if p.Rig != b.o.Rig || slices.ContainsFunc(flows, func(f Flow) bool { return f.Type == p.Type }) {
			continue
		}
		b.add(Object{Kind: "pointer", Name: p.String() + " -> " + p.Flow, Action: NotInPack, Pointer: &p})
		b.plan.Governs = append(b.plan.Governs, Governs{Type: p.Type, Flow: p.Flow, Source: "store", Now: now[p.Type]})
	}
	// A rig pointer in the store wins over the repository as a whole: its
	// default.flow and its type files stop governing, though they stay.
	own := slices.IndexFunc(after, func(p store.FlowPointer) bool { return p.Rig == b.o.Rig && p.Type == "" })
	for _, rf := range rigFlows {
		if own < 0 || len(flows) == 0 || rf.Source.Kind != "repo" {
			continue
		}
		what := "the rig's beads"
		if rf.Type != "" {
			what = "beads of type " + rf.Type
			b.plan.Governs = append(b.plan.Governs, Governs{Type: rf.Type, Flow: after[own].Flow, Source: "store", Now: now[rf.Type]})
		}
		b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("rig %s's repository carries %s (%s), which governs %s now. The import points the rig at a flow in the store, and a store pointer wins over the repository's flow files: the file stays where it is and governs nothing until the pointer is cleared (yardr rig flow %s --clear).",
			b.o.Rig, rf.Source.Path, rf.Source.String(), what, b.o.Rig))
	}
	return b.strays(want, after)
}

// flowDiff compares a stored flow with the pack's, stage by stage in the
// line format a flow file is written in.
func flowDiff(old, new store.Flow) []Diff {
	var out []Diff
	if !slices.Equal(old.Stages, new.Stages) {
		out = append(out, Diff{"stages", strings.Join(old.Stages, ", "), strings.Join(new.Stages, ", ")})
	}
	line := func(f store.Flow, s string) string {
		switch {
		case f.IsTerminal(s):
			return "terminal"
		case !slices.Contains(f.Stages, s):
			return "no such stage"
		case len(f.Transitions[s]) == 0:
			return "no way out"
		}
		return "-> " + strings.Join(f.Edges(s), " | ")
	}
	stages := slices.Clone(new.Stages)
	for _, s := range old.Stages {
		if !slices.Contains(stages, s) {
			stages = append(stages, s)
		}
	}
	for _, s := range stages {
		if o, n := line(old, s), line(new, s); o != n {
			out = append(out, Diff{"stage " + s, o, n})
		}
	}
	return out
}

// strays refuses the plan while an open bead that would follow one of the
// pack's flows sits in a stage that flow lacks: the store refuses the
// pointer and the flow for the same reason (store.SetFlowPointer,
// store.PutFlow), and an import that stopped halfway there would leave the
// yard between two states. want are the pack's flows by store name, after
// the pointers as the import would leave them.
func (b *planner) strays(want map[string]store.Flow, after []store.FlowPointer) error {
	beads, err := b.st.ListBeads(b.ctx, store.BeadFilter{})
	if err != nil {
		return err
	}
	pointer := func(rig, typ string) string {
		for _, p := range after {
			if p.Rig == rig && p.Type == typ {
				return p.Flow
			}
		}
		return ""
	}
	lost := map[string][]string{}
	for _, bead := range beads {
		if bead.Status == "closed" || store.NotWork[bead.Type] {
			continue
		}
		name := store.PinnedFlow(&bead)
		if name == "" && bead.Rig != nil {
			if name = pointer(*bead.Rig, bead.Type); name == "" {
				name = pointer(*bead.Rig, "")
			}
		}
		if f, ok := want[name]; ok && !slices.Contains(f.Stages, bead.Stage) {
			lost[name] = append(lost[name], fmt.Sprintf("%s (%s)", bead.ID, bead.Stage))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(lost)) {
		b.plan.Refused = append(b.plan.Refused, fmt.Sprintf("flow %s lacks the stage of open beads that would follow it: %s; advance them to a stage the flow has, or pin them to the flow they follow now (yardr bead update <id> --meta flow=<name>)",
			name, strings.Join(lost[name], ", ")))
	}
	return nil
}

func (b *planner) routes(flows []Flow) error {
	have, err := b.st.Routes(b.ctx)
	if err != nil {
		return err
	}
	store.SortRoutes(have)
	taken := map[string]bool{}
	for _, f := range flows {
		for _, s := range f.Stages {
			if s.Group == "" {
				if !s.End {
					b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("stage %s of flow %s names no group: the pack routes it to nobody, and a bead there waits for a hand", s.Name, f.Name))
				}
				// A stage in the flow with no group still replaces the yard's
				// route for that rig, stage and type. Leaving that route in
				// place would make the stage look unassigned in the plan while
				// beads continued going to its old group.
				var old []store.Route
				for _, r := range have {
					if r.Stage == s.Name && r.Rig != nil && *r.Rig == b.o.Rig && r.Label == nil && deref(r.Type) == f.Type {
						old = append(old, r)
						taken[r.ID] = true
					}
				}
				if len(old) > 0 {
					name := "rig " + b.o.Rig + " stage " + s.Name
					if f.Type != "" {
						name += " type " + f.Type
					}
					var deletes []string
					for _, r := range old {
						deletes = append(deletes, r.ID)
					}
					changes := []Diff{{"group", old[0].Group, "(no route)"}}
					if len(old) > 1 {
						changes = append(changes, Diff{"routes", fmt.Sprintf("%d for this stage", len(old)), "0"})
					}
					b.add(Object{Kind: "route", Name: name + " -> (no route)", Action: Change, Changes: changes,
						Route: &RouteOp{Stage: s.Name, Rig: b.o.Rig, Type: f.Type, Delete: deletes}})
				}
				continue
			}
			g := b.p.Groups[slices.IndexFunc(b.p.Groups, func(g Group) bool { return g.Name == s.Group })]
			op := &RouteOp{Stage: s.Name, Rig: b.o.Rig, Type: f.Type, Group: GroupName(b.o.Rig, g)}
			// The rig's routes for the stage (and type), label routes
			// aside: those say something the pack does not.
			var old []store.Route
			for _, r := range have {
				if r.Stage == s.Name && r.Rig != nil && *r.Rig == b.o.Rig && r.Label == nil && deref(r.Type) == f.Type {
					old = append(old, r)
					taken[r.ID] = true
				}
			}
			var changes []Diff
			if len(old) > 0 {
				// The priority is the yard's: the pack carries none.
				op.Priority = old[0].Priority
				if old[0].Group != op.Group {
					changes = append(changes, Diff{"group", old[0].Group, op.Group})
				}
				if len(old) > 1 {
					changes = append(changes, Diff{"routes", fmt.Sprintf("%d for this stage", len(old)), "1"})
				}
				if len(changes) > 0 {
					for _, r := range old {
						op.Delete = append(op.Delete, r.ID)
					}
				}
			}
			name := "rig " + b.o.Rig + " stage " + s.Name
			if f.Type != "" {
				name += " type " + f.Type
			}
			b.add(Object{Kind: "route", Name: name + " -> " + op.Group, Action: action(len(old) > 0, changes), Changes: changes, Route: op})
		}
	}
	for _, r := range have {
		if r.Rig == nil || *r.Rig != b.o.Rig || taken[r.ID] {
			continue
		}
		name := "rig " + b.o.Rig + " stage " + r.Stage
		if r.Type != nil {
			name += " type " + *r.Type
		}
		if r.Label != nil {
			name += " label " + *r.Label
		}
		b.add(Object{Kind: "route", Name: name + " -> " + r.Group, Action: NotInPack,
			Route: &RouteOp{Stage: r.Stage, Rig: b.o.Rig, Type: deref(r.Type), Group: r.Group, Priority: r.Priority}})
	}
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (b *planner) residents() error {
	have, err := b.st.Residents(b.ctx)
	if err != nil {
		return err
	}
	settings, err := b.st.Settings(b.ctx)
	if err != nil {
		return err
	}
	var subs []Object
	for _, pr := range b.p.Residents {
		name, told := b.plan.Residents[pr.Name], b.told[pr.Name]
		i := slices.IndexFunc(have, func(r store.Resident) bool { return r.Name == name })
		exists := i >= 0
		var old store.Resident
		if exists {
			old = have[i]
		}
		op := &ResidentOp{Pack: pr.Name, Resident: name, On: slices.Clone(pr.Events)}
		if op.On == nil {
			op.On = []string{}
		}
		o := Object{Kind: "resident", Name: name, Resident: op}
		var changes []Diff
		if pr.Role != "" {
			op.Prompt = b.installed(pr.Role)
			prompt, _ := old.Config["prompt"].(string)
			own, err := b.rigRole(pr.Role)
			if err != nil {
				return err
			}
			switch {
			case own == "":
			case !b.plan.RigRoles:
				o.Shadows = append(o.Shadows, own)
			case prompt != "" && prompt != op.Prompt:
				// A resident is the yard's, not the rig's, so only one that
				// has a prompt of its own keeps it; a new one gets the pack's.
				op.Prompt = prompt
				o.Keeps = append(o.Keeps, fmt.Sprintf("prompt %s: the rig has its own %s", prompt, own))
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
				ask := "which resident of the yard takes the pack's " + pr.Name + " (--resident " + pr.Name + "=<name>)?"
				if len(names) > 0 {
					ask += " The yard has: " + strings.Join(names, ", ") + "."
				}
				b.ask(&o, "resident", ask, "a new resident "+name)
			}
			b.ask(&o, "kind", "which agent runs resident "+name+" (claude, codex, ...)?", settings["default_kind"])
			b.ask(&o, "model", "which model does resident "+name+" run (empty: the agent's own default)?", "")
			b.ask(&o, "args", "which permission arguments does resident "+name+"'s agent run with (empty: none, and it stops at every permission prompt)?", "")
			b.ask(&o, "dir", "which directory does resident "+name+" work in?", "")
		}
		o.Action, o.Changes = action(exists, changes), changes
		b.add(o)

		var want []store.Subscription
		for _, s := range pr.Stages {
			want = append(want, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"rig": b.o.Rig, "stage": s}})
		}
		if pr.Done {
			want = append(want, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"rig": b.o.Rig, "state": store.StateDone}})
		}
		for _, s := range pr.Subscribe {
			want = append(want, store.Subscription{Source: s.Name, Scope: s.Scope})
		}
		taken := make([]bool, len(old.Subscriptions))
		hears := len(changes) > 0
		for _, w := range want {
			so := Object{Kind: "subscription", Name: name + " " + w.String(), Action: Create, Subscription: &SubscriptionOp{Resident: name, Add: &w}}
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
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("resident %s is running and its role or what it hears changes: the import does not restart it, so restart it afterwards (yardr resident restart %s)", name, name))
		}
		for j, s := range old.Subscriptions {
			if !taken[j] {
				subs = append(subs, Object{Kind: "subscription", Name: name + " " + s.String(), Action: NotInPack, Subscription: &SubscriptionOp{Resident: name}})
			}
		}
		for _, s := range pr.Subscribe {
			needs, err := b.needs(s.Name, name, exists, old)
			if err != nil {
				return err
			}
			b.plan.Sources = append(b.plan.Sources, SourceNeeds{Source: s.Name, Resident: name, Needs: needs})
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

// needs is what hearing source, and answering in it, takes of the yard:
// the hook that receives its deliveries and the file holding that hook's
// secret, who the resident is in the source, the credential it answers
// with, and the server's hook listener. Only names are read, never a
// secret's value.
func (b *planner) needs(source, resident string, exists bool, r store.Resident) ([]Need, error) {
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
		out = append(out, Need{"identity", ok, "resident " + resident + " has an identity in " + source})
	} else {
		out = append(out, Need{"identity", missing, fmt.Sprintf("yardr resident identity %s --source %s --id <id> --key <key>", resident, source)})
	}
	if key, has := credentialKeys[source]; has {
		path, _ := r.Config["env_file"].(string)
		n := Need{"credential", missing, fmt.Sprintf("%s in resident %s's env_file, to answer in %s", key, resident, source)}
		switch {
		case !exists:
		case path == "":
			n.Detail += ": it has no env_file"
		default:
			f, err := config.ReadEnvFile(path)
			switch {
			case err != nil:
				n.Detail += ": " + err.Error()
			case slices.Contains(f.Keys(), key):
				n = Need{"credential", ok, key + " is set in " + f.Path}
			default:
				n.Detail += ": " + f.Path + " does not set it"
			}
		}
		out = append(out, n)
	}
	if b.o.HookAddr != "" {
		out = append(out, Need{"listener", ok, "YARDR_HOOK_ADDR=" + b.o.HookAddr + " here; the server listens if it was started with it"})
	} else {
		out = append(out, Need{"listener", unknown, "YARDR_HOOK_ADDR is not set here; the server receives hooks only if it was started with it"})
	}
	return out, nil
}

// rigRole names the rig's own role file that stands in for the pack's role
// rel (roles/builder.md -> .yardr/roles/builder.md), or "" when the rig has
// none or the pack's file is no role by yardr's naming (roles/<name>.md).
// The pack's role is set over it unless the import keeps the rig's roles.
func (b *planner) rigRole(rel string) (string, error) {
	dir, name := filepath.Split(filepath.FromSlash(rel))
	if filepath.Clean(dir) != "roles" || filepath.Ext(name) != ".md" {
		return "", nil
	}
	has, err := runner.RigRole(b.rig, name)
	if err != nil {
		return "", fmt.Errorf("rig %s: looking for its own role %s: %w", b.rig.Name, name, err)
	}
	if !has {
		return "", nil
	}
	return ".yardr/roles/" + name, nil
}

// Ident is the record of an object the pack carries: what a later import
// finds it by. ok is false for what is not the pack's to own: a resident,
// which stays the yard's, and an object the pack does not carry.
func Ident(o Object) (po store.PackObject, ok bool) {
	if o.Action == Dropped {
		if o.Owned == nil {
			return po, false
		}
		return *o.Owned, true
	}
	if o.Action == NotInPack {
		return po, false
	}
	switch {
	case o.Kind == "file" && o.File != nil:
		return store.PackObject{Kind: o.Kind, Name: o.File.To, SHA256: o.File.SHA256}, true
	case o.Kind == "group" || o.Kind == "flow":
		return store.PackObject{Kind: o.Kind, Name: o.Name}, true
	case o.Kind == "pointer" && o.Pointer != nil:
		return store.PackObject{Kind: o.Kind, Type: o.Pointer.Type, Flow: o.Pointer.Flow}, true
	case o.Kind == "route" && o.Route != nil:
		return store.PackObject{Kind: o.Kind, Stage: o.Route.Stage, Type: o.Route.Type, Group: o.Route.Group}, true
	case o.Kind == "subscription" && o.Subscription != nil && o.Subscription.Add != nil:
		return store.PackObject{Kind: o.Kind, Resident: o.Subscription.Resident, Subscription: o.Subscription.Add}, true
	}
	return po, false
}

// dropped adds what the last import of this pack into the rig brought and
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
	for pk, name := range b.rec.Residents {
		if now, ok := b.plan.Residents[pk]; !ok || now != name {
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("resident %s took the pack's %s at the last import and no longer does: it is the yard's, so its role and the event kinds it hears stay as that import left them", name, pk))
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
		file := &FileOp{To: po.Name, From: filepath.Join(b.p.Dir, rel)}
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
		// A people group is the yard's, shared by its rigs: it stays while
		// another import or another rig's route still uses it.
		for _, r := range b.all {
			if (r.Pack != b.p.Name || r.Rig != b.o.Rig) && slices.ContainsFunc(r.Objects, func(x store.PackObject) bool { return x.Kind == "group" && x.Name == po.Name }) {
				o.Held = fmt.Sprintf("pack %s in rig %s names it too", r.Pack, r.Rig)
			}
		}
		routes, err := b.st.Routes(b.ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			if o.Held == "" && r.Group == po.Name && (r.Rig == nil || *r.Rig != b.o.Rig) {
				o.Held = "a route outside rig " + b.o.Rig + " leads to it"
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
			if p.Flow == po.Name && p.Rig != b.o.Rig {
				o.Held = p.String() + " follows it"
			}
		}
		return o, nil
	case "pointer":
		ptrs, err := b.st.FlowPointers(b.ctx)
		if err != nil {
			return nil, err
		}
		p := store.FlowPointer{Rig: b.o.Rig, Type: po.Type, Flow: po.Flow}
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
		op := &RouteOp{Stage: po.Stage, Rig: b.o.Rig, Type: po.Type, Group: po.Group}
		for _, r := range routes {
			if r.Stage == po.Stage && r.Rig != nil && *r.Rig == b.o.Rig && r.Label == nil && deref(r.Type) == po.Type && r.Group == po.Group {
				op.Delete = append(op.Delete, r.ID)
				op.Priority = r.Priority
			}
		}
		if len(op.Delete) == 0 {
			return nil, nil
		}
		name := "rig " + b.o.Rig + " stage " + po.Stage
		if po.Type != "" {
			name += " type " + po.Type
		}
		return &Object{Kind: po.Kind, Name: name + " -> " + po.Group, Route: op}, nil
	case "subscription":
		if po.Subscription == nil {
			return nil, nil
		}
		r, err := b.st.GetResident(b.ctx, po.Resident)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		sub := *po.Subscription
		if !slices.ContainsFunc(r.Subscriptions, func(s store.Subscription) bool { return s.String() == sub.String() }) {
			return nil, nil
		}
		return &Object{Kind: po.Kind, Name: po.Resident + " " + sub.String(), Subscription: &SubscriptionOp{Resident: po.Resident, Remove: &sub}}, nil
	}
	return nil, nil
}

// strandedTypePointer keeps a dropped type-flow pointer when clearing
// it would make the type's open beads follow the rig's newly chosen default
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
		if bead.Status == "closed" || bead.Rig == nil || *bead.Rig != p.Rig || bead.Type != p.Type || store.PinnedFlow(&bead) != "" {
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
