package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A preview is a program the yard's crew built, run inside the yard under
// the supervisor's care and opened by the yard's person through the edge
// (design yardr-hosted-q0xp, sections 4-7). This file is the registry: one
// row per app in the previews bucket. Starting, stopping and watching the
// process are the supervisor's (internal/preview, part of yardr server);
// the approval card and the edge's origin are later beads. The supervisor
// may start only a row whose approval stands, and only the command that
// approval names.

var bPreviews = []byte("previews") // <app>

func init() { allBuckets = append(allBuckets, bPreviews) }

// Preview states. A stopped preview keeps its reason in StateDetail
// ("idle", "aged", "memory", "reboot", "stopped").
const (
	PreviewPending  = "pending"
	PreviewApproved = "approved"
	PreviewRunning  = "running"
	PreviewStopped  = "stopped"
	PreviewRefused  = "refused"
	PreviewCrashed  = "crashed"
	PreviewFailed   = "failed"
)

// Preview settings and bounds.
const (
	// PreviewIdleSetting is how long a preview may go without a request
	// before the supervisor stops it (design section 5).
	PreviewIdleSetting = "preview_idle"
	// DefaultPreviewIdle is the idle bound while the setting is unset.
	DefaultPreviewIdle = 4 * time.Hour
	// PreviewAgeLimit stops a preview however often it is used.
	PreviewAgeLimit = 7 * 24 * time.Hour
	// PreviewLimit is how many previews one yard may hold at once.
	PreviewLimit = 3
	// PreviewRunningLimit is how many may run at once.
	PreviewRunningLimit = 2
	// PreviewMemoryLimit is the process-group RSS cap the supervisor
	// watches (design section 7).
	PreviewMemoryLimit = 512 << 20
	// PreviewMaxInFlight is the requests in flight per preview.
	PreviewMaxInFlight = 20
)

// CheckPreviewApp refuses an app name outside its shape (design
// section 2.2): lowercase letters and digits with single dashes
// between, beginning with a letter or digit, at most 30 characters.
func CheckPreviewApp(app string) error {
	bad := len(app) == 0 || len(app) > 30 || strings.HasPrefix(app, "-") || strings.HasSuffix(app, "-") ||
		strings.Contains(app, "--")
	if !bad {
		for _, r := range app {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				bad = true
				break
			}
		}
	}
	if bad {
		return fmt.Errorf("preview app %q: lowercase letters, digits and single dashes, beginning with a letter or digit, at most 30 characters", app)
	}
	return nil
}

// PreviewApproval is who approved a preview and when. Its absence is what
// refuses a start: the supervisor starts no row without one.
type PreviewApproval struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
	// Command, Dir and Port are the approved form. A proposal that
	// changes any of them is a new card (design section 4.1); the
	// supervisor compares what it is about to start against these.
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Port    int    `json:"port"`
}

// PreviewRestartDetail is the state detail of a stopped preview whose
// person asked for a restart on the card: the supervisor kills its
// process if one still runs, flips the row back to approved, and starts
// the approved form again on its next pass, at the commit the ask
// re-read. The web view and the supervisor are separate processes
// sharing only the store, so the ask is completed asynchronously.
const PreviewRestartDetail = "restart"

// Preview is one row of the registry.
type Preview struct {
	App     string `json:"app"`
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Port    int    `json:"port"`
	// Depot, Commit and Dirty are the depot state the yard read itself
	// at proposal (design section 4.1), so the crew cannot misstate the
	// commit the person is approving.
	Depot  string `json:"depot,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`

	State       string           `json:"state"`
	StateDetail string           `json:"state_detail,omitempty"`
	Proposer    string           `json:"proposer,omitempty"`
	ProposedAt  time.Time        `json:"proposed_at"`
	Approval    *PreviewApproval `json:"approval,omitempty"`

	PID           int        `json:"pid,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`
	RestartCount  int        `json:"restart_count,omitempty"`
	// Exits holds the moments of the recent unclean exits the crash
	// rule counts (five in ten minutes, design section 6.2).
	Exits []time.Time `json:"exits,omitempty"`
}

// NewPreview is a preview as it is proposed. It carries no depot,
// commit or dirty mark: the store reads those from Dir itself
// (previewDepotState), since a proposer on the socket could otherwise
// name any commit for the person to approve.
type NewPreview struct {
	App     string `json:"app"`
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Port    int    `json:"port"`
}

// previewDepotState reads the depot state a proposal names: the depot
// whose path holds dir (the longest such), its HEAD commit and whether
// its tree is dirty (design section 4.1). A directory in no depot reads
// its own repository, named by its directory; one in no repository has
// no commit.
func (s *Store) previewDepotState(ctx context.Context, dir string) (depot, commit string, dirty bool) {
	depots, _ := s.Depots(ctx)
	root := dir
	for _, d := range depots {
		if (d.Path == dir || strings.HasPrefix(dir, d.Path+string(filepath.Separator))) && (depot == "" || len(d.Path) > len(root)) {
			depot, root = d.Name, d.Path
		}
	}
	if depot == "" {
		depot = filepath.Base(dir)
	}
	if out, err := git(ctx, root, "rev-parse", "HEAD"); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	if out, err := git(ctx, root, "status", "--porcelain"); err == nil {
		dirty = len(strings.TrimSpace(string(out))) > 0
	}
	return depot, commit, dirty
}

// PreviewIdle reads the yard's idle bound; a setting that does not parse
// counts as unset.
func PreviewIdle(settings map[string]string) time.Duration {
	if v := settings[PreviewIdleSetting]; v != "" {
		if d, err := ParseSpan(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultPreviewIdle
}

// ProposePreview records a pending preview. A live preview of the same
// name in the yard is refused, as is a fourth preview (PreviewLimit).
// Nothing runs yet: the row waits for its person's approval.
func (s *Store) ProposePreview(ctx context.Context, np NewPreview, actor string) (*Preview, error) {
	if err := CheckPreviewApp(np.App); err != nil {
		return nil, err
	}
	if strings.TrimSpace(np.Command) == "" {
		return nil, errors.New("preview: a start command is required (--do)")
	}
	if strings.TrimSpace(np.Dir) == "" {
		return nil, errors.New("preview: a directory is required (--in)")
	}
	// The supervisor runs the command in Dir, so a relative one would
	// be read against the server's own directory, not the one shown.
	if !filepath.IsAbs(np.Dir) {
		return nil, fmt.Errorf("preview: directory %q is not an absolute path", np.Dir)
	}
	if np.Port < 1 || np.Port > 65535 {
		return nil, fmt.Errorf("preview: port %d is not a port (1-65535)", np.Port)
	}
	p := &Preview{App: np.App, Command: np.Command, Dir: np.Dir, Port: np.Port, State: PreviewPending}
	p.Depot, p.Commit, p.Dirty = s.previewDepotState(ctx, np.Dir)
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		if resolved == "" {
			return fmt.Errorf("preview: the proposer is required")
		}
		actor = resolved
		// A forgotten preview is gone, so any row still here is live,
		// whatever its state (design section 3.1).
		if ok, err := get(w.Tx, bPreviews, np.App, new(Preview)); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("preview %s: a preview of that name is already in the yard (yardr preview list); forget it first if it should go", np.App)
		}
		live := 0
		if err := each(w.Tx, bPreviews, "", func(_ []byte, _ *Preview) error { live++; return nil }); err != nil {
			return err
		}
		if live >= PreviewLimit {
			return fmt.Errorf("preview: the yard holds %d previews, the most it may (%d): forget one first (yardr preview forget <app>)", live, PreviewLimit)
		}
		p.Proposer, p.ProposedAt = actor, now()
		if err := put(w.Tx, bPreviews, p.App, p); err != nil {
			return err
		}
		return w.event("", "preview_proposed", actor, map[string]any{
			"app": p.App, "command": p.Command, "dir": p.Dir, "port": p.Port,
			"depot": p.Depot, "commit": p.Commit, "dirty": p.Dirty})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Previews lists the yard's previews, by app name.
func (s *Store) Previews(ctx context.Context) ([]Preview, error) {
	out := []Preview{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPreviews, "", func(_ []byte, p *Preview) error {
			out = append(out, *p)
			return nil
		})
	})
	slices.SortFunc(out, func(a, b Preview) int { return strings.Compare(a.App, b.App) })
	return out, err
}

// GetPreview reads one preview.
func (s *Store) GetPreview(ctx context.Context, app string) (*Preview, error) {
	p := new(Preview)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bPreviews, app, p)
		if err == nil && !ok {
			err = fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		return err
	})
	return p, err
}

// PreviewHead reads the depot HEAD for the preview app now, so the
// Previews page can mark a preview whose commit the depot has moved past
// as stale. It is the read proposals use (previewDepotState), at page
// load rather than at proposal; the row keeps the commit it runs.
func (s *Store) PreviewHead(ctx context.Context, app string) (string, error) {
	p, err := s.GetPreview(ctx, app)
	if err != nil {
		return "", err
	}
	_, head, _ := s.previewDepotState(ctx, p.Dir)
	return head, nil
}

// ApprovePreview approves the pending preview app as actor, writing the
// approval the supervisor requires before any start. The approved form
// is the proposal as it stands; a later change to command, directory or
// port is refused by the supervisor until it is approved as its own
// proposal. This is the write the approval card (a later bead) makes;
// tests make it directly as their fixture.
func (s *Store) ApprovePreview(ctx context.Context, app, actor string) (*Preview, error) {
	p := new(Preview)
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if err := w.may(ctx, actor, ActApprovePreview, Object{Kind: KindPreview, Name: app}); err != nil {
			return err
		}
		if p.State != PreviewPending && p.Approval == nil {
			return fmt.Errorf("preview %s is %s and has no proposal to approve", app, p.State)
		}
		p.Approval = &PreviewApproval{By: actor, At: now(), Command: p.Command, Dir: p.Dir, Port: p.Port}
		p.State, p.StateDetail = PreviewApproved, ""
		if err := put(w.Tx, bPreviews, app, p); err != nil {
			return err
		}
		return w.event("", "preview_approved", actor, map[string]any{"app": app, "command": p.Command, "port": p.Port})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RefusePreview refuses the pending preview app as actor, leaving the
// row refused with its proposer, so the refused card stays listed and a
// later identical proposal is a new card (design yardr-hosted-q0xp
// section 4.1). Refusal is the operator's, by the same rule as approval;
// nothing starts.
func (s *Store) RefusePreview(ctx context.Context, app, actor string) (*Preview, error) {
	p := new(Preview)
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if err := w.may(ctx, actor, ActApprovePreview, Object{Kind: KindPreview, Name: app}); err != nil {
			return err
		}
		if p.State != PreviewPending {
			return fmt.Errorf("preview %s is %s and has no proposal to refuse", app, p.State)
		}
		p.State, p.StateDetail = PreviewRefused, ""
		if err := put(w.Tx, bPreviews, app, p); err != nil {
			return err
		}
		return w.event("", "preview_refused", actor, map[string]any{"app": app, "command": p.Command, "port": p.Port})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// PreviewUpdate is one change the supervisor records. ExpectedState
// conditions a state change on the row still having the state read by its
// caller; empty leaves it unconditioned. The zero fields are left as they
// are, except PID, where -1 clears it, and the two moments, where a nil
// pointer leaves them. State "" leaves the state.
type PreviewUpdate struct {
	ExpectedState string // "": any state; otherwise only this state may change
	State         string
	StateDetail   *string
	PID           int // 0: leave; -1: clear; >0: set
	StartedAt     *time.Time
	LastRequest   *time.Time
	Restarts      *int
	Exits         []time.Time // nil: leave
	Commit        string      // "": leave; the commit a (re)start runs
}

// UpdatePreview records upd on the preview app. It is the supervisor's
// write; it refuses a row that is not there.
func (s *Store) UpdatePreview(ctx context.Context, app string, upd PreviewUpdate) (*Preview, error) {
	p := new(Preview)
	err := s.write(func(w *wtx) error {
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if upd.ExpectedState != "" && p.State != upd.ExpectedState {
			return fmt.Errorf("preview %s is %s; expected %s", app, p.State, upd.ExpectedState)
		}
		if upd.State == PreviewRunning && p.State != PreviewApproved {
			return fmt.Errorf("preview %s is %s; only an approved preview may start", app, p.State)
		}
		if upd.State != "" {
			p.State = upd.State
		}
		if upd.StateDetail != nil {
			p.StateDetail = *upd.StateDetail
		}
		switch {
		case upd.PID > 0:
			p.PID = upd.PID
		case upd.PID < 0:
			p.PID = 0
		}
		if upd.StartedAt != nil {
			p.StartedAt = upd.StartedAt
		}
		if upd.LastRequest != nil {
			p.LastRequestAt = upd.LastRequest
		}
		if upd.Restarts != nil {
			p.RestartCount = *upd.Restarts
		}
		if upd.Exits != nil {
			p.Exits = upd.Exits
		}
		if upd.Commit != "" {
			p.Commit = upd.Commit
		}
		return put(w.Tx, bPreviews, app, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// LogPreviewEvent records one supervisor event for a preview (a start,
// a stop, a refusal), beside the registry write that carries its state.
func (s *Store) LogPreviewEvent(ctx context.Context, app, kind, detail string) error {
	return s.write(func(w *wtx) error {
		data := map[string]any{"app": app}
		if detail != "" {
			data["detail"] = detail
		}
		return w.event("", kind, "yard", data)
	})
}

// PreviewStop asks for the preview app to stop: the row is marked
// stopped and the supervisor kills its process group. The proposer and
// the yard's person may stop it (design section 5); the store records
// the ask and the supervisor does the killing, so a stop asked while no
// supervisor runs still stands at its next start.
func (s *Store) PreviewStop(ctx context.Context, app, actor string) (*Preview, error) {
	p := new(Preview)
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if err := w.may(ctx, actor, ActStopPreview, Object{Kind: KindPreview, Name: app, Whose: p.Proposer}); err != nil {
			return err
		}
		if p.State == PreviewPending {
			return fmt.Errorf("preview %s is pending and has not started; forget it to remove it (yardr preview forget %s)", app, app)
		}
		p.State, p.StateDetail = PreviewStopped, "stopped"
		if err := put(w.Tx, bPreviews, app, p); err != nil {
			return err
		}
		return w.event("", "preview_stop_asked", actor, map[string]any{"app": app})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RestartPreview asks for the preview app to stop and start its approved
// form again: the ask on the Previews page's Restart button. Only a row
// with an approval restarts; the depot commit is re-read as proposals
// read it, so the restart runs what the yard has built since. Starting
// crew code is the person's authority, so the gate is the operator's,
// the same rule as approval. The row is left stopped for the supervisor
// to complete (PreviewRestartDetail).
func (s *Store) RestartPreview(ctx context.Context, app, actor string) (*Preview, error) {
	// The commit is re-read before the write, as proposals read it:
	// the read opens its own transaction, which a write cannot hold.
	row, err := s.GetPreview(ctx, app)
	if err != nil {
		return nil, err
	}
	_, head, _ := s.previewDepotState(ctx, row.Dir)
	p := new(Preview)
	err = s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if err := w.may(ctx, actor, ActApprovePreview, Object{Kind: KindPreview, Name: app}); err != nil {
			return err
		}
		if p.Approval == nil {
			return fmt.Errorf("preview %s has no approved form to restart", app)
		}
		p.State, p.StateDetail = PreviewStopped, PreviewRestartDetail
		if head != "" {
			p.Commit = head
		}
		if err := put(w.Tx, bPreviews, app, p); err != nil {
			return err
		}
		return w.event("", "preview_restart_asked", actor, map[string]any{"app": app, "commit": p.Commit})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ForgetPreview removes the preview app and its approval with it, so
// the next start of that app is a new card (design section 5). A running
// preview is refused: stop it first, so forgetting never orphans a
// process the registry no longer names.
func (s *Store) ForgetPreview(ctx context.Context, app, actor string) (*Preview, error) {
	p := new(Preview)
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ok, err := get(w.Tx, bPreviews, app, p)
		if err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("preview %s: %w", app, ErrNotFound)
		}
		if err := w.may(ctx, actor, ActForgetPreview, Object{Kind: KindPreview, Name: app}); err != nil {
			return err
		}
		if p.State == PreviewRunning {
			return fmt.Errorf("preview %s is running: stop it first (yardr preview stop %s)", app, app)
		}
		if err := w.Bucket(bPreviews).Delete([]byte(app)); err != nil {
			return err
		}
		return w.event("", "preview_forgotten", actor, map[string]any{"app": app})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}
