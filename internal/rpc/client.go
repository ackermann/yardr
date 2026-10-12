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
		return unanswered{err}
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
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
func (c *Client) Waiting(ctx context.Context) ([]store.Dep, error) {
	return callV[[]store.Dep](c, ctx, "Waiting")
}
func (c *Client) Dispatchable(ctx context.Context) ([]store.ReadyBead, error) {
	return callV[[]store.ReadyBead](c, ctx, "Dispatchable")
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

func (c *Client) AddDep(ctx context.Context, from, to, kind, at, until, actor string) error {
	return c.call(ctx, "AddDep", nil, from, to, kind, at, until, actor)
}
func (c *Client) RemoveDep(ctx context.Context, from, to, kind, at, until, actor string) error {
	return c.call(ctx, "RemoveDep", nil, from, to, kind, at, until, actor)
}
func (c *Client) Deps(ctx context.Context, id string) ([]store.Dep, error) {
	return callV[[]store.Dep](c, ctx, "Deps", id)
}
func (c *Client) AllDeps(ctx context.Context) ([]store.Dep, error) {
	return callV[[]store.Dep](c, ctx, "AllDeps")
}
func (c *Client) OpenBlockers(ctx context.Context, id string) ([]string, error) {
	return callV[[]string](c, ctx, "OpenBlockers", id)
}
func (c *Client) HoldingDeps(ctx context.Context, id string) ([]store.Dep, error) {
	return callV[[]store.Dep](c, ctx, "HoldingDeps", id)
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
func (c *Client) MoveLinkSource(ctx context.Context, from, to, actor string) (int, error) {
	return callV[int](c, ctx, "MoveLinkSource", from, to, actor)
}
func (c *Client) MoveSourceRecords(ctx context.Context, from, to, actor string) (*store.SourceMove, error) {
	return callV[*store.SourceMove](c, ctx, "MoveSourceRecords", from, to, actor)
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
func (c *Client) AddAsk(ctx context.Context, id string, ask store.Ask, actor string) (uint64, error) {
	return callV[uint64](c, ctx, "AddAsk", id, ask, actor)
}
func (c *Client) AskNew(ctx context.Context, nb store.NewBead, ask store.Ask, actor string) (*store.AskedBead, error) {
	return callV[*store.AskedBead](c, ctx, "AskNew", nb, ask, actor)
}
func (c *Client) AddAnswer(ctx context.Context, id string, ans store.Answer, actor string) error {
	return c.call(ctx, "AddAnswer", nil, id, ans, actor)
}
func (c *Client) UserAsks(ctx context.Context, name string) (*store.AskQueue, error) {
	return callV[*store.AskQueue](c, ctx, "UserAsks", name)
}
func (c *Client) Bead(ctx context.Context, id string) (*store.BeadView, error) {
	return callV[*store.BeadView](c, ctx, "Bead", id)
}
func (c *Client) AnswerAsk(ctx context.Context, id string, ask uint64, option int, words, actor string) error {
	return c.call(ctx, "AnswerAsk", nil, id, ask, option, words, actor)
}
func (c *Client) DeferAsk(ctx context.Context, id string, ask uint64, actor string) error {
	return c.call(ctx, "DeferAsk", nil, id, ask, actor)
}
func (c *Client) DeclineAsk(ctx context.Context, id string, ask uint64, words, actor string) error {
	return c.call(ctx, "DeclineAsk", nil, id, ask, words, actor)
}
func (c *Client) TakeBackAsk(ctx context.Context, id string, ask uint64, reason, actor string) error {
	return c.call(ctx, "TakeBackAsk", nil, id, ask, reason, actor)
}

func (c *Client) PutDepot(ctx context.Context, r store.Depot, actor string) error {
	return c.call(ctx, "PutDepot", nil, r, actor)
}
func (c *Client) Depots(ctx context.Context) ([]store.Depot, error) {
	return callV[[]store.Depot](c, ctx, "Depots")
}
func (c *Client) GetDepot(ctx context.Context, name string) (*store.Depot, error) {
	return callV[*store.Depot](c, ctx, "GetDepot", name)
}
func (c *Client) DeleteDepot(ctx context.Context, name, actor string) (*store.DepotRemoval, error) {
	return callV[*store.DepotRemoval](c, ctx, "DeleteDepot", name, actor)
}

func (c *Client) PutGroup(ctx context.Context, g store.Group, actor string) error {
	return c.call(ctx, "PutGroup", nil, g, actor)
}
func (c *Client) SetGroup(ctx context.Context, name string, e store.GroupEdit, actor string) (*store.GroupChange, error) {
	return callV[*store.GroupChange](c, ctx, "SetGroup", name, e, actor)
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
func (c *Client) AddRoute(ctx context.Context, r store.Route, actor string) (*store.Route, error) {
	return callV[*store.Route](c, ctx, "AddRoute", r, actor)
}
func (c *Client) Routes(ctx context.Context) ([]store.Route, error) {
	return callV[[]store.Route](c, ctx, "Routes")
}
func (c *Client) DeleteRoute(ctx context.Context, id, actor string) error {
	return c.call(ctx, "DeleteRoute", nil, id, actor)
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
func (c *Client) SetFlowPointer(ctx context.Context, depot, typ, flow, actor string) error {
	return c.call(ctx, "SetFlowPointer", nil, depot, typ, flow, actor)
}
func (c *Client) FlowPointers(ctx context.Context) ([]store.FlowPointer, error) {
	return callV[[]store.FlowPointer](c, ctx, "FlowPointers")
}
func (c *Client) RepoFlows(ctx context.Context, depot string) (*store.RepoFlows, error) {
	return callV[*store.RepoFlows](c, ctx, "RepoFlows", depot)
}
func (c *Client) DepotFlows(ctx context.Context, depot string) ([]store.DepotFlow, error) {
	return callV[[]store.DepotFlow](c, ctx, "DepotFlows", depot)
}
func (c *Client) NextStages(ctx context.Context, id string) (*store.NextStages, error) {
	return callV[*store.NextStages](c, ctx, "NextStages", id)
}
func (c *Client) FlowMap(ctx context.Context, id string) (*store.FlowMap, error) {
	return callV[*store.FlowMap](c, ctx, "FlowMap", id)
}
func (c *Client) Stranded(ctx context.Context, depot string) ([]store.StrandedBead, error) {
	return callV[[]store.StrandedBead](c, ctx, "Stranded", depot)
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
func (c *Client) ListSessions(ctx context.Context, f store.SessionFilter) ([]store.Session, error) {
	return callV[[]store.Session](c, ctx, "ListSessions", f)
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
func (c *Client) EventsOfKind(ctx context.Context, kind string, limit int) ([]store.Event, error) {
	return callV[[]store.Event](c, ctx, "EventsOfKind", kind, limit)
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

func (c *Client) PutCrew(ctx context.Context, def store.CrewMember, actor string) (*store.CrewMember, error) {
	return callV[*store.CrewMember](c, ctx, "PutCrew", def, actor)
}
func (c *Client) Crew(ctx context.Context) ([]store.CrewMember, error) {
	return callV[[]store.CrewMember](c, ctx, "Crew")
}
func (c *Client) GetCrew(ctx context.Context, name string) (*store.CrewMember, error) {
	return callV[*store.CrewMember](c, ctx, "GetCrew", name)
}
func (c *Client) DeleteCrew(ctx context.Context, name, rev, actor string) error {
	return c.call(ctx, "DeleteCrew", nil, name, rev, actor)
}
func (c *Client) SetCrewEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error {
	return c.call(ctx, "SetCrewEnabled", nil, name, enabled, rev, actor)
}
func (c *Client) CrewStarted(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "CrewStarted", nil, name, handle)
}
func (c *Client) CrewSettled(ctx context.Context, name string) error {
	return c.call(ctx, "CrewSettled", nil, name)
}
func (c *Client) SetCrewHandle(ctx context.Context, name string, handle map[string]any) error {
	return c.call(ctx, "SetCrewHandle", nil, name, handle)
}
func (c *Client) CrewStopped(ctx context.Context, name, reason string) error {
	return c.call(ctx, "CrewStopped", nil, name, reason)
}
func (c *Client) CrewGone(ctx context.Context, name, reason string) error {
	return c.call(ctx, "CrewGone", nil, name, reason)
}
func (c *Client) CrewLeft(ctx context.Context, name string, left []map[string]any) error {
	return c.call(ctx, "CrewLeft", nil, name, left)
}
func (c *Client) CrewStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error {
	return c.call(ctx, "CrewStartFailed", nil, name, reason, backoff)
}
func (c *Client) RequestRestart(ctx context.Context, name, reason, rev, actor string) error {
	return c.call(ctx, "RequestRestart", nil, name, reason, rev, actor)
}
func (c *Client) MarkRotationAsked(ctx context.Context, name string) error {
	return c.call(ctx, "MarkRotationAsked", nil, name)
}
func (c *Client) CrewStatus(ctx context.Context, name, status string) error {
	return c.call(ctx, "CrewStatus", nil, name, status)
}
func (c *Client) MarkNotified(ctx context.Context, name string, seq uint64) error {
	return c.call(ctx, "MarkNotified", nil, name, seq)
}
func (c *Client) Journal(ctx context.Context, name, kind, text, actor string) error {
	return c.call(ctx, "Journal", nil, name, kind, text, actor)
}
func (c *Client) Mail(ctx context.Context, to, text, actor string, files ...string) error {
	return c.call(ctx, "Mail", nil, to, text, actor, files)
}

func (c *Client) PutBlob(ctx context.Context, data []byte) (*store.BlobRef, error) {
	return callV[*store.BlobRef](c, ctx, "PutBlob", data)
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
func (c *Client) SetUser(ctx context.Context, name string, set map[string]string, actor string) (*store.User, error) {
	return callV[*store.User](c, ctx, "SetUser", name, set, actor)
}
func (c *Client) DeleteUser(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteUser", nil, name, actor)
}
func (c *Client) AddDevice(ctx context.Context, user, name, actor string) (*store.NewDevice, error) {
	return callV[*store.NewDevice](c, ctx, "AddDevice", user, name, actor)
}
func (c *Client) Devices(ctx context.Context, user string) ([]store.Device, error) {
	return callV[[]store.Device](c, ctx, "Devices", user)
}
func (c *Client) RemoveDevice(ctx context.Context, user, name, actor string) (*store.Device, error) {
	return callV[*store.Device](c, ctx, "RemoveDevice", user, name, actor)
}
func (c *Client) DevicePairOpen(ctx context.Context, user, name, actor string) (*store.DevicePairingCode, error) {
	return callV[*store.DevicePairingCode](c, ctx, "DevicePairOpen", user, name, actor)
}
func (c *Client) DevicePairClose(ctx context.Context, actor string) (*store.DevicePairing, error) {
	return callV[*store.DevicePairing](c, ctx, "DevicePairClose", actor)
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
func (c *Client) RemoveTimer(ctx context.Context, id, actor string) (*store.Timer, error) {
	return callV[*store.Timer](c, ctx, "RemoveTimer", id, actor)
}
func (c *Client) FireTimers(ctx context.Context, at time.Time) (*store.TimerPass, error) {
	return callV[*store.TimerPass](c, ctx, "FireTimers", at)
}
func (c *Client) CrewPrime(ctx context.Context, name string) (*store.Prime, error) {
	return callV[*store.Prime](c, ctx, "CrewPrime", name)
}
func (c *Client) Remember(ctx context.Context, m store.NewMemory, actor string) (*store.Bead, error) {
	return callV[*store.Bead](c, ctx, "Remember", m, actor)
}
func (c *Client) Recall(ctx context.Context, scope, query string) ([]store.MemoryInfo, error) {
	return callV[[]store.MemoryInfo](c, ctx, "Recall", scope, query)
}
func (c *Client) RecallJournal(ctx context.Context, scope, query string) ([]store.JournalHit, error) {
	return callV[[]store.JournalHit](c, ctx, "RecallJournal", scope, query)
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
func (c *Client) Replying(ctx context.Context, name string, seq uint64, to, body, actor string) (*store.Reply, error) {
	return callV[*store.Reply](c, ctx, "Replying", name, seq, to, body, actor)
}
func (c *Client) Replied(ctx context.Context, name string, rp store.Reply, actor string) error {
	return c.call(ctx, "Replied", nil, name, rp, actor)
}
func (c *Client) Opening(ctx context.Context, name, source, in, title, body, actor string) (*store.Reply, error) {
	return callV[*store.Reply](c, ctx, "Opening", name, source, in, title, body, actor)
}
func (c *Client) Opened(ctx context.Context, name string, rp store.Reply, actor string) error {
	return c.call(ctx, "Opened", nil, name, rp, actor)
}
func (c *Client) AllowOpening(ctx context.Context, name string, sources []string, pack, actor string) ([]string, error) {
	return callV[[]string](c, ctx, "AllowOpening", name, sources, pack, actor)
}
func (c *Client) SettingState(ctx context.Context, name, source, thread, actor string) (*store.Participation, error) {
	return callV[*store.Participation](c, ctx, "SettingState", name, source, thread, actor)
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
func (c *Client) PutSource(ctx context.Context, file, text, actor string) (*store.Source, error) {
	return callV[*store.Source](c, ctx, "PutSource", file, text, actor)
}
func (c *Client) Sources(ctx context.Context) ([]store.Source, error) {
	return callV[[]store.Source](c, ctx, "Sources")
}
func (c *Client) GetSource(ctx context.Context, name string) (*store.Source, error) {
	return callV[*store.Source](c, ctx, "GetSource", name)
}
func (c *Client) SetSource(ctx context.Context, name, key, value, actor string) (*store.Source, error) {
	return callV[*store.Source](c, ctx, "SetSource", name, key, value, actor)
}
func (c *Client) GiveSecret(ctx context.Context, id string, ask uint64, values map[string]string, actor string) (*store.SourceKeyed, error) {
	return callV[*store.SourceKeyed](c, ctx, "GiveSecret", id, ask, values, actor)
}
func (c *Client) KeySource(ctx context.Context, name string, values map[string]string, actor string) (*store.SourceKeyed, error) {
	return callV[*store.SourceKeyed](c, ctx, "KeySource", name, values, actor)
}
func (c *Client) DeleteSource(ctx context.Context, name, actor string) error {
	return c.call(ctx, "DeleteSource", nil, name, actor)
}
func (c *Client) GrantSource(ctx context.Context, src, what string, to store.Grantee, actor string) (*store.SourceGrant, error) {
	return callV[*store.SourceGrant](c, ctx, "GrantSource", src, what, to, actor)
}
func (c *Client) RevokeSource(ctx context.Context, src, what string, to store.Grantee, actor string) (*store.SourceGrant, error) {
	return callV[*store.SourceGrant](c, ctx, "RevokeSource", src, what, to, actor)
}
func (c *Client) SourceGrants(ctx context.Context, src string) ([]store.SourceGrant, error) {
	return callV[[]store.SourceGrant](c, ctx, "SourceGrants", src)
}
func (c *Client) SourceCalls(ctx context.Context, session, actor string) (*store.Callables, error) {
	return callV[*store.Callables](c, ctx, "SourceCalls", session, actor)
}
func (c *Client) SessionCalls(ctx context.Context, session string) (*store.Callables, error) {
	return callV[*store.Callables](c, ctx, "SessionCalls", session)
}
func (c *Client) CallSource(ctx context.Context, req store.SourceCall) (*store.SourceAnswer, error) {
	return callV[*store.SourceAnswer](c, ctx, "CallSource", req)
}
func (c *Client) Pushes(ctx context.Context) ([]store.Push, error) {
	return callV[[]store.Push](c, ctx, "Pushes")
}
func (c *Client) DropPushes(ctx context.Context, name string, seqs []uint64) error {
	return c.call(ctx, "DropPushes", nil, name, seqs)
}
func (c *Client) PushTried(ctx context.Context, name string, seqs []uint64, res store.PushResult) error {
	return c.call(ctx, "PushTried", nil, name, seqs, res)
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
func (c *Client) SetPeerURL(ctx context.Context, name, url, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "SetPeerURL", name, url, actor)
}
func (c *Client) SetPeerIntake(ctx context.Context, name string, in *store.PeerIntake, actor string) (*store.Peer, error) {
	return callV[*store.Peer](c, ctx, "SetPeerIntake", name, in, actor)
}
func (c *Client) PeerSend(ctx context.Context, name, kind string, body json.RawMessage, actor string) (*store.PeerOut, error) {
	return callV[*store.PeerOut](c, ctx, "PeerSend", name, kind, body, actor)
}
func (c *Client) MailPeer(ctx context.Context, to, peer, text, actor string, files ...string) (*store.PeerOut, error) {
	return callV[*store.PeerOut](c, ctx, "MailPeer", to, peer, text, actor, files)
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
func (c *Client) PeerRefused(ctx context.Context, name string, seq uint64, reason string) error {
	return c.call(ctx, "PeerRefused", nil, name, seq, reason)
}
func (c *Client) PeerBeadSend(ctx context.Context, session string) (*store.BeadPeer, error) {
	return callV[*store.BeadPeer](c, ctx, "PeerBeadSend", session)
}
func (c *Client) PeerRead(ctx context.Context, bead, actor string) (*store.PeerReading, error) {
	return callV[*store.PeerReading](c, ctx, "PeerRead", bead, actor)
}
func (c *Client) PeerReadKept(ctx context.Context, bead, actor string) (*store.PeerReading, error) {
	return callV[*store.PeerReading](c, ctx, "PeerReadKept", bead, actor)
}
func (c *Client) PeerNote(ctx context.Context, bead, text, actor string) (*store.PeerNoted, error) {
	return callV[*store.PeerNoted](c, ctx, "PeerNote", bead, text, actor)
}
func (c *Client) SettlePeerBeads(ctx context.Context) error {
	return c.call(ctx, "SettlePeerBeads", nil)
}
func (c *Client) PeerFetches(ctx context.Context) ([]store.PeerFetch, error) {
	return callV[[]store.PeerFetch](c, ctx, "PeerFetches")
}
func (c *Client) PeerFetched(ctx context.Context, bead, message, notTaken string) error {
	return c.call(ctx, "PeerFetched", nil, bead, message, notTaken)
}
func (c *Client) PeerFetchFailed(ctx context.Context, bead, message, reason string, retryAt time.Time, final bool) error {
	return c.call(ctx, "PeerFetchFailed", nil, bead, message, reason, retryAt, final)
}
func (c *Client) PeerBases(ctx context.Context) ([]store.PeerBase, error) {
	return callV[[]store.PeerBase](c, ctx, "PeerBases")
}
func (c *Client) PeerPulled(ctx context.Context, name string, b store.PeerBatch) (*store.PeerPulled, error) {
	return callV[*store.PeerPulled](c, ctx, "PeerPulled", name, b)
}

func (c *Client) PeerPostAnswered(ctx context.Context, name string, answer store.PeerAnswer) error {
	return c.call(ctx, "PeerPostAnswered", nil, name, answer)
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
