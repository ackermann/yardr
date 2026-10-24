package source

import (
	"strings"
	"testing"
)

func registerText(register string) string {
	return `
name: bot
base: http://127.0.0.1:9
credential: { env: BOT_TOKEN }
calls:
  setwebhook: { kind: update, params: { url: {type: string, max: 2048}, secret_token: {type: string, max: 256} }, request: { method: POST, path: /setwebhook, body: { url: "{url}", secret_token: "{secret_token}", allowed_updates: ["message"] } } }
  ping: { kind: read, request: { method: GET, path: /ping } }
hook:
  verify: { scheme: secret, signature: { header: X-Test-Secret } }
  bodies: unique
  normalize: [bot/normalize]
` + register
}

func TestParseHookRegister(t *testing.T) {
	d, err := Parse("bot.yaml", []byte(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)))
	if err != nil {
		t.Fatalf("a register naming its update call refused: %v", err)
	}
	if d.Hook.Register == nil || d.Hook.Register.Call != "setwebhook" || d.Hook.Register.URL != "url" || d.Hook.Register.Secret != "secret_token" {
		t.Fatalf("register = %+v", d.Hook.Register)
	}
	if !d.Hook.RegistersSecret() {
		t.Fatal("RegistersSecret is false for a register with scheme secret")
	}
	asked := func(text string) string {
		text = strings.Replace(text, `secret_token: {type: string, max: 256}`,
			`secret_token: {type: string, max: 256}, project: {type: string, pattern: "^[0-9]+$", max: 20}`, 1)
		text = strings.Replace(text, `allowed_updates: ["message"]`,
			`project: "{project}", allowed_updates: ["message"]`, 1)
		return strings.Replace(text, `secret: secret_token }`, `secret: secret_token, ask: [project] }`, 1)
	}
	d, err = Parse("bot.yaml", []byte(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))))
	if err != nil {
		t.Fatalf("a register asking for a required parameter refused: %v", err)
	}
	if len(d.Hook.Register.Ask) != 1 || d.Hook.Register.Ask[0] != "project" {
		t.Fatalf("ask = %+v, want [project]", d.Hook.Register.Ask)
	}
	withExisting := func(text string) string {
		text = strings.Replace(text, `  ping: { kind: read, request: { method: GET, path: /ping } }`,
			"  ping: { kind: read, request: { method: GET, path: /ping } }\n"+`  webhooks: { kind: read, params: { project: {type: string, pattern: "^[0-9]+$", max: 20} }, request: { method: GET, path: /webhooks, query: { project: "{project}" } } }`, 1)
		return strings.Replace(text, `ask: [project] }`, `ask: [project], existing: { call: webhooks, in: results, field: url } }`, 1)
	}
	d, err = Parse("bot.yaml", []byte(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)))))
	if err != nil {
		t.Fatalf("a register with a check refused: %v", err)
	}
	if ex := d.Hook.Register.Existing; ex == nil || ex.Call != "webhooks" || ex.In != "results" || ex.Field != "url" {
		t.Fatalf("existing = %+v, want the webhooks call with results and url", d.Hook.Register.Existing)
	}
	urlCheck := strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), `query: { project: "{project}" }`, `query: { project: "{project}", url: "{url}" }`, 1)
	urlCheck = strings.Replace(urlCheck, `webhooks: { kind: read, params: { project:`, `webhooks: { kind: read, params: { url: {type: string, max: 2048}, project:`, 1)
	if _, err := Parse("bot.yaml", []byte(urlCheck)); err != nil {
		t.Fatalf("a check taking the URL refused: %v", err)
	}
	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"a read call": {registerText(`
  register: { call: ping, url: url, secret: secret_token }
`), "is a read call: registering a webhook"},
		"no such call": {registerText(`
  register: { call: vanish, url: url, secret: secret_token }
`), "is no call of this file"},
		"url is no parameter": {registerText(`
  register: { call: setwebhook, url: uri, secret: secret_token }
`), "is no parameter of the call setwebhook"},
		"url is not sent": {
			strings.Replace(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`), `url: "{url}", `, "", 1),
			"does not send parameter url",
		},
		"secret is not sent": {
			strings.Replace(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`), `secret_token: "{secret_token}", `, "", 1),
			"does not send parameter secret_token",
		},
		"no secret for scheme secret": {registerText(`
  register: { call: setwebhook, url: url }
`), "missing: the parameter that takes the hook's secret"},
		"service-made signature registers later": {
			strings.Replace(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`), "scheme: secret", "scheme: hmac-sha256", 1),
			"registering a hook is supported only with verify.scheme secret",
		},
		"a required parameter nothing fills": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `, ask: [project]`, "", 1),
			"the call setwebhook needs the parameter project, and nothing here fills it",
		},
		"ask names no parameter": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), "ask: [project]", "ask: [region]", 1),
			"region is no parameter of the call setwebhook",
		},
		"ask names a parameter with no pattern": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `pattern: "^[0-9]+$", `, "", 1),
			"the parameter project is asked for in the clear",
		},
		"ask names an optional parameter": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `max: 20}`, `max: 20, optional: true}`, 1),
			"the parameter project is optional",
		},
		"ask names a defaulted parameter": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `max: 20}`, `max: 20, default: "48291"}`, 1),
			"the parameter project is optional",
		},
		"ask names a parameter that is no string": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `project: {type: string, pattern: "^[0-9]+$", max: 20}`, `project: {type: int}`, 1),
			"the parameter project is asked for in the clear",
		},
		"ask names the secret": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), "ask: [project]", "ask: [secret_token]", 1),
			"the parameter secret_token is filled by hook.register.secret already",
		},
		"ask names a parameter the call does not send": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), `project: "{project}", `, "", 1),
			"the call setwebhook does not send parameter project",
		},
		"ask names a parameter twice": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), "ask: [project]", "ask: [project, project]", 1),
			"project is listed twice",
		},
		"ask is no list": {
			strings.Replace(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)), "ask: [project]", "ask: project", 1),
			"want a list ([a, b])",
		},
		"existing names an update": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), "existing: { call: webhooks,", "existing: { call: setwebhook,", 1),
			"setwebhook is an update: the check is a read call",
		},
		"existing names no call": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), "existing: { call: webhooks,", "existing: { call: vanish,", 1),
			"vanish is no call of this file",
		},
		"existing names a build call": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), "webhooks: { kind: read,", "webhooks: { kind: read, build: [webhooks],", 1),
			"webhooks names build: the check sends the request's template",
		},
		"existing without its list": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), ", in: results", "", 1),
			"missing: the dot path to the list",
		},
		"existing without its field": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), ", field: url", "", 1),
			"missing: the dot path in each listed webhook",
		},
		"existing field is no path": {
			strings.Replace(withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`))), "field: url", "field: inputs..url", 1),
			`"inputs..url": a dot path in each listed webhook`,
		},
		"check call needs a parameter nothing fills": {
			func() string {
				text := withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)))
				text = strings.Replace(text, `webhooks: { kind: read, params: { project:`, `webhooks: { kind: read, params: { region: {type: string, max: 20}, project:`, 1)
				return strings.Replace(text, `query: { project: "{project}" }`, `query: { project: "{project}", region: "{region}" }`, 1)
			}(),
			"the call webhooks needs the parameter region, and nothing here fills it",
		},
		"check call needs the secret": {
			func() string {
				text := withExisting(asked(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`)))
				text = strings.Replace(text, `webhooks: { kind: read, params: { project:`, `webhooks: { kind: read, params: { secret_token: {type: string, max: 256}, project:`, 1)
				return strings.Replace(text, `query: { project: "{project}" }`, `query: { project: "{project}", secret_token: "{secret_token}" }`, 1)
			}(),
			"the call webhooks needs the parameter secret_token, and nothing here fills it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("bot.yaml", []byte(tc.text))
			if err == nil {
				t.Fatalf("a bad register parsed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("register refused without %q: %v", tc.want, err)
			}
		})
	}
}
