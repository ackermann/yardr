package bdp

import (
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// errBadSegment means a path segment is not a canonical BDP ID segment.
var errBadSegment = errors.New("non-canonical id segment")

// pathChars are emitted literally in a canonical segment: RFC 3986
// unreserved plus the sub-delims, ':' and '@' a path segment may carry.
// Everything else is percent-encoded with uppercase hex.
func literal(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-._~!$&'()*+,;=:@", c) >= 0
}

// EncodeSegment returns the canonical spelling of one decoded ID segment.
func EncodeSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if literal(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte("0123456789ABCDEF"[c>>4])
			b.WriteByte("0123456789ABCDEF"[c&15])
		}
	}
	return b.String()
}

// DecodeSegment decodes a raw segment exactly once and checks that it is
// the canonical spelling of a valid segment (nonempty, not . or .., valid
// UTF-8, no controls, no slash or backslash).
func DecodeSegment(raw string) (string, error) {
	if raw == "" {
		return "", errBadSegment
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(raw) {
			return "", errBadSegment
		}
		hi, lo := unhex(raw[i+1]), unhex(raw[i+2])
		if hi < 0 || lo < 0 {
			return "", errBadSegment
		}
		b.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	s := b.String()
	if s == "." || s == ".." || !utf8.ValidString(s) {
		return "", errBadSegment
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '/' || r == '\\' {
			return "", errBadSegment
		}
	}
	if EncodeSegment(s) != raw {
		return "", errBadSegment
	}
	return s, nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// CompareIDs orders ids by UTF-16 code unit, the canonical-uri order. It
// differs from byte order only for characters outside the BMP.
func CompareIDs(a, b string) int {
	if isASCII(a) && isASCII(b) {
		return strings.Compare(a, b)
	}
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	return len(ua) - len(ub)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
