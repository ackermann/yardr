// Tests for contrib/release/stage, the release flow's scripted stages. The
// script is run the way the exec group runs it, against a throwaway git
// repository with a local bare remote, with stand-ins for gh, minisign and
// yardr first on PATH; what it asked of them is asserted from their shared
// call log. CI is the fake gh's canned answers (the draft it serves, the
// runs it lists), so no test reaches GitHub.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/testload"
)

// fakeYardr answers bead show and prime from canned files, logs every call
// with the note it carried on stdin, and mints one sync bead id.
const fakeYardr = `#!/bin/sh
printf 'yardr %s\n' "$*" >>"$FAKE_DIR/calls.log"
case "$1 $2" in
"bead show")
  cat "$FAKE_DIR/show.json"
  ;;
"prime --bead")
  cat "$FAKE_DIR/prime.json"
  ;;
"bead done" | "bead advance")
  { echo "--note--"; cat; echo "--end--"; } >>"$FAKE_DIR/calls.log"
  ;;
"bead create")
  { echo "--create--"; cat; echo "--end--"; } >>"$FAKE_DIR/calls.log"
  printf '{"id": "%s"}\n' "${FAKE_CREATE_ID:-yardr.dev-t1st}"
  ;;
esac
`

// fakeGh serves release view from FAKE_GH_VIEW_SEQ, one response per call
// ($n.json for the nth call, the last one standing after that, a file
// holding ABSENT for no release, and no directory for never one); lists
// the runs FAKE_GH_RUNS cans; saves the notes file a release edit or
// create was given; serves release downloads from a directory; verifies
// attestations as FAKE_GH_ATTEST_RC says; and answers whether Actions is
// enabled from FAKE_GH_ACTIONS_ENABLED.
const fakeGh = `#!/bin/sh
printf 'gh %s\n' "$*" >>"$FAKE_DIR/calls.log"
case "$1 $2" in
"release view")
  if [ -n "${FAKE_GH_VIEW_SEQ:-}" ]; then
    n=1
    if [ -f "$FAKE_DIR/view.n" ]; then n=$(($(cat "$FAKE_DIR/view.n") + 1)); fi
    echo "$n" >"$FAKE_DIR/view.n"
    f="$FAKE_GH_VIEW_SEQ/$n.json"
    if [ ! -f "$f" ]; then
      last=$(ls "$FAKE_GH_VIEW_SEQ" 2>/dev/null | grep -E '^[0-9]+\.json$' | sort -n | tail -n 1)
      if [ -n "$last" ]; then f="$FAKE_GH_VIEW_SEQ/$last"; else f=""; fi
    fi
    if [ -z "$f" ] || [ "$(cat "$f")" = ABSENT ]; then exit 1; fi
    cat "$f"
    exit 0
  fi
  exit "${FAKE_GH_VIEW_RC:-1}"
  ;;
"release download")
  rc="${FAKE_GH_DOWNLOAD_RC:-0}"
  if [ "$rc" -eq 0 ]; then
    prev=""
    for a in "$@"; do
      if [ "$prev" = "--dir" ]; then dir="$a"; fi
      prev="$a"
    done
    cp -R "$FAKE_RELEASE_DIR"/. "$dir"/
  fi
  exit "$rc"
  ;;
"release edit")
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--notes-file" ]; then cp "$a" "$FAKE_DIR/notes.md"; fi
    prev="$a"
  done
  exit "${FAKE_GH_EDIT_RC:-0}"
  ;;
"release create")
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--notes-file" ]; then cp "$a" "$FAKE_DIR/notes.md"; fi
    prev="$a"
  done
  exit "${FAKE_GH_CREATE_RC:-0}"
  ;;
"run list")
  printf '%s\n' "${FAKE_GH_RUNS:-[]}"
  ;;
"attestation verify")
  exit "${FAKE_GH_ATTEST_RC:-0}"
  ;;
api*)
  printf '%s\n' "${FAKE_GH_ACTIONS_ENABLED:-}"
  ;;
esac
exit 0
`

// fakeMinisign verifies as its exit status says, printing its canned line.
const fakeMinisign = `#!/bin/sh
printf 'minisign %s\n' "$*" >>"$FAKE_DIR/calls.log"
printf '%s\n' "${FAKE_MINISIGN_OUT:-Signature and comment signature verified}"
exit "${FAKE_MINISIGN_RC:-0}"
`

// fakeBuild stands in for contrib/release/build in the checkout: it logs
// its arguments and SOURCE_DATE_EPOCH, and writes four archives, a
// checksums.txt and, unless FAKE_BUILD_NO_INSTALLER is set (another depot's
// build, which has none), an install.sh to its out-dir.
const fakeBuild = `#!/bin/sh
printf 'build %s SOURCE_DATE_EPOCH=%s\n' "$*" "${SOURCE_DATE_EPOCH:-}" >>"$FAKE_DIR/calls.log"
out=$2
mkdir -p "$out"
targets=${YARDR_RELEASE_TARGETS:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64}
for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  printf 'fake %s\n' "$t" >"$out/yardr_${1#v}_${os}_${arch}.tar.gz"
done
printf 'fake checksums\n' >"$out/checksums.txt"
[ -n "${FAKE_BUILD_NO_INSTALLER:-}" ] || printf 'fake installer\n' >"$out/install.sh"
exit "${FAKE_BUILD_RC:-0}"
`

// stage is one run's world: the stand-ins, their canned answers, and the
// environment the script runs with.
type stage struct {
	t   *testing.T
	dir string // FAKE_DIR: show.json, prime.json, calls.log, notes.md
	bin string // the stand-ins, first on PATH
	env []string
}

var gitEnv = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
	"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// The stage runs make their repositories and the scripts make git calls of
// their own, many in all: on macOS each is two execs, the system's shim and the
// git it runs, so the real git goes first on PATH, for the tests and the
// scripts they start (testload.PreferRealGit). Inherited Git settings reach
// every subprocess, and GIT_EXEC_PATH can make the shim report a directory
// unrelated to the git it runs, so what the pane or hook that runs the tests
// sets for git goes first.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	testload.PreferRealGit()
	os.Exit(m.Run())
}

// sumCmd is what checksums are made and checked with here.
func sumCmd(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("shasum"); err == nil {
		return "shasum -a 256"
	}
	if _, err := exec.LookPath("sha256sum"); err == nil {
		return "sha256sum"
	}
	t.Fatal("neither shasum nor sha256sum is on PATH")
	return ""
}

func newStage(t *testing.T) *stage {
	t.Helper()
	// A stage run's world is temp dirs of its own (the stand-ins, their
	// canned answers, the repositories), so it runs beside the others from
	// here on.
	t.Parallel()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH, which the stage script needs")
	}
	s := &stage{t: t, dir: t.TempDir(), bin: t.TempDir()}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(s.bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("yardr", fakeYardr)
	write("gh", fakeGh)
	write("minisign", fakeMinisign)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") {
			continue
		}
		s.env = append(s.env, kv)
	}
	s.env = append(s.env, gitEnv...)
	s.env = append(s.env,
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_DIR="+s.dir,
		"YARDR_BIN=yardr",
		"YARDR_BEAD=yardr-rel1",
		"YARDR_BASE=main",
	)
	s.prime("done", "failed", "question")
	return s
}

// show cans the bead's title and body, through real JSON encoding, so the
// script's jq reads what the yard's would.
func (s *stage) show(title, body string) {
	s.t.Helper()
	out, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "show.json"), out, 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// prime cans the flow's outcomes from here, in the shape has_outcome greps.
func (s *stage) prime(names ...string) {
	s.t.Helper()
	var b strings.Builder
	b.WriteString("{\n  \"next\": {\n    \"stage\": \"here\",\n    \"outcomes\": [\n")
	for i, n := range names {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "      {\n        \"name\": %q,\n        \"to\": \"somewhere\"\n      }", n)
	}
	if len(names) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("    ]\n  }\n}\n")
	if err := os.WriteFile(filepath.Join(s.dir, "prime.json"), []byte(b.String()), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// viewAbsent is a release view response for no release standing.
const viewAbsent = "ABSENT\n"

// view cans what gh release view serves, one response per call; calls past
// the last are served the last response again.
func (s *stage) view(responses ...string) {
	s.t.Helper()
	dir := s.t.TempDir()
	for i, r := range responses {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", i+1)), []byte(r), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	s.env = append(s.env, "FAKE_GH_VIEW_SEQ="+dir)
}

// draftJSON is a release view answer holding these asset names.
func draftJSON(names []string, isDraft bool) string {
	assets := make([]string, 0, len(names))
	for _, n := range names {
		assets = append(assets, fmt.Sprintf(`{"name": %q}`, n))
	}
	return fmt.Sprintf(`{"isDraft": %v, "assets": [%s]}`, isDraft, strings.Join(assets, ","))
}

// run runs ./stage the way the exec group does and returns what it printed;
// the script ends every path in a move and exits 0.
func (s *stage) run(mode string, extra ...string) string {
	s.t.Helper()
	cmd := exec.Command("./stage", mode)
	cmd.Env = append(append([]string{}, s.env...), extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("stage %s: %v\n%s", mode, err, out)
	}
	return string(out)
}

// log is what the stand-ins were asked.
func (s *stage) log() string {
	s.t.Helper()
	out, err := os.ReadFile(filepath.Join(s.dir, "calls.log"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return string(out)
}

// repo makes a clean checkout on main with a bare origin that has it.
func (s *stage) repo() (checkout, origin string) {
	s.t.Helper()
	checkout = s.t.TempDir()
	origin = filepath.Join(s.t.TempDir(), "origin.git")
	s.git("", "init", "--bare", "-q", origin)
	s.git("", "init", "-b", "main", "-q", checkout)
	s.git(checkout, "remote", "add", "origin", origin)
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("v1\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.git(checkout, "add", "file")
	s.git(checkout, "commit", "-q", "-m", "one")
	s.git(checkout, "push", "-q", "origin", "main")
	return checkout, origin
}

// build installs the fake release build script in the checkout and pushes
// it, as the real one rides main: the stage runs it from there when
// Actions is off, and refuses a dirty or behind checkout first.
func (s *stage) build(checkout string) {
	s.t.Helper()
	dir := filepath.Join(checkout, "contrib", "release")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build"), []byte(fakeBuild), 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.git(checkout, "add", "contrib/release/build")
	s.git(checkout, "commit", "-q", "-m", "fake build")
	s.git(checkout, "push", "-q", "origin", "main")
}

func (s *stage) git(dir string, args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (s *stage) mustContain(haystack, needle string) {
	s.t.Helper()
	if !strings.Contains(haystack, needle) {
		s.t.Fatalf("want %q in:\n%s", needle, haystack)
	}
}

func (s *stage) mustLack(haystack, needle string) {
	s.t.Helper()
	if strings.Contains(haystack, needle) {
		s.t.Fatalf("want no %q in:\n%s", needle, haystack)
	}
}

const releaseKey = "RWSzUPGBbv7MDqH+nnbNmYdOm20UTkuk7eC5e6XuVtYRwZtaEZRlV/WB"

func TestStageBuildRefusesABadTitle(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("cut a release sometime", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout)
	log := s.log()
	s.mustContain(log, "yardr bead done yardr-rel1 --outcome question --note -")
	s.mustContain(log, "names no version")
	s.mustLack(log, "gh release")
	s.mustLack(out, "built")
	probe := exec.Command("git", "-C", checkout, "rev-parse", "--verify", "-q", "refs/tags/v1.2.3")
	probe.Env = s.env
	if err := probe.Run(); err == nil {
		t.Fatal("a tag was made for a refused build")
	}
}

// A title is text from whoever filed the bead, a linked yard's among them,
// and may hold a newline: a version on a line of its own below anything
// else is no version, so nothing else of the title reaches a tag, a gh
// argument or the signing commands mckean pastes.
func TestStageBuildRefusesAVersionOnALineOfItsOwn(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr $(touch${IFS}pwned)\nv1.2.3", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout)
	log := s.log()
	s.mustContain(log, "yardr bead done yardr-rel1 --outcome question --note -")
	s.mustContain(log, "names no version")
	s.mustLack(log, "gh release")
	s.mustLack(out, "built")
}

func TestStageBuildRefusesADirtyTree(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	if err := os.WriteFile(filepath.Join(checkout, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.show("yardr v1.2.3", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout)
	log := s.log()
	s.mustContain(log, "yardr bead done yardr-rel1 --outcome failed --note -")
	s.mustContain(log, "uncommitted or untracked files")
	s.mustLack(log, "gh run list")
	s.mustLack(out, "draft")
}

func TestStageBuildRefusesAPublishedRelease(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.view(draftJSON([]string{"checksums.txt"}, false))
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "already exists")
	s.mustContain(got, "published")
	s.mustLack(got, "gh run list")
	probe := exec.Command("git", "-C", checkout, "rev-parse", "--verify", "-q", "refs/tags/v1.2.3")
	probe.Env = s.env
	if err := probe.Run(); err == nil {
		t.Fatal("a tag was made for a refused build")
	}
}

func TestStageBuildRefusesATagAtAnotherCommit(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.git(checkout, "tag", "v1.2.3")
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.git(checkout, "commit", "-q", "-am", "two")
	s.git(checkout, "push", "-q", "origin", "main")
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "at another commit")
	s.mustLack(got, "gh run list")
}

func TestStageBuildRefusesACheckoutBehindItsRemote(t *testing.T) {
	s := newStage(t)
	checkout, origin := s.repo()
	// The remote moves on without the checkout.
	other := t.TempDir()
	s.git("", "clone", "-q", "-b", "main", origin, other)
	if err := os.WriteFile(filepath.Join(other, "file"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.git(other, "commit", "-q", "-am", "two")
	s.git(other, "push", "-q", "origin", "main")
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "not at origin/main")
	s.mustLack(got, "gh run list")
}

func TestStageBuildTagsPushesAndWaitsForTheDraft(t *testing.T) {
	s := newStage(t)
	checkout, origin := s.repo()
	complete := []string{
		"yardr_1.2.3_darwin_amd64.tar.gz", "yardr_1.2.3_darwin_arm64.tar.gz",
		"yardr_1.2.3_linux_amd64.tar.gz", "yardr_1.2.3_linux_arm64.tar.gz",
		"checksums.txt",
	}
	// No release, then a draft still filling, then the whole draft: the
	// build waits through the first two rounds, not just one look.
	s.view(viewAbsent, draftJSON(complete[:2], true), draftJSON(complete, true))
	body := "Release \"v1.2.3\"\n\n- fixes <bugs> & things"
	s.show("yardr v1.2.3", body)
	s.run("build", "YARDR_DEPOT_PATH="+checkout,
		`FAKE_GH_RUNS=[{"status":"in_progress","conclusion":"","url":"https://example.com/run/1"}]`,
		"YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	// The tag is at the base, here and on the remote.
	at := strings.TrimSpace(s.git(checkout, "rev-parse", "refs/tags/v1.2.3"))
	base := strings.TrimSpace(s.git(checkout, "rev-parse", "refs/heads/main"))
	if at != base {
		t.Fatalf("tag v1.2.3 is at %s, not at main", at)
	}
	remote := exec.Command("git", "--git-dir="+origin, "rev-parse", "refs/tags/v1.2.3")
	remote.Env = s.env
	out, err := remote.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != at {
		t.Fatalf("tag v1.2.3 is not pushed: %v\n%s", err, out)
	}
	// The build makes no release of its own; it asks CI's run after this
	// commit, and the release after its assets, until the draft is whole.
	s.mustLack(got, "gh release create")
	s.mustContain(got, "gh run list --repo yardr-dev/yardr --workflow release.yml --commit "+base+" --event push")
	s.mustContain(got, "gh release view v1.2.3 --repo yardr-dev/yardr --json isDraft")
	s.mustContain(got, "gh release view v1.2.3 --repo yardr-dev/yardr --json assets")
	if n := strings.Count(got, "gh release view"); n != 3 {
		t.Errorf("gh release view was asked %d times; want 3 (absent, filling, whole)", n)
	}
	// The draft's notes are the body, set once it is whole.
	s.mustContain(got, "gh release edit v1.2.3 --repo yardr-dev/yardr --notes-file")
	notes, err := os.ReadFile(filepath.Join(s.dir, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != body+"\n" {
		t.Fatalf("notes file = %q; want the body's bytes", notes)
	}
	// Done, with the run's URL and the five commands, each naming the version.
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
	s.mustContain(got, "is drafted")
	s.mustContain(got, "built by CI")
	s.mustContain(got, "https://example.com/run/1")
	for _, cmd := range []string{
		"mkdir -p ~/releases/v1.2.3",
		"gh release download v1.2.3 --repo yardr-dev/yardr --pattern checksums.txt --dir ~/releases/v1.2.3 --clobber",
		"minisign -S -s ~/.minisign/yardr-release.key -m ~/releases/v1.2.3/checksums.txt",
		"minisign -V -P " + releaseKey + " -m ~/releases/v1.2.3/checksums.txt",
		"gh release upload v1.2.3 --repo yardr-dev/yardr ~/releases/v1.2.3/checksums.txt.minisig --clobber",
	} {
		s.mustContain(got, cmd)
	}
}

func TestStageBuildBuildsOnThisMachineWhenActionsIsOff(t *testing.T) {
	s := newStage(t)
	checkout, origin := s.repo()
	s.build(checkout)
	// No release standing: the build makes the draft itself, never waiting.
	s.view(viewAbsent)
	body := "Release \"v1.2.3\"\n\n- fixes <bugs> & things"
	s.show("yardr v1.2.3", body)
	s.run("build", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_ACTIONS_ENABLED=false")
	got := s.log()
	// The tag is at the base, here and on the remote.
	at := strings.TrimSpace(s.git(checkout, "rev-parse", "refs/tags/v1.2.3"))
	base := strings.TrimSpace(s.git(checkout, "rev-parse", "refs/heads/main"))
	if at != base {
		t.Fatalf("tag v1.2.3 is at %s, not at main", at)
	}
	remote := exec.Command("git", "--git-dir="+origin, "rev-parse", "refs/tags/v1.2.3")
	remote.Env = s.env
	out, err := remote.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != at {
		t.Fatalf("tag v1.2.3 is not pushed: %v\n%s", err, out)
	}
	// Actions was asked, the checkout's build script ran with the tagged
	// commit's time, and the draft was created from it; CI was never asked.
	s.mustContain(got, "gh api repos/yardr-dev/yardr/actions/permissions --jq .enabled")
	epoch := strings.TrimSpace(s.git(checkout, "log", "-1", "--format=%ct", base))
	s.mustContain(got, "build v1.2.3")
	s.mustContain(got, "SOURCE_DATE_EPOCH="+epoch)
	s.mustContain(got, "gh release create v1.2.3 --repo yardr-dev/yardr --draft --verify-tag --notes-file")
	s.mustContain(got, ".tar.gz")
	s.mustContain(got, "checksums.txt")
	// The installer the build wrote goes up beside the archives.
	s.mustContain(got, "dist/install.sh")
	s.mustLack(got, "gh run list")
	if n := strings.Count(got, "gh release view"); n != 1 {
		t.Errorf("gh release view was asked %d times; want 1 (the look before tagging)", n)
	}
	// The draft's notes are the body, set at creation.
	s.mustLack(got, "gh release edit")
	notes, err := os.ReadFile(filepath.Join(s.dir, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != body+"\n" {
		t.Fatalf("notes file = %q; want the body's bytes", notes)
	}
	// Done, with no attestation and the five commands, each naming the version.
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
	s.mustContain(got, "is drafted")
	s.mustContain(got, "built on this machine")
	s.mustContain(got, "no attestation")
	s.mustLack(got, "built by CI")
	for _, cmd := range []string{
		"mkdir -p ~/releases/v1.2.3",
		"gh release download v1.2.3 --repo yardr-dev/yardr --pattern checksums.txt --dir ~/releases/v1.2.3 --clobber",
		"minisign -S -s ~/.minisign/yardr-release.key -m ~/releases/v1.2.3/checksums.txt",
		"minisign -V -P " + releaseKey + " -m ~/releases/v1.2.3/checksums.txt",
		"gh release upload v1.2.3 --repo yardr-dev/yardr ~/releases/v1.2.3/checksums.txt.minisig --clobber",
	} {
		s.mustContain(got, cmd)
	}
}

// Another depot's release flow runs this script too, with a build that writes
// no installer: nothing is uploaded under that name.
func TestStageBuildUploadsNoInstallerThatTheBuildDidNotWrite(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.build(checkout)
	s.view(viewAbsent)
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_ACTIONS_ENABLED=false", "FAKE_BUILD_NO_INSTALLER=1")
	got := s.log()
	s.mustContain(got, "gh release create v1.2.3 --repo yardr-dev/yardr --draft --verify-tag --notes-file")
	s.mustContain(got, "checksums.txt")
	s.mustLack(got, "install.sh")
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
}

func TestStageBuildAdoptsAStandingDraftWhenActionsIsOff(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.build(checkout)
	// A run that died after tagging: the tag is already at the base, and a
	// draft is already standing. It is adopted and filled again, not
	// refused or remade.
	s.git(checkout, "tag", "v1.2.3", "refs/heads/main")
	s.view(draftJSON([]string{"a.tar.gz", "b.tar.gz", "c.tar.gz", "d.tar.gz", "checksums.txt"}, true))
	s.show("yardr v1.2.3", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_ACTIONS_ENABLED=false")
	got := s.log()
	s.mustContain(out, "finishing that run")
	s.mustContain(out, "adopting it")
	s.mustContain(got, "build v1.2.3")
	s.mustContain(got, "gh release upload v1.2.3 --repo yardr-dev/yardr")
	s.mustContain(got, "dist/install.sh")
	s.mustLack(got, "gh release create")
	s.mustContain(got, "gh release edit v1.2.3 --repo yardr-dev/yardr --notes-file")
	s.mustLack(got, "gh run list")
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustContain(got, "built on this machine")
}

func TestStageBuildRejectsAnIncompleteLocalTargetSet(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.build(checkout)
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_ACTIONS_ENABLED=false",
		"YARDR_RELEASE_TARGETS=darwin/amd64 darwin/arm64 linux/amd64 windows/amd64")
	got := s.log()
	s.mustContain(got, "did not make the four expected archives")
	s.mustContain(got, "yardr_1.2.3_linux_arm64.tar.gz")
	s.mustContain(got, "--outcome failed")
	s.mustLack(got, "gh release create")
	s.mustLack(got, "gh run list")
}

func TestStageBuildResumesAfterTagging(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	// A run that died after tagging: the tag is already at the base.
	s.git(checkout, "tag", "v1.2.3", "refs/heads/main")
	s.view(draftJSON([]string{"a.tar.gz", "b.tar.gz", "c.tar.gz", "d.tar.gz", "checksums.txt"}, true))
	s.show("yardr v1.2.3", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout, "YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	s.mustContain(out, "finishing that run")
	s.mustContain(out, "adopting it")
	s.mustContain(got, "gh release edit v1.2.3 --repo yardr-dev/yardr --notes-file")
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
}

func TestStageBuildRefusesAnEmptyBody(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "")
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "--outcome question")
	s.mustContain(got, "no body")
	s.mustLack(got, "gh run list")
}

func TestStageBuildFailsWhenCIFails(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout,
		`FAKE_GH_RUNS=[{"status":"completed","conclusion":"failure","url":"https://example.com/run/9"}]`,
		"YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	s.mustContain(got, "yardr bead done yardr-rel1 --outcome failed --note -")
	s.mustContain(got, "CI's release run for v1.2.3 failure")
	s.mustContain(got, "https://example.com/run/9")
	s.mustContain(got, "delete the tag")
	s.mustLack(got, "gh release edit")
}

func TestStageBuildTimesOutWaitingForTheDraft(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout,
		"YARDR_RELEASE_WAIT_ROUNDS=3", "YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "did not finish the draft")
	s.mustContain(got, "no release run has shown")
	if n := strings.Count(got, "gh release view"); n != 4 {
		t.Errorf("gh release view was asked %d times; want 4 (one look, three rounds)", n)
	}
	if n := strings.Count(got, "gh run list"); n != 3 {
		t.Errorf("gh run list was asked %d times; want 3 (one per round)", n)
	}
}

func TestStageBuildTimeoutNamesTheRun(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout,
		`FAKE_GH_RUNS=[{"status":"in_progress","conclusion":"","url":"https://example.com/run/2"}]`,
		"YARDR_RELEASE_WAIT_ROUNDS=2", "YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "did not finish the draft")
	s.mustContain(got, "https://example.com/run/2")
}

func TestStageBuildFailsWhenTheNotesAreNotSet(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.view(draftJSON([]string{"a.tar.gz", "b.tar.gz", "c.tar.gz", "d.tar.gz", "checksums.txt"}, true))
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout,
		"FAKE_GH_EDIT_RC=1", "YARDR_RELEASE_WAIT_SLEEP=0")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "setting its notes from the bead failed")
	s.mustContain(got, "advance the bead to sign")
}

func TestStageHoldsWhenTheFlowOffersNoOutcome(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.prime()
	s.show("cut a release sometime", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "yardr bead note yardr-rel1 build: the bead's title names no version")
	s.mustContain(got, "yardr bead hold yardr-rel1")
	s.mustLack(got, "bead done")
}

// release serves verify's download: archives with a checksums.txt over
// them, a signature beside it unless told otherwise.
func (s *stage) release(archives map[string]string, corrupt, minisig bool) {
	s.t.Helper()
	dir := s.t.TempDir()
	for name, content := range archives {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	names := make([]string, 0, len(archives))
	for name := range archives {
		names = append(names, name)
	}
	words := strings.Fields(sumCmd(s.t))
	cmd := exec.Command(words[0], append(append([]string{}, words[1:]...), names...)...)
	cmd.Dir = dir
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("checksums: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), out, 0o644); err != nil {
		s.t.Fatal(err)
	}
	if corrupt {
		for name := range archives {
			f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				s.t.Fatal(err)
			}
			fmt.Fprintln(f, "corrupted")
			f.Close()
			break
		}
	}
	if minisig {
		if err := os.WriteFile(filepath.Join(dir, "checksums.txt.minisig"), []byte("sig\n"), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	s.env = append(s.env, "FAKE_RELEASE_DIR="+dir)
}

// releaseDir is where release put verify's download.
func (s *stage) releaseDir() string {
	s.t.Helper()
	for _, kv := range s.env {
		if v, ok := strings.CutPrefix(kv, "FAKE_RELEASE_DIR="); ok {
			return v
		}
	}
	s.t.Fatal("no release was served")
	return ""
}

func TestStageVerifyChecksASignedRelease(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n", "b.tar.gz": "b\n"}, false, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify")
	got := s.log()
	s.mustContain(got, "gh release download v1.2.3 --repo yardr-dev/yardr")
	s.mustContain(got, "minisign -V -P "+releaseKey)
	s.mustContain(got, "gh attestation verify")
	s.mustContain(got, "a.tar.gz --repo yardr-dev/yardr")
	s.mustContain(got, "b.tar.gz --repo yardr-dev/yardr")
	s.mustContain(got, "--signer-workflow yardr-dev/yardr/.github/workflows/release.yml --source-ref refs/tags/v1.2.3")
	if n := strings.Count(got, "attestation verify"); n != 2 {
		t.Errorf("attestation verify ran %d times; want once per archive", n)
	}
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
	s.mustContain(got, "the signature on v1.2.3 is good")
	s.mustContain(got, "2 archives match checksums.txt")
	s.mustContain(got, "attestation verifies")
}

func TestStageVerifySkipsAttestationsWhenActionsIsOff(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n", "b.tar.gz": "b\n"}, false, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify", "FAKE_GH_ACTIONS_ENABLED=false")
	got := s.log()
	s.mustContain(got, "gh api repos/yardr-dev/yardr/actions/permissions --jq .enabled")
	s.mustContain(got, "gh release download v1.2.3 --repo yardr-dev/yardr")
	s.mustContain(got, "minisign -V -P "+releaseKey)
	s.mustLack(got, "attestation verify")
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
	s.mustContain(got, "the signature on v1.2.3 is good")
	s.mustContain(got, "2 archives match checksums.txt")
	s.mustContain(got, "no attestation: built on this machine")
}

func TestStageVerifyQuestionsABadAttestation(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n", "b.tar.gz": "b\n"}, false, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify", "FAKE_GH_ATTEST_RC=1")
	got := s.log()
	s.mustContain(got, "yardr bead done yardr-rel1 --outcome question --note -")
	s.mustContain(got, "the attestation for")
	s.mustContain(got, "is missing or bad")
}

func TestStageVerifyFailsABadSignature(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n"}, false, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify", "FAKE_MINISIGN_RC=1", "FAKE_MINISIGN_OUT=Signature verification failed")
	got := s.log()
	s.mustContain(got, "yardr bead done yardr-rel1 --outcome failed --note -")
	s.mustContain(got, "the signature on release v1.2.3 is bad")
	s.mustContain(got, "Signature verification failed")
}

func TestStageVerifyFailsAMissingSignature(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n"}, false, false)
	s.show("yardr v1.2.3", "notes")
	s.run("verify")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "checksums.txt.minisig is not on release v1.2.3")
}

func TestStageVerifyQuestionsMismatchedArchives(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n", "b.tar.gz": "b\n"}, true, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify")
	got := s.log()
	s.mustContain(got, "--outcome question")
	s.mustContain(got, "do not match its checksums.txt")
}

func TestStageVerifyQuestionsAnUnlistedFile(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n"}, false, true)
	dir := ""
	for _, kv := range s.env {
		if v, ok := strings.CutPrefix(kv, "FAKE_RELEASE_DIR="); ok {
			dir = v
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stowaway.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.show("yardr v1.2.3", "notes")
	s.run("verify")
	got := s.log()
	s.mustContain(got, "--outcome question")
	s.mustContain(got, "does not name")
	s.mustContain(got, "stowaway.tar.gz")
}

// install.sh rides beside the archives with no checksum, so verify holds it to
// the tag it was cut from: the release has the tagged commit's own, byte for
// byte, and has one where the tag does.
func TestStageVerifyHoldsTheInstallerToTheTag(t *testing.T) {
	const one, two = "#!/bin/sh\necho one\n", "#!/bin/sh\necho two\n"
	const notTags = "is not tag v1.2.3's own"
	for _, tc := range []struct {
		name    string
		release string // the install.sh on the release; "-" for none
		tagged  string // the install.sh the tag holds; "-" for none
		noDepot bool   // verify is not told where the depot is
		want    string // what a question says, else it passes
	}{
		{"the tag's own", one, one, false, ""},
		{"another file", two, one, false, notTags},
		{"an empty one, where the tag has none", "", "-", false, notTags},
		{"none, where the tag has none", "-", "-", false, ""},
		{"none, where the tag has one", "-", one, false, "tag v1.2.3 has an install.sh and release v1.2.3 holds none"},
		{"one, where the tag has none", one, "-", false, notTags},
		{"one, with no depot to compare it in", one, one, true, "compared in no depot"},
		{"none, with no depot to ask", "-", one, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStage(t)
			checkout, _ := s.repo()
			if tc.tagged != "-" {
				if err := os.WriteFile(filepath.Join(checkout, "install.sh"), []byte(tc.tagged), 0o755); err != nil {
					t.Fatal(err)
				}
				s.git(checkout, "add", "install.sh")
				s.git(checkout, "commit", "-q", "-m", "installer")
			}
			s.git(checkout, "tag", "v1.2.3")
			s.release(map[string]string{"a.tar.gz": "a\n"}, false, true)
			if tc.release != "-" {
				if err := os.WriteFile(filepath.Join(s.releaseDir(), "install.sh"), []byte(tc.release), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s.show("yardr v1.2.3", "notes")
			var extra []string
			if !tc.noDepot {
				extra = append(extra, "YARDR_DEPOT_PATH="+checkout)
			}
			s.run("verify", extra...)
			got := s.log()
			if tc.want != "" {
				s.mustContain(got, "yardr bead done yardr-rel1 --outcome question --note -")
				s.mustContain(got, tc.want)
				s.mustLack(got, "gh attestation verify")
				return
			}
			s.mustContain(got, "yardr bead done yardr-rel1 --note -")
			s.mustLack(got, "--outcome")
			s.mustContain(got, "1 archives match checksums.txt")
			if tc.release != "-" {
				s.mustContain(got, "Its install.sh is tag v1.2.3's own.")
			} else {
				s.mustLack(got, "install.sh")
			}
		})
	}
}

// A yardr tag that holds an install.sh files a sync bead naming
// public/install.sh as the one other path the sync writes; a tag without
// one files the bead as before.
func TestStagePublishFilesTheSyncBead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installer bool // the tag holds an install.sh
	}{
		{"without the installer", false},
		{"with the installer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStage(t)
			checkout, _ := s.repo()
			if tc.installer {
				if err := os.WriteFile(filepath.Join(checkout, "install.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				s.git(checkout, "add", "install.sh")
				s.git(checkout, "commit", "-q", "-m", "installer")
			}
			s.git(checkout, "tag", "v1.2.3")
			at := strings.TrimSpace(s.git(checkout, "rev-parse", "--short", "refs/tags/v1.2.3"))
			s.show("yardr v1.2.3", "notes")
			s.run("publish", "YARDR_DEPOT_PATH="+checkout, "FAKE_CREATE_ID=yardr.dev-s9nc")
			got := s.log()
			title := "Sync yardr v1.2.3 to yardr.dev: scripts/sync-release yardr v1.2.3"
			s.mustContain(got, "gh release edit v1.2.3 --repo yardr-dev/yardr --draft=false")
			s.mustContain(got, "yardr bead create "+title+" -d yardr.dev -p 1 -b - -l difficulty:1,size:small --json")
			s.mustContain(got, "https://github.com/yardr-dev/yardr/releases/tag/v1.2.3")
			s.mustContain(got, "built from "+at)
			s.mustContain(got, "scripts/sync-release yardr v1.2.3")
			s.mustContain(got, "yardr bead advance yardr.dev-s9nc --to new")
			s.mustContain(got, "publish: filed yardr.dev-s9nc")
			s.mustContain(got, "released from its draft")
			s.mustContain(got, "yardr bead done yardr-rel1 --note -")
			s.mustLack(got, "--outcome")
			if tc.installer {
				s.mustContain(got, "copies the release's install.sh to public/install.sh")
				s.mustContain(got, "shows only public/releases/ and public/install.sh")
			} else {
				s.mustContain(got, "shows only public/releases/ (the yardr paths, not footplate's)")
				s.mustLack(got, "public/install.sh")
			}
		})
	}
}

func TestStagePublishFailsWhenTheDraftIsNotReleased(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.git(checkout, "tag", "v1.2.3")
	s.show("yardr v1.2.3", "notes")
	s.run("publish", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_EDIT_RC=1")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "releasing the draft for v1.2.3 failed")
	s.mustLack(got, "bead create")
}

func TestStagePublishRefusesAMissingTag(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "notes")
	s.run("publish", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "tag v1.2.3 is not in")
	s.mustLack(got, "bead create")
}
