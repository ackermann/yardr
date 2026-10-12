package hooks

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/source"
)

func TestGitHubPackNormalizeCollapsesUnicodeWhitespace(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "github")
	declaration, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), declaration)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	adapter := Declared{D: d, Home: home}
	body := []byte(`{"repository":{"full_name":"octo/repo"},"issue":{"number":1,"body":"one two three"}}`)
	e, err := adapter.Normalize(http.Header{"X-GitHub-Event": {"issues"}}, body)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "one two three" {
		t.Fatalf("text = %q, want Unicode whitespace collapsed", e.Text)
	}
}
