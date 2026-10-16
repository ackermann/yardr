# Setting up Telegram

The pack wires Telegram into the yard as the declared source `telegram`:
one message to one chat through the Bot API's sendMessage, and the
operator's chat heard back, answered with `yardr reply`. What lands in
a user's inbox then reaches their phone, through the bot below; what
the operator writes to the bot reaches the yardmaster's inbox.

1. **The bot.** In Telegram, message @BotFather with a name and a
   username, then send:
   ```text
   /newbot
   ```
   It answers with the bot's token, digits, a colon and letters:
   `110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw`. It is the *yard's*:
   anyone with it sends as the bot.
2. **One message to the bot.** From the operator's Telegram account,
   open the bot and press Start, or send it anything. Until the operator
   has written to it there is no chat, and the yard's messages have
   nowhere to go.
3. **Apply the pack.** As the operator. The steps use
   `mckean` as the operator's user name; replace it with the
   operator's user name in this yard if it differs.
   ```sh
   yardr pack apply telegram --notify mckean=telegram
   # source telegram is declared: 2 calls under https://api.telegram.org (yardr source show telegram)
   # user mckean: told outside the yard: through telegram, which is not set up: user mckean has no env_file: yardr user key mckean telegram gives TELEGRAM_CHAT_ID
   ```
   `--notify` is telling, and telling through telegram is optional:
   hearing the chat back (steps 8 on) wants only the chat id in the
   operator's env file (step 6), whatever tells them. To keep being
   told through pushover and chat through telegram, apply without it:
   ```sh
   yardr pack apply telegram
   # source telegram is declared: 2 calls under https://api.telegram.org (yardr source show telegram)
   ```
4. **Give the yard the bot's token.** No file is written by hand: the
   command asks for its value and shows nothing as it is typed or
   pasted, and the yard's server writes it to a file only you can read
   (`~/.yardr/sources/telegram.env`). The command below is one thing
   to type, in a block of its own.
   ```sh
   yardr source key telegram           # TELEGRAM_BOT_TOKEN, the bot's token: the operator's to give
   ```
   Or have a crew member ask for it, and answer on its card, in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "The Telegram bot's token" --secret telegram --context "@BotFather answered /newbot with it."
   ```
   The secret goes nowhere else: not into mail, chat, a bead or a pack.
5. **The chat id: write to the bot, and confirm it is you.** No
   number is looked up and none is pasted. Once hearing is set up
   (steps 9 to 12 below: the webhook secret, the hook and Telegram
   told where to send), write anything to the bot from the operator's
   Telegram account, as in step 2. The delivery reaches the yard from
   a chat it has no address for, so it is dropped -- and, while the
   operator has no chat id in telegram, the yard asks the operator on
   a card, in footplate or in `yardr chat`:

   > A message reached telegram from chat 123456789 (first words:
   > 'hi'). Is this you?

   The card shows the chat id and at most the first 80 characters of
   the message; the message itself is kept nowhere else. One such
   card per source waits at a time.
6. **Confirm the card, and the yard has the chat id.** Approving the
   card (`yardr bead answer <bead> <ask> --approve`, or the card's
   button in footplate) writes `TELEGRAM_CHAT_ID` into the operator's
   env file (`~/.yardr/users/mckean.env`, mode 0600), exactly where
   this command writes it:
   ```sh
   yardr user key mckean telegram      # TELEGRAM_CHAT_ID, the operator's chat id
   # user mckean: TELEGRAM_CHAT_ID is set in …/mckean.env
   ```
   That command, typed unseen, is still the way to give or replace
   the chat id by hand, and a crew member can still ask for it on a
   card, answered where the field shows dots:
   ```sh
   yardr bead ask <bead> "Your Telegram chat id" --secret telegram --for-user mckean --context "The chat id the claim card showed, or the one under result at getUpdates."
   ```
   Declining the claim card drops it and writes nothing. Once the
   chat id is written, step 7's test message can be sent, and the
   next message from that chat is heard (step 13).
7. **One test message.**
   ```sh
   yardr source check telegram --user mckean
   ```
   The phone shows "yardr: a test", and the command prints `telegram:
   told mckean: 200 in …ms`. This is one real message through Telegram,
   sent by the command itself. Anything but 200 fails the command with
   the status: 401 is a token it does not take, 400 a chat id it does
   not take or a chat that never wrote to the bot, so look at steps 1,
   2, 4 and 5 again.

Where the operator is told through telegram, mail to the user and
their own timers are told from now on. To see who is told through what:
```sh
yardr user list
```

## Hearing the operator's chat

The pack hears one chat: the operator's. A message from any other chat
is dropped — answered 200, recorded nowhere, never answered — even
where another user's env file sets the address too: hearing is the
operator's address alone, and telling another through telegram
tells them, but hears nothing back. While the operator has no chat
id set, a dropped message offers them the claim card of steps 5
and 6 instead of being dropped in silence. Write to the bot in a private
chat, one to one; if the operator's chat id is a group's, every
member of the group is heard.

The yardmaster treats a line from your chat as your word, as your mail:
it discusses, shapes, builds and lands what you ask there. A number in
the chat answers no ask: asks keep their card, told on the phone as
`yardmaster asks`, answered at the yard, in footplate or through a
device. Approving a run and giving a secret stay the operator's at the
terminal too: the yard refuses both from a chat. Pictures and files
arrive as a bracketed note (`[photo]`, `[document: <name>]`, `[voice
message]`), and nothing is fetched. There is no `thread open telegram`,
no status and no bead link to a chat.

8. **Two-step verification on the Telegram account.** Turn it on, in
   Telegram under Settings → Privacy and Security. Whoever holds the
   account writes as the operator: a taken-over account can have the
   yardmaster shape, build and land. Two-step verification adds another
   barrier to taking over the account.
9. **The webhook and its secret, on one card.** Set the yard's public
   hook address once, as the operator, to the address Telegram can
   reach this yard's hooks at (its host, on a port Telegram takes:
   443, 80, 88 or 8443):
   ```sh
   yardr settings set hook_public_url https://<his host>/hooks
   ```
   The URL of the telegram hook is then
   `https://<his host>/hooks/telegram`. A crew member then sets the
   hook up with one ask on one bead (`--run "hook add telegram
   --source telegram"`; no secret ask, and no secret typed): its card
   says it will call `POST /bot{credential}/setWebhook` at
   `https://api.telegram.org` to register that URL, and on approval
   the yard makes the signing secret (no one types it), keeps it in
   `<home>/hooks/telegram.secret` (mode 0600) which the hook names,
   and makes that call with the URL and the secret. The secret is in
   no note, event, answer or card. If the call fails the hook stays
   in place and approving again retries it. One hook serves the yard.
   `yardr hook list` shows it with STATE `ok`; step 12's `getWebhookInfo`
   shows what Telegram holds.

   By hand, the secret and the call are yours to make instead. The
   secret goes in a file, mode 0600, which the hook names, as a Linear
   signing secret does. A new file takes its mode from the shell's
   umask, so narrow it first: with the usual 022 the secret is
   readable by others until the chmod. Each command below is one thing
   to type, in a block of its own.
   ```sh
   umask 077
   ```
   ```sh
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > ~/.yardr/telegram.secret
   ```
   ```sh
   chmod 600 ~/.yardr/telegram.secret
   ```
   ```sh
   yardr hook add telegram --source telegram --secret-file ~/.yardr/telegram.secret
   ```
   The manual steps above remain available by hand; the card above
   is the way a crew member sets it up.
10. **The crew member's credential.** The yardmaster answers with the
   bot's token, as every reply is answered with the crew member's own
   credential: there is one bot for the whole yard and it speaks as the
   bot. The token is the same value the source's env file holds, in the
   crew member's env file. First note which file that is:
   ```sh
   yardr crew show yardmaster
   ```
   If the crew member has none, choose one. Appending creates the file
   where there is none, with the shell's umask for its mode, so narrow
   it first. Append the token to that file, `<that file>` below:
   ```sh
   umask 077
   ```
   ```sh
   printf 'TELEGRAM_BOT_TOKEN=%s\n' "$(grep TELEGRAM_BOT_TOKEN ~/.yardr/sources/telegram.env | cut -d= -f2-)" >> <that file>
   ```
   ```sh
   chmod 600 <that file>
   ```
   The crew member does not start unless its env file is 0600 or
   stricter. Only when it had none, name the file and restart:
   ```sh
   yardr crew set yardmaster env_file=<that file>
   ```
   ```sh
   yardr crew restart yardmaster
   ```
   Rotation rewrites both files, the source's and the crew member's.
11. **The subscription.** The yardmaster alone hears the chat, with one
    subscription. `<his chat id>` is the operator's chat id from step 5:
    ```sh
    yardr subscribe yardmaster telegram --scope all --where type=message --where chat=<his chat id>
    ```
    `all` is needed, not `involved`: the first message mentions nobody
    and is assigned to nobody. The brakeman subscribes to nothing: he
    reaches the chat through the yardmaster, as today. Set no telegram
    identity on any crew member: the API sends the bot only others'
    messages, so there is no echo of its answers to exclude. And do not
    subscribe the user himself: his own messages would arrive as
    notifications of his own acts and be told back to the phone he is
    holding.
12. **Tell Telegram where to send.** The hook card of step 9 does
    this: on approval the yard calls `setWebhook` itself with the
    public URL from `hook_public_url` and the secret it made. By hand
    it is yours instead: `<his host>` is the host his GitHub and Linear
    webhooks have, on a port Telegram takes: 443, 80, 88 or 8443. The
    bot's token is part of the address, and an argument can be read by
    every process on the machine while curl runs, and stays in the
    shell's history: the token and the webhook secret are read from
    their files and go to curl as config lines on its stdin, written by
    the shell itself. Type neither into a command.
    ```sh
    printf 'url = "https://api.telegram.org/bot%s/setWebhook"\ndata = "secret_token=%s"\n' "$(grep TELEGRAM_BOT_TOKEN ~/.yardr/sources/telegram.env | cut -d= -f2-)" "$(cat ~/.yardr/telegram.secret)" | curl -K - -d url=https://<his host>/hooks/telegram -d allowed_updates='["message"]'
    ```
    That Telegram took it, whichever way it was told:
    ```sh
    printf 'url = "https://api.telegram.org/bot%s/getWebhookInfo"\n' "$(grep TELEGRAM_BOT_TOKEN ~/.yardr/sources/telegram.env | cut -d= -f2-)" | curl -K -
    ```
13. **Try it: one line from the phone.** Write this to the bot:
    ```text
    Can the shed land on Monday?
    ```
    The yardmaster's inbox has it as a thread delivery:
    ```text
    4211 10-13 17:40  hook (all) telegram message create by mckean on "mckean": Can the shed land on Monday?
    ```
    Answer it where it came from:
    ```sh
    yardr reply 4211 "Noted; I will post here when it lands."
    ```
    The answer is a message from the bot in the same chat, and arrives
    once: the API sends the bot only others' messages, so no echo of
    the answer arrives. Three limits, where you meet them: a reply run
    again after its answer was lost posts twice, since the service
    takes no idempotency key; `yardr thread show` on a telegram delivery
    shows nothing, since a bot cannot read a chat's history — the
    delivery's own text is in the inbox line and the raw body in `yardr
    hook show <seq>`; and `yardr reply --to` is refused for telegram,
    since it finds the conversation by listing the thread.
14. **Ending hearing.** `deleteWebhook` ends hearing while leaving
    telling as it was:
    ```sh
    printf 'url = "https://api.telegram.org/bot%s/deleteWebhook"\n' "$(grep TELEGRAM_BOT_TOKEN ~/.yardr/sources/telegram.env | cut -d= -f2-)" | curl -K -
    ```
    So does taking the address out of the operator's env file: delete
    the `TELEGRAM_CHAT_ID` line from `~/.yardr/users/mckean.env` (the
    operator's user name from step 3), and the chat is heard no more.
    Setting the operator's `notify` to empty ends telling alone now:
    hearing follows the address, not `notify`, so the chat is heard
    while the address is set, whatever the operator is told through.
