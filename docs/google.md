# Google in the yard

Three packs wire Google in, grouped by what a person turns on: each
is applied alone, and a yard takes only the Google access it needs.

| pack | wires in | the login's services |
|---|---|---|
| `google-mail` | Gmail read and drafts, never sent (`google-mail`) | `gmail` |
| `google-drive` | Drive files and folders, and the empty Docs and Slides the docs pack fills (`google-drive`) | `drive` |
| `google-docs` | Docs and Slides read and appended to (`google-docs`, `google-slides`) | `docs,slides` |

Each pack's own steps are its setup notes (`yardr pack show
google-mail`, and the drive and docs packs'); this page has what the
three share: the gws login every call mints from, the mint, the env
file, and calling as a user.

## The gws login

The machine holds it: gws 0.22.5 at `/opt/homebrew/bin/gws`, its
config in `~/.config/gws`. The account it is logged in as is what
the sources mint from:

```sh
gws auth status
```

Its `user` is the account's address, which the setup sets as
`GOOGLE_ACCOUNT`. Its `scopes` must hold the scopes the packs call
with — `https://www.googleapis.com/auth/gmail.modify` for mail,
`https://www.googleapis.com/auth/drive` for drive,
`https://www.googleapis.com/auth/documents` and
`https://www.googleapis.com/auth/presentations` for docs — else the
calls come back 403. A login without one is made again with every
service the yard wants, so nothing set up is lost:

```sh
gws auth login -s gmail,drive,docs,slides
```

A yard with one pack logs in with that pack's services alone (the
table above); adding a pack later means logging in again with the
services added. Google's token narrows by this grant, not by call,
so the login's scopes are the yard's whole Google access.

## The mint

`contrib/google-mint` turns the login into a token per call. Put it
where the yard runs, from `main`, not a checkout, so a branch cannot
change how the token is minted, and set it on each source the yard
applies:

```sh
install contrib/google-mint/google-mint ~/bin/    # from main
yardr source set google-mail mint_command=$HOME/bin/google-mint
```

It needs `gws` and `curl` on PATH (`GWS_BIN` names another gws). It
refuses when the gws login is for another account than
`GOOGLE_ACCOUNT`, and it never prints the refresh token or client
secret, nor writes them anywhere. No new copy of a secret is stored:
the token is handed to each call under `GOOGLE_TOKEN` and kept in
memory alone. The longer telling is
[`contrib/google-mint/README.md`](../contrib/google-mint/README.md).

## The env file

One file holds the account for the google packs' sources:
`~/.yardr/sources/google.env`, with `GOOGLE_ACCOUNT` set to the
address the login showed. The address is no secret, so it is typed
as usual; the file still goes 0600, as every env file. Each source
is pointed at it (`yardr source set <source>
env_file=~/.yardr/sources/google.env`), as the pack's own steps say.
A source can keep its own file instead: `yardr source key <source>`
writes the names into the source's `env_file`, or with none set
into `~/.yardr/sources/<name>.env`.

## Calling as a user

The yard can call as another person of the yard, with their own
credential: the yardmaster reading ada's mail, not the operator's.
It takes three things, beside the pack and its grants:

1. The person is a user of the yard (`yardr user add <name>`), and
   their Google account has a gws login of its own, in
   `~/.config/gws-accounts/<account>`, with the scopes the calls
   need. The mint reads that login for their calls and the
   machine's for the yard's.
2. Their account's address goes into their env file, not the
   source's: `yardr source key <source> --as <user>` asks for
   `GOOGLE_ACCOUNT` and writes it to the user's `env_file`, or with
   none set to `~/.yardr/users/<user>.env`. Or a crew member asks
   for it on a card, answered where the field shows dots:
   ```sh
   yardr bead ask <bead> "Your Google account for your mail?" --secret google-mail --as ada --context "<where to find it>"
   ```
3. The grant as them is theirs alone to give: `yardr source grant
   <source> read|<call> --crew <crew member> --as <user>`. The
   operator is refused it, at a terminal as on a card, and a plain
   grant covers no call as them. Anyone may take it back, as any
   grant.

```sh
yardr source check google-mail --as ada   # her credential, tried as a check tries the source's
yardr call google-mail search --as ada q="from:me" maxResults=5
```

How a call as a user is granted, made and counted is the same for
every source: [integrations.md](integrations.md#a-call-by-a-session-or-a-crew-member)
has it.
