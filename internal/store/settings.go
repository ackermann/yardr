package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
)

// bSettings holds the yard's own defaults, key -> value (a JSON string),
// such as default_kind, the agent kind a herdr group or crew member gets when
// its config names none. Which keys exist and what each accepts is
// Settings' table below, which SetSetting holds a write to.
var bSettings = []byte("settings")

func init() { allBuckets = append(allBuckets, bSettings) }

// YardmasterSetting names the crew member that coordinates the yard, whom the
// role prompts tell agents to mail and who may remove a route beside the
// operator.
const YardmasterSetting = "yardmaster"

// The settings whose readers live in packages that import this one: named
// here so that the table can list them, and there under their own names.
const (
	// StyleSetting is how agents should work with the operator, one of
	// OperatorStyles, and NoteSetting one line they should know about them.
	StyleSetting = "operator_style"
	NoteSetting  = "operator_note"
	// DefaultKindSetting is the agent kind of herdr groups and crew
	// members whose config names none.
	DefaultKindSetting = "default_kind"
	// DefaultPackSetting names the pack a depot is set up from when depot
	// init is given none.
	DefaultPackSetting = "default_pack"
	// HookAddrSetting is the address the server takes webhooks on.
	HookAddrSetting = "hook_addr"
	// HookPublicURLSetting is the public address hooks are reached at:
	// the URL of hook <name> is it plus /<name>. A hook whose source
	// registers its webhook is registered at that URL.
	HookPublicURLSetting = "hook_public_url"
	// RouterAddrSetting is the address of the yard's router on the user's
	// network, which GiveSecret is taken from besides the yard's own
	// machine (rpc).
	RouterAddrSetting = "router_addr"
	// WebAddrSetting and BDPAddrSetting are the addresses the web view and
	// the BDP server listen on when their --addr is not given.
	WebAddrSetting = "web_addr"
	BDPAddrSetting = "bdp_addr"
	// WebPagesAddrSetting is the address the web view's sandbox origin
	// listens on when its --pages-addr is not given; WebPagesHostSetting
	// is the public name run links use for it behind a proxy of yours.
	WebPagesAddrSetting = "web_pages_addr"
	WebPagesHostSetting = "web_pages_host"
	// WebAsSetting is the user the web view acts as when its --as is not
	// given.
	WebAsSetting = "web_as"
	// WebJWTIssuerSetting, WebJWTAudienceSetting, WebJWTJWKSSetting,
	// WebJWTHeaderSetting and WebJWTUsersSetting shape the web view's JWT
	// mode: who proves each request (internal/web).
	WebJWTIssuerSetting   = "web_jwt_issuer"
	WebJWTAudienceSetting = "web_jwt_audience"
	WebJWTJWKSSetting     = "web_jwt_jwks"
	WebJWTHeaderSetting   = "web_jwt_header"
	WebJWTUsersSetting    = "web_jwt_users"
	// NameSetting is the yard's name.
	NameSetting = "name"
	// MaxSessionsSetting caps the sessions that run on this machine at
	// once, over all groups (MaxSessions).
	MaxSessionsSetting = "max_sessions"
	// HerdrSessionSetting names the herdr session the yard's panes are in.
	HerdrSessionSetting = "herdr_session"
	// UpgradeFromSetting names the depot whose landed build the yard
	// installs itself after the assembly lands on it (yardr upgrade).
	UpgradeFromSetting = "upgrade_from"
	// UpgradeRestoreSetting says whether a failed upgrade restores the
	// pre-install store copies (yardr upgrade): on by default, off stops
	// and tells instead.
	UpgradeRestoreSetting = "upgrade_restore"
)

// DefaultWebAddr and DefaultBDPAddr are where the web view and the BDP
// server listen in a yard that sets neither. DefaultWebPagesAddr is
// where the view's sandbox origin listens: its own loopback port, so a
// scripted page runs on an origin of its own.
const (
	DefaultWebAddr      = "127.0.0.1:8791"
	DefaultBDPAddr      = "127.0.0.1:8765"
	DefaultWebPagesAddr = "127.0.0.1:8789"
)

// herdrSessionName is what herdr 0.9 takes as a session's name.
var herdrSessionName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// httpToken is what a header's name may hold (RFC 9110's token), for the
// web_jwt_header setting.
var httpToken = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// MaxSessions is the max_sessions of settings: the most sessions that run
// something on this machine (RunsHere) the dispatcher has going at once,
// whatever their groups' limits allow, and whether the setting caps at
// all. Unset, nothing is capped; set to 0, nothing starts: the yard is
// paused, and its live sessions run on.
func MaxSessions(settings map[string]string) (max int, capped bool) {
	v, ok := settings[MaxSessionsSetting]
	if !ok || v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// OperatorStyles are the values of operator_style, from the most explaining
// to the least. What each means is the CLI's to say (its form and primes).
var OperatorStyles = []string{"guide", "show", "work", "report"}

// packFile is the file that makes a directory a pack (pack.File, which
// imports this package).
const packFile = "pack.yaml"

// Setting is one of the yard's settings: what it means, what it accepts,
// and what the yard goes by while it is unset.
type Setting struct {
	Key  string
	Help string
	// Default is the value an unset setting reads as; "" when there is
	// none, or when it is no one value (Help says what it is then).
	Default string
	// Env is the environment variable that is used instead of the setting
	// where it is set, without its YARDR_ prefix (config.Getenv).
	Env string
	// Check refuses a value the setting's readers would not take.
	Check func(string) error
}

// partyName is what names a crew member, a group or the yard.
var partyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// KnownSettings are the yard's settings. A key that is not here is read by
// nothing, and SetSetting refuses it.
var KnownSettings = []Setting{
	{Key: OperatorSetting, Help: "who the role prompts name, the person agents bring decisions to (a pack's roles are installed naming them); default: your login name",
		Check: func(string) error { return nil }},
	{Key: YardmasterSetting, Help: "the yardmaster crew member's name, whom the role prompts tell agents to mail, and who may remove a route beside the operator", Default: "yardmaster",
		Check: func(v string) error {
			if !partyName.MatchString(v) {
				return fmt.Errorf("yardmaster name %q: letters, digits, '.', '_', '-', '/'", v)
			}
			return nil
		}},
	{Key: StyleSetting, Help: "how agents should work with the operator, which every session reads when it starts (yardr prime): " +
		strings.Join(OperatorStyles, ", "),
		Check: func(v string) error {
			if !slices.Contains(OperatorStyles, v) {
				return fmt.Errorf("%s %q: there is %s", StyleSetting, v, strings.Join(OperatorStyles, ", "))
			}
			return nil
		}},
	{Key: NoteSetting, Help: "one line agents should know about the operator, read with operator_style",
		Check: func(v string) error {
			if strings.ContainsAny(v, "\n\r") {
				return errors.New(NoteSetting + ": one line")
			}
			return nil
		}},
	{Key: DefaultKindSetting, Help: "the agent kind of herdr groups and crew members whose config names none", Default: "claude",
		Check: func(v string) error {
			if !agents.IsKind(v) {
				return fmt.Errorf("%s %q: herdr starts %s", DefaultKindSetting, v, strings.Join(agents.Kinds(), ", "))
			}
			return nil
		}},
	{Key: DefaultPackSetting, Help: "the pack a depot is set up from when yardr depot init names none, and the pack yardr init takes the yardmaster's role from: " +
		"a pack's name (one the yard has imported, or one yardr carries), its directory, absolute, or a pack's URL; default: starter, and dir for a directory depot",
		Default: "starter", Check: checkDefaultPack},
	{Key: HookAddrSetting, Help: "the address the server takes webhooks on, such as 127.0.0.1:8790, when YARDR_HOOK_ADDR is not set where it starts; " +
		"the server reads it when it starts", Env: "HOOK_ADDR", Check: CheckHookAddr},
	{Key: HookPublicURLSetting, Help: "the public address hooks are reached at, such as https://yard.example/hooks: the URL of hook <name> is it plus /<name>. " +
		"A hook whose source names a register call and secret scheme is registered at that URL when its card is approved; no card for one is written while it is unset",
		Check: CheckHookPublicURL},
	{Key: RouterAddrSetting, Help: "the address of the yard's router on the user's network, such as 172.18.0.2, " +
		"written by the provisioner of a hosted yard: GiveSecret is taken from that peer when it says the request came to it over https " +
		"(X-Forwarded-Proto: https), and from the yard's own machine as before. Read on every call; unset, from the yard's own machine alone",
		Check: func(v string) error {
			if net.ParseIP(v) == nil {
				return fmt.Errorf("%s %q: the router's address as an IP address, such as 172.18.0.2: it is compared with the address the request came from", RouterAddrSetting, v)
			}
			return nil
		}},
	{Key: WebAddrSetting, Help: "the address the web view listens on, on loopback, when YARDR_WEB_ADDR is not set and yardr web serve, start or install is given no --addr; " +
		"a view that runs keeps its address, and a login service the one in its file until it is installed again",
		Default: DefaultWebAddr, Env: "WEB_ADDR", Check: func(v string) error {
			if err := CheckWebAddr("--addr", v, false); err != nil {
				return fmt.Errorf("%s: %w, and a setting has none: an address off loopback is given to yardr web serve --unsafe with --addr", WebAddrSetting, err)
			}
			return nil
		}},
	{Key: WebPagesAddrSetting, Help: "the address the web view's sandbox origin listens on, on loopback, when YARDR_WEB_PAGES_ADDR is not set and yardr web serve, start or install is given no --pages-addr; " +
		"a view that runs keeps its address, and a login service the one in its file until it is installed again",
		Default: DefaultWebPagesAddr, Env: "WEB_PAGES_ADDR", Check: func(v string) error {
			if err := CheckWebAddr("--pages-addr", v, false); err != nil {
				return fmt.Errorf("%s: %w, and a setting has none: an address off loopback is given to yardr web serve --unsafe with --pages-addr", WebPagesAddrSetting, err)
			}
			return nil
		}},
	{Key: WebPagesHostSetting, Help: "the public name run links use for the web view's sandbox origin behind a proxy of yours, when YARDR_WEB_PAGES_HOST is not set and yardr web serve, start or install is given no --pages-host; " +
		"unset, run links use the host the view was reached by",
		Env: "WEB_PAGES_HOST", Check: func(v string) error {
			if _, err := CheckWebHost("--pages-host", v); err != nil {
				return fmt.Errorf("%s: %w", WebPagesHostSetting, err)
			}
			return nil
		}},
	{Key: WebAsSetting, Help: "the user the web view acts as when YARDR_WEB_AS is not set and yardr web serve, start or install is given no --as; " +
		"the view refuses to start when no user of the yard has the name, as --as does; " +
		"a view that runs keeps its user until it is stopped and started again; when YARDR_WEB_AS is unset in a login service's saved environment, its view reads the setting at every start",
		Env: "WEB_AS", Check: func(v string) error {
			if err := checkUserName(v); err != nil {
				return fmt.Errorf("%s: %w", WebAsSetting, err)
			}
			return nil
		}},
	{Key: WebJWTIssuerSetting, Help: "the issuer a gateway's token must carry (its iss claim), such as https://team.cloudflareaccess.com; " +
		"setting it turns the web view's JWT mode on: every request but /health must then carry a token the gateway signed (docs/web.md, \"Behind an auth gateway\"). " +
		"The view refuses to start without web_jwt_audience and web_jwt_jwks, with --as, YARDR_WEB_AS or web_as alongside, or while the keys cannot be fetched",
		Check: func(v string) error {
			if err := checkJWTURL("an issuer", v); err != nil {
				return fmt.Errorf("%s: %w", WebJWTIssuerSetting, err)
			}
			return nil
		}},
	{Key: WebJWTAudienceSetting, Help: "the audience a gateway's token must carry (its aud claim), such as an Access application's AUD tag; " +
		"required in JWT mode, so a token minted for another application is refused",
		Check: func(v string) error {
			if strings.TrimSpace(v) == "" || strings.ContainsAny(v, " \t\n\r") {
				return fmt.Errorf("%s %q: an audience is one word, such as the application's AUD tag", WebJWTAudienceSetting, v)
			}
			return nil
		}},
	{Key: WebJWTJWKSSetting, Help: "the URL the keys that verify a gateway's tokens are fetched from, " +
		"such as https://team.cloudflareaccess.com/cdn-cgi/access/certs; required in JWT mode",
		Check: func(v string) error {
			if err := checkJWTURL("the keys' URL", v); err != nil {
				return fmt.Errorf("%s: %w", WebJWTJWKSSetting, err)
			}
			// A key set fetched over plain http is the network's to
			// rewrite, and its keys admit anyone as anyone: http serves a
			// test issuer on this machine, and a hosted yard's router,
			// only.
			if u, _ := url.Parse(v); u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !isRouterHost(u.Hostname()) {
				return fmt.Errorf("%s %q: http is for a test issuer on loopback or the router; a gateway's keys come over https", WebJWTJWKSSetting, v)
			}
			return nil
		}},
	{Key: WebJWTHeaderSetting, Help: "the header carrying the gateway's token; a Bearer prefix is stripped on Authorization only",
		Default: "Cf-Access-Jwt-Assertion", Check: func(v string) error {
			if !httpToken.MatchString(v) {
				return fmt.Errorf("%s %q: a header's name, such as X-Pomerium-Jwt-Assertion", WebJWTHeaderSetting, v)
			}
			return nil
		}},
	{Key: WebJWTUsersSetting, Help: "whose token acts as whom in JWT mode: email=user pairs separated by commas, each user a user of the yard; " +
		"matched without regard to case. A token whose email maps to no user is refused, and the view refuses to start when no user of the yard has a name in the table",
		Check: func(v string) error {
			if _, err := ParseWebJWTUsers(v); err != nil {
				return fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
			}
			return nil
		}},
	{Key: BDPAddrSetting, Help: "the address the BDP server listens on when YARDR_BDP_ADDR is not set and yardr bdp serve, start or install is given no --addr; " +
		"a server that runs keeps its address, and a login service the one in its file until it is installed again",
		Default: DefaultBDPAddr, Env: "BDP_ADDR", Check: func(v string) error { return checkHostPort("BDP address", v, DefaultBDPAddr) }},
	{Key: TimerLimitSetting, Help: "how many live timers one crew member or user may have set (yardr timer)",
		Default: strconv.Itoa(DefaultTimerLimit), Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return fmt.Errorf("%s %q: a whole number, 1 or more", TimerLimitSetting, v)
			}
			return nil
		}},
	{Key: TimerMinEverySetting, Help: "the shortest period of a recurring timer (yardr timer add --every), such as 30m or 1d",
		Default: Span(DefaultTimerMinEvery), Check: func(v string) error {
			if _, err := ParseSpan(v); err != nil {
				return fmt.Errorf("%s: %w", TimerMinEverySetting, err)
			}
			return nil
		}},
	{Key: BlobMaxSetting, Help: "the most bytes one file in mail may have (yardr mail --file)",
		Default: strconv.Itoa(DefaultBlobMax), Check: func(v string) error {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 1 {
				return fmt.Errorf("%s %q: a whole number of bytes, 1 or more", BlobMaxSetting, v)
			}
			return nil
		}},
	{Key: FilesMaxSetting, Help: "how many files one mail or note may carry; the yard answers its own count, and it is not set",
		Default: strconv.Itoa(MaxMailFiles), Check: func(v string) error {
			return fmt.Errorf("%s is answered by the yard and not set", FilesMaxSetting)
		}},
	{Key: PeerFileMaxSetting, Help: fmt.Sprintf("the most bytes one file sent to a linked yard, or taken from one, may have (yardr peer allow --send/--receive file), at most %d; default: %s", MaxPeerFile, BlobMaxSetting),
		Check: func(v string) error {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 1 || n > MaxPeerFile {
				return fmt.Errorf("%s %q: a whole number of bytes, 1 to %d", PeerFileMaxSetting, v, MaxPeerFile)
			}
			return nil
		}},
	{Key: BackupEverySetting, Help: "how often the daemon makes a dated copy of the store (yardr store backup), such as 1d or 6h; " +
		"the daemon reads it on every pass; default: never, as 0",
		Check: func(v string) error {
			if _, err := ParseSpan(v); err != nil && v != "0" {
				return fmt.Errorf("%s: %w, or 0 for never", BackupEverySetting, err)
			}
			return nil
		}},
	{Key: BackupKeepSetting, Help: "how many dated copies of the store the daemon keeps: it removes its own older ones, and no other file",
		Default: strconv.Itoa(DefaultBackupKeep), Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				return fmt.Errorf("%s %q: a whole number, 1 or more", BackupKeepSetting, v)
			}
			return nil
		}},
	{Key: BackupDirSetting, Help: "the directory the daemon keeps the dated copies of the store in, as an absolute path; default: backups in the yard's home",
		Check: func(v string) error {
			if !filepath.IsAbs(v) {
				return fmt.Errorf("%s %q: a directory is given as an absolute path, since the server writes the copy", BackupDirSetting, v)
			}
			return nil
		}},
	{Key: MaxSessionsSetting, Help: "the most sessions that run an agent or a script on this machine at once, over all groups, " +
		"beside each group's limit; the daemon reads it on every pass; unset, no cap; 0 pauses the yard: nothing new starts until it is raised",
		Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return fmt.Errorf("%s %q: a whole number, 0 or more", MaxSessionsSetting, v)
			}
			return nil
		}},
	{Key: HerdrSessionSetting, Help: "the herdr session the yard's agents run in (herdr session list), which every call the yard makes to herdr goes to, " +
		"whatever shell the daemon or a command was started from; it is changed while no crew member and no session of a bead runs, " +
		"or, from unset, to the session their panes are recorded in; " +
		"default: the session of the environment each is started in (HERDR_SOCKET_PATH, HERDR_SESSION, else herdr's default)",
		Check: func(v string) error {
			if !herdrSessionName.MatchString(v) || v == "." || v == ".." {
				return fmt.Errorf("%s %q: a herdr session's name has letters, digits, '.', '_' and '-' (herdr session list)", HerdrSessionSetting, v)
			}
			return nil
		}},
	{Key: NameSetting, Help: "the yard's name, which its crew's herdr agents and workspaces carry (<name>-yardmaster); default: the home's directory name",
		Check: func(v string) error {
			if !partyName.MatchString(v) {
				return fmt.Errorf("yard name %q: letters, digits, '.', '_', '-', '/'", v)
			}
			return nil
		}},
	{Key: UpgradeFromSetting, Help: "the depot whose landed build the yard installs itself after the assembly lands on it (yardr upgrade); unset, nothing is installed. " +
		"It names a depot of the yard: a depot the yard has not got is refused, and the named depot is not removed until the setting is cleared",
		Check: func(v string) error { return checkName("depot name", v) }},
	{Key: UpgradeRestoreSetting, Help: "whether a failed yardr upgrade restores the pre-install store copies (true) or leaves refused stores down for a person to restore (false); default: true",
		Default: "true", Env: "UPGRADE_RESTORE", Check: func(v string) error {
			if v != "true" && v != "false" {
				return fmt.Errorf("%s %q: true or false", UpgradeRestoreSetting, v)
			}
			return nil
		}},
	{Key: QuietLandingsSetting, Help: "how many code landings on a depot's base pass before the daemon owes the whole suite a run on a quiet machine (docs/testing.md R6): ten, about two hours of landings at the 60-140 a day of 14-16 Oct 2026, so a quiet spell is used within hours; 0 turns the quiet run off; the daemon reads it on every event it judges",
		Default: strconv.Itoa(DefaultQuietLandings), Check: func(v string) error {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return fmt.Errorf("%s %q: a whole number, 0 (off) or more", QuietLandingsSetting, v)
			}
			return nil
		}},
}

// SettingOf is the setting key names.
func SettingOf(key string) (Setting, bool) {
	for _, s := range KnownSettings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// settingAsk is whether setting key may be proposed as a run ask. Alone
// are who the operator is, what the yard installs itself from, who the
// web view acts as and what it trusts, where backups go, the addresses
// the servers bind, and the herdr session, whose check turns on which
// panes run now: the operator types those. hook_public_url is Askable:
// it binds nothing and serves nothing, only says where the yard is
// reached from outside, and its card shows the whole URL each hook is
// registered at. A new setting defaults to Alone until it is marked
// Askable here.
func settingAsk(key string) AskClass {
	switch key {
	case OperatorSetting, DefaultPackSetting,
		UpgradeFromSetting, UpgradeRestoreSetting,
		WebAsSetting, WebJWTIssuerSetting, WebJWTAudienceSetting,
		WebJWTJWKSSetting, WebJWTHeaderSetting, WebJWTUsersSetting,
		BackupDirSetting, HookAddrSetting, RouterAddrSetting, WebAddrSetting,
		WebPagesAddrSetting, WebPagesHostSetting, BDPAddrSetting,
		HerdrSessionSetting:
		return Alone
	case YardmasterSetting, StyleSetting, NoteSetting, DefaultKindSetting,
		HookPublicURLSetting,
		TimerLimitSetting, TimerMinEverySetting, BlobMaxSetting,
		PeerFileMaxSetting, BackupEverySetting, BackupKeepSetting,
		MaxSessionsSetting, NameSetting:
		return Askable
	default:
		return Alone
	}
}

// SettingKeys are the keys of KnownSettings, sorted.
func SettingKeys() []string {
	out := make([]string, len(KnownSettings))
	for i, s := range KnownSettings {
		out[i] = s.Key
	}
	slices.Sort(out)
	return out
}

// Where the value a setting is read as comes from (Setting.Effective).
const (
	FromSet         = "set"
	FromDefault     = "default"
	FromEnvironment = "environment"
)

// Effective is the value the setting is read as, given what is stored, and
// where it is from: the environment variable that stands in for it, in the
// environment of whoever asks (a server reads its own when it starts), else
// what is set, else the default. Both are "" for a setting that is unset
// and has no default.
func (s Setting) Effective(stored map[string]string) (value, from string) {
	if s.Env != "" {
		if v := config.Getenv(s.Env); v != "" {
			return v, FromEnvironment
		}
	}
	if v := stored[s.Key]; v != "" {
		return v, FromSet
	}
	if s.Default != "" {
		return s.Default, FromDefault
	}
	return "", ""
}

// checkJWTURL takes what a gateway's URLs are: absolute, over http or
// https. An issuer is compared with a token's iss claim, never fetched;
// the keys' URL is fetched, and http serves a test issuer on loopback or a
// hosted yard's router only (the web_jwt_jwks check holds it so).
func checkJWTURL(what, v string) error {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s %q: a URL, such as https://team.cloudflareaccess.com", what, v)
	}
	return nil
}

// isLoopbackHost reports whether host is this machine's loopback: an IP of
// it, or localhost.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isRouterHost reports whether host is the name a hosted yard reaches its
// router by: the router's network name on a user's Docker network, or a
// provisioner-written hosts entry, so its keys may come over plain http
// as a test issuer's do.
func isRouterHost(host string) bool {
	return strings.EqualFold(host, "router")
}

// ParseWebJWTUsers is the web_jwt_users setting as the web view reads it:
// email=user pairs separated by commas, emails in lower case, matched
// without regard to case. It refuses a pair with no "=", no email or no
// user, an email with no @, a user name no user may have, and an email
// named twice.
func ParseWebJWTUsers(value string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		email, user, ok := strings.Cut(pair, "=")
		email, user = strings.TrimSpace(email), strings.TrimSpace(user)
		switch {
		case !ok || email == "" || user == "":
			return nil, fmt.Errorf("%q: email=user pairs separated by commas", pair)
		case !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\n\r"):
			return nil, fmt.Errorf("email %q: an address with an @", email)
		}
		if err := checkUserName(user); err != nil {
			return nil, err
		}
		if low := strings.ToLower(email); out[low] != "" {
			return nil, fmt.Errorf("email %q twice", email)
		} else {
			out[low] = user
		}
	}
	return out, nil
}

// checkDefaultPack takes what names a pack wherever a later command runs:
// a name, a directory written absolute, or a pack's URL. A relative
// directory would mean another pack in every directory depot init is run
// from.
func checkDefaultPack(v string) error {
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return nil
	}
	if !strings.ContainsRune(v, filepath.Separator) && !strings.Contains(v, "/") && v != "." && v != ".." {
		if !partyName.MatchString(v) {
			return fmt.Errorf("%s %q: a pack's name, or its directory as an absolute path", DefaultPackSetting, v)
		}
		return nil
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("%s %q: a directory is given as an absolute path, since the setting is read wherever yardr depot init is run", DefaultPackSetting, v)
	}
	if _, err := os.Stat(filepath.Join(v, packFile)); err != nil {
		return fmt.Errorf("%s %q: no pack there: %w", DefaultPackSetting, v, err)
	}
	return nil
}

// CheckHookAddr refuses what the webhook receiver could not listen on: an
// address that is no host:port.
func CheckHookAddr(addr string) error { return checkHostPort("hook address", addr, "127.0.0.1:8790") }

// CheckHookPublicURL refuses what no service could be told to send a
// webhook to: not an absolute https address (plain http to this
// machine alone, for a stand-in), with a user, a query or a fragment,
// or a path that leaves its base. The operator gives it as a setting:
// no proxy or tunnel address is worked out from anything else.
func CheckHookPublicURL(v string) error {
	u, err := url.Parse(v)
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		return fmt.Errorf("%s %q: a public address, such as https://yard.example/hooks", HookPublicURLSetting, v)
	case u.User != nil:
		return fmt.Errorf("%s %q: an address with no user or password in it", HookPublicURLSetting, v)
	case u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(v, "?#"):
		return fmt.Errorf("%s %q: an address with no query or fragment: the hook's name is added to its path", HookPublicURLSetting, v)
	case u.Scheme == "http" && !isLoopbackHost(u.Hostname()):
		return fmt.Errorf("%s %q: http is for a stand-in on this machine only: a public address is https", HookPublicURLSetting, v)
	case u.Scheme != "https" && u.Scheme != "http":
		return fmt.Errorf("%s %q: %s:// is not https://", HookPublicURLSetting, v, u.Scheme)
	case hasDotsPath(u.Path):
		return fmt.Errorf("%s %q: its path has no '..' segment", HookPublicURLSetting, v)
	}
	return nil
}

func hasDotsPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// checkHostPort refuses an address that is no host:port, naming it as what
// it is the address of, with one that would be taken.
func checkHostPort(what, addr, example string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: want host:port, such as %s (%v)", what, addr, example, err)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s %q: the port is a number from 1 to 65535, as in %s", what, addr, example)
	}
	return nil
}

// CheckWebAddr refuses a listen address of the web view that is not on the
// loopback interface, saying why as whose refused it (the flag that gave
// it: --addr, --pages-addr). With unsafe it refuses only what is no
// address. It is here for the web_addr setting, which is held to what the
// view's --addr is (web.CheckAddr).
func CheckWebAddr(whose, addr string, unsafe bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %s: %w", whose, addr, err)
	}
	if unsafe {
		return nil
	}
	switch ip := net.ParseIP(host); {
	case host == "":
		return WebAddrRefused(whose, addr, "an address with no host listens on every interface")
	case ip == nil && !strings.EqualFold(host, "localhost"):
		return WebAddrRefused(whose, addr, host+" is not a loopback address")
	case ip != nil && !ip.IsLoopback():
		return WebAddrRefused(whose, addr, host+" is not a loopback address")
	}
	return nil
}

// WebAddrRefused is the refusal of addr as the web view's, for why, as
// whose refused it (the flag that gave it: --addr, --pages-addr).
func WebAddrRefused(whose, addr, why string) error {
	return fmt.Errorf("%s %s refused: %s, and yardr web has no authentication yet, so whoever reaches it reads the whole yard. "+
		"It listens on loopback only (127.0.0.1, ::1 or localhost) unless --unsafe is given", whose, addr, why)
}

// CheckWebHost returns name as a request's Host header is compared with
// it, in lower case and without the dot that ends a full name, or says
// why it is no host name, as whose refused it (the flag that gave it:
// --host, --pages-host). A wildcard is none: a view that answers to
// every name has no guard against DNS rebinding left. It is here for
// the web_pages_host setting, which is held to what --pages-host takes
// (web.CheckHost).
func CheckWebHost(whose, name string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(name), ".")
	switch {
	case host == "":
		return "", fmt.Errorf("%s %q refused: it is no name", whose, name)
	case strings.Contains(host, "*"):
		return "", fmt.Errorf("%s %s refused: a wildcard would answer to any site's name, and yardr web has no authentication yet. Name each host", whose, name)
	case strings.Contains(host, ":"):
		return "", fmt.Errorf("%s %s refused: give the name alone, without a scheme or a port (an IP address is answered already)", whose, name)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return "", fmt.Errorf("%s %s refused: it is no host name (letters, digits, - and _, in parts joined by dots)", whose, name)
		}
	}
	return host, nil
}

// Settings returns every setting, and files_max beside them: how many
// files one mail or note may carry. That count is the yard's own, not a
// setting the operator changes, so a client needs no copy of the number.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bSettings, "", func(k []byte, v *string) error {
			out[string(k)] = *v
			return nil
		})
	})
	// The yard's own count wins over anything stored under its key.
	out[FilesMaxSetting] = strconv.Itoa(MaxMailFiles)
	return out, err
}

// SetSetting sets key to value; an empty value removes it. A key that is
// not in KnownSettings is refused with the keys there are, and so is a
// value the key's check does not take: whoever writes (the CLI, init, the
// pairing form) is told a misspelt key instead of having it stored and
// never read. One the yard has stored already can still be removed.
//
// Settings are the operator's alone to change (operatorOnly): they say who
// the operator is, where the server listens and what every later session
// is told, so no agent sets one, and reading them stays anyone's. A yard
// that names no operator yet takes one from whoever is no agent of its own
// (notOperator), which is how its first operator is named.
//
// The operator is
// a user (see User), so naming one adds the user when there is none. A
// crew member's name is refused: mail to a name reaches one party. A yard that
// has a crew member named as its operator already keeps its setting as it is.
func (s *Store) SetSetting(ctx context.Context, key, value, actor string) error {
	if err := checkName("setting", key); err != nil {
		return err
	}
	set, known := SettingOf(key)
	if known && value != "" {
		if err := set.Check(value); err != nil {
			return err
		}
	}
	if key == OperatorSetting && value != "" {
		if err := checkUserName(value); err != nil {
			return fmt.Errorf("operator: %w", err)
		}
	}
	return s.write(func(w *wtx) error {
		if key == HerdrSessionSetting {
			// Alone (settingAsk): the check turns on which panes run
			// now, which no card can be held to. It stays here, with
			// the store, and not in setSetting, which a run ask runs.
			if err := s.herdrSessionFree(w.Tx, value); err != nil {
				return err
			}
		}
		return w.setSetting(ctx, key, value, known, actor, nil)
	})
}

// setSetting is SetSetting inside the transaction, so that an approved run
// ask changes the setting in the write that records its approval: by is
// then the ask it was approved on, which the event carries. known is
// whether key is one of KnownSettings.
func (w *wtx) setSetting(ctx context.Context, key, value string, known bool, actor string, by *approvedOn) error {
	if err := w.operatorOnly(ctx, actor, "change a setting", settingAsk(key)); err != nil {
		return err
	}
	if !known && (value != "" || w.Bucket(bSettings).Get([]byte(key)) == nil) {
		return fmt.Errorf("no setting %q; there is: %s", key, strings.Join(SettingKeys(), ", "))
	}
	if key == OperatorSetting && value != "" && w.Bucket(bCrew).Get([]byte(value)) != nil {
		if was, err := operatorIn(w.Tx); err != nil {
			return err
		} else if was != value {
			return fmt.Errorf("%s %q: that is a crew member's name, and mail to a name reaches one party: "+
				"choose another name for the operator, or remove the crew member first (yardr crew rm %s)", key, value, value)
		}
	}
	if key == UpgradeFromSetting && value != "" {
		if err := upgradeFromTellable(w.Tx); err != nil {
			return err
		}
		// The install builds the named depot's checkout, so a name the
		// yard has not got would let whoever creates a depot under it
		// choose the install source. DeleteDepot refuses the other half:
		// removing the depot the setting names.
		if w.Bucket(bDepots).Get([]byte(value)) == nil {
			return fmt.Errorf("%s: depot %s is not in the yard: add it first (yardr depot add %s <path>)",
				UpgradeFromSetting, value, value)
		}
	}
	var err error
	if value == "" {
		err = w.Bucket(bSettings).Delete([]byte(key))
	} else {
		err = put(w.Tx, bSettings, key, value)
	}
	if err != nil {
		return err
	}
	if key == OperatorSetting {
		if err := w.ensureOperator(actor); err != nil {
			return err
		}
	}
	if key == UpgradeFromSetting && value != "" {
		if err := w.subscribeUpgradeFailures(); err != nil {
			return err
		}
	}
	data := map[string]any{"key": key, "value": value}
	if by != nil {
		data["approved"] = by
	}
	return w.event("", "setting_changed", actor, data)
}

// upgradeFromSet reports whether the automatic install is on: the
// upgrade_from setting names a depot.
func upgradeFromSet(tx *bolt.Tx) bool {
	return tx.Bucket(bSettings).Get([]byte(UpgradeFromSetting)) != nil
}

// upgradeFromTellable refuses the automatic install where a failure could
// tell nobody outside the yard: with no operator, with an operator who is
// no user, or with one naming no notify source. The refusal names how to
// set one.
func upgradeFromTellable(tx *bolt.Tx) error {
	operator, err := operatorIn(tx)
	if err != nil {
		return err
	}
	if operator == "" {
		return fmt.Errorf("%s: the yard names no operator, and a failed install is told to the operator outside the yard: "+
			"name one first (yardr settings set operator <name>)", UpgradeFromSetting)
	}
	var u User
	if ok, err := get(tx, bUsers, operator, &u); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s: the operator %s is a crew member's name, and mail to a name reaches one party: "+
			"choose another name for the operator, or remove the crew member first (yardr crew rm %s)", UpgradeFromSetting, operator, operator)
	}
	if u.Notify == "" {
		return fmt.Errorf("%s: the operator %s names no notify source, and a failed install is told outside the yard: "+
			"name one first (yardr pack apply pushover --source-env pushover=<file> --notify %s=pushover,<file>, or the telegram pack the same way)",
			UpgradeFromSetting, operator, operator)
	}
	return nil
}

// HandleHerdrSession is where a handle names the herdr session its pane is
// in: the runner writes it when it has made the pane and found its shell
// there, from the socket that call went to, so it says where the pane is
// and not what a later caller's shell says. A pane of a build before it
// has none.
const HandleHerdrSession = "herdr_session"

// herdrSessionFree refuses a change of herdr_session to value while
// anything of the yard runs in a pane: a crew member that runs or is being
// started, or a live session of a bead that herdr runs. Their panes are in
// the session the yard used until now, and pane ids are a session's own, so
// in another one the yard would take them for gone, or another pane of the
// same id for theirs. So they are stopped first; the refusal lists them.
//
// One change moves no pane and is taken while they run: from unset to the
// session a handle says its pane is in already (HandleHerdrSession), which
// is how a yard that runs names its session for the first time. A handle
// that names another session, or none (a pane of an older build, a session
// that has no pane yet), is in the list as before.
func (s *Store) herdrSessionFree(tx *bolt.Tx, value string) error {
	var was string
	if _, err := get(tx, bSettings, HerdrSessionSetting, &was); err != nil {
		return err
	}
	if was == value {
		return nil
	}
	in := func(handle map[string]any) bool {
		name, _ := handle[HandleHerdrSession].(string)
		return was == "" && name != "" && name == value
	}
	var running []string
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		// The pane of a start is one of its own beside a running one's.
		if r.Live() && !in(r.Handle) || r.Starting != nil && !in(r.Starting) {
			running = append(running, "crew member "+r.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	live, err := s.liveSessions(tx, "")
	if err != nil {
		return err
	}
	for _, sess := range live {
		if sess.RunnerName() == "herdr" && !in(sess.Handle) {
			running = append(running, fmt.Sprintf("session %s (%s)", sess.ID, sess.Group))
		}
	}
	if len(running) == 0 {
		return nil
	}
	slices.Sort(running)
	return fmt.Errorf("%s is changed while nothing of the yard runs in a pane, since the panes of what runs are in the herdr session used until now "+
		"and are not moved: %s. Stop the daemon so that it starts nothing more (yardr daemon stop), stop the crew (yardr crew stop <name>) "+
		"and let the sessions end (yardr session list); then set it and start them again",
		HerdrSessionSetting, strings.Join(running, ", "))
}
