package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

const crmFreeDecl = `name: crm
base: https://crm.example.com
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

const crmCommandsDecl = `name: crm
base: https://crm.example.com
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
thread:
  reply: [crm/thread, reply]
`

// carryLockYard is a yard in home with the operator mckean, for the
// terminal source commands under the apply lock.
func carryLockYard(t *testing.T) (*store.Store, string, *app) {
	t.Helper()
	ctx := context.Background()
	home := filepath.Join(shortDir(t, "ycl"), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(ctx) })
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	env := yardEnv(t, st)
	env["YARDR_ACTOR"] = "mckean"
	a := appOn(st)
	a.cfg = config.At(home)
	a.actor = "mckean"
	return st, home, a
}

func runRm(a *app) chan error {
	cmd := a.sourceCmd()
	cmd.SetArgs([]string{"rm", "crm"})
	cmd.SetOut(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	return done
}

func runAdd(a *app, file string) chan error {
	cmd := a.sourceCmd()
	cmd.SetArgs([]string{"add", file})
	cmd.SetOut(io.Discard)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	return done
}

// A terminal rm takes the apply lock before it reads the source and
// decides whether command files exist: a remove that waits while a
// carrying out gives a commands-free source command files deletes
// those files too, never leaving the copies behind (yardr-bht9 3.5,
// the sixth critique on yardr-bht9).
func TestSourceRmLockBeforeRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, home, a := carryLockYard(t)
	if _, err := st.PutSource(ctx, "crm.yaml", crmFreeDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	// The carrying out holds the lock for its write phase; the rm
	// started now waits on it before it reads anything.
	release, err := config.AcquireApplyLock(home)
	if err != nil {
		t.Fatal(err)
	}
	done := runRm(a)
	time.Sleep(100 * time.Millisecond)
	if _, err := st.GetSource(ctx, "crm"); err != nil {
		t.Fatalf("the waiting rm deleted early: %v", err)
	}
	// The carrying out gives the source its command files.
	if _, err := st.PutSource(ctx, "crm.yaml", crmCommandsDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteCopies(home, "crm", []source.FileCopy{
		{Program: "crm/thread", Data: []byte("#!/bin/sh\n# thread\n")},
	}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSource(ctx, "crm"); err == nil {
		t.Fatal("the rm left the source")
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "crm")); !os.IsNotExist(err) {
		t.Fatalf("the rm left the copies the carrying out wrote: %v", err)
	}
}

// A terminal rm whose lock is held waits, and completes whole when
// it lets go: source and copies go together.
func TestSourceRmWaitsOnApplyLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, home, a := carryLockYard(t)
	if _, err := st.PutSource(ctx, "crm.yaml", crmCommandsDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteCopies(home, "crm", []source.FileCopy{
		{Program: "crm/thread", Data: []byte("#!/bin/sh\n# thread\n")},
	}); err != nil {
		t.Fatal(err)
	}
	release, err := config.AcquireApplyLock(home)
	if err != nil {
		t.Fatal(err)
	}
	done := runRm(a)
	time.Sleep(100 * time.Millisecond)
	if _, err := st.GetSource(ctx, "crm"); err != nil {
		t.Fatalf("the waiting rm deleted early: %v", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSource(ctx, "crm"); err == nil {
		t.Fatal("the rm left the source")
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "crm")); !os.IsNotExist(err) {
		t.Fatalf("the rm left its copies: %v", err)
	}
}

// A terminal add decides whether it must replace the source's copies
// directory only after taking the apply lock: while it waits, a carrying
// out may give a commands-free source command files that the add must
// remove when it replaces the source.
func TestSourceAddReadsStandingCopiesUnderApplyLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, home, a := carryLockYard(t)
	yardEnv(t, st)["YARDR_HOME"] = home
	if _, err := st.PutSource(ctx, "crm.yaml", crmFreeDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "crm.yaml")
	if err := os.WriteFile(file, []byte(crmFreeDecl), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := config.AcquireApplyLock(home)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	done := runAdd(a, file)
	// The add reaches its lock while the simulated carrying out holds it.
	time.Sleep(100 * time.Millisecond)
	if _, err := st.GetSource(ctx, "crm"); err != nil {
		t.Fatalf("the waiting add replaced the source early: %v", err)
	}
	if _, err := st.PutSource(ctx, "crm.yaml", crmCommandsDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := source.WriteCopies(home, "crm", []source.FileCopy{
		{Program: "crm/thread", Data: []byte("#!/bin/sh\n# thread\n")},
	}); err != nil {
		t.Fatal(err)
	}
	release()
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	src, err := st.GetSource(ctx, "crm")
	if err != nil || src.Text != crmFreeDecl {
		t.Fatalf("the source after add = %+v, %v", src, err)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "crm")); !os.IsNotExist(err) {
		t.Fatalf("the commands-free add left the carrying out's copies: %v", err)
	}
}
