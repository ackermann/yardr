package web

// A strong card's approval in the hosted web (design yardr-2nb1 5.1): the
// bead page approves run and setup cards, weight first with the yard's
// sentences, and a strong card — trust or code in its stored weight — is
// approved only under a fresh sign-in. Approving one starts the gateway
// re-authentication unless the session's last authentication was a
// strong-card re-authentication by this operator inside the window,
// whichever strong card started it; a standing login, however recent,
// never counts. The return state is one-use and bound to its card,
// consumed only for it, and the store takes the approval only with the
// request's fresh witness. Anywhere but the hosted web a strong card
// stays terminal-only.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"yardr.dev/yardr/internal/store"
)

// freshPendingTTL bounds a challenge left at the gateway: one never
// finished is dropped after the hour, past any window it could have been
// used in.
const freshPendingTTL = time.Hour

// freshChallenge is a strong card's sign-in waiting at the gateway: the
// card it was raised for and who raised it, when, and the approval it
// carries back (a setup's preset pick and account, a run's values).
type freshChallenge struct {
	bead    string
	ask     uint64
	user    string
	at      time.Time
	option  int
	values  map[string]string
	account string
}

// freshAuth is what a consumed challenge returned: the authentication time
// the gateway's token carried, and the challenge it answered. One fresh
// sign-in witnesses several strong cards inside the window, so a later
// card whose token carries the same time approves without a new sign-in.
type freshAuth struct {
	auth  time.Time
	since time.Time
}

// freshState mints one-use return state for a challenge: 256 random bits,
// base64url, carrying nothing itself.
func freshState() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// beadApprove approves the run or setup card ask, as the request's user. A
// routine card is approved as it always was, asking for no witness; a card
// that is weighty but not strong keeps the window's no-gate words; a
// strong card goes through its sign-in (approveStrong).
func (s *Server) beadApprove(w http.ResponseWriter, r *http.Request, b *store.Bead, ask store.BeadAsk) {
	ctx := r.Context()
	user := s.acting(ctx)
	option, values, account, ok := s.approveParams(w, r, b, ask)
	if !ok {
		return
	}
	if !store.StrongWeight(ask.Weight) {
		if len(ask.Weight) > 0 {
			s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d is %s, and this window has no gate. Approve it in footplate, or in a terminal on the yard's machine: yardr bead answer %s %d %s",
				ask.Ask, store.WeightWords(ask.Weight), b.ID, ask.Ask, approveAgain(ask)))
			return
		}
		if err := s.approveNow(ctx, b.ID, ask, option, values, account, user, store.FreshWitness{}, false); err != nil {
			s.beadAgain(w, r, b.ID, err.Error())
			return
		}
		http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
		return
	}
	s.approveStrong(w, r, b, ask, option, values, account)
}

// approveAgain is what goes after "yardr bead answer <bead> <ask>" to
// approve ask where a gate stands: a run's approval, a setup's preset
// pick with its account.
func approveAgain(ask store.BeadAsk) string {
	if ask.Setup != nil {
		return "<n> --secret"
	}
	return "--approve"
}

// approveParams reads the approval the form posts for ask: a setup's
// preset pick with the account typed beside it or the candidate taken
// instead, a run's values where the card asks for any. An ask that is
// neither is refused with the page redrawn.
func (s *Server) approveParams(w http.ResponseWriter, r *http.Request, b *store.Bead, ask store.BeadAsk) (option int, values map[string]string, account string, ok bool) {
	switch {
	case ask.Setup != nil:
		n, err := strconv.Atoi(r.PostFormValue("option"))
		if err != nil || n < 1 || n > len(ask.Options) {
			s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d sets up %s: pick one of its %d presets", ask.Ask, ask.Setup.Pack, len(ask.Options)))
			return 0, nil, "", false
		}
		option = n
		for _, name := range ask.Setup.Account {
			if v := r.PostFormValue("secret-" + name); v != "" {
				if values == nil {
					values = map[string]string{}
				}
				values[name] = v
			}
		}
		account = r.PostFormValue("account")
		return option, values, account, true
	case ask.Run != nil:
		for _, name := range ask.Run.Ask {
			if values == nil {
				values = map[string]string{}
			}
			values[name] = r.PostFormValue("value-" + name)
		}
		return 0, values, "", true
	default:
		s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d is answered, not approved: pick an option, answer with words, put it aside with Later, or decline it", ask.Ask))
		return 0, nil, "", false
	}
}

// approveStrong approves a strong card under a fresh sign-in. Anywhere but
// the hosted web — no gateway, or a yard that is not hosted — the card
// stays terminal-only. Where the session's last authentication is already
// a strong-card re-authentication by this operator inside the window, the
// card approves at once; otherwise the gateway authenticates now, and the
// one-use state returns to this card alone.
func (s *Server) approveStrong(w http.ResponseWriter, r *http.Request, b *store.Bead, ask store.BeadAsk, option int, values map[string]string, account string) {
	ctx := r.Context()
	user := s.acting(ctx)
	terminal := fmt.Sprintf("ask %d is %s: a strong card is not approved in this window. Approve it at a terminal on the yard's machine: yardr bead answer %s %d %s",
		ask.Ask, store.StrongWords(ask.Weight), b.ID, ask.Ask, approveAgain(ask))
	if s.jwt == nil {
		s.beadAgain(w, r, b.ID, terminal)
		return
	}
	settings, err := s.st.Settings(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if settings[store.HostedSetting] != "true" {
		s.beadAgain(w, r, b.ID, terminal)
		return
	}
	if auth, since, ok := s.strongAuth(ctx, user); ok && s.freshSession(ctx, since) {
		fw := store.FreshWitness{User: user, AuthTime: auth}
		if err := s.approveNow(ctx, b.ID, ask, option, values, account, user, fw, true); err != nil {
			s.beadAgain(w, r, b.ID, err.Error())
			return
		}
		http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
		return
	}
	state := freshState()
	s.freshMu.Lock()
	for st, ch := range s.freshPending {
		if time.Since(ch.at) > freshPendingTTL {
			delete(s.freshPending, st)
		}
	}
	s.freshPending[state] = freshChallenge{bead: b.ID, ask: ask.Ask, user: user, at: time.Now(),
		option: option, values: values, account: account}
	s.freshMu.Unlock()
	to := "/beads/" + url.PathEscape(b.ID) + "/fresh/" + strconv.FormatUint(ask.Ask, 10) + "/" + state
	http.Redirect(w, r, "/edge/reauth?return_to="+url.QueryEscape(to), http.StatusSeeOther)
}

// strongAuth is the authentication time the operator's last consumed
// strong challenge returned, with the challenge it answered: ok false
// where no challenge has returned one for them on this view, or the
// request's token carries another time. A standing login, however recent,
// is never one a challenge returned.
func (s *Server) strongAuth(ctx context.Context, user string) (auth, since time.Time, ok bool) {
	id := jwtIdentityOf(ctx)
	if id.authTime.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	s.freshMu.Lock()
	defer s.freshMu.Unlock()
	st, ok := s.freshStrong[user]
	if !ok || !st.auth.Equal(id.authTime) {
		return time.Time{}, time.Time{}, false
	}
	return st.auth, st.since, true
}

// handleFresh takes the gateway's return after a strong card's sign-in:
// the one-use state the approve minted, bound to its card. It consumes
// the state and approves the card with the request's fresh witness, and
// lands on the bead; a replayed or foreign state, a stale sign-in, or a
// card that moved on re-reads the bead with the reason.
func (s *Server) handleFresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := strconv.ParseUint(r.PathValue("ask"), 10, 64)
	if err != nil || n == 0 {
		s.beadAgain(w, r, id, "that sign-in names no ask: approve the card again")
		return
	}
	state := r.PathValue("state")
	s.freshMu.Lock()
	ch, ok := s.freshPending[state]
	s.freshMu.Unlock()
	if !ok {
		s.beadAgain(w, r, id, "that sign-in was used already, expired, or never started: approve the card again")
		return
	}
	ctx := r.Context()
	user := s.acting(ctx)
	if ch.bead != id || ch.ask != n || ch.user != user {
		s.beadAgain(w, r, id, "that sign-in was raised for another card: approve this one again")
		return
	}
	if !s.freshSession(ctx, ch.at) {
		s.beadAgain(w, r, id, "that sign-in is not fresh: the gateway authenticated before the card's challenge, or outside its window. Approve the card again")
		return
	}
	auth := jwtIdentityOf(ctx).authTime
	if !s.consumeFresh(state, ch, user, auth) {
		s.beadAgain(w, r, id, "that sign-in was used already, expired, or never started: approve the card again")
		return
	}
	// The card may have moved while its operator signed in: approve what
	// stands now, as the bead's page reads it, and let the store judge the
	// rest.
	asks, err := s.openBeadAsks(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var ask *store.BeadAsk
	for i := range asks {
		if asks[i].Ask == ch.ask {
			ask = &asks[i]
		}
	}
	if ask == nil {
		s.beadAgain(w, r, id, fmt.Sprintf("ask %d on bead %s is answered already, or asked of another; read it again", ch.ask, id))
		return
	}
	fw := store.FreshWitness{User: user, AuthTime: auth}
	if err := s.approveNow(ctx, id, *ask, ch.option, ch.values, ch.account, user, fw, true); err != nil {
		s.beadAgain(w, r, id, err.Error())
		return
	}
	http.Redirect(w, r, "/beads/"+url.PathEscape(id), http.StatusSeeOther)
}

// consumeFresh atomically spends the one-use return state and remembers the
// authentication it returned. Two callbacks racing on the same state cannot
// both carry that sign-in to an approval.
func (s *Server) consumeFresh(state string, challenge freshChallenge, user string, auth time.Time) bool {
	s.freshMu.Lock()
	defer s.freshMu.Unlock()
	current, ok := s.freshPending[state]
	if !ok || current.bead != challenge.bead || current.ask != challenge.ask ||
		current.user != challenge.user || current.user != user || !current.at.Equal(challenge.at) {
		return false
	}
	if time.Since(current.at) > freshPendingTTL {
		delete(s.freshPending, state)
		return false
	}
	delete(s.freshPending, state)
	s.freshStrong[user] = freshAuth{auth: auth, since: current.at}
	return true
}

// approveNow approves the run or setup card ask with the store, saying the
// person's word where the card is weighty, and carrying the fresh witness
// where the sign-in is fresh. The store judges the card again in the
// write.
func (s *Server) approveNow(ctx context.Context, id string, ask store.BeadAsk, option int, values map[string]string, account, user string, fw store.FreshWitness, fresh bool) error {
	if fresh {
		ctx = store.WithFreshWitness(ctx, fw.User, fw.AuthTime)
	}
	weighed := len(ask.Weight) > 0
	if ask.Run != nil {
		_, err := s.st.ApproveAsk(ctx, id, ask.Ask, user, weighed, values)
		return err
	}
	_, err := s.st.ApproveSetup(ctx, id, ask.Ask, option, values, account, user, weighed)
	return err
}
