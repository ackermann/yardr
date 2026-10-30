package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

const keyedFile = `name: crm
base: https://crm.example.com/api
credential: { env: CRM_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

const urlKeyFile = `name: hook
credential: { env: HOOK_URL, url: true }
calls:
  send: { kind: update, request: { method: POST } }
`

// keyedYard is a store with the source crm, and the file its credential
// goes to when none is set.
func keyedYard(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	if _, err := s.PutSource(context.Background(), "crm.yaml", keyedFile, "jo"); err != nil {
		t.Fatal(err)
	}
	return s, filepath.Join(filepath.Dir(path), "sources", "crm.env")
}

// sourceSetEvents counts the source_set events of the store.
func sourceSetEvents(t *testing.T, s *Store) int {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "source_set" {
			n++
		}
	}
	return n
}

// A transaction that fails after the rename leaves the value in the file
// and nothing in the store: no env_file where there was none, no event.
// The caller is told so with the names and the file, never the value, and
// the same giving again ends as one that worked: the file with the line
// once, the env_file set, one source_set event.
func TestKeySourceNotRecorded(t *testing.T) {
	// Sequential: the afterKeyRename hook.
	s, file := keyedYard(t)
	ctx := context.Background()
	full := errors.New("no space left on device")
	afterKeyRename = func() error { return full }
	t.Cleanup(func() { afterKeyRename = nil })

	_, err := s.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": "s3cret"}, "jo")
	var half *KeyNotRecorded
	if !errors.As(err, &half) || !errors.Is(err, full) {
		t.Fatalf("err = %v, want a KeyNotRecorded around the store's error", err)
	}
	// What is left is the yard's own file failing under a committed write,
	// not a refusal: marked, so a device that is no operator's is told
	// that it failed and not on which file.
	var op *OperationalError
	if !errors.As(err, &op) {
		t.Fatalf("err = %v, want it marked as the yard's own file failing", err)
	}
	if want := "CRM_TOKEN is written to " + file + ", but the yard could not record it: no space left on device. Give it again."; err.Error() != want {
		t.Fatalf("err = %q\nwant  %q", err, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q, want the value: the rename is not undone", b)
	}
	// The file stands where no setting names it, and the source says so.
	if src, err := s.GetSource(ctx, "crm"); err != nil || src.EnvFile != "" || src.EnvFileUnset != file {
		t.Fatalf("the source = %+v, %v: want no env_file, and the file said as unset", src, err)
	}
	if n := sourceSetEvents(t, s); n != 0 {
		t.Fatalf("%d source_set events after a giving that was not recorded", n)
	}

	// The way back is the same call again.
	afterKeyRename = nil
	keyed, err := s.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": "s3cret"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.State != "CRM_TOKEN is set in "+file || keyed.Source.EnvFile != file {
		t.Fatalf("keyed = %+v (source %+v)", keyed, keyed.Source)
	}
	if src, err := s.GetSource(ctx, "crm"); err != nil || src.EnvFile != file || src.EnvFileUnset != "" {
		t.Fatalf("the source after the second giving = %+v, %v", src, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if n := sourceSetEvents(t, s); n != 1 {
		t.Fatalf("%d source_set events, want 1", n)
	}
	if _, err := os.Lstat(EnvFileNew(file)); !os.IsNotExist(err) {
		t.Fatalf("a second file is left: %v", err)
	}
}

// With an env_file set the store has nothing to record, and a failure
// after the rename is said the same: the new value is in use, and the
// setting is as it was.
func TestKeySourceNotRecordedWithAnEnvFile(t *testing.T) {
	// Sequential: the afterKeyRename hook.
	s, _ := keyedYard(t)
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "mine.env")
	if err := os.WriteFile(file, []byte("# mine\nCRM_TOKEN=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSource(ctx, "crm", SourceEnvFile, file, "jo"); err != nil {
		t.Fatal(err)
	}
	afterKeyRename = func() error { return errors.New("the store failed") }
	t.Cleanup(func() { afterKeyRename = nil })
	_, err := s.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": "s3cret"}, "jo")
	var half *KeyNotRecorded
	if !errors.As(err, &half) || half.File != file || strings.Join(half.Names, ",") != "CRM_TOKEN" || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "# mine\nCRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if src, err := s.GetSource(ctx, "crm"); err != nil || src.EnvFile != file {
		t.Fatalf("the source = %+v, %v", src, err)
	}
	if n := sourceSetEvents(t, s); n != 1 {
		t.Fatalf("%d source_set events, want the one of SetSource", n)
	}
}

// write's batch may run a function twice for one call: once in a
// transaction that another caller's function fails, and again by itself.
// The giving ends the same then as when it ran once.
func TestKeySourceRunTwice(t *testing.T) {
	t.Parallel()
	s, file := keyedYard(t)
	ctx := context.Background()
	var k sourceKey
	give := func(w *wtx) error {
		return w.keySource(ctx, s.home, "crm", map[string]string{"CRM_TOKEN": "s3cret"}, "jo", &k)
	}
	others := errors.New("another caller's function failed")
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := give(&wtx{Tx: tx, mentions: &s.mentions}); err != nil {
			return err
		}
		return others
	})
	if !errors.Is(err, others) {
		t.Fatal(err)
	}
	if err := s.write(give); err != nil {
		t.Fatal(err)
	}
	if !k.renamed || k.file != file || k.src.EnvFile != file {
		t.Fatalf("the giving = %+v", k)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
	if src, err := s.GetSource(ctx, "crm"); err != nil || src.EnvFile != file {
		t.Fatalf("the source = %+v, %v", src, err)
	}
	if n := sourceSetEvents(t, s); n != 1 {
		t.Fatalf("%d source_set events, want 1", n)
	}
	if _, err := os.Lstat(EnvFileNew(file)); !os.IsNotExist(err) {
		t.Fatalf("a second file is left: %v", err)
	}
}

// A second file left beside the env file by a write that died is removed
// by the next giving, and nothing of it is in the file.
func TestKeySourceRemovesALeftSecondFile(t *testing.T) {
	t.Parallel()
	s, file := keyedYard(t)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(filepath.Dir(file), ".crm.env.new")
	if err := os.WriteFile(left, []byte("CRM_TOKEN=stale\nSTALE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KeySource(context.Background(), "crm", map[string]string{"CRM_TOKEN": "s3cret"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(left); !os.IsNotExist(err) {
		t.Fatalf("the left file is still there: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret\n" {
		t.Fatalf("the file holds %q", b)
	}
}

// A value given for a credential that is the address is refused where it
// is given when it is no address calls go to, in words that name the fault
// and none of the value; an https address is accepted. The refusal comes
// before any file write and before the env_file is set.
func TestKeySourceRefusesACredentialThatIsNoAddress(t *testing.T) {
	t.Parallel()
	s, _ := keyedYard(t)
	ctx := context.Background()
	if _, err := s.PutSource(ctx, "hook.yaml", urlKeyFile, "jo"); err != nil {
		t.Fatal(err)
	}
	const secret = "abc123"
	for _, tc := range []struct{ value, want string }{
		{"https://hooks.example.com/catch/" + secret + "?key=" + secret, "has a query or a fragment"},
		{"http://hooks.example.com/catch/" + secret, "is plain http to a machine that is not this one"},
	} {
		_, err := s.KeySource(ctx, "hook", map[string]string{"HOOK_URL": tc.value}, "jo")
		if err == nil || !strings.Contains(err.Error(), "source hook: the value of HOOK_URL "+tc.want) {
			t.Errorf("%q: err = %v, want the fault named", tc.value, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hooks.example.com") {
			t.Errorf("%q: the error repeats the value: %v", tc.value, err)
		}
	}
	file := filepath.Join(s.home, "sources", "hook.env")
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("a refused giving wrote the file: %v", err)
	}
	if src, err := s.GetSource(ctx, "hook"); err != nil || src.EnvFile != "" {
		t.Fatalf("after refused givings the source = %+v, %v: want no env_file", src, err)
	}
	if n := sourceSetEvents(t, s); n != 0 {
		t.Fatalf("%d source_set events after refused givings", n)
	}
	keyed, err := s.KeySource(ctx, "hook", map[string]string{"HOOK_URL": "https://hooks.example.com/catch/" + secret}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || strings.Join(keyed.Given, ",") != "HOOK_URL" {
		t.Fatalf("keyed = %+v", keyed)
	}
	if b, _ := os.ReadFile(file); string(b) != "HOOK_URL=https://hooks.example.com/catch/"+secret+"\n" {
		t.Fatalf("the file holds %q", b)
	}
}
