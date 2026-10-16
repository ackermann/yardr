package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hookLinear declares the hook of a Linear-like source: deliveries
// signed with a secret from a file, read into one envelope.
const hookLinear = `name: linear
base: https://api.linear.app
credential: { env: LINEAR_API_KEY }
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Linear-Signature, encoding: hex }
    delivery: { header: Linear-Delivery }
    timestamp: { body: webhookTimestamp, unit: ms, window: 1m }
  bodies: unique
  normalize: [linear/normalize]
  keys: [team, project, label]
calls:
  viewer: { kind: read, request: { method: GET, path: /viewer } }
`

const hookSecretValue = "whsec-test-signing-secret-0123456789"

func hookYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "linear.yaml", hookLinear, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutSource(y.ctx, "crm.yaml", runCRM, "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func hookSecretAsk(source, hook string) Ask {
	return Ask{Question: "The signing secret for it?", To: "mckean",
		Context: []string{"it is shown once where the webhook is made"},
		Secret:  &AskSecret{Source: source, Hook: hook}}
}

func hookRunAsk(command string) Ask {
	return Ask{Question: "Set up the webhook?", To: "mckean",
		Context: []string{"the signing secret is asked for on the ask before this one; give it first"},
		Run:     &AskRun{Argv: []string{command}}}
}

// A hook is set up by two asks on one bead, the secret first: the
// operator gives the signing secret into the file the yard owns for
// the hook, then approves the hook that names it. The value stands in
// that file alone: in no note, event or answer, and nowhere in the
// store file.
func TestHookAskSetup(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := hookYard(t)
	secretSeq := y.ask(t, hookSecretAsk("linear", "linear"), "yardmaster")
	runSeq := y.ask(t, hookRunAsk("hook add linear --source linear"), "yardmaster")
	if secretSeq >= runSeq {
		t.Fatalf("the secret ask %d is not before the hook ask %d", secretSeq, runSeq)
	}

	secretAsk := y.asked(t, secretSeq)
	if secretAsk.Ask.Secret == nil || secretAsk.Ask.Secret.Hook != "linear" ||
		secretAsk.Ask.Secret.Source != "linear" ||
		!slices.Equal(secretAsk.Ask.Secret.Env, []string{"LINEAR_SIGNING_SECRET"}) {
		t.Fatalf("the secret ask stores %+v", secretAsk.Ask.Secret)
	}
	if lines := strings.Join(secretAsk.Lines(), "\n"); !strings.Contains(lines, "a secret: the signing secret for the hook linear (POST /hooks/linear), for the source linear (https://api.linear.app)") {
		t.Fatalf("the secret card:\n%s", lines)
	}
	runAsk := y.asked(t, runSeq)
	if runAsk.Ask.Run == nil || !slices.Equal(runAsk.Ask.Run.Argv, []string{"hook", "add", "linear", "--source", "linear"}) {
		t.Fatalf("the run ask stores %+v", runAsk.Ask.Run)
	}
	does := strings.Join(runAsk.Ask.Run.Does, "\n")
	for _, want := range []string{
		"it adds the hook linear: POST /hooks/linear takes deliveries from the source linear (https://api.linear.app).",
		"what it sends in: deliveries are verified by HMAC-SHA256 over the body (scheme hmac-sha256, signed: body), the signature in the header Linear-Signature as hex past the prefix none",
		"the signing secret is given on the hook's own secret card for linear and kept in " + HookSecretFile(y.home, "linear") + " (mode 0600); the hook names that file, and the value is in no note, event or answer.",
	} {
		if !strings.Contains(does, want) {
			t.Fatalf("the card says:\n%s\nwant %q", does, want)
		}
	}
	if runAsk.Ask.Run.Description != "Create a webhook endpoint POST /hooks/<name> (a replacement stays in a terminal)" ||
		runAsk.Ask.Run.Back != "yardr hook rm linear" {
		t.Fatalf("the run ask = %+v", runAsk.Ask.Run)
	}

	// The hook runs only with its secret standing: approving first
	// refuses and makes nothing.
	if _, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "give its signing secret on the hook's secret card for linear first") {
		t.Fatalf("approving before the giving = %v", err)
	}
	if _, err := y.GetHook(y.ctx, "linear"); err == nil {
		t.Fatal("the refused approval made the hook")
	}

	keyed, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
		map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	file := HookSecretFile(y.home, "linear")
	if keyed.Hook != "linear" || keyed.File != file || !slices.Equal(keyed.Given, []string{"LINEAR_SIGNING_SECRET"}) {
		t.Fatalf("keyed = %+v", keyed)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("the secret file is mode %04o, want 0600", st.Mode().Perm())
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != hookSecretValue {
		t.Fatalf("the secret file holds %q, %v", raw, err)
	}
	if lines := strings.Join(y.asked(t, secretSeq).Lines(), "\n"); !strings.Contains(lines, "mckean gave LINEAR_SIGNING_SECRET") {
		t.Fatalf("the given card:\n%s", lines)
	}

	done, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "hook linear saved: POST /hooks/linear (linear)" {
		t.Fatalf("the approval answers %+v", done)
	}
	hook, err := y.GetHook(y.ctx, "linear")
	if err != nil {
		t.Fatal(err)
	}
	if hook.Source != "linear" || hook.SecretFile != file || hook.Blocked != "" {
		t.Fatalf("the hook = %+v", hook)
	}

	// The value is in no note, event or answer, and nowhere in the
	// store file: the file alone holds it.
	notes, err := y.Notes(y.ctx, y.bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, hookSecretValue) {
			t.Fatalf("note %d holds the secret", n.Seq)
		}
	}
	evs, err := y.EventsOfKind(y.ctx, "hook_added", 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("hook_added events = %+v, %v", evs, err)
	}
	y.approved(t, evs[0], runSeq)
	if raw, err := os.ReadFile(filepath.Join(y.home, "y.db")); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(raw, []byte(hookSecretValue)) {
		t.Fatal("the store file holds the secret")
	}
}

// A private file left at the conventional path does not stand in for the
// secret ask on this bead, and a secret asked for another source cannot
// authorize this hook.
func TestHookAskNeedsItsAnsweredSecretCard(t *testing.T) {
	t.Run("preexisting file", func(t *testing.T) {
		y := hookYard(t)
		file := HookSecretFile(y.home, "linear")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(hookSecretValue), 0o600); err != nil {
			t.Fatal(err)
		}
		seq := y.ask(t, hookRunAsk("hook add linear --source linear"), "yardmaster")
		if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean"); err == nil ||
			!strings.Contains(err.Error(), "answered ask for source linear on this bead") {
			t.Fatalf("approval with only a preexisting file = %v", err)
		}
		if _, err := y.GetHook(y.ctx, "linear"); err == nil {
			t.Fatal("approval without its secret card made a hook")
		}
	})

	t.Run("wrong source", func(t *testing.T) {
		y := hookYard(t)
		if _, err := y.PutSource(y.ctx, "tracker.yaml", trackerDecl, "mckean"); err != nil {
			t.Fatal(err)
		}
		secretSeq := y.ask(t, hookSecretAsk("tracker", "linear"), "yardmaster")
		if _, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
			map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "mckean"); err != nil {
			t.Fatal(err)
		}
		runSeq := y.ask(t, hookRunAsk("hook add linear --source linear"), "yardmaster")
		if _, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean"); err == nil ||
			!strings.Contains(err.Error(), "answered ask for source linear on this bead") {
			t.Fatalf("approval with a secret card for another source = %v", err)
		}
		if _, err := y.GetHook(y.ctx, "linear"); err == nil {
			t.Fatal("approval with a secret card for another source made a hook")
		}
	})

	t.Run("secret asked after hook", func(t *testing.T) {
		y := hookYard(t)
		runSeq := y.ask(t, hookRunAsk("hook add linear --source linear"), "yardmaster")
		secretSeq := y.ask(t, hookSecretAsk("linear", "linear"), "yardmaster")
		if _, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
			map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "mckean"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean"); err == nil ||
			!strings.Contains(err.Error(), "answered ask for source linear on this bead") {
			t.Fatalf("approval with a later secret ask = %v", err)
		}
	})
}

// Hook names may begin with a digit. Its input name still has to be a
// valid KEY for the non-terminal NAME=value path.
func TestHookAskDigitLeadingName(t *testing.T) {
	y := hookYard(t)
	secretSeq := y.ask(t, hookSecretAsk("linear", "3jira"), "yardmaster")
	if got := strings.Join(y.asked(t, secretSeq).Ask.Secret.Env, ","); got != "_3JIRA_SIGNING_SECRET" {
		t.Fatalf("the secret input name is %q", got)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
		map[string]string{"_3JIRA_SIGNING_SECRET": hookSecretValue}, "mckean"); err != nil {
		t.Fatal(err)
	}
	runSeq := y.ask(t, hookRunAsk("hook add 3jira --source linear"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean"); err != nil {
		t.Fatal(err)
	}
}

// Hook secret writes refuse unsafe existing files without touching them or
// repeating the value in the refusal.
func TestWriteHookSecretRefusesUnsafeFiles(t *testing.T) {
	const old = "old-hook-secret"
	for _, mode := range []struct {
		name string
		perm os.FileMode
		want string
	}{
		{name: "open to others", perm: 0o644, want: "is open to others (mode 0644); chmod 600 it"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			home := t.TempDir()
			file := HookSecretFile(home, "linear")
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(old), mode.perm); err != nil {
				t.Fatal(err)
			}
			_, renamed, err := writeHookSecret(home, "linear", hookSecretValue)
			if err == nil || renamed || !strings.Contains(err.Error(), mode.want) || strings.Contains(err.Error(), hookSecretValue) {
				t.Fatalf("writeHookSecret = renamed %v, err %v", renamed, err)
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != old {
				t.Fatalf("the refused write changed the file: %q, %v", got, err)
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(file), ".linear.secret.new")); !os.IsNotExist(err) {
				t.Fatalf("the refused write left a temporary file: %v", err)
			}
		})
	}

	t.Run("symbolic link", func(t *testing.T) {
		home := t.TempDir()
		file := HookSecretFile(home, "linear")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.secret")
		if err := os.WriteFile(target, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
		_, renamed, err := writeHookSecret(home, "linear", hookSecretValue)
		if err == nil || renamed || !strings.Contains(err.Error(), "is a symbolic link") || strings.Contains(err.Error(), hookSecretValue) {
			t.Fatalf("writeHookSecret = renamed %v, err %v", renamed, err)
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != old {
			t.Fatalf("the symlink target changed: %q, %v", got, err)
		}
	})
}

// If recording fails after the private file is renamed, the ask stays open
// and the refusal identifies the file and name without repeating the value.
func TestGiveHookSecretNotRecorded(t *testing.T) {
	y := hookYard(t)
	seq := y.ask(t, hookSecretAsk("linear", "linear"), "yardmaster")
	full := errors.New("no space left on device")
	afterKeyRename = func() error { return full }
	t.Cleanup(func() { afterKeyRename = nil })
	_, err := y.GiveSecret(y.ctx, y.bead, seq,
		map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "mckean")
	var half *KeyNotRecorded
	if !errors.As(err, &half) || !errors.Is(err, full) || strings.Contains(err.Error(), hookSecretValue) {
		t.Fatalf("err = %v, want KeyNotRecorded with no secret", err)
	}
	file := HookSecretFile(y.home, "linear")
	if raw, err := os.ReadFile(file); err != nil || string(raw) != hookSecretValue {
		t.Fatalf("the file holds %q, %v", raw, err)
	}
	if r := y.resolved(t)[0]; r.State != AskOpen || len(r.Answers) != 0 {
		t.Fatalf("the ask is %s with %d answers, want open with none", r.State, len(r.Answers))
	}
	if n := len(answerNotes(t, y)); n != 0 {
		t.Fatalf("%d answer notes after a giving that was not recorded", n)
	}
}

// Each refusal of the two asks, by test: an undeclared source, a
// source whose declaration has no hook key, a hook the yard has, and
// the flags that stay in a terminal.
func TestHookAskRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"a secret for an undeclared source", hookSecretAsk("nope", "linear"), "source nope: not found"},
		{"a secret for a source with no hook key", hookSecretAsk("crm", "crmhook"), "its declaration has no hook key"},
		{"a secret with a bad hook name", hookSecretAsk("linear", "Linear"), `hook name "Linear"`},
		{"a run for an undeclared source", hookRunAsk("hook add linear --source nope"), "source nope: not found"},
		{"a run for a source with no hook key", hookRunAsk("hook add fresh --source crm"), "its declaration has no hook key"},
		{"a run with --map", hookRunAsk("hook add linear --source linear --map action=open"), "hook add with --map is the operator's to type in a terminal"},
		{"a run with --as", hookRunAsk("hook add linear --source linear --as yardmaster"), "hook add with --as is the operator's to type in a terminal"},
		{"a run with --secret-file", hookRunAsk("hook add linear --source linear --secret-file /tmp/x"), "hook add with --secret-file is the operator's to type in a terminal"},
		{"a run with no source", hookRunAsk("hook add linear"), "--source once"},
	} {
		t.Run(c.name, func(t *testing.T) {
			y := hookYard(t)
			if _, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster"); err == nil ||
				!strings.Contains(err.Error(), c.want) {
				t.Fatalf("AddAsk = %v, want %q", err, c.want)
			}
			if notes, err := y.Notes(y.ctx, y.bead); err != nil || len(notes) != 0 {
				t.Fatalf("the refused ask left %d notes, %v", len(notes), err)
			}
		})
	}

	// A hook the yard has: neither ask is taken; the secret giving
	// that races its making is refused too.
	y := hookYard(t)
	if _, err := y.PutHook(y.ctx, Hook{Name: "linear", Source: "linear", SecretFile: filepath.Join(y.home, "elsewhere.secret")}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, hookSecretAsk("linear", "linear"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "hook linear is in the yard already") {
		t.Fatalf("a secret ask for a hook the yard has = %v", err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, hookRunAsk("hook add linear --source linear"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "replace a hook") || !strings.Contains(err.Error(), "operator's alone") {
		t.Fatalf("a run ask for a hook the yard has = %v", err)
	}

	y = hookYard(t)
	seq := y.ask(t, hookSecretAsk("linear", "linear"), "yardmaster")
	if _, err := y.PutHook(y.ctx, Hook{Name: "linear", Source: "linear", SecretFile: filepath.Join(y.home, "elsewhere.secret")}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, seq,
		map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "hook linear is in the yard already") {
		t.Fatalf("giving after the hook was made = %v", err)
	}
	if _, err := os.Lstat(HookSecretFile(y.home, "linear")); !os.IsNotExist(err) {
		t.Fatalf("the refused giving wrote the file: %v", err)
	}

	// Giving is the operator's alone and names its names exactly,
	// without saying back a value.
	y = hookYard(t)
	seq = y.ask(t, hookSecretAsk("linear", "linear"), "yardmaster")
	for _, c := range []struct {
		actor  string
		values map[string]string
		want   string
	}{
		{"yardmaster", map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "only the operator (mckean) may give a hook's signing secret"},
		{"ada", map[string]string{"LINEAR_SIGNING_SECRET": hookSecretValue}, "only the operator (mckean) may give a hook's signing secret"},
		{"mckean", map[string]string{"OTHER": hookSecretValue}, "asks for LINEAR_SIGNING_SECRET, and was given a name it did not ask for"},
		{"mckean", map[string]string{"LINEAR_SIGNING_SECRET": ""}, "the signing secret is empty"},
	} {
		if _, err := y.GiveSecret(y.ctx, y.bead, seq, c.values, c.actor); err == nil ||
			!strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), hookSecretValue) {
			t.Errorf("giving as %s with %v = %v, want %q and no value", c.actor, c.values, err, c.want)
		}
	}
	if _, err := os.Lstat(HookSecretFile(y.home, "linear")); !os.IsNotExist(err) {
		t.Fatalf("refused givings wrote the file: %v", err)
	}
}

const trackerDecl = `name: tracker
base: https://tracker.example.com
credential: { env: TRACKER_TOKEN }
hook:
  verify: { scheme: secret, signature: { header: X-Tracker-Secret } }
  bodies: unique
  normalize: [tracker/normalize]
  keys: [project]
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// The row is for any declared source with a hook key: the card's words
// about the source come from its declaration (here the secret scheme
// and its header), and the same two asks set the hook up.
func TestHookAskGenericSource(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	if _, err := y.PutSource(y.ctx, "tracker.yaml", trackerDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	secretSeq := y.ask(t, hookSecretAsk("tracker", "tracker"), "yardmaster")
	runSeq := y.ask(t, hookRunAsk("hook add tracker --source tracker"), "yardmaster")
	does := strings.Join(y.asked(t, runSeq).Ask.Run.Does, "\n")
	if !strings.Contains(does, "deliveries are verified by the header X-Tracker-Secret holding the hook's secret itself (scheme secret)") {
		t.Fatalf("the card says:\n%s", does)
	}
	if _, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
		map[string]string{"TRACKER_SIGNING_SECRET": hookSecretValue}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean"); err != nil {
		t.Fatal(err)
	}
	hook, err := y.GetHook(y.ctx, "tracker")
	if err != nil || hook.Source != "tracker" {
		t.Fatalf("the hook = %+v, %v", hook, err)
	}
}

// A chmod of a command copy under the hook's source after the ask
// makes the approval stale: the card's source fingerprint holds the
// whole destination inventory (path, sha256, mode), as a source set or
// grant card's does.
func TestHookAskStaleOnChmod(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	dir := plantWired(t, y)
	seq := y.ask(t, hookRunAsk("hook add wired --source wired"), "yardmaster")
	if err := os.Chmod(filepath.Join(dir, "wired", "normalize"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.refusedStale(t, seq, "was written from another declaration of the source wired: it was replaced since")
}

// A source the loose road added (a source add run ask) takes a hook
// the same way once it stands declared.
func TestHookAskAfterSourceAdd(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y, _ := runYard(t)
	add := sourceAddAsk(carriedOf(t, trackerDecl, map[string]string{"tracker/normalize": "#!/bin/sh\ncat >/dev/null\n"}))
	add.Run.Argv = []string{"source add tracker.yaml"}
	addSeq := y.ask(t, add, "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, addSeq, "mckean"); err != nil {
		t.Fatal(err)
	}
	secretSeq := y.ask(t, hookSecretAsk("tracker", "tracker"), "yardmaster")
	runSeq := y.ask(t, hookRunAsk("hook add tracker --source tracker"), "yardmaster")
	if _, err := y.GiveSecret(y.ctx, y.bead, secretSeq,
		map[string]string{"TRACKER_SIGNING_SECRET": hookSecretValue}, "mckean"); err != nil {
		t.Fatal(err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, runSeq, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != "hook tracker saved: POST /hooks/tracker (tracker)" {
		t.Fatalf("the approval answers %+v", done)
	}
}
