package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
)

// pairYards are two yards whose operators pair them: north is the one that
// is called, and its server has said it listens where its receiver does.
func pairYards(t *testing.T) (north, laptop *cliYard) {
	t.Helper()
	old, stretch := pairPoll, pairing.Stretch
	pairPoll.host, pairPoll.caller, pairing.Stretch = 5*time.Millisecond, 5*time.Millisecond, 16
	t.Cleanup(func() { pairPoll, pairing.Stretch = old, stretch })
	north, laptop = newCLIYard(t, "north"), newCLIYard(t, "laptop")
	said(t, north.a.cfg, hooks.Listener{Addr: strings.TrimPrefix(north.srv.URL, "http://"), From: hooks.FromSetting})
	return north, laptop
}

var pairCommand = regexp.MustCompile(`yardr peer pair (http://\S+) ([0-9A-Z]{4}-[0-9A-Z]{4})`)

// output is what the two commands print, read while they run.
type output struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

// pairBoth runs yardr peer pair on north and, once that has shown its code,
// on laptop with what call makes of the address and the code, and then
// after, while north's may still wait. It returns what the two printed and
// how each ended.
func pairBoth(t *testing.T, north, laptop *cliYard, host []string, call func(addr, code string) []string, after ...func()) (out string, hostErr, callErr error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var printed output
	read := make(chan struct{})
	go func() { _, _ = io.Copy(&printed, r); close(read) }()
	hosted := make(chan error, 1)
	go func() {
		c := north.a.peerCmd()
		c.SetArgs(append([]string{"pair"}, host...))
		c.SilenceUsage, c.SilenceErrors = true, true
		hosted <- c.Execute()
	}()
	var m []string
	for deadline := time.Now().Add(10 * time.Second); m == nil && time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		m = pairCommand.FindStringSubmatch(printed.String())
		select {
		case hostErr = <-hosted:
			hosted <- hostErr
			deadline = time.Now()
		default:
		}
	}
	if m != nil {
		north.lastCommand = m[0]
		c := laptop.a.peerCmd()
		c.SetArgs(append([]string{"pair"}, call(m[1], m[2])...))
		c.SilenceUsage, c.SilenceErrors = true, true
		callErr = c.Execute()
	}
	for _, fn := range after {
		fn()
	}
	select {
	case hostErr = <-hosted:
	case <-time.After(20 * time.Second):
		t.Error("yardr peer pair on the host did not end")
	}
	os.Stdout = old
	w.Close()
	<-read
	if m == nil && hostErr == nil {
		t.Fatalf("the host showed no code:\n%s", printed.String())
	}
	return printed.String(), hostErr, callErr
}

// bothStderr collects what the two commands, which run at once, write to
// stderr.
func bothStderr(t *testing.T) *output {
	t.Helper()
	var errs output
	old := stderr
	stderr = &errs
	t.Cleanup(func() { stderr = old })
	return &errs
}

func withCode(extra ...string) func(addr, code string) []string {
	return func(addr, code string) []string { return append([]string{addr, code}, extra...) }
}

func peersOf(t *testing.T, y *cliYard) []store.Peer {
	t.Helper()
	ps, err := y.st.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func eventsOf(t *testing.T, y *cliYard, kind string) []store.Event {
	t.Helper()
	evs, err := y.st.Events(context.Background(), "", 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

var checkLine = regexp.MustCompile(`check  (\d{3} \d{3})`)

// yardr peer pair on the yard that is called shows a code and the command
// for the other; that command there does the rest. Both print the same
// check, each saves the other with ping allowed both ways, and each prints
// the command that allows mail. With no daemon the ping is said to be
// owed, and the pairing stands.
func TestPeerPair(t *testing.T) {
	north, laptop := pairYards(t)
	setInteractive(t, false)
	errs := bothStderr(t)
	out, hostErr, callErr := pairBoth(t, north, laptop, []string{"--yes", "--timeout", "50ms"}, withCode("--yes"))
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	wantText(t, out,
		"Pairing is open for 10 minutes, until ", "for one yard", "yardr peer pair "+north.srv.URL+" ",
		"Waiting for the other yard to call. Ctrl+C ends the pairing.",
		"laptop called (it calls itself laptop). Its key: ",
		"north answered at "+north.srv.URL+" (it calls itself north). Its key: ",
		"--yes: the check is confirmed without being compared with laptop's",
		"--yes: the check is confirmed without being compared with north's",
		"peer laptop saved (it calls itself laptop): the link carries ping both ways and nothing else",
		"it gave no URL, so this yard never calls it",
		"peer north saved (it calls itself north): the link carries ping both ways and nothing else",
		"peer allow laptop --send mail --receive mail",
		"peer allow north --send mail --receive mail")
	checks := checkLine.FindAllStringSubmatch(out, -1)
	if len(checks) != 2 || checks[0][1] != checks[1][1] {
		t.Fatalf("the checks printed: %v\n%s", checks, out)
	}
	if n := strings.Count(errs.String(), "the link is saved, and not proved yet: "); n != 2 || !strings.Contains(errs.String(), "no daemon runs on this yard") {
		t.Fatalf("stderr:\n%s", errs.String())
	}

	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || onNorth[0].Name != "laptop" || onNorth[0].URL != "" || strings.Join(append(onNorth[0].Send, onNorth[0].Receive...), ",") != "ping,ping" {
		t.Fatalf("north's peers = %+v", onNorth)
	}
	if len(onLaptop) != 1 || onLaptop[0].Name != "north" || onLaptop[0].URL != north.srv.URL || strings.Join(append(onLaptop[0].Send, onLaptop[0].Receive...), ",") != "ping,ping" {
		t.Fatalf("laptop's peers = %+v", onLaptop)
	}
	if onNorth[0].Key == onLaptop[0].Key {
		t.Fatal("both yards saved one key")
	}
	// The code is in no event of either yard.
	code := pairCommand.FindStringSubmatch(out)[2]
	for _, y := range []*cliYard{north, laptop} {
		evs, _ := y.st.Events(context.Background(), "", 200)
		b, _ := json.Marshal(evs)
		if bytes.Contains(b, []byte(code)) || bytes.Contains(b, []byte(strings.ReplaceAll(code, "-", ""))) {
			t.Errorf("the code is in an event: %s", b)
		}
	}
	if len(eventsOf(t, north, "peer_pair_opened")) != 1 || len(eventsOf(t, north, "peer_paired")) != 1 || len(eventsOf(t, laptop, "peer_paired")) != 1 {
		t.Fatal("the pairing's events are not all there")
	}
	// The pairing is over: --end has nothing to end. An open one it ends.
	if out, err := north.peer(t, "pair", "--end"); err == nil || !strings.Contains(err.Error(), "this yard has no pairing open") {
		t.Fatalf("pair --end after a pairing = %q, %v", out, err)
	}
	opened, err := north.st.PairOpen(context.Background(), store.PairOffer{Name: "north", Key: onLaptop[0].Key, URL: north.srv.URL}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := north.peer(t, "pair", "--end"); err != nil || !strings.Contains(out, "the pairing is ended") {
		t.Fatalf("pair --end = %q, %v", out, err)
	}
	if p, err := north.st.GetPairing(context.Background(), opened.ID); err != nil || p.State != store.PairEnded || p.Reason != pairing.Closed {
		t.Fatalf("the pairing after --end = %+v, %v", p, err)
	}
}

// With --json the yard that opens the pairing prints the code as a first
// document and the result as a second; the one that calls prints the
// result. The names given with --name are the ones saved.
func TestPeerPairJSON(t *testing.T) {
	north, laptop := pairYards(t)
	bothStderr(t)
	north.a.json, laptop.a.json = true, true
	// The code is read from the first document as a script would read it.
	var out string
	var hostErr, callErr error
	func() {
		old := pairCommand
		pairCommand = regexp.MustCompile(`"command": "yardr peer pair (http://\S+) ([0-9A-Z]{4}-[0-9A-Z]{4})"`)
		defer func() { pairCommand = old }()
		out, hostErr, callErr = pairBoth(t, north, laptop, []string{"--yes", "--timeout", "50ms", "--name", "jos-laptop"}, withCode("--yes", "--name", "the-north"))
	}()
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var opened struct {
		Pairing, Code, Address, Command string
		Expires                         time.Time
	}
	if err := dec.Decode(&opened); err != nil || opened.Code == "" || opened.Address != north.srv.URL || opened.Pairing == "" || time.Until(opened.Expires) > pairing.TTL {
		t.Fatalf("the first document = %+v, %v\n%s", opened, err, out)
	}
	results := map[string]paired{}
	for dec.More() {
		var res paired
		if err := dec.Decode(&res); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		results[res.Role] = res
	}
	host, joiner := results["host"], results["joiner"]
	if host.Peer == nil || host.Peer.Name != "jos-laptop" || host.Peer.Calls != "laptop" || joiner.Peer == nil || joiner.Peer.Name != "the-north" || joiner.Peer.Calls != "north" ||
		host.Check == "" || host.Check != joiner.Check || host.PingError == "" || joiner.PingError == "" {
		t.Fatalf("results = %+v\n%s", results, out)
	}
}

// Without a terminal there is no one to ask: both forms refuse, naming
// --yes, before anything is opened or called. And only the operator pairs.
func TestPeerPairWithoutATerminal(t *testing.T) {
	north, laptop := pairYards(t)
	setInteractive(t, false)
	for _, y := range []struct {
		yard *cliYard
		args []string
	}{{north, []string{"pair"}}, {laptop, []string{"pair", north.srv.URL, "K7QM-2XPD"}}} {
		if _, err := y.yard.peer(t, y.args...); err == nil || !strings.Contains(err.Error(), "--yes confirms the check without asking") {
			t.Errorf("peer %v without a terminal = %v", y.args, err)
		}
	}
	if _, err := north.st.GetPairing(context.Background(), ""); err == nil {
		t.Fatal("a pairing was opened")
	}
	// With a terminal and --json there is nothing to print a question in.
	setInteractive(t, true)
	north.a.json = true
	if _, err := north.peer(t, "pair"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("peer pair --json without --yes = %v", err)
	}
	north.a.json = false

	for _, y := range []*cliYard{north, laptop} {
		y.a.actor = "mayor"
		t.Setenv("YARDR_RESIDENT", "mayor")
		args := []string{"pair", "--yes"}
		if y == laptop {
			args = []string{"pair", north.srv.URL, "K7QM-2XPD", "--yes"}
		}
		if _, err := y.peer(t, args...); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may pair this yard with another; this is mayor") {
			t.Errorf("a resident pairing = %v", err)
		}
		y.a.actor = "jo"
	}
	if _, err := north.peer(t, "pair", north.srv.URL); err == nil || !strings.Contains(err.Error(), "the address and the code") {
		t.Errorf("peer pair <address> without a code = %v", err)
	}
}

// Either operator saying no to the check ends the pairing with nothing
// saved on either yard, and both commands say so.
func TestPeerPairDeclined(t *testing.T) {
	for name, no := range map[string]string{"by the host": "laptop", "by the caller": "north"} {
		t.Run(name, func(t *testing.T) {
			north, laptop := pairYards(t)
			setInteractive(t, true)
			bothStderr(t)
			var asked []string
			var mu sync.Mutex
			old := askCheck
			askCheck = func(peer, calls, check string) (bool, error) {
				mu.Lock()
				defer mu.Unlock()
				asked = append(asked, peer+" "+check)
				return peer != no, nil // the operator who is asked about no declines
			}
			t.Cleanup(func() { askCheck = old })

			out, hostErr, callErr := pairBoth(t, north, laptop, nil, withCode())
			if hostErr == nil || callErr == nil || !strings.HasSuffix(hostErr.Error(), nothingSaved) || !strings.HasSuffix(callErr.Error(), nothingSaved) {
				t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
			}
			mine, theirs := hostErr.Error(), callErr.Error()
			if no == "north" {
				mine, theirs = theirs, mine
			}
			if !strings.HasPrefix(mine, "declined") || !strings.Contains(theirs, "operator declined") {
				t.Errorf("the one who declined was told %q, the other %q", mine, theirs)
			}
			if len(peersOf(t, north))+len(peersOf(t, laptop)) != 0 {
				t.Fatalf("a peer was saved: %+v %+v", peersOf(t, north), peersOf(t, laptop))
			}
			if len(asked) == 0 || len(asked) > 2 {
				t.Fatalf("asked %v", asked)
			}
			for _, y := range []*cliYard{north, laptop} {
				if evs := eventsOf(t, y, "peer_pair_refused"); len(evs) != 1 {
					t.Errorf("%s: refusals %+v", y.home, evs)
				}
			}
		})
	}
}

// A name that is a peer already is refused in peer add's words before the
// operator is asked anything, on the yard that calls as on the one that is
// called, and nothing is saved.
func TestPeerPairAPeerAlready(t *testing.T) {
	north, laptop := pairYards(t)
	setInteractive(t, true)
	bothStderr(t)
	asked := 0
	old := askCheck
	askCheck = func(string, string, string) (bool, error) { asked++; return true, nil }
	t.Cleanup(func() { askCheck = old })
	ctx := context.Background()
	other := newCLIYard(t, "other")
	t.Setenv("YARDR_HOME", other.home)
	inv, err := invite(ctx, other.a.cfg, other.st, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.st.AddPeer(ctx, store.Peer{Name: "north", Key: inv.Key}, "jo"); err != nil {
		t.Fatal(err)
	}

	// The call ends before laptop has said who it is: north's operator is
	// asked nothing, its pairing stays open, and the code is still good.
	var again string
	var againErr error
	out, hostErr, callErr := pairBoth(t, north, laptop, []string{"--timeout", "50ms"}, withCode(), func() {
		if p, err := north.st.GetPairing(ctx, ""); err != nil || p.State != store.PairOpened || p.Tries != 0 {
			t.Errorf("north's pairing after the refused call = %+v, %v", p, err)
		}
		m := pairCommand.FindStringSubmatch(north.lastCommand)
		c := laptop.a.peerCmd()
		c.SetArgs([]string{"pair", m[1], m[2], "--name", "the-north"})
		c.SilenceUsage, c.SilenceErrors = true, true
		againErr = c.Execute()
		again = "ran"
	})
	if callErr == nil || !strings.Contains(callErr.Error(), "peer north is added already; to replace it, remove it first") || !strings.Contains(callErr.Error(), "--name saves the other yard under a name of your choosing") {
		t.Fatalf("the caller = %v\n%s", callErr, out)
	}
	if hostErr != nil || againErr != nil || again == "" || asked != 2 {
		t.Fatalf("the call again with --name: host %v, caller %v; asked %d times\n%s", hostErr, againErr, asked, out)
	}
	if ps := peersOf(t, laptop); len(ps) != 2 || len(peersOf(t, north)) != 1 {
		t.Fatalf("peers: north %+v, laptop %+v", peersOf(t, north), ps)
	}
	if evs := eventsOf(t, laptop, "peer_pair_refused"); len(evs) != 1 || evs[0].Data["reason"] != pairing.AlreadyPeer {
		t.Fatalf("laptop's refusals = %+v", evs)
	}
	asked = 0

	// On the host: laptop is a peer's name there. The caller is told the
	// other yard cannot add it, the host why, in peer add's words.
	north, laptop = pairYards(t)
	if _, err := north.st.AddPeer(ctx, store.Peer{Name: "laptop", Key: inv.Key}, "jo"); err != nil {
		t.Fatal(err)
	}
	out, hostErr, callErr = pairBoth(t, north, laptop, nil, withCode())
	if hostErr == nil || !strings.Contains(hostErr.Error(), "peer laptop is added already; to replace it, remove it first") {
		t.Fatalf("the host = %v\n%s", hostErr, out)
	}
	if callErr == nil || !strings.Contains(callErr.Error(), "cannot add this one") || asked != 0 {
		t.Fatalf("the caller = %v; asked %d times\n%s", callErr, asked, out)
	}
	if len(peersOf(t, north)) != 1 || len(peersOf(t, laptop)) != 0 {
		t.Fatalf("peers: north %+v, laptop %+v", peersOf(t, north), peersOf(t, laptop))
	}
	// A name given with --name that is taken is refused at once.
	if _, err := north.peer(t, "pair", "--name", "laptop"); err == nil || !strings.Contains(err.Error(), "peer laptop is added already") {
		t.Fatalf("pair --name laptop = %v", err)
	}
}

// What the yard that calls is told when there is no pairing for it: a
// wrong code, no pairing open, no yard at the address (one of the two has
// to be reachable), and a code that is none.
func TestPeerPairCallFails(t *testing.T) {
	north, laptop := pairYards(t)
	setInteractive(t, true)
	bothStderr(t)

	if _, err := laptop.peer(t, "pair", north.srv.URL, "K7QM-2XPD"); err == nil || !strings.Contains(err.Error(), "has no pairing open") {
		t.Errorf("a call with no pairing open = %v", err)
	}
	if _, err := laptop.peer(t, "pair", north.srv.URL, "K7QM"); err == nil || !strings.Contains(err.Error(), "that is no pairing code") {
		t.Errorf("a code that is none = %v", err)
	}
	if _, err := laptop.peer(t, "pair", "ftp://north", "K7QM-2XPD"); err == nil || !strings.Contains(err.Error(), "the address yardr peer pair prints") {
		t.Errorf("an address that is none = %v", err)
	}
	gone := newCLIYard(t, "gone")
	gone.srv.Close()
	_, err := laptop.peer(t, "pair", strings.TrimPrefix(gone.srv.URL, "http://"), "K7QM-2XPD")
	if err == nil {
		t.Fatal("a call to no yard succeeded")
	}
	wantText(t, err.Error(), "no yard was reached at "+gone.srv.URL, "One of the two yards has to be reachable by the other",
		"a listener on an address it can call, Tailscale, a tunnel", "docs/peers.md")

	// A wrong code: the caller is told, and so is the host, which counts.
	out, hostErr, callErr := pairBoth(t, north, laptop, []string{"--yes"}, func(addr, code string) []string {
		wrong := "0000-0000"
		if code == wrong {
			wrong = "1111-1111"
		}
		return []string{addr, wrong}
	}, func() {
		// The host's command goes on waiting, as it should: once it has said
		// what it saw, its pairing is ended as yardr peer pair --end ends it.
		time.Sleep(100 * time.Millisecond)
		if err := north.st.PairClose(context.Background(), "", "jo"); err != nil {
			t.Error(err)
		}
	})
	if callErr == nil || !strings.Contains(callErr.Error(), "that is not the code the yard at "+north.srv.URL+" shows") {
		t.Fatalf("a wrong code = %v\n%s", callErr, out)
	}
	if hostErr == nil || !strings.Contains(hostErr.Error(), "the pairing was ended"+nothingSaved) || !strings.Contains(out, "a wrong code was tried (1 of 5)") {
		t.Fatalf("the host = %v\n%s", hostErr, out)
	}
	if evs := eventsOf(t, laptop, "peer_pair_refused"); len(evs) != 1 || evs[0].Data["reason"] != pairing.WrongCode {
		t.Fatalf("laptop's refusals = %+v", evs)
	}
}

// A yard without a listener cannot be the one that is called, and says so
// with the way out; one that listens on every address needs --url.
func TestPeerPairNeedsAListener(t *testing.T) {
	_, laptop := pairYards(t)
	setInteractive(t, false)
	_, err := laptop.peer(t, "pair", "--yes")
	if err == nil {
		t.Fatal("a pairing was opened on a yard without a listener")
	}
	wantText(t, err.Error(), "this yard has no listener, so the other yard cannot call it", "One of the two yards has to be reachable by the other",
		"peer pair <address> <code>", "docs/peers.md")
	said(t, laptop.a.cfg, hooks.Listener{Addr: ":62199", From: hooks.FromSetting})
	if _, err := laptop.peer(t, "pair", "--yes"); err == nil || !strings.Contains(err.Error(), "--url http://<this machine>:62199") {
		t.Fatalf("a pairing on a listener on every address = %v", err)
	}
	if _, err := laptop.st.GetPairing(context.Background(), ""); err == nil {
		t.Fatal("a pairing was opened")
	}
}
