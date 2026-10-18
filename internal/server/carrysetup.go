package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// The server's carrying out of an approved setup (design yardr-7d4e 3.6):
// the apply, the account and the grants, once, in order, as the operator.
// The watcher takes the setup's carrying record like any other (carry.go);
// the apply step is the pack row's plan-again, holding and copy
// (carryPackApply), the account step the secret path's write of the values
// the approval handed to the server's memory, and the grants step the
// picked preset's calls, one call a write. Each step is verified back
// before the next runs; a failure stops the rest and is recorded with
// what stands and the terminal commands that finish it.

// askSetup is the setup ask of the carrying record rec with the preset its
// approval picked, as they stand on its bead: the carded pack, account and
// grantees, and the pick 1-based into the carded presets.
func (c *carrier) askSetup(ctx context.Context, rec *store.Carry) (*store.AskSetup, int, error) {
	notes, err := c.st.Notes(ctx, rec.Bead)
	if err != nil {
		return nil, 0, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != rec.Ask || r.Ask.Setup == nil {
			continue
		}
		if r.Current == nil || r.Current.Answer.Ran == nil {
			return nil, 0, fmt.Errorf("ask %d on %s holds no approved setup to carry out", rec.Ask, rec.Bead)
		}
		picked := r.Current.Answer.Option
		if picked < 1 || picked > len(r.Ask.Setup.Presets) {
			return nil, 0, fmt.Errorf("ask %d on %s holds no preset pick to carry out", rec.Ask, rec.Bead)
		}
		return r.Ask.Setup, picked, nil
	}
	return nil, 0, fmt.Errorf("ask %d on %s holds no setup ask to carry out", rec.Ask, rec.Bead)
}

// carrySetup carries out the approved setup setup with the preset picked:
// the apply, the account and the grants, each verified back. A failure of
// any step stops the rest and is recorded on the ask (FailCarry).
func (c *carrier) carrySetup(ctx context.Context, rec *store.Carry, setup *store.AskSetup, picked int) (string, string) {
	lines, failure := c.carrySetupApply(ctx, rec, setup)
	if failure != "" {
		return "", failure
	}
	file, failure := c.carrySetupAccount(ctx, rec, setup, picked)
	if failure != "" {
		return "", failure
	}
	lines = append(lines, "the account "+strings.Join(setup.Account, " and ")+" is set in "+file)
	grants, failure := c.carrySetupGrants(ctx, rec, setup, picked, file)
	if failure != "" {
		return "", failure
	}
	return strings.Join(append(lines, grants...), "\n"), ""
}

// carrySetupApply is a setup's apply step: the pack row's plan-again,
// holding and copy over the carded bytes, as the operator. The apply is
// worded beside the account and grants: the credential's owed lines are
// the account step's to settle, so only what else the apply leaves owed
// stands in them.
func (c *carrier) carrySetupApply(ctx context.Context, rec *store.Carry, setup *store.AskSetup) ([]string, string) {
	run := &store.AskRun{
		Argv:    []string{"pack", "apply", setup.Pack},
		Pack:    &store.AskPack{Name: setup.Pack, Kind: setup.Kind, Label: setup.Label},
		Carried: setup.Carried,
		Seen:    setup.Seen,
	}
	res, plan, failure := c.carryPackApply(ctx, rec, run)
	if failure != "" {
		return nil, failure
	}
	return packResultLines(res, plan, false), ""
}

// carrySetupAccount is a setup's account step: the secret path's write of
// the values the approval handed to the server's memory, into the first
// credentialed source's file, verified back from the file. It answers the
// file the account stands in.
//
// Where the memory holds no values the server stopped with the ask in
// hand: a file that holds every name given is the account standing — the
// stop came after its write, whose take was once — and the carrying out
// goes on to the grants. Anything else is the value lost to a stop before
// the write, recorded failed naming the account asked again.
func (c *carrier) carrySetupAccount(ctx context.Context, rec *store.Carry, setup *store.AskSetup, picked int) (string, string) {
	names := strings.Join(setup.Account, " and ")
	src, err := setupAccountSource(setup)
	if err != nil {
		return "", failWords(fmt.Sprintf("pack %s is applied, but %v", setup.Pack, err),
			"yardr pack apply "+setup.Pack)
	}
	preset := setup.Presets[picked-1]
	values, ok := c.st.TakeSetupSecret(rec.Bead, rec.Ask)
	if !ok {
		if file, stands := c.setupAccountStands(ctx, src, setup.Account); stands {
			return file, ""
		}
		return "", fmt.Sprintf("pack %s is applied, but the account %s the approval typed is lost: the server stopped before writing it, and a typed value is kept in no note and no file. Ask for it again (%s), or type it with %s in a terminal, then grant the preset's calls: %s.",
			setup.Pack, names, setupSecretAsk(rec.Bead, src), setupKeyCommand(src, setup.Account),
			strings.Join(setupGrantCommands(setup, preset), "; "))
	}
	keyed, err := c.st.KeySource(ctx, src, values, rec.Actor)
	if err != nil {
		var lost *store.KeyNotRecorded
		if errors.As(err, &lost) {
			return "", fmt.Sprintf("pack %s is applied, but %v Finish it with %s in a terminal, then grant the preset's calls: %s.",
				setup.Pack, err, setupKeyCommand(src, setup.Account),
				strings.Join(setupGrantCommands(setup, preset), "; "))
		}
		return "", fmt.Sprintf("pack %s is applied, but the account %s was not written to %s: %v. Finish it with %s in a terminal, then grant the preset's calls: %s.",
			setup.Pack, names, setup.AccountFile, err, setupKeyCommand(src, setup.Account),
			strings.Join(setupGrantCommands(setup, preset), "; "))
	}
	// What stands is verified, never trusted: every name given is read
	// back from the file the write reports.
	f, err := config.ReadEnvFile(keyed.File)
	if err != nil {
		return "", fmt.Sprintf("pack %s is applied, but the account %s does not stand in %s after the write: %v. Finish it with %s in a terminal, then grant the preset's calls: %s.",
			setup.Pack, names, keyed.File, err, setupKeyCommand(src, setup.Account),
			strings.Join(setupGrantCommands(setup, preset), "; "))
	}
	for _, n := range setup.Account {
		if source.Lookup(f.Env, n) == "" {
			return "", fmt.Sprintf("pack %s is applied, but the account %s does not stand in %s after the write: %s is not set there. Finish it with %s in a terminal, then grant the preset's calls: %s.",
				setup.Pack, names, keyed.File, n, setupKeyCommand(src, setup.Account),
				strings.Join(setupGrantCommands(setup, preset), "; "))
		}
	}
	return keyed.File, ""
}

// setupAccountSource is the source a setup's account step writes: the first
// source the pack carries, in its declaration order, whose declaration
// names a credential — the source the card's account file is.
func setupAccountSource(setup *store.AskSetup) (string, error) {
	for i := range setup.Carried {
		d, err := source.Parse("pack source "+setup.Carried[i].Name, []byte(setup.Carried[i].Text))
		if err != nil {
			return "", err
		}
		if d.Credential != nil {
			return d.Name, nil
		}
	}
	return "", fmt.Errorf("pack %s names no credential, so no account is owed", setup.Pack)
}

// setupAccountStands reports whether the account step already stands for
// src: its file holds every name the card asked for.
func (c *carrier) setupAccountStands(ctx context.Context, src string, names []string) (string, bool) {
	s, err := c.st.GetSource(ctx, src)
	if err != nil {
		return "", false
	}
	file := s.EnvFile
	if file == "" {
		file = source.EnvFile(c.home, src)
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return "", false
	}
	for _, n := range names {
		if source.Lookup(f.Env, n) == "" {
			return "", false
		}
	}
	return file, true
}

// carrySetupGrants is a setup's grants step: the picked preset's calls of
// each source to each grantee, one call a write as the operator, each
// verified back. A call a grantee holds already is run past, as the grant
// row runs past grants held since. A failure stops the rest and names what
// stands with the commands that grant what is still missing.
func (c *carrier) carrySetupGrants(ctx context.Context, rec *store.Carry, setup *store.AskSetup, picked int, file string) ([]string, string) {
	preset := setup.Presets[picked-1]
	var lines []string
	for _, t := range setup.To {
		for _, g := range preset.Grants {
			line, failure := c.carrySetupGrant(ctx, rec, setup, preset, file, g, t)
			if failure != "" {
				return nil, failure
			}
			lines = append(lines, line)
		}
	}
	// What stands is verified, never trusted: every planned call is read
	// back from the yard.
	if missing := c.setupMissingGrants(ctx, setup, preset); len(missing) > 0 {
		stands := fmt.Sprintf("pack %s is applied and the account %s is set in %s",
			setup.Pack, strings.Join(setup.Account, " and "), file)
		return nil, fmt.Sprintf("%s, but %s no longer stand after the granting. Finish it with %s in a terminal, or ask again if it is still wanted.",
			stands, strings.Join(missing, ", "), strings.Join(setupGrantCommands(setup, preset), "; "))
	}
	return lines, ""
}

// carrySetupGrant grants the preset's calls of one source to one grantee,
// running past what is held already, and words what the grantee may call.
func (c *carrier) carrySetupGrant(ctx context.Context, rec *store.Carry, setup *store.AskSetup, preset store.AskPreset, file string, g store.AskPresetGrant, t store.Grantee) (string, string) {
	src, err := c.st.GetSource(ctx, g.Source)
	if err != nil {
		return "", c.setupGrantFailure(ctx, setup, preset, file,
			fmt.Sprintf("granting %s to %s failed: the source %s is not in the yard: %v", grantWords(g), granteeWords(t), g.Source, err))
	}
	d, err := src.Declaration()
	if err != nil {
		return "", c.setupGrantFailure(ctx, setup, preset, file,
			fmt.Sprintf("granting %s to %s failed: %v", grantWords(g), granteeWords(t), err))
	}
	held := store.SourceGrant{Grantee: t}
	if grants, err := c.st.SourceGrants(ctx, g.Source); err != nil {
		return "", c.setupGrantFailure(ctx, setup, preset, file,
			fmt.Sprintf("granting %s to %s failed: what %s holds is not read: %v", grantWords(g), granteeWords(t), granteeWords(t), err))
	} else {
		for _, h := range grants {
			if h.Grantee == t && h.As == "" {
				held = h
			}
		}
	}
	for _, call := range g.Calls {
		// A call held already is run past, as the grant row runs past
		// grants held since: by its name, or a read call through a
		// grant of read.
		if call == store.GrantRead && slices.Contains(held.Calls, store.GrantRead) {
			continue
		}
		if cc, ok := d.Call(call); ok && held.Covers(cc, "") {
			continue
		}
		given, err := c.st.GrantSource(ctx, g.Source, call, t, "", rec.Actor)
		if err != nil {
			return "", c.setupGrantFailure(ctx, setup, preset, file,
				fmt.Sprintf("granting %s of the source %s to %s failed: %v", call, g.Source, granteeWords(t), err))
		}
		held = *given
	}
	return fmt.Sprintf("%s may call %s: %s", held.Holder(), g.Source, held.Words(d)), ""
}

// setupGrantFailure words a grants step that stopped on why: what stands —
// the apply and the account — with the commands that grant every call of
// the preset still missing, read back from the yard.
func (c *carrier) setupGrantFailure(ctx context.Context, setup *store.AskSetup, preset store.AskPreset, file, why string) string {
	stands := fmt.Sprintf("pack %s is applied and the account %s is set in %s",
		setup.Pack, strings.Join(setup.Account, " and "), file)
	var missing []string
	for _, t := range setup.To {
		for _, g := range preset.Grants {
			for _, call := range g.Calls {
				if !c.setupGrantHeld(ctx, g.Source, call, t) {
					missing = append(missing, setupGrantCommand(g.Source, call, t))
				}
			}
		}
	}
	if len(missing) == 0 {
		return fmt.Sprintf("%s, but %s. Nothing further is missing: ask again if it is still wanted.", stands, why)
	}
	return fmt.Sprintf("%s, but %s. Finish it with %s in a terminal, or ask again if it is still wanted.",
		stands, why, strings.Join(missing, "; "))
}

// setupMissingGrants is every planned call of the preset no grantee holds,
// worded for the failure that names them.
func (c *carrier) setupMissingGrants(ctx context.Context, setup *store.AskSetup, preset store.AskPreset) []string {
	var missing []string
	for _, t := range setup.To {
		for _, g := range preset.Grants {
			for _, call := range g.Calls {
				if !c.setupGrantHeld(ctx, g.Source, call, t) {
					missing = append(missing, fmt.Sprintf("%s of the source %s to %s", call, g.Source, granteeWords(t)))
				}
			}
		}
	}
	return missing
}

// setupGrantHeld reports whether the grantee holds the call of the source:
// by its name, or a read call through a grant of read. What cannot be read
// is not held: the failure then names it missing.
func (c *carrier) setupGrantHeld(ctx context.Context, src, call string, t store.Grantee) bool {
	s, err := c.st.GetSource(ctx, src)
	if err != nil {
		return false
	}
	d, err := s.Declaration()
	if err != nil {
		return false
	}
	grants, err := c.st.SourceGrants(ctx, src)
	if err != nil {
		return false
	}
	for _, g := range grants {
		if g.Grantee != t || g.As != "" {
			continue
		}
		if slices.Contains(g.Calls, call) {
			return true
		}
		if cc, ok := d.Call(call); ok && g.Covers(cc, "") {
			return true
		}
	}
	return false
}

// grantWords is the preset's calls of one source, in words.
func grantWords(g store.AskPresetGrant) string {
	return strings.Join(g.Calls, ", ") + " of the source " + g.Source
}

// granteeWords is who a grant is for, in words.
func granteeWords(t store.Grantee) string {
	if t.Crew != "" {
		return "crew " + t.Crew
	}
	return "group " + t.Group
}

// setupGranteeFlag is who a grant is for, as a grant command names them.
func setupGranteeFlag(t store.Grantee) string {
	if t.Crew != "" {
		return "--crew " + t.Crew
	}
	return "--group " + t.Group
}

// setupGrantCommand is the terminal command granting one call.
func setupGrantCommand(src, call string, t store.Grantee) string {
	return fmt.Sprintf("yardr source grant %s %s %s", src, call, setupGranteeFlag(t))
}

// setupGrantCommands are the terminal commands granting the preset whole.
func setupGrantCommands(setup *store.AskSetup, preset store.AskPreset) []string {
	var cmds []string
	for _, t := range setup.To {
		for _, g := range preset.Grants {
			for _, call := range g.Calls {
				cmds = append(cmds, setupGrantCommand(g.Source, call, t))
			}
		}
	}
	return cmds
}

// setupKeyCommand is the terminal command typing the account.
func setupKeyCommand(src string, names []string) string {
	return fmt.Sprintf("yardr source key %s %s", src, strings.Join(names, " "))
}

// setupSecretAsk is the secret ask that gives the account again, in words.
func setupSecretAsk(bead, src string) string {
	return fmt.Sprintf("yardr bead ask %s \"<question>\" --secret %s --context \"<where to get it>\"", bead, src)
}
