package config

import (
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
	if got, _ := ExpandPath("~/.yardr/residents/mayor.env"); got != filepath.Join(home, ".yardr/residents/mayor.env") {
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
