package bdp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Selector is BDP's bounded RFC 9535 filter: $[?expr] over a candidate
// {id, type, [source, target,] properties}. Supported: singular relative
// paths (@.a.b, @['a'], @[0]), JSON literals, existence tests, == != < <=
// > >=, && || ! and parentheses. Not supported, by design: functions,
// regex, recursive descent, nested filters, path-to-path comparison.

// Bounds advertised in discovery; exceeding one is limit-exceeded.
type SelectorLimits struct {
	Bytes, Depth, Nodes int
}

var (
	errSelectorSyntax = errors.New("invalid selector")   // 400 invalid-parameter
	errSelectorLimit  = errors.New("selector too large") // 413 limit-exceeded
)

type selNode interface {
	eval(doc any) bool
}

type operand struct {
	path    []any // string keys and int indexes; nil for a literal
	literal any
	isPath  bool
}

type cmpNode struct {
	left, right operand
	op          string
}

type existsNode struct{ path []any }
type notNode struct{ x selNode }
type andNode struct{ a, b selNode }
type orNode struct{ a, b selNode }

// ParseSelector compiles s within the given limits.
func ParseSelector(s string, lim SelectorLimits) (selNode, error) {
	if len(s) > lim.Bytes {
		return nil, errSelectorLimit
	}
	p := &selParser{s: s, lim: lim}
	p.ws()
	if !p.eat("$") {
		return nil, errSelectorSyntax
	}
	p.ws()
	if !p.eat("[") {
		return nil, errSelectorSyntax
	}
	p.ws()
	if !p.eat("?") {
		return nil, errSelectorSyntax
	}
	n, err := p.or(1)
	if err != nil {
		return nil, err
	}
	p.ws()
	if !p.eat("]") {
		return nil, errSelectorSyntax
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, errSelectorSyntax
	}
	return n, nil
}

type selParser struct {
	s     string
	i     int
	nodes int
	lim   SelectorLimits
}

func (p *selParser) ws() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *selParser) eat(tok string) bool {
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func (p *selParser) count(depth int) error {
	p.nodes++
	if p.nodes > p.lim.Nodes || depth > p.lim.Depth {
		return errSelectorLimit
	}
	return nil
}

func (p *selParser) or(depth int) (selNode, error) {
	a, err := p.and(depth)
	if err != nil {
		return nil, err
	}
	for {
		p.ws()
		if !p.eat("||") {
			return a, nil
		}
		if err := p.count(depth); err != nil {
			return nil, err
		}
		b, err := p.and(depth)
		if err != nil {
			return nil, err
		}
		a = orNode{a, b}
	}
}

func (p *selParser) and(depth int) (selNode, error) {
	a, err := p.unary(depth)
	if err != nil {
		return nil, err
	}
	for {
		p.ws()
		if !p.eat("&&") {
			return a, nil
		}
		if err := p.count(depth); err != nil {
			return nil, err
		}
		b, err := p.unary(depth)
		if err != nil {
			return nil, err
		}
		a = andNode{a, b}
	}
}

func (p *selParser) unary(depth int) (selNode, error) {
	p.ws()
	if err := p.count(depth); err != nil {
		return nil, err
	}
	if p.eat("!") {
		p.ws()
		x, err := p.unary(depth + 1)
		if err != nil {
			return nil, err
		}
		return notNode{x}, nil
	}
	if p.eat("(") {
		x, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		p.ws()
		if !p.eat(")") {
			return nil, errSelectorSyntax
		}
		return x, nil
	}
	left, err := p.operand(depth)
	if err != nil {
		return nil, err
	}
	p.ws()
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.eat(op) {
			right, err := p.operand(depth)
			if err != nil {
				return nil, err
			}
			if left.isPath && right.isPath {
				return nil, errSelectorSyntax // path-to-path comparison is not supported
			}
			return cmpNode{left, right, op}, nil
		}
	}
	if !left.isPath {
		return nil, errSelectorSyntax // a bare literal is not a test
	}
	return existsNode{left.path}, nil
}

func (p *selParser) operand(depth int) (operand, error) {
	p.ws()
	if err := p.count(depth); err != nil {
		return operand{}, err
	}
	if p.eat("@") {
		var path []any
		for {
			switch {
			case p.eat("."):
				start := p.i
				for p.i < len(p.s) {
					c := p.s[p.i]
					if c == '_' || c >= 0x80 || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') ||
						(p.i > start && '0' <= c && c <= '9') {
						p.i++
						continue
					}
					break
				}
				if p.i == start {
					return operand{}, errSelectorSyntax // includes recursive descent ".."
				}
				path = append(path, p.s[start:p.i])
			case p.eat("["):
				p.ws()
				if p.i < len(p.s) && (p.s[p.i] == '\'' || p.s[p.i] == '"') {
					k, err := p.str()
					if err != nil {
						return operand{}, err
					}
					path = append(path, k)
				} else {
					n, err := p.integer()
					if err != nil {
						return operand{}, err
					}
					path = append(path, n)
				}
				p.ws()
				if !p.eat("]") {
					return operand{}, errSelectorSyntax
				}
			default:
				if len(path)+1 > p.lim.Depth {
					return operand{}, errSelectorLimit
				}
				return operand{path: path, isPath: true}, nil
			}
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == '\'' || p.s[p.i] == '"') {
		v, err := p.str()
		return operand{literal: v}, err
	}
	for _, kw := range []struct {
		t string
		v any
	}{{"true", true}, {"false", false}, {"null", nil}} {
		if p.eat(kw.t) {
			return operand{literal: kw.v}, nil
		}
	}
	start := p.i
	for p.i < len(p.s) && strings.IndexByte("+-0123456789.eE", p.s[p.i]) >= 0 {
		p.i++
	}
	if start == p.i {
		return operand{}, errSelectorSyntax
	}
	var f float64
	if err := json.Unmarshal([]byte(p.s[start:p.i]), &f); err != nil {
		return operand{}, errSelectorSyntax
	}
	return operand{literal: f}, nil
}

func (p *selParser) integer() (int, error) {
	start := p.i
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.s) && '0' <= p.s[p.i] && p.s[p.i] <= '9' {
		p.i++
	}
	n, err := strconv.Atoi(p.s[start:p.i])
	if err != nil {
		return 0, errSelectorSyntax
	}
	return n, nil
}

// str parses an RFC 9535 string literal in single or double quotes.
func (p *selParser) str() (string, error) {
	q := p.s[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == q:
			p.i++
			return b.String(), nil
		case c == '\\':
			if p.i+1 >= len(p.s) {
				return "", errSelectorSyntax
			}
			e := p.s[p.i+1]
			p.i += 2
			switch e {
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '/', '\\', '\'', '"':
				if (e == '\'' && q != '\'') || (e == '"' && q != '"') {
					return "", errSelectorSyntax
				}
				b.WriteByte(e)
			case 'u':
				r, ok := p.hex4()
				if !ok {
					return "", errSelectorSyntax
				}
				if utf16.IsSurrogate(r) {
					// A high surrogate must pair with a following low one.
					if r >= 0xDC00 || !p.eat("\\u") {
						return "", errSelectorSyntax
					}
					lo, ok := p.hex4()
					if !ok || lo < 0xDC00 || lo > 0xDFFF {
						return "", errSelectorSyntax
					}
					r = utf16.DecodeRune(r, lo)
				}
				b.WriteRune(r)
			default:
				return "", errSelectorSyntax
			}
		case c < 0x20:
			return "", errSelectorSyntax
		default:
			_, size := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteString(p.s[p.i : p.i+size])
			p.i += size
		}
	}
	return "", errSelectorSyntax
}

func (p *selParser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	var r rune
	for _, c := range []byte(p.s[p.i : p.i+4]) {
		d := unhex(c)
		if d < 0 {
			return 0, false
		}
		r = r<<4 | rune(d)
	}
	p.i += 4
	return r, true
}

// nothing marks an absent path result (RFC 9535 "Nothing").
type nothingT struct{}

var nothing = nothingT{}

func resolve(doc any, path []any) any {
	cur := doc
	for _, seg := range path {
		switch k := seg.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return nothing
			}
			v, ok := m[k]
			if !ok {
				return nothing
			}
			cur = v
		case int:
			a, ok := cur.([]any)
			if !ok {
				return nothing
			}
			if k < 0 {
				k += len(a)
			}
			if k < 0 || k >= len(a) {
				return nothing
			}
			cur = a[k]
		}
	}
	return cur
}

func (o operand) value(doc any) any {
	if o.isPath {
		return resolve(doc, o.path)
	}
	return o.literal
}

func (n existsNode) eval(doc any) bool { return resolve(doc, n.path) != nothing }
func (n notNode) eval(doc any) bool    { return !n.x.eval(doc) }
func (n andNode) eval(doc any) bool    { return n.a.eval(doc) && n.b.eval(doc) }
func (n orNode) eval(doc any) bool     { return n.a.eval(doc) || n.b.eval(doc) }

func (n cmpNode) eval(doc any) bool {
	l, r := n.left.value(doc), n.right.value(doc)
	switch n.op {
	case "==":
		return equal(l, r)
	case "!=":
		return !equal(l, r)
	case "<":
		return less(l, r)
	case ">":
		return less(r, l)
	case "<=":
		return less(l, r) || equal(l, r)
	case ">=":
		return less(r, l) || equal(l, r)
	}
	return false
}

func equal(a, b any) bool {
	if fa, ok := num(a); ok {
		fb, ok := num(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case nothingT:
		return b == nothing
	case nil:
		return b == nil
	case string, bool:
		return a == b
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !equal(v, w) {
				return false
			}
		}
		return true
	}
	return false
}

func less(a, b any) bool {
	if fa, ok := num(a); ok {
		fb, ok := num(b)
		return ok && fa < fb
	}
	sa, ok1 := a.(string)
	sb, ok2 := b.(string)
	// Strings compare by Unicode scalar value, which byte order of UTF-8 gives.
	return ok1 && ok2 && sa < sb
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// Match evaluates the selector against a candidate document.
func Match(n selNode, doc any) bool { return n.eval(doc) }

func (o operand) String() string {
	if o.isPath {
		return fmt.Sprint(o.path)
	}
	return fmt.Sprint(o.literal)
}
