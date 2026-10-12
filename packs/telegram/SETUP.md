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
4. **Each secret in a file of its own.** Two files that only you can
   read, and nowhere else: not into mail, chat, a bead or a pack. The
   steps use `mckean` as the user; put the told person's name there.
   ```sh
   mkdir -p ~/.yardr/sources ~/.yardr/users
   printf 'TELEGRAM_BOT_TOKEN=%s\n' 'PASTE-THE-BOT-TOKEN' > ~/.yardr/sources/telegram.env
   printf 'TELEGRAM_CHAT_ID=%s\n' 'PASTE-THE-CHAT-ID' > ~/.yardr/users/mckean.env
   chmod 600 ~/.yardr/sources/telegram.env ~/.yardr/users/mckean.env
   ```
   A file that others can read is refused when it is set, with the
   `chmod` that fixes it.
5. **Apply the pack.** As the operator, with the user told through it:
   ```sh
   yardr pack apply telegram --source-env telegram=~/.yardr/sources/telegram.env --notify mckean=telegram,~/.yardr/users/mckean.env
   # source telegram is declared: 1 call under https://api.telegram.org (yardr source show telegram)
   # user mckean: told outside the yard: through telegram (TELEGRAM_CHAT_ID is set in …/mckean.env)
   ```
   The same two settings on their own are `yardr source set telegram
   env_file=…` and `yardr user set mckean notify=telegram env_file=…`.
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
