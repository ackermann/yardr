package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// The server's carrying out of an approved teammate invite or removal
// (design yardr-hosted-d3wc sections 5.2 and 7): under the apply lock,
// in the design's order, as the operator who approved. An invite makes
// the yard user and the web_jwt_users mapping first and reports member
// add after them; a removal reports member remove first and removes
// the user and the mapping only once it lands. Where a report cannot
// land (no answer, or a signed 503) the yard writes still stand or
// still go as the design orders, and the claim is left standing, never
// failed: the watcher drains on every event and a 2s tick, and the next
// start takes it up (ResumeCarry), so the report lands when the edge
// answers again. Only a signed refusal waiting cannot cure (403 the
// report names another yard, 409 a standing membership, 422 an
// unverified address) fails the carry, with the edge's words.

func isTeammateCarry(argv []string) bool {
	return len(argv) >= 3 && argv[0] == "user" && (argv[1] == "invite" || argv[1] == "uninvite")
}

// carryTeammateClaimed runs a claimed teammate carrying out under the
// apply lock, as carryClaimed does for an apply. A report still owed
// answers an error, which leaves the claim standing for the next drain
// or the next start; a refusal for good fails the carry on the card.
func (c *carrier) carryTeammateClaimed(ctx context.Context, rec *store.Carry, run *store.AskRun) (bool, error) {
	release, err := config.AcquireApplyLock(c.home)
	if err != nil {
		return true, fmt.Errorf("carrying out ask %d on %s: the apply lock: %w", rec.Ask, rec.Bead, err)
	}
	defer release()
	result, failure, owed := c.carryTeammate(ctx, rec, run)
	if owed != nil {
		return true, owed
	}
	if failure != "" {
		if err := c.st.FailCarry(ctx, rec.Bead, rec.Ask, c.boot, failure); err != nil {
			return true, err
		}
		c.log.Warn("carrying out a teammate ask failed", "bead", rec.Bead, "ask", rec.Ask, "failure", failure)
		return true, nil
	}
	if err := c.st.CompleteCarry(ctx, rec.Bead, rec.Ask, c.boot, result); err != nil {
		return true, err
	}
	c.log.Info("carried out a teammate ask", "bead", rec.Bead, "ask", rec.Ask, "command", run.Command())
	return true, nil
}

// carryTeammate is one claimed invite or removal: it answers what it
// printed (done), why it failed for good (failed), or the report it
// still owes (owed, which leaves the claim standing).
func (c *carrier) carryTeammate(ctx context.Context, rec *store.Carry, run *store.AskRun) (string, string, error) {
	if run.Argv[1] == "invite" {
		return c.carryInvite(ctx, rec, run)
	}
	return c.carryUninvite(ctx, rec, run)
}

// inviteOf is the name, the address and the role of a parsed invite,
// the role a member's where the command names none.
func inviteOf(argv []string) (name, email, role string) {
	name, role = argv[2], store.RoleMember
	for i := 3; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--email":
			email = argv[i+1]
		case "--role":
			role = argv[i+1]
		}
	}
	return name, email, role
}

func (c *carrier) carryInvite(ctx context.Context, rec *store.Carry, run *store.AskRun) (string, string, error) {
	name, email, role := inviteOf(run.Argv)
	if email == "" && len(rec.TeammateEmails) > 0 {
		email = rec.TeammateEmails[0]
	}
	if err := c.st.CarryInvite(ctx, rec.Bead, rec.Ask, c.boot, name, email, role, rec.Actor); err != nil {
		return "", fmt.Sprintf("the invite of %s did not land at the yard: %v", name, err), nil
	}
	if err := c.reportMemberAdd(ctx, email, name, rec.Actor); err != nil {
		if failure, owed := memberFailure(err); owed {
			return "", "", fmt.Errorf("carrying out ask %d on %s: the member add for %s is still owed: %w", rec.Ask, rec.Bead, email, err)
		} else {
			return "", fmt.Sprintf("the invite of %s stands at the yard, and the edge refused its report: %s", name, failure), nil
		}
	}
	return fmt.Sprintf("user %s added as a %s, %s is mapped to them, and the edge took the member add", name, role, email), "", nil
}

func (c *carrier) carryUninvite(ctx context.Context, rec *store.Carry, run *store.AskRun) (string, string, error) {
	name := run.Argv[2]
	emails := rec.TeammateEmails
	if len(emails) == 0 {
		var err error
		if emails, err = c.st.TeammateEmails(ctx, name); err != nil {
			return "", fmt.Sprintf("the removal of %s did not land at the yard: %v", name, err), nil
		}
	}
	if len(emails) == 0 {
		return "", fmt.Sprintf("the removal of %s names no mapped address to report: the mapping no longer maps any address to them", name), nil
	}
	owed := false
	for _, email := range emails {
		if err := c.reportMemberRemove(ctx, email); err != nil {
			if failure, isOwed := memberFailure(err); isOwed {
				owed = true
				continue
			} else {
				return "", fmt.Sprintf("the edge refused the removal report for %s, so the user %s stands: %s", email, name, failure), nil
			}
		}
	}
	if err := c.st.CarryUninvite(ctx, rec.Bead, rec.Ask, c.boot, name, rec.Actor); err != nil {
		return "", fmt.Sprintf("the removal of %s did not land at the yard: %v", name, err), nil
	}
	if owed {
		return "", "", fmt.Errorf("carrying out ask %d on %s: the member remove for %s is still owed", rec.Ask, rec.Bead, strings.Join(emails, ", "))
	}
	return fmt.Sprintf("the edge took the member remove for %s, and user %s and their mapping are removed", strings.Join(emails, ", "), name), "", nil
}

// memberFailure reads a member report's error: a signed refusal waiting
// cannot cure answers its words as a failure for good; no answer, a
// signed 503 and an answer signed by no pinned key each leave the
// report owed.
func memberFailure(err error) (failure string, owed bool) {
	var refused *edgelink.Refused
	if errors.As(err, &refused) {
		switch {
		case refused.Status == 503:
			return "", true
		case refused.Status == http.StatusForbidden || refused.Status == http.StatusConflict || refused.Status == http.StatusUnprocessableEntity:
			return refused.Error(), false
		default:
			return "", true
		}
	}
	return "", true
}

// memberDoor is the carrier's way to the edge: the client at the edge
// this yard pinned, and the yard's key it signs with.
func (c *carrier) memberDoor(ctx context.Context) (*edgelink.Client, store.Edge, ed25519.PrivateKey, error) {
	settings, err := c.st.Settings(ctx)
	if err != nil {
		return nil, store.Edge{}, nil, err
	}
	edge, connected, err := store.EdgeOf(settings)
	if err != nil {
		return nil, store.Edge{}, nil, err
	}
	if !connected {
		return nil, store.Edge{}, nil, fmt.Errorf("this yard is connected to no edge (the %s setting names none)", store.EdgeSetting)
	}
	yard, err := yardkey.Load(config.At(c.home).PeerKey())
	if err != nil {
		return nil, store.Edge{}, nil, err
	}
	return &edgelink.Client{URL: edge.URL, HTTP: c.edgeHTTP}, edge, yard, nil
}

func (c *carrier) reportMemberAdd(ctx context.Context, email, yardUser, invitedBy string) error {
	client, edge, yard, err := c.memberDoor(ctx)
	if err != nil {
		return err
	}
	return client.MemberAdd(ctx, edge.Key, yard, edgelink.MemberAdd{
		YardHandle: edge.Handle, YardName: edge.Name, Email: email,
		YardUser: yardUser, InvitedBy: invitedBy, YardKey: yardkey.Public(yard),
	})
}

func (c *carrier) reportMemberRemove(ctx context.Context, email string) error {
	client, edge, yard, err := c.memberDoor(ctx)
	if err != nil {
		return err
	}
	return client.MemberRemove(ctx, edge.Key, yard, edgelink.MemberRemove{
		YardHandle: edge.Handle, YardName: edge.Name, Email: email, YardKey: yardkey.Public(yard),
	})
}
