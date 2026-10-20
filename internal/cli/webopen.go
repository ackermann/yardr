package cli

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
)

// openURL is the URL a browser opens for the view: its address, and for a
// view that acts as a user the token that buys the browser its cookie.
func (v webView) openURL() string {
	if v.token == "" {
		return v.URL
	}
	return v.URL + "?token=" + v.token
}

// openBrowser opens url in this machine's browser. The opener is not waited
// for: one that stays until the browser it started ends would hold the
// command with it.
func openBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// webViewToOpen is the view yardr web open opens: the one that runs, or the
// one at addr when several do.
func webViewToOpen(cfg *config.Config, views []webView, addr string) (webView, error) {
	if len(views) == 0 {
		return webView{}, fmt.Errorf("no web view runs on the yard in %s: %s web start starts one", cfg.Home, yardrAt(cfg.Home))
	}
	var addrs []string
	for _, v := range views {
		addrs = append(addrs, v.Addr)
	}
	switch {
	case addr != "":
		if i := slices.Index(addrs, addr); i >= 0 {
			return views[i], nil
		}
		return webView{}, fmt.Errorf("no web view runs at %s: %s", addr, strings.Join(addrs, ", "))
	case len(views) > 1:
		return webView{}, fmt.Errorf("%d web views run on the yard (%s): --addr names the one to open", len(views), strings.Join(addrs, ", "))
	}
	return views[0], nil
}

func (a *app) webOpenCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the web view that runs in this machine's browser, where it acts as a user too",
		Long: `Open the web view that runs on this yard in this machine's browser.

A view that acts as a user (--as, YARDR_WEB_AS or the web_as setting) takes
its writes only from a browser that holds its cookie, which a URL with the
view's token buys once. The view prints that URL in the terminal it runs in
when it starts, and prints none where no terminal runs it (yardr web start,
the login service); this command opens it from the view's record in
<home>/webviews, a file only you can read. The cookie lasts as long as the
browser's session, and the token as long as the view runs: a view that is
started again has a token of its own. A view that does not act is opened at
its plain address.

Where no browser opens (a machine with no display, an ssh session), the URL
is printed instead, and it is the one to open in a browser on this machine.
It holds the token, as the file does: it is for you alone
(docs/web.md, "Acting as a user").

More than one view may run on a yard: --addr names the one to open by the
host:port it listens on (the error that lists them says them so).`,
		Example: `  yardr web open
  yardr web open --addr 127.0.0.1:9000`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			v, err := webViewToOpen(cfg, webViews(cfg), addr)
			if err != nil {
				return err
			}
			open := a.browser
			if open == nil {
				open = openBrowser
			}
			url := v.openURL()
			opened := open(url)
			res := map[string]any{"address": v.Addr, "url": url, "opened": opened == nil}
			if v.As != "" {
				res["acts_as"] = v.As
			}
			return a.out(c.OutOrStdout(), res, func(w io.Writer) {
				if opened != nil {
					// The token is in the URL: it is printed only when it is
					// all there is to open it by.
					fmt.Fprintf(w, "No browser opened (%v). Open this URL in one on this machine:\n%s\n", opened, url)
					return
				}
				fmt.Fprintf(w, "Opened %s", v.URL)
				if v.token != "" {
					fmt.Fprintf(w, ", where this browser acts as %s", v.As)
				}
				fmt.Fprintln(w)
			})
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "the host:port of the view to open, when several run")
	return cmd
}
