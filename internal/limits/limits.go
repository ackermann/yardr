// Package limits says what a harness kind's subscription still carries: the
// windows of its usage limit, how far each is used and when it resets. Ask
// is the whole of it for a caller, which decides that it wants to know and
// what to do with the answer, and does not know how the answer is got.
//
// yardr holds no provider logic. The answer comes from a source of the yard
// whose file has a limits key (internal/source): a command of the
// operator's, which is run for one kind and prints the report as JSON. That
// command is the one provider here; a second way to an answer (a declared
// HTTPS call, say) would be one more.
//
// Asking is not free and not harmless: a provider may be slow, may be
// bounded itself in how often it is asked, and may renew a stored login as
// it reads it. So nothing here asks by itself, remembers an answer or
// repeats an ask: every call of Ask is one run of the command, and whoever
// calls says when. Nothing is written by this package: no log, no event.
package limits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Timeout bounds one ask. It is long on purpose: a provider that renews a
// login writes the new one back where the harness keeps it, and a kill
// between the two would leave the harness with a login that no longer
// works. So a command that answers at all is never cut short.
var Timeout = source.Timeout

// earliest is the first reset time that is taken as one. A provider that
// knows no reset may print the zero time, year 1, in place of none.
var earliest = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Report is what a provider said of one harness kind, as the yard keeps it.
type Report struct {
	// Source is the source of the yard that answered, Provider the name the
	// provider gave itself.
	Source   string    `json:"source"`
	Provider string    `json:"provider"`
	AskedAt  time.Time `json:"asked_at"`
	Plan     string    `json:"plan,omitempty"`
	Windows  []Window  `json:"windows"`
}

// Window is one window of a usage limit: five hours, a week. What the
// provider did not say is nil.
type Window struct {
	// Label is the provider's name for the window, for a person: "5h".
	Label       string     `json:"label"`
	UsedPercent *float64   `json:"used_percent,omitempty"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	Used        *int64     `json:"used,omitempty"`
	Limit       *int64     `json:"limit,omitempty"`
}

// UsedUp reports whether nothing is left of the window: the one thing the
// yard derives from a report itself.
func (w Window) UsedUp() bool {
	if w.UsedPercent != nil && *w.UsedPercent >= 100 {
		return true
	}
	return w.Used != nil && w.Limit != nil && *w.Limit > 0 && *w.Used >= *w.Limit
}

// UsedUp is the window an agent of the kind waits for: of those that are
// used up the one that resets last, since the agent cannot work before it
// opens. One whose reset is not known counts as the earliest: a time that
// is known says more. nil when no window is used up.
func (r *Report) UsedUp() *Window {
	var out *Window
	for i := range r.Windows {
		w := &r.Windows[i]
		if !w.UsedUp() {
			continue
		}
		if out == nil || w.ResetsAt != nil && (out.ResetsAt == nil || w.ResetsAt.After(*out.ResetsAt)) {
			out = w
		}
	}
	return out
}

// NoSource is an ask that was not made because no source of the yard says
// the limits of the kind: nothing is known, as in a yard that never heard
// of limits. It is no failure of a provider.
type NoSource struct{ Kind string }

func (e *NoSource) Error() string {
	return fmt.Sprintf("no source of this yard says the limits of %s (a source with a limits key whose kinds list it: yardr source --help)", e.Kind)
}

// Store is what of the yard's store an ask reads.
type Store interface {
	Sources(ctx context.Context) ([]store.Source, error)
}

// provider is one way the limits of a kind are learned.
type provider interface {
	// ask returns what the provider says of kind. An error is no answer,
	// and says why in a line.
	ask(ctx context.Context, kind string) (*Report, error)
}

// Answerer is a source of the yard that says limits, and the kinds it
// answers for.
type Answerer struct {
	Source string
	Limits *source.Limits
	// Home is the yard's home, where the command runs.
	Home string
}

// Answerers are the sources of the yard that say limits, by name. home is
// the yard's home. A source whose file this build does not read answers
// for nothing.
func Answerers(ctx context.Context, st Store, home string) ([]Answerer, error) {
	srcs, err := st.Sources(ctx)
	if err != nil {
		return nil, err
	}
	var out []Answerer
	for _, src := range srcs {
		if d, err := src.Declaration(); err == nil && d.Limits != nil {
			out = append(out, Answerer{Source: d.Name, Limits: d.Limits, Home: home})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// Ask asks the source of the yard that answers for kind what the kind's
// subscription still carries, now: one run of its command. home is the
// yard's home. The error is a *NoSource when no source maps the kind, and
// otherwise why the provider gave no answer.
func Ask(ctx context.Context, st Store, home, kind string) (*Report, error) {
	as, err := Answerers(ctx, st, home)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		if _, ok := a.Limits.Kinds[kind]; ok {
			return a.Ask(ctx, kind)
		}
	}
	return nil, &NoSource{Kind: kind}
}

// Ask asks this source for kind: one run of its command.
func (a Answerer) Ask(ctx context.Context, kind string) (*Report, error) {
	var p provider = command(a)
	return p.ask(ctx, kind)
}

// Missing reports why the source's program cannot be found by this process,
// without running it; nil when it can. Another process of the yard (the
// daemon) has a PATH of its own, which is why a file names the program by
// its whole path, or a file it brings, run from its copy.
func (a Answerer) Missing() error {
	if len(a.Limits.Command) == 0 {
		return nil
	}
	cmd := source.Command{Key: "limits.command", Argv: a.Limits.Command}
	argv := slices.Clone(a.Limits.Command)
	argv[0] = cmd.Program(a.Home, a.Source)
	return config.Command{What: "limits command " + a.Limits.Command[0], Argv: argv, Dir: a.Home}.Missing()
}

// command answers by running the command the source's file declares.
type command Answerer

// ask runs the command for kind: without a shell, its stdin closed, in the
// yard's home, with the caller's environment and nothing added, in a process
// group of its own that is killed whole at Timeout. A program with no / at
// its start is the file the declaration brought, run from its copy.
func (c command) ask(ctx context.Context, kind string) (*Report, error) {
	argv, ok := c.Limits.Argv(kind)
	if !ok {
		return nil, &NoSource{Kind: kind}
	}
	what := "limits command " + strings.Join(argv, " ")
	argv[0] = source.Command{Key: "limits.command", Argv: argv}.Program(c.Home, c.Source)
	asked := time.Now()
	var r *Report
	err := source.Run(ctx, what, argv, nil, nil, c.Home, Timeout, func(out []byte) error {
		var err error
		r, err = read(out)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", c.Source, err)
	}
	r.Source, r.AskedAt = c.Source, asked
	return r, nil
}

// read reads what a command printed: one JSON object with the provider's
// name and its windows. Keys it does not know are the provider's own and
// are passed over; a key it knows with another type is an answer the yard
// would misread, and is none.
func read(out []byte) (*Report, error) {
	var raw struct {
		Provider *string `json:"provider"`
		Plan     string  `json:"plan"`
		Windows  *[]struct {
			Label       *string  `json:"label"`
			UsedPercent *float64 `json:"used_percent"`
			ResetsAt    *string  `json:"resets_at"`
			Used        *int64   `json:"used"`
			Limit       *int64   `json:"limit"`
		} `json:"windows"`
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, errors.New("it printed nothing: want one JSON object with provider and windows")
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("what it printed is not the JSON object the yard reads (provider, windows): %s", jsonError(err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("it printed more than one JSON object")
	}
	switch {
	case raw.Provider == nil || *raw.Provider == "":
		return nil, errors.New("its answer has no provider: want the provider's name as a string")
	case raw.Windows == nil:
		return nil, errors.New("its answer has no windows: want a list, empty when the provider knows of none")
	}
	r := &Report{Provider: *raw.Provider, Plan: raw.Plan, Windows: []Window{}}
	for i, w := range *raw.Windows {
		if w.Label == nil || *w.Label == "" {
			return nil, fmt.Errorf("window %d of its answer has no label", i+1)
		}
		win := Window{Label: *w.Label, UsedPercent: w.UsedPercent, Used: w.Used, Limit: w.Limit}
		if w.ResetsAt != nil {
			t, err := time.Parse(time.RFC3339, *w.ResetsAt)
			if err != nil {
				return nil, fmt.Errorf("window %s of its answer resets at %q, which is no time as RFC 3339 writes one (2026-10-07T00:02:32Z)", *w.Label, *w.ResetsAt)
			}
			if !t.Before(earliest) {
				win.ResetsAt = &t
			}
		}
		r.Windows = append(r.Windows, win)
	}
	return r, nil
}

// jsonError is why JSON was not read, without what was printed: a type
// error names the key, a syntax error where it is.
func jsonError(err error) string {
	var typ *json.UnmarshalTypeError
	var syn *json.SyntaxError
	switch {
	case errors.As(err, &typ):
		return fmt.Sprintf("%s is %s, not %s", typ.Field, typ.Value, typ.Type)
	case errors.As(err, &syn):
		return fmt.Sprintf("no JSON at byte %d", syn.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "the JSON ends early"
	}
	return "no JSON object"
}

// Clock is a time as a person reads it on the day of now: "00:02", and with
// the day for a time that is not today, "Sat 10 Oct 09:02". Both are local.
func Clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

// Span is a length of time in its two largest units: 42m, 3h 5m, 3d 9h.
func Span(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case mins > 0:
		return fmt.Sprintf("%dm", mins)
	}
	return "under a minute"
}
