package isolatedyard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sourcesFuse is how long startSources waits for the stand-in to take an
// address: a fuse against a stand-in that never takes one, not a window its
// start must fit in. The first run of a file written a moment ago waits behind
// the first runs of the other new files on the machine, a few a second, and
// beside a full race run the stand-in's took longer than the 20 s this once
// was (yardr-fwcc).
const sourcesFuse = 2 * time.Minute

// startSources builds the sources stand-in into the yard and starts it,
// returning the address it took. `down` stops it with the rest of the
// yard, by its home.
func startSources(t *testing.T, d string) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(d, "bin", "sources")
	cmd := exec.Command("go", "build", "-o", bin, "./contrib/isolated-yard/sources")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sources: %v\n%s", err, out)
	}
	run := exec.Command(filepath.Join(d, "e"), "sources", "-state", filepath.Join(d, "issues.json"))
	out, err := run.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	run.Stderr = run.Stdout
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		for s := bufio.NewScanner(out); s.Scan(); {
			lines <- s.Text()
		}
	}()
	// The address the stand-in took is its first log line; wait on
	// that effect, not on wall time.
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stand-in ended before it took an address")
			}
			if _, addr, found := strings.Cut(line, "a customer system (/crm/) on "); found {
				return strings.TrimSpace(addr)
			}
		case <-time.After(sourcesFuse):
			t.Fatalf("the stand-in took no address in %s", sourcesFuse)
		}
	}
}

// messagesOf asks the stand-in at addr what messages it took and how
// many were asked for.
func messagesOf(t *testing.T, addr string) ([]map[string]any, int) {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Messages []map[string]any `json:"messages"`
		Asked    int              `json:"asked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.Messages, got.Asked
}

// answerAs has the stand-in answer every message from now on with
// status: 500 to fail, 200 to take them again.
func answerAs(t *testing.T, addr string, status string) {
	t.Helper()
	resp, err := http.PostForm("http://"+addr+"/messages/answer", url.Values{"status": {status}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// An approved source check --user sends the test message once, end to
// end: in a bare isolated yard, a crew member proposes the check, the
// operator approves it on its card, and the stand-in takes one message.
// A send the service answers with no success is said on the approval,
// the ask stays open, and approving again retries it — never touching
// the yard of whoever runs the test.
func TestSourceCheckUserCard(t *testing.T) {
	d := bare(t)
	addr := startSources(t, d)
	as := func(actor string, args ...string) string {
		return in(t, d, append([]string{"env", "YARDR_ACTOR=" + actor, "yardr"}, args...)...)
	}
	as("mckean", "init", "--harness", "claude", "--operator", "mckean")
	decl := "name: push\nbase: http://" + addr + "/1\n" +
		"credential: { env: PUSH_TOKEN }\n" +
		"calls:\n" +
		"  message:\n" +
		"    kind: update\n" +
		"    params: { user: {type: string}, title: {type: string}, text: {type: string} }\n" +
		"    request: { method: POST, path: /messages.json, body: { token: \"{credential}\", user: \"{user}\", title: \"{title}\", message: \"{text}\" } }\n" +
		"notify: { call: message, address: { param: user, env: PUSH_USER }, title: title, text: text }\n"
	file := filepath.Join(t.TempDir(), "push.yaml")
	if err := os.WriteFile(file, []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	as("mckean", "source", "add", file)
	cred := filepath.Join(t.TempDir(), "push.env")
	if err := os.WriteFile(cred, []byte("PUSH_TOKEN=push-standin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	as("mckean", "source", "set", "push", "env_file="+cred)
	userEnv := filepath.Join(t.TempDir(), "mckean.env")
	if err := os.WriteFile(userEnv, []byte("PUSH_USER=mckean-at-push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	as("mckean", "user", "set", "mckean", "env_file="+userEnv)
	bead := strings.Fields(as("yardmaster", "bead", "create", "Try push"))[0]
	as("yardmaster", "bead", "ask", bead, "May the yard send the test message?",
		"--context", "the phone should show the test.",
		"--run", "source check push --user mckean")
	shown := as("mckean", "prime", "--bead", bead)
	for _, want := range []string{
		"yardr source check push --user mckean",
		"it sends one real test message to mckean through the source push",
		"nobody can take it back: a sent test message cannot be unsent",
	} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the card lacks %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, "mckean-at-push") {
		t.Fatalf("the card holds the address:\n%s", shown)
	}
	out, err := approve(d, bead, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "push: told mckean: 200 in ") {
		t.Fatalf("the approval printed:\n%s", out)
	}
	got, asked := messagesOf(t, addr)
	if len(got) != 1 || asked != 1 {
		t.Fatalf("the stand-in took %d messages, asked %d", len(got), asked)
	}
	if got[0]["user"] != "mckean-at-push" || got[0]["title"] != "yardr: a test" ||
		got[0]["message"] != "mckean is told here by the yard, through the source push. Sent by yardr source check push --user mckean." {
		t.Fatalf("the stand-in took %v", got)
	}
	shown = as("mckean", "prime", "--bead", bead)
	if !strings.Contains(shown, "approved, ran: yardr source check push --user mckean") {
		t.Fatalf("the card reads:\n%s", shown)
	}

	// The service fails: the approval says the answer and the retry,
	// the ask stays open, and nothing is taken.
	answerAs(t, addr, "500")
	as("yardmaster", "bead", "ask", bead, "May the yard send the test message again?",
		"--context", "the service failed the first one.",
		"--run", "source check push --user mckean")
	// The approval of the first ask is note 2, so the second ask is 3.
	failed, err := approve(d, bead, 3)
	if err == nil {
		t.Fatalf("the failed approval passed:\n%s", failed)
	}
	for _, want := range []string{"was answered 500", "Approve again to retry"} {
		if !strings.Contains(failed, want) {
			t.Fatalf("the failed approval lacks %q:\n%s", want, failed)
		}
	}
	shown = as("mckean", "prime", "--bead", bead)
	if !strings.Contains(shown, "approve with: yardr bead answer "+bead+" 3 --approve") {
		t.Fatalf("the ask is not open after the failure:\n%s", shown)
	}
	if got, _ := messagesOf(t, addr); len(got) != 1 {
		t.Fatalf("the failed send took %d messages", len(got))
	}
	answerAs(t, addr, "200")
	out, err = approve(d, bead, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "push: told mckean: 200 in ") {
		t.Fatalf("the retry printed:\n%s", out)
	}
	if got, asked := messagesOf(t, addr); len(got) != 2 || asked != 3 {
		t.Fatalf("after the retry the stand-in took %d messages, asked %d", len(got), asked)
	}
	out, err = script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
}
