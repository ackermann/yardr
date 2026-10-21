package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// yardr source grants says each call's own grant: on which card and asked
// by whom where it was approved on one, unknown where the ask's note is
// gone. A call with no Given entry reads not kept and -, and its record's
// At and By are on the record's own line below the summary, never on a
// row.
func TestPrintGrants(t *testing.T) {
	t.Parallel()
	d, err := source.Parse("mail.yaml", []byte("name: mail\nbase: https://mail.example.com\n"+
		"credential: { env: MAIL_TOKEN }\n"+
		"calls:\n"+
		"  list: { kind: read, request: { method: GET, path: /list } }\n"+
		"  get: { kind: read, request: { method: GET, path: /get } }\n"+
		"  send: { kind: update, request: { method: POST, path: /send } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC) }
	grants := []store.SourceGrant{
		{Source: "mail", Grantee: store.Grantee{Crew: "yardmaster"}, Calls: []string{"read"}, At: at(17, 19, 2), By: "mckean",
			Given: map[string]store.GrantGiven{"read": {At: at(17, 19, 2), By: "mckean", Bead: "yd-c546", Ask: 409, Asker: "yardmaster"}}},
		{Source: "mail", Grantee: store.Grantee{Crew: "yardmaster"}, As: "ada", Calls: []string{"send"}, At: at(17, 19, 4), By: "ada",
			Given: map[string]store.GrantGiven{"send": {At: at(17, 19, 4), By: "ada", Bead: "yd-c546", Ask: 412}}},
		{Source: "telegram", Grantee: store.Grantee{Group: "builders"}, Calls: []string{"read", "reply"}, At: at(12, 10, 15), By: "mckean"},
		{Source: "telegram", Grantee: store.Grantee{Group: "reviewers"}, Calls: []string{"read", "reply"}, At: at(20, 8, 0), By: "jo",
			Given: map[string]store.GrantGiven{"reply": {At: at(20, 8, 0), By: "jo"}}},
	}
	var out bytes.Buffer
	printGrants(&out, grants, map[string]*source.Declaration{"mail": d}, time.UTC)
	want := `SOURCE    HOLDER                         CALLS           GRANTED       BY
mail      crew member yardmaster         read (2 calls)  17 Oct 19:02  mckean, on yd-c546 ask 409 asked by yardmaster
mail      crew member yardmaster as ada  send (update)   17 Oct 19:04  ada, on yd-c546 ask 412 asked by unknown
telegram  group builders                 read            not kept      -
telegram  group builders                 reply           not kept      -
telegram  group reviewers                read            not kept      -
telegram  group reviewers                reply           20 Oct 08:00  jo
6 calls granted of 2 sources to 3 holders: 1 may change something outside the yard, 1 calls as a user.
telegram to group builders: granted before its history was kept; its record last changed 12 Oct 10:15 by mckean.
telegram to group reviewers: granted before its history was kept; its record last changed 20 Oct 08:00 by jo.
take one back: yardr source revoke <source> <call> --crew|--group <holder> [--as <user>]
`
	if got := strings.Join(strings.Fields(out.String()), " "); got != strings.Join(strings.Fields(want), " ") {
		t.Fatalf("source grants printed\n%s\nwant\n%s", out.String(), want)
	}
	if strings.Contains(strings.Split(out.String(), "6 calls granted")[0], "12 Oct") {
		t.Errorf("a row carries the record's own time:\n%s", out.String())
	}
}
