# Setting up Drive

The pack wires Drive into the yard as the declared source
`google-drive`: files listed, read and shared with one address,
folders made, empty Docs and Slides made for the google-docs pack's
sources to fill. Each call goes out with a short-lived token minted
from a gws login on this machine, so no new copy of a secret is
stored anywhere. The login, the
account, the mint and calling as a user are the three google packs'
shared setup, said once in `docs/google.md`.

1. **The gws login.** It needs the Drive scope,
   `https://www.googleapis.com/auth/drive`, else the calls come back
   403. The shared login instructions in `docs/google.md` show how to
   check and add the `drive` service while keeping the other applied
   packs' scopes.
2. **The pack.** It declares the source and nothing else. Have a crew
   member ask for it, and approve its card, in footplate or in
   `yardr chat` (`ctrl+o`); a person applies the packs they want,
   one card each:
   ```sh
   yardr bead ask <bead> "Add the Drive source?" --run "pack apply google-drive" --context "The pack declares the google-drive source and nothing else; the card carries its declaration whole."
   ```
   By hand it is the operator's to type:
   ```sh
   yardr pack apply google-drive
   # applied pack google-drive to the yard: 1 created, 0 changed, 0 unchanged
   # source google-drive is declared: 6 calls under https://www.googleapis.com (yardr source show google-drive)
   ```
3. **The account.** Give the yard's Google account for `google-drive`,
   one card, as described in `docs/google.md` (step 3).
4. **The mint.** Install the shared `google-mint` and set it on
   `google-drive` as described in `docs/google.md`. What it needs and
   refuses is there too.
5. **The grants.** A pack of kind source asks none at import, so what
   a crew member may call is granted now: `read` for every read call,
   each update by its name. Plain, a crew member asks and the operator
   approves — one card per grant. For a crew member called
   `yardmaster`:
   ```sh
   yardr bead ask <bead> "Let the yardmaster read Drive" --run "source grant google-drive read --crew yardmaster" --context "The yardmaster lists and reads Drive files to work beads; the card names each call it covers."
   yardr bead ask <bead> "Let the yardmaster make Drive folders" --run "source grant google-drive folder --crew yardmaster" --context "Folders to keep the yard's Drive files in."
   yardr bead ask <bead> "Let the yardmaster make empty Drive docs" --run "source grant google-drive doc --crew yardmaster" --context "Empty docs for the google-docs pack's sources to fill."
   yardr bead ask <bead> "Let the yardmaster make empty decks" --run "source grant google-drive slides --crew yardmaster" --context "Empty decks for the google-docs pack's sources to fill."
   yardr bead ask <bead> "Let the yardmaster share Drive files" --run "source grant google-drive share --crew yardmaster" --context "Shares one file with one address as reader or commenter; each share to a new address is asked on its own card first."
   ```
   By hand they are the operator's to type:
   ```sh
   yardr source grant google-drive read --crew yardmaster
   yardr source grant google-drive folder --crew yardmaster
   yardr source grant google-drive doc --crew yardmaster
   yardr source grant google-drive slides --crew yardmaster
   yardr source grant google-drive share --crew yardmaster
   ```
   Sharing sends yard content to a person outside it, so `share` is a
   grant the operator may give or withhold: without it a crew member
   shares nothing. The grant alone allows any address. The
   yardmaster's habit, not a mechanism, is to ask on a card before
   each share to a new address, naming the file and the address, and
   to leave `notify` out for quiet sharing, or pass `notify=true` when
   the person expects mail.
   As ada, the same grants with `--as ada`, on cards addressed to
   ada and approved by ada alone: the shared card is in
   `docs/google.md` ("Calling as a user"), one card per grant, with
   `read`, `folder`, `doc`, `slides` and `share` from above. There is no
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

Calling as a user — the yard reading another person's drive with
their own credential — is the shared setup too: `docs/google.md`
has it.

What is not there, on purpose: deleting, updating, trash; uploading
file content and attachments (multipart, which a declared source
cannot say); exporting file content (a media download, not JSON);
paging past the first page; writer or owner shares, domain or
anyone-with-link shares, and unsharing; and no call the pack does
not list. The yard's source format cannot express what is not
listed, and the pack does not change yardr.
