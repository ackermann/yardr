package source

import (
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
		{"a name yardr has built in", with("name: crm", "name: linear"),
			"name", "linear is a source yardr has built in"},
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
