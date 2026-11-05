package store

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Fetch is how a program gets onto a Mac that has no package manager and
// nobody at a terminal: one file its maker publishes, at one address, of one
// version, with the checksum it had when it was verified. yardr fetches
// nothing itself but herdr, on a yes in its first-run wizard; this is data for
// a program that does (yardr doctor --json, yardr harness show --json;
// docs/doctor.md, "A program fetched on a Mac" says what such a program does
// with it, step by step).
//
// The version is pinned, and the checksum is yardr's, not read from the
// maker's site when the file is fetched: a checksum served beside a download
// shows that it is whole, not who made it, and what is fetched is then
// exactly what was verified. Every program here keeps itself current
// afterwards. So a Fetch changes only with a bead that verifies the new one
// again and says where from (Verified).
type Fetch struct {
	// Verified is the maker's own page this way was read from, and when.
	Verified Verified `yaml:"verified" json:"verified"`
	// Version is the program's version the targets are of.
	Version string `yaml:"version" json:"version"`
	// Path is where the program is afterwards, under the user's home
	// ("~/..."): where its maker's own installer puts it.
	Path string `yaml:"path" json:"path"`
	// Run, when set, are the arguments the fetched file is run with once it
	// is verified, which puts the program at Path: its maker's own install
	// command. With none the file itself is moved to Path.
	Run []string `yaml:"run" json:"run,omitempty"`
	// Targets is the file per system, by "<os>/<arch>" as Go names them.
	Targets map[string]FetchFile `yaml:"targets" json:"targets"`
}

// Verified is where a Fetch was verified from and on which day.
type Verified struct {
	From string `yaml:"from" json:"from"`
	On   string `yaml:"on" json:"on"` // 2006-01-02
}

// FetchFile is the one file of a Fetch for a system.
type FetchFile struct {
	URL    string `yaml:"url" json:"url"`
	SHA256 string `yaml:"sha256" json:"sha256"`
	Size   int64  `yaml:"size" json:"size"`
	// Member, when set, says the file is a gzipped tar and names the one
	// member of it that is the program. With none the file is the program.
	Member string `yaml:"member" json:"member,omitempty"`
}

// Download is a Fetch for one system and one home: what a program that
// fetches goes by.
type Download struct {
	Version string `json:"version"`
	FetchFile
	// Path is absolute here.
	Path     string   `json:"path"`
	Run      []string `json:"run,omitempty"`
	Verified Verified `json:"verified"`
}

// fetchTarget is a system a Fetch may name: a Mac, or a Linux box. A Linux
// target is for yardr's own first-run wizard, which installs herdr from it;
// doctor's report still carries a Mac's alone, since on Linux a missing
// program stays a hint to install it by hand for a program that fetches
// (design yardr-3az6, open question 3).
var fetchTarget = regexp.MustCompile(`^(darwin|linux)/(arm64|amd64)$`)

var fetchSHA256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// For is f for the system goos/goarch and the user's home; nil when f is
// nil or names no file for that system.
func (f *Fetch) For(goos, goarch, home string) *Download {
	if f == nil {
		return nil
	}
	file, ok := f.Targets[goos+"/"+goarch]
	if !ok {
		return nil
	}
	return &Download{Version: f.Version, FetchFile: file, Run: f.Run, Verified: f.Verified,
		Path: path.Join(home, strings.TrimPrefix(f.Path, "~/"))}
}

// Check finds what f says that a program fetching by it could not go by, or
// should not: program is the name the fetched program must have at Path.
func (f *Fetch) Check(program string) error {
	if u, err := url.Parse(f.Verified.From); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("verified.from %q is no https address: it is the maker's page this was read from", f.Verified.From)
	}
	if _, err := time.Parse(time.DateOnly, f.Verified.On); err != nil {
		return fmt.Errorf("verified.on %q is no day (2006-01-02)", f.Verified.On)
	}
	switch {
	case f.Version == "":
		return fmt.Errorf("no version")
	case !strings.HasPrefix(f.Path, "~/") || path.Clean(f.Path) != f.Path || strings.Contains(f.Path, ".."):
		return fmt.Errorf("path %q is not a clean path under the home (~/...)", f.Path)
	case path.Base(f.Path) != program:
		return fmt.Errorf("path %q does not end in %s, the name the program is started by", f.Path, program)
	case len(f.Targets) == 0:
		return fmt.Errorf("no targets")
	}
	for _, a := range f.Run {
		if a == "" {
			return fmt.Errorf("run: an empty argument")
		}
	}
	for target, file := range f.Targets {
		u, err := url.Parse(file.URL)
		switch {
		case !fetchTarget.MatchString(target):
			return fmt.Errorf("targets: %q is none of darwin/arm64, darwin/amd64, linux/arm64, linux/amd64: only a Mac or a Linux box fetches", target)
		case err != nil || u.Scheme != "https" || u.Host == "":
			return fmt.Errorf("targets.%s: url %q is no https address", target, file.URL)
		case !strings.Contains(file.URL, f.Version):
			return fmt.Errorf("targets.%s: url %q does not name version %s: a file that changes at its address has no one checksum", target, file.URL, f.Version)
		case !fetchSHA256Hex.MatchString(file.SHA256):
			return fmt.Errorf("targets.%s: sha256 %q is not 64 lower-case hex digits", target, file.SHA256)
		case file.Size <= 0:
			return fmt.Errorf("targets.%s: no size", target)
		case file.Member != "" && (path.Base(file.Member) != file.Member || strings.HasPrefix(file.Member, ".")):
			return fmt.Errorf("targets.%s: member %q is not a plain file name", target, file.Member)
		}
	}
	return nil
}
