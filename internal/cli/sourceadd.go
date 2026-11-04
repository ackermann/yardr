package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"yardr.dev/yardr/internal/source"
)

// addFile is what yardr source add reads of its argument: the text of the
// declaration, and where the files its commands name stand.
type addFile struct {
	text []byte
	// path is the file given. Empty for a source yardr carries.
	path string
	// known is the directory of a source yardr carries, which no directory of
	// the machine holds; nil for a file.
	known fs.FS
	// shadows is set when the file given has the name of a source yardr
	// carries, so that the add can say which of the two it added.
	shadows bool
}

// readAddFile reads the argument of yardr source add: a file, and only where
// no such file stands, the name of a source yardr carries (aiquokka), so a
// file by that name beside the command is the one added, as ever, and every
// other argument fails as it did.
func readAddFile(arg string) (addFile, error) {
	if text, files, ok := knownFor(arg); ok {
		return addFile{text: text, known: files}, nil
	}
	text, err := os.ReadFile(arg)
	_, _, carried := source.Known(arg)
	return addFile{text: text, path: arg, shadows: carried}, err
}

// notes are what the add says beside the source it saved: that the file
// given stands where the name of a source yardr carries was meant, and was
// added instead, so a file that happens to be there is not taken for it.
func (f addFile) notes() []string {
	if !f.shadows {
		return nil
	}
	return []string{fmt.Sprintf("the file %s stands here, so it was added, not the source yardr carries under that name", f.path)}
}

// knownFor is the source yardr carries under arg, when no file stands
// there: the one rule for the argument of yardr source add and of the ask
// that proposes it.
func knownFor(arg string) (text []byte, files fs.FS, ok bool) {
	if _, err := os.Stat(arg); !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false
	}
	return source.Known(arg)
}

// check refuses d when a program of its commands is no file beside the
// declaration. A source yardr carries has them all, as its tests hold.
func (f addFile) check(d *source.Declaration) error {
	if f.known != nil || !d.HasCommandFiles() {
		return nil
	}
	abs, err := filepath.Abs(f.path)
	if err != nil {
		return err
	}
	return d.CheckCommandFiles(filepath.Dir(abs))
}

// copy puts the files d's commands name into the yard's home, the directory
// made anew so that none of the declaration d replaces is kept
// (source.CopyCommandFiles).
func (f addFile) copy(d *source.Declaration, home string) error {
	if f.known != nil {
		return d.CopyCommandFilesFS(f.known, ".", home)
	}
	abs, err := filepath.Abs(f.path)
	if err != nil {
		return err
	}
	return d.CopyCommandFiles(filepath.Dir(abs), home)
}
