// How a bead ask carries a --run command's bytes beside argv: whether the
// command is a depot test ask, whose command --run-test carries, and the
// declaration file a source add ask carries.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// depotTestCommand reports whether command is a --run "depot test <depot>"
// ask: exactly those three words, with or without the leading yardr. The
// store parses it; this only says the ask carries --run-test beside it,
// so no character gate goes here: a command the store refuses is refused
// there, carried or not.
func depotTestCommand(command string) bool {
	words := strings.Fields(command)
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	return len(words) == 3 && words[0] == "depot" && words[1] == "test"
}

// sourceAddCarried reads what a --run "source add <file>" ask carries:
// the declaration file once, resolved as yardr source add resolves it,
// and each relative program's bytes from beside it as CopyCommandFiles
// reads them, each with its SHA-256. It answers nil for any other
// command. A file that cannot be read or does not parse is refused
// here, in the parser's words, and nothing is stored.
func sourceAddCarried(command string) ([]store.AskSource, error) {
	// Match parseRow's character gate before touching a path: a command
	// the store will reject must not make the CLI read a file first.
	if strings.ContainsRune(command, ',') {
		return nil, nil
	}
	for _, r := range command {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(" \t._/=-@:", r) {
			continue
		}
		return nil, nil
	}
	words := strings.Fields(command)
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	if len(words) != 3 || words[0] != "source" || words[1] != "add" {
		return nil, nil
	}
	file := words[2]
	text, err := readSourceAskFile(file, store.MaxRunSourceDeclaration, "the declaration")
	if err != nil {
		return nil, err
	}
	d, err := source.Parse(file, text)
	if err != nil {
		return nil, fmt.Errorf("%s", store.OneLine(err.Error()))
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	declDir := filepath.Dir(abs)
	total := len(text)
	var programs []string
	seen := map[string]bool{}
	for _, c := range d.Commands() {
		if c.Relative() && !seen[c.Argv[0]] {
			seen[c.Argv[0]] = true
			programs = append(programs, c.Argv[0])
		}
	}
	var files []store.AskFile
	for _, prog := range programs {
		path := filepath.Join(declDir, prog)
		data, err := readSourceAskFile(path, store.MaxRunSourceCommandFile, "source "+d.Name+" command file "+store.OneLine(prog))
		if err != nil {
			return nil, fmt.Errorf("source %s: a command's program %s is a file beside the declaration's file: %v", d.Name, store.OneLine(prog), err)
		}
		total += len(data)
		if total > store.MaxRunSourceTexts {
			return nil, fmt.Errorf("source %s: its declaration with command files exceeds %d bytes together: a card carries at most that, so add it with yardr source add %s in a terminal", d.Name, store.MaxRunSourceTexts, file)
		}
		sum := sha256.Sum256(data)
		files = append(files, store.AskFile{Program: prog, Text: string(data), SHA256: hex.EncodeToString(sum[:])})
	}
	return []store.AskSource{{Name: d.Name, Text: string(text), Files: files}}, nil
}

func readSourceAskFile(file string, limit int, what string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s is not a regular file", store.OneLine(file), what)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %s", store.OneLine(file), store.OneLine(err.Error()))
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes: a run ask carries at most that, so add it with yardr source add %s in a terminal", what, limit, store.OneLine(file))
	}
	return data, nil
}
