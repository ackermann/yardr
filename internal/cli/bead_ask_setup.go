// How a bead answer approves a setup ask: the account typed unseen,
// a candidate taken, or the standing account kept, each with the
// weighty gate before it.
package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

func (a *app) approveSetup(c *cobra.Command, st store.API, id string, seq uint64, n int) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("--secret gives the secret and takes nothing else: the value is typed at a terminal")
	}
	// The sources the pack brings are not in the yard yet: read what the
	// account is typed against from the carried texts the yard stored.
	var d *source.Declaration
	for _, carried := range setup.Carried {
		parsed, err := source.Parse("pack source "+carried.Name, []byte(carried.Text))
		if err != nil {
			return fmt.Errorf("%s", store.OneLine(err.Error()))
		}
		if parsed.Credential != nil {
			d = parsed
			break
		}
	}
	if d == nil || d.Credential == nil {
		return fmt.Errorf("ask %d sets up a pack with no credentialed source: nothing typed settles it", seq)
	}
	names := setup.Account
	if len(setup.Missing) > 0 {
		names = setup.Missing
	}
	// A setup takes the account, so its card is weighty: the gate comes
	// before a value is typed, as a secret's does.
	weighed, err := a.gate(c, st, id, seq, "approve", fmt.Sprintf("%d --secret", n))
	if err != nil {
		return err
	}
	values, err := readKeys(d, names, c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()), "")
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, values, "", a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s gave %s\n", a.who(), strings.Join(done.Given, " and "))
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
	})
}

// approveSetupAccount is yardr bead answer <id> <ask> <n> --account
// <source> on a setup ask: it approves the setup with the preset pick,
// taking the named candidate's account file instead of typing a value.
// What it prints is the preset picked and who took which source.
func (a *app) approveSetupAccount(c *cobra.Command, st store.API, id string, seq uint64, n int, account string) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("--account takes a candidate of a setup ask: yardr bead answer %s %d <n> --account <source>", id, seq)
	}
	weighed, err := a.gate(c, st, id, seq, "approve", fmt.Sprintf("%d --account %s", n, account))
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, nil, account, a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s took %s\n", a.who(), done.Account)
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
	})
}

// approveSetupKeep is yardr bead answer <id> <ask> <n> on a drift card
// whose file holds every account name: it approves the setup with the
// preset pick, keeping the standing account. What it prints is the preset
// picked and who kept it.
func (a *app) approveSetupKeep(c *cobra.Command, st store.API, id string, seq uint64, n int) error {
	notes, err := st.Notes(c.Context(), id)
	if err != nil {
		return err
	}
	var setup *store.AskSetup
	found := false
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		found = true
		if r.AskErr != nil {
			return r.AskErr
		}
		setup = r.Ask.Setup
	}
	switch {
	case !found:
		return fmt.Errorf("bead %s has no ask %d", id, seq)
	case setup == nil:
		return fmt.Errorf("ask %d sets up no pack", seq)
	}
	weighed, err := a.gate(c, st, id, seq, "approve", strconv.Itoa(n))
	if err != nil {
		return err
	}
	done, err := st.ApproveSetup(c.Context(), id, seq, n, nil, "", a.who(), weighed)
	if err != nil {
		return secretErr(err)
	}
	return a.out(c.OutOrStdout(), done, func(w io.Writer) {
		fmt.Fprintf(w, "ask %d, asked by %s: setup of pack %s, preset %s to %s\n", done.Ask, done.Asker, done.Pack, done.Preset, setup.Grantees())
		fmt.Fprintf(w, "%s kept the standing account\n", a.who())
		fmt.Fprintf(w, "%s approved it: %s\n", a.who(), done.Result)
	})
}
