package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // past the umask
		t.Fatal(err)
	}
	return p
}

func TestReadEnvFile(t *testing.T) {
	p := writeEnv(t, "# Linear\n\nLINEAR_API_KEY=lin_api_s3cret\n  SPACED = a b=c  \nRAW=$HOME/x\nEMPTY=\n", 0o600)
	f, err := ReadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LINEAR_API_KEY=lin_api_s3cret", "SPACED= a b=c", "RAW=$HOME/x", "EMPTY="}
	if !slices.Equal(f.Env, want) {
		t.Fatalf("env = %q, want %q", f.Env, want)
	}
	if keys := f.Keys(); !slices.Equal(keys, []string{"LINEAR_API_KEY", "SPACED", "RAW", "EMPTY"}) {
		t.Fatalf("keys = %v", keys)
	}
	if _, err := ReadEnvFile(writeEnv(t, "A=1\n", 0o400)); err != nil {
		t.Fatalf("0400 is stricter than 0600 and allowed: %v", err)
	}
}

func TestReadEnvFileRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"missing":        filepath.Join(dir, "nope.env"),
		"directory":      dir,
		"group readable": writeEnv(t, "TOKEN=s3cret\n", 0o640),
		"world readable": writeEnv(t, "TOKEN=s3cret\n", 0o604),
		"group writable": writeEnv(t, "TOKEN=s3cret\n", 0o620),
		"bad line":       writeEnv(t, "TOKEN=ok\nnot a pair s3cret\n", 0o600),
		"bad key":        writeEnv(t, "export TOKEN=s3cret\n", 0o600),
	} {
		_, err := ReadEnvFile(path)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: error shows a value: %v", name, err)
		}
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got, _ := ExpandPath("~/.yardr/crew/yardmaster.env"); got != filepath.Join(home, ".yardr/crew/yardmaster.env") {
		t.Fatalf("~ not expanded: %s", got)
	}
}

// A hook's secret file is read under the env_file rule.
func TestReadSecretFile(t *testing.T) {
	b, p, err := ReadSecretFile(writeEnv(t, "  s3cret\n", 0o600))
	if err != nil || string(b) != "s3cret" || !filepath.IsAbs(p) {
		t.Fatalf("ReadSecretFile = %q %q %v", b, p, err)
	}
	dir := t.TempDir()
	for name, path := range map[string]string{
		"missing":        filepath.Join(dir, "nope"),
		"directory":      dir,
		"group readable": writeEnv(t, "s3cret\n", 0o640),
		"world readable": writeEnv(t, "s3cret\n", 0o644),
	} {
		b, _, err := ReadSecretFile(path)
		if err == nil || b != nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: error shows the secret: %v", name, err)
		}
	}
}

// The writer keeps what it was not given: comments, blank lines, other
// names and lines it cannot read. A given name is set on its first line
// and its later lines go, a new one is a line at the end, and the file is
// replaced by a rename with mode 0600.
func TestWriteEnvFile(t *testing.T) {
	p := writeEnv(t, "# the CRM\n\nCRM_TOKEN=old\n  OTHER = kept as written  \nexport NOT=a line\nCRM_TOKEN=older\nLAST=no newline", 0o400)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	got, renamed, err := WriteEnvFile(p, map[string]string{"CRM_TOKEN": "s3cret", "CRM_UPDATE_TOKEN": "wr1te"})
	if err != nil || !renamed || got != p {
		t.Fatalf("WriteEnvFile = %q, %v, %v", got, renamed, err)
	}
	want := "# the CRM\n\nCRM_TOKEN=s3cret\n  OTHER = kept as written  \nexport NOT=a line\nLAST=no newline\nCRM_UPDATE_TOKEN=wr1te\n"
	if b, _ := os.ReadFile(p); string(b) != want {
		t.Fatalf("file =\n%s\nwant\n%s", b, want)
	}
	after, err := os.Stat(p)
	if err != nil || after.Mode().Perm() != 0o600 || os.SameFile(before, after) {
		t.Fatalf("mode %v, the same file as before %v, %v: want 0600 and another file, renamed into place", after.Mode(), os.SameFile(before, after), err)
	}
	if _, err := os.Lstat(EnvFileNew(p)); !os.IsNotExist(err) {
		t.Fatalf("the second file is still there: %v", err)
	}
	// The same values again write the same file.
	if _, _, err := WriteEnvFile(p, map[string]string{"CRM_UPDATE_TOKEN": "wr1te", "CRM_TOKEN": "s3cret"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != want {
		t.Fatalf("written twice =\n%s", b)
	}

	// Names struck by the same write are dropped wherever they stand,
	// and a file that is not there is not made by striking.
	struck := writeEnv(t, "# mine\nTOKEN=gone\nOTHER=kept\nTOKEN=again\n", 0o600)
	if _, renamed, err := RemoveEnvNames(struck, []string{"TOKEN", "ABSENT"}); err != nil || !renamed {
		t.Fatalf("RemoveEnvNames = %v, %v", renamed, err)
	}
	if b, _ := os.ReadFile(struck); string(b) != "# mine\nOTHER=kept\n" {
		t.Fatalf("struck file =\n%s", b)
	}
	// A name asked to be removed stays removed even if the same write
	// also supplies a value for it and the old file had no such line.
	overlap := filepath.Join(t.TempDir(), "overlap.env")
	if _, _, err := WriteEnvFile(overlap, map[string]string{"TOKEN": ""}, "TOKEN"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(overlap); len(b) != 0 {
		t.Fatalf("a struck value was added to an empty file: %q", b)
	}
	missing := filepath.Join(t.TempDir(), "none.env")
	if _, renamed, err := RemoveEnvNames(missing, []string{"TOKEN"}); err != nil || renamed {
		t.Fatalf("a missing file: renamed %v, %v", renamed, err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("striking made the file: %v", err)
	}

	// A file that is not there is made, with its directory, and read back
	// under the rule.
	fresh := filepath.Join(t.TempDir(), "sources", "crm.env")
	if _, renamed, err := WriteEnvFile(fresh, map[string]string{"B": "2", "A": "1"}); err != nil || !renamed {
		t.Fatalf("a new file: %v, %v", renamed, err)
	}
	f, err := ReadEnvFile(fresh)
	if err != nil || !slices.Equal(f.Env, []string{"A=1", "B=2"}) {
		t.Fatalf("read back = %+v, %v", f, err)
	}
	if st, err := os.Stat(filepath.Dir(fresh)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("its directory: %v, %v", st.Mode(), err)
	}
}

// What the writer refuses leaves the file as it was, to the byte, and no
// second file; no error holds a value. A failure of the file is marked
// (OperationalError); a refusal of a name or a value is not, and reads
// whole wherever it is told.
func TestWriteEnvFileRefuses(t *testing.T) {
	const body = "# kept\nTOKEN=old-s3cret\n"
	elsewhere := writeEnv(t, body, 0o600)
	link := filepath.Join(t.TempDir(), "link.env")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	// A directory nothing can be made in: the second file cannot be.
	locked := writeEnv(t, body, 0o600)
	if err := os.Chmod(filepath.Dir(locked), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(locked), 0o700) })
	good := map[string]string{"TOKEN": "new-s3cret"}
	for name, c := range map[string]struct {
		path   string
		values map[string]string
		want   string
		marked bool
	}{
		"open to others":  {writeEnv(t, body, 0o640), good, "is open to others (mode 0640); chmod 600 it", true},
		"a directory":     {t.TempDir(), good, "is not a regular file", true},
		"a symbolic link": {link, good, "is a symbolic link, and the yard writes through none", true},
		"an empty value":  {writeEnv(t, body, 0o600), map[string]string{"TOKEN": ""}, "the value for TOKEN is empty", false},
		"a line break":    {writeEnv(t, body, 0o600), map[string]string{"TOKEN": "new-s3cret\nOTHER=x"}, "the value for TOKEN has a line break in it", false},
		"a carriage":      {writeEnv(t, body, 0o600), map[string]string{"TOKEN": "new-s3cret\rx"}, "the value for TOKEN has a line break in it", false},
		"white space":     {writeEnv(t, body, 0o600), map[string]string{"TOKEN": "new-s3cret "}, "the value for TOKEN begins or ends with white space", false},
		"no name":         {writeEnv(t, body, 0o600), map[string]string{"A B": "new-s3cret"}, `"A B" is no name of a variable`, false},
		"a locked folder": {locked, good, "permission denied", true},
		"one bad of two":  {writeEnv(t, body, 0o600), map[string]string{"TOKEN": "new-s3cret", "OTHER": ""}, "the value for OTHER is empty", false},
	} {
		_, renamed, err := WriteEnvFile(c.path, c.values)
		if err == nil || renamed || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: renamed %v, err = %v, want %q", name, renamed, err, c.want)
			continue
		}
		var op *OperationalError
		if marked := errors.As(err, &op); marked != c.marked {
			t.Errorf("%s: errors.As OperationalError = %v, want %v: %v", name, marked, c.marked, err)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error shows a value: %v", name, err)
		}
		if _, err := os.Lstat(EnvFileNew(c.path)); !os.IsNotExist(err) {
			t.Errorf("%s: a second file was left: %v", name, err)
		}
		if st, err := os.Stat(c.path); err == nil && st.Mode().IsRegular() {
			if b, _ := os.ReadFile(c.path); string(b) != body {
				t.Errorf("%s: the file changed:\n%s", name, b)
			}
		}
	}
	// The link is a link still, and the file it points to was not read
	// into another and not written.
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link: %v, %v", st, err)
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != body {
		t.Fatalf("the file the link points to changed:\n%s", b)
	}
}

// EnvValueProblem says why a value cannot be written, naming the variable and
// no file, and the write's refusal is those words after the file's name: a
// caller that refuses before it looks at any file (a device's giving, which is
// told no path of the yard's) says what the write would have.
func TestEnvValueProblemIsTheWritesRefusalWithoutTheFile(t *testing.T) {
	p := writeEnv(t, "TOKEN=old\n", 0o600)
	for _, value := range []string{"", "a\nb", "a\rb", " a", "a "} {
		why := EnvValueProblem("TOKEN", value)
		if why == "" || strings.Contains(why, p) || strings.Contains(why, "env_file") {
			t.Errorf("%q: the problem is %q, want words that name the variable and no file", value, why)
		}
		if _, _, err := WriteEnvFile(p, map[string]string{"TOKEN": value}); err == nil || err.Error() != "env_file "+p+": "+why {
			t.Errorf("%q: the write says %v, want the file's name and %q", value, err, why)
		}
	}
	if why := EnvValueProblem("TOKEN", "s3cret"); why != "" {
		t.Errorf("a value the file holds is a problem: %q", why)
	}
}

// A second file left by a write that died is removed by the next write,
// whatever it is and whatever it holds: nothing of it reaches the env file.
func TestWriteEnvFileRemovesALeftSecondFile(t *testing.T) {
	p := writeEnv(t, "TOKEN=old\n", 0o600)
	left := EnvFileNew(p)
	if filepath.Base(left) != ".r.env.new" || filepath.Dir(left) != filepath.Dir(p) {
		t.Fatalf("the second file is %s", left)
	}
	for name, leave := range map[string]func() error{
		"a file open to others": func() error { return os.WriteFile(left, []byte("TOKEN=stale\nSTALE=1\n"), 0o644) },
		"a link to elsewhere":   func() error { return os.Symlink(writeEnv(t, "ELSEWHERE=1\n", 0o600), left) },
	} {
		if err := leave(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := WriteEnvFile(p, map[string]string{"TOKEN": "new"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Lstat(left); !os.IsNotExist(err) {
			t.Fatalf("%s: still there: %v", name, err)
		}
		st, err := os.Lstat(p)
		if b, _ := os.ReadFile(p); err != nil || string(b) != "TOKEN=new\n" || st.Mode() != 0o600 {
			t.Fatalf("%s: the env file is %q, mode %v, %v", name, b, st.Mode(), err)
		}
	}
}
