package hooks_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// linearBodies are the Linear webhook bodies the tests of this package
// already have.
var linearBodies = []string{
	"linear_comment_bodydata.json",
	"linear_comment_create.json",
	"linear_comment_plain_mention.json",
	"linear_issue_create.json",
	"linear_issue_delegate_set.json",
	"linear_issue_delegated.json",
	"linear_issue_update.json",
}

// The proof of yardr-x0bh: the declared Linear source (packs/linear)
// receives what Linear{} does. Each body above goes through both Linear{}.
// Normalize and the declaration's normalize command, and the envelopes
// compare equal, but for the source, which the core sets itself.
func TestLinearDeclaredMatchesLinear(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the declared source's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "linear-declared" || d.Hook == nil {
		t.Fatalf("declaration = %+v", d)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	ad := hooks.Declared{D: d, Home: home}
	may := append(slices.Clone(store.WhereKeys), d.Hook.Keys...)
	for _, name := range linearBodies {
		body := fixture(t, name)
		want, err := hooks.Linear{}.Normalize(nil, body)
		if err != nil {
			t.Fatalf("%s: Linear{}.Normalize: %v", name, err)
		}
		got, err := ad.Normalize(http.Header{"Linear-Delivery": {"d-1"}}, body)
		if err != nil {
			t.Fatalf("%s: declared Normalize: %v", name, err)
		}
		want.Source = "linear-declared" // the core sets the source itself
		if !reflect.DeepEqual(got, want) {
			g, _ := json.MarshalIndent(got, "", "  ")
			w, _ := json.MarshalIndent(want, "", "  ")
			t.Errorf("%s: declared envelope differs:\n got %s\nwant %s", name, g, w)
		}
		for k := range got.Fields {
			if !slices.Contains(may, k) {
				t.Errorf("%s: field %q is in neither hook.keys nor store.WhereKeys", name, k)
			}
		}
	}
}
