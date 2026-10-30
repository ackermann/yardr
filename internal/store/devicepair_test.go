package store_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// A device pairing is opened by whoever adds a device and by nobody else:
// the operator, outside an agent's pane. What device add refuses of the
// device, it refuses at once, before a code is shown.
func TestDevicePairOpenIsTheOperators(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	opened, err := api.DevicePairOpen(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if opened.State != store.PairOpened || opened.User != "ada" || opened.Device != "phone" || len(opened.Code) != 9 {
		t.Fatalf("opened = %+v", opened)
	}
	// The holder opening again is told of their own pairing, device and
	// all; opening for anyone else names the holder's yard user, and
	// nothing of the device, the code or when it runs out.
	if _, err := api.DevicePairOpen(ctx, "ada", "laptop", "jo"); err == nil || !strings.Contains(err.Error(), "a pairing is open already, for the device phone of ada until ") {
		t.Fatalf("a second pairing for the holder = %v", err)
	}
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err == nil || err.Error() != "a pairing is already open for ada, try again in a few minutes" {
		t.Fatalf("a second pairing for another user = %v", err)
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

// The open pairing reads back with its code and QR string, so a window
// reopened shows the same pairing again; with none open there is no
// pairing, before one opens and after it ends. The code is good for ten
// minutes, and the QR holds the edge's address, the code and the yard's
// handle/name there — and nothing while the yard is connected to no edge.
func TestDevicePairState(t *testing.T) {
	t.Parallel()
	api, _ := deviceYard(t)
	ctx := context.Background()
	if _, err := api.DevicePairState(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairState with none open = %v, want no pairing", err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := store.Edge{URL: "https://my.yardr.dev", Key: yardkey.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := api.SetSetting(ctx, store.EdgeSetting, edge.String(), "jo"); err != nil {
		t.Fatal(err)
	}
	opened, err := api.DevicePairOpen(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if d := opened.Expires.Sub(time.Now()); d <= 9*time.Minute || d > pairing.TTL {
		t.Fatalf("the code runs out in %v, want ten minutes", d)
	}
	got, err := api.DevicePairState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != opened.ID || got.User != "ada" || got.Device != "phone" || got.State != store.PairOpened ||
		!got.Expires.Equal(opened.Expires) || got.Code != opened.Code {
		t.Fatalf("DevicePairState = %+v, want the pairing %+v", got, opened)
	}
	if want := "https://my.yardr.dev#" + opened.Code + "#mckean/edgeyard"; got.QR != want {
		t.Fatalf("DevicePairState QR = %q, want %q", got.QR, want)
	}
	if _, err := api.DevicePairClose(ctx, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairState(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairState after the pairing ended = %v, want no pairing", err)
	}
	// Connected to no edge, the yard names no address: the state still
	// reads, with the code and no QR string.
	if err := api.SetSetting(ctx, store.EdgeSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairOpen(ctx, "ada", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	if got, err := api.DevicePairState(ctx); err != nil || got.QR != "" || got.Code == "" {
		t.Fatalf("DevicePairState with no edge = %+v, %v, want the code and no QR", got, err)
	}
}

// What is true when the code is used decides: a user removed, or a device
// of the name added, while the pairing was open ends it with nothing added,
// and the code is used all the same.
func TestDevicePairNotAdded(t *testing.T) {
	t.Parallel()
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

// A redemption that names its presenter is the pairing's user's alone. Any
// other mapped presenter is refused as a wrong code is and counted as one,
// before the code is looked at and with the code left unused. An unmapped
// presenter is refused when the pairing's user is mapped, and otherwise
// redeems as with no presenter. What is no code at all is no try, whoever
// presents it. The fifth refusal ends the pairing, and the code is still
// required.
func TestDevicePairRedeemBy(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	var no *pairing.Refused
	notThe := func(code string) string {
		if strings.HasPrefix(code, "0") {
			return "1" + code[1:]
		}
		return "0" + code[1:]
	}
	refused := func(what, code, presenter, reason string) {
		t.Helper()
		if d, err := own.DevicePairRedeemBy(ctx, code, presenter); d != nil || !errors.As(err, &no) || no.Reason != reason {
			t.Errorf("%s = %+v, %v, want %s", what, d, err, reason)
		}
	}

	// Four refusals leave the code good for ada, who redeems it. A body that
	// is no code is no try for the presenter that cannot redeem, or the
	// fourth refusal would have been the fifth.
	opened, err := api.DevicePairOpenSelf(ctx, "ada", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := own.DevicePairRedeemBy(ctx, "", "bob"); d != nil || err == nil || errors.As(err, &no) {
		t.Fatalf("no code, presented by bob = %+v, %v, want an error that is no refusal", d, err)
	}
	for _, presenter := range []string{"bob", "", "jo", "carol"} {
		refused("ada's code presented by "+presenter, opened.Code, presenter, pairing.WrongCode)
	}
	d, err := own.DevicePairRedeemBy(ctx, opened.Code, "ada")
	if err != nil || d.User != "ada" || d.Name != "phone" || d.CreatedBy != "ada" {
		t.Fatalf("ada's code presented by ada = %+v, %v", d, err)
	}

	// The right presenter is held to the code, and the fifth refusal ends
	// the pairing whoever is refused: the right presenter finds none then.
	opened, err = api.DevicePairOpenSelf(ctx, "bob", "phone")
	if err != nil {
		t.Fatal(err)
	}
	refused("a code that is not bob's presented by bob", notThe(opened.Code), "bob", pairing.WrongCode)
	for try := 2; try <= pairing.MaxTries; try++ {
		refused("bob's code presented by ada", opened.Code, "ada", pairing.WrongCode)
	}
	refused("bob's code after the fifth", opened.Code, "bob", pairing.NoPairing)
	ended, _ := api.EventsOfKind(ctx, "device_pair_refused", 10)
	if len(ended) != 1 || ended[0].Data["reason"] != pairing.WrongCode || ended[0].Data["wrong_codes"] != float64(pairing.MaxTries) || ended[0].Data["user"] != "bob" {
		t.Fatalf("device_pair_refused = %+v", ended)
	}
	if list, _ := api.Devices(ctx, ""); len(list) != 1 || list[0].User != "ada" {
		t.Fatalf("devices = %+v", list)
	}

	// An unmapped presenter stays out of a mapped user's pairing and leaves
	// the code good for that user. A pairing for an unmapped user redeems
	// with an unmapped presenter, but the code is still checked.
	mapped := map[string]bool{"bob": true}
	opened, err = api.DevicePairOpenSelf(ctx, "bob", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := own.DevicePairRedeemUnmapped(ctx, opened.Code, mapped); d != nil || !errors.As(err, &no) || no.Reason != pairing.WrongCode {
		t.Fatalf("bob's code, bob mapped = %+v, %v, want wrong_code", d, err)
	}
	d, err = own.DevicePairRedeem(ctx, opened.Code)
	if err != nil || d.User != "bob" || d.Name != "phone" {
		t.Fatalf("bob's code after the refusal = %+v, %v", d, err)
	}
	opened, err = api.DevicePairOpenSelf(ctx, "ada", "tablet")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := own.DevicePairRedeemUnmapped(ctx, notThe(opened.Code), mapped); d != nil || !errors.As(err, &no) || no.Reason != pairing.WrongCode {
		t.Fatalf("a code that is not ada's, ada unmapped = %+v, %v, want wrong_code", d, err)
	}
	d, err = own.DevicePairRedeemUnmapped(ctx, opened.Code, mapped)
	if err != nil || d.User != "ada" || d.Name != "tablet" {
		t.Fatalf("ada's code, ada unmapped = %+v, %v", d, err)
	}

	// What is no code is no try, and an empty mapping redeems for an
	// unmapped user just as a mapping that names another user does.
	opened, err = api.DevicePairOpenSelf(ctx, "ada", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := own.DevicePairRedeemUnmapped(ctx, "", mapped); d != nil || err == nil || errors.As(err, &no) {
		t.Fatalf("no code, ada unmapped = %+v, %v, want an error that is no refusal", d, err)
	}
	d, err = own.DevicePairRedeemUnmapped(ctx, opened.Code, map[string]bool{})
	if err != nil || d.User != "ada" || d.Name != "laptop" {
		t.Fatalf("ada's code with an empty mapping = %+v, %v", d, err)
	}
	if d, err := own.DevicePairRedeemUnmapped(ctx, opened.Code, mapped); d != nil || !errors.As(err, &no) || no.Reason != pairing.NoPairing {
		t.Fatalf("ada's code after it was used = %+v, %v, want no pairing", d, err)
	}
	if list, _ := api.Devices(ctx, ""); len(list) != 4 {
		t.Fatalf("devices = %+v, want 4", list)
	}
}

// A user opens a pairing for one of their own devices with no leave taken:
// good for the pairing's ten minutes and for one device, under their own
// name as its actor, and redeemable as the operator's pairing is. What the
// operator's opening refuses of the device, this one refuses too.
func TestDevicePairOpenSelf(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	opened, err := api.DevicePairOpenSelf(ctx, "ada", "phone")
	if err != nil {
		t.Fatal(err)
	}
	if opened.State != store.PairOpened || opened.User != "ada" || opened.Device != "phone" || len(opened.Code) != 9 {
		t.Fatalf("opened = %+v", opened)
	}
	if until := time.Until(opened.Expires); until > pairing.TTL || until < pairing.TTL-time.Minute {
		t.Fatalf("the pairing runs out in %v, want about %v", until, pairing.TTL)
	}
	if got, _ := api.EventsOfKind(ctx, "device_pair_opened", 10); len(got) != 1 || got[0].Actor != "ada" {
		t.Fatalf("device_pair_opened = %+v, want one by ada", got)
	}
	// The code getter answers the same pairing, code and all, so a page
	// that reloads shows the same QR.
	again, err := api.DevicePairCodeSelf(ctx, "ada")
	if err != nil || again.Code != opened.Code || again.Device != "phone" || !again.Expires.Equal(opened.Expires) {
		t.Fatalf("DevicePairCodeSelf = %+v, %v, want %+v again", again, err, opened)
	}
	// Another user is told nothing of it: no code, and no end either.
	if _, err := api.DevicePairCodeSelf(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairCodeSelf as bob = %v", err)
	}
	if _, err := api.DevicePairCloseSelf(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairCloseSelf as bob = %v", err)
	}
	if !own.DevicePairOpened(ctx) {
		t.Fatal("another user's asking ended the pairing")
	}
	// The code still redeems, and the device is ada's, made by ada.
	d, err := own.DevicePairRedeem(ctx, opened.Code)
	if err != nil || d.User != "ada" || d.Name != "phone" || d.CreatedBy != "ada" {
		t.Fatalf("the code redeemed to %+v, %v", d, err)
	}
	if _, err := api.DevicePairCodeSelf(ctx, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the code of a used pairing = %v", err)
	}
}

// One pairing lives at a time, whosever it is: a second opening of one's
// own is refused while one lives, another user's opening likewise in words
// that name the holder's yard user and nothing of the device, and so is the
// operator's — and the operator's pairing refuses a user's the same way.
// Only the owner ends their own; the operator still ends any.
func TestDevicePairSelfOneAtATime(t *testing.T) {
	t.Parallel()
	api, _ := deviceYard(t)
	ctx := context.Background()
	if _, err := api.DevicePairOpenSelf(ctx, "ada", "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairOpenSelf(ctx, "ada", "laptop"); err == nil || !strings.Contains(err.Error(), "a pairing is open already, for your device phone until ") {
		t.Fatalf("a second pairing of one's own = %v", err)
	}
	other, err := api.DevicePairOpenSelf(ctx, "bob", "phone")
	if err == nil || other != nil || err.Error() != "a pairing is already open for ada, try again in a few minutes" {
		t.Fatalf("another user's pairing = %+v, %v", other, err)
	}
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err == nil || err.Error() != "a pairing is already open for ada, try again in a few minutes" {
		t.Fatalf("the operator's pairing over one's own = %v", err)
	}
	ended, err := api.DevicePairCloseSelf(ctx, "ada")
	if err != nil || ended.State != store.PairEnded || ended.Reason != pairing.Closed || ended.Device != "phone" {
		t.Fatalf("DevicePairCloseSelf = %+v, %v", ended, err)
	}
	if _, err := api.DevicePairCloseSelf(ctx, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DevicePairCloseSelf with none open = %v", err)
	}
	// The operator's pairing now: a user's opening is refused, and the
	// operator's own ending still works over it.
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairOpenSelf(ctx, "ada", "laptop"); err == nil || err.Error() != "a pairing is already open for bob, try again in a few minutes" {
		t.Fatalf("one's own pairing over the operator's = %v", err)
	}
	// The pairing is bob's, whoever opened it: bob ends it, and the
	// operator's ending then finds none. A fresh one is the operator's to
	// end as before.
	if _, err := api.DevicePairCloseSelf(ctx, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ending another's pairing as ada = %v", err)
	}
	if _, err := api.DevicePairCloseSelf(ctx, "bob"); err != nil {
		t.Fatalf("ending one's own pairing the operator opened: %v", err)
	}
	if _, err := api.DevicePairOpen(ctx, "bob", "phone", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairClose(ctx, "jo"); err != nil {
		t.Fatalf("the operator ends the pairing: %v", err)
	}
}

// What one's own opening refuses before any code: a user the yard has not,
// a device of no name, and a device the user already has.
func TestDevicePairOpenSelfRefusals(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	if _, err := api.DevicePairOpenSelf(ctx, "carol", "phone"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a pairing for a user the yard has not = %v", err)
	}
	if _, err := api.DevicePairOpenSelf(ctx, "ada", "my phone"); err == nil || !strings.Contains(err.Error(), "use letters, digits") {
		t.Errorf("a pairing for a device of no name = %v", err)
	}
	if _, err := api.AddDevice(ctx, "ada", "laptop", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.DevicePairOpenSelf(ctx, "ada", "laptop"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a pairing for a device ada has = %v", err)
	}
	if own.DevicePairOpened(ctx) {
		t.Fatal("a refused pairing is open")
	}
}
