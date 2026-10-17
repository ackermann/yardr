# Setting up Docs and Slides

The pack wires Google Docs and Slides into the yard as the declared
sources `google-docs` (documents read and appended to) and
`google-slides` (decks read, slides with title and body appended).
Each call goes out with a short-lived token minted from the
operator's gws login, so no new copy of a secret is stored anywhere.
The login, the mint and calling as a user are the three google
packs' shared setup, said once in `docs/google.md`.

1. **The gws login.** It needs the Docs and Slides scopes,
   `https://www.googleapis.com/auth/documents` and
   `https://www.googleapis.com/auth/presentations`, else the calls
   come back 403. The shared login instructions in `docs/google.md`
   show how to check and add the `docs,slides` services while keeping
   the other applied packs' scopes.
2. **The pack.** It declares the two sources and nothing else:
   ```sh
   yardr pack apply google-docs
   # applied pack google-docs to the yard: 2 created, 0 changed, 0 unchanged
   # source google-docs is declared: 2 calls under https://docs.googleapis.com (yardr source show google-docs)
   # source google-slides is declared: 2 calls under https://slides.googleapis.com (yardr source show google-slides)
   ```
   Or have a crew member ask for it, and approve its card, in
   footplate or in `yardr chat` (`ctrl+o`):
   ```sh
   yardr bead ask <bead> "Add the Docs and Slides sources?" --run "pack apply google-docs"
   ```
3. **The account.** Create the shared Google account file and point
   `google-docs` and `google-slides` at it, as described in
   `docs/google.md`.
4. **The mint.** Install the shared `google-mint` and set it on
   `google-docs` and `google-slides` as described in `docs/google.md`.
   What it needs and refuses is there too.
5. **The grants.** A pack of kind source asks none at import, so the
   operator grants what a crew member may call: `read` for every read
   call, each update by its name. For a crew member called
   `yardmaster`:
   ```sh
   for s in google-docs google-slides; do yardr source grant $s read --crew yardmaster; done
   yardr source grant google-docs write --crew yardmaster
   yardr source grant google-slides slide --crew yardmaster
   ```
   `yardr source show google-docs` lists each call with its kind and
   who may make it.
6. **Check it.** Neither source names a check call, so each check
   looks the credential over and sends nothing:
   ```sh
   yardr source check google-docs
   # google-docs: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted with /Users/me/bin/google-mint
   # google-docs: its file names no check call (check: <a read call without parameters>), so nothing was sent
   ```
7. **Try it.** As the operator, who may make any call. A doc is an
   empty file the drive source makes, whose id this pack's docs
   source then fills, so the google-drive pack is applied first and
   tried up to its own step 7. Read the doc for its endIndex E
   first: text appends at the end, and the style lands on the range
   start and end overlap, so appending to the last paragraph styles
   with start=E-1, end=E, and a new paragraph begun with a blank
   line starts at E:
   ```sh
   yardr call google-drive folder name="notes" parent=<a folder id>
   yardr call google-drive doc name="lunch" parent=<the folder's id>
   yardr call google-docs doc docId=<the doc's id>   # its body's endIndex E
   yardr call google-docs write docId=<the id> text="Lunch places" style=HEADING_1 start=<E-1> end=<E>
   ```
   A slide is the same shape: an empty deck the drive source makes,
   then a slide with the caller's own id for it, unique in the deck:
   ```sh
   yardr call google-drive slides name="friday" parent=<a folder id>
   yardr call google-slides slide id=<the deck's id> slide=friday_1 title="Friday" body="We shipped."
   ```

Calling as a user — the yard reading another person's documents
with their own credential — is the shared setup too:
`docs/google.md` has it.

What is not there, on purpose: Sheets, which joins this pack when a
sheets source exists; Calendar and Tasks, which are no source yet,
so no pack wires them in; paging past the first page; and no call
the pack does not list. The yard's source format cannot express what
is not listed, and the pack does not change yardr.
