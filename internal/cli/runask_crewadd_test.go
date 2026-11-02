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

// bead ask --run "crew add ..." carries its role text from --run-role,
// or from the file prompt= names, beside the ask: prime shows the
// weighty card whole, a file changed after the ask changes nothing, and
// the weighed approval adds the member with exactly those bytes.
func TestBeadAskRunCrewAdd(t *testing.T) {
	t.Parallel()
	key := airyKey(t)
	y, b := runYardCLI(t, "crewadd", key)
	ctx := context.Background()
	const role = "You are the porter.\n\nCarry the bags carefully and note what each holds.\nBring decisions to the operator.\n"
	roleFile := filepath.Join(t.TempDir(), "porter.md")
	if err := os.WriteFile(roleFile, []byte(role), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := y.as(t, "yardmaster", "ask", b.ID, "Add the porter?",
		"--context", "the yard needs a porter", "--run", "crew add porter kind=claude model=opus preset=bypass", "--run-role", roleFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + b.ID + " for jo: to approve, yardr crew add porter kind=claude model=opus preset=bypass\n"; out != want {
		t.Fatalf("bead ask --run crew add printed %q, want %q", out, want)
	}
	shown, err := run(t, y.a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"WEIGHTY · code:",
		"adds the crew member porter on claude",
		"Permissions: bypass",
		"It skips permissions: it runs without asking for permission.",
		"Carry the bags carefully and note what each holds.",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("prime lacks %q:\n%s", want, shown)
		}
	}
	// The file changed after the ask changes nothing.
	if err := os.WriteFile(roleFile, []byte("changed after the ask\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = y.as(t, "jo", "answer", b.ID, "1", "--approve")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "crew member porter added") {
		t.Fatalf("the approval printed:\n%s", out)
	}
	m, err := y.st.GetCrew(ctx, "porter")
	if err != nil {
		t.Fatal(err)
	}
	if m.Config["kind"] != "claude" || m.Config["model"] != "opus" || m.Config["prompt"] != filepath.Join(m.Dir, "roles", "porter.md") {
		t.Fatalf("the member holds %+v", m)
	}
	got, err := os.ReadFile(filepath.Join(m.Dir, "roles", "porter.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != role {
		t.Fatalf("role file = %q, want the bytes as asked, not the changed file", got)
	}

	// prompt= names the file the same way, without --run-role.
	const role2 = "You are the clerk.\n\nFile what arrives.\n"
	role2File := filepath.Join(t.TempDir(), "clerk.md")
	if err := os.WriteFile(role2File, []byte(role2), 0o600); err != nil {
		t.Fatal(err)
	}
	out2, err := y.as(t, "yardmaster", "ask", b.ID, "Add the clerk?",
		"--context", "the yard needs a clerk", "--run", "crew add clerk kind=claude model=sonnet preset=auto prompt="+role2File)
	if err != nil {
		t.Fatal(err)
	}
	var seq2 int
	if _, err := fmt.Sscanf(out2, "ask %d", &seq2); err != nil {
		t.Fatalf("second ask printed %q: %v", out2, err)
	}
	if _, err := y.as(t, "jo", "answer", b.ID, fmt.Sprint(seq2), "--approve"); err != nil {
		t.Fatal(err)
	}
	m2, err := y.st.GetCrew(ctx, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	got2, err := os.ReadFile(filepath.Join(m2.Dir, "roles", "clerk.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != role2 {
		t.Fatalf("clerk role file = %q, want %q", got2, role2)
	}
	invalidRole := filepath.Join(t.TempDir(), "invalid.md")
	if err := os.WriteFile(invalidRole, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Add the cook?", "--context", "the yard needs a cook",
		"--run", "crew add cook kind=claude", "--run-role", invalidRole); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("crew add with an invalid UTF-8 role file = %v, want it refused before JSON encoding", err)
	}
	longRole := filepath.Join(t.TempDir(), "long.md")
	if err := os.WriteFile(longRole, []byte(strings.Repeat("é", store.MaxRunCrewRole/2+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.as(t, "yardmaster", "ask", b.ID, "Add the cook?", "--context", "the yard needs a cook",
		"--run", "crew add cook kind=claude preset=auto", "--run-role", longRole); err == nil ||
		!strings.Contains(err.Error(), "yardr crew add cook") || !strings.Contains(err.Error(), "auto preset's flags as its args") {
		t.Errorf("crew add with a role past the card bound = %v, want the terminal command and preset route", err)
	}
}
