package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An approved pack apply leaves a carrying record in the approval's
// write; one boot claims it, another does not take it, and only the
// claiming boot completes it, which completes the answer with it.
func TestCarryRecordClaimAndComplete(t *testing.T) {
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Fatalf("an unapproved ask's record = %+v, %v", rec, err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err != nil {
		t.Fatal(err)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved || rec.Actor != "mckean" {
		t.Fatalf("the approved ask's record = %+v, %v", rec, err)
	}
	if !strings.Contains(strings.Join(y.asked(t, seq).Lines(), "\n"), "approved, being carried out: yardr pack apply wired") {
		t.Fatalf("the card does not read as being carried out:\n%s", strings.Join(y.asked(t, seq).Lines(), "\n"))
	}
	claimed, err := y.ClaimCarry(y.ctx, "boot-one")
	if err != nil || claimed == nil || claimed.Bead != y.bead || claimed.Ask != seq || claimed.Boot != "boot-one" {
		t.Fatalf("the claim = %+v, %v", claimed, err)
	}
	if again, err := y.ClaimCarry(y.ctx, "boot-two"); err != nil || again != nil {
		t.Fatalf("a second boot's claim = %+v, %v: one claiming write wins", again, err)
	}
	if err := y.CompleteCarry(y.ctx, y.bead, seq, "boot-two", "applied"); err == nil ||
		!strings.Contains(err.Error(), "claimed by another server boot") {
		t.Fatalf("a completion under another boot = %v", err)
	}
	if err := y.CompleteCarry(y.ctx, y.bead, seq, "boot-one", "applied pack wired to the yard: 1 created"); err != nil {
		t.Fatal(err)
	}
	rec, err = y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec.State != CarryDone || rec.Result != "applied pack wired to the yard: 1 created" {
		t.Fatalf("the completed record = %+v, %v", rec, err)
	}
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if !strings.Contains(lines, "approved, ran: yardr pack apply wired") || !strings.Contains(lines, "applied pack wired to the yard: 1 created") {
		t.Fatalf("the completed card reads:\n%s", lines)
	}
	if q, err := y.UserAsks(y.ctx, "mckean"); err != nil || len(q.Decided) != 1 || q.Decided[0].Ran == nil ||
		q.Decided[0].Ran.Result != "applied pack wired to the yard: 1 created" {
		t.Fatalf("the asker's decided row = %+v, %v", q, err)
	}
	if again, err := y.ClaimCarry(y.ctx, "boot-three"); err != nil || again != nil {
		t.Fatalf("a done apply claimed again = %+v, %v", again, err)
	}
}

// A claim of an earlier boot is dead with that boot: a starting server
// takes it up, carries it, and fails it; a failed apply is no more
// taken up than a done one.
func TestCarryResumeAndFail(t *testing.T) {
	y, _ := runYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ClaimCarry(y.ctx, "boot-one"); err != nil {
		t.Fatal(err)
	}
	if n, err := y.ResumeCarry(y.ctx, "boot-two"); err != nil || n != 1 {
		t.Fatalf("the taking up = %d, %v", n, err)
	}
	claimed, err := y.ClaimCarry(y.ctx, "boot-two")
	if err != nil || claimed == nil || claimed.Ask != seq {
		t.Fatalf("the resumed claim = %+v, %v", claimed, err)
	}
	if err := y.FailCarry(y.ctx, y.bead, seq, "boot-two", "boom: nothing further was applied"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(y.asked(t, seq).Lines(), "\n")
	if !strings.Contains(lines, "approved, failed: yardr pack apply wired") || !strings.Contains(lines, "boom: nothing further was applied") {
		t.Fatalf("the failed card reads:\n%s", lines)
	}
	if n, err := y.ResumeCarry(y.ctx, "boot-three"); err != nil || n != 0 {
		t.Fatalf("a failed apply taken up = %d, %v", n, err)
	}
	if again, err := y.ClaimCarry(y.ctx, "boot-three"); err != nil || again != nil {
		t.Fatalf("a failed apply claimed again = %+v, %v", again, err)
	}
}

// An approved setup leaves a carrying record one boot claims: another
// boot does not take it, and the handed account values are taken once,
// for the account's write.
func TestCarrySetupRecordClaimAndSecretOnce(t *testing.T) {
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Fatalf("an unapproved setup's record = %+v, %v", rec, err)
	}
	y.approveSetup(t, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved || rec.Actor != "mckean" {
		t.Fatalf("the approved setup's record = %+v, %v", rec, err)
	}
	if pending, err := y.PendingCarry(y.ctx); err != nil || !pending {
		t.Fatalf("a waiting setup pends = %v, %v", pending, err)
	}
	claimed, err := y.ClaimCarry(y.ctx, "boot-one")
	if err != nil || claimed == nil || claimed.Ask != seq || claimed.Boot != "boot-one" {
		t.Fatalf("the claim = %+v, %v", claimed, err)
	}
	if again, err := y.ClaimCarry(y.ctx, "boot-two"); err != nil || again != nil {
		t.Fatalf("a second boot's claim = %+v, %v: one claiming write wins", again, err)
	}
	values, ok := y.TakeSetupSecret(y.bead, seq)
	if !ok || values["SETUP_ACCOUNT"] != "ops@example.com" {
		t.Fatalf("the taken values = %+v, %v", values, ok)
	}
	if again, ok := y.TakeSetupSecret(y.bead, seq); ok || again != nil {
		t.Fatalf("a second take = %+v, %v: values are taken once", again, ok)
	}
}

// CheckCarrySource holds a declared source as its approval was held
// to it, finishes a half-written one forward, and refuses a third
// hand, each by the file that moved.
func TestCheckCarrySource(t *testing.T) {
	y, _ := runYard(t)
	dir := plantWired(t, y)
	// The standing copies differ from the pack's in one file, so the
	// card holds an old and a new standing for it.
	thread := filepath.Join(dir, "wired", "thread")
	if err := os.WriteFile(thread, []byte("#!/bin/sh\n# thread, as the yard has it\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	seq := y.ask(t, packAsk(), "yardmaster")
	got := y.asked(t, seq)
	carried := got.Ask.Run.Carried[0]
	seen := got.Ask.Run.Seen["source wired"]
	snap := CarrySource{Name: "wired"}

	if state, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err != nil || state != CarryApproved {
		t.Fatalf("as carded = %q, %v", state, err)
	}
	// The pack's text stored and its file copied: finishing, not stale.
	if err := os.WriteFile(thread, []byte(wiredFiles["wired/thread"]), 0o700); err != nil {
		t.Fatal(err)
	}
	if state, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err != nil || state != CarryStored {
		t.Fatalf("half written = %q, %v", state, err)
	}
	// A third text in the copy: a hand outside the carrying out.
	if err := os.WriteFile(thread, []byte("#!/bin/sh\n# a third hand\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err == nil || !strings.Contains(err.Error(), "wired/thread") {
		t.Fatalf("a third copy = %v", err)
	}
	// The pack's bytes at another mode than the card showed.
	if err := os.WriteFile(thread, []byte(wiredFiles["wired/thread"]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(thread, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err == nil || !strings.Contains(err.Error(), "wired/thread") {
		t.Fatalf("a chmodded copy = %v", err)
	}
	if err := os.Chmod(thread, 0o700); err != nil {
		t.Fatal(err)
	}
	// An entry neither declaration names, added since the card.
	if err := os.WriteFile(filepath.Join(dir, "planted.txt"), []byte("by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err == nil || !strings.Contains(err.Error(), "planted.txt") {
		t.Fatalf("a planted entry = %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "planted.txt")); err != nil {
		t.Fatal(err)
	}
	// A credential's file set on the half-added source.
	if _, err := y.SetSource(context.Background(), "wired", SourceEnvFile, "/tmp/wired.env", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("a set credential = %v", err)
	}
	// A third declaration under the name.
	if _, err := y.PutSource(y.ctx, "wired.yaml", strings.Replace(wiredDecl, "https://wired.example.com/v1", "https://other.example.com", 1), "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.CheckCarrySource(y.ctx, carried, seen, snap); err == nil || !strings.Contains(err.Error(), "another declaration") {
		t.Fatalf("a third text = %v", err)
	}
}
