package hooks_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The store checks a map's words without importing this package.
func TestStatesAreTheStores(t *testing.T) {
	if !reflect.DeepEqual(hooks.States(), store.ThreadStates) {
		t.Fatalf("hooks.States %v, store.ThreadStates %v", hooks.States(), store.ThreadStates)
	}
}

// A declared source has a label for a word when its thread takes
// labels, and where yardr sets none it has not. A label without a name
// is no word anywhere.
func TestLabelPlaces(t *testing.T) {
	d, err := source.Parse("acme.yaml", []byte(acmeThread))
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := source.Parse("acme.yaml", []byte(strings.Replace(acmeThread,
		"  states: [started, review, done]\n", "  states: [started, review, done]\n  labels: true\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"label:in-review", "label:In Review", "label:a:b"} {
		if !hooks.Word(word) {
			t.Errorf("%q is no word", word)
		}
		if err := (hooks.Declared{D: d}).Places(word); err == nil || !hooks.Final(err) || !strings.Contains(err.Error(), "acme: yardr sets no label there") {
			t.Errorf("declared %q: %v", word, err)
		}
		if err := (hooks.Declared{D: tagged}).Places(word); err != nil {
			t.Errorf("declared with labels %q: %v", word, err)
		}
	}
	for _, word := range []string{"label:", "label: x", "label:x ", "Label:x", "labels:x"} {
		if hooks.Word(word) {
			t.Errorf("%q is a word", word)
		}
		if err := (hooks.Declared{D: tagged}).Places(word); err == nil || !strings.Contains(err.Error(), "is no state") {
			t.Errorf("declared with labels %q: %v", word, err)
		}
	}
}

// An issue is named from the fixed form of its URL alone.
func TestIssueThread(t *testing.T) {
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gh, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ source, url, want string }{
		{"github", "https://github.com/acme/webapp/issues/7", "acme/webapp#7"},
		// A pull request a bead tracks is named as an issue is: Forward
		// is what refuses to set its status.
		{"github", "https://github.com/acme/webapp/pull/7", "acme/webapp#7"},
		{"github", "https://github.com/acme/webapp/discussions/7", ""},
		{"github", "https://github.com/acme/webapp/issues/x", ""},
	} {
		f, ok := hooks.ForwarderFor(c.source, gh, "", "")
		if !ok {
			t.Fatalf("%s has no Forwarder", c.source)
		}
		got, err := f.IssueThread(c.url)
		if got != c.want || (err == nil) != (c.want != "") || (err != nil && !hooks.Final(err)) {
			t.Errorf("%s: %q, %v; want %q", c.url, got, err, c.want)
		}
	}
	if got := hooks.Forwardable(); len(got) != 0 {
		t.Fatalf("forwardable %v, want none: no builtin moves issues", got)
	}
	if _, ok := hooks.ForwarderFor("token", nil, "", ""); ok {
		t.Fatal("token has a Forwarder")
	}
	if _, ok := hooks.ForwarderFor("github", nil, "", ""); ok {
		t.Fatal("github without a declaration has a Forwarder")
	}
	f, _ := hooks.ForwarderFor("github", gh, "", "")
	if f.Places("review") == nil || f.Places("done") != nil {
		t.Fatal("places: github has no review")
	}
	// The credential is read from what an env_file sets, by name; with
	// none of the names set the error says so in a way the caller can tell.
	file := map[string]string{"GITHUB_TOKEN": " gh-tok "}
	get := func(name string) string { return file[name] }
	if c, err := f.CredentialFrom(get); err != nil || c != "gh-tok" {
		t.Fatalf("github credential %q, %v", c, err)
	}
	{
		var missing *hooks.MissingCredential
		if _, err := f.CredentialFrom(func(string) string { return "" }); !errors.As(err, &missing) {
			t.Fatalf("nothing set: %v", err)
		}
	}
}
