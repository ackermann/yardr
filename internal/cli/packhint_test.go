package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A yard without a source the binary carries a pack for is told what
// brings it: every refusal names `yardr pack apply <name>`. A source no
// pack carries gets no such line, and neither does one the yard has.
func TestMissingSourceNamesItsPack(t *testing.T) {
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "operator", "me", "me"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_CREW", "yardmaster")
	secret := filepath.Join(t.TempDir(), "linear.secret")
	if err := os.WriteFile(secret, []byte("s3cret-checksource-passes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := hooks.Envelope{Source: "linear", Type: "issue",
		Thread: hooks.Thread{ID: "YRD-1", Title: "T", URL: "https://linear.app/acme/issue/YRD-1"}}
	seq, _, err := own.RecordHook(ctx, "lin", env.Map(), []byte("{}"), nil)
	if err != nil {
		t.Fatal(err)
	}

	const hint = "; yardr pack apply linear brings it"
	for _, c := range []struct {
		name string
		run  func() error
		want string
	}{
		{"hook add", func() error {
			_, err := run(t, a.hookCmd(), "add", "lin", "--source", "linear", "--secret-file", secret)
			return err
		}, `--source "linear": one of token, or a declared source with a hook key` + hint},
		{"subscribe", func() error {
			_, err := run(t, a.subscribeCmd(), "yardmaster", "linear")
			return err
		}, `--source "linear": one of token, or a declared source with a hook key` + hint},
		{"thread open", func() error {
			_, err := run(t, a.threadOpenCmd(), "linear", "--in", "ENG", "T")
			return err
		}, `yardr cannot open threads in linear (it can in none of the yard's sources)` + hint},
		{"thread state", func() error {
			if err := st.FollowThread(ctx, "yardmaster", "linear", "YRD-1", "me"); err != nil {
				t.Fatal(err)
			}
			_, err := run(t, a.threadStateCmd(), "YRD-1", "done")
			return err
		}, `yardr cannot set the status of a thread in linear (it can in none of the yard's sources)` + hint},
		{"reply", func() error {
			_, err := run(t, a.replyCmd(), strconv.FormatUint(seq, 10), "x")
			return err
		}, `which yardr cannot answer in (it can in none of the yard's sources)` + hint},
		{"link move", func() error {
			_, err := run(t, a.linkCmd(), "move", "--from-source", "linear", "--to-source", "github")
			return err
		}, `linear is no link source (links go to none of the yard's sources)` + hint},
		{"crew set", func() error {
			_, err := run(t, a.crewCmd(), "set", "yardmaster", "set_state=linear")
			return err
		}, `yardr cannot set the status of threads in "linear" (it can in none of the yard's sources)` + hint},
	} {
		if err := c.run(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// No pack carries signal: the refusal stands alone.
	if _, err := run(t, a.threadOpenCmd(), "signal", "--in", "ENG", "T"); err == nil || strings.Contains(err.Error(), "pack apply") {
		t.Errorf("signal: %v", err)
	}
	// The yard declares linear: the refusals go quiet.
	if _, err := st.PutSource(ctx, "linear.yaml", linearPackDecl, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.crewCmd(), "set", "yardmaster", "set_state=linear"); err != nil {
		t.Errorf("declared: %v", err)
	}
}
