package isolatedyard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/testload"
)

// runIn is in without the failure: a caller that waits on an effect
// reads the error itself.
func runIn(d string, args ...string) (string, error) {
	out, err := exec.Command(filepath.Join(d, "e"), args...).CombinedOutput()
	return string(out), err
}

// An approved pack apply is carried out by the yard's own server end
// to end (design yardr-bht9 B3): in a bare isolated yard, a crew
// member proposes telegram, the operator approves it, and the source
// stands with its command files copied — never touching the yard of
// whoever runs the test.
func TestCarryPackApplyTelegram(t *testing.T) {
	d := bare(t)
	as := func(actor string, args ...string) string {
		return in(t, d, append([]string{"env", "YARDR_ACTOR=" + actor, "yardr"}, args...)...)
	}
	as("mckean", "init", "--harness", "claude", "--operator", "mckean")
	bead := strings.Fields(as("yardmaster", "bead", "create", "Try telegram"))[0]
	as("yardmaster", "bead", "ask", bead, "Add the telegram source so I can tell you on your phone?",
		"--context", "yardr has no source that tells a user outside the yard.",
		"--run", "pack apply telegram")
	if _, err := approve(d, bead, 1); err != nil {
		t.Fatal(err)
	}

	// The carrying out is the server's, after the approval: the
	// source and its copies stand once it has run, so wait on that
	// effect — the declaration is stored a moment before its files,
	// and the files are copied one at a time, so wait for each copy
	// the test then holds to its mode, not only the first.
	deadline := time.Now().Add(testload.Scale(60 * time.Second))
	var shown string
	for {
		out, err := runIn(d, "env", "YARDR_ACTOR=mckean", "yardr", "source", "show", "telegram")
		_, threadErr := os.Stat(filepath.Join(d, "y", "sources", "telegram", "telegram", "thread"))
		_, normalizeErr := os.Stat(filepath.Join(d, "y", "sources", "telegram", "telegram", "normalize"))
		if err == nil && threadErr == nil && normalizeErr == nil {
			shown = out
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the telegram source never stood: %v\n%s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(shown, "telegram  https://api.telegram.org") {
		t.Fatalf("yardr source show telegram:\n%s", shown)
	}
	for _, prog := range []string{"telegram/thread", "telegram/normalize"} {
		info, err := os.Stat(filepath.Join(d, "y", "sources", "telegram", filepath.FromSlash(prog)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("the copy of %s stands at mode %v, not 0700", prog, info.Mode().Perm())
		}
	}
	// The card flips to ran after the files stand: the carrying out
	// copies them before it records the completion, so the source and
	// its copies can be there while the card still reads as being
	// carried out. Wait on that last effect too, with a deadline of
	// its own: under load the copies' wait may take most of its, and
	// the card must not fail on the intermediate state for that.
	deadline = time.Now().Add(testload.Scale(60 * time.Second))
	for {
		got, err := runIn(d, "env", "YARDR_ACTOR=mckean", "yardr", "prime", "--bead", bead)
		if err == nil && strings.Contains(got, "approved, ran: yardr pack apply telegram") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the carried-out card reads:\n%s", got)
		}
		time.Sleep(200 * time.Millisecond)
	}
	out, err := script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
}
