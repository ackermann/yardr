package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A user's own address in a source is not given from the pane of an agent,
// whoever the call says it is: neither as the answer of a secret ask nor
// unasked. The pane's own asking is taken, since asking gives it nothing.
func TestAUsersAddressIsNotGivenFromAnAgentsPane(t *testing.T) {
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutSource(ctx, "p.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	b, err := own.CreateBead(pane, store.NewBead{Title: "Tell ada on her phone"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := own.AddAsk(pane, b.ID, store.Ask{Question: "Your Pushover user key", To: "ada",
		Context: []string{"It is on the first page of pushover.net after login."},
		Secret:  &store.AskSecret{Source: "pushover", User: "ada"}}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	const want = "where ada is told outside the yard is set by ada, or by the operator (jo): what lands in their inbox goes there; this is the crew member yardmaster"
	for _, actor := range []string{"ada", "jo"} {
		_, err := own.GiveSecret(pane, b.ID, seq, map[string]string{"PUSHOVER_USER": "u-s3cret"}, actor)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "u-s3cret") {
			t.Errorf("GiveSecret from the yardmaster's pane as %s = %v", actor, err)
		}
		_, err = own.KeyUser(pane, "ada", "pushover", map[string]string{"PUSHOVER_USER": "u-s3cret"}, actor)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "u-s3cret") {
			t.Errorf("KeyUser from the yardmaster's pane as %s = %v", actor, err)
		}
	}
	if u, err := api.GetUser(ctx, "ada"); err != nil || u.EnvFile != "" {
		t.Fatalf("ada = %+v, %v: a refused giving set a file", u, err)
	}
	if notes, err := api.Notes(ctx, b.ID); err != nil || len(notes) != 1 {
		t.Fatalf("the bead has %d notes, %v: want the ask alone", len(notes), err)
	}
	// Outside the pane, as the user.
	keyed, err := own.GiveSecret(ctx, b.ID, seq, map[string]string{"PUSHOVER_USER": "u-s3cret"}, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(keyed.File) != "ada.env" || filepath.Base(filepath.Dir(keyed.File)) != "users" {
		t.Fatalf("the file = %s", keyed.File)
	}
}
