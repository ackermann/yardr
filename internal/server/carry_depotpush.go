package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// The server's carrying out of an approved depot-push card
// (yardr-zbcp): both halves, idempotently, as the operator who
// approved. A half that stands is written no second time, so a carry
// taken up again after a failure between the halves completes the
// depot instead of refusing it. No failure here names a terminal: a
// no-terminal operator files the card again.

func isDepotPushCarry(argv []string) bool { return store.IsDepotPushCarry(argv) }

// carryDepotPushClaimed runs a claimed depot-push carrying out under
// the apply lock, as carryClaimed does for an apply.
func (c *carrier) carryDepotPushClaimed(ctx context.Context, rec *store.Carry) (bool, error) {
	release, err := config.AcquireApplyLock(c.home)
	if err != nil {
		return true, fmt.Errorf("carrying out ask %d on %s: the apply lock: %w", rec.Ask, rec.Bead, err)
	}
	defer release()
	result, failure := c.carryDepotPush(ctx, rec)
	if failure != "" {
		if err := c.st.FailCarry(ctx, rec.Bead, rec.Ask, c.boot, failure); err != nil {
			return true, err
		}
		c.log.Warn("carrying out a depot-push ask failed", "bead", rec.Bead, "ask", rec.Ask, "failure", failure)
		return true, nil
	}
	if err := c.st.CompleteCarry(ctx, rec.Bead, rec.Ask, c.boot, result); err != nil {
		return true, err
	}
	c.log.Info("carried out a depot-push ask", "bead", rec.Bead, "ask", rec.Ask, "depot", rec.Argv[1])
	return true, nil
}

// pushFail words a depot-push carrying failure: what happened, and
// that filing the card again completes it. It names no terminal.
func pushFail(format string, args ...any) string {
	return fmt.Sprintf(format, args...) + " File the card again to complete it."
}

func gitConfig(path string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", path, "config"}, args...)...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git config %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func gitConfigGetAll(path, key string, local bool) ([]string, bool) {
	args := []string{}
	if local {
		args = append(args, "--local")
	}
	args = append(args, "--null", "--get-all", key)
	cmd := exec.Command("git", append([]string{"-C", path, "config"}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []string{}, true
		}
		return nil, false
	}
	raw := out.String()
	if raw == "" {
		return []string{}, true
	}
	values := strings.Split(raw, "\x00")
	if values[len(values)-1] == "" {
		values = values[:len(values)-1]
	}
	return values, true
}

// carryDepotPush makes both halves of the card rec.Argv names, each
// verified back before the next is counted done. The push source,
// its host and its helper are read from the declaration whose host
// the depot's origin is on, as the store read them at approval.
func (c *carrier) carryDepotPush(ctx context.Context, rec *store.Carry) (string, string) {
	off := rec.Argv[0] == "depot-push-off"
	depotName := rec.Argv[1]
	depot, err := c.st.GetDepot(ctx, depotName)
	if err != nil {
		return "", pushFail("depot %s is not in the yard: %v", depotName, err)
	}
	if depot.Path == "" {
		return "", pushFail("depot %s has no repository here", depotName)
	}
	groupName := depotName + "-assembly"
	sources, err := c.st.Sources(ctx)
	if err != nil {
		return "", pushFail("the yard's sources are not read: %v", err)
	}
	ps, found, err := store.DepotPushSourceFor(sources, c.home, store.PushOrigin(depot.Path))
	if err != nil {
		return "", pushFail("the push source for depot %s could not be checked", depotName)
	}
	if !found {
		return "", pushFail("depot %s: its origin is not https on a host a source gives git a credential for", depotName)
	}
	if rec.DepotPushSource == "" || rec.DepotPushSource != store.DepotPushSourceFingerprint(ps) {
		return "", pushFail("the push source for depot %s changed after approval", depotName)
	}
	helper, helperKey := ps.Helper, ps.HelperKey

	if off {
		localHelpers, ok := gitConfigGetAll(depot.Path, helperKey, true)
		if !ok {
			return "", pushFail("depot %s's git config could not be read", depotName)
		}
		localPack := false
		for _, value := range localHelpers {
			localPack = localPack || store.IsPackHelper(value, helper)
		}
		if localPack && len(localHelpers) != 1 {
			return "", pushFail("more than one credential helper line stands in depot %s's git config, so the pack's line cannot be removed alone", depotName)
		}
		// The exact reverse of the on carry, each half idempotent.
		if _, err := c.st.SetAssemblyCmd(ctx, depotName, true, rec.Actor); err != nil {
			return "", pushFail("the cmd of %s was not changed: %v", groupName, err)
		}
		if len(localHelpers) == 1 {
			// Only the pack's line is the on card's to take out: another
			// helper standing locally was never this card's to remove.
			if store.IsPackHelper(localHelpers[0], helper) {
				cmd := exec.Command("git", "-C", depot.Path, "config", "--unset", helperKey)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					return "", pushFail("the helper line in depot %s's git config was not removed: %v: %s", depotName, err, strings.TrimSpace(stderr.String()))
				}
			}
		}
		// Verified back: neither half stands.
		g, err := c.st.GetGroup(ctx, groupName)
		if err != nil {
			return "", pushFail("group %s is not in the yard: %v", groupName, err)
		}
		if cmd, _ := g.Config["cmd"].(string); strings.Contains(cmd, "YARDR_PUSH=") {
			if v, ok := pushOf(cmd); ok && v == "origin" {
				return "", pushFail("YARDR_PUSH=origin still stands in %s's cmd", groupName)
			}
		}
		localHelpers, ok = gitConfigGetAll(depot.Path, helperKey, true)
		if !ok {
			return "", pushFail("depot %s's git config could not be read after removing the helper", depotName)
		}
		for _, value := range localHelpers {
			if store.IsPackHelper(value, helper) {
				return "", pushFail("the pack's helper still stands in depot %s's git config", depotName)
			}
		}
		return fmt.Sprintf("push to %s is off for depot %s: YARDR_PUSH is out of %s's cmd and the pack's helper line is out of its git config", ps.Host, depotName, groupName), ""
	}

	if !store.PushTokenStanding(c.home, ps) {
		return "", pushFail("no %s stands for the source %s, so its credential helper has no token", ps.TokenName, ps.Name)
	}

	// On: where a helper already answers, verify it is the pack's
	// answering with the source's token and write nothing for that half.
	effectiveHelpers, ok := store.PushHelpers(depot.Path, ps.Host)
	if !ok {
		return "", pushFail("depot %s's git config could not be read", depotName)
	}
	gitDone := false
	if len(effectiveHelpers) > 1 {
		return "", pushFail("more than one credential helper answers for https://%s in depot %s, so nothing was written over them", ps.Host, depotName)
	}
	if len(effectiveHelpers) == 1 {
		if !store.IsPackHelper(effectiveHelpers[0], helper) {
			// The helper's value is not repeated: an inline helper may
			// carry a token, and this text is kept on the bead.
			return "", pushFail("a credential helper already answers for https://%s in depot %s, and it is not the pack's helper, so nothing was written over it", ps.Host, depotName)
		}
		gitDone = true
	}
	if _, err := c.st.SetAssemblyCmd(ctx, depotName, false, rec.Actor); err != nil {
		return "", pushFail("the cmd of %s was not changed: %v", groupName, err)
	}
	if !gitDone {
		if _, err := gitConfig(depot.Path, helperKey, helper); err != nil {
			return "", pushFail("the helper line in depot %s's git config was not written: %v", depotName, err)
		}
	}
	// Verified back: both halves stand, and no other depot was touched
	// (each write named this depot alone).
	g, err := c.st.GetGroup(ctx, groupName)
	if err != nil {
		return "", pushFail("group %s is not in the yard: %v", groupName, err)
	}
	cmd, _ := g.Config["cmd"].(string)
	if v, ok := pushOf(cmd); !ok || v != "origin" {
		return "", pushFail("YARDR_PUSH=origin does not stand in %s's cmd after writing it", groupName)
	}
	localHelpers, ok := gitConfigGetAll(depot.Path, helperKey, true)
	if !ok {
		return "", pushFail("depot %s's git config could not be read after writing the helper", depotName)
	}
	if len(localHelpers) != 1 || !store.IsPackHelper(localHelpers[0], helper) {
		// A box-wide pack helper answers and the local line was left
		// alone: that is the verified git half standing.
		effectiveHelpers, ok = store.PushHelpers(depot.Path, ps.Host)
		if !ok {
			return "", pushFail("depot %s's git config could not be read after writing the helper", depotName)
		}
		if len(effectiveHelpers) != 1 || !store.IsPackHelper(effectiveHelpers[0], helper) {
			return "", pushFail("the pack's helper does not stand in depot %s's git config after writing it", depotName)
		}
	}
	return fmt.Sprintf("push to %s is on for depot %s: YARDR_PUSH=origin stands in %s's cmd and the pack's helper stands in its git config for https://%s", ps.Host, depotName, groupName, ps.Host), ""
}

func pushOf(cmd string) (string, bool) {
	for _, w := range strings.Fields(cmd) {
		if strings.HasPrefix(w, "YARDR_PUSH=") {
			return strings.Trim(strings.TrimPrefix(w, "YARDR_PUSH="), "'\""), true
		}
		if !strings.Contains(w, "=") {
			break
		}
	}
	return "", false
}
