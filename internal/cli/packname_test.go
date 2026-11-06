package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// afterFirstLine is a plan without the line that names where the pack was
// read from, the one thing a pack's plan differs in by how it was named.
func afterFirstLine(t *testing.T, out, wantIn string) string {
	t.Helper()
	first, rest, _ := strings.Cut(out, "\n")
	if !strings.Contains(first, wantIn) {
		t.Fatalf("the plan begins %q, want %q in it", first, wantIn)
	}
	return rest
}

// The starter pack is in the binary: named, it gives the plan its directory
// in the checkout gives.
func TestPackCheckByNameIsTheEmbeddedPack(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	byName, err := run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	byDir, err := run(t, a.packCmd(), "check", "../../packs/starter", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs("../../packs/starter")
	if got, want := afterFirstLine(t, byName, "pack starter (embedded in yardr "), afterFirstLine(t, byDir, "pack starter ("+abs+")"); got != want {
		t.Errorf("by name:\n%s\nby directory:\n%s", got, want)
	}
}

// A bare word is never read as a directory, so what it names does not depend
// on where the command is run: beside a directory ./starter, starter is
// still the embedded pack, and ./starter is the directory.
func TestPackNameIsNeverADirectory(t *testing.T) {
	a := packApp(t)
	here := t.TempDir()
	if err := os.CopyFS(filepath.Join(here, "starter"), os.DirFS("../../packs/starter")); err != nil {
		t.Fatal(err)
	}
	// The directory's pack is told apart by its name.
	file := filepath.Join(here, "starter", "pack.yaml")
	text, _ := os.ReadFile(file)
	renamed := strings.Replace(string(text), "name: starter\n", "name: local\n", 1)
	if renamed == string(text) {
		t.Fatal("pack.yaml has no name: starter line")
	}
	if err := os.WriteFile(file, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(here, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(here)
	byName, err := run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	byDir, err := run(t, a.packCmd(), "check", "./starter", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(byName, "pack starter (embedded in yardr ") {
		t.Errorf("starter, beside a directory of that name, is not the embedded pack:\n%s", byName)
	}
	if !strings.HasPrefix(byDir, "pack local (") {
		t.Errorf("./starter is not the directory:\n%s", byDir)
	}
	// A word that names no pack is not tried as a directory either; the
	// error says how the directory is written.
	_, err = run(t, a.packCmd(), "check", "mine", "--depot", "web")
	if err == nil || !strings.Contains(err.Error(), `no pack named "mine": this yard has imported none, yardr embeds ci-red, cloudflare, contributors, design, dir, github, google-calendar, google-docs, google-drive, google-mail, linear, posthog, pushover, slack, starter, telegram, webhook, website; for the directory mine here, write ./mine`) {
		t.Fatalf("check mine: %v", err)
	}
}

// The help of pack check, apply and show names every pack the binary
// carries, so a pack added to it is added to the help with it.
func TestPackHelpNamesEveryEmbeddedPack(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(packs.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	cmd := (&app{}).packCmd()
	for _, name := range []string{"check", "apply", "show"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.Contains(sub.Long, e.Name()) {
				t.Errorf("yardr pack %s --help does not name the embedded pack %s", name, e.Name())
			}
		}
	}
}

// Applying the embedded pack installs its script executable, though an
// embedded file has no mode, and records where it came from: embedded, and
// the build, with no commit of a directory.
func TestPackApplyTheEmbeddedPack(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	requireNonInteractive(t)
	args := append([]string{"apply", "starter", "--depot", "web", "--yes"}, starterFlags...)
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	script := filepath.Join(config.At(a.cfg.Home).Pack("starter"), "scripts", "merge-local")
	if info, err := os.Stat(script); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("%s: %v, mode %v", script, err, info)
	}
	want, _ := os.ReadFile("../../packs/starter/scripts/merge-local")
	if got, _ := os.ReadFile(script); string(got) != string(want) {
		t.Error("the installed script is not the pack's")
	}
	// The binary's own copy of the pack says how the assembly lands: its
	// cmd starts with the pack's env, so a bead lands by squash.
	if g, err := a.st.GetGroup(context.Background(), "web-assembly"); err != nil || g.Config["cmd"] != "YARDR_LAND=squash "+script {
		t.Errorf("web-assembly = %+v, %v, want its cmd to be YARDR_LAND=squash %s", g, err, script)
	}
	if info, err := os.Stat(filepath.Join(config.At(a.cfg.Home).PackRoles("starter"), "builder.md")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("roles/builder.md: %v, mode %v", err, info)
	}
	recs, err := a.st.Packs(context.Background())
	if err != nil || len(recs) != 1 {
		t.Fatalf("Packs = %v, %v", recs, err)
	}
	if r := recs[0]; !strings.HasPrefix(r.Source, "embedded in yardr ") || r.Commit != "" || r.Dirty {
		t.Errorf("record: source %q, commit %q, dirty %v", r.Source, r.Commit, r.Dirty)
	}
	// Named again it is the same pack: nothing to change, and nothing kept
	// of the env, which the group the import made carries.
	out, err := run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if err != nil || !strings.Contains(out, "0 to create, 0 to change, ") || strings.Contains(out, "kept:") {
		t.Fatalf("check after the import: %v\n%s", err, out)
	}
	// A group the yard set to the script alone (it predates the env, or
	// opted out) is not set to the env by the import: check says what it
	// keeps and changes nothing.
	ctx := context.Background()
	g, err := a.st.GetGroup(ctx, "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	g.Config["cmd"] = script
	if err := a.st.PutGroup(ctx, *g, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if want := "  unchanged        web-assembly\n                     kept: cmd " + script + ": the pack's env YARDR_LAND=squash is set on a new group only\n"; err != nil || !strings.Contains(out, want) || !strings.Contains(out, "0 to create, 0 to change, ") {
		t.Fatalf("check over a group set to the script alone: %v\n%s\nwant %q", err, out, want)
	}
}

// The contributors pack is found by its name too. Its import asks for what
// the pack cannot say: who the maintainers are, and the agent of the group
// that reviews a pull request from outside, which takes the kind's
// read-only preset, so that a reviewer of a stranger's code runs in its
// agent's sandbox. It leaves the depot's own flow as it is: its flows are
// for the types issue and pr.
func TestPackApplyTheContributorsPack(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	requireNonInteractive(t)
	ctx := context.Background()
	flags := []string{"--depot", "web", "--yes", "--crew", "yardmaster=boss", "--bind", "crew:yardmaster=claude", "--bind", "builders=claude", "--bind", "reviewers=claude",
		"--members", "backlog=", "--members", "decisions=", "--members", "outside="}
	out, err := run(t, a.packCmd(), append([]string{"apply", "contributors"}, flags...)...)
	want := "--bind pr-reviewers=<kind>[:<model>]\n--members maintainers=<users>\n"
	if !errors.Is(err, errCheckFailed) || out != want {
		t.Fatalf("an import that names neither the maintainers nor the pr-reviewers' agent: %v, got:\n%s\nwant:\n%s", err, out, want)
	}
	if _, err := a.st.GetGroup(ctx, "maintainers"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused import made a group: %v", err)
	}
	for kind, want := range map[string]string{
		"codex": `[--enable network_proxy -c default_permissions="yardr_read_only" -c permissions.yardr_read_only.extends=":read-only"` +
			` -c permissions.yardr_read_only.network.enabled=true -c permissions.yardr_read_only.network.unix_sockets={"{socket}"="allow"}` +
			` -c permissions.yardr_read_only.filesystem={"{tmpdir}"="write"}]`,
		"claude": `[--settings {"sandbox":{"enabled":true,"filesystem":{"denyWrite":["{workdir}"]},"network":{"allowUnixSockets":["{socket}"]}}}]`,
	} {
		a := packApp(t)
		args := append([]string{"apply", "contributors", "--members", "maintainers=jo", "--bind", "pr-reviewers=" + kind + ",read-only"}, flags...)
		if out, err := run(t, a.packCmd(), args...); err != nil {
			t.Fatalf("%s: %v\n%s", kind, err, out)
		}
		g, err := a.st.GetGroup(ctx, "web-pr-reviewers")
		if err != nil || g.Config["kind"] != kind || fmt.Sprint(g.Config["args"]) != want ||
			g.Config["prompt"] != filepath.Join(config.At(a.cfg.Home).PackRoles("contributors"), "pr-reviewer.md") {
			t.Fatalf("%s: web-pr-reviewers = %+v, %v; want args %s", kind, g, err, want)
		}
		if g, err := a.st.GetGroup(ctx, "maintainers"); err != nil || g.Runner != "manual" || !slices.Equal(g.Members, []string{"jo"}) {
			t.Fatalf("maintainers = %+v, %v", g, err)
		}
		out, err := run(t, a.packCmd(), "check", "contributors", "--depot", "web")
		if err != nil || !strings.Contains(out, "0 to create, 0 to change, ") ||
			!strings.Contains(out, "type issue: contributors.issue (store)") || !strings.Contains(out, "type pr: contributors.pr (store)") ||
			strings.Contains(out, "contributors.default") {
			t.Fatalf("check after the import: %v\n%s", err, out)
		}
	}
}

// An imported pack is found by its name, read again from the directory the
// import recorded, before any embedded pack of that name; when that
// directory is gone the error names it.
func TestPackByNameOfAnImport(t *testing.T) {
	a := packApp(t)
	requireNonInteractive(t)
	dir := filepath.Join(t.TempDir(), "kept")
	if err := os.CopyFS(dir, os.DirFS("../../packs/starter")); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"apply", dir, "--depot", "web", "--yes"}, starterFlags...)
	if out, err := run(t, a.packCmd(), args...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// From anywhere: the name leads to the recorded directory.
	t.Chdir(t.TempDir())
	out, err := run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if err != nil || !strings.HasPrefix(out, "pack starter ("+dir+"), kind git, into depot web\n") || !strings.Contains(out, "0 to create, 0 to change, ") {
		t.Fatalf("check starter after importing %s: %v\n%s", dir, err, out)
	}
	// Another depot has no import of its own: the name is still the yard's
	// imported pack.
	if err := a.st.PutDepot(context.Background(), store.Depot{Name: "api", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.packCmd(), "check", "starter", "--depot", "api"); err != nil || !strings.HasPrefix(out, "pack starter ("+dir+"), kind git, into depot api\n") {
		t.Fatalf("check starter --depot api: %v\n%s", err, out)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, a.packCmd(), "check", "starter", "--depot", "web")
	if err == nil || !strings.Contains(err.Error(), "pack starter was imported into depot web from "+dir+", which is gone") {
		t.Fatalf("check starter with its source gone: %v", err)
	}
	// The directory, given as one, is still read.
	if _, err := run(t, a.packCmd(), "check", dir, "--depot", "web"); err == nil || !strings.Contains(err.Error(), "not found: a pack is a directory with a pack.yaml") {
		t.Fatalf("check %s: %v", dir, err)
	}
}
