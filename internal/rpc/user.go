package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
)

// A person's device calls the yard from outside it, on the yard's listener
// (the one the webhooks and the linked yards use), under UserPath:
//
//	POST /user/call/<Method>     one of Calls, arguments as a JSON object;
//	                             for a device of the operator, one of
//	                             OperatorCalls too (operatorCall), and for
//	                             a device of a user the one that gives
//	                             their own address or credential (ownSecret)
//	GET  /user/events?after=N    the stream, as on the socket but for the
//	                             value of a setting_changed the device is
//	                             not told, and but for mail, read receipts
//	                             and reactions it is no party to
//	                             (deviceEvent)
//	GET  /user/blobs/<hash>      a blob the caller may read, as the web
//	                             view serves it (userBlob)
//	GET  /user/me                {"user": ..., "said_by": ..., "yard": ...}
//	POST /user/pair              a pairing's code for the device's token (pair)
//
// It is docs/api.md over TCP with one header more, `Authorization: Bearer
// <credential>`, and the same implementation serves both (api). What makes
// it fit to stand on a port that may face the internet:
//
//   - only the calls of Calls, by name: every other method of the store, and
//     the array form yardr's own builds use, is answered as not there. The
//     calls more are the four of OperatorCalls, GiveSecret, ApproveAsk,
//     ApproveSetup and SourceGrants, for a device of the operator, and not
//     there for anyone else (operatorCall), but GiveSecret for a device of
//     a user whose own address or credential in a source is asked for
//     (ownSecret);
//   - every call is made as the user the credential resolves to (asUser);
//   - Settings answers the keys of deviceSettings and no other, and the
//     stream carries the value of a setting_changed for those alone
//     (deviceTold), so a lost token gives away no address or secret that
//     the yard keeps in another setting;
//   - the stream carries a mail, a read receipt and a reaction only to a
//     device of its sender or its receiver (deviceEvent), and a blob only
//     to one that may read it (userBlob), so a lost token gives away no
//     mail but its user's own;
//   - without a credential that resolves every path is 401, answered before
//     the body is read or anything of the yard but the credential's record;
//   - a refusal is counted by its reason and logged in sums (refuse), and
//     nothing is kept for the address it came from or for the credential, so
//     no number of them shuts out or slows a device at that address;
//   - with no device in the yard the paths are not there: 404.
//
// PairPath is the one path that takes no credential, since it is where a
// device comes for one: there only while the operator has a pairing open
// for a device, and for whoever has that pairing's code.
const (
	UserPath = "/user/"
	PairPath = "/user/pair"
)

const (
	// UserRefused is the kind of the event refused requests are logged as:
	// the reason, the status answered, how many since the last such event
	// for that reason and when the last of them was. Never anything of a
	// request.
	UserRefused = "user_refused"
	// RefuseWindow is how long one reason logs no second event; what is
	// refused meanwhile is counted, and said by the next (as the hook
	// receiver's RejectWindow).
	RefuseWindow = 10 * time.Minute
	// ResolveTimeout is how long a credential's resolver is waited for. One
	// that has not answered by then is given up and the request refused: a
	// device's is a lookup in the store, and what comes later (a source
	// that is asked who a bearer is) is a program that may not return.
	ResolveTimeout = 5 * time.Second
	// StreamRecheck is how often a stream's credential is resolved again.
	// A stream is closed when its credential no longer resolves: at once
	// for a device removed while the stream is read (endsAccess), and
	// within this long whatever took the way in away.
	StreamRecheck = time.Minute
	// StreamsPerCredential is how many streams one device holds at a time:
	// each is a subscription of the yard. More than a window opens, and one
	// past it is answered 503 with Retry-After, as the web view answers a
	// stream it has no place for.
	StreamsPerCredential = 16
	// maxCredential is the longest credential that is looked at.
	maxCredential = 4096
	// maxPair bounds the body of a pairing's request: a code.
	maxPair = 4 << 10
	// maxUserCall is the most bytes a call's arguments may be, but for
	// PutBlob, which is bounded by the yard's cap on a blob.
	maxUserCall = 1 << 20
)

// The reasons a request is refused for, as they are counted and logged.
const (
	refusedNone    = "no_credential"      // no Authorization header, or none that is one bearer
	refusedUnknown = "unknown_credential" // it resolves to nobody: wrong, or of a device since removed
	refusedTimeout = "resolve_timeout"    // the resolver did not answer in ResolveTimeout
	refusedStore   = "store"              // the yard could not look
)

// Caller is who a request is: a user of this yard, and what says so (the
// device's name).
type Caller struct {
	User   string `json:"user"`
	SaidBy string `json:"said_by"`
	// device is set for a caller the yard's own record of a device token
	// names (UserServer.device), and by no other resolver: a credential
	// that some service outside the yard vouches for is a user's word, and
	// is not what a call of OperatorCalls is served to (operatorCall).
	device bool
}

// Resolver answers who the bearer of credential is, nil for nobody. from is
// the address the request came from. It is asked under a deadline
// (ResolveTimeout) and need not honour it: one that does not return is left
// behind.
type Resolver func(ctx context.Context, credential, from string) (*Caller, error)

// UserServer serves UserPath and nothing else. It is meant for the yard's
// listener, in the process that owns the store.
type UserServer struct {
	st   *store.Store
	api  *api
	yard func(context.Context) string
	log  *slog.Logger
	now  func() time.Time

	resolve        Resolver
	resolveTimeout time.Duration
	recheck        time.Duration

	mu       sync.Mutex
	rejected map[string]int
	refused  map[string]*refusals // by reason: a fixed few, whatever is sent
	streams  map[string]int       // open streams by user NUL said-by
}

// refusals are the refusals for one reason since their last event.
type refusals struct {
	logged  time.Time
	pending int
	last    time.Time
}

// NewUserServer serves the devices of st's yard, which yard names for
// /user/me.
func NewUserServer(st *store.Store, yard func(context.Context) string, log *slog.Logger) *UserServer {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	u := &UserServer{st: st, api: newAPI(st, StreamWriteTimeout), yard: yard, log: log, now: time.Now,
		resolveTimeout: ResolveTimeout, recheck: StreamRecheck,
		rejected: map[string]int{}, refused: map[string]*refusals{}, streams: map[string]int{}}
	u.resolve = u.device
	u.api.methods["Settings"] = method{"Settings", reflect.ValueOf(u.settings)}
	return u
}

// deviceSettings are the settings a device is told: the operator, the caps
// on a file, and the operator's own words to the agents, the last to a
// device of the operator alone (deviceTold). What docs/api.md promises, and
// nothing else: no edge, no hook URL, no JWT audience, no backup directory.
// Settings answers these alone, and a setting_changed carries its value for
// these alone, so a lost device token gives away no address or secret of
// the yard.
var deviceSettings = []string{
	store.OperatorSetting,
	store.BlobMaxSetting,
	store.FilesMaxSetting,
	store.StyleSetting,
	store.NoteSetting,
}

// deviceTold reports whether a device of user is told the setting key: one
// of deviceSettings, but the operator's own words to the agents
// (operator_style, operator_note) only when user is the yard's operator.
// operator is the yard's operator setting, "" when it names none: a yard
// without an operator tells the words to no device, since there is nobody
// they are the words of.
func deviceTold(key, user, operator string) bool {
	if !slices.Contains(deviceSettings, key) {
		return false
	}
	if key == store.StyleSetting || key == store.NoteSetting {
		return operator != "" && operator == user
	}
	return true
}

// settings is Settings as a device is told it: the allow-listed settings
// alone (deviceTold), the operator's own words to the agents only to a
// device of the operator. The socket tells whoever calls, since an agent's
// session reads them there (yardr prime); over the network a device is a
// person's, and anything else stays on the yard's own machine.
func (u *UserServer) settings(ctx context.Context) (map[string]string, error) {
	all, err := u.st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var user string
	if c, _ := ctx.Value(callerKey{}).(*Caller); c != nil {
		user = c.User
	}
	out := map[string]string{}
	for key, value := range all {
		if deviceTold(key, user, all[store.OperatorSetting]) {
			out[key] = value
		}
	}
	return out, nil
}

// device resolves a device token: the one kind of credential there is.
func (u *UserServer) device(ctx context.Context, credential, from string) (*Caller, error) {
	d, err := u.st.ResolveDevice(ctx, credential, from)
	if err != nil || d == nil {
		return nil, err
	}
	return &Caller{User: d.User, SaidBy: d.Name, device: true}, nil
}

// Rejected counts the refused requests by reason.
func (u *UserServer) Rejected() map[string]int {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[string]int, len(u.rejected))
	for k, v := range u.rejected {
		out[k] = v
	}
	return out
}

func (u *UserServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /user/call/{method}", u.call)
	mux.HandleFunc("GET /user/events", u.events)
	mux.HandleFunc("GET /user/blobs/{hash}", u.userBlob)
	mux.HandleFunc("GET /user/me", func(w http.ResponseWriter, r *http.Request) {
		c := callerOf(r)
		writeJSON(w, struct {
			Caller
			Yard string `json:"yard"`
		}{*c, u.yard(r.Context())})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A device that has no token yet comes for one with a pairing's
		// code. Asked first: the device a pairing is opened for may be the
		// yard's first. With no pairing open the path is as any other here.
		if r.Method == http.MethodPost && r.URL.Path == PairPath && u.st.DevicePairOpened(r.Context()) {
			u.pair(w, r)
			return
		}
		// There only when someone can use it: a yard that never gave a
		// device a token has the listener it had before.
		if any, err := u.st.AnyDevice(r.Context()); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		} else if !any {
			http.NotFound(w, r)
			return
		}
		// Who is calling comes before what is asked for: a path that is not
		// served is 401 too for a caller that is not let in.
		c, reason := u.who(r)
		if c == nil {
			u.refuse(w, reason)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}

// Paired is the answer to the code of a device's pairing: the token, which
// is in no later answer, and what /user/me says of whoever carries it.
type Paired struct {
	Token  string `json:"token"`
	User   string `json:"user"`
	Device string `json:"device"`
	Yard   string `json:"yard"`
}

// pair takes the code of the yard's open device pairing, as the body
// {"code": ...}, and answers the device it was opened for with its token
// (docs/users.md, "Enrolled by a code"). A wrong code is 403 and counted by
// the pairing, which the fifth ends; the right one is good once. What is no
// code at all is 400 and no try. Nothing of a code is logged or counted.
func (u *UserServer) pair(w http.ResponseWriter, r *http.Request) {
	var asked struct {
		Code string `json:"code"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPair))
	if err == nil {
		err = json.Unmarshal(body, &asked)
	}
	var d *store.NewDevice
	if err == nil {
		d, err = u.st.DevicePairRedeem(r.Context(), asked.Code)
	}
	var no *pairing.Refused
	switch {
	case errors.As(err, &no) && no.Reason == pairing.NoPairing:
		// It ended between the look and the call: the path is gone.
		http.NotFound(w, r)
		return
	case errors.As(err, &no):
		u.mu.Lock()
		u.rejected["pair_"+no.Reason]++
		u.mu.Unlock()
		u.log.Warn("a device's pairing call refused", "reason", no.Reason)
		status := http.StatusConflict
		if no.Reason == pairing.WrongCode {
			status = http.StatusForbidden
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"refused": no.Reason})
		return
	case err != nil:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	u.log.Info("a device was paired", "user", d.User, "device", d.Name)
	// The one answer that holds the token: nothing between keeps it.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, Paired{Token: d.Token, User: d.User, Device: d.Name, Yard: u.yard(r.Context())})
}

type callerKey struct{}

func callerOf(r *http.Request) *Caller {
	c, _ := r.Context().Value(callerKey{}).(*Caller)
	return c
}

// who resolves the request's credential, or says why it is refused. It
// reads the request's headers and nothing else of it.
func (u *UserServer) who(r *http.Request) (*Caller, string) {
	credential, ok := bearer(r.Header.Values("Authorization"))
	if !ok {
		return nil, refusedNone
	}
	if len(credential) > maxCredential {
		return nil, refusedUnknown
	}
	c, err := u.resolveIn(r.Context(), credential, remote(r))
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil, refusedTimeout
	case err != nil:
		u.log.Error("a credential could not be resolved", "err", err)
		return nil, refusedStore
	case c == nil:
		return nil, refusedUnknown
	}
	return c, ""
}

// remote is the address r came from, as a device's last use keeps it. A
// header that names another address (X-Forwarded-For) is not read: whoever
// asks writes it.
func remote(r *http.Request) string {
	from, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return from
}

// resolveIn asks the resolver and waits no longer than resolveTimeout for
// it. The resolver runs on beside the wait, so one that does not return
// holds a goroutine and not the request.
func (u *UserServer) resolveIn(ctx context.Context, credential, from string) (*Caller, error) {
	ctx, cancel := context.WithTimeout(ctx, u.resolveTimeout)
	defer cancel()
	type answer struct {
		c   *Caller
		err error
	}
	done := make(chan answer, 1)
	go func() {
		c, err := u.resolve(ctx, credential, from)
		done <- answer{c, err}
	}()
	select {
	case a := <-done:
		return a.c, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// bearer is the credential of `Authorization: Bearer <credential>`, when
// the request has that header once and it is that.
func bearer(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	scheme, credential, ok := strings.Cut(values[0], " ")
	credential = strings.TrimSpace(credential)
	if !ok || !strings.EqualFold(scheme, "Bearer") || credential == "" {
		return "", false
	}
	return credential, true
}

// refuse answers a request that is not let in, the same whatever the
// reason, and counts it: by its reason alone. The event that says it is
// logged when the reason has had none within RefuseWindow; otherwise the
// next one says it, by its count.
func (u *UserServer) refuse(w http.ResponseWriter, reason string) {
	now := u.now()
	u.mu.Lock()
	u.rejected[reason]++
	t := u.refused[reason]
	if t == nil {
		t = &refusals{}
		u.refused[reason] = t
	}
	t.pending++
	t.last = now
	var due []refusedEvent
	if now.Sub(t.logged) >= RefuseWindow {
		due = append(due, t.take(reason, now))
	}
	u.mu.Unlock()
	u.logRefused(due)
	status := http.StatusUnauthorized
	if reason == refusedStore {
		status = http.StatusInternalServerError
	} else {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	http.Error(w, http.StatusText(status), status)
}

// refusedEvent is one UserRefused event to log.
type refusedEvent struct {
	reason string
	count  int
	last   time.Time
}

// take empties t into the event that says it. Called with u.mu held.
func (t *refusals) take(reason string, now time.Time) refusedEvent {
	e := refusedEvent{reason, t.pending, t.last}
	t.logged, t.pending = now, 0
	return e
}

// logRefused writes the events, outside u.mu: a slow store does not hold up
// the requests being refused meanwhile. One that is not written goes back
// on its count, for the next event to say.
func (u *UserServer) logRefused(due []refusedEvent) {
	for _, e := range due {
		status := http.StatusUnauthorized
		if e.reason == refusedStore {
			status = http.StatusInternalServerError
		}
		u.log.Warn("requests under /user/ refused", "reason", e.reason, "count", e.count)
		// Not a request's context: the sender going away does not unsay it.
		err := u.st.LogEvent(context.Background(), "", UserRefused, "yard", map[string]any{
			"reason": e.reason, "status": status, "count": e.count, "last": e.last.UTC().Format(time.RFC3339)})
		if err == nil {
			continue
		}
		u.log.Error("refused requests not recorded", "reason", e.reason, "count", e.count, "err", err)
		u.mu.Lock()
		if t := u.refused[e.reason]; t != nil {
			t.pending += e.count
		}
		u.mu.Unlock()
	}
}

// Flush logs what was refused within a window that has since ended, so a
// burst is said within RefuseWindow of its start and not only when the next
// refusal comes. With all it logs everything counted, as a server that
// stops does.
func (u *UserServer) Flush(all bool) {
	now := u.now()
	u.mu.Lock()
	var due []refusedEvent
	for reason, t := range u.refused {
		if t.pending > 0 && (all || now.Sub(t.logged) >= RefuseWindow) {
			due = append(due, t.take(reason, now))
		}
	}
	u.mu.Unlock()
	u.logRefused(due)
}

// Run flushes as windows end until ctx ends, and then everything counted:
// the server runs it beside the listener, and ends ctx while the store is
// still open.
func (u *UserServer) Run(ctx context.Context) {
	tick := time.NewTicker(RefuseWindow / 10)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			u.Flush(true)
			return
		case <-tick.C:
			u.Flush(false)
		}
	}
}

// selfArg is, for each call that names whose inbox or queue it reads or
// who writes, the argument that does: over the network it is the caller.
// Conversation names two parties and is checked by itself (asUser).
var selfArg = map[string]string{
	"GetUser":              "name",
	"UserInbox":            "name",
	"AckUserInbox":         "name",
	"UserNotifications":    "name",
	"AckUserNotifications": "name",
	"UserAsks":             "name",
	"Mail":                 "actor",
	"MailPeer":             "actor",
	"React":                "actor",
	"AnswerAsk":            "actor",
	"DeferAsk":             "actor",
	"DeclineAsk":           "actor",
	"TakeBackAsk":          "actor",
	"AddNote":              "actor",
	"GiveSecret":           "actor",
	"ApproveAsk":           "actor",
	"ApproveSetup":         "actor",
}

// asUser is the arguments body of the call method as user makes it: the
// argument that says whose inbox is read or who writes is filled in with
// user when it is left out, and refused when it names anyone else; one side
// of a Conversation is user. The arguments are an object, by name: the
// array is the form of yardr's own builds, and is not taken here.
func asUser(method string, body []byte, user string) (json.RawMessage, error) {
	if b := bytes.TrimLeft(body, " \t\r\n"); len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("arguments: over the network %s takes them as a JSON object, by name", method)
	}
	var given map[string]json.RawMessage
	if err := json.Unmarshal(body, &given); err != nil {
		return nil, fmt.Errorf("arguments: %w", err)
	}
	me, _ := json.Marshal(user)
	// self is the name the argument arg gives, "" for one left out, null or
	// empty. An argument of another type is no name (ok false): it is left
	// as it is, for the call to refuse as it does on the socket, and no
	// call is made with it.
	self := func(arg string) (name string, ok bool) {
		raw, has := given[arg]
		if !has {
			return "", true
		}
		return name, json.Unmarshal(raw, &name) == nil
	}
	if arg := selfArg[method]; arg != "" {
		switch name, ok := self(arg); {
		case !ok:
		case name == "":
			given[arg] = me
		case name != user && isOperatorCall(method):
			// Not said back: such a call may carry a secret, and a client
			// that put it where the name goes would read it in the refusal.
			return nil, fmt.Errorf("this device calls as %s: %q is %s or left out", user, arg, user)
		case name != user:
			return nil, fmt.Errorf("this device calls as %s: %q is %s or left out, not %s", user, arg, user, name)
		}
	}
	if method == "Conversation" {
		a, aok := self("a")
		b, bok := self("b")
		if aok && a == "" {
			given["a"], a = me, user
		}
		if aok && bok && a != user && b != user {
			return nil, fmt.Errorf("this device calls as %s: one of \"a\" and \"b\" is %s", user, user)
		}
	}
	return json.Marshal(given)
}

func isOperatorCall(method string) bool {
	_, ok := OperatorCalls[method]
	return ok
}

// operatorCall reports whether the call name is one of OperatorCalls and c
// may make it here: c is a device of the yard's operator. Anyone else is
// answered as for a method the yard does not have, so the listener is for
// them what it was before it served the call.
//
// On the socket the store holds such a call to two things (maySource): the
// actor is the operator, and the call does not come from the pane of an
// agent. Of the two the listener can know the first, and knows it better
// than the socket does: there the actor is the caller's word, here it is
// the user of the token the request carries (asUser), which the yard gave
// to a device the operator added for themselves. The second it cannot
// know. The pane is found by the process at the other end of the socket,
// and a request over TCP has no such process: the store looks for a pane,
// finds none and lets the call through. What stands in its place is the
// token: 256 random bits shown once, to the operator, and kept by their
// device. An agent that reads it from that device's file calls as the
// operator, and gains what an agent that leaves its pane's process tree
// gains on the socket: to replace a credential, never to read one, and to
// approve a run ask, which runs one command of the store's fixed list
// (store.ApproveAsk) and no other.
//
// A token is asked for, and not only a credential that resolves to the
// operator: one that a service outside the yard vouches for (an identity
// source) would let that service set the yard's credentials, and have the
// operator's commands run, by saying who is calling. So the narrower of the two, until someone decides otherwise.
// A yard without an operator serves the call to nobody.
func (u *UserServer) operatorCall(ctx context.Context, c *Caller, name string) bool {
	if !isOperatorCall(name) || !c.device {
		return false
	}
	all, err := u.st.Settings(ctx)
	return err == nil && all[store.OperatorSetting] != "" && all[store.OperatorSetting] == c.User
}

// ownSecret reports whether body, the arguments of GiveSecret from the
// device c of a user who is not the operator, names a secret ask for that
// user's own address in a source (store.AskSecret.User) or for their own
// credential in one (store.AskSecret.As). Those givings are the user's own
// to make, on the socket (the store's mayTell) and so here: each writes a
// file of theirs and no credential of the yard. For every other ask, and
// for arguments that name none, the call stays what it is to anyone but
// the operator: not there. Who may give is still the store's to decide,
// with the user of the device's token as the actor (asUser).
func (u *UserServer) ownSecret(ctx context.Context, c *Caller, body []byte) bool {
	var named struct {
		Bead string `json:"bead"`
		Ask  uint64 `json:"ask"`
	}
	if json.Unmarshal(body, &named) != nil || named.Bead == "" || c.User == "" {
		return false
	}
	notes, err := u.st.Notes(ctx, named.Bead)
	if err != nil {
		return false
	}
	for _, n := range notes {
		if n.Seq != named.Ask || n.Kind != store.NoteAsk {
			continue
		}
		ask, err := store.ParseAsk(n)
		return err == nil && ask.Secret != nil && (ask.Secret.User == c.User || ask.Secret.As == c.User)
	}
	return false
}

// linkRemoteAddr is the RemoteAddr of a request that came down the link
// from the edge. It is the link's own address said here, not imported:
// the link's tests serve this package's user server, so the import would
// cycle. A test holds the two equal.
const linkRemoteAddr = "edge:0"

// inTheClear says why the call name of OperatorCalls is not taken from the
// request r, "" when it is taken. router is the yard's router_addr setting:
// the address of its router on the user's network, "" when unset.
// yardr does not do TLS: the listener speaks plain HTTP, and what ends TLS
// stands in front of it. So a secret, and an approval that runs a command
// as the operator, is taken only from a request
// that reached the listener from this machine, which is the proxy or the
// tunnel that ended TLS here, or a client on the machine itself; and not
// from one whose proxy says it came in plain (X-Forwarded-Proto, read for a
// no and never for a yes: whoever asks writes it). It is what the listener
// can see of the client's rule, "https, or loopback" (docs/api.md), and no
// more than that: a proxy on this machine that takes plain HTTP from
// outside and does not say so is not seen.
//
// A hosted yard's router is the one peer besides this machine a secret is
// taken from: the address the setting names, saying the request came to it
// over https (fromRouter). An approval is not: ApproveAsk and ApproveSetup
// stay loopback-only, whatever the setting names.
//
// An approval holds no secret of its own. It is held to the same rule for
// what it does: a token read off a network in the clear would otherwise
// have the operator's commands run from wherever it was read.
//
// The refusal comes after the request has crossed, so it says that too.
//
// Over the link from the edge the listener's reason does not hold: the
// request came down the link, and secrets and approvals never cross the
// edge (design yardr-hosted-050n 3.6). The refusal names that reason,
// answers on the yard's own machine as the fix, and says the edge saw
// what this one carried, so the key or the device is remade.
func inTheClear(r *http.Request, name, router string) string {
	if name == "SourceGrants" {
		return sourceGrantsInTheClear(r, router)
	}
	what, flag := "a secret", "--secret"
	crossed := " What was sent may have crossed a network in the clear: if it was a real key, make a new one at the service."
	if name == "ApproveAsk" || name == "ApproveSetup" {
		what, flag = "an approval", "--approve"
		if name == "ApproveSetup" {
			flag = "<n> --secret"
		}
		crossed = " The device's token was sent with it and may have crossed a network in the clear: if it did, remove the device and add it again (yardr user device rm)."
	}
	if r.RemoteAddr == linkRemoteAddr {
		never, seen := "a secret never crosses the edge",
			" The edge saw what was sent with it: if it was a real key, make a new one at the service."
		if name == "ApproveAsk" || name == "ApproveSetup" {
			never, seen = "an approval never crosses the edge",
				" The edge saw the device's token with it: remove the device and add it again (yardr user device rm)."
		}
		return never + ": this request came down the link from the edge. " +
			"Answer on the yard's machine (ssh -t <host> yardr bead answer <bead> <ask> " + flag + ")." + seen
	}
	if ip := net.ParseIP(remote(r)); ip == nil || !ip.IsLoopback() {
		if name == "GiveSecret" && fromRouter(r, router) {
			return ""
		}
		return what + " is taken over https only, or from the yard's own machine: this request reached the yard's listener in plain HTTP from another machine. " +
			"Put the proxy or tunnel that ends TLS on the yard's machine, or answer there (ssh -t <host> yardr bead answer <bead> <ask> " + flag + ")." + crossed
	}
	for _, proto := range r.Header.Values("X-Forwarded-Proto") {
		for _, p := range strings.Split(proto, ",") {
			if !strings.EqualFold(strings.TrimSpace(p), "https") {
				return what + " is taken over https only, or from the yard's own machine: the proxy in front of the yard says this request came to it in plain HTTP. " +
					"Give the device the yard's https:// address." + crossed
			}
		}
	}
	return ""
}

// sourceGrantsInTheClear keeps the operator's bearer token under the same
// transport rule as the other operator calls, while naming this read's
// command and the device to replace if the token crossed in plain.
func sourceGrantsInTheClear(r *http.Request, router string) string {
	const crossed = " The device's token was sent with it and may have crossed a network in the clear: if it did, remove the device and add it again (yardr user device rm)."
	const readHere = "Read it on the yard's machine (`yardr source grants`)."
	if r.RemoteAddr == linkRemoteAddr {
		return "the operator's grant list is not read over the link: this request came down the link from the edge. " +
			readHere + " The edge saw the device's token with it: remove the device and add it again (yardr user device rm)."
	}
	if ip := net.ParseIP(remote(r)); ip == nil || !ip.IsLoopback() {
		if fromRouter(r, router) {
			return ""
		}
		return "the operator's grant list is taken over https only, or from the yard's own machine: this request reached the yard's listener in plain HTTP from another machine. " +
			"Put the proxy or tunnel that ends TLS on the yard's machine, or " + readHere + crossed
	}
	for _, proto := range r.Header.Values("X-Forwarded-Proto") {
		for _, p := range strings.Split(proto, ",") {
			if !strings.EqualFold(strings.TrimSpace(p), "https") {
				return "the operator's grant list is taken over https only, or from the yard's own machine: the proxy in front of the yard says this request came to it in plain HTTP. " +
					"Give the device the yard's https:// address, or " + readHere + crossed
			}
		}
	}
	return ""
}

// fromRouter reports whether the request r reached the listener from the
// yard's router: the peer the router_addr setting names, saying the
// request came to it over https. The header is read for a yes here, as it
// is read for a no from the yard's own machine: the router is the edge,
// and a secret is taken from it only for what came to it over https.
// Anything else — no header, or any word but https — is not from the
// router. The peer is compared as an address, not as words, so the
// setting's form of it does not matter.
func fromRouter(r *http.Request, router string) bool {
	addr := net.ParseIP(router)
	peer := net.ParseIP(remote(r))
	if addr == nil || peer == nil || !peer.Equal(addr) {
		return false
	}
	protos := r.Header.Values("X-Forwarded-Proto")
	if len(protos) == 0 {
		return false
	}
	for _, proto := range protos {
		for _, p := range strings.Split(proto, ",") {
			if !strings.EqualFold(strings.TrimSpace(p), "https") {
				return false
			}
		}
	}
	return true
}

// routerAddr is the yard's router_addr setting: the address of its router
// on the user's network, "" when unset. It is read on every call, so a
// change moves nothing that runs; what cannot be read is no router.
func (u *UserServer) routerAddr(ctx context.Context) string {
	all, err := u.st.Settings(ctx)
	if err != nil {
		return ""
	}
	return all[store.RouterAddrSetting]
}

func (u *UserServer) call(w http.ResponseWriter, r *http.Request) {
	c, name := callerOf(r), r.PathValue("method")
	_, listed := Calls[name]
	operators := !listed && u.operatorCall(r.Context(), c, name)
	// GiveSecret from a device of anyone else may still be that user's own
	// to make (ownSecret), which what it names says. An approval is not:
	// to anyone but the operator's device it is not there, body unread.
	own := !listed && !operators && name == "GiveSecret" && c.device
	if !listed && !operators && !own {
		writeJSON(w, noSuchMethod(name))
		return
	}
	if operators {
		if why := inTheClear(r, name, u.routerAddr(r.Context())); why != "" {
			writeJSON(w, reply{Error: toWire(errors.New(why))})
			return
		}
	}
	limit := int64(maxUserCall)
	if name == "PutBlob" {
		limit += int64(base64.StdEncoding.EncodedLen(u.blobMax(r.Context())))
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if own {
		if !u.ownSecret(r.Context(), c, body) {
			writeJSON(w, noSuchMethod(name))
			return
		}
		if why := inTheClear(r, name, u.routerAddr(r.Context())); why != "" {
			writeJSON(w, reply{Error: toWire(errors.New(why))})
			return
		}
	}
	args, err := asUser(name, body, c.User)
	if err != nil {
		writeJSON(w, reply{Error: toWire(err)})
		return
	}
	u.api.call(w, r.WithContext(store.WithVia(r.Context(), c.User, c.SaidBy)), name, args)
}

// userBlob serves a blob as the web view serves it to the caller: a note's
// file and a kept hook delivery's to any device, and a mail's file to a
// device of its sender or recipient (NoteBlob, DeliveryBlob, MailBlob). A
// delivery reaches every device user already (deviceEvent), so its files
// do too. Anything else is 404, as there: one the
// yard never held, a name that is no hash, another user's mail file. One
// exception, which the web view has no use for: a file the caller's own
// user put within the hour that no mail or note names yet is served to
// the caller's device (unclaimedPut).
// A device puts before it sends, so its own upload would otherwise be
// unreadable to it until its mail goes; another user's device is served
// no upload of this user's, whatever hash it asks with.
func (u *UserServer) userBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	ctx, c := r.Context(), callerOf(r)
	_, noteErr := u.st.NoteBlob(ctx, hash)
	_, hookErr := u.st.DeliveryBlob(ctx, hash)
	_, mailErr := u.st.MailBlob(ctx, c.User, hash)
	switch {
	case noteErr == nil || hookErr == nil || mailErr == nil || u.unclaimedPut(ctx, c.User, hash):
		u.api.blob(w, r)
	case !errors.Is(noteErr, store.ErrNotFound):
		http.Error(w, noteErr.Error(), http.StatusInternalServerError)
	case !errors.Is(hookErr, store.ErrNotFound):
		http.Error(w, hookErr.Error(), http.StatusInternalServerError)
	case !errors.Is(mailErr, store.ErrNotFound):
		http.Error(w, mailErr.Error(), http.StatusInternalServerError)
	default:
		http.Error(w, noteErr.Error(), http.StatusNotFound)
	}
}

// unclaimedPut reports whether hash is a blob user's device put within
// BlobGrace that no mail or note names yet: one whose mail or note is on
// its way, as pruneBlobs keeps it. BlobPutBy checks the hash before it is
// used as a path; the stat comes before the mail scan because most owned
// uploads are old.
func (u *UserServer) unclaimedPut(ctx context.Context, user, hash string) bool {
	by, err := u.st.BlobPutBy(ctx, hash)
	if err != nil || by == "" || by != user {
		return false
	}
	st, err := os.Stat(u.st.BlobPath(hash))
	if err != nil || st.ModTime().Before(u.now().Add(-store.BlobGrace)) {
		return false
	}
	mailed, err := u.st.BlobMailed(ctx, hash)
	return err == nil && !mailed
}

// blobMax is the yard's cap on a blob, which PutBlob holds its data to.
func (u *UserServer) blobMax(ctx context.Context) int {
	all, _ := u.st.Settings(ctx)
	if n, err := strconv.Atoi(all[store.BlobMaxSetting]); err == nil && n > 0 {
		return n
	}
	return store.DefaultBlobMax
}

// events is the stream for a device: as the socket's, but for what
// deviceEvent holds back, for as long as the credential it was opened with
// resolves to the same caller.
func (u *UserServer) events(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	key := c.User + "\x00" + c.SaidBy
	u.mu.Lock()
	full := u.streams[key] >= StreamsPerCredential
	if !full {
		u.streams[key]++
	}
	u.mu.Unlock()
	if full {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many event streams are open for this device: close one, or try again shortly", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		u.mu.Lock()
		// A device with no stream left is forgotten.
		if u.streams[key]--; u.streams[key] <= 0 {
			delete(u.streams, key)
		}
		u.mu.Unlock()
	}()
	credential, _ := bearer(r.Header.Values("Authorization"))
	still := func() bool {
		now, _ := u.resolveIn(r.Context(), credential, remote(r))
		return now != nil && *now == *c
	}
	u.api.events(w, r, still, u.recheck, func(e store.Event) (store.Event, bool) {
		return u.deviceEvent(r.Context(), c, e)
	})
}

// deviceEvent is the event e as the device c is sent it, and whether it is
// sent at all: as it is, but for a setting_changed, which carries its
// value only for a setting the device is told (deviceTold), and but for
// mail, which goes out only to its sender or its receiver. The key of a
// setting is always told: a device matches on it alone for what it reads
// again, and the values it is not told stay on the yard's own machine
// with the settings themselves. A hook delivery goes out to every device,
// as a bead's events do: it is the yard's news, told to no one user, and
// whose subscriptions it reached is on the event.
func (u *UserServer) deviceEvent(ctx context.Context, c *Caller, e store.Event) (store.Event, bool) {
	switch e.Kind {
	case "mail":
		// Whose inbox it is in is the store's to say (MailReceiver), as
		// for the inbox itself: mail that left for a linked yard is its
		// sender's alone.
		return e, e.Actor == c.User || store.MailReceiver(e) == c.User
	case "mail_read":
		// The reader alone: who wrote what was read is not named, so a
		// sender reads again whether its mail was read.
		reader, _ := e.Data["reader"].(string)
		if reader == "" {
			reader = e.Actor
		}
		return e, reader == c.User
	case "reaction":
		// The reactor and the other party to the mail, as React names
		// them: the reaction carries the mail's first words.
		to, _ := e.Data["to"].(string)
		return e, e.Actor == c.User || to == c.User
	case "setting_changed":
		key, _ := e.Data["key"].(string)
		operator := ""
		if key == store.StyleSetting || key == store.NoteSetting {
			// The operator alone is told the words: read who that is now, so
			// a change of operator moves the next such event with it.
			if all, err := u.st.Settings(ctx); err == nil {
				operator = all[store.OperatorSetting]
			}
		}
		if deviceTold(key, c.User, operator) {
			return e, true
		}
		e.Data = maps.Clone(e.Data)
		delete(e.Data, "value")
		return e, true
	default:
		return e, true
	}
}
