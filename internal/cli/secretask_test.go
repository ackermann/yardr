package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// secretYard is a yard whose operator is mckean, with the source crm (two
// variables), one bead, and the file crm's credential goes to.
func secretYard(t *testing.T) (store.API, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "operator", "mckean", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "crm.yaml", keyDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, b.ID, filepath.Join(filepath.Dir(path), "sources", "crm.env")
}

// answerFrom runs yardr bead answer with stdin as its standard input.
func answerFrom(t *testing.T, a *app, stdin string, args ...string) (string, error) {
	t.Helper()
	c := a.beadCmd()
	c.SetIn(strings.NewReader(stdin))
	return run(t, c, append([]string{"answer"}, args...)...)
}

// bead ask --secret stores the ask with where its answer goes and prints
// the line of the design's section 3.2; --env narrows it; prime shows the
// yard's line and how to answer. bead answer --secret reads NAME=value
// lines from a pipe, as source key does, and prints who gave which names
// and the file, never a value.
func TestBeadAskAndAnswerASecret(t *testing.T) {
	st, id, file := secretYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}

	out, err := run(t, yard.beadCmd(), "ask", id, "The API key for the CRM", "--secret", "crm", "--env", "CRM_TOKEN",
		"--context", "Create one at https://crm.example.com/account/api (read only).")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ask 1 on " + id + " for mckean: a secret, CRM_TOKEN of the source crm\n"; out != want {
		t.Fatalf("ask printed %q\nwant        %q", out, want)
	}
	out, err = run(t, yard.beadCmd(), "ask", "--new", "Both tokens of the CRM", "--secret", "crm", "--context", "As above.")
	if err != nil || !strings.HasSuffix(out, " created; ask 2 on it for mckean: a secret, CRM_TOKEN and CRM_UPDATE_TOKEN of the source crm\n") {
		t.Fatalf("ask --new printed %q, %v", out, err)
	}
	jsonOut, err := run(t, (&app{st: st, actor: "yardmaster", json: true}).beadCmd(), "ask", id, "Again", "--secret", "crm", "--env", "CRM_UPDATE_TOKEN", "--context", "As above.")
	var asked struct {
		Ask    uint64
		Secret store.AskSecret
	}
	if err != nil || json.Unmarshal([]byte(jsonOut), &asked) != nil || asked.Secret.Source != "crm" || strings.Join(asked.Secret.Env, ",") != "CRM_UPDATE_TOKEN" {
		t.Fatalf("ask --json = %q, %v", jsonOut, err)
	}

	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ask 1 · open", "The API key for the CRM",
		"a secret: CRM_TOKEN, for the source crm (https://crm.example.com)",
		"it goes to a file only the yard's server reads; no agent is shown it",
		"answer with: yardr bead answer " + id + " 1 --secret",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"secret"`) || strings.Contains(out, "words wanted") {
		t.Errorf("prime:\n%s", out)
	}

	for name, c := range map[string]struct {
		a     *app
		stdin string
		args  []string
		want  string
	}{
		"a pick":            {me, "", []string{id, "1", "1"}, "takes a secret, not words"},
		"words":             {me, "", []string{id, "1", "--words", "s3cret-as-words"}, "takes a secret, not words"},
		"decline, words":    {me, "", []string{id, "1", "--decline", "s3cret-as-why"}, "declining it takes no words"},
		"secret and a pick": {me, "CRM_TOKEN=s3cret\n", []string{id, "1", "1", "--secret"}, "takes nothing else"},
		"another name":      {me, "CRM_UPDATE_TOKEN=s3cret\n", []string{id, "1", "--secret"}, "name that was not asked for"},
		"secret in name":    {me, "s3cret_as_name=ignored\n", []string{id, "1", "--secret"}, "name that was not asked for"},
		"no line":           {me, "# nothing\n", []string{id, "1", "--secret"}, "has no line CRM_TOKEN=<value>"},
		"an agent":          {yard, "CRM_TOKEN=s3cret\n", []string{id, "1", "--secret"}, "yardmaster is a crew member"},
		"no secret ask":     {me, "CRM_TOKEN=s3cret\n", []string{id, "9", "--secret"}, "has no ask 9"},
	} {
		if name == "an agent" {
			if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "mckean"); err != nil {
				t.Fatal(err)
			}
		}
		out, err := answerFrom(t, c.a, c.stdin, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(out+err.Error(), "s3cret") {
			t.Errorf("%s: %q, err = %v, want %q and no value", name, out, err, c.want)
		}
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused answer wrote the file: %v", err)
	}

	out, err = answerFrom(t, me, "CRM_TOKEN=s3cret-read\n", id, "1", "--secret")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mckean gave CRM_TOKEN · it is set in " + file + " · env_file " + file + " does not set CRM_UPDATE_TOKEN\n"; out != want {
		t.Fatalf("answer printed %q\nwant           %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret-read\n" {
		t.Fatalf("the file holds %q", b)
	}
	out, err = answerFrom(t, me, "CRM_UPDATE_TOKEN=s3cret-write\n", id, "3", "--secret")
	if want := "mckean gave CRM_UPDATE_TOKEN · it is set in " + file + "\n"; err != nil || out != want {
		t.Fatalf("answer printed %q, %v\nwant           %q", out, err, want)
	}
	out, err = run(t, me.primeCmd(), "--bead", id)
	if err != nil || !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean gave CRM_TOKEN") || strings.Contains(out, "s3cret") {
		t.Fatalf("prime after the giving, %v:\n%s", err, out)
	}

	// Declined without words, which a secret ask alone is.
	if _, err := run(t, yard.beadCmd(), "ask", id, "Once more", "--secret", "crm", "--env", "CRM_TOKEN", "--context", "As above."); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, me.beadCmd(), "answer", id, "6", "--decline", "")
	if err != nil || !strings.Contains(out, "mckean declined to give it") {
		t.Fatalf("decline printed %q, %v", out, err)
	}
}

// bead ask --secret is refused by the yard in each case it lists, and
// --env goes with --secret alone.
func TestBeadAskSecretRefusals(t *testing.T) {
	st, id, _ := secretYard(t)
	a := &app{st: st, actor: "yardmaster"}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no such source": {[]string{"--secret", "nowhere"}, "source nowhere: not found"},
		"a name":         {[]string{"--secret", "crm", "--env", "GITHUB_PASSWORD"}, "is no variable of its credential"},
		"not operator":   {[]string{"--secret", "crm", "--to", "ada"}, "the operator's to give (mckean)"},
		"a peer":         {[]string{"--secret", "crm", "--to", "mckean@north"}, "nothing of a secret crosses a link"},
		"an option":      {[]string{"--secret", "crm", "--option", "Yes: it is given"}, "takes no --option"},
		"about":          {[]string{"--secret", "crm", "--about", id}, "takes no --about"},
		"env alone":      {[]string{"--env", "CRM_TOKEN"}, "goes with --secret"},
	} {
		args := append([]string{"ask", id, "The API key for the CRM", "--context", "where to get it."}, c.args...)
		if _, err := run(t, a.beadCmd(), args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("refused asks left %d notes", len(notes))
	}
}
