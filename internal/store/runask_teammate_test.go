package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	"yardr.dev/yardr/internal/yardkey"
)

// teammateYard is a run yard connected to an edge as mckean/edgeyard.
func teammateYard(t *testing.T) *askYard {
	t.Helper()
	y, _ := runYard(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge := Edge{URL: "https://my.yardr.dev", Key: yardkey.Public(key), Handle: "mckean", Name: "edgeyard"}
	if err := y.SetSetting(y.ctx, EdgeSetting, edge.String(), "mckean"); err != nil {
		t.Fatal(err)
	}
	return y
}

func teammateAsk(command string) Ask {
	return Ask{Question: "Invite them?", To: "mckean", Context: []string{"they work with us daily"}, Run: &AskRun{Argv: []string{command}}}
}

// An invite is stored in the yard's form, with the operator sentence
// only where the role is an operator's, and its approval records
// CarryPending with a carrying record and writes no user or mapping
// itself: the server carries it out after the approval.
func TestInviteCardAndApprovalDefersToCarrier(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := teammateYard(t)
	seq := y.ask(t, teammateAsk("user invite eve --email Eve@Example.net --role operator"), "yardmaster")
	got := y.asked(t, seq).Ask.Run
	if got == nil || !slices.Equal(got.Argv, []string{"user", "invite", "eve", "--email", "eve@example.net", "--role", "operator"}) {
		t.Fatalf("the invite stores %+v", got)
	}
	joined := strings.Join(got.Does, " ")
	for _, want := range []string{"adds the user eve with the role operator", "maps eve@example.net to eve in web_jwt_users", "reports the invite to the edge for the yard mckean/edgeyard (member add)", "can approve and administer this yard"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the invite card lacks %q:\n%s", want, joined)
		}
	}
	if got.Back != "yardr user uninvite eve" {
		t.Errorf("the invite's way back = %q", got.Back)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the approval records %q, want CarryPending", done.Result)
	}
	if _, err := y.GetUser(y.ctx, "eve"); err == nil {
		t.Fatal("the approval itself added the user: the carrier does that after it")
	}
	if all, _ := y.Settings(y.ctx); all[WebJWTUsersSetting] != "" {
		t.Fatalf("the approval itself wrote the mapping: %q", all[WebJWTUsersSetting])
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved || rec.Actor != "mckean" || !slices.Equal(rec.TeammateEmails, []string{"eve@example.net"}) {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	if pending, _ := y.PendingCarry(y.ctx); !pending {
		t.Fatal("the approved invite does not pend carrying out")
	}
	// A member invite carries no operator sentence, and an address
	// with a '+' is one word of it.
	y2 := teammateYard(t)
	seq2 := y2.ask(t, teammateAsk("user invite eve --email eve+team@example.net"), "yardmaster")
	if got := y2.asked(t, seq2).Ask.Run; !slices.Equal(got.Argv, []string{"user", "invite", "eve", "--email", "eve+team@example.net"}) {
		t.Fatalf("a member invite with a + address stores %+v", got)
	}
	if joined := strings.Join(y2.asked(t, seq2).Ask.Run.Does, " "); strings.Contains(joined, "can approve and administer") {
		t.Errorf("a member invite claims the operator's powers:\n%s", joined)
	}
	// An approval is held to the mapping it was carded against.
	if err := y2.SetSetting(y2.ctx, WebJWTUsersSetting, "other@example.net=ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y2.ApproveAsk(y2.ctx, y2.bead, seq2, "mckean", true, nil); err == nil ||
		!strings.Contains(err.Error(), "the mapping was changed since") {
		t.Fatalf("approving after the mapping changed = %v", err)
	}

	// A custom policy may allow adding a user but refuse changing a role.
	// An operator invite must pass both gates at approval.
	y3 := teammateYard(t)
	seq3 := y3.ask(t, teammateAsk("user invite eve --email eve@example.net --role operator"), "yardmaster")
	denied := errors.New("role changes are disabled")
	y3.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActChangeRole {
			return denied
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := y3.ApproveAsk(y3.ctx, y3.bead, seq3, "mckean", true, nil); !errors.Is(err, denied) {
		t.Fatalf("approving an operator invite under a policy that refuses role changes = %v", err)
	}
	if _, err := y3.GetUser(y3.ctx, "eve"); err == nil {
		t.Fatal("an operator invite wrote the user after the role gate refused")
	}
}

// The invite card refuses, before any write, a taken user or crew name,
// an address already mapped to anyone, and a yard with no edge.
func TestInviteCardRefusals(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := teammateYard(t)
	if err := y.SetSetting(y.ctx, WebJWTUsersSetting, "ada@example.net=ada", "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ command, want string }{
		{"user invite ada --email new@example.net", "there is a user ada already"},
		{"user invite yardmaster --email new@example.net", "that is a crew member's name"},
		{"user invite eve --email ada@example.net", "is mapped to ada in web_jwt_users already"},
		{"user invite eve --email ADA@example.net", "is mapped to ada in web_jwt_users already"},
	} {
		if _, err := y.AddAsk(y.ctx, y.bead, teammateAsk(c.command), "yardmaster"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s = %v, want %q", c.command, err, c.want)
		}
	}
	if notes, _ := y.Notes(y.ctx, y.bead); len(notes) != 0 {
		t.Fatalf("refused invites left %d notes", len(notes))
	}
	// No edge: the card names the missing edge.
	y2, _ := runYard(t)
	if _, err := y2.AddAsk(y2.ctx, y2.bead, teammateAsk("user invite eve --email eve@example.net"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "connected to no edge") {
		t.Fatalf("an invite with no edge = %v", err)
	}
}

// The carrier's invite write makes the user with its role and the
// mapping in one go, and taken up again it writes neither twice.
func TestInviteTeammateWriteIsIdempotent(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := teammateYard(t)
	if err := y.InviteTeammate(y.ctx, "eve", "eve@example.net", RoleOperator, "mckean"); err != nil {
		t.Fatal(err)
	}
	u, err := y.GetUser(y.ctx, "eve")
	if err != nil || !u.IsOperator() {
		t.Fatalf("the invited user = %+v, %v", u, err)
	}
	if all, _ := y.Settings(y.ctx); all[WebJWTUsersSetting] != "eve@example.net=eve" {
		t.Fatalf("the mapping = %q", all[WebJWTUsersSetting])
	}
	if events, err := y.Events(y.ctx, "", 100); err != nil {
		t.Fatal(err)
	} else {
		roleRecorded := false
		for _, event := range events {
			if event.Kind == "user_set" && event.Data["user"] == "eve" && event.Data[UserRole] == RoleOperator {
				roleRecorded = true
			}
		}
		if !roleRecorded {
			t.Fatal("the operator role was written without a user_set event")
		}
	}
	if err := y.InviteTeammate(y.ctx, "eve", "eve@example.net", RoleOperator, "mckean"); err != nil {
		t.Fatalf("a taken-up invite writes again: %v", err)
	}
	// An address mapped to another user is never re-pointed.
	if err := y.InviteTeammate(y.ctx, "ada", "eve@example.net", RoleMember, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "never re-points it") {
		t.Fatalf("re-pointing an address = %v", err)
	}

	y2 := teammateYard(t)
	denied := errors.New("role changes are disabled")
	y2.SetPolicy(storePolicyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act Action, obj Object) error {
		if act == ActChangeRole {
			return denied
		}
		return CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if err := y2.InviteTeammate(y2.ctx, "eve", "eve@example.net", RoleOperator, "mckean"); !errors.Is(err, denied) {
		t.Fatalf("carrying an operator invite under a policy that refuses role changes = %v", err)
	}
	if _, err := y2.GetUser(y2.ctx, "eve"); err == nil {
		t.Fatal("the denied operator invite left a user behind")
	}
	if all, _ := y2.Settings(y2.ctx); all[WebJWTUsersSetting] != "" {
		t.Fatalf("the denied operator invite left a mapping: %q", all[WebJWTUsersSetting])
	}
}

// An uninvite card refuses a plain user and a user who is not found, is
// filed by the operator as by anyone, holds the user and the mapping
// entry in Seen, and its carrier write removes user, devices and mapping
// together and is safe taken up again.
func TestUninviteCardAndRemovalWrite(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := teammateYard(t)
	if err := y.InviteTeammate(y.ctx, "eve", "eve@example.net", RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddDevice(y.ctx, "eve", "phone", "mckean"); err != nil {
		t.Fatal(err)
	}
	// A plain user (ada) is user rm's, not uninvite's.
	if _, err := y.AddAsk(y.ctx, y.bead, teammateAsk("user uninvite ada"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "is no teammate") || !strings.Contains(err.Error(), "yardr user rm ada") {
		t.Fatalf("uninviting a plain user = %v", err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, teammateAsk("user uninvite nobody"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("uninviting nobody = %v", err)
	}
	// The operator files the same card: with no terminal verb the card
	// is the only way in, and no check refuses them with "run it
	// yourself".
	if _, err := y.AddAsk(y.ctx, y.bead, teammateAsk("user uninvite eve"), "mckean"); err != nil {
		t.Fatalf("the operator filing a removal = %v", err)
	}
	// The user the operator setting names is kept, as user rm keeps them.
	y2 := teammateYard(t)
	if err := y2.SetSetting(y2.ctx, WebJWTUsersSetting, "mckean@example.net=mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y2.AddAsk(y2.ctx, y2.bead, teammateAsk("user uninvite mckean"), "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "is the yard's operator") {
		t.Fatalf("uninviting the yard's operator = %v", err)
	}
	seq := y.ask(t, teammateAsk("user uninvite eve"), "yardmaster")
	got := y.asked(t, seq).Ask.Run
	if got.Seen["user eve"] == "" || got.Seen["web_jwt_users eve"] == "" {
		t.Fatalf("the removal's Seen holds %+v, want the user and the mapping entry", got.Seen)
	}
	if !strings.Contains(strings.Join(got.Does, " "), "reports the removal to the edge first") {
		t.Fatalf("the removal card:\n%s", strings.Join(got.Does, "\n"))
	}
	if got.Back != "yardr user invite eve --email eve@example.net" {
		t.Fatalf("the removal's way back = %q", got.Back)
	}
	done, err := y.ApproveAsk(y.ctx, y.bead, seq, "mckean", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the approval records %q, want CarryPending", done.Result)
	}
	if _, err := y.GetUser(y.ctx, "eve"); err != nil {
		t.Fatal("the approval itself removed the user: the carrier does that after it")
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || !slices.Equal(rec.TeammateEmails, []string{"eve@example.net"}) {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	if err := y.RemoveTeammate(y.ctx, "eve", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GetUser(y.ctx, "eve"); err == nil {
		t.Fatal("the user stands after the removal write")
	}
	if devices, _ := y.Devices(y.ctx, "eve"); len(devices) != 0 {
		t.Fatalf("devices stand after the removal: %+v", devices)
	}
	if all, _ := y.Settings(y.ctx); all[WebJWTUsersSetting] != "" {
		t.Fatalf("the mapping stands after the removal: %q", all[WebJWTUsersSetting])
	}
	if err := y.RemoveTeammate(y.ctx, "eve", "mckean"); err != nil {
		t.Fatalf("a taken-up removal writes again: %v", err)
	}
}

// A teammate carry left claimed while its report is owed does not hold
// up what was approved after it: the next claim takes the waiting apply,
// and the owed report again once nothing else waits.
func TestOwedTeammateCarryLetsOthersPass(t *testing.T) {
	// Sequential: the package's fake clock (via newAskYard).
	y := teammateYard(t)
	y.setPack(&fakePack{label: "embedded in yardr test1"})
	invite := y.ask(t, teammateAsk("user invite eve --email eve@example.net"), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, invite, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	if claimed, err := y.ClaimCarry(y.ctx, "boot-one"); err != nil || claimed == nil || claimed.Ask != invite {
		t.Fatalf("the invite's claim = %+v, %v", claimed, err)
	}
	apply := y.ask(t, packAsk(), "yardmaster")
	if _, err := y.ApproveAsk(y.ctx, y.bead, apply, "mckean", true, nil); err != nil {
		t.Fatal(err)
	}
	if claimed, err := y.ClaimCarry(y.ctx, "boot-one"); err != nil || claimed == nil || claimed.Ask != apply {
		t.Fatalf("with the invite's report owed the next claim = %+v, %v, want the apply approved after it", claimed, err)
	}
	if err := y.CompleteCarry(y.ctx, y.bead, apply, "boot-one", "applied"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := y.ClaimCarry(y.ctx, "boot-one"); err != nil || claimed == nil || claimed.Ask != invite {
		t.Fatalf("once nothing else waits the claim = %+v, %v, want the owed invite", claimed, err)
	}
}
