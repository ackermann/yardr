package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// summaryT is crew show's reading of cfg's environment, as its JSON
// has it: the fields of each source, by setting.
func summaryT(cfg map[string]any) map[string]map[string]any {
	env, _ := crewEnvSummary(context.Background(), store.CrewMember{Name: "yardmaster", Config: cfg}, "")
	out := map[string]map[string]any{}
	for _, e := range env {
		out[e.Setting] = e.fields()
	}
	return out
}

// crew show lists the env_file's key names, never its values, and says
// why a runner would refuse the file.
func TestEnvFileSummaryShowsKeysNotValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "yardmaster.env")
	if err := os.WriteFile(p, []byte("LINEAR_API_KEY=lin_api_s3cret\nGH_TOKEN=ghp_s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(summaryT(map[string]any{})) != 0 {
		t.Fatal("summary without an env_file")
	}
	got := summaryT(map[string]any{"env_file": p})["env_file"]
	if s := fmt.Sprint(got); strings.Contains(s, "s3cret") || got["path"] != p ||
		fmt.Sprint(got["keys"]) != "[LINEAR_API_KEY GH_TOKEN]" {
		t.Fatalf("summary = %v", got)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	got = summaryT(map[string]any{"env_file": p})["env_file"]
	if e, _ := got["error"].(string); !strings.Contains(e, "chmod 600") || strings.Contains(fmt.Sprint(got), "s3cret") {
		t.Fatalf("refused summary = %v", got)
	}
}
