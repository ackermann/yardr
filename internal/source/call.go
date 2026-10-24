package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Timeout bounds one call, from the connection to the last byte of the
// answer. A variable so a test need not wait it out.
var Timeout = 30 * time.Second

const (
	// AnswerMax is the most a call returns unless its declaration raises
	// it (answer.max): what a call returns is read by an agent or sent on,
	// and a page of JSON is enough for either.
	AnswerMax = 64 << 10
	// AnswerLimit is the most answer.max may ask for, and the most yardr
	// reads of any answer before select is applied to it.
	AnswerLimit = 1 << 20
)

// Why a call was not made, or made and not returned: the Reason of a
// CallError.
const (
	NoCall       = "no_call"       // the declaration has no call of the name
	BadParam     = "bad_param"     // a parameter is unknown, missing or not what the declaration allows
	NoCredential = "no_credential" // the declaration names a credential and none was given
	Refused      = "refused"       // the declaration's base, or a credential that is the address, is not one a call is made to
	Unreachable  = "unreachable"   // no connection, or it broke
	TimedOut     = "timeout"
	Redirect     = "redirect"  // the service answered 3xx, which is not followed
	TooLarge     = "too_large" // the answer is over its bound
	NotJSON      = "not_json"
	BuildRefused = "build_refused" // the build command exited non-zero, refusing the call
	BuildFailed  = "build_failed"  // the build command could not produce one bounded JSON value
)

// CallError is a call that returned nothing, with why. Its text names the
// source, the call and the cause. Except for a build command's bounded
// stderr, it never holds a parameter's value, the credential, the request's
// body or the address with its values in it. The stderr may repeat parameters
// or service-derived metadata, but the credential's literal value is removed;
// the source_call event keeps only the reason.
type CallError struct {
	Source, Call, Reason string
	// Status is what the service answered, 0 when nothing was sent or
	// nothing came back.
	Status int
	msg    string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("source %s, call %s: %s", e.Source, e.Call, e.msg)
}

// Result is what a call that was answered returns.
type Result struct {
	// Status is the service's. An answer that is no success (4xx, 5xx) is
	// a Result too, without an Answer: what a service says of a request it
	// refused may repeat the request, which is not kept.
	Status int `json:"status"`
	// Answer is the JSON of a success, after the call's select; empty for
	// a success that said nothing.
	Answer json.RawMessage `json:"answer,omitempty"`
	// Bytes is how much the service sent.
	Bytes int           `json:"bytes"`
	Took  time.Duration `json:"-"`
}

// OK reports whether the service answered with a success.
func (r *Result) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Do makes the call name of d with params, each a parameter's value as a
// command line gives it, and credential, the value of the variable d names
// ("" for a declaration that names none). It is the one place a declared
// source is spoken to, and what it holds every call to is the same for
// each: the base's own host and no other, over https (plain http only to
// this machine, for a stand-in), no redirect followed, Timeout, and an
// answer that is JSON within its bound. It writes nothing anywhere: the
// request, its body and the credential are in this call's memory alone.
//
// Where the credential is the address (Credential.URL), the address a call
// goes to is the credential's own value, held to the same rules, and no
// error repeats any of it. An answer that is no JSON, such as the
// "Accepted" Make's webhooks say, is a success without an answer, since
// the endpoint is whatever the operator pointed the URL at.
func Do(ctx context.Context, d *Declaration, name string, params map[string]string, credential string) (*Result, error) {
	return DoKeyed(ctx, d, name, params, credential, "")
}

// ValidateCallParams checks parameters against the named call without
// making a request. A caller that must write something before making the
// request can refuse bad values before that write.
func ValidateCallParams(d *Declaration, name string, params map[string]string) error {
	c, ok := d.Call(name)
	if !ok {
		return fmt.Errorf("source %s, call %s: the source has no call of that name", d.Name, name)
	}
	_, err := c.values(params)
	return err
}

// DoKeyed is Do with the key of an update: where d names the header the
// service takes one in (Idempotency) and the call is an update, key is
// sent in it, and the service writes once however often it gets the key.
// The key is the caller's to keep between two tries of what is one call to
// it; "" sends none. credential is the value of the variable d names for
// the call's kind (Credential.EnvOf).
func DoKeyed(ctx context.Context, d *Declaration, name string, params map[string]string, credential, key string) (*Result, error) {
	return doKeyed(ctx, d, name, params, credential, key, nil)
}

// DoKeyedBody is DoKeyed with body already built, rather than filled from
// the call's template. The body is sent unchanged; nil sends no body. Path,
// query, credential and key are filled and checked as DoKeyed fills them.
func DoKeyedBody(ctx context.Context, d *Declaration, name string, params map[string]string, credential, key string, body json.RawMessage) (*Result, error) {
	return doKeyed(ctx, d, name, params, credential, key, body)
}

func doKeyed(ctx context.Context, d *Declaration, name string, params map[string]string, credential, key string, built json.RawMessage) (*Result, error) {
	// Where the credential is the address, it is the secret of the call,
	// and nothing said of the call repeats it or its host. where is how the
	// messages below name the address, and hide takes it out of what the
	// network's own errors say.
	address := d.Credential != nil && d.Credential.URL
	var hide *regexp.Regexp
	fail := func(reason string, status int, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if hide != nil {
			msg = hide.ReplaceAllLiteralString(msg, redacted)
		}
		return &CallError{Source: d.Name, Call: name, Reason: reason, Status: status, msg: msg}
	}
	c, ok := d.Call(name)
	if !ok {
		return nil, fail(NoCall, 0, "the source has no call of that name")
	}
	if d.Credential != nil {
		switch {
		case credential == "":
			return nil, fail(NoCredential, 0, "no credential: the source's env file does not set %s", d.Credential.EnvOf(c.Kind))
		case strings.ContainsFunc(credential, func(r rune) bool { return r < ' ' || r == 0x7f }):
			return nil, fail(NoCredential, 0, "the value of %s has a control character in it, a line break perhaps: it is one line", d.Credential.EnvOf(c.Kind))
		}
	}
	var base *url.URL
	where := ""
	if address {
		u, fault := checkAddress(credential)
		if fault != addressOK {
			return nil, fail(Refused, 0, "the value of %s %s", d.Credential.EnvOf(c.Kind), fault.words())
		}
		base, where, hide = u, "the credential's URL", hiding(credential, u)
	} else {
		var err error
		base, err = url.Parse(d.Base)
		switch {
		case err != nil || base.Host == "" || base.User != nil:
			return nil, fail(Refused, 0, "its base is no address")
		case base.Scheme == "http" && !LoopbackHost(base.Hostname()):
			return nil, fail(Refused, 0, "its base is plain http to %s, which is not this machine: a call goes over https", base.Hostname())
		case base.Scheme != "https" && base.Scheme != "http":
			return nil, fail(Refused, 0, "its base is %s://, not https://", base.Scheme)
		}
		where = base.Host
	}
	if strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return nil, fail(BadParam, 0, "the key of the update is no value of a header")
	}
	vals, err := c.values(params)
	if err != nil {
		return nil, fail(BadParam, 0, "%v", err)
	}
	inPath := d.Credential != nil && d.Credential.InPath
	var target string
	if address {
		target = c.addressTarget(credential, vals)
	} else if target, err = c.target(d.Base, base, vals, credential, inPath); err != nil {
		return nil, fail(BadParam, 0, "%v", err)
	}

	// start is before Timeout is applied: cause() measures the source's
	// own bound from it, and Took below too.
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var body io.Reader
	contentType := ""
	if built != nil {
		body = bytes.NewReader(built)
		contentType = "application/json"
	} else if c.Request.Body != nil {
		var raw []byte
		var err error
		if c.hasFileParam() {
			raw, contentType, err = c.multipart(vals, credential)
			if err != nil {
				return nil, fail(BadParam, 0, "the body cannot be sent as an upload")
			}
		} else {
			var filled any
			filled, _ = fill(c.Request.Body, vals, credential)
			raw, err = json.Marshal(filled)
			if err != nil {
				return nil, fail(BadParam, 0, "the body cannot be sent as JSON")
			}
			contentType = "application/json"
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, c.Request.Method, target, body)
	if err != nil {
		return nil, fail(BadParam, 0, "the request cannot be made")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yardr")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if d.Credential != nil && d.Credential.Header != "" {
		k, v, _ := strings.Cut(d.Credential.Header, ": ")
		req.Header.Set(k, strings.Replace(v, CredentialMark, credential, 1))
	}
	if key != "" && d.Idempotency != "" && c.Kind == Update {
		req.Header.Set(d.Idempotency, key)
	}

	resp, err := client(base.Scheme == "http").Do(req)
	if err != nil {
		return nil, fail(unsent(ctx, err), 0, "no answer from %s: %s", where, cause(ctx, err, start))
	}
	defer resp.Body.Close()
	res := &Result{Status: resp.StatusCode}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		to := ""
		// Where a redirect points is not said of an address that is a
		// secret: it may be the host that is.
		if l, err := url.Parse(resp.Header.Get("Location")); err == nil && l.Host != "" && !address {
			to = " to " + l.Host
		}
		return nil, fail(Redirect, resp.StatusCode, "%s answered %d, a redirect%s: yardr follows none, since the request and its credential would go where the redirect points",
			where, resp.StatusCode, to)
	case !res.OK():
		res.Took = time.Since(start)
		return res, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, AnswerLimit+1))
	res.Bytes, res.Took = len(raw), time.Since(start)
	switch {
	case err != nil:
		return nil, fail(unsent(ctx, err), resp.StatusCode, "the answer of %s broke off: %s", where, cause(ctx, err, start))
	case len(raw) > AnswerLimit:
		return nil, fail(TooLarge, resp.StatusCode, "the answer is over %d bytes, the most yardr reads of one: it is refused, not cut, since half a JSON document misleads", AnswerLimit)
	case len(bytes.TrimSpace(raw)) == 0:
		return res, nil
	case !json.Valid(raw):
		// A webhook says "Accepted" in words (Make's does): its 2xx is
		// that it took the call, and the words are not kept.
		if address {
			return res, nil
		}
		return nil, fail(NotJSON, resp.StatusCode, "%s answered %d with what is no JSON", where, resp.StatusCode)
	}
	answer := selected(raw, c.Answer.Select)
	if len(answer) > c.Answer.Max {
		return nil, fail(TooLarge, resp.StatusCode, "the answer is %d bytes, over the call's bound of %d (answer.max in the declaration raises it, to %d at most): it is refused, not cut, since half a JSON document misleads",
			len(answer), c.Answer.Max, AnswerLimit)
	}
	res.Answer = answer
	return res, nil
}

// client is the client of one call. Nothing of it outlives the call: no
// connection is kept for the next, which may be an hour away.
func client(plain bool) *http.Client {
	dialer := &net.Dialer{Timeout: Timeout}
	tr := &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: Timeout}
	if plain {
		// Held again where the connection is made: a name can be made to
		// say anything, and what is checked here is the address it gave.
		dialer.Control = loopbackOnly
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	tr.DialContext = dialer.DialContext
	return &http.Client{
		Transport: tr,
		Timeout:   Timeout,
		// The 3xx is the answer: Do fails with it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// loopbackOnly refuses a connection to anything but this machine.
func loopbackOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if i := strings.IndexByte(host, '%'); i >= 0 { // a zone
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("plain http is for this machine only, and %s is not", host)
	}
	return nil
}

// unsent is the reason of a call that failed on the way.
func unsent(ctx context.Context, err error) string {
	var t interface{ Timeout() bool }
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &t) && t.Timeout() {
		return TimedOut
	}
	return Unreachable
}

// cause says why without the address: net/http's error names the URL, and
// a URL has the values of the call in it. start is when the call began,
// before Timeout was applied to ctx: a deadline of ctx's that falls short
// of start+Timeout is the caller's own, and the wait it names is the
// caller's bound, not Timeout's.
func cause(ctx context.Context, err error, start time.Time) string {
	if unsent(ctx, err) == TimedOut {
		if deadline, ok := ctx.Deadline(); ok && deadline.Before(start.Add(Timeout)) {
			return "nothing within the caller's " + roundBound(deadline.Sub(start)).String()
		}
		return "nothing within " + Timeout.String()
	}
	var u *url.Error
	if errors.As(err, &u) {
		err = u.Err
	}
	return err.Error()
}

// roundBound rounds a caller's bound to what reads: the wait a deadline
// leaves is the caller's whole seconds minus the breath the call took to
// begin, which unrounded reads as 9.999999s. Within a tenth of a second
// of whole seconds it says those; further off it steps down, to a tenth
// of a second past one and to milliseconds below.
func roundBound(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if near := d.Round(time.Second); near > 0 && d-near < 100*time.Millisecond && near-d < 100*time.Millisecond {
		return near
	}
	if d >= time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}

// redacted stands in what is said of a call for the address it went to.
const redacted = "[address redacted]"

// hiding finds the address in what is said of a call to a credential that
// is one: the credential whole, the host it names and that host without
// its port, which the network's own errors name (a lookup that failed, a
// connection refused), in any case, since they may lower it. The longest
// comes first, as a host stands inside the credential and a hostname
// inside the host.
func hiding(credential string, u *url.URL) *regexp.Regexp {
	parts := []string{regexp.QuoteMeta(credential), regexp.QuoteMeta(u.Host)}
	if h := u.Hostname(); h != "" && h != u.Host {
		parts = append(parts, regexp.QuoteMeta(h))
	}
	return regexp.MustCompile("(?i)" + strings.Join(parts, "|"))
}

// value is a parameter's value as it was given and as its type reads it.
type value struct {
	text  string
	typed any
	// file is what a file parameter's path held when the call was made,
	// read once so the size it is held to and the bytes that are sent are
	// one file's. Its typed value stays the path, as a build command is
	// handed it.
	file []byte
}

// defaultText turns a declaration's typed default back into the text a call
// parameter takes. Parse checked the type when it read the declaration.
func defaultText(p Param) (string, error) {
	switch p.Type {
	case "string", "enum", "file":
		var s string
		if err := json.Unmarshal(p.Default, &s); err != nil {
			return "", err
		}
		return s, nil
	case "int":
		var n int64
		if err := json.Unmarshal(p.Default, &n); err != nil {
			return "", err
		}
		return strconv.FormatInt(n, 10), nil
	case "bool":
		var b bool
		if err := json.Unmarshal(p.Default, &b); err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	default:
		return "", fmt.Errorf("parameter has no type")
	}
}

// values reads params against the call's declaration. Its errors name the
// parameter and what is wrong, never the value.
func (c *Call) values(params map[string]string) (map[string]value, error) {
	for name := range params {
		if _, ok := c.param(name); !ok {
			return nil, fmt.Errorf("%s is no parameter of the call (it takes: %s)", strconv.Quote(name), or(strings.Join(c.names(), ", "), "none"))
		}
	}
	out := map[string]value{}
	for _, p := range c.Params {
		s, given := params[p.Name]
		if !given && len(p.Default) > 0 {
			var err error
			s, err = defaultText(p)
			if err != nil {
				return nil, fmt.Errorf("parameter %s has an invalid default", p.Name)
			}
			given = true
		}
		if !given {
			if !p.Optional {
				return nil, fmt.Errorf("parameter %s is missing", p.Name)
			}
			continue
		}
		v := value{text: s, typed: s}
		switch p.Type {
		case "string":
			switch {
			case !utf8.ValidString(s):
				return nil, fmt.Errorf("parameter %s is no text (not UTF-8)", p.Name)
			case p.Max > 0 && utf8.RuneCountInString(s) > p.Max:
				return nil, fmt.Errorf("parameter %s is %d characters, and at most %d", p.Name, utf8.RuneCountInString(s), p.Max)
			case p.re != nil && !p.re.MatchString(s):
				return nil, fmt.Errorf("parameter %s does not match its pattern %s", p.Name, p.Pattern)
			// A declaration that did not come from Parse.
			case p.re == nil && p.Pattern != "":
				return nil, fmt.Errorf("parameter %s has a pattern that was not read", p.Name)
			}
		case "int":
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parameter %s is no whole number", p.Name)
			}
			v.typed = n
		case "bool":
			if s != "true" && s != "false" {
				return nil, fmt.Errorf("parameter %s is true or false", p.Name)
			}
			v.typed = s == "true"
		case "enum":
			if !slices.Contains(p.Values, s) {
				return nil, fmt.Errorf("parameter %s is one of %s", p.Name, strings.Join(p.Values, ", "))
			}
		case "file":
			// The path is read now, not when the body is sent, so the
			// size it is held to and the bytes that go are one file's.
			// The errors name the parameter, never the path: no error of
			// a call holds a value.
			st, err := os.Stat(s)
			switch {
			case err != nil || !st.Mode().IsRegular():
				return nil, fmt.Errorf("parameter %s is no file that can be read", p.Name)
			case p.Max > 0 && st.Size() > int64(p.Max):
				return nil, fmt.Errorf("parameter %s is %d bytes, and at most %d", p.Name, st.Size(), p.Max)
			}
			data, err := os.ReadFile(s)
			switch {
			case err != nil:
				return nil, fmt.Errorf("parameter %s is no file that can be read", p.Name)
			case p.Max > 0 && len(data) > p.Max:
				return nil, fmt.Errorf("parameter %s is %d bytes, and at most %d", p.Name, len(data), p.Max)
			}
			v.file = data
		default:
			return nil, fmt.Errorf("parameter %s has no type", p.Name)
		}
		out[p.Name] = v
	}
	return out, nil
}

func (c *Call) names() []string {
	out := make([]string, len(c.Params))
	for i, p := range c.Params {
		out[i] = p.Name
	}
	return out
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// target is the address of the call: the base, the path with each {param}
// as one escaped segment or part of one, and the query. A value cannot
// leave the base: a slash in it is escaped, and one that makes a segment
// "." or ".." (which no escaping keeps a server from walking up by) or
// nothing is refused.
//
// A path the file says takes the credential (in_path) names {credential},
// which goes in as it is: a service that takes it there reads the URL as
// every client sends it, and an escaped ':' would go as %3A where it
// expects ':'. The address is still held under the base below.
func (c *Call) target(baseText string, base *url.URL, vals map[string]value, credential string, inPath bool) (string, error) {
	if strings.Contains(c.Request.Path, CredentialMark) {
		switch {
		case !inPath:
			return "", fmt.Errorf("%s is in the path, and the file does not say the service takes it there (credential.in_path: true)", CredentialMark)
		case credential == "":
			return "", fmt.Errorf("the credential is empty, and it is part of the path")
		}
	}
	at := func(m string) string {
		if m == CredentialMark {
			return credential
		}
		return vals[m[1:len(m)-1]].text
	}
	segs := strings.Split(c.Request.Path, "/")
	for i, seg := range segs {
		if !mark.MatchString(seg) {
			continue
		}
		var bad error
		plain := mark.ReplaceAllStringFunc(seg, at)
		segs[i] = mark.ReplaceAllStringFunc(seg, func(m string) string {
			if m == CredentialMark {
				return credential
			}
			v := vals[m[1:len(m)-1]].text
			if v == "" {
				bad = fmt.Errorf("parameter %s is empty, and it is part of the path", m[1:len(m)-1])
			}
			return url.PathEscape(v)
		})
		if bad != nil {
			return "", bad
		}
		if plain == "." || plain == ".." {
			return "", fmt.Errorf("a parameter makes a %q segment of the path, which leaves the source's base", plain)
		}
	}
	u, err := url.Parse(strings.TrimRight(baseText, "/") + strings.Join(segs, "/"))
	if err != nil || u.Host != base.Host || u.Scheme != base.Scheme || !strings.HasPrefix(u.EscapedPath(), strings.TrimRight(base.EscapedPath(), "/")+"/") {
		return "", errors.New("the path with its parameters is no address under the source's base")
	}
	u.RawQuery = c.query(vals)
	return u.String(), nil
}

// addressTarget is the address of a call whose credential is the address:
// the credential exactly as it was given, since the service made its path
// and a trailing slash is its own, then the call's query where it has one.
// checkAddress kept the credential from having a query of its own.
func (c *Call) addressTarget(address string, vals map[string]value) string {
	if q := c.query(vals); q != "" {
		return address + "?" + q
	}
	return address
}

// query is the call's query with its parameters filled, encoded; "" for a
// call with none given.
func (c *Call) query(vals map[string]value) string {
	q := url.Values{}
	for _, kv := range c.Request.Query {
		if m := mark.FindStringSubmatch(kv.Value); m != nil && m[0] == kv.Value {
			if _, given := vals[m[1]]; !given {
				continue // an optional parameter that was not given
			}
		}
		q.Add(kv.Key, mark.ReplaceAllStringFunc(kv.Value, func(m string) string { return vals[m[1:len(m)-1]].text }))
	}
	return q.Encode()
}

// fill is the body v with its {name}s replaced. A string that is exactly
// {param} becomes the parameter's typed value, and is left out (keep
// false) when the parameter is optional and was not given; one that is
// exactly {credential} becomes the credential.
func fill(v any, vals map[string]value, credential string) (out any, keep bool) {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, child := range v {
			if o, keep := fill(child, vals, credential); keep {
				m[k] = o
			}
		}
		return m, true
	case []any:
		l := make([]any, 0, len(v))
		for _, child := range v {
			if o, keep := fill(child, vals, credential); keep {
				l = append(l, o)
			}
		}
		return l, true
	case string:
		if v == CredentialMark {
			return credential, true
		}
		if m := mark.FindStringSubmatch(v); m != nil && m[0] == v {
			val, given := vals[m[1]]
			return val.typed, given
		}
		return mark.ReplaceAllStringFunc(v, func(m string) string { return vals[m[1:len(m)-1]].text }), true
	}
	return v, true
}

// hasFileParam reports whether the body's template names a file parameter.
// Such a body is sent as a multipart upload, not as JSON; what names none
// is sent as it always was.
func (c *Call) hasFileParam() bool {
	var walk func(v any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case map[string]any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if walk(child) {
					return true
				}
			}
		case string:
			for _, m := range mark.FindAllStringSubmatch(v, -1) {
				if p, ok := c.param(m[1]); ok && p.Type == "file" {
					return true
				}
			}
		}
		return false
	}
	return walk(c.Request.Body)
}

// multipart is the body's template filled as a multipart upload: each file
// parameter one file part under the body's key for it, every other value a
// text field. Keys go in order, as JSON's do. A whole value whose optional
// parameter was not given is left out, as fill leaves it.
func (c *Call) multipart(vals map[string]value, credential string) (raw []byte, contentType string, err error) {
	top, _ := c.Request.Body.(map[string]any)
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, k := range keys {
		if s, ok := top[k].(string); ok {
			if m := mark.FindStringSubmatch(s); m != nil && m[0] == s {
				if p, declared := c.param(m[1]); declared && p.Type == "file" {
					val, given := vals[m[1]]
					if !given {
						continue
					}
					if err := writeFilePart(w, k, val.text, val.file); err != nil {
						return nil, "", err
					}
					continue
				}
			}
		}
		filled, keep := fill(top[k], vals, credential)
		if !keep {
			continue
		}
		field, ok := filled.(string)
		if !ok {
			var b []byte
			if b, err = json.Marshal(filled); err != nil {
				return nil, "", err
			}
			field = string(b)
		}
		if err := w.WriteField(k, field); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// writeFilePart writes one file parameter as a part of an upload: the
// body's key is the part's name, the path's base its file name, and its
// type is sniffed from what the file holds, as a client's upload goes.
func writeFilePart(w *multipart.Writer, key, path string, data []byte) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", multipart.FileContentDisposition(key, filepath.Base(path)))
	h.Set("Content-Type", http.DetectContentType(data))
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

// selected is the answer raw with only keys kept: of the object it is, or
// of each object in the list it is. Anything else, and an answer without
// keys to keep, is returned as it is, without its spaces.
func selected(raw []byte, keys []string) []byte {
	pick := func(obj json.RawMessage) json.RawMessage {
		var m map[string]json.RawMessage
		if t := bytes.TrimSpace(obj); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &m) != nil {
			return obj
		}
		out := map[string]json.RawMessage{}
		for _, k := range keys {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
		b, _ := json.Marshal(out)
		return b
	}
	out := json.RawMessage(raw)
	if len(keys) > 0 {
		var list []json.RawMessage
		if t := bytes.TrimSpace(raw); t[0] == '[' && json.Unmarshal(t, &list) == nil {
			for i := range list {
				list[i] = pick(list[i])
			}
			out, _ = json.Marshal(list)
		} else {
			out = pick(raw)
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, out) != nil {
		return out
	}
	return buf.Bytes()
}
