package source

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/user"
	"slices"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/config"
)

// A credential that may be minted is not a secret but where one is made:
// the declaration names the variables under mint, and the operator sets the
// source's mint_command, the command that turns their values into a
// short-lived token. Both credential paths resolve through here: a thread
// command, with the acting crew member's env and the caller's scope, and a
// declared call, with the source's own env file and no scope.

// MintTimeout bounds one run of a source's mint_command: a command that
// waits on its service must not hold up the call it mints for. A variable
// so a test need not wait it out.
var MintTimeout = 30 * time.Second

// mintLeft is how long before a minted token expires it is no longer used:
// a call made with it must still be answered within its life, with room
// for a clock that disagrees.
const mintLeft = 5 * time.Minute

// mintNow is the clock minted tokens are aged by; tests move it.
var mintNow = time.Now

// Missing is what Select fails with when nothing it was given sets the
// source's credential. Names says what would, for the caller to say where.
type Missing struct{ Names string }

func (e *Missing) Error() string { return e.Names + " is not set" }

// NoMintCommand is what resolving a minted credential fails with when the
// source has no mint_command setting: the operator's command that mints
// the token is not set. Source is the source that needs it.
type NoMintCommand struct{ Source string }

func (e *NoMintCommand) Error() string {
	return "no mint_command is set: yardr source set " + e.Source + " mint_command=<program> [args...]"
}

// Select applies the credential precedence to the acting env: the mint's
// inputs when it sets every name the file lists under mint, one of them
// alone refused, else the value of the static variable for this use. get is
// what the env sets a name to. inputs is non-nil for a credential to mint;
// value is the static one, used as it is. err is a *Missing when the env
// sets nothing the credential goes by.
func (c *Credential) Select(static string, get func(string) string) (value string, inputs map[string]string, err error) {
	if c.Minted() {
		inputs = map[string]string{}
		var set, missing []string
		for _, m := range c.Mint {
			if v := strings.TrimSpace(get(m)); v != "" {
				inputs[m], set = v, append(set, m)
			} else {
				missing = append(missing, m)
			}
		}
		switch {
		case len(missing) == 0:
			return "", inputs, nil
		case len(set) > 0:
			return "", nil, fmt.Errorf("%s is set without %s: a minted credential takes every name", strings.Join(set, " and "), strings.Join(missing, " and "))
		}
	}
	if value = strings.TrimSpace(get(static)); value != "" {
		return value, nil, nil
	}
	if !c.Minted() {
		return "", nil, &Missing{Names: static}
	}
	return "", nil, &Missing{Names: c.Names()}
}

// mintScheme starts the credential Select returns for a mint's inputs: JSON
// of the inputs, base64ed. No minted token starts so: Mint answers a token,
// never another reference.
const mintScheme = "mint:v1:"

// MintRef is the credential for inputs, the mint's inputs as the acting env
// sets them: for the call to resolve with the caller's scope, since the env
// itself is not passed on. It holds the values, so it is never shown,
// logged or kept: the call it was read for resolves it and drops it.
func MintRef(inputs map[string]string) string {
	raw, _ := json.Marshal(inputs)
	return mintScheme + base64.RawURLEncoding.EncodeToString(raw)
}

// UnmintRef reads the mint inputs ref holds: nil when ref is no MintRef (a
// static credential, used as it is). What starts so but holds anything else
// than exactly names is refused, without showing it: it may hold secrets.
func UnmintRef(ref string, names []string) (map[string]string, error) {
	enc, ok := strings.CutPrefix(ref, mintScheme)
	if !ok {
		return nil, nil
	}
	var inputs map[string]string
	if raw, err := base64.RawURLEncoding.DecodeString(enc); err != nil || json.Unmarshal(raw, &inputs) != nil {
		return nil, fmt.Errorf("this credential starts with %q, as a minted one does, but holds no mint's inputs", mintScheme)
	}
	for _, n := range names {
		if _, has := inputs[n]; !has {
			return nil, fmt.Errorf("this credential starts with %q, as a minted one does, but holds no mint's inputs", mintScheme)
		}
	}
	if len(inputs) != len(names) {
		return nil, fmt.Errorf("this credential starts with %q, as a minted one does, but holds no mint's inputs", mintScheme)
	}
	return inputs, nil
}

// mintedToken is a token in hand, with when the minter says it expires.
type mintedToken struct {
	token   string
	expires time.Time
}

// minted are the tokens in hand, by source, mint command, credential and
// scope. They are kept in this process's memory only: never in the store,
// an event or a log, and a new process asks for its own.
var mintedTokens = struct {
	sync.Mutex
	m map[string]mintedToken
}{m: map[string]mintedToken{}}

// mintKey is the key the token inputs mints with scope is kept under: the
// source, the command that mints it, the credential's identity and the
// scope. The identity is a hash of the inputs, so the key holds no secret
// value.
func mintKey(source string, argv []string, inputs map[string]string, scope string) string {
	sum := sha256.New()
	for _, n := range slices.Sorted(maps.Keys(inputs)) {
		fmt.Fprintf(sum, "%d:%s%d:%s", len(n), n, len(inputs[n]), inputs[n])
	}
	return source + "\x00" + strings.Join(argv, "\x00") + "\x00" + hex.EncodeToString(sum.Sum(nil)) + "\x00" + scope
}

// Mint is the token inputs mints with scope, and when it expires: the named
// values read from the acting env, and the caller's scope (the thread, or
// the place for an open), so the token narrows where the service allows, ""
// where the caller has none. argv is the source's mint_command, one line
// split on spaces, run without a shell: it reads {inputs, scope} on stdin
// and answers {token, expires_at}. The token is kept in this process's
// memory only and asked for again shortly before it expires; a token the
// service no longer takes is not dropped, since the core cannot see a
// command's answer. Held while the token is asked for, so two calls at once
// ask once.
func Mint(ctx context.Context, d *Declaration, argv []string, inputs map[string]string, scope string) (string, time.Time, error) {
	if len(argv) == 0 {
		return "", time.Time{}, &NoMintCommand{Source: d.Name}
	}
	key := mintKey(d.Name, argv, inputs, scope)
	// Held while the token is asked for, so two calls at once ask once.
	mintedTokens.Lock()
	now := mintNow()
	for key, cached := range mintedTokens.m {
		if !now.Before(cached.expires) {
			delete(mintedTokens.m, key)
		}
	}
	t, ok := mintedTokens.m[key]
	if !ok || !now.Before(t.expires.Add(-mintLeft)) {
		var err error
		if t, err = mint(ctx, d, argv, inputs, scope); err != nil {
			delete(mintedTokens.m, key)
			mintedTokens.Unlock()
			return "", time.Time{}, err
		}
		mintedTokens.m[key] = t
	}
	mintedTokens.Unlock()
	return t.token, t.expires, nil
}

// osUserLookup is user.Current as a variable, so a test can be a machine
// whose user lookup fails or blocks (yardr-tbm4, yardr-8ms7).
var osUserLookup = user.Current

// mintUser is the user name the mint command runs under: USER and LOGNAME
// as the asking process has them, each empty one taken from the OS's own
// name for the process's uid. The server and the daemon start with
// neither variable (server.ServerEnv passes none), so without the
// fallback they mint with an empty user and a helper that reads the
// keyring by user name answers with none. The OS is asked only when one
// of them is empty, where its lookup can hang past signals, and a lookup
// that answers with no user leaves the empty names as they are.
func mintUser() (name, login string) {
	name, login = os.Getenv("USER"), os.Getenv("LOGNAME")
	if name != "" && login != "" {
		return name, login
	}
	if who, err := config.LookupUsername(osUserLookup); err == nil && who != "" {
		if name == "" {
			name = who
		}
		if login == "" {
			login = who
		}
	}
	return name, login
}

// mint runs argv and reads its {token, expires_at}. Its environment is an
// allow-list: PATH, HOME, USER and LOGNAME, and YARDR_SOURCE_BASE, the
// declaration's base, so the command speaks to the same service the call
// goes to; the inputs reach it on stdin alone. USER and LOGNAME are the
// asking user's name, from the asking process or, where it has none,
// from the OS's own user for the process's uid, because a helper that
// reads the asking user's keyring looks it up by user name, and answers
// with no user without one. TMPDIR and the rest of the caller's
// environment stay out: a short-lived mint wants no private temp dir of
// its own, and locale, terminal and session variables carry nothing a
// mint needs.
func mint(ctx context.Context, d *Declaration, argv []string, inputs map[string]string, scope string) (mintedToken, error) {
	var none mintedToken
	in := map[string]any{"inputs": inputs, "scope": scope}
	name, login := mintUser()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "USER=" + name, "LOGNAME=" + login}
	env = append(env, config.Env("SOURCE_BASE", d.Base)...)
	what := "mint_command " + strings.Join(argv, " ")
	var t mintedToken
	err := Run(ctx, what, argv, in, env, "", MintTimeout, func(out []byte) error {
		var o struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {token, expires_at}: %v", err)
		}
		t.token = strings.TrimSpace(o.Token)
		switch {
		case t.token == "":
			return fmt.Errorf("its answer holds no token")
		case strings.ContainsFunc(t.token, func(r rune) bool { return r < ' ' || r == 0x7f }):
			return fmt.Errorf("the token it answered has a control character in it, a line break perhaps: it is one line")
		}
		var err error
		if t.expires, err = time.Parse(time.RFC3339, strings.TrimSpace(o.ExpiresAt)); err != nil {
			return fmt.Errorf("its answer holds no expiry yardr reads (%q): expires_at is a time as RFC 3339 writes one", o.ExpiresAt)
		}
		if !mintNow().Before(t.expires) {
			return fmt.Errorf("the token it answered is already expired (expires_at %s)", o.ExpiresAt)
		}
		return nil
	})
	if err != nil {
		return none, fmt.Errorf("source %s: %w", d.Name, err)
	}
	return t, nil
}
