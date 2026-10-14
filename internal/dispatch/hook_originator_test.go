package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// hookYard is a depot whose hook makes beads, with operator jo and user
// hal, hal linked to no author yet.
func hookYard(t *testing.T) *ghDepot {
	t.Helper()
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	if _, err := r.api.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	must(t, r.api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	if _, err := r.api.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	return r
}

// linkHal records that hal is jane-doe in github, the author of the
// issues fixtures.
func linkHal(t *testing.T, r *ghDepot) {
	t.Helper()
	must(t, r.api.SetIdentity(context.Background(), "hal", "github", store.Identity{Key: "jane-doe"}, "jo"))
}

// writeEnv is a file of outbound credentials for a test group to carry.
func writeEnv(t *testing.T) string {
	t.Helper()
	env := filepath.Join(t.TempDir(), "hal.env")
	must(t, os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR=/tmp/hal-login\n"), 0o600))
	return env
}

// halGroup adds hal-builders, hal's herdr group carrying envFile.
func halGroup(t *testing.T, r *ghDepot, envFile string) {
	t.Helper()
	must(t, r.api.PutGroup(context.Background(), store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": envFile}}, "jo"))
}

// halRoute adds hal's route in stage to group, and returns its id.
func halRoute(t *testing.T, r *ghDepot, stage, group string) string {
	t.Helper()
	hal := "hal"
	rt, err := r.api.AddRoute(context.Background(), store.Route{Stage: stage, Group: group, Originator: &hal}, "jo")
	must(t, err)
	return rt.ID
}

// makeEvent is the hook_rule event of what the delivery made on bead.
func makeEvent(t *testing.T, r *ghDepot, bead string) map[string]any {
	t.Helper()
	evs, err := r.api.Events(context.Background(), bead, 50)
	must(t, err)
	for _, e := range evs {
		if e.Kind == "hook_rule" && e.Data["makes"] != nil {
			return e.Data
		}
	}
	t.Fatalf("no hook_rule make event on %s", bead)
	return nil
}

// A delivery from a linked author with a personal group and a personal
// route makes an interactor-originated bead, with no fallback recorded.
func TestHookLinkedAuthorWithPersonalRouteBillsTheirLogin(t *testing.T) {
	r := hookYard(t)
	linkHal(t, r)
	halGroup(t, r, writeEnv(t))
	halRoute(t, r, "triage", "hal-builders")
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Originator != "hal" {
		t.Fatalf("made %s for %q, want hal", b.ID, b.Originator)
	}
	if data := makeEvent(t, r, b.ID); data["originator_rule"] != nil || data["linked_author"] != nil {
		t.Fatalf("hook_rule of an interactor bead records a fallback: %v", data)
	}
}

// A personal group with only a subsidy route for the stage bills the
// operator's, and the hook_rule event says which route matched instead.
func TestHookSubsidyRouteFallsBackToOperator(t *testing.T) {
	r := hookYard(t)
	linkHal(t, r)
	halGroup(t, r, writeEnv(t))
	id := halRoute(t, r, "triage", "maintainers")
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Originator != "jo" {
		t.Fatalf("made %s for %q, want jo", b.ID, b.Originator)
	}
	data := makeEvent(t, r, b.ID)
	if data["linked_author"] != "hal" || data["originator_rule"] != "no_personal_route" {
		t.Fatalf("hook_rule fallback = %v, want hal/no_personal_route", data)
	}
	if data["route"] != id || data["worker"] != "maintainers" {
		t.Fatalf("hook_rule route/worker = %v/%v, want %s/maintainers", data["route"], data["worker"], id)
	}
}

// With no personal group at all, a linked author bills the operator's,
// whether a subsidy route names them or nothing does.
func TestHookWithoutPersonalGroupFallsBackToOperator(t *testing.T) {
	for _, subsidy := range []bool{true, false} {
		r := hookYard(t)
		linkHal(t, r)
		if subsidy {
			halRoute(t, r, "triage", "maintainers")
		}
		r.deliverAs(t, "gh", "issues", "issues_opened", nil)
		r.pass(t)
		b := r.made(t, issue23)
		if b.Originator != "jo" {
			t.Fatalf("subsidy=%v: made %s for %q, want jo", subsidy, b.ID, b.Originator)
		}
		data := makeEvent(t, r, b.ID)
		if data["linked_author"] != "hal" || data["originator_rule"] != "no_personal_group" {
			t.Fatalf("subsidy=%v: hook_rule fallback = %v, want hal/no_personal_group", subsidy, data)
		}
		if data["route"] != nil || data["worker"] != nil {
			t.Fatalf("subsidy=%v: hook_rule names a route: %v", subsidy, data)
		}
	}
}

// A personal group with no personal route in the stage bills the
// operator's: the operator's any-route matches but proves no login, and
// with no route in the stage at all the rule names none.
func TestHookPersonalGroupWithoutPersonalRouteFallsBack(t *testing.T) {
	r := hookYard(t)
	linkHal(t, r)
	halGroup(t, r, writeEnv(t))
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Originator != "jo" {
		t.Fatalf("made %s for %q, want jo", b.ID, b.Originator)
	}
	routes, err := r.api.Routes(context.Background())
	must(t, err)
	triage := ""
	for _, rt := range routes {
		if rt.Stage == "triage" {
			triage = rt.ID
		}
	}
	data := makeEvent(t, r, b.ID)
	if data["linked_author"] != "hal" || data["originator_rule"] != "no_personal_route" {
		t.Fatalf("hook_rule fallback = %v, want hal/no_personal_route", data)
	}
	if data["route"] != triage || data["worker"] != "maintainers" {
		t.Fatalf("hook_rule route/worker = %v/%v, want %s/maintainers", data["route"], data["worker"], triage)
	}

	bare := hookYard(t)
	linkHal(t, bare)
	halGroup(t, bare, writeEnv(t))
	routes, err = bare.api.Routes(context.Background())
	must(t, err)
	for _, rt := range routes {
		if rt.Stage == "triage" {
			must(t, bare.api.DeleteRoute(context.Background(), rt.ID, "jo"))
		}
	}
	bare.deliverAs(t, "gh", "issues", "issues_opened", nil)
	bare.pass(t)
	bb := bare.made(t, issue23)
	if bb.Originator != "jo" {
		t.Fatalf("no route: made %s for %q, want jo", bb.ID, bb.Originator)
	}
	if data := makeEvent(t, bare, bb.ID); data["linked_author"] != "hal" ||
		data["originator_rule"] != "no_personal_route" || data["route"] != nil || data["worker"] != nil {
		t.Fatalf("no route: hook_rule fallback = %v, want hal/no_personal_route alone", data)
	}
}

// A personal group whose configured env file is missing or unreadable
// still makes an interactor-originated bead: the declaration wins while
// the group waits runtime_down.
func TestHookMissingEnvFileStillBillsInteractor(t *testing.T) {
	missing := hookYard(t)
	linkHal(t, missing)
	halGroup(t, missing, filepath.Join(t.TempDir(), "nowhere.env"))
	halRoute(t, missing, "triage", "hal-builders")
	missing.deliverAs(t, "gh", "issues", "issues_opened", nil)
	missing.pass(t)
	if b := missing.made(t, issue23); b.Originator != "hal" {
		t.Fatalf("missing env file: made %s for %q, want hal", b.ID, b.Originator)
	}

	unreadable := hookYard(t)
	linkHal(t, unreadable)
	env := filepath.Join(t.TempDir(), "hal.env")
	must(t, os.WriteFile(env, []byte("CLAUDE_CONFIG_DIR=/tmp/hal-login\n"), 0o000))
	halGroup(t, unreadable, env)
	halRoute(t, unreadable, "triage", "hal-builders")
	unreadable.deliverAs(t, "gh", "issues", "issues_opened", nil)
	unreadable.pass(t)
	if b := unreadable.made(t, issue23); b.Originator != "hal" {
		t.Fatalf("unreadable env file: made %s for %q, want hal", b.ID, b.Originator)
	}
}

// A delivery whose author matches no linked identity bills the
// operator's, as every hook bead does, with nothing recorded.
func TestHookUnlinkedAuthorStaysOperators(t *testing.T) {
	r := hookYard(t)
	must(t, r.api.SetIdentity(context.Background(), "hal", "github", store.Identity{Key: "someone-else"}, "jo"))
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Originator != "jo" {
		t.Fatalf("made %s for %q, want jo", b.ID, b.Originator)
	}
	if data := makeEvent(t, r, b.ID); data["originator_rule"] != nil || data["linked_author"] != nil {
		t.Fatalf("hook_rule of a bystander bead records a fallback: %v", data)
	}
}

// A delivery the operator themself authored is the operator's already:
// no fallback to record.
func TestHookOperatorAuthorStaysOperators(t *testing.T) {
	r := hookYard(t)
	must(t, r.api.SetIdentity(context.Background(), "jo", "github", store.Identity{Key: "jane-doe"}, "jo"))
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Originator != "jo" {
		t.Fatalf("made %s for %q, want jo", b.ID, b.Originator)
	}
	if data := makeEvent(t, r, b.ID); data["originator_rule"] != nil || data["linked_author"] != nil {
		t.Fatalf("hook_rule of the operator's own bead records a fallback: %v", data)
	}
}
