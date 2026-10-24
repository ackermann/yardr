# Setting up Docs and Slides

The pack wires Google Docs and Slides into the yard as the declared
sources `google-docs` (documents read, appended to and replaced in
place) and `google-slides` (decks read, slides with title and body
appended).
Each call goes out with a short-lived token minted from a gws login
on this machine, so no new copy of a secret is stored anywhere.
The login, the account, the mint and calling as a user are the four
google packs' shared setup, said once in `yardr docs google`.

1. **The gws login.** It needs the Docs and Slides scopes,
   `https://www.googleapis.com/auth/documents` and
   `https://www.googleapis.com/auth/presentations`, else the calls
   come back 403. The shared login instructions in `yardr docs google`
   start from a machine with no gws, and show how to check and add
   the `docs,slides` services while keeping the other applied packs'
   scopes.
2. **The setup card.** One card carries the pack's whole setup: the
   pack, the account and the grants. Have a crew member ask for it,
   and approve it, in footplate or in `yardr chat` (`ctrl+o`); a
   person sets up the packs they want, one card each:
   ```sh
   yardr bead ask <bead> "Set up Docs and Slides for the yardmaster?" --setup google-docs --crew yardmaster --recommend 1 --why "reading is the job; appending can follow" --context "The yardmaster reads docs and decks to work beads."
   ```
   The card shows both declarations whole with their mint, the
   account to type or take (step 3), and one option per preset
   (step 5).
   By hand it is the operator's to type, step by step, starting
   with the pack:
   ```sh
   yardr pack apply google-docs
   # applied pack google-docs to the yard: 2 created, 0 changed, 0 unchanged
   # source google-docs is declared: 3 calls under https://docs.googleapis.com (yardr source show google-docs)
   # source google-slides is declared: 2 calls under https://slides.googleapis.com (yardr source show google-slides)
   ```
3. **The account.** The yard's Google account for `google-docs`
   and `google-slides` is answered on the setup card, as described
   in `yardr docs google` (step 3): typed unseen on the first pack,
   then picked, one typed value serving both sources.
4. **The mint.** Nothing to type: the pack brings `google-mint`
   beside its declaration, and applying it (step 2) copies it for
   `google-docs` and `google-slides`, its sha256 on the card. What it needs and refuses,
   and how a mint of the operator's own replaces it, are in
   `yardr docs google`.
5. **The grants.** A pack of kind source asks none at import, so what
   a crew member may call is picked on the setup card as one preset.
   The pack's presets, as `yardr pack show google-docs` prints them
   numbered, grant both sources together:
   1. `read`: every read call of `google-docs` and `google-slides`,
      and it covers any read call either source is later given;
      changes nothing.
   2. `append`: every read call of both, `write` on `google-docs`
      and `slide` on `google-slides`: paragraphs appended at the end
      of a doc, each with its style, and slides appended to a deck,
      with title and body; replaces nothing.
   3. `everything`: what `append` grants, and `replace` on
      `google-docs`: text replaced in place, more than one match
      refused unless the call passes all=true.
   A grantee who comes later gets one preset grant card per source:
   ```sh
   yardr bead ask <bead> "Let the reviewers read Docs" --run "source grant google-docs --preset read --group reviewers" --context "The reviewers read docs to check a bead's work; the card names each call it covers."
   yardr bead ask <bead> "Let the reviewers read Slides" --run "source grant google-slides --preset read --group reviewers" --context "The reviewers read decks to check a bead's work; the card names each call it covers."
   ```
   By hand they are the operator's to type:
   ```sh
   for s in google-docs google-slides; do yardr source grant $s read --crew yardmaster; done
   yardr source grant google-docs write --crew yardmaster
   yardr source grant google-docs replace --crew yardmaster
   yardr source grant google-slides slide --crew yardmaster
   ```
   As ada, the same grants with `--as ada`, on cards addressed to
   ada and approved by ada alone: the shared card is in
   `yardr docs google` ("Calling as a user"), one card per grant, with
   `read`, `write`, `replace` and `slide` from above. There is no typed way
   beside these: a grant `as` ada is ada's to give.
   `yardr source show google-docs` lists each call with its kind and
   who may make it, plain and `as` each user.
6. **Check it.** Neither source names a check call, so each check
   looks the credential over and sends nothing:
   ```sh
   yardr source check google-docs
   # google-docs: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted with /Users/me/.yardr/sources/google-docs/google-mint
   # google-docs: its file names no check call (check: <a read call without parameters>), so nothing was sent
   ```
7. **Try it.** As the operator, who may make any call. A doc is an
   empty file the drive source makes, whose id this pack's docs
   source then fills, so the google-drive pack is applied first and
   tried up to its own step 7. One write appends several paragraphs
   in one call, each with its style: the call reads the doc for its
   end index itself and works out each paragraph's range, and the
   first paragraph continues the doc's last paragraph:
   ```sh
   yardr call google-drive folder name="notes" parent=<a folder id>
   yardr call google-drive doc name="lunch" parent=<the folder's id>
   yardr call google-docs write docId=<the doc's id> paragraphs='[{"text":"Lunch places","style":"HEADING_1"},{"text":"Friday we ship.","style":"NORMAL_TEXT"}]'
   ```
   Text is changed in place with replace, which counts its matches
   first: more than one is refused unless the call passes all=true, and
   the answer says how many changed.
   ```sh
   yardr call google-docs replace docId=<the doc's id> find="Friday" with="Saturday"
   ```
   A slide is the same shape: an empty deck the drive source makes,
   then a slide with the caller's own id for it, unique in the deck:
   ```sh
   yardr call google-drive slides name="friday" parent=<a folder id>
   yardr call google-slides slide id=<the deck's id> slide=friday_1 title="Friday" body="We shipped."
   ```

Calling as a user — the yard reading another person's documents
with their own credential — is the shared setup too:
`yardr docs google` has it.

What is not there, on purpose: Sheets, which joins this pack when a
sheets source exists; Tasks, which is no source yet, so no pack
wires it in (Calendar is the google-calendar pack); paging past the
first page; and no call the pack does not list. The yard's source
format cannot express what is not listed, and the pack does not
change yardr.
