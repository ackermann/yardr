package hooks_test

import (
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
)

// A declared source folds identity keys when its identity says so; one
// without keeps them as typed. A builtin source folds as before.
func TestKeyFolderForDeclared(t *testing.T) {
	text := func(identity string) string {
		return "name: acme\n" + identity + "base: https://acme.test\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	}
	folded, err := source.Parse("acme.yaml", []byte(text("identity:\n  fold: lower\n")))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := source.Parse("acme.yaml", []byte(text("")))
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := hooks.KeyFolderFor("acme", folded); !ok || k.FoldKey("YardMaster") != "yardmaster" {
		t.Errorf("folded: %v, %v", k, ok)
	}
	for name, d := range map[string]*source.Declaration{"acme": plain, "acme-nil": nil, "other": folded} {
		src := name
		if src == "acme-nil" {
			src = "acme"
		}
		if k, ok := hooks.KeyFolderFor(src, d); ok {
			t.Errorf("%s: folds with %v", name, k)
		}
	}
	if k, ok := hooks.KeyFolderFor("github", nil); !ok || k.FoldKey("OctoCat") != "octocat" {
		t.Errorf("github: %v, %v", k, ok)
	}
	if _, ok := hooks.KeyFolderFor("token", nil); ok {
		t.Error("token folds")
	}
}
