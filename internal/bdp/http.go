package bdp

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// acceptsJSON applies Accept negotiation for parameter-free
// application/json: the most specific matching range decides, q=0 refuses,
// and a range carrying media parameters cannot match. No header means yes.
func acceptsJSON(values []string) bool {
	if len(values) == 0 {
		return true
	}
	ranges, ok := splitQuoted(strings.Join(values, ","), ',')
	if !ok {
		return false
	}
	specificity, quality := -1, 0.0
	for _, rng := range ranges {
		spec, q, ok := jsonRange(rng)
		if !ok {
			continue
		}
		switch {
		case spec > specificity:
			specificity, quality = spec, q
		case spec == specificity:
			quality = max(quality, q)
		}
	}
	return quality > 0
}

var (
	token   = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	quoted  = regexp.MustCompile(`^"(?:[\t\x20\x21\x23-\x5b\x5d-\x7e\x80-\xff]|\\[\t\x20-\x7e\x80-\xff])*"$`)
	quality = regexp.MustCompile(`^(?:0(?:\.\d{0,3})?|1(?:\.0{0,3})?)$`)
)

func jsonRange(v string) (specificity int, q float64, ok bool) {
	parts, ok := splitQuoted(v, ';')
	if !ok {
		return 0, 0, false
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "application/json":
		specificity = 2
	case "application/*":
		specificity = 1
	case "*/*":
		specificity = 0
	default:
		return 0, 0, false
	}
	q = 1
	weighted := false
	for _, part := range parts[1:] {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		name, raw, found := strings.Cut(p, "=")
		if !found || !token.MatchString(name) || (!token.MatchString(raw) && !quoted.MatchString(raw)) {
			return 0, 0, false
		}
		if !strings.EqualFold(name, "q") {
			return 0, 0, false // media parameters never match parameter-free JSON
		}
		if weighted || !quality.MatchString(raw) {
			return 0, 0, false
		}
		q, _ = strconv.ParseFloat(raw, 64)
		weighted = true
	}
	return specificity, q, true
}

// splitQuoted splits on delim outside quoted strings.
func splitQuoted(v string, delim byte) ([]string, bool) {
	var parts []string
	inQuote, start := false, 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case inQuote && c == '\\':
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == delim:
			parts = append(parts, v[start:i])
			start = i + 1
		}
	}
	if inQuote {
		return nil, false
	}
	return append(parts, v[start:]), true
}

// conditional evaluates If-Match (strong) then If-None-Match (weak) for an
// existing representation with the given entity tag ("" for none).
func conditional(h http.Header, etag string, status int) int {
	if v := h.Values("If-Match"); len(v) > 0 && !tagListMatches(strings.Join(v, ","), etag, true) {
		return http.StatusPreconditionFailed
	}
	if v := h.Values("If-None-Match"); len(v) > 0 && tagListMatches(strings.Join(v, ","), etag, false) {
		return http.StatusNotModified
	}
	return status
}

var entityTag = regexp.MustCompile(`^(?:W/)?"[\x21\x23-\x7e\x80-\xff]*"`)

// tagListMatches reports whether an If-Match/If-None-Match list matches
// current. "*" matches any existing representation; a malformed list never
// matches.
func tagListMatches(list, current string, strong bool) bool {
	if strings.TrimSpace(list) == "*" {
		return true
	}
	matched := false
	for i := 0; i < len(list); {
		for i < len(list) && strings.IndexByte("\t ,", list[i]) >= 0 {
			i++
		}
		if i == len(list) {
			break
		}
		m := entityTag.FindString(list[i:])
		if m == "" {
			return false
		}
		if current != "" {
			if strong {
				matched = matched || (!strings.HasPrefix(m, "W/") && !strings.HasPrefix(current, "W/") && m == current)
			} else {
				matched = matched || strings.TrimPrefix(m, "W/") == strings.TrimPrefix(current, "W/")
			}
		}
		i += len(m)
		for i < len(list) && (list[i] == ' ' || list[i] == '\t') {
			i++
		}
		if i < len(list) && list[i] != ',' {
			return false
		}
	}
	return matched
}
