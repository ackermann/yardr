package cli

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

var deviceToken = regexp.MustCompile(`yardr-dev-[A-Za-z0-9_-]{43}`)

type deviceSettingsFailure struct {
	store.API
	added bool
}

func (f *deviceSettingsFailure) Settings(context.Context) (map[string]string, error) {
	return nil, errors.New("settings unavailable")
}

func (f *deviceSettingsFailure) AddDevice(context.Context, string, string, string) (*store.NewDevice, error) {
	f.added = true
	return nil, nil
}

// If a precondition needed for the output fails, device add must fail before
// creating a device whose one-time token it cannot show.
func TestDeviceAddReadsSettingsBeforeCreating(t *testing.T) {
	t.Parallel()
	failure := &deviceSettingsFailure{API: userYard(t)}
	if out, err := as(t, failure, "jo", "user", "device", "add", "jo", "phone"); err == nil || !strings.Contains(err.Error(), "settings unavailable") {
		t.Fatalf("device add = %v\n%s", err, out)
	}
	if failure.added {
		t.Fatal("device add created a device before settings were available")
	}
}

// yardr user device add prints the token once; list, user show and status
// have the device and never the token; rm ends it. Only the operator adds
// one, and the operator and the user remove one.
func TestDeviceCommands(t *testing.T) {
	t.Parallel()
	_, own := storetest.NewOwned(t)
	st := userYardOn(t, own)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "ada")

	if out := mustAs(t, st, "jo", "user", "device", "list"); out != "no devices (yardr user device add <user> <device>)\n" {
		t.Fatalf("list, with none:\n%s", out)
	}
	out := mustAs(t, st, "jo", "user", "device", "add", "jo", "phone")
	token := deviceToken.FindString(out)
	want := "device phone of jo. Its token, shown once:\n\n  " + token + "\n\n" +
		"Give it to the device with this yard's address, the one the device will reach it at.\n" +
		"It calls as jo until: yardr user device rm jo phone\n"
	if token == "" || out != want {
		t.Fatalf("device add:\n%s\nwant:\n%s", out, want)
	}
	if d, err := own.ResolveDevice(ctx, token, "100.64.0.7"); err != nil || d == nil || d.User != "jo" || d.Name != "phone" {
		t.Fatalf("the printed token resolves to %+v, %v", d, err)
	}

	// A user who is not the operator gets one too, and is told what it reads.
	out = mustAs(t, st, "jo", "user", "device", "add", "ada", "laptop", "--url", "https://mac.example.net")
	wantText(t, out,
		"Give it to the device with this yard's address: https://mac.example.net\n",
		"It calls as ada until: yardr user device rm ada laptop\n",
		"ada is not the operator, and the device reads every bead in the yard, with its notes, and the mail of ada alone.\n")
	adas := deviceToken.FindString(out)

	var added struct {
		User, Name, Token, URL string
	}
	out = mustAs(t, st, "jo", "user", "device", "add", "ada", "tablet", "--url", "https://mac.example.net", "--json")
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.User != "ada" || added.Name != "tablet" ||
		!deviceToken.MatchString(added.Token) || added.URL != "https://mac.example.net" {
		t.Fatalf("device add --json: %+v, %v\n%s", added, err, out)
	}

	refusedAs(t, st, "jo", "ada has a device laptop already", "user", "device", "add", "ada", "laptop")
	refusedAs(t, st, "ada", "only the operator (jo) may add a device; this is ada", "user", "device", "add", "ada", "watch")
	refusedAs(t, st, "yardmaster", "only the operator (jo) may add a device; this is yardmaster", "user", "device", "add", "jo", "watch")
	refusedAs(t, st, "jo", "user carol: not found", "user", "device", "add", "carol", "phone")

	used := time.Now().Local().Format(mailTime)
	list := mustAs(t, st, "jo", "user", "device", "list")
	wantText(t, list, "USER  DEVICE  ADDED", "LAST USED\n", "  laptop  ", "  never\n", "jo    phone   ", " from 100.64.0.7\n")
	if got := strings.Count(list, "\n"); got != 4 {
		t.Fatalf("list has %d lines, want the heading and three devices:\n%s", got, list)
	}
	if out := mustAs(t, st, "ada", "user", "device", "list", "jo"); strings.Contains(out, "laptop") || !strings.Contains(out, "phone") {
		t.Fatalf("list jo:\n%s", out)
	}
	show := mustAs(t, st, "jo", "user", "show", "jo")
	if !regexp.MustCompile(`(?m)^  device: phone, added \d\d-\d\d \d\d:\d\d, last used \d\d-\d\d \d\d:\d\d from 100\.64\.0\.7$`).MatchString(show) {
		t.Fatalf("user show (used about %s):\n%s", used, show)
	}
	wantText(t, mustAs(t, st, "jo", "user", "show", "ada"), "  device: laptop, added ", ", last used never\n", "  device: tablet, added ")
	for _, args := range [][]string{
		{"user", "device", "list"}, {"user", "device", "list", "--json"}, {"user", "show", "jo"}, {"user", "show", "ada", "--json"},
		{"user", "list", "--json"}, {"events", "--json"},
	} {
		if out := mustAs(t, st, "jo", args...); strings.Contains(out, "yardr-dev-") || strings.Contains(out, "token") {
			t.Fatalf("yardr %s says something of a token:\n%s", strings.Join(args, " "), out)
		}
	}

	refusedAs(t, st, "ada", "only jo or the operator (jo) may remove a device of jo; this is ada", "user", "device", "rm", "jo", "phone")
	refusedAs(t, st, "yardmaster", "may remove a device of ada; this is yardmaster", "user", "device", "rm", "ada", "laptop")
	if out := mustAs(t, st, "ada", "user", "device", "rm", "ada", "laptop"); out != "device laptop of ada removed: its token is refused from now on, and the streams it held are closed\n" {
		t.Fatalf("rm, by the user:\n%s", out)
	}
	if d, err := own.ResolveDevice(ctx, adas, "100.64.0.7"); err != nil || d != nil {
		t.Fatalf("a removed device's token resolves: %+v, %v", d, err)
	}
	mustAs(t, st, "jo", "user", "device", "rm", "jo", "phone")
	refusedAs(t, st, "jo", "device phone of jo: not found", "user", "device", "rm", "jo", "phone")

	// Removing the user removes what is left of theirs.
	mustAs(t, st, "jo", "user", "rm", "ada")
	if d, err := own.ResolveDevice(ctx, added.Token, "100.64.0.7"); err != nil || d != nil {
		t.Fatalf("a removed user's token resolves: %+v, %v", d, err)
	}
	if out := mustAs(t, st, "jo", "user", "device", "list"); !strings.HasPrefix(out, "no devices") {
		t.Fatalf("list, after the user was removed:\n%s", out)
	}
}

// agedDevices is a store whose devices are as old as a test says: the
// store's own are added now.
type agedDevices struct {
	store.API
	devices []store.Device
}

func (a agedDevices) Devices(context.Context, string) ([]store.Device, error) { return a.devices, nil }

// yardr doctor notes a device that has not been used for 30 days, and one
// that never was 30 days after it was added; it says nothing of one in use.
func TestDoctorNotesAnUnusedDevice(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	now := time.Now()
	added := now.Add(-40 * 24 * time.Hour)
	old, recent := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	aged := agedDevices{st, []store.Device{
		{User: "jo", Name: "phone", CreatedAt: added, LastUsed: &old, LastFrom: "100.64.0.7"},
		{User: "jo", Name: "laptop", CreatedAt: added, LastUsed: &recent, LastFrom: "100.64.0.7"},
		{User: "jo", Name: "watch", CreatedAt: added},
		{User: "jo", Name: "new", CreatedAt: now},
	}}
	out, _ := as(t, aged, "jo", "doctor")
	wantText(t, out,
		"device phone of jo has not been used for 30 days (last used "+old.Local().Format(mailTime)+" from 100.64.0.7), and its token still calls as jo. "+
			"If it is no longer in use: ",
		" user device rm jo phone\n",
		"device watch of jo has not been used for 30 days (never used, added "+added.Local().Format(mailTime)+"), and its token still calls as jo.")
	for _, name := range []string{"laptop", "new"} {
		if strings.Contains(out, "device "+name) {
			t.Errorf("doctor notes the device %s, which is in use or new:\n%s", name, out)
		}
	}
	if !regexp.MustCompile(`note\s+names\s+device phone`).MatchString(out) {
		t.Errorf("the note is not one of the names notes:\n%s", out)
	}
}
