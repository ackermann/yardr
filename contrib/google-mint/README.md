# contrib/google-mint

One shell script, run as a source's `mint_command`, for a source whose
credential is the operator's gws login: `credential.mint` names
`GOOGLE_ACCOUNT`, and this turns the login into the token each call is
made with.

```sh
install contrib/google-mint/google-mint ~/bin/    # from main
yardr source set google-mail mint_command=$HOME/bin/google-mint
```

Install it from `main`, not a checkout, so a branch cannot change how
the token is minted. It needs `gws` and `curl` on PATH: `GWS_BIN`
names another gws, else the one on PATH, else
`/opt/homebrew/bin/gws`.

`GOOGLE_ACCOUNT` is the account's address, no secret. The script asks
gws whose login it holds (`gws auth status`, gws 0.22.5) and refuses
when that is another account; it reads the client id, client secret
and refresh token (`gws auth export --unmasked`) and exchanges them at
`https://oauth2.googleapis.com/token` (`GOOGLE_TOKEN_ENDPOINT` may name
a loopback HTTP endpoint for a test's stand-in). No new copy of a secret is
stored: the three are held in this process's memory alone and never
printed, neither on stdout nor on stderr, nor written anywhere. The
scope it is handed is the call's; Google's token narrows by the gws
grant, not by call, so the scope is read and ignored.

Either way stdout is `{token, expires_at}`, and nothing of either is
printed elsewhere; of the inputs only the account, where it says the
login is another's. The core caches the token in memory alone, keyed
by the credential and the scope.

`go test ./contrib/google-mint/` runs it against a fake gws on PATH
and a fake token endpoint: a good exchange, a wrong account refused,
and every refusal with no secret in its output or stderr.
