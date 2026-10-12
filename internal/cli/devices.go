package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

// A device is a program of a user that calls the yard from outside it
// (docs/users.md, "Devices"): a window on another machine, a phone. The
// yard gives it a token, shown once, and takes it back by name.

func (a *app) deviceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "device", Short: "A user's devices: programs that call the yard from outside it, each let in by a token",
		Long: `A device is one program of one user on one machine or phone that calls the
yard from outside it. The yard gives it a token, and every call made with the
token is made as that user (docs/users.md, "Devices").

The token is shown once, by yardr user device add. The yard keeps its
SHA-256, never the token: one that is lost is not read again, the device is
removed and added anew. A token does not end by itself. It is refused once
its device is removed, or its user.

Only the operator adds a device, and never from the pane of an agent,
whoever the command says it is: whoever adds one decides who may speak as a
person. The operator and the user whose it is remove one.

A device reads what any user of the yard can: every bead with its notes, the
crew, the users, and the mail of everyone in the yard. It writes as its
user.`}
	var url string
	add := &cobra.Command{
		Use: "add <user> <device>", Short: "Add a device of a user and show its token, once", Args: cobra.ExactArgs(2),
		Long: `The device's name is yours to choose, one of a kind among that user's
devices: phone, laptop. The token is printed once and is in no later answer;
with --json it is the field token.

--url is the address the device will reach this yard at. The yard does not
know it, since a proxy or a tunnel of yours may stand in front: it is
printed with the token, to hand both over together.`,
		Example: `  yardr user device add mckean phone
  yardr user device add mckean laptop --url https://mac.example.net`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			d, err := st.AddDevice(c.Context(), args[0], args[1], a.who())
			if err != nil {
				return err
			}
			all, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			out := struct {
				*store.NewDevice
				URL string `json:"url,omitempty"`
			}{d, url}
			return a.out(out, func() {
				fmt.Printf("device %s of %s. Its token, shown once:\n\n  %s\n\n", d.Name, d.User, d.Token)
				if url != "" {
					fmt.Printf("Give it to the device with this yard's address: %s\n", url)
				} else {
					fmt.Println("Give it to the device with this yard's address, the one the device will reach it at.")
				}
				fmt.Printf("It calls as %s until: yardr user device rm %s %s\n", d.User, d.User, d.Name)
				if d.User != all[store.OperatorSetting] {
					fmt.Printf("%s is not the operator, and the device reads what any user of the yard can: every bead, and the mail of everyone in it.\n", d.User)
				}
			})
		},
	}
	add.Flags().StringVar(&url, "url", "", "where the device reaches this yard, such as https://mac.example.net: printed with the token")
	list := &cobra.Command{
		Use: "list [<user>]", Short: "List the devices, of one user or of all", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			user := ""
			if len(args) == 1 {
				user = args[0]
			}
			devices, err := st.Devices(c.Context(), user)
			if err != nil {
				return err
			}
			return a.out(devices, func() {
				if len(devices) == 0 {
					if user != "" {
						fmt.Printf("%s has no device (yardr user device add %s <device>)\n", user, user)
					} else {
						fmt.Println("no devices (yardr user device add <user> <device>)")
					}
					return
				}
				rows := [][]string{}
				for _, d := range devices {
					rows = append(rows, []string{d.User, d.Name, d.CreatedAt.Local().Format(mailTime), lastUsed(d)})
				}
				table("USER\tDEVICE\tADDED\tLAST USED", rows)
			})
		},
	}
	rm := &cobra.Command{
		Use: "rm <user> <device>", Short: "Remove a device: its token is refused from now on", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			d, err := st.RemoveDevice(c.Context(), args[0], args[1], a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"removed": d}, func() {
				fmt.Printf("device %s of %s removed: its token is refused from now on\n", d.Name, d.User)
			})
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

// lastUsed says when a device was last used and from where, "never" for
// one that was not.
func lastUsed(d store.Device) string {
	if d.LastUsed == nil {
		return "never"
	}
	out := d.LastUsed.Local().Format(mailTime)
	if d.LastFrom != "" {
		out += " from " + d.LastFrom
	}
	return out
}

// unusedDevice is yardr doctor's note on a device nothing has called with
// for store.DeviceUnusedAfter: its token still lets in whoever has it.
func unusedDevice(d store.Device, yardr string) string {
	days := int(store.DeviceUnusedAfter / (24 * time.Hour))
	last := "last used " + lastUsed(d)
	if d.LastUsed == nil {
		last = "never used, added " + d.CreatedAt.Local().Format(mailTime)
	}
	return fmt.Sprintf("device %s of %s has not been used for %d days (%s), and its token still calls as %s. "+
		"If it is no longer in use: %s user device rm %s %s", d.Name, d.User, days, last, d.User, yardr, d.User, d.Name)
}
