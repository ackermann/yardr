// Tests for contrib/release/stage, the release flow's scripted stages. The
// script is run the way the exec group runs it, against a throwaway git
// repository with a local bare remote, with stand-ins for gh, minisign and
// yardr first on PATH; what it asked of them is asserted from their shared
// call log. The build step runs a stand-in build script (the real
// contrib/release/build is build_test.go's), so no test compiles yardr.
package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// fakeGh answers release view from an exit status, saves the notes file a
// release create was given, and serves release downloads from a directory.
const fakeGh = `#!/bin/sh
printf 'gh %s\n' "$*" >>"$FAKE_DIR/calls.log"
case "$1 $2" in
"release view")
  exit "${FAKE_GH_VIEW_RC:-1}"
  ;;
"release create")
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--notes-file" ]; then cp "$a" "$FAKE_DIR/notes.md"; fi
    prev="$a"
  done
  exit "${FAKE_GH_CREATE_RC:-0}"
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
esac
exit 0
`

// fakeMinisign verifies as its exit status says, printing its canned line.
const fakeMinisign = `#!/bin/sh
printf 'minisign %s\n' "$*" >>"$FAKE_DIR/calls.log"
printf '%s\n' "${FAKE_MINISIGN_OUT:-Signature and comment signature verified}"
exit "${FAKE_MINISIGN_RC:-0}"
`

// fakeBuild stands in for contrib/release/build: four archives and a
// checksums.txt over them, and a log of where it ran and with what cache.
const fakeBuild = `#!/bin/sh
# fakebuild <version> <out-dir>
{
  echo "fakebuild $*"
  echo "pwd: $PWD"
  echo "gocache: $GOCACHE"
  echo "out: $2"
} >>"$FAKE_DIR/calls.log"
for target in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  printf 'fake yardr %s for %s\n' "$1" "$target" >"$2/yardr_fake_${1}_$target.tar.gz"
done
(cd "$2" && $FAKE_SUM yardr_fake_*.tar.gz >checksums.txt)
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
	write("fakebuild", fakeBuild)
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
		"FAKE_SUM="+sumCmd(t),
		"YARDR_BIN=yardr",
		"YARDR_BEAD=yardr-rel1",
		"YARDR_BASE=main",
		"YARDR_RELEASE_BUILD="+filepath.Join(s.bin, "fakebuild"),
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
	s.mustLack(log, "gh release create")
	s.mustLack(out, "built")
}

func TestStageBuildRefusesAnExistingRelease(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.show("yardr v1.2.3", "notes")
	s.run("build", "YARDR_DEPOT_PATH="+checkout, "FAKE_GH_VIEW_RC=0")
	got := s.log()
	s.mustContain(got, "--outcome failed")
	s.mustContain(got, "already exists")
	s.mustLack(got, "gh release create")
	s.mustLack(got, "fakebuild")
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
	s.mustLack(got, "fakebuild")
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
	s.mustLack(got, "fakebuild")
}

func TestStageBuildReleases(t *testing.T) {
	s := newStage(t)
	checkout, origin := s.repo()
	body := "Release \"v1.2.3\"\n\n- fixes <bugs> & things"
	s.show("yardr v1.2.3", body)
	s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	// The tag is at the base, here and on the remote.
	at := strings.TrimSpace(s.git(checkout, "rev-parse", "refs/tags/v1.2.3"))
	if at != strings.TrimSpace(s.git(checkout, "rev-parse", "refs/heads/main")) {
		t.Fatalf("tag v1.2.3 is at %s, not at main", at)
	}
	remote := exec.Command("git", "--git-dir="+origin, "rev-parse", "refs/tags/v1.2.3")
	remote.Env = s.env
	out, err := remote.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != at {
		t.Fatalf("tag v1.2.3 is not pushed: %v\n%s", err, out)
	}
	// The build ran in the depot, into a private dir with a private cache.
	s.mustContain(got, "fakebuild v1.2.3")
	s.mustContain(got, "pwd: "+checkout)
	s.mustContain(got, "gocache: ")
	s.mustLack(got, "gocache: \n")
	// The release carries the five files with the body as its notes.
	s.mustContain(got, "gh release create v1.2.3")
	s.mustContain(got, "--repo yardr-dev/yardr")
	s.mustContain(got, "--title v1.2.3")
	s.mustContain(got, "--verify-tag")
	for _, target := range []string{"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"} {
		s.mustContain(got, "yardr_fake_v1.2.3_"+target+".tar.gz")
	}
	s.mustContain(got, "checksums.txt")
	notes, err := os.ReadFile(filepath.Join(s.dir, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(notes) != body+"\n" {
		t.Fatalf("notes file = %q; want the body's bytes", notes)
	}
	// Done, with the five commands, each naming the version.
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
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

func TestStageBuildResumesAfterTagging(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	// A run that died after tagging: the tag is already at the base.
	s.git(checkout, "tag", "v1.2.3", "refs/heads/main")
	s.show("yardr v1.2.3", "notes")
	out := s.run("build", "YARDR_DEPOT_PATH="+checkout)
	got := s.log()
	s.mustContain(out, "finishing that run")
	s.mustContain(got, "gh release create v1.2.3")
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
	s.mustLack(got, "fakebuild")
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

func TestStageVerifyChecksASignedRelease(t *testing.T) {
	s := newStage(t)
	s.release(map[string]string{"a.tar.gz": "a\n", "b.tar.gz": "b\n"}, false, true)
	s.show("yardr v1.2.3", "notes")
	s.run("verify")
	got := s.log()
	s.mustContain(got, "gh release download v1.2.3 --repo yardr-dev/yardr")
	s.mustContain(got, "minisign -V -P "+releaseKey)
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
	s.mustContain(got, "the signature on v1.2.3 is good")
	s.mustContain(got, "2 archives match checksums.txt")
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

func TestStagePublishFilesTheSyncBead(t *testing.T) {
	s := newStage(t)
	checkout, _ := s.repo()
	s.git(checkout, "tag", "v1.2.3")
	at := strings.TrimSpace(s.git(checkout, "rev-parse", "--short", "refs/tags/v1.2.3"))
	s.show("yardr v1.2.3", "notes")
	s.run("publish", "YARDR_DEPOT_PATH="+checkout, "FAKE_CREATE_ID=yardr.dev-s9nc")
	got := s.log()
	title := "Sync yardr v1.2.3 to yardr.dev: scripts/sync-release yardr v1.2.3"
	s.mustContain(got, "yardr bead create "+title+" -d yardr.dev -p 1 -b - -l difficulty:1,size:small --json")
	s.mustContain(got, "https://github.com/yardr-dev/yardr/releases/tag/v1.2.3")
	s.mustContain(got, "built from "+at)
	s.mustContain(got, "scripts/sync-release yardr v1.2.3")
	s.mustContain(got, "yardr bead advance yardr.dev-s9nc --to new")
	s.mustContain(got, "publish: filed yardr.dev-s9nc")
	s.mustContain(got, "yardr bead done yardr-rel1 --note -")
	s.mustLack(got, "--outcome")
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
