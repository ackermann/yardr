package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"rsc.io/qr"

	"io"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// A device is enrolled by a code, as two yards are paired (docs/users.md,
// "Enrolled by a code"): the operator opens a pairing for one device of one
// user, and the device that calls the yard with the code is answered with
// its token. Nobody types or pastes the token, and no command prints it.

// pairQR is what the QR code of a device's pairing holds: the address the
// device reaches the yard at, then '#', then the code. Nothing else is in
// it, and an address has no '#' of its own (yardkey.CleanURL).
func pairQR(address, code string) string { return address + "#" + code }

// drawQR draws text as a QR code in the terminal's characters, two modules
// to a line, inside the quiet zone a reader wants around it. With colour it
// is black on white whatever the terminal's own colours are; without, the
// dark modules are the text's colour, which reads on a light ground.
func drawQR(text string, colour bool) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	dark := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < code.Size+quiet; y += 2 {
		b.WriteString("  ")
		if colour {
			b.WriteString("\x1b[30;47m")
		}
		for x := -quiet; x < code.Size+quiet; x++ {
			switch top, bottom := dark(x, y), dark(x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		if colour {
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// devicePaired is what yardr user device pair prints of a pairing it
// opened.
type devicePaired struct {
	Pairing string    `json:"pairing"`
	User    string    `json:"user"`
	Device  string    `json:"device"`
	Code    string    `json:"code"`
	Address string    `json:"address"`
	Expires time.Time `json:"expires"`
	// QR is what a QR code of the pairing holds (pairQR).
	QR string `json:"qr"`
}

func (a *app) devicePairCmd() *cobra.Command {
	var addr string
	var draw, end bool
	cmd := &cobra.Command{
		Use: "pair <user> <device>", Short: "Enrol a device by a short code, or a QR code, with no token to copy",
		Long: `Opens a pairing for one device of one user and shows its code. The device
calls this yard with the code and is answered with its token, once: nobody
types or pastes the token, and nothing here prints it. The device is added
when the code is used, and is then as one yardr user device add made.

The code is good for 10 minutes and for one device, and 5 wrong codes end
the pairing. A yard has one such pairing open at a time; --end ends it.

--url is the address the device reaches this yard at, which the device is
given with the code; the default is the listener's own address, which is
right on one machine or one network. --qr also draws a QR code that holds
the address and the code, as <address>#<code>, and nothing else: a phone
reads it in place of both being typed.

The code is all a device needs, and whoever has it within those minutes gets
the token: show it, and the QR code, to the device and to nobody else. What
keeps the code and the token from whoever is between the device and the
yard is the connection, so give an https address off this machine.

Only the operator opens a pairing, and never from the pane of an agent, as
for yardr user device add: whoever enrols a device decides who may speak as
a person.`,
		Example: `  yardr user device pair mckean phone --url https://mac.example.net --qr
  yardr user device pair --end`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return errors.New("give the user and the device's name, or --end alone")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if end {
				if len(args) != 0 || addr != "" || draw {
					return errors.New("--end ends the device pairing this yard has open: it takes nothing else")
				}
				p, err := st.DevicePairClose(ctx, a.who())
				if errors.Is(err, store.ErrNotFound) {
					return errors.New("this yard has no device pairing open")
				} else if err != nil {
					return err
				}
				return a.out(c.OutOrStdout(), map[string]any{"pairing": "ended", "user": p.User, "device": p.Device}, func(w io.Writer) {
					fmt.Printf("the pairing for the device %s of %s is ended: its code is good for nothing\n", p.Device, p.User)
				})
			}
			if len(args) != 2 {
				return errors.New("give the user and the device's name, or --end to end the pairing that is open")
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			all, err := st.Settings(ctx)
			if err != nil {
				return err
			}
			if listening, text := receiver(ctx, cfg, st); !listening {
				return fmt.Errorf("this yard has no listener, so no device can call it with a code: %s", text)
			}
			if addr == "" {
				if addr, err = listenerURL(cfg); err != nil {
					l, _ := hooks.ReadListener(cfg)
					return fmt.Errorf("the listener's address (%s) is none to give a device: say where the device reaches this yard with --url, such as --url https://<this machine>", l.Addr)
				}
			} else if addr, err = yardkey.CleanURL(addr); err != nil {
				return fmt.Errorf("%w: the address the device reaches this yard at", err)
			}
			opened, err := st.DevicePairOpen(ctx, args[0], args[1], a.who())
			if err != nil {
				return err
			}
			out := devicePaired{Pairing: opened.ID, User: opened.User, Device: opened.Device, Code: opened.Code,
				Address: addr, Expires: opened.Expires, QR: pairQR(addr, opened.Code)}
			var picture string
			if draw && !a.json {
				if picture, err = drawQR(out.QR, term.IsTerminal(int(os.Stdout.Fd()))); err != nil {
					// The pairing is open and its code good: only the picture is not there.
					fmt.Fprintf(stderr, "no QR code could be drawn of %d characters (%v): give the device the address and the code\n", len(out.QR), err)
				}
			}
			yardr := yardrAt(cfg.Home)
			return a.out(c.OutOrStdout(), out, func(w io.Writer) {
				fmt.Printf("Pairing is open for %s, until %s, for the device %s of %s. Give the device this yard's address and the code:\n\n"+
					"  address  %s\n  code     %s\n\n",
					short(pairing.TTL), out.Expires.Local().Format("15:04:05"), out.Device, out.User, out.Address, out.Code)
				if picture != "" {
					fmt.Printf("%s\n", picture)
				}
				fmt.Printf("The code is good once: the device is answered with its token, which is shown nowhere else. "+
					"%d wrong codes end the pairing, and so does: %s user device pair --end\n", pairing.MaxTries, yardr)
				fmt.Printf("It then calls as %s until: %s user device rm %s %s\n", out.User, yardr, out.User, out.Device)
				if out.User != all[store.OperatorSetting] {
					fmt.Printf("%s is not the operator, and the device reads what any user of the yard can: every bead, and the mail of everyone in it.\n", out.User)
				}
				if u, err := url.Parse(out.Address); err == nil && u.Scheme == "http" && u.Hostname() != "localhost" && !isLoopback(u.Hostname()) {
					fmt.Printf("%s is plain http: whoever is between the device and this yard reads the code, and the token after it. Use it on a network you trust.\n", out.Address)
				}
			})
		},
	}
	cmd.Flags().StringVar(&addr, "url", "", "where the device reaches this yard's listener, such as https://mac.example.net (default: the listener's address)")
	cmd.Flags().BoolVar(&draw, "qr", false, "also draw a QR code that holds the address and the code")
	cmd.Flags().BoolVar(&end, "end", false, "end the device pairing this yard has open")
	return cmd
}
