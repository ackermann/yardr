package store

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// crewAddRoleText is a role of porter size: 50 lines, over two thousand
// bytes, which a card must carry whole.
func crewAddRoleText() string {
	var b strings.Builder
	b.WriteString("You are the porter.\n\n")
	for i := 0; i < 48; i++ {
		b.WriteString("Carry the bags carefully and note what each holds inside.\n")
	}
	return b.String()
}

func crewAddAsk(role string, argv ...string) Ask {
	return Ask{Question: "Add the porter?", To: "mckean", Context: []string{"the yard needs a porter"},
		Run: &AskRun{Argv: argv, Role: role}}
}

// A crew add ask from a crew pane shows a weighty code card with the
// kind, model, preset and whole role text, and approving it creates the
// member with exactly those bytes as its role file.
func TestRunAskCrewAddApproves(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	role := crewAddRoleText()
	dirInput := filepath.Join(y.Store.home, "workspace")
	if err := os.MkdirAll(filepath.Join(dirInput, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(dirInput)
	if err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(dir, "roles", "porter.md")
	if err := os.WriteFile(shadow, []byte("a different role\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rolePath := filepath.Join(y.Store.home, "roles", "porter.md")
	seq := y.ask(t, crewAddAsk(role, "crew", "add", "porter", "kind=codex", "model=opus", "preset=approve-for-me", "dir="+dirInput), "yardmaster")
	r := y.asked(t, seq)
	if r.Ask.Run == nil {
		t.Fatal("the ask holds no run")
	}
	if len(r.Ask.Weight) != 1 || r.Ask.Weight[0].Class != WeightCode {
		t.Fatalf("weight = %+v, want one code line", r.Ask.Weight)
	}
	joined := strings.Join(r.Ask.Run.Does, "\n")
	for _, want := range []string{"porter", "codex", "opus", "approve for me", dir, "It skips permissions: it runs without asking for permission."} {
		if !strings.Contains(joined, want) {
			t.Errorf("card does not say %q:\n%s", want, joined)
		}
	}
	if r.Ask.Run.Description != "Add a new crew member without replacing one" {
		t.Errorf("description = %q, want the add-only card description", r.Ask.Run.Description)
	}
	if !strings.Contains(joined, strings.TrimSuffix(role, "\n")) {
		t.Errorf("card does not show the role whole:\n%s", joined)
	}
	if got := r.Ask.Run.Seen["permission preset codex approve-for-me"]; got != seen("approve for me", []string{"--approve-for-me"}, "automatic review inside a workspace-write sandbox") {
		t.Errorf("card's preset fingerprint = %q, want it held to the preset's args and note", got)
	}
	if !strings.Contains(joined, `passes "--approve-for-me"`) {
		t.Errorf("card does not show the preset's exact arguments:\n%s", joined)
	}
	// A weighty card approved without its person seeing the weight runs
	// nothing.
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil); err == nil || !strings.Contains(err.Error(), "weighty") {
		t.Fatalf("unweighed approval = %v, want a weighty refusal", err)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done.Result, "crew member porter added") {
		t.Errorf("result = %q", done.Result)
	}
	m, err := y.GetCrew(y.ctx, "porter")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Enabled || m.Runner != "herdr" || m.Dir != dir {
		t.Errorf("member = %+v, want enabled herdr in %s", m, dir)
	}
	if m.Config["kind"] != "codex" || m.Config["model"] != "opus" || m.Config["prompt"] != rolePath {
		t.Errorf("config = %v", m.Config)
	}
	if !reflect.DeepEqual(m.Config["args"], []any{"--approve-for-me"}) {
		t.Errorf("args = %v, want the approve-for-me preset's", m.Config["args"])
	}
	got, err := os.ReadFile(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != role {
		t.Errorf("role file = %q, want exactly the carried bytes", got)
	}
	if got, err := os.ReadFile(shadow); err != nil || string(got) != "a different role\n" {
		t.Errorf("the member's working directory role changed: %q, %v", got, err)
	}
}

// Denying a crew add creates nothing: no member, no role file.
func TestRunAskCrewAddDeniedCreatesNothing(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	role := crewAddRoleText()
	seq := y.ask(t, crewAddAsk(role, "crew", "add", "porter", "kind=claude", "model=opus", "preset=auto", "dir="+y.Store.home), "yardmaster")
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "not now"}, "mckean")
	if _, err := y.GetCrew(y.ctx, "porter"); err == nil {
		t.Fatal("a declined crew add made the member")
	}
	if _, err := os.Stat(filepath.Join(y.Store.home, "roles", "porter.md")); !os.IsNotExist(err) {
		t.Fatalf("role file after a decline: %v", err)
	}
}

// The carried bytes are what runs, not a file read again at approval.
func TestRunAskCrewAddUsesCarriedBytes(t *testing.T) {
	y, _ := runYard(t)
	role := crewAddRoleText()
	seq := y.ask(t, crewAddAsk(role, "crew", "add", "porter", "kind=claude", "preset=auto", "dir="+y.Store.home), "yardmaster")
	if card := y.asked(t, seq).Ask.Run; card == nil || !strings.Contains(strings.Join(card.Does, "\n"), `passes "--permission-mode" "auto"`) {
		t.Errorf("auto preset card does not show its exact arguments: %+v", card)
	}
	// A file that arrives where the role will go is another's prompt as
	// far as the yard can tell: the approval refuses it and leaves it, and
	// the ask stays open for when it has gone.
	path := filepath.Join(y.Store.home, "roles", "porter.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed after the ask\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "never replaces") {
		t.Errorf("approval over a file that stands = %v, want it refused", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "changed after the ask\n" {
		t.Errorf("the refused approval changed the file: %q, %v", got, err)
	}
	if _, err := y.GetCrew(y.ctx, "porter"); err == nil {
		t.Error("a refused approval created its member")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != role {
		t.Errorf("role file = %q, want the carried bytes, not the changed file", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("role file mode = %v, %v, want 0600", info, err)
	}
	m, err := y.GetCrew(y.ctx, "porter")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Config["args"], []any{"--permission-mode", "auto"}) {
		t.Errorf("args = %v, want the auto preset's", m.Config["args"])
	}
	// A file symlink at the destination is neither replaced nor followed
	// into another file.
	role2 := "You are the clerk.\n\nFile what arrives.\n"
	seq2 := y.ask(t, crewAddAsk(role2, "crew", "add", "clerk", "kind=claude", "preset=auto", "dir="+y.Store.home), "yardmaster")
	path2 := filepath.Join(y.Store.home, "roles", "clerk.md")
	outside := filepath.Join(y.Store.home, "outside.md")
	if err := os.WriteFile(outside, []byte("keep this file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path2); err != nil {
		t.Fatal(err)
	}
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq2, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "never replaces") {
		t.Errorf("approval over a role symlink = %v, want it refused", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "keep this file\n" {
		t.Errorf("the target of the role symlink changed: %q, %v", got, err)
	}
	if got, err := os.Readlink(path2); err != nil || got != outside {
		t.Errorf("the role symlink changed: %q, %v", got, err)
	}
	// A preset changed after the ask makes its card stale, even if its
	// label and description still look the same.
	seq3 := y.ask(t, crewAddAsk(role2, "crew", "add", "scribe", "kind=claude", "preset=auto", "dir="+y.Store.home), "yardmaster")
	presets := HarnessPresets["claude"]
	changed := slices.Clone(presets)
	for i := range changed {
		if changed[i].Label == "auto" {
			changed[i].Args = []string{"--different-auto-mode"}
		}
	}
	HarnessPresets["claude"] = changed
	defer func() { HarnessPresets["claude"] = presets }()
	_, approveErr := y.ApproveAsk(y.ctx, y.bead, seq3, "mckean", true, nil)
	if approveErr == nil || !strings.Contains(approveErr.Error(), "from before that changed") {
		t.Errorf("approval after the preset changed = %v, want a stale-card refusal", approveErr)
	}
	if _, err := y.GetCrew(y.ctx, "scribe"); err == nil {
		t.Error("a stale preset card created its member")
	}
	HarnessPresets["claude"] = presets
	// A symlinked nested directory cannot redirect a role write outside
	// roles, and the failed write leaves no member behind.
	outsideDir := filepath.Join(y.Store.home, "outside-dir")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(y.Store.home, "roles", "alias")); err != nil {
		t.Fatal(err)
	}
	seq4 := y.ask(t, crewAddAsk(role2, "crew", "add", "alias/scribe", "kind=claude", "preset=auto", "dir="+y.Store.home), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, seq4, "mckean", true, nil); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("approval through a symlinked role directory = %v, want it refused", err)
	}
	if _, err := y.GetCrew(y.ctx, "alias/scribe"); err == nil {
		t.Error("a failed role write created its member")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "scribe.md")); !os.IsNotExist(err) {
		t.Errorf("role was written outside roles: %v", err)
	}
}

// Each key this row leaves out is refused, in words that say where it
// goes instead. Without its check each ask below would be stored.
func TestRunAskCrewAddRefusesLeftOutKeys(t *testing.T) {
	t.Parallel()
	role := crewAddRoleText()
	for _, c := range []struct {
		name string
		argv []string
		want string
	}{
		{"env_file", []string{"crew", "add", "porter", "kind=claude", "env_file=/tmp/x.env"}, "env is the operator's alone"},
		{"env_command", []string{"crew", "add", "porter", "kind=claude", "env_command=printenv"}, "env is the operator's alone"},
		{"free args", []string{"crew", "add", "porter", "kind=claude", "args=--verbose"}, "name the kind's permission preset"},
		{"open_threads grant", []string{"crew", "add", "porter", "kind=claude", "open_threads=crm"}, "proposed apart as"},
		{"set_state grant", []string{"crew", "add", "porter", "kind=claude", "set_state=crm"}, "proposed apart as"},
		{"peers grant", []string{"crew", "add", "porter", "kind=claude", "peers=airy"}, "proposed apart as"},
	} {
		t.Run(c.name, func(t *testing.T) {
			y, _ := runYard(t)
			ask := crewAddAsk(role, c.argv...)
			// The directory these argv name no dir: the home default
			// stands, so only the left-out key can refuse them.
			if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("AddAsk = %v, want a refusal saying %q", err, c.want)
			}
		})
	}
}

// A name in use, a kind the yard does not know, a preset of another
// kind, a directory outside the known places and a role past the bound
// are each refused.
func TestRunAskCrewAddRefusals(t *testing.T) {
	t.Parallel()
	role := crewAddRoleText()
	y, _ := runYard(t)
	home := y.Store.home
	sibling, err := os.MkdirTemp(filepath.Dir(home), "beside-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sibling) })
	// A group's prompt: no record names the file, only the group's config.
	builder := filepath.Join(home, "roles", "builder.md")
	if err := os.MkdirAll(filepath.Dir(builder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(builder, []byte("the builders' role\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want string
	}{
		{"name in use", crewAddAsk(role, "crew", "add", "yardmaster", "kind=claude", "dir="+home), "never replaces"},
		{"unknown kind", crewAddAsk(role, "crew", "add", "porter", "kind=nosuch", "dir="+home), "no harness kind the yard knows"},
		{"preset of another kind", crewAddAsk(role, "crew", "add", "porter", "kind=claude", "preset=yolo", "dir="+home), "has no permission preset"},
		{"noncanonical name path", crewAddAsk(role, "crew", "add", "porter/./copy", "kind=claude", "dir="+home), "unique path"},
		{"invalid role text", crewAddAsk(string([]byte{0xff}), "crew", "add", "porter", "kind=claude", "dir="+home), "not valid UTF-8"},
		{"dir outside", crewAddAsk(role, "crew", "add", "porter", "kind=claude", "dir=/etc"), "known places"},
		{"dir under the home's parent", crewAddAsk(role, "crew", "add", "porter", "kind=claude", "dir="+sibling), "known places"},
		{"model that reads as a flag", crewAddAsk(role, "crew", "add", "porter", "kind=claude", "model=--dangerously-skip-permissions", "dir="+home), "no model name"},
		{"escape in role", crewAddAsk("shown\x1b[8mhidden\n", "crew", "add", "porter", "kind=claude", "dir="+home), "control characters"},
		{"carriage return in role", crewAddAsk("shown\rover it\n", "crew", "add", "porter", "kind=claude", "dir="+home), "control characters"},
		{"bidi control in role", crewAddAsk("shown\u202Erevlated\n", "crew", "add", "porter", "kind=claude", "dir="+home), "control characters"},
		{"files mark in role", crewAddAsk(NoteFilesMark+" dropped on a card\n", "crew", "add", "porter", "kind=claude", "dir="+home), "files mark"},
		{"another's role file", crewAddAsk(role, "crew", "add", "builder", "kind=claude", "dir="+home), "never replaces"},
		{"no role", crewAddAsk("", "crew", "add", "porter", "kind=claude", "dir="+home), "carries no role text"},
		{"role past the bound", crewAddAsk(strings.Repeat("x", MaxRunCrewRole+1), "crew", "add", "porter", "kind=claude", "dir="+home), "yardr crew add"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, c.ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: AddAsk = %v, want a refusal saying %q", c.name, err, c.want)
		}
	}
	// The home's parent itself is where a yardmaster works.
	if _, err := y.AddAsk(y.ctx, y.bead, crewAddAsk(role, "crew", "add", "porter", "kind=claude", "dir="+filepath.Dir(home)), "yardmaster"); err != nil {
		t.Errorf("a crew member in the home's parent was refused: %v", err)
	}
	// Porter itself fits: its size in lines and bytes, asked whole.
	if _, err := y.AddAsk(y.ctx, y.bead, crewAddAsk(role, "crew", "add", "porter", "kind=claude", "preset=auto", "dir="+home), "yardmaster"); err != nil {
		t.Errorf("a porter-sized role was refused: %v", err)
	}
	alias := filepath.Join(home, "etc-link")
	if err := os.Symlink("/etc", alias); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, crewAddAsk(role, "crew", "add", "porter", "kind=claude", "dir="+alias), "yardmaster"); err == nil || !strings.Contains(err.Error(), "outside the yard's known places") {
		t.Errorf("AddAsk through a symlink to /etc = %v, want it refused", err)
	}
	if _, err := y.Store.PutCrew(y.ctx, CrewMember{Name: "PORTER", Runner: "herdr", Dir: home,
		Config: map[string]any{"kind": "claude", "prompt": "roles/PORTER.md"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, crewAddAsk(role, "crew", "add", "Porter", "kind=claude", "dir="+home), "yardmaster"); err == nil || !strings.Contains(err.Error(), "case-insensitive filesystem") {
		t.Errorf("AddAsk with a case-colliding role path = %v, want it refused", err)
	}
}
