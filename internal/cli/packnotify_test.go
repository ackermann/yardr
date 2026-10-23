package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// An apply with --source-env and --notify finishes what the pack declares
// but cannot carry: the source's file and the user told through it. One
// apply, and yardr user list shows the user told through the source, with
// nothing still open and no secret in anything printed.
func TestPackApplyNotifySetsSourceAndUser(t *testing.T) {
	t.Parallel()
	const secret, address = "tok-not-to-be-seen", "u9f2k4xq71ab03cd55ef8gh2ij6kl9"
	a := openApp(t)
	ctx := context.Background()
	srcEnv := envFile(t, "PUSHOVER_TOKEN="+secret+"\n", 0o600)
	userEnv := envFile(t, "PUSHOVER_USER="+address+"\n", 0o600)

	out, err := run(t, a.packCmd(), "apply", "pushover", "--yes",
		"--source-env", "pushover="+srcEnv, "--notify", "me=pushover,"+userEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)\n",
		"source pushover: PUSHOVER_TOKEN is set in " + srcEnv + "\n",
		"user me: told outside the yard: through pushover (PUSHOVER_USER is set in " + userEnv + ")\n",
		"setup notes of pack pushover, from SETUP.md (from the pack, not checked by yardr):",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "next, for source") {
		t.Errorf("pack apply still owes a next step:\n%s", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, address) {
		t.Fatalf("pack apply prints a secret:\n%s", out)
	}

	if out, err := run(t, a.userCmd(), "list"); err != nil || !strings.Contains(out, "through pushover (PUSHOVER_USER is set in ") {
		t.Fatalf("user list hides where me is told: %q %v", out, err)
	}
	if out, err := run(t, a.userCmd(), "show", "me"); err != nil || !strings.Contains(out, "told outside the yard: through pushover (PUSHOVER_USER is set in ") {
		t.Fatalf("user show hides where me is told: %q %v", out, err)
	}
	out, err = run(t, a.packCmd(), "check", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  unchanged        pushover\n", "  ok       env file     PUSHOVER_TOKEN is set in ", "  ok       notify       tells me\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the second pack check lacks %q:\n%s", want, out)
		}
	}

	// The same command again changes nothing: the sets are already as
	// they would be, so nothing is written and no event is logged.
	events := func() int {
		t.Helper()
		evs, err := a.st.Events(ctx, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, e := range evs {
			if e.Kind == "user_set" {
				n++
			}
		}
		return n
	}
	before := events()
	out, err = run(t, a.packCmd(), "apply", "pushover", "--yes",
		"--source-env", "pushover="+srcEnv, "--notify", "me=pushover,"+userEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "pack pushover is already applied to the yard: nothing changed\n") {
		t.Errorf("the second apply says it changed something:\n%s", out)
	}
	if !strings.Contains(out, "user me: told outside the yard: through pushover (PUSHOVER_USER is set in ") {
		t.Errorf("the second apply hides where me is told:\n%s", out)
	}
	if got := events(); got != before {
		t.Errorf("the second apply logged %d user_set events, want %d", got, before)
	}
}

// A source the pack brings may bring its own mint: after --source-env
// sets the mint's inputs, the apply result says the copied command that
// will mint with them, even though SetSource's write response has no
// computed command of its own.
func TestPackApplySourceEnvReportsPackMint(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	env := envFile(t, "GOOGLE_ACCOUNT=op@example.com\n", 0o600)

	out, err := run(t, a.packCmd(), "apply", "google-mail", "--yes", "--source-env", "google-mail="+env)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "source google-mail: GOOGLE_ACCOUNT is set in " + env + ", minted with " + filepath.Join(a.cfg.Home, "sources", "google-mail", "google-mint")
	if !strings.Contains(out, want) {
		t.Fatalf("pack apply does not say the declaration's mint runs:\nwant %q\n%s", want, out)
	}
	if strings.Contains(out, "no mint_command is set") {
		t.Fatalf("pack apply says a mint command is missing:\n%s", out)
	}
}

// The telegram pack applies the same way: the source with the token in
// its path, and the user told through it.
func TestPackApplyNotifyTelegram(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	srcEnv := envFile(t, "TELEGRAM_BOT_TOKEN=tok-not-to-be-seen\n", 0o600)
	userEnv := envFile(t, "TELEGRAM_CHAT_ID=987654321\n", 0o600)

	out, err := run(t, a.packCmd(), "check", "telegram")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"pack telegram (embedded in yardr ",
		", kind source, into the yard",
		"\ndeclared sources\n  create           telegram\n",
		"its calls go to https://api.telegram.org: message (update)",
		"missing  env file",
		"missing  notify",
		"@BotFather",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, a.packCmd(), "apply", "telegram", "--yes",
		"--source-env", "telegram="+srcEnv, "--notify", "me=telegram,"+userEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource telegram is declared: 3 calls under https://api.telegram.org (yardr source show telegram)\n",
		"source telegram: TELEGRAM_BOT_TOKEN is set in " + srcEnv + "\n",
		"user me: told outside the yard: through telegram (TELEGRAM_CHAT_ID is set in " + userEnv + ")\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "next, for source") {
		t.Errorf("pack apply still owes a next step:\n%s", out)
	}
	if out, err := run(t, a.userCmd(), "list"); err != nil || !strings.Contains(out, "through telegram (TELEGRAM_CHAT_ID is set in ") {
		t.Fatalf("user list hides where me is told: %q %v", out, err)
	}
}

// The telegram pack carries its hook, thread and hear keys: pack check
// names the hook and thread commands with their files, and the apply
// stores the keys and copies the files.
func TestPackApplyTelegramCarriesHookThreadHear(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	ctx := context.Background()
	srcEnv := envFile(t, "TELEGRAM_BOT_TOKEN=tok-not-to-be-seen\n", 0o600)
	userEnv := envFile(t, "TELEGRAM_CHAT_ID=987654321\n", 0o600)

	out, err := run(t, a.packCmd(), "check", "telegram")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  command hook.normalize: telegram/normalize, sha256 ",
		"  command thread.reply: telegram/thread, sha256 ",
		"  command thread.posted: telegram/thread, sha256 ",
		"  command thread.read: telegram/thread, sha256 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, a.packCmd(), "apply", "telegram", "--yes",
		"--source-env", "telegram="+srcEnv, "--notify", "me=telegram,"+userEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sources := filepath.Join(a.cfg.Home, "sources", "telegram")
	if want := "  its commands' files are under " + sources + ", each mode 0700: telegram/thread, telegram/normalize\n"; !strings.Contains(out, want) {
		t.Errorf("pack apply lacks %q:\n%s", want, out)
	}
	src, err := a.st.GetSource(ctx, "telegram")
	if err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	if d.Hook == nil || d.Hook.Verify.Scheme != "secret" || d.Hook.Verify.Delivery.Body != "update_id" ||
		len(d.Hook.Keys) != 1 || d.Hook.Keys[0] != "chat" {
		t.Errorf("the stored source hears nothing as the pack says: %+v", d.Hook)
	}
	if d.Thread == nil || len(d.Thread.Reply) == 0 || len(d.Thread.Posted) == 0 || len(d.Thread.Read) == 0 {
		t.Errorf("the stored source answers nowhere: %+v", d.Thread)
	}
	if d.Hear == nil || d.Hear.Field != "chat" || d.Hear.Address.Env != "TELEGRAM_CHAT_ID" || d.Hear.From != "operator" {
		t.Errorf("the stored source hears every chat: %+v", d.Hear)
	}
	for _, f := range []string{"telegram/normalize", "telegram/thread"} {
		fi, err := os.Stat(filepath.Join(sources, f))
		if err != nil {
			t.Errorf("the copy of %s: %v", f, err)
		} else if fi.Mode().Perm() != 0o700 {
			t.Errorf("the copy of %s is mode %o, want 700", f, fi.Mode().Perm())
		}
	}

	out, err = run(t, a.sourceCmd(), "show", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  command thread.reply: telegram/thread reply\n",
		"  command thread.posted: telegram/thread posted\n",
		"  command thread.read: telegram/thread read\n",
		"  command hook.normalize: telegram/normalize\n",
		"  hook.verify: secret in the header X-Telegram-Bot-Api-Secret-Token, the hook's secret itself, compared as it arrives\n",
		"  hook.bodies: unique\n",
		"  hook.keys: chat\n",
		"  hear: keeps a delivery whose chat is the TELEGRAM_CHAT_ID of an operator's env_file; anything else is dropped after verification\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("source show lacks %q:\n%s", want, out)
		}
	}
}

// The telegram pack's declaration sends what the service takes: the token
// in the path as it is, the chat id and the one text in the body. The
// stand-in stands in for the service; nothing here reaches it.
func TestTelegramPackDeclarationSends(t *testing.T) {
	t.Parallel()
	const cred, chat = "110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", "987654321"
	var uri string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri = r.RequestURI
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()
	text, err := fs.ReadFile(packs.FS, "telegram/telegram.yaml")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "telegram.yaml")
	// The base line alone: the file's comments name the service's
	// address too, and they stay as they are.
	replaced := strings.Replace(string(text), "base: https://api.telegram.org", "base: "+srv.URL, 1)
	if replaced == string(text) {
		t.Fatal("the pack's declaration has no base line to point at the stand-in")
	}
	if err := os.WriteFile(file, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}
	// The commands' files beside it, as the pack carries them: source add
	// refuses a declaration whose commands name files that are not there.
	for _, name := range []string{"telegram/normalize", "telegram/thread"} {
		script, err := fs.ReadFile(packs.FS, "telegram/"+name)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(filepath.Dir(file), name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := openApp(t)
	if _, err := run(t, a.sourceCmd(), "add", file); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.sourceCmd(), "set", "telegram", "env_file="+envFile(t, "TELEGRAM_BOT_TOKEN="+cred+"\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.userCmd(), "set", "me", "notify=telegram", "env_file="+envFile(t, "TELEGRAM_CHAT_ID="+chat+"\n", 0o600)); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.sourceCmd(), "check", "telegram", "--user", "me")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "telegram: told me: 200 in ") {
		t.Fatalf("source check: %q", out)
	}
	if uri != "/bot"+cred+"/sendMessage" {
		t.Fatalf("the service was sent to %q", uri)
	}
	textBody, _ := body["text"].(string)
	if body["chat_id"] != chat || !strings.HasPrefix(textBody, "yardr: a test\n") || len(body) != 2 {
		t.Fatalf("the service was sent %v", body)
	}
	if strings.Contains(out, cred) || strings.Contains(out, chat) {
		t.Fatalf("source check prints a secret: %q", out)
	}
}

// A flag the import cannot honor fails it before anything is written: the
// source is not added, and no user is set.
func TestPackApplyNotifyRefusesBeforeWriting(t *testing.T) {
	t.Parallel()
	dir, _ := widgetPack(t, "https://widget.example.com")
	for _, tc := range []struct {
		name string
		pack string
		args []string
		want string
	}{
		{"source-env without a file", "pushover", []string{"--source-env", "pushover"}, `--source-env "pushover": want <source>=<file>`},
		{"source-env without a source", "pushover", []string{"--source-env", "=x"}, `--source-env "=x": want <source>=<file>`},
		{"source-env for no declared source", "pushover", []string{"--source-env", "crm=x"}, `--source-env "crm=x": the pack declares no source crm`},
		{"source-env for a missing file", "pushover", []string{"--source-env", "pushover=" + filepath.Join(t.TempDir(), "missing")}, "no such file"},
		{"source-env for a file others can read", "pushover", []string{"--source-env", "pushover=" + envFile(t, "PUSHOVER_TOKEN=x\n", 0o644)}, "chmod 600"},
		{"source-env for a file without the name", "pushover", []string{"--source-env", "pushover=" + envFile(t, "ELSE=x\n", 0o600)}, "does not set PUSHOVER_TOKEN"},
		{"notify without a source", "pushover", []string{"--notify", "me"}, `--notify "me": want <user>=<source>[,<envfile>]`},
		{"notify without a user", "pushover", []string{"--notify", "=pushover"}, `--notify "=pushover": want <user>=<source>[,<envfile>]`},
		{"notify for no declared source", "pushover", []string{"--notify", "me=crm"}, `--notify "me=crm": the pack declares no source crm`},
		{"notify for no user", "pushover", []string{"--notify", "ada=pushover"}, "--notify ada=pushover: no user ada (yardr user add ada)"},
		{"notify for a file without the address", "pushover", []string{"--notify", "me=pushover," + envFile(t, "ELSE=x\n", 0o600)}, "does not set PUSHOVER_USER"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := openApp(t)
			args := append([]string{"apply", tc.pack, "--yes"}, tc.args...)
			out, err := run(t, a.packCmd(), args...)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.want, out)
			}
			if _, err := a.st.GetSource(context.Background(), tc.pack); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("the refused import wrote the source: %v", err)
			}
			if u, _ := a.st.GetUser(context.Background(), "me"); u.Notify != "" {
				t.Fatalf("the refused import told me through %s", u.Notify)
			}
		})
	}
	// A source without a notify key cannot tell a user, through the flags
	// or otherwise.
	a := openApp(t)
	out, err := run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes", "--notify", "me=widget")
	if err == nil || !strings.Contains(err.Error(), "--notify me=widget: the source cannot tell a user: its file has no notify key") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if _, err := a.st.GetSource(context.Background(), "widget"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused import wrote the source: %v", err)
	}
}

// Setting a source's file is the operator's, and telling a user theirs or
// the operator's: run by anyone else the import does the rest and sets
// neither.
func TestPackApplyNotifyByAnother(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	if _, err := run(t, a.userCmd(), "add", "ada"); err != nil {
		t.Fatal(err)
	}
	a.actor = "web-builder-1"
	srcEnv := envFile(t, "PUSHOVER_TOKEN=x\n", 0o600)
	userEnv := envFile(t, "PUSHOVER_USER=u9f2k4xq71ab03cd55ef8gh2ij6kl9\n", 0o600)
	out, err := run(t, a.packCmd(), "apply", "pushover", "--yes",
		"--source-env", "pushover="+srcEnv, "--notify", "ada=pushover,"+userEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"the source pushover was not added: adding a source is the operator's (me), and this is web-builder-1. The rest of the import does not depend on it\n",
		"the env_file of source pushover was not set: the source was not added (adding a source is the operator's (me), and this is web-builder-1)\n",
		"user ada was not told through pushover: the source was not added (adding a source is the operator's (me), and this is web-builder-1)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "next, for source pushover") {
		t.Errorf("a source that was not added has nothing to set next:\n%s", out)
	}
	if u, _ := a.st.GetUser(context.Background(), "ada"); u.Notify != "" || u.EnvFile != "" {
		t.Fatalf("the refused import set ada: %+v", u)
	}

	// Even the user themselves cannot be told through a source this import
	// was not allowed to add. The dependent set is skipped with the source
	// refusal, and the import succeeds with neither setting written.
	b := openApp(t)
	if _, err := run(t, b.userCmd(), "add", "ada"); err != nil {
		t.Fatal(err)
	}
	b.actor = "ada"
	out, err = run(t, b.packCmd(), "apply", "pushover", "--yes", "--notify", "ada=pushover")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"the source pushover was not added: adding a source is the operator's (me), and this is ada.",
		"user ada was not told through pushover: the source was not added (adding a source is the operator's (me), and this is ada)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if u, _ := b.st.GetUser(context.Background(), "ada"); u.Notify != "" || u.EnvFile != "" {
		t.Fatalf("the skipped notify was written: %+v", u)
	}
}

// Each flag stands on its own: what one set is said, and what is still
// open is said fresh, with the commands that are owed.
func TestPackApplyNotifyPartial(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	srcEnv := envFile(t, "PUSHOVER_TOKEN=x\n", 0o600)
	out, err := run(t, a.packCmd(), "apply", "pushover", "--yes", "--source-env", "pushover="+srcEnv)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "source pushover: PUSHOVER_TOKEN is set in "+srcEnv+"\n") {
		t.Errorf("pack apply hides what --source-env set:\n%s", out)
	}
	if strings.Contains(out, "next, for source pushover: no env_file is set") {
		t.Errorf("pack apply owes the env_file it just set:\n%s", out)
	}
	if !strings.Contains(out, "next, for source pushover: it can tell a user outside the yard and no user names it.") {
		t.Errorf("pack apply lost what is still open:\n%s", out)
	}

	b := openApp(t)
	out, err = run(t, b.packCmd(), "apply", "pushover", "--yes", "--notify", "me=pushover")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "user me: told outside the yard: through pushover, which is not set up: ") {
		t.Errorf("pack apply hides that me has no address yet:\n%s", out)
	}
	if !strings.Contains(out, "next, for source pushover: no env_file is set for it") {
		t.Errorf("pack apply lost what is still open:\n%s", out)
	}
}

// The pack's setup notes without a file written by hand: one apply with
// --notify and no file, which says the command that gives the address, then
// yardr source key and yardr user key. After them the pack has nothing
// open, and no key is in anything printed.
func TestPackApplyNotifyThenTheTwoKeys(t *testing.T) {
	t.Parallel()
	const secret, address = "tok-not-to-be-seen", "u9f2k4xq71ab03cd55ef8gh2ij6kl9"
	a := openApp(t)
	out, err := run(t, a.packCmd(), "apply", "pushover", "--yes", "--notify", "me=pushover")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"\nsource pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)\n",
		"user me: told outside the yard: through pushover, which is not set up: user me has no env_file: yardr user key me pushover gives PUSHOVER_USER\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	printed := out
	out, err = keyFrom(t, a, "PUSHOVER_TOKEN="+secret+"\n", "pushover")
	if err != nil || !strings.Contains(out, "source pushover: PUSHOVER_TOKEN is set in ") {
		t.Fatalf("source key: %q, %v", out, err)
	}
	printed += out
	out, err = userKeyFrom(t, a, "PUSHOVER_USER="+address+"\n", "me", "pushover")
	if err != nil || !strings.Contains(out, "user me: PUSHOVER_USER is set in ") ||
		!strings.Contains(out, "next: yardr source check pushover --user me sends one test message\n") {
		t.Fatalf("user key: %q, %v", out, err)
	}
	printed += out
	out, err = run(t, a.packCmd(), "check", "pushover")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  ok       env file     PUSHOVER_TOKEN is set in ", "  ok       notify       tells me\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	printed += out
	if out, err := run(t, a.userCmd(), "show", "me"); err != nil || !strings.Contains(out, "told outside the yard: through pushover (PUSHOVER_USER is set in ") {
		t.Fatalf("user show: %q %v", out, err)
	}
	if strings.Contains(printed, secret) || strings.Contains(printed, address) {
		t.Fatalf("a command prints a key:\n%s", printed)
	}
}
