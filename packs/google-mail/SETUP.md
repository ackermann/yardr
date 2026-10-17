# Setting up Gmail

The pack wires Gmail into the yard as the declared source
`google-mail`: mail searched and read, threads read, drafts written,
nothing sent. Each call goes out with a short-lived token minted from
the operator's gws login, so no new copy of a secret is stored
anywhere. The login, the mint and calling as a user are the three
google packs' shared setup, said once in `docs/google.md`.

1. **The gws login.** It needs the Gmail scope,
   `https://www.googleapis.com/auth/gmail.modify`, else the calls
   come back 403. The shared login instructions in `docs/google.md`
   show how to check and add the `gmail` service while keeping the
   other applied packs' scopes.
2. **The pack.** It declares the source and nothing else:
   ```sh
   yardr pack apply google-mail
   # applied pack google-mail to the yard: 1 created, 0 changed, 0 unchanged
   # source google-mail is declared: 4 calls under https://gmail.googleapis.com (yardr source show google-mail)
   ```
   Or have a crew member ask for it, and approve its card, in
   footplate or in `yardr chat` (`ctrl+o`):
   ```sh
   yardr bead ask <bead> "Add the Gmail source?" --run "pack apply google-mail"
   ```
3. **The account.** Create the shared Google account file and point
   `google-mail` at it, as described in `docs/google.md`.
4. **The mint.** Install the shared `google-mint` and set it on
   `google-mail` as described in `docs/google.md`. What it needs and
   refuses is there too.
5. **The grants.** A pack of kind source asks none at import, so the
   operator grants what a crew member may call: `read` for every read
   call, each update by its name. For a crew member called
   `yardmaster`:
   ```sh
   yardr source grant google-mail read --crew yardmaster
   yardr source grant google-mail draft --crew yardmaster
   ```
   `yardr source show google-mail` lists each call with its kind and
   who may make it.
6. **Check it.** The source names no check call, so its check looks
   the credential over and sends nothing:
   ```sh
   yardr source check google-mail
   # google-mail: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted with /Users/me/bin/google-mint
   # google-mail: its file names no check call (check: <a read call without parameters>), so nothing was sent
   ```
7. **Try it.** As the operator, who may make any call: a search, a
   message read, a draft:
   ```sh
   yardr call google-mail search q="from:me" maxResults=5
   yardr call google-mail message id=<an id the search showed> format=metadata
   ```
   A draft takes the whole RFC 2822 message, base64url-encoded, with or
   without padding: headers, a blank line, the body. The yard writes the draft;
   the person presses send in Gmail:
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
