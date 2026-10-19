package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// yardr react puts a party's emoji on mail: it shows under the letter in
// yardr mail --with and yardr inbox, and a reaction to a crew member's own
// mail lands in that crew member's inbox. --clear takes it away.
func TestReactCommand(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "yardmaster", "mail", "jo", "the shed is painted")
	conv, _, err := st.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	seq := fmt.Sprint(conv[0].Seq)
	if out := mustAs(t, st, "jo", "react", seq, "👍"); out != "reacted 👍 to "+seq+"\n" {
		t.Fatalf("react: %q", out)
	}
	if out := mustAs(t, st, "jo", "mail", "--with", "yardmaster"); !strings.Contains(out, "  👍 jo\n") {
		t.Fatalf("mail --with shows no reaction:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "inbox"); !strings.Contains(out, "  👍 jo\n") {
		t.Fatalf("inbox shows no reaction:\n%s", out)
	}
	crewbox := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster")
	if want := "👍 by jo on your mail " + seq + ": the shed is painted"; !strings.Contains(crewbox, want) {
		t.Fatalf("the crew inbox holds no reaction line:\n%s", crewbox)
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "mail", "--with", "yardmaster", "--json")), &raw); err != nil || len(raw) != 1 {
		t.Fatalf("--json: %v, %v", raw, err)
	}
	rs, _ := raw[0]["reactions"].([]any)
	if len(rs) != 1 {
		t.Fatalf("--json reactions = %v", raw[0]["reactions"])
	}
	if r, _ := rs[0].(map[string]any); r["from"] != "jo" || r["emoji"] != "👍" {
		t.Fatalf("--json reaction = %v", rs[0])
	}
	// A new one replaces the old one.
	mustAs(t, st, "jo", "react", seq, "❤️")
	if out := mustAs(t, st, "jo", "mail", "--with", "yardmaster"); !strings.Contains(out, "  ❤️ jo\n") || strings.Contains(out, "👍 jo") {
		t.Fatalf("after the replace:\n%s", out)
	}
	// --clear takes it away.
	if out := mustAs(t, st, "jo", "react", seq, "--clear"); out != "cleared reaction on "+seq+"\n" {
		t.Fatalf("clear: %q", out)
	}
	if out := mustAs(t, st, "jo", "mail", "--with", "yardmaster"); strings.Contains(out, "❤️ jo") {
		t.Fatalf("after the clear:\n%s", out)
	}
	// Refusals: a stranger, an emoji outside the set, no mail, no seq.
	mustAs(t, st, "jo", "user", "add", "ada")
	refusedAs(t, st, "ada", "only its sender or receiver may react", "react", seq, "👍")
	refusedAs(t, st, "jo", "use one of 👍 👎 ❤️ 🎉 👀 ✅", "react", seq, "💩")
	refusedAs(t, st, "jo", "not found", "react", "99999", "👍")
	refusedAs(t, st, "jo", "want the number", "react", "x", "👍")
	if _, err := as(t, st, "jo", "react", seq, "👍", "--clear"); err == nil {
		t.Fatal("--clear took an emoji")
	}
	if _, err := as(t, st, "jo", "react", seq); err == nil {
		t.Fatal("react without an emoji and without --clear was taken")
	}
}
