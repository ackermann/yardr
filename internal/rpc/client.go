package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Client is store.API over the owner's unix socket.
type Client struct {
	http *http.Client
}

var _ store.API = (*Client)(nil)

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
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

func (c *Client) call(ctx context.Context, method string, out any, args ...any) error {
	if args == nil {
		args = []any{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://yard/call/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("yardr server: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *wireError      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("yardr server reply: %w", err)
	}
	if r.Error != nil {
		switch r.Error.Code {
		case "not-found":
			return wrapped{r.Error.Message, store.ErrNotFound}
		case "taken":
			return store.ErrTaken
		case "ambiguous":
			return wrapped{r.Error.Message, store.ErrAmbiguous}
		}
		return fmt.Errorf("%s", r.Error.Message)
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

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

func (c *Client) CreateBead(ctx context.Context, nb store.NewBead, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "CreateBead", nb, actor)
}
func (c *Client) GetBead(ctx context.Context, id string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "GetBead", id)
}
func (c *Client) ListBeads(ctx context.Context, f store.BeadFilter) ([]store.Bead, error) {
	return callV[[]store.Bead](c, ctx, "ListBeads", f)
}
func (c *Client) Ready(ctx context.Context) ([]store.Bead, error) {
	return callV[[]store.Bead](c, ctx, "Ready")
}
func (c *Client) Dispatchable(ctx context.Context) ([]store.Bead, error) {
	return callV[[]store.Bead](c, ctx, "Dispatchable")
}
func (c *Client) UpdateBead(ctx context.Context, id string, p store.BeadPatch, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "UpdateBead", id, p, actor)
}
func (c *Client) Advance(ctx context.Context, id, stage, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Advance", id, stage, actor)
}
func (c *Client) AdvanceNote(ctx context.Context, id, stage, note, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "AdvanceNote", id, stage, note, actor)
}
func (c *Client) CloseBead(ctx context.Context, id, reason, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "CloseBead", id, reason, actor)
}
func (c *Client) Reopen(ctx context.Context, id, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Reopen", id, actor)
}
func (c *Client) SetHold(ctx context.Context, id string, hold bool, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "SetHold", id, hold, actor)
}
func (c *Client) Release(ctx context.Context, id, reason, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Release", id, reason, actor)
}

func (c *Client) AddDep(ctx context.Context, from, to, kind, actor string) error {
	return c.call(ctx, "AddDep", nil, from, to, kind, actor)
}
func (c *Client) RemoveDep(ctx context.Context, from, to, kind, actor string) error {
	return c.call(ctx, "RemoveDep", nil, from, to, kind, actor)
}
func (c *Client) Deps(ctx context.Context, id string) ([]store.Dep, error) {
	return callV[[]store.Dep](c, ctx, "Deps", id)
}
func (c *Client) OpenBlockers(ctx context.Context, id string) ([]string, error) {
	return callV[[]string](c, ctx, "OpenBlockers", id)
}
func (c *Client) AddNote(ctx context.Context, id, text, actor string) error {
	return c.call(ctx, "AddNote", nil, id, text, actor)
}
func (c *Client) Notes(ctx context.Context, id string) ([]store.Note, error) {
	return callV[[]store.Note](c, ctx, "Notes", id)
}

func (c *Client) PutRig(ctx context.Context, r store.Rig) error {
	return c.call(ctx, "PutRig", nil, r)
}
func (c *Client) Rigs(ctx context.Context) ([]store.Rig, error) {
	return callV[[]store.Rig](c, ctx, "Rigs")
}
func (c *Client) GetRig(ctx context.Context, name string) (*store.Rig, error) {
	return callV[*store.Rig](c, ctx, "GetRig", name)
}

func (c *Client) PutGroup(ctx context.Context, g store.Group) error {
	return c.call(ctx, "PutGroup", nil, g)
}
func (c *Client) Groups(ctx context.Context) ([]store.Group, error) {
	return callV[[]store.Group](c, ctx, "Groups")
}
func (c *Client) GetGroup(ctx context.Context, name string) (*store.Group, error) {
	return callV[*store.Group](c, ctx, "GetGroup", name)
}
func (c *Client) DeleteGroup(ctx context.Context, name, actor string) (int, error) {
	return callV[int](c, ctx, "DeleteGroup", name, actor)
}
func (c *Client) AddRoute(ctx context.Context, r store.Route) (*store.Route, error) {
	return callV[*store.Route](c, ctx, "AddRoute", r)
}
func (c *Client) Routes(ctx context.Context) ([]store.Route, error) {
	return callV[[]store.Route](c, ctx, "Routes")
}
func (c *Client) DeleteRoute(ctx context.Context, id string) error {
	return c.call(ctx, "DeleteRoute", nil, id)
}
func (c *Client) PutFlow(ctx context.Context, f store.Flow, actor string) (*store.Flow, error) {
	return callV[*store.Flow](c, ctx, "PutFlow", f, actor)
}
func (c *Client) Flows(ctx context.Context) ([]store.Flow, error) {
	return callV[[]store.Flow](c, ctx, "Flows")
}
func (c *Client) GetFlow(ctx context.Context, rig, typ string) (*store.Flow, error) {
	return callV[*store.Flow](c, ctx, "GetFlow", rig, typ)
}
func (c *Client) DeleteFlow(ctx context.Context, rig, typ, actor string) error {
	return c.call(ctx, "DeleteFlow", nil, rig, typ, actor)
}
func (c *Client) NextStages(ctx context.Context, id string) (*store.NextStages, error) {
	return callV[*store.NextStages](c, ctx, "NextStages", id)
}

func (c *Client) Claim(ctx context.Context, beadID, group, stage, actor string) (*store.Session, error) {
	return callV[*store.Session](c, ctx, "Claim", beadID, group, stage, actor)
}
func (c *Client) MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error {
	return c.call(ctx, "MarkRunning", nil, sessionID, handle)
}
func (c *Client) EndSession(ctx context.Context, sessionID, state, reason string) error {
	return c.call(ctx, "EndSession", nil, sessionID, state, reason)
}
func (c *Client) MarkStopped(ctx context.Context, sessionID string) error {
	return c.call(ctx, "MarkStopped", nil, sessionID)
}
func (c *Client) SetHandle(ctx context.Context, sessionID string, handle map[string]any) error {
	return c.call(ctx, "SetHandle", nil, sessionID, handle)
}
func (c *Client) MarkCleaned(ctx context.Context, sessionID string) error {
	return c.call(ctx, "MarkCleaned", nil, sessionID)
}
func (c *Client) GetSession(ctx context.Context, id string) (*store.Session, error) {
	return callV[*store.Session](c, ctx, "GetSession", id)
}
func (c *Client) Sessions(ctx context.Context, all bool) ([]store.Session, error) {
	return callV[[]store.Session](c, ctx, "Sessions", all)
}
func (c *Client) Unstopped(ctx context.Context) ([]store.Session, error) {
	return callV[[]store.Session](c, ctx, "Unstopped")
}
func (c *Client) ClosedWithWorkspace(ctx context.Context) ([]store.Session, error) {
	return callV[[]store.Session](c, ctx, "ClosedWithWorkspace")
}
func (c *Client) FailureCount(ctx context.Context, bead, stage string) (int, error) {
	return callV[int](c, ctx, "FailureCount", bead, stage)
}

func (c *Client) Events(ctx context.Context, bead string, limit int) ([]store.Event, error) {
	return callV[[]store.Event](c, ctx, "Events", bead, limit)
}
func (c *Client) EventsSince(ctx context.Context, after uint64, limit int) ([]store.Event, error) {
	return callV[[]store.Event](c, ctx, "EventsSince", after, limit)
}
func (c *Client) Head(ctx context.Context) (uint64, error) {
	return callV[uint64](c, ctx, "Head")
}
func (c *Client) LogEvent(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	return c.call(ctx, "LogEvent", nil, bead, kind, actor, data)
}
func (c *Client) Snapshot(ctx context.Context) (*store.Snapshot, error) {
	return callV[*store.Snapshot](c, ctx, "Snapshot")
}

func (c *Client) PutResident(ctx context.Context, def store.Resident, actor string) (*store.Resident, error) {
	return callV[*store.Resident](c, ctx, "PutResident", def, actor)
}
func (c *Client) Residents(ctx context.Context) ([]store.Resident, error) {
	return callV[[]store.Resident](c, ctx, "Residents")
}
func (c *Client) GetResident(ctx context.Context, name string) (*store.Resident, error) {
	return callV[*store.Resident](c, ctx, "GetResident", name)
}
func (c *Client) DeleteResident(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteResident", nil, name, actor)
}
func (c *Client) SetResidentEnabled(ctx context.Context, name string, enabled bool, actor string) error {
	return c.call(ctx, "SetResidentEnabled", nil, name, enabled, actor)
}
func (c *Client) ResidentStarted(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "ResidentStarted", nil, name, handle)
}
func (c *Client) SetResidentHandle(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "SetResidentHandle", nil, name, handle)
}
func (c *Client) ResidentStopped(ctx context.Context, name, reason string) error {
	return c.call(ctx, "ResidentStopped", nil, name, reason)
}
func (c *Client) ResidentStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error {
	return c.call(ctx, "ResidentStartFailed", nil, name, reason, backoff)
}
func (c *Client) RequestRestart(ctx context.Context, name, reason, actor string) error {
	return c.call(ctx, "RequestRestart", nil, name, reason, actor)
}
func (c *Client) MarkRotationAsked(ctx context.Context, name string) error {
	return c.call(ctx, "MarkRotationAsked", nil, name)
}
func (c *Client) MarkNotified(ctx context.Context, name string, seq uint64) error {
	return c.call(ctx, "MarkNotified", nil, name, seq)
}
func (c *Client) Journal(ctx context.Context, name, kind, text, actor string) error {
	return c.call(ctx, "Journal", nil, name, kind, text, actor)
}
func (c *Client) Mail(ctx context.Context, to, text, actor string) error {
	return c.call(ctx, "Mail", nil, to, text, actor)
}
func (c *Client) Inbox(ctx context.Context, name string, limit int) (*store.InboxPage, error) {
	return callV[*store.InboxPage](c, ctx, "Inbox", name, limit)
}
func (c *Client) AckInbox(ctx context.Context, name string, through uint64) error {
	return c.call(ctx, "AckInbox", nil, name, through)
}
func (c *Client) ResidentPrime(ctx context.Context, name string) (*store.Prime, error) {
	return callV[*store.Prime](c, ctx, "ResidentPrime", name)
}
func (c *Client) Remember(ctx context.Context, m store.NewMemory, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Remember", m, actor)
}
func (c *Client) Recall(ctx context.Context, scope, query string) ([]store.MemoryInfo, error) {
	return callV[[]store.MemoryInfo](c, ctx, "Recall", scope, query)
}
func (c *Client) ExpireMemories(ctx context.Context) (int, error) {
	return callV[int](c, ctx, "ExpireMemories")
}
func (c *Client) SetIdentity(ctx context.Context, name, source string, id store.Identity, actor string) error {
	return c.call(ctx, "SetIdentity", nil, name, source, id, actor)
}
func (c *Client) AddSubscription(ctx context.Context, name string, sub store.Subscription, actor string) error {
	return c.call(ctx, "AddSubscription", nil, name, sub, actor)
}
func (c *Client) RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error) {
	return callV[int](c, ctx, "RemoveSubscriptions", name, source, scope, where, actor)
}
func (c *Client) FollowThread(ctx context.Context, name, source, thread, actor string) error {
	return c.call(ctx, "FollowThread", nil, name, source, thread, actor)
}
func (c *Client) UnfollowThread(ctx context.Context, name, source, thread, actor string) error {
	return c.call(ctx, "UnfollowThread", nil, name, source, thread, actor)
}
func (c *Client) Threads(ctx context.Context, name string) ([]store.Participation, error) {
	return callV[[]store.Participation](c, ctx, "Threads", name)
}

func (c *Client) PutHook(ctx context.Context, h store.Hook, actor string) (*store.Hook, error) {
	return callV[*store.Hook](c, ctx, "PutHook", h, actor)
}
func (c *Client) Hooks(ctx context.Context) ([]store.Hook, error) {
	return callV[[]store.Hook](c, ctx, "Hooks")
}
func (c *Client) GetHook(ctx context.Context, name string) (*store.Hook, error) {
	return callV[*store.Hook](c, ctx, "GetHook", name)
}
func (c *Client) DeleteHook(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteHook", nil, name, actor)
}
func (c *Client) RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte) (uint64, error) {
	return callV[uint64](c, ctx, "RecordHook", name, envelope, payload)
}
func (c *Client) HookPayload(ctx context.Context, seq uint64) ([]byte, error) {
	return callV[[]byte](c, ctx, "HookPayload", seq)
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
