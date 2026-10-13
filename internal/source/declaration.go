// Package source reads the declaration of a service outside the yard, one
// YAML file that names the service's address, the credential it takes and
// the requests the yard may make of it, and makes one such request (Do).
// A service is added by its file and no Go: what the file cannot say (a
// request that must be signed, an answer in pages, anything that is no
// JSON over HTTPS) is not a declared source.
//
// One thing a file can say is no request: a command the yard runs. The
// command that tells what a harness kind's subscription still carries (the
// key limits) is the one that is run today; internal/limits runs it. A file
// with that key may have no calls at all. The thread and link keys say how
// the yard speaks in the source's threads and how its issues are a bead's
// link — thread's by one command per thing the yard does there, run by
// internal/hooks. The hook key says what the service sends in: how the
// core verifies its webhooks, and the command that turns a verified body
// into the envelope.
//
// The events key says what the source sends in about pull requests and
// CI, and the commands that read them back: the daemon judges CI runs
// against the pull request's head, and reads the pull request and its CI
// live when no event told what it needs.
//
// The file holds no secret. It names the variable the credential is in, and
// the value is read from a file of the yard's at each call, so a
// declaration can be kept in the store, shown and copied.
package source

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"yardr.dev/yardr/internal/agents"
)

// BuiltIn are the names a declaration may not take: the sources yardr
// has in Go (hooks.Adapters), but for one whose pack has taken the name
// while its Go adapter is still compiled in, its own name for itself
// (store.SourceYardr), and its name for the yards it is linked with
// (store.SourcePeer). The pack's source then wins every lookup when the
// yard has it. A test in hooks holds this list against the adapters.
var BuiltIn = []string{"peer", "token", "yardr"}

// MaxDeclaration bounds a declaration file: it is a list of requests, read
// whole at every use.
const MaxDeclaration = 256 << 10

// The kinds of a call. An update changes something in the service; a read
// does not, whatever its method (a search by POST, GraphQL).
const (
	Read   = "read"
	Update = "update"
)

// CredentialMark is where the credential's value goes: in the credential's
// header, or as a whole leaf of a body. Never in a path or a query, which
// end up in the logs of every proxy and server on the way.
const CredentialMark = "{credential}"

// ProviderMark is where, in the command of a limits key, the provider's own
// name for the harness kind asked about goes.
const ProviderMark = "{provider}"

// Declaration is a source as its file declares it.
type Declaration struct {
	Name  string `json:"name"`
	About string `json:"about,omitempty"`
	// Base is where every call's path is under, without a trailing slash.
	// Empty for a source that declares no call, which has a Limits.
	Base       string      `json:"base"`
	Credential *Credential `json:"credential,omitempty"`
	// Check names the call `yardr source check` makes: a read call that
	// needs no parameter. Empty when the file names none.
	Check string `json:"check,omitempty"`
	// Idempotency is the name of the header the service takes a key in, by
	// which it tells an update it was sent before from a new one; "" for a
	// service that takes none. Whoever makes an update call gives the key
	// (DoKeyed): the same for a call run again after one whose outcome was
	// lost, so that it writes once.
	Idempotency string `json:"idempotency,omitempty"`
	Calls       []Call `json:"calls"` // in file order
	// Notify is the call the yard makes to tell a person outside the yard,
	// nil for a source that cannot: the capability a user names a source
	// for (yardr user set <name> notify=<source>).
	Notify *Notify `json:"notify,omitempty"`
	// Hear is which deliveries of the source the yard keeps, nil for a
	// source that keeps every delivery it verifies: one whose envelope
	// field holds the address of a user told through the source.
	Hear *Hear `json:"hear,omitempty"`
	// Limits is the command that says what a harness kind's subscription
	// still carries, nil for a source that cannot: see internal/limits.
	Limits *Limits `json:"limits,omitempty"`
	// Thread is how the yard speaks in the source's threads: a command per
	// thing it does there, nil for a source the yard does not speak in.
	Thread *Thread `json:"thread,omitempty"`
	// Link is the source's issues as a bead's link, nil for a source whose
	// issues are none.
	Link *Link `json:"link,omitempty"`
	// Identity is who a crew member is in the source, as the declaration
	// says it, nil for a source whose keys keep their case.
	Identity *Identity `json:"identity,omitempty"`
	// Hook is what the source sends in: how the core verifies its
	// webhooks and the command that turns a verified body into the
	// envelope, nil for a source the yard hears nothing from.
	Hook *Hook `json:"hook,omitempty"`
	// Events is what the source sends in about pull requests and CI, and
	// the commands that read them back, nil for a source without either.
	Events *Events `json:"events,omitempty"`
}

// The bodies of a hook key: how far a body identifies a delivery.
const (
	// BodiesUnique: no two deliveries from the source share a body
	// (a source that signs a timestamp into each).
	BodiesUnique = "unique"
	// BodiesUniquePerHook: no two deliveries to one hook share a body,
	// but one event's body goes to every hook that asked for it.
	BodiesUniquePerHook = "unique-per-hook"
	// BodiesRepeat: a body may come again as a new delivery.
	BodiesRepeat = "repeat"
)

// bodiesKinds are the values a hook's bodies takes, in the order the
// reader names them.
var bodiesKinds = []string{BodiesUnique, BodiesUniquePerHook, BodiesRepeat}

// The ways the core verifies a delivery of a declared source.
const (
	// HookScheme is a scheme of hook.verify: HMAC-SHA256 over the body.
	HookScheme = "hmac-sha256"
	// HookSecret is a scheme of hook.verify: the header holds the hook's
	// secret itself, compared as it arrives.
	HookSecret = "secret"
	// HookSigned is what the signature covers: the raw body.
	HookSigned = "body"
	// HookEncoding is how the signature arrives: hex.
	HookEncoding = "hex"
)

// hookSchemes are the values hook.verify.scheme takes, in the order the
// reader names them.
var hookSchemes = []string{HookScheme, HookSecret}

// The units of hook.verify.timestamp: milliseconds or seconds since the
// epoch, as the service counts them.
const (
	HookMs = "ms"
	HookS  = "s"
)

// Hook is what the source sends in: the data the core verifies each
// delivery from, the command that turns a verified body into the envelope,
// and the keys its events' fields may carry.
type Hook struct {
	Verify HookVerify `json:"verify"`
	// Bodies is how far a body identifies a delivery: BodiesUnique,
	// BodiesUniquePerHook or BodiesRepeat.
	Bodies string `json:"bodies"`
	// Normalize turns a verified body into the envelope: in {headers, body},
	// out the envelope. It is handed no credential.
	Normalize []string `json:"normalize"`
	// Keys are the keys its events' fields may carry, which a
	// subscription's --where and a pack's says may then name beside
	// store.WhereKeys.
	Keys []string `json:"keys,omitempty"`
}

// HookVerify is how the core proves a delivery came from the source, from
// the request's headers and the raw body, using the hook's secret. The
// signing secret reaches no command, and an unverified body reaches none.
type HookVerify struct {
	// Scheme is how a delivery is verified: hmac-sha256, or secret, the
	// header holding the hook's secret itself. Signed is what the
	// signature covers: body, for hmac-sha256 alone.
	Scheme string `json:"scheme"`
	Signed string `json:"signed"`
	// Signature is the header the signature arrives in, in hex, past the
	// prefix ("sha256=", or none); with scheme secret it is the header
	// the secret arrives in, compared as it is.
	Signature HookSignature `json:"signature"`
	// Delivery is where the service's id of the delivery arrives: in a
	// header, or in the body at the dot path. Empty with scheme secret
	// for a service that sends none.
	Delivery HookDelivery `json:"delivery"`
	// Timestamp is when the delivery was sent, checked after the
	// signature, so it is one the sender signed. Nil for a service that
	// sends none, whose deliveries cannot be told from a replay by age.
	Timestamp *HookTimestamp `json:"timestamp,omitempty"`
}

// HookSignature is where a delivery's signature arrives.
type HookSignature struct {
	Header string `json:"header"`
	// Encoding is how it arrives: hex. For hmac-sha256 alone.
	Encoding string `json:"encoding"`
	// Prefix is what stands before it ("sha256="), "" for none. For
	// hmac-sha256 alone.
	Prefix string `json:"prefix,omitempty"`
}

// HookDelivery is where the service's id of a delivery arrives: in a
// header, or in the body at the dot path. One of the two; both empty for
// a service that sends none, with scheme secret.
type HookDelivery struct {
	Header string `json:"header,omitempty"`
	Body   string `json:"body,omitempty"`
}

// HookTimestamp is when a delivery was sent: in the body at the dot path
// (webhookTimestamp), or in a header, in ms or s, within window of the
// yard's clock.
type HookTimestamp struct {
	Body   string `json:"body,omitempty"`
	Header string `json:"header,omitempty"`
	Unit   string `json:"unit"`
	Window string `json:"window"`
}

// Events is what the source sends in about pull requests and CI, and the
// commands that read them back. Runs are the envelope types that are CI
// runs, judged against the pull request's head; HeadFrom the types whose
// head_sha becomes that head. PR reads the pull request live, for the
// head no event has told; CI reads the CI state of one commit of it.
type Events struct {
	// Runs are the envelope types that are CI runs, whatever became of
	// them. A run counts for the pull request's head only.
	Runs []string `json:"runs,omitempty"`
	// HeadFrom are the envelope types that tell the pull request's head
	// commit: an event about the pull request itself, or a review of it.
	HeadFrom []string `json:"head_from,omitempty"`
	// PR reads the pull request live: in {repo, number, url}, out
	// {head, base, state}. It is handed the credential under
	// credential.env, as a thread command is.
	PR []string `json:"pr,omitempty"`
	// CI reads CI state: in {repo, number, sha}, out {head, base, state,
	// required, checks}, the checks in the tongue the core judges
	// (docs/integrations.md). Handed the credential as PR is.
	CI []string `json:"ci,omitempty"`
}

// Limits says how a source answers what a harness kind's subscription still
// carries: by a command of the operator's, which the yard runs. It is the
// one thing of a declaration that is run and not sent, so it is the
// operator's alone to add and no pack brings it.
type Limits struct {
	// Command is the program and its arguments, run without a shell. One
	// argument at least holds ProviderMark; the program never does.
	Command []string `json:"command"`
	// Kinds maps a harness kind (a group's or a crew member's kind) to the
	// provider's own name for it. A kind that is not here has no limits
	// known through this source.
	Kinds map[string]string `json:"kinds"`
}

// Argv is the command that asks for kind: Command with the provider's name
// for the kind in place of ProviderMark. ok is false for a kind the source
// does not map.
func (l *Limits) Argv(kind string) (argv []string, ok bool) {
	name, ok := l.Kinds[kind]
	if !ok {
		return nil, false
	}
	return l.ArgvName(name), true
}

// ArgvName is the command that asks for the provider named name: Command
// with the name in place of ProviderMark.
func (l *Limits) ArgvName(name string) []string {
	argv := make([]string, len(l.Command))
	for i, a := range l.Command {
		argv[i] = strings.ReplaceAll(a, ProviderMark, name)
	}
	return argv
}

// KindNames are the harness kinds the source answers for, sorted.
func (l *Limits) KindNames() []string {
	names := make([]string, 0, len(l.Kinds))
	for k := range l.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Thread is how the yard speaks in the source's threads: a command per
// thing it does there. Each is the program and its arguments, a list run
// without a shell, as limits.command is: one JSON object on stdin, one on
// stdout, exit 0.
type Thread struct {
	// Reply answers in a thread: in {thread, parent, body, key}, out
	// {id, url}. Posted finds what a reply whose outcome was lost left:
	// in {thread, key}, out {id, url} or {}.
	Reply  []string `json:"reply,omitempty"`
	Posted []string `json:"posted,omitempty"`
	// Read lists a thread's messages: in {thread}, out {messages: [...]}.
	Read []string `json:"read,omitempty"`
	// Open starts a thread: in {in, title, body, key, assignee?}, out
	// {id, title, url}.
	Open []string `json:"open,omitempty"`
	// Opened finds what an open whose outcome was lost left: in {in, key},
	// out {id, title, url} or {}. Posted's sibling for opened threads.
	Opened []string `json:"opened,omitempty"`
	// State sets a thread's status: in {thread, state, forward?}, out
	// {status, moved, was?}. forward is a stage map's move, which never
	// moves backwards; setting moves exactly.
	State []string `json:"state,omitempty"`
	// Place is what a place to open a thread in is called in this source
	// (team): the word --in takes. Open needs it.
	Place string `json:"place,omitempty"`
	// States are the words State takes, of started, review and done.
	States []string `json:"states,omitempty"`
	// Labels is whether the source takes free-form tags: State then takes
	// label:<name> beside the states, adding the label and never removing
	// one, to any thread.
	Labels bool `json:"labels,omitempty"`
	// Assignee is whether Open takes whom the thread is assigned to, and
	// as what: the person's id in the source (AssigneeID) or their key
	// (AssigneeKey), the two parts of an identity. "" is a source whose
	// open assigns nobody.
	Assignee string `json:"assignee,omitempty"`
}

// The parts of an identity a thread.assignee names.
const (
	AssigneeID  = "id"
	AssigneeKey = "key"
)

// FoldLower folds to lowercase: how a source carries what it matches in
// any case.
const FoldLower = "lower"

// Identity is who a crew member is in the source, as the declaration says
// it: how its keys fold. A key is kept folded, as the source's deliveries
// carry it, so one spelling is stored however it was typed.
type Identity struct {
	// Fold is how identity keys fold: lower.
	Fold string `json:"fold"`
}

// Link is the source's issues as a bead's link: the host its issue URLs are
// on, a regular expression that reads an issue from a URL's path, and two
// templates over the expression's groups ({1}, {2}): Fixed, the issue's one
// fixed form, and Thread, the id a thread command takes for it.
type Link struct {
	Host   string `json:"host"`
	Issue  string `json:"issue"`
	Fixed  string `json:"fixed"`
	Thread string `json:"thread"`
	// Fold is how the expression's groups fold before the templates
	// expand: lower, or "" for a source that matches them exactly.
	Fold string `json:"fold,omitempty"`
	// Pull is the source's second thread shape, its pull requests, nil
	// for a source whose threads are issues alone.
	Pull *PullShape `json:"pull,omitempty"`
	// Repos is the regular expression a repository's path matches, as
	// store.RemoteForm carries it past "host/": "" for a source that
	// keeps no repositories.
	Repos string `json:"repos,omitempty"`

	re      *regexp.Regexp
	pullRe  *regexp.Regexp
	reposRe *regexp.Regexp
}

// PullShape is the source's second thread shape, its pull requests: a
// regular expression that reads one from a URL's path, with its own
// fixed form and thread id, over the same groups in the same order.
// The groups are the repository's path segments and the pull request's
// number, in that order, so a pull request's repository and number read
// back out of them.
type PullShape struct {
	Issue  string `json:"issue"`
	Fixed  string `json:"fixed"`
	Thread string `json:"thread"`
}

// Groups are what link.issue reads from path, the groups of the expression
// without the whole match, or nil when the path is no issue's. The
// expression is compiled anew: a link read back from the store has no
// compiled form. With fold, the groups fold before the fixed and thread
// templates expand, so one spelling names the issue however it was typed.
func (l *Link) Groups(path string) []string {
	re := l.re
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Issue); err != nil {
			return nil // the reader refused anything else
		}
	}
	m := re.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	return l.foldGroups(m[1:])
}

// foldGroups folds groups as the link says, before the fixed and thread
// templates expand over them.
func (l *Link) foldGroups(g []string) []string {
	if l.Fold == FoldLower {
		for i := range g {
			g[i] = strings.ToLower(g[i])
		}
	}
	return g
}

// pullGroups are what the pull shape reads from path, the groups of its
// expression without the whole match, or nil when the path is no pull
// request's or the source has one shape alone. As Groups, the compiled
// form is compiled anew, and the groups fold with it.
func (l *Link) pullGroups(path string) []string {
	if l.Pull == nil {
		return nil
	}
	re := l.pullRe
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Pull.Issue); err != nil {
			return nil // the reader refused anything else
		}
	}
	m := re.FindStringSubmatch(path)
	if m == nil {
		return nil
	}
	return l.foldGroups(m[1:])
}

// FixedForm is the issue whose URL has path in its one fixed form:
// link.fixed with the groups link.issue reads from the path. ok is false
// when the path is no issue's.
func (l *Link) FixedForm(path string) (fixed string, ok bool) {
	g := l.Groups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Fixed, g), true
}

// ThreadOf is the id a thread command takes for the issue whose fixed URL
// has path: link.thread over the same groups. ok is false when the path
// is no issue's.
func (l *Link) ThreadOf(path string) (thread string, ok bool) {
	g := l.Groups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Thread, g), true
}

// PullFixedForm is the pull request whose URL has path in its one fixed
// form: the pull shape's fixed template over the groups its pattern
// reads from the path. ok is false when the path is no pull request's,
// or the source has one shape alone.
func (l *Link) PullFixedForm(path string) (fixed string, ok bool) {
	if l.Pull == nil {
		return "", false
	}
	g := l.pullGroups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Pull.Fixed, g), true
}

// PullThreadOf is the id a thread command takes for the pull request
// whose fixed URL has path: the pull shape's thread template over the
// same groups. ok is false when the path is no pull request's, or the
// source has one shape alone.
func (l *Link) PullThreadOf(path string) (thread string, ok bool) {
	if l.Pull == nil {
		return "", false
	}
	g := l.pullGroups(path)
	if g == nil {
		return "", false
	}
	return expandLink(l.Pull.Thread, g), true
}

// AliasOf is the other fixed form of the thread whose URL has path: the
// pull shape's over the groups the issue shape reads, or the issue
// shape's over the pull shape's. An issue and its pull request share
// their groups, so one names the other. ok is false when the path is no
// thread of either shape, or the source has one shape alone.
func (l *Link) AliasOf(path string) (string, bool) {
	if l.Pull == nil {
		return "", false
	}
	if g := l.Groups(path); g != nil {
		return expandLink(l.Pull.Fixed, g), true
	}
	if g := l.pullGroups(path); g != nil {
		return expandLink(l.Fixed, g), true
	}
	return "", false
}

// PullParts reads the repository and the number from the pull request
// whose URL has path: the pull shape's groups are the repository's path
// segments and the number, in that order. ok is false when the path is
// no pull request's.
func (l *Link) PullParts(path string) (repo, number string, ok bool) {
	g := l.pullGroups(path)
	if len(g) == 0 {
		return "", "", false
	}
	return strings.Join(g[:len(g)-1], "/"), g[len(g)-1], true
}

// PullURL is the URL of the pull request number of repo, as a link
// stores it: the pull shape's fixed template over the repository's path
// and the number. ok is false when they are no pull request of the
// source's: no pull shape, no repository or number, or parts the
// pattern has no groups for.
func (l *Link) PullURL(repo, number string) (string, bool) {
	if l.Pull == nil || repo == "" || number == "" {
		return "", false
	}
	g := append(strings.Split(repo, "/"), number)
	for _, p := range g {
		if p == "" {
			return "", false
		}
	}
	re := l.pullRe
	if re == nil {
		var err error
		if re, err = regexp.Compile(l.Pull.Issue); err != nil {
			return "", false // the reader refused anything else
		}
	}
	if len(g) != re.NumSubexp() {
		return "", false
	}
	return expandLink(l.Pull.Fixed, l.foldGroups(g)), true
}

// RepoOf is the repository the remote names: path, the remote in
// RemoteForm past "host/", when the repos pattern matches it whole.
// ok is false when the source keeps no repositories, or the path
// matches none.
func (l *Link) RepoOf(path string) (string, bool) {
	if l.Repos == "" {
		return "", false
	}
	re := l.reposRe
	if re == nil {
		var err error
		if re, err = regexp.Compile("^(?:" + l.Repos + ")$"); err != nil {
			return "", false // the reader refused anything else
		}
	}
	if !re.MatchString(path) {
		return "", false
	}
	if l.Fold == FoldLower {
		path = strings.ToLower(path)
	}
	return path, true
}

// expandLink fills the template with groups: {1} is the first. A group the
// expression has not is left as it is; the reader refuses such a template,
// so it is never from a file that was added.
func expandLink(tmpl string, groups []string) string {
	var b strings.Builder
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			continue
		}
		m := groupRefAt.FindStringSubmatch(tmpl[i:])
		if m == nil {
			b.WriteByte(tmpl[i])
			continue
		}
		if g, _ := strconv.Atoi(m[1]); g >= 1 && g <= len(groups) {
			b.WriteString(groups[g-1])
		} else {
			b.WriteString(m[0])
		}
		i += len(m[0]) - 1
	}
	return b.String()
}

// Notify says which call of a source tells a person, and which of the
// call's parameters take the four values the yard has for it: where the
// person is reached, a title, a text and a link.
type Notify struct {
	Call string `json:"call"`
	// Address is the parameter that takes where the person is reached, and
	// AddressEnv the name of the variable that holds it in the user's own
	// env file: it is one person's and often private, so it is kept as the
	// source's credential is, and is no more in the store.
	Address    string `json:"address"`
	AddressEnv string `json:"address_env"`
	// Title, Text and Link are the parameters that take each; "" for one
	// the call has no place for. One of Title and Text is always there.
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
	Link  string `json:"link,omitempty"`
}

// Hear says which deliveries of the source the yard keeps: one whose
// envelope field holds the address of a user told through the source. A
// delivery from anything else is dropped after verification: answered
// 200, recorded nowhere, and counted in sums beside refusals.
type Hear struct {
	// Field is the envelope field that must hold a told address.
	Field string `json:"field"`
	// Address is where that address is read from: the variable of the
	// user's own env file, the same one telling reads.
	Address HearAddress `json:"address"`
	// From narrows whom the source hears: HearFromOperator, or "" for
	// every user told through it.
	From string `json:"from,omitempty"`
}

// HearAddress is where a told address is read from: the variable of the
// user's own env file, as notify.address says it.
type HearAddress struct {
	Env string `json:"env"`
}

// HearFromOperator is the one from a hear key takes: the yard's operator
// (the operator setting, a user of the yard), so the file names a role,
// never a person.
const HearFromOperator = "operator"

// Credential is where a source's credential comes from and where it goes.
type Credential struct {
	// Env is the name of the variable that holds it, in the env file the
	// operator sets for the source. The name, never the value.
	Env string `json:"env"`
	// Header is the header it is sent in, "Name: text {credential}", for a
	// service that takes it there. One that takes it in the body names it
	// as a leaf of the call's body instead.
	Header string `json:"header,omitempty"`
	// UpdateEnv is the name of the variable an update call is sent with,
	// in the same file, so that the read calls can run on a credential
	// that may only read. Empty for a source with one credential, which
	// its updates are sent with too.
	UpdateEnv string `json:"update_env,omitempty"`
	// Mint are the names of the variables that together select a minted
	// credential: when the acting env sets every one, the core runs the
	// source's mint_command with their values and hands what it mints to
	// the call under Env. Names only, never values and never the command,
	// so a pack may bring them as it brings Env. Empty for a source whose
	// credential is used as it is set.
	Mint []string `json:"mint,omitempty"`
	// InPath says the service takes the credential in the request path,
	// so a call's path may name {credential}. It is said out loud because
	// a credential there is in the logs of every server on the way;
	// without it a path refuses one. A query refuses one either way.
	InPath bool `json:"in_path,omitempty"`
}

// Minted reports whether the credential may be minted: the file names the
// variables a mint takes. Nil-safe, for a file that names no credential.
func (c *Credential) Minted() bool { return c != nil && len(c.Mint) > 0 }

// EnvOf is the name of the variable a call of kind is sent with.
func (c *Credential) EnvOf(kind string) string {
	if kind == Update && c.UpdateEnv != "" {
		return c.UpdateEnv
	}
	return c.Env
}

// Envs are the names of the variables the source's env file sets: the
// credential's, and the one of its updates where that is another.
func (c *Credential) Envs() []string {
	if c.UpdateEnv != "" {
		return []string{c.Env, c.UpdateEnv}
	}
	return []string{c.Env}
}

// Names says in words what an env file sets for the credential: the static
// names, or with a mint, the mint's names or the static ones, the mint
// first, since it is what a call uses when both are set.
func (c *Credential) Names() string {
	if !c.Minted() {
		return strings.Join(c.Envs(), " and ")
	}
	return strings.Join(c.Mint, " and ") + ", or " + strings.Join(c.Envs(), " and ")
}

// Variables are the names an env file may set for the credential, every
// one the declaration has: the mint's inputs first, then the static ones.
func (c *Credential) Variables() []string {
	if c == nil {
		return nil
	}
	return append(append([]string{}, c.Mint...), c.Envs()...)
}

// Call is one request the yard may make of the source.
type Call struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	About   string  `json:"about,omitempty"`
	Params  []Param `json:"params,omitempty"` // in file order
	Request Request `json:"request"`
	Answer  Answer  `json:"answer"`
}

// Param is one parameter of a call. All are required unless Optional.
type Param struct {
	Name string `json:"name"`
	// Type is string, int, bool or enum.
	Type     string   `json:"type"`
	Pattern  string   `json:"pattern,omitempty"` // of a string: a regular expression it must match
	Max      int      `json:"max,omitempty"`     // of a string: its length at most, in characters
	Values   []string `json:"values,omitempty"`  // of an enum: what it may be
	Optional bool     `json:"optional,omitempty"`
	// Log keeps the parameter's value in the record of each call (the
	// source_call event), which otherwise has its name alone: for what an
	// audit asks by (a customer's id), and never for free text.
	Log bool `json:"log,omitempty"`

	re *regexp.Regexp
}

// Request is what a call sends. A {param} in Path, Query or Body is
// replaced by the parameter's value, escaped by where it stands.
type Request struct {
	Method string       `json:"method"`
	Path   string       `json:"path"`
	Query  []QueryValue `json:"query,omitempty"` // in file order
	// Body is sent as JSON: maps, lists and scalars as the file has them.
	// A string that is exactly {param} becomes the parameter's typed value.
	Body any `json:"body,omitempty"`
}

// QueryValue is one key of a request's query.
type QueryValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Answer is what of the service's answer a call returns.
type Answer struct {
	// Select keeps these top-level keys, of each element when the answer
	// is a list; without it the answer is returned as it is.
	Select []string `json:"select,omitempty"`
	// Max is the most a call returns, in bytes after Select: AnswerMax
	// unless the file raises it, AnswerLimit at most.
	Max int `json:"max"`
}

// Call returns the call of that name.
func (d *Declaration) Call(name string) (*Call, bool) {
	for i := range d.Calls {
		if d.Calls[i].Name == name {
			return &d.Calls[i], true
		}
	}
	return nil, false
}

func (c *Call) param(name string) (*Param, bool) {
	if c == nil {
		return nil, false
	}
	for i := range c.Params {
		if c.Params[i].Name == name {
			return &c.Params[i], true
		}
	}
	return nil, false
}

// Line is the call in a line: "POST /messages.json".
func (r Request) Line() string { return r.Method + " " + r.Path }

// Error is one thing wrong with a declaration: the file, the line and the
// key as a dotted path from the file's root.
type Error struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
	Key  string `json:"key,omitempty"`
	Msg  string `json:"error"`
}

func (e Error) Error() string {
	out := e.File
	if e.Line > 0 {
		out += ":" + strconv.Itoa(e.Line)
	}
	if e.Key != "" {
		out += ": " + e.Key
	}
	return out + ": " + e.Msg
}

// Errors is everything wrong with a declaration, in file order: it is read
// to its end so one run names every mistake.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

var (
	// validName is what a source and a call may be named: each becomes a
	// key in the store and a word on a command line.
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// paramName is also the name of a variable, for the credential's.
	paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// mark is a {name} in a path, a query or a body.
	mark       = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	headerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	// providerName is a provider's own name for a harness kind. It becomes
	// an argument of a command, so it cannot be read as a flag.
	providerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// hookKey is a key a hook's events may carry in their fields, as a
	// subscription's --where names it.
	hookKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

var (
	methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	types   = []string{"string", "int", "bool", "enum"}
)

// booleans are the plain scalars YAML 1.1 reads as a boolean (see
// internal/pack, whose reader this one follows): a key or a word that is
// one would be a name to this reader and a boolean to the next.
var booleans = []string{"y", "yes", "n", "no", "on", "off", "true", "false"}

func booleanLike(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!bool" || (n.Style == 0 && slices.Contains(booleans, strings.ToLower(n.Value))))
}

// Parse reads the declaration in text. file is how errors name it. What it
// returns with a nil error is safe to call: every refusal that does not
// depend on a parameter's value is made here.
func Parse(file string, text []byte) (*Declaration, error) {
	if len(text) > MaxDeclaration {
		return nil, Errors{{File: file, Msg: fmt.Sprintf("%d bytes: a declaration is at most %d", len(text), MaxDeclaration)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, Errors{{File: file, Msg: err.Error()}}
	}
	if len(doc.Content) == 0 {
		return nil, Errors{{File: file, Msg: "the file is empty"}}
	}
	r := &reader{file: file}
	d := &Declaration{}
	r.declaration(d, doc.Content[0])
	if len(r.errs) > 0 {
		sort.SliceStable(r.errs, func(i, j int) bool { return r.errs[i].Line < r.errs[j].Line })
		return nil, r.errs
	}
	return d, nil
}

type reader struct {
	file string
	errs Errors
}

func (r *reader) errf(n *yaml.Node, key, format string, args ...any) {
	r.errs = append(r.errs, Error{File: r.file, Line: n.Line, Key: key, Msg: fmt.Sprintf(format, args...)})
}

type entry struct {
	key  string
	path string // the key's dotted path from the root
	k, v *yaml.Node
}

func join(key, name string) string {
	if key == "" {
		return name
	}
	return key + "." + name
}

// entries are a mapping's keys and values in file order. With allowed, a
// key outside it is an error. A key YAML reads as a boolean, a key that is
// not a string and a key given twice are errors always, and such an entry
// is left out.
func (r *reader) entries(n *yaml.Node, key string, allowed ...string) []entry {
	if n.Kind != yaml.MappingNode {
		r.errf(n, key, "want a mapping (key: value), got %s", describe(n))
		return nil
	}
	var out []entry
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := join(key, k.Value)
		switch {
		case booleanLike(k):
			r.errf(k, p, "YAML reads the bare key %s as a boolean, not a name: rename it, or quote it (%q)", k.Value, k.Value)
		case k.Kind != yaml.ScalarNode || k.Tag != "!!str":
			r.errf(k, p, "a key must be a string, got %s", describe(k))
		case seen[k.Value]:
			r.errf(k, p, "given twice")
		case len(allowed) > 0 && !slices.Contains(allowed, k.Value):
			r.errf(k, p, "unknown key (known here: %s)", strings.Join(allowed, ", "))
		default:
			seen[k.Value] = true
			out = append(out, entry{key: k.Value, path: p, k: k, v: v})
		}
	}
	return out
}

func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias (not read in a declaration)"
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return "nothing"
		}
		if n.Tag == "!!str" {
			return strconv.Quote(n.Value)
		}
		return strings.TrimPrefix(n.Tag, "!!") + " " + n.Value
	}
	return "something else"
}

func (r *reader) str(n *yaml.Node, key string) (string, bool) {
	if booleanLike(n) {
		r.errf(n, key, "YAML reads the bare word %s as a boolean, not a string: quote it (%q)", n.Value, n.Value)
		return "", false
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Value == "" {
		r.errf(n, key, "want a string, got %s", describe(n))
		return "", false
	}
	return n.Value, true
}

func (r *reader) strs(n *yaml.Node, key string) []string {
	if n.Kind != yaml.SequenceNode {
		r.errf(n, key, "want a list ([a, b]), got %s", describe(n))
		return nil
	}
	var out []string
	for _, c := range n.Content {
		s, ok := r.str(c, key)
		if !ok {
			continue
		}
		if slices.Contains(out, s) {
			r.errf(c, key, "%s is listed twice", s)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *reader) boolean(n *yaml.Node, key string) (value, ok bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false") {
		r.errf(n, key, "want true or false, got %s", describe(n))
		return false, false
	}
	return n.Value == "true", true
}

// count reads a whole number from 1 to most.
func (r *reader) count(n *yaml.Node, key string, most int) (int, bool) {
	v, err := strconv.Atoi(n.Value)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || err != nil || v < 1 || v > most {
		r.errf(n, key, "want a whole number from 1 to %d, got %s", most, describe(n))
		return 0, false
	}
	return v, true
}

func (r *reader) declaration(d *Declaration, root *yaml.Node) {
	var calls, check, notify, hear, base, credential, idempotency *entry
	named := false
	for _, e := range r.entries(root, "", "name", "about", "base", "credential", "check", "idempotency", "calls", "notify", "hear", "limits", "thread", "link", "hook", "identity", "events") {
		switch e.key {
		case "name":
			named = true
			name, ok := r.str(e.v, e.path)
			switch {
			case !ok:
			case !validName.MatchString(name):
				r.errf(e.v, e.path, "name %q: use lowercase letters, digits, - and _, starting with a letter or digit", name)
			case slices.Contains(BuiltIn, name):
				r.errf(e.v, e.path, "%s is a source yardr has built in (%s): a declared source takes another name", name, strings.Join(BuiltIn, ", "))
			default:
				d.Name = name
			}
		case "about":
			d.About, _ = r.str(e.v, e.path)
		case "base":
			base = &e
			if s, ok := r.str(e.v, e.path); ok {
				d.Base = r.base(e.v, e.path, s)
			}
		case "credential":
			credential = &e
			d.Credential = r.credential(e)
		case "check":
			check = &e
		case "idempotency":
			idempotency = &e
		case "calls":
			calls = &e
		case "notify":
			notify = &e
		case "hear":
			hear = &e
		case "limits":
			d.Limits = r.limits(e)
		case "thread":
			d.Thread = r.thread(e)
		case "link":
			d.Link = r.link(e)
		case "hook":
			d.Hook = r.hook(e)
		case "identity":
			d.Identity = r.identity(e)
		case "events":
			d.Events = r.events(e)
		}
	}
	if root.Kind != yaml.MappingNode {
		return
	}
	if !named {
		r.errf(root, "name", "missing: what the source is called in this yard")
	}
	if calls == nil && (d.Limits != nil || d.Hook != nil) {
		// A source that is its limits command and/or what it sends in,
		// and nothing else: there is no request, so nothing a request
		// needs has a meaning.
		alone := "limits alone"
		if d.Hook != nil && d.Limits != nil {
			alone = "hook and limits alone"
		} else if d.Hook != nil {
			alone = "hook alone"
		}
		for _, e := range []*entry{base, credential, check, idempotency, notify, hear} {
			if e != nil {
				r.errf(e.k, e.path, "is for the calls of a source, and this file declares none (it has %s)", alone)
			}
		}
		d.Base, d.Credential = "", nil
		return
	}
	if base == nil {
		r.errf(root, "base", "missing: the address every call's path is under (https://…)")
	}
	if calls == nil {
		r.errf(root, "calls", "missing: a source is the calls it declares, or its limits command, or what it sends in (hook)")
		return
	}
	for _, e := range r.entries(calls.v, calls.path) {
		switch {
		case !validName.MatchString(e.key):
			r.errf(e.k, e.path, "name %q: use lowercase letters, digits, - and _, starting with a letter or digit", e.key)
		// The word a grant uses for every read call of a source.
		case e.key == Read:
			r.errf(e.k, e.path, "a call cannot be named %s: the word stands for every read call of a source", Read)
		default:
			inPath := d.Credential != nil && d.Credential.InPath
			d.Calls = append(d.Calls, r.call(e, d.Credential != nil, inPath))
		}
	}
	if calls.v.Kind == yaml.MappingNode && len(calls.v.Content) == 0 {
		r.errf(calls.v, calls.path, "no call: a source is the calls it declares")
	}
	if check != nil {
		name, ok := r.str(check.v, check.path)
		c, found := d.Call(name)
		switch {
		case !ok:
		case !found:
			r.errf(check.v, check.path, "%s is no call of this file", name)
		case c.Kind != Read:
			r.errf(check.v, check.path, "%s is an update: the check is a read call, made to see that the source answers", name)
		case slices.ContainsFunc(c.Params, func(p Param) bool { return !p.Optional }):
			r.errf(check.v, check.path, "%s needs a parameter: the check is a call without any", name)
		default:
			d.Check = name
		}
	}
	if idempotency != nil {
		d.Idempotency = r.idempotency(d, *idempotency)
	}
	if notify != nil {
		d.Notify = r.notify(d, *notify)
	}
	if hear != nil {
		d.Hear = r.hear(d, *hear)
	}
}

// ownHeaders are the headers Do sets on every request itself.
var ownHeaders = []string{"Accept", "Content-Type", "User-Agent"}

// idempotency reads the name of the header an update's key goes in. What
// it refuses is a header that is another's already, the credential's above
// all, and a file whose calls are all reads, which no key is sent with.
func (r *reader) idempotency(d *Declaration, e entry) string {
	name, ok := r.str(e.v, e.path)
	if !ok {
		return ""
	}
	credential := ""
	if d.Credential != nil {
		credential, _, _ = strings.Cut(d.Credential.Header, ":")
	}
	switch {
	case !headerName.MatchString(name):
		r.errf(e.v, e.path, "%q is no name of a header: this is the header the service takes an update's key in (Idempotency-Key)", name)
	case credential != "" && strings.EqualFold(name, credential):
		r.errf(e.v, e.path, "%s is the header the credential is sent in", name)
	case slices.ContainsFunc(ownHeaders, func(h string) bool { return strings.EqualFold(h, name) }):
		r.errf(e.v, e.path, "%s is a header every call is sent with already (%s)", name, strings.Join(ownHeaders, ", "))
	case !slices.ContainsFunc(d.Calls, func(c Call) bool { return c.Kind == Update }):
		r.errf(e.v, e.path, "the file has no %s call, and a key is sent with those alone", Update)
	default:
		return name
	}
	return ""
}

// command reads one command of a declaration: the program and its
// arguments, a list of strings run without a shell, so each word is one
// element. example shows the list's shape in an error (`"{provider}",
// --json`). A program with no / at its start is a file beside the
// declaration's file; one with a '.' or '..' segment would leave it.
func (r *reader) command(f entry, example string) []string {
	if f.v.Kind != yaml.SequenceNode {
		r.errf(f.v, f.path, "want a list of strings ([program, %s]), got %s: the command is run without a shell, so each word is one element", example, describe(f.v))
		return nil
	}
	var out []string
	whole := true
	for _, c := range f.v.Content {
		s, ok := r.str(c, f.path)
		whole = whole && ok
		out = append(out, s)
	}
	switch {
	case !whole:
		return nil
	case len(out) == 0:
		r.errf(f.v, f.path, "an empty list: want the program and its arguments")
		return nil
	case hasDots(out[0]):
		r.errf(f.v, f.path, "the program %q has a '.' or '..' segment: one with no / at its start is a file beside the declaration's file, and stays under it", out[0])
		return nil
	}
	return out
}

// limits reads the key that makes a source one that says what a harness
// kind's subscription still carries. What it refuses is what would fail at
// every ask, or would ask something else than the file reads as: a command
// that is no list of words, one with no place for the provider's name, a
// kind herdr starts no agent of.
func (r *reader) limits(e entry) *Limits {
	l := &Limits{Kinds: map[string]string{}}
	commanded, kinded := false, false
	for _, f := range r.entries(e.v, e.path, "command", "kinds") {
		switch f.key {
		case "command":
			commanded = true
			l.Command = r.command(f, strconv.Quote(ProviderMark)+", --json")
			switch {
			case l.Command == nil:
			case strings.Contains(l.Command[0], ProviderMark):
				r.errf(f.v, f.path, "%s in the program: it is the same program for every kind, and the provider's name is an argument of it", ProviderMark)
			case !slices.ContainsFunc(l.Command[1:], func(a string) bool { return strings.Contains(a, ProviderMark) }):
				r.errf(f.v, f.path, "no %s among the arguments: it is where the provider's name for the kind asked about goes", ProviderMark)
			}
		case "kinds":
			kinded = true
			kinds := r.entries(f.v, f.path)
			for _, k := range kinds {
				name, ok := r.str(k.v, k.path)
				switch {
				case !agents.IsKind(k.key):
					r.errf(k.k, k.path, "%s is no harness kind herdr starts (it starts: %s)", k.key, strings.Join(agents.HerdrKinds, ", "))
				case !ok:
				case !providerName.MatchString(name):
					r.errf(k.v, k.path, "%q is no name of a provider: use letters, digits, '.', '-' and '_', starting with a letter or digit", name)
				default:
					l.Kinds[k.key] = name
				}
			}
			if f.v.Kind == yaml.MappingNode && len(f.v.Content) == 0 {
				r.errf(f.v, f.path, "no kind: the source answers for the harness kinds listed here (kimi: kimi)")
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return l
	}
	if !commanded {
		r.errf(e.v, join(e.path, "command"), "missing: the program that says the limits and its arguments, as a list with %s in it", ProviderMark)
	}
	if !kinded {
		r.errf(e.v, join(e.path, "kinds"), "missing: the harness kinds the source answers for, each with the provider's own name for it (kimi: kimi)")
	}
	return l
}

// thread reads the keys that name the commands by which the yard speaks in
// the source's threads. Refused is what would fail at every run: a command
// that is no list of strings or has no program, an open without the place a
// thread is opened in, a state without the words it takes, an assignee
// that is no part of an identity or has no open to take it.
func (r *reader) thread(e entry) *Thread {
	t := &Thread{}
	var place, states, assignee *entry
	for _, f := range r.entries(e.v, e.path, "reply", "posted", "read", "open", "opened", "state", "place", "states", "labels", "assignee") {
		switch f.key {
		case "reply":
			t.Reply = r.command(f, "linear/thread, reply")
		case "posted":
			t.Posted = r.command(f, "linear/thread, posted")
		case "read":
			t.Read = r.command(f, "linear/thread, read")
		case "open":
			t.Open = r.command(f, "linear/thread, open")
		case "opened":
			t.Opened = r.command(f, "linear/thread, opened")
		case "state":
			t.State = r.command(f, "linear/thread, state")
		case "place":
			place = &f
			if s, ok := r.str(f.v, f.path); ok && !validName.MatchString(s) {
				r.errf(f.v, f.path, "%q: one word: what a place to open a thread in is called in this source (team)", s)
			} else if ok {
				t.Place = s
			}
		case "states":
			states = &f
			for _, s := range r.strs(f.v, f.path) {
				if !slices.Contains(threadStates, s) {
					r.errf(f.v, f.path, "%s is no state of a thread (%s)", s, strings.Join(threadStates, ", "))
					continue
				}
				t.States = append(t.States, s)
			}
		case "labels":
			t.Labels, _ = r.boolean(f.v, f.path)
		case "assignee":
			assignee = &f
			if s, ok := r.str(f.v, f.path); ok && s != AssigneeID && s != AssigneeKey {
				r.errf(f.v, f.path, "%q: %s or %s: which part of a person's identity in the source thread.open takes as assignee", s, AssigneeID, AssigneeKey)
			} else if ok {
				t.Assignee = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return t
	}
	if t.Open != nil && place == nil {
		r.errf(e.v, join(e.path, "place"), "missing: what a place to open a thread in is called in this source (team); thread.open needs it")
	}
	if assignee != nil && t.Open == nil {
		r.errf(assignee.v, assignee.path, "thread.open is missing: the assignee is whom the thread it opens is assigned to")
	}
	// A states given but with no word the yard knows is refused above, word
	// by word; an empty one is as good as none.
	statesGiven := states != nil && (states.v.Kind != yaml.SequenceNode || len(states.v.Content) > 0)
	if t.State != nil && !statesGiven {
		r.errf(e.v, join(e.path, "states"), "missing: the words thread.state takes, of %s", strings.Join(threadStates, ", "))
	}
	return t
}

// threadStates are the words a thread.state takes.
var threadStates = []string{"started", "review", "done"}

// hostName is the host of the source's issue URLs (linear.app).
var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

// groupRefAt is a group of a link template at where it stands: {1}, {2}.
var groupRefAt = regexp.MustCompile(`^\{([0-9]+)\}`)

// link reads the keys that make the source's issues a bead's link: data,
// not a command. Refused is what would fail at every link made: a host that
// is none, an expression that does not compile, a template naming a group
// the expression has not, a fold that is none, a pull shape over other
// groups than the issue's, a repos pattern that does not compile.
func (r *reader) link(e entry) *Link {
	l := &Link{}
	var host, issue, fixed, thread, pull, pullIssue *entry
	for _, f := range r.entries(e.v, e.path, "host", "issue", "fixed", "thread", "fold", "pull", "repos") {
		switch f.key {
		case "host":
			host = &f
			if s, ok := r.str(f.v, f.path); ok && !hostName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no host of the source's issue URLs (linear.app)", s)
			} else if ok {
				l.Host = s
			}
		case "issue":
			issue = &f
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				l.Issue, l.re = s, re
			}
		case "fixed":
			fixed = &f
			l.Fixed, _ = r.str(f.v, f.path)
		case "thread":
			thread = &f
			l.Thread, _ = r.str(f.v, f.path)
		case "fold":
			if s, ok := r.str(f.v, f.path); ok && s != FoldLower {
				r.errf(f.v, f.path, "%q: want %s", s, FoldLower)
			} else if ok {
				l.Fold = s
			}
		case "pull":
			pull = &f
			l.Pull, pullIssue = r.linkPull(f, l)
		case "repos":
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			if _, err := regexp.Compile(s); err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				l.Repos = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return l
	}
	if host == nil {
		r.errf(e.v, join(e.path, "host"), "missing: the host the source's issue URLs are on (linear.app)")
	}
	if issue == nil {
		r.errf(e.v, join(e.path, "issue"), "missing: the regular expression that reads an issue from a URL's path")
	}
	if fixed == nil {
		r.errf(e.v, join(e.path, "fixed"), "missing: the issue's one fixed form, a template over the expression's groups (https://linear.app/{1}/issue/{2})")
	}
	if thread == nil {
		r.errf(e.v, join(e.path, "thread"), "missing: the id a thread command takes for the issue, a template over the expression's groups ({2})")
	}
	if l.re != nil {
		if fixed != nil && l.Fixed != "" {
			r.linkTemplate(fixed.v, fixed.path, l.Fixed, l.re, "link.issue")
		}
		if thread != nil && l.Thread != "" {
			r.linkTemplate(thread.v, thread.path, l.Thread, l.re, "link.issue")
		}
	}
	// The pull shape's groups are the issue's groups: one names the
	// other, so their counts are one. The file may name pull first, so
	// the count is checked here, with both expressions read.
	if pull != nil && pullIssue != nil && l.re != nil && l.pullRe != nil &&
		l.pullRe.NumSubexp() != l.re.NumSubexp() {
		r.errf(pullIssue.v, pullIssue.path, "has %d groups, want the %d of link.issue, in the same order",
			l.pullRe.NumSubexp(), l.re.NumSubexp())
	}
	if pull != nil && pullIssue != nil && l.pullRe != nil && l.pullRe.NumSubexp() < 2 {
		r.errf(pullIssue.v, pullIssue.path, "has %d groups, want at least 2: one or more repository path segments, then the pull request number",
			l.pullRe.NumSubexp())
	}
	return l
}

// linkPull reads the source's second thread shape, its pull requests: a
// pattern with its own fixed and thread templates, over the same groups
// in the same order. It returns the shape and its issue entry: the
// group count is checked by link, with both expressions read. l carries
// the compiled form, as it carries the issue's.
func (r *reader) linkPull(e entry, l *Link) (*PullShape, *entry) {
	p := &PullShape{}
	var issue, fixed, thread *entry
	for _, f := range r.entries(e.v, e.path, "issue", "fixed", "thread") {
		switch f.key {
		case "issue":
			issue = &f
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(f.v, f.path, "%v", err)
			} else {
				p.Issue, l.pullRe = s, re
			}
		case "fixed":
			fixed = &f
			p.Fixed, _ = r.str(f.v, f.path)
		case "thread":
			thread = &f
			p.Thread, _ = r.str(f.v, f.path)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return p, issue
	}
	if issue == nil {
		r.errf(e.v, join(e.path, "issue"), "missing: the regular expression that reads a pull request from a URL's path")
	}
	if fixed == nil {
		r.errf(e.v, join(e.path, "fixed"), "missing: the pull request's one fixed form, a template over the expression's groups (https://forge.example/{1}/{2}/pull/{3})")
	}
	if thread == nil {
		r.errf(e.v, join(e.path, "thread"), "missing: the id a thread command takes for the pull request, a template over the expression's groups ({1}/{2}#{3})")
	}
	if l.pullRe != nil {
		if fixed != nil && p.Fixed != "" {
			r.linkTemplate(fixed.v, fixed.path, p.Fixed, l.pullRe, "link.pull.issue")
		}
		if thread != nil && p.Thread != "" {
			r.linkTemplate(thread.v, thread.path, p.Thread, l.pullRe, "link.pull.issue")
		}
	}
	return p, issue
}

// linkTemplate refuses a {n} of s that is no group of re: n counts from 1,
// as the groups of the expression expr do.
func (r *reader) linkTemplate(n *yaml.Node, key, s string, re *regexp.Regexp, expr string) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		m := groupRefAt.FindStringSubmatch(s[i:])
		if m == nil {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				end = len(s) - i
			}
			r.errf(n, key, "%s is no group of a template: a group is {1}, {2}, of the expression %s", s[i:i+end+1], expr)
			continue
		}
		g, _ := strconv.Atoi(m[1])
		if g < 1 || g > re.NumSubexp() {
			r.errf(n, key, "%s names a group %s has not (it has %d)", m[0], expr, re.NumSubexp())
		}
		i += len(m[0]) - 1
	}
}

// identity reads how a crew member's keys in the source fold: data, not a
// command. An identity key that says nothing is refused: fold is what it
// is for.
func (r *reader) identity(e entry) *Identity {
	id := &Identity{}
	var fold *entry
	for _, f := range r.entries(e.v, e.path, "fold") {
		fold = &f
		if s, ok := r.str(f.v, f.path); ok && s != FoldLower {
			r.errf(f.v, f.path, "%q: want %s", s, FoldLower)
		} else if ok {
			id.Fold = s
		}
	}
	if e.v.Kind == yaml.MappingNode && fold == nil {
		r.errf(e.v, join(e.path, "fold"), "missing: %s", FoldLower)
	}
	return id
}

// hook reads what the source sends in: how the core verifies its
// webhooks, the command that turns a verified body into the envelope, and
// the keys its events' fields may carry. Refused is what would fail at
// every delivery: no way to verify one, a command that is no list of
// strings or has no program, a timestamp in two places or none, a delivery
// id in two places, and bodies that may come again with no delivery id to
// tell a retry by.
func (r *reader) hook(e entry) *Hook {
	h := &Hook{}
	var verify, bodies, normalize *entry
	var deliveryNamed bool
	for _, f := range r.entries(e.v, e.path, "verify", "bodies", "normalize", "keys") {
		switch f.key {
		case "verify":
			verify = &f
			h.Verify, deliveryNamed = r.hookVerify(f)
		case "bodies":
			bodies = &f
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(bodiesKinds, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(bodiesKinds, ", "))
			} else if ok {
				h.Bodies = s
			}
		case "normalize":
			normalize = &f
			h.Normalize = r.command(f, "linear/normalize")
		case "keys":
			for _, s := range r.strs(f.v, f.path) {
				if !hookKey.MatchString(s) {
					r.errf(f.v, f.path, "%q: a key is lowercase letters, digits and _, starting with a letter or digit (team)", s)
					continue
				}
				h.Keys = append(h.Keys, s)
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return h
	}
	if verify == nil {
		r.errf(e.v, join(e.path, "verify"), "missing: how the core verifies a delivery (scheme, signature, delivery)")
	}
	if bodies == nil {
		r.errf(e.v, join(e.path, "bodies"), "missing: %s", strings.Join(bodiesKinds, ", "))
	}
	if normalize == nil {
		r.errf(e.v, join(e.path, "normalize"), "missing: the command that turns a verified body into the envelope")
	}
	// A body that may come again is told from its retry by the service's
	// id alone. With hmac-sha256 a delivery is always named, so this is
	// for scheme secret, whose delivery is optional; a delivery that is
	// named but wrong is its own error, not this one.
	if bodies != nil && h.Bodies == BodiesRepeat && h.Verify.Scheme == HookSecret && !deliveryNamed {
		r.errf(bodies.v, bodies.path, "repeat: needs verify.delivery: without the service's id of the delivery a retry is a new delivery")
	}
	return h
}

// hookVerify reads how the core proves a delivery came from the source:
// HMAC-SHA256 over the body, or the secret itself in a header. Signed,
// encoding and prefix are for the signature alone; the delivery id is in a
// header or in the body, and a service that sends none says no delivery
// with scheme secret. It reports whether a delivery was named, for the
// bodies check in hook.
func (r *reader) hookVerify(e entry) (HookVerify, bool) {
	v := HookVerify{}
	var scheme, signed, signature, delivery *entry
	for _, f := range r.entries(e.v, e.path, "scheme", "signed", "signature", "delivery", "timestamp") {
		switch f.key {
		case "scheme":
			scheme = &f
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(hookSchemes, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(hookSchemes, ", "))
			} else if ok {
				v.Scheme = s
			}
		case "signed":
			signed = &f
		case "signature":
			signature = &f
		case "delivery":
			delivery = &f
		case "timestamp":
			v.Timestamp = r.hookTimestamp(f)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return v, delivery != nil
	}
	// Signed, signature and delivery are read after the scheme is known,
	// since what they may say depends on it.
	secret := v.Scheme == HookSecret
	if scheme == nil {
		r.errf(e.v, join(e.path, "scheme"), "missing: how a delivery is verified (%s)", strings.Join(hookSchemes, ", "))
	}
	if signed == nil {
		if !secret {
			r.errf(e.v, join(e.path, "signed"), "missing: what the signature covers (%s)", HookSigned)
		}
	} else if secret {
		r.errf(signed.v, signed.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
	} else if s, ok := r.str(signed.v, signed.path); ok && s != HookSigned {
		r.errf(signed.v, signed.path, "%q: the signature covers %s", s, HookSigned)
	} else if ok {
		v.Signed = s
	}
	if signature == nil {
		if secret {
			r.errf(e.v, join(e.path, "signature"), "missing: the header the secret arrives in ({ header })")
		} else {
			r.errf(e.v, join(e.path, "signature"), "missing: the header the signature arrives in ({ header, encoding, prefix })")
		}
	} else {
		v.Signature = r.hookSignature(*signature, secret)
	}
	if delivery == nil {
		if !secret {
			r.errf(e.v, join(e.path, "delivery"), "missing: where the service's id of the delivery arrives (header: its name, or body: a dot path into it)")
		}
	} else {
		v.Delivery = r.hookDelivery(*delivery)
	}
	return v, delivery != nil
}

// hookSignature reads where a delivery's signature arrives: the header, in
// hex, past the prefix. With scheme secret the header holds the secret
// itself, and encoding and prefix do not apply.
func (r *reader) hookSignature(e entry, secret bool) HookSignature {
	s := HookSignature{}
	var header, encoding *entry
	for _, f := range r.entries(e.v, e.path, "header", "encoding", "prefix") {
		switch f.key {
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the %s arrives in", name, signedWhat(secret))
			} else if ok {
				s.Header = name
			}
		case "encoding":
			encoding = &f
			if secret {
				r.errf(f.v, f.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
			} else if enc, ok := r.str(f.v, f.path); ok && enc != HookEncoding {
				r.errf(f.v, f.path, "%q: a signature arrives %s", enc, HookEncoding)
			} else if ok {
				s.Encoding = enc
			}
		case "prefix":
			if secret {
				r.errf(f.v, f.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
			} else if f.v.Kind == yaml.ScalarNode && f.v.Tag == "!!str" {
				// An explicit "" is no prefix, as leaving it out is.
				s.Prefix = f.v.Value
			} else {
				r.errf(f.v, f.path, "want a string, got %s", describe(f.v))
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return s
	}
	if header == nil {
		if secret {
			r.errf(e.v, join(e.path, "header"), "missing: the header the secret arrives in")
		} else {
			r.errf(e.v, join(e.path, "header"), "missing: the header the signature arrives in (Linear-Signature)")
		}
	}
	if encoding == nil && !secret {
		r.errf(e.v, join(e.path, "encoding"), "missing: how the signature arrives (%s)", HookEncoding)
	}
	return s
}

// signedWhat is what the signature header carries under the scheme: the
// signature, or the secret itself.
func signedWhat(secret bool) string {
	if secret {
		return "secret"
	}
	return "signature (Linear-Signature)"
}

// hookDelivery reads where the service's id of a delivery arrives: in a
// header, or in the body at the dot path.
func (r *reader) hookDelivery(e entry) HookDelivery {
	d := HookDelivery{}
	var header, body *entry
	for _, f := range r.entries(e.v, e.path, "header", "body") {
		switch f.key {
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the service's id of the delivery arrives in (Linear-Delivery)", name)
			} else if ok {
				d.Header = name
			}
		case "body":
			body = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (update_id)", s)
			} else if ok {
				d.Body = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return d
	}
	if header != nil && body != nil {
		r.errf(header.v, header.path, "a delivery id is in a header or in the body, not both")
	}
	if header == nil && body == nil {
		r.errf(e.v, join(e.path, "header"), "missing: where the service's id of the delivery arrives (header: its name, or body: a dot path into it)")
	}
	return d
}

// hookTimestamp reads when a delivery was sent: in the body at the dot
// path, or in a header, in ms or s, within window of the yard's clock.
func (r *reader) hookTimestamp(e entry) *HookTimestamp {
	t := &HookTimestamp{}
	var body, header, unit, window *entry
	for _, f := range r.entries(e.v, e.path, "body", "header", "unit", "window") {
		switch f.key {
		case "body":
			body = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (webhookTimestamp)", s)
			} else if ok {
				t.Body = s
			}
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the timestamp arrives in", name)
			} else if ok {
				t.Header = name
			}
		case "unit":
			unit = &f
			if s, ok := r.str(f.v, f.path); ok && s != HookMs && s != HookS {
				r.errf(f.v, f.path, "%q: want %s or %s", s, HookMs, HookS)
			} else if ok {
				t.Unit = s
			}
		case "window":
			window = &f
			if s, ok := r.str(f.v, f.path); ok {
				if d, err := time.ParseDuration(s); err != nil || d <= 0 {
					r.errf(f.v, f.path, "%q: a window is a positive duration (1m)", s)
				} else {
					t.Window = s
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return t
	}
	if body != nil && header != nil {
		r.errf(header.v, header.path, "a timestamp is in the body or in a header, not both")
	}
	if body == nil && header == nil {
		r.errf(e.v, join(e.path, "body"), "missing: where the timestamp arrives (body: a dot path into it, or header: its name)")
	}
	if unit == nil {
		r.errf(e.v, join(e.path, "unit"), "missing: %s or %s", HookMs, HookS)
	}
	if window == nil {
		r.errf(e.v, join(e.path, "window"), "missing: how far the timestamp may be from now (1m)")
	}
	return t
}

// events reads the keys that say what the source sends in about pull
// requests and CI, and the commands that read them back. Each stands
// alone: runs and head_from name envelope types without any command, and
// either command reads without the other. Refused is what would fail at
// every read: a type list that is no list of strings, a command that is
// no list of strings or has no program.
func (r *reader) events(e entry) *Events {
	ev := &Events{}
	for _, f := range r.entries(e.v, e.path, "runs", "head_from", "pr", "ci") {
		switch f.key {
		case "runs":
			ev.Runs = r.strs(f.v, f.path)
		case "head_from":
			ev.HeadFrom = r.strs(f.v, f.path)
		case "pr":
			ev.PR = r.command(f, "events/pr")
		case "ci":
			ev.CI = r.command(f, "events/ci")
		}
	}
	return ev
}

// hookPath reports whether s is a dot path into a JSON body: segments with
// something between the dots.
func hookPath(s string) bool {
	segs := strings.Split(s, ".")
	for _, seg := range segs {
		if seg == "" {
			return false
		}
	}
	return len(segs) > 0
}

// notify reads the key that makes a source one a user can be told through.
// What it refuses is what would fail at every send, or at some: a call that
// is not there or only reads, a value given to no parameter or to one that
// is no text, a required parameter nothing fills, and a link the call
// insists on, since most of what the yard tells has none.
func (r *reader) notify(d *Declaration, e entry) *Notify {
	n := &Notify{}
	var c *Call
	called, addressed := false, false
	fills := map[string]string{} // parameter -> the key that fills it
	// param reads the name of a parameter of the call that takes text.
	param := func(f entry) string {
		name, ok := r.str(f.v, f.path)
		if !ok || c == nil {
			return ""
		}
		p, found := c.param(name)
		switch {
		case !found:
			r.errf(f.v, f.path, "%s is no parameter of the call %s (it takes: %s)", name, c.Name, or(strings.Join(c.names(), ", "), "none"))
		case p.Type != "string":
			r.errf(f.v, f.path, "the parameter %s is %s: what the yard gives is text, so it is a string", name, p.Type)
		case fills[name] != "":
			r.errf(f.v, f.path, "the parameter %s is filled by %s already", name, fills[name])
		default:
			fills[name] = f.path
			return name
		}
		return ""
	}
	fields := r.entries(e.v, e.path, "call", "address", "title", "text", "link")
	// The call first, wherever the file has it: the others name its
	// parameters.
	for _, f := range fields {
		if f.key != "call" {
			continue
		}
		called = true
		name, ok := r.str(f.v, f.path)
		found, has := d.Call(name)
		switch {
		case !ok:
		case !has:
			r.errf(f.v, f.path, "%s is no call of this file", name)
		case found.Kind != Update:
			r.errf(f.v, f.path, "%s is a %s call: telling a person sends something, so it is an %s", name, Read, Update)
		default:
			n.Call, c = name, found
		}
	}
	for _, f := range fields {
		switch f.key {
		case "address":
			addressed = true
			var hasParam, hasEnv bool
			for _, g := range r.entries(f.v, f.path, "param", "env") {
				switch g.key {
				case "param":
					hasParam = true
					n.Address = param(g)
				case "env":
					hasEnv = true
					if s, ok := r.str(g.v, g.path); ok && !paramName.MatchString(s) {
						r.errf(g.v, g.path, "%q is no name of a variable: this is the name the address is under in a user's env file, never the address", s)
					} else if ok {
						n.AddressEnv = s
					}
				}
			}
			if f.v.Kind == yaml.MappingNode && !hasParam {
				r.errf(f.v, join(f.path, "param"), "missing: the parameter that takes where the person is reached")
			}
			if f.v.Kind == yaml.MappingNode && !hasEnv {
				r.errf(f.v, join(f.path, "env"), "missing: the name of the variable the address is in, in a user's env file")
			}
		case "title":
			n.Title = param(f)
		case "text":
			n.Text = param(f)
		case "link":
			n.Link = param(f)
			if p, ok := c.param(n.Link); ok && !p.Optional {
				r.errf(f.v, f.path, "the parameter %s is required, and most of what the yard tells has no link: make it optional: true", n.Link)
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return n
	}
	if !called {
		r.errf(e.v, join(e.path, "call"), "missing: the call that tells a person")
	}
	if !addressed {
		r.errf(e.v, join(e.path, "address"), "missing: { param: <the parameter that takes where the person is reached>, env: <its name in a user's env file> }")
	}
	if !slices.ContainsFunc(fields, func(f entry) bool { return f.key == "title" || f.key == "text" }) {
		r.errf(e.v, e.path, "neither title nor text: the call is given nothing to say")
	}
	if c != nil {
		for _, p := range c.Params {
			if !p.Optional && fills[p.Name] == "" {
				r.errf(e.v, e.path, "the call %s needs the parameter %s, and nothing here fills it (address, title, text and link are what the yard has)", c.Name, p.Name)
			}
		}
	}
	return n
}

// hear reads the key that makes a source one that hears only its told
// addresses back. What it refuses is what would fail at every delivery,
// or hear nothing: a key beside field, address and from, a field the
// envelope carries under no key the file names, an address that is not
// the one telling reads, and a from that is no role the yard has. Without
// a hook key there is no delivery to hear, and without a notify key no
// user is told through the source, so with neither is hear refused too.
func (r *reader) hear(d *Declaration, e entry) *Hear {
	h := &Hear{}
	var field, address, env *entry
	for _, f := range r.entries(e.v, e.path, "field", "address", "from") {
		switch f.key {
		case "field":
			field = &f
			if s, ok := r.str(f.v, f.path); ok && !hookKey.MatchString(s) {
				r.errf(f.v, f.path, "%q: a field is lowercase letters, digits and _, starting with a letter or digit (chat)", s)
			} else if ok {
				h.Field = s
			}
		case "address":
			address = &f
			for _, g := range r.entries(f.v, f.path, "env") {
				env = &g
				if s, ok := r.str(g.v, g.path); ok && !paramName.MatchString(s) {
					r.errf(g.v, g.path, "%q is no name of a variable: this is the name the address is under in a user's env file, never the address", s)
				} else if ok {
					h.Address.Env = s
				}
			}
		case "from":
			if s, ok := r.str(f.v, f.path); ok && s != HearFromOperator {
				r.errf(f.v, f.path, "%q: want %s", s, HearFromOperator)
			} else if ok {
				h.From = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return h
	}
	if field == nil {
		r.errf(e.v, join(e.path, "field"), "missing: the envelope field that must hold a told address (chat)")
	}
	if address == nil {
		r.errf(e.v, join(e.path, "address"), "missing: { env: <the name the address is under in a user's env file> }")
	} else if address.v.Kind == yaml.MappingNode && env == nil {
		r.errf(address.v, join(address.path, "env"), "missing: the name of the variable the address is in, in a user's env file")
	}
	if d.Hook == nil {
		r.errf(e.v, e.path, "needs hook: hear keeps the deliveries of a source, and this file has no hook key to receive any")
	} else if field != nil && h.Field != "" && !slices.Contains(d.Hook.Keys, h.Field) {
		r.errf(field.v, field.path, "%s is no key the envelope carries: hook.keys names %s", h.Field, or(strings.Join(d.Hook.Keys, ", "), "none"))
	}
	if d.Notify == nil {
		r.errf(e.v, e.path, "needs notify: hear keeps the deliveries from a told address, and this file has no notify key to tell any")
	} else if env != nil && h.Address.Env != "" && h.Address.Env != d.Notify.AddressEnv {
		r.errf(env.v, env.path, "%s is not the address telling reads: notify.address.env names %s, and hear reads the same file", h.Address.Env, d.Notify.AddressEnv)
	}
	return h
}

// base reads the address calls are under. Anything but https is refused,
// since the credential goes with every call; plain http is for a stand-in
// on this machine alone. Do holds a call to that again when it connects.
func (r *reader) base(n *yaml.Node, key, s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		r.errf(n, key, "%q is no address: want https://host/path", s)
	case u.User != nil:
		r.errf(n, key, "an address with a user or password in it: the credential is named under credential, never written here")
	case u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, "?#"):
		r.errf(n, key, "an address with a query or a fragment: a call's query is under its request")
	case u.Scheme == "http" && !loopbackHost(u.Hostname()):
		r.errf(n, key, "http:// is for a stand-in on this machine only (127.0.0.1, [::1], localhost): %s is not, so it is https:// or nothing", u.Hostname())
	case u.Scheme != "https" && u.Scheme != "http":
		r.errf(n, key, "%s:// is not https://", u.Scheme)
	case hasDots(u.Path) || strings.Contains(u.Path, "//") || strings.Contains(s, `\`):
		r.errf(n, key, "the path of an address has no '..', '//' or '\\'")
	default:
		return strings.TrimRight(s, "/")
	}
	return ""
}

// loopbackHost reports whether host, as written, is this machine's
// loopback: an address of it, or the name every system gives it.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hasDots reports whether a segment of path is "." or "..".
func hasDots(path string) bool {
	return slices.ContainsFunc(strings.Split(path, "/"), func(s string) bool { return s == "." || s == ".." })
}

func (r *reader) credential(e entry) *Credential {
	c, has := &Credential{}, false
	var update, mint *entry
	for _, f := range r.entries(e.v, e.path, "env", "update_env", "header", "mint", "in_path") {
		switch f.key {
		case "env":
			has = true
			if s, ok := r.str(f.v, f.path); ok && !paramName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no name of a variable: this is the name the credential is under in the source's env file, never the credential", s)
			} else if ok {
				c.Env = s
			}
		case "update_env":
			update = &f
			if s, ok := r.str(f.v, f.path); ok && !paramName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no name of a variable: this is the name the credential of an update call is under in the source's env file, never the credential", s)
			} else if ok {
				c.UpdateEnv = s
			}
		case "mint":
			mint = &f
			c.Mint = r.mintNames(f)
		case "in_path":
			if v, ok := r.boolean(f.v, f.path); ok {
				c.InPath = v
			}
		case "header":
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			name, value, cut := strings.Cut(s, ":")
			marks := mark.FindAllString(value, -1)
			switch {
			case !cut || !headerName.MatchString(name) || strings.TrimSpace(value) == "":
				r.errf(f.v, f.path, `want "Name: text %s", got %q`, CredentialMark, s)
			case strings.ContainsFunc(value, func(c rune) bool { return c < ' ' || c == 0x7f }):
				r.errf(f.v, f.path, "a header is one line")
			case len(marks) != 1 || marks[0] != CredentialMark:
				r.errf(f.v, f.path, "a header is fixed text and %s, once: a parameter never reaches a header", CredentialMark)
			default:
				c.Header = name + ": " + strings.TrimSpace(value)
			}
		}
	}
	if e.v.Kind == yaml.MappingNode && !has {
		r.errf(e.v, join(e.path, "env"), "missing: the name of the variable the credential is in")
	}
	// The same name twice says there are two credentials where there is one.
	if update != nil && c.UpdateEnv != "" && c.UpdateEnv == c.Env {
		r.errf(update.v, update.path, "%s is env already: update_env is for a second credential, which the update calls are sent with. Leave it out for a source with one", c.Env)
		c.UpdateEnv = ""
	}
	// A mint's name cannot be the variable the minted token is handed
	// under: the mint's inputs are beside the credential, never it.
	if mint != nil {
		for _, m := range c.Mint {
			switch m {
			case c.Env:
				r.errf(mint.v, mint.path, "%s is env already: mint names the variables the token is minted from, beside the variable the token is handed under", m)
			case c.UpdateEnv:
				r.errf(mint.v, mint.path, "%s is update_env already: mint names the variables the token is minted from, beside the variables a call is sent with", m)
			}
		}
	}
	return c
}

// mintNames reads the names of the variables that together select a minted
// credential: a list of one or more, each a name of a variable, never a
// value. env stays required beside them: it is the variable the minted
// token is handed under.
func (r *reader) mintNames(e entry) []string {
	if e.v.Kind == yaml.SequenceNode && len(e.v.Content) == 0 {
		r.errf(e.v, e.path, "missing: the names of the variables that together select a minted credential")
		return nil
	}
	var out []string
	for _, s := range r.strs(e.v, e.path) {
		if !paramName.MatchString(s) {
			r.errf(e.v, e.path, "%q is no name of a variable: these are the names the mint's inputs are under in the source's env file, never the values", s)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *reader) call(e entry, credential, inPath bool) Call {
	c := Call{Name: e.key, Answer: Answer{Max: AnswerMax}}
	var request *entry
	kind := false
	for _, f := range r.entries(e.v, e.path, "kind", "about", "params", "request", "answer") {
		switch f.key {
		case "kind":
			kind = true
			if s, ok := r.str(f.v, f.path); ok && s != Read && s != Update {
				r.errf(f.v, f.path, "%q: want %s or %s", s, Read, Update)
			} else if ok {
				c.Kind = s
			}
		case "about":
			c.About, _ = r.str(f.v, f.path)
		case "params":
			for _, p := range r.entries(f.v, f.path) {
				switch {
				case !paramName.MatchString(p.key):
					r.errf(p.k, p.path, "name %q: use letters, digits and _, not starting with a digit", p.key)
				case p.key == "credential":
					r.errf(p.k, p.path, "a parameter cannot be named credential: %s is the source's own", CredentialMark)
				default:
					c.Params = append(c.Params, r.param(p))
				}
			}
		case "request":
			request = &f
		case "answer":
			for _, a := range r.entries(f.v, f.path, "select", "max") {
				switch a.key {
				case "select":
					c.Answer.Select = r.strs(a.v, a.path)
				case "max":
					if n, ok := r.count(a.v, a.path, AnswerLimit); ok {
						c.Answer.Max = n
					}
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return c
	}
	if !kind {
		r.errf(e.v, join(e.path, "kind"), "missing: %s or %s", Read, Update)
	}
	if request == nil {
		r.errf(e.v, join(e.path, "request"), "missing: the method and the path the call sends")
		return c
	}
	r.request(&c, *request, credential, inPath)
	return c
}

func (r *reader) param(e entry) Param {
	p := Param{Name: e.key}
	typed := false
	var pattern, most, values *entry
	for _, f := range r.entries(e.v, e.path, "type", "pattern", "max", "values", "optional", "log") {
		switch f.key {
		case "type":
			typed = true
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(types, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(types, ", "))
			} else if ok {
				p.Type = s
			}
		case "pattern":
			pattern = &f
		case "max":
			most = &f
		case "values":
			values = &f
		case "optional":
			p.Optional, _ = r.boolean(f.v, f.path)
		case "log":
			p.Log, _ = r.boolean(f.v, f.path)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return p
	}
	if !typed {
		r.errf(e.v, join(e.path, "type"), "missing: %s", strings.Join(types, ", "))
	}
	if p.Type == "" {
		return p
	}
	// only refuses a key under a type it says nothing of.
	only := func(f *entry, typ string) bool {
		if f != nil && p.Type != typ {
			r.errf(f.k, f.path, "is for a parameter of type %s, and this one is %s", typ, p.Type)
			return false
		}
		return f != nil
	}
	if only(pattern, "string") {
		if s, ok := r.str(pattern.v, pattern.path); ok {
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(pattern.v, pattern.path, "%v", err)
			} else {
				p.Pattern, p.re = s, re
			}
		}
	}
	if only(most, "string") {
		p.Max, _ = r.count(most.v, most.path, AnswerLimit)
	}
	if only(values, "enum") {
		p.Values = r.strs(values.v, values.path)
	}
	if p.Type == "enum" && len(p.Values) == 0 && values == nil {
		r.errf(e.v, join(e.path, "values"), "missing: what an enum may be ([a, b])")
	} else if p.Type == "enum" && len(p.Values) == 0 && values.v.Kind == yaml.SequenceNode && len(values.v.Content) == 0 {
		r.errf(values.v, values.path, "an enum that may be nothing")
	}
	return p
}

func (r *reader) request(c *Call, e entry, credential, inPath bool) {
	method, path := false, false
	// marks checks the {name}s of a string that stands at where (a path,
	// a query): each a parameter of the call and none optional, and the
	// credential only in a path the file says takes it (in_path).
	marks := func(n *yaml.Node, key, s, where string) bool {
		ok := true
		for _, m := range mark.FindAllStringSubmatch(s, -1) {
			p, declared := c.param(m[1])
			switch {
			case m[0] == CredentialMark && where == "a path" && inPath:
				continue
			case m[0] == CredentialMark:
				msg := "%s in %s: a credential there is in the logs of every server on the way. It goes in the credential's header, or as a leaf of the body"
				if where == "a path" {
					msg += "; a service that takes it in the path says so (credential.in_path: true)"
				}
				r.errf(n, key, msg, CredentialMark, where)
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Optional && where == "a path":
				r.errf(n, key, "%s is optional, and a path has no place for a value that is not given", m[0])
			case p.Optional && s != m[0]:
				r.errf(n, key, "%s is optional: it stands alone as a value, which is left out when it is not given", m[0])
			default:
				continue
			}
			ok = false
		}
		return ok
	}
	for _, f := range r.entries(e.v, e.path, "method", "path", "query", "body") {
		switch f.key {
		case "method":
			method = true
			s, ok := r.str(f.v, f.path)
			switch {
			case !ok:
			case !slices.Contains(methods, s):
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(methods, ", "))
			case c.Kind == Read && s != "GET" && s != "POST":
				r.errf(f.v, f.path, "%s in a call of kind %s: %s changes what it is sent to, so the call is an %s", s, Read, s, Update)
			default:
				c.Request.Method = s
			}
		case "path":
			path = true
			s, ok := r.str(f.v, f.path)
			switch {
			case !ok:
			case strings.Contains(s, "://") || strings.HasPrefix(s, "//"):
				r.errf(f.v, f.path, "%q is an address, not a path: a call goes to the source's base and nowhere else", s)
			case !strings.HasPrefix(s, "/"):
				r.errf(f.v, f.path, "%q: a path begins with /", s)
			case hasDots(s):
				r.errf(f.v, f.path, "%q has a '.' or '..' segment, which leaves the source's base", s)
			case strings.ContainsAny(s, "?#\\") || strings.ContainsFunc(s, func(c rune) bool { return c <= ' ' || c == 0x7f }):
				r.errf(f.v, f.path, "%q: a path has no '?', '#', '\\' or space (a query is under query)", s)
			case marks(f.v, f.path, s, "a path"):
				c.Request.Path = s
			}
		case "query":
			for _, q := range r.pairs(f.v, f.path) {
				if mark.MatchString(q.key) {
					r.errf(q.k, q.path, "a key is fixed text: a parameter is a value")
				} else if marks(q.v, q.path, q.v.Value, "a query") {
					c.Request.Query = append(c.Request.Query, QueryValue{Key: q.key, Value: q.v.Value})
				}
			}
		case "body":
			c.Request.Body = r.body(c, f.v, f.path, credential)
			// Left out of a mapping or a list, an optional parameter that is
			// not given leaves a body. As the whole body it leaves none to
			// send, and what went instead was null.
			if m := mark.FindStringSubmatch(f.v.Value); f.v.Kind == yaml.ScalarNode && m != nil && m[0] == f.v.Value {
				if p, ok := c.param(m[1]); ok && p.Optional {
					r.errf(f.v, f.path, "%s is optional, and it is the whole body: not given, there is no body to leave it out of. Put it in a mapping ({ key: %q }), or make it required", m[0], m[0])
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return
	}
	if !method {
		r.errf(e.v, join(e.path, "method"), "missing: %s", strings.Join(methods, ", "))
	}
	if !path {
		r.errf(e.v, join(e.path, "path"), "missing: the path under the source's base")
	}
}

// pairs are a query's keys and values: any text for a key (filter[name]),
// one line of text, a number or true or false for a value.
func (r *reader) pairs(n *yaml.Node, key string) []entry {
	if n.Kind != yaml.MappingNode {
		r.errf(n, key, "want a mapping (key: value), got %s", describe(n))
		return nil
	}
	var out []entry
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := join(key, k.Value)
		switch {
		case k.Kind != yaml.ScalarNode || k.Value == "":
			r.errf(k, p, "a key must be a string, got %s", describe(k))
		case seen[k.Value]:
			r.errf(k, p, "given twice")
		case v.Kind != yaml.ScalarNode || v.Tag == "!!null":
			r.errf(v, p, "want one value, got %s", describe(v))
		default:
			seen[k.Value] = true
			out = append(out, entry{key: k.Value, path: p, k: k, v: v})
		}
	}
	return out
}

// body reads a request's body into what encoding/json sends: maps, lists
// and scalars. Its {name}s are checked here, so that Do finds nothing in a
// body it cannot fill.
func (r *reader) body(c *Call, n *yaml.Node, key string, credential bool) any {
	switch n.Kind {
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := join(key, k.Value)
			switch {
			case k.Kind != yaml.ScalarNode:
				r.errf(k, p, "a key must be a string, got %s", describe(k))
			case mark.MatchString(k.Value):
				r.errf(k, p, "a key is fixed text: a parameter or the credential is a value")
			default:
				if _, twice := out[k.Value]; twice {
					r.errf(k, p, "given twice")
				}
				out[k.Value] = r.body(c, v, p, credential)
			}
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, v := range n.Content {
			out = append(out, r.body(c, v, key, credential))
		}
		return out
	case yaml.ScalarNode:
		if n.Tag != "!!str" {
			var v any
			if err := n.Decode(&v); err != nil {
				r.errf(n, key, "%v", err)
			}
			return v
		}
		for _, m := range mark.FindAllStringSubmatch(n.Value, -1) {
			p, declared := c.param(m[1])
			switch {
			case m[0] == CredentialMark && n.Value != m[0]:
				r.errf(n, key, "%s is a whole value of a body, not a part of one", CredentialMark)
			case m[0] == CredentialMark && !credential:
				r.errf(n, key, "%s, and the file names no credential", CredentialMark)
			case m[0] == CredentialMark:
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Optional && n.Value != m[0]:
				r.errf(n, key, "%s is optional: it stands alone as a value, which is left out when it is not given", m[0])
			}
		}
		return n.Value
	}
	r.errf(n, key, "want a value, a mapping or a list, got %s", describe(n))
	return nil
}
