package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// server status only asks whether the server answers: on a home with no
// yard it says so and creates nothing, not even the home directory.
func TestServerStatusNoYard(t *testing.T) {
	home := newHome(t)
	out, code := executeOut(&app{env: testEnv(t, home)}, "server", "status")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantText(t, out, filepath.Join(home, "yard.db")+" running=false\n")
	if _, err := os.Stat(home); err == nil {
		t.Fatal("server status created the home directory")
	}
}
