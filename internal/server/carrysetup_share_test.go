package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// askSetupAccount asks the setup of pack for to and approves it taking the
// named candidate, as the operator.
func askSetupAccount(t *testing.T, st *store.Store, bead, pack string, option int, account string, to ...store.Grantee) uint64 {
	t.Helper()
	ctx := context.Background()
	seq, err := st.AddAsk(ctx, bead, setupAskOf(pack, to...), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveSetup(ctx, bead, seq, option, nil, account, "mckean", true); err != nil {
		t.Fatal(err)
	}
	return seq
}

func setupCandidatesOf(t *testing.T, st *store.Store, bead string, seq uint64) []store.AskCandidate {
	t.Helper()
	notes, err := st.Notes(context.Background(), bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			if r.Ask.Setup == nil {
				t.Fatalf("ask %d sets up no pack", seq)
			}
			return r.Ask.Setup.Candidates
		}
	}
	t.Fatalf("no ask %d on %s", seq, bead)
	return nil
}

// A second google source's card lists the first by name with its account
// address, and picking it points the new source's env_file at that file.
func TestCarrySetupPickPointsEnvFile(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-drive", 1, map[string]string{"GOOGLE_ACCOUNT": "ops@example.com"}, to)
	drain(t, st, home, "boot-one")
	drive, err := st.GetSource(ctx, "google-drive")
	if err != nil || drive.EnvFile == "" {
		t.Fatalf("google-drive holds %+v, %v", drive, err)
	}
	seq := askSetupAccount(t, st, bead, "google-mail", 1, "google-drive", to)
	got := setupCandidatesOf(t, st, bead, seq)
	if len(got) != 1 || got[0].Source != "google-drive" || got[0].Values["GOOGLE_ACCOUNT"] != "ops@example.com" {
		t.Fatalf("the candidates = %+v, want google-drive with its address", got)
	}
	drain(t, st, home, "boot-one")
	mail, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if mail.EnvFile != drive.EnvFile {
		t.Fatalf("google-mail points at %s, want %s", mail.EnvFile, drive.EnvFile)
	}
	f, err := config.ReadEnvFile(mail.EnvFile)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the shared file holds %+v, %v", f.Env, err)
	}
	lines := resolvedLines(t, st, bead, seq)
	if !strings.Contains(lines, "the account GOOGLE_ACCOUNT is set in "+drive.EnvFile) {
		t.Errorf("the carried card reads:\n%s", lines)
	}
	// A device that is not told the file reads the carrier's own result cut
	// at it: the account is set, and the grants' line after it is as it was.
	view, err := st.Bead(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range view.Asks {
		if a.Ask != seq {
			continue
		}
		if a.Ran == nil || a.Setup == nil {
			t.Fatalf("the carried ask reads %+v", a)
		}
		told := a.Setup.WithoutAccountFile(*a.Ran).Result
		if strings.Contains(told, drive.EnvFile) || !strings.Contains(told, "\nthe account GOOGLE_ACCOUNT is set\n") ||
			strings.Count(told, "\n") != strings.Count(a.Ran.Result, "\n") {
			t.Errorf("the result is told as\n%s\nfrom\n%s", told, a.Ran.Result)
		}
	}
}

// The first setup of google-docs writes one typed value to its first
// source's default and points both sources at it, the file named on the
// card.
func TestCarrySetupGoogleDocsWritesOnceAndPointsBoth(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	seq, err := st.AddAsk(ctx, bead, setupAskOf("google-docs", to), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	var file string
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			file = r.Ask.Setup.AccountFile
		}
	}
	want := filepath.Join(home, "sources", "google-docs.env")
	if file != want {
		t.Fatalf("the card names %s, want %s", file, want)
	}
	if _, err := st.ApproveSetup(ctx, bead, seq, 1,
		map[string]string{"GOOGLE_ACCOUNT": "ops@example.com"}, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	f, err := config.ReadEnvFile(want)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the shared file holds %+v, %v", f.Env, err)
	}
	for _, name := range []string{"google-docs", "google-slides"} {
		s, err := st.GetSource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if s.EnvFile != want {
			t.Fatalf("%s points at %s, want %s", name, s.EnvFile, want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "google-slides.env")); !os.IsNotExist(err) {
		t.Error("a second account file was written beside the shared one")
	}
}

// Typing another leaves a shared file for the source's own default before
// writing, and the sibling still uses its original value.
func TestCarrySetupTypingAnotherLeavesShared(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-drive", 1, map[string]string{"GOOGLE_ACCOUNT": "ops@example.com"}, to)
	drain(t, st, home, "boot-one")
	drive, err := st.GetSource(ctx, "google-drive")
	if err != nil {
		t.Fatal(err)
	}
	reading, err := st.ReadPack(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "google-mail.yaml", reading.Sources[0].Text, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "google-mail", store.SourceEnvFile, drive.EnvFile, "mckean"); err != nil {
		t.Fatal(err)
	}
	askSetupApprove(t, st, bead, "google-mail", 1, map[string]string{"GOOGLE_ACCOUNT": "other@example.com"}, to)
	drain(t, st, home, "boot-one")
	mail, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, "sources", "google-mail.env")
	if mail.EnvFile != own {
		t.Fatalf("google-mail points at %s, want its own %s", mail.EnvFile, own)
	}
	f, err := config.ReadEnvFile(own)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "other@example.com" {
		t.Fatalf("the source's own file holds %+v, %v", f.Env, err)
	}
	drive, err = st.GetSource(ctx, "google-drive")
	if err != nil || drive.EnvFile == own {
		t.Fatalf("the sibling points at %+v, %v", drive, err)
	}
	f, err = config.ReadEnvFile(drive.EnvFile)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the sibling's file holds %+v, %v", f.Env, err)
	}
}

// Right before pointing at the picked file, the carrying out rechecks the
// carded candidate: a file changed since the approval fails the step and
// records what stands, so no source uses an address the card did not show.
func TestCarrySetupCandidateRecheckFails(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-drive", 1, map[string]string{"GOOGLE_ACCOUNT": "ops@example.com"}, to)
	drain(t, st, home, "boot-one")
	seq := askSetupAccount(t, st, bead, "google-mail", 1, "google-drive", to)
	drive, err := st.GetSource(ctx, "google-drive")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(drive.EnvFile, []byte("GOOGLE_ACCOUNT=changed@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	rec := carryState(t, st, bead, seq)
	if rec.State != store.CarryFailed {
		t.Fatalf("the carrying state = %s, want failed", rec.State)
	}
	if !strings.Contains(rec.Result, "the candidate google-drive no longer holds GOOGLE_ACCOUNT as carded: its file no longer holds it") {
		t.Errorf("the failure records %q", rec.Result)
	}
	mail, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if mail.EnvFile == drive.EnvFile {
		t.Error("the source points at a file the card did not show")
	}
	grants, err := st.SourceGrants(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Errorf("the grants ran past a failed account step: %+v", grants)
	}
}
