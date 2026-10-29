// The depot-push card (yardr-zbcp): `yardr bead ask <bead> "<q>"
// --depot-push <depot>` and the same with --depot-push-off turn a
// depot's push to its origin on or off as one decision, carried out by
// the yard's server on approval. It is not a --run row (the git half is
// no yardr command) and not a setup card (a depot is linked after
// setup, and the turn-on is per depot). Which source's credential a
// push answers with is the pack's to say: the declaration that gives
// git a credential helper (its git key), the host its link key names,
// and the variable its credential key names are read from the source
// whose host the depot's origin is on, never named here.
//
// On approval the server makes both writes: `YARDR_PUSH=origin` in the
// cmd of the group `<depot>-assembly`, and that source's credential
// helper in that depot's git config alone, through `git -C <depot>
// config`. The off card is the exact reverse. Nothing here runs
// `git push`, and no token stands in the card, the cmd, the config or
// a log: the config holds only the helper's path, and the helper reads
// the token from the source's env file at push time.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// AskDepotPush is the depot-push a depot-push ask proposes: the
// depot, whether the card turns push off, and the yard's card for it.
// Its writer sends Depot and Off alone; the rest is the yard's, written
// in the write that stores the ask (askedDepotPush) whatever the
// writer sent for it.
type AskDepotPush struct {
	Depot string `json:"depot"`
	// Off turns push off; absent turns it on.
	Off bool `json:"off,omitempty"`
	// Does are the yard's sentences for what approving changes and what
	// is there now, and Back the filing that takes it back.
	Does   []string          `json:"does,omitempty"`
	Back   string            `json:"back,omitempty"`
	BackBy string            `json:"back_by,omitempty"`
	Seen   map[string]string `json:"seen,omitempty"`
}

// TakenBack is the card's line for the filing that takes it back.
func (g AskDepotPush) TakenBack() string {
	return or(g.BackBy, "anyone") + " can take it back: " + g.Back
}

// DepotPushAsked is the push a depot-push ask proposes, as a client
// shows it.
type DepotPushAsked struct {
	Depot  string   `json:"depot"`
	Off    bool     `json:"off,omitempty"`
	Does   []string `json:"does"`
	Back   string   `json:"back"`
	BackBy string   `json:"back_by,omitempty"`
}

func (a Ask) depotPushAsked() *DepotPushAsked {
	if a.DepotPush == nil {
		return nil
	}
	return &DepotPushAsked{Depot: a.DepotPush.Depot, Off: a.DepotPush.Off,
		Does: a.DepotPush.Does, Back: a.DepotPush.Back, BackBy: a.DepotPush.BackBy}
}

const depotPushOnValue = "origin"

// DepotPushSource is the source a depot's push answers with, as its
// declaration says it: the host of its link key, the helper of its git
// key as the import copies it under home, the git config key for that
// host, and the variable of its credential key. Nothing here names a
// source, a host or a variable: each comes from the pack's file. The
// server reads it through DepotPushSourceFor as the store does.
type DepotPushSource struct {
	Name        string // the source's name, as its file gives it
	Host        string // the host the source's repositories are on
	Helper      string // the helper's copy, the value git config holds
	HelperKey   string // credential.https://<host>.helper
	TokenName   string // the variable the credential is in
	EnvFile     string // where the yard keeps that variable, "" for the default
	fingerprint string // the source and its command copies, held inside the yard
}

// DepotPushSourceFingerprint is the source identity a carrying record holds
// across approval: the declaration, token file and command-copy inventory.
func DepotPushSourceFingerprint(ps DepotPushSource) string {
	if ps.fingerprint != "" {
		return ps.fingerprint
	}
	return seen(ps.Name, ps.Host, ps.Helper, ps.HelperKey, ps.TokenName, ps.EnvFile)
}

// pushHelper is the helper's value for git config from the declaration
// d of the source src in the yard home: the copy of the git key's
// program under <home>/sources/<src>/, with its arguments where the
// file names any.
func pushHelper(home string, src *Source, d *source.Declaration) string {
	c := source.Command{Key: "git.credential", Argv: d.Git.Credential}
	return c.Program(home, src.Name)
}

// pushSourcesOf are the sources that give git a credential, in the
// order given: each with a git helper, a link host and a credential.
func pushSourcesOf(sources []Source, home string) []DepotPushSource {
	var out []DepotPushSource
	for i := range sources {
		src := &sources[i]
		// Git config must hold only the copied program path; arguments can
		// carry a credential, so a card does not write them there.
		d, err := src.Declaration()
		if err != nil || d.Git == nil || len(d.Git.Credential) != 1 ||
			d.Link == nil || d.Link.Host == "" || d.Credential == nil || d.Credential.Env == "" {
			continue
		}
		out = append(out, DepotPushSource{
			Name:      src.Name,
			Host:      d.Link.Host,
			Helper:    pushHelper(home, src, d),
			HelperKey: "credential.https://" + d.Link.Host + ".helper",
			TokenName: d.Credential.Env,
			EnvFile:   src.EnvFile,
		})
	}
	return out
}

// pushSources are the yard's sources that give git a credential, in
// name order: each with a git helper, a link host and a credential.
func (w *wtx) pushSources() ([]DepotPushSource, error) {
	home := ""
	if w.store != nil {
		home = w.store.home
	}
	var srcs []Source
	err := each(w.Tx, bSources, "", func(_ []byte, src *Source) error {
		srcs = append(srcs, *src)
		return nil
	})
	if err != nil {
		return nil, err
	}
	pushSources := pushSourcesOf(srcs, home)
	byName := make(map[string]*Source, len(srcs))
	for i := range srcs {
		byName[srcs[i].Name] = &srcs[i]
	}
	for i := range pushSources {
		fingerprint, err := w.sourceSeen(byName[pushSources[i].Name])
		if err != nil {
			return nil, err
		}
		pushSources[i].fingerprint = fingerprint
	}
	return pushSources, nil
}

// pushSourceIn is the candidate whose host origin is on, for an
// https origin alone: no other origin is a push's.
func pushSourceIn(cands []DepotPushSource, origin string) (DepotPushSource, bool) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Port() != "" {
		return DepotPushSource{}, false
	}
	for _, c := range cands {
		if strings.EqualFold(u.Hostname(), c.Host) {
			return c, true
		}
	}
	return DepotPushSource{}, false
}

// DepotPushSourceFor is the push source whose host origin is on. The
// server carries an approved card out with the same declaration and
// command-copy inventory the store filed and approved it by.
func DepotPushSourceFor(sources []Source, home, origin string) (DepotPushSource, bool, error) {
	ps, ok := pushSourceIn(pushSourcesOf(sources, home), origin)
	if !ok {
		return DepotPushSource{}, false, nil
	}
	for i := range sources {
		if sources[i].Name != ps.Name {
			continue
		}
		fingerprint, err := sourceFingerprint(&sources[i], home)
		if err != nil {
			return DepotPushSource{}, false, err
		}
		ps.fingerprint = fingerprint
		return ps, true, nil
	}
	return DepotPushSource{}, false, nil
}

// PushOrigin is a depot's origin as git holds it, "" for none: the
// server reads it with the same words the store does.
func PushOrigin(path string) string { return gitRemoteURL(path) }

// PushTokenStanding says whether the push source's token stands in
// the file the helper reads, without keeping the value.
func PushTokenStanding(home string, ps DepotPushSource) bool {
	return pushTokenStanding(home, ps)
}

// IsPackHelper says whether helper, as a git config holds it, is the
// pack's helper want: exactly that absolute path. A value that merely
// ends the same way is no match: a helper value is run by git, and an
// inline helper ending in that path ends so too, so a looser match
// would let the card call an agent's own helper the pack's and write
// nothing over it. The helper's value itself is never repeated by a
// refusal: an inline one may carry a token.
func IsPackHelper(helper, want string) bool {
	h := strings.TrimSpace(helper)
	return want != "" && h != "" && filepath.IsAbs(h) && filepath.Clean(h) == filepath.Clean(want)
}

// --- the cmd's assignments -------------------------------------------

var pushEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func pushWordEnd(s string, i int) int {
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

type cmdAssign struct {
	word, name, value string
	start, end        int // end is just past the word
}

// cmdAssigns are the variable assignments before the command in cmd,
// as splitCmd in internal/pack reads them, with their places.
func cmdAssigns(cmd string) ([]cmdAssign, int) {
	var out []cmdAssign
	i := 0
	for i < len(cmd) {
		for i < len(cmd) && (cmd[i] == ' ' || cmd[i] == '\t') {
			i++
		}
		end := pushWordEnd(cmd, i)
		if i == end {
			break
		}
		word := cmd[i:end]
		if !pushEnvName.MatchString(word) {
			return out, i
		}
		name, value, _ := strings.Cut(word, "=")
		out = append(out, cmdAssign{word: word, name: name, value: value, start: i, end: end})
		i = end
	}
	return out, i
}

// cmdRest is cmd with every YARDR_PUSH assignment taken out, the rest
// standing as it was: what an approval is held to beside the push half.
func cmdRest(cmd string) string {
	assigns, _ := cmdAssigns(cmd)
	rest := cmd
	// Take out from the back, so the earlier places stand.
	for i := len(assigns) - 1; i >= 0; i-- {
		if assigns[i].name != "YARDR_PUSH" {
			continue
		}
		start, end := assigns[i].start, assigns[i].end
		if end < len(rest) && (rest[end] == ' ' || rest[end] == '\t') {
			end++
		} else if start > 0 && (rest[start-1] == ' ' || rest[start-1] == '\t') {
			start--
		}
		rest = rest[:start] + rest[end:]
	}
	return rest
}

func pushAssignments(cmd string) []cmdAssign {
	assigns, _ := cmdAssigns(cmd)
	var out []cmdAssign
	for _, a := range assigns {
		if a.name == "YARDR_PUSH" {
			out = append(out, a)
		}
	}
	return out
}

// pushValue is the value YARDR_PUSH stands at in cmd, and whether it
// stands there at all: the last assignment, as the shell reads it,
// with one layer of quotes off.
func pushValue(cmd string) (string, bool) {
	assigns := pushAssignments(cmd)
	value, found := "", false
	for _, a := range assigns {
		value, found = unquoteAssign(a.value), true
	}
	return value, found
}

func unquoteAssign(v string) string {
	if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
		return v[1 : len(v)-1]
	}
	return v
}

// DepotPushCmd is cmd with the push half made as off says: on adds
// `YARDR_PUSH=origin` before the command, at the front of its
// assignments, and off takes the YARDR_PUSH=origin assignment out.
// It answers whether cmd changed. YARDR_LAND and every other word
// stand untouched. A YARDR_PUSH of another value is a conflict, never
// overwritten, on and off alike.
func DepotPushCmd(cmd string, off bool) (string, bool, error) {
	assigns := pushAssignments(cmd)
	if len(assigns) > 1 {
		return "", false, fmt.Errorf("more than one YARDR_PUSH assignment stands in the cmd, and a depot-push card cannot choose which one to change")
	}
	value, found := pushValue(cmd)
	if found && value != depotPushOnValue {
		return "", false, fmt.Errorf("YARDR_PUSH has a value other than origin in the cmd already, and a depot-push card never overwrites it")
	}
	if off {
		if !found {
			return cmd, false, nil
		}
		return cmdRest(cmd), true, nil
	}
	if found {
		return cmd, false, nil
	}
	if strings.TrimSpace(cmd) == "" {
		return "YARDR_PUSH=" + depotPushOnValue, true, nil
	}
	// In front of the assignments, so the rest stands byte for byte.
	return "YARDR_PUSH=" + depotPushOnValue + " " + cmd, true, nil
}

// --- git, read outside nothing else ----------------------------------

// gitConfigGet is `git -C path config --get <key>`: the value and
// whether it stands. local holds it to the depot's own config.
func gitConfigGet(path, key string, local bool) (string, bool) {
	args := []string{"-C", path, "config"}
	if local {
		args = append(args, "--local")
	}
	args = append(args, "--get", key)
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return strings.TrimSpace(out.String()), true
}

func gitConfigGetAll(path, key string, local bool) ([]string, bool) {
	args := []string{"-C", path, "config"}
	if local {
		args = append(args, "--local")
	}
	args = append(args, "--null", "--get-all", key)
	cmd := exec.Command("git", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []string{}, true
		}
		return nil, false
	}
	raw := out.String()
	if raw == "" {
		return []string{}, true
	}
	values := strings.Split(raw, "\x00")
	if values[len(values)-1] == "" {
		values = values[:len(values)-1]
	}
	return values, true
}

// PushHelpers are the credential helpers git runs for https://host in
// the depot at path, in the order it runs them, and whether its config
// could be read. That is every credential.helper line, box-wide and
// local, whose key is the bare one or names that host, with an empty
// value clearing those before it as git reads it. Reading the host's
// key alone is not enough: git runs a bare credential.helper for every
// host too, and after a push it hands the token the pack's helper gave
// to each helper it runs (store), so one an agent wrote in the depot's
// shared config would be given the token.
func PushHelpers(path, host string) ([]string, bool) {
	cmd := exec.Command("git", "-C", path, "config", "--null", "--get-regexp", `^credential\.`)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []string{}, true
		}
		return nil, false
	}
	helpers := []string{}
	for _, entry := range strings.Split(out.String(), "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		if !helperKeyFor(key, host) {
			continue
		}
		if value == "" {
			helpers = helpers[:0]
			continue
		}
		helpers = append(helpers, value)
	}
	return helpers, true
}

// helperKeyFor says whether the config key is a credential helper git
// may run for https://host: the bare key, or one whose URL names that
// host. A URL it cannot read as another host's counts, so a helper is
// never missed.
func helperKeyFor(key, host string) bool {
	lower := strings.ToLower(key)
	if lower == "credential.helper" {
		return true
	}
	if !strings.HasPrefix(lower, "credential.") || !strings.HasSuffix(lower, ".helper") {
		return false
	}
	sub := key[len("credential.") : len(key)-len(".helper")]
	u, err := url.Parse(sub)
	if err != nil || u.Host == "" {
		return true
	}
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	name := u.Hostname()
	if wild, ok := strings.CutPrefix(name, "*"); ok {
		return strings.HasSuffix(strings.ToLower(host), strings.ToLower(wild))
	}
	return strings.EqualFold(name, host)
}

// gitRemoteURL is the depot's origin as git holds it, "" for none.
func gitRemoteURL(path string) string {
	cmd := exec.Command("git", "-C", path, "remote", "get-url", "origin")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// originDisplay is an origin as a card names it: scheme, host
// and path, with any userinfo the URL carries left out, so no
// credential a URL was written with stands in a card, an event or a
// log.
func originDisplay(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "(none)"
	}
	host := u.Hostname()
	if port := u.Port(); port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host + u.Path
}

// pushTokenStanding says whether the push source's token stands in
// the file the helper reads: the source's env_file, or the yard's
// default for it. The value is read and never kept.
func pushTokenStanding(home string, ps DepotPushSource) bool {
	file := ps.EnvFile
	if file == "" {
		file = source.EnvFile(home, ps.Name)
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return false
	}
	return source.Lookup(f.Env, ps.TokenName) != ""
}

// depotPushState is what the card reads of a depot as it stands now.
type depotPushState struct {
	depot        Depot
	group        Group
	hasGrp       bool
	origin       string
	src          DepotPushSource // the push source of the origin's host
	hasSrc       bool
	helperValues []string
	localValues  []string
	token        bool
	edgeOK       bool
	pushVal      string
	pushSet      bool
	pushCount    int
}

// readDepotPushState reads the depot, its group, its origin, its git
// helper and the push token as they stand in w. Git runs here: the
// reads are `git config --get` and `remote get-url`, which touch no
// index and take no lock a write of the store holds.
func (w *wtx) readDepotPushState(depotName string) (*depotPushState, error) {
	st := &depotPushState{}
	var depot Depot
	ok, err := get(w.Tx, bDepots, depotName, &depot)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("depot %s: %w", depotName, ErrNotFound)
	}
	st.depot = depot
	var g Group
	if ok, err := get(w.Tx, bGroups, depotName+"-assembly", &g); err != nil {
		return nil, err
	} else if ok {
		st.group, st.hasGrp = g, true
	}
	edge, connected, err := EdgeOf(map[string]string{EdgeSetting: w.settingValue(EdgeSetting)})
	if err != nil {
		return nil, err
	}
	st.edgeOK = connected
	_ = edge
	if depot.Path != "" {
		st.origin = gitRemoteURL(depot.Path)
	}
	cands, err := w.pushSources()
	if err != nil {
		return nil, err
	}
	if ps, found := pushSourceIn(cands, st.origin); found {
		st.src, st.hasSrc = ps, true
		if depot.Path != "" {
			var ok bool
			st.helperValues, ok = PushHelpers(depot.Path, ps.Host)
			if !ok {
				return nil, fmt.Errorf("depot %s: its git config could not be read", depotName)
			}
			st.localValues, ok = gitConfigGetAll(depot.Path, ps.HelperKey, true)
			if !ok {
				return nil, fmt.Errorf("depot %s: its git config could not be read", depotName)
			}
		}
		home := ""
		if w.store != nil {
			home = w.store.home
		}
		st.token = pushTokenStanding(home, ps)
	}
	if st.hasGrp {
		cmd, _ := st.group.Config["cmd"].(string)
		st.pushVal, st.pushSet = pushValue(cmd)
		st.pushCount = len(pushAssignments(cmd))
	}
	return st, nil
}

// checkDepotPush refuses, in the card's words, a state no card may be
// filed or approved from, for the card gp as it stands in st. asking
// refuses what would change nothing as filing does; an approval reads
// the same checks and refuses in its own stale words where the caller
// wraps them. It answers the two halves as they stand: cmdOn is
// YARDR_PUSH=origin in the cmd, gitOn the pack's helper answering
// (already verified: the pack's helper with the token standing).
func (w *wtx) checkDepotPush(gp AskDepotPush, st *depotPushState, asking bool) (cmdOn, gitOn bool, err error) {
	depot := gp.Depot
	if !st.edgeOK {
		return false, false, fmt.Errorf("this yard is connected to no edge (the %s setting names none), so a depot-push card has no hosted operator to approve it: connect the yard to its edge first (yardr edge connect)", EdgeSetting)
	}
	if st.depot.IsDir() || st.depot.Path == "" {
		return false, false, fmt.Errorf("depot %s has no repository here, so its landings cannot push", depot)
	}
	if !st.hasGrp {
		return false, false, fmt.Errorf("depot %s: there is no group %s-assembly, and a depot-push card never picks another group", depot, depot)
	}
	if !st.hasSrc {
		return false, false, fmt.Errorf("depot %s: its origin %s is not https on a host a source gives git a credential for, and a depot-push card turns on no other origin", depot, originDisplay(st.origin))
	}
	if !gp.Off && !st.token {
		return false, false, fmt.Errorf("depot %s: no %s stands for the source %s, so a push would have no token to answer with: give it on a secret card for the source %s first", depot, st.src.TokenName, st.src.Name, st.src.Name)
	}
	if st.pushCount > 1 {
		return false, false, fmt.Errorf("depot %s: more than one YARDR_PUSH assignment stands in %s-assembly's cmd, and a depot-push card cannot choose which one to change", depot, depot)
	}
	if st.pushSet && st.pushVal != depotPushOnValue {
		return false, false, fmt.Errorf("depot %s: YARDR_PUSH has a value other than origin in %s-assembly's cmd, and a depot-push card never overwrites it", depot, depot)
	}
	// An on card accepts one effective helper only, and only when that is
	// the pack's exact helper. Git can run several credential helpers in
	// order, so reading just one value would leave another helper in reach.
	if gp.Off {
		localPack := false
		for _, helper := range st.localValues {
			localPack = localPack || IsPackHelper(helper, st.src.Helper)
		}
		if localPack && len(st.localValues) != 1 {
			return false, false, fmt.Errorf("depot %s: more than one credential helper line stands in its own git config, so a depot-push card cannot remove only the pack's line", depot)
		}
		gitOn = localPack
	} else {
		if len(st.helperValues) > 1 {
			return false, false, fmt.Errorf("depot %s: more than one credential helper answers for https://%s, so a depot-push card writes nothing over them", depot, st.src.Host)
		}
		gitOn = len(st.helperValues) == 1 && IsPackHelper(st.helperValues[0], st.src.Helper)
		if len(st.helperValues) == 1 && !gitOn {
			// The helper's value is not repeated: an inline helper may carry
			// a token, and this text lands in notes, events and logs.
			return false, false, fmt.Errorf("depot %s: a credential helper already answers for https://%s, and it is not the pack's helper, so a depot-push card writes nothing over it", depot, st.src.Host)
		}
	}
	cmdOn = st.pushSet && st.pushVal == depotPushOnValue
	if gp.Off {
		if asking && !cmdOn && !gitOn {
			return false, false, fmt.Errorf("depot %s: push to %s is not on, so there is nothing to turn off", depot, st.src.Host)
		}
	} else {
		if asking && cmdOn && gitOn {
			return false, false, fmt.Errorf("depot %s: push to %s is on already (YARDR_PUSH=origin in %s-assembly's cmd and the pack's helper in its git config)", depot, st.src.Host, depot)
		}
	}
	return cmdOn, gitOn, nil
}

// depotPushCard writes the card of the depot-push gp from the yard
// as it stands: its sentences, the filing that takes it back, and what
// it saw of each thing it names. asking refuses what filing refuses.
func (w *wtx) depotPushCard(bead string, gp AskDepotPush, asking bool) (AskDepotPush, *depotPushState, error) {
	gp.Depot = strings.TrimSpace(gp.Depot)
	if gp.Depot == "" {
		return AskDepotPush{}, nil, fmt.Errorf("--depot-push names the depot: --depot-push <depot>")
	}
	st, err := w.readDepotPushState(gp.Depot)
	if err != nil {
		return AskDepotPush{}, nil, err
	}
	cmdOn, gitOn, err := w.checkDepotPush(gp, st, asking)
	if err != nil {
		return AskDepotPush{}, nil, err
	}
	groupName := gp.Depot + "-assembly"
	cmd, _ := st.group.Config["cmd"].(string)
	rest := cmdRest(cmd)
	other := map[string]any{}
	for k, v := range st.group.Config {
		if k != "cmd" {
			other[k] = v
		}
	}
	seenMap := map[string]string{
		"depot " + gp.Depot:  seen(st.depot.Path, st.depot.Kind, st.depot.Base),
		"group " + groupName: seen(st.group.Runner, rest, other),
		"origin":             seen(st.origin),
		"edge":               seen(w.settingValue(EdgeSetting)),
		"push source":        st.src.fingerprint,
	}
	if !gp.Off {
		seenMap["push token"] = seen(st.token)
	}
	var does []string
	if gp.Off {
		does = append(does,
			fmt.Sprintf("turns push to %s off for depot %s (origin %s).", st.src.Host, gp.Depot, originDisplay(st.origin)),
		)
		if cmdOn {
			does = append(does, fmt.Sprintf("takes YARDR_PUSH=origin out of %s's cmd; the rest of the cmd stands untouched, YARDR_LAND as it was.", groupName))
		} else {
			does = append(does, fmt.Sprintf("%s's cmd holds no YARDR_PUSH=origin, so its cmd stands untouched.", groupName))
		}
		if gitOn {
			does = append(does, fmt.Sprintf("takes the pack's helper line for https://%s out of depot %s's own git config with git -C %s config --unset %s; no other depot's config changes.", st.src.Host, gp.Depot, gp.Depot, st.src.HelperKey))
		} else {
			does = append(does, fmt.Sprintf("depot %s's git config holds no pack helper line for https://%s, so its config stands untouched.", gp.Depot, st.src.Host))
		}
		does = append(does, "landings of the depot then land here and push nowhere.")
		return AskDepotPush{Depot: gp.Depot, Off: true, Does: does,
			Back:   fmt.Sprintf("yardr bead ask %s \"<question>\" --depot-push %s", bead, gp.Depot),
			BackBy: "only the operator", Seen: seenMap}, st, nil
	}
	does = append(does,
		fmt.Sprintf("turns push to %s on for depot %s (origin %s): its landings then push to origin.", st.src.Host, gp.Depot, originDisplay(st.origin)),
	)
	if cmdOn {
		does = append(does, fmt.Sprintf("YARDR_PUSH=origin stands in %s's cmd already, so its cmd stands untouched, YARDR_LAND as it was.", groupName))
	} else {
		does = append(does, fmt.Sprintf("adds YARDR_PUSH=origin at the front of %s's cmd, before its assignments and its command; the rest of the cmd stands untouched, YARDR_LAND as it was.", groupName))
	}
	if gitOn {
		does = append(does, fmt.Sprintf("the pack's helper already answers for https://%s in depot %s's git config with the source's token, so its config stands untouched: the helper is verified and nothing is written.", st.src.Host, gp.Depot))
	} else {
		does = append(does, fmt.Sprintf("points depot %s's own git at the pack's credential helper for https://%s alone; no other depot's config changes, and the config holds the helper's path alone, no token.", gp.Depot, st.src.Host))
	}
	does = append(does, "nothing here runs git push: the assembly pushes at landing, as it does on any yard.")
	return AskDepotPush{Depot: gp.Depot, Does: does,
		Back:   fmt.Sprintf("yardr bead ask %s \"<question>\" --depot-push-off %s", bead, gp.Depot),
		BackBy: "only the operator", Seen: seenMap}, st, nil
}

// askedDepotPush checks the depot-push an ask to proposes, written by
// actor on the bead id, and answers it as the ask stores it. Who may
// file is the setup asks' rule: a crew member or a user, never a bead's
// agent. Who it is put to is the operator alone, as ApproveAsk's rule
// is. A half-done depot (one half standing from a carry that failed
// after it) is filed again to complete it, not refused as already on.
func (w *wtx) askedDepotPush(ctx context.Context, gp AskDepotPush, bead, to, actor string) (*AskDepotPush, error) {
	const whose = "a depot-push card is filed by a crew member or a user of the yard"
	sess, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return nil, err
	}
	if sess != nil && !sess.Manual() {
		return nil, fmt.Errorf("%s; this is the session %s of a bead's agent: say what is needed in a note on the bead (yardr bead note)", whose, sess.ID)
	}
	if err := startingRefused(w.Tx, sessionOf(ctx)); err != nil {
		return nil, err
	}
	if w.Bucket(bCrew).Get([]byte(actor)) == nil && w.Bucket(bUsers).Get([]byte(actor)) == nil {
		return nil, fmt.Errorf("%s; this is %s: say what is needed in a note on the bead (yardr bead note)", whose, or(actor, "nobody"))
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a depot-push card is the operator's to approve, and this yard has none: yardr settings set operator <name>")
	case !ops.is(to):
		return nil, fmt.Errorf("a depot-push card is %s to approve (%s): the yard carries it out as them, so %s cannot be asked to", ops.article(), ops, to)
	}
	card, _, err := w.depotPushCard(bead, gp, true)
	if err != nil {
		return nil, err
	}
	return &card, nil
}

// depotPushWeight is a depot-push card's weight: turning a depot's
// push on or off lets the yard change something outside it.
func depotPushWeight(gp AskDepotPush) []AskWeight {
	what := fmt.Sprintf("landings of depot %s push to its origin", gp.Depot)
	if gp.Off {
		what = fmt.Sprintf("landings of depot %s push to its origin no more", gp.Depot)
	}
	return []AskWeight{weighs(WeightOutside, what)}
}

// depotPushStale says why an approval is refused when the yard would
// not write the card as it stands: what the sentences are held to
// (Seen) moved. The push and helper halves are no part of it: a half
// written since is the completion the approval makes, read in the
// approval itself.
func (w *wtx) depotPushStale(seq uint64, was, now AskDepotPush) error {
	if !sameMap(was.Seen, now.Seen) {
		return fmt.Errorf("ask %d was written when the depot, its %s-assembly group, its origin, the edge, the push source or the push token stood otherwise: the card is from before that changed. %s", seq, was.Depot, nothingRan)
	}
	return staleCard(seq, AskRun{Does: was.Does}, AskRun{Does: now.Does})
}

// approveDepotPush is ApproveAsk for a depot-push ask, inside the
// write: it holds the approval to the card as the yard stands now,
// records CarryPending with its carrying record, and runs nothing
// itself. The server carries both halves out after the write.
func (w *wtx) approveDepotPush(ctx context.Context, row *beadRow, r *ResolvedAsk, actor string, weighed bool) (*AskApproved, error) {
	seq := r.Note.Seq
	was := *r.Ask.DepotPush
	if err := w.may(ctx, actor, ActApproveRun, Object{Kind: KindAsk, Name: row.ID, Seq: seq, To: r.Ask.To}); err != nil {
		return nil, err
	}
	// Re-read the group's cmd and the depot's git config as they stand:
	// a card whose halves both stand now is already on (or, off, not
	// on), and a YARDR_PUSH of another value is a conflict, each
	// refused with nothing written.
	card, st, err := w.depotPushCard(row.ID, was, false)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	cmdOn, gitOn, err := w.checkDepotPush(was, st, false)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if was.Off {
		if !cmdOn && !gitOn {
			return nil, fmt.Errorf("ask %d: push to %s is not on for depot %s, so there is nothing to turn off. %s", seq, st.src.Host, was.Depot, nothingRan)
		}
	} else if cmdOn && gitOn {
		return nil, fmt.Errorf("ask %d: push to %s is on already for depot %s. %s", seq, st.src.Host, was.Depot, nothingRan)
	}
	if !sameMap(card.Seen, was.Seen) {
		if err := w.depotPushStale(seq, was, card); err != nil {
			return nil, err
		}
	}
	weight := depotPushWeight(card)
	if err := weightStale(seq, r.Ask.Weight, weight); err != nil {
		return nil, err
	}
	if err := weighedRefusal(seq, weight, weighed); err != nil {
		return nil, err
	}
	argv := []string{"depot-push", was.Depot}
	if was.Off {
		argv = []string{"depot-push-off", was.Depot}
	}
	rec := Carry{Bead: row.ID, Ask: seq, Argv: argv, State: CarryApproved, Actor: actor,
		DepotPushSource: DepotPushSourceFingerprint(st.src)}
	if err := put(w.Tx, bCarry, carryKey(row.ID, seq), rec); err != nil {
		return nil, err
	}
	text, err := json.Marshal(Answer{Ask: seq, Ran: &AskRan{Argv: argv, Result: CarryPending}})
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return nil, err
	}
	return &AskApproved{Bead: row.ID, Ask: seq, Asker: r.Note.Author, Argv: argv, Does: was.Does, Result: CarryPending}, nil
}

// SetAssemblyCmd makes the cmd half of a depot-push of depot as off
// says, in one write, for the server that carries an approved card
// out: it answers whether the cmd changed. It is idempotent: a cmd
// half that stands is written no second time. As actor it is the
// approval's operator; the write is recorded as theirs.
func (s *Store) SetAssemblyCmd(ctx context.Context, depot string, off bool, actor string) (bool, error) {
	changed := false
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		var g Group
		ok, err := get(w.Tx, bGroups, depot+"-assembly", &g)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("group %s-assembly: %w", depot, ErrNotFound)
		}
		cmd, _ := g.Config["cmd"].(string)
		next, did, err := DepotPushCmd(cmd, off)
		if err != nil {
			return err
		}
		if !did {
			return nil
		}
		if g.Config == nil {
			g.Config = map[string]any{}
		}
		g.Config["cmd"] = next
		changed = true
		return putGroup(w, &g, actor, false)
	})
	return changed, err
}

// IsDepotPushCarry says whether argv is a depot-push carrying out.
func IsDepotPushCarry(argv []string) bool {
	return len(argv) == 2 && (argv[0] == "depot-push" || argv[0] == "depot-push-off")
}
