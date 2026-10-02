package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resident show lists the env_file's key names, never its values, and says
// why a runner would refuse the file.
func TestEnvFileSummaryShowsKeysNotValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mayor.env")
	if err := os.WriteFile(p, []byte("LINEAR_API_KEY=lin_api_s3cret\nGH_TOKEN=ghp_s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if envFileSummary(map[string]any{}) != nil {
		t.Fatal("summary without an env_file")
	}
	got := envFileSummary(map[string]any{"env_file": p})
	if s := fmt.Sprint(got); strings.Contains(s, "s3cret") || got["path"] != p ||
		fmt.Sprint(got["keys"]) != "[LINEAR_API_KEY GH_TOKEN]" {
		t.Fatalf("summary = %v", got)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	got = envFileSummary(map[string]any{"env_file": p})
	if e, _ := got["error"].(string); !strings.Contains(e, "chmod 600") || strings.Contains(fmt.Sprint(got), "s3cret") {
		t.Fatalf("refused summary = %v", got)
	}
}
