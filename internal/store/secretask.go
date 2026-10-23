// A secret ask is an ask whose answer is no words: its fields say where the
// person's value goes (GiveSecret), never into a note. The ask says where
// its answer goes, by destination and name; the answer says which names
// were given. Neither holds a value, and nothing else an ask has can take
// one: a pick, words and a decline's words are refused on a secret ask
// (answerable).
//
// A secret ask may ask for a person's own address in a source that tells
// people instead (AskSecret.User): the variable the source's notify key
// names, kept in that user's env file. It is the user's to give, or the
// operator's for them, as the settings that say where a user is told are
// (mayTell). Or it may ask for a crew member's own credential in the
// source (AskSecret.Crew), kept in that crew member's env file: the
// operator's alone to give, taking effect when the crew member next
// starts. Or it may ask for a user's own credential in the source
// (AskSecret.As), kept in that user's env file: the user's to give, or
// the operator's for them, as their address is.
// A waiting login's input (AskSecret.Login) is handed to it and kept
// nowhere; a harness API key (AskSecret.Harness) is kept in the operator's
// key file for its kind and read by sessions on that login.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// AskSecret says where the answer of a secret ask goes. For a source ask,
// Env names variables of the source's credential as its declaration stood
// when the ask was written: the asker may narrow those names, but names no
// path and no variable the declaration does not have.
//
// With Login instead of a source it is a login-input ask: the value goes to
// the harness login that waits under that reference (loginwait.go), and Env
// is the one name it is asked for.
//
// With Harness instead of a source it is a harness-key ask: the value is
// the API key of that harness kind, and goes to the operator's key file
// of the kind (HarnessKeyFile, through giveHarnessKey), which every
// session of the kind on the operator's login reads; Env holds variables
// of the kind's login_env. Crew is the crew member whose session the
// yard's led login found at the login screen, and whose journal it is
// asked on. No source declares it.
type AskSecret struct {
	Source string   `json:"source"`
	Env    []string `json:"env"`
	// Hook is the hook whose signing secret is asked for, "" for a
	// credential: Source is then the hook's source, Env the one name
	// the value is given under (hookSecretEnv), and the value goes to
	// the hook's secret file (HookSecretFile), never to an env file.
	Hook string `json:"hook,omitempty"`
	// Login is the waiting login the value goes to, the reference its
	// leader printed in its input need. Set instead of a source; with As,
	// the login is that user's own.
	Login string `json:"login,omitempty"`
	// Harness is the kind whose API key is asked for, as its harness
	// file names it. Set instead of a source, with Crew: the crew member
	// whose login screen the key is asked for, not the only reader of it.
	Harness string `json:"harness,omitempty"`
	// Host is where the source's calls go, the scheme and host of its base:
	// what the card says beside the source's name. It is kept on the ask
	// because the views render an ask from its note alone; GiveSecret
	// refuses when the source's calls go elsewhere by then.
	Host string `json:"host,omitempty"`
	// User makes it an ask for that user's own address in the source, where
	// the source reaches a person: Env is then the one variable the source's
	// notify key names, and the answer goes to the user's env file, not the
	// source's. "" asks for the source's credential.
	User string `json:"user,omitempty"`
	// Crew makes it an ask for that crew member's own credential in the
	// source, which a reply answers with: Env is then variables of the
	// source's credential, as for the source's own, and the answer goes
	// to the crew member's env file. "" asks for the source's
	// credential. Never set with User.
	Crew string `json:"crew,omitempty"`
	// As makes it an ask for that user's own credential in the source,
	// which a call made as them reads: Env is then the names such a call
	// mints or uses, every name under mint where the credential is
	// minted and the static ones otherwise unless the asker narrowed to
	// others, and the answer goes to the user's env file. "" asks for
	// the source's credential. Never set with User or Crew. With a Login
	// it is the user whose own login that is, and they give its input.
	As string `json:"as,omitempty"`
	// Concealed says whether the value is drawn hidden: false where the
	// source marks every asked name as no secret, absent (concealed) on
	// any other secret. It is kept on the ask because the views render an
	// ask from its note alone, as Host is.
	Concealed *bool `json:"concealed,omitempty"`
}

// Names are the variables asked for, in words.
func (s AskSecret) Names() string { return strings.Join(s.Env, " and ") }

// Line is what a card says a secret ask asks for, in the yard's words and
// never the asker's: whatever the question and its context say, the value
// becomes these variables of this source, of this user's file, or of this
// crew member's. Giving a user's address does not say the user is told
// through the source, so the line names the command that does. A crew
// member's running session does not see the new value, so its line says
// the env_file line is the one written and when it takes effect. A user's
// own credential is read by a call made as them, so its line says as
// much. A login input goes to the login that waits, and names it, and
// whose it is where it is a user's own. A
// harness key goes to the operator's key file of the kind, and its line
// says the reach the person consents to: every session of the kind that
// burns the operator's login reads it.
func (s AskSecret) Line() string {
	if s.Hook != "" {
		return fmt.Sprintf("a secret: the signing secret for the hook %s (POST /hooks/%s), for the source %s (%s)", s.Hook, s.Hook, s.Source, s.Host)
	}
	if s.Login != "" {
		whose := "the"
		if s.As != "" {
			whose = s.As + "'s"
		}
		return fmt.Sprintf("a secret: %s, for %s waiting login %s", s.Names(), whose, s.Login)
	}
	if s.Harness != "" {
		label := s.Harness
		if h, ok := agents.Lookup(s.Harness); ok && h.Label != "" {
			label = h.Label
		}
		return fmt.Sprintf("a secret: %s, the API key %s runs with, asked for crew member %s; it is read by every %s session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start",
			s.Names(), label, s.Crew, label)
	}
	if s.User != "" {
		return fmt.Sprintf("a secret: %s, %s's own address in the source %s (%s); %s is told through %s once it is given and yardr user set %s %s=%s is run",
			s.Names(), s.User, s.Source, s.Host, s.User, s.Source, s.User, UserNotify, s.Source)
	}
	if s.Crew != "" {
		return fmt.Sprintf("a secret: %s, for crew member %s in the source %s (%s); the env_file line is the one written, and it takes effect when %s next starts (rotation or yardr crew restart %s)",
			s.Names(), s.Crew, s.Source, s.Host, s.Crew, s.Crew)
	}
	if s.As != "" {
		return fmt.Sprintf("a secret: %s, %s's own credential for the source %s (%s), read as %s when a call is made --as %s",
			s.Names(), s.As, s.Source, s.Host, s.As, s.As)
	}
	return fmt.Sprintf("a secret: %s, for the source %s (%s)", s.Names(), s.Source, s.Host)
}

// SecretAsked is the secret an ask asks for, as a client shows it: the
// source, the names to ask the person for, the user whose own address it
// is (absent for a source's credential), the crew member whose own
// credential or harness key it is (absent for any other secret), the user
// whose own credential it is (absent for any other secret), the waiting
// login it goes to (absent for any other secret), the harness kind whose
// API key it is (absent for any other secret), and the card's line whole.
type SecretAsked struct {
	Source string   `json:"source"`
	Env    []string `json:"env"`
	User   string   `json:"user,omitempty"`
	Crew   string   `json:"crew,omitempty"`
	As     string   `json:"as,omitempty"`
	Login  string   `json:"login,omitempty"`
	// Hook is the hook whose signing secret is asked for; absent on any
	// other secret.
	Hook string `json:"hook,omitempty"`
	// Harness is the kind whose API key is asked for; absent on any
	// other secret.
	Harness string `json:"harness,omitempty"`
	Line    string `json:"line"`
}

// asked is the secret of the ask a as a client shows it, nil for an ask
// that asks for none.
func (a Ask) asked() *SecretAsked {
	if a.Secret == nil {
		return nil
	}
	return &SecretAsked{Source: a.Secret.Source, Env: a.Secret.Env, User: a.Secret.User, Crew: a.Secret.Crew, As: a.Secret.As, Login: a.Secret.Login, Hook: a.Secret.Hook, Harness: a.Secret.Harness, Line: a.Secret.Line()}
}

// concealed is the ask's concealment as a client shows it: false where the
// source marks every asked name as no secret, nil (concealed) for a
// concealed secret and for an ask that asks for none.
func (a Ask) concealed() *bool {
	if a.Secret == nil {
		return nil
	}
	return a.Secret.Concealed
}

// sourceHost is where the calls of the source d declares go: the scheme and
// host of its base, or the variable whose value is the address, which is
// what the ask gives.
func sourceHost(d *source.Declaration) string {
	if d.Credential != nil && d.Credential.URL {
		return d.Address()
	}
	if u, err := url.Parse(d.Base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return d.Base
}

// concealedAsk is the ask's concealment for names of d: false where the
// source marks every asked name as no secret, nil (concealed) otherwise. Nil
// is what an ask from before the field stores, so an old and a concealed ask
// read the same.
func concealedAsk(d *source.Declaration, names []string) *bool {
	if d.Credential.Concealed(names) {
		return nil
	}
	f := false
	return &f
}

// askedSecret checks the secret an ask for the user to asks for, and
// answers it as the ask stores it: the source by its own name, the names in
// the declaration's order (the mint's inputs where the credential is
// minted, else every one of it, when the asker narrowed to none), and the
// host. It refuses a source the yard does not have, one whose declaration
// names no credential, a name the declaration does not have, and anyone but
// the operator as the one asked: giving is setting a source's credential,
// which is the operator's alone (maySource).
//
// With a User it is that user's own address in the source that is asked
// for (askedAddress), with a Crew that crew member's own credential in
// it (askedCrewSecret), and with an As that user's own credential in it
// (askedUserSecret). With a Login it is the waiting login's input
// (askedLogin), and with a Harness that crew member's harness API key
// (askedHarnessKey).
func (w *wtx) askedSecret(sec AskSecret, to string) (*AskSecret, error) {
	if strings.TrimSpace(sec.Login) != "" {
		return w.askedLogin(sec, to)
	}
	if strings.TrimSpace(sec.Harness) != "" {
		return w.askedHarnessKey(sec, to)
	}
	name := strings.TrimSpace(sec.Source)
	if name == "" {
		return nil, fmt.Errorf("a secret ask names the source the secret is for: --secret <source>")
	}
	var src Source
	if ok, err := get(w.Tx, bSources, name, &src); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("a secret is asked for a source the yard has (yardr source list), which the operator adds: source %s: %w", name, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return nil, err
	}
	d = d.WithBase(src.Base)
	if sec.Hook != "" {
		hook := strings.TrimSpace(sec.Hook)
		if hook == "" {
			return nil, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", sec.Hook)
		}
		return w.askedHookSecret(d, hook, sec, to)
	}
	if user := strings.TrimSpace(sec.User); user != "" {
		if crew := strings.TrimSpace(sec.Crew); crew != "" {
			return nil, fmt.Errorf("a secret ask names a user or a crew member, not both: --for-user %s or --for-crew %s", user, crew)
		}
		if as := strings.TrimSpace(sec.As); as != "" {
			return nil, fmt.Errorf("a secret ask names a user's address or a user's credential, not both: --for-user %s or --as %s", user, as)
		}
		return w.askedAddress(d, user, sec.Env, to)
	}
	if crew := strings.TrimSpace(sec.Crew); crew != "" {
		if as := strings.TrimSpace(sec.As); as != "" {
			return nil, fmt.Errorf("a secret ask names a crew member's credential or a user's, not both: --for-crew %s or --as %s", crew, as)
		}
		return w.askedCrewSecret(d, crew, sec.Env, to)
	}
	if as := strings.TrimSpace(sec.As); as != "" {
		return w.askedUserSecret(d, as, sec.Env, to)
	}
	if d.Credential == nil {
		return nil, fmt.Errorf("source %s: its declaration names no credential, so there is no secret to ask for", name)
	}
	has := d.Credential.Variables()
	for _, n := range sec.Env {
		if !slices.Contains(has, n) {
			return nil, fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", name, n, d.Credential.Names())
		}
	}
	// Without --env the ask is for what the source needs given: the
	// mint's inputs where the credential is minted, since the token is
	// minted per call and never given, else every variable of it. The
	// mint's names and the static ones are alternatives, not all
	// required, and --env names any of them instead.
	env := d.Credential.MintOrStatic()
	if len(sec.Env) > 0 {
		env = slices.DeleteFunc(slices.Clone(has), func(n string) bool { return !slices.Contains(sec.Env, n) })
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case !ops.is(to):
		return nil, fmt.Errorf("a secret is %s to give (%s): setting a source's credential is theirs alone, so %s cannot be asked for one", ops.article(), ops, to)
	}
	return &AskSecret{Source: d.Name, Env: env, Host: sourceHost(d), Concealed: concealedAsk(d, env)}, nil
}

// hookSecretEnv is the one name a hook's signing secret is given under:
// the hook's name as a variable, so a giving typed as NAME=value lines
// names it and a card's "gave <name>" says which hook it was for.
func hookSecretEnv(hook string) string {
	name := strings.ToUpper(strings.NewReplacer("-", "_").Replace(hook))
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name + "_SIGNING_SECRET"
}

// askedHookSecret checks an ask for the signing secret of the hook hook
// of the source d declares, asked of to, and answers it as the ask
// stores it: the hook, the source by its own name, the one name the
// value is given under and the host. It refuses a hook name no hook
// may take, a hook the yard has already (its replacement, secret
// included, stays the operator's in a terminal), a source whose
// declaration has no hook key, a user beside the hook, and anyone but
// the operator as the one asked: giving is the operator's alone, as
// adding the hook is.
func (w *wtx) askedHookSecret(d *source.Declaration, hook string, sec AskSecret, to string) (*AskSecret, error) {
	switch {
	case strings.TrimSpace(sec.User) != "":
		return nil, fmt.Errorf("a hook's signing secret is the operator's to give, not %s's address: it takes no --for-user", strings.TrimSpace(sec.User))
	case strings.TrimSpace(sec.Crew) != "":
		return nil, fmt.Errorf("a hook's signing secret is the operator's to give, not crew member %s's credential: it takes no --for-crew", strings.TrimSpace(sec.Crew))
	case strings.TrimSpace(sec.As) != "":
		return nil, fmt.Errorf("a hook's signing secret is the operator's to give, not %s's credential: it takes no --as", strings.TrimSpace(sec.As))
	case !hookName.MatchString(hook):
		return nil, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", hook)
	case d.Hook == nil:
		return nil, fmt.Errorf("source %s: its declaration has no hook key, so the yard hears nothing from it and there is no signing secret to ask for", d.Name)
	case d.Hook.RegistersSecret():
		return nil, fmt.Errorf("source %s registers its webhook itself (call %s): the yard makes the signing secret for hook %s, and no secret is typed: ask the hook alone (--run \"hook add %s --source %s\")", d.Name, d.Hook.Register.Call, hook, hook, d.Name)
	}
	if w.Bucket(bHooks).Get([]byte(hook)) != nil {
		return nil, fmt.Errorf("hook %s is in the yard already: replacing it, secret included, is the operator's to type in a terminal (yardr hook add %s --source %s --secret-file <file>)", hook, hook, d.Name)
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case !ops.is(to):
		return nil, fmt.Errorf("a secret is %s to give (%s): giving a hook's signing secret is theirs alone, so %s cannot be asked for one", ops.article(), ops, to)
	}
	return &AskSecret{Source: d.Name, Env: []string{hookSecretEnv(hook)}, Host: sourceHost(d), Hook: hook}, nil
}

// askedLogin checks an ask for the input of the waiting login sec names,
// asked of to, and answers it as the ask stores it: the login's reference
// and the one name the input is asked for. It refuses a source or a user's
// address beside the login, a name beside the one, and anyone but the
// operator as the one asked: giving is handing the operator's login its
// answer, which is the operator's alone (maySource).
//
// With an As the login is that user's own, led for the group that carries it
// (yardr user add --login): the code the sign-in page shows is the person's
// to read, so the ask is theirs, or the operator's for them, as their own
// credential in a source is (askedUserSecret), and they give it (mayTell).
// Whether the login waits is GiveSecret's to say when the value comes, not
// the ask's: the wait may end between the two.
func (w *wtx) askedLogin(sec AskSecret, to string) (*AskSecret, error) {
	login := strings.TrimSpace(sec.Login)
	as := strings.TrimSpace(sec.As)
	switch {
	case strings.TrimSpace(sec.Source) != "":
		return nil, fmt.Errorf("a secret ask names a waiting login or a source, not both: --secret-login %s or --secret <source>", login)
	case strings.TrimSpace(sec.Harness) != "":
		return nil, fmt.Errorf("a secret ask names a waiting login or a harness, not both: --secret-login %s or an API key", login)
	case strings.TrimSpace(sec.User) != "":
		return nil, fmt.Errorf("a login input is the login's to take, not %s's address: it takes no --for-user", strings.TrimSpace(sec.User))
	case strings.TrimSpace(sec.Crew) != "":
		return nil, fmt.Errorf("a login input is the login's to take, not crew member %s's credential: it takes no --for-crew", strings.TrimSpace(sec.Crew))
	}
	for _, n := range sec.Env {
		if n != loginInputName {
			return nil, fmt.Errorf("a login input is asked for as %s, and %s is no name of it", loginInputName, n)
		}
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case as == "":
		if !ops.is(to) {
			return nil, fmt.Errorf("a secret is %s to give (%s): answering the login is theirs alone, so %s cannot be asked for one", ops.article(), ops, to)
		}
		return &AskSecret{Env: []string{loginInputName}, Login: login}, nil
	}
	if _, err := getUser(w.Tx, as); err != nil {
		return nil, fmt.Errorf("a login input is asked for the login of a user of the yard (yardr user list): %w", err)
	}
	if to != as && !ops.is(to) {
		whose := fmt.Sprintf("the code that signs %s in is theirs to give", as)
		if !ops.is(as) {
			whose += ", or " + ops.whose() + " for them"
		}
		return nil, fmt.Errorf("%s, so %s cannot be asked for it", whose, to)
	}
	return &AskSecret{Env: []string{loginInputName}, Login: login, As: as}, nil
}

// askedHarnessKey checks an ask for the API key of the harness kind sec
// names, for the crew member whose session sits at its login screen, asked
// of to, and answers it as the ask stores it: the kind by its own name, the
// member, and the names in the file's order (every one of login_env when
// the asker narrowed to none). It refuses a kind the yard has no harness file
// for, one whose file names no login_env, a name the file does not name,
// no crew member at all, one the yard does not have, and anyone but the
// operator as the one asked: giving is setting what a session of the yard
// runs with, which is the operator's alone, as a crew member's credential
// in a source is.
func (w *wtx) askedHarnessKey(sec AskSecret, to string) (*AskSecret, error) {
	kind := strings.TrimSpace(sec.Harness)
	switch {
	case strings.TrimSpace(sec.Source) != "":
		return nil, fmt.Errorf("a secret ask names a harness or a source, not both: an API key or --secret <source>")
	case strings.TrimSpace(sec.User) != "":
		return nil, fmt.Errorf("a harness API key is a crew member's to run with, not %s's address: it takes no --for-user", strings.TrimSpace(sec.User))
	case strings.TrimSpace(sec.As) != "":
		return nil, fmt.Errorf("a harness API key is a crew member's to run with, not %s's credential: it takes no --as", strings.TrimSpace(sec.As))
	case strings.TrimSpace(sec.Hook) != "":
		return nil, fmt.Errorf("a harness API key is no hook's signing secret: it takes no hook")
	}
	h, ok := agents.Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("harness %s: the yard has no harness file for it (yardr harness list)", kind)
	}
	if len(h.LoginEnv) == 0 {
		return nil, fmt.Errorf("harness %s: its file names no login_env, so there is no API key to ask for", h.Kind)
	}
	crew := strings.TrimSpace(sec.Crew)
	if crew == "" {
		return nil, fmt.Errorf("a harness API key is asked for the crew member whose harness runs with it")
	}
	if w.Bucket(bCrew).Get([]byte(crew)) == nil {
		return nil, fmt.Errorf("a harness API key is asked for a crew member of the yard (yardr crew list): crew member %s: %w", crew, ErrNotFound)
	}
	for _, n := range sec.Env {
		if !slices.Contains(h.LoginEnv, n) {
			return nil, fmt.Errorf("harness %s: %s is no variable of its API key (its file names %s)", h.Kind, n, strings.Join(h.LoginEnv, ", "))
		}
	}
	names := slices.Clone(h.LoginEnv)
	if len(sec.Env) > 0 {
		names = slices.DeleteFunc(slices.Clone(h.LoginEnv), func(n string) bool { return !slices.Contains(sec.Env, n) })
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case !ops.is(to):
		return nil, fmt.Errorf("a secret is %s to give (%s): giving a harness API key is theirs alone, so %s cannot be asked for one", ops.article(), ops, to)
	}
	return &AskSecret{Harness: h.Kind, Crew: crew, Env: names}, nil
}

// askedAddress checks an ask for the user's own address in the source d
// declares, asked of to, and answers it as the ask stores it: the one
// variable the source's notify key names. It refuses a source that reaches
// no person, a user the yard does not have, a name the notify key does not
// name, and anyone but that user and the operator as the one asked: where a
// user is told is theirs to say, or the operator's for them (mayTell).
func (w *wtx) askedAddress(d *source.Declaration, user string, env []string, to string) (*AskSecret, error) {
	if d.Notify == nil {
		return nil, fmt.Errorf("source %s: its declaration has no notify key, so it reaches no person and there is no address of %s's to ask for", d.Name, user)
	}
	if _, err := getUser(w.Tx, user); err != nil {
		return nil, fmt.Errorf("an address is asked for a user of the yard (yardr user list): %w", err)
	}
	for _, n := range env {
		if n != d.Notify.AddressEnv {
			return nil, fmt.Errorf("source %s: %s is not what it reads of a user's file (its notify key names %s)", d.Name, n, d.Notify.AddressEnv)
		}
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	if to != user && !ops.is(to) {
		whose := fmt.Sprintf("%s's own address in the source %s is theirs to give", user, d.Name)
		if !ops.none() && !ops.is(user) {
			whose += ", or " + ops.whose() + " for them"
		}
		return nil, fmt.Errorf("%s: what lands in their inbox goes there, so %s cannot be asked for it", whose, to)
	}
	return &AskSecret{Source: d.Name, Env: []string{d.Notify.AddressEnv}, Host: sourceHost(d), User: user}, nil
}

// askedCrewSecret checks an ask for the crew member's own credential in
// the source d declares, asked of to, and answers it as the ask stores
// it: the source by its own name, the names in the declaration's order
// (the mint's inputs where the credential is minted, else every one of
// it, when the asker narrowed to none), and the host. It refuses a source
// whose declaration names no credential, a crew member the yard does not
// have, a name the declaration does not have, and anyone but the operator
// as the one asked: giving is setting what a session of the yard runs
// with, which is the operator's alone.
func (w *wtx) askedCrewSecret(d *source.Declaration, crew string, env []string, to string) (*AskSecret, error) {
	if d.Credential == nil {
		return nil, fmt.Errorf("source %s: its declaration names no credential, so there is no secret to ask for", d.Name)
	}
	if w.Bucket(bCrew).Get([]byte(crew)) == nil {
		return nil, fmt.Errorf("a crew member's credential is asked for a crew member of the yard (yardr crew list): crew member %s: %w", crew, ErrNotFound)
	}
	has := d.Credential.Variables()
	for _, n := range env {
		if !slices.Contains(has, n) {
			return nil, fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", d.Name, n, d.Credential.Names())
		}
	}
	// Without --env the ask is for what the source needs given, as a
	// source's own credential is: the mint's inputs where it is minted,
	// else every variable of it.
	names := d.Credential.MintOrStatic()
	if len(env) > 0 {
		names = slices.DeleteFunc(slices.Clone(has), func(n string) bool { return !slices.Contains(env, n) })
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	switch {
	case ops.none():
		return nil, fmt.Errorf("a secret is the operator's to give, and this yard has none: yardr settings set operator <name>")
	case !ops.is(to):
		return nil, fmt.Errorf("a secret is %s to give (%s): giving a crew member's credential is theirs alone, so %s cannot be asked for one", ops.article(), ops, to)
	}
	return &AskSecret{Source: d.Name, Env: names, Host: sourceHost(d), Crew: crew, Concealed: concealedAsk(d, names)}, nil
}

// askedUserSecret checks an ask for the user's own credential in the
// source d declares, asked of to, and answers it as the ask stores it:
// the source by its own name, the names in the declaration's order (every
// name under mint where the credential is minted, else the static ones,
// when the asker narrowed to none), and the host. It refuses a source
// whose declaration names no credential, a user the yard does not have,
// a name the declaration does not have, and anyone but that user and the
// operator as the one asked: a user's own credential is theirs to give,
// or the operator's for them, as their address is (mayTell).
func (w *wtx) askedUserSecret(d *source.Declaration, user string, env []string, to string) (*AskSecret, error) {
	if d.Credential == nil {
		return nil, fmt.Errorf("source %s: its declaration names no credential, so there is no secret to ask for", d.Name)
	}
	if _, err := getUser(w.Tx, user); err != nil {
		return nil, fmt.Errorf("a user's credential is asked for a user of the yard (yardr user list): %w", err)
	}
	has := d.Credential.Variables()
	for _, n := range env {
		if !slices.Contains(has, n) {
			return nil, fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", d.Name, n, d.Credential.Names())
		}
	}
	names := d.Credential.MintOrStatic()
	if len(env) > 0 {
		names = slices.DeleteFunc(slices.Clone(has), func(n string) bool { return !slices.Contains(env, n) })
	}
	ops, err := operatorsIn(w.Tx)
	if err != nil {
		return nil, err
	}
	if to != user && !ops.is(to) {
		whose := fmt.Sprintf("%s's own credential in the source %s is theirs to give", user, d.Name)
		if !ops.none() && !ops.is(user) {
			whose += ", or " + ops.whose() + " for them"
		}
		return nil, fmt.Errorf("%s: a call made as them reads it, so %s cannot be asked for it", whose, to)
	}
	return &AskSecret{Source: d.Name, Env: names, Host: sourceHost(d), As: user, Concealed: concealedAsk(d, names)}, nil
}

// GiveSecret answers the secret ask ask on the bead id with values, the
// names it asked for to the values they take: the values are written into
// the file the source's credential is read from (KeySource), and the answer
// is a note of kind answer that holds the names and never a value. It
// answers where the credential is set now, as KeySource does.
//
// It refuses an ask that is not on the bead, asks for no secret or was
// declined; names that are not exactly the ones asked for; whoever may not
// set that source's credential (maySource: the operator, and never the pane
// of an agent); and a source whose calls go elsewhere than the ask's card
// says. No refusal repeats a name or a value that was sent.
//
// An ask for a user's own address in the source (AskSecret.User) is given
// the same way into that user's env file (keyUser), by whoever may say
// where the user is told (mayTell: the user and the operator, and never the
// pane of an agent). It sets no notify: that the user is told through the
// source is a second thing, and theirs to say.
//
// An ask for a user's own credential in the source (AskSecret.As) is given
// the same way into that user's env file (keyUser for a credential), by
// the user or the operator for them, as their address is (mayTell). The
// source needs no notify key: a source that is called as a user need tell
// nobody.
//
// An ask for a crew member's own credential in the source (AskSecret.Crew)
// is given the same way into that crew member's env file
// (giveCrewSecret), by the operator alone: the crew member's running
// session does not see the new value, which takes effect when it next
// starts. It restarts nothing: that is a second thing, and the card and
// the answer say when the value takes effect.
//
// An ask for a harness API key (AskSecret.Harness) is given the same way
// into the operator's key file of the kind (giveHarnessKey), by the
// operator alone: no source declares it, so the kind's harness file holds
// the names instead, and it takes effect as each session of the kind on
// the operator's login next starts.
//
// An ask for a hook's signing secret (AskSecret.Hook) is given the same
// way into the hook's secret file the yard owns (giveHookSecret), by the
// operator alone: the hook the ask names must still be the yard's to
// add, and the hook add that names the file is a second ask.
//
// An ask for a waiting login's input (AskSecret.Login) is handed to the
// login that waits under its reference (LoginWait) and goes to no file: the
// answer note holds the names and never the value, as any giving does, and
// is refused when no login waits under it, or when the login that waits is
// another user's than the ask is for (giveLogin). Giving is the operator's,
// as a source's is (maySource); for a user's own login (AskSecret.As) it is
// that user's or the operator's for them (mayTell), as for their own
// credential.
//
// Giving again is allowed while the ask is neither declined nor taken
// back, as a newer pick replaces an older one: each giving is a note with
// the names.
//
// The answer note is put in the transaction that the file's rename ends
// (keySource), so the store never says "gave" of a value the file does not
// hold. The other way round is not held: when that transaction fails after
// the rename the value is in the file, the ask is open and has no answer
// (*KeyNotRecorded), and giving again answers it. A login's value is handed
// before its note for the same reason in reverse: the wait ends with the
// handing, so a note that failed after it leaves the login answered and the
// ask open, where the other order leaves the ask answered and the login
// waiting.
//
// It is the one call of the operator's that a device may make, and only a
// device of the operator, or of the user whose own address or credential
// is asked for (rpc.OperatorCalls): the note and the events of such a
// giving say how it came, as a device's other writes do (writeVia).
//
// Every secret ask is weighty (weight.go: it gives the yard a credential),
// so a giving is made only by a call that says its person saw the weight
// first (weighed). Without it the giving is refused before anything is
// written (weighedRefusal): no file, no note, and the ask stays open. The
// yard checks what the client says and no more.
func (s *Store) GiveSecret(ctx context.Context, id string, ask uint64, values map[string]string, actor string, weighed bool) (*SourceKeyed, error) {
	var k sourceKey
	var lg loginSent
	err := s.writeVia(ctx, func(w *wtx) error { return w.giveSecret(ctx, s, s.home, id, ask, values, actor, weighed, &k, &lg) })
	if err != nil {
		return nil, k.failed(err)
	}
	if lg.sent {
		return &SourceKeyed{Given: lg.given, Login: lg.id}, nil
	}
	return k.keyed(s.home), nil
}

// giveSecret is GiveSecret inside the transaction: the checks, the answer
// note, and last the file (keySource), after whose rename nothing may
// follow but the commit. A batch may run it twice for one call (write):
// the note of the run that is rolled back goes with it, and the second run
// finds the file's lines and writes the same content. A login's handing
// runs once: the second run finds lg set and writes the note only.
func (w *wtx) giveSecret(ctx context.Context, s *Store, home, id string, ask uint64, values map[string]string, actor string, weighed bool, k *sourceKey, lg *loginSent) error {
	resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	actor = resolved
	row, err := w.loadBead(id)
	if err != nil {
		return err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	asked, err := givable(row.ID, notes, ask, values, now())
	if err != nil {
		return err
	}
	sec := asked.Secret
	// Before the note is put: an agent's giving writes nothing, not even
	// what the transaction would take back.
	if sec.Harness != "" {
		err = w.operatorOnly(ctx, actor, "give a harness API key", Alone)
	} else if sec.User != "" {
		err = w.mayTell(ctx, sec.User, actor)
	} else if sec.Login != "" && sec.As != "" {
		err = w.mayAnswerLogin(ctx, sec.As, actor)
	} else if sec.As != "" {
		err = w.mayTell(ctx, sec.As, actor)
	} else if sec.Crew != "" {
		err = w.operatorOnly(ctx, actor, "give a crew member's credential", Alone)
	} else if sec.Hook != "" {
		err = w.operatorOnly(ctx, actor, "give a hook's signing secret", Alone)
	} else if sec.Login != "" {
		err = w.maySource(ctx, actor, "answering the login", Alone)
	} else {
		err = w.maySource(ctx, actor, "setting a source's credential", Alone)
	}
	if err != nil {
		return err
	}
	// Weighed again, as an approval weighs every card: an ask written
	// before secrets were weighed showed none, and is given no more. It
	// is weighty, so it is given by a client that says its person saw it.
	weight := secretWeight(*sec)
	if err := weightStale(ask, storedWeight(notes, ask), weight); err != nil {
		return err
	}
	if err := weighedRefusal(ask, weight, weighed); err != nil {
		return err
	}
	if sec.Harness != "" {
		return w.giveHarnessKey(ctx, home, row, ask, sec, values, actor, k)
	}
	if sec.Login != "" {
		return w.giveLogin(s, row, ask, sec, asked.To, values, actor, lg)
	}
	var src Source
	if ok, err := get(w.Tx, bSources, sec.Source, &src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("ask %d: source %s: %w", ask, sec.Source, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	d = d.WithBase(src.Base)
	if host := sourceHost(d); host != sec.Host {
		return fmt.Errorf("ask %d says the secret is for the source %s at %s, and the source's calls go to %s now: the ask is from before the source was replaced, and is to be asked again",
			ask, sec.Source, sec.Host, host)
	}
	// Before the note is put: a URL-credential value that is no address
	// is refused here, as the writers below refuse it, so a refused value
	// leaves neither the note nor the file. A user's own address and a
	// hook's secret are not the credential and pass unchecked.
	if sec.Hook == "" && sec.User == "" {
		if err := checkURLCredential(d, sec.Source, values); err != nil {
			return err
		}
	}
	text, err := json.Marshal(Answer{Ask: ask, Given: sec.Env})
	if err != nil {
		return err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return err
	}
	if sec.Hook != "" {
		return w.giveHookSecret(home, sec, &src, values, k)
	}
	if sec.User != "" {
		return w.keyUser(ctx, home, sec.User, sec.Source, values, actor, false, k)
	}
	if sec.As != "" {
		return w.keyUser(ctx, home, sec.As, sec.Source, values, actor, true, k)
	}
	if sec.Crew != "" {
		return w.giveCrewSecret(ctx, home, sec, &src, values, actor, k)
	}
	return w.keySource(ctx, home, sec.Source, values, actor, k)
}

// giveHookSecret is giveSecret for a hook's signing secret: the value
// goes to the hook's secret file the yard owns (HookSecretFile), in the
// order keySource keeps: the answer note is put first and the file's
// rename last, so the store never says "gave" of a value the file does
// not hold, and a commit that fails after the rename leaves the value
// in the file with the ask open (KeyNotRecorded, through k). It refuses
// a hook the yard has taken since the ask, as askedHookSecret refused
// one it had then: the hook that stands names its own secret file.
func (w *wtx) giveHookSecret(home string, sec *AskSecret, src *Source, values map[string]string, k *sourceKey) error {
	if w.Bucket(bHooks).Get([]byte(sec.Hook)) != nil {
		return fmt.Errorf("hook %s is in the yard already: its signing secret is the file it names, changed in a terminal", sec.Hook)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	if d.Hook == nil {
		return fmt.Errorf("source %s: its declaration has no hook key now, so there is no signing secret to give", sec.Source)
	}
	k.src, k.hook, k.names = src, sec.Hook, slices.Clone(sec.Env)
	file, renamed, err := writeHookSecret(home, sec.Hook, values[sec.Env[0]])
	k.file, k.renamed = file, k.renamed || renamed
	if err != nil {
		return err
	}
	if afterKeyRename != nil {
		return afterKeyRename()
	}
	return nil
}

// crewEnvFile is where the yard keeps what a source needs of the crew
// member name when nobody named a file for them: <home>/crew/<name>.env,
// beside the store and not in it. A name with a path separator in it
// could name a file outside that directory or another member's, so the
// yard chooses no file for it.
func crewEnvFile(home, name string) (string, error) {
	if strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("crew member %s has no env_file, and the yard chooses no file for a name with a path separator in it: yardr crew set %s env_file=<file> first", name, name)
	}
	return filepath.Join(home, CrewDir, name+".env"), nil
}

// giveCrewSecret is giveSecret for a crew member's own credential in the
// source: the values go to the crew member's env file, in the order
// keySource keeps: the answer note is put first and the file's rename
// last, so the store never says "gave" of a value the file does not
// hold, and a commit that fails after the rename leaves the value in the
// file with the ask open (KeyNotRecorded, through k). With no env_file
// set the file is crewEnvFile, which is then set as yardr crew set sets
// it, through the same write. The crew member the ask names must still
// be the yard's, and the names are held to the source's credential as it
// stands now, since its declaration may have changed since the ask. The
// running session is restarted by nothing here: it reads its environment
// when its next session starts.
func (w *wtx) giveCrewSecret(ctx context.Context, home string, sec *AskSecret, src *Source, values map[string]string, actor string, k *sourceKey) error {
	var r CrewMember
	if ok, err := get(w.Tx, bCrew, sec.Crew, &r); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("crew member %s: %w", sec.Crew, ErrNotFound)
	}
	d, err := src.Declaration()
	if err != nil {
		return err
	}
	if d.Credential == nil {
		return fmt.Errorf("source %s: its declaration names no credential now, so there is no secret to give", sec.Source)
	}
	// givable held the names to the ask's; the declaration is the
	// writer's to check, without saying back what was sent.
	names := make([]string, 0, len(values))
	for n := range values {
		if !slices.Contains(d.Credential.Variables(), n) {
			return fmt.Errorf("source %s: %s is no variable of its credential now (its declaration names %s)", sec.Source, n, d.Credential.Names())
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if err := checkURLCredential(d, sec.Source, values); err != nil {
		return err
	}
	saved, file, err := w.crewSecretFile(ctx, home, &r, actor)
	if err != nil {
		return err
	}
	k.src, k.crew, k.names = src, saved, names
	resolved, renamed, err := config.WriteEnvFile(file, values)
	k.file = resolved
	k.renamed = k.renamed || renamed
	if err != nil {
		return fmt.Errorf("crew member %s: %w", sec.Crew, err)
	}
	if afterKeyRename != nil {
		return afterKeyRename()
	}
	return nil
}

// crewSecretFile is the env file a secret for the crew member r is written
// to: the env_file it has, or crewEnvFile when it has none, which is then
// set as yardr crew set sets it, through the same write. It answers the
// member as the write leaves it.
func (w *wtx) crewSecretFile(ctx context.Context, home string, r *CrewMember, actor string) (*CrewMember, string, error) {
	file, _ := r.Config["env_file"].(string)
	if file != "" {
		return r, file, nil
	}
	// The server chooses the path: nothing a caller sends is one.
	file, err := crewEnvFile(home, r.Name)
	if err != nil {
		return nil, "", err
	}
	def := *r
	def.Config = make(map[string]any, len(r.Config)+1)
	for key, value := range r.Config {
		def.Config[key] = value
	}
	def.Config["env_file"] = file
	def.Revision = r.Revision
	saved, err := w.putCrew(ctx, def, actor, nil)
	if err != nil {
		return nil, "", err
	}
	return saved, file, nil
}

// HarnessDir is the directory of a yard's home where the yard keeps the
// API key of each harness kind it was given, one file per login user
// (HarnessKeyFile).
const HarnessDir = "harness"

// HarnessKeyFile is where the yard keeps the API key of the harness kind
// for the sessions that burn user's login: <home>/harness/<kind>/<user>.env,
// beside the store and not in it. The kind comes first, so a user's name
// is one file's name and never a directory's. Every session of the kind on
// that login reads it as it starts (the runner), so a key given once
// reaches the crew, the groups and the conversations that burn the login,
// as the machine's login of the kind does. A kind or a name that could
// name a file elsewhere (a path separator, a dot name) has no file.
func HarnessKeyFile(home, kind, user string) (string, error) {
	for _, part := range []string{kind, user} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("the yard keeps no harness key file for kind %q and user %q: neither may be empty, a dot name or hold a path separator", kind, user)
		}
	}
	return filepath.Join(home, HarnessDir, kind, user+".env"), nil
}

// giveHarnessKey is giveSecret for a harness API key: the values go to the
// operator's key file of the kind (HarnessKeyFile), in the order keySource
// keeps: the answer note is put first and the file's rename last, so the
// store never says "gave" of a value the file does not hold, and a commit
// that fails after the rename leaves the value in the file with the ask
// open (KeyNotRecorded, through k). No source declares the names: the
// kind's harness file holds them as it stands now. The file is the
// operator's because the led login asks the operator, and an unset
// login_for is the operator's (Group.EffectiveLogin): a group that burns
// another user's login reads that user's file, which this never writes.
// No crew member's env_file is set. The running sessions are restarted by
// nothing here: each reads the file when its next session starts.
func (w *wtx) giveHarnessKey(ctx context.Context, home string, row *beadRow, ask uint64, sec *AskSecret, values map[string]string, actor string, k *sourceKey) error {
	if w.Bucket(bCrew).Get([]byte(sec.Crew)) == nil {
		return fmt.Errorf("crew member %s: %w", sec.Crew, ErrNotFound)
	}
	h, ok := agents.Lookup(sec.Harness)
	if !ok || len(h.LoginEnv) == 0 {
		return fmt.Errorf("harness %s: its file names no login_env now, so there is no API key to give", sec.Harness)
	}
	// givable held the names to the ask's; the harness file is the
	// writer's to check, without saying back what was sent.
	names := make([]string, 0, len(values))
	for n := range values {
		if !slices.Contains(h.LoginEnv, n) {
			return fmt.Errorf("harness %s: %s is no variable of its API key now (its file names %s)", sec.Harness, n, strings.Join(h.LoginEnv, ", "))
		}
		names = append(names, n)
	}
	sort.Strings(names)
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if operator == "" {
		return fmt.Errorf("a harness API key is kept for the operator's login, and this yard has none: yardr settings set operator <name>")
	}
	file, err := HarnessKeyFile(home, h.Kind, operator)
	if err != nil {
		return err
	}
	text, err := json.Marshal(Answer{Ask: ask, Given: sec.Env})
	if err != nil {
		return err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return err
	}
	k.harness, k.login, k.names = &h, operator, names
	resolved, renamed, err := config.WriteEnvFile(file, values)
	k.file = resolved
	k.renamed = k.renamed || renamed
	if err != nil {
		return fmt.Errorf("harness %s: %w", h.Kind, err)
	}
	if afterKeyRename != nil {
		return afterKeyRename()
	}
	return nil
}

// giveLogin is giveSecret for an ask for a waiting login's input: the value
// is handed to the login that waits under sec's reference and the answer
// note holds the names, to no file. The login must be the one of the user the
// ask is for: the user whose own login it is (sec.As), else the one it is
// addressed to (to), the operator by askedLogin. The reference alone ties a
// wait to nobody, so a wait that is another user's is refused here and left
// waiting for the ask that is its own. The handing runs once per call: a
// batch rerun finds lg set and writes the note only. No refusal repeats a
// value that was sent.
func (w *wtx) giveLogin(s *Store, row *beadRow, ask uint64, sec *AskSecret, to string, values map[string]string, actor string, lg *loginSent) error {
	if !lg.sent {
		for _, n := range sec.Env {
			if err := loginValueError(ask, n, values[n]); err != nil {
				return err
			}
		}
		// givable held the names to the ask's: the one value is the one's.
		forUser := or(sec.As, to)
		owner, waits, handed := s.handLogin(sec.Login, forUser, values[sec.Env[0]])
		switch {
		case !waits:
			return fmt.Errorf("ask %d names the waiting login %s, and no login waits under it: the login ended, or its wait ran out, and its next input need names the reference to ask for", ask, sec.Login)
		case !handed:
			// Only a yard that names no operator has a wait with no owner.
			whose := owner + "'s"
			if owner == "" {
				whose = "nobody's, because this yard names no operator (yardr settings set operator <name>)"
			}
			return fmt.Errorf("ask %d is for %s's login, and the login waiting under %s is %s: a code is handed to the login of the user it is for, and that login keeps waiting for the ask that is its own", ask, forUser, sec.Login, whose)
		}
		lg.sent, lg.id, lg.given = true, sec.Login, slices.Clone(sec.Env)
	}
	text, err := json.Marshal(Answer{Ask: ask, Given: sec.Env})
	if err != nil {
		return err
	}
	_, err = w.addNoted(row, NoteAnswer, string(text), actor)
	return err
}

// givable decides whether values may be given to the ask seq among notes,
// the notes of the bead id, at the time at, and answers the ask, whose Secret
// is what it asks for and whose To is who it is asked of. The names of values
// are held to the ask's here and to the source's declaration by keySource or
// keyUser; the values are the writer's to check (config.WriteEnvFile,
// loginValueError for a waiting login).
func givable(id string, notes []Note, seq uint64, values map[string]string, at time.Time) (*Ask, error) {
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, at)
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, id)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Secret == nil:
		return nil, fmt.Errorf("ask %d asks for no secret: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, id, seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	}
	sec := r.Ask.Secret
	// What was sent is not said back: a client that put a value where a
	// name goes would read it in the refusal.
	for n := range values {
		if !slices.Contains(sec.Env, n) {
			return nil, fmt.Errorf("ask %d asks for %s, and was given a name it did not ask for", seq, sec.Names())
		}
	}
	for _, n := range sec.Env {
		if _, ok := values[n]; !ok {
			return nil, fmt.Errorf("ask %d asks for %s: %s was not given", seq, sec.Names(), n)
		}
	}
	return &r.Ask, nil
}
