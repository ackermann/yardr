package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// --ack takes the seq to acknowledge through: a crew member reads its
// inbox, new mail arrives, and --ack through the last seq read leaves the
// new mail unread and says so. Bare --ack is refused with the newest seq
// and the command to ack through it.
func TestInboxAckThroughSeq(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	for _, text := range []string{"one", "two"} {
		if err := st.Mail(ctx, "yardmaster", text, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	var page store.InboxPage
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--json")), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("the read holds %d, want 2", page.Total)
	}
	if err := st.Mail(ctx, "yardmaster", "three", "jo"); err != nil {
		t.Fatal(err)
	}
	out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--ack", fmt.Sprint(page.Last))
	wantText(t, out, "mail from jo: one\n", "mail from jo: two\n", "mail from jo: three\n",
		"acknowledged 2\n", "1 newer stays unread\n")
	if out := mustAs(t, st, "jo", "inbox", "--crew", "yardmaster"); !strings.Contains(out, "three") ||
		strings.Contains(out, "two") || strings.Contains(out, "one") {
		t.Fatalf("after --ack through the last read:\n%s", out)
	}

	// Bare --ack is refused with the newest seq and the command to ack
	// through it, in this inbox's scope.
	var after store.InboxPage
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--json")), &after); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("--ack takes the seq to acknowledge through: the newest unread is %d, so yardr inbox --crew yardmaster --ack %d",
		after.Last, after.Last)
	refusedAs(t, st, "jo", want, "inbox", "--crew", "yardmaster", "--ack")
	refusedAs(t, st, "jo", `--ack "banana": want the seq to acknowledge through`,
		"inbox", "--crew", "yardmaster", "--ack", "banana")
	refusedAs(t, st, "jo", `--ack "0": want the seq to acknowledge through`,
		"inbox", "--crew", "yardmaster", "--ack", "0")
	refusedAs(t, st, "jo", "a seq names what --ack acknowledges through",
		"inbox", "--crew", "yardmaster", "123")

	// With nothing unread there is no seq to name.
	mustAs(t, st, "jo", "inbox", "--crew", "yardmaster", "--ack", fmt.Sprint(after.Last))
	refusedAs(t, st, "jo", "nothing to acknowledge", "inbox", "--crew", "yardmaster", "--ack")

	// The prime names the seq form.
	if out := mustAs(t, st, "jo", "prime", "--crew", "yardmaster"); !strings.Contains(out, "yardr inbox --ack <seq>") {
		t.Fatalf("the prime names no seq form of --ack:\n%s", out)
	}
}

// As above for a user's inbox: --ack through the last seq read leaves a
// mail that arrived since unread and says so, and bare --ack is refused
// with the hint, in the parts shown.
func TestUserInboxAckThroughSeq(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	for _, text := range []string{"one", "two"} {
		if err := st.Mail(ctx, "jo", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	var box userBox
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--json")), &box); err != nil || box.Mailbox == nil {
		t.Fatalf("inbox --json = %+v, %v", box, err)
	}
	if box.Unread != 2 {
		t.Fatalf("the read holds %d, want 2", box.Unread)
	}
	if err := st.Mail(ctx, "jo", "three", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	out := mustAs(t, st, "jo", "inbox", "--ack", fmt.Sprint(box.Last))
	wantText(t, out, "Mail: 3 unread\n", "acknowledged 2\n", "1 newer stays unread\n")
	if out := mustAs(t, st, "jo", "inbox"); !strings.Contains(out, "Mail: 1 unread\n") ||
		!strings.Contains(out, "mail from yardmaster: three") || strings.Count(out, "(read)") != 2 {
		t.Fatalf("after --ack through the last read:\n%s", out)
	}

	var after userBox
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--json")), &after); err != nil {
		t.Fatal(err)
	}
	refusedAs(t, st, "jo", fmt.Sprintf("the newest unread is %d, so yardr inbox --ack %d", after.Last, after.Last),
		"inbox", "--ack")
	refusedAs(t, st, "jo", fmt.Sprintf("the newest unread is %d, so yardr inbox --mail --ack %d", after.Last, after.Last),
		"inbox", "--mail", "--ack")
}
