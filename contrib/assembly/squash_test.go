package assembly

// With YARDR_LAND=squash in the group's cmd, merge-local lands a bead as one
// Conventional Commit written from the bead (docs/line.md, section 4). The
// message is one block of shell, squash_message, which pr-merge carries a
// copy of. TestSquashMessage runs that block itself for the cases of its
// format, over a throwaway yard and repository; the landing tests run the
// script for what only a landing shows.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// commitMsg commits files (path to content) in dir under the message msg
// and returns the commit. The yard's commit hook is not here (TestMain), so
// the message carries the trailers it would have written for an agent.
func (y *yard) commitMsg(dir, msg string, files map[string]string) string {
	y.t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			y.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			y.t.Fatal(err)
		}
	}
	y.git(dir, "add", "-A")
	y.run(dir, "git", msg, "commit", "-q", "-F", "-")
	return y.git(dir, "rev-parse", "HEAD")
}

// beadWith makes bead id in approved, where a landing script claims it, of
// type typ with these labels and links.
func (y *yard) beadWith(id, title, typ string, labels []string, links ...store.NewLink) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if _, err := st.CreateBead(context.Background(), store.NewBead{Title: title, ID: id, Depot: "r", Type: typ,
		Stage: "approved", Priority: store.DefaultPriority, Labels: labels, Links: links}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// setHold puts bead id on hold or takes it off.
func (y *yard) setHold(id string, hold bool) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if _, err := st.SetHold(context.Background(), id, hold, store.Expect{}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// retype gives bead id another type.
func (y *yard) retype(id, typ string) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if _, err := st.UpdateBead(context.Background(), id, store.BeadPatch{Type: &typ}, store.Expect{}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

const (
	squashOpen  = "# --- squash_message: "
	squashClose = "# --- end of squash_message ---\n"
)

// squashBlock is the text of script from the line that opens squash_message
// to the one that closes it: the block merge-local and pr-merge both carry.
func squashBlock(t *testing.T, script string) string {
	t.Helper()
	return markedBlock(t, script, squashOpen, squashClose)
}

const (
	bindingOpen  = "# --- github_binding: "
	bindingClose = "# --- end of github_binding ---\n"
)

// markedBlock is the text of script from the line that starts with open to the
// one that is close: a block that two scripts carry, as they are installed
// one file each and have none to source.
func markedBlock(t *testing.T, script, open, close string) string {
	t.Helper()
	_, rest, ok := strings.Cut(readFile(t, script), open)
	if !ok {
		t.Fatalf("%s has no block (no line starting %q)", script, open)
	}
	block, _, ok := strings.Cut(rest, close)
	if !ok {
		t.Fatalf("%s: the block starting %q does not end in %q", script, open, strings.TrimSpace(close))
	}
	return open + block + close
}

// pr-merge writes the squash commit's message with merge-local's block,
// copied: the scripts are installed one file each, so there is none to
// source. A change to one is made in both, as to the copies of merge-local
// (TestMergeLocalCopiesIdentical) and the credit loop.
func TestPRMergeSquashMessageIsMergeLocals(t *testing.T) {
	want, got := squashBlock(t, "merge-local"), squashBlock(t, "pr-merge")
	if got != want {
		t.Errorf("pr-merge's squash_message differs from merge-local's: change both\nmerge-local:\n%s\npr-merge:\n%s", want, got)
	}
}

// squashMessage runs merge-local's squash_message for bead id over what head
// adds to base in repo, with the yardr at bin, and returns the subject and
// the body it leaves.
func (y *yard) squashMessage(bin, repo, id, base, head string) (subject, body string) {
	y.t.Helper()
	script := readFile(y.t, "merge-local")
	containers := regexp.MustCompile(`(?m)^CONTAINERS="([^"]*)"$`).FindStringSubmatch(script)
	if containers == nil {
		y.t.Fatal("merge-local has no CONTAINERS")
	}
	file := filepath.Join(y.t.TempDir(), "squash_message")
	if err := os.WriteFile(file, []byte(squashBlock(y.t, "merge-local")), 0o644); err != nil {
		y.t.Fatal(err)
	}
	out := y.run(y.home, "sh", "", "-c",
		`Y=$1 REPO=$2 CONTAINERS=$3; . "$4"; squash_message "$5" "$6" "$7" || { echo "squash_message: $ERR" >&2; exit 1; }
printf '%s\n\n%s\n' "$SQ_SUBJECT" "$SQ_BODY"`,
		"sh", bin, repo, containers[1], file, id, base, head)
	subject, body, _ = strings.Cut(out, "\n\n")
	return subject, body
}

// A yardr that lists the links of a bead as the test says, and is the real
// one for everything else: a bead of another yard that this one is at is
// listed among the links (kind at), which no real yard here can make.
func fakeLinkLister(t *testing.T, list string) string {
	t.Helper()
	return writeScript(t, t.TempDir(), "yardr", "#!/bin/sh\n"+
		`if [ "$1 $2" = "link list" ]; then cat <<'EOF'`+"\n"+list+"\nEOF\nexit 0; fi\n"+
		"exec "+shellQuote(yardrBin)+` "$@"`+"\n")
}

const peerAndIssueLinks = `[
  {
    "bead": "r-9",
    "kind": "relates",
    "source": "linear",
    "url": "https://linear.app/acme/issue/ACM-9",
    "created_at": "2026-10-22T04:19:56Z"
  },
  {
    "bead": "r-9",
    "kind": "at",
    "source": "peer",
    "url": "r-4@elsewhere",
    "peer": "elsewhere",
    "stage": "review",
    "created_at": "2026-10-22T04:19:57Z"
  },
  {
    "bead": "r-9",
    "kind": "tracks",
    "source": "github",
    "url": "https://github.com/acme/widgets/issues/7?a=1\u0026b=2",
    "created_at": "2026-10-22T04:19:58Z"
  }
]`

const (
	builderTrailer  = "Yardr-Agent: yardr-builders (claude opus)"
	reviewerTrailer = "Yardr-Agent: yardr-reviewers (codex gpt-5)"
)

// The format of the squash commit's message, case by case: the type from the
// kind: label, else from the bead's type; the scope from the paths the
// squash changes; the commits' messages without their trailers; and the
// footer. Each case is a bead and a branch of commits.
func TestSquashMessage(t *testing.T) {
	t.Parallel() // as its subtests are: see TestMain
	type commit struct {
		msg   string
		files map[string]string
		mv    [2]string // a rename done first
		merge bool      // main moves and is merged in first
	}
	for _, c := range []struct {
		id, title, typ string
		labels         []string
		links          string // the links the bead lists, when not its own
		commits        []commit
		subject, body  string
	}{
		{
			// No kind: label, a bug: fix. One package: its name.
			id: "r-2", title: "Stop the store losing writes", typ: "bug",
			commits: []commit{{msg: "Fix the lost write\n\nYardr-Bead: r-2\n" + builderTrailer + "\n", files: map[string]string{"internal/store/w.go": "w\n"}}},
			subject: "fix(store): Stop the store losing writes (r-2)",
			body:    "Fix the lost write\n\nYardr-Bead: r-2\n" + builderTrailer,
		}, {
			// No kind: label, not a bug: chore. Two packages under internal
			// share only internal, which names nothing. The title is used as
			// it is, whatever yardr's JSON made of its < > & and quotes.
			id: "r-3", title: `Share <rows> between "packages" & more`, typ: "task",
			commits: []commit{{msg: "Share the rows\n", files: map[string]string{"internal/store/s.go": "s\n", "internal/rpc/s.go": "s\n"}}},
			subject: `chore: Share <rows> between "packages" & more (r-3)`,
			body:    "Share the rows\n\nYardr-Bead: r-3",
		}, {
			// Everything under docs/ is docs: the scope would say it twice.
			id: "r-4", title: "Say what a squash commit is", typ: "task", labels: []string{"difficulty:1", "kind:docs"},
			commits: []commit{{msg: "Docs\n", files: map[string]string{"docs/a.md": "a\n", "docs/more/b.md": "b\n"}}},
			subject: "docs: Say what a squash commit is (r-4)",
			body:    "Docs\n\nYardr-Bead: r-4",
		}, {
			// A rename counts both paths: the new one alone would say sub.
			id: "r-6", title: "Move the file down", typ: "task", labels: []string{"kind:perf"},
			commits: []commit{{msg: "Move\n", mv: [2]string{"internal/store/old.go", "internal/store/sub/old.go"}}},
			subject: "perf(store): Move the file down (r-6)",
			body:    "Move\n\nYardr-Bead: r-6",
		}, {
			// A BREAKING CHANGE leaves the commit it was written in, wherever
			// it stood (above the yard's trailers, in a block with them), keeps
			// its words and its folded lines, and marks the subject.
			id: "r-7", title: "Add the flag", typ: "task", labels: []string{"kind:feat"},
			commits: []commit{
				{msg: "Add the flag\n\nBREAKING CHANGE: --old is gone;\nuse --new.\n\nYardr-Bead: r-7\n" + builderTrailer + "\n", files: map[string]string{"internal/store/f.go": "f\n"}},
				{msg: "Tidy\n\nBREAKING-CHANGE: the old key means something else now\nYardr-Bead: r-7\n" + reviewerTrailer + "\n", files: map[string]string{"internal/store/g.go": "g\n"}},
			},
			subject: "feat(store)!: Add the flag (r-7)",
			body: "Add the flag\n\nTidy\n\nBREAKING CHANGE: --old is gone;\nuse --new.\nBREAKING-CHANGE: the old key means something else now\n" +
				"Yardr-Bead: r-7\n" + builderTrailer + "\n" + reviewerTrailer,
		}, {
			// A bead that came back red is squashed again: the first squash
			// gives up its subject line and its footer, the merge of main
			// contributes its message, and the agents stay in first-seen order.
			id: "r-8", title: "Keep the title", typ: "task", labels: []string{"kind:feat"},
			commits: []commit{
				{msg: "feat(store): Keep the title (r-8)\n\nAdd the thing\n\nRefs: https://github.com/acme/widgets/issues/7\nYardr-Bead: r-8\n" + builderTrailer + "\n", files: map[string]string{"internal/store/h.go": "h\n"}},
				{merge: true},
				{msg: "Fix it after the red run\n\nYardr-Bead: r-8\n" + reviewerTrailer + "\n", files: map[string]string{"internal/store/i.go": "i\n"}},
			},
			subject: "feat(store): Keep the title (r-8)",
			body:    "Add the thing\n\nMerge main\n\nFix it after the red run\n\nYardr-Bead: r-8\n" + builderTrailer + "\n" + reviewerTrailer,
		}, {
			// The links of a bead: the tracked issue first, then the related
			// one, and a bead of another yard it is at is no issue. The one
			// file is at the root, where there is no directory to name.
			id: "r-9", title: "Links", typ: "task", links: peerAndIssueLinks,
			commits: []commit{{msg: "x\n", files: map[string]string{"x.txt": "x\n"}}},
			subject: "chore: Links (r-9)",
			body:    "x\n\nRefs: https://github.com/acme/widgets/issues/7?a=1&b=2\nRefs: https://linear.app/acme/issue/ACM-9\nYardr-Bead: r-9",
		},
	} {
		t.Run(c.id, func(t *testing.T) {
			y := newYard(t)
			y.commitMsg(y.repo, "base\n", map[string]string{"internal/store/old.go": "old\n"})
			y.beadWith(c.id, c.title, c.typ, c.labels)
			wt := filepath.Join(t.TempDir(), c.id)
			y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+c.id, wt, "main")
			for _, k := range c.commits {
				if k.merge {
					y.commit(y.repo, "m-"+c.id+".txt", "main moved\n")
					y.git(wt, "merge", "-q", "--no-ff", "-m", "Merge main", "main")
					continue
				}
				if k.mv != ([2]string{}) {
					if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(k.mv[1])), 0o755); err != nil {
						t.Fatal(err)
					}
					y.git(wt, "mv", k.mv[0], k.mv[1])
				}
				y.commitMsg(wt, k.msg, k.files)
			}
			bin := yardrBin
			if c.links != "" {
				bin = fakeLinkLister(t, c.links)
			}

			subject, body := y.squashMessage(bin, y.repo, c.id, "main", "yard/"+c.id)

			if subject != c.subject || body != c.body {
				t.Errorf("%s:\nsubject %q\nwant    %q\nbody:\n%s\nwant:\n%s", c.id, subject, c.subject, body, c.body)
			}
		})
	}
}

// YARDR_LAND chooses how a bead lands. A value that is neither mode holds the
// bead with nothing moved. Rebase, named, lands the branch's commits as they
// are. Squash lands one commit written from the bead on top of whatever the
// base gained meanwhile, and it is the commit the tests ran on; one that
// cannot be made holds the bead.
func TestMergeLocalLandMode(t *testing.T) {
	y := newYard(t)
	orig := y.git(y.repo, "rev-parse", "main")
	one := y.commitMsg(y.wt, "one\n", map[string]string{"b.txt": "b\n"})
	two := y.commitMsg(y.wt, "two\n", map[string]string{"c.txt": "c\n"})

	y.opts = "YARDR_LAND=squahs"
	y.assembly()

	if b := y.bead(); !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v after a typo in YARDR_LAND, want held in approved", b.Stage, b.Hold)
	}
	if notes := y.notes(); !strings.Contains(notes, "YARDR_LAND is squahs") {
		t.Fatalf("no note naming the value:\n%s", notes)
	}
	if y.git(y.repo, "rev-parse", "main") != orig || y.git(y.wt, "rev-parse", "HEAD") != two {
		t.Fatal("main or the branch moved under a mode that is none")
	}

	// A squash that git cannot commit (no name to commit as) holds it too,
	// with the branch where it was, not landed by rebase instead.
	y.setHold("r-1", false)
	y.opts = "YARDR_LAND=squash GIT_COMMITTER_NAME= GIT_AUTHOR_NAME="
	y.assembly()

	if b := y.bead(); !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v after a squash git could not commit, want held in approved", b.Stage, b.Hold)
	}
	if notes := y.notes(); !strings.Contains(notes, "cannot write the squash commit of r-1: ") {
		t.Fatalf("no note on the squash that failed:\n%s", notes)
	}
	if y.git(y.repo, "rev-parse", "main") != orig || y.git(y.wt, "rev-parse", "HEAD") != two {
		t.Fatal("main or the branch moved under a squash that failed")
	}

	y.setHold("r-1", false)
	y.opts = "YARDR_LAND=rebase"
	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s after landing by rebase, want merged and closed", b.Stage, b.Status)
	}
	if got := y.git(y.repo, "log", "--format=%H %s", orig+"..main"); got != two+" two\n"+one+" one" {
		t.Fatalf("main gained\n%s\nwant the branch's two commits as they were", got)
	}

	// r-2 was begun from where main was, so it is rebased onto r-1 first.
	y.beadWith("r-2", "Withhold setup candidates from non-operator devices", "task", []string{"kind:feat"},
		store.NewLink{Kind: store.LinkRelates, Source: "linear", URL: "https://linear.app/acme/issue/ACM-9"},
		store.NewLink{Kind: store.LinkTracks, Source: "github", URL: "https://github.com/acme/widgets/issues/7"})
	wt := filepath.Join(y.home, "worktrees", "r", "r-2")
	y.git(y.repo, "worktree", "add", "-q", "-b", "yard/r-2", wt, orig)
	y.commitMsg(wt, "Add the withholding\n\nThe setup reply carried every candidate.\n\nYardr-Bead: r-2\n"+builderTrailer+"\n",
		map[string]string{"internal/store/a.go": "a\n"})
	y.commitMsg(wt, "review: keep the operator's candidates\n\nBREAKING CHANGE: a device that is not the operator's no longer receives candidates\n\nYardr-Bead: r-2\n"+reviewerTrailer+"\n",
		map[string]string{"internal/store/b.go": "b\n"})
	y.commitMsg(wt, "Document the withholding\n\nYardr-Bead: r-2\n"+builderTrailer+"\n",
		map[string]string{"internal/store/c.go": "c\n"})
	before := y.git(y.repo, "rev-parse", "main")
	y.opts = "YARDR_LAND=squash"

	y.exec("merge-local", "assembly", "r-2")

	if b := y.beadOf("r-2"); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead r-2 %s/%s after landing by squash, want merged and closed:\n%s", b.Stage, b.Status, y.notesOf("r-2"))
	}
	if y.git(y.repo, "rev-list", "--count", before+"..main") != "1" || y.git(y.repo, "rev-parse", "main^") != before {
		t.Fatalf("main gained\n%s\nwant one commit on top of %s", y.git(y.repo, "log", "--format=%H %s", before+"..main"), before)
	}
	want := `feat(store)!: Withhold setup candidates from non-operator devices (r-2)

Add the withholding

The setup reply carried every candidate.

review: keep the operator's candidates

Document the withholding

BREAKING CHANGE: a device that is not the operator's no longer receives candidates
Refs: https://github.com/acme/widgets/issues/7
Refs: https://linear.app/acme/issue/ACM-9
Yardr-Bead: r-2
` + builderTrailer + "\n" + reviewerTrailer
	got := y.git(y.repo, "log", "-1", "--format=%B", "main")
	t.Logf("the squash commit's message:\n%s", got)
	if got != want {
		t.Fatalf("the squash commit's message is\n%s\nwant\n%s", got, want)
	}
	for _, f := range []string{"b.txt", "c.txt", "internal/store/a.go", "internal/store/b.go", "internal/store/c.go"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("the checkout of main lacks %s: %v", f, err)
		}
	}
	// The commit the tests ran on is the commit that landed, and the branch
	// is that commit, with a worktree and an index as clean as before.
	short := y.git(y.repo, "rev-parse", "--short", "main")
	tested := regexp.MustCompile(`(?m)^== the tests on yard/r-2 at (\w+): `).FindStringSubmatch(y.log)
	if tested == nil || tested[1] != short {
		t.Fatalf("the log says the tests ran on %v, want the landed %s:\n%s", tested, short, y.log)
	}
	if !strings.Contains(y.notesOf("r-2"), "merged "+short+" (+3 code, +0 test), tested with") {
		t.Fatalf("no landing note for %s:\n%s", short, y.notesOf("r-2"))
	}
	if y.git(wt, "rev-parse", "HEAD") != y.git(y.repo, "rev-parse", "main") || y.git(wt, "status", "--porcelain") != "" || y.git(y.repo, "status", "--porcelain") != "" {
		t.Fatal("the branch is not the landed commit, or a tree is not clean")
	}
}
