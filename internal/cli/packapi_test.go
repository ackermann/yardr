package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// docs/pack-api.md lists the JSON fields of the stable commands a pack's
// script runs. This is the same contract as a test: packBeadFields and the
// others name every promised field with its type, the test runs each command
// with --json and checks that every promised field is there with its type,
// and the page's field tables between markers are made from those lists
// (TestPackAPIDoc). So a field renamed in the store fails here, and the page
// cannot show a field the commands do not give.
//
// The pattern is internal/rpc's TestAPIDoc, but only the tables are made:
// the page shows no example replies.

// packField is one promised field: where it is in the answer ("id",
// "next.outcomes[].name"), its type, and what it says. A type ending in "?"
// is of a field that is absent when it has nothing to say.
type packField struct{ path, typ, says string }

// packBeadFields is a bead as bead show and bead create print it, and the
// bead of prime: the fields a script reads.
var packBeadFields = []packField{
	{"id", "string", "the bead's handle"},
	{"title", "string", ""},
	{"body", "string?", "absent when empty"},
	{"type", "string", ""},
	{"stage", "string", "the stage the bead is in"},
	{"status", "string", "`open` or `closed`"},
	{"priority", "number", "0 is the highest"},
	{"depot", "string?", "absent when it has none"},
	{"labels", "array of string", ""},
	{"metadata", "object?", "what `bead update --meta` writes; absent when empty"},
}

// packPrimeNextFields is prime's next: where the bead may go from its stage.
var packPrimeNextFields = []packField{
	{"next.stage", "string", "the bead's stage"},
	{"next.stages", "array of string", "the stages it may be moved to, empty for a bead no flow governs, which may go to any"},
	{"next.outcomes[].name", "string", "an outcome `bead done --outcome` takes, absent when the flow leaves the stage to explicit moves"},
	{"next.outcomes[].goes", "string", "the stage reporting it takes the bead to now, which a limit on rounds can change from the flow's edge"},
}

// packLinkFields is one entry of link list: the fields are checked on each
// entry of the array the command prints.
var packLinkFields = []packField{
	{"kind", "string", "`tracks` or `relates`. A bead of another yard that this one is at, was at or came from is listed too, with the kind `at`, `was at` or `from`, and is no issue"},
	{"source", "string", "the outside source, and `peer` for a bead of another yard"},
	{"url", "string", "the issue in its one form, and for a bead of another yard its handle there"},
}

// packSettingsFields is settings get <key> --json. From, which the answer
// also carries, is internal, as the page says of every other field.
var packSettingsFields = []packField{
	{"key", "string", "the setting"},
	{"value", "string", "the value the yard goes by"},
}

// TestPackAPIDoc runs the stable commands with --json and holds their
// promised fields to the tables of docs/pack-api.md: a field removed from a
// command's output fails the check, and a table that is not what the test
// says fails the doc. YARDR_UPDATE_GOLDEN=1 writes the tables again instead.
func TestPackAPIDoc(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review on done\nreview -> merged on done\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, st, newHomeDir(t))
	a.actor = "me"
	a.json = true

	out, err := run(t, a.beadCmd(), "create", "the title", "-d", "yard", "-b", "the body",
		"-t", "task", "-s", "new", "-p", "1", "-l", "topic:api", "--meta", "model=opus", "--id", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	created := packJSON(t, out)
	if _, err := st.AddLink(ctx, "yard-1", store.NewLink{Kind: store.LinkTracks, Source: "tracker", URL: "https://tracker.test/issue/YRD-12"}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	shown := packJSON(t, out)
	out, err = run(t, a.primeCmd(), "--bead", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	primed := packJSON(t, out)
	primeBead, _ := primed.(map[string]any)["bead"]
	out, err = run(t, a.linkCmd(), "list", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	links, _ := packJSON(t, out).([]any)
	out, err = run(t, a.settingsCmd(), "get", "timer_limit")
	if err != nil {
		t.Fatal(err)
	}
	setting := packJSON(t, out)

	checkPackFields(t, "bead", packBeadFields, []any{shown, created, primeBead})
	checkPackFields(t, "prime", packPrimeNextFields, []any{primed})
	checkPackFields(t, "link list", packLinkFields, links)
	checkPackFields(t, "settings get", packSettingsFields, []any{setting})

	const file = "../../docs/pack-api.md"
	sections := map[string][]string{
		"bead":         packFieldTable(packBeadFields),
		"prime-next":   packFieldTable(packPrimeNextFields),
		"links":        packFieldTable(packLinkFields),
		"settings-get": packFieldTable(packSettingsFields),
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	var rewritten []string
	found := map[string]bool{}
	stale := false
	for i := 0; i < len(lines); i++ {
		rewritten = append(rewritten, lines[i])
		name, _, ok := strings.Cut(strings.TrimPrefix(lines[i], "<!-- "), ":")
		want := sections[name]
		if !ok || want == nil || !strings.HasPrefix(lines[i], "<!-- ") {
			continue
		}
		found[name] = true
		end := i + 1
		for end < len(lines) && lines[end] != "<!-- /"+name+" -->" {
			end++
		}
		if end == len(lines) {
			t.Fatalf("%s: no `<!-- /%s -->` after line %d", file, name, i+1)
		}
		if got := lines[i+1 : end]; !slices.Equal(got, want) {
			stale = true
			if os.Getenv("YARDR_UPDATE_GOLDEN") == "" {
				t.Errorf("%s: the section %q is not what the commands answer; write it again with\n"+
					"  YARDR_UPDATE_GOLDEN=1 go test ./internal/cli -run TestPackAPIDoc\ngot:\n%s\nwant:\n%s",
					file, name, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
		rewritten = append(rewritten, want...)
		i = end - 1
	}
	for name := range sections {
		if !found[name] {
			t.Errorf("%s has no `<!-- %s: ... -->` marker: the section is gone", file, name)
		}
	}
	if stale && os.Getenv("YARDR_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(file, []byte(strings.Join(rewritten, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func packFieldTable(fields []packField) []string {
	rows := []string{"| field | type | |", "|---|---|---|"}
	for _, f := range fields {
		typ := f.typ
		if opt, ok := strings.CutSuffix(typ, "?"); ok {
			typ = opt + ", may be absent"
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", f.path, typ, f.says))
	}
	return rows
}

func packJSON(t *testing.T, out string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("no JSON: %v:\n%s", err, out)
	}
	return v
}

// packAt are the values at path in v, and how many places that should have
// one have none. A segment ending in [] walks each element of the array
// there, so next.outcomes[].name are the names of the outcomes.
func packAt(v any, path string) (vals []any, missing int) {
	if path == "" {
		return []any{v}, 0
	}
	curs := []any{v}
	for _, seg := range strings.Split(path, ".") {
		var next []any
		key, isArray := strings.CutSuffix(seg, "[]")
		for _, c := range curs {
			if isArray {
				var arr any
				if key == "" {
					arr = c
				} else {
					m, ok := c.(map[string]any)
					if !ok {
						missing++
						continue
					}
					got, present := m[key]
					if !present {
						missing++
						continue
					}
					arr = got
				}
				a, ok := arr.([]any)
				if !ok {
					missing++
					continue
				}
				next = append(next, a...)
				continue
			}
			m, ok := c.(map[string]any)
			if !ok {
				missing++
				continue
			}
			val, ok := m[seg]
			if !ok {
				missing++
				continue
			}
			next = append(next, val)
		}
		if curs = next; len(curs) == 0 {
			break
		}
	}
	return curs, missing
}

// checkPackFields fails for every promised field that the answers do not
// have with its type. A field that may be absent must still be in one of
// them: the yard of the test is made so that each has something to say.
func checkPackFields(t *testing.T, what string, fields []packField, answers []any) {
	t.Helper()
	for _, f := range fields {
		typ, may := strings.CutSuffix(f.typ, "?")
		var vals []any
		missing := 0
		for _, a := range answers {
			got, m := packAt(a, f.path)
			vals, missing = append(vals, got...), missing+m
		}
		if len(vals) == 0 {
			t.Errorf("%s: the field %q the doc promises is in no answer", what, f.path)
			continue
		}
		if missing > 0 && !may {
			t.Errorf("%s: the field %q is absent in %d places, and the doc does not say it may be", what, f.path, missing)
		}
		for _, v := range vals {
			if !packIsType(v, typ) {
				got, _ := json.Marshal(v)
				t.Errorf("%s: the field %q is %s, and the doc says %s", what, f.path, got, typ)
			}
		}
	}
}

func packIsType(v any, typ string) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "array of string":
		a, ok := v.([]any)
		for _, e := range a {
			if _, isString := e.(string); !isString {
				return false
			}
		}
		return ok
	}
	return false
}
