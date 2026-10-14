package pack_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/storetest"
)

// template is the skeleton a new source pack starts from: a directory to
// copy. The test holds it to the reader the way an author uses it: it
// copies the directory elsewhere, renames the skeleton's service, and
// applies the copy in a scratch yard. The skeleton's service is acme, and
// nothing outside the copy is read, so the template is self-contained.
const template = "../../packs/template"

// copyTemplate copies the template to a new directory, as an author would,
// and renames its service from acme to demo: the files and directories that
// carry the name, and every acme and ACME_ in their text. The skeleton's
// prose calls itself the skeleton and the service, so the two tokens are
// the name everywhere they stand.
func copyTemplate(t *testing.T, from string) string {
	t.Helper()
	to := t.TempDir()
	err := fs.WalkDir(os.DirFS(from), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		dest := filepath.Join(to, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := os.ReadFile(filepath.Join(from, filepath.FromSlash(p)))
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, info.Mode())
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	err = filepath.Walk(to, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(p, "acme") {
			paths = append(paths, p)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			replaced := strings.ReplaceAll(strings.ReplaceAll(string(data), "ACME_", "DEMO_"), "acme", "demo")
			if replaced != string(data) {
				if err := os.WriteFile(p, []byte(replaced), info.Mode()); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The shallowest path first, and nothing under a renamed directory:
	// renaming acme/ carries what is in it.
	slices.Sort(paths)
	var renamed []string
	for _, p := range paths {
		if slices.ContainsFunc(renamed, func(q string) bool {
			return strings.HasPrefix(p, q+string(os.PathSeparator))
		}) {
			continue
		}
		if err := os.Rename(p, strings.ReplaceAll(p, "acme", "demo")); err != nil {
			t.Fatal(err)
		}
		renamed = append(renamed, p)
	}
	return to
}

// A new source pack starts from the template and applies in a scratch
// yard: the copy loads as a pack of kind source, its declaration names
// every key such a pack may bring, and the apply adds the source and
// copies the commands' files, unchanged on a second apply.
func TestTemplateAppliesAsANewSourcePack(t *testing.T) {
	dir := copyTemplate(t, template)
	p, err := pack.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo" || p.Kind != pack.KindSource || len(p.Declared) != 1 {
		t.Fatalf("pack = %+v", p)
	}
	d := p.Declared[0]
	if d.Name != "demo" || d.File != "demo.yaml" {
		t.Fatalf("declared = %+v", d)
	}
	decl := d.Declaration
	if decl.Name != "demo" || decl.Base == "" || decl.Credential == nil || decl.Check == "" ||
		decl.Idempotency == "" || len(decl.Calls) == 0 || decl.Notify == nil || decl.Hear == nil ||
		decl.Hook == nil || decl.Thread == nil || decl.Link == nil || decl.Identity == nil ||
		decl.Events == nil {
		t.Fatalf("the skeleton names every key: %+v", decl)
	}
	if decl.Credential.Env == "" || len(decl.Credential.Mint) == 0 {
		t.Fatalf("credential = %+v", decl.Credential)
	}
	if th := decl.Thread; len(th.Reply) == 0 || len(th.Posted) == 0 || len(th.Read) == 0 ||
		len(th.Open) == 0 || len(th.Opened) == 0 || len(th.State) == 0 || th.Place == "" ||
		len(th.States) == 0 || !th.Labels || th.Assignee == "" {
		t.Fatalf("thread = %+v", th)
	}
	if l := decl.Link; l.Host == "" || l.Issue == "" || l.Fixed == "" || l.Thread == "" ||
		l.Pull == nil || l.Repos == "" {
		t.Fatalf("link = %+v", l)
	}
	if ev := decl.Events; len(ev.Runs) == 0 || len(ev.HeadFrom) == 0 || len(ev.PR) == 0 || len(ev.CI) == 0 {
		t.Fatalf("events = %+v", ev)
	}

	st := storetest.New(t)
	home := t.TempDir()
	plan := build(t, st, p, pack.Options{Home: home})
	if got := find(t, plan, "source", "demo"); got.Action != pack.Create {
		t.Fatalf("plan = %+v", got)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || len(res.Declared) != 1 {
		t.Fatalf("result = %+v", res)
	}
	src, err := st.GetSource(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Declaration(); err != nil {
		t.Fatalf("the added source does not read: %v", err)
	}
	for _, prog := range []string{"demo/normalize", "demo/thread", "demo/pr", "demo/ci"} {
		fi, err := os.Stat(filepath.Join(home, "sources", "demo", filepath.FromSlash(prog)))
		if err != nil {
			t.Fatalf("the copy of %s: %v", prog, err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("the copy of %s has mode %o, want 700", prog, fi.Mode().Perm())
		}
	}
	again := build(t, st, load(t, dir), pack.Options{Home: home})
	if got := find(t, again, "source", "demo"); got.Action != pack.Unchanged {
		t.Fatalf("second plan = %+v", got)
	}
}

// The skeleton's stubs answer in the shapes the core's contracts take:
// each is run as the core runs it, one JSON object on stdin, one on
// stdout, with a credential and a base that reach nothing, since a stub
// calls no service.
func TestTemplateStubsAnswerInShape(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the skeleton's stubs are written in it")
	}
	dir := copyTemplate(t, template)
	run := func(prog string, args []string, in string) map[string]any {
		t.Helper()
		cmd := exec.Command(filepath.Join(dir, filepath.FromSlash(prog)), args...)
		cmd.Stdin = strings.NewReader(in)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
			"DEMO_TOKEN=dummy", "YARDR_SOURCE_BASE=http://127.0.0.1:1"}
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Fatalf("%s %v: %v: %s", prog, args, err, exit.Stderr)
			}
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("%s %v printed no JSON object: %v: %q", prog, args, err, out)
		}
		return v
	}
	if got := run("demo/normalize", nil, `{"headers":{},"body":{"action":"create"}}`); got["type"] == "" {
		t.Fatalf("normalize: %v", got)
	}
	if got := run("demo/thread", []string{"reply"}, `{"thread":"DEMO-1","parent":"","body":"hi","key":"k-1"}`); got["id"] == "" || got["url"] == "" {
		t.Fatalf("thread reply: %v", got)
	}
	if got := run("demo/thread", []string{"posted"}, `{"thread":"DEMO-1","key":"k-1"}`); len(got) != 0 && got["id"] == "" {
		t.Fatalf("thread posted: %v", got)
	}
	if got := run("demo/thread", []string{"read"}, `{"thread":"DEMO-1"}`); got["messages"] == nil {
		t.Fatalf("thread read: %v", got)
	}
	if got := run("demo/thread", []string{"open"}, `{"in":"demo","title":"T","body":"B","key":"k-2"}`); got["id"] == "" || got["title"] == "" || got["url"] == "" {
		t.Fatalf("thread open: %v", got)
	}
	if got := run("demo/thread", []string{"opened"}, `{"in":"demo","key":"k-2"}`); len(got) != 0 && got["id"] == "" {
		t.Fatalf("thread opened: %v", got)
	}
	if got := run("demo/thread", []string{"state"}, `{"thread":"DEMO-1","state":"started"}`); got["status"] == "" || got["moved"] == nil {
		t.Fatalf("thread state: %v", got)
	}
	if got := run("demo/pr", nil, `{"repo":"demo/web","number":"7","url":""}`); got["head"] == "" || got["base"] == "" {
		t.Fatalf("pr: %v", got)
	}
	got := run("demo/ci", nil, `{"repo":"demo/web","number":"7","sha":"abc123"}`)
	if got["head"] == "" || got["base"] == "" || got["required"] == nil || got["checks"] == nil {
		t.Fatalf("ci: %v", got)
	}
	for _, c := range got["checks"].([]any) {
		check := c.(map[string]any)
		if check["name"] == "" || check["status"] == "" || check["conclusion"] == "" {
			t.Fatalf("ci check: %v", check)
		}
	}
}
