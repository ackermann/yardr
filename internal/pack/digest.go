package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
)

// reads are the files of one pack as they were read, each fetched from the
// pack once. A pack served over HTTP answers every GET afresh (URLFS), so the
// file Load checked, the plan hashed and the digest names could be three
// answers of a server that tells them apart; read here they are one, and the
// digest is of the bytes the plan was made from. Apply does not read here: it
// reads the pack again, to hold it to the plan's hashes (check).
type reads struct {
	fsys  fs.FS
	mu    sync.Mutex
	files map[string][]byte
}

func newReads(fsys fs.FS) *reads { return &reads{fsys: fsys, files: map[string][]byte{}} }

// file is the file name of the pack, a slash path in its directory. The
// caller may change the bytes it gets.
func (r *reads) file(name string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, ok := r.files[name]
	if !ok {
		var err error
		if data, err = fs.ReadFile(r.fsys, name); err != nil {
			return nil, err
		}
		r.files[name] = data
	}
	return slices.Clone(data), nil
}

// read is a file of the pack, as the pack's first read of it had it.
func (p *Pack) read(rel string) ([]byte, error) { return p.reads.file(rel) }

// named are the files the pack is made of, as slash paths in its directory,
// sorted, each once: pack.yaml, and every file it names: the roles and
// scripts of its groups and crew members, the stage files of all its flows,
// its setup notes, its declarations and the programs of their commands. They
// are the pack's, not a plan's: a flow the depot does not follow has its
// stage files here too.
func (p *Pack) named() []string {
	out := append([]string{File, p.Setup}, p.Files()...)
	for _, f := range p.Flows {
		for _, s := range f.Stages {
			out = append(out, s.File)
		}
	}
	for _, d := range p.Declared {
		out = append(out, d.File)
		for _, c := range d.Declaration.Commands() {
			if c.Relative() {
				out = append(out, path.Join(declDir(d.File), c.Argv[0]))
			}
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(s string) bool { return s == "" })
}

// digest is the sha256 of the pack's files, in hex: for each of named, its
// path, a NUL, its length in decimal, a NUL and its bytes, so that neither a
// file renamed nor bytes moved from the end of one file to the start of the
// next hash alike. The bytes are those the plan was made from, before any
// placeholder is filled, so one pack has one digest whatever depot it goes
// into and whatever the import is answered.
func (p *Pack) digest() (string, error) {
	h := sha256.New()
	for _, rel := range p.named() {
		// A NUL in a name would move the end of the path.
		if strings.Contains(rel, "\x00") {
			return "", fmt.Errorf("%q: a file name has a NUL in it", rel)
		}
		data, err := p.read(rel)
		if err != nil {
			return "", fmt.Errorf("%s: %w", joinName(p.Dir, rel), err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ParseDigest is the digest a --sha256 flag gives, as Plan.Digest has it: 64
// hex digits in lower case. Upper case is accepted, and so is the "sha256:"
// that pack check prints before the digest.
func ParseDigest(s string) (string, error) {
	hexDigits := s
	if len(s) > len("sha256:") && strings.EqualFold(s[:len("sha256:")], "sha256:") {
		hexDigits = s[len("sha256:"):]
	}
	raw, err := hex.DecodeString(hexDigits)
	if err != nil || len(raw) != sha256.Size {
		return "", errors.New("not a digest: want the 64 hex digits of the pack's sha256, as pack check prints it")
	}
	return hex.EncodeToString(raw), nil
}

// CheckDigest refuses a plan of any pack but the one want names, the digest
// an operator read the pack by (ParseDigest takes it in any form it is
// given), and the error names both digests. A caller that holds the plan to
// it before it asks anything or writes anything installs nothing of a pack
// that is not the one that was read.
func (p *Plan) CheckDigest(want string) error { return checkDigest(p.Pack, want, p.Digest) }

// CheckDigest holds a pack that is read without a plan of it to want, as the
// plan's holds a plan: yardr init installs the yardmaster's role from the
// default pack and makes none. The digest is the same, of the same bytes
// (digest reads through reads), and InstallRole reads the role through them
// too, so a pack that passes installs the role the pin names and not a
// second answer of the server it came from.
func (p *Pack) CheckDigest(want string) error {
	have, err := p.digest()
	if err != nil {
		return err
	}
	return checkDigest(p.Name, want, have)
}

// checkDigest refuses the pack called name, whose digest is have, unless
// want, in any form ParseDigest takes, is it.
func checkDigest(name, want, have string) error {
	pin, err := ParseDigest(want)
	if err != nil {
		return fmt.Errorf("%q: %w", want, err)
	}
	if have != pin {
		return fmt.Errorf("pack %s is not the one pinned: the pin is sha256:%s, the pack read has sha256:%s; nothing was changed", name, pin, have)
	}
	return nil
}
