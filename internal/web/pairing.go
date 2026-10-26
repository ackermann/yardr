package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"rsc.io/qr"

	"yardr.dev/yardr/internal/store"
)

// The pairing page enrols one of the signed-in user's own devices by a
// short code, as `yardr user device pair` does from a terminal: the user
// opens a pairing here and the device that calls the yard with the code is
// answered with its token. It is the web view's only pairing surface, and
// it exists in JWT mode alone: the signed-in session names the user, the
// handlers pass that name and no name from the request, and the store's
// Self pairings take no other leave, so a pairing opened here is for the
// visitor's own device or for no one's. A view that acts as a fixed user
// (--as) and a view that only reads refuse the page outright.

// Pairing is the pairing page: the visitor's own open pairing, or none,
// with the form that opens one.
type Pairing struct {
	// As is the yard user the visitor is signed in as.
	As string `json:"as"`
	// Open says a pairing of theirs is open now; Device, Code and Expires
	// are its, and Left how long its code still has, in words.
	Open    bool      `json:"open"`
	Device  string    `json:"device,omitempty"`
	Code    string    `json:"code,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
	Left    string    `json:"left,omitempty"`
	// Address is where a device reaches this yard through the edge, and
	// Yard this yard's name there: the QR code holds the two with the
	// code. Address is "" while the yard is connected to no edge, and the
	// page then shows the code with no QR to scan.
	Address string `json:"address,omitempty"`
	Yard    string `json:"yard,omitempty"`
	// Error is why a POST opened or ended nothing; Draft is the device
	// name it did not open a pairing for, back in the form.
	Error string `json:"error,omitempty"`
	Draft string `json:"draft,omitempty"`
}

// pairingQR is what the QR code of a device's pairing holds, built as the
// terminal's pairQR (internal/cli) builds it: the address the device
// reaches the yard at, then '#', then the code, and '#' and the yard's
// name beside them where one is named, so the device knows which of the
// account's yards it pairs with. Nothing else is in it.
func pairingQR(address, code, yard string) string {
	if yard == "" {
		return address + "#" + code
	}
	return address + "#" + code + "#" + yard
}

// pairingData is the pairing page as it stands for user: their own open
// pairing with its code, or none. Another user's pairing is none here, as
// the store answers it.
func (s *Server) pairingData(r *http.Request, user string) (Pairing, error) {
	ctx := r.Context()
	out := Pairing{As: user}
	open, err := s.st.DevicePairCodeSelf(ctx, user)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return out, nil
	case err != nil:
		return out, err
	}
	out.Open, out.Device, out.Code, out.Expires = true, open.Device, open.Code, open.Expires
	out.Left = pairingLeft(time.Until(open.Expires))
	if all, err := s.st.Settings(ctx); err == nil {
		if e, connected, err := store.EdgeOf(all); err == nil && connected {
			out.Address = e.URL
		}
	}
	out.Yard = s.yardName(ctx)
	return out, nil
}

// pairingLeft is how long a code still has, in words for the page: whole
// minutes while it has them, and plainer words under one.
func pairingLeft(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	if m := int(d.Round(time.Minute).Minutes()); m == 1 {
		return "1 minute"
	} else {
		return fmt.Sprintf("%d minutes", m)
	}
}

// refusePairing answers a pairing request on a view that is not in JWT
// mode: the page is the signed-in visitor's, and no other view has one.
func (s *Server) refusePairing(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, Page{Status: http.StatusForbidden, Template: "error", Title: "Forbidden",
		Data: errorData{Error: "device pairing is on the signed-in view only: this view has no signed-in user to pair a device for"}})
}

func (s *Server) handlePairing(w http.ResponseWriter, r *http.Request) {
	if s.jwt == nil {
		s.refusePairing(w, r)
		return
	}
	d, err := s.pairingData(r, s.acting(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "pairing", Title: "Pair a device", Menu: "pairing", Data: d})
}

// handlePairingPost takes the pairing page's forms: open a pairing for a
// device of the visitor's own, or end the one that is open. It is
// registered only on a view that acts (ActAs, or UseJWT for JWT mode), and
// refuses every view but JWT mode's before anything is read: the user it
// opens and ends for is the request's signed-in one, never a name the
// request carries. Opened or ended, the page is read again; refused, it is
// redrawn with the reason, as 200 so a form puts it in place.
func (s *Server) handlePairingPost(w http.ResponseWriter, r *http.Request) {
	if s.jwt == nil {
		s.refusePairing(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); !ownOrigin(origin, r.Host) {
		http.Error(w, "yardr web takes a pairing only from its own pages: the request's origin is "+quoted(origin)+", not this view's. A page on another site cannot pair a device as this view's user",
			http.StatusForbidden)
		return
	}
	ctx, user := r.Context(), s.acting(r.Context())
	switch r.PostFormValue("action") {
	case "open":
		if _, err := s.st.DevicePairOpenSelf(ctx, user, r.PostFormValue("device")); err != nil {
			s.pairingAgain(w, r, r.PostFormValue("device"), err.Error())
			return
		}
	case "end":
		if _, err := s.st.DevicePairCloseSelf(ctx, user); errors.Is(err, store.ErrNotFound) {
			s.pairingAgain(w, r, "", "there is no pairing open to end: it ended, or its time ran out")
			return
		} else if err != nil {
			s.pairingAgain(w, r, "", err.Error())
			return
		}
	default:
		s.pairingAgain(w, r, "", "say whether to open a pairing or end the one that is open")
		return
	}
	http.Redirect(w, r, "/pairing", http.StatusSeeOther)
}

// pairingAgain re-reads the pairing page after a POST that changed
// nothing, with the reason and the device name back in the form.
func (s *Server) pairingAgain(w http.ResponseWriter, r *http.Request, draft, why string) {
	d, err := s.pairingData(r, s.acting(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error, d.Draft = why, draft
	s.respond(w, r, Page{Template: "pairing", Title: "Pair a device", Menu: "pairing", Data: d})
}

// handlePairingPNG serves the QR code of the visitor's own open pairing as
// a PNG: the edge's address, the code and the yard's name, as a phone
// scans them (pairingQR). It answers the pairing's own user alone — the
// code comes from their pairing or not at all — and no pairing is a plain
// 404, as another user's is.
func (s *Server) handlePairingPNG(w http.ResponseWriter, r *http.Request) {
	if s.jwt == nil {
		http.Error(w, "device pairing is on the signed-in view only", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	open, err := s.st.DevicePairCodeSelf(ctx, s.acting(ctx))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no pairing is open", http.StatusNotFound)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	address := ""
	if all, err := s.st.Settings(ctx); err == nil {
		if e, connected, err := store.EdgeOf(all); err == nil && connected {
			address = e.URL
		}
	}
	code, err := qr.Encode(pairingQR(address, open.Code, s.yardName(ctx)), qr.M)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(code.PNG())
}
