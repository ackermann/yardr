package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"yardr.dev/yardr/internal/telemetry"
)

// Telemetry is the Telemetry page (/telemetry): the yard's opt-in
// telemetry switch for a browser, off unless the operator turns it on.
// Every reader gets the consent words the switch is turned on under;
// the switch itself, the install id and the next payload are the
// operator's alone, and the id and the payload only with the switch on,
// as yardr telemetry show renders them.
type Telemetry struct {
	// As is the user the request acts as, "" when it acts as none.
	As string `json:"as,omitempty"`
	// Operator is whether the request acts as an operator of the yard:
	// the switch and its control are drawn only then.
	Operator bool `json:"operator"`
	// Switch is the switch as it stands, "on" or "off"; operators only.
	Switch string `json:"telemetry,omitempty"`
	// Questions, Profile and IDSentence are the consent words: the four
	// questions the batch answers and where it goes, the id-only
	// profile, and the pseudonymous sentence.
	Questions  string `json:"questions"`
	Profile    string `json:"profile"`
	IDSentence string `json:"id_sentence"`
	// InstallID, Since, SendingDay and SourceDays are the preview's, as
	// yardr telemetry show prints them; operators only, with the switch
	// on.
	InstallID  string   `json:"install_id,omitempty"`
	Since      string   `json:"since,omitempty"`
	SendingDay string   `json:"sending_day,omitempty"`
	SourceDays []string `json:"source_days,omitempty"`
	// Payload is the exact next request as indented JSON, as show
	// renders it; operators only, with a batch to send.
	Payload string `json:"payload,omitempty"`
	// NoBatch is why there is no batch, in words, where the switch is on
	// and no request follows.
	NoBatch string `json:"no_batch,omitempty"`
	// Deletion is how the operator has the yard's data deleted; with
	// the switch on.
	Deletion string `json:"deletion,omitempty"`
	// Error is why a POST did not write, back on the redrawn page.
	Error string `json:"error,omitempty"`
	// Head is the yard's newest seq from before the preview was read.
	Head uint64 `json:"head"`
}

// SourceDaysSay is the source days of the next request line, as yardr
// telemetry show renders them.
func (d Telemetry) SourceDaysSay() string { return telemetry.SourceDaysSay(d.SourceDays) }

// isOperator reports whether the request acts as an operator of the
// yard: a user whose role is operator, as the listener reads it.
func (s *Server) isOperator(ctx context.Context) bool {
	me := s.acting(ctx)
	if me == "" {
		return false
	}
	u, err := s.st.GetUser(ctx, me)
	return err == nil && u.IsOperator()
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.telemetryPage(r.Context(), head)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "telemetry", Title: "Telemetry", Menu: "config", Data: d, head: head})
}

func (s *Server) telemetryPage(ctx context.Context, head uint64) (Telemetry, error) {
	// A page names no host but the yard's, so where the batch goes
	// reads without its scheme: a destination, not a link.
	dest := strings.TrimPrefix(telemetry.Endpoint, "https://")
	d := Telemetry{
		As:         s.acting(ctx),
		Head:       head,
		Questions:  strings.Replace(telemetry.Questions, telemetry.Endpoint, dest, 1),
		Profile:    telemetry.Profile,
		IDSentence: telemetry.IDSentence,
	}
	if !s.isOperator(ctx) {
		return d, nil
	}
	preview, err := s.st.TelemetryShow(ctx)
	if err != nil {
		return Telemetry{}, err
	}
	d.Operator, d.Switch = true, preview.Switch
	if preview.Switch != "on" {
		return d, nil
	}
	d.InstallID, d.Since = preview.InstallID, preview.Since
	d.SendingDay, d.SourceDays = preview.SendingDay, preview.SourceDays
	d.Deletion = telemetry.DeletionSentence
	if preview.Request != nil {
		data, _ := json.MarshalIndent(preview.Request, "", "  ")
		d.Payload = string(data)
		return d, nil
	}
	d.NoBatch = telemetry.NoBatchSay(preview.NoBatch)
	return d, nil
}

// handleTelemetryPost takes the Telemetry page's forms: turn the switch
// on or off, as the request's user. It is registered only on a view
// that acts (ActAs, or UseJWT for JWT mode): without either the mux
// answers 405 itself, as on every other route. A write answered lands
// on the page; a write refused re-reads the page with the reason, as
// 200 so a form puts it in place.
func (s *Server) handleTelemetryPost(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !ownOrigin(origin, r.Host) {
		http.Error(w, "yardr web takes telemetry changes only from its own pages: the request's origin is "+quoted(origin)+", not this view's. A page on another site cannot turn telemetry on or off as this view's user",
			http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if !s.isOperator(ctx) {
		s.telemetryAgain(w, r, "telemetry is the operator's to change")
		return
	}
	var on bool
	switch action := r.PostFormValue("action"); action {
	case "on":
		on = true
	case "off":
	case "":
		s.telemetryAgain(w, r, "on or off: the button names the change")
		return
	default:
		s.telemetryAgain(w, r, fmt.Sprintf("no telemetry change is named %q", action))
		return
	}
	if _, err := s.st.TelemetrySet(ctx, on, s.acting(ctx)); err != nil {
		s.telemetryAgain(w, r, err.Error())
		return
	}
	http.Redirect(w, r, "/telemetry", http.StatusSeeOther)
}

// telemetryAgain re-reads the Telemetry page after a POST that wrote
// nothing, with the reason on the redrawn page.
func (s *Server) telemetryAgain(w http.ResponseWriter, r *http.Request, why string) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.telemetryPage(r.Context(), head)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error = why
	s.respond(w, r, Page{Template: "telemetry", Title: "Telemetry", Menu: "config", Data: d, head: head})
}

// telemetryLink is the Telemetry page on the config hub: the yard's
// opt-in telemetry switch, the operator's alone.
var telemetryLink = ConfigLink{Title: "Telemetry", Path: "/telemetry", Blurb: "the yard's opt-in telemetry switch"}
