package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// statusReport is what yardr status prints: what of the yard runs now, and
// where each part is reached. It holds facts only; what is wrong or missing
// is yardr doctor's, which Faults points at.
type statusReport struct {
	Home     string `json:"home"`
	Name     string `json:"name,omitempty"`
	Operator string `json:"operator,omitempty"`

	Server serverStatus `json:"server"`
	Daemon daemonState  `json:"daemon"`
	// Residents, Hooks, Peers, Work and Faults are the store's, and absent
	// while no server answers for it: status starts none to ask.
	Residents []residentStatus `json:"residents,omitzero"`
	Web       webStatus        `json:"web"`
	// Hooks is absent too on a yard with no hook and no address for them.
	Hooks *hookStatus  `json:"hooks,omitempty"`
	Peers []peerStatus `json:"peers,omitempty"`
	Work  *workCounts  `json:"work,omitempty"`
	// Gates are the full gates of the machine, of this yard and any other of
	// the user: absent with no limit set, and while none runs or waits.
	Gates *gateStatus `json:"gates,omitempty"`
	// Faults is how many faults yardr doctor reports on this yard now.
	Faults *int `json:"faults,omitempty"`
}

// serverStatus is the process that owns the store.
type serverStatus struct {
	// Running is whether one answers on the home's socket. Error is why
	// one that answers there could not be asked about the yard.
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	PID     int    `json:"pid,omitempty"`
	// Build is the revision it runs, as yardr version names one; CLI is
	// the build of the yardr that was asked.
	Build string     `json:"build,omitempty"`
	Since *time.Time `json:"since,omitempty"`
	CLI   string     `json:"cli_build"`
	// Differs is whether the two are different builds.
	Differs bool `json:"differs"`
}

// residentStatus is a resident with the state yardr chat lists it with.
type residentStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
	// Agent is herdr's name for its session; "" when none runs.
	Agent string `json:"agent,omitempty"`
}

// webStatus is the web view: each one that runs, and its login service.
type webStatus struct {
	Views   []webView    `json:"views"`
	Service serviceState `json:"service"`
}

// hookStatus is the webhook receiver and the hooks it serves.
type hookStatus struct {
	// Addr is the address the server was given for webhooks, "" for none;
	// Listening is whether it takes them there.
	Addr      string         `json:"addr,omitempty"`
	Listening bool           `json:"listening"`
	Hooks     []hookEndpoint `json:"hooks"`
}

// hookEndpoint is one hook.
type hookEndpoint struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	// URL is where its source posts to, "" while the server takes none.
	URL string `json:"url,omitempty"`
}

type peerStatus struct {
	Name string `json:"name"`
	// URL is where the peer is called; "" for one that calls this yard.
	URL string `json:"url,omitempty"`
}

// workCounts are the yard's work in numbers.
type workCounts struct {
	// Sessions are the sessions that run something, by group: those of a
	// group whose runner starts an agent or a script.
	Sessions map[string]int `json:"sessions"`
	// Waiting are the beads a manual group has claimed, by group: a
	// person's turn, with no process behind the claim.
	Waiting map[string]int `json:"waiting"`
	Held    int            `json:"held"`
}

// gateStatus is what runs and waits for a gate slot on the machine, and the
// yard's limit.
type gateStatus struct {
	rigcheck.SlotCount
	Limit int `json:"limit"`
}

// serverInfo asks the home's server which process and build it is; tests
// replace it.
var serverInfo = server.Describe

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "What of the yard runs now, and where to reach it (read-only)",
		Long: `Print what runs right now and where it is reached, in one screen:

  yard       its home, its name, the operator
  server     running or not, its pid, the build it runs and since when; the
             build of this yardr when that is another
  daemon     running or not, its pid; its login service
  residents  each with what it is doing, as yardr chat lists it, and its
             herdr agent
  web view   each view that runs: the URL to open, its pid, the build it
             runs, and whether it is open to the network (--unsafe, no login)
             or to this machine only; the build of this yardr when that is
             another; its login service
  hooks      the address the server takes webhooks on, each hook and its URL
  peers      each linked yard and where it is called
  work       how many sessions run in each group, how many beads wait for
             a person in each manual group, and how many beads are held
  gates      how many full gates (yardr rig check) run on the machine now and
             how many wait for a slot, with the yard's limit (gate_slots);
             they are the machine's, so another yard's gates are counted

A part the yard does not have (hooks, peers) has no line, nor have the gates
while none runs or waits. It is read-only
and starts nothing: on a yard whose server is not running it says so, with
the daemon and the web views, which it reads from the home, and one line
for the residents, hooks, peers and work, which only the server knows.

A web view does not replace itself when a new yardr is installed, as the
server and the daemon do: it runs the build it was started on until it is
started again, which is why its build is listed.

yardr status says what is there; yardr doctor says what is wrong or missing.
When doctor reports a fault, the last line of status says so and names it,
without repeating it. The exit status is 0 whenever the home holds a yard,
and 2 on a home with none.

A web view is found by the record it keeps in <home>/webviews while it
runs. One started by a build of yardr before this command existed keeps
none and is not listed until it is started again.`,
		Example: `  yardr status
  yardr status --json | jq -r '.web.views[].url'   # where the web view is`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			rep, err := a.status(c.Context(), cfg)
			if err != nil {
				return err
			}
			return a.out(rep, rep.print)
		},
	}
}

// status reads the report. The yard's state is doctor's, read once: status
// lists from it what doctor counts, and takes doctor's faults as they are.
//
// It starts no server: it says what runs, and a server it started itself
// would be one that did not. So the home is asked first whether it holds a
// store (a home without one holds no yard), then whether a server answers,
// and only one that answers is asked about the yard. Without one the report
// is what the home and the machine say: the daemon, the web views, the
// login services. Whether a store nobody answers for holds a yard that was
// set up is not known then, and it is reported as the yard it most likely
// is.
func (a *app) status(ctx context.Context, cfg *config.Config) (*statusReport, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, fs.ErrNotExist) {
			return nil, notInitialized{cfg.Home}
		}
	}
	rep := &statusReport{Home: cfg.Home,
		Daemon: readDaemon(ctx, cfg),
		Web:    webStatus{Views: webViews(cfg), Service: stateOf(ctx, cfg, svcWeb)},
		Server: serverStatus{CLI: version.Describe()}}
	if !serverRunning(ctx, cfg) {
		return rep, nil
	}
	rep.Server.Running = true
	if a.st == nil {
		// A client of the server that answered, not a.store: that one
		// starts a server when this one has stopped in between, and here a
		// server that is gone is one that cannot be reached.
		a.st, a.cfg = rpc.NewClient(cfg.Socket()), cfg
	}
	set, err := a.initialized(ctx, cfg)
	if err == nil && !set {
		return nil, notInitialized{cfg.Home}
	}
	doc, err := a.examine(ctx, cfg, false, true, set, err)
	if err != nil {
		return nil, err
	}
	faults := len(doc.Faults)
	rep.Faults = &faults
	if doc.State.Server == "unreachable" {
		rep.Server.Error = doc.State.ServerError
		return rep, nil
	}
	if info, ok := serverInfo(ctx, cfg); ok {
		rep.Server.PID, rep.Server.Build = info.PID, version.Show(info.Revision)
		rep.Server.Differs = info.Revision != version.Current()
		if !info.Since.IsZero() {
			rep.Server.Since = &info.Since
		}
	}
	rep.Residents = []residentStatus{}
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rep.Name, rep.Operator = runner.YardName(cfg.Home, all), or(all["operator"], defaultOperator())
	for _, r := range doc.State.residents {
		rs := residentStatus{Name: r.Name, State: chat.StateOf(r)}
		if r.Live() && r.Handle["agent"] != nil {
			rs.Agent = show(r.Handle["agent"])
		}
		rep.Residents = append(rep.Residents, rs)
	}
	if rep.Hooks, err = readHooks(ctx, cfg, st); err != nil {
		return nil, err
	}
	peers, err := st.Peers(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range peers {
		rep.Peers = append(rep.Peers, peerStatus{Name: p.Name, URL: p.URL})
	}
	sessions, err := st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	rep.Work = &workCounts{Sessions: map[string]int{}, Waiting: map[string]int{}, Held: len(doc.State.Held)}
	for _, s := range sessions {
		if s.Manual() {
			rep.Work.Waiting[s.Group]++
		} else {
			rep.Work.Sessions[s.Group]++
		}
	}
	// A limit that does not read is rig check's to say, not status's.
	if limit, err := rigcheck.SlotLimit(all); err == nil && limit > 0 {
		if c, err := rigcheck.Count(rigcheck.SlotDir()); err == nil && c.Running+c.Waiting > 0 {
			rep.Gates = &gateStatus{SlotCount: c, Limit: limit}
		}
	}
	return rep, nil
}

// readHooks is the receiver as the running server said it is, and the
// yard's hooks; nil for a yard with neither a hook nor an address.
func readHooks(ctx context.Context, cfg *config.Config, st store.API) (*hookStatus, error) {
	hs, err := st.Hooks(ctx)
	if err != nil {
		return nil, err
	}
	l, known := hooks.ReadListener(cfg)
	if len(hs) == 0 && !(known && l.Addr != "") {
		return nil, nil
	}
	out := &hookStatus{Hooks: []hookEndpoint{}}
	if known {
		out.Addr, out.Listening = l.Addr, l.Listening()
	}
	for _, h := range hs {
		one := hookEndpoint{Name: h.Name, Source: h.Source}
		if out.Listening {
			one.URL = "http://" + out.Addr + "/hooks/" + h.Name
		}
		out.Hooks = append(out.Hooks, one)
	}
	return out, nil
}

// since is a time as the listings print one.
func since(t time.Time) string { return t.Local().Format("01-02 15:04") }

// plural is n of a thing, in words: 1 bead, 2 beads.
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// columns pads the cells of each row to the widest of their column, the
// last one aside, and joins them.
func columns(rows [][]string) []string {
	var width []int
	for _, r := range rows {
		for i, c := range r[:max(len(r)-1, 0)] {
			if i == len(width) {
				width = append(width, 0)
			}
			width[i] = max(width[i], len(c))
		}
	}
	out := make([]string, len(rows))
	for n, r := range rows {
		for i, c := range r {
			if i < len(r)-1 {
				c = fmt.Sprintf("%-*s", width[i], c)
			}
			r[i] = c
		}
		out[n] = strings.TrimRight(strings.Join(r, "  "), " ")
	}
	return out
}

// statusPart is a part of the report: what it is about, and its lines.
type statusPart struct {
	name  string
	lines []string
}

// parts are the report as it is printed, a part for what exists.
func (r *statusReport) parts() []statusPart {
	yardr := yardrAt(r.Home)
	yard := r.Home
	if r.Name != "" {
		yard += fmt.Sprintf("  name %s  operator %s", r.Name, r.Operator)
	}
	parts := []statusPart{{"yard", []string{yard}}}

	var srv []string
	switch s := r.Server; {
	case !s.Running:
		srv = []string{fmt.Sprintf("not running: any yardr command that needs the yard starts it (%s ready), or %s server start", yardr, yardr)}
	case s.Error != "":
		srv = []string{"cannot be reached: " + s.Error}
	default:
		line := "running"
		if s.PID != 0 {
			line += fmt.Sprintf("  pid %d  build %s", s.PID, s.Build)
		}
		if s.Since != nil {
			line += "  since " + since(*s.Since)
		}
		srv = []string{line}
		if s.PID != 0 && s.Differs {
			srv = append(srv, "this yardr is build "+s.CLI)
		}
	}
	parts = append(parts, statusPart{"server", srv},
		statusPart{"daemon", []string{r.Daemon.line(), "login service: " + r.Daemon.Service.line()}})

	if len(r.Residents) > 0 {
		var rows [][]string
		for _, res := range r.Residents {
			row := []string{res.Name, res.State, ""}
			if res.Agent != "" {
				row[2] = "herdr agent " + res.Agent
			}
			rows = append(rows, row)
		}
		parts = append(parts, statusPart{"residents", columns(rows)})
	}

	var web []string
	if len(r.Web.Views) == 0 {
		web = []string{fmt.Sprintf("not running: %s web serve", yardr)}
	} else {
		var rows [][]string
		differs := false
		for _, v := range r.Web.Views {
			rows = append(rows, []string{v.URL, fmt.Sprintf("pid %d", v.PID), "build " + v.Build, v.reach()})
			differs = differs || v.Differs
		}
		web = columns(rows)
		if differs {
			web = append(web, "this yardr is build "+r.Server.CLI+": a view runs the build it was started on until it is started again")
		}
	}
	parts = append(parts, statusPart{"web view", append(web, "login service: "+r.Web.Service.line())})

	if h := r.Hooks; h != nil {
		var lines []string
		switch {
		case h.Listening:
			lines = []string{"the server takes webhooks on " + h.Addr}
		case h.Addr != "":
			lines = []string{"the server takes no webhooks: it does not listen on " + h.Addr}
		default:
			lines = []string{"the server takes no webhooks: it has no address for them"}
		}
		var rows [][]string
		for _, one := range h.Hooks {
			rows = append(rows, []string{one.Name, one.Source, one.URL})
		}
		parts = append(parts, statusPart{"hooks", append(lines, columns(rows)...)})
	}

	if len(r.Peers) > 0 {
		var rows [][]string
		for _, p := range r.Peers {
			rows = append(rows, []string{p.Name, or(p.URL, "it calls this yard")})
		}
		parts = append(parts, statusPart{"peers", columns(rows)})
	}

	if w := r.Work; w != nil {
		n, groups := byGroup(w.Sessions)
		line := plural(n, "session") + " running" + groups
		// A manual group's claim is a person's turn; a yard with none
		// has nothing to say about it.
		if n, groups := byGroup(w.Waiting); n == 1 {
			line += "; 1 bead waits for a person" + groups
		} else if n > 1 {
			line += fmt.Sprintf("; %d beads wait for a person%s", n, groups)
		}
		line += fmt.Sprintf("; %d held", w.Held)
		parts = append(parts, statusPart{"work", []string{line}})
	}
	if g := r.Gates; g != nil {
		parts = append(parts, statusPart{"gates", []string{fmt.Sprintf("%d running, %d waiting (limit %d)", g.Running, g.Waiting, g.Limit)}})
	}
	return parts
}

// byGroup is the sum of counts by group and the groups with theirs, in
// name order: " (builders 2, reviewers 1)", "" for none.
func byGroup(counts map[string]int) (int, string) {
	n, groups := 0, []string{}
	for g, c := range counts {
		n += c
		groups = append(groups, fmt.Sprintf("%s %d", g, c))
	}
	if n == 0 {
		return 0, ""
	}
	slices.Sort(groups)
	return n, " (" + strings.Join(groups, ", ") + ")"
}

// print is the report for a person: one block, the parts' names in a
// column of their own.
func (r *statusReport) print() {
	for _, p := range r.parts() {
		name := p.name
		for _, l := range p.lines {
			fmt.Printf("%-9s  %s\n", name, l)
			name = ""
		}
	}
	yardr := yardrAt(r.Home)
	switch {
	case !r.Server.Running:
		fmt.Println("\nresidents, hooks, peers and work: not known while the server is not running")
	case r.Faults == nil || *r.Faults == 0:
	case *r.Faults == 1:
		fmt.Printf("\n%s doctor reports 1 fault, and says what it is.\n", yardr)
	default:
		fmt.Printf("\n%s doctor reports %d faults, and says what they are.\n", yardr, *r.Faults)
	}
}
