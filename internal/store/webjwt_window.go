package store

import "fmt"

// WebJWTPrivilegedSessionMaxAgeSetting is how long after a person last
// authenticated at the gateway the web view still calls their session fresh
// (internal/web). The number is the gateway's, not the yard's: the name is
// the gateway's own key, Kratos's privileged_session_max_age on a hosted
// yard, so that whoever sets one sees the other, and there is no default
// for the yard to hold a second copy of unseen. Unset, no session is fresh.
const WebJWTPrivilegedSessionMaxAgeSetting = "web_jwt_privileged_session_max_age"

// webJWTPrivilegedSessionMaxAge is the setting's entry, added to
// KnownSettings at the end of the table in settings.go. It stands here,
// beside the setting's name and its check, because settings.go is at the
// ceiling the gate holds it to (.yardr/test, filesizes) and a landing is
// judged by the table on main, so a line more or fewer there cannot land
// (yardr-g729).
var webJWTPrivilegedSessionMaxAge = Setting{
	Key: WebJWTPrivilegedSessionMaxAgeSetting,
	Help: "how recently a person must have signed in at the gateway for the web view to take their session as freshly authenticated, a duration such as 15m: " +
		"the gateway's own limit for a privileged session (Kratos's privileged_session_max_age on a hosted yard), set to the same number there, and not a policy of the yard's. " +
		"It is measured on the token's auth_time claim, and a token without one is never fresh. Unset, no session is fresh. Read when the view starts, with the rest of JWT mode",
	Check: func(v string) error {
		if _, err := ParseSpan(v); err != nil {
			return fmt.Errorf("%s: %w", WebJWTPrivilegedSessionMaxAgeSetting, err)
		}
		return nil
	},
}
