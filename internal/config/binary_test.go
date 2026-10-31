package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The record is the path and a newline, written whole: another path replaces
// it, the same one leaves the file as it is (same file, same time), and what
// is not one absolute path is refused before anything is written.
func TestRecordBinary(t *testing.T) {
	c := &Config{Home: t.TempDir()}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(c.BinaryFile())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := c.RecordedBinary(); got != "" {
		t.Fatalf("RecordedBinary with no file = %q", got)
	}

	if err := c.RecordBinary("/opt/yardr/bin/yardr"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "/opt/yardr/bin/yardr\n" {
		t.Fatalf("the file holds %q", got)
	}
	if got := c.RecordedBinary(); got != "/opt/yardr/bin/yardr" {
		t.Fatalf("RecordedBinary = %q", got)
	}
	if fi, err := os.Stat(c.BinaryFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file is %v, %v; want mode 0600", fi, err)
	}

	// The same path again writes nothing: the file keeps its time, which a
	// rename of a new file over it would not.
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(c.BinaryFile(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordBinary("/opt/yardr/bin/yardr"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(c.BinaryFile()); err != nil || !fi.ModTime().Equal(old) {
		t.Fatalf("the same path was written again: %v, %v", fi, err)
	}

	// Another path replaces it.
	if err := c.RecordBinary("/usr/local/bin/yardr"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "/usr/local/bin/yardr\n" {
		t.Fatalf("the file holds %q", got)
	}
	// So does a file that names the same path in another shape.
	if err := os.WriteFile(c.BinaryFile(), []byte("/usr/local/bin/yardr"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordBinary("/usr/local/bin/yardr"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "/usr/local/bin/yardr\n" {
		t.Fatalf("the file holds %q", got)
	}

	for _, bad := range []string{"", "yardr", "./yardr", "/opt/yardr\n/bin/yardr", "/opt/yardr/bin/yardr\n"} {
		if err := c.RecordBinary(bad); err == nil {
			t.Errorf("RecordBinary(%q) took it", bad)
		}
	}
	if got := read(); got != "/usr/local/bin/yardr\n" {
		t.Fatalf("a refused path changed the file to %q", got)
	}
	entries, err := os.ReadDir(c.Home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the home holds %v, %v; want the record alone, with no temporary file", entries, err)
	}
}

// A record that cannot be written is an error for the caller to log, and
// leaves nothing behind. A directory in its place stands for it as a
// read-only home does, which root writes into all the same.
func TestRecordBinaryFailure(t *testing.T) {
	c := &Config{Home: t.TempDir()}
	if err := os.Mkdir(c.BinaryFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordBinary("/opt/yardr/bin/yardr"); err == nil {
		t.Fatal("RecordBinary over a directory: no error")
	}
	if got := c.RecordedBinary(); got != "" {
		t.Errorf("RecordedBinary over a directory = %q", got)
	}
	if entries, err := os.ReadDir(c.Home); err != nil || len(entries) != 1 {
		t.Errorf("the home holds %v, %v; want the directory alone, with no temporary file", entries, err)
	}

	if os.Getuid() == 0 {
		return
	}
	ro := &Config{Home: filepath.Join(t.TempDir(), "ro")}
	if err := os.Mkdir(ro.Home, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := ro.RecordBinary("/opt/yardr/bin/yardr"); err == nil {
		t.Fatal("RecordBinary in a read-only home: no error")
	}
}

// Only a single absolute path is a record: a file of anything else is no
// answer, as a missing one is.
func TestRecordedBinary(t *testing.T) {
	c := &Config{Home: t.TempDir()}
	for file, want := range map[string]string{
		"/opt/yardr/bin/yardr\n": "/opt/yardr/bin/yardr",
		"/opt/yardr/bin/yardr":   "/opt/yardr/bin/yardr",
		"/opt/my yardr/yardr\n":  "/opt/my yardr/yardr",
		"":                       "",
		"\n":                     "",
		"yardr\n":                "",
		"bin/yardr\n":            "",
		"/opt/a/yardr\n/opt/b\n": "",
	} {
		if err := os.WriteFile(c.BinaryFile(), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := c.RecordedBinary(); got != want {
			t.Errorf("RecordedBinary of %q = %q, want %q", file, got, want)
		}
	}
}
