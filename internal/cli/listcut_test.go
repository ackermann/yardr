package cli

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// rows runs a listing and returns how many rows its table has, and what it
// said on its error stream.
func rows(t *testing.T, st store.API, args ...string) (int, string) {
	t.Helper()
	var said bytes.Buffer
	out, err := run(t, (&app{st: st, stderr: &said}).rootCmd(), args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return len(strings.Split(strings.TrimSpace(out), "\n")) - 1, said.String()
}

// bead list -a is the newest 200 beads, -n another number and --all every
// one; without them the open beads are listed as before (yardr-wav4). The
// cut is the same at a lower limit, which needs fewer beads to try it.
func TestBeadListWithClosedIsCut(t *testing.T) {
	stub(t, &listLimit, 10)
	st := storetest.New(t)
	ctx := context.Background()
	total, open := listLimit+5, 3
	for i := 0; i < total; i++ {
		b, err := st.CreateBead(ctx, store.NewBead{Title: "bead " + strconv.Itoa(i)}, "me")
		if err != nil {
			t.Fatal(err)
		}
		if i < open {
			continue
		}
		if _, err := st.CloseBead(ctx, b.ID, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	const note = "there may be older ones"
	for _, c := range []struct {
		args string
		want int
		cut  bool
	}{
		{"", open, false},
		{"-a", listLimit, true},
		{"--closed", listLimit, true},
		{"--all", total, false},
		{"-a --all", total, false},
		{"-a -n 7", 7, true},
		{"-n 2", 2, true},
		{"-a -n 1000", total, false},
		{"-a --since 1h", listLimit, true},
		{"--all --since 1h", total, false},
	} {
		n, said := rows(t, st, append([]string{"bead", "list"}, strings.Fields(c.args)...)...)
		if n != c.want || strings.Contains(said, note) != c.cut {
			t.Errorf("bead list %s: %d beads, stderr %q; want %d, and the note that it is cut: %v", c.args, n, said, c.want, c.cut)
		}
	}
	for _, bad := range []string{"--all -n 5", "-a -n 0", "-a --since soon"} {
		if out, err := run(t, (&app{st: st}).rootCmd(), append([]string{"bead", "list"}, strings.Fields(bad)...)...); err == nil {
			t.Errorf("bead list %s was accepted:\n%s", bad, out)
		}
	}
}

// session list -a is the newest ended sessions with the live ones, cut as
// bead list -a is, and --bead keeps one bead's (yardr-wav4).
func TestSessionListWithEndedIsCut(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	var beads []string
	for i := 0; i < 2; i++ {
		b, err := st.CreateBead(ctx, store.NewBead{Title: "bead " + strconv.Itoa(i), Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		beads = append(beads, b.ID)
	}
	// The first bead is worked five times, four of them dead; the second
	// once, and that session lives.
	for i := 0; i < 5; i++ {
		sess, err := st.Claim(ctx, beads[0], "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			if err := st.EndSession(ctx, sess.ID, "died", "gone"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.Claim(ctx, beads[1], "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args string
		want int
		cut  bool
	}{
		{"", 2, false},
		{"-a", 6, false},
		{"--ended", 6, false},
		{"--all", 6, false},
		{"-a -n 3", 3, true},
		{"--bead " + beads[0], 1, false},
		{"-a --bead " + beads[0], 5, false},
		{"-a --bead " + beads[1], 1, false},
		{"-a -n 2 --bead " + beads[0], 2, true},
		{"--all --since 1h", 6, false},
	} {
		n, said := rows(t, st, append([]string{"session", "list"}, strings.Fields(c.args)...)...)
		if n != c.want || strings.Contains(said, "there may be older ones") != c.cut {
			t.Errorf("session list %s: %d sessions, stderr %q; want %d, and the note that it is cut: %v", c.args, n, said, c.want, c.cut)
		}
	}
	for _, bad := range []string{"--all -n 5", "-a --bead nope-1"} {
		if out, err := run(t, (&app{st: st}).rootCmd(), append([]string{"session", "list"}, strings.Fields(bad)...)...); err == nil {
			t.Errorf("session list %s was accepted:\n%s", bad, out)
		}
	}
}
