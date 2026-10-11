# contrib/github-mint

One shell script, run as a source's `mint_command`, for a source whose
credential is a GitHub App's: `credential.mint` names `GITHUB_APP_ID` and
`GITHUB_APP_PRIVATE_KEY_FILE`, and this turns their values into the token
each call is made with.

```sh
install contrib/github-mint/github-mint ~/bin/    # from main
yardr source set github mint_command=$HOME/bin/github-mint
```

Install it from `main`, not a checkout, so a branch cannot change how the
token is minted. It needs `openssl` and `curl` on PATH.

With a scope naming a repository (`owner/repo`, or a thread there like
`owner/repo#7`) it answers an installation token narrowed to that
repository, looked up and minted over the API `YARDR_SOURCE_BASE` names
(`https://api.github.com`). With no scope it answers the App's JWT itself,
good for the `/app` endpoints a check call reads. Either way stdout is
`{token, expires_at}`, and nothing of either is printed elsewhere; of the
inputs only the key's path, where it says what is wrong with the file.

The key file is held to the rule of every file yardr reads a secret from:
for the owner alone (mode 600 or stricter). The JWT is issued a minute back
and lives nine minutes, as GitHub allows an App's. The core caches the
token in memory alone, keyed by the credential and the scope.

`go test ./contrib/github-mint/` runs it against the stand-in: narrowed and
scope-free mints, and every refusal.
