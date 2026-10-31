package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// memberEdge is a stand-in edge that records, for each member report,
// whether the yard user and the mapping stood at the moment it heard
// the report, and that a test can take down (no answer at all) and
// bring back at the same address.
type memberEdge struct {
	edge *standin.Edge
	st   *store.Store

	mu     sync.Mutex
	down   bool
	atCall []string
}

func (m *memberEdge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == edgelink.MemberAddPath || r.URL.Path == edgelink.MemberRemovePath {
		m.mu.Lock()
		down := m.down
		m.mu.Unlock()
		if down {
			// No answer at all: the report cannot land.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
			return
		}
		_, userErr := m.st.GetUser(context.Background(), "eve")
		all, _ := m.st.Settings(context.Background())
		m.mu.Lock()
		m.atCall = append(m.atCall, fmt.Sprintf("%s user=%v mapping=%v", r.URL.Path, userErr == nil,
			strings.Contains(all[store.WebJWTUsersSetting], "eve@example.net=eve")))
		m.mu.Unlock()
	}
	m.edge.ServeHTTP(w, r)
}

func (m *memberEdge) setDown(down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
}

func (m *memberEdge) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.atCall...)
}

// teammateCarrierYard is a carry yard connected to a stand-in edge,
// with a carrier that trusts it.
func teammateCarrierYard(t *testing.T) (*store.Store, string, string, *standin.Edge, *memberEdge, *carrier, *httptest.Server) {
	t.Helper()
	st, home, bead := carryYard(t)
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &memberEdge{edge: edge, st: st}
	srv := httptest.NewTLSServer(wrapped)
	t.Cleanup(srv.Close)
	if _, _, err := yardkey.Ensure(config.At(home).PeerKey()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.EdgeSetting, store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean", Name: "edgeyard"}.String(), "mckean"); err != nil {
		t.Fatal(err)
	}
	c := testCarrier(st, home, "boot-one")
	c.edgeHTTP = srv.Client()
	return st, home, bead, edge, wrapped, c, srv
}

func inviteAsk(command string) store.Ask {
	return store.Ask{Question: "Invite eve?", To: "mckean", Context: []string{"they work with us daily"},
		Run: &store.AskRun{Argv: []string{command}}}
}

// Approving an invite and carrying it out makes the user and the
// mapping before the edge hears the report, and the card reads as
// carried out only once the report lands.
func TestCarryInviteReportsAfterUserAndMapping(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, wrapped, c, _ := teammateCarrierYard(t)
	seq := askApprove(t, st, bead, inviteAsk("user invite eve --email eve@example.net"))
	if _, err := st.GetUser(ctx, "eve"); err == nil {
		t.Fatal("the approval itself added the user")
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "being carried out") {
		t.Fatalf("before the report lands the card reads:\n%s", lines)
	}
	if did, err := c.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext = %v, %v", did, err)
	}
	u, err := st.GetUser(ctx, "eve")
	if err != nil || u.IsOperator() {
		t.Fatalf("the invited user = %+v, %v, want a member", u, err)
	}
	if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "eve@example.net=eve" {
		t.Fatalf("the mapping = %q", all[store.WebJWTUsersSetting])
	}
	adds := edge.State().MemberAdds
	if len(adds) != 1 || adds[0].YardHandle != "mckean" || adds[0].YardName != "edgeyard" || adds[0].Email != "eve@example.net" ||
		adds[0].YardUser != "eve" || adds[0].InvitedBy != "mckean" || adds[0].YardKey == "" || adds[0].Key != adds[0].YardKey {
		t.Fatalf("the edge took %+v, want one signed add naming this yard, its user and the approver", adds)
	}
	if calls := wrapped.calls(); len(calls) != 1 || calls[0] != "/member/add user=true mapping=true" {
		t.Fatalf("at the report the yard stood as %v, want the user and the mapping already standing", calls)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "approved, ran: yardr user invite eve") {
		t.Fatalf("the carried-out card reads:\n%s", lines)
	}

	// An operator invite makes an operator.
	st2, _, bead2, _, _, c2, _ := teammateCarrierYard(t)
	askApprove(t, st2, bead2, inviteAsk("user invite eve --email eve@example.net --role operator"))
	if did, err := c2.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext (operator) = %v, %v", did, err)
	}
	if u, err := st2.GetUser(ctx, "eve"); err != nil || !u.IsOperator() {
		t.Fatalf("the operator invite made %+v, %v", u, err)
	}
}

// An invite approved while the edge answers nothing leaves the user
// and the mapping standing and the claim owed, and lands when the edge
// answers again.
func TestCarryInviteEdgeDownLeavesYardWritesAndRetries(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, wrapped, c, _ := teammateCarrierYard(t)
	seq := askApprove(t, st, bead, inviteAsk("user invite eve --email eve@example.net"))
	wrapped.setDown(true)
	if did, err := c.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with the edge down = %v, %v, want it taken and still owed", did, err)
	}
	if _, err := st.GetUser(ctx, "eve"); err != nil {
		t.Fatalf("the user does not stand while the report is owed: %v", err)
	}
	if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "eve@example.net=eve" {
		t.Fatalf("the mapping does not stand while the report is owed: %q", all[store.WebJWTUsersSetting])
	}
	if n := len(edge.State().MemberAdds); n != 0 {
		t.Fatalf("the edge took %d adds while down", n)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryClaimed {
		t.Fatalf("the record = %+v, want the claim left standing", rec)
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "being carried out") || strings.Contains(lines, "approved, failed") {
		t.Fatalf("while the report is owed the card reads:\n%s", lines)
	}
	wrapped.setDown(false)
	if did, err := c.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext when the edge returns = %v, %v", did, err)
	}
	if n := len(edge.State().MemberAdds); n != 1 {
		t.Fatalf("the edge took %d adds after it returned, want 1", n)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
}

// Approving an uninvite reports the removal before the user goes, and
// leaves no user, device or mapping.
func TestCarryUninviteReportsBeforeUserGoes(t *testing.T) {
	ctx := context.Background()
	st, home, bead, edge, wrapped, c, srv := teammateCarrierYard(t)
	if err := st.InviteTeammate(ctx, "eve", "eve@example.net", store.RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	yard, err := store.Load(config.At(home).PeerKey())
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := st.Settings(ctx)
	edgeRow, _, _ := store.EdgeOf(settings)
	client := &edgelink.Client{URL: srv.URL, HTTP: srv.Client()}
	if err := client.MemberAdd(ctx, edgeRow.Key, yard, edgelink.MemberAdd{YardHandle: "mckean", YardName: "edgeyard", Email: "eve@example.net", YardUser: "eve", InvitedBy: "mckean", YardKey: store.Public(yard)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddDevice(ctx, "eve", "phone", "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := askApprove(t, st, bead, inviteAsk("user uninvite eve"))
	if did, err := c.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext = %v, %v", did, err)
	}
	if calls := wrapped.calls(); len(calls) != 2 || calls[1] != "/member/remove user=true mapping=true" {
		t.Fatalf("at the reports the yard stood as %v, want the removal heard while the user stood", calls)
	}
	if n := len(edge.State().MemberRemoves); n != 1 {
		t.Fatalf("the edge took %d removes", n)
	}
	if _, err := st.GetUser(ctx, "eve"); err == nil {
		t.Fatal("the user stands after the removal")
	}
	if devices, _ := st.Devices(ctx, "eve"); len(devices) != 0 {
		t.Fatalf("devices stand after the removal: %+v", devices)
	}
	if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "" {
		t.Fatalf("the mapping stands after the removal: %q", all[store.WebJWTUsersSetting])
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
}

// An uninvite whose edge answers nothing still removes the user and
// the mapping, and its report lands when the edge answers again.
func TestCarryUninviteEdgeDownStillRemovesAndRetries(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, wrapped, c, _ := teammateCarrierYard(t)
	if err := st.InviteTeammate(ctx, "eve", "eve@example.net", store.RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := askApprove(t, st, bead, inviteAsk("user uninvite eve"))
	wrapped.setDown(true)
	if did, err := c.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with the edge down = %v, %v, want it taken and still owed", did, err)
	}
	if _, err := st.GetUser(ctx, "eve"); err == nil {
		t.Fatal("the user stands while the report is owed: they must act as nobody")
	}
	if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "" {
		t.Fatalf("the mapping stands while the report is owed: %q", all[store.WebJWTUsersSetting])
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryClaimed {
		t.Fatalf("the record = %+v, want the claim left standing", rec)
	}
	wrapped.setDown(false)
	if did, err := c.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext when the edge returns = %v, %v", did, err)
	}
	if n := len(edge.State().MemberRemoves); n != 1 {
		t.Fatalf("the edge took %d removes after it returned, want 1 and no second removal as an error", n)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
}

// A signed refusal waiting cannot cure fails the carry with the
// edge's words, for either report, and is distinguished from a signed
// 429 or 503 that leaves the report owed past its pause: the record
// keeps the edge's words with the next time it may be tried, nothing is
// signed again until then, and the report lands after.
func TestCarryMemberRefusalFailsWithEdgeWords(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, _, c, _ := teammateCarrierYard(t)
	edge.Behave(func(b *standin.EdgeBehaviour) {
		b.MemberRefuse, b.MemberReason = http.StatusUnprocessableEntity, "the address has an unverified account at the edge"
	})
	seq := askApprove(t, st, bead, inviteAsk("user invite eve --email eve@example.net"))
	if did, err := c.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext = %v, %v", did, err)
	}
	rec := carryState(t, st, bead, seq)
	if rec.State != store.CarryFailed || !strings.Contains(rec.Result, "the address has an unverified account at the edge") {
		t.Fatalf("the record = %+v, want failed with the edge's words", rec)
	}
	if lines := resolvedLines(t, st, bead, seq); !strings.Contains(lines, "approved, failed") {
		t.Fatalf("the failed card reads:\n%s", lines)
	}

	st2, _, bead2, edge2, _, c2, _ := teammateCarrierYard(t)
	edge2.Behave(func(b *standin.EdgeBehaviour) {
		b.MemberRefuse, b.MemberReason = http.StatusServiceUnavailable, "the membership did not land"
	})
	seq2 := askApprove(t, st2, bead2, inviteAsk("user invite eve --email eve@example.net"))
	if did, err := c2.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with a signed 503 = %v, %v, want it taken and still owed", did, err)
	}
	rec2 := carryState(t, st2, bead2, seq2)
	if rec2.State == store.CarryFailed {
		t.Fatalf("a signed 503 failed the carry: %+v", rec2)
	}
	if !strings.Contains(rec2.Result, "the membership did not land") {
		t.Fatalf("while the report is owed the record holds %q, want the edge's words", rec2.Result)
	}
	if d := time.Until(rec2.RetryAfter); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("the pause after a signed 503 = %v, want about a minute", d)
	}
	edge2.Behave(func(b *standin.EdgeBehaviour) { b.MemberRefuse, b.MemberReason = 0, "" })
	if did, err := c2.carryNext(ctx); err != nil || did {
		t.Fatalf("carryNext before the pause ends = %v, %v, want nothing taken", did, err)
	}
	if n := len(edge2.State().MemberAdds); n != 1 {
		t.Fatalf("the edge took %d adds before the pause ended, want the refused one alone", n)
	}
	c2.clock = func() time.Time { return rec2.RetryAfter.Add(time.Second) }
	if did, err := c2.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext after the 503 clears = %v, %v", did, err)
	}
	if rec := carryState(t, st2, bead2, seq2); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}

	st3, _, bead3, edge3, _, c3, _ := teammateCarrierYard(t)
	edge3.Behave(func(b *standin.EdgeBehaviour) {
		b.MemberRefuse, b.MemberReason = http.StatusTooManyRequests, "the edge is busy"
	})
	seq3 := askApprove(t, st3, bead3, inviteAsk("user invite eve --email eve@example.net"))
	if did, err := c3.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with a signed 429 = %v, %v, want the report left owed", did, err)
	}
	rec3 := carryState(t, st3, bead3, seq3)
	if rec3.State != store.CarryClaimed {
		t.Fatalf("a signed 429 failed the carry: %+v", rec3)
	}
	if !strings.Contains(rec3.Result, "the edge is busy") {
		t.Fatalf("while the report is owed the record holds %q, want the edge's words", rec3.Result)
	}
	if lines := resolvedLines(t, st3, bead3, seq3); !strings.Contains(lines, "being carried out") ||
		!strings.Contains(lines, "the edge is busy") {
		t.Fatalf("while the report is owed the card reads:\n%s, want the edge's words and a pending status", lines)
	}
	if d := time.Until(rec3.RetryAfter); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("the pause after a signed 429 = %v, want about a minute", d)
	}
	edge3.Behave(func(b *standin.EdgeBehaviour) { b.MemberRefuse, b.MemberReason = 0, "" })
	if did, err := c3.carryNext(ctx); err != nil || did {
		t.Fatalf("carryNext before the pause ends = %v, %v, want nothing taken", did, err)
	}
	if n := len(edge3.State().MemberAdds); n != 1 {
		t.Fatalf("the edge took %d adds before the pause ended, want the refused one alone", n)
	}
	c3.clock = func() time.Time { return rec3.RetryAfter.Add(time.Second) }
	if did, err := c3.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext past the pause = %v, %v, want the report landed", did, err)
	}
	if rec := carryState(t, st3, bead3, seq3); rec.State != store.CarryDone || !rec.RetryAfter.IsZero() || rec.Defers != 0 {
		t.Fatalf("the landed record = %+v, want the wait cleared", rec)
	}
	if lines := resolvedLines(t, st3, bead3, seq3); strings.Contains(lines, "the edge is busy") {
		t.Fatalf("after the report lands the card still reads its pending reason:\n%s", lines)
	}
	if n := len(edge3.State().MemberAdds); n != 2 {
		t.Fatalf("the edge took %d adds, want the refused one and the landing", n)
	}

	st4, _, bead4, edge4, _, c4, _ := teammateCarrierYard(t)
	if err := st4.InviteTeammate(ctx, "eve", "eve@example.net", store.RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	edge4.Behave(func(b *standin.EdgeBehaviour) {
		b.MemberRefuse, b.MemberReason = http.StatusTooManyRequests, "the edge is busy"
	})
	seq4 := askApprove(t, st4, bead4, inviteAsk("user uninvite eve"))
	if did, err := c4.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with a signed 429 = %v, %v, want the removal report left owed", did, err)
	}
	rec4 := carryState(t, st4, bead4, seq4)
	if rec4.State != store.CarryClaimed || !strings.Contains(rec4.Result, "the edge is busy") {
		t.Fatalf("the owed removal's record = %+v, want the claim standing with the edge's words", rec4)
	}
	edge4.Behave(func(b *standin.EdgeBehaviour) { b.MemberRefuse, b.MemberReason = 0, "" })
	if did, err := c4.carryNext(ctx); err != nil || did {
		t.Fatalf("carryNext before the pause ends = %v, %v, want nothing taken", did, err)
	}
	if n := len(edge4.State().MemberRemoves); n != 1 {
		t.Fatalf("the edge took %d removes before the pause ended, want the refused one alone", n)
	}
	c4.clock = func() time.Time { return rec4.RetryAfter.Add(time.Second) }
	if did, err := c4.carryNext(ctx); err != nil || !did {
		t.Fatalf("carryNext past the pause = %v, %v, want the removal landed", did, err)
	}
	if rec := carryState(t, st4, bead4, seq4); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
	if n := len(edge4.State().MemberRemoves); n != 2 {
		t.Fatalf("the edge took %d removes, want the refused one and the landing", n)
	}
}

// An invite whose report is owed, and whose user and mapping the
// operator removes meanwhile, is not made again when it is taken up: the
// retry brings back no user and no mapping, reports nothing to admit,
// and fails the carry.
func TestCarryInviteOwedRetryKeepsOperatorsRemoval(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, wrapped, c, _ := teammateCarrierYard(t)
	seq := askApprove(t, st, bead, inviteAsk("user invite eve --email eve@example.net --role operator"))
	wrapped.setDown(true)
	if did, err := c.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with the edge down = %v, %v, want it taken and still owed", did, err)
	}
	if err := st.DeleteUser(ctx, "eve", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.WebJWTUsersSetting, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	wrapped.setDown(false)
	if did, _ := c.carryNext(ctx); !did {
		t.Fatal("the owed invite was not taken up")
	}
	if u, err := st.GetUser(ctx, "eve"); err == nil {
		t.Fatalf("the retry made the removed user again: %+v", u)
	}
	if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "" {
		t.Fatalf("the retry mapped the address again: %q", all[store.WebJWTUsersSetting])
	}
	if n := len(edge.State().MemberAdds); n != 0 {
		t.Fatalf("the edge took %d adds for an invite the operator undid", n)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryFailed {
		t.Fatalf("the record = %+v, want failed", rec)
	}
}

// A removal whose report is owed, retried after a user was added again
// under the same name, removes only the user it was approved for.
func TestCarryUninviteOwedRetryKeepsNewUser(t *testing.T) {
	ctx := context.Background()
	st, _, bead, edge, wrapped, c, _ := teammateCarrierYard(t)
	if err := st.InviteTeammate(ctx, "eve", "eve@example.net", store.RoleMember, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := askApprove(t, st, bead, inviteAsk("user uninvite eve"))
	wrapped.setDown(true)
	if did, err := c.carryNext(ctx); !did || err == nil {
		t.Fatalf("carryNext with the edge down = %v, %v, want it taken and still owed", did, err)
	}
	if _, err := st.AddUser(ctx, "eve", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.WebJWTUsersSetting, "eve@other.example=eve", "mckean"); err != nil {
		t.Fatal(err)
	}
	for _, down := range []bool{true, false} {
		wrapped.setDown(down)
		if did, _ := c.carryNext(ctx); !did {
			t.Fatal("the owed removal was not taken up")
		}
		if _, err := st.GetUser(ctx, "eve"); err != nil {
			t.Fatalf("the retry removed the user added since: %v", err)
		}
		if all, _ := st.Settings(ctx); all[store.WebJWTUsersSetting] != "eve@other.example=eve" {
			t.Fatalf("the retry removed the mapping made since: %q", all[store.WebJWTUsersSetting])
		}
	}
	if removes := edge.State().MemberRemoves; len(removes) != 1 || removes[0].Email != "eve@example.net" {
		t.Fatalf("the edge took %+v, want the one approved address", removes)
	}
	if rec := carryState(t, st, bead, seq); rec.State != store.CarryDone {
		t.Fatalf("the record = %+v", rec)
	}
}
