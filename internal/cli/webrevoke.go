package cli

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// webCookieAge is how long the cookie a view's token URL buys lasts, by the
// yard's web_cookie_days setting in all; 0, which the view reads as the
// setting's default, where it is not a number of days the setting takes (its
// check refuses one at set; a value stored before the check had its bound is
// read the same way).
func webCookieAge(all map[string]string) time.Duration {
	set, _ := store.SettingOf(store.WebCookieDaysSetting)
	v, _ := set.Effective(all, config.Getenv)
	days, err := strconv.Atoi(v)
	if err != nil || days < 1 || days > store.MaxWebCookieDays {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

func (a *app) webRevokeCmd() *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Log every browser out of the web view that acts as a user: a new token in place of the old",
		Long: `Replace the token of the web view that acts as a user (--as, YARDR_WEB_AS or
the web_as setting) with a new one. Every cookie a browser holds from the old
token, and every URL that carries it, stops working at once, on a view that
runs too: the view reads its token from <home>/web on every request, and no
restart is needed. yardr web open then opens the view with the new token, and
a browser that opens that URL is logged in again.

The token lasts until this command replaces it: a view that is stopped and
started again, or started at the next login, takes the same one, so a
browser stays logged in across restarts until its cookie is web_cookie_days
old (docs/web.md, "The token and the cookie"). Revoke after a cookie or a
token URL may have been seen by someone else.

--as names the user whose token is replaced; without it, the user the view
acts as by YARDR_WEB_AS or the web_as setting. Only the operator may revoke.`,
		Example: `  yardr web revoke
  yardr web revoke --as mckean`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg, err := a.config()
			if err != nil {
				return err
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if err := st.May(ctx, a.who(), store.ActRevokeWebTokens, store.Object{Kind: store.KindYard}); err != nil {
				return err
			}
			if !c.Flags().Changed("as") {
				all, err := st.Settings(ctx)
				if err != nil {
					return err
				}
				set, _ := store.SettingOf(store.WebAsSetting)
				as, _ = set.Effective(all, config.Getenv)
			}
			if as == "" {
				return fmt.Errorf("no user to revoke the web view's token of: the view acts as no one (web_as is unset), so --as names the user")
			}
			// A name of no user would make a file for a token no view takes.
			if _, err := st.GetUser(ctx, as); err != nil {
				return webAsRefused(as, "")
			}
			if _, err := web.RevokeViewToken(cfg.WebToken(as)); err != nil {
				return fmt.Errorf("could not replace the web view's token for %s: %w", as, err)
			}
			running := 0
			for _, v := range webViews(cfg) {
				if v.As == as {
					running++
				}
			}
			res := map[string]any{"user": as, "revoked": true, "running_views": running}
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				fmt.Fprintf(w, "Revoked the web view's token for %s: every browser that held its cookie is logged out", as)
				if running > 0 {
					fmt.Fprint(w, ", on the view that runs too")
				}
				fmt.Fprintln(w, ". yardr web open opens the view with the new token.")
			})
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "the user whose token is replaced (default: YARDR_WEB_AS, else web_as)")
	return cmd
}
