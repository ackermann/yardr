package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// yardr mail --with prints a user's conversation with the mail of the name
// a link trusts as them, each line saying how that mail went, to the user
// and to the crew member alike. The address alone is still its own.
func TestMailWithHasTheTrustedName(t *testing.T) {
	st, own := storetest.NewOwned(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.AddUser(ctx, "jo", "jo")
	must(err)
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Config: map[string]any{store.MailPeers: "airy"}}, "jo")
	must(err)
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "airy.key"))
	must(err)
	key := yardkey.Public(k)
	_, err = st.AddPeer(ctx, store.Peer{Name: "airy", Key: key, URL: "http://airy.test"}, "jo")
	must(err)
	says := store.Says("v1")
	says.Roster = []store.PeerRosterEntry{{Name: "jo", Kind: store.RosterUser, Mail: true}}
	must(own.PeerHeard(ctx, "airy", key, says))
	_, err = st.AllowPeer(ctx, "airy", store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{"clerk"}, Trust: map[string]string{"jo": "jo"}}, "jo")
	must(err)

	must(st.Mail(ctx, "clerk", "close the books", "jo"))
	body, _ := json.Marshal(map[string]string{"to": "clerk", "from": "jo", "text": "by Friday"})
	if taken, err := own.PeerReceive(ctx, "airy", key, []store.PeerMessage{{ID: "m1", Kind: store.PeerMail, Body: body}}); err != nil || len(taken) != 1 || taken[0].Refused != "" {
		t.Fatalf("airy's mail = %+v, %v", taken, err)
	}
	_, err = st.MailPeer(ctx, "jo", "airy", "they are closed", "clerk")
	must(err)

	want := []string{"unread  jo: close the books\n", "unread  jo@airy (as jo): by Friday\n", "queued  clerk to jo@airy: they are closed\n"}
	wantText(t, mustAs(t, st, "jo", "mail", "--with", "clerk"), want...)
	wantText(t, mustAs(t, st, "clerk", "mail", "--with", "jo"), want...)
	wantText(t, mustAs(t, st, "clerk", "mail", "--with", "jo@airy"), "unread  jo@airy (as jo): by Friday\n", "queued  clerk: they are closed\n")
}
