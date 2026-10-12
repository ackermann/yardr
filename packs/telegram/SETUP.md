# Setting up Telegram

The pack wires Telegram into the yard as the declared source `telegram`:
one message to one chat through the Bot API's sendMessage. What lands in
a user's inbox then reaches their phone, through the bot below.

1. **The bot.** In Telegram, message @BotFather `/newbot`, with a name
   and a username. It answers with the bot's token, digits, a colon and
   letters: `110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw`. It is the
   *yard's*: anyone with it sends as the bot.
2. **One message to the bot.** Open the bot and press Start, or send it
   anything. Until the person wrote to it there is no chat, and the
   yard's messages have nowhere to go.
3. **The chat id.** After step 2, with the token in place of
   `<token>`, open in a browser:
   `https://api.telegram.org/bot<token>/getUpdates`
   Under `result`, by the message just sent, is `"chat":{"id":123456789,…}`:
   that number is the chat id. It is where *the person* is reached.
4. **Apply the pack.** As the operator, with the user told through it.
   The steps use `mckean` as the user; put the told person's name there.
   ```sh
   yardr pack apply telegram --notify mckean=telegram
   # source telegram is declared: 1 call under https://api.telegram.org (yardr source show telegram)
   # user mckean: told outside the yard: through telegram, which is not set up: user mckean has no env_file: yardr user key mckean telegram gives TELEGRAM_CHAT_ID
   ```
5. **Give the yard the two secrets.** No file is written by hand: each
   command asks for its value and shows nothing as it is typed or
   pasted, and the yard's server writes it to a file only you can read
   (`~/.yardr/sources/telegram.env`, `~/.yardr/users/mckean.env`).
   ```sh
   yardr source key telegram           # TELEGRAM_BOT_TOKEN, the bot's token: the operator's to give
   yardr user key mckean telegram      # TELEGRAM_CHAT_ID, the chat id: the person's to give, or the operator's
   # user mckean: TELEGRAM_CHAT_ID is set in …/mckean.env
   ```
   Or have a crew member ask for them, and answer each on its card, in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "The Telegram bot's token" --secret telegram --context "@BotFather answered /newbot with it."
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

Mail to the user and their own timers are told from now on. `yardr user
list` shows who is told through what; `yardr user set mckean notify=`
ends it, and drops what was still waiting.
