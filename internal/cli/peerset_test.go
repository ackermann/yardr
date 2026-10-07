package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// yardr peer set --url gives a peer added without a URL one, and the next
// ping goes to it by push; --url "" takes it away, and the next goes by
// pull. Only the operator may, and a URL that yardr peer add would not
// take is refused with its reason.
func TestPeerSetURL(t *testing.T) {
	north, laptop := newCLIYard(t, "north"), newCLIYard(t, "laptop")
	ctx := context.Background()
	t.Setenv("YARDR_HOME", north.home)
	northInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	t.Setenv("YARDR_HOME", laptop.home)
	laptopInvite := strings.TrimSpace(laptop.must(t, "invite"))
	north.must(t, "add", "laptop", laptopInvite)
	laptop.must(t, "add", "north", northInvite)
	north.must(t, "allow", "laptop", "--send", "ping", "--receive", "ping")
	laptop.must(t, "allow", "north", "--send", "ping", "--receive", "ping")
	added, err := north.st.GetPeer(ctx, "laptop")
	if err != nil || added.URL != "" {
		t.Fatalf("laptop as added = %+v, %v", added, err)
	}

	north.a.actor = "yardmaster"
	for _, url := range []string{laptop.srv.URL, ""} {
		if _, err := north.peer(t, "set", "laptop", "--url", url); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may change where a peer is called; this is yardmaster") {
			t.Fatalf("a crew member setting a peer's URL to %q = %v", url, err)
		}
	}
	north.a.actor = "jo"
	// What yardr peer add refuses in an invite's URL, in the same words.
	_, inInvite := north.peer(t, "add", "other", laptopInvite+",ftp://laptop.test")
	const reason = `url "ftp://laptop.test": want http://host:port or https://host, where the yard's server takes webhooks`
	if inInvite == nil || !strings.Contains(inInvite.Error(), reason) {
		t.Fatalf("peer add with a URL that is none = %v", inInvite)
	}
	for args, want := range map[string]string{
		"set laptop":                                   "say what: --url <url>",
		"set laptop --url ftp://laptop.test":           "peer laptop: " + reason,
		"set laptop --url http://jo:pw@laptop.test":    "want http://host:port or https://host",
		"set laptop --url http://laptop.test/?token=1": "want http://host:port or https://host",
		"set laptop --url=":                            "peer laptop has no URL already",
		"set nobody --url " + laptop.srv.URL:           "not found",
	} {
		if _, err := north.peer(t, strings.Fields(args)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("peer %s = %v, want %q", args, err, want)
		}
	}
	if p, _ := north.st.GetPeer(ctx, "laptop"); p.URL != "" {
		t.Fatalf("a refused URL was saved: %+v", p)
	}

	// The daemons of both.
	dctx, stop := context.WithCancel(ctx)
	done := make(chan struct{}, 2)
	for _, y := range []*cliYard{north, laptop} {
		d := &dispatch.Dispatcher{Store: y.st, Runners: map[string]dispatch.Runner{}, PeerKey: y.a.cfg.PeerKey()}
		go func() {
			defer func() { done <- struct{}{} }()
			for dctx.Err() == nil {
				_ = d.Reconcile(dctx)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	defer func() {
		stop()
		<-done
		<-done
	}()
	wantText(t, north.must(t, "ping", "laptop", "--timeout", "20s"), "the ping went by pull, the pong came by push")
	// Logged as sent once laptop's next question says it has the ping: one
	// it had not said so of would be posted to it as well, and dropped there.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if p, err := north.st.GetPeer(ctx, "laptop"); err != nil || time.Now().After(deadline) {
			t.Fatalf("the pulled ping was not logged as sent: %+v, %v", p, err)
		} else if p.Waiting == 0 {
			break
		}
	}

	out := north.must(t, "set", "laptop", "--url", laptop.srv.URL+"/")
	wantText(t, out, "peer laptop is called at "+laptop.srv.URL+" now")
	wantText(t, north.must(t, "show", "laptop"), "url      "+laptop.srv.URL+": this yard posts to it")
	wantText(t, north.must(t, "list"), laptop.srv.URL)
	wantText(t, north.must(t, "ping", "laptop", "--timeout", "20s"), "pong from laptop in ", "the ping went by push, the pong came by push")
	sent, err := north.st.Events(ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	ways := []any{}
	for _, e := range sent {
		if e.Kind == "peer_message_sent" {
			ways = append(ways, e.Data["way"])
		}
	}
	if len(ways) != 2 || ways[0] != store.PeerPull || ways[1] != store.PeerPush {
		t.Fatalf("north's pings were sent by %v, want pull before the URL and push with it", ways)
	}

	out = north.must(t, "set", "laptop", "--url", "")
	wantText(t, out, "peer laptop is never called now: it calls this yard and asks for what waits here", "incoming ")
	wantText(t, north.must(t, "show", "laptop"), "url      none: this yard never calls it; it calls this one")
	wantText(t, north.must(t, "ping", "laptop", "--timeout", "20s"), "the ping went by pull, the pong came by push")

	// The link is the one that was added: its key, what it carries.
	north.a.json = true
	var p store.Peer
	if err := json.Unmarshal([]byte(north.must(t, "set", "laptop", "--url", "https://laptop.example")), &p); err != nil ||
		p.URL != "https://laptop.example" || p.Key != added.Key || !p.AddedAt.Equal(added.AddedAt) || strings.Join(p.Send, ",") != "ping" || strings.Join(p.Receive, ",") != "ping" {
		t.Fatalf("peer set --json = %+v, %v", p, err)
	}
}

// What waits for a peer when its URL changes still waits, and the command
// says how many and how they go now.
func TestPeerSetURLSaysWhatWaits(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", south.home)
	north.must(t, "add", "south", strings.TrimSpace(south.must(t, "invite")))
	north.must(t, "allow", "south", "--send", "ping")
	if _, err := north.st.PeerSend(context.Background(), "south", store.PeerPing, nil, "jo"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_HOME", north.home)
	out := north.must(t, "set", "south", "--url", south.srv.URL)
	wantText(t, out, "1 messages that waited for it still wait, and are posted there, each at its next try",
		"no daemon runs on this yard, and the daemon is what calls it")
	out = north.must(t, "set", "south", "--url", "")
	wantText(t, out, "1 messages that waited for it still wait, and are handed over when it asks",
		"incoming none. No listener, no incoming: this peer is never called and cannot call")
	wantText(t, north.must(t, "--help"), "yardr peer set <name> --url <url>")
}
