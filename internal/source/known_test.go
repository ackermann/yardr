package source

import (
	"bytes"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Every source yardr carries is a declaration that parses under the name it
// is carried by, whose commands name only files that stand beside it (an
// absolute program would be one of the machine it was written on), with no
// path or login of one machine in any of its files. The table is walked, so a
// source added to it is held to the same.
func TestKnownSources(t *testing.T) {
	t.Parallel()
	if _, _, ok := Known("nosuch"); ok {
		t.Error("Known answers for a name yardr carries nothing under")
	}
	if got, want := KnownNames(), []string{"aiquokka"}; !slices.Equal(got, want) {
		t.Errorf("KnownNames = %v, want %v", got, want)
	}
	for _, name := range KnownNames() {
		text, files, ok := Known(name)
		if !ok {
			t.Errorf("%s: Known finds nothing under the name KnownNames gives", name)
			continue
		}
		d, err := Parse(name+".yaml", text)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if d.Name != name {
			t.Errorf("the source carried as %s declares itself %s", name, d.Name)
		}
		for _, c := range d.Commands() {
			if !c.Relative() {
				t.Errorf("%s: %s runs %s, a program of one machine", name, c.Key, c.Argv[0])
			}
		}
		if err := d.CopyCommandFilesFS(files, ".", t.TempDir()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		for _, not := range []string{"MUSE_AUTH_PATH", "/Users/", "/home/", "/usr/", "/opt/"} {
			if bytes.Contains(text, []byte(not)) {
				t.Errorf("the declaration of %s names %q: a path or a login of one machine", name, not)
			}
		}
	}
}

// aiquokka's source says the limits of five kinds by one command, the
// program of which is a file beside the declaration: a shell script that
// runs the aiquokka found on PATH, so that the binary is the person's to
// install and nothing of it is carried here.
func TestKnownAiquokka(t *testing.T) {
	t.Parallel()
	text, files, ok := Known(Aiquokka)
	if !ok {
		t.Fatal("yardr carries no source under the name aiquokka")
	}
	d, err := Parse("aiquokka.yaml", text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Limits == nil || len(d.Calls) != 0 || d.Credential != nil {
		t.Fatalf("declaration = %+v, want the limits command alone", d)
	}
	wantKinds := map[string]string{"claude": "claude", "codex": "codex", "grok": "grok", "kimi": "kimi", "muse": "muse"}
	if !reflect.DeepEqual(d.Limits.Kinds, wantKinds) {
		t.Errorf("kinds = %v, want %v", d.Limits.Kinds, wantKinds)
	}
	if cs := d.Commands(); len(cs) != 1 || cs[0].Key != "limits.command" ||
		strings.Join(cs[0].Argv, " ") != "aiquokka {provider} --json" {
		t.Errorf("commands = %+v, want limits.command aiquokka {provider} --json", cs)
	}
	shim, err := fs.ReadFile(files, "aiquokka")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(shim, []byte("#!/bin/sh\n")) || !bytes.HasSuffix(shim, []byte("\nexec aiquokka \"$@\"\n")) {
		t.Errorf("the command file is no shell script that ends by running the aiquokka on PATH:\n%s", shim)
	}
}
