# Google in the yard

Four packs wire Google in, grouped by what a person turns on: each
is applied alone, and a yard takes only the Google access it needs.

| pack | wires in | the login's services |
|---|---|---|
| `google-mail` | Gmail read, drafts listed, written and edited, never sent (`google-mail`) | `gmail` |
| `google-drive` | Drive files and folders, shared with one address, the empty Docs and Slides the docs pack fills, and comments read and replied to (`google-drive`) | `drive` |
| `google-docs` | Docs read, appended to and replaced in place, Slides read and appended to (`google-docs`, `google-slides`) | `docs,slides` |
| `google-calendar` | Events listed, read, created and edited without invitations, calendars listed (`google-calendar`) | `calendar` |

Each pack's own steps are its setup notes (`yardr pack show
google-mail`, `yardr pack show google-drive`, `yardr pack show
google-docs` and `yardr pack show google-calendar`): the pack itself
(step 2), its grants (step 5), and the operator trying what they set
up (steps 6 and 7). This page has what the four share: the gws login
every call mints from (step 1), the account (step 3), the mint (step
4), and calling as a user. The steps
use `ada` for a second person, `mckean` for the operator's user name
and `yardmaster` for the crew member; replace each with this yard's
where they differ.

Two identities can call. The yard's own account answers the plain
calls, `yardr call google-mail search …`, made with the credential
the yard holds for each source. A person's own account answers the
calls made as them, `yardr call google-mail search --as ada …`, made
with the credential in their user file. The steps below set the
yard's account up first, then one person's, ada's; the operator's own
is a second person of the same shape, kept apart from the yard's
even where it is the same address.

Everything a crew member asks for — a user added, a pack's whole
setup, a grant plain or `as` — is a card, answered in
footplate or in `yardr chat` (`ctrl+o`). Setting a pack up is one
card per pack: the apply, the account and the grants together
(`bead ask --setup`), the address typed once, then picked. What
stays in the terminal is what cannot be a card: a login in a
browser (step 1), and the operator trying what they set up (steps
6 and 7). The mint (step 4) comes with the pack.

## Step 0. The person is a user

Nothing can be asked of ada or granted as her until she is a user of
the yard: both refuse a name it has not. Have a crew member ask for
it, and approve its card:

```sh
yardr bead ask <bead> "Add ada as a user of the yard" --run "user add ada" --context "Ada's Google account is set up next; the yard needs her as a user first."
```

The card is the operator's to approve (`yardr bead answer <bead>
<ask> --approve`, or the card's button in footplate). By hand it is
one command:

```sh
yardr user add ada
```

The yard's own account needs no user; the operator needs one only
to be called as (step 3).

## Step 1. The gws login

The machine holds it: gws 0.22.5 at `/opt/homebrew/bin/gws`, its
config in `~/.config/gws`. The account it is logged in as is what
the yard's calls mint from:

```sh
gws auth status
```

Its `user` is the account's address, which the setup sets as
`GOOGLE_ACCOUNT`. Its `scopes` must hold the scopes the packs call
with — `https://www.googleapis.com/auth/gmail.modify` for mail,
`https://www.googleapis.com/auth/drive` for drive,
`https://www.googleapis.com/auth/documents` and
`https://www.googleapis.com/auth/presentations` for docs, and
`https://www.googleapis.com/auth/calendar` for calendar — else the
calls come back 403. When a pack is added, log in again with every
service the yard wants, so nothing set up is lost:

```sh
gws auth login -s gmail,drive,docs,slides,calendar
```

A yard with one pack logs in with that pack's services alone (the
table above); adding a pack later means logging in again with the
services added. Google's token narrows by this grant, not by call,
so the login's scopes are the yard's whole Google access.

A second person's login stands beside it, in that account's own
directory, `~/.config/gws-accounts/<account>`, made with the same
login for that directory alone:

```sh
GOOGLE_WORKSPACE_CLI_CONFIG_DIR="$HOME/.config/gws-accounts/ada@example.com" gws auth login -s gmail,drive,docs,slides,calendar
```

Replace `ada@example.com` with ada's Google account address, then
sign in as ada in the browser, with the services the yard's packs
need as the yard's login has them. That variable is set for this
login command alone, as the mint later sets it for the gws it
starts; it is written into no env file and declared in no source. A
browser login cannot be a card, so this step stays in the terminal,
once per person.

## Step 3. The account

The yard's own first: one card per pack, each naming
`GOOGLE_ACCOUNT`, the address step 1 showed. A crew member asks,
and the operator answers where the field shows dots. The first
pack's card is typed unseen, into that pack's first source's own
file (`~/.yardr/sources/google-mail.env`), which the yard chooses
and records, so no `env_file` is set by anyone. Every pack after
lists the first by name with its address, to take it or type
another: the address is typed once, then picked. Run only the cards
for the packs the yard uses:

```sh
yardr bead ask <bead> "Set up Gmail for the crew?" --setup google-mail --crew <name> --recommend 1 --why "reading is the job; drafts can follow" --context "The address step 1 showed: the address alone, no token."
yardr bead ask <bead> "Set up Drive for the crew?" --setup google-drive --crew <name> --recommend 1 --why "reading is the job; making and sharing can follow" --context "The address step 1 showed: the address alone, no token."
yardr bead ask <bead> "Set up Docs and Slides for the crew?" --setup google-docs --crew <name> --recommend 1 --why "reading is the job; appending can follow" --context "The address step 1 showed: the address alone, no token."
yardr bead ask <bead> "Set up Calendar for the crew?" --setup google-calendar --crew <name> --recommend 1 --why "reading is the job; creating can follow" --context "The address step 1 showed: the address alone, no token."
```

Each names the grantee and recommends a preset with why, as any
setup ask does; the numbers count the pack's presets as `yardr pack
show` prints them, which each pack's own step 5 says.

The second card reads `the account GOOGLE_ACCOUNT: already given
for google-mail (GOOGLE_ACCOUNT=…, in
~/.yardr/sources/google-mail.env). Take it with --account
google-mail, or type another unseen`, answered `yardr bead answer
<bead> <ask> <n> --account google-mail` or `<n> --secret`. Taking
it points the new pack's sources at the listed file, which
`yardr source show` says; typing another writes to the new pack's
own file. There is no silent reuse, not even with a single match:
the answer names a candidate or types another. For google-docs,
whose two sources share the one account, the first card's typed
value is written once to `~/.yardr/sources/google-docs.env` and
both sources point at it, the file named on the card.

Each card asks for `GOOGLE_ACCOUNT` and never for `GOOGLE_TOKEN`,
which is minted per call, never given.

By hand the five share one file instead of one per pack. The
address is no secret, so it is typed as usual; the file still goes
0600, as every env file. A new file takes its mode from the
shell's umask, so narrow it first. Each command below is one thing
to type, in a block of its own.

```sh
umask 077
```

```sh
printf 'GOOGLE_ACCOUNT=%s\n' '<the address step 1 showed>' > ~/.yardr/sources/google.env
```

```sh
chmod 600 ~/.yardr/sources/google.env
```

```sh
for s in google-mail google-drive google-docs google-slides google-calendar; do yardr source set $s env_file=~/.yardr/sources/google.env; done
```

Run only the sources of the packs the yard uses. Either way the
file gains no second name, and a file already holding
`GOOGLE_ACCOUNT` alone needs nothing more given or set again.

A person's is one card however many sources: one line in
`~/.yardr/users/ada.env` serves every applied pack's sources. It is
asked of ada, or of the operator for her, and no `env_file` is set,
since a call `as ada` reads her file by her record. The card names
`google-mail`; with another pack alone applied it names that pack's
source instead:

```sh
yardr bead ask <bead> "Your Google account for your mail" --secret google-mail --as ada --context "The address of the login step 1 made for you: the address alone, no token." --context "The yard reads your Gmail as you once you grant it (step 5)."
```

This is `--as`, not `--for-user`: that asks for the address in a
source that tells people, and this for the credential a call made
as them reads. By hand it is typed unseen:

```sh
yardr source key google-mail --as ada
# ada gave GOOGLE_ACCOUNT · it is set in ~/.yardr/users/ada.env · yardr source check google-mail --as ada
```

The operator called as is kept apart: once he is a user (step 0),
the same card with `--as mckean` gives his account a second time
into `~/.yardr/users/mckean.env`, with its own login under step
1's directory, though it is the same address as the yard's — one
setup for the yard's calls, one for calls as him, since either may
change alone.

```sh
yardr bead ask <bead> "Your Google account, to be called as" --secret google-mail --as mckean --context "The address step 1 showed: the address alone, no token." --context "Kept apart from the yard's own: one setup for its calls, one for calls as you."
```

`yardr source show google-mail` says of each user whether their
names are set (`credential as ada: …`), and `yardr user show ada`
names the sources holding a credential of hers — never a value.

## Step 4. The mint

Nothing to install or set: each google pack brings `google-mint`,
the command that turns a login into a token per call, beside its
declarations (`credential.mint_command: [google-mint]`). Applying
the pack copies it to `~/.yardr/sources/<source>/google-mint`, mode
0700, for each of its sources, and the apply card names it with its
sha256 on the source's credential and command lines: approving the
card approves those bytes. `yardr source show google-mail` says
which mint runs (`mint: google-mint, the declaration's own …`) and
the copy's sha256.

Each pack carries its own copy, the same bytes in all four: a pack
is applied alone, and a command's program is a file inside its own
pack, so no pack depends on another being applied. A test holds the
four copies to one text.

It needs `gws` and `curl` on PATH (`GWS_BIN` names another gws).
There is no per-user mint: one command mints every account,
choosing the login by the account it is given — the default login
where it is for that account, else that account's own login in
`~/.config/gws-accounts/<account>`. It refuses an account with no
login of its own, naming the login command that makes it, and a
login for another account; it never prints the refresh token or
client secret, nor writes them anywhere. No new copy of a secret is
stored: the token is handed to each call under `GOOGLE_TOKEN` and
kept in memory alone.

Which program runs with a person's login stays the operator's:

- A pack that later brings another `google-mint` is one card again:
  a setup ask on the applied pack (`yardr bead ask <bead> "Take the
  new <pack>?" --setup <pack> --crew <name>`) shows the copy's old
  and new sha256 with the diff beside the account as it stands and
  each preset held and new, and nothing is copied until the operator
  approves it (or types `yardr pack apply` and says yes)
  ([chat.md](chat.md#setting-up-a-packs-sources)).
- A mint the operator sets by hand wins over the pack's, and
  applying the pack again keeps it:

  ```sh
  yardr source set google-mail mint_command=/Users/me/bin/my-mint
  ```

  `mint_command=` with nothing after it takes it away, and the
  pack's mints again. Setting one is the operator's alone, typed:
  no card and no agent sets it.

## Calling as a user

The yard can call as another person of the yard, with their own
credential: the yardmaster reading ada's mail, not the operator's.
It takes three things, beside the pack and its grants:

1. The person is a user of the yard (step 0), and their Google
   account has a gws login of its own (step 1), with the scopes the
   calls need. The mint reads that login for their calls and the
   machine's for the yard's.
2. Their account's address goes into their env file, not the
   source's: one `--as` card, however many sources (step 3).
3. The grant as them is theirs alone to give, on a card addressed
   to them and approved by them alone — the yardmaster asks, ada
   allows, and the operator cannot allow it for her:
   ```sh
   yardr bead ask <bead> "Let the yardmaster read your Gmail as you" --run "source grant google-mail read --crew yardmaster --as ada" --to ada --context "The yardmaster reads your mail as you when your beads need it." --context "You approve this yourself; the operator cannot approve it for you."
   ```
   `--to ada` is what addresses it to her: without it the card is
   refused. Each update call `as` her is its own card the same way,
   with the call's name from that pack's step 5 — for Gmail,
   `draft_edit` is its own grant apart from `draft`. There is no typed
   way beside these: a grant `as` ada is ada's to give, so the
   operator typing one is refused with the card that asks her. A
   plain grant covers no call as them. Anyone may take a grant
   back, as any grant.

```sh
yardr source check google-mail --as ada   # her credential, tried as a check tries the source's
yardr call google-mail search --as ada q="from:me" maxResults=5
```

How a call as a user is granted, made and counted is the same for
every source: [integrations.md](integrations.md#a-call-by-a-session-or-a-crew-member)
has it.
