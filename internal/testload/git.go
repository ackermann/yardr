package testload

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// systemGit is where macOS keeps its git: not git itself but a shim, which finds
// the developer directory and starts the git that names. A call through it is two
// execs where git alone needs one, and a suite that shells to git makes thousands.
const systemGit = "/usr/bin/git"

// PreferRealGit makes `git` the real git for this process and every program it
// starts, where PATH finds macOS's shim: it puts the directory of git's own
// programs first on PATH, so that the tests and the scripts they start pay for
// one exec of git, not two. A suite's TestMain calls it once, before anything
// runs git, and after it has stripped the git hook's variables from the
// environment (a GIT_EXEC_PATH would answer for git). Anything else leaves PATH
// as it is (realGitDir says when), and a test that puts a stand-in git first on
// PATH after this still has it first, as before.
func PreferRealGit() {
	git, err := exec.LookPath("git")
	if err != nil {
		return
	}
	if dir := realGitDir(runtime.GOOS, git, readExecPath); dir != "" {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
}

// realGitDir is the directory to put first on PATH for the real git, or "" where
// PATH is better left as it is: on any system but macOS (the git PATH finds there
// is the real one), where the git PATH found is not the shim (a person's own git
// first, or a test's stand-in), where the shim does not say where git's programs
// are, or where that directory holds no executable git.
//
// The directory is git's exec path. On macOS it holds a link to the git the shim
// runs, beside links to git's other programs, and nothing else a test would run.
// It is not the directory of the git binary itself, the developer directory's
// usr/bin: that holds a hundred tools more (python3, make and ld among them), and
// putting it first would change which of those the tests run. git is the one PATH
// found, and execPath asks it where its programs are; both are inputs so that a
// test of the choice needs neither a Mac nor a git.
func realGitDir(goos, git string, execPath func(git string) (string, error)) string {
	if goos != "darwin" || git != systemGit {
		return ""
	}
	dir, err := execPath(git)
	if err != nil {
		return ""
	}
	if _, err := exec.LookPath(filepath.Join(dir, "git")); err != nil {
		return ""
	}
	return dir
}

// readExecPath asks git where its programs are, as `git --exec-path` does.
func readExecPath(git string) (string, error) {
	out, err := exec.Command(git, "--exec-path").Output()
	return strings.TrimSpace(string(out)), err
}
