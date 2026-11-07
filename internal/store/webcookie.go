package store

import (
	"fmt"
	"strconv"
)

// WebCookieDaysSetting is how many days the cookie a web view's token URL
// buys lasts (internal/web, viewtoken.go). The view refuses a cookie older
// than that whatever the browser keeps, and a request that carries one does
// not make it last longer: a cookie that was taken dies on schedule.
const WebCookieDaysSetting = "web_cookie_days"

// DefaultWebCookieDays is web_cookie_days in a yard that sets none.
const DefaultWebCookieDays = 365

// MaxWebCookieDays is the most web_cookie_days takes: a century, well inside
// what a time.Duration holds (about 292 years), so the days never wrap round
// to a cookie that lasts a moment or forever.
const MaxWebCookieDays = 36500

// webCookieDays is the setting's entry, added to KnownSettings at the end of
// the table in settings.go, as webJWTPrivilegedSessionMaxAge is and for its
// reason: settings.go is at the ceiling the gate holds it to.
var webCookieDays = Setting{
	Key: WebCookieDaysSetting,
	Help: "how many days the cookie a web view's token URL buys lasts, from when the browser opened that URL: the view refuses it after that, and a request that carries it does not extend it; " +
		"the token itself lasts until yardr web revoke. Read when the view starts; default: 365",
	Default: strconv.Itoa(DefaultWebCookieDays),
	Check: func(v string) error {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > MaxWebCookieDays {
			return fmt.Errorf("%s %q: a whole number of days, 1 to %d", WebCookieDaysSetting, v, MaxWebCookieDays)
		}
		return nil
	},
}

// ActRevokeWebTokens is yardr web revoke: replacing the secret a web view
// that acts as a user asks a browser for, which logs out every browser that
// holds a cookie of it. It is a CLI-only gate, asked through May before the
// command touches the file, and the operator's. It stands here and not in
// policy.go's list for the same ceiling.
const ActRevokeWebTokens Action = "revoke the web view's token"

func init() { coreRules[ActRevokeWebTokens] = ruleOperator }
