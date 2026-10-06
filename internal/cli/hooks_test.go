package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// hook add takes every source with an adapter, github among them, and its
// help lists them.
func TestHookAddGitHub(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	f := filepath.Join(t.TempDir(), "github.secret")
	if err := os.WriteFile(f, []byte("gh_test_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.hookCmd(), "add", "gh", "--source", "github", "--secret-file", f)
	if err != nil || !strings.Contains(out, "hook gh saved: POST /hooks/gh (github)") {
		t.Fatalf("hook add: %q %v", out, err)
	}
	h, err := st.GetHook(context.Background(), "gh")
	if err != nil || h.Source != "github" || h.SecretFile != f {
		t.Fatalf("stored hook %+v %v", h, err)
	}
	_, err = run(t, a.hookCmd(), "add", "x", "--source", "gitlab", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), "github, linear, token") {
		t.Fatalf("unknown source err = %v", err)
	}
	for _, c := range a.hookCmd().Commands() {
		if c.Name() == "add" && !strings.Contains(c.Long, "\n  github  ") {
			t.Fatalf("hook add help does not list github:\n%s", c.Long)
		}
	}
}

// A secret file anyone else can read is refused and nothing is saved, as an
// env_file of that mode is; 0600 is saved. (yardr-7730: it used to be saved
// with a warning.)
func TestHookAddRefusesSecretFileOpenToOthers(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	f := filepath.Join(t.TempDir(), "t.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o644); err != nil { // past the umask
		t.Fatal(err)
	}
	_, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f)
	if err == nil || !strings.Contains(err.Error(), "mode 0644") || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("0644 secret file: err = %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error shows the secret: %v", err)
	}
	if hs, err := st.Hooks(context.Background()); err != nil || len(hs) != 0 {
		t.Fatalf("a refused hook was saved: %+v %v", hs, err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", t.TempDir()); err == nil {
		t.Fatal("a directory was accepted as secret file")
	}

	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatalf("0600 secret file: %q %v", out, err)
	}
	h, err := st.GetHook(context.Background(), "t")
	if err != nil || h.SecretFile != f || h.Blocked != "" {
		t.Fatalf("stored hook %+v %v", h, err)
	}
	out, err := run(t, a.hookCmd(), "list")
	if err != nil || !strings.Contains(out, "STATE") || !strings.Contains(out, "ok") || strings.Contains(out, "blocked") {
		t.Fatalf("hook list: %q %v", out, err)
	}
}

// hook list shows a hook the receiver has blocked, and why; adding the hook
// again starts it unblocked, and no client can save one as blocked.
func TestHookListShowsBlocked(t *testing.T) {
	st, own := storetest.NewOwned(t)
	a := &app{st: st, actor: "me"}
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "t.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatal(err)
	}
	if err := own.BlockHook(ctx, "t", "secret file "+f+" is open to others (mode 0644); chmod 600 it"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.hookCmd(), "list")
	if err != nil || !strings.Contains(out, "blocked: secret file "+f+" is open to others (mode 0644); chmod 600 it") {
		t.Fatalf("hook list: %q %v", out, err)
	}
	if _, err := run(t, a.hookCmd(), "add", "t", "--source", "token", "--secret-file", f); err != nil {
		t.Fatal(err)
	}
	if h, err := st.GetHook(ctx, "t"); err != nil || h.Blocked != "" {
		t.Fatalf("re-added hook %+v %v", h, err)
	}
	h, err := st.PutHook(ctx, store.Hook{Name: "u", Source: "token", SecretFile: f, Blocked: "forged"}, "me")
	if err != nil || h.Blocked != "" {
		t.Fatalf("PutHook kept a client's blocked state: %+v %v", h, err)
	}
}

// hook add takes the crew member to act as and the map; list and show print
// them, and what cannot be acted on is refused with nothing saved.
func TestHookAddMap(t *testing.T) {
	a, st, _ := stateYard(t, "")
	a.actor = "jo"
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "s.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(name, source string, more ...string) (string, error) {
		t.Helper()
		return run(t, a.hookCmd(), append([]string{"add", name, "--source", source, "--secret-file", f}, more...)...)
	}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a map without --as", []string{"--map", "task:new=started"}, "--map needs --as <crew member>"},
		{"an unknown word", []string{"--as", "yardmaster", "--map", "task:new=closed"}, `"closed" is no state: want started, review, done`},
		{"no word", []string{"--as", "yardmaster", "--map", "task:new"}, "want <bead type>:<stage>=<state>"},
		{"no stage", []string{"--as", "yardmaster", "--map", "task=started"}, "want <bead type>:<stage>=<state>"},
		{"no type", []string{"--as", "yardmaster", "--map", ":new=started"}, "want <bead type>:<stage>=<state>"},
		{"a bad stage", []string{"--as", "yardmaster", "--map", "task:In Review=review"}, "stage"},
		{"a stage mapped twice", []string{"--as", "yardmaster", "--map", "task:new=started", "--map", "task:new=done"}, "task:new is mapped to started already"},
		{"a crew member that does not exist", []string{"--as", "deputy", "--map", "task:new=started"}, "crew member deputy: not found"},
	} {
		_, err := add("linear", "linear", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// GitHub has no review status, and a token hook no issues.
	if _, err := add("gh", "github", "--as", "yardmaster", "--map", "task:review=review"); err == nil || !strings.Contains(err.Error(), "an issue has no review status") {
		t.Errorf("github review: %v", err)
	}
	if _, err := add("t", "token", "--as", "yardmaster", "--map", "task:new=started"); err == nil || !strings.Contains(err.Error(), "yardr cannot move an issue in token (it can in github, linear)") {
		t.Errorf("token: %v", err)
	}
	// A crew member with no identity in the hook's source.
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := add("linear", "linear", "--as", "scout", "--map", "task:new=started"); err == nil ||
		!strings.Contains(err.Error(), "yardr crew identity scout --source linear --id") {
		t.Errorf("no identity: %v", err)
	}
	// Acting as a crew member is speaking as it: its own session or the
	// operator, not a bead's agent.
	t.Setenv("YARDR_CREW", "")
	a.actor = "yard-10-new"
	if _, err := add("linear", "linear", "--as", "yardmaster", "--map", "task:new=started"); err == nil ||
		!strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster; this is yard-10-new") {
		t.Errorf("a bead's agent: %v", err)
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}

	a.actor = "jo"
	out, err := add("linear", "linear", "--as", "yardmaster", "--map", "task:new=started", "--map", "task:review=review",
		"--map", "task:merged=done", "--map", "train:open=started", "--map", "train:done=done")
	lines := "task:merged=done task:new=started task:review=review train:done=done train:open=started"
	if err != nil || !strings.Contains(out, "moves tracked linear issues as yardmaster: "+lines+"\n") ||
		!strings.Contains(out, "note: yardmaster may not set a status in linear yet, so nothing is moved: the operator allows it with yardr crew set yardmaster set_state=linear") {
		t.Fatalf("add: %q, %v", out, err)
	}
	h, err := st.GetHook(ctx, "linear")
	if err != nil || h.As != "yardmaster" || len(h.Map) != 5 || h.Map["task:merged"] != "done" || h.Map["train:open"] != "started" {
		t.Fatalf("stored %+v, %v", h, err)
	}
	if out, err = run(t, a.hookCmd(), "list"); err != nil || !strings.Contains(out, "MOVES ISSUES") || !strings.Contains(out, "as yardmaster: "+lines) {
		t.Fatalf("list: %q, %v", out, err)
	}

	// show <name>: the definition, and what the hook has still to do.
	nb := store.NewBead{ID: "yard-a", Title: "x", Stage: "new",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: "https://linear.app/acme/issue/YRD-1"}}}
	if _, err := st.CreateBead(ctx, nb, "jo"); err != nil {
		t.Fatal(err)
	}
	ms, err := st.IssueMoves(ctx)
	if err != nil || len(ms) != 1 {
		t.Fatalf("moves %+v, %v", ms, err)
	}
	if err := st.IssueMoveFailed(ctx, ms[0].ID, "linear: 503", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.hookCmd(), "show", "linear")
	for _, want := range []string{"linear  linear  POST /hooks/linear\n", "  secret file " + f + "\n", "  acts as yardmaster\n", "  map task:review=review\n",
		"  to do: yard-a (task in new) -> https://linear.app/acme/issue/YRD-1 started; tried 1 times, next at ", ": linear: 503\n"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("show: %q, %v; want %q", out, err, want)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("show prints the secret: %q", out)
	}
	if _, err = run(t, a.hookCmd(), "show", "nope"); err == nil || !strings.Contains(err.Error(), "hook nope: not found") {
		t.Fatalf("show of no hook: %v", err)
	}
	// add replaces the definition: without the flags the hook maps nothing.
	if out, err = add("linear", "linear"); err != nil || strings.Contains(out, "moves tracked") {
		t.Fatalf("replace: %q, %v", out, err)
	}
	if h, _ := st.GetHook(ctx, "linear"); h.As != "" || h.Map != nil {
		t.Fatalf("replaced %+v", h)
	}
}

// hook list and hook show say when each hook last accepted and last refused
// a delivery, and doctor says so of a hook whose last one was refused: its
// source reaches the yard and is turned away.
func TestHookListShowsLastAcceptedAndRefused(t *testing.T) {
	st, own := storetest.NewOwned(t)
	a := &app{st: st, actor: "me"}
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "t.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quiet", "broken", "mended"} {
		if _, err := run(t, a.hookCmd(), "add", name, "--source", "token", "--secret-file", f); err != nil {
			t.Fatal(err)
		}
	}
	refused := time.Now().Add(-time.Hour)
	for _, name := range []string{"broken", "mended"} {
		if err := own.RejectHook(ctx, name, "unauthorized", 401, 3, refused); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := own.RecordHook(ctx, "mended", map[string]any{"source": "token"}, []byte("ok"), nil); err != nil {
		t.Fatal(err)
	}
	mended, err := st.GetHook(ctx, "mended")
	if err != nil || mended.Accepted == nil {
		t.Fatalf("mended: %+v %v", mended, err)
	}
	out, err := run(t, a.hookCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string][]string{}
	for _, l := range strings.Split(out, "\n") {
		if fs := strings.Fields(l); len(fs) > 0 {
			lines[fs[0]] = fs
		}
	}
	at, on := strings.Fields(since(refused)), strings.Fields(since(*mended.Accepted))
	for name, want := range map[string][]string{
		"NAME":   {"NAME", "SOURCE", "PATH", "SECRET", "FILE", "STATE", "LAST", "ACCEPTED", "LAST", "REFUSED", "MOVES", "ISSUES"},
		"quiet":  {"quiet", "token", "/hooks/quiet", f, "ok", "-", "-", "-"},
		"broken": {"broken", "token", "/hooks/broken", f, "ok", "-", at[0], at[1], "unauthorized", "-"},
		"mended": {"mended", "token", "/hooks/mended", f, "ok", on[0], on[1], at[0], at[1], "unauthorized", "-"},
	} {
		if !slices.Equal(lines[name], want) {
			t.Fatalf("hook list, %s: %q, want %q\n%s", name, lines[name], want, out)
		}
	}
	out, err = run(t, a.hookCmd(), "show", "mended")
	if err != nil || !strings.Contains(out, "  last accepted a delivery "+since(*mended.Accepted)+"\n") ||
		!strings.Contains(out, "  last refused one "+since(refused)+": unauthorized\n") {
		t.Fatalf("hook show: %q %v", out, err)
	}

	parts, err := optionalParts(ctx, &config.Config{Home: t.TempDir()}, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, p := range parts {
		if p.Key == "hooks" {
			for _, n := range p.Notes {
				if strings.Contains(n, "refused") {
					notes = append(notes, n)
				}
			}
		}
	}
	want := "hook broken: its last delivery was refused (unauthorized, " + since(refused) + "); it has accepted none. Events of kind hook_rejected say how many"
	if len(notes) != 1 || notes[0] != want {
		t.Fatalf("doctor's hooks notes: %q\nwant only %q", notes, want)
	}
	// One refused after it last accepted says when it did.
	if err := own.RejectHook(ctx, "mended", "bad_request", 400, 1, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	h, _ := st.GetHook(ctx, "mended")
	if got := lastRefused(*h); !strings.Contains(got, "(bad_request, ") || !strings.Contains(got, "; it last accepted one "+since(*h.Accepted)+".") {
		t.Fatalf("lastRefused: %q", got)
	}
}
