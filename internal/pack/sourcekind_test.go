package pack_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
)

// cmdDecl is a declaration with commands, as a pack of kind source carries:
// what the source sends in (hook.normalize), what the yard does in its
// threads (thread.*), and its issues as a link.
const cmdDecl = `name: acme
about: Acme issues.
base: https://acme.test
credential: { env: ACME_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex }
    delivery: { header: Acme-Delivery }
  bodies: unique
  normalize: [acme/normalize]
  keys: [team]
thread:
  reply: [acme/thread, reply]
  posted: [acme/thread, posted]
  read: [acme/thread, read]
  place: team
  open: [acme/thread, open]
  opened: [acme/thread, opened]
  state: [acme/thread, state]
  states: [started, review, done]
link:
  host: acme.test
  issue: '^/issue/([0-9]+)'
  fixed: "https://acme.test/issue/{1}"
  thread: "{1}"
`

const sourcePackYAML = `name: acme-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
`

func sourcePackFiles() map[string]string {
	return map[string]string{
		"pack.yaml":      sourcePackYAML,
		"acme.yaml":      cmdDecl,
		"acme/normalize": "#!/bin/sh\necho '{}'\n",
		"acme/thread":    "#!/bin/sh\necho '{}'\n",
	}
}

// A pack of kind source is sources alone: each with a declaration, which
// may name commands, and the files those commands name beside it.
func TestSourcePackLoads(t *testing.T) {
	p := load(t, writePack(t, sourcePackFiles()))
	if p.Kind != pack.KindSource || len(p.Flows) != 0 || len(p.Groups) != 0 || len(p.Crew) != 0 {
		t.Fatalf("pack = %+v", p)
	}
	if len(p.Declared) != 1 || len(p.Sources) != 0 {
		t.Fatalf("declared = %+v, sources = %+v", p.Declared, p.Sources)
	}
	d := p.Declared[0]
	if d.Name != "acme" || d.File != "acme.yaml" || d.Text != cmdDecl {
		t.Fatalf("declared = %+v", d)
	}
	if d.Declaration.Hook == nil || d.Declaration.Thread == nil || d.Declaration.Link == nil {
		t.Fatalf("declaration = %+v", d.Declaration)
	}
	// The commands' files are the source's, copied into the yard: not
	// roles or scripts, so not among the pack's files.
	if len(p.Files()) != 0 {
		t.Fatalf("files = %v", p.Files())
	}
}

func TestSourcePackRefusals(t *testing.T) {
	validFlow := `
flows:
  default:
    stages:
      new: { group: builders }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md }
`
	cases := []struct {
		name     string
		yaml     string            // appended to sourcePackYAML; "" is as it is
		files    map[string]string // changed in the pack; "" deletes
		key, msg string
		line     int
	}{
		{name: "flows", yaml: validFlow, files: map[string]string{"roles/builder.md": "build\n"},
			key: "flows", line: 7, msg: "carries sources alone, and no flows"},
		{name: "groups", yaml: "\ngroups:\n  builders: { agent: roles/builder.md }\n", files: map[string]string{"roles/builder.md": "build\n"},
			key: "groups", line: 7, msg: "carries sources alone, and no groups"},
		{name: "crew", yaml: "\ncrew:\n  boss: { role: roles/boss.md }\n", files: map[string]string{"roles/boss.md": "boss\n"},
			key: "crew", line: 7, msg: "carries sources alone, and no crew"},
		{name: "a stage map beside the declaration",
			yaml: "    as: yardmaster\n",
			key:  "sources.acme.as", line: 6, msg: "has declaration: <file> and nothing else"},
		{name: "says beside the declaration",
			yaml: "    says: []\n",
			key:  "sources.acme.says", line: 6, msg: "hears, says and makes are the flow pack's to say of it"},
		{name: "limits in the declaration",
			files: map[string]string{"acme.yaml": cmdDecl + "limits:\n  command: [/opt/q, \"{provider}\"]\n  kinds: { kimi: kimi }\n"},
			key:   "limits", line: 30, msg: "a declaration in a pack of kind source has"},
		{name: "a missing command file",
			files: map[string]string{"acme/thread": ""},
			key:   "sources.acme.declaration", line: 5, msg: "names the command file acme/thread, which is not in the pack"},
		{name: "a declaration for a built-in name",
			yaml: "  linear:\n    declaration: acme.yaml\n",
			key:  "sources.linear", line: 6, msg: "a declaration is for a source of another name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := sourcePackFiles()
			if c.yaml != "" {
				files["pack.yaml"] += c.yaml
			}
			for name, content := range c.files {
				if content == "" {
					delete(files, name)
				} else {
					files[name] = content
				}
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want the pack's errors", err)
			}
			// The file is the pack's own, or the declaration's for what
			// that says.
			file := "pack.yaml"
			if c.key == "limits" {
				file = "acme.yaml"
			}
			want := filepath.Join(dir, filepath.FromSlash(file))
			if !slices.ContainsFunc(errs, func(e pack.Error) bool {
				return e.File == want && e.Key == c.key && e.Line == c.line && strings.Contains(e.Msg, c.msg)
			}) {
				t.Fatalf("no error at %s:%d %s with %q:\n%v", file, c.line, c.key, c.msg, errs)
			}
		})
	}
}

// A pack of kind source without sources is no pack: it declares at least
// one.
func TestSourcePackWithoutSourcesIsRefused(t *testing.T) {
	for name, yaml := range map[string]string{
		"missing": "name: acme-pack\nkind: source\n",
		"empty":   "name: acme-pack\nkind: source\nsources: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writePack(t, map[string]string{"pack.yaml": yaml})
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) || len(errs) != 1 {
				t.Fatalf("err = %v, want one error", err)
			}
			if e := errs[0]; e.Key != "sources" || !strings.Contains(e.Msg, "declares") {
				t.Fatalf("error = %s", e)
			}
		})
	}
}
