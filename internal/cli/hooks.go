package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mckean/yard/internal/hooks"
	"github.com/mckean/yard/internal/store"
)

func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hook", Short: "Webhook endpoints that turn deliveries into hook events"}

	var h store.Hook
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Create or replace a webhook endpoint POST /hooks/<name>",
		Long: `A hook receives webhooks from one source on the yard server's hook listener,
which runs only when YARD_HOOK_ADDR (e.g. 127.0.0.1:8790) is set in the
environment the server starts in. The listener serves POST /hooks/<name> and
nothing else. Each verified delivery becomes one 'hook' event; its raw body is
kept for 'yard hook show <seq>'.

The secret stays in --secret-file and is read on every delivery, so rotating it
is rewriting the file. Sources:
  linear  the webhook's signing secret (Linear-Signature HMAC)
  token   a shared bearer token (Authorization: Bearer <token>)`,
		Example: `  yard hook add linear --source linear --secret-file ~/.config/yard/linear.secret`,
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if _, ok := hooks.Adapters[h.Source]; !ok {
				return fmt.Errorf("--source %q: one of %s", h.Source, strings.Join(hooks.Sources(), ", "))
			}
			path, err := secretPath(h.SecretFile)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			h.Name, h.SecretFile = args[0], path
			saved, err := st.PutHook(c.Context(), h, a.who())
			if err != nil {
				return err
			}
			return a.out(saved, func() {
				fmt.Printf("hook %s saved: POST /hooks/%s (%s)\n", saved.Name, saved.Name, saved.Source)
				if a.cfg != nil && a.cfg.HookAddr == "" {
					fmt.Println("note: YARD_HOOK_ADDR is not set here; the server receives hooks only if it was started with it")
				}
			})
		},
	}
	add.Flags().StringVar(&h.Source, "source", "", "source adapter: "+strings.Join(hooks.Sources(), ", "))
	add.Flags().StringVar(&h.SecretFile, "secret-file", "", "file holding the secret (never the secret itself)")
	_ = add.MarkFlagRequired("source")
	_ = add.MarkFlagRequired("secret-file")

	list := &cobra.Command{
		Use: "list", Short: "List hooks", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			hs, err := st.Hooks(c.Context())
			if err != nil {
				return err
			}
			return a.out(hs, func() {
				rows := [][]string{}
				for _, h := range hs {
					rows = append(rows, []string{h.Name, h.Source, "/hooks/" + h.Name, h.SecretFile})
				}
				table("NAME\tSOURCE\tPATH\tSECRET FILE", rows)
			})
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Short: "Delete a hook (events it accepted are kept)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.DeleteHook(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"hook": args[0], "rm": "ok"}, func() { fmt.Printf("%s: removed\n", args[0]) })
		},
	}

	show := &cobra.Command{
		Use: "show <event seq>", Short: "Print the raw payload of a hook event", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			seq, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("event seq %q: %w", args[0], err)
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			raw, err := st.HookPayload(c.Context(), seq)
			if err != nil {
				return err
			}
			var payload any = string(raw)
			if json.Valid(raw) {
				payload = json.RawMessage(raw)
			}
			return a.out(map[string]any{"seq": seq, "payload": payload}, func() {
				var pretty bytes.Buffer
				if json.Indent(&pretty, raw, "", "  ") == nil {
					raw = pretty.Bytes()
				}
				os.Stdout.Write(raw)
				if !bytes.HasSuffix(raw, []byte("\n")) {
					fmt.Println()
				}
			})
		},
	}

	cmd.AddCommand(add, list, rm, show)
	return cmd
}

// secretPath resolves the secret file and checks it holds something,
// without ever showing what.
func secretPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[2:])
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("--secret-file: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return "", fmt.Errorf("--secret-file %s is empty", p)
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "warning: %s is readable by others (mode %o); chmod 600 it\n", p, fi.Mode().Perm())
	}
	return p, nil
}
