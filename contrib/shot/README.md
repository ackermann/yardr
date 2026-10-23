# contrib/shot

One shell script, run by hand in a builder's session: the reference the
builder's role points at for a screenshot of a web page (design
`yardr-wm32`, bead 6.5).

**shot** takes a URL and a file and writes a 1280x800 PNG of the page with
headless Chrome:

```sh
contrib/shot/shot <url> <file>
```

The URL is `http://` or `https://`, and the file is not a symbolic link:
anything else is refused. It refuses a page that is not there and a file that cannot be written, with
the reason, before Chrome runs — headless Chrome itself exits 0 however the
page loaded, so the script fetches the page first with curl and checks the
shot afterwards. The check bypasses the proxy for a loopback page
(127.0.0.0/8, ::1, localhost), the way Go's clients do; a remote page goes
through the proxy the box sets. Chrome is `SHOT_CHROME` where that names
an executable, else the first of `google-chrome`, `google-chrome-stable`,
`chromium` and `chromium-browser` on `PATH`, else the macOS application; it
runs against a throwaway profile, so the shot never touches the operator's
own. Chrome gets `SHOT_TIMEOUT` seconds to finish, a minute when that is
unset; a slower shot fails with `shot: chrome did not finish within ...` and
what Chrome printed so far.

The builder's role (`packs/starter/roles/builder.md`) quotes the script's
flags; a test fails when the two drift
(`TestBuilderRoleQuotesTheShotFlags`). Where the page needs driving first
(a click, a scroll), the role names `agent-browser screenshot` instead; a
depot may carry its own shot script, named by its README or `.yardr/depot.md`.
