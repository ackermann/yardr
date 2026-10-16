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
		"no secret for scheme secret": {registerText(`
  register: { call: setwebhook, url: url }
`), "missing: the parameter that takes the hook's secret"},
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
