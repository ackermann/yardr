package server

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// The mappings' open-threads and calls become the apply's answers for
// exactly what they name, and a mapping for what the pack does not ask
// is refused in its flag's words.
func TestMappingAnswers(t *testing.T) {
	ctx := context.Background()
	st := packReaderYard(t)
	plan := &pack.Plan{
		Groups: map[string]string{"builders": "web-builders"},
		Crew:   map[string]string{"yardmaster": "yardmaster"},
		OpenThreads: []pack.OpenThreads{
			{Pack: "yardmaster", CrewMember: "yardmaster", Wants: []string{"linear"}, Asks: []string{"linear"}},
		},
		Calls: []pack.Calls{
			{Pack: "builders", Group: "web-builders", Source: "crm", Wants: []string{"read"}, Asks: []string{"read"}},
		},
	}
	_, open, calls, err := mappingAnswers(ctx, st, plan, store.Mappings{
		OpenThreads: []string{"yardmaster=linear"},
		Calls:       []string{"builders=crm:read"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !open["yardmaster"]["linear"] {
		t.Errorf("the open-threads answers = %+v, want yardmaster let open threads in linear", open)
	}
	if !calls["group web-builders"]["crm:read"] {
		t.Errorf("the calls answers = %+v, want the builders let call crm:read", calls)
	}
	if _, _, _, err := mappingAnswers(ctx, st, plan, store.Mappings{
		Calls: []string{"builders=crm:write"},
	}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "--calls") {
		t.Errorf("a call the pack does not ask for = %v, want a --calls refusal", err)
	}
}
