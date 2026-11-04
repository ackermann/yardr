package standin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// Edge is yardr's edge as far as a yard calls it, written for the calls
// internal/edgelink makes and not from the router, which is another
// repository's: it serves the key it signs with at GET /edge/key, takes a
// yard's enrolment at POST /edge/enrol (answering a code, and the account's
// handle with the yard's name once a person has approved the yard),
// drops a yard's row at POST /edge/disconnect, mints a Google access
// token at POST /mint/google, takes a yard's preview reports at POST
// /preview/report (answering the id it issued the preview and its host),
// and serves the link at GET /edge/link (edgelink.go). A request is
// checked as a peer's is, signed by the yard whose key it names, to this
// edge's key, within yardkey.MaxSkew; every answer is signed with the key
// the stand-in is told to sign with.
//
// It is an http.Handler: a test serves it with httptest.NewTLSServer, whose
// client trusts its certificate, or httptest.NewServer for the loopback
// stand-in that plain http is for.
type Edge struct {
	key ed25519.PrivateKey

	mu    sync.Mutex
	b     EdgeBehaviour
	state EdgeState
	// asked counts the enrolling calls of each yard since it last asked to
	// be forgotten: a yard whose row is dropped is enrolled afresh.
	asked map[string]int
	// links are the links welcomed and not yet taken by the test, and
	// upgrades the signed upgrades taken.
	links    chan *EdgeLink
	upgrades int
	// members are the memberships a member add has landed, by yard and
	// address, so a removal of one never added answers 404 as the
	// edge's does.
	members map[string]bool
	// previews are the ids issued to the previews a yard has reported, by
	// yard and app, and lastPreview the latest id issued: an id is issued
	// once and never again, not even to a preview reported forgotten and
	// then again.
	previews    map[string]int
	lastPreview int
}

// EdgeBehaviour is what a test changes about an Edge, with Behave.
type EdgeBehaviour struct {
	// Served is the key GET /edge/key answers, and Signer the key that signs
	// the answers: the edge's own until a test serves or signs with another,
	// as an edge whose key changed would, or something that is not the edge.
	// ServedAfterApproval, when not nil, is the key served from the moment a
	// yard has been answered with its handle.
	Served, Signer, ServedAfterApproval ed25519.PrivateKey
	// ApproveAfter is how many enrolling calls of a yard get a code before
	// the person has approved it and a call is answered with its Handle,
	// Name and Email. A yard the edge knows already is approved at once:
	// zero.
	ApproveAfter int
	// Handle is the account's, and Name the yard's among its yards. Name
	// is the account's own until a test says otherwise: the yard named as
	// its account, as an edge with one yard per account has it.
	Handle, Name, Email string
	// Now is the time a request is judged by; nil is the clock.
	Now func() time.Time
	// Refuse, when not zero, is the status every enrolling call is refused
	// with, the reason being "expired"; DisconnectStatus is what a
	// disconnecting yard is answered, 200 when zero.
	Refuse, DisconnectStatus int
	// MintGranted and MintToken are the scopes and the token a mint is
	// answered with. MintRefuse, when not zero, is the status every mint
	// is refused with, in MintReason's words; MintUnsigned answers the
	// mint with no signature at all.
	MintGranted  []string
	MintToken    string
	MintRefuse   int
	MintReason   string
	MintUnsigned bool
	// MemberRefuse, when not zero, is the status every member report is
	// refused with, in MemberReason's words; MemberAddRefuse and
	// MemberRemoveRefuse refuse one report alone where they are not
	// zero. A refusal a test leaves zero is answered 200 (and a removal
	// of an address no add named, 404).
	MemberRefuse, MemberAddRefuse, MemberRemoveRefuse int
	MemberReason                                      string
	// PreviewRefuse, when not zero, is the status every preview report is
	// refused with, in PreviewReason's words; a report is then recorded
	// and issued no id. PreviewBase is what the hosts it answers are under,
	// p-<id>.<base>: PreviewBaseDefault when empty.
	PreviewRefuse int
	PreviewReason string
	PreviewBase   string
	// ChallengeSigner, when not nil, signs the link's challenge in place of
	// the edge's key, and ChallengeOverNonce, when not nil, is the yard nonce
	// it is signed over in place of the one the upgrade named: a challenge
	// that is not the edge's, or not this socket's.
	ChallengeSigner    ed25519.PrivateKey
	ChallengeOverNonce []byte
}

// EdgeEnrol is one enrolling call as the edge took it.
type EdgeEnrol struct {
	// Key and Name are the yard's, from the body; Time and Sig the
	// signature's own.
	Key, Name, Time, Sig string
}

// EdgeMint is one mint call as the edge took it.
type EdgeMint struct {
	// Key is the yard's, from the signature's header; Account the Google
	// account from the body; Time and Sig the signature's own.
	Key, Account, Time, Sig string
}

// EdgeMemberAdd is one member-add report as the edge took it.
type EdgeMemberAdd struct {
	// Key is the yard's, from the signature's header; the rest are the
	// body's, and Time and Sig the signature's own.
	Key, YardHandle, YardName, Email, YardUser, InvitedBy, YardKey, Time, Sig string
}

// EdgeMemberRemove is one member-remove report as the edge took it.
type EdgeMemberRemove struct {
	Key, YardHandle, YardName, Email, YardKey, Time, Sig string
}

// EdgePreviewReport is one preview report as the edge took it.
type EdgePreviewReport struct {
	// Key is the yard's, from the signature's header; App, Port, Kind and
	// State are the body's, and Time and Sig the signature's own.
	Key, App, Kind, State, Time, Sig string
	Port                             int
}

// EdgeState is what the edge has been asked.
type EdgeState struct {
	KeyFetches int
	Approved   bool
	Enrols     []EdgeEnrol
	// Drops are the keys of the yards that asked to be forgotten.
	Drops []string
	// Mints are the mint calls, in order.
	Mints []EdgeMint
	// MemberAdds and MemberRemoves are the member reports, in order.
	MemberAdds    []EdgeMemberAdd
	MemberRemoves []EdgeMemberRemove
	// PreviewReports are the preview reports, in order.
	PreviewReports []EdgePreviewReport
}

// NewEdge makes an edge with a key of its own, which approves a yard on its
// second enrolling call as mckean, and answers every call as it should.
func NewEdge() (*Edge, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Edge{key: key, asked: map[string]int{}, members: map[string]bool{}, previews: map[string]int{},
		b: EdgeBehaviour{Served: key, Signer: key, ApproveAfter: 1, Handle: "mckean", Name: "mckean", Email: "chris@mckean.co"}}, nil
}

// PublicKey is the edge's own key as yards write one.
func (e *Edge) PublicKey() string { return store.Public(e.key) }

// Behave changes the edge's behaviour: f is given it to change, under the
// edge's lock.
func (e *Edge) Behave(f func(*EdgeBehaviour)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(&e.b)
}

// State is a copy of what the edge has been asked so far.
func (e *Edge) State() EdgeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state
	s.Enrols = append([]EdgeEnrol(nil), s.Enrols...)
	s.Drops = append([]string(nil), s.Drops...)
	s.Mints = append([]EdgeMint(nil), s.Mints...)
	s.MemberAdds = append([]EdgeMemberAdd(nil), s.MemberAdds...)
	s.MemberRemoves = append([]EdgeMemberRemove(nil), s.MemberRemoves...)
	s.PreviewReports = append([]EdgePreviewReport(nil), s.PreviewReports...)
	return s
}

func (e *Edge) behaviour() EdgeBehaviour {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.b
}

func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "GET /edge/key":
		e.mu.Lock()
		e.state.KeyFetches++
		served := e.b.Served
		if e.state.Approved && e.b.ServedAfterApproval != nil {
			served = e.b.ServedAfterApproval
		}
		e.mu.Unlock()
		fmt.Fprintln(w, store.Public(served))
	case "POST /edge/enrol":
		e.enrol(w, r)
	case "POST /edge/disconnect":
		e.disconnect(w, r)
	case "POST /mint/google":
		e.mintGoogle(w, r)
	case "POST /member/add":
		e.memberAdd(w, r)
	case "POST /member/remove":
		e.memberRemove(w, r)
	case "POST /preview/report":
		e.previewReport(w, r)
	case "GET /edge/link":
		e.link(w, r)
	default:
		http.NotFound(w, r)
	}
}

// signed reads a request and checks that the yard it names signed it to this
// edge; when it did not, the stand-in has answered 401 and ok is false.
func (e *Edge) signed(w http.ResponseWriter, r *http.Request) (body []byte, ok bool) {
	body, _ = io.ReadAll(io.LimitReader(r.Body, 4<<10))
	now := time.Now
	if b := e.behaviour(); b.Now != nil {
		now = b.Now
	}
	sig := r.Header.Get(yardkey.HeaderSig)
	err := yardkey.VerifyRequest(r.Header.Get(yardkey.HeaderKey), e.PublicKey(), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, now())
	if err != nil {
		e.answer(w, r, http.StatusUnauthorized, map[string]string{"refused": err.Error()})
		return nil, false
	}
	return body, true
}

// answer writes v as the answer to r, signed as the stand-in is to sign.
func (e *Edge) answer(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set(yardkey.HeaderSig, store.SignResponse(e.behaviour().Signer, status, r.Header.Get(yardkey.HeaderSig), body))
	w.WriteHeader(status)
	w.Write(body)
}

func (e *Edge) enrol(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct{ Key, Name string }
	if json.Unmarshal(body, &in) != nil || in.Key != r.Header.Get(yardkey.HeaderKey) || in.Name == "" {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": "an enrolment names the yard's key, the one that signed it, and its name"})
		return
	}
	e.mu.Lock()
	e.state.Enrols = append(e.state.Enrols, EdgeEnrol{in.Key, in.Name, r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig)})
	e.asked[in.Key]++
	calls, b := e.asked[in.Key], e.b
	e.mu.Unlock()
	switch {
	case b.Refuse != 0:
		e.answer(w, r, b.Refuse, map[string]string{"refused": "expired"})
	case calls > b.ApproveAfter:
		e.mu.Lock()
		e.state.Approved = true
		e.mu.Unlock()
		e.answer(w, r, http.StatusOK, map[string]string{"handle": b.Handle, "name": b.Name, "email": b.Email})
	default:
		e.answer(w, r, http.StatusOK, map[string]any{"code": "WQ4F-7K2M", "expires": time.Now().Add(10 * time.Minute)})
	}
}

func (e *Edge) mintGoogle(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct{ Account string }
	if json.Unmarshal(body, &in) != nil || in.Account == "" {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": "a mint names the Google account to mint for"})
		return
	}
	e.mu.Lock()
	e.state.Mints = append(e.state.Mints, EdgeMint{r.Header.Get(yardkey.HeaderKey), in.Account, r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig)})
	b := e.b
	e.mu.Unlock()
	if b.MintRefuse != 0 {
		e.answer(w, r, b.MintRefuse, map[string]string{"refused": b.MintReason})
		return
	}
	granted := b.MintGranted
	if granted == nil {
		// The granted set is always an array on the wire, even where the
		// test granted nothing.
		granted = []string{}
	}
	v := map[string]any{"token": b.MintToken, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "granted_scopes": granted}
	if b.MintUnsigned {
		body, _ := json.Marshal(v)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return
	}
	e.answer(w, r, http.StatusOK, v)
}

func (e *Edge) memberAdd(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct {
		YardHandle string `json:"yard_handle"`
		YardName   string `json:"yard_name"`
		Email      string `json:"email"`
		YardUser   string `json:"yard_user"`
		InvitedBy  string `json:"invited_by"`
		YardKey    string `json:"yard_key"`
	}
	if json.Unmarshal(body, &in) != nil || in.YardHandle == "" || in.Email == "" || in.YardUser == "" {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": "a member add names its yard, the address and the yard user"})
		return
	}
	e.mu.Lock()
	e.state.MemberAdds = append(e.state.MemberAdds, EdgeMemberAdd{r.Header.Get(yardkey.HeaderKey), in.YardHandle, in.YardName, in.Email, in.YardUser, in.InvitedBy, in.YardKey, r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig)})
	refuse := e.b.MemberAddRefuse
	if refuse == 0 {
		refuse = e.b.MemberRefuse
	}
	reason := e.b.MemberReason
	if refuse == 0 {
		if e.members == nil {
			e.members = map[string]bool{}
		}
		e.members[in.YardHandle+"/"+in.YardName+"/"+in.Email] = true
	}
	e.mu.Unlock()
	if refuse != 0 {
		e.answer(w, r, refuse, map[string]string{"refused": reason})
		return
	}
	e.answer(w, r, http.StatusOK, map[string]string{"status": "invited"})
}

func (e *Edge) memberRemove(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct {
		YardHandle string `json:"yard_handle"`
		YardName   string `json:"yard_name"`
		Email      string `json:"email"`
		YardKey    string `json:"yard_key"`
	}
	if json.Unmarshal(body, &in) != nil || in.YardHandle == "" || in.Email == "" {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": "a member remove names its yard and the address"})
		return
	}
	e.mu.Lock()
	e.state.MemberRemoves = append(e.state.MemberRemoves, EdgeMemberRemove{r.Header.Get(yardkey.HeaderKey), in.YardHandle, in.YardName, in.Email, in.YardKey, r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig)})
	refuse := e.b.MemberRemoveRefuse
	if refuse == 0 {
		refuse = e.b.MemberRefuse
	}
	reason := e.b.MemberReason
	key := in.YardHandle + "/" + in.YardName + "/" + in.Email
	held := e.members[key]
	if refuse == 0 && held {
		delete(e.members, key)
	}
	e.mu.Unlock()
	if refuse != 0 {
		e.answer(w, r, refuse, map[string]string{"refused": reason})
		return
	}
	if !held {
		e.answer(w, r, http.StatusNotFound, map[string]string{"refused": "the yard holds no membership for that address"})
		return
	}
	e.answer(w, r, http.StatusOK, map[string]string{"status": "removed"})
}

// PreviewBaseDefault is what the hosts the stand-in answers a preview report
// with are under, until a test says another.
const PreviewBaseDefault = "preview.example.net"

// previewReport takes a yard's report of one preview as the router does
// (previews design, 4.2): the key names the yard, the app is named as the
// yard's registry names one, a port is a port except that a watch binds none,
// and a preview kept addressable (approved, running, stopped) is answered its
// id and host. The id is issued the first time the yard reports the app and
// never again. A forgotten preview is dropped and answers no host; another
// state is answered its id alone.
func (e *Edge) previewReport(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct {
		App   string `json:"app"`
		Port  int    `json:"port"`
		Kind  string `json:"kind"`
		State string `json:"state"`
	}
	bad := json.Unmarshal(body, &in) != nil || store.CheckPreviewApp(in.App) != nil || in.State == ""
	switch in.Kind {
	case "":
		bad = bad || in.Port < 1 || in.Port > 65535
	case store.PreviewKindWatch:
		bad = bad || in.Port < 0 || in.Port > 65535
	default:
		bad = true
	}
	if bad {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": `a preview report names the app, its port and its state, and a watch's kind: {"app":"demo","port":8080,"state":"running"}`})
		return
	}
	yard := r.Header.Get(yardkey.HeaderKey)
	e.mu.Lock()
	e.state.PreviewReports = append(e.state.PreviewReports, EdgePreviewReport{
		Key: yard, App: in.App, Port: in.Port, Kind: in.Kind, State: in.State,
		Time: r.Header.Get(yardkey.HeaderTime), Sig: r.Header.Get(yardkey.HeaderSig)})
	b := e.b
	if e.previews == nil {
		e.previews = map[string]int{}
	}
	key := yard + "/" + in.App
	id := e.previews[key]
	if b.PreviewRefuse == 0 {
		switch {
		case in.State == "forgotten":
			delete(e.previews, key)
		case id == 0:
			e.lastPreview++
			id = e.lastPreview
			e.previews[key] = id
		}
	}
	e.mu.Unlock()
	if b.PreviewRefuse != 0 {
		e.answer(w, r, b.PreviewRefuse, map[string]string{"refused": b.PreviewReason})
		return
	}
	answer := map[string]any{"status": in.State}
	switch in.State {
	case "forgotten":
	case "approved", "running", "stopped":
		base := b.PreviewBase
		if base == "" {
			base = PreviewBaseDefault
		}
		answer["preview_id"], answer["host"] = id, fmt.Sprintf("p-%d.%s", id, base)
	default:
		answer["preview_id"] = id
	}
	e.answer(w, r, http.StatusOK, answer)
}

func (e *Edge) disconnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := e.signed(w, r); !ok {
		return
	}
	e.mu.Lock()
	e.state.Drops = append(e.state.Drops, r.Header.Get(yardkey.HeaderKey))
	delete(e.asked, r.Header.Get(yardkey.HeaderKey))
	status := e.b.DisconnectStatus
	e.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	e.answer(w, r, status, map[string]string{})
}
