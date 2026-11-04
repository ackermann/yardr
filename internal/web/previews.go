package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	"yardr.dev/yardr/internal/preview"
	"yardr.dev/yardr/internal/store"
)

// Previews is the Previews page: one card per preview of the yard, as
// `yardr preview list` has them, with what the card approves: the start
// command in full, the directory, the port, the depot commit and dirty
// mark, the proposer and the lifetime, the running preview's log tail,
// and a stale mark when the depot HEAD has moved past the running
// commit. The command and the log are the operator's to read; any other
// reader gets the card without them. A watch has a card of its own kind
// while it runs, with the bead, directory and lifetime, and Open and Stop:
// it has no command or commit to approve, and a watch that ended leaves
// the page, where a card that waits for a person must stand out. A card
// shows the host the edge gave the preview beside its path, once it has
// one.
type Previews struct {
	// As is the user the request acts as, "" when it acts as none: the
	// card's buttons are drawn only for a request that acts.
	As string `json:"as,omitempty"`
	// Previews are the yard's previews, by app name.
	Previews []PreviewCard `json:"previews"`
	// Idle is how long a preview may go without a request before the
	// supervisor stops it, as the page shows it: "4h".
	Idle string `json:"idle"`
	// MaxAge is how long a preview may run before the supervisor stops
	// it however often it is used: "7d".
	MaxAge string `json:"max_age"`
	// Error is why a POST did not write, back on the redrawn page.
	Error string `json:"error,omitempty"`
	// Head is the yard's newest seq from before the previews were read.
	Head uint64 `json:"head"`
}

// PreviewCard is one preview as the Previews page shows it: the row,
// with the depot HEAD now beside the commit it runs, whether that
// commit is stale, and the running preview's log tail.
type PreviewCard struct {
	store.Preview
	// Head is the depot's HEAD now, read at page load.
	Head string `json:"head,omitempty"`
	// Stale marks a preview whose commit the depot has moved past.
	Stale bool `json:"stale,omitempty"`
	// Log is the running preview's log tail, newest last.
	Log []string `json:"log,omitempty"`
}

// Status is the state as `yardr preview list` shows it: the state, with
// its detail behind it. A running preview has one only when the edge has
// refused the report of it, which is the page's word of why it has no
// address.
func (c PreviewCard) Status() string {
	if c.StateDetail != "" {
		return c.State + " (" + c.StateDetail + ")"
	}
	return c.State
}

// Dot is the status dot's class: running green, a pending proposal the
// accent, a crashed or failed one red, the rest none.
func (c PreviewCard) Dot() string {
	switch c.State {
	case store.PreviewRunning:
		return "running"
	case store.PreviewPending:
		return "working"
	case store.PreviewCrashed, store.PreviewFailed:
		return "held"
	}
	return ""
}

// ShortCommit and ShortHead are the commit the preview runs and the
// depot's HEAD now as the card shows them: twelve characters, "-" for
// none, as `yardr preview list` shortens.
func (c PreviewCard) ShortCommit() string { return shortHash(c.Commit) }
func (c PreviewCard) ShortHead() string   { return shortHash(c.Head) }

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "-"
	}
	return h
}

func (s *Server) handlePreviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.previewsPage(ctx, head)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "previews", Title: "Previews", Menu: "previews", Data: d, head: head, stream: "seq"})
}

func (s *Server) previewsPage(ctx context.Context, head uint64) (Previews, error) {
	all, err := s.st.Previews(ctx)
	if err != nil {
		return Previews{}, err
	}
	settings, err := s.st.Settings(ctx)
	if err != nil {
		return Previews{}, err
	}
	d := Previews{As: s.acting(ctx), Previews: []PreviewCard{},
		Idle: age(store.PreviewIdle(settings)), MaxAge: age(store.PreviewAgeLimit), Head: head}
	// The start command and the running preview's log are the operator's
	// to read: a command names paths, and a log the words of what ran. A
	// reader who is no operator gets the card without them, the
	// approval's command with the card's: it is the same command.
	operator := s.isOperator(ctx)
	for _, p := range all {
		if p.Kind == store.PreviewKindWatch && p.State != store.PreviewRunning {
			continue
		}
		c := PreviewCard{Preview: p}
		// A row forgotten between the list and this read leaves the
		// card without a head, not the page without its cards. A watch
		// runs no commit, so it is never behind one.
		if h, err := s.st.PreviewHead(ctx, p.App); err == nil && p.Kind != store.PreviewKindWatch {
			c.Head = h
			c.Stale = h != "" && p.Commit != "" && h != p.Commit
		}
		if p.State == store.PreviewRunning && operator {
			if lines, err := preview.LogTail(filepath.Join(s.home, "previews", p.App+".log"), 100); err == nil {
				c.Log = lines
			}
		}
		if !operator {
			c.Command = ""
			if c.Approval != nil {
				approval := *c.Approval
				approval.Command = ""
				c.Approval = &approval
			}
		}
		d.Previews = append(d.Previews, c)
	}
	return d, nil
}

// handlePreviewsPost takes the Previews page's forms: approve, refuse,
// stop or restart one preview, as the request's user. It is registered
// only on a view that acts (ActAs, or UseJWT for JWT mode): without
// either the mux answers 405 itself, as on every other route. A write
// answered lands on the page; a write refused re-reads the page with
// the reason, as 200 so a form puts it in place.
func (s *Server) handlePreviewsPost(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !ownOrigin(origin, r.Host) {
		http.Error(w, "yardr web takes preview approvals only from its own pages: the request's origin is "+quoted(origin)+", not this view's. A page on another site cannot approve or stop as this view's user",
			http.StatusForbidden)
		return
	}
	ctx := r.Context()
	app, action := r.PostFormValue("app"), r.PostFormValue("action")
	var err error
	switch action {
	case "approve":
		_, err = s.st.ApprovePreview(ctx, app, s.acting(ctx))
	case "refuse":
		_, err = s.st.RefusePreview(ctx, app, s.acting(ctx))
	case "stop":
		_, err = s.st.PreviewStop(ctx, app, s.acting(ctx))
	case "restart":
		_, err = s.st.RestartPreview(ctx, app, s.acting(ctx))
	case "":
		err = fmt.Errorf("approve, refuse, stop or restart: the card's button names the write")
	default:
		err = fmt.Errorf("no preview write is named %q", action)
	}
	if err != nil {
		s.previewsAgain(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/previews", http.StatusSeeOther)
}

// previewsAgain re-reads the Previews page after a POST that wrote
// nothing, with the reason on the redrawn page.
func (s *Server) previewsAgain(w http.ResponseWriter, r *http.Request, why string) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.previewsPage(r.Context(), head)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error = why
	s.respond(w, r, Page{Template: "previews", Title: "Previews", Menu: "previews", Data: d, head: head, stream: "seq"})
}
