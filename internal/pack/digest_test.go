package pack_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
)

// digestYAML is a pack with every kind of file but a declaration: roles, a
// script, stage files of two flows (a depot follows one of them), and setup
// notes.
const digestYAML = `name: digested
setup: SETUP.md
flows:
  default:
    stages:
      new:  { group: builders, file: flows/default/new.md, outcomes: { done: land } }
      land: { group: assembly, outcomes: { done: done } }
      done: { end: true }
  other:
    stages:
      new:  { group: builders, file: flows/other/new.md, outcomes: { done: done } }
      done: { end: true }
groups:
  builders: { agent: roles/builder.md }
  assembly: { run: scripts/land }
crew:
  yardmaster:
    role: roles/yardmaster.md
`

func digestFiles() map[string]string {
	return map[string]string{
		"pack.yaml":            digestYAML,
		"SETUP.md":             "set up\n",
		"flows/default/new.md": "work it\n",
		"flows/other/new.md":   "work it another way\n",
		"roles/builder.md":     "build\n",
		"roles/yardmaster.md":  "yardmaster\n",
		"scripts/land":         "#!/bin/sh\n",
	}
}

// recordsDigest is the digest of a pack whose every file is named, worked
// out as docs/packs.md says: for each file in path order, its path, a NUL,
// its length, a NUL and its bytes.
func recordsDigest(files map[string]string) string {
	h := sha256.New()
	for _, name := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s\x00%d\x00%s", name, len(files[name]), files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The digest names a pack by every file it is made of: pack.yaml and each
// file it names, those of a flow the depot does not follow, its setup notes,
// its declarations and the programs of their commands among them. A byte
// more in any of them is another digest. A file the pack does not name, the
// depot it goes into and the answers it is given change nothing: one pack,
// one digest.
func TestDigestIsOfEveryFileThePackNames(t *testing.T) {
	t.Parallel()
	st, o := yard(t)
	// A pack of agents alone runs nothing of its own.
	noScript := digestFiles()
	noScript["pack.yaml"] = strings.Replace(digestYAML, "assembly: { run: scripts/land }", "assembly: { agent: roles/builder.md }", 1)
	delete(noScript, "scripts/land")
	// acme's declaration up to the keys that name commands.
	noCommands, _, _ := strings.Cut(cmdDecl, "hook:")
	depotOther := pack.Options{Depot: "web", Home: t.TempDir(), Flow: "other", Crew: map[string]string{"yardmaster": "boss"}}
	// A call a command answers (run) has its program carried like any
	// command's: it is a file that runs, and so one the digest pins.
	answered := map[string]string{
		"pack.yaml": strings.ReplaceAll(sourcePackYAML, "acme", "mail"),
		"mail.yaml": "name: mail\nbase: http://127.0.0.1:1143\ncredential: { env: MAIL_PASSWORD }\ncalls:\n  search:\n    kind: read\n    run: [mail/imap, search]\n",
		"mail/imap": "#!/bin/sh\necho imap\n",
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		o     pack.Options
		other pack.Options // the same pack, imported otherwise
		// programs are the files of it that run: what the plan names above
		// its digest, which pins them with the rest.
		programs []string
	}{
		{"a pack for a depot", digestFiles(), o, depotOther, []string{"scripts/land"}},
		{"a pack of kind source", sourcePackFiles(), pack.Options{Home: o.Home}, pack.Options{Home: t.TempDir()}, []string{"acme/normalize", "acme/thread"}},
		{"a pack of kind source whose call a command answers", answered, pack.Options{Home: o.Home}, pack.Options{Home: t.TempDir()}, []string{"mail/imap"}},
		{"a pack that runs nothing", noScript, o, depotOther, []string{}},
		{"a pack of kind source that runs nothing", map[string]string{"pack.yaml": sourcePackYAML, "acme.yaml": noCommands}, pack.Options{Home: o.Home}, pack.Options{Home: t.TempDir()}, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := func(files map[string]string, o pack.Options) *pack.Plan {
				t.Helper()
				return build(t, st, load(t, writePack(t, files)), o)
			}
			want := plan(c.files, c.o)
			if want.Digest != recordsDigest(c.files) {
				t.Errorf("digest = %s, want %s: each file's path, a NUL, its length, a NUL and its bytes, in path order", want.Digest, recordsDigest(c.files))
			}
			// Each once (acme/thread is six commands' program), and an array
			// in the plan's JSON also when there is none.
			if !slices.Equal(want.Programs, c.programs) || want.Programs == nil {
				t.Errorf("programs = %#v, want %#v", want.Programs, c.programs)
			}
			for _, name := range slices.Sorted(maps.Keys(c.files)) {
				changed := maps.Clone(c.files)
				changed[name] += "\n"
				if got := plan(changed, c.o).Digest; got == want.Digest {
					t.Errorf("a byte more in %s leaves the digest as it was", name)
				}
			}
			plus := maps.Clone(c.files)
			plus["NOTES.md"] = "the author's, and no part of the pack\n"
			if got := plan(plus, c.o).Digest; got != want.Digest {
				t.Errorf("a file the pack does not name changed the digest: %s, want %s", got, want.Digest)
			}
			if got := plan(c.files, c.other).Digest; got != want.Digest {
				t.Errorf("another depot's options changed the digest: %s, want %s", got, want.Digest)
			}

			// The pin is the digest in any of the forms pack check and a
			// shell give it. A pack read without a plan (yardr init's
			// default pack) is held to it as the plan is, in the same words.
			p := load(t, writePack(t, c.files))
			for _, pin := range []string{want.Digest, strings.ToUpper(want.Digest), "sha256:" + want.Digest} {
				if err := want.CheckDigest(pin); err != nil {
					t.Errorf("the pin %s was refused: %v", pin, err)
				}
				if err := p.CheckDigest(pin); err != nil {
					t.Errorf("the pin %s was refused for the pack read alone: %v", pin, err)
				}
			}
			wrong := strings.Repeat("0", 64)
			err := want.CheckDigest(wrong)
			if err == nil || !strings.Contains(err.Error(), "sha256:"+wrong) || !strings.Contains(err.Error(), "sha256:"+want.Digest) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Errorf("a wrong pin: %v, want an error naming both digests", err)
			}
			if alone := p.CheckDigest(wrong); err == nil || alone == nil || alone.Error() != err.Error() {
				t.Errorf("a wrong pin for the pack read alone: %v, want the plan's words: %v", alone, err)
			}
			if err := p.CheckDigest(want.Digest[1:]); err == nil || !strings.Contains(err.Error(), "not a digest") {
				t.Errorf("a pin of 63 digits for the pack read alone: %v, want an error saying it is not a digest", err)
			}
		})
	}
}

// ParseDigest takes the 64 hex digits of a sha256, in either case, with the
// sha256: pack check prints before them or without it, and nothing else.
func TestParseDigest(t *testing.T) {
	t.Parallel()
	const lower = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, in := range []string{lower, strings.ToUpper(lower), "sha256:" + lower, "SHA256:" + strings.ToUpper(lower)} {
		if got, err := pack.ParseDigest(in); err != nil || got != lower {
			t.Errorf("ParseDigest(%q) = %q, %v, want %q", in, got, err, lower)
		}
	}
	for _, in := range []string{"", "sha256:", lower[:63], lower + "0", "g" + lower[1:], " " + lower, lower + "\n", "sha1:" + lower, "sha256: " + lower, "sha256:sha256:" + lower} {
		if got, err := pack.ParseDigest(in); err == nil || !strings.Contains(err.Error(), "not a digest") {
			t.Errorf("ParseDigest(%q) = %q, %v, want an error saying it is not a digest", in, got, err)
		}
	}
}
