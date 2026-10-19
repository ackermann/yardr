package chat

import (
	"context"
	"testing"
)

// The parties' reactions stand under the mail, one a line, as the store
// answers them and as the stream hands them.
func TestReactionsShow(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, me, "the shed is painted", "chief"))
	conv, _, err := st.Conversation(ctx, me, "chief", 0, 0, 0)
	must(t, err)
	must(t, st.React(ctx, conv[0].Seq, "👍", me))
	v := open(t, st, "chief", 120, 24)
	v.shows("👍 " + me)
	must(t, st.React(ctx, conv[0].Seq, "🎉", "chief"))
	v.hides("🎉 chief")
	v.stream()
	v.shows("👍 "+me, "🎉 chief")
	must(t, st.React(ctx, conv[0].Seq, "", me))
	v.stream()
	v.hides("👍 " + me)
	v.shows("🎉 chief")
}
