package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A pull request and its CI, read back from the source: the pr command
// reads the pull request live, for the head no event has told, and the ci
// command reads the CI state of one commit of it. Both run as a thread
// command does: one JSON object on stdin, one on stdout, with the
// credential under the variable credential.env names and
// YARDR_SOURCE_BASE, and nothing else of the yard. Both run with the
// hook's as crew member's credential, read as for a move (issues.go):
// nothing ambient is tried.

// lookbackHead is the head commit of the pull request q names, read live
// through the source's pr command. Without the declaration, or the
// command, the setup is named.
func (d *Dispatcher) lookbackHead(ctx context.Context, h *store.Hook, source string, decl *source.Declaration, q PRQuery) (string, error) {
	if decl == nil {
		return "", fmt.Errorf("source %s is not declared", source)
	}
	if decl.Events == nil || len(decl.Events.PR) == 0 {
		return "", fmt.Errorf("source %s declares no pr command (events.pr)", decl.Name)
	}
	cred, err := d.lookbackCredential(ctx, h, decl, q.Repo+"#"+q.Number)
	if err != nil {
		return "", err
	}
	in := map[string]any{"repo": q.Repo, "number": q.Number, "url": q.URL}
	var head string
	err = d.lookbackRun(ctx, "events.pr", decl.Events.PR, decl, cred, in, func(out []byte) error {
		var o struct{ Head, Base, State string }
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {head, base, state}: %v", err)
		}
		if o.Head == "" || o.Base == "" {
			return fmt.Errorf("its answer holds no head or base (out {head, base, state})")
		}
		head = o.Head
		return nil
	})
	if err != nil {
		return "", err
	}
	return head, nil
}

// lookbackCI is the CI state of q's commit, through the source's ci
// command. Without the declaration, or the command, the setup is named.
func (d *Dispatcher) lookbackCI(ctx context.Context, h *store.Hook, source string, decl *source.Declaration, q CIQuery) (CIChecks, error) {
	var none CIChecks
	if decl == nil {
		return none, fmt.Errorf("source %s is not declared", source)
	}
	if decl.Events == nil || len(decl.Events.CI) == 0 {
		return none, fmt.Errorf("source %s declares no ci command (events.ci)", decl.Name)
	}
	cred, err := d.lookbackCredential(ctx, h, decl, q.Repo+"#"+q.Number)
	if err != nil {
		return none, err
	}
	in := map[string]any{"repo": q.Repo, "number": q.Number, "sha": q.SHA}
	var c CIChecks
	err = d.lookbackRun(ctx, "events.ci", decl.Events.CI, decl, cred, in, func(out []byte) error {
		if err := json.Unmarshal(out, &c); err != nil {
			return fmt.Errorf("its answer is no {head, base, state, required, checks}: %v", err)
		}
		if c.Head == "" || c.Base == "" {
			return fmt.Errorf("its answer holds no head or base (out {head, base, state, required, checks})")
		}
		return nil
	})
	if err != nil {
		return none, err
	}
	return c, nil
}

// lookbackCredential is the credential a look-back read runs with: the
// hook's as crew member's, read as for a move, the token a minted one
// mints with the pull request as the scope. Without a crew member to act
// as, or a credential to read, the setup is named.
func (d *Dispatcher) lookbackCredential(ctx context.Context, h *store.Hook, decl *source.Declaration, scope string) (string, error) {
	if h.As == "" {
		return "", fmt.Errorf("hook %s acts as no crew member (--as): its pull requests are read with that crew member's credential", h.Name)
	}
	if decl.Credential == nil {
		return "", fmt.Errorf("source %s declares no credential: its look-back commands are handed the variable credential.env names", decl.Name)
	}
	cred, err := d.credential(ctx, h.As, h.Source, decl, hooks.Declared{D: decl})
	if err != nil {
		return "", err
	}
	if !decl.Credential.Minted() {
		return cred, nil
	}
	inputs, err := source.UnmintRef(cred, decl.Credential.Mint)
	if err != nil || inputs == nil {
		return cred, err
	}
	src, err := d.Store.GetSource(ctx, h.Source)
	if err != nil {
		return "", err
	}
	token, _, err := source.Mint(ctx, decl, strings.Fields(src.MintCommand), inputs, scope)
	if err != nil {
		return "", err
	}
	return token, nil
}

// lookbackRun runs the look-back command argv (key, e.g. "events.ci") of
// decl: in is the JSON object on its stdin, and it is handed the
// credential cred under the variable credential.env names, with
// YARDR_SOURCE_BASE from the declaration and nothing else of the yard.
// read says in the caller's words what the answer lacks.
func (d *Dispatcher) lookbackRun(ctx context.Context, key string, argv []string, decl *source.Declaration, cred string, in map[string]any, read func(out []byte) error) error {
	full := slices.Clone(argv)
	full[0] = source.Command{Key: key, Argv: argv}.Program(d.Home, decl.Name)
	return source.Run(ctx, key+" of source "+decl.Name, full, in, decl.CommandEnv(cred), d.Home, 0, read)
}
