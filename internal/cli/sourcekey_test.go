package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/storetest"
)

const keyDecl = `name: crm
base: https://crm.example.com/api
credential: { env: CRM_TOKEN, update_env: CRM_UPDATE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// keyYard is a yard with the source crm, and the file yardr source key
// writes its credential to.
func keyYard(t *testing.T) (*app, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	if _, err := st.PutSource(context.Background(), "crm.yaml", keyDecl, "jo"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "jo"}, filepath.Join(filepath.Dir(path), "sources", "crm.env")
}

// keyFrom runs yardr source key with stdin as its standard input.
func keyFrom(t *testing.T, a *app, stdin string, args ...string) (string, error) {
	t.Helper()
	c := a.sourceCmd()
	c.SetIn(strings.NewReader(stdin))
	return run(t, c, append([]string{"key"}, args...)...)
}

// yardr source key reads NAME=value lines when its standard input is no
// terminal, has the server write them, and prints where the credential is
// set and never a value. One name of two is written and what is missing
// is said; a second giving completes it and keeps the first.
func TestSourceKeyFromAPipe(t *testing.T) {
	a, file := keyYard(t)
	out, err := keyFrom(t, a, "# from the vault\nCRM_TOKEN=s3cret-read\n", "crm")
	if err != nil {
		t.Fatal(err)
	}
	if want := "source crm: CRM_TOKEN written to " + file + "; env_file " + file + " does not set CRM_UPDATE_TOKEN\n"; out != want {
		t.Fatalf("out = %q\nwant  %q", out, want)
	}
	out, err = keyFrom(t, a, "CRM_UPDATE_TOKEN=s3cret-write\n", "crm", "CRM_UPDATE_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if want := "source crm: CRM_TOKEN is set in " + file + "; for an update, CRM_UPDATE_TOKEN is set in " + file + "\n"; out != want {
		t.Fatalf("out = %q\nwant  %q", out, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret-read\nCRM_UPDATE_TOKEN=s3cret-write\n" {
		t.Fatalf("the file holds %q", b)
	}
	if st, err := os.Stat(file); err != nil || st.Mode() != 0o600 {
		t.Fatalf("the file: %v, %v", st, err)
	}
	// show and check say the same, and no command prints a value.
	for _, cmd := range []string{"show", "check"} {
		out, err := run(t, a.sourceCmd(), cmd, "crm")
		if cmd == "show" && (err != nil || !strings.Contains(out, "credential: CRM_TOKEN is set in "+file)) {
			t.Fatalf("show: %q, %v", out, err)
		}
		if strings.Contains(out, "s3cret") || (err != nil && strings.Contains(err.Error(), "s3cret")) {
			t.Fatalf("%s shows a value: %q, %v", cmd, out, err)
		}
	}

	for name, c := range map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"a name that was not asked for": {"CRM_TOKEN=new-s3cret\n", []string{"crm", "CRM_UPDATE_TOKEN"}, "standard input sets CRM_TOKEN, which was not asked for (CRM_UPDATE_TOKEN)"},
		"a name asked for and not sent": {"CRM_TOKEN=new-s3cret\n", []string{"crm", "CRM_TOKEN", "CRM_UPDATE_TOKEN"}, "standard input has no line CRM_UPDATE_TOKEN=<value>"},
		"a name the file does not have": {"CRM_TOKEN=new-s3cret\n", []string{"crm", "PATH"}, "PATH is no variable of its credential (its declaration names CRM_TOKEN and CRM_UPDATE_TOKEN)"},
		"one the pipe invents":          {"PATH=new-s3cret\n", []string{"crm"}, "PATH is no variable of its credential"},
		"no line":                       {"\n", []string{"crm"}, "standard input has no NAME=value line"},
		"a line that is none":           {"new-s3cret\n", []string{"crm"}, "standard input line 1: want KEY=VALUE"},
		"an empty value":                {"CRM_TOKEN=\n", []string{"crm"}, "the value for CRM_TOKEN is empty"},
	} {
		out, err := keyFrom(t, a, c.stdin, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q, err = %v, want %q", name, out, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(out, "s3cret") {
			t.Errorf("%s: a value is shown: %q, %v", name, out, err)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret-read\nCRM_UPDATE_TOKEN=s3cret-write\n" {
		t.Fatalf("a refused giving changed the file: %q", b)
	}
}

// On a terminal each value is asked for by its name and the source's, with
// the words that nothing is shown; what a paste brings at its ends is
// dropped. Without names every variable is asked for and enter alone
// leaves one as it is; a name that was asked for wants a value.
func TestSourceKeyTyped(t *testing.T) {
	d, err := source.Parse("crm.yaml", []byte(keyDecl))
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	typing := func(answers ...string) func(string) (string, error) {
		prompts = nil
		return func(prompt string) (string, error) {
			prompts = append(prompts, prompt)
			a := answers[0]
			answers = answers[1:]
			return a, nil
		}
	}
	got, err := readKeys(d, nil, nil, typing("", " s3cret-write\t"))
	if err != nil || len(got) != 1 || got["CRM_UPDATE_TOKEN"] != "s3cret-write" {
		t.Fatalf("readKeys = %d names, %v", len(got), err)
	}
	if want := []string{
		"CRM_TOKEN for the source crm (nothing is shown as you type; enter alone leaves it as it is): ",
		"CRM_UPDATE_TOKEN for the source crm (nothing is shown as you type; enter alone leaves it as it is): ",
	}; !slices.Equal(prompts, want) {
		t.Fatalf("prompts = %q", prompts)
	}
	got, err = readKeys(d, []string{"CRM_TOKEN"}, nil, typing("s3cret-read"))
	if err != nil || len(got) != 1 || got["CRM_TOKEN"] != "s3cret-read" ||
		!slices.Equal(prompts, []string{"CRM_TOKEN for the source crm (nothing is shown as you type): "}) {
		t.Fatalf("readKeys of one name = %d names, %v, prompts %q", len(got), err, prompts)
	}
	if _, err := readKeys(d, []string{"CRM_TOKEN"}, nil, typing("")); err == nil || !strings.Contains(err.Error(), "nothing was typed for CRM_TOKEN, and nothing is written") {
		t.Fatalf("enter alone for a name that was asked for: %v", err)
	}
	if _, err := readKeys(d, nil, nil, typing("", "")); err == nil || !strings.Contains(err.Error(), "nothing was typed, and nothing is written") {
		t.Fatalf("enter alone for every name: %v", err)
	}
	gone := errors.New("the terminal went away")
	if _, err := readKeys(d, nil, nil, func(string) (string, error) { return "s3cr", gone }); !errors.Is(err, gone) || strings.Contains(err.Error(), "s3cr") {
		t.Fatalf("a read that failed: %v", err)
	}
}

// A file where yardr source key writes one, which no setting names (a
// giving that was written and not recorded, or a file put there by hand),
// is said by yardr source show, with the two ways out; the giving takes
// it up and keeps its other lines.
func TestSourceShowSaysAFileThatIsNotSet(t *testing.T) {
	a, file := keyYard(t)
	out, err := run(t, a.sourceCmd(), "show", "crm")
	if err != nil || !strings.Contains(out, "credential: CRM_TOKEN is not set up: yardr source set crm env_file=") {
		t.Fatalf("show with no file: %q, %v", out, err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("# left\nCRM_TOKEN=s3cret-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.sourceCmd(), "show", "crm")
	if want := "  credential: no credential; " + tilde(file) + " exists and is not set: yardr source key crm, or remove it\n"; err != nil || !strings.Contains(out, want) {
		t.Fatalf("show = %q, %v\nwant the line %q", out, err, want)
	}
	if _, err := keyFrom(t, a, "CRM_UPDATE_TOKEN=s3cret-write\n", "crm"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.sourceCmd(), "show", "crm")
	if err != nil || !strings.Contains(out, "credential: CRM_TOKEN is set in "+file+"; for an update, CRM_UPDATE_TOKEN is set in "+file) || strings.Contains(out, "is not set:") {
		t.Fatalf("show after the giving: %q, %v", out, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "# left\nCRM_TOKEN=s3cret-read\nCRM_UPDATE_TOKEN=s3cret-write\n" {
		t.Fatalf("the file holds %q", b)
	}
}
