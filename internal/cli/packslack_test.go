package cli

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/packs"
)

// The slack pack carries its declaration and both scripts: pack check
// names the hook and thread commands with their files, and the apply
// stores the keys and copies the files.
func TestPackApplySlackCarriesDeclarationAndScripts(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	ctx := context.Background()
	srcEnv := envFile(t, "SLACK_BOT_TOKEN=tok-not-to-be-seen\n", 0o600)

	out, err := run(t, a.packCmd(), "check", "slack")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  command hook.normalize: slack/normalize, sha256 ",
		"  command thread.reply: slack/thread, sha256 ",
		"  command thread.posted: slack/thread, sha256 ",
		"  command thread.read: slack/thread, sha256 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, a.packCmd(), "apply", "slack", "--yes", "--source-env", "slack="+srcEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource slack is declared: 2 calls under https://slack.com/api (yardr source show slack)\n",
		"source slack: SLACK_BOT_TOKEN is set in " + srcEnv + "\n",
		"setup notes of pack slack, from SETUP.md (from the pack, not checked by yardr):",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tok-not-to-be-seen") {
		t.Fatalf("pack apply prints the credential:\n%s", out)
	}
	sources := filepath.Join(a.cfg.Home, "sources", "slack")
	if want := "  its commands' files are under " + sources + ", each mode 0700: slack/thread, slack/normalize\n"; !strings.Contains(out, want) {
		t.Errorf("pack apply lacks %q:\n%s", want, out)
	}
	src, err := a.st.GetSource(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	if d.Hook == nil || d.Hook.Verify.Scheme != "hmac-sha256" || d.Hook.Verify.Signed != "v0:{timestamp}:{body}" ||
		d.Hook.Verify.Delivery.Body != "event_id" || d.Hook.Challenge == nil ||
		len(d.Hook.Keys) != 4 || d.Hook.Keys[0] != "channel" || d.Hook.Keys[3] != "room" {
		t.Errorf("the stored source hears nothing as the pack says: %+v", d.Hook)
	}
	if d.Thread == nil || len(d.Thread.Reply) == 0 || len(d.Thread.Posted) == 0 || len(d.Thread.Read) == 0 {
		t.Errorf("the stored source answers nowhere: %+v", d.Thread)
	}
	if d.Notify != nil {
		t.Errorf("the stored source tells where the pack says it does not: notify %+v", d.Notify)
	}
	if h := d.Hear; h == nil || h.Field != "sender" || h.By != "identity" {
		t.Errorf("the stored source hears as the pack says it does not: hear %+v, want field sender by identity", d.Hear)
	}
	if d.Check != "whoami" {
		t.Errorf("the stored source checks nothing as the pack says: %q", d.Check)
	}
	for _, f := range []string{"slack/normalize", "slack/thread"} {
		fi, err := os.Stat(filepath.Join(sources, f))
		if err != nil {
			t.Errorf("the copy of %s: %v", f, err)
		} else if fi.Mode().Perm() != 0o700 {
			t.Errorf("the copy of %s is mode %o, want 700", f, fi.Mode().Perm())
		}
	}

	out, err = run(t, a.sourceCmd(), "show", "slack")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  check: the call whoami (yardr source check slack)\n",
		"    answers user_id, team_id, bot_id\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("source show lacks %q:\n%s", want, out)
		}
	}

	// The same apply again changes nothing.
	out, err = run(t, a.packCmd(), "apply", "slack", "--yes", "--source-env", "slack="+srcEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "pack slack is already applied to the yard: nothing changed\n") {
		t.Errorf("the second apply says it changed something:\n%s", out)
	}
}

// yardr source check slack against a stand-in auth.test prints the
// check call's selected fields, one per line in the form the setup's
// step 4 shows, and carries the same three under answer with --json.
// The declaration is the pack's own, with its base pointed at the
// stand-in; fields the answer carries beyond the selection are shown
// nowhere, and the credential is in neither output.
func TestSourceCheckSlackPrintsSelectedFields(t *testing.T) {
	t.Parallel()
	const token = "xoxb-not-to-be-seen"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth.test" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"user_id":"U999BOT99","team_id":"T012AB3CD","bot_id":"B012AB3CD","user":"yardr","team":"hooli"}`))
	}))
	defer srv.Close()
	a := openApp(t)
	dir := t.TempDir()
	sub, err := fs.Sub(packs.FS, "slack")
	if err != nil {
		t.Fatal(err)
	}
	// The pack as the yard installed it, with its base at the stand-in.
	if err := os.CopyFS(dir, sub); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"normalize", "thread"} {
		if err := os.Chmod(filepath.Join(dir, "slack", script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	decl := filepath.Join(dir, "slack.yaml")
	text, err := os.ReadFile(decl)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "base: https://slack.com/api") {
		t.Fatalf("the pack's declaration has no base to point at the stand-in")
	}
	if err := os.WriteFile(decl, []byte(strings.Replace(string(text), "base: https://slack.com/api", "base: "+srv.URL, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "add", decl); err != nil {
		t.Fatal(err)
	}
	env := envFile(t, "SLACK_BOT_TOKEN="+token+"\n", 0o600)
	if _, err := run(t, a.sourceCmd(), "set", "slack", "env_file="+env); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, a.sourceCmd(), "check", "slack")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"slack: check call whoami (POST /auth.test): 200 in ",
		"slack: user_id: U999BOT99\n",
		"slack: team_id: T012AB3CD\n",
		"slack: bot_id: B012AB3CD\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("source check lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{token, "hooli", "yardr", "slack: ok: "} {
		if strings.Contains(out, not) {
			t.Errorf("source check shows %q:\n%s", not, out)
		}
	}

	a.json = true
	defer func() { a.json = false }()
	out, err = run(t, a.sourceCmd(), "check", "slack")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got struct {
		Answer map[string]any `json:"answer"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("source check --json is no JSON: %v\n%s", err, out)
	}
	want := map[string]any{"user_id": "U999BOT99", "team_id": "T012AB3CD", "bot_id": "B012AB3CD"}
	if len(got.Answer) != len(want) {
		t.Fatalf("answer = %v, want %v", got.Answer, want)
	}
	for k, v := range want {
		if got.Answer[k] != v {
			t.Fatalf("answer = %v, want %v", got.Answer, want)
		}
	}
	if strings.Contains(out, token) {
		t.Errorf("source check --json shows the credential:\n%s", out)
	}
}
