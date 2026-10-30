package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// oldGoogleMail returns the pack's google-mail declaration without the
// drafts read call and the draft_edit update call: the yard's old source
// before the update added them.
func oldGoogleMail(t *testing.T, text string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		if line == "  drafts:" || line == "  draft_edit:" {
			skip = true
			continue
		}
		if skip {
			if len(line) > 2 && strings.HasPrefix(line, "  ") && line[2] != ' ' && strings.HasSuffix(line, ":") {
				skip = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	old := strings.Join(out, "\n")
	d, err := source.Parse("old google-mail", []byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Call("drafts"); ok {
		t.Fatal("the old declaration still names drafts")
	}
	if _, ok := d.Call("draft_edit"); ok {
		t.Fatal("the old declaration still names draft_edit")
	}
	if _, ok := d.Call("draft"); !ok {
		t.Fatal("the old declaration names no draft")
	}
	return old
}

// Picking a preset on a drift card grants the new and revokes nothing: a
// new update call is granted, a new read call is covered by the standing
// read, and a narrower preset leaves the extra holding standing.
func TestCarrySetupDriftGrantsNewAndRevokesNothing(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-mail", 1, setupMailValues(), to)
	drain(t, st, home, "boot-one")
	grantHeld(t, st, "google-mail", to, "read")

	reading, err := st.ReadPack(ctx, "google-mail", "", store.Mappings{})
	if err != nil {
		t.Fatal(err)
	}
	old := oldGoogleMail(t, reading.Sources[0].Text)
	if _, err := st.PutSource(ctx, "google-mail.yaml", old, "mckean"); err != nil {
		t.Fatal(err)
	}
	// Drift to everything: read held, draft new, draft_edit new waiting.
	// The standing read covers the new drafts read, no grant needed.
	seq, err := st.AddAsk(ctx, bead, setupAskOf("google-mail", to), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	done, err := st.ApproveSetup(ctx, bead, seq, 3, nil, "", "mckean", true)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Keep {
		t.Fatalf("the drift approval kept nothing: %+v", done)
	}
	drain(t, st, home, "boot-one")
	grantHeld(t, st, "google-mail", to, "draft", "draft_edit", "read")
	src, err := st.GetSource(ctx, "google-mail")
	if err != nil || src.Text != reading.Sources[0].Text {
		t.Fatalf("the applied declaration is not the pack's")
	}
	f, err := store.ReadEnvFile(src.EnvFile)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the kept account holds %+v, %v", f.Env, err)
	}

	// Drift again to a narrower preset: draft_edit is held beside it and
	// stays, since presets only add.
	if _, err := st.PutSource(ctx, "google-mail.yaml", old, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq, err = st.AddAsk(ctx, bead, setupAskOf("google-mail", to), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveSetup(ctx, bead, seq, 2, nil, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	grantHeld(t, st, "google-mail", to, "draft", "draft_edit", "read")
}

// Typing another account value on a drift card leaves a shared file for
// the source's own default, and the sibling still uses its original value.
func TestCarrySetupDriftTypingAnotherLeavesShared(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-drive", 1, setupMailValues(), to)
	drain(t, st, home, "boot-one")
	askSetupAccount(t, st, bead, "google-mail", 1, "google-drive", to)
	drain(t, st, home, "boot-one")
	mail, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	drive, err := st.GetSource(ctx, "google-drive")
	if err != nil || mail.EnvFile != drive.EnvFile {
		t.Fatalf("google-mail points at %s, want the shared %s", mail.EnvFile, drive.EnvFile)
	}
	seq, err := st.AddAsk(ctx, bead, setupAskOf("google-mail", to), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq && (r.Ask.Setup == nil || !r.Ask.Setup.Given) {
			t.Fatalf("the drift card is not given: %+v", r.Ask.Setup)
		}
	}
	if _, err := st.ApproveSetup(ctx, bead, seq, 2,
		map[string]string{"GOOGLE_ACCOUNT": "other@example.com"}, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	drain(t, st, home, "boot-one")
	mail, err = st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	own := source.EnvFile(home, "google-mail")
	if mail.EnvFile != own {
		t.Fatalf("google-mail points at %s, want its own %s", mail.EnvFile, own)
	}
	f, err := store.ReadEnvFile(own)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "other@example.com" {
		t.Fatalf("the source's own file holds %+v, %v", f.Env, err)
	}
	drive, err = st.GetSource(ctx, "google-drive")
	if err != nil || drive.EnvFile == own {
		t.Fatalf("the sibling points at %+v, %v", drive, err)
	}
	f, err = store.ReadEnvFile(drive.EnvFile)
	if err != nil || source.Lookup(f.Env, "GOOGLE_ACCOUNT") != "ops@example.com" {
		t.Fatalf("the sibling's file holds %+v, %v", f.Env, err)
	}
}

// Taking a candidate's file on a drift card points env_file at it.
func TestCarrySetupDriftTakeCandidatePointsEnvFile(t *testing.T) {
	ctx := context.Background()
	st, home, bead := carryYard(t)
	to := store.Grantee{Crew: "yardmaster"}
	askSetupApprove(t, st, bead, "google-drive", 1, setupMailValues(), to)
	drain(t, st, home, "boot-one")
	askSetupApprove(t, st, bead, "google-mail", 1,
		map[string]string{"GOOGLE_ACCOUNT": "other@example.com"}, to)
	drain(t, st, home, "boot-one")
	mail, err := st.GetSource(ctx, "google-mail")
	if err != nil {
		t.Fatal(err)
	}
	drive, err := st.GetSource(ctx, "google-drive")
	if err != nil || mail.EnvFile == drive.EnvFile {
		t.Fatalf("google-mail points at %s, want its own file", mail.EnvFile)
	}
	seq := askSetupAccount(t, st, bead, "google-mail", 2, "google-drive", to)
	drain(t, st, home, "boot-one")
	mail, err = st.GetSource(ctx, "google-mail")
	if err != nil || mail.EnvFile != drive.EnvFile {
		t.Fatalf("google-mail points at %s, want %s", mail.EnvFile, drive.EnvFile)
	}
	_ = seq
}
