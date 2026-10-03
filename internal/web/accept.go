package web

import (
	"strconv"
	"strings"
)

// wantsJSON reports whether a request's Accept header prefers JSON to HTML.
// HTML is the default: a browser, curl without a header and `*/*` all get
// the page, and only a client that names application/json (or
// application/*) above text/html gets the data.
func wantsJSON(accept []string) bool {
	var json, html float64
	for _, rng := range strings.Split(strings.Join(accept, ","), ",") {
		media, params, _ := strings.Cut(rng, ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if name, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(name), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil {
					f = 0 // a weight that cannot be read asks for nothing
				}
				q = f
			}
		}
		switch strings.ToLower(strings.TrimSpace(media)) {
		case "application/json", "application/*":
			json = max(json, q)
		case "text/html", "text/*":
			html = max(html, q)
		}
	}
	return json > 0 && json > html
}
