package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func packApp(t *testing.T) *app {
	t.Helper()
	st := storetest.New(t)
	if err := st.PutRig(context.Background(), store.Rig{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "me", cfg: &config.Config{Home: t.TempDir()}}
}

func TestPackCheckPrintsThePlan(t *testing.T) {
	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web", "--resident", "mayor=boss")
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(a.cfg.Home, "packs", "starter")
	for _, want := range []string{
		"into rig web\n",
		"its roles and scripts would be copied to " + install + "\n",
		"  create           roles/builder.md -> " + filepath.Join(install, "roles", "builder.md") + "\n",
		"  create           web-builders\n                     asks: group.web-builders.kind\n",
		"  create           rig web stage approved -> web-refinery\n",
		"  create           boss linear involved\n",
		"an import would ask\n  group.web-builders.kind: which agent runs group web-builders",
		"flows governing rig web afterwards\n  the rig's beads: starter.default (store); now: none\n",
		"source linear, for resident boss\n  missing  hook ",
		"20 to create, 0 to change, 0 unchanged, 0 not in the pack\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
	// --resident named it, so the import need not ask who takes mayor.
	if strings.Contains(out, "resident.boss.resident") {
		t.Errorf("asks who takes mayor though --resident said:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web")
	if err != nil {
		t.Fatal(err)
	}
	var plan pack.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.Pack != "starter" || len(plan.Objects) != 20 || len(plan.Questions) != 10 {
		t.Fatalf("json plan: %v\n%s", err, out)
	}
}

// An invalid pack exits non-zero, each mistake with file, line and key,
// and the store is not even asked.
func TestPackCheckInvalidPackFails(t *testing.T) {
	dir := t.TempDir()
	text := "name: bad\nflows:\n  default:\n    stages:\n      new: { group: nobody, on: { done: new } }\n"
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{actor: "me"} // no store: reaching for one would fail the test's home
	_, err := run(t, a.packCmd(), "check", dir, "--rig", "web")
	if err == nil || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1", err, exitCode(err))
	}
	file := filepath.Join(dir, "pack.yaml")
	for _, want := range []string{
		file + ":4: flows.default.stages: flow default has no end",
		file + ":5: flows.default.stages.new.group: no group nobody is declared",
		file + ":5: flows.default.stages.new.on: YAML reads the bare key on as a boolean",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}

	a.json = true
	out, err := run(t, a.packCmd(), "check", dir, "--rig", "web")
	var got struct {
		Errors []pack.Error `json:"errors"`
	}
	if !errors.Is(err, errCheckFailed) || json.Unmarshal([]byte(out), &got) != nil || len(got.Errors) != 3 || got.Errors[0].Line != 4 {
		t.Fatalf("json: %v\n%s", err, out)
	}

	if _, err := run(t, packApp(t).packCmd(), "check", "../../packs/starter"); err == nil || !strings.Contains(err.Error(), "rig") {
		t.Fatalf("without --rig: %v", err)
	}
}

// A plan the import would refuse is printed whole, then fails the check.
func TestPackCheckRefusedPlanFails(t *testing.T) {
	a := packApp(t)
	b, err := a.st.CreateBead(context.Background(), store.NewBead{Title: "stray", Rig: "web", Stage: "triage"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.packCmd(), "check", "../../packs/starter", "--rig", "web")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v, want the check failed", err)
	}
	if !strings.Contains(out, "an import would be refused\n  flow starter.default lacks the stage of open beads that would follow it: "+b.ID+" (triage)") ||
		!strings.Contains(out, "20 to create") {
		t.Fatalf("out:\n%s", out)
	}
}
