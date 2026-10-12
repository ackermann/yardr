package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// A device pairing is opened by whoever adds a device and by nobody else:
// the operator, outside an agent's pane. What device add refuses of the
// device, it refuses at once, before a code is shown.
func TestDevicePairOpenIsTheOperators(t *testing.T) {
	api, own := deviceYard(t)
	ctx := context.Background()
	for actor, want := range map[string]string{
		"ada":        "only the operator (jo) may add a device; this is ada",
		"yardmaster": "only the operator (jo) may add a device; this is yardmaster",
		"":           "only the operator (jo) may add a device; this is nobody",
	} {
		if _, err := api.DevicePairOpen(ctx, "ada", "phone", actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("DevicePairOpen as %q = %v", actor, err)
		}
	}
	if _, err := api.DevicePairOpen(ctx, "carol", "phone", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a pairing for a user the yard has not = %v", err)
	}
	if _, err := api.DevicePairOpen(ctx, "ada", "my phone", "jo"); err == nil || !strings.Contains(err.Error(), "use letters, digits") {
		t.Errorf("a pairing for a device of no name = %v", err)
	}
	if _, err := api.AddDevice(ctx, "ada", "laptop", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairOpen(ctx, "ada", "laptop", "jo"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a pairing for a device ada has = %v", err)
	}

	// From the yardmaster's pane, naming the operator.
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	const want = "a device is added by the operator, outside an agent's pane: whoever adds one decides who may speak as ada; this is the crew member yardmaster"
	if _, err := own.DevicePairOpen(store.WithCaller(ctx, me), "ada", "phone", "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("DevicePairOpen from the yardmaster's pane as jo = %v", err)
	}
	if own.DevicePairOpened(ctx) {
		t.Fatal("a refused pairing is open")
	}
	if got, _ := api.EventsOfKind(ctx, "device_pair_opened", 10); len(got) != 0 {
		t.Fatalf("a refused pairing was logged as opened: %+v", got)
	}
}

// A yard has one device pairing open at a time. The operator ends it, and
// its code is good for nothing then; with none open there is none to end.
func TestDevicePairOneAtATime(t *testing.T) {
	api, own := deviceYard(t)
	ctx := context.Background()
	opened, err := api.DevicePairOpen(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if opened.State != store.PairOpened || opened.User != "ada" || opened.Device != "phone" || len(opened.Code) != 9 {
		t.Fatalf("opened = %+v", opened)
	}
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err == nil || !strings.Contains(err.Error(), "a pairing is open already, for the device phone of ada until ") {
		t.Fatalf("a second pairing = %v", err)
	}
	if _, err := api.DevicePairClose(ctx, "ada"); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may end a device's pairing; this is ada") {
		t.Fatalf("DevicePairClose as ada = %v", err)
	}
	ended, err := api.DevicePairClose(ctx, "jo")
	if err != nil || ended.State != store.PairEnded || ended.Reason != pairing.Closed || ended.Device != "phone" {
		t.Fatalf("DevicePairClose = %+v, %v", ended, err)
	}
	var no *pairing.Refused
	if _, err := own.DevicePairRedeem(ctx, opened.Code); !errors.As(err, &no) || no.Reason != pairing.NoPairing {
		t.Fatalf("the code of an ended pairing = %v", err)
	}
	if _, err := api.DevicePairClose(ctx, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairClose with none open = %v", err)
	}
	if got, _ := api.EventsOfKind(ctx, "device_pair_refused", 10); len(got) != 1 || got[0].Actor != "jo" || got[0].Data["reason"] != pairing.Closed {
		t.Fatalf("device_pair_refused = %+v", got)
	}
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err != nil {
		t.Fatalf("a pairing after the last was ended = %v", err)
	}
}

// What is true when the code is used decides: a user removed, or a device
// of the name added, while the pairing was open ends it with nothing added,
// and the code is used all the same.
func TestDevicePairNotAdded(t *testing.T) {
	api, own := deviceYard(t)
	ctx := context.Background()
	var no *pairing.Refused
	for what, spoil := range map[string]func() error{
		"the user was removed": func() error { return api.DeleteUser(ctx, "ada", "jo") },
		"the name was taken": func() error {
			_, err := api.AddDevice(ctx, "bob", "phone", "jo")
			return err
		},
	} {
		user := "ada"
		if what == "the name was taken" {
			user = "bob"
		}
		opened, err := api.DevicePairOpen(ctx, user, "phone", "jo")
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if err := spoil(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if d, err := own.DevicePairRedeem(ctx, opened.Code); d != nil || !errors.As(err, &no) || no.Reason != store.DeviceNotAdded {
			t.Errorf("%s: the code = %+v, %v", what, d, err)
		}
		if d, err := own.DevicePairRedeem(ctx, opened.Code); d != nil || !errors.As(err, &no) || no.Reason != pairing.NoPairing {
			t.Errorf("%s: the code again = %+v, %v", what, d, err)
		}
	}
	got, _ := api.EventsOfKind(ctx, "device_pair_refused", 10)
	if len(got) != 2 || got[0].Data["reason"] != store.DeviceNotAdded || got[1].Data["reason"] != store.DeviceNotAdded {
		t.Fatalf("device_pair_refused = %+v", got)
	}
	if list, _ := api.Devices(ctx, ""); len(list) != 1 || list[0].User != "bob" {
		t.Fatalf("devices = %+v", list)
	}
}
