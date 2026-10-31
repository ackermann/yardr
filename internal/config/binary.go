package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecordBinary records path as the yardr this yard runs, in BinaryFile: the
// path and a newline. The server, the daemon and init each record the one
// they run, so the last start decides. The path is as the operating system
// reported it for the process and is not resolved any further, so a symlink to
// the installed build (Homebrew's) keeps naming whatever is installed there
// after an upgrade.
//
// A program that has no PATH to find yardr by (an app started from the macOS
// Dock) reads the file instead (docs/configuration.md, "yardr's own state").
// It is written through a temporary file renamed into place, so that program
// never reads half a line, and only when the file does not hold the path
// already: a start of the same binary touches nothing.
func (c *Config) RecordBinary(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("the yardr binary %q is not one absolute path", path)
	}
	line := path + "\n"
	if b, err := os.ReadFile(c.BinaryFile()); err == nil && string(b) == line {
		return nil
	}
	return writeFile(c.BinaryFile(), []byte(line))
}

// RecordedBinary is the path BinaryFile holds, "" when the yard has recorded
// none: no file, one that cannot be read, or one that is not a single
// absolute path. It does not look whether the path runs.
func (c *Config) RecordedBinary() string {
	b, err := os.ReadFile(c.BinaryFile())
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(string(b), "\n")
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n") {
		return ""
	}
	return path
}
