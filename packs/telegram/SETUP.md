# Setting up Telegram

The pack wires Telegram into the yard as the declared source `telegram`:
one message to one chat through the Bot API's sendMessage, and the
operator's chat heard back, answered with `yardr reply`. What lands in
a user's inbox then reaches their phone, through the bot below; what
the operator writes to the bot reaches the yardmaster's inbox.

1. **The bot.** In Telegram, message @BotFather `/newbot`, with a name
   and a username. It answers with the bot's token, digits, a colon and
   letters: `110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw`. It is the
   *yard's*: anyone with it sends as the bot.
2. **One message to the bot.** From the operator's Telegram account,
   open the bot and press Start, or send it anything. Until the operator
   has written to it there is no chat, and the yard's messages have
   nowhere to go.
3. **The chat id.** After step 2, with the token in place of
   `<token>`, open this in a browser:
   ```text
   https://api.telegram.org/bot<token>/getUpdates
   ```
   Under `result`, by the operator's message, is
   `"chat":{"id":123456789,…}`: that number is the operator's chat id.
   It is where the operator is reached.
4. **Apply the pack.** As the operator, with the operator told through
   it. The steps use `mckean` as the operator's user name; replace it
   with the operator's user name in this yard if it differs.
   ```sh
   yardr pack apply telegram --notify mckean=telegram
   # source telegram is declared: 1 call under https://api.telegram.org (yardr source show telegram)
   # user mckean: told outside the yard: through telegram, which is not set up: user mckean has no env_file: yardr user key mckean telegram gives TELEGRAM_CHAT_ID
   ```
5. **Give the yard the two secrets.** No file is written by hand: each
   command asks for its value and shows nothing as it is typed or
   pasted, and the yard's server writes it to a file only you can read
   (`~/.yardr/sources/telegram.env`, `~/.yardr/users/mckean.env`). Each
   command below is one thing to type, in a block of its own.
   ```sh
   yardr source key telegram           # TELEGRAM_BOT_TOKEN, the bot's token: the operator's to give
   ```
   ```sh
   yardr user key mckean telegram      # TELEGRAM_CHAT_ID, the operator's chat id
   # user mckean: TELEGRAM_CHAT_ID is set in …/mckean.env
   ```
   Or have a crew member ask for them, and answer each on its card, in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "The Telegram bot's token" --secret telegram --context "@BotFather answered /newbot with it."
   ```
   ```sh
   yardr bead ask <bead> "Your Telegram chat id" --secret telegram --for-user mckean --context "The chat id under result at getUpdates."
   ```
   The secrets go nowhere else: not into mail, chat, a bead or a pack.
6. **One test message.**
   ```sh
   yardr source check telegram --user mckean
   ```
   The phone shows "yardr: a test", and the command prints `telegram:
   told mckean: 200 in …ms`. This is one real message through Telegram,
   sent by the command itself. Anything but 200 fails the command with
   the status: 401 is a token it does not take, 400 a chat id it does
   not take or a chat that never wrote to the bot, so look at steps 1
   to 3 again.

Mail to the user and their own timers are told from now on. To see who
is told through what:
```sh
yardr user list
```

## Hearing the operator's chat

The pack hears one chat: the operator's. A message from any other chat
is dropped — answered 200, recorded nowhere, never answered — even
where another user is told through telegram too: telling another
through telegram tells them, but hears nothing back. Write to the bot
in a private chat, one to one; if the operator's chat id is a group's,
every member of the group is heard.

The yardmaster treats a line from your chat as your word, as your mail:
it discusses, shapes, builds and lands what you ask there. A number in
the chat answers no ask: asks keep their card, told on the phone as
`yardmaster asks`, answered at the yard, in footplate or through a
device. Approving a run and giving a secret stay the operator's at the
terminal too: the yard refuses both from a chat. Pictures and files
arrive as a bracketed note (`[photo]`, `[document: <name>]`, `[voice
message]`), and nothing is fetched. There is no `thread open telegram`,
no status and no bead link to a chat.

7. **Two-step verification on the Telegram account.** Turn it on, in
   Telegram under Settings → Privacy and Security. Whoever holds the
   account writes as the operator: a taken-over account can have the
   yardmaster shape, build and land. Two-step verification adds another
   barrier to taking over the account.
8. **The webhook secret.** The yard never registers a webhook: the
   secret and the call below are yours to make. The secret goes in a
   file, mode 0600, which the hook names, as a Linear signing secret
   does. Each command below is one thing to type, in a block of its own.
   ```sh
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > ~/.yardr/telegram.secret
   ```
   ```sh
   chmod 600 ~/.yardr/telegram.secret
   ```
   ```sh
   yardr hook add telegram --source telegram --secret-file ~/.yardr/telegram.secret
   ```
   One hook serves the yard. `yardr hook list` shows it with STATE `ok`.
9. **The crew member's credential.** The yardmaster answers with the
   bot's token, as every reply is answered with the crew member's own
   credential: there is one bot for the whole yard and it speaks as the
   bot. The token is the same value the source's env file holds, in the
   crew member's env file. First note which file that is:
   ```sh
   yardr crew show yardmaster
   ```
   If the crew member has none, choose one. Append the token to that
   file, `<that file>` below:
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
10. **The subscription.** The yardmaster alone hears the chat, with one
    subscription. `<his chat id>` is the operator's chat id from step 3:
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
11. **Tell Telegram where to send.** `<token>` is the bot's token of step
    1, `<his host>` the host his GitHub and Linear webhooks have, on a
    port Telegram takes: 443, 80, 88 or 8443.
    ```sh
    curl https://api.telegram.org/bot<token>/setWebhook -d url=https://<his host>/hooks/telegram -d secret_token="$(cat ~/.yardr/telegram.secret)" -d allowed_updates='["message"]'
    ```
    That Telegram took it:
    ```sh
    curl https://api.telegram.org/bot<token>/getWebhookInfo
    ```
    The yard never calls `setWebhook` itself.
12. **Try it: one line from the phone.** Write this to the bot:
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
13. **Ending hearing.** `deleteWebhook` ends hearing while leaving
    telling as it was:
    ```sh
    curl https://api.telegram.org/bot<token>/deleteWebhook
    ```
    Setting the operator's `notify` to empty disconnects the phone both
    ways with one switch: telling ends, and with it hearing, since the
    chat is heard while the operator is told through telegram. Use the
    operator's user name from step 4:
    ```sh
    yardr user set mckean notify=
    ```
