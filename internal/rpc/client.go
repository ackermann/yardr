package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

//go:generate go run ./gen

// ReplaceRetry is how long a call keeps retrying while no server answers,
// the default Retry. A server replacing itself refuses the call, stops
// listening, waits up to 15s for calls in flight (server.Run's grace and
// settle), execs the new build and opens the store again, migrating it if
// need be; a call issued meanwhile must outwait all of that, or every
// replace fails the agents' commands, and a client that gives up sooner
// than the server drains would start a second server that cannot take the
// store.
const ReplaceRetry = 30 * time.Second

// Client is store.API over the owner's unix socket.
type Client struct {
	http *http.Client
	// Revision is the build this client runs (version.Current), sent with
	// every call.
	Revision string
	// Retry is how long a call keeps retrying while no server listens, as
	// while one execs a new build of itself (default ReplaceRetry).
	Retry time.Duration
}

var _ store.API = (*Client)(nil)

func NewClient(socket string) *Client {
	return &Client{Revision: version.Current(), Retry: ReplaceRetry, http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		// A fresh connection per call: a server draining for a replace
		// closes idle ones, and a POST on a closed one is not retried.
		DisableKeepAlives: true,
	}}}
}

// Ping reports whether the owner answers.
func (c *Client) Ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://yard/ping", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Owner asks the owner which process it is and which build it runs, from
// the headers it stamps every reply with. pid is 0 and revision "" for what
// a reply does not say.
func (c *Client) Owner(ctx context.Context) (pid int, revision string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://yard/ping", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	resp.Body.Close()
	pid, _ = strconv.Atoi(resp.Header.Get(PIDHeader))
	return pid, resp.Header.Get(version.Header), nil
}

func (c *Client) call(ctx context.Context, method string, out any, args ...any) error {
	if args == nil {
		args = []any{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(c.Retry)
	for {
		err := c.callOnce(ctx, method, out, body)
		var retry retryable
		if !errors.As(err, &retry) {
			return err
		}
		if time.Now().After(deadline) {
			return retry.err
		}
		select {
		case <-ctx.Done():
			return retry.err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// retryable is a call the server never ran: nothing listened, or it refused
// the call because it is replacing itself.
type retryable struct{ err error }

func (e retryable) Error() string { return e.err.Error() }

func (c *Client) callOnce(ctx context.Context, method string, out any, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://yard/call/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if c.Revision != "" {
		req.Header.Set(version.Header, c.Revision)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		err = fmt.Errorf("yardr server: %w", err)
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return retryable{err}
		}
		return unanswered{err}
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		More   bool            `json:"more"`
		Error  *wireError      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return unanswered{fmt.Errorf("yardr server reply: %w", err)}
	}
	if r.Error != nil {
		switch r.Error.Code {
		case "not-found":
			return wrapped{r.Error.Message, store.ErrNotFound}
		case "taken":
			return store.ErrTaken
		case "at-limit":
			return wrapped{r.Error.Message, store.ErrAtLimit}
		case "conflict":
			return wrapped{r.Error.Message, store.ErrConflict}
		case "ambiguous":
			return wrapped{r.Error.Message, store.ErrAmbiguous}
		case "replacing":
			return retryable{fmt.Errorf("%s", r.Error.Message)}
		}
		if theirs := resp.Header.Get(version.Header); theirs != c.Revision && skewed(r.Error.Message) {
			pid := resp.Header.Get(PIDHeader)
			if pid == "" {
				pid = "unknown"
			}
			return fmt.Errorf("the yardr server (pid %s, built %s) differs from this yardr (%s); run `yardr server stop` (%s)",
				pid, version.Show(theirs), version.Show(c.Revision), r.Error.Message)
		}
		return fmt.Errorf("%s", r.Error.Message)
	}
	if p, ok := out.(*part); ok {
		out, p.more = p.list, r.More
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// part is what a call that answers a part of a list is decoded into: the
// list from the result, and from beside it whether there is more.
type part struct {
	list any
	more bool
}

// skewed reports whether a server error means the call did not match the
// server's API, which a different build explains.
func skewed(msg string) bool {
	return strings.HasPrefix(msg, "no such method ") || strings.HasPrefix(msg, "want ") ||
		strings.HasPrefix(msg, "argument")
}

// unanswered is a call that was sent and ended without the server's answer:
// the error as it is, and store.ErrNoAnswer. The server may have run it. A
// call nothing listened for is not one: that the server never ran
// (retryable).
type unanswered struct{ err error }

func (e unanswered) Error() string   { return e.err.Error() }
func (e unanswered) Unwrap() []error { return []error{e.err, store.ErrNoAnswer} }

// wrapped keeps the owner's message while matching errors.Is(err, base).
type wrapped struct {
	msg  string
	base error
}

func (e wrapped) Error() string { return e.msg }
func (e wrapped) Unwrap() error { return e.base }

func callV[T any](c *Client, ctx context.Context, method string, args ...any) (T, error) {
	var out T
	err := c.call(ctx, method, &out, args...)
	return out, err
}

func (c *Client) Conversation(ctx context.Context, a, b string, after uint64, limit int, before uint64) ([]store.Letter, bool, error) {
	var out []store.Letter
	p := part{list: &out}
	err := c.call(ctx, "Conversation", &p, a, b, after, limit, before)
	return out, p.more, err
}

// Subscribe streams events with seq > after. The channel closes if the
// connection drops; resubscribe from the last seq seen.
func (c *Client) Subscribe(ctx context.Context, after uint64) (<-chan store.Event, error) {
	return streamOf[store.Event](c, ctx, "/events?after="+strconv.FormatUint(after, 10))
}

func (c *Client) TailEvents(ctx context.Context) (<-chan store.Event, error) {
	head, err := c.Head(ctx)
	if err != nil {
		return nil, err
	}
	return c.Subscribe(ctx, head)
}

func (c *Client) Wake(ctx context.Context) (<-chan struct{}, error) {
	pings, err := streamOf[bool](c, ctx, "/wake")
	if err != nil {
		return nil, err
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for range pings {
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, nil
}

func streamOf[T any](c *Client, ctx context.Context, path string) (<-chan T, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://yard"+path, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yardr server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("yardr server: %s", resp.Status)
	}
	out := make(chan T)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			var v T
			if json.Unmarshal(sc.Bytes(), &v) != nil {
				return
			}
			select {
			case out <- v:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Close is a no-op: the client holds no resources beyond idle connections.
func (c *Client) Close(ctx context.Context) error {
	c.http.CloseIdleConnections()
	return nil
}
