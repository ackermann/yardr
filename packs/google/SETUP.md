# Setting up Google

The pack wires Google into the yard as four declared sources, one per
host: `google-mail` (Gmail read and drafts, never send), `google-drive`
(Drive files, folders and empty Docs and Slides), `google-docs` (Docs
read and appended to) and `google-slides` (decks read, slides with
title and body appended). Each call goes out with a short-lived token
minted from the operator's gws login, so no new copy of a secret is
stored anywhere.

1. **The gws login.** The machine holds it: gws 0.22.5 at
   `/opt/homebrew/bin/gws`, its config in `~/.config/gws`. The account
   it is logged in as is what the sources mint from:
   ```sh
   gws auth status
   ```
   Its `user` is the account's address, which step 3 sets as
   `GOOGLE_ACCOUNT`. Its `scopes` must hold the four the pack calls
   with — `https://www.googleapis.com/auth/gmail.modify`,
   `https://www.googleapis.com/auth/drive`,
   `https://www.googleapis.com/auth/documents` and
   `https://www.googleapis.com/auth/presentations` — else the calls
   come back 403. A login without one is made again with the services
   it lacks:
   ```sh
   gws auth login -s gmail,drive,docs,slides
   ```
2. **The pack.** It declares the four sources and nothing else:
   ```sh
   yardr pack apply google
   # applied pack google to the yard: 4 created, 0 changed, 0 unchanged
   # source google-mail is declared: 4 calls under https://gmail.googleapis.com (yardr source show google-mail)
   # ...
   ```
3. **The account.** One env file holds it for all four sources. The
   address is no secret, so it is typed as usual; the file still goes
   0600, as every env file. A new file takes its mode from the shell's
   umask, so narrow it first:
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
   for s in google-mail google-drive google-docs google-slides; do yardr source set $s env_file=~/.yardr/sources/google.env; done
   ```
4. **The mint.** The script turns the login into a token per call. Put
   it where the yard runs, from `main`, not a checkout, so a branch
   cannot change how the token is minted, and set it on each source:
   ```sh
   install contrib/google-mint/google-mint ~/bin/    # from main
   for s in google-mail google-drive google-docs google-slides; do yardr source set $s mint_command=$HOME/bin/google-mint; done
   # source google-mail: mint command /Users/me/bin/google-mint
   # ...
   ```
   It needs `gws` and `curl` on PATH (`GWS_BIN` names another gws).
   It refuses when the gws login is for another account than
   `GOOGLE_ACCOUNT`, and it never prints the refresh token or client
   secret, nor writes them anywhere.
5. **The grants.** A pack of kind source asks none at import, so the
   operator grants what a crew member may call: `read` for every read
   call, each update by its name. For a crew member called
   `yardmaster`:
   ```sh
   for s in google-mail google-drive google-docs google-slides; do yardr source grant $s read --crew yardmaster; done
   yardr source grant google-mail draft --crew yardmaster
   yardr source grant google-drive folder --crew yardmaster
   yardr source grant google-drive doc --crew yardmaster
   yardr source grant google-drive slides --crew yardmaster
   yardr source grant google-docs write --crew yardmaster
   yardr source grant google-slides slide --crew yardmaster
   ```
   `yardr source show google-mail` lists each call with its kind and
   who may make it.
6. **Check it.** The drive source's check call lists the drive's files,
   minted and live; the other three name no check call, so theirs looks
   the credential over and sends nothing:
   ```sh
   yardr source check google-drive
   # google-drive: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted, good until 16:59
   # google-drive: check call files (GET /drive/v3/files): 200 in 412ms, 1834 bytes
   yardr source check google-mail
   # google-mail: credential: GOOGLE_ACCOUNT is set in /Users/me/.yardr/sources/google.env, minted with /Users/me/bin/google-mint
   # google-mail: its file names no check call (check: <a read call without parameters>), so nothing was sent
   ```
7. **Try it.** As the operator, who may make any call: a search, a
   message read, a draft, a folder with a doc in it that is then
   written:
   ```sh
   yardr call google-mail search q="from:me" maxResults=5
   yardr call google-mail message id=<an id the search showed> format=metadata
   ```
   A draft takes the whole RFC 2822 message, base64url-encoded without
   padding: headers, a blank line, the body. The yard writes the draft;
   the person presses send in Gmail:
   ```sh
   raw=$(printf 'From: me\nTo: someone@example.com\nSubject: hello\n\nThe body.\n' | base64 | tr '+/' '-_' | tr -d '=')   # GNU base64 wraps: base64 -w0 there
   yardr call google-mail draft raw="$raw"
   ```
   A doc is an empty file the drive source makes, whose id the docs
   source then fills. Read it for its endIndex E first: text appends
   at the end, and the style lands on the range start and end overlap,
   so appending to the last paragraph styles with start=E-1, end=E,
   and a new paragraph begun with a blank line starts at E:
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

What is not there, on purpose: sending mail (no messages.send, no
drafts.send); labels, trash and delete; Calendar, Sheets and Tasks;
uploading file content and attachments (multipart, which a declared
source cannot say); paging past the first page; and no call the pack
does not list. The yard's source format cannot express what is not
listed, and the pack does not change yardr.
