package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// deviceYard is a yard with the operator jo, the users ada and bob and the
// crew member yardmaster.
func deviceYard(t *testing.T) (store.API, *store.Store) {
	t.Helper()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if _, err := api.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return api, own
}

// A device is added with a token that is answered once and resolves to the
// device and its user; the store has the token's SHA-256 and not the token.
// A second device of the name for the same user is refused, and another
// user may have one of that name.
func TestDeviceAddAndResolve(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	d, err := api.AddDevice(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if d.User != "ada" || d.Name != "phone" || d.CreatedBy != "jo" || d.LastUsed != nil {
		t.Fatalf("the device = %+v", d.Device)
	}
	secret, ok := strings.CutPrefix(d.Token, store.DeviceTokenPrefix)
	if !ok || len(secret) != 43 {
		t.Fatalf("the token %q: want %s and 43 characters, 32 bytes in base64", d.Token, store.DeviceTokenPrefix)
	}

	_, err = api.AddDevice(ctx, "ada", "phone", "jo")
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "ada has a device phone already") {
		t.Fatalf("a second device of the name = %v", err)
	}
	other, err := api.AddDevice(ctx, "bob", "phone", "jo")
	if err != nil || other.Token == d.Token {
		t.Fatalf("another user's device of the name: %+v, %v", other, err)
	}
	if _, err := api.AddDevice(ctx, "carol", "phone", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a device of a user that is not there = %v", err)
	}
	for _, name := range []string{"", "my phone", "-phone", "a\x00b", strings.Repeat("a", 65)} {
		if _, err := api.AddDevice(ctx, "ada", name, "jo"); err == nil || !strings.Contains(err.Error(), "use letters, digits") {
			t.Errorf("AddDevice %q = %v", name, err)
		}
	}

	got, err := own.ResolveDevice(ctx, d.Token, "100.64.0.7")
	if err != nil || got == nil || got.User != "ada" || got.Name != "phone" {
		t.Fatalf("ResolveDevice = %+v, %v", got, err)
	}
	if got.LastUsed == nil || got.LastFrom != "100.64.0.7" {
		t.Fatalf("the last use was not recorded: %+v", got)
	}
	for _, token := range []string{
		"", "phone", secret, store.DeviceTokenPrefix, d.Token + "x", d.Token[:len(d.Token)-1],
		strings.ToUpper(d.Token), " " + d.Token, store.DeviceTokenPrefix + strings.Repeat("A", 43),
	} {
		if got, err := own.ResolveDevice(ctx, token, "10.0.0.1"); err != nil || got != nil {
			t.Errorf("ResolveDevice(%q) = %+v, %v; want nothing", token, got, err)
		}
	}

	list, err := api.Devices(ctx, "")
	if err != nil || len(list) != 2 || list[0].User != "ada" || list[1].User != "bob" {
		t.Fatalf("Devices = %+v, %v", list, err)
	}
	if list[0].LastUsed == nil || list[0].LastFrom != "100.64.0.7" || list[1].LastUsed != nil {
		t.Fatalf("the last use, as listed: %+v", list)
	}
	if list, err = api.Devices(ctx, "bob"); err != nil || len(list) != 1 || list[0].Name != "phone" {
		t.Fatalf("Devices of bob = %+v, %v", list, err)
	}
	if _, err := api.Devices(ctx, "carol"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Devices of a user that is not there = %v", err)
	}
	// No listing has the token or its hash.
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "token") {
		t.Fatalf("a listed device says something of its token: %s", raw)
	}

	var added []map[string]any
	evs, err := api.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "device_added" {
			added = append(added, e.Data)
		}
	}
	if raw, _ := json.Marshal(added); string(raw) != `[{"device":"phone","user":"ada"},{"device":"phone","user":"bob"}]` {
		t.Fatalf("device_added events = %s", raw)
	}
}

// The store's file has the token's SHA-256 and nowhere the token.
func TestDeviceTokenIsNotStored(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yard.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	d, err := st.AddDevice(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.ResolveDevice(ctx, d.Token, "127.0.0.1"); err != nil || got == nil {
		t.Fatalf("ResolveDevice = %+v, %v", got, err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(d.Token))
	if !bytes.Contains(raw, []byte(hex.EncodeToString(sum[:]))) {
		t.Fatal("the store does not have the token's SHA-256")
	}
	secret := strings.TrimPrefix(d.Token, store.DeviceTokenPrefix)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("the store has the token")
	}
}

// Adding a device is the operator's: anyone else is refused by name, the
// user whose device it would be too, and an agent by its pane whatever name
// the command gives. Whoever adds one decides who may speak as a person.
func TestDeviceAddIsTheOperators(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	for actor, want := range map[string]string{
		"ada":            "only the operator (jo) may add a device; this is ada",
		"bob":            "only the operator (jo) may add a device; this is bob",
		"yardmaster":     "only the operator (jo) may add a device; this is yardmaster",
		"yardr-1my8-new": "only the operator (jo) may add a device; this is yardr-1my8-new",
		"":               "only the operator (jo) may add a device; this is nobody",
	} {
		if _, err := api.AddDevice(ctx, "ada", "phone", actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AddDevice as %q = %v", actor, err)
		}
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
	pane := store.WithCaller(ctx, me)
	const want = "a device is added by the operator, outside an agent's pane: whoever adds one decides who may speak as ada; this is the crew member yardmaster"
	if _, err := own.AddDevice(pane, "ada", "phone", "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("AddDevice from the yardmaster's pane as jo = %v", err)
	}
	if list, _ := api.Devices(ctx, ""); len(list) != 0 {
		t.Fatalf("a refused device was added: %+v", list)
	}
	if _, err := own.AddDevice(ctx, "ada", "phone", "jo"); err != nil {
		t.Fatalf("the operator, from no agent's pane: %v", err)
	}
}

// A bead's agent is refused from its pane, whatever --actor says.
func TestDeviceAddRefusedFromASessionsPane(t *testing.T) {
	t.Parallel()
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	for _, actor := range []string{"jo", "ada", sess.ID} {
		if _, err := s.AddDevice(pane, "ada", "phone", actor); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
			t.Fatalf("AddDevice from a session's pane as %s = %v", actor, err)
		}
	}
	if list, _ := s.Devices(ctx, ""); len(list) != 0 {
		t.Fatalf("a refused device was added: %+v", list)
	}
}

// Removing a device is the operator's and the user's own; its token is
// refused from then on, and the name is free again with a new token.
func TestDeviceRemove(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	phone, err := api.AddDevice(ctx, "ada", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	laptop, err := api.AddDevice(ctx, "ada", "laptop", "jo")
	if err != nil {
		t.Fatal(err)
	}
	for actor, want := range map[string]string{
		"bob":        "only ada or the operator (jo) may remove a device of ada; this is bob",
		"yardmaster": "only ada or the operator (jo) may remove a device of ada; this is yardmaster",
		"":           "only ada or the operator (jo) may remove a device of ada; this is nobody",
	} {
		if _, err := api.RemoveDevice(ctx, "ada", "phone", actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("RemoveDevice as %q = %v", actor, err)
		}
	}
	if got, err := own.ResolveDevice(ctx, phone.Token, "10.0.0.1"); err != nil || got == nil {
		t.Fatalf("a refused removal took the device: %+v, %v", got, err)
	}

	gone, err := api.RemoveDevice(ctx, "ada", "phone", "ada")
	if err != nil || gone.Name != "phone" || gone.LastFrom != "10.0.0.1" {
		t.Fatalf("the user, removing their own: %+v, %v", gone, err)
	}
	if got, err := own.ResolveDevice(ctx, phone.Token, "10.0.0.1"); err != nil || got != nil {
		t.Fatalf("a removed device's token resolves: %+v, %v", got, err)
	}
	if got, err := own.ResolveDevice(ctx, laptop.Token, "10.0.0.1"); err != nil || got == nil || got.Name != "laptop" {
		t.Fatalf("the user's other device: %+v, %v", got, err)
	}
	if _, err := api.RemoveDevice(ctx, "ada", "phone", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing it again = %v", err)
	}
	if _, err := api.RemoveDevice(ctx, "ada", "laptop", "jo"); err != nil {
		t.Fatalf("the operator: %v", err)
	}

	again, err := api.AddDevice(ctx, "ada", "phone", "jo")
	if err != nil || again.Token == phone.Token {
		t.Fatalf("the name, added again: %+v, %v", again, err)
	}
	if got, err := own.ResolveDevice(ctx, phone.Token, "10.0.0.1"); err != nil || got != nil {
		t.Fatalf("the old token resolves to the new device: %+v, %v", got, err)
	}

	var removed []string
	evs, err := api.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "device_removed" {
			raw, _ := json.Marshal(e.Data)
			removed = append(removed, e.Actor+" "+string(raw))
		}
	}
	if got := strings.Join(removed, "\n"); got != `ada {"device":"phone","user":"ada"}`+"\n"+`jo {"device":"laptop","user":"ada"}` {
		t.Fatalf("device_removed events:\n%s", got)
	}
}

// Removing a user removes their devices: the tokens are refused, the event
// names the devices, and a later user of the name has none.
func TestUserRemovalTakesTheDevices(t *testing.T) {
	t.Parallel()
	api, own := deviceYard(t)
	ctx := context.Background()
	var tokens []string
	for _, name := range []string{"phone", "laptop"} {
		d, err := api.AddDevice(ctx, "ada", name, "jo")
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, d.Token)
	}
	bobs, err := api.AddDevice(ctx, "bob", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		if got, err := own.ResolveDevice(ctx, token, "10.0.0.1"); err != nil || got != nil {
			t.Fatalf("a removed user's token resolves: %+v, %v", got, err)
		}
	}
	if got, err := own.ResolveDevice(ctx, bobs.Token, "10.0.0.1"); err != nil || got == nil || got.User != "bob" {
		t.Fatalf("another user's device: %+v, %v", got, err)
	}
	evs, err := api.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var data string
	for _, e := range evs {
		if e.Kind == "user_removed" {
			raw, _ := json.Marshal(e.Data)
			data = string(raw)
		}
	}
	if data != `{"devices":["laptop","phone"],"user":"ada"}` {
		t.Fatalf("user_removed = %s", data)
	}
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if list, err := api.Devices(ctx, "ada"); err != nil || len(list) != 0 {
		t.Fatalf("the devices of a later ada = %+v, %v", list, err)
	}
}

// A device is unused once 30 days have passed since its last use, or since
// it was added when it never was used.
func TestDeviceUnused(t *testing.T) {
	t.Parallel()
	added := time.Date(2026, 10, 12, 19, 2, 0, 0, time.UTC)
	used := added.Add(10 * 24 * time.Hour)
	d := store.Device{CreatedAt: added}
	if d.Unused(added.Add(store.DeviceUnusedAfter-time.Second)) || !d.Unused(added.Add(store.DeviceUnusedAfter)) {
		t.Fatal("a device never used: unused 30 days after it was added, and not before")
	}
	d.LastUsed = &used
	if d.Unused(added.Add(store.DeviceUnusedAfter)) || !d.Unused(used.Add(store.DeviceUnusedAfter)) {
		t.Fatal("a device that was used: unused 30 days after its last use, and not before")
	}
}
