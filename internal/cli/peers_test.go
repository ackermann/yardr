package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// cliYard is a yard a test drives through the peer commands: a home with
// its key, a store, and a listener for its peers. Its operator is jo.
type cliYard struct {
	a    *app
	st   store.API
	own  *store.Store
	home string
	srv  *httptest.Server
	// lastCommand is the command its last yardr peer pair printed for the
	// other yard, code and all (pairBoth).
	lastCommand string
}

func newCLIYard(t *testing.T, name string) *cliYard {
	t.Helper()
	st, own := storetest.NewOwned(t)
	home := filepath.Join(shortDir(t, "yp"), name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(context.Background(), "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Home: home}
	y := &cliYard{a: &app{st: st, actor: "jo", cfg: cfg}, st: st, own: own, home: home}
	y.srv = httptest.NewServer(yards.NewReceiver(own, cfg.PeerKey(), nil).Handler())
	t.Cleanup(y.srv.Close)
	return y
}

func (y *cliYard) peer(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return run(t, y.a.peerCmd(), args...)
}

func (y *cliYard) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := y.peer(t, args...)
	if err != nil {
		t.Fatalf("peer %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// yardr peer key makes the key once and prints the same public key every
// time; a key file open to others is refused. The invite carries the
// yard's name, that key and the URL when one is given.
func TestPeerKeyAndInvite(t *testing.T) {
	y := newCLIYard(t, "north")
	t.Setenv("YARDR_HOME", y.home)
	first := strings.TrimSpace(y.must(t, "key"))
	if _, err := yardkey.ParsePublic(first); err != nil {
		t.Fatalf("peer key printed %q: %v", first, err)
	}
	st, err := os.Stat(filepath.Join(y.home, "peer.key"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("peer.key: %v, %v", st, err)
	}
	if again := strings.TrimSpace(y.must(t, "key")); again != first {
		t.Fatalf("peer key printed %q, then %q", first, again)
	}

	line := strings.TrimSpace(y.must(t, "invite", "--url", y.srv.URL+"/"))
	inv, err := yardkey.ParseInvite(line)
	if err != nil || inv.Name != "north" || inv.Key != first || inv.URL != y.srv.URL {
		t.Fatalf("invite %q = %+v, %v", line, inv, err)
	}
	if err := y.st.SetSetting(context.Background(), "name", "the-north", "jo"); err != nil {
		t.Fatal(err)
	}
	inv, err = yardkey.ParseInvite(strings.TrimSpace(y.must(t, "invite")))
	if err != nil || inv.Name != "the-north" || inv.Key != first || inv.URL != "" {
		t.Fatalf("invite without a URL = %+v, %v", inv, err)
	}
	if _, err := y.peer(t, "invite", "--url", "north.example"); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("invite --url north.example = %v", err)
	}

	if err := os.Chmod(filepath.Join(y.home, "peer.key"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"key", "invite"} {
		if out, err := y.peer(t, cmd); err == nil || !strings.Contains(err.Error(), "open to others") || strings.Contains(out, first) {
			t.Errorf("peer %s with the key file at 0640 = %q, %v", cmd, out, err)
		}
	}
}

// Who a yard is linked with, and what it takes from them, is the
// operator's to say: a crew member's session, and a bead's agent, are refused
// peer add and peer allow. Anyone may stop a kind or remove a peer.
func TestPeerAddAndAllowAreTheOperators(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", south.home)
	invite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	ctx := context.Background()

	for _, who := range []string{"yardmaster", "yard-10-new"} {
		north.a.actor = who
		if who == "yardmaster" {
			t.Setenv("YARDR_CREW", "yardmaster")
		} else {
			t.Setenv("YARDR_CREW", "")
		}
		if _, err := north.peer(t, "add", "south", invite); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may add a peer; this is "+who) {
			t.Fatalf("%s adding a peer: %v", who, err)
		}
	}
	if ps, _ := north.st.Peers(ctx); len(ps) != 0 {
		t.Fatalf("a refused peer was added: %+v", ps)
	}
	north.a.actor = "jo"
	out := north.must(t, "add", "south", invite)
	wantText(t, out, "peer south added (it calls itself south)", "the link carries nothing yet", "peer allow south --send ping --receive ping")
	p, err := north.st.GetPeer(ctx, "south")
	if err != nil || p.URL != south.srv.URL || len(p.Send)+len(p.Receive) != 0 {
		t.Fatalf("south as added = %+v, %v", p, err)
	}

	for _, who := range []string{"yardmaster", "yard-10-new"} {
		north.a.actor = who
		for _, flag := range []string{"--send", "--receive"} {
			if _, err := north.peer(t, "allow", "south", flag, "ping"); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may allow a peer's messages; this is "+who) {
				t.Fatalf("%s allowing %s ping: %v", who, flag, err)
			}
		}
	}
	if p, _ := north.st.GetPeer(ctx, "south"); len(p.Send)+len(p.Receive) != 0 {
		t.Fatalf("a refused allow was saved: %+v", p)
	}
	north.a.actor = "jo"
	if out := north.must(t, "allow", "south", "--send", "ping", "--receive", "ping"); !strings.Contains(out, "peer south: send ping; receive ping") {
		t.Fatalf("allow printed %q", out)
	}
	// Narrowing is anyone's, as it is in a crew member's definition.
	north.a.actor = "yardmaster"
	if out := north.must(t, "allow", "south", "--no-receive", "ping"); !strings.Contains(out, "send ping; receive nothing") {
		t.Fatalf("allow --no-receive printed %q", out)
	}
	north.a.actor = "jo"
	for args, want := range map[string]string{
		"allow south":                 "say what",
		"allow south --send beads":    "no kind of message between yards",
		"allow nobody --send ping":    "not found",
		"add south " + invite:         "is added already",
		"add twin " + invite:          "that key is peer south's already",
		"add other not-an-invite":     "is no invite",
		"add other yardr-peer-1,x,yz": "no yard's public key",
	} {
		if _, err := north.peer(t, strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("peer %s = %v, want %q", args, err, want)
		}
	}
	// A yard's own invite is not a peer to add.
	t.Setenv("YARDR_HOME", north.home)
	own := strings.TrimSpace(north.must(t, "invite"))
	if _, err := north.peer(t, "add", "me", own); err == nil || !strings.Contains(err.Error(), "this yard's own") {
		t.Fatalf("adding one's own invite = %v", err)
	}

	list := north.must(t, "list")
	wantText(t, list, "NAME", "south", south.srv.URL, "ping")
	show := north.must(t, "show", "south")
	wantText(t, show, "south  (it calls itself south)", "key      "+p.Key, "url      "+south.srv.URL, "send     ping", "receive  nothing",
		"incoming nothing can be posted to this yard, so it gets this peer's messages by asking it")
	north.a.actor = "yardmaster"
	if out := north.must(t, "rm", "south"); !strings.Contains(out, "south: removed") {
		t.Fatalf("rm printed %q", out)
	}
	if _, err := north.peer(t, "show", "south"); exitCode(err) != 3 {
		t.Fatalf("show of a removed peer = %v", err)
	}
}

// A peer that is never called, on a yard with no listener, cannot be heard
// at all, and show says so.
func TestPeerShowSaysNoListenerNoIncoming(t *testing.T) {
	north, laptop := newCLIYard(t, "north"), newCLIYard(t, "laptop")
	t.Setenv("YARDR_HOME", laptop.home)
	invite := strings.TrimSpace(laptop.must(t, "invite"))
	out := north.must(t, "add", "laptop", invite)
	wantText(t, out, "it gave no URL, so this yard never calls it", "peer invite --url <where it reaches this yard>")
	show := north.must(t, "show", "laptop")
	wantText(t, show, "url      none: this yard never calls it; it calls this one",
		"incoming none. No listener, no incoming: this peer is never called and cannot call")
	var got struct {
		Listening bool `json:"listening"`
	}
	north.a.json = true
	js := north.must(t, "show", "laptop")
	if err := json.Unmarshal([]byte(js), &got); err != nil || got.Listening {
		t.Fatalf("show --json = %s, %v", js, err)
	}
}

// yardr peer ping sends a ping, waits for the pong and says how long it
// took and which way each went; with nothing to carry it, it says what is
// missing.
func TestPeerPing(t *testing.T) {
	north, laptop := newCLIYard(t, "north"), newCLIYard(t, "laptop")
	// north can be called, laptop cannot.
	t.Setenv("YARDR_HOME", north.home)
	northInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	t.Setenv("YARDR_HOME", laptop.home)
	laptopInvite := strings.TrimSpace(laptop.must(t, "invite"))
	north.must(t, "add", "laptop", laptopInvite)
	laptop.must(t, "add", "north", northInvite)

	// Not allowed: refused before it leaves.
	if _, err := laptop.peer(t, "ping", "north", "--timeout", "1s"); err == nil || !strings.Contains(err.Error(), "does not carry ping out") {
		t.Fatalf("ping with nothing allowed = %v", err)
	}
	north.must(t, "allow", "laptop", "--send", "ping", "--receive", "ping")
	laptop.must(t, "allow", "north", "--send", "ping", "--receive", "ping")
	if _, err := laptop.peer(t, "ping", "nobody"); exitCode(err) != 3 {
		t.Fatalf("ping of no peer = %v", err)
	}
	// No daemon: nothing carries it, and the command says so.
	if _, err := laptop.peer(t, "ping", "north", "--timeout", "200ms"); err == nil ||
		!strings.Contains(err.Error(), "no pong from north within 200ms: no daemon runs on this yard") {
		t.Fatalf("ping with no daemon = %v", err)
	}

	// The daemons of both.
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	for _, y := range []*cliYard{north, laptop} {
		d := &dispatch.Dispatcher{Store: y.st, Runners: map[string]dispatch.Runner{}, PeerKey: y.a.cfg.PeerKey()}
		go func() {
			defer func() { done <- struct{}{} }()
			for ctx.Err() == nil {
				_ = d.Reconcile(ctx)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	defer func() {
		stop()
		<-done
		<-done
	}()

	out := laptop.must(t, "ping", "north", "--timeout", "20s")
	wantText(t, out, "pong from north in ", "the ping went by push, the pong came by pull")
	out = north.must(t, "ping", "laptop", "--timeout", "20s")
	wantText(t, out, "pong from laptop in ", "the ping went by pull, the pong came by push")
	north.a.json = true
	var res pinged
	if err := json.Unmarshal([]byte(north.must(t, "ping", "laptop", "--timeout", "20s")), &res); err != nil || res.Peer != "laptop" ||
		res.Out != "pull" || res.Back != "push" || res.Took <= 0 || res.ID == "" {
		t.Fatalf("ping --json = %+v, %v", res, err)
	}
}

// doctor says of the peers who they are, whom the mail of each reaches,
// and what keeps their messages from crossing: no key, no listener for a
// peer that must call, messages given up, mail taken for nobody.
func TestDoctorPeers(t *testing.T) {
	y := newCLIYard(t, "north")
	ctx := context.Background()
	part, err := peerPart(ctx, y.a.cfg, y.st)
	if err != nil || part.Configured || !strings.Contains(part.Detail, "linked with no other") || len(part.Notes) != 0 {
		t.Fatalf("with no peer: %+v, %v", part, err)
	}
	keys := map[string]string{}
	for _, name := range []string{"laptop", "south"} {
		k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = yardkey.Public(k)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "laptop", Key: keys["laptop"]}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "south", Key: keys["south"], URL: "http://south.test"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AllowPeer(ctx, "south", store.PeerAllow{Send: []string{"ping"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	m, err := y.st.PeerSend(ctx, "south", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.st.PeerSendFailed(ctx, "south", m.Seq, "connection refused", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	part, err = peerPart(ctx, y.a.cfg, y.st)
	if err != nil || !part.Configured || part.Detail != "laptop (it calls), south" {
		t.Fatalf("with two peers: %+v, %v", part, err)
	}
	notes := strings.Join(part.Notes, "\n")
	wantText(t, notes, "1 messages for peer south were given up", "this yard has no key", "peer key",
		"no listener, no incoming", "laptop cannot be called and so cannot be heard at all")

	// A link that takes mail and exposes nobody takes none: one from
	// before there was a roster, until its operator says who.
	const nobody = "the link with peer south takes mail and exposes nobody, so all its mail is refused"
	if strings.Contains(notes, "exposes nobody") {
		t.Fatalf("a link that takes no mail: %s", notes)
	}
	if _, err := y.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AllowPeer(ctx, "south", store.PeerAllow{Receive: []string{"mail"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	part, _ = peerPart(ctx, y.a.cfg, y.st)
	wantText(t, strings.Join(part.Notes, "\n"), nobody, "peer allow south --expose <crew member or user>,...")
	if part.Detail != "laptop (it calls), south (its mail reaches nobody)" {
		t.Fatalf("with mail taken for nobody: %q", part.Detail)
	}
	wantText(t, y.must(t, "show", "south"), "  expose   nobody: the link takes mail and exposes nobody, so all mail from south is refused (yardr peer allow south --expose <crew member or user>,...)\n")
	wantText(t, y.must(t, "allow", "south", "--expose", "yardmaster"), "peer south: send ping; receive mail; expose yardmaster\n")
	if part, _ = peerPart(ctx, y.a.cfg, y.st); strings.Contains(strings.Join(part.Notes, "\n"), "exposes nobody") {
		t.Fatalf("with someone exposed: %+v", part.Notes)
	}
	// Who is exposed is said per link that takes mail, beside its other mark.
	if _, err := y.st.AllowPeer(ctx, "laptop", store.PeerAllow{Receive: []string{"mail"}, Expose: []string{"jo", "yardmaster"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if part, _ = peerPart(ctx, y.a.cfg, y.st); part.Detail != "laptop (it calls; its mail reaches jo, yardmaster), south (its mail reaches yardmaster)" {
		t.Fatalf("with someone exposed: %q", part.Detail)
	}
	// The other way round the peer sees a name it cannot write to.
	wantText(t, y.must(t, "allow", "south", "--no-receive", "mail"), "expose yardmaster\nsouth sees them and cannot write to them: the link takes no mail (yardr peer allow south --receive mail)\n")
	// A link that takes no mail says nothing of whom it reaches.
	if part, _ = peerPart(ctx, y.a.cfg, y.st); !strings.HasSuffix(part.Detail, ", south") {
		t.Fatalf("with no mail taken: %q", part.Detail)
	}
	if _, err := y.peer(t, "allow", "south", "--expose", "builders"); err == nil || !strings.Contains(err.Error(), `"builders" is neither a crew member nor a user of this yard`) {
		t.Fatalf("exposing no one of the yard = %v", err)
	}
	if _, err := y.peer(t, "allow", "south"); err == nil || !strings.Contains(err.Error(), "--expose or --no-expose <name>") {
		t.Fatalf("allow with nothing to change = %v", err)
	}

	if _, _, err := yardkey.Ensure(y.a.cfg.PeerKey()); err != nil {
		t.Fatal(err)
	}
	part, _ = peerPart(ctx, y.a.cfg, y.st)
	if strings.Contains(strings.Join(part.Notes, "\n"), "no key") {
		t.Fatalf("with a key: %+v", part.Notes)
	}
	if err := os.Chmod(y.a.cfg.PeerKey(), 0o644); err != nil {
		t.Fatal(err)
	}
	part, _ = peerPart(ctx, y.a.cfg, y.st)
	wantText(t, strings.Join(part.Notes, "\n"), "this yard's key cannot be used", "open to others")
}

// yardr peer show lists whom the peer exposes to this yard as the peer
// said it: each name with what it is, a person for a user, and the peer's
// line of what they do. That its link takes no mail is said once, for all.
func TestPeerShowExposes(t *testing.T) {
	p := store.Peer{Name: "north"}
	if got := exposesLine(p); got != "" {
		t.Fatalf("a peer that has not said = %q", got)
	}
	p.Said = &store.PeerSaid{PeerSays: store.PeerSays{Understands: []string{"mail"}}}
	if got := exposesLine(p); got != "" {
		t.Fatalf("a peer that exposes nobody = %q", got)
	}
	p.Said.Roster = []store.PeerRosterEntry{
		{Name: "clerk", Kind: store.RosterCrew, Mail: true},
		{Name: "mckean", Kind: store.RosterUser, Role: "operator", Mail: true},
		{Name: "yardmaster", Kind: store.RosterCrew, Role: "Shapes the beads", Mail: true},
	}
	if got, want := exposesLine(p), "exposes: clerk (crew), mckean (person, operator), yardmaster (crew, Shapes the beads)"; got != want {
		t.Fatalf("exposes = %q, want %q", got, want)
	}
	for i := range p.Said.Roster {
		p.Said.Roster[i].Mail = false
	}
	wantText(t, exposesLine(p), "yardmaster (crew, Shapes the beads). Its link takes no mail from this yard, so none of them can be written to yet")
}

// yardr peer show says which kinds the peer's yardr last said it
// understands, with its build and when, or that it has not said; and yardr
// peer allow --send warns, and allows all the same, for a kind the peer has
// not said it understands.
func TestPeerShowAndAllowSayWhatThePeerUnderstands(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", south.home)
	invite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", invite)
	ctx := context.Background()
	p, err := north.st.GetPeer(ctx, "south")
	if err != nil {
		t.Fatal(err)
	}

	// Nothing has come from south: it has not said.
	wantText(t, north.must(t, "show", "south"), "  has not said what it understands (an older yardr")
	errs := captureStderr(t)
	out := north.must(t, "allow", "south", "--send", "ping", "--receive", "ping")
	wantText(t, out, "peer south: send ping; receive ping")
	wantText(t, errs.String(), "warning: south has not said what its yardr understands (an older yardr, or nothing has come from it yet): a ping sent to it may be refused there")
	if strings.Count(errs.String(), "warning:") != 1 {
		t.Fatalf("--receive warned, or --send twice:\n%s", errs)
	}

	// It said, on a build that knows no bead.
	if err := north.own.PeerHeard(ctx, "south", p.Key, &store.PeerSays{Understands: []string{"ping", "mail"}, Yardr: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	show := north.must(t, "show", "south")
	wantText(t, show, "  understands: mail, ping (yardr v1.2.3, said "+time.Now().Format("2006-01-02"))
	if strings.Contains(show, "has not said what it understands") {
		t.Fatalf("show says both:\n%s", show)
	}
	errs.Reset()
	out = north.must(t, "allow", "south", "--send", "bead", "--send", "mail", "--no-receive", "ping")
	wantText(t, out, "peer south: send bead, mail, ping; receive nothing")
	wantText(t, errs.String(), "warning: south's yardr (v1.2.3) said at ", " that it understands mail, ping, and not bead: a bead sent to it is refused there until it is upgraded")
	if strings.Count(errs.String(), "warning:") != 1 {
		t.Fatalf("a kind south understands was warned of, or bead was not:\n%s", errs)
	}
	// Stopping a kind warns of nothing.
	errs.Reset()
	north.must(t, "allow", "south", "--no-send", "bead")
	if errs.Len() != 0 {
		t.Fatalf("--no-send warned:\n%s", errs)
	}
	// As --json it is the peer's record, and the warning stays on stderr.
	north.a.json = true
	var got struct {
		Said *store.PeerSaid `json:"said"`
		Send []string        `json:"send"`
	}
	js := north.must(t, "allow", "south", "--send", "bead")
	if err := json.Unmarshal([]byte(js), &got); err != nil || got.Said == nil || got.Said.Yardr != "v1.2.3" || len(got.Send) != 3 {
		t.Fatalf("allow --json = %s, %v", js, err)
	}
	wantText(t, errs.String(), "and not bead")

	// The line as yardr peer show prints it for what this build says.
	at := time.Date(2026, 10, 6, 14, 10, 0, 0, time.Local)
	line := understandsLine(store.Peer{Name: "south", Said: &store.PeerSaid{PeerSays: *store.Says("v1.3.0+0123456789ab"), At: at}})
	if want := "understands: ask, bead, file, mail, ping, thread (yardr v1.3.0+0123456789ab, said 2026-10-06 14:10)"; line != want {
		t.Fatalf("understandsLine = %q, want %q", line, want)
	}
}

// doctor notes a flow in which an outcome of a stage a linked yard works
// leads to a terminal stage or to a script's: once per flow and stage, with
// the outcome and both stages, and for every depot the flow governs. A
// flow whose peer outcomes all lead to agents or people gets none.
func TestDoctorPeerLandings(t *testing.T) {
	y := newCLIYard(t, "laptop")
	ctx := context.Background()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(k), URL: "http://north.test"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{store.PeerBead}}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "builders", Runner: "herdr"}, {Name: "assembly", Runner: "exec", Config: map[string]any{"cmd": "true"}},
		{Name: "at-north", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "north"}},
	} {
		if err := y.st.PutGroup(ctx, g, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		"lands": "build -> approved on done | dropped on refused | review on unsure\nreview -> approved on done\n" +
			"approved -> merged on done\nmerged terminal\ndropped terminal\n",
		"seen": "build -> review on done\nreview -> approved on done\napproved -> merged on done\nmerged terminal\n",
	} {
		f, err := store.ParseFlow(text)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = name
		if _, err := y.st.PutFlow(ctx, *f, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for depot, flow := range map[string]string{"web": "lands", "api": "lands", "docs": "seen"} {
		if err := y.st.PutDepot(ctx, store.Depot{Name: depot, Base: "main"}, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := y.st.SetFlowPointer(ctx, depot, "", flow, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	landings := func() []string {
		t.Helper()
		part, err := peerPart(ctx, y.a.cfg, y.st)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range part.Notes {
			if strings.HasPrefix(n, "flow ") {
				out = append(out, n)
			}
		}
		return out
	}
	// The stages are routed, and none to the peer yet: nothing to note.
	for _, r := range []store.Route{{Stage: "review", Group: "builders"}, {Stage: "approved", Group: "assembly"}} {
		if _, err := y.st.AddRoute(ctx, r, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if got := landings(); len(got) != 0 {
		t.Fatalf("with no stage a peer works: %q", got)
	}
	if _, err := y.st.AddRoute(ctx, store.Route{Stage: "build", Group: "at-north"}, "jo"); err != nil {
		t.Fatal(err)
	}
	want := `flow lands (depots api, web): a linked yard (north) works the stage build, and its outcome done leads to approved, ` +
		`which the script of the group assembly works; its outcome refused leads to dropped, which is terminal. ` +
		`An outcome is the other yard's word, and nobody of this yard looks at what came back before that. ` +
		`Where that is not meant, lead the outcome to a stage that agents or people of this yard work ` +
		`(docs/peers.md, "What a returned outcome is evidence of")`
	if got := landings(); len(got) != 1 || got[0] != want {
		t.Fatalf("landings:\n got %q\nwant %q", got, want)
	}
	// A note and no fault: the report's faults are read off its first two
	// parts, and this is a note of the third.
	rep := &doctorReport{Home: y.home, Yard: true, Optional: []optionalPart{{Key: "peers", Notes: landings()}}}
	if rep.faults(); len(rep.Faults) != 0 {
		t.Errorf("faults = %q", rep.Faults)
	}
}
