package source

import (
	"encoding/json"
	"errors"
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
		{"a name yardr has built in", with("name: crm", "name: github"),
			"name", "github is a source yardr has built in"},
		{"events in", crm + "events: { verify: hmac }\n",
			"events", "not read yet"},
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
	text := strings.NewReplacer("name: crm", "name: github", "base: https://crm.example.com/api/v2", "base: http://crm.example.com", "method: POST", "method: POSTED").Replace(crm)
	_, err := Parse("crm.yaml", []byte(text))
	var errs Errors
	if !errors.As(err, &errs) || len(errs) != 3 {
		t.Fatalf("errors = %v", err)
	}
	if errs[0].Line != 1 || errs[1].Line != 3 || errs[2].Line <= errs[1].Line {
		t.Fatalf("not in file order: %v", err)
	}
	if got := errs[0].Error(); !strings.HasPrefix(got, "crm.yaml:1: name: github is a source yardr has built in") {
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
		"no kinds":                        {limits(cmd), "limits.kinds: missing: the harness kinds the source answers for"},
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
		"a key it does not have":         {src("thread:\n  wait: [linear/thread, wait]\n"), "thread.wait: unknown key (known here: reply, posted, read, open, opened, state, place, states, labels)"},
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
		"no verify":                        {hooked("  bodies: unique\n  normalize: [normalize]\n"), "hook.verify: missing: how the core verifies a delivery"},
		"no bodies":                        {hooked(verify + "  normalize: [normalize]\n"), "hook.bodies: missing: unique, unique-per-hook, repeat"},
		"no normalize":                     {hooked(verify + "  bodies: unique\n"), "hook.normalize: missing: the command that turns a verified body into the envelope"},
		"a key it does not have":           {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  secret: s\n"), "hook.secret: unknown key (known here: verify, bodies, normalize, keys)"},
		"bodies that are neither":          {hooked(verify + "  bodies: sometimes\n  normalize: [normalize]\n"), `hook.bodies: "sometimes": want unique, unique-per-hook, repeat`},
		"a normalize that is a string":     {hooked(verify + "  bodies: unique\n  normalize: normalize\n"), "hook.normalize: want a list of strings"},
		"a normalize with no program":      {hooked(verify + "  bodies: unique\n  normalize: []\n"), "hook.normalize: an empty list"},
		"a normalize that leaves the file": {hooked(verify + "  bodies: unique\n  normalize: [../normalize]\n"), `hook.normalize: the program "../normalize" has a '.' or '..' segment`},
		"a key that is none":               {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  keys: [team, Team]\n"), `hook.keys: "Team": a key is lowercase letters, digits and _`},
		"a key listed twice":               {hooked(verify + "  bodies: unique\n  normalize: [normalize]\n  keys: [team, team]\n"), "hook.keys: team is listed twice"},
		"no scheme":                        {hooked("  verify:\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.scheme: missing: how the body is signed (hmac-sha256)"},
		"a scheme that is none":            {hooked("  verify:\n    scheme: sha1\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"sha1": the scheme is hmac-sha256`},
		"no signed":                        {hooked("  verify:\n    scheme: hmac-sha256\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signed: missing: what the signature covers (body)"},
		"a signed that is none":            {hooked("  verify:\n    scheme: hmac-sha256\n    signed: headers\n    signature: { header: S-Sig, encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"headers": the signature covers body`},
		"no signature":                     {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature: missing"},
		"no delivery":                      {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: hex }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.delivery: missing"},
		"a signature with no header":       {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.signature.header: missing"},
		"a header that is none":            {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: \"S Sig\", encoding: hex }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"S Sig" is no name of a header`},
		"an encoding that is none":         {hooked("  verify:\n    scheme: hmac-sha256\n    signed: body\n    signature: { header: S-Sig, encoding: base64 }\n    delivery: { header: S-Delivery }\n  bodies: unique\n  normalize: [normalize]\n"), `"base64": a signature arrives hex`},
		"a timestamp in both":              {hooked(verify + "    timestamp: { body: ts, header: S-Ts, unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "a timestamp is in the body or in a header, not both"},
		"a timestamp in neither":           {hooked(verify + "    timestamp: { unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.body: missing: where the timestamp arrives"},
		"a timestamp with no unit":         {hooked(verify + "    timestamp: { body: ts, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.unit: missing: ms or s"},
		"a unit that is none":              {hooked(verify + "    timestamp: { body: ts, unit: ns, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), `"ns": want ms or s`},
		"a timestamp with no window":       {hooked(verify + "    timestamp: { body: ts, unit: ms }\n  bodies: unique\n  normalize: [normalize]\n"), "hook.verify.timestamp.window: missing"},
		"a window that is none":            {hooked(verify + "    timestamp: { body: ts, unit: ms, window: a minute }\n  bodies: unique\n  normalize: [normalize]\n"), `"a minute": a window is a positive duration (1m)`},
		"a window that is not positive":    {hooked(verify + "    timestamp: { body: ts, unit: ms, window: 0s }\n  bodies: unique\n  normalize: [normalize]\n"), `"0s": a window is a positive duration (1m)`},
		"a path that is none":              {hooked(verify + "    timestamp: { body: data..ts, unit: ms, window: 1m }\n  bodies: unique\n  normalize: [normalize]\n"), `"data..ts": a dot path into the body`},
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
}
