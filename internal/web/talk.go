package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// Talk is the talk page: the conversation of the user the view acts as
// (--as) with one crew member, live, and the form that sends it mail. It
// is the web's `yardr chat` for one member at a time.
type Talk struct {
	// As is the user the view acts as, "" when it does not act: the page
	// then says so instead of any conversation.
	As string `json:"as"`
	// With is the crew member in conversation, "" when the view does not
	// act or the yard has no crew.
	With string `json:"with"`
	// Crew are the members to talk to, in the yard's order.
	Crew []TalkPeer `json:"crew"`
	// Letters are the conversation's newest page, oldest first, or the
	// page under Before.
	Letters []TalkLetter `json:"letters"`
	// More is whether mail older than the oldest letter shown was left
	// out; Before reads it.
	More bool `json:"more,omitempty"`
	// Before is the ?before= the page was read with, 0 for the newest
	// page: an older page is a snapshot, only the newest is live.
	Before uint64 `json:"before,omitempty"`
	// Head is the yard's newest seq when the page was read, before its
	// letters: the stream after it brings what happened since.
	Head uint64 `json:"head"`
	// MaxMail is how many bytes a mail may hold, the form's limit.
	MaxMail int `json:"max_mail"`
	// Error is why a POST did not send, "" otherwise; Draft is the text
	// it did not send, back in the form.
	Error string `json:"error,omitempty"`
	Draft string `json:"draft,omitempty"`
}

// TalkPeer is one crew member to talk to.
type TalkPeer struct {
	Name string `json:"name"`
	// Unread is whether the user has unread mail from it.
	Unread bool `json:"unread"`
}

// TalkLetter is one letter of the conversation.
type TalkLetter struct {
	store.Letter
	// Own is whether the user the view acts as wrote it, under their own
	// name or one a link trusted as them.
	Own bool `json:"own"`
	// Sender is who the page names for it: the sender, with the device
	// and, on mail to another name, the addressee.
	Sender string `json:"sender"`
}

// talkLimit is how many letters the talk page shows: the newest page of
// the conversation, an older page a ?before= away.
const talkLimit = 50

func (s *Server) handleTalk(w http.ResponseWriter, r *http.Request) {
	if s.as == "" {
		// The view does not act: no user to show a conversation of.
		s.respond(w, r, Page{Template: "talk", Title: "Talk", Menu: "talk", Data: Talk{}})
		return
	}
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	crew, err := s.st.Crew(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	with := r.URL.Query().Get("with")
	switch {
	case with == "":
		with = defaultPeer(crew)
	case slices.ContainsFunc(crew, func(r store.CrewMember) bool { return r.Name == with }):
	default:
		s.fail(w, r, fmt.Errorf("crew member %s: %w", with, store.ErrNotFound))
		return
	}
	var before uint64
	if v := r.URL.Query().Get("before"); v != "" {
		if before, err = strconv.ParseUint(v, 10, 64); err != nil || before == 0 {
			s.respond(w, r, Page{Status: http.StatusBadRequest, Template: "error", Title: "Bad request",
				Data: errorData{Error: "before is an event's seq, not " + v}})
			return
		}
	}
	out := Talk{As: s.as, With: with, Crew: []TalkPeer{}, Letters: []TalkLetter{},
		Before: before, Head: head, MaxMail: store.MaxMailBytes}
	if with == "" {
		// The yard has no crew: the page says whose command that is.
		s.respond(w, r, Page{Template: "talk", Title: "Talk", Menu: "talk", Data: out, head: head})
		return
	}
	// Whose unread mail the user has, for the peers' marks: the letters
	// are not needed here.
	box, err := s.st.UserInbox(ctx, s.as, 1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, r := range crew {
		out.Crew = append(out.Crew, TalkPeer{Name: r.Name, Unread: slices.Contains(box.From, r.Name)})
	}
	letters, more, err := s.st.Conversation(ctx, s.as, with, 0, talkLimit, before)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.More = more
	for _, l := range letters {
		out.Letters = append(out.Letters, TalkLetter{Letter: l, Own: l.From == s.as || l.As == s.as, Sender: sender(l, s.as, with)})
	}
	title := "Talk with " + with
	s.respond(w, r, Page{Template: "talk", Title: title, Menu: "talk", Data: out, head: head})
}

// defaultPeer is the member the talk page opens on: the yardmaster where
// the yard has one, else its first crew member, "" with no crew.
func defaultPeer(crew []store.CrewMember) string {
	if slices.ContainsFunc(crew, func(r store.CrewMember) bool { return r.Name == "yardmaster" }) {
		return "yardmaster"
	}
	if len(crew) > 0 {
		return crew[0].Name
	}
	return ""
}

// sender is who the talk page names for l: the sender with its device, as
// `yardr chat` shows it, and the addressee where it is neither party of
// the conversation (mail to a name a link trusted as the user).
func sender(l store.Letter, me, with string) string {
	from := store.ViaMark(l.From, l.Via)
	if l.As != "" {
		from += " (as " + l.As + ")"
	}
	if l.To != with && l.To != me {
		from += " to " + l.To
	}
	return from
}

// handleTalkPost takes the talk page's form: mail to the member in
// conversation, sent as the user the view acts as. It is registered only
// with --as (ActAs): without it the mux answers 405 itself, as on every
// other route. Mail sent, it redirects to the conversation; mail refused
// re-reads the page with the reason, as 200 so htmx puts it in place (it
// leaves an error's page out).
func (s *Server) handleTalkPost(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !ownOrigin(origin, r.Host) {
		http.Error(w, "yardr web takes mail only from its own pages: the request's origin is "+quoted(origin)+", not this view's. A page on another site cannot send mail as this view's user",
			http.StatusForbidden)
		return
	}
	ctx := r.Context()
	crew, err := s.st.Crew(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	with := r.URL.Query().Get("with")
	if with == "" {
		with = defaultPeer(crew)
	}
	if with == "" || !slices.ContainsFunc(crew, func(r store.CrewMember) bool { return r.Name == with }) {
		s.fail(w, r, fmt.Errorf("crew member %s: %w", with, store.ErrNotFound))
		return
	}
	text := r.PostFormValue("text")
	if err := s.st.Mail(ctx, with, text, s.as); err != nil {
		s.talkAgain(w, r, with, text, err.Error())
		return
	}
	http.Redirect(w, r, "/talk?with="+url.QueryEscape(with), http.StatusSeeOther)
}

// talkAgain re-reads the talk page after a POST that sent nothing, with
// the reason and the text back in the form.
func (s *Server) talkAgain(w http.ResponseWriter, r *http.Request, with, text, why string) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Talk{As: s.as, With: with, Crew: []TalkPeer{}, Letters: []TalkLetter{},
		Head: head, MaxMail: store.MaxMailBytes, Error: why, Draft: text}
	crew, err := s.st.Crew(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	box, err := s.st.UserInbox(ctx, s.as, 1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, r := range crew {
		out.Crew = append(out.Crew, TalkPeer{Name: r.Name, Unread: slices.Contains(box.From, r.Name)})
	}
	letters, more, err := s.st.Conversation(ctx, s.as, with, 0, talkLimit, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.More = more
	for _, l := range letters {
		out.Letters = append(out.Letters, TalkLetter{Letter: l, Own: l.From == s.as || l.As == s.as, Sender: sender(l, s.as, with)})
	}
	s.respond(w, r, Page{Template: "talk", Title: "Talk with " + with, Menu: "talk", Data: out, head: head})
}

// ownOrigin reports whether origin is this request's own: an http or
// https URL whose host is the one the request used, with any port. The
// request's host was checked by ServeHTTP against the names the view
// answers to. Other names that the view also answers to are distinct
// origins: a page on one cannot send mail through a request to another.
// The scheme and port may belong to an edge in front (https on its port,
// behind tailscale serve) or to the view itself, so neither is compared.
func ownOrigin(origin, requestHost string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "" && host == strings.ToLower(hostOf(requestHost))
}

// quoted is origin as the refusal names it: what a request without one
// sent.
func quoted(origin string) string {
	if origin == "" {
		return "none"
	}
	return strconv.Quote(origin)
}
