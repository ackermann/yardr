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

// hook add takes a declared source with a hook key, github among them, and
// its help lists the secrets by source.
func TestHookAddGitHub(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	declareGitHubPack(t, st, "", "", "me")
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
	if err == nil || !strings.Contains(err.Error(), "one of token, or a declared source with a hook key") {
		t.Fatalf("unknown source err = %v", err)
	}
	for _, c := range a.hookCmd().Commands() {
		if c.Name() == "add" && !strings.Contains(c.Long, "GitHub's is the\nwebhook's secret") {
			t.Fatalf("hook add help does not work GitHub as the example:\n%s", c.Long)
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
	a, st, _, _ := stateYard(t, "", "")
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
		_, err := add("acme", "acme", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// GitHub has no review status, and a token hook no issues.
	if _, err := add("gh", "github", "--as", "yardmaster", "--map", "task:review=review"); err == nil || !strings.Contains(err.Error(), `"review" is no state of github`) {
		t.Errorf("github review: %v", err)
	}
	// A label is a word on GitHub only, and needs a name.
	if _, err := add("acme", "acme", "--as", "yardmaster", "--map", "task:review=label:in-review"); err == nil || !strings.Contains(err.Error(), "acme: yardr sets no label there") {
		t.Errorf("acme label: %v", err)
	}
	if _, err := add("gh", "github", "--as", "yardmaster", "--map", "task:review=label:"); err == nil || !strings.Contains(err.Error(), `"label:" is no state`) {
		t.Errorf("github label without a name: %v", err)
	}
	if _, err := add("t", "token", "--as", "yardmaster", "--map", "task:new=started"); err == nil || !strings.Contains(err.Error(), "yardr cannot move an issue in token (it can in acme, github)") {
		t.Errorf("token: %v", err)
	}
	// A crew member with no identity in the hook's source.
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scout", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := add("acme", "acme", "--as", "scout", "--map", "task:new=started"); err == nil ||
		!strings.Contains(err.Error(), "yardr crew identity scout --source acme --id") {
		t.Errorf("no identity: %v", err)
	}
	// Acting as a crew member is speaking as it: its own session or the
	// operator, not a bead's agent. The hook exists, as the operator
	// added it, so the agent's add is a map update, which still speaks
	// as the crew member.
	if _, err := add("acme", "acme"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_CREW", "")
	a.actor = "yard-10-new"
	if _, err := add("acme", "acme", "--as", "yardmaster", "--map", "task:new=started"); err == nil ||
		!strings.Contains(err.Error(), "only yardmaster's own session or the operator (jo) may speak as yardmaster; this is yard-10-new") {
		t.Errorf("a bead's agent: %v", err)
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 1 || hs[0].As != "" || len(hs[0].Map) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}

	a.actor = "jo"
	out, err := add("acme", "acme", "--as", "yardmaster", "--map", "task:new=started", "--map", "task:review=review",
		"--map", "task:merged=done", "--map", "train:open=started", "--map", "train:done=done")
	lines := "task:merged=done task:new=started task:review=review train:done=done train:open=started"
	if err != nil || !strings.Contains(out, "moves tracked acme issues as yardmaster: "+lines+"\n") ||
		!strings.Contains(out, "note: yardmaster may not set a status in acme yet, so nothing is moved: the operator allows it with yardr crew set yardmaster set_state=acme") {
		t.Fatalf("add: %q, %v", out, err)
	}
	h, err := st.GetHook(ctx, "acme")
	if err != nil || h.As != "yardmaster" || len(h.Map) != 5 || h.Map["task:merged"] != "done" || h.Map["train:open"] != "started" {
		t.Fatalf("stored %+v, %v", h, err)
	}
	if out, err = run(t, a.hookCmd(), "list"); err != nil || !strings.Contains(out, "MOVES ISSUES") || !strings.Contains(out, "as yardmaster: "+lines) {
		t.Fatalf("list: %q, %v", out, err)
	}
	// On GitHub a stage may add a label, whose name is kept as typed.
	out, err = add("gh", "github", "--as", "yardmaster", "--map", "task:new=started", "--map", "task:review=label:In Review", "--map", "task:merged=done")
	if err != nil || !strings.Contains(out, "task:merged=done task:new=started task:review=label:In Review") {
		t.Fatalf("add with a label: %q, %v", out, err)
	}
	if h, err := st.GetHook(ctx, "gh"); err != nil || h.Map["task:review"] != "label:In Review" {
		t.Fatalf("stored %+v, %v", h, err)
	}

	// show <name>: the definition, and what the hook has still to do.
	nb := store.NewBead{ID: "yard-a", Title: "x", Stage: "new",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "acme", URL: "https://acme.test/issue/ACME-1"}}}
	if _, err := st.CreateBead(ctx, nb, "jo"); err != nil {
		t.Fatal(err)
	}
	ms, err := st.IssueMoves(ctx)
	if err != nil || len(ms) != 1 {
		t.Fatalf("moves %+v, %v", ms, err)
	}
	if err := st.IssueMoveFailed(ctx, ms[0].ID, "acme: 503", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.hookCmd(), "show", "acme")
	for _, want := range []string{"acme  acme  POST /hooks/acme\n", "  secret file " + f + "\n", "  acts as yardmaster\n", "  map task:review=review\n",
		"  to do: yard-a (task in new) -> https://acme.test/issue/ACME-1 started; tried 1 times, next at ", ": acme: 503\n"} {
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
	if out, err = add("acme", "acme"); err != nil || strings.Contains(out, "moves tracked") {
		t.Fatalf("replace: %q, %v", out, err)
	}
	if h, _ := st.GetHook(ctx, "acme"); h.As != "" || h.Map != nil {
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

// hook add --says gives a hook its table of what the source's events say of
// a bead, in the order given. A line is refused as a pack's is: one that
// parses wrong, one that filters on a key the source has not, and one whose
// outcome no bead of the yard's depots could report, under the depot's own
// flow or one it has for a type. show prints the table.
func TestHookAddSays(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	declareGitHubPack(t, st, "", "", "me")
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "s.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(name, source string, more ...string) (string, error) {
		t.Helper()
		return run(t, a.hookCmd(), append([]string{"add", name, "--source", source, "--secret-file", f}, more...)...)
	}
	// A yard with no depot has no flow a train follows.
	if _, err := add("gh", "github", "--says", "event=pull_request -> merged"); err == nil || !strings.Contains(err.Error(), "no bead of the yard's depots could take it: no stage has an outcome merged") {
		t.Errorf("no depot: %v", err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"own":   "backlog -> new\nnew -> done on landed\ndone terminal\n",
		"train": "open -> review on done\nreview -> open-pr on done | open on changes | decide on question\nopen-pr -> land on done | open on changes | merged on merged\nland -> merged on done\ndecide -> open\nmerged terminal\n",
	} {
		fl, err := store.ParseFlow(text)
		if err != nil {
			t.Fatal(err)
		}
		fl.Name = name
		if _, err := st.PutFlow(ctx, *fl, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "own", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", store.TypeTrain, "train", "me"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no arrow", []string{"--says", "event=pull_request merged"}, "want <key>=<value> ... -> <outcome>"},
		{"a word that is no key=value", []string{"--says", "pull_request -> merged"}, `"pull_request": want key=value`},
		{"no filter", []string{"--says", " -> merged"}, "no filter"},
		{"no outcome", []string{"--says", "event=pull_request -> "}, "no stage has an outcome"},
		{"a key the source has not", []string{"--says", "kind=held -> merged"}, `cannot filter github on "kind"`},
		{"an outcome no flow has", []string{"--says", "event=pull_request -> shipped"}, "no stage has an outcome shipped"},
		{"an outcome the stage has not", []string{"--says", "event=pull_request stage=review -> merged"}, "stage review has no outcome merged (it has: done, changes, question)"},
		{"a stage no flow has", []string{"--says", "event=ci stage=in-pr -> done"}, "no stage in-pr"},
	} {
		_, err := add("h", "github", c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}

	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIdentity(ctx, "yardmaster", "github", store.Identity{ID: "u-yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := add("gh", "github", "--as", "yardmaster",
		"--says", "event=pull_request action=closed merged=true -> merged: PR merged: by hand",
		"--says", "event=workflow_run event=check_suite conclusion=failure stage=review -> changes",
		"--says", "event=ci conclusion=success stage=open-pr -> done",
		// What only the depot's own flow has, for a bead that tracks the
		// event's thread, filtered on whom GitHub says wrote it.
		"--says", "event=issues action=labeled label=accepted association=member stage=new -> landed")
	lines := []string{
		"action=closed event=pull_request merged=true -> merged: PR merged: by hand",
		"conclusion=failure event=check_suite event=workflow_run stage=review -> changes",
		"conclusion=success event=ci stage=open-pr -> done",
		"action=labeled association=member event=issues label=accepted stage=new -> landed",
	}
	if err != nil || !strings.Contains(out, "its events say of a bead:\n  "+strings.Join(lines, "\n  ")+"\n") {
		t.Fatalf("add: %q, %v", out, err)
	}
	h, err := st.GetHook(ctx, "gh")
	if err != nil || !slices.Equal(store.SayLines(h.Says), lines) || h.Says[0].Note != "PR merged: by hand" {
		t.Fatalf("stored %+v, %v", h, err)
	}
	if out, err = run(t, a.hookCmd(), "show", "gh"); err != nil || !strings.Contains(out, "  says "+lines[0]+"\n") || !strings.Contains(out, "  says "+lines[2]+"\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
	// add replaces the definition: without the flag the hook has no table.
	if _, err = add("gh", "github"); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.GetHook(ctx, "gh"); h.Says != nil {
		t.Fatalf("replaced %+v", h)
	}
}

// --makes sets the hook's table of what its events make, in the order
// given. A line is refused when the type's flow in the depot it names has
// no such stage a bead can be made in, and one that names no depot when no
// depot of the yard could take it. show prints the table.
func TestHookAddMakes(t *testing.T) {
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	declareGitHubPack(t, st, "", "", "me")
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "s.secret")
	if err := os.WriteFile(f, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := func(more ...string) (string, error) {
		t.Helper()
		return run(t, a.hookCmd(), append([]string{"add", "gh", "--source", "github", "--secret-file", f}, more...)...)
	}
	if _, err := add("--makes", "event=issues -> issue:triage"); err == nil || !strings.Contains(err.Error(), "the yard has no depot to make a bead in") {
		t.Errorf("no depot: %v", err)
	}
	for _, depot := range []string{"web", "api"} {
		if err := st.PutDepot(ctx, store.Depot{Name: depot, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		"own":    "backlog -> new\nnew -> done on landed\ndone terminal\n",
		"issues": "triage -> backlog on accepted | closed on dropped\nbacklog -> closed\nclosed terminal\n",
	} {
		fl, err := store.ParseFlow(text)
		if err != nil {
			t.Fatal(err)
		}
		fl.Name = name
		if _, err := st.PutFlow(ctx, *fl, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, depot := range []string{"web", "api"} {
		if err := st.SetFlowPointer(ctx, depot, "", "own", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetFlowPointer(ctx, "web", "issue", "issues", "me"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, line, want string
	}{
		{"no arrow", "event=issues issue:triage", "want <key>=<value> ... -> <type>:<stage>[@<depot>]"},
		{"no stage", "event=issues -> issue", "want <key>=<value> ... -> <type>:<stage>[@<depot>]"},
		{"no filter", " -> issue:triage", "no filter"},
		{"the bead's stage", "event=issues stage=new -> issue:triage", `cannot filter github on "stage"`},
		{"a stage the type's flow has not there", "event=issues -> issue:triage@api", "depot api: no stage triage a bead can be made in (stages: backlog, new)"},
		{"a stage that ends the flow", "event=issues -> issue:closed@web", "depot web: no stage closed a bead can be made in (stages: triage, backlog)"},
		{"a stage no depot's flow has", "event=issues -> task:triage", "depot api: no stage triage a bead can be made in (stages: backlog, new); depot web: no stage triage"},
		{"a depot that is none", "event=issues -> issue:triage@nowhere", "depot nowhere"},
	} {
		if _, err := add("--makes", c.line); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}

	out, err := add(
		// Without a depot the line passes when one depot could take it.
		"--makes", "event=issues action=opened action=reopened -> issue:triage",
		"--makes", "event=pull_request action=opened association=none -> task:backlog@api")
	lines := []string{
		"action=opened action=reopened event=issues -> issue:triage",
		"action=opened association=none event=pull_request -> task:backlog@api",
	}
	if err != nil || !strings.Contains(out, "its events make a bead for a thread none tracks:\n  "+strings.Join(lines, "\n  ")+"\n") {
		t.Fatalf("add: %q, %v", out, err)
	}
	h, err := st.GetHook(ctx, "gh")
	if err != nil || !slices.Equal(store.MakeLines(h.Makes), lines) {
		t.Fatalf("stored %+v, %v", h, err)
	}
	if out, err = run(t, a.hookCmd(), "show", "gh"); err != nil || !strings.Contains(out, "  makes "+lines[0]+"\n") || !strings.Contains(out, "  makes "+lines[1]+"\n") {
		t.Fatalf("show: %q, %v", out, err)
	}
	// add replaces the definition: without the flag the hook makes nothing.
	if _, err = add(); err != nil {
		t.Fatal(err)
	}
	if h, _ := st.GetHook(ctx, "gh"); h.Makes != nil {
		t.Fatalf("replaced %+v", h)
	}
}
