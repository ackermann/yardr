# Setting up Drive

The pack wires Drive into the yard as the declared source
`google-drive`: files listed, read and shared with one address,
folders made, empty Docs and Slides made for the google-docs pack's
sources to fill, and a file's comments listed and replied to.
Each call goes out with a short-lived token minted
from a gws login on this machine, so no new copy of a secret is
stored anywhere. The login, the
account, the mint and calling as a user are the four google packs'
shared setup, said once in `yardr docs google`.

1. **The gws login.** It needs the Drive scope,
   `https://www.googleapis.com/auth/drive`, else the calls come back
   403. The shared login instructions in `yardr docs google` show how to
   check and add the `drive` service while keeping the other applied
   packs' scopes.
2. **The setup card.** One card carries the pack's whole setup: the
   pack, the account and the grants. Have a crew member ask for it,
   and approve it, in footplate or in `yardr chat` (`ctrl+o`); a
   person sets up the packs they want, one card each:
   ```sh
   yardr bead ask <bead> "Set up Drive for the yardmaster?" --setup google-drive --crew yardmaster --recommend 1 --why "reading is the job; making and sharing can follow" --context "The yardmaster lists and reads Drive files to work beads."
   ```
   The card shows the declaration whole with its mint, the account
   to type or take (step 3), and one option per preset (step 5).
   By hand it is the operator's to type, step by step, starting
   with the pack:
   ```sh
   yardr pack apply google-drive
   # applied pack google-drive to the yard: 1 created, 0 changed, 0 unchanged
   # source google-drive is declared: 8 calls under https://www.googleapis.com (yardr source show google-drive)
   ```
3. **The account.** The yard's Google account for `google-drive` is
   answered on the setup card, as described in `yardr docs google`
   (step 3): typed unseen on the first pack, then picked.
4. **The mint.** Nothing to type: the pack brings `google-mint`
   beside its declaration, and applying it (step 2) copies it for
   `google-drive`, its sha256 on the card. What it needs and refuses,
   and how a mint of the operator's own replaces it, are in
   `yardr docs google`.
5. **The grants.** A pack of kind source asks none at import, so what
   a crew member may call is picked on the setup card as one preset.
   The pack's presets, as `yardr pack show google-drive` prints them
   numbered:
   1. `read`: every read call — files and comments listed and read —
      and it covers any read call the source is later given; changes
      nothing.
   2. `make`: every read call, `folder`, `doc`, `slides` and `reply`:
      folders made, empty Docs and Slides made for the google-docs
      pack's sources to fill, replies in a file's comments; shares
      nothing.
   3. `everything`: every read call, `folder`, `doc`, `slides`,
      `share` and `reply`: what `make` grants, and shares of one
      file with any address.
   A grantee who comes later gets one preset grant card:
   ```sh
   yardr bead ask <bead> "Let the reviewers read Drive" --run "source grant google-drive --preset read --group reviewers" --context "The reviewers list and read Drive files to check a bead's work; the card names each call it covers."
   ```
   By hand they are the operator's to type:
   ```sh
   yardr source grant google-drive read --crew yardmaster
   yardr source grant google-drive folder --crew yardmaster
   yardr source grant google-drive doc --crew yardmaster
   yardr source grant google-drive slides --crew yardmaster
   yardr source grant google-drive share --crew yardmaster
   yardr source grant google-drive reply --crew yardmaster
   ```
   Sharing sends yard content to a person outside it, so `share` is
   granted by the `everything` preset alone, which the operator may
   give or withhold: without it a crew member shares nothing. The
   grant alone allows any address. The
   yardmaster's habit, not a mechanism, is to ask on a card before
   each share to a new address, naming the file and the address, and
   to leave `notify` out for quiet sharing, or pass `notify=true` when
   the person expects mail.
   As ada, the same grants with `--as ada`, on cards addressed to
   ada and approved by ada alone: the shared card is in
   `yardr docs google` ("Calling as a user"), one card per grant, with
   `read`, `folder`, `doc`, `slides`, `share` and `reply` from above. There is no
   typed way beside these: a grant `as` ada is ada's to give.
   `yardr source show google-drive` lists each call with its kind
   and who may make it, plain and `as` each user.
6. **Check it.** The drive source's check call lists the drive's
   files, minted and live:
   ```sh
   yardr source check google-drive
   # google-drive: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted, good until 16:59
   # google-drive: check call files (GET /drive/v3/files): 200 in 412ms, 1834 bytes
   ```
7. **Try it.** As the operator, who may make any call: a folder with
   an empty doc in it, which the google-docs pack's sources then
   fill:
   ```sh
   yardr call google-drive folder name="notes" parent=<a folder id>
   yardr call google-drive doc name="lunch" parent=<the folder's id>
   ```
   And a share, which is for real: the address sees the file, quiet by
   default unless `notify=true`:
   ```sh
   yardr call google-drive share fileId=<the file's id> email=<an address> role=reader
   ```
   And a file's comments, listed with their replies: resolved and
   open alike, each told by its own `resolved` flag, since Drive
   has no resolved filter:
   ```sh
   yardr call google-drive comments fileId=<the file's id>
   yardr call google-drive reply fileId=<the file's id> commentId=<a comment's id> content="Done."
   yardr call google-drive reply fileId=<the file's id> commentId=<a comment's id> content="Fixed." action=resolve
   ```
   The last reply closes the thread with it; without `action`
   the reply only answers.

Calling as a user — the yard reading another person's drive with
their own credential — is the shared setup too: `yardr docs google`
has it.

What is not there, on purpose: deleting, updating, trash; uploading
file content and attachments (multipart, which a declared source
cannot say); exporting file content (a media download, not JSON);
paging past the first page; writer or owner shares, domain or
anyone-with-link shares, and unsharing; new top-level anchored
comments (anchors are fragile), deleting comments or replies,
reopening, and hearing comments as events; and no call the pack
does not list. The yard's source format cannot express what is not
listed, and the pack does not change yardr.
