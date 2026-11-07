package store

import (
	"fmt"
	"net/url"
	"strings"
)

// WebYardsSetting lists the web views of other yards that this yard's view
// links to from every page's header, so that the operator goes from one to
// another in one click. A peer's URL is where its yard is called, not where
// its view is opened, so the views are named here and not taken from peers.
const WebYardsSetting = "web_yards"

// webYards is the setting's entry, added to KnownSettings at the end of the
// table in settings.go, as webJWTPrivilegedSessionMaxAge is and for the same
// reason: settings.go is at the ceiling the gate holds it to.
var webYards = Setting{
	Key: WebYardsSetting,
	Help: "the web views of other yards that every page's header links to, beside this yard's name: name=url pairs separated by commas, in the order shown, " +
		"each name a yard's and each URL the view's own, http or https; a link is a plain one, so that view's own login decides who you are there. " +
		"Unset, the header links to no other yard",
	Check: func(v string) error {
		if _, err := ParseWebYards(v); err != nil {
			return fmt.Errorf("%s: %w", WebYardsSetting, err)
		}
		return nil
	},
}

// WebYard is one other yard's web view, as the header links to it.
type WebYard struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ParseWebYards is the web_yards setting as the view reads it: name=url
// pairs separated by commas, in the setting's order, an empty value none.
// It refuses a pair with no "=", a name a yard may not have or named twice,
// and a URL that is not an absolute http or https one with a host, or that
// carries a user or password: the link is in every page, and a secret in it
// would be shown to whoever reads one.
func ParseWebYards(value string) ([]WebYard, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var out []WebYard
	for _, pair := range strings.Split(value, ",") {
		name, raw, ok := strings.Cut(pair, "=")
		name, raw = strings.TrimSpace(name), strings.TrimSpace(raw)
		if !ok || name == "" || raw == "" {
			return nil, fmt.Errorf("%q: name=url pairs separated by commas", pair)
		}
		if !partyName.MatchString(name) {
			return nil, fmt.Errorf("yard name %q: letters, digits, '.', '_', '-', '/'", name)
		}
		u, err := url.Parse(raw)
		switch {
		case err != nil:
			return nil, fmt.Errorf("yard %s: %w", name, err)
		case u.Scheme != "http" && u.Scheme != "https":
			return nil, fmt.Errorf("yard %s: %q is no http or https URL", name, raw)
		case u.Host == "":
			return nil, fmt.Errorf("yard %s: %q names no host", name, raw)
		case u.User != nil:
			// Redacted, so the refusal does not print the password either.
			return nil, fmt.Errorf("yard %s: %q carries a user or password, which every page would show", name, u.Redacted())
		}
		for _, y := range out {
			if y.Name == name {
				return nil, fmt.Errorf("yard %s twice", name)
			}
		}
		out = append(out, WebYard{Name: name, URL: u.String()})
	}
	return out, nil
}
