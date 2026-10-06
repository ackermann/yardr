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
		return err
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
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// skewed reports whether a server error means the call did not match the
// server's API, which a different build explains.
func skewed(msg string) bool {
	return strings.HasPrefix(msg, "no such method ") || strings.HasPrefix(msg, "want ") ||
		strings.HasPrefix(msg, "argument")
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
func (c *Client) UpdateBead(ctx context.Context, id string, p store.BeadPatch, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "UpdateBead", id, p, exp, actor)
}
func (c *Client) Advance(ctx context.Context, id, stage string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Advance", id, stage, exp, actor)
}
func (c *Client) AdvanceNote(ctx context.Context, id, stage, note string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "AdvanceNote", id, stage, note, exp, actor)
}
func (c *Client) Done(ctx context.Context, id, outcome, note string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Done", id, outcome, note, exp, actor)
}
func (c *Client) CloseBead(ctx context.Context, id, reason string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "CloseBead", id, reason, exp, actor)
}
func (c *Client) Reopen(ctx context.Context, id string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Reopen", id, exp, actor)
}
func (c *Client) SetHold(ctx context.Context, id string, hold bool, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "SetHold", id, hold, exp, actor)
}
func (c *Client) Release(ctx context.Context, id, reason string, exp store.Expect, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Release", id, reason, exp, actor)
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
func (c *Client) BlockerCycles(ctx context.Context) ([][]string, error) {
	return callV[[][]string](c, ctx, "BlockerCycles")
}

func (c *Client) CheckBlockers(ctx context.Context, refs []string) error {
	return c.call(ctx, "CheckBlockers", nil, refs)
}

func (c *Client) Discovery(ctx context.Context, id string) (*store.Discovery, error) {
	return callV[*store.Discovery](c, ctx, "Discovery", id)
}

func (c *Client) Family(ctx context.Context, id string) (*store.Family, error) {
	return callV[*store.Family](c, ctx, "Family", id)
}
func (c *Client) AddLink(ctx context.Context, id string, nl store.NewLink, actor string) (*store.LinkChange, error) {
	return callV[*store.LinkChange](c, ctx, "AddLink", id, nl, actor)
}
func (c *Client) RemoveLink(ctx context.Context, id, issue, actor string) error {
	return c.call(ctx, "RemoveLink", nil, id, issue, actor)
}
func (c *Client) MoveLink(ctx context.Context, issue, to, actor string) (*store.LinkChange, error) {
	return callV[*store.LinkChange](c, ctx, "MoveLink", issue, to, actor)
}
func (c *Client) Links(ctx context.Context, id string) ([]store.Link, error) {
	return callV[[]store.Link](c, ctx, "Links", id)
}
func (c *Client) IssueLinks(ctx context.Context, issue string) ([]store.Link, error) {
	return callV[[]store.Link](c, ctx, "IssueLinks", issue)
}
func (c *Client) AddNote(ctx context.Context, id, text, actor string) error {
	return c.call(ctx, "AddNote", nil, id, text, actor)
}
func (c *Client) Notes(ctx context.Context, id string) ([]store.Note, error) {
	return callV[[]store.Note](c, ctx, "Notes", id)
}

func (c *Client) PutRig(ctx context.Context, r store.Rig, actor string) error {
	return c.call(ctx, "PutRig", nil, r, actor)
}
func (c *Client) Rigs(ctx context.Context) ([]store.Rig, error) {
	return callV[[]store.Rig](c, ctx, "Rigs")
}
func (c *Client) GetRig(ctx context.Context, name string) (*store.Rig, error) {
	return callV[*store.Rig](c, ctx, "GetRig", name)
}
func (c *Client) DeleteRig(ctx context.Context, name, actor string) (*store.RigRemoval, error) {
	return callV[*store.RigRemoval](c, ctx, "DeleteRig", name, actor)
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
func (c *Client) GetFlow(ctx context.Context, name string) (*store.Flow, error) {
	return callV[*store.Flow](c, ctx, "GetFlow", name)
}
func (c *Client) DeleteFlow(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteFlow", nil, name, actor)
}
func (c *Client) SetFlowPointer(ctx context.Context, rig, typ, flow, actor string) error {
	return c.call(ctx, "SetFlowPointer", nil, rig, typ, flow, actor)
}
func (c *Client) FlowPointers(ctx context.Context) ([]store.FlowPointer, error) {
	return callV[[]store.FlowPointer](c, ctx, "FlowPointers")
}
func (c *Client) RepoFlows(ctx context.Context, rig string) (*store.RepoFlows, error) {
	return callV[*store.RepoFlows](c, ctx, "RepoFlows", rig)
}
func (c *Client) RigFlows(ctx context.Context, rig string) ([]store.RigFlow, error) {
	return callV[[]store.RigFlow](c, ctx, "RigFlows", rig)
}
func (c *Client) NextStages(ctx context.Context, id string) (*store.NextStages, error) {
	return callV[*store.NextStages](c, ctx, "NextStages", id)
}
func (c *Client) FlowMap(ctx context.Context, id string) (*store.FlowMap, error) {
	return callV[*store.FlowMap](c, ctx, "FlowMap", id)
}
func (c *Client) Stranded(ctx context.Context, rig string) ([]store.StrandedBead, error) {
	return callV[[]store.StrandedBead](c, ctx, "Stranded", rig)
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
func (c *Client) LogDispatch(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	return c.call(ctx, "LogDispatch", nil, bead, kind, actor, data)
}
func (c *Client) Snapshot(ctx context.Context) (*store.Snapshot, error) {
	return callV[*store.Snapshot](c, ctx, "Snapshot")
}
func (c *Client) Prune(ctx context.Context, keep store.Retention, actor string) (*store.PruneResult, error) {
	return callV[*store.PruneResult](c, ctx, "Prune", keep, actor)
}

func (c *Client) Backup(ctx context.Context, path string) (*store.BackupResult, error) {
	return callV[*store.BackupResult](c, ctx, "Backup", path)
}

func (c *Client) Settings(ctx context.Context) (map[string]string, error) {
	return callV[map[string]string](c, ctx, "Settings")
}
func (c *Client) SetSetting(ctx context.Context, key, value, actor string) error {
	return c.call(ctx, "SetSetting", nil, key, value, actor)
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
func (c *Client) DeleteResident(ctx context.Context, name, rev, actor string) error {
	return c.call(ctx, "DeleteResident", nil, name, rev, actor)
}
func (c *Client) SetResidentEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error {
	return c.call(ctx, "SetResidentEnabled", nil, name, enabled, rev, actor)
}
func (c *Client) ResidentStarted(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "ResidentStarted", nil, name, handle)
}
func (c *Client) ResidentSettled(ctx context.Context, name string) error {
	return c.call(ctx, "ResidentSettled", nil, name)
}
func (c *Client) SetResidentHandle(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "SetResidentHandle", nil, name, handle)
}
func (c *Client) ResidentStopped(ctx context.Context, name, reason string) error {
	return c.call(ctx, "ResidentStopped", nil, name, reason)
}
func (c *Client) ResidentGone(ctx context.Context, name, reason string) error {
	return c.call(ctx, "ResidentGone", nil, name, reason)
}
func (c *Client) ResidentLeft(ctx context.Context, name string, left []map[string]any) error {
	return c.call(ctx, "ResidentLeft", nil, name, left)
}
func (c *Client) ResidentStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error {
	return c.call(ctx, "ResidentStartFailed", nil, name, reason, backoff)
}
func (c *Client) RequestRestart(ctx context.Context, name, reason, rev, actor string) error {
	return c.call(ctx, "RequestRestart", nil, name, reason, rev, actor)
}
func (c *Client) MarkRotationAsked(ctx context.Context, name string) error {
	return c.call(ctx, "MarkRotationAsked", nil, name)
}
func (c *Client) ResidentStatus(ctx context.Context, name, status string) error {
	return c.call(ctx, "ResidentStatus", nil, name, status)
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
func (c *Client) AddUser(ctx context.Context, name, actor string) (*store.User, error) {
	return callV[*store.User](c, ctx, "AddUser", name, actor)
}
func (c *Client) Users(ctx context.Context) ([]store.User, error) {
	return callV[[]store.User](c, ctx, "Users")
}
func (c *Client) GetUser(ctx context.Context, name string) (*store.User, error) {
	return callV[*store.User](c, ctx, "GetUser", name)
}
func (c *Client) DeleteUser(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteUser", nil, name, actor)
}
func (c *Client) UserInbox(ctx context.Context, name string, limit int) (*store.Mailbox, error) {
	return callV[*store.Mailbox](c, ctx, "UserInbox", name, limit)
}
func (c *Client) AckUserInbox(ctx context.Context, name string, through uint64) error {
	return c.call(ctx, "AckUserInbox", nil, name, through)
}
func (c *Client) UserNotifications(ctx context.Context, name string, limit int) (*store.NotificationPage, error) {
	return callV[*store.NotificationPage](c, ctx, "UserNotifications", name, limit)
}
func (c *Client) AckUserNotifications(ctx context.Context, name string, through uint64) error {
	return c.call(ctx, "AckUserNotifications", nil, name, through)
}
func (c *Client) Conversation(ctx context.Context, a, b string, after uint64) ([]store.Letter, error) {
	return callV[[]store.Letter](c, ctx, "Conversation", a, b, after)
}
func (c *Client) AddTimer(ctx context.Context, nt store.NewTimer, actor string) (*store.Timer, error) {
	return callV[*store.Timer](c, ctx, "AddTimer", nt, actor)
}
func (c *Client) Timers(ctx context.Context, name string) ([]store.Timer, error) {
	return callV[[]store.Timer](c, ctx, "Timers", name)
}
func (c *Client) RemoveTimer(ctx context.Context, id, actor string, anyone bool) (*store.Timer, error) {
	return callV[*store.Timer](c, ctx, "RemoveTimer", id, actor, anyone)
}
func (c *Client) FireTimers(ctx context.Context, at time.Time) (*store.TimerPass, error) {
	return callV[*store.TimerPass](c, ctx, "FireTimers", at)
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
func (c *Client) RemoveSubscription(ctx context.Context, name string, sub store.Subscription, actor string) error {
	return c.call(ctx, "RemoveSubscription", nil, name, sub, actor)
}
func (c *Client) RecordPack(ctx context.Context, rec store.PackImport, changed bool, actor string) (bool, error) {
	return callV[bool](c, ctx, "RecordPack", rec, changed, actor)
}
func (c *Client) Packs(ctx context.Context) ([]store.PackImport, error) {
	return callV[[]store.PackImport](c, ctx, "Packs")
}
func (c *Client) Unwatch(ctx context.Context, name, bead, actor string) (int, error) {
	return callV[int](c, ctx, "Unwatch", name, bead, actor)
}
func (c *Client) FollowThread(ctx context.Context, name, source, thread, actor string) error {
	return c.call(ctx, "FollowThread", nil, name, source, thread, actor)
}
func (c *Client) UnfollowThread(ctx context.Context, name, source, thread, actor string) error {
	return c.call(ctx, "UnfollowThread", nil, name, source, thread, actor)
}
func (c *Client) Replying(ctx context.Context, name string, seq uint64, to, body string) (*store.Reply, error) {
	return callV[*store.Reply](c, ctx, "Replying", name, seq, to, body)
}
func (c *Client) Replied(ctx context.Context, name string, rp store.Reply, actor string) error {
	return c.call(ctx, "Replied", nil, name, rp, actor)
}
func (c *Client) Opening(ctx context.Context, name, source, in, title, body string) (*store.Reply, error) {
	return callV[*store.Reply](c, ctx, "Opening", name, source, in, title, body)
}
func (c *Client) Opened(ctx context.Context, name string, rp store.Reply, actor string) error {
	return c.call(ctx, "Opened", nil, name, rp, actor)
}
func (c *Client) AllowOpening(ctx context.Context, name string, sources []string, pack, actor string) ([]string, error) {
	return callV[[]string](c, ctx, "AllowOpening", name, sources, pack, actor)
}
func (c *Client) SettingState(ctx context.Context, name, source, thread string) (*store.Participation, error) {
	return callV[*store.Participation](c, ctx, "SettingState", name, source, thread)
}
func (c *Client) StateSet(ctx context.Context, name string, ts store.ThreadState, actor string) error {
	return c.call(ctx, "StateSet", nil, name, ts, actor)
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
func (c *Client) HookPayload(ctx context.Context, seq uint64) ([]byte, error) {
	return callV[[]byte](c, ctx, "HookPayload", seq)
}
func (c *Client) IssueMoves(ctx context.Context) ([]store.IssueMove, error) {
	return callV[[]store.IssueMove](c, ctx, "IssueMoves")
}
func (c *Client) IssueMoveAllowed(ctx context.Context, id string) (string, error) {
	return callV[string](c, ctx, "IssueMoveAllowed", id)
}
func (c *Client) IssueMoveFailed(ctx context.Context, id, reason string, retryAt time.Time) error {
	return c.call(ctx, "IssueMoveFailed", nil, id, reason, retryAt)
}
func (c *Client) IssueMoved(ctx context.Context, id string, ts store.ThreadState) error {
	return c.call(ctx, "IssueMoved", nil, id, ts)
}
func (c *Client) IssueMoveSkipped(ctx context.Context, id string, skip store.IssueSkip) error {
	return c.call(ctx, "IssueMoveSkipped", nil, id, skip)
}

func (c *Client) AddPeer(ctx context.Context, p store.Peer, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "AddPeer", p, actor)
}
func (c *Client) Peers(ctx context.Context) ([]store.Peer, error) {
	return callV[[]store.Peer](c, ctx, "Peers")
}
func (c *Client) GetPeer(ctx context.Context, name string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "GetPeer", name)
}
func (c *Client) DeletePeer(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeletePeer", nil, name, actor)
}
func (c *Client) AllowPeer(ctx context.Context, name string, ch store.PeerAllow, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "AllowPeer", name, ch, actor)
}
func (c *Client) RenamePeer(ctx context.Context, name, to, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "RenamePeer", name, to, actor)
}
func (c *Client) SetPeerIntake(ctx context.Context, name string, in *store.PeerIntake, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "SetPeerIntake", name, in, actor)
}
func (c *Client) PeerSend(ctx context.Context, name, kind string, body json.RawMessage, actor string) (*store.PeerOut, error) {
	return callV[*store.PeerOut](c, ctx, "PeerSend", name, kind, body, actor)
}
func (c *Client) MailPeer(ctx context.Context, to, peer, text, actor string) (*store.PeerOut, error) {
	return callV[*store.PeerOut](c, ctx, "MailPeer", to, peer, text, actor)
}
func (c *Client) PeerOutbox(ctx context.Context, name string, limit int) ([]store.PeerOut, error) {
	return callV[[]store.PeerOut](c, ctx, "PeerOutbox", name, limit)
}
func (c *Client) PeerDelivered(ctx context.Context, name string, seq uint64) error {
	return c.call(ctx, "PeerDelivered", nil, name, seq)
}
func (c *Client) PeerSendFailed(ctx context.Context, name string, seq uint64, reason string, retryAt time.Time, final bool) error {
	return c.call(ctx, "PeerSendFailed", nil, name, seq, reason, retryAt, final)
}
func (c *Client) PeerPulled(ctx context.Context, name string, b store.PeerBatch) (*store.PeerPulled, error) {
	return callV[*store.PeerPulled](c, ctx, "PeerPulled", name, b)
}
func (c *Client) CheckPeer(ctx context.Context, p store.Peer) error {
	return c.call(ctx, "CheckPeer", nil, p)
}
func (c *Client) PairPeer(ctx context.Context, p store.Peer, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "PairPeer", p, actor)
}
func (c *Client) PairRefused(ctx context.Context, reason, peer, actor string) error {
	return c.call(ctx, "PairRefused", nil, reason, peer, actor)
}
func (c *Client) PairOpen(ctx context.Context, o store.PairOffer, actor string) (*store.PairingCode, error) {
	return callV[*store.PairingCode](c, ctx, "PairOpen", o, actor)
}
func (c *Client) GetPairing(ctx context.Context, id string) (*store.Pairing, error) {
	return callV[*store.Pairing](c, ctx, "GetPairing", id)
}
func (c *Client) PairAnswer(ctx context.Context, id string, yes bool, actor string) (*store.Pairing, error) {
	return callV[*store.Pairing](c, ctx, "PairAnswer", id, yes, actor)
}
func (c *Client) PairName(ctx context.Context, id, name, actor string) (*store.Pairing, error) {
	return callV[*store.Pairing](c, ctx, "PairName", id, name, actor)
}

func (c *Client) PairClose(ctx context.Context, id, actor string) error {
	return c.call(ctx, "PairClose", nil, id, actor)
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
