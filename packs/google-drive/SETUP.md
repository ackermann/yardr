# Setting up Drive

The pack wires Drive into the yard as the declared source
`google-drive`: files listed and read, folders made, empty Docs and
Slides made for the google-docs pack's sources to fill. Each call goes
out with a short-lived token minted from the operator's gws login, so
no new copy of a secret is stored anywhere. The login, the mint and
calling as a user are the three google packs' shared setup, said once
in `docs/google.md`.

1. **The gws login.** It needs the Drive scope,
   `https://www.googleapis.com/auth/drive`, else the calls come back
   403. The shared login instructions in `docs/google.md` show how to
   check and add the `drive` service while keeping the other applied
   packs' scopes.
2. **The pack.** It declares the source and nothing else:
   ```sh
   yardr pack apply google-drive
   # applied pack google-drive to the yard: 1 created, 0 changed, 0 unchanged
   # source google-drive is declared: 5 calls under https://www.googleapis.com (yardr source show google-drive)
   ```
   Or have a crew member ask for it, and approve its card, in
   footplate or in `yardr chat` (`ctrl+o`):
   ```sh
   yardr bead ask <bead> "Add the Drive source?" --run "pack apply google-drive"
   ```
3. **The account.** Create the shared Google account file and point
   `google-drive` at it, as described in `docs/google.md`.
4. **The mint.** Install the shared `google-mint` and set it on
   `google-drive` as described in `docs/google.md`. What it needs and
   refuses is there too.
5. **The grants.** A pack of kind source asks none at import, so the
   operator grants what a crew member may call: `read` for every read
   call, each update by its name. For a crew member called
   `yardmaster`:
   ```sh
   yardr source grant google-drive read --crew yardmaster
   yardr source grant google-drive folder --crew yardmaster
   yardr source grant google-drive doc --crew yardmaster
   yardr source grant google-drive slides --crew yardmaster
   ```
   `yardr source show google-drive` lists each call with its kind and
   who may make it.
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

Calling as a user — the yard reading another person's drive with
their own credential — is the shared setup too: `docs/google.md`
has it.

What is not there, on purpose: deleting, updating, trash; uploading
file content and attachments (multipart, which a declared source
cannot say); exporting file content (a media download, not JSON);
paging past the first page; and no call the pack does not list. The
yard's source format cannot express what is not listed, and the pack
does not change yardr.
