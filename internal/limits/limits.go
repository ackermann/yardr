// Package limits says what a harness kind's subscription still carries: the
// windows of its usage limit, how far each is used and when it resets. Ask
// and AskFor are the whole of it for a caller, which decides that it wants
// to know and what to do with the answer, and does not know how the answer
// is got.
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
// repeats an ask: every call of either is one run of the command, and whoever
// calls says when. Nothing is written by this package: no log, no event.
package limits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/shared"
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
	Label string `json:"label"`
	// TargetPercent is the share of the window that has passed, for a
	// route that compares the pace instead of a number. The source is
	// the one that knows the window, so the yard never works it out.
	TargetPercent *float64   `json:"target_percent,omitempty"`
	UsedPercent   *float64   `json:"used_percent,omitempty"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	Used          *int64     `json:"used,omitempty"`
	Limit         *int64     `json:"limit,omitempty"`
}

// UsedUp reports whether nothing is left of the window.
func (w Window) UsedUp() bool {
	if w.UsedPercent != nil && *w.UsedPercent >= 100 {
		return true
	}
	return w.Used != nil && w.Limit != nil && *w.Limit > 0 && *w.Used >= *w.Limit
}

// OverTarget reports whether the window is burned past the share of it
// that has passed: both shares are known and used is above target. A
// provider that counts instead reads the same in two numbers, as UsedUp
// does.
func (w Window) OverTarget() bool {
	if w.TargetPercent == nil {
		return false
	}
	if w.UsedPercent != nil {
		return *w.UsedPercent > *w.TargetPercent
	}
	return w.Used != nil && w.Limit != nil && *w.Limit > 0 &&
		float64(*w.Used)*100/float64(*w.Limit) > *w.TargetPercent
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
// the limits asked for: nothing is known, as in a yard that never heard of
// limits. It is no failure of a provider. Kind is the kind no source lists;
// Provider the provider no source's kinds table names, for an ask with the
// provider setting; Source the source asked for itself that answers for
// nothing of its own.
type NoSource struct {
	Kind     string
	Provider string
	Source   string
}

func (e *NoSource) Error() string {
	switch {
	case e.Provider != "":
		return fmt.Sprintf("no source of this yard answers for provider %s (a source with a limits key whose kinds name it)", e.Provider)
	case e.Source != "":
		return fmt.Sprintf("no source named %s says limits of itself (a source with a limits key and no kinds: yardr source --help)", e.Source)
	default:
		return fmt.Sprintf("no source of this yard says the limits of %s (a source with a limits key whose kinds list it: yardr source --help)", e.Kind)
	}
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
	// Env, when set, joins the command's environment over the caller's:
	// the env_file entries of the group whose login is read, so the
	// provider reads that login instead of the machine's. nil runs with
	// the caller's environment, as before.
	Env []string
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

// AskFor asks what the subscription still carries of an agent of kind with
// the provider setting: one run of the answering source's command. home is
// the yard's home. With the setting, the first source by name whose kinds
// table names it is asked, even where no table lists the kind; without it,
// the kind resolves through the kinds table exactly as Ask does. The error
// is a *NoSource naming the provider where no source names it, and
// otherwise why the provider gave no answer.
func AskFor(ctx context.Context, st Store, home, kind, provider string) (*Report, error) {
	as, err := Answerers(ctx, st, home)
	if err != nil {
		return nil, err
	}
	a, name, ok := Resolve(as, kind, provider)
	if !ok {
		if provider != "" {
			return nil, &NoSource{Provider: provider}
		}
		return nil, &NoSource{Kind: kind}
	}
	if provider != "" {
		return a.AskName(ctx, name)
	}
	return a.Ask(ctx, kind)
}

// AskSource asks the source named name for what it says of itself: one run
// of its command. home is the yard's home. Only a source whose limits key
// names no kinds answers; the error is a *NoSource naming the source where
// none does, and otherwise why the source gave no answer.
func AskSource(ctx context.Context, st Store, home, name string) (*Report, error) {
	as, err := Answerers(ctx, st, home)
	if err != nil {
		return nil, err
	}
	a, ok := SelfAnswerer(as, name)
	if !ok {
		return nil, &NoSource{Source: name}
	}
	return a.AskSelf(ctx)
}

// SelfAnswerer is the source named name where it answers for itself: one
// whose limits key names no kinds.
func SelfAnswerer(as []Answerer, name string) (Answerer, bool) {
	for _, a := range as {
		if a.Source == name && a.Limits.Self() {
			return a, true
		}
	}
	return Answerer{}, false
}

// Resolve is the provider an agent of kind with the provider setting burns,
// and the source that answers for it, without asking. With the setting, the
// provider is the setting, answered by the first source by name whose kinds
// table names it among its values; without it, the kinds table's mapping of
// the kind through the first source by name that lists it, as Ask picks. ok
// is false when no source answers: name is then the setting where one is
// set, else "".
func Resolve(as []Answerer, kind, provider string) (a Answerer, name string, ok bool) {
	if provider != "" {
		for _, a := range as {
			if a.Provides(provider) {
				return a, provider, true
			}
		}
		return Answerer{}, provider, false
	}
	for _, a := range as {
		if mapped, listed := a.Limits.Kinds[kind]; listed {
			return a, mapped, true
		}
	}
	return Answerer{}, "", false
}

// Ask asks this source for kind: one run of its command.
func (a Answerer) Ask(ctx context.Context, kind string) (*Report, error) {
	var p provider = command(a)
	return p.ask(ctx, kind)
}

// Provides reports whether the source answers for the provider named name:
// the name is among its kinds table's values.
func (a Answerer) Provides(name string) bool {
	for _, v := range a.Limits.Kinds {
		if v == name {
			return true
		}
	}
	return false
}

// AskName asks this source for the provider named name: one run of its
// command with the name substituted directly.
func (a Answerer) AskName(ctx context.Context, name string) (*Report, error) {
	return command(a).askName(ctx, name)
}

// AskSelf asks this source for what it says of itself: one run of its
// command as its file has it, for a source whose limits key names no kinds.
func (a Answerer) AskSelf(ctx context.Context) (*Report, error) {
	return command(a).run(ctx, slices.Clone(a.Limits.Command))
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

// ask runs the command for kind.
func (c command) ask(ctx context.Context, kind string) (*Report, error) {
	argv, ok := c.Limits.Argv(kind)
	if !ok {
		return nil, &NoSource{Kind: kind}
	}
	return c.run(ctx, argv)
}

// askName runs the command for the provider named name, substituted directly.
func (c command) askName(ctx context.Context, name string) (*Report, error) {
	return c.run(ctx, c.Limits.ArgvName(name))
}

// run runs argv: without a shell, its stdin closed, in the yard's home, with
// the caller's environment and nothing added — or with Env joined over it
// where the answerer names a group's — in a process group of its own that
// is killed whole at Timeout. A program with no / at its start is the file
// the declaration brought, run from its copy.
func (c command) run(ctx context.Context, argv []string) (*Report, error) {
	what := "limits command " + strings.Join(argv, " ")
	argv[0] = source.Command{Key: "limits.command", Argv: argv}.Program(c.Home, c.Source)
	var env []string
	if c.Env != nil {
		env = append(os.Environ(), c.Env...)
	}
	asked := time.Now()
	var r *Report
	err := source.Run(ctx, what, argv, nil, env, c.Home, Timeout, func(out []byte) error {
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
			Label         *string  `json:"label"`
			TargetPercent *float64 `json:"target_percent"`
			UsedPercent   *float64 `json:"used_percent"`
			ResetsAt      *string  `json:"resets_at"`
			Used          *int64   `json:"used"`
			Limit         *int64   `json:"limit"`
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
		if w.TargetPercent != nil && !(*w.TargetPercent >= 0 && *w.TargetPercent <= 100) {
			return nil, fmt.Errorf("window %s of its answer has target_percent %v, which is no share of the window from 0 to 100", *w.Label, *w.TargetPercent)
		}
		win := Window{Label: *w.Label, TargetPercent: w.TargetPercent, UsedPercent: w.UsedPercent, Used: w.Used, Limit: w.Limit}
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
// The one implementation is shared's.
func Span(d time.Duration) string { return shared.Span(d) }
