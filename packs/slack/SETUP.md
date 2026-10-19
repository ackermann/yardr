# Setting up Slack

The pack wires Slack into the yard as the declared source `slack`: the
mentions of a bot in a workspace and its one-to-one direct messages,
heard through the Events API and answered in their threads with `yardr
reply`. A mention in a channel the bot is invited into lands in the
yardmaster's inbox, as does a mention in a group direct message the bot
is part of; a one-to-one direct message lands whole, mentioned or not.
What lands is answered where it stood, in its thread.

The steps below are the terminal way. The steps use `mckean` as the
operator's user name; replace it with the operator's user name in this
yard if it differs.

1. **The app, from the pack's manifest, in a browser.** At
   api.slack.com/apps, Create New App, From a manifest: paste
   `packs/slack/app-manifest.yaml` from a checkout of yardr (or from the
   pack as the yard installed it). The manifest names the bot user and
   the scopes (`app_mentions:read`, `channels:history`,
   `groups:history`, `im:history`, `mpim:history`, `chat:write` — the
   four histories are bot scopes, and they are what
   `conversations.replies` reads a thread back with on the bot token in
   each kind of conversation; `mpim:history` in particular is a reading
   scope only: it lets a heard group thread be read back, and no
   `message.mpim` event is ever subscribed, so it widens nothing that is
   heard), and carries no event subscriptions and no Request URL. A
   manifest that names events without a Request URL does not validate,
   so the events cannot stand in the document the app is created from
   while its hook address is not yet known; they are added in step 6,
   once the hook stands and Slack can verify its address. Install the
   app to the workspace. Two values Slack shows are copied from there
   into the steps below: the signing secret (Basic Information) and,
   after install, the bot token `xoxb-…` (OAuth & Permissions). Making
   the app and installing it are the operator's in Slack's pages; no
   terminal is involved.
2. **Apply the pack.** As the operator:
   ```sh
   yardr pack apply slack
   # source slack is declared: 2 calls under https://slack.com/api (yardr source show slack)
   ```
3. **Give the yard the bot's token.** No file is written by hand: the
   command asks for its value and shows nothing as it is typed or
   pasted, and the yard's server writes it to a file only you can read
   (`~/.yardr/sources/slack.env`). The command below is one thing
   to type, in a block of its own.
   ```sh
   yardr source key slack           # SLACK_BOT_TOKEN, the bot's token: the operator's to give
   ```
   The secret goes nowhere else: not into mail, chat, a bead or a pack.
4. **Whose token it is.**
   ```sh
   yardr source check slack
   ```
   prints the selected fields of `auth.test`, one per line under the
   status line:
   ```text
   slack: check call whoami (POST /auth.test): 200 in …ms, … bytes
   slack: user_id: U…
   slack: team_id: T…
   slack: bot_id: B…
   ```
   The first is the id step 7 sets, a token Slack refuses fails the
   check here rather than at the first mention, and a check that prints
   no `user_id` line has not shown whose token it holds: setup stops
   there rather than guessing an id from elsewhere.
5. **The public hook address.** Set the yard's public hook address once,
   as the operator, to the address that reaches this yard's hooks:
   ```sh
   yardr settings set hook_public_url https://<his host>/hooks
   ```
   The URL of the slack hook is then
   `https://<his host>/hooks/slack`. The address reaching the yard — a
   tunnel or funnel in front of `hook_addr` — is networking outside
   yardr, the operator's, and is the one piece of plumbing no command
   carries.
6. **The signing secret and the hook, then the events in the browser.**
   The secret goes in a file, mode 0600, which the hook names. A new
   file takes its mode from the shell's umask, so narrow it first. The
   secret is never a command's word: that line would stay in the
   shell's history. Each command below is one thing to type, in a block
   of its own; run the second, type or paste the secret blind and press
   Enter:
   ```sh
   umask 077
   ```
   ```sh
   IFS= read -rs v && printf '%s' "$v" > ~/.yardr/slack.secret; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/slack.secret
   ```
   ```sh
   yardr hook add slack --source slack --secret-file ~/.yardr/slack.secret
   ```
   Unlike telegram there is no registration call for the yard to make:
   Slack takes its Request URL only in its own configuration, so the
   hook stands the endpoint up and nothing is sent anywhere. Then, in
   the browser, the operator opens the app's Event Subscriptions, turns
   it on, pastes the hook's URL (`https://<his host>/hooks/slack`) as
   the Request URL, and subscribes to the two bot events, exactly these
   two:
   ```text
   app_mention
   message.im
   ```
   Never subscribe `message.mpim`: Slack delivers it for every group
   message, mentioned or not, so the yard would receive whole group
   conversations it was never addressed in, and each group mention
   would arrive a second time beside its `app_mention`. With the two
   above, a group direct message is heard only when it mentions the
   bot, by its `app_mention`, while a one-to-one direct message is
   heard whole. Slack sends a `url_verification` challenge to the URL
   when it is saved; the yard answers it from the hook just made, and
   the app shows verified. This is the visit the manifest of step 1
   deliberately left undone: only now does the app have a reachable
   Request URL to name beside its events, so the subscriptions
   validate. This paste is a step Slack leaves no API for; it is in a
   browser, not a terminal.
7. **The bot's identity.** As the operator, with the `user_id` step 4
   printed, so `@bot` is a mention of the yardmaster:
   ```sh
   yardr crew identity yardmaster --source slack --id <user_id from step 4>
   ```
8. **The yardmaster's own credential, for answering.** `yardr reply`
   speaks with the answering crew member's copy of the bot token. First
   note which file that is:
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
   printf 'SLACK_BOT_TOKEN=%s\n' "$(grep SLACK_BOT_TOKEN ~/.yardr/sources/slack.env | cut -d= -f2-)" >> <that file>
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
   The running session does not see the new value: it takes effect
   when it next starts, so restart it even where the file was already
   named. Rotation rewrites both files, the source's and the crew
   member's.
9. **The subscription, and an invitation.** The yardmaster subscribes
   itself from its pane — an agent step, not an operator terminal —
   to mentions, and to one-to-one direct messages whole:
   ```sh
   yardr subscribe yardmaster slack --scope mentions --where type=message
   ```
   ```sh
   yardr subscribe yardmaster slack --scope all --where type=message --where room=dm
   ```
   `mentions`, not telegram's `all`: a workspace is many channels of
   other people's talk, and the bot is woken by being addressed, not
   by everything said where it stands. The operator then invites the
   bot into each channel it should hear (`/invite @<bot>` in Slack),
   without which Slack sends the channel nothing. A first mention is
   then the test: it lands in the yardmaster's inbox as
   ```text
   5210 10-17 18:02  hook (mentioned) slack message create by ada (U012AB3CD) on "C012AB3CD:1729156800.000100": @yardr can the shed land on Monday?
   ```
   and `yardr reply 5210 "Noted; I will post here when it lands."`
   answers in that thread. The thread is read back with `yardr thread
   show 5210` on this channel delivery too — the bot belongs to the
   channel it was invited into, and its token holds `channels:history`
   — and `yardr hook show 5210` holds the delivery itself meanwhile.
   Two companion checks pin the group boundary in the same walk: a
   message mentioning the bot in a group direct message the bot is
   part of lands in the inbox once, as a `mentioned` delivery, and a
   group message mentioning nobody lands nowhere — Slack sends no
   event for it under step 6's two subscriptions — while a
   one-to-one direct message mentioning nobody still lands whole by
   the `room=dm` subscription.

Who is heard is what the subscriptions above say. Matching a speaker
to a user of the yard is a later step, not this walk: until it lands,
every mention and direct message the yard is subscribed to is heard
alike. Three limits, where you meet them: a reply run again after its
answer was lost posts twice, since the service takes no idempotency
key; `yardr thread show` on a delivery whose conversation the bot has
left, or whose kind its token has no history scope for, fails with
Slack's reason rather than showing an empty thread; and `yardr reply
--to` stands with `thread show`, since it finds the message to answer
under by listing the thread. The bot's own posts name it as actor
where Slack sends them back, and never reach its inbox.
