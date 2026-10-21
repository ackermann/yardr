package isolatedyard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// drive runs yardr in the yard as a program that leads a person through
// its first run does (footplate): stdin closed, stdout a pipe, so no
// command finds a terminal to ask in, and --json on every one. It returns
// the exit status and what was printed on stdout, which must be one JSON
// value and is read into v. Nothing else is looked at: no line of prose,
// nothing of stderr.
func drive(t *testing.T, d string, v any, args ...string) int {
	t.Helper()
	return driveVia(t, []string{filepath.Join(d, "e")}, v, args...)
}

// driveVia is drive through the command via, which is D/e alone for most
// yards. A yard whose commands need more of the environment than D/e gives
// (the server yard's service managers read their state from it) passes
// D/e followed by `env NAME=value ...`.
func driveVia(t *testing.T, via []string, v any, args ...string) int {
	t.Helper()
	cmd := exec.Command(via[0], append(append(slices.Clone(via[1:]), "yardr", "--json"), args...)...)
	cmd.Stdin = nil // the null device: nothing to read, and no terminal
	var out bytes.Buffer
	cmd.Stdout = &out
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("yardr %s: %v", strings.Join(args, " "), err)
		}
		code = exit.ExitCode()
	}
	dec := json.NewDecoder(&out)
	if err := dec.Decode(v); err != nil {
		t.Fatalf("yardr %s (exit %d) printed no JSON: %v\n%s", strings.Join(args, " "), code, err, out.String())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			t.Fatalf("yardr %s printed more than one JSON value", strings.Join(args, " "))
		}
		t.Fatalf("yardr %s printed trailing output after JSON: %v", strings.Join(args, " "), err)
	}
	return code
}

// refusal is what a command that did not go through prints.
type refusal struct {
	Error   string   `json:"error"`
	Missing []string `json:"missing"`
}

// nextStep is the key of the first setup step not done, from yardr setup
// --json, and the depot it is about.
func nextStep(t *testing.T, d string, as ...string) (key, depot string) {
	t.Helper()
	var s struct {
		Home  string `json:"home"`
		Steps []struct {
			Key  string `json:"key"`
			Done bool   `json:"done"`
		} `json:"steps"`
		Next *struct {
			Key   string `json:"key"`
			Depot string `json:"depot"`
		} `json:"next"`
	}
	if code := drive(t, d, &s, append(as, "setup")...); code != 0 || s.Next == nil || len(s.Steps) == 0 {
		t.Fatalf("setup --json: exit %d, %+v", code, s)
	}
	return s.Next.Key, s.Next.Depot
}

// The whole first run of a yard, as docs/setup.md ("For a program that
// drives the first run") names it, with stdin closed and every step read
// from JSON and the exit status alone: create the yard, add a depot, set it
// up, create and advance a first bead, subscribe the operator. A bare yard:
// none of it starts an agent, and creating needs no herdr.
func TestFirstRunWithStdinClosed(t *testing.T) {
	d := bare(t)
	home := filepath.Join(d, "y")

	// No yard yet: doctor says so and creates none, and every command that
	// needs one is refused with exit 2.
	var doc struct {
		Home    string `json:"home"`
		Yard    bool   `json:"yard"`
		Machine []struct {
			Program string `json:"program"`
			Found   bool   `json:"found"`
			Harness string `json:"harness"`
		} `json:"machine"`
	}
	drive(t, d, &doc, "doctor")
	harness := ""
	for _, m := range doc.Machine {
		if m.Harness != "" && m.Found {
			harness = m.Harness
		}
	}
	if doc.Home != home || doc.Yard || harness != "claude" {
		t.Fatalf("doctor on no yard: %+v (harness found: %q)", doc, harness)
	}
	var no refusal
	if code := drive(t, d, &no, "setup"); code != 2 || no.Error == "" {
		t.Fatalf("setup on no yard: exit %d, %+v; want 2 and an error", code, no)
	}

	// The yard. The person's name is not this login's, so every command
	// after is run as them (--actor): the operator is a user, and a user is
	// who commands act as.
	const operator = "jo"
	as := []string{"--actor", operator}
	type initOut struct {
		Home           string   `json:"home"`
		Harness        string   `json:"harness"`
		Yardmaster     string   `json:"yardmaster"`
		Operator       string   `json:"operator"`
		Style          string   `json:"operator_style"`
		YardmasterKept bool     `json:"yardmaster_kept"`
		SettingsKept   []string `json:"settings_kept"`
		Start          any      `json:"start"`
	}
	no = refusal{}
	if code := drive(t, d, &no, append(as, "init")...); code != 1 || no.Error == "" {
		t.Fatalf("init with no harness: exit %d, %+v; want 1 and an error", code, no)
	}
	var yard initOut
	initArgs := append(as, "init", "--harness", harness, "--operator", operator, "--style", "guide", "--about", "teaches, marks essays")
	if code := drive(t, d, &yard, initArgs...); code != 0 {
		t.Fatalf("init: exit %d", code)
	}
	if yard.Home != home || yard.Harness != harness || yard.Operator != operator || yard.Yardmaster != "yardmaster" ||
		yard.Style != "guide" || yard.YardmasterKept || yard.SettingsKept == nil || len(yard.SettingsKept) != 0 || yard.Start != nil {
		t.Fatalf("init = %+v", yard)
	}
	// Again, as a second click does: nothing is made twice.
	var again initOut
	if code := drive(t, d, &again, initArgs...); code != 0 || !again.YardmasterKept || again.Operator != operator {
		t.Fatalf("init again: exit %d, %+v", code, again)
	}
	if key, _ := nextStep(t, d, as...); key != "depot" {
		t.Fatalf("after init the next step is %q, want depot", key)
	}

	// The depot: a repository with one commit on main.
	repo := filepath.Join(d, "h", "web")
	in(t, d, "git", "init", "-q", "-b", "main", repo)
	in(t, d, "git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "first")
	type depot struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Kind string `json:"kind"`
		Base string `json:"base"`
	}
	no = refusal{}
	if code := drive(t, d, &no, append(as, "depot", "add", "web", repo, "--base", "nowhere")...); code != 1 || no.Error == "" {
		t.Fatalf("depot add on a base that is not there: exit %d, %+v; want 1 and an error", code, no)
	}
	var dep depot
	if code := drive(t, d, &dep, append(as, "depot", "add", "web", repo, "--base", "main")...); code != 0 ||
		dep != (depot{"web", repo, "git", "main"}) {
		t.Fatalf("depot add: exit %d, %+v", code, dep)
	}
	if key, of := nextStep(t, d, as...); key != "depot_files" || of != "web" {
		t.Fatalf("after depot add the next step is %q of %q, want depot_files of web", key, of)
	}

	// Its setup. Asked for nothing but --yes, the import says what it
	// would have asked, as flags, and exits 2; a depot that is not there is
	// 3. Each flag is "--<flag> <name>=<what>": the name is kept and the
	// answer put after the =, a harness for --bind, users for --members.
	no = refusal{}
	if code := drive(t, d, &no, append(as, "depot", "init", "nope", "--yes")...); code != 3 || no.Error == "" {
		t.Fatalf("depot init of no depot: exit %d, %+v; want 3 and an error", code, no)
	}
	no = refusal{}
	if code := drive(t, d, &no, append(as, "depot", "init", "web", "--yes")...); code != 2 || no.Error == "" {
		t.Fatalf("depot init with answers open: exit %d, %+v; want 2 and an error", code, no)
	}
	want := []string{"--bind builders=<kind>[:<model>]", "--bind reviewers=<kind>[:<model>]",
		"--members backlog=<users>", "--members decisions=<users>"}
	if !slices.Equal(no.Missing, want) {
		t.Fatalf("missing = %q, want %q", no.Missing, want)
	}
	importArgs := append(as, "depot", "init", "web")
	for _, m := range no.Missing {
		flag, rest, _ := strings.Cut(m, " ")
		name, _, ok := strings.Cut(rest, "=")
		if !ok {
			t.Fatalf("missing %q is not <flag> <name>=<what>", m)
		}
		answer := ""
		switch {
		case flag == "--bind":
			answer = yard.Harness
		case flag == "--members" && name == "decisions":
			answer = operator
		}
		importArgs = append(importArgs, flag, name+"="+answer)
	}
	// Every answer and no --yes: the confirmation is left, and that is 2 too.
	no = refusal{}
	if code := drive(t, d, &no, importArgs...); code != 2 || no.Error == "" || no.Missing != nil {
		t.Fatalf("depot init without --yes: exit %d, %+v; want 2 and an error", code, no)
	}
	var imp struct {
		Result struct {
			Pack    string `json:"pack"`
			Depot   string `json:"depot"`
			Created int    `json:"created"`
		} `json:"result"`
	}
	if code := drive(t, d, &imp, append(importArgs, "--yes")...); code != 0 ||
		imp.Result.Pack != "starter" || imp.Result.Depot != "web" || imp.Result.Created == 0 {
		t.Fatalf("depot init: exit %d, %+v", code, imp)
	}
	imp.Result.Created = -1
	if code := drive(t, d, &imp, append(importArgs, "--yes")...); code != 0 || imp.Result.Created != 0 {
		t.Fatalf("depot init again: exit %d, created %d; want nothing made twice", code, imp.Result.Created)
	}
	if key, of := nextStep(t, d, as...); key != "first_bead" || of != "web" {
		t.Fatalf("after depot init the next step is %q of %q, want first_bead of web", key, of)
	}

	// The first bead, and into the line.
	type bead struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Stage string `json:"stage"`
		Depot string `json:"depot"`
	}
	var b bead
	if code := drive(t, d, &b, append(as, "bead", "create", "A first essay", "--depot", "web", "-b", "Done when it reads well.")...); code != 0 ||
		b.ID == "" || b.Title != "A first essay" || b.Stage != "backlog" || b.Depot != "web" {
		t.Fatalf("bead create: exit %d, %+v", code, b)
	}
	var moved bead
	if code := drive(t, d, &moved, append(as, "bead", "advance", b.ID, "--to", "new")...); code != 0 || moved.ID != b.ID || moved.Stage != "new" {
		t.Fatalf("bead advance: exit %d, %+v", code, moved)
	}
	no = refusal{}
	if code := drive(t, d, &no, append(as, "bead", "advance", "web-none", "--to", "new")...); code != 3 || no.Error == "" {
		t.Fatalf("advance of no bead: exit %d, %+v; want 3 and an error", code, no)
	}
	if key, _ := nextStep(t, d, as...); key != "merged_bead" {
		t.Fatalf("after the first bead the next step is %q, want merged_bead (the line's)", key)
	}

	// Being told when beads land: the operator's subscriptions come back.
	type sub struct {
		Source string            `json:"source"`
		Where  map[string]string `json:"where"`
	}
	for range 2 { // a second time changes nothing
		var subs []sub
		if code := drive(t, d, &subs, append(as, "subscribe", operator, "yardr", "--done", "--where", "depot=web")...); code != 0 ||
			len(subs) != 2 ||
			!slices.ContainsFunc(subs, func(s sub) bool {
				return s.Source == "yardr" && s.Where["depot"] == "web" && s.Where["state"] == "done"
			}) ||
			!slices.ContainsFunc(subs, func(s sub) bool {
				return s.Source == "yardr" && s.Where["kind"] == "install_failed\x00rolled_back"
			}) {
			t.Fatalf("subscribe: exit %d, %+v", code, subs)
		}
	}
	// As this login, who is no user of the yard, it is refused: 1.
	no = refusal{}
	if code := drive(t, d, &no, "--actor", "someone-else", "subscribe", operator, "yardr", "--done", "--where", "depot=web"); code != 1 || no.Error == "" {
		t.Fatalf("subscribe for another: exit %d, %+v; want 1 and an error", code, no)
	}
}
