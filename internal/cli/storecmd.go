package cli

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// retention is how long the store keeps events (and ended sessions) and
// webhook bodies, and how long an unread inbox event holds the log back:
// YARDR_EVENT_RETENTION, YARDR_HOOK_PAYLOAD_RETENTION and
// YARDR_INBOX_RETENTION, else store.DefaultRetention.
func retention() (store.Retention, error) {
	ev, err := config.Retention("EVENT_RETENTION", store.DefaultRetention.Events)
	if err != nil {
		return store.Retention{}, err
	}
	hp, err := config.Retention("HOOK_PAYLOAD_RETENTION", store.DefaultRetention.HookPayloads)
	if err != nil {
		return store.Retention{}, err
	}
	ib, err := config.Retention("INBOX_RETENTION", store.DefaultRetention.Inbox)
	if err != nil {
		return store.Retention{}, err
	}
	return store.Retention{Events: ev, HookPayloads: hp, Inbox: ib}, nil
}

func (a *app) storeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "store", Short: "The store file: back up, prune old events, compact"}
	var events, payloads, inbox string
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Remove events, ended sessions and webhook bodies past their retention",
		Long: `Remove events and ended sessions older than the event retention and webhook
bodies older than the hook payload retention (defaults 90d and 30d, or
YARDR_EVENT_RETENTION and YARDR_HOOK_PAYLOAD_RETENTION). The daemon does this
every hour.

The newest event and anything since the oldest live session started are kept
whatever their age, and so is an event a running crew member has not read yet,
for up to the inbox retention (180d, or YARDR_INBOX_RETENTION); a stopped or
disabled crew member holds nothing back.

What a link keeps of its messages goes at the age of events too, but never
younger than 7 days: the ids of the messages taken from a peer, by which one
sent twice is taken once, and the messages for it that were given up.

The file does not shrink until it is compacted (yardr store compact).`,
		Example: `  yardr store prune
  yardr store prune --events 30d --hook-payloads 7d
  yardr store prune --hook-payloads off            # keep every webhook body
  yardr store prune --inbox off                    # unread inbox events hold the log back for good`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			keep, err := retention()
			if err != nil {
				return err
			}
			if events != "" {
				if keep.Events, err = config.ParseRetention(events); err != nil {
					return err
				}
			}
			if payloads != "" {
				if keep.HookPayloads, err = config.ParseRetention(payloads); err != nil {
					return err
				}
			}
			if inbox != "" {
				if keep.Inbox, err = config.ParseRetention(inbox); err != nil {
					return err
				}
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			res, err := st.Prune(c.Context(), keep, a.who())
			if err != nil {
				return err
			}
			return a.out(res, func() {
				fmt.Printf("pruned %d events, %d sessions, %d webhook bodies\n", res.Events, res.Sessions, res.HookPayloads)
				if res.Blobs > 0 {
					fmt.Printf("  %d images no mail names any more\n", res.Blobs)
				}
				if res.HeldBy != "" {
					fmt.Printf("older events kept: %s\n", res.HeldBy)
				}
				for _, name := range slices.Sorted(maps.Keys(res.Expired)) {
					fmt.Printf("crew member %s: %d unread inbox events went with the log\n", name, res.Expired[name])
				}
				for _, name := range slices.Sorted(maps.Keys(res.Peers)) {
					fmt.Printf("peer %s: %d ids of messages taken from it forgotten, %d messages given up removed\n", name, res.Peers[name].Seen, res.Peers[name].Failed)
				}
			})
		},
	}
	prune.Flags().StringVar(&events, "events", "", "keep events this long: 90d, 720h, or off (default $YARDR_EVENT_RETENTION, else 90d)")
	prune.Flags().StringVar(&payloads, "hook-payloads", "", "keep webhook bodies this long (default $YARDR_HOOK_PAYLOAD_RETENTION, else 30d)")
	prune.Flags().StringVar(&inbox, "inbox", "", "an unread inbox event holds the log back this long (default $YARDR_INBOX_RETENTION, else 180d)")

	compact := &cobra.Command{
		Use:   "compact",
		Short: "Rewrite the store file without its free space (server stopped)",
		Long: `Rewrite yard.db without the free pages pruning leaves, so the file shrinks.
It needs the file to itself: stop the daemon and the server first (the daemon
would start the server again). This is the one command on the store that
does not start the server. The rewrite replaces the file only once it is
complete.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// Said before the file is tried: a server holds it for as long
			// as it runs, and ErrLocked alone does not say who has it.
			if server.Running(c.Context(), cfg) {
				return fmt.Errorf("the yardr server is running and holds %s: stop the daemon, then `yardr server stop`, and compact again", cfg.DBPath())
			}
			res, err := store.Compact(cfg.DBPath())
			if errors.Is(err, store.ErrLocked) {
				return fmt.Errorf("%w: stop the daemon, then `yardr server stop`, and compact again", err)
			} else if err != nil {
				return err
			}
			return a.out(res, func() {
				fmt.Printf("%s: %d -> %d bytes\n", cfg.DBPath(), res.Before, res.After)
			})
		},
	}
	backup := &cobra.Command{
		Use:   "backup <path>",
		Short: "Write a consistent copy of the store to path while it keeps serving",
		Long: `Write a copy of yard.db to path through the running server: one read
transaction copies the store as of a single commit while writes go on, so
nothing needs stopping. The copy lands beside path and is renamed into place
once complete; an existing file at path is replaced.

Restore by stopping the daemon and the server and copying the backup over
yard.db.`,
		Example: `  yardr store backup ~/backups/yard-$(date +%F).db`,
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// The server writes the file, from its own working directory.
			path, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			res, err := st.Backup(c.Context(), path)
			if err != nil {
				return err
			}
			return a.out(res, func() {
				fmt.Printf("%s: %d bytes, events up to %d\n", res.Path, res.Bytes, res.Head)
			})
		},
	}
	cmd.AddCommand(backup, prune, compact)
	return cmd
}
