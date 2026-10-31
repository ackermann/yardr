package store

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// Teammates lists one row per address of the standing mapping, joined to
// its user and devices: the name, role, email, invited-by and when, and
// each device's name and last use. A mapped name with no user record is
// omitted, not a row; an empty mapping lists none.
func TestTeammates(t *testing.T) {
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y := teammateYard(t)
	if err := y.InviteTeammate(y.ctx, "eve", "eve@example.net", RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	phone, err := y.AddDevice(y.ctx, "eve", "phone", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.ResolveDevice(y.ctx, phone.Token, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetSetting(y.ctx, WebJWTUsersSetting, "eve@example.net=eve, ghost@example.net=ghost, mckean@example.net=mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	y.SetLogger(slog.New(slog.NewTextHandler(&logged, nil)))
	got, err := y.Teammates(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "eve" || got[1].Name != "mckean" {
		t.Fatalf("teammates = %+v, want eve's and mckean's rows in address order, and no row for ghost", got)
	}
	eve := got[0]
	if eve.Role != RoleMember || eve.Email != "eve@example.net" || eve.InvitedBy != "mckean" || !eve.InvitedAt.Equal(y.now) {
		t.Errorf("eve's row = %+v, want member eve@example.net invited by mckean at %v", eve, y.now)
	}
	if len(eve.Devices) != 1 || eve.Devices[0].Name != "phone" || eve.Devices[0].LastUsed == nil || !eve.Devices[0].LastUsed.Equal(y.now) {
		t.Errorf("eve's devices = %+v, want phone last used at %v", eve.Devices, y.now)
	}
	ops := got[1]
	if ops.Role != RoleOperator || ops.Email != "mckean@example.net" || ops.InvitedBy != "mckean" || !ops.InvitedAt.Equal(y.now) {
		t.Errorf("mckean's row = %+v, want operator mckean@example.net invited by mckean at %v", ops, y.now)
	}
	if len(ops.Devices) != 0 {
		t.Errorf("mckean's devices = %+v, want none: a row without is waiting", ops.Devices)
	}
	if log := logged.String(); !strings.Contains(log, "teammate mapping has no user record") ||
		!strings.Contains(log, "user=ghost") || strings.Contains(log, "ghost@example.net") {
		t.Errorf("orphan mapping log = %q, want the missing user named without its email", log)
	}
	y2 := teammateYard(t)
	if got, err := y2.Teammates(y2.ctx); err != nil || len(got) != 0 {
		t.Fatalf("teammates with no mapping = %+v, %v, want none", got, err)
	}
}

// UninviteTeammate files the removal as the run ask `user uninvite <user>`
// on a new bead, addressed to the yard's operator: the card a
// `yardr bead ask --run` makes. An unknown user, an unmapped one and the
// caller's own are refused with their reasons, and file nothing.
func TestUninviteTeammate(t *testing.T) {
	t.Parallel()
	y := teammateYard(t)
	if err := y.InviteTeammate(y.ctx, "eve", "eve@example.net", RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	filed, err := y.UninviteTeammate(y.ctx, "eve", "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if filed.Bead == "" || filed.Ask != 1 {
		t.Fatalf("filed = %+v, want the new bead and its first ask", filed)
	}
	notes, err := y.Notes(y.ctx, filed.Bead)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Kind != NoteAsk || notes[0].Author != "mckean" {
		t.Fatalf("notes on the new bead = %+v, want the one ask by mckean", notes)
	}
	ask, err := ParseAsk(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	if ask.Question != "Remove eve as a teammate?" || ask.To != "mckean" || len(ask.Context) != 1 {
		t.Errorf("the ask = %+v, want the removal question to mckean with its context", ask)
	}
	if ask.Run == nil || !slices.Equal(ask.Run.Argv, []string{"user", "uninvite", "eve"}) {
		t.Fatalf("the ask runs %+v, want exactly user uninvite eve", ask.Run)
	}
	if !strings.Contains(strings.Join(ask.Run.Does, " "), "reports the removal to the edge first") {
		t.Errorf("the card lacks the edge-first removal:\n%s", strings.Join(ask.Run.Does, "\n"))
	}
	// A member files the same card on the socket, to the operator.
	if _, err := y.UninviteTeammate(y.ctx, "eve", "ada"); err != nil {
		t.Errorf("a member filing a removal = %v", err)
	}
	head, err := y.Head(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, actor, want string }{
		{"nobody", "mckean", "user nobody"},
		{"nobody", "mckean", "not found"},
		{"ada", "mckean", "is no teammate"},
		{"ada", "mckean", "yardr user rm ada"},
		{"mckean", "mckean", "is the caller"},
	} {
		if _, err := y.UninviteTeammate(y.ctx, c.user, c.actor); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("uninviting %s as %s = %v, want %q", c.user, c.actor, err, c.want)
		}
	}
	if now, _ := y.Head(y.ctx); now != head {
		t.Errorf("the refused filings wrote %d events", now-head)
	}
}
