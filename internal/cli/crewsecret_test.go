package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// crewSecretYard is secretYard with the crew member yardmaster and the
// source open, which names no credential. It answers the file the crew
// member's own credential goes to when it has no env_file.
func crewSecretYard(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	st, id, crmFile := secretYard(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	const openDecl = "name: open\nbase: https://open.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	if _, err := st.PutSource(ctx, "open.yaml", openDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(crmFile))
	return st, id, filepath.Join(home, "crew", "yardmaster.env")
}

// bead ask --secret <source> --for-crew <crew member> asks the operator
// for that crew member's own credential in the source, and bead answer
// --secret gives it into the crew member's file and says when it takes
// effect. --env narrows the ask to some of the credential's variables.
func TestBeadAskAndAnswerACrewMembersCredential(t *testing.T) {
	st, id, file := crewSecretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	ada := &app{st: st, actor: "ada"}
	const value = "tok-0f-s3cret"

	ask := []string{"ask", id, "The token it answers with", "--context", "Create one at https://crm.example.com/account/api (read only)."}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"for-crew alone":   {[]string{"--for-crew", "yardmaster"}, "it goes with --secret <source>"},
		"a user beside it": {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--for-user", "ada"}, "one of the two"},
		"no credential":    {[]string{"--secret", "open", "--for-crew", "yardmaster"}, "its declaration names no credential"},
		"no such source":   {[]string{"--secret", "nowhere", "--for-crew", "yardmaster"}, "source nowhere: not found"},
		"no such crew":     {[]string{"--secret", "crm", "--for-crew", "eve"}, "crew member eve: not found"},
		"a name":           {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--env", "GITHUB_PASSWORD"}, "is no variable of its credential"},
		"not operator":     {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--to", "ada"}, "so ada cannot be asked for one"},
		"another yard":     {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--to", "mckean@north"}, "nothing of a secret crosses a link"},
		"with an option":   {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--option", "Yes: it is given"}, "takes no --option"},
		"about other work": {[]string{"--secret", "crm", "--for-crew", "yardmaster", "--about", id}, "takes no --about"},
		"a login beside":   {[]string{"--secret-login", "claude-x", "--for-crew", "yardmaster"}, "takes no --for-crew"},
		"a hook beside":    {[]string{"--secret-hook", "h", "--source", "crm", "--for-crew", "yardmaster"}, "it goes with --secret <source>"},
	} {
		if _, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}

	out, err := run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "crm", "--for-crew", "yardmaster", "--env", "CRM_TOKEN")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for mckean: a secret, CRM_TOKEN, for crew member yardmaster in the source crm\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	// Without --env, every variable of the credential.
	out, err = run(t, yard.beadCmd(), append(append([]string{}, ask...), "--secret", "crm", "--for-crew", "yardmaster")...)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 2 on " + id + " for mckean: a secret, CRM_TOKEN and CRM_UPDATE_TOKEN, for crew member yardmaster in the source crm\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	jsonOut, err := run(t, (&app{st: st, actor: "yardmaster", json: true}).beadCmd(), append(append([]string{}, ask...), "--secret", "crm", "--for-crew", "yardmaster")...)
	var asked struct {
		Ask    uint64
		Secret store.AskSecret
	}
	if err != nil || json.Unmarshal([]byte(jsonOut), &asked) != nil || asked.Secret.Crew != "yardmaster" || asked.Secret.User != "" {
		t.Fatalf("ask --json = %q, %v", jsonOut, err)
	}

	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a secret: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)",
		"it goes to a file only the yard's server reads; no agent is shown it",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}

	if _, err := answerFrom(t, ada, "CRM_TOKEN="+value+"\n", id, "1", "--secret"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (mckean) may give a crew member's credential; this is ada") ||
		strings.Contains(err.Error(), value) {
		t.Fatalf("ada answering the ask: %v", err)
	}
	if _, err := answerFrom(t, me, value, id, "1", "--words", value); err == nil || strings.Contains(err.Error(), value) {
		t.Fatalf("the credential as words: %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused answer wrote the file: %v", err)
	}
	out, err = answerFrom(t, me, "CRM_TOKEN="+value+"\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mckean gave CRM_TOKEN · it is set in " + file + "\nnext: it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN="+value+"\n" {
		t.Fatalf("the file holds %q", b)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file's mode: %v, %v", fi.Mode(), err)
	}
	if r, err := st.GetCrew(context.Background(), "yardmaster"); err != nil || r.Config["env_file"] != file {
		t.Fatalf("yardmaster = %+v, %v", r, err)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean gave CRM_TOKEN") || strings.Contains(out, value) {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}
}
