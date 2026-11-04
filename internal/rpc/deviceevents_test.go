package rpc

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A kind of event that has no row in the store's table of what a person who
// is not the operator is told (store.EventDataToldKinds) tells that person no
// data, which is safe and silent: its author never decided what it tells. So
// every kind the code writes must have a row, and this test is where one that
// has none is found. It reads the source of internal/ for the writers of
// events (the store's event, LogEvent and LogDispatch), takes their kinds from
// the strings and the constants they are given, and adds what no scan of the
// source can read: the kinds a variable carries (variableSites, by hand, and
// counted so that a new site fails until it is listed) and the kinds
// LogDispatch accepts (store.DispatchKinds).
//
// The gate runs this test on any change under internal/: the reads table of
// .yardr/test has the row (internal/ -> internal/rpc). It must, since rpc
// imports only the store: a change to a writer in another package (dispatch,
// server, cli) selects that package and its importers, none of which is rpc,
// and the kinds it writes would land with no row decided.
func TestEveryKindIsDecided(t *testing.T) {
	written, variable := writtenKinds(t)
	for _, kind := range store.DispatchKinds {
		written[kind] = append(written[kind], "store.DispatchKinds")
	}
	for file, site := range variableSites {
		for _, kind := range site.kinds {
			written[kind] = append(written[kind], file+" (variableSites)")
		}
	}
	for kind, where := range written {
		if _, ok := store.EventDataTold(kind); !ok {
			t.Errorf("the kind %q is written (%s) and has no row in eventDataTold (internal/store/eventtold.go): list the keys of its data that a person who is not the operator is told, or none", kind, strings.Join(where, ", "))
		}
	}
	// A row for a kind nothing writes says something that is not so.
	for _, kind := range store.EventDataToldKinds() {
		if _, ok := written[kind]; !ok {
			t.Errorf("eventDataTold has a row for %q, which nothing writes: remove it", kind)
		}
	}
	// A new site of a kind that is a variable is not found by the scan, so it
	// is counted: its kinds are listed in variableSites and decided with the
	// others.
	for file, n := range variable {
		if got := variableSites[file].sites; got != n {
			t.Errorf("%s writes %d events with a kind that is a variable, and variableSites lists %d: list the kinds each writes there", file, n, got)
		}
	}
	for file, site := range variableSites {
		if variable[file] == 0 {
			t.Errorf("variableSites lists %d sites in %s, which has none now: remove the entry", site.sites, file)
		}
	}
}

// variableSites are the call sites that write an event whose kind is a
// variable, by file: how many sites it has, and the kinds they write between
// them. A kind that comes in through a variable is read from the code by hand,
// and this is where it is written down. The writers of LogEvent and
// LogDispatch take the kind of their callers, which the scan reads as it reads
// every other call.
var variableSites = map[string]struct {
	sites int
	kinds []string
}{
	"internal/cli/upgrade_flow.go":  {2, []string{"install_failed", "rolled_back"}},
	"internal/dispatch/dispatch.go": {1, []string{"backoff", "unrouted", "no_runner", "capped", "bad_depot", "bad_flow", "bad_train", "wagon_without_train"}},
	"internal/dispatch/limits.go":   {1, []string{"session_idle", "session_aged"}},
	"internal/dispatch/owninput.go": {1, []string{"session_prompt_gave_up", "session_prompt_typed"}},
	"internal/server/edge.go":       {1, []string{"edge_link_up", "edge_link_down"}},
	"internal/store/beads.go": {2, []string{"unblocked", "reblocked", "updated", "originator_changed", "advanced", "closed", "reopened",
		"held", "unheld", "released"}},
	"internal/store/crew.go":        {1, []string{"crew_enabled", "crew_disabled"}},
	"internal/store/crewsession.go": {1, []string{"crew_start_failed", "crew_gave_up"}},
	"internal/store/events.go":      {2, nil},
	"internal/store/flows.go":       {1, []string{"flow_pointer_set", "flow_pointer_cleared"}},
	"internal/store/graph.go":       {1, []string{"noted", "journal", "handoff"}},
	"internal/store/peersend.go": {2, []string{"peer_bead_returned", "peer_bead_refused", "peer_bead_undelivered",
		"peer_bead_fetched", "peer_bead_not_fetched"}},
	"internal/store/peertrust.go":     {1, []string{"peer_trust_dropped", "peer_reach_dropped"}},
	"internal/store/subscriptions.go": {5, []string{"subscribed", "unsubscribed", "identity_set", "identity_removed"}},
	"internal/store/threads.go":       {1, []string{"thread_opened", "replied"}},
	"internal/store/previews.go": {1, []string{"preview_started", "preview_stopped", "preview_refused",
		"preview_failed", "preview_crashed", "preview_exited", "preview_addressed"}},
}

// prefixedKinds are the kinds a call writes as a prefix and a variable: the
// states a session ends in (store.EndSession takes no others).
var prefixedKinds = map[string][]string{"session_": {"died", "failed", "aborted"}}

// writerKind is, for the call of each writer of events, the place of its kind
// among its arguments.
var writerKind = map[string]int{"event": 1, "LogEvent": 2, "LogDispatch": 2}

// writtenKinds are the kinds the Go files of internal/ write as events, each
// with the places it is written at, and how many writes of each file take
// their kind from a variable.
func writtenKinds(t *testing.T) (written map[string][]string, variable map[string]int) {
	t.Helper()
	written, variable = map[string][]string{}, map[string]int{}
	var files []string
	constants := map[string]string{} // a name a kind is given by, to its value
	// A constant of a package: one const line, or a line of a const block. A
	// name a function assigns is no constant, whatever it is called.
	single := regexp.MustCompile(`^const\s+([A-Za-z_]\w*)\s*(?:string\s*)?=\s*"([a-z][a-z_]*)"`)
	inBlock := regexp.MustCompile(`^\t([A-Za-z_]\w*)\s*(?:string\s*)?=\s*"([a-z][a-z_]*)"`)
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		block := false
		for _, line := range strings.Split(string(src), "\n") {
			switch {
			case line == "const (":
				block = true
			case block && line == ")":
				block = false
			case block:
				if m := inBlock.FindStringSubmatch(line); m != nil {
					constants[m[1]] = m[2]
				}
			default:
				if m := single.FindStringSubmatch(line); m != nil {
					constants[m[1]] = m[2]
				}
			}
		}
		if bytes.Contains(src, []byte(".event(")) || bytes.Contains(src, []byte("LogEvent(")) || bytes.Contains(src, []byte("LogDispatch(")) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		file := "internal/" + filepath.ToSlash(strings.TrimPrefix(path, "../"))
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			at, ok := writerKind[sel.Sel.Name]
			if !ok || len(call.Args) <= at {
				return true
			}
			here := file + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			name := ""
			switch kind := call.Args[at].(type) {
			case *ast.BasicLit:
				if s, err := strconv.Unquote(kind.Value); err == nil && kind.Kind == token.STRING {
					written[s] = append(written[s], here)
					return true
				}
			case *ast.Ident:
				name = kind.Name
			case *ast.SelectorExpr:
				name = kind.Sel.Name
			case *ast.BinaryExpr:
				if lit, ok := kind.X.(*ast.BasicLit); ok && kind.Op == token.ADD {
					if prefix, err := strconv.Unquote(lit.Value); err == nil && prefixedKinds[prefix] != nil {
						for _, rest := range prefixedKinds[prefix] {
							written[prefix+rest] = append(written[prefix+rest], here)
						}
						return true
					}
				}
			}
			if value, ok := constants[name]; ok && name != "" {
				written[value] = append(written[value], here)
				return true
			}
			variable[file]++
			return true
		})
	}
	if len(written) < 100 {
		t.Fatalf("%d kinds are written in %d files of internal/: the scan has lost the writers", len(written), len(files))
	}
	return written, variable
}

// The data the stream promises (docs/api.md, "The stream") is in the row of
// its kind, whoever the device is: what a client acts on is not held back. The
// documents are the test's own (apiEvents), so a field the page adds is held
// to the table as it is added, and a row that drops one fails here.
func TestTheTableTellsWhatTheStreamPromises(t *testing.T) {
	for _, ev := range apiEvents {
		kind := ""
		for _, f := range ev.fields {
			if f.path == "kind" {
				kind = strings.Trim(f.says, "`")
			}
		}
		if kind == "" {
			continue // an event of any kind (data.notified), which every kind tells
		}
		keys, ok := store.EventDataTold(kind)
		if !ok {
			t.Errorf("%s: the page says the stream carries it, and it has no row", ev.title)
			continue
		}
		if slices.Contains(keys, "*") {
			continue
		}
		for _, f := range ev.fields {
			key, ok := strings.CutPrefix(f.path, "data.")
			if !ok {
				continue
			}
			key, _, _ = strings.Cut(strings.Split(key, "[")[0], ".")
			if !slices.Contains(keys, key) && key != "via" {
				t.Errorf("%s: the page promises data.%s, and the row of %s does not tell it", ev.title, key, kind)
			}
		}
	}
	// Every event may say whom it became a notification for, and which device
	// a call was written through.
	probe := store.Event{Kind: "a_kind_added_later", Data: map[string]any{"notified": map[string]any{"jo": "all"}, "via": "phone", "dir": "/home/jo"}}
	if held, _ := probe.WithoutHeld(); len(held.Data) != 2 || held.Data["notified"] == nil || held.Data["via"] != "phone" {
		t.Errorf("an event of a kind that has no row tells %v, want notified and via", held.Data)
	}
}

// Every kind the approval paragraph names tells approved. The paragraph is
// hand-written prose (docs/api.md, what an approval through a device
// writes), not one of the generated event tables above, so it is parsed
// where it stands: the parenthesised list before the words that say the
// change's event carries approved with the bead and the ask. One direction
// only: pack_applied and pack_unchanged tell approved without standing in
// the paragraph, on purpose, since the server's carrying-out writes them
// after the call and not in the approval's own write.
func TestApprovalParagraphKindsTellApproved(t *testing.T) {
	const file = "../../docs/api.md"
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	const marker = "which also carries `approved` with"
	if n := strings.Count(text, marker); n != 1 {
		t.Fatalf("%s says the change's event carries approved %d times, want once: the paragraph moved", file, n)
	}
	at := strings.Index(text, marker)
	open := strings.LastIndex(text[:at], "(")
	if open < 0 {
		t.Fatalf("%s has no list before %q: the paragraph moved", file, marker)
	}
	rest := text[open:at]
	close := strings.Index(rest, ")")
	if close < 0 {
		t.Fatalf("%s has no list before %q: the paragraph moved", file, marker)
	}
	kinds := regexp.MustCompile("`([a-z][a-z0-9_]*)`").FindAllStringSubmatch(rest[1:close], -1)
	if len(kinds) == 0 {
		t.Fatalf("%s names no event before %q: the paragraph moved", file, marker)
	}
	for _, m := range kinds {
		kind := m[1]
		keys, ok := store.EventDataTold(kind)
		if !ok {
			t.Errorf("%s: the approval paragraph names it, and it has no row", kind)
			continue
		}
		if !slices.Contains(keys, "approved") {
			t.Errorf("%s: the approval paragraph names it, and its row does not tell approved", kind)
		}
	}
}

// No row tells a key whose value is a path, a command, the handle of a
// session or the screen of a pane, or a URL: those are the operator's
// configuration, and the words of an error that names one are held with them.
// A row that lists one of these names is a mistake the table's author is asked
// to look at again; the few that are not (a hook delivery is the sender's
// words, and the files it names are the sender's) are said here. Mail is told
// whole to its parties and to nobody else, which EventFor decides, and no
// other kind is.
func TestNoRowTellsWhatNamesAFileOrACommand(t *testing.T) {
	never := []string{"path", "dir", "worktree", "handle", "command", "argv", "test", "error", "hint", "tail", "line", "dialog",
		"env_file", "env_files", "mint_command", "remote", "home", "detail", "prompted", "url", "file", "files", "text", "message", "base_url"}
	exempt := map[string][]string{
		"hook":          {"url", "files", "text", "message"},
		"base_diverged": {"remote"}, // the commit the remote is at, not where it is
	}
	whole := []string{}
	for _, kind := range store.EventDataToldKinds() {
		keys, _ := store.EventDataTold(kind)
		if slices.Contains(keys, "*") {
			whole = append(whole, kind)
			continue
		}
		for _, key := range keys {
			// A field of a record (login.groups) is named by the part after its dot.
			_, field, ok := strings.Cut(key, ".")
			if !ok {
				field = key
			}
			if slices.Contains(never, field) && !slices.Contains(exempt[kind], key) {
				t.Errorf("the row of %s tells %q: a path, a command or the words of an error is the operator's", kind, key)
			}
		}
	}
	if want := []string{"mail", "mail_read", "reaction"}; !slices.Equal(whole, want) {
		t.Errorf("the kinds told whole are %v, want %v: those EventFor tells to their parties alone", whole, want)
	}
}
