package pack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/depotfile"
	"yardr.dev/yardr/internal/source"
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
	// Takes is a group of the yard that takes one of the pack's
	// (Options.Groups): the pack's routes lead to it, and an import changes
	// nothing of it and does not record it as the pack's.
	Takes = "takes"
)

// Options say where a pack would be imported.
type Options struct {
	Depot string
	// Home is the yard's home; the pack's files would go under
	// <Home>/packs/<pack>.
	Home string
	// Flow names the pack's flow the depot would follow; "" is the first
	// flow without a type.
	Flow string
	// Crew says which crew member of the yard takes each of the pack's:
	// pack name -> yard name.
	Crew map[string]string
	// Groups says which group of the yard takes each of the pack's, as it
	// is: pack name -> yard name. The name the import would give the pack's
	// group itself (GroupName) says none does, where the yard has no group
	// of that name of its own.
	Groups map[string]string
	// Bound names the pack's groups the caller binds itself (--bind): a group
	// that binds as another (Group.BindsAs) is asked about, as any new agent
	// group is, instead of taking the other's agent, model and arguments.
	Bound []string
	// DepotRoles leaves a group or crew member its depot's own role where the depot
	// has one of that name; PackRoles sets the pack's role everywhere, which
	// is the default. With neither, the choice is the last import's.
	DepotRoles bool
	PackRoles  bool
	// Operator is who the pack's roles and stage files name where they say
	// {{operator}}; "" is the yard's operator setting.
	Operator string
}

// Plan is what importing a pack into a depot of a yard would do, object by
// object and in the order an import would do it: files, groups, flows,
// pointers, routes, crew members, subscriptions, stage maps, declared
// sources. Each object carries the state
// the import would leave it in, so applying a plan needs no second look at
// the pack; what the yard must still be asked is under Questions, by the id
// the object refers to.
type Plan struct {
	Pack string `json:"pack"`
	Dir  string `json:"dir"`
	// Kind is the kind of depot the pack says it is for, "" when it says
	// none.
	Kind  string `json:"kind,omitempty"`
	Depot string `json:"depot"`
	// src is the pack's directory, which Apply reads the files from again.
	src fs.FS
	// Flow is the pack's flow the depot would follow, by the pack's name.
	Flow string `json:"flow,omitempty"`
	// DepotRoles says a depot's own role is kept where it has one, instead of
	// the pack's being set.
	DepotRoles bool `json:"depot_roles"`
	// Groups and Crew members map the pack's names to the yard's.
	Groups map[string]string `json:"groups"`
	Crew   map[string]string `json:"crew"`
	// Fill is what the import puts where a role or stage file of the pack
	// has a placeholder: operator -> the yard's operator, and each of the
	// pack's crew -> the yard's crew member that takes it.
	Fill map[string]string `json:"fill"`
	// Home is the yard's home: Install and StageFiles are under it, and a
	// declared source's command files go under <Home>/sources/<source>/.
	Home string `json:"home"`
	// Install is where the pack's roles and scripts would be copied.
	Install string `json:"install"`
	// StageFiles is where the yard keeps the stage files of its store flows,
	// <home>/flows: the pack's go to <StageFiles>/<pack>.<flow>/<stage>.md.
	StageFiles string     `json:"stage_files"`
	Objects    []Object   `json:"objects"`
	Questions  []Question `json:"questions"`
	// Governs says which flow would govern the depot's beads afterwards.
	Governs []Governs `json:"governs"`
	// Sources says, for each outside source a crew member would subscribe to,
	// what the yard has and lacks to hear it and answer in it.
	Sources []SourceNeeds `json:"sources"`
	// OpenThreads says, for each crew member the pack asks it for, which
	// sources it would be let open threads in and what an import does about
	// each: nothing, ask the operator, or neither ask nor grant.
	OpenThreads []OpenThreads `json:"open_threads"`
	// Maps says, for each source whose issues hear the pack's flows, the map,
	// the yard's crew member it is acted on as, and what the yard has and lacks
	// for it. A map the import would set is among Objects too, as kind map.
	Maps []MapPlan `json:"maps"`
	// Optional names the sources the pack could use that the yard has
	// nothing of: no source of the name and no hook of it. The import goes
	// through without them, so the plan says each here once, with the
	// command that sets it up, instead of a missing need on every step that
	// could use it. The steps still carry their needs as data.
	Optional []OptionalSource `json:"optional"`
	// Declared says, for each source the pack brings by a declaration, what
	// it declares and what the yard must still be told for it. Each is among
	// Objects too, as kind source.
	Declared []DeclaredPlan `json:"declared"`
	// Calls says, for each group and crew member the pack asks it for and
	// each declared source, which calls its sessions or it would be let make
	// and what an import does about each: nothing, ask the operator, or
	// neither ask nor grant.
	Calls []Calls `json:"calls"`
	// Expose says, for each crew member the pack asks it for
	// (expose_to_peers), which of the yard's peers it is exposed to and
	// what an import does about each of the others: ask the operator, or
	// not ask again.
	Expose []Expose `json:"expose"`
	// Setup are the pack's setup notes, its author's words about what a
	// setup takes, as the file held them when the plan was made; SetupFile
	// is that file of the pack. Both are "" for a pack without any. check
	// prints them after the plan and apply after the result, marked as
	// the pack's and not yardr's.
	Setup     string   `json:"setup,omitempty"`
	SetupFile string   `json:"setup_file,omitempty"`
	Notes     []string `json:"notes"`
	// Refused is why this plan must not be applied as it is; empty when it
	// may be.
	Refused []string `json:"refused"`
}

// Object is one thing in the yard and what the import would do to it. One
// of File, Group, Flow, Pointer, Route, CrewMember, Subscription, Map and
// Source is set, by Kind.
type Object struct {
	Kind    string   `json:"kind"` // file group flow pointer route crew member subscription map source
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Changes []Diff   `json:"changes,omitempty"`
	Asks    []string `json:"asks,omitempty"` // question ids
	// Binds is the yard's group whose agent kind, model and arguments a new
	// group of the pack that binds as it (Group.BindsAs) gets: the import
	// asks nothing of it. A group of the yard has them in its configuration,
	// copied into Group; one the same import makes has them from its
	// answers, when it is applied.
	Binds string `json:"binds,omitempty"`
	// Keeps says what the import leaves as the yard has it though the pack
	// carries it, and why: a prompt, where the depot has its own role and the
	// import was told to keep those; the role and the limit of a group of
	// the yard that takes the pack's, where they differ from the pack's.
	Keeps []string `json:"keeps,omitempty"`
	// Shadows are the depot's files that are not used while the object is as
	// the pack sets it: the depot's own role, under a prompt from the pack.
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
	CrewMember   *CrewOp            `json:"crew,omitempty"`
	Subscription *SubscriptionOp    `json:"subscription,omitempty"`
	Map          *MapOp             `json:"map,omitempty"`
	Source       *SourceOp          `json:"source,omitempty"`
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

// RouteOp is the route state a depot's stage (and type) would have. The store
// mints an id per added route, so a route that changes is added anew and
// the ones it stands in for, listed under Delete, are removed. An empty
// Group with Delete means the pack leaves that stage without a route.
// Originator is any for a stage whose group starts no agents, so it takes
// every originator's beads; "" for an agent stage, which takes the
// operator's alone.
type RouteOp struct {
	Stage      string   `json:"stage"`
	Depot      string   `json:"depot"`
	Type       string   `json:"type,omitempty"`
	Group      string   `json:"group"`
	Originator string   `json:"originator,omitempty"`
	Priority   int      `json:"priority"`
	Delete     []string `json:"delete,omitempty"`
}

// CrewOp is what the pack sets on a crew member: its role and the event
// kinds it hears. Everything else of the crew member is the yard's.
type CrewOp struct {
	Pack       string   `json:"pack"` // the pack's name for it
	CrewMember string   `json:"crew"` // the yard's
	Prompt     string   `json:"prompt,omitempty"`
	On         []string `json:"on"`
	// Restart says the crew member is running and its role or what it hears
	// would change: an import does not restart it.
	Restart bool `json:"restart,omitempty"`
}

// SubscriptionOp subscribes a crew member; Remove is the subscription it
// replaces, when a source's scope changes.
type SubscriptionOp struct {
	CrewMember string              `json:"crew"`
	Add        *store.Subscription `json:"add,omitempty"`
	Remove     *store.Subscription `json:"remove,omitempty"`
}

// MapOp sets on the yard's hook Hook the crew member it moves tracked issues
// as and its map (store.Hook's As and Map), and the table of what the
// source's events say of a bead (Says), and leaves the rest of the hook. It
// sets the parts it has: no As, and the hook's crew member and map stay; no
// Says, and its table does. A dropped one clears the parts it has.
type MapOp struct {
	Hook   string            `json:"hook"`
	Source string            `json:"source"`
	As     string            `json:"as"`
	Map    map[string]string `json:"map"`
	Says   []store.Say       `json:"says,omitempty"`
	Makes  []store.Make      `json:"makes,omitempty"`
}

// SourceOp adds a declared source to the yard, or replaces the one of its
// name, which keeps its env_file (store.PutSource). A dropped one is removed.
type SourceOp struct {
	Name string `json:"name"`
	// From is the declaration's file in the pack, and Text what it held when
	// the plan was made: what is added is what was planned.
	From string `json:"from"`
	Text string `json:"declaration"`
	// DeclDir is the declaration's directory in the pack, a slash path (""
	// for the pack's root): the commands' relative programs are under it.
	DeclDir string `json:"decl_dir,omitempty"`
	// Commands are the command files the declaration brings: each command,
	// its program and the sha256 of the pack's file when the plan was made.
	// Apply copies them, and refuses a pack that changed since.
	Commands []CommandPlan `json:"commands,omitempty"`
}

// CommandPlan is one command a declaration names: the key that names it
// (thread.reply), its program, and for a program the pack carries the
// sha256 of the pack's file. An absolute program is the operator's own,
// which the pack does not carry: its SHA256 is "".
type CommandPlan struct {
	Key     string `json:"key"`
	Program string `json:"program"`
	SHA256  string `json:"sha256,omitempty"`
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

// Governs is the flow the depot's beads (of Type, when set) would follow
// once the plan is applied, and the one they follow now.
type Governs struct {
	Type   string `json:"type,omitempty"`
	Flow   string `json:"flow"`
	Source string `json:"source"`
	Now    string `json:"now"`
	InPack bool   `json:"in_pack"`
}

// SourceNeeds is what a crew member's subscription to a source takes.
type SourceNeeds struct {
	Source     string `json:"source"`
	CrewMember string `json:"crew"`
	Needs      []Need `json:"needs"`
}

// Need is one of them: ok, missing, or unknown when this process cannot
// tell.
type Need struct {
	What   string `json:"what"` // hook secret_file identity credential listener
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// NotSet is the Action of a MapPlan the import sets nothing for: the yard
// has no hook of the source, or the crew member no identity in it.
const NotSet = "not_set"

// Skipped is the Action of a MapPlan the import sets nothing for though no
// hook or identity is missing: the yard has no source of the name, or the
// pack's lines do not fit the one it has. Skipped says why, and the next
// apply sets them once it is there.
const Skipped = "skipped"

// DeclaredPlan is what an import does about a source the pack declares.
// The import adds the declaration and nothing else: the file the source's
// credential is read from is named by the operator, never by a pack, and so
// is the source a user is told through.
type DeclaredPlan struct {
	Source string `json:"source"`
	File   string `json:"file"` // in the pack
	// Base is where the source's calls go, in words (Declaration.Address):
	// its base, or the variable a credential that is the address is in.
	Base string `json:"base"`
	// Calls are the source's calls, each "name (kind)".
	Calls []string `json:"calls"`
	// Commands are the declaration's commands that name a file: each
	// command, the file it names and that file's sha256, as the pack has
	// it. The import copies the files into the yard.
	Commands []CommandPlan `json:"commands,omitempty"`
	// Action is create, change or unchanged.
	Action string `json:"action"`
	// Needs are env_file, for a source with a credential, and notify, for
	// one that can tell a user: each ok or missing, with the command.
	Needs []Need `json:"needs"`
	// Reaches is set when the pack's declaration replaces the yard's and
	// somebody gets a call through a grant they have already.
	Reaches *Reach `json:"reaches,omitempty"`
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

// MapPlan is what an import does about the stage map a pack carries for
// one source. The import sets the map and the crew member on a hook the yard
// has; it never makes the hook, and never lets the crew member set a status:
// Needs says which of those the yard lacks and the command for each.
type MapPlan struct {
	Source string `json:"source"`
	Pack   string `json:"pack"` // the pack's name for the crew member
	As     string `json:"as"`   // the yard's
	// Map is the pack's, keyed as a hook's is: "<bead type>:<stage>".
	Map map[string]string `json:"map"`
	// Says is the pack's table of what the source's events say of a bead.
	// A plan with it and no map has no As, and needs the hook only.
	Says []store.Say `json:"says,omitempty"`
	// Makes is the pack's table of what the source's events make, which
	// needs the hook only too.
	Makes []store.Make `json:"makes,omitempty"`
	// Hook is the yard's hook of the source, "" when it has none.
	Hook string `json:"hook,omitempty"`
	// Action is create for a hook without a map, change for one with
	// another (Was), unchanged, NotSet, or Skipped.
	Action string `json:"action"`
	Was    *MapOp `json:"was,omitempty"`
	// Drops are the hook's says and makes lines the pack does not give,
	// which setting the pack's tables would drop: each with whose it is,
	// another pack's or the operator's. Empty when the hook holds none
	// beyond the pack's own lines from its last import. An import drops
	// them only with --yes.
	Drops []HookDrop `json:"drops,omitempty"`
	// Skipped is why the lines are not set: the yard has no source of the
	// name, or they do not fit the one it has. Empty when they are not
	// skipped.
	Skipped string `json:"skipped,omitempty"`
	// Needs are hook, identity and set_state, each ok or missing.
	Needs []Need `json:"needs"`
}

// HookDrop is one says or makes line on the yard's hook that the pack does
// not give and setting the pack's table would drop: the line as the hook
// has it, and whose, another pack's import or the operator's hand.
type HookDrop struct {
	Table string `json:"table"` // says or makes
	Line  string `json:"line"`
	Owner string `json:"owner"` // pack <pack> in <depot>, or the operator's
}

// Missing is the detail of the need what when the yard lacks it, else "".
func (m MapPlan) Missing(what string) string {
	for _, n := range m.Needs {
		if n.What == what && n.Status != "ok" {
			return n.Detail
		}
	}
	return ""
}

// OptionalSource is a source the pack names that the yard has nothing of.
// Setup is the command that sets it up: the pack apply that brings a
// source the yard lacks, or the hook add that receives one it has in Go.
// Empty when no command does: a source no pack brings. Detail is the
// line's words: why, with the setup.
type OptionalSource struct {
	Source string `json:"source"`
	Setup  string `json:"setup,omitempty"`
	Detail string `json:"detail"`
}

// MapLines are a hook's map as yardr hook add --map takes it, sorted.
func MapLines(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// OpenThreads is what the pack asks that one of its crew may open
// threads in. Each source of Wants is in exactly one of Has, Lacks,
// Declined and Asks. Nothing here is granted by the plan: the operator's
// answers are given when it is applied (ApplyOptions.OpenThreads).
type OpenThreads struct {
	Pack       string `json:"pack"` // the pack's name for the crew member
	CrewMember string `json:"crew"` // the yard's
	// Wants are the sources the pack names, in its order.
	Wants []string `json:"wants"`
	// Has are those the yard's crew member may open threads in already: no
	// question.
	Has []string `json:"has"`
	// Lacks are those this yard has no hook or no credential for: an import
	// asks nothing and grants nothing for them, and records no answer.
	Lacks []Lack `json:"lacks"`
	// Declined are those an earlier import was told no for: not asked again.
	Declined []string `json:"declined"`
	// Asks are the rest: what an import asks the operator.
	Asks []string `json:"asks"`
	// Recorded are the answers the last import recorded for the sources in
	// Wants.
	Recorded map[string]bool `json:"recorded,omitempty"`
}

// Expose is what the pack asks for one of its crew that it wants the
// yard's peers to see and write to (expose_to_peers). Each peer the yard
// has is in exactly one of Has, Declined and Asks. Nothing here is granted
// by the plan: the operator's answers are given when it is applied
// (ApplyOptions.Expose). A yard with no peer has nothing to ask.
type Expose struct {
	Pack       string `json:"pack"` // the pack's name for the crew member
	CrewMember string `json:"crew"` // the yard's
	// Has are the peers the crew member is exposed to already: no question.
	Has []string `json:"has"`
	// Declined are those an earlier import was told no for: not asked again.
	Declined []string `json:"declined"`
	// Asks are the rest: what an import asks the operator, once per peer.
	Asks []string `json:"asks"`
	// Keys are the public keys of the yard's peers, by name: an answer is
	// recorded by key (store.PackImport.Expose).
	Keys map[string]string `json:"keys"`
	// Recorded are the answers the last import recorded, by peer name.
	Recorded map[string]bool `json:"recorded,omitempty"`
}

// Lack is a source a pack wants a crew member to open threads in, and what of
// hook and credential this yard does not have for it, with the detail the
// plan's Sources table gives.
type Lack struct {
	Source  string `json:"source"`
	Missing []Need `json:"missing"`
}

// Grantable reports whether an import may be answered for source: it would
// ask about it, or an earlier one was told no.
func (o OpenThreads) Grantable(source string) bool {
	return slices.Contains(o.Asks, source) || slices.Contains(o.Declined, source)
}

// Calls is what the pack asks that the sessions of one of its groups, or
// one of its crew, may call of one declared source. Each of Wants is in
// exactly one of Has, Lacks, Declined and Asks. Nothing here is granted by
// the plan: the operator's answers are given when it is applied
// (ApplyOptions.Calls).
type Calls struct {
	// Pack is the pack's name for the group or crew member; one of Group
	// and CrewMember is the yard's.
	Pack       string `json:"pack"`
	Group      string `json:"group,omitempty"`
	CrewMember string `json:"crew,omitempty"`
	Source     string `json:"source"`
	// Wants are what the pack names, in its order: store.GrantRead, and
	// calls by name. Updates are those of them that are update calls.
	Wants   []string `json:"wants"`
	Updates []string `json:"updates"`
	// Has are those the grantee is granted already: no question.
	Has []string `json:"has"`
	// Lacks are those nothing is asked or granted for, and no answer
	// recorded, and Missing why: the yard has no such source, the source no
	// such call, or no file is set that its credential is read from.
	Lacks   []string `json:"lacks"`
	Missing []Need   `json:"missing"`
	// Declined are those an earlier import was told no for: not asked again.
	Declined []string `json:"declined"`
	// Asks are the rest: what an import asks the operator.
	Asks []string `json:"asks"`
	// Recorded are the answers the last import recorded for what is in
	// Wants.
	Recorded map[string]bool `json:"recorded,omitempty"`
}

// Grantee is who the calls are asked for, in the yard.
func (c Calls) Grantee() store.Grantee {
	return store.Grantee{Group: c.Group, Crew: c.CrewMember}
}

// Grantable reports whether an import may be answered for call: it would
// ask about it, or an earlier one was told no.
func (c Calls) Grantable(call string) bool {
	return slices.Contains(c.Asks, call) || slices.Contains(c.Declined, call)
}

// Words are calls as a plan prints them, an update marked as one: "read,
// note (update)".
func (c Calls) Words(calls []string) string {
	words := make([]string, len(calls))
	for i, w := range calls {
		words[i] = w
		if slices.Contains(c.Updates, w) {
			words[i] += " (" + source.Update + ")"
		}
	}
	return strings.Join(words, ", ")
}

// CallKey is what one call of a source is answered and recorded under:
// "crm:read".
func CallKey(source, call string) string { return source + ":" + call }

// FlowName is the name a pack's flow has in the store. Flow names are the
// yard's, shared by its depots, so the pack's name goes in front: two packs
// may each have a flow called default.
func FlowName(pack, flow string) string { return pack + "." + flow }

// FlowQuestion is the id of the question a plan asks when the pack has
// several flows a depot could follow and nothing says which.
const FlowQuestion = "pack.flow"

// GroupName is the name a pack's group has in the yard: agent and script
// groups are the depot's, people groups the yard's.
func GroupName(depot string, g Group) string {
	if g.People {
		return g.Name
	}
	return depot + "-" + g.Name
}

// runner is the runner a group of the yard has that the pack's g is set on.
func (g Group) runner() string {
	switch {
	case g.Agent != "":
		return "herdr"
	case g.Run != "":
		return "exec"
	}
	return "manual"
}

// Fits reports whether h, a group of the yard, may take the pack's g: an
// agent group an agent group, a script group a script group, people people.
func Fits(g Group, h store.Group) bool { return h.Runner == g.runner() }

// groupKind is the kind of group with runner, as a sentence names it.
func groupKind(runner string) string {
	switch runner {
	case "herdr":
		return "an agent group"
	case "exec":
		return "a script group"
	case "manual":
		return "a people group"
	}
	return "a group of runner " + runner
}

type planner struct {
	ctx   context.Context
	st    store.API
	p     *Pack
	o     Options
	plan  *Plan
	depot *store.Depot
	// rec is the record of this pack's last import into the depot, nil when
	// there was none; all are the records of every import.
	rec *store.PackImport
	all []store.PackImport
	// told are the pack's crew whose taker a flag or the last import
	// named; the others go by their own name.
	told map[string]bool
	// roles is what the depot has of the pack's role names, read together
	// on the first asked for; rolesErr fails every role after a read that
	// failed the call.
	roles     map[string]depotfile.File
	rolesRead bool
	rolesErr  error
}

// Build plans the import of p. It only reads: the store, the pack and the
// files under the yard's home.
func Build(ctx context.Context, st store.API, p *Pack, o Options) (*Plan, error) {
	if p.Kind == KindSource {
		return buildSource(ctx, st, p, o)
	}
	depot, err := st.GetDepot(ctx, o.Depot)
	if err != nil {
		return nil, err
	}
	b := &planner{ctx: ctx, st: st, p: p, o: o, depot: depot, plan: &Plan{
		Pack: p.Name, Dir: p.Dir, Kind: p.Kind, src: p.fsys, Depot: o.Depot, Home: o.Home, Install: config.At(o.Home).Pack(p.Name), StageFiles: config.At(o.Home).Flows(), Setup: p.SetupText, SetupFile: p.Setup,
		Groups: map[string]string{}, Crew: map[string]string{}, Fill: map[string]string{},
		Objects: []Object{}, Questions: []Question{}, Governs: []Governs{}, Sources: []SourceNeeds{}, OpenThreads: []OpenThreads{}, Maps: []MapPlan{}, Optional: []OptionalSource{}, Declared: []DeclaredPlan{}, Calls: []Calls{}, Expose: []Expose{}, Notes: []string{}, Refused: []string{},
	}}
	if b.all, err = st.Packs(ctx); err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(b.all, func(r store.PackImport) bool { return r.Pack == p.Name && r.Depot == o.Depot }); i >= 0 {
		b.rec = &b.all[i]
	}
	if o.DepotRoles && o.PackRoles {
		return nil, errors.New("--depot-roles and --pack-roles are opposite choices: give one")
	}
	// Whose roles, as at the last import unless told anew.
	b.plan.DepotRoles = o.DepotRoles || (!o.PackRoles && b.rec != nil && b.rec.DepotRoles)
	for name := range o.Crew {
		if !slices.ContainsFunc(p.Crew, func(r CrewMember) bool { return r.Name == name }) {
			return nil, fmt.Errorf("--crew %s=%s: the pack has no crew member %s", name, o.Crew[name], name)
		}
	}
	for name := range o.Groups {
		if !slices.ContainsFunc(p.Groups, func(g Group) bool { return g.Name == name }) {
			return nil, fmt.Errorf("--group %s=%s: the pack has no group %s", name, o.Groups[name], name)
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
		b.crew,
		b.maps,
		b.optional,
		b.declared,
		b.calls,
		b.dropped,
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.plan, nil
}

// buildSource plans the import of a pack of kind source, into the yard
// itself: its declared sources and nothing else. The yard needs no depot
// for it.
func buildSource(ctx context.Context, st store.API, p *Pack, o Options) (*Plan, error) {
	if o.Depot != "" {
		return nil, fmt.Errorf("pack %s is of kind source: it is imported into the yard, not into depot %s", p.Name, o.Depot)
	}
	if o.Flow != "" {
		return nil, fmt.Errorf("pack %s is of kind source: it has no flow (--flow %s)", p.Name, o.Flow)
	}
	b := &planner{ctx: ctx, st: st, p: p, o: o, plan: &Plan{
		Pack: p.Name, Dir: p.Dir, Kind: p.Kind, src: p.fsys, Home: o.Home, Install: config.At(o.Home).Pack(p.Name), StageFiles: config.At(o.Home).Flows(), Setup: p.SetupText, SetupFile: p.Setup,
		Groups: map[string]string{}, Crew: map[string]string{}, Fill: map[string]string{},
		Objects: []Object{}, Questions: []Question{}, Governs: []Governs{}, Sources: []SourceNeeds{}, OpenThreads: []OpenThreads{}, Maps: []MapPlan{}, Optional: []OptionalSource{}, Declared: []DeclaredPlan{}, Calls: []Calls{}, Expose: []Expose{}, Notes: []string{}, Refused: []string{},
	}}
	var err error
	if b.all, err = st.Packs(ctx); err != nil {
		return nil, err
	}
	if i := slices.IndexFunc(b.all, func(r store.PackImport) bool { return r.Pack == p.Name && r.Depot == "" }); i >= 0 {
		b.rec = &b.all[i]
	}
	for name := range o.Crew {
		return nil, fmt.Errorf("--crew %s=%s: the pack has no crew member %s", name, o.Crew[name], name)
	}
	for name := range o.Groups {
		return nil, fmt.Errorf("--group %s=%s: the pack has no group %s", name, o.Groups[name], name)
	}
	if err := b.declared(); err != nil {
		return nil, err
	}
	if err := b.dropped(); err != nil {
		return nil, err
	}
	return b.plan, nil
}

// names settles which crew member of the yard takes each of the pack's and who
// the operator is, before the files: a role or stage file is installed with
// those names in it.
func (b *planner) names() error {
	b.told = map[string]bool{}
	for _, pr := range b.p.Crew {
		name, told := b.o.Crew[pr.Name]
		if !told && b.rec != nil {
			// Whoever took it at the last import, unless told anew.
			name, told = b.rec.Crew[pr.Name]
		}
		if !told {
			name = pr.Name
		}
		b.plan.Crew[pr.Name], b.told[pr.Name] = name, told
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
	// Last: {{operator}} is the operator also in a pack with a crew member of
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

// chosen are the pack's flows an import into the depot would use: the one
// the depot follows (Options.Flow, else the first without a type) and every
// flow for a type. Another flow a depot could follow is not imported: it has
// no depot to point at it, and its stages would get no routes.
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
			return nil, fmt.Errorf("--flow %s: the pack has no such flow for a depot (it has: %s)", b.o.Flow, strings.Join(names, ", "))
		}
		own[0], own[i] = own[i], own[0]
	} else if i := slices.IndexFunc(own, func(f Flow) bool { return b.rec != nil && f.Name == b.rec.Flow }); i >= 0 {
		// The flow the depot was given by the last import, unless told anew.
		own[0], own[i] = own[i], own[0]
	} else if len(own) > 1 {
		names := make([]string, len(own))
		for i, f := range own {
			names[i] = f.Name
		}
		b.plan.Questions = append(b.plan.Questions, Question{ID: FlowQuestion, Object: "pack " + b.p.Name, Field: "flow",
			Ask: fmt.Sprintf("which of the pack's flows does depot %s follow (--flow <name>)? It has: %s.", b.o.Depot, strings.Join(names, ", ")), Default: own[0].Name})
	}
	if len(own) > 0 {
		b.plan.Flow = own[0].Name
		out = append(out, own[0])
		for _, f := range own[1:] {
			b.plan.Notes = append(b.plan.Notes, fmt.Sprintf("flow %s of the pack is not imported: depot %s would follow %s (--flow %s picks this one)", f.Name, b.o.Depot, own[0].Name, f.Name))
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
			// The reader refuses a name that leads out of the flow's
			// directory; nothing is written by a pack that got past it.
			rel := filepath.FromSlash(s.Name) + ".md"
			if !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
				return fmt.Errorf("stage %s of flow %s: its stage file would be written outside %s", s.Name, f.Name, dir)
			}
			op := &FileOp{To: filepath.Join(dir, rel), Flow: name, Stage: s.Name, Fill: b.p.Filled(s.File)}
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
	op.From = joinName(b.p.Dir, rel)
	text, err := fs.ReadFile(b.p.fsys, rel)
	if err != nil {
		return err
	}
	if op.Fill {
		text = Fill(text, b.plan.Fill)
	}
	op.Mode, op.SHA256 = "0644", sum(text)
	// A script, by the pack's saying so and not by its mode in the source
	// (Pack.Filled).
	if !b.p.Filled(rel) {
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
// installed file to, one per depot it was imported into: the depots share the
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

// DroppedHookLines are the hook tables the plan would replace though they
// hold says or makes lines the pack does not give, each with which lines
// and whose: another pack's, or the operator's. An import drops them only
// with --yes.
func (p *Plan) DroppedHookLines() []string {
	var out []string
	for _, m := range p.Maps {
		if len(m.Drops) == 0 || m.Hook == "" {
			continue
		}
		var lines []string
		for _, d := range m.Drops {
			owner := d.Owner
			if owner == "the operator's" {
				owner += " (set by hand with yardr hook add)"
			}
			lines = append(lines, fmt.Sprintf("%q (%s)", d.Line, owner))
		}
		out = append(out, fmt.Sprintf("hook %s's %s table has lines the pack %s does not give, which the import would drop: %s", m.Hook, tables(m.Drops), p.Pack, strings.Join(lines, ", ")))
	}
	return out
}

// tables is the tables drops take lines from: says, makes, or both.
func tables(drops []HookDrop) string {
	var says, makes bool
	for _, d := range drops {
		if d.Table == "says" {
			says = true
		} else {
			makes = true
		}
	}
	switch {
	case says && makes:
		return "says and makes"
	case says:
		return "says"
	default:
		return "makes"
	}
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
