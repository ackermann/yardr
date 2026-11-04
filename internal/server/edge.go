package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
)

// KeepEdge keeps a link of role to the configured edge and serves h over it.
// It follows the edge setting without a restart and loads the yard's key
// from PeerKey. An empty role keeps the default yard link and its edge.state;
// the web role reports events only, since that file describes the server, and
// keeps its link only while no other web view of the home does (lockWebLink).
// up, when not nil, is called each time a link of the role comes up, from the
// link's own goroutine, so it must not block: the preview supervisor reports
// its previews to the edge again then. The function returned closes the link
// and waits for it.
func KeepEdge(ctx context.Context, c *config.Config, st store.API, role string, h http.Handler, log *slog.Logger, up func()) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var (
			current  string
			stopLink = func() {}
		)
		var links linkHolder
		var sender *pushSender
		// A web view that was passed over (lockWebLink) stays so while it
		// runs. Were it to try again at the next change of the setting, a
		// second view that lives a minute could take the link from the one
		// that serves for good, and leave the yard with none when it ends.
		var passedOver bool
		if role == "" {
			// The yard role's link carries the footplate sender: intents
			// and removals ride it up to the relay. The sender runs
			// whether or not a link is up, so a yard with no edge still
			// sweeps its rows. Only the server keeps this role with its
			// own store.
			if ps, ok := st.(*store.Store); ok {
				sender = &pushSender{st: ps, log: log, link: links.get}
				defer startPushSender(ctx, sender)()
			}
		}
		apply := func() {
			all, err := st.Settings(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("edge: the edge setting is not read", "err", err)
				}
				return
			}
			v := all[store.EdgeSetting]
			if v == current {
				return
			}
			stopLink()
			links.set(nil)
			stopLink, current = func() {}, v
			if role == "" {
				os.Remove(c.EdgeState())
			}
			if v == "" {
				return
			}
			e, err := store.ParseEdge(v)
			if err != nil {
				log.Warn("edge: the edge setting is not one this build reads; no link", "err", err)
				return
			}
			key, err := store.Load(c.PeerKey())
			if err != nil {
				log.Warn("edge: the yard's key is not read; no link", "err", err)
				return
			}
			release := func() {}
			if role == "web" {
				if passedOver {
					log.Info("edge: this view was passed over for the edge link, and serves without it until it is restarted")
					return
				}
				unlock, err := lockWebLink(c)
				switch {
				case errors.Is(err, errWebLinkHeld):
					passedOver = true
					log.Info("edge: another web view of this yard holds the edge link; this view serves without it", "lock", c.EdgeWebLock())
					return
				case err != nil:
					log.Warn("edge: the web link's lock is not taken; linking without it", "err", err)
				default:
					release = unlock
				}
			}
			l := &edgelink.Link{Edge: e, Key: key, Role: role, Handler: h, Log: log}
			if sender != nil {
				l.OnPushAck = sender.ack
				links.set(l)
			}
			stop := startLink(ctx, c, st, l, up)
			// The lock is let go once the link is closed, so that no moment
			// has two views of the home linked.
			stopLink = func() { stop(); release() }
		}
		// The stream ends whenever the server does, and a server that is
		// replacing itself does not answer for a moment: subscribe again
		// from the last event seen, asking again every second until it
		// answers, as the dispatcher's wake stream and the quiet run's
		// trigger do. A web view lives across every server replace, so
		// without this its link keeps whatever edge it had until the
		// view is restarted.
		var after uint64
		have, told := false, false
		// pause says why the server could not be asked, once for each
		// time it could not, and waits to ask again.
		pause := func(what string, err error) {
			if !told {
				told = true
				log.Warn("edge: cannot "+what+"; asking again", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		for ctx.Err() == nil {
			if !have {
				head, err := st.Head(ctx)
				if err != nil {
					pause("read the event log", err)
					continue
				}
				after, have = head, true
			}
			events, err := st.Subscribe(ctx, after)
			if err != nil {
				pause("watch the yard's events", err)
				continue
			}
			told = false
			// Subscribed first, so nothing committed while applying is
			// missed: it is on the stream behind this pass.
			apply()
			for e := range events {
				after = e.Seq
				if e.Kind == "setting_changed" && e.Data["key"] == store.EdgeSetting {
					apply()
				}
			}
		}
		stopLink()
		links.set(nil)
		if role == "" {
			os.Remove(c.EdgeState())
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// errWebLinkHeld is what lockWebLink answers when another web view of the
// home keeps the yard's web link.
var errWebLinkHeld = errors.New("another web view keeps the yard's web link")

// lockWebLink takes the flock by which one web view of the home keeps the
// yard's web link; unlock lets it go. The edge keeps one link for a yard and
// role and closes the older with 1001 when a newer one answers its challenge
// (that is how a laptop that woke on a new network gets its link back), so two
// web views that dial the same edge close each other's link and dial again,
// for as long as both run. Only the web role can meet itself: the yard's link
// and the preview link are the server's, and a home has one server.
//
// A flock and not the views' records (internal/cli/webviews.go): views that
// start together each find no record of the other, and a killed view's record
// is stale, but exactly one of any number of views takes a flock, and it dies
// with its holder. The call does not wait, and a view that loses holds
// nothing, so one restarted after the holder has gone always takes the link;
// the loser serves without it until it is restarted.
//
// The file stays when the lock is let go: a lock file that is removed lets
// one process lock the file it opened while another makes it anew and locks
// that.
func lockWebLink(c *config.Config) (unlock func(), err error) {
	path := c.EdgeWebLock()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errWebLinkHeld
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// linkHolder is the yard role's current link, for the footplate sender:
// set when the link starts, cleared when it stops.
type linkHolder struct {
	mu sync.Mutex
	l  *edgelink.Link
}

func (h *linkHolder) set(l *edgelink.Link) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.l = l
}

func (h *linkHolder) get() *edgelink.Link {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.l
}

// linkTest, when set, changes a link before it runs: a test's client that
// trusts its stand-in edge, and the timing shortened.
var linkTest func(*edgelink.Link)

// startLink runs l until the function it returns is called, and tells each
// change of it, and tells up (when not nil) each time it comes up.
func startLink(ctx context.Context, c *config.Config, st store.API, l *edgelink.Link, up func()) (stop func()) {
	if linkTest != nil {
		linkTest(l)
	}
	l.Told = func(s edgelink.State) {
		role := l.Role
		if role == "" {
			role = "yard"
			if err := edgelink.WriteState(c, s); err != nil {
				l.Log.Warn("edge: the link's state is not written", "err", err)
			}
		}
		kind, data := "edge_link_up", map[string]any{"url": s.URL, "handle": s.Handle, "role": role}
		if !s.Up {
			kind, data["reason"], data["detail"] = "edge_link_down", s.Reason, s.Detail
		}
		// The link's own context may be the one ending: the event is the
		// yard's all the same, and the store is open until KeepEdge returns.
		if err := st.LogDispatch(context.Background(), "", kind, "yard", data); err != nil {
			l.Log.Warn(kind+" event", "err", err)
		}
		if s.Up && up != nil {
			up()
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// withoutPeers is h less /peers/: a linked yard's requests are not carried
// through the edge, whose operator would then read the peers' mail.
func withoutPeers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/peers" || strings.HasPrefix(r.URL.Path, "/peers/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
