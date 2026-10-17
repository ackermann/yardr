package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const buildTestDeclaration = `name: crm
base: https://crm.example.com/api
credential:
  env: CRM_TOKEN
  update_env: CRM_UPDATE_TOKEN
calls:
  write:
    kind: update
    params:
      count: { type: int }
      ok: { type: bool }
      mode: { type: enum, values: [fast, slow] }
      extra: { type: string, optional: true }
      defaulted: { type: bool, default: false }
    request: { method: POST, path: /notes }
    build: [crm/build, argument]
`

func TestParseBuild(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(buildTestDeclaration))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := d.Call("write")
	if !ok || strings.Join(c.Build, " ") != "crm/build argument" || c.Request.Body != nil {
		t.Fatalf("call = %+v", c)
	}
	d.Events = &Events{PR: []string{"events/pr"}}
	commands := d.Commands()
	if len(commands) != 2 || commands[0].Key != "events.pr" || commands[1].Key != "call.write.build" || strings.Join(commands[1].Argv, " ") != "crm/build argument" {
		t.Fatalf("commands = %+v", commands)
	}
	read := strings.Replace(buildTestDeclaration, "kind: update", "kind: read", 1)
	if _, err := Parse("read.yaml", []byte(read)); err != nil {
		t.Fatalf("a read may build too: %v", err)
	}
}

func TestParseRefusesBuild(t *testing.T) {
	both := strings.Replace(buildTestDeclaration, "request: { method: POST, path: /notes }", "request: { method: POST, path: /notes, body: { text: value } }", 1)
	bothNull := strings.Replace(buildTestDeclaration, "request: { method: POST, path: /notes }", "request: { method: POST, path: /notes, body: null }", 1)
	absolute := strings.Replace(buildTestDeclaration, "build: [crm/build, argument]", "build: [/opt/crm/build]", 1)
	check := `name: crm
base: https://crm.example.com
check: ping
calls:
  ping:
    kind: read
    request: { method: GET, path: /ping }
    build: [crm/build]
`
	notify := strings.Replace(pushover, `      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}", url: "{link}" }`, `    build: [pushover/build]`, 1)
	register := strings.Replace(registerText(`
  register: { call: setwebhook, url: url, secret: secret_token }
`), `request: { method: POST, path: /setwebhook, body: { url: "{url}", secret_token: "{secret_token}", allowed_updates: ["message"] } }`, `request: { method: POST, path: /setwebhook }, build: [bot/build]`, 1)
	for name, tc := range map[string]struct {
		text string
		want string
	}{
		"body and build":      {both, "a call names request.body or build, never both"},
		"null body and build": {bothNull, "a call names request.body or build, never both"},
		"absolute program":    {absolute, "is absolute: a build's program must be relative"},
		"check call":          {check, "check: ping names build: source check and yardr doctor send the request's template"},
		"notify call":         {notify, "notify.call: message names build: telling a person sends the request's template"},
		"hook register call":  {register, "hook.register.call: setwebhook names build: registering a webhook sends the request's template"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse("test.yaml", []byte(tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRunBuildHandsInputAndReadsAnyJSONValue(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	p := standIn(t, `cat >`+seen+`; printf '%s' '[true,{"n":2}]'`)
	body, refusal, err := RunBuild(context.Background(), "call.write.build", []string{p}, map[string]any{
		"params": map[string]any{"count": int64(2), "ok": false, "text": "x"},
	}, nil, t.TempDir(), 0)
	if err != nil || refusal != "" || string(body) != `[true,{"n":2}]` {
		t.Fatalf("RunBuild = %s, %q, %v", body, refusal, err)
	}
	in, _ := os.ReadFile(seen)
	if string(in) != `{"params":{"count":2,"ok":false,"text":"x"}}` {
		t.Fatalf("stdin = %s", in)
	}
	for _, value := range []string{
		`{}`, `[]`, `"text"`, `2`, `false`, `null`,
		`9007199254740993123456789`, `1e1000`, `{"id":9007199254740993123456789}`,
	} {
		if err := oneValue("build", []byte(value)); err != nil {
			t.Errorf("%s is one JSON value: %v", value, err)
		}
	}
}

func TestRunBuildRefusalIsOnlyTheScriptsWords(t *testing.T) {
	p := standIn(t, `printf '%s' 'stdout-must-not-leak'; echo '3 matches; pass all=true to replace them all' >&2; exit 1`)
	body, refusal, err := RunBuild(context.Background(), "call.write.build", []string{p}, nil, nil, t.TempDir(), 0)
	if err != nil || body != nil || refusal != "3 matches; pass all=true to replace them all" {
		t.Fatalf("RunBuild = %s, %q, %v", body, refusal, err)
	}
	if strings.Contains(refusal, "stdout-must-not-leak") || strings.Contains(refusal, "exit status") {
		t.Fatalf("refusal = %q", refusal)
	}
}

func TestBuildBodyBreakage(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(buildTestDeclaration))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	program := filepath.Join(home, "sources", "crm", "crm", "build")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	run := func() error {
		_, err := BuildBody(context.Background(), d, "write", map[string]string{"count": "2", "ok": "true", "mode": "fast"}, "write-token", home)
		return err
	}
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"nothing":       {`exit 0`, "it printed nothing"},
		"no JSON":       {`printf '%s' 'stdout-must-not-leak'`, "what it printed is not one JSON value"},
		"trailing data": {`printf '{} trailing'`, "what it printed is not one JSON value"},
		"two values":    {`printf '{} []'`, "it printed more than one JSON value"},
		"too much":      {`head -c 1048577 /dev/zero | tr '\0' ' '; printf '{}'`, "printed more than 1048576 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			write(tc.body)
			err := run()
			if reason(err) != BuildFailed || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildBody = %v; want %s containing %q", err, BuildFailed, tc.want)
			}
			if strings.Contains(err.Error(), "stdout-must-not-leak") {
				t.Fatalf("the error holds stdout: %v", err)
			}
		})
	}

	if err := os.Remove(program); err != nil {
		t.Fatal(err)
	}
	if err := run(); reason(err) != BuildFailed {
		t.Fatalf("a missing program = %v", err)
	}

	write(`exec sleep 300`)
	was := Timeout
	Timeout = 50 * time.Millisecond
	t.Cleanup(func() { Timeout = was })
	if err := run(); reason(err) != BuildFailed || !strings.Contains(err.Error(), "no answer within 50ms") {
		t.Fatalf("a slow build = %v", err)
	}
}

func TestBuildBodyHandsTypedParamsAndUpdateCredential(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(buildTestDeclaration))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	program := filepath.Join(home, "sources", "crm", "crm", "build")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(`cat > build-stdin; env > build-environment; printf '%s' "$1" > build-argument; printf '{"built":true}'`)
	t.Setenv("YARDR_SESSION", "not-for-a-build")
	t.Setenv("CRM_UPDATE_TOKEN", "not-this-one")
	params := map[string]string{"count": "2", "ok": "true", "mode": "fast"}
	body, err := BuildBody(context.Background(), d, "write", params, "write-token", home)
	if err != nil || string(body) != `{"built":true}` {
		t.Fatalf("BuildBody = %s, %v", body, err)
	}
	in, _ := os.ReadFile(filepath.Join(home, "build-stdin"))
	if string(in) != `{"params":{"count":2,"defaulted":false,"mode":"fast","ok":true}}` {
		t.Fatalf("stdin = %s", in)
	}
	arg, _ := os.ReadFile(filepath.Join(home, "build-argument"))
	if string(arg) != "argument" {
		t.Fatalf("argument = %q", arg)
	}
	env, _ := os.ReadFile(filepath.Join(home, "build-environment"))
	for _, want := range []string{"CRM_UPDATE_TOKEN=write-token", "YARDR_SOURCE_BASE=https://crm.example.com/api"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("environment lacks %q:\n%s", want, env)
		}
	}
	for _, unwanted := range []string{"CRM_TOKEN=", "YARDR_SESSION=", "not-this-one"} {
		if strings.Contains(string(env), unwanted) {
			t.Errorf("environment contains %q:\n%s", unwanted, env)
		}
	}

	write(`echo '3 matches; pass all=true to replace them all' >&2; exit 1`)
	_, err = BuildBody(context.Background(), d, "write", params, "write-token", home)
	if reason(err) != BuildRefused || err.Error() != "source crm, call write: 3 matches; pass all=true to replace them all" {
		t.Fatalf("refusal = %v", err)
	}
}
