package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// pushover is the file of the design this package was built for
// (yardr-1481), whole.
const pushover = `name: pushover
about: A push to a person's phone through Pushover.
base: https://api.pushover.net/1
credential:
  env: PUSHOVER_TOKEN                 # the application's token: the name, never the value
calls:
  message:
    kind: update
    about: One push to one person
    params:
      user:  { type: string, pattern: "^[A-Za-z0-9]{30}$" }
      title: { type: string, max: 250 }
      text:  { type: string, max: 1024 }
      link:  { type: string, max: 512, optional: true }
    request:
      method: POST
      path: /messages.json
      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}", url: "{link}" }
notify:
  call: message
  address: { param: user, env: PUSHOVER_USER }   # the person's own key, read from the user's env_file
  title: title                                   # yardr's value -> the call's parameter
  text: text
  link: link
`

// crm is the worked example of the declaration format (yardr-3e8x).
const crm = `name: crm
about: The customer system. Customers by id; notes on a customer.
base: https://crm.example.com/api/v2
credential:
  env: CRM_TOKEN                                  # the name, never the value
  header: "Authorization: Bearer {credential}"
check: ping                                       # a read call without parameters, for source check
calls:
  ping:     { kind: read, request: { method: GET, path: /me } }
  customer:
    kind: read
    about: Look a customer up by id
    params: { id: { type: string, pattern: "^[0-9]{1,12}$" } }
    request: { method: GET, path: "/customers/{id}" }
    answer: { select: [id, name, plan, owner, open_tickets] }
  note:
    kind: update
    about: Add a note to a customer
    params: { id: { type: string, pattern: "^[0-9]{1,12}$" }, text: { type: string, max: 2000 } }
    request: { method: POST, path: "/customers/{id}/notes", body: { text: "{text}" } }
`

func TestParsePushover(t *testing.T) {
	d, err := Parse("pushover.yaml", []byte(pushover))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "pushover" || d.Base != "https://api.pushover.net/1" || d.Credential == nil || d.Credential.Env != "PUSHOVER_TOKEN" || d.Credential.Header != "" || d.Check != "" {
		t.Fatalf("declaration = %+v", d)
	}
	if n := d.Notify; n == nil || *n != (Notify{Call: "message", Address: "user", AddressEnv: "PUSHOVER_USER", Title: "title", Text: "text", Link: "link"}) {
		t.Fatalf("notify = %+v", n)
	}
	c, ok := d.Call("message")
	if !ok || len(d.Calls) != 1 || c.Kind != Update || c.Request.Line() != "POST /messages.json" || c.Answer.Max != AnswerMax {
		t.Fatalf("call = %+v", c)
	}
	if len(c.Params) != 4 || c.Params[0].Name != "user" || c.Params[0].Pattern == "" || c.Params[1].Max != 250 || !c.Params[3].Optional || c.Params[2].Optional {
		t.Fatalf("params = %+v", c.Params)
	}
	body, _ := c.Request.Body.(map[string]any)
	if body["token"] != CredentialMark || body["message"] != "{text}" || len(body) != 5 {
		t.Fatalf("body = %v", c.Request.Body)
	}
}

func TestParseCRM(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(crm))
	if err != nil {
		t.Fatal(err)
	}
	if d.Check != "ping" || d.Credential.Header != "Authorization: Bearer {credential}" || len(d.Calls) != 3 || d.Notify != nil {
		t.Fatalf("declaration = %+v", d)
	}
	if c, _ := d.Call("customer"); c.Kind != Read || len(c.Answer.Select) != 5 || c.Request.Path != "/customers/{id}" {
		t.Fatalf("customer = %+v", c)
	}
	if d.Calls[0].Name != "ping" || d.Calls[2].Name != "note" {
		t.Fatalf("calls are not in file order: %+v", d.Calls)
	}
}

// A default is a typed value sent when its parameter is left out, and makes
// the parameter optional to callers.
func TestParseParameterDefaults(t *testing.T) {
	const text = `name: defaults
base: https://example.com
calls:
  set:
    kind: update
    params:
      phrase: { type: string, default: "hello", max: 10 }
      count: { type: int, default: 0 }
      notify: { type: bool, default: false }
      role: { type: enum, values: [reader, commenter], default: reader }
    request:
      method: POST
      path: /set
      body: { phrase: "{phrase}", count: "{count}", notify: "{notify}", role: "{role}" }
`
	d, err := Parse("defaults.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := d.Call("set")
	for i, want := range []string{`"hello"`, "0", "false", `"reader"`} {
		p := c.Params[i]
		if !p.Optional || string(p.Default) != want {
			t.Errorf("parameter %s = %+v, want optional default %s", p.Name, p, want)
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Declaration
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	got, _ := roundTrip.Call("set")
	if string(got.Params[2].Default) != "false" || !got.Params[2].Optional {
		t.Fatalf("false default did not survive storage JSON: %+v", got.Params[2])
	}
}

func TestParseRefusesInvalidParameterDefaults(t *testing.T) {
	const file = `name: defaults
base: https://example.com
calls:
  set:
    kind: update
    params:
      value: { type: %s, %s }
    request: { method: POST, path: /set, body: { value: "{value}" } }
`
	for _, tc := range []struct{ typ, setting, want string }{
		{"bool", `default: "false"`, "want true or false"},
		{"enum", "values: [reader, commenter], default: writer", "default is one of reader, commenter"},
		{"string", `default: too-long, max: 3`, "default is 8 characters, and at most 3"},
	} {
		_, err := Parse("defaults.yaml", []byte(fmt.Sprintf(file, tc.typ, tc.setting)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: err = %v, want %q", tc.typ, tc.setting, err, tc.want)
		}
	}
}

// A file whose service takes the credential in the request path says so
// (credential.in_path: true), and then a path may name {credential} as a
// whole segment or as part of one. A query refuses one either way.
func TestParseCredentialInPath(t *testing.T) {
	text := strings.Replace(crm, "  header: \"Authorization: Bearer {credential}\"\n", "  in_path: true\n", 1)
	text = strings.Replace(text, `path: "/customers/{id}/notes"`, `path: "/t{credential}/notes/{id}"`, 1)
	d, err := Parse("inpath.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if d.Credential == nil || !d.Credential.InPath {
		t.Fatalf("credential = %+v", d.Credential)
	}
	if c, _ := d.Call("note"); c.Request.Path != "/t{credential}/notes/{id}" {
		t.Fatalf("path = %q", c.Request.Path)
	}
}

// webhook is a service whose address is its secret: the credential is the
// URL every call goes to, so the file names no base and a call no path of
// its own (or the path /, which says the same).
const webhook = `name: webhook
about: Any service that takes a POST at a URL.
credential:
  env: WEBHOOK_URL
  url: true
calls:
  send:
    kind: update
    about: POST an object to the URL
    params: { text: { type: string, max: 200 } }
    request: { method: POST, body: { text: "{text}" } }
  ping:
    kind: update
    request: { method: POST, path: / }
  tagged:
    kind: update
    params: { tag: { type: string, max: 20 } }
    request: { method: POST, query: { tag: "{tag}" }, body: { a: 1 } }
`

// A credential that is the address needs no base, and no path: the file
// reads with a call's path left out or written /, which are the same, and
// says where its calls go in words, since the address is a secret and is
// in no file.
func TestParseCredentialURL(t *testing.T) {
	d, err := Parse("webhook.yaml", []byte(webhook))
	if err != nil {
		t.Fatal(err)
	}
	if d.Base != "" || d.Credential == nil || !d.Credential.URL || d.Credential.Env != "WEBHOOK_URL" {
		t.Fatalf("declaration = %+v, credential %+v", d, d.Credential)
	}
	if got := d.Address(); got != "the URL in WEBHOOK_URL" {
		t.Fatalf("address = %q", got)
	}
	for _, name := range []string{"send", "ping", "tagged"} {
		c, ok := d.Call(name)
		if !ok || c.Request.Path != "/" || c.Request.Line() != "POST /" {
			t.Errorf("call %s = %+v: want the path /, left out or written", name, c)
		}
	}
	// What a file names as its base is words in its place, and the flag
	// survives the JSON the declaration is shown in.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var again Declaration
	if err := json.Unmarshal(raw, &again); err != nil || !again.Credential.URL || again.Address() != d.Address() {
		t.Fatalf("round trip = %+v, %v", again.Credential, err)
	}
	// url: false is a credential like any other, with its base.
	plain, err := Parse("crm.yaml", []byte(strings.Replace(crm, "  env: CRM_TOKEN ", "  url: false\n  env: CRM_TOKEN ", 1)))
	if err != nil || plain.Credential.URL || plain.Address() != "https://crm.example.com/api/v2" {
		t.Fatalf("url: false = %+v, %v", plain, err)
	}
}

// What contradicts a credential that is the address is refused where the
// file is read: a base beside it, a header, a path or a mint to put it
// through, a path of a call beyond /, the credential written into a call's
// request, and a file that names neither a base nor the address.
func TestParseRefusesACredentialThatIsTheAddress(t *testing.T) {
	with := func(old, new string) string {
		if !strings.Contains(webhook, old) {
			t.Fatalf("the webhook file has no %q", old)
		}
		return strings.Replace(webhook, old, new, 1)
	}
	for _, tc := range []struct {
		name, text, key, want string
	}{
		{"a base beside it", with("credential:", "base: https://hooks.example.com\ncredential:"),
			"base", "the credential is the address every call goes to (credential.url: true), so the file names no base beside it"},
		{"a header to send it in", with("  url: true", "  url: true\n  header: \"X-Hook: {credential}\""),
			"credential.header", "the credential is the address every call goes to (url: true), and is not sent in a header"},
		{"a path to put it in", with("  url: true", "  url: true\n  in_path: true"),
			"credential.in_path", "and is not put in a path"},
		{"a mint", with("  url: true", "  url: true\n  mint: [HOOK_ID, HOOK_KEY]"),
			"credential.mint", "and is not minted"},
		{"a second variable for updates", with("  url: true", "  url: true\n  update_env: WEBHOOK_SEND_URL"),
			"credential.update_env", "and is not given a second variable for updates"},
		{"url that is no boolean", with("  url: true", "  url: yes"),
			"credential.url", "want true or false"},
		{"a path beyond /", with("request: { method: POST, path: / }", "request: { method: POST, path: /hooks }"),
			"calls.ping.request.path", `"/hooks": the credential is the address the call goes to (credential.url: true), so the call has no path of its own: leave it out, or write /`},
		{"a path with a parameter", with("request: { method: POST, path: / }", "request: { method: POST, path: \"/{text}\" }"),
			"calls.ping.request.path", "so the call has no path of its own"},
		{"the credential in a query", with(`query: { tag: "{tag}" }`, `query: { tag: "{credential}" }`),
			"calls.tagged.request.query.tag", "{credential} in a query: the credential is the address the call goes to (credential.url: true), and is sent nowhere else"},
		{"the credential in a body", with(`body: { a: 1 }`, `body: { a: "{credential}" }`),
			"calls.tagged.request.body.a", "{credential} in a body: the credential is the address the call goes to (credential.url: true), and is sent nowhere else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse("webhook.yaml", []byte(tc.text))
			var errs Errors
			if !errors.As(err, &errs) || d != nil {
				t.Fatalf("Parse = %+v, %v; want it refused", d, err)
			}
			for _, e := range errs {
				if e.Key == tc.key && strings.Contains(e.Msg, tc.want) && e.Line > 0 {
					return
				}
			}
			t.Fatalf("no error at %s with %q in:\n%v", tc.key, tc.want, err)
		})
	}
	// With url left out the file names its base, as before, and the
	// message says there is another way to be a source's address.
	_, err := Parse("webhook.yaml", []byte(with("  url: true\n", "")))
	if err == nil || !strings.Contains(err.Error(), "base: missing: the address every call's path is under (https://…), or credential.url: true where the credential is the address itself") {
		t.Fatalf("without url or a base: %v", err)
	}
}

// Every refusal of a declaration, each with a message that names the
// cause, at the key and the line it is on.
func TestParseRefuses(t *testing.T) {
	// with is the CRM file with one line replaced.
	with := func(old, new string) string {
		if !strings.Contains(crm, old) {
			t.Fatalf("the CRM file has no %q", old)
		}
		return strings.Replace(crm, old, new, 1)
	}
	for _, tc := range []struct {
		name, text, key, want string
	}{
		{"the credential in a path", with(`path: "/customers/{id}" }`, `path: "/customers/{id}/{credential}" }`),
			"calls.customer.request.path", "{credential} in a path: a credential there is in the logs of every server on the way"},
		{"the credential in a query", with(`path: "/customers/{id}" }`, `path: "/customers/{id}", query: { key: "{credential}" } }`),
			"calls.customer.request.query.key", "{credential} in a query"},
		{"the credential in a query of a file with in_path", strings.Replace(with(`path: "/customers/{id}" }`, `path: "/customers/{id}", query: { key: "{credential}" } }`),
			"  header: \"Authorization: Bearer {credential}\"\n", "  header: \"Authorization: Bearer {credential}\"\n  in_path: true\n", 1),
			"calls.customer.request.query.key", "{credential} in a query"},
		{"in_path that is no boolean", with(`  header: "Authorization: Bearer {credential}"`, "  in_path: yes\n  header: \"Authorization: Bearer {credential}\""),
			"credential.in_path", "want true or false"},
		{"the credential as a part of a body's value", with(`body: { text: "{text}" }`, `body: { text: "{text}", auth: "Bearer {credential}" }`),
			"calls.note.request.body.auth", "{credential} is a whole value of a body"},
		{"the credential in a body of a file that names none", strings.Replace(with(`body: { text: "{text}" }`, `body: { text: "{text}", auth: "{credential}" }`),
			"credential:\n  env: CRM_TOKEN                                  # the name, never the value\n  header: \"Authorization: Bearer {credential}\"\n", "", 1),
			"calls.note.request.body.auth", "the file names no credential"},
		{"a parameter in the credential's header", with(`header: "Authorization: Bearer {credential}"`, `header: "Authorization: Bearer {credential} {id}"`),
			"credential.header", "a parameter never reaches a header"},
		{"a credential written into the file", with("env: CRM_TOKEN ", "env: s3cr3t-value "),
			"credential.env", "never the credential"},
		{"plain http to another machine", with("base: https://crm.example.com/api/v2", "base: http://crm.example.com/api/v2"),
			"base", "http:// is for a stand-in on this machine only"},
		{"no http at all", with("base: https://crm.example.com/api/v2", "base: ftp://crm.example.com/api/v2"),
			"base", "ftp:// is not https://"},
		{"a password in the base", with("base: https://crm.example.com/api/v2", "base: https://me:pw@crm.example.com/api/v2"),
			"base", "the credential is named under credential"},
		{"a path that is an address", with(`path: "/customers/{id}" }`, `path: "https://elsewhere.example.com/{id}" }`),
			"calls.customer.request.path", "is an address, not a path"},
		{"a path of another host", with(`path: "/customers/{id}" }`, `path: "//elsewhere.example.com/{id}" }`),
			"calls.customer.request.path", "is an address, not a path"},
		{"a path that leaves the base", with(`path: "/customers/{id}" }`, `path: "/../admin/{id}" }`),
			"calls.customer.request.path", "leaves the source's base"},
		{"a query written into the path", with(`path: "/customers/{id}" }`, `path: "/customers?id={id}" }`),
			"calls.customer.request.path", "a query is under query"},
		{"a parameter that is not declared", with(`path: "/customers/{id}" }`, `path: "/customers/{id}/{other}" }`),
			"calls.customer.request.path", "{other} is no parameter of the call customer"},
		{"a parameter that is not declared, in a body", with(`body: { text: "{text}" }`, `body: { text: "{text}", by: [ "{who}" ] }`),
			"calls.note.request.body.by", "{who} is no parameter of the call note"},
		{"a read call that deletes", with("ping:     { kind: read, request: { method: GET, path: /me } }", "ping:     { kind: read, request: { method: DELETE, path: /me } }"),
			"calls.ping.request.method", "DELETE in a call of kind read"},
		{"a name yardr has built in", with("name: crm", "name: token"),
			"name", "token is a source yardr has built in"},
		{"the name of the yards it is linked with", with("name: crm", "name: peer"),
			"name", "peer is a source yardr has built in (peer, token, yardr)"},
		{"a key it does not know", crm + "retries: 3\n",
			"retries", "unknown key"},
		{"a check that is an update", with("check: ping ", "check: note "),
			"check", "note is an update"},
		{"a check that needs a parameter", with("check: ping ", "check: customer "),
			"check", "customer needs a parameter"},
		{"a pattern that is none", with(`pattern: "^[0-9]{1,12}$" } }`+"\n    request: { method: GET", `pattern: "^[0-9" } }`+"\n    request: { method: GET"),
			"calls.customer.params.id.pattern", "error parsing regexp"},
		{"a call named as every read call", with("  ping:     {", "  read:     {"),
			"calls.read", "stands for every read call"},
		{"no calls", "name: x\nbase: https://x.example.com\n", "calls", "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse("crm.yaml", []byte(tc.text))
			var errs Errors
			if !errors.As(err, &errs) || d != nil {
				t.Fatalf("Parse = %+v, %v; want it refused", d, err)
			}
			for _, e := range errs {
				if e.Key == tc.key && strings.Contains(e.Msg, tc.want) {
					if e.File != "crm.yaml" || e.Line == 0 {
						t.Fatalf("the error names no file and line: %+v", e)
					}
					return
				}
			}
			t.Fatalf("no error at %s with %q in:\n%v", tc.key, tc.want, err)
		})
	}
}

// An optional parameter is a whole value, which can be left out: not a
// segment of a path, nor a part of a text.
func TestParseRefusesAnOptionalParameterThatCannotBeLeftOut(t *testing.T) {
	const file = `name: x
base: https://x.example.com
calls:
  get:
    kind: read
    params: { id: { type: string, optional: true } }
    request: { method: GET, path: "%s", query: { q: "%s" }, body: { v: "%s" } }
`
	for _, tc := range []struct{ path, query, body, want string }{
		{"/a/{id}", "{id}", "{id}", "a path has no place for a value that is not given"},
		{"/a", "id-{id}", "{id}", "it stands alone as a value"},
		{"/a", "{id}", "id {id}", "it stands alone as a value"},
	} {
		text := strings.Replace(file, "%s", tc.path, 1)
		text = strings.Replace(text, "%s", tc.query, 1)
		text = strings.Replace(text, "%s", tc.body, 1)
		if _, err := Parse("x.yaml", []byte(text)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v", tc, err)
		}
	}
	text := strings.Replace(strings.Replace(strings.Replace(file, "%s", "/a", 1), "%s", "{id}", 1), "%s", "{id}", 1)
	if _, err := Parse("x.yaml", []byte(text)); err != nil {
		t.Fatalf("an optional parameter as a whole value: %v", err)
	}
}

// A file is read to its end: one run names every mistake, in file order.
func TestParseNamesEveryMistake(t *testing.T) {
	text := strings.NewReplacer("name: crm", "name: token", "base: https://crm.example.com/api/v2", "base: http://crm.example.com", "method: POST", "method: POSTED").Replace(crm)
	_, err := Parse("crm.yaml", []byte(text))
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 3 {
		t.Fatalf("errors = %v", err)
	}
	if errs[0].Line != 1 || errs[1].Line != 3 || errs[2].Line <= errs[1].Line {
		t.Fatalf("not in file order: %v", err)
	}
	if got := errs[0].Error(); !strings.HasPrefix(got, "crm.yaml:1: name: token is a source yardr has built in") {
		t.Fatalf("error text = %q", got)
	}
}

// Plain http is for a stand-in on this machine: the loopback by address or
// by the name every system gives it.
func TestParseTakesHTTPToTheLoopback(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8080", "http://[::1]:8080/api", "http://localhost:8080"} {
		if _, err := Parse("x.yaml", []byte(strings.Replace(crm, "https://crm.example.com/api/v2", base, 1))); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
	for _, base := range []string{"http://127.0.0.1.example.com", "http://192.168.1.4:8080", "http://localhost.example.com"} {
		if _, err := Parse("x.yaml", []byte(strings.Replace(crm, "https://crm.example.com/api/v2", base, 1))); err == nil {
			t.Errorf("%s was taken", base)
		}
	}
}

// Every refusal of a notify key: what would fail at every send, or at the
// sends that have no link, is refused when the file is read.
func TestParseRefusesNotify(t *testing.T) {
	with := func(old, new string) string {
		if !strings.Contains(pushover, old) {
			t.Fatalf("the Pushover file has no %q", old)
		}
		return strings.Replace(pushover, old, new, 1)
	}
	const address = "  address: { param: user, env: PUSHOVER_USER }   # the person's own key, read from the user's env_file\n"
	for _, tc := range []struct {
		name, text, key, want string
	}{
		{"a call that is not in the file", with("  call: message", "  call: push"),
			"notify.call", "push is no call of this file"},
		{"a read call", with("    kind: update", "    kind: read"),
			"notify.call", "message is a read call: telling a person sends something"},
		{"no call", with("  call: message\n", ""),
			"notify.call", "missing: the call that tells a person"},
		{"a parameter the call does not have", with("  text: text", "  text: body"),
			"notify.text", "body is no parameter of the call message (it takes: user, title, text, link)"},
		{"an address the call does not have", with("param: user, env", "param: to, env"),
			"notify.address.param", "to is no parameter of the call message"},
		{"a parameter that is no text", with("title: { type: string, max: 250 }", "title: { type: int }"),
			"notify.title", "the parameter title is int"},
		{"a required parameter nothing fills", with("  text: text\n", ""),
			"notify", "the call message needs the parameter text, and nothing here fills it"},
		{"one parameter for two values", with("  text: text", "  text: title"),
			"notify.text", "the parameter title is filled by notify.title already"},
		{"a link the call insists on", with("link:  { type: string, max: 512, optional: true }", "link:  { type: string, max: 512 }"),
			"notify.link", "the parameter link is required, and most of what the yard tells has no link"},
		{"no address", with(address, ""),
			"notify.address", "missing"},
		{"an address without the name of its variable", with("param: user, env: PUSHOVER_USER", "param: user"),
			"notify.address.env", "missing: the name of the variable the address is in, in a user's env file"},
		{"an address written into the file", with("env: PUSHOVER_USER", "env: u-9f2k"),
			"notify.address.env", "never the address"},
		{"a key it does not know", with("  link: link\n", "  link: link\n  priority: high\n"),
			"notify.priority", "unknown key"},
		{"nothing to say", strings.NewReplacer("title: { type: string, max: 250 }", "title: { type: string, max: 250, optional: true }",
			"text:  { type: string, max: 1024 }", "text:  { type: string, max: 1024, optional: true }",
			"  title: title                                   # yardr's value -> the call's parameter\n", "", "  text: text\n", "").Replace(pushover),
			"notify", "neither title nor text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Parse("pushover.yaml", []byte(tc.text))
			var errs Errors
			if !errors.As(err, &errs) || d != nil {
				t.Fatalf("Parse = %+v, %v; want it refused", d, err)
			}
			for _, e := range errs {
				if e.Key == tc.key && strings.Contains(e.Msg, tc.want) && e.Line > 0 {
					return
				}
			}
			t.Fatalf("no error at %s with %q in:\n%v", tc.key, tc.want, err)
		})
	}
	// A call with no place for a title or a link is told through as it is.
	plain := strings.NewReplacer("      title: { type: string, max: 250 }\n", "", "      link:  { type: string, max: 512, optional: true }\n", "",
		`title: "{title}", `, "", `, url: "{link}"`, "",
		"  title: title                                   # yardr's value -> the call's parameter\n", "", "  link: link\n", "").Replace(pushover)
	d, err := Parse("pushover.yaml", []byte(plain))
	if err != nil || d.Notify.Text != "text" || d.Notify.Title != "" || d.Notify.Link != "" {
		t.Fatalf("a notify with a text alone: %+v, %v", d, err)
	}
}

// A body that is one optional parameter and nothing else has nothing to
// leave it out of: not given, it was sent as null (found reviewing
// yardr-26a0).
func TestParseRefusesABodyThatIsOneOptionalParameter(t *testing.T) {
	const file = `name: x
base: https://x.example.com
calls:
  put:
    kind: update
    params: { v: { type: string%s } }
    request: { method: POST, path: /a, body: "{v}" }
`
	_, err := Parse("x.yaml", []byte(strings.Replace(file, "%s", ", optional: true", 1)))
	if err == nil || !strings.Contains(err.Error(), "x.yaml:7: calls.put.request.body: {v} is optional, and it is the whole body") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse("x.yaml", []byte(strings.Replace(file, "%s", "", 1))); err != nil {
		t.Fatalf("a required parameter as the whole body: %v", err)
	}
}

// aiquokka is the file of the design the limits key was built for
// (yardr-m71s), whole: a source that is its limits command and has no call.
const aiquokka = `name: aiquokka
about: Usage limits of the subscriptions the agents' CLIs are logged in to (github.com/McKean/aiquokka).
limits:
  command: [/Users/mckean/go/bin/aiquokka, "{provider}", --json]
  kinds:              # harness kind -> the provider's own name for it
    claude: claude
    codex: codex
    kimi: kimi
    grok: grok
    copilot: copilot
    kiro: kiro
`

func TestParseLimits(t *testing.T) {
	d, err := Parse("aiquokka.yaml", []byte(aiquokka))
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "aiquokka" || d.Base != "" || len(d.Calls) != 0 || d.Credential != nil || d.Notify != nil || d.Limits == nil {
		t.Fatalf("declaration = %+v", d)
	}
	if got := strings.Join(d.Limits.KindNames(), " "); got != "claude codex copilot grok kimi kiro" {
		t.Fatalf("kinds = %q", got)
	}
	argv, ok := d.Limits.Argv("kimi")
	if !ok || strings.Join(argv, " ") != "/Users/mckean/go/bin/aiquokka kimi --json" {
		t.Fatalf("Argv(kimi) = %q, %v", argv, ok)
	}
	if _, ok := d.Limits.Argv("gemini"); ok {
		t.Fatal("Argv of a kind the file does not list")
	}
	if argv = d.Limits.ArgvName("zai"); strings.Join(argv, " ") != "/Users/mckean/go/bin/aiquokka zai --json" {
		t.Fatalf("ArgvName(zai) = %q", argv)
	}
	// The provider's name is its own: it need not be the kind's.
	other := strings.Replace(aiquokka, "kimi: kimi", "kimi: moonshot-ai", 1)
	other = strings.Replace(other, `"{provider}", --json`, `"--provider={provider}", "--as={provider}.json"`, 1)
	if d, err = Parse("f", []byte(other)); err != nil {
		t.Fatal(err)
	}
	if argv, _ = d.Limits.Argv("kimi"); strings.Join(argv, " ") != "/Users/mckean/go/bin/aiquokka --provider=moonshot-ai --as=moonshot-ai.json" {
		t.Fatalf("Argv = %q", argv)
	}
	// With calls it is a source as before, which says limits too.
	both := pushover + "limits:\n  command: [aiquokka, \"{provider}\"]\n  kinds: { kimi: kimi }\n"
	if d, err = Parse("f", []byte(both)); err != nil || d.Limits == nil || len(d.Calls) != 1 || d.Notify == nil {
		t.Fatalf("calls and limits = %+v, %v", d, err)
	}
	// With no kinds it answers for itself: its command takes no
	// {provider} and is asked by the source's name.
	self := "name: machine\nlimits:\n  command: [machine/limits, --target, load=80]\n"
	if d, err = Parse("f", []byte(self)); err != nil || d.Limits == nil || !d.Limits.Self() || len(d.Limits.Kinds) != 0 {
		t.Fatalf("a limits key with no kinds = %+v, %v", d, err)
	} else if got := strings.Join(d.Limits.Command, " "); got != "machine/limits --target load=80" {
		t.Fatalf("command = %q", got)
	}
}

func TestParseLimitsRefusals(t *testing.T) {
	limits := func(body string) string { return "name: q\nlimits:\n" + body }
	cmd := "  command: [q, \"{provider}\"]\n"
	kinds := "  kinds: { kimi: kimi }\n"
	for name, tc := range map[string]struct{ text, want string }{
		"a command that is a string":      {limits("  command: q {provider} --json\n" + kinds), `f:3: limits.command: want a list of strings ([program, "{provider}", --json]), got "q {provider} --json"`},
		"a number among the words":        {limits("  command: [q, \"{provider}\", 5]\n" + kinds), "f:3: limits.command: want a string, got int 5"},
		"an empty command":                {limits("  command: []\n" + kinds), "limits.command: an empty list"},
		"no place for the provider":       {limits("  command: [q, --json]\n" + kinds), "f:3: limits.command: no {provider} among the arguments"},
		"the provider as the program":     {limits("  command: [\"/bin/{provider}\", \"{provider}\"]\n" + kinds), "limits.command: {provider} in the program"},
		"a kind herdr does not start":     {limits(cmd + "  kinds: { kimi: kimi, chatgpt: codex }\n"), "f:4: limits.kinds.chatgpt: chatgpt is no harness kind herdr starts (it starts: pi, claude,"},
		"a provider's name like a flag":   {limits(cmd + "  kinds: { kimi: --all }\n"), `limits.kinds.kimi: "--all" is no name of a provider`},
		"a provider's name that is a map": {limits(cmd + "  kinds: { kimi: { name: kimi } }\n"), "limits.kinds.kimi: want a string, got a mapping"},
		"a mark with no kinds":            {limits(cmd), "limits.kinds: missing: the harness kinds the source answers for"},
		"kinds that are empty":            {limits(cmd + "  kinds: {}\n"), "limits.kinds: no kind"},
		"no command":                      {limits(kinds), "limits.command: missing: the program that says the limits"},
		"a program that leaves the file":  {limits("  command: [../q, \"{provider}\"]\n" + kinds), `limits.command: the program "../q" has a '.' or '..' segment`},
		"a key it does not have":          {limits(cmd + kinds + "  timeout: 5\n"), "limits.timeout: unknown key (known here: command, kinds)"},
		"a base and no calls":             {"name: q\nbase: https://q.example.com\n" + limits(cmd + kinds)[len("name: q\n"):], "f:2: base: is for the calls of a source, and this file declares none"},
		"a credential and no calls":       {"name: q\ncredential: { env: Q }\n" + limits(cmd + kinds)[len("name: q\n"):], "f:2: credential: is for the calls of a source"},
		"neither calls nor limits":        {"name: q\n", "calls: missing: a source is the calls it declares, or its limits command"},
		"calls, limits and no base":       {strings.Replace(pushover, "base: https://api.pushover.net/1\n", "", 1) + "limits:\n" + cmd + kinds, "base: missing: the address every call's path is under"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// One mistake is one line: a file that is its limits alone is not told
	// of a base it has no use for.
	_, err := Parse("f", []byte(limits("  command: [q]\n"+kinds)))
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want one", err)
	}
}

// A parameter marked log: true is one whose value the record of a call
// keeps; unmarked is not, and the key takes true or false alone.
func TestParseLoggedParameter(t *testing.T) {
	marked := strings.Replace(crm, `id: { type: string, pattern: "^[0-9]{1,12}$" }`, `id: { type: string, pattern: "^[0-9]{1,12}$", log: true }`, 1)
	if marked == crm {
		t.Fatal("the file has no id parameter to mark")
	}
	d, err := Parse("crm.yaml", []byte(marked))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := d.Call("customer")
	if p, _ := c.param("id"); p == nil || !p.Log {
		t.Fatalf("id = %+v", p)
	}
	if n, _ := d.Call("note"); n.Params[0].Log || n.Params[1].Log {
		t.Fatalf("an unmarked parameter is logged: %+v", n.Params)
	}
	_, err = Parse("crm.yaml", []byte(strings.Replace(marked, "log: true", "log: yes", 1)))
	if err == nil || !strings.Contains(err.Error(), "calls.customer.params.id.log: want true or false") {
		t.Fatalf("log: yes = %v", err)
	}
}

// keyed is crm with a second credential for its updates and the header its
// service takes an update's key in.
var keyed = strings.NewReplacer(
	"  env: CRM_TOKEN ", "  env: CRM_TOKEN\n  update_env: CRM_WRITE_TOKEN ",
	"check: ping ", "idempotency: Idempotency-Key\ncheck: ping ",
).Replace(crm)

// A file names a second variable for its updates and the header an update's
// key goes in. What is refused is what would say two credentials where
// there is one, send the key over another header, or send it with nothing.
func TestParseUpdateCredentialAndKey(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(keyed))
	if err != nil {
		t.Fatal(err)
	}
	c := d.Credential
	if c.EnvOf(Read) != "CRM_TOKEN" || c.EnvOf(Update) != "CRM_WRITE_TOKEN" || strings.Join(c.Envs(), ",") != "CRM_TOKEN,CRM_WRITE_TOKEN" || d.Idempotency != "Idempotency-Key" {
		t.Fatalf("credential = %+v, idempotency %q", c, d.Idempotency)
	}
	one, err := Parse("crm.yaml", []byte(crm))
	if err != nil || one.Credential.EnvOf(Update) != "CRM_TOKEN" || len(one.Credential.Envs()) != 1 || one.Idempotency != "" {
		t.Fatalf("a file with one credential = %+v, %v", one, err)
	}
	readsOnly := keyed[:strings.Index(keyed, "  note:")]
	for name, tc := range map[string]struct{ text, want string }{
		"the same variable twice":   {strings.Replace(keyed, "CRM_WRITE_TOKEN", "CRM_TOKEN", 1), "credential.update_env: CRM_TOKEN is env already"},
		"a value for a name":        {strings.Replace(keyed, "CRM_WRITE_TOKEN", `"s3cr3t value"`, 1), `credential.update_env: "s3cr3t value" is no name of a variable`},
		"the credential's header":   {strings.Replace(keyed, "Idempotency-Key", "authorization", 1), "idempotency: authorization is the header the credential is sent in"},
		"a header yardr sets":       {strings.Replace(keyed, "Idempotency-Key", "Content-Type", 1), "idempotency: Content-Type is a header every call is sent with already"},
		"no name of a header":       {strings.Replace(keyed, "Idempotency-Key", `"Key: {id}"`, 1), `idempotency: "Key: {id}" is no name of a header`},
		"a file without an update":  {readsOnly, "idempotency: the file has no update call"},
		"a file that is its limits": {"name: q\nidempotency: Idempotency-Key\nlimits:\n  command: [q, \"{provider}\"]\n  kinds: { claude: claude }\n", "idempotency: is for the calls of a source"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// minted is crm with a credential that may be minted: the two names that
// together select it beside the variable the minted token is handed under.
var minted = strings.Replace(crm,
	"  env: CRM_TOKEN                                  # the name, never the value\n",
	"  env: CRM_TOKEN                                  # the name, never the value\n  mint: [CRM_APP_ID, CRM_APP_KEY]\n", 1)

// A file names the variables that together select a minted credential:
// names only, beside env, which the minted token is handed under. What is
// refused is no name, a name twice, and one the credential already goes by.
func TestParseCredentialMint(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(minted))
	if err != nil {
		t.Fatal(err)
	}
	c := d.Credential
	if !c.Minted() || strings.Join(c.Mint, ",") != "CRM_APP_ID,CRM_APP_KEY" || c.Env != "CRM_TOKEN" {
		t.Fatalf("credential = %+v", c)
	}
	if got := c.Names(); got != "CRM_APP_ID and CRM_APP_KEY, or CRM_TOKEN" {
		t.Fatalf("names = %q", got)
	}
	one, err := Parse("crm.yaml", []byte(crm))
	if err != nil || one.Credential.Minted() || one.Credential.Names() != "CRM_TOKEN" {
		t.Fatalf("a file with one credential = %+v, %v", one.Credential, err)
	}
	two, err := Parse("crm.yaml", []byte(keyed))
	if err != nil || two.Credential.Names() != "CRM_TOKEN and CRM_WRITE_TOKEN" {
		t.Fatalf("a file with two = %+v, %v", two.Credential, err)
	}
	if ((*Credential)(nil)).Minted() {
		t.Fatal("a file with no credential mints")
	}
	for name, tc := range map[string]struct{ text, want string }{
		"no name":            {strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: []", 1), "credential.mint: missing: the names of the variables"},
		"no list":            {strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: CRM_APP_ID", 1), "credential.mint: want a list ([a, b])"},
		"a value for a name": {strings.Replace(minted, "CRM_APP_KEY]", `"s3cr3t value"]`, 1), `credential.mint: "s3cr3t value" is no name of a variable`},
		"a name twice":       {strings.Replace(minted, "CRM_APP_KEY", "CRM_APP_ID", 1), "credential.mint: CRM_APP_ID is listed twice"},
		"env again":          {strings.Replace(minted, "CRM_APP_KEY", "CRM_TOKEN", 1), "credential.mint: CRM_TOKEN is env already"},
		"update_env again":   {strings.Replace(keyed, "  update_env: CRM_WRITE_TOKEN ", "  update_env: CRM_WRITE_TOKEN\n  mint: [CRM_APP_ID, CRM_WRITE_TOKEN] ", 1), "credential.mint: CRM_WRITE_TOKEN is update_env already"},
		"env still required": {strings.Replace(minted, "  env: CRM_TOKEN                                  # the name, never the value\n", "", 1), "credential.env: missing: the name of the variable the credential is in"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// A mint input the file marks with secret false is no secret: an account's
// address, asked in the clear. A secret ask for such names alone is
// unconcealed; a mixed or secret one is concealed, and a static token
// always is.
func TestParseCredentialMintSecretFalse(t *testing.T) {
	text := strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]",
		"mint:\n    - CRM_APP_ID\n    - { name: CRM_APP_KEY, secret: false }", 1)
	d, err := Parse("crm.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	c := d.Credential
	if strings.Join(c.Mint, ",") != "CRM_APP_ID,CRM_APP_KEY" || strings.Join(c.NotSecret, ",") != "CRM_APP_KEY" {
		t.Fatalf("credential = %+v", c)
	}
	for _, tc := range []struct {
		names []string
		want  bool
	}{
		{[]string{"CRM_APP_KEY"}, false},
		{[]string{"CRM_APP_ID"}, true},
		{[]string{"CRM_APP_ID", "CRM_APP_KEY"}, true},
		{[]string{"CRM_TOKEN"}, true},
		{nil, true},
	} {
		if got := c.Concealed(tc.names); got != tc.want {
			t.Errorf("Concealed(%q) = %v, want %v", tc.names, got, tc.want)
		}
	}
	if !((*Credential)(nil)).Concealed([]string{"CRM_APP_KEY"}) {
		t.Fatal("a file with no credential unconceals")
	}
	// The block form the google packs use reads the same.
	blocked := strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]",
		"mint:\n    - name: CRM_APP_KEY\n      secret: false", 1)
	blocked = strings.Replace(blocked, "mint:\n    - name: CRM_APP_KEY", "mint:\n    - CRM_APP_ID\n    - name: CRM_APP_KEY", 1)
	bd, err := Parse("crm.yaml", []byte(blocked))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(bd.Credential.Mint, ",") != "CRM_APP_ID,CRM_APP_KEY" || strings.Join(bd.Credential.NotSecret, ",") != "CRM_APP_KEY" {
		t.Fatalf("the block form reads as %+v", bd.Credential)
	}
	for name, tc := range map[string]struct{ text, want string }{
		"no name": {
			strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: [{ secret: false }]", 1),
			"credential.mint: missing: the name the mint's input is under",
		},
		"secret no boolean": {
			strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: [{ name: CRM_APP_KEY, secret: sometimes }]", 1),
			"want true or false",
		},
		"unknown key": {
			strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: [{ name: CRM_APP_KEY, secret: false, extra: 1 }]", 1),
			"unknown key (known here: name, secret)",
		},
		"a name twice across forms": {
			strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", "mint: [CRM_APP_ID, { name: CRM_APP_ID, secret: false }]", 1),
			"credential.mint: CRM_APP_ID is listed twice",
		},
		"a value for a name": {
			strings.Replace(minted, "mint: [CRM_APP_ID, CRM_APP_KEY]", `mint: [{ name: "s3cr3t value", secret: false }]`, 1),
			`credential.mint: "s3cr3t value" is no name of a variable`,
		},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// linearish is the declaration of the sketch (yardr-embp), with the name a
// built-in source holds replaced: a command per thing the yard does in a
// thread, the word its place is called, the states it has a status for, and
// its issues as a bead's link.
const linearish = `name: lin
about: Linear issues and comments.
base: https://api.linear.app
credential:
  env: LINEAR_API_KEY
thread:
  reply:  [linear/thread, reply]      # in  {thread, parent, body, key}      out {id, url}
  posted: [linear/thread, posted]     # in  {thread, key}                    out {id, url} or {}
  read:   [linear/thread, read]       # in  {thread}                         out {messages: [...]}
  place:  team
  open:   [linear/thread, open]       # in  {in, title, body, key}           out {id, title, url}
  opened: [linear/thread, opened]     # in  {in, key}                        out {id, title, url} or {}
  state:  [linear/thread, state]      # in  {thread, state, forward?}        out {status, moved, was?}
  states: [started, review, done]
link:
  host: linear.app
  issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'
  fixed: "https://linear.app/{1}/issue/{2}"
  thread: "{2}"
calls:
  viewer:
    kind: read
    request: { method: POST, path: /graphql, body: { query: "{ viewer { id name } }" } }
check: viewer
`

func TestParseThreadAndLink(t *testing.T) {
	d, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	th := d.Thread
	if th == nil || d.Link == nil {
		t.Fatalf("declaration = %+v", d)
	}
	if strings.Join(th.Reply, " ") != "linear/thread reply" || strings.Join(th.Posted, " ") != "linear/thread posted" ||
		strings.Join(th.Read, " ") != "linear/thread read" || strings.Join(th.Open, " ") != "linear/thread open" ||
		strings.Join(th.Opened, " ") != "linear/thread opened" || strings.Join(th.State, " ") != "linear/thread state" ||
		th.Place != "team" || strings.Join(th.States, " ") != "started review done" {
		t.Fatalf("thread = %+v", th)
	}
	l := d.Link
	if l.Host != "linear.app" || l.Issue != `^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)` ||
		l.Fixed != "https://linear.app/{1}/issue/{2}" || l.Thread != "{2}" || l.re == nil || l.re.NumSubexp() != 2 {
		t.Fatalf("link = %+v", l)
	}
	keys := []string{}
	for _, c := range d.Commands() {
		keys = append(keys, c.Key)
	}
	if strings.Join(keys, " ") != "thread.reply thread.posted thread.read thread.open thread.opened thread.state" {
		t.Fatalf("commands = %v", keys)
	}
}

// Labels says the source takes free-form tags: thread.state then takes
// label:<name> beside the states. Absent, the yard sets no label there,
// as before.
func TestParseThreadLabels(t *testing.T) {
	d, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	if d.Thread.Labels {
		t.Fatalf("thread = %+v, want no labels", d.Thread)
	}
	with := strings.Replace(linearish, "  states: [started, review, done]\n", "  states: [started, review, done]\n  labels: true\n", 1)
	d, err = Parse("lin.yaml", []byte(with))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Thread.Labels {
		t.Fatalf("thread = %+v, want labels", d.Thread)
	}
}

// Fixed and ThreadOf expand the link's templates over the groups its
// expression reads from a URL's path: the issue's one fixed form, and the
// id a thread command takes for it. A link read back from the store has no
// compiled expression and answers the same.
func TestLinkFixedAndThread(t *testing.T) {
	d, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d.Link)
	var back Link
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*Link{d.Link, &back} {
		fixed, ok := l.FixedForm("/acme/issue/YRD-12/web-view-shows-links")
		if !ok || fixed != "https://linear.app/acme/issue/YRD-12" {
			t.Fatalf("fixed = %q, %v", fixed, ok)
		}
		thread, ok := l.ThreadOf("/acme/issue/YRD-12")
		if !ok || thread != "YRD-12" {
			t.Fatalf("thread = %q, %v", thread, ok)
		}
		if _, ok := l.FixedForm("/acme/project/12"); ok {
			t.Fatal("a project has a fixed form")
		}
		if _, ok := l.ThreadOf("/acme/project/12"); ok {
			t.Fatal("a project has a thread")
		}
	}
}

// Identity says how a crew member's keys in the source fold, and link's
// fold lowercases the groups before the fixed and thread templates expand:
// one spelling names the issue however it was typed. A link read back from
// the store folds the same.
func TestParseFold(t *testing.T) {
	text := "name: q\nbase: https://q.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"identity:\n  fold: lower\n" +
		"link:\n  host: q.example.com\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n" +
		"  fixed: \"https://q.example.com/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n  fold: lower\n"
	d, err := Parse("q.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if d.Identity == nil || d.Identity.Fold != FoldLower {
		t.Fatalf("identity = %+v", d.Identity)
	}
	raw, _ := json.Marshal(d.Link)
	var back Link
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*Link{d.Link, &back} {
		fixed, ok := l.FixedForm("/Acme/WebApp/issues/7")
		if !ok || fixed != "https://q.example.com/acme/webapp/issues/7" {
			t.Fatalf("fixed = %q, %v", fixed, ok)
		}
		thread, ok := l.ThreadOf("/Acme/WebApp/issues/7")
		if !ok || thread != "acme/webapp#7" {
			t.Fatalf("thread = %q, %v", thread, ok)
		}
	}
	plain, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Identity != nil || plain.Link.Fold != "" {
		t.Fatalf("a file without fold = %+v, %+v", plain.Identity, plain.Link)
	}
	if fixed, _ := plain.Link.FixedForm("/Acme/issue/YRD-12"); fixed != "https://linear.app/Acme/issue/YRD-12" {
		t.Fatalf("without fold, fixed = %q", fixed)
	}
}

// A link with a pull shape reads pull requests as its second thread shape:
// their fixed form and thread, the other shape's form over the same
// groups, the repository and number back out of a pull request's groups,
// and its URL back from the parts. repos matches a repository's path.
// A link read back from the store answers the same.
func TestParseLinkPullAndRepos(t *testing.T) {
	text := "name: forge\nbase: https://forge.example\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"link:\n  host: forge.example\n  issue: '^/([^/]+)/([^/]+)/issues/([0-9]+)'\n" +
		"  fixed: \"https://forge.example/{1}/{2}/issues/{3}\"\n  thread: \"{1}/{2}#{3}\"\n  fold: lower\n" +
		"  pull:\n    issue: '^/([^/]+)/([^/]+)/pull/([0-9]+)'\n" +
		"    fixed: \"https://forge.example/{1}/{2}/pull/{3}\"\n    thread: \"{1}/{2}#{3}\"\n" +
		"  repos: '[^/]+/[^/]+'\n"
	d, err := Parse("forge.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if d.Link.Pull == nil || d.Link.Repos != "[^/]+/[^/]+" || d.Link.pullRe == nil || d.Link.pullRe.NumSubexp() != 3 {
		t.Fatalf("link = %+v", d.Link)
	}
	raw, _ := json.Marshal(d.Link)
	var back Link
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*Link{d.Link, &back} {
		if fixed, ok := l.PullFixedForm("/Acme/WebApp/pull/7"); !ok || fixed != "https://forge.example/acme/webapp/pull/7" {
			t.Fatalf("pull fixed = %q, %v", fixed, ok)
		}
		if thread, ok := l.PullThreadOf("/Acme/WebApp/pull/7"); !ok || thread != "acme/webapp#7" {
			t.Fatalf("pull thread = %q, %v", thread, ok)
		}
		if alias, ok := l.AliasOf("/acme/webapp/issues/7"); !ok || alias != "https://forge.example/acme/webapp/pull/7" {
			t.Fatalf("alias of the issue = %q, %v", alias, ok)
		}
		if alias, ok := l.AliasOf("/Acme/WebApp/pull/7"); !ok || alias != "https://forge.example/acme/webapp/issues/7" {
			t.Fatalf("alias of the pull request = %q, %v", alias, ok)
		}
		if repo, number, ok := l.PullParts("/Acme/WebApp/pull/7"); !ok || repo != "acme/webapp" || number != "7" {
			t.Fatalf("pull parts = %q, %q, %v", repo, number, ok)
		}
		if url, ok := l.PullURL("acme/webapp", "7"); !ok || url != "https://forge.example/acme/webapp/pull/7" {
			t.Fatalf("pull URL = %q, %v", url, ok)
		}
		if repo, ok := l.RepoOf("acme/webapp"); !ok || repo != "acme/webapp" {
			t.Fatalf("repo = %q, %v", repo, ok)
		}
		if repo, ok := l.RepoOf("Acme/WebApp"); !ok || repo != "acme/webapp" {
			t.Fatalf("a repo in another case = %q, %v", repo, ok)
		}
		for _, path := range []string{"/acme/webapp/discussions/7", "/acme/webapp/issues/7"} {
			if _, ok := l.PullFixedForm(path); ok {
				t.Fatalf("PullFixedForm(%q) is a pull request", path)
			}
			if _, _, ok := l.PullParts(path); ok {
				t.Fatalf("PullParts(%q) has parts", path)
			}
		}
		if _, ok := l.AliasOf("/acme/webapp/discussions/7"); ok {
			t.Fatal("a discussion has an alias")
		}
		if _, ok := l.PullURL("acme/webapp/extra", "7"); ok {
			t.Fatal("a repository the pattern has no groups for has a URL")
		}
		if _, ok := l.PullURL("", "7"); ok {
			t.Fatal("no repository has a URL")
		}
		if _, ok := l.RepoOf("acme/webapp/extra"); ok {
			t.Fatal("a path past the pattern is a repository")
		}
		if _, ok := l.RepoOf("acme"); ok {
			t.Fatal("half a path is a repository")
		}
	}
	plain, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Link.Pull != nil || plain.Link.Repos != "" {
		t.Fatalf("a file with one shape = %+v", plain.Link)
	}
	if _, ok := plain.Link.PullFixedForm("/acme/issue/YRD-12"); ok {
		t.Fatal("one shape has a pull request")
	}
	if _, ok := plain.Link.AliasOf("/acme/issue/YRD-12"); ok {
		t.Fatal("one shape has an alias")
	}
	if _, ok := plain.Link.PullURL("acme", "YRD-12"); ok {
		t.Fatal("one shape builds a pull request URL")
	}
	if _, ok := plain.Link.RepoOf("acme"); ok {
		t.Fatal("no repos pattern matches a repository")
	}
}

// What is refused of the thread and link keys is what would fail at every
// run, as limits.command is refused where the same reason holds.
func TestParseThreadLinkRefusals(t *testing.T) {
	src := func(body string) string {
		return "name: q\nbase: https://q.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" + body
	}
	link := "link:\n  host: linear.app\n  issue: '^/([^/]+)/issue/([A-Z]+-[0-9]+)'\n  fixed: \"https://linear.app/{1}/issue/{2}\"\n  thread: \"{2}\"\n"
	for name, tc := range map[string]struct{ text, want string }{
		"a command that is a string":     {src("thread:\n  reply: linear/thread reply\n"), `f:6: thread.reply: want a list of strings ([program, linear/thread, reply]), got "linear/thread reply"`},
		"a number among the words":       {src("thread:\n  reply: [linear/thread, 5]\n"), "thread.reply: want a string, got int 5"},
		"an empty command":               {src("thread:\n  reply: []\n"), "thread.reply: an empty list"},
		"a program that leaves the file": {src("thread:\n  reply: [../thread, reply]\n"), `thread.reply: the program "../thread" has a '.' or '..' segment`},
		"open without place":             {src("thread:\n  open: [linear/thread, open]\n"), "thread.place: missing: what a place to open a thread in is called"},
		"state without states":           {src("thread:\n  state: [linear/thread, state]\n"), "thread.states: missing: the words thread.state takes, of started, review, done"},
		"state with empty states":        {src("thread:\n  state: [linear/thread, state]\n  states: []\n"), "thread.states: missing"},
		"a state the yard has not":       {src("thread:\n  state: [linear/thread, state]\n  states: [started, blocked]\n"), "thread.states: blocked is no state of a thread (started, review, done)"},
		"a place of two words":           {src("thread:\n  open: [linear/thread, open]\n  place: the team\n"), `thread.place: "the team": one word`},
		"a key it does not have":         {src("thread:\n  wait: [linear/thread, wait]\n"), "thread.wait: unknown key (known here: reply, posted, read, file, open, opened, state, place, states, labels, assignee)"},
		"an assignee that is no part":    {src("thread:\n  place: team\n  open: [linear/thread, open]\n  assignee: email\n"), "thread.assignee: \"email\": id or key: which part of a person's identity in the source thread.open takes as assignee"},
		"an assignee with no open":       {src("thread:\n  assignee: id\n"), "thread.assignee: thread.open is missing: the assignee is whom the thread it opens is assigned to"},
		"labels that are no boolean":     {src("thread:\n  state: [linear/thread, state]\n  states: [started, done]\n  labels: sometimes\n"), "thread.labels: want true or false, got \"sometimes\""},
		"an expression that is none":     {src("link:\n  host: linear.app\n  issue: '^/([('\n  fixed: x\n  thread: y\n"), "link.issue: error parsing regexp"},
		"a group the expression has not": {src(strings.Replace(link, `thread: "{2}"`, `thread: "{3}"`, 1)), "link.thread: {3} names a group link.issue has not (it has 2)"},
		"a group that is none":           {src(strings.Replace(link, `thread: "{2}"`, `thread: "{two}"`, 1)), "link.thread: {two} is no group of a template"},
		"a fold that is none":            {src(strings.Replace(link, `thread: "{2}"`, "thread: \"{2}\"\n  fold: upper", 1)), `link.fold: "upper": want lower`},
		"an identity fold that is none":  {src("identity:\n  fold: upper\n"), `identity.fold: "upper": want lower`},
		"an identity with no fold":       {src("identity: {}\n"), "identity.fold: missing: lower"},
		"a key identity does not have":   {src("identity:\n  key: id\n"), "identity.key: unknown key (known here: fold)"},
		"no host":                        {src("link:\n  issue: '^/x'\n  fixed: y\n  thread: z\n"), "link.host: missing: the host the source's issue URLs are on"},
		"a host that is none":            {src(strings.Replace(link, "host: linear.app", "host: https://linear.app", 1)), `link.host: "https://linear.app" is no host`},
		"no issue":                       {src("link:\n  host: linear.app\n  fixed: y\n  thread: z\n"), "link.issue: missing"},
		"no fixed":                       {src("link:\n  host: linear.app\n  issue: '^/x'\n  thread: z\n"), "link.fixed: missing"},
		"no thread":                      {src("link:\n  host: linear.app\n  issue: '^/x'\n  fixed: y\n"), "link.thread: missing"},
		"a pull over other groups":       {src(link + "  pull:\n    issue: '^/([^/]+)/pull/([0-9]+)/([^/]+)'\n    fixed: x\n    thread: y\n"), "link.pull.issue: has 3 groups, want the 2 of link.issue, in the same order"},
		"a pull without repo and number": {src("link:\n  host: linear.app\n  issue: '^/issue/([A-Z]+-[0-9]+)'\n  fixed: \"https://linear.app/issue/{1}\"\n  thread: \"{1}\"\n  pull:\n    issue: '^/pull/([0-9]+)'\n    fixed: \"https://linear.app/pull/{1}\"\n    thread: \"{1}\"\n"), "link.pull.issue: has 1 groups, want at least 2"},
		"a pull pattern that is none":    {src(link + "  pull:\n    issue: '^/([('\n    fixed: x\n    thread: y\n"), "link.pull.issue: error parsing regexp"},
		"a pull group it has not":        {src(link + "  pull:\n    issue: '^/([^/]+)/pull/([0-9]+)'\n    fixed: x\n    thread: \"{3}\"\n"), "link.pull.thread: {3} names a group link.pull.issue has not (it has 2)"},
		"a pull with no fixed":           {src(link + "  pull:\n    issue: '^/([^/]+)/pull/([0-9]+)'\n    thread: y\n"), "link.pull.fixed: missing: the pull request's one fixed form"},
		"a pull with no thread":          {src(link + "  pull:\n    issue: '^/([^/]+)/pull/([0-9]+)'\n    fixed: x\n"), "link.pull.thread: missing: the id a thread command takes for the pull request"},
		"a key pull does not have":       {src(link + "  pull:\n    issue: '^/([^/]+)/pull/([0-9]+)'\n    fixed: x\n    thread: y\n    base: main\n"), "link.pull.base: unknown key (known here: issue, fixed, thread)"},
		"a repos pattern that is none":   {src(link + "  repos: '^/([('\n"), "link.repos: error parsing regexp"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// One mistake is one line: a state the yard has not is not also told to
	// be missing.
	_, err := Parse("f", []byte(src("thread:\n  state: [linear/thread, state]\n  states: [blocked]\n")))
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want one", err)
	}
}

// hookish is what the sketch (yardr-x0bh) has Linear send in: how the core
// verifies a delivery, how far a body identifies one, the command that
// turns a verified body into the envelope, and the keys its events' fields
// may carry.
const hookish = `name: lin
base: https://api.linear.app
credential:
  env: LINEAR_API_KEY
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery:  { header: Linear-Delivery }
    timestamp: { body: webhookTimestamp, unit: ms, window: 1m }
  bodies: unique
  normalize: [linear/normalize]
  keys: [team, project, label]
calls:
  viewer:
    kind: read
    request: { method: POST, path: /graphql, body: { query: "{ viewer { id name } }" } }
check: viewer
`

func TestParseHook(t *testing.T) {
	d, err := Parse("lin.yaml", []byte(hookish))
	if err != nil {
		t.Fatal(err)
	}
	h := d.Hook
	if h == nil {
		t.Fatalf("declaration = %+v", d)
	}
	v := h.Verify
	if v.Scheme != "hmac-sha256" || v.Signed != "body" ||
		v.Signature.Header != "Linear-Signature" || v.Signature.Encoding != "hex" || v.Signature.Prefix != "" ||
		v.Delivery.Header != "Linear-Delivery" {
		t.Fatalf("verify = %+v", v)
	}
	if v.Timestamp == nil || v.Timestamp.Body != "webhookTimestamp" || v.Timestamp.Header != "" ||
		v.Timestamp.Unit != "ms" || v.Timestamp.Window != "1m" {
		t.Fatalf("timestamp = %+v", v.Timestamp)
	}
	if h.Bodies != "unique" || strings.Join(h.Normalize, " ") != "linear/normalize" ||
		strings.Join(h.Keys, " ") != "team project label" {
		t.Fatalf("hook = %+v", h)
	}
	keys := []string{}
	for _, c := range d.Commands() {
		keys = append(keys, c.Key)
	}
	if strings.Join(keys, " ") != "hook.normalize" {
		t.Fatalf("commands = %v", keys)
	}
}

// A signature over timestamp and body together is a template over the
// two, with the service's literal text around them; signed: body is the
// body alone said in one word, and a challenge names the handshake a
// verified body is answered with.
func TestParseHookSignedTemplateAndChallenge(t *testing.T) {
	d, err := Parse("slack.yaml", []byte(`name: slackish
hook:
  verify:
    scheme: hmac-sha256
    signed: "v0:{timestamp}:{body}"
    signature: { header: X-Slack-Signature, encoding: hex, prefix: "v0=" }
    delivery:  { body: event_id }
    timestamp: { header: X-Slack-Request-Timestamp, unit: s, window: 5m }
  bodies: repeat
  challenge: { body: type, value: url_verification, answer: challenge }
  normalize: [slack/normalize]
  keys: [channel, team, sender, room]
`))
	if err != nil {
		t.Fatal(err)
	}
	v := d.Hook.Verify
	if v.Signed != "v0:{timestamp}:{body}" || v.Signature.Header != "X-Slack-Signature" ||
		v.Signature.Prefix != "v0=" || v.Delivery.Body != "event_id" ||
		v.Timestamp == nil || v.Timestamp.Header != "X-Slack-Request-Timestamp" ||
		v.Timestamp.Unit != "s" || v.Timestamp.Window != "5m" {
		t.Fatalf("verify = %+v", v)
	}
	if c := d.Hook.Challenge; c == nil || c.Body != "type" || c.Value != "url_verification" || c.Answer != "challenge" {
		t.Fatalf("challenge = %+v", d.Hook.Challenge)
	}
	for _, signed := range []string{"body"} {
		text := "name: q\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: " +
			fmt.Sprintf("%q", signed) + "\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { header: Q-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"
		d, err := Parse("q.yaml", []byte(text))
		if err != nil {
			t.Fatalf("signed %q: %v", signed, err)
		}
		if d.Hook.Verify.Signed != signed {
			t.Fatalf("signed %q parsed as %q", signed, d.Hook.Verify.Signed)
		}
	}
}

// A hook verified by the secret itself in a header: signed, encoding and
// prefix do not apply, delivery is optional, and where the service sends
// its id of the delivery in the body, the declaration names the dot path.
func TestParseHookSecret(t *testing.T) {
	for name, tc := range map[string]struct {
		verify         string
		bodies         string
		scheme         string
		signed         string
		header         string
		encoding       string
		deliveryHeader string
		deliveryBody   string
	}{
		"no delivery": {
			"    scheme: secret\n    signature: { header: Q-Secret }\n",
			"unique", "secret", "", "Q-Secret", "", "", "",
		},
		"a delivery in a header": {
			"    scheme: secret\n    signature: { header: Q-Secret }\n    delivery: { header: Q-Delivery }\n",
			"repeat", "secret", "", "Q-Secret", "", "Q-Delivery", "",
		},
		"a delivery in the body": {
			"    scheme: secret\n    signature: { header: Q-Secret }\n    delivery: { body: update_id }\n",
			"repeat", "secret", "", "Q-Secret", "", "", "update_id",
		},
		"a delivery in the body with hmac": {
			"    scheme: hmac-sha256\n    signed: body\n    signature: { header: Q-Sig, encoding: hex }\n    delivery: { body: meta.id }\n",
			"repeat", "hmac-sha256", "body", "Q-Sig", "hex", "", "meta.id",
		},
	} {
		d, err := Parse("q.yaml", []byte("name: q\nhook:\n  verify:\n"+tc.verify+
			"  bodies: "+tc.bodies+"\n  normalize: [normalize]\n"))
		if err != nil {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		v := d.Hook.Verify
		if v.Scheme != tc.scheme || v.Signed != tc.signed ||
			v.Signature.Header != tc.header || v.Signature.Encoding != tc.encoding || v.Signature.Prefix != "" ||
			v.Delivery.Header != tc.deliveryHeader || v.Delivery.Body != tc.deliveryBody {
			t.Errorf("%s: verify = %+v", name, v)
		}
	}
}

// Bodies scoped per hook: one event's body goes to every hook that asked
// for it, and each hook drops only its own repeats.
func TestParseBodiesPerHook(t *testing.T) {
	text := strings.Replace(hookish, "bodies: unique", "bodies: unique-per-hook", 1)
	d, err := Parse("q.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if d.Hook == nil || d.Hook.Bodies != BodiesUniquePerHook {
		t.Fatalf("hook = %+v", d.Hook)
	}
}

// A hook whose deliveries follow events, not a rhythm, says untimed: the
// quiet check never times its silence. A source that hears a person's
// chat back is untimed without the key, and one that says neither is
// timed.
func TestParseHookUntimed(t *testing.T) {
	base := pushover + "hook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: P-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [chat]\n"
	for name, tc := range map[string]struct {
		text    string
		key     bool // the untimed key, as read
		untimed bool // the declaration, by the key or the chat
	}{
		"untimed":         {base + "  untimed: true\n", true, true},
		"timed, said so":  {base + "  untimed: false\n", false, false},
		"timed, unsaid":   {base, false, false},
		"a chat, unsaid":  {base + "hear:\n  field: chat\n  address: { env: PUSHOVER_USER }\n", false, true},
		"a chat, untimed": {base + "  untimed: true\nhear:\n  field: chat\n  address: { env: PUSHOVER_USER }\n", true, true},
	} {
		d, err := Parse("p.yaml", []byte(tc.text))
		if err != nil {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if d.Hook == nil || d.Hook.Untimed != tc.key {
			t.Errorf("%s: hook = %+v, want the key %v", name, d.Hook, tc.key)
		}
		if d.Untimed() != tc.untimed {
			t.Errorf("%s: Untimed = %v, want %v", name, d.Untimed(), tc.untimed)
		}
	}
	var none *Declaration
	if none.Untimed() {
		t.Error("no declaration is untimed")
	}
}

// A hook alone is a source: one that sends in and is never called has no
// base and no calls, as one that is its limits command has neither. What
// is for the calls is refused with it.
func TestParseHookAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		text, want string
		hook       bool // parses, with a hook key
	}{
		"hook alone":                     {"name: q\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: repeat\n  normalize: [normalize]\n", "", true},
		"hook and limits alone":          {"name: q\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: repeat\n  normalize: [normalize]\nlimits:\n  command: [q, \"{provider}\"]\n  kinds: { kimi: kimi }\n", "", true},
		"a base and no calls":            {"name: q\nbase: https://q.example.com\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: repeat\n  normalize: [normalize]\n", "base: is for the calls of a source, and this file declares none (it has hook alone)", false},
		"a credential and no calls":      {"name: q\ncredential: { env: Q }\nhook:\n  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: repeat\n  normalize: [normalize]\n", "credential: is for the calls of a source", false},
		"neither calls, limits nor hook": {"name: q\n", "calls: missing: a source is the calls it declares, or its limits command, or what it sends in (hook)", false},
	} {
		d, err := Parse("f", []byte(tc.text))
		if tc.hook {
			if err != nil {
				t.Errorf("%s: err = %v", name, err)
			} else if d.Hook == nil {
				t.Errorf("%s: hook = nil", name)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// What is refused of the hook keys is what would fail at every delivery:
// no way to verify one, a command that is no list of strings or has no
// program, a timestamp in two places or none.
func TestParseHookRefusals(t *testing.T) {
	verify := "  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n"
	src := func(body string) string {
		return "name: q\nbase: https://q.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" + body
	}
	hooked := func(body string) string { return src("hook:\n" + body) }
	for name, tc := range map[string]struct{ text, want string }{
		"no verify":                           {hooked("  bodies: unique\n  normalize: [normalize]\n"), "hook.verify: missing: how the core verifies a delivery"},
		"no bodies":                           {hooked(verify + "  normalize: [normalize]\n"), "hook.bodies: missing: unique, unique-per-hook, repeat"},
		"no normalize":                        {hooked(verify + "  bodies: unique\n"), "hook.normalize: missing: the command that turns a verified body into the envelope"},
		"a key it does not have":              {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  secret: s\n"), "hook.secret: unknown key (known here: verify, bodies, challenge, normalize, keys, register, untimed)"},
		"bodies that are neither":             {hooked(verify + "  bodies: sometimes\n  normalize: [normalize]\n"), `hook.bodies: "sometimes": want unique, unique-per-hook, repeat`},
		"a normalize that is a string":        {hooked(verify + "  bodies: unique\n  normalize: normalize\n"), "hook.normalize: want a list of strings"},
		"a normalize with no program":         {hooked(verify + "  bodies: unique\n  normalize: []\n"), "hook.normalize: an empty list"},
		"a normalize that leaves the file":    {hooked(verify + "  bodies: unique\n  normalize: [../normalize]\n"), `hook.normalize: the program "../normalize" has a '.' or '..' segment`},
		"a key that is none":                  {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  keys: [team, Team]\n"), `hook.keys: "Team": a key is lowercase letters, digits and _`},
		"a key listed twice":                  {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  keys: [team, team]\n"), "hook.keys: team is listed twice"},
		"the derived speaker key":             {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  keys: [speaker]\n"), "hook.keys: speaker is derived by the yard for hearing by identity, so it is not a hook key"},
		"an untimed that is neither":          {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  untimed: sometimes\n"), `hook.untimed: want true or false, got "sometimes"`},
		"no scheme":                           {hooked("  verify:\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.scheme: missing: how a delivery is verified (hmac-sha256, secret)"},
		"a scheme that is none":               {hooked("  verify:\n    scheme: sha1\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"sha1": want hmac-sha256, secret`},
		"no signed":                           {hooked("  verify:\n    scheme: hmac-sha256\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signed: missing: what the signature covers (body)"},
		"a signed that is none":               {hooked("  verify:\n    scheme: hmac-sha256\n    signed: headers\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"headers": missing {body}`},
		"no signature":                        {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature: missing"},
		"no delivery":                         {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.delivery: missing"},
		"a signature with no header":          {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature.header: missing"},
		"a header that is none":               {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: \"S Sig\", encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"S Sig" is no name of a header`},
		"an encoding that is none":            {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: base64 }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"base64": a signature arrives hex`},
		"a timestamp in both":                 {hooked(verify + "    timestamp: { body: ts, header: S-Ts, unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "a timestamp is in the body or in a header, not both"},
		"a timestamp in neither":              {hooked(verify + "    timestamp: { unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.body: missing: where the timestamp arrives"},
		"a timestamp with no unit":            {hooked(verify + "    timestamp: { body: ts, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.unit: missing: ms or s"},
		"a unit that is none":                 {hooked(verify + "    timestamp: { body: ts, unit: ns, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), `"ns": want ms or s`},
		"a timestamp with no window":          {hooked(verify + "    timestamp: { body: ts, unit: ms }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.window: missing"},
		"a window that is none":               {hooked(verify + "    timestamp: { body: ts, unit: ms, window: a minute }\n  bodies: unique\n  normalize: [normalize]\n"), `"a minute": a window is a positive duration (1m)`},
		"a window that is not positive":       {hooked(verify + "    timestamp: { body: ts, unit: ms, window: 0s }\n  bodies: unique\n  normalize: [normalize]\n"), `"0s": a window is a positive duration (1m)`},
		"a path that is none":                 {hooked(verify + "    timestamp: { body: data..ts, unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), `"data..ts": a dot path into the body`},
		"a secret with signed":                {hooked("  verify:\n    scheme: secret\n    signed: body\n    signature: { header: S-Secret }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signed: is for scheme hmac-sha256"},
		"a secret with encoding":              {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret, encoding: hex }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature.encoding: is for scheme hmac-sha256"},
		"a secret with a prefix":              {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret, prefix: \"Token \" }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature.prefix: is for scheme hmac-sha256"},
		"a secret with no signature":          {hooked("  verify:\n    scheme: secret\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature: missing: the header the secret arrives in"},
		"a secret header that is none":        {hooked("  verify:\n    scheme: secret\n    signature: { header: \"S Secret\" }\n  bodies: unique\n  normalize: [normalize]\n"), `"S Secret" is no name of a header`},
		"a delivery in both":                  {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret }\n    delivery: { header: S-Delivery, body: update_id }\n  bodies: unique\n  normalize: [normalize]\n"), "a delivery id is in a header or in the body, not both"},
		"a delivery in neither":               {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret }\n    delivery: {}\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.delivery.header: missing: where the service's id of the delivery arrives"},
		"a delivery path that is none":        {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret }\n    delivery: { body: data..id }\n  bodies: unique\n  normalize: [normalize]\n"), `"data..id": a dot path into the body`},
		"bodies that repeat with no delivery": {hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret }\n  bodies: repeat\n  normalize: [normalize]\n"), "hook.bodies: repeat: needs verify.delivery"},
		"a signed template with no body":      {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"v0:{timestamp}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n    timestamp: { header: S-Ts, unit: s, window: 5m }\n  bodies: unique\n  normalize: [normalize]\n"), "missing {body}"},
		"a signed template with no time":      {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"v0:{body}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n    timestamp: { header: S-Ts, unit: s, window: 5m }\n  bodies: unique\n  normalize: [normalize]\n"), "missing {timestamp}"},
		"a signed template with body twice":   {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"{body}:{body}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "repeated {body}"},
		"a signed template with time twice":   {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"{timestamp}:{timestamp}:{body}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n    timestamp: { header: S-Ts, unit: s, window: 5m }\n  bodies: unique\n  normalize: [normalize]\n"), "repeated {timestamp}"},
		"a signed template with an unknown":   {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"v0:{sig}:{body}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "unknown placeholder {sig}"},
		"a signed template with no timestamp": {hooked("  verify:\n    scheme: hmac-sha256\n    signed: \"v0:{timestamp}:{body}\"\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "{timestamp} with no timestamp"},
		"a challenge with no body":            {hooked(verify + "  bodies: unique\n  challenge: { value: url_verification, answer: challenge }\n  normalize: [normalize]\n"), "hook.challenge.body: missing"},
		"a challenge with no value":           {hooked(verify + "  bodies: unique\n  challenge: { body: type, answer: challenge }\n  normalize: [normalize]\n"), "hook.challenge.value: missing"},
		"a challenge with no answer":          {hooked(verify + "  bodies: unique\n  challenge: { body: type, value: url_verification }\n  normalize: [normalize]\n"), "hook.challenge.answer: missing"},
		"a challenge body that is no path":    {hooked(verify + "  bodies: unique\n  challenge: { body: data..type, value: url_verification, answer: challenge }\n  normalize: [normalize]\n"), `"data..type": a dot path into the body`},
		"a challenge answer that is no path":  {hooked(verify + "  bodies: unique\n  challenge: { body: type, value: url_verification, answer: data..challenge }\n  normalize: [normalize]\n"), `"data..challenge": a dot path into the body`},
		"a challenge key it does not have":    {hooked(verify + "  bodies: unique\n  challenge: { body: type, value: url_verification, answer: challenge, echo: challenge }\n  normalize: [normalize]\n"), "hook.challenge.echo: unknown key (known here: body, value, answer)"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// One mistake is one line: a scheme that is none is not also told to
	// be missing.
	_, err := Parse("f", []byte(hooked("  verify:\n    scheme: sha1\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n")))
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want one", err)
	}
	// Bodies that repeat with no delivery under scheme secret are the one
	// error: no delivery is only refused then.
	_, err = Parse("f", []byte(hooked("  verify:\n    scheme: secret\n    signature: { header: S-Secret }\n  bodies: repeat\n  normalize: [normalize]\n")))
	if !errors.As(err, &errs) || len(errs) != 1 {
		t.Fatalf("errors = %v, want one", err)
	}
}

// The events key: the envelope types that are CI runs and that tell the
// pull request's head, and the commands that read the pull request and
// its CI back. Each stands alone: either list without the commands, and
// either command without the other.
func TestParseEvents(t *testing.T) {
	const file = `name: forge
base: https://forge.test
credential: { env: FORGE_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
events:
  runs: [workflow_run, check_suite]
  head_from: [pull_request, pull_request_review]
  pr: [forge/pr]
  ci: [forge/ci]
`
	d, err := Parse("forge.yaml", []byte(file))
	if err != nil {
		t.Fatal(err)
	}
	ev := d.Events
	if ev == nil {
		t.Fatalf("declaration = %+v, want events", d)
	}
	if strings.Join(ev.Runs, " ") != "workflow_run check_suite" ||
		strings.Join(ev.HeadFrom, " ") != "pull_request pull_request_review" ||
		strings.Join(ev.PR, " ") != "forge/pr" || strings.Join(ev.CI, " ") != "forge/ci" {
		t.Fatalf("events = %+v", ev)
	}
	keys := []string{}
	for _, c := range d.Commands() {
		keys = append(keys, c.Key)
	}
	if strings.Join(keys, " ") != "events.pr events.ci" {
		t.Fatalf("commands = %v", keys)
	}

	// The lists without the commands: runs are judged against the head
	// the events told, and the head is never read back.
	d, err = Parse("forge.yaml", []byte(`name: forge
base: https://forge.test
credential: { env: FORGE_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
events:
  runs: [workflow_run]
  head_from: [pull_request]
`))
	if err != nil {
		t.Fatal(err)
	}
	if ev := d.Events; ev == nil || len(ev.PR) > 0 || len(ev.CI) > 0 || len(d.Commands()) > 0 {
		t.Fatalf("events = %+v, commands = %v", ev, d.Commands())
	}
}

func TestParseEventsRefusals(t *testing.T) {
	src := func(body string) string {
		return "name: q\nbase: https://q.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" + body
	}
	evented := func(body string) string { return src("events:\n" + body) }
	for name, tc := range map[string]struct{ text, want string }{
		"a key it does not have":     {evented("  ran: [workflow_run]\n"), "events.ran: unknown key (known here: runs, head_from, pr, ci)"},
		"runs that are a string":     {evented("  runs: workflow_run\n"), "events.runs: want a list ([a, b]), got \"workflow_run\""},
		"a run listed twice":         {evented("  runs: [workflow_run, workflow_run]\n"), "events.runs: workflow_run is listed twice"},
		"head_from that is a string": {evented("  head_from: pull_request\n"), "events.head_from: want a list ([a, b]), got \"pull_request\""},
		"a pr that is a string":      {evented("  pr: forge/pr\n"), "events.pr: want a list of strings ([program, events/pr])"},
		"a pr with no program":       {evented("  pr: []\n"), "events.pr: an empty list"},
		"a pr that leaves the file":  {evented("  pr: [../pr]\n"), `events.pr: the program "../pr" has a '.' or '..' segment`},
		"a ci that is a string":      {evented("  ci: forge/ci\n"), "events.ci: want a list of strings ([program, events/ci])"},
		"a ci with no program":       {evented("  ci: []\n"), "events.ci: an empty list"},
		"events that are no mapping": {src("events: [runs]\n"), "events: want a mapping (key: value), got a list"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// A hear key: the envelope field that must hold a user's address, the
// variable it is under in a user's env file, and from, which narrows whom
// the source hears to the operator. Without from every user whose env
// file sets the address is heard. Subscribe is the filter a claim
// approval subscribes the yard's hearer with beside the claimed address,
// left out where the address alone is the filter.
func TestParseHear(t *testing.T) {
	base := pushover + "hook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: P-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [chat]\n"
	with := func(hear string) string { return base + "hear:\n" + hear }
	d, err := Parse("p.yaml", []byte(with("  field: chat\n  address: { env: PUSHOVER_USER }\n  from: operator\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h := d.Hear; h == nil || h.Field != "chat" || h.Address.Env != "PUSHOVER_USER" || h.From != HearFromOperator {
		t.Fatalf("hear = %+v", d.Hear)
	}
	d, err = Parse("p.yaml", []byte(with("  field: chat\n  address: { env: PUSHOVER_USER }\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h := d.Hear; h == nil || h.Field != "chat" || h.Address.Env != "PUSHOVER_USER" || h.From != "" {
		t.Fatalf("hear = %+v", d.Hear)
	}
	d, err = Parse("p.yaml", []byte(with("  field: chat\n  address: { env: PUSHOVER_USER }\n  subscribe: { type: message }\n")))
	if err != nil {
		t.Fatal(err)
	}
	if h := d.Hear; h == nil || len(h.Subscribe) != 1 || h.Subscribe["type"] != "message" {
		t.Fatalf("hear = %+v", d.Hear)
	}
}

// Hearing by identity: the field holds a speaker's id in the source,
// matched against a user's identity there. It needs no notify key and
// no address, and address and from are refused with it, as is a field
// no hook key names and a by that is no way the yard hears.
func TestParseHearByIdentity(t *testing.T) {
	hooked := "hook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: P-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [sender, channel]\n"
	base := pushover + hooked
	// Without its notify key the file still parses by identity: hearing
	// by who speaks names no address telling reads.
	unnotified := strings.SplitN(pushover, "notify:\n", 2)[0] + hooked
	for _, text := range []string{
		base + "hear:\n  field: sender\n  by: identity\n",
		unnotified + "hear:\n  field: sender\n  by: identity\n",
	} {
		d, err := Parse("p.yaml", []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if h := d.Hear; h == nil || h.Field != "sender" || h.By != HearByIdentity || !h.ByIdentity() || !d.HearsByIdentity() {
			t.Fatalf("hear = %+v", d.Hear)
		}
		if _, _, ok := d.ClaimAddress(); ok {
			t.Fatal("a source hearing by identity claims an address")
		}
	}
	for name, tc := range map[string]struct{ text, want string }{
		"by identity with an address": {base + "hear:\n  field: sender\n  by: identity\n  address: { env: PUSHOVER_USER }\n",
			"hear.address: is for hearing by address, and by: identity hears by who a speaker is: leave address out with by: identity"},
		"by identity with a from": {base + "hear:\n  field: sender\n  by: identity\n  from: operator\n",
			"hear.from: is for hearing by address, and by: identity hears by who a speaker is: leave from out with by: identity"},
		"a by that is none": {base + "hear:\n  field: sender\n  by: address\n  address: { env: PUSHOVER_USER }\n",
			`hear.by: "address": want identity`},
		"a field under no key": {base + "hear:\n  field: room\n  by: identity\n",
			"hear.field: room is no key the envelope carries: hook.keys names sender, channel"},
		"no field": {base + "hear:\n  by: identity\n",
			"hear.field: missing: the envelope field that must hold a user's address (chat)"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// What is refused of the hear key is what would fail at every delivery,
// or hear nothing: no field, no address, a field the envelope carries
// under no key the file names, an address that is not the one telling
// reads, a from that is no role, a subscribe that is no filter or names
// the field the claim sets itself, and a hear without the hook that
// receives or the notify that names the address.
func TestParseHearRefusals(t *testing.T) {
	hooked := "hook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: P-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [chat]\n"
	base := pushover + hooked
	with := func(hear string) string { return base + "hear:\n" + hear }
	good := "  field: chat\n  address: { env: PUSHOVER_USER }\n"
	// unnotified is the file without its notify key: pushover ends in it.
	unnotified := strings.SplitN(pushover, "notify:\n", 2)[0] + hooked + "hear:\n" + good
	for name, tc := range map[string]struct{ text, want string }{
		"no field":                        {with("  address: { env: PUSHOVER_USER }\n"), "hear.field: missing: the envelope field that must hold a user's address (chat)"},
		"no address":                      {with("  field: chat\n"), "hear.address: missing: { env: <the name the address is under in a user's env file> }"},
		"an address with no env":          {with("  field: chat\n  address: {}\n"), "hear.address.env: missing: the name of the variable the address is in, in a user's env file"},
		"a key it does not have":          {with(good + "  allow: [chat]\n"), "hear.allow: unknown key (known here: field, by, address, from, subscribe)"},
		"an address key it does not have": {with("  field: chat\n  address: { env: PUSHOVER_USER, file: u.env }\n"), "hear.address.file: unknown key (known here: env)"},
		"a field that is none":            {with("  field: Chat\n  address: { env: PUSHOVER_USER }\n"), `hear.field: "Chat": a field is lowercase letters, digits and _`},
		"a field under no key":            {with("  field: room\n  address: { env: PUSHOVER_USER }\n"), "hear.field: room is no key the envelope carries: hook.keys names chat"},
		"an address that is no variable":  {with("  field: chat\n  address: { env: \"9LIVES\" }\n"), `hear.address.env: "9LIVES" is no name of a variable`},
		"an address telling does not read": {with("  field: chat\n  address: { env: PUSHOVER_ALIAS }\n"),
			"hear.address.env: PUSHOVER_ALIAS is not the address telling reads: notify.address.env names PUSHOVER_USER, and hear reads the same file"},
		"a from that is none": {with(good + "  from: crew\n"), `hear.from: "crew": want operator`},
		"a subscribe that is no mapping": {with(good + "  subscribe: type=message\n"),
			"hear.subscribe: want a mapping (key: value), got \"type=message\""},
		"a subscribe key that is none": {with(good + "  subscribe: { Type: message }\n"),
			`hear.subscribe.Type: "Type": a filter key is lowercase letters, digits and _`},
		"a subscribe with no value": {with(good + "  subscribe: { type: \"\" }\n"),
			`hear.subscribe.type: want a string, got ""`},
		"a subscribe naming the field": {with(good + "  subscribe: { chat: \"123\" }\n"),
			"hear.subscribe.chat: chat is the field the claim sets to the claimed address: subscribe names only the filter beside it"},
		"hear without hook": {pushover + "hear:\n" + good,
			"hear: needs hook: hear keeps the deliveries of a source, and this file has no hook key to receive any"},
		"hear without notify": {unnotified,
			"hear: needs notify: hear keeps the deliveries from an address the notify key names, and this file has no notify key to name one"},
		"hear with hook alone": {"name: q\nhook:\n  verify:\n    scheme: secret\n    signature: { header: Q-Secret }\n  bodies: unique\n  normalize: [normalize]\n  keys: [chat]\nhear:\n" + good,
			"hear: is for the calls of a source, and this file declares none (it has hook alone)"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// thread.assignee says the source's open takes whom the thread is
// assigned to, and by which part of their identity. Absent, its open
// assigns nobody.
func TestParseThreadAssignee(t *testing.T) {
	d, err := Parse("lin.yaml", []byte(linearish))
	if err != nil {
		t.Fatal(err)
	}
	if d.Thread.Assignee != "" {
		t.Fatalf("thread = %+v, want no assignee", d.Thread)
	}
	for _, part := range []string{AssigneeID, AssigneeKey} {
		with := strings.Replace(linearish, "  states: [started, review, done]\n", "  states: [started, review, done]\n  assignee: "+part+"\n", 1)
		if d, err = Parse("lin.yaml", []byte(with)); err != nil {
			t.Fatal(err)
		}
		if d.Thread.Assignee != part {
			t.Fatalf("thread = %+v, want assignee %s", d.Thread, part)
		}
	}
}
