package scripttest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	Remove()
	os.Exit(code)
}

// A stand-in is to its caller the script a file at its path would be: it
// has the arguments, the standard input, the $0 and the exit status of one,
// and a second Put at the path gives it a new body.
func TestStandInRunsAsAScriptAtItsPath(t *testing.T) {
	dir := t.TempDir()
	herdr := filepath.Join(dir, "herdr")
	Write(t, herdr, "#!/bin/sh\necho \"$0: $# args, second $2\" >\"$0.calls\"\ncat\necho gone >&2\nexit 3\n")
	run := func(path string, args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(path, args...)
		cmd.Stdin = strings.NewReader("from stdin\n")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatal(err)
		}
		return string(out), stderr.String(), cmd.ProcessState.ExitCode()
	}
	out, stderr, code := run(herdr, "one", "two words")
	if out != "from stdin\n" || stderr != "gone\n" || code != 3 {
		t.Fatalf("stdout %q, stderr %q, exit %d", out, stderr, code)
	}
	if calls, _ := os.ReadFile(herdr + ".calls"); string(calls) != herdr+": 2 args, second two words\n" {
		t.Fatalf("what the script saw: %q", calls)
	}
	// By its name on PATH, as a test's fake git is found.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, _, _ := run("herdr"); out != "from stdin\n" {
		t.Fatalf("found on PATH: %q", out)
	}
	Write(t, herdr, "echo again\n")
	if out, _, code := run(herdr); out != "again\n" || code != 0 {
		t.Fatalf("with a new body: %q, exit %d", out, code)
	}
	if to, err := os.Readlink(herdr); err != nil || filepath.Dir(to) == dir {
		t.Fatalf("the stand-in is no link to the launcher: %q %v", to, err)
	}
}

// Relink makes stand-ins of the executable scripts under a directory, each
// under its own shell, over a file that was there, and leaves alone what is
// no script of those shells and what cannot be run.
func TestRelink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]struct {
		body string
		mode os.FileMode
	}{
		"plain":      {"#!/bin/sh\necho \"plain $1\"\n", 0o700},
		"sub/bashed": {"#!/bin/bash\na=(in bash)\necho \"${a[1]} $1\"\n", 0o700},
		"other":      {"#!/usr/bin/env sh\necho \"other $1\"\n", 0o700},
		"data":       {"#!/bin/sh\necho data\n", 0o600},
	}
	for name, f := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
	}
	Relink(t, dir)
	for name, want := range map[string]string{"plain": "plain x\n", "sub/bashed": "bash x\n", "other": "other x\n"} {
		out, err := exec.Command(filepath.Join(dir, name), "x").CombinedOutput()
		if err != nil || string(out) != want {
			t.Errorf("%s: %q %v, want %q", name, out, err, want)
		}
	}
	for name, linked := range map[string]bool{"plain": true, "sub/bashed": true, "other": false, "data": false} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || (info.Mode()&os.ModeSymlink != 0) != linked {
			t.Errorf("%s: mode %v %v, want a link: %v", name, info.Mode(), err, linked)
		}
	}
	// A second time changes nothing: the links are no files of their own.
	Relink(t, dir)
	if out, err := exec.Command(filepath.Join(dir, "plain"), "y").CombinedOutput(); err != nil || string(out) != "plain y\n" {
		t.Errorf("after a second Relink: %q %v", out, err)
	}
}
