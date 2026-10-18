# Setting up Gmail

The pack wires Gmail into the yard as the declared source
`google-mail`: mail searched and read, threads read, drafts listed,
written and edited, nothing sent. Each call goes out with a short-lived
token minted from
a gws login on this machine, so no new copy of a secret is stored
anywhere. The login, the account, the mint and calling as a user are
the four google packs' shared setup, said once in `docs/google.md`.

1. **The gws login.** It needs the Gmail scope,
   `https://www.googleapis.com/auth/gmail.modify`, else the calls
   come back 403. The shared login instructions in `docs/google.md`
   show how to check and add the `gmail` service while keeping the
   other applied packs' scopes.
2. **The pack.** It declares the source and nothing else. Have a crew
   member ask for it, and approve its card, in footplate or in
   `yardr chat` (`ctrl+o`); a person applies the packs they want,
   one card each:
   ```sh
   yardr bead ask <bead> "Add the Gmail source?" --run "pack apply google-mail" --context "The pack declares the google-mail source and nothing else; the card carries its declaration whole."
   ```
   By hand it is the operator's to type:
   ```sh
   yardr pack apply google-mail
   # applied pack google-mail to the yard: 1 created, 0 changed, 0 unchanged
   # source google-mail is declared: 6 calls under https://gmail.googleapis.com (yardr source show google-mail)
   ```
3. **The account.** Give the yard's Google account for `google-mail`,
   one card, as described in `docs/google.md` (step 3).
4. **The mint.** Nothing to type: the pack brings `google-mint`
   beside its declaration, and applying it (step 2) copies it for
   `google-mail`, its sha256 on the card. What it needs and refuses,
   and how a mint of the operator's own replaces it, are in
   `docs/google.md`.
5. **The grants.** A pack of kind source asks none at import, so what
   a crew member may call is granted now: `read` for every read call,
   each update by its name. Plain, a crew member asks and the operator
   approves — one card per grant. For a crew member called
   `yardmaster`:
   ```sh
   yardr bead ask <bead> "Let the yardmaster read Gmail" --run "source grant google-mail read --crew yardmaster" --context "The yardmaster reads mail to work beads; the card names each call it covers."
   yardr bead ask <bead> "Let the yardmaster draft Gmail" --run "source grant google-mail draft --crew yardmaster" --context "Drafts only; the person presses send in Gmail."
   yardr bead ask <bead> "Let the yardmaster edit Gmail drafts" --run "source grant google-mail draft_edit --crew yardmaster" --context "Editing drafts only, a separate grant from draft; the person presses send in Gmail."
   ```
   By hand they are the operator's to type:
   ```sh
   yardr source grant google-mail read --crew yardmaster
   yardr source grant google-mail draft --crew yardmaster
   yardr source grant google-mail draft_edit --crew yardmaster
   ```
   A grant on `draft_edit` is a separate grant from `draft`: one
   covers writing a new draft, the other replacing a draft that is
   there, and neither covers the other.
   As ada, the same grants with `--as ada`, on cards addressed to
   ada and approved by ada alone: the shared card is in
   `docs/google.md` ("Calling as a user"), one card per grant, with
   `read`, `draft` and `draft_edit` from above. There is no typed way
   beside these: a grant `as` ada is ada's to give.
   `yardr source show google-mail` lists each call with its kind and
   who may make it, plain and `as` each user.
6. **Check it.** The source names no check call, so its check looks
   the credential over and sends nothing:
   ```sh
   yardr source check google-mail
   # google-mail: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted with /Users/me/.yardr/sources/google-mail/google-mint
   # google-mail: its file names no check call (check: <a read call without parameters>), so nothing was sent
   ```
7. **Try it.** As the operator, who may make any call: a search, a
   message read, the drafts, a draft:
   ```sh
   yardr call google-mail search q="from:me" maxResults=5
   yardr call google-mail message id=<an id the search showed> format=metadata
   yardr call google-mail drafts maxResults=5
   ```
   A draft takes who it is to, its subject and its text, and builds the
   message itself; cc goes beside to, and thread answers the thread whose
   id it names. The yard writes the draft; the person presses send in Gmail:
   ```sh
   yardr call google-mail draft to=someone@example.com subject=hello body="The body."
   yardr call google-mail draft to=someone@example.com subject="Re: hello" body="On it." thread=<a thread id the search showed>
   ```
   An edit takes the id of the draft to replace, as the drafts list
   shows it, beside either shape, and replaces the draft whole:
   ```sh
   yardr call google-mail draft_edit id=<a draft id drafts showed> to=someone@example.com subject=hello body="The new body."
   ```
   And raw still takes the whole RFC 2822 message, base64url-encoded, with
   or without padding — headers, a blank line, the body — for what the
   composed shape cannot say, alone, never beside it:
   ```sh
   raw=$(printf 'From: me\nTo: someone@example.com\nSubject: hello\n\nThe body.\n' | base64 | tr -d '=\n' | tr '+/' '-_')
   yardr call google-mail draft raw="$raw"
   ```

Calling as a user — the yard reading another person's mail with their
own credential — is the shared setup too: `docs/google.md` has it.

What is not there, on purpose: sending mail (no messages.send, no
drafts.send); labels, trash and delete; and no call the pack does not
list. The yard's source format cannot express what is not listed, and
the pack does not change yardr.
