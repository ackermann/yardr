package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardr mail sends a text over one mail's size as numbered mails, in order,
// and says how many; past the most mails one text is sent as it is refused,
// naming the file it goes as instead.
func TestMailLong(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	st := userYardOn(t, api)
	ctx := context.Background()
	text := strings.Repeat("x", 10<<10)
	if out := mustAs(t, st, "jo", "mail", "yardmaster", text); out != "3 mails sent to yardmaster\n" {
		t.Fatalf("mail = %q", out)
	}
	letters, _, err := st.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 3 {
		t.Fatalf("%d mails, %v", len(letters), err)
	}
	bodies := ""
	for i, l := range letters {
		head, body, _ := strings.Cut(l.Text, "\n")
		if head != fmt.Sprintf("(%d/3)", i+1) {
			t.Fatalf("mail %d begins %q", i+1, head)
		}
		bodies += body
	}
	if bodies != text {
		t.Fatal("the mails do not make the text again")
	}

	var sent map[string]any
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "mail", "yardmaster", text, "--json")), &sent); err != nil ||
		sent["to"] != "yardmaster" || sent["sent"] != "ok" || sent["mails"] != 3.0 {
		t.Fatalf("--json = %v, %v", sent, err)
	}
	refusedAs(t, st, "jo", "--file", "mail", "yardmaster", strings.Repeat("x", store.MaxMailParts*store.MaxMailBytes+1))
	letters, _, err = st.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(letters) != 6 {
		t.Fatalf("%d mails after the refusal, %v", len(letters), err)
	}
}
