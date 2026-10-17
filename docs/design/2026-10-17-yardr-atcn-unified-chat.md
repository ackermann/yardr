# One chat in footplate: mail and Telegram (later Slack) as one conversation with each person

Design yardr-atcn, proposed 2026-10-17. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. The crew are the yard's always-on agents, tied to no bead; the yardmaster is the crew member that coordinates. A user is a person of the yard: a name and an inbox. The operator is the person who runs the yard. A source is an outside system the yard hears from and tells through. A hook is a webhook endpoint that turns an outside system's deliveries into events. A thread is a conversation in a source that a crew member takes part in. A conversation, in this document, is the exchange between one user and one crew member that `yardr mail --with <crew member>`, `yardr chat` and footplate's Chat all show. Mail is the record that conversation is made of today: one `mail` event per message, between two parties of the yard. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 17 Oct 2026 (Telegram, after the trial yardr-b6sq): "I'm scared this could go over board, but then again it could be a nice way to tie things together, unified chat. maybe design bead? keeping it clean simple and streamlined?" Today (checked 17 Oct 11:31) footplate's Chat shows mail only (`yardr mail --with`). A Telegram message from mckean is kept as a hook event (`yardr hook show <seq>`) and reaches the crew's inbox; the crew's `yardr reply` is recorded as a `replied` event with the message id, not its text. A bot can read no chat history (`yardr thread show`: 0 messages). So the two halves of one conversation live in different places, and only one of them is in footplate. This design says how the Telegram half joins the conversation the person already sees, in the smallest way that holds, and what stays out.

## 2. What exists today

### 2.1. Mail is the conversation, and one record carries it

Mail is written by `Store.Mail` in internal/store/crew.go: one event of kind `mail` with `to`, `text`, optional `files`, and `to_user` when the receiver is a user, indexed in the inbox bucket under the receiver's name. A user's device writing as them marks the event `via` with the device's name (`WithVia`, `viaOf` and `ViaMark` in internal/store/via.go: "mckean (via phone)"); it is a record of the way in, and nothing is decided by it (docs/users.md, "A write says how it came").

Three readers show the same record. `Store.Conversation` in internal/store/users.go returns the mail between two parties, both directions, oldest first, paged from the newest end; it reads the inbox index of the two parties and, for a user party, also the mail of names of linked yards trusted as that user (`historyOf` in the same file). `Store.UserInbox` returns a user's unread mail, its count and who it is from. What a reader sees is a `Letter` (internal/store/users.go): `seq`, `at`, `from`, `to`, `text`, `read`, `timer`, `delivery`, `as`, `via`, `files`. `read` comes from a cursor: a user's `Cursor` or a crew member's inbox cursor (the conversation slice's cursor where the crew member serves that user; `readers.letter` and `sliceCursor` in internal/store/users.go and internal/store/crew.go), advanced by `AckUserInbox` and `AckInbox`. `yardr mail --with <name>` (the `conversation` function in internal/cli/users.go) prints each letter with its state and `ViaMark`; `yardr chat` (internal/cli/chat.go, internal/chat) reads with `Conversation` and sends with `Mail`, and opening a conversation marks its mail read.

Footplate's Chat is the same record over the yard's API. The client's `conversation` and `conversation_page` in footplate's src/api.rs call `Conversation` with `a`, `b`, `after` (and `limit`/`before` for paging, falling back to the whole conversation where a yard refuses the new arguments), and its `Letter` struct holds `seq`, `at`, `from`, `to`, `text`, `read`, `files` and `delivery`. Note what it does not hold: `via` and `as` never reach footplate today, so no way-in mark is shown there. The rail's Chat badge counts the user's unread mail (`Inbox.unread` in src/api.rs, shown by `list_chat` and the rail in src/window.rs and src/model.rs; the counting itself is footplate-608q's subject on the footplate side). A change to what a `Letter` is stays additive where a program reads it: a new field with a default, never a renamed or removed one (mem-kf2y).

### 2.2. Telegram hears one person, and the answer is kept without its words

The telegram pack (packs/telegram/telegram.yaml, kind `source`) declares one source with three parts that matter here. `notify` names the call `message` (a `sendMessage`), the parameter `chat` that takes where the person is reached, and the variable `TELEGRAM_CHAT_ID` that holds it in the person's own env file. `hear` names the envelope field `chat` and the same variable, narrowed with `from: operator`: only the operator's chat is heard. `thread` names `reply`, `posted` and `read` programs (packs/telegram/telegram/thread), and `hook` verifies each delivery by the secret token in its header (scheme `secret`) and normalizes it with packs/telegram/telegram/normalize.

A delivery travels one path. `Receiver.serve` in internal/hooks/receiver.go verifies it, normalizes it, then drops it when the source hears and the address matches no user (`hears` in the same file, reading each user's env file as telling reads it, and only the operator's when the declaration says `from: operator`): answered 200, recorded nowhere, counted beside refusals, with a claim offered to the operator for the address it came from (`ClaimAddress` in internal/source/notify.go and internal/store/claim.go). A kept delivery is recorded by `Store.RecordHook` in internal/store/hooks.go; `delivered` in the same file works out which crew members and users it reaches (`hookDeliveries`), records thread participation, logs one `hook` event holding the whole envelope, and keeps the raw payload. The normalize program truncates the envelope's `text` to 500 runes and turns a message with no text into a bracketed note of what arrived: `[photo]`, `[document: <name>]`, `[voice message]`, and the like. There is no attachment pipeline for hook events; nothing is fetched.

The crew member hears the delivery in its inbox as a `hook` event (`forCrew` in internal/store/crew.go, by the subscriptions on the event) and answers with `yardr reply <seq>`. The command (internal/cli/reply.go) first records the answer with `Store.Replying` in internal/store/threads.go: a `Reply` record under a key chosen now, identified by the hash of the conversation and the body, `Attempts` counted up, written before anything is posted. It then asks the source what the key already posted (`Posted`), posts through the pack's `thread.reply`, and completes the record with `Store.Replied`: the `Reply` gains the source, the thread, the source's id of the message (`Message`) and `Posted`, a `replied` event goes on the crew member's journal with `crew`, `source`, `thread`, `url`, `title`, `message` and `seq`, and the crew member takes part in the thread (`ParticipantReplied`).

Two gaps in that record are the heart of this bead. First, the words are kept nowhere: `Reply` has no body field, the `replied` event has no text, and the envelope's 500-rune `text` is the only copy of what the person wrote. Second, Telegram gives no second chance to read them back: the Bot API gives a bot no chat history, so the pack's `thread.read` answers `{messages: []}` always and `yardr thread show` on a Telegram delivery shows nothing, and it takes no idempotency key, so `thread.posted` answers `{}` always and a reply retried after its answer was lost posts twice (the design docs/design/2026-10-13-yardr-c55e-telegram-hears.md, sections 2.6 and 3.4, records both). The bot is never sent its own `sendMessage`, so no webhook echo of an answer exists. The trial yardr-b6sq ran this end to end on 17 Oct: mckean's line reached the yardmaster's inbox, and the yardmaster's `yardr reply` posted in the chat.

Mail answers mail and never crosses over: `yardr reply` on a mail event sends mail to its sender (`replyMail` in internal/cli/reply.go, by `Mail` or `MailPeer`), and what a person types in footplate's Chat is mail (`Client::mail` in footplate's src/api.rs, then `Store.Mail`). Nothing turns a hook delivery into mail, and nothing posts mail to a source. Where an answer goes is where the person wrote.

### 2.3. Being told is a second sending of mail, and it would echo

A user told through a source (`User.Notify` and `User.Tell` in internal/store/users.go; docs/users.md, "Told outside the yard") is sent their mail, their notifications and their asks that are still unread after a short wait, through the source's `notify` call, at most a bounded number an hour. This matters to the choice in section 3.2: any design that records a crew member's Telegram answer as ordinary mail to the person hands that same text to the telling path, which would post it to Telegram a second time unless the new mail is marked to be left alone. It also matters to unread: mail to a user counts in `UserInbox` until the user acknowledges it, wherever the user actually read the words.

### 2.4. Who a heard line is from, today and next

Telegram hears by address: the envelope's `chat` field must equal what a user's env file sets, and `from: operator` narrows that to the one user the `operator` setting names (`Receiver.hears`). The yard learns the person's word is theirs from that match alone; the envelope's `actor` is Telegram's account of the sender, matched to nobody. The Slack design (yardr-pcxd, in draft at the time of writing) generalizes the same idea by identity rather than address: a delivery whose sender a user is mapped to carries `fields.speaker` naming that yard user, and only a person's own direct conversation with the crew is in its scope; channels and threads with outsiders stay threads. Any unified record has to name its person from what the hearing already established — the matched user — never from the service's display name, and has to stay general: no service is named in product code, so the mechanism keys off the declaration's `hear`, not off Telegram.

## 3. The proposal

The smallest version that answers the question: the conversation a person sees — in footplate's Chat, in `yardr chat`, and in `yardr mail --with` — gains the lines that were heard from that person's own address in a source that hears, and the answers the crew posted back there, each marked with the channel it came from or went to. The crew's side does not move at all: a heard line still arrives in the crew member's inbox as a `hook` event, is still answered with `yardr reply <seq>`, and the answer still goes where the person wrote. No new view, no new command, no new store. Two records gain what they are missing, and one reader learns to read them together.

### 3.1. The reply's words are kept

`Store.Replying` already writes the `Reply` record before anything is posted, from the body the command was given. The record gains the body itself (a `Body` field beside `Hash`, which already identifies it), and `Store.Replied` puts the same text on the `replied` event it logs. Both are additive: an older `Reply` record simply has no body, and no reader of the event is refused by a field it did not know. This is worth having even without the rest of the proposal — today a posted answer's words survive nowhere in the yard — and it is the only new writing the proposal needs. A reply whose posting was lost and retried is unchanged in every other way: Telegram still posts it twice, because the service takes no key to look the first post up by (section 2.2); keeping the words does not fix that, and section 4 leaves it where it is.

### 3.2. The conversation reads mail, heard lines and answers together

The store that carries the unified conversation is a view, not a second copy in mail. `Store.Conversation` (internal/store/users.go) keeps returning `Letter`s for the two parties, and learns a second source of letters beside the inbox indexes:

- A `hook` event is a letter of the conversation when its source's declaration hears (a `hear` key, read as the declaration is read today), the delivery was kept (a dropped stranger was recorded nowhere, so it can never appear), the user it was heard from is one of the two parties, and the crew member it was delivered to is the other. Which user it was heard from must be known at read time without guessing: when `Receiver.hears` matches a user today it answers only yes or no, so the match is recorded on the event the way the way-in is recorded on mail — the receiver stamps the kept envelope with the matched user's name (a `speaker` field of the hook event, the same name yardr-pcxd proposes for identity hearing, so address hearing and identity hearing land on one field), and `delivered` keeps it. The letter's `from` is that user, its `to` the crew member on the event's deliveries, its `text` the envelope's text as normalized (the bracketed note for a photo, section 2.2), its time the event's.
- A `replied` event is a letter when the delivery it answers (its `seq`) was such a heard line of the same two parties. Its `from` is the crew member, its `to` that user, its `text` the body section 3.1 keeps. Looking the answered delivery up by its seq at read time is one event read per answer; stamping the user on the `replied` event when it is written (the writer, `posted` in internal/store/threads.go, can read the delivery it answers in the same transaction) is the cheaper shape and the one proposed, for the same reason the receiver stamps the heard line: the record says whose it is from when it is written, not from who the parties happen to be mapped to later.

`Letter` gains one additive field, `channel`: the source's name on a letter that came from or went to that source (`telegram` today), empty on mail as it has always been. It is not `via`: `via` names the person's own device that wrote as them, and a heard line was written by the person on the service, not through a device of the yard; overloading `via` would make one field say two things. Footplate's client `Letter` gains the same field with a default, and its conversation shows a small cue where a letter's channel is set — the word the source names itself by, as the list in section 3.4 shows — and nothing where it is empty, so a mail-only conversation looks exactly as it does today. `yardr mail --with` and `yardr chat` show the same cue in words, as they already show `ViaMark`.

Reading costs one honest change: `Conversation` today reads the two parties' inbox indexes and nothing else of the log, and says so. Heard lines and answers are not in any inbox index, so the merged reader also walks the log's kind index for `hook` and `replied` events newer than `after` (the same walk `mailAfter` in internal/store/users.go already does for trusted-name mail) and keeps the ones.
The alternative — mail itself as the carrier — was weighed and set aside. In that shape a kept delivery would also be written as mail from the person to each crew member it was delivered to, and `Replied` would also write the answer as mail to the person, marked `via telegram` as a device is today. Its appeal is real: `Conversation`, `UserInbox`, paging and footplate would all work without a new reader. Its costs are larger. Every heard line and every answer would exist twice, once as the hook or `replied` event and once as mail, with two texts that can disagree (the envelope's 500-rune truncation against mail's full text). The crew member's inbox would show the same line twice, as a hook delivery and as mail, unless deliveries of a hearing source were suppressed there — a new rule in the inbox itself. The answer-as-mail would be handed to the telling path and posted to Telegram a second time unless channel mail is marked never to be told, a new exception in `notify`. And it still needs section 3.1: mail cannot carry words `Replied` was never given. The view pays one wider read in `Conversation` and keeps one record per message, one inbox, and one telling path. Where the volume of hook events ever makes that walk slow, an index under the user and crew member is an additive later bead; it is not needed to start.

What the view deliberately does not merge is as important as what it does. A thread with outsiders — a Linear issue, a GitHub pull request, later a Slack channel — is never a letter, however many crew members take part in it: its source hears nobody by address or identity as a user of this yard, or its sender was not heard as one of the two parties. Only a person's own conversation with the crew is unified. That line is exactly the `speaker` stamp: no stamp, no letter.

### 3.3. Where an answer goes, and what counts as read

Nothing about answering moves. A crew member reading the inbox answers a heard line with `yardr reply <seq>` and the answer is posted in that source, as today; answering mail sends mail, as today. What a person types in footplate's Chat is mail, and mail written from footplate (or `yardr chat`, or `yardr mail`) is never also posted to Telegram under this proposal: the two channels meet in what the person reads, not in where one message is sent twice. A person who wants the crew member on the phone writes on the phone; a person at the laptop writes mail. The crew member's answer follows the message it answers.

Read state follows the record that already holds it, and the Chat badge does not move:

- A heard line's letter is `read` when the crew member it was delivered to has acknowledged it in their inbox (`AckInbox` and the crew cursors of section 2.1, which already advance past hook events). The person sees the same "read" standing under their line that mail shows, now true of the Telegram line as well, and acknowledging it in the inbox is what clears it — there is no second acknowledgement in footplate for the crew's reading.
- An answer posted in a source is not mail, so it never enters `UserInbox`, never counts in the rail's Chat badge, and is never told again. In the conversation it stands as sent; the service gives no read receipt a bot can see, so it claims nothing more. Opening the conversation in footplate acknowledges mail as it does today, and channel letters neither wait on that cursor nor are cleared by it.
- Mail keeps exactly its present standing: unread mail counts on the badge (footplate-608q), heard lines count nowhere but in the conversation itself.

Whether a Telegram answer should nevertheless count as unread in footplate until the person opens the conversation there is a real choice the yard cannot make from a receipt it does not have; it is open question 1 below, and nothing else in this proposal waits on it.

### 3.4. What a person sees

In footplate's Chat, on the yardmaster's row, a conversation that today holds only mail gains the phone lines where they happened, each with its small cue:

```text
10-17 11:20  mckean
  Can the shed land on Monday?
  · via Telegram

10-17 11:24  yardmaster
  It can. Brakeman has the slot from 09:00.
  · to Telegram

10-17 11:31  mckean
  thanks — see the bead
```

And `yardr mail --with yardmaster` prints the same letters in its present shape, the cue in words:

```text
  5120 10-17 11:20  read    mckean (via Telegram): Can the shed land on Monday?
  5124 10-17 11:24  sent    yardmaster (to Telegram): It can. Brakeman has the slot from 09:00.
```

No new view, no new row, no new setting. A yard with no hearing source, or a person never heard, sees the conversation exactly as today.

## 4. Left out

**Photos, files and voice.** A Telegram message with no text already arrives as `[photo]`, `[document: <name>]` and their kin, and that bracketed note is all the unified conversation shows. Nothing is fetched: the normalize program is handed no credential and cannot call `getFile`, and hook events have no attachment pipeline (mail's blob store is mail's). Fetching media into the yard is a later bead if the notes prove too thin, and is named here so nobody reads the bracketed note as the design's answer to a photo.

**Posting mail onwards.** Mail written in footplate is never mirrored to Telegram, and a heard line is never re-sent as mail to anyone (section 3.3). Cross-posting is a sending rule, not a reading one, and the first version keeps sending exactly where it is.

**Answering from footplate on the channel.** Footplate sends mail; it gains no "answer on Telegram" action, and the crew gains no new reply surface. The crew answers from the inbox as today.

**Threads with outsiders, and group chats.** Slack channels, Linear issues and GitHub threads stay threads (section 3.2). A Telegram group chat is not heard today — its chat id is no user's address — and stays unheard.

**A second told person heard on Telegram.** The declaration's `from: operator` stands: hearing a second user's chat, and removing that narrowing in the declaration, is a change to the telegram pack's stated scope, not to this mechanism; the view already names its person from the stamp, whoever a future declaration hears.

**Fixing the double post.** A Telegram reply retried after a lost answer still posts twice; the service takes no idempotency key and `thread.posted` can find nothing (section 2.2). Keeping the words does not change it, and no client-side guess is proposed in its place.

**History before this lands.** Heard lines from before the stamp exists carry no user, and answers from before section 3.1 carry no words. Section 6's beads make no attempt to reconstruct either from payloads or logs; the unified conversation begins where the records begin. Open question 3 asks whether even that much backfill is wanted.

**Slack in the first version.** The mechanism is general — it keys off a declaration's hearing and one `speaker` stamp — but the first version proves it on the one source that hears today. A Slack letter arrives when yardr-pcxd's identity hearing lands and stamps the same field; no second design should be needed, and none is proposed here.

## 5. Open questions

Each of these is the operator's to decide, and each becomes its own ask when this design reaches decide (mem-xyzn). Nowhere else in this document is any of them decided.

**1. Does a crew answer sent on Telegram count as unread in footplate's Chat until the person opens that conversation there?**

- *(a) No count (recommended).* The answer was delivered on the channel the person wrote from, where they read it; footplate is the record, not a second inbox. Cost: nothing — section 3.3 as proposed. A person who muted the phone and lives in footplate would find the answer only by opening the conversation, with no badge to prompt them.
- *(b) Count it until opened in footplate.* The badge and the conversation both treat the answer as unread mail would be, cleared by opening the conversation. Cost: a second read cursor over channel letters (the mail cursor cannot carry it, section 3.3), and a badge that stays lit for words already read on the phone until the laptop catches up — the double notice the unified view exists to remove.
- *(c) Count it until the person next writes from anywhere.* Writing on either channel acknowledges the other. Cost: an acknowledgement rule across channels with no receipt behind it, and a badge cleared by an act that may not have seen the answer.

I would take (a): the yard has no read receipt from the service, and (b) and (c) both invent one.

**2. May mail written in footplate ever also be posted to Telegram?**

- *(a) Never (recommended).* Sending stays where the message was written (section 3.3); the conversation unifies reading only. Cost: a person at the laptop who wants the phone's thread continued writes on the phone.
- *(b) The person's choice per message.* A toggle in footplate's composer posts that mail to the person's own Telegram chat as well. Cost: a new sending path in footplate and in `Client::mail`'s caller, a second record of the same words on the channel (with Telegram's double-post exposure on a retry), and an answer that may now arrive on either channel for one question.
- *(c) Always mirror.* Every footplate mail to a crew member is also posted to the person's chat. Cost: (b)'s path on every message, the phone buzzing for a conversation the person is having on the laptop in front of them.

I would take (a): mirroring is a feature a real need can ask for later, with its own bead.

**3. Does the unified conversation reach backwards, or only forwards?**

- *(a) Forwards only (recommended).* Letters appear from when the stamp and the kept words exist. Cost: the first days of the phone conversation are missing from footplate, and the seam is visible.
- *(b) Heard lines backwards, answers as placeholders.* Past kept deliveries whose chat is the operator's are shown (their text survives, truncated), and each past `replied` event is shown as a line with its time and no words ("an answer was posted on Telegram"), since the words were kept nowhere. Cost: a one-time derivation the reader must carry forever — matching unstamped events by chat and address at read time, the guessing section 3.2 refuses — for a record that still cannot show what was said.
- *(c) Full backfill from payloads.* Re-derive speakers from stored payloads and leave answers placeholdered as in (b). Cost: (b)'s reader plus payload archaeology, for the same missing words.

I would take (a): a record that starts honestly is worth more than a reconstructed one with gaps dressed as lines.

**4. Who besides the operator is ever heard into their own conversation?**

- *(a) Nobody in the first version (recommended).* `from: operator` stands on Telegram, and the view serves whoever a declaration hears without itself hearing anyone new. Cost: a second person told through Telegram is told but not heard, as today, until their hearing is decided as its own change.
- *(b) Every user told through a hearing source.* Removing the narrowing (a declaration change per source) makes each told user's own address a conversation of theirs. Cost: each such person's phone words enter the yard with the standing the c55e design gave the operator's (docs/design/2026-10-13-yardr-c55e-telegram-hears.md, its open question 1 as decided) — a decision per person that (a) defers and (b) takes wholesale.

I would take (a): the mechanism does not care, and the trust decision should be taken per source and per person, not as a side effect of a view.

## 6. The beads it would become

In the order they would land. Each is small enough that a builder finishes it without asking. Yardr-side beads are built and tested as this depot's beads are (isolated yard for anything that starts agents, `contrib/isolated-yard/isolated-yard up`, commands as `D/e …`); the footplate bead follows that depot's own test command.

**1. Yardr: a reply keeps its words.** Goal: `Store.Replying` records the answer's body on the `Reply` record before anything is posted, and `Store.Replied` carries the same text on the `replied` event. Where to look: internal/store/threads.go (`Reply`, `Replying`, `Replied`, `posted`), internal/cli/reply.go (the body is already in hand where `Replying` is called). Done when: a reply posted through a stand-in source leaves its exact words on the record and on the event; a retry of the same answer finds the same record; older records without a body still read; unit tests cover both, and the Telegram double-post behaviour on a lost answer is unchanged and still documented in packs/telegram.

**2. Yardr: a kept delivery names who it was heard from.** Goal: `Receiver.hears` reports the matched user as well as the match, the receiver stamps that user as the kept envelope's `speaker`, and `posted` stamps the same user on a `replied` event that answers such a delivery. Where to look: internal/hooks/receiver.go (`hears`, `serve`), internal/source/notify.go (`Hear`, `ClaimAddress` for the field's derivation), internal/store/hooks.go (`delivered`), internal/store/threads.go (`posted`, reading the answered delivery in its transaction). Done when: a delivery from the operator's chat is recorded with the operator named, a stranger's is still dropped with no event, a source with no `hear` key records exactly what it records today, and the claim offer for a stranger is unchanged; tests cover match, drop and no-hear alike. No service is named in product code: the stamp comes from the declaration's hearing alone.

**3. Yardr: the conversation reads heard lines and answers.** Goal: `Store.Conversation` also returns the heard lines and the answers of section 3.2 as `Letter`s with `channel` set, merged with mail in `seq` order under the existing `after`/`limit`/`before` paging, with read standing as section 3.3 defines. Where to look: internal/store/users.go (`Conversation`, `Letter`, `readers.letter`, `mailAfter` for the kind-index walk), internal/store/api.go (the `Conversation` shape stays additive: one new field). Done when: mail-only conversations return byte-for-byte what they return today; a heard line and its answer appear between the mail around them for the right two parties and for no other pair; a thread with outsiders never appears; paging across the seam returns each letter once; store tests cover the merge, the parties, the seam and the read rules. Depends on beads 1 and 2.

**4. Footplate: the channel cue in Chat.** Goal: the client's `Letter` gains `channel` with a default, and the conversation shows the small cue of section 3.4 where it is set and nothing where it is not. Where to look: footplate's src/api.rs (`Letter`, `conversation`, `conversation_page`), src/window.rs (`list_chat`, the conversation rendering), src/model.rs (Chat state). Done when: against a yard that answers with the field, a Telegram line and its answer show the cue and a mail line shows none; against an older yard that never sends it, conversations render exactly as today; the depot's tests cover both. Depends on bead 3's field existing, and is otherwise independent: the additive shape (mem-kf2y) means neither side breaks the other while one waits.

**5. Yardr: words for the unified conversation.** Goal: docs/chat.md and docs/integrations.md say what changed — that a conversation includes what was heard from a person's own address and what was answered there, where an answer is sent, what read means per channel, and that photos arrive as bracketed notes. Where to look: docs/chat.md (the conversation, its read lines), docs/users.md ("Told outside the yard", for what is not told twice), packs/telegram/SETUP.md (what the operator sees differently: nothing to set up). Done when: following the docs, a reader can say where a Telegram line appears, where its answer is sent, and why the badge did not move; the operator's isolated-yard run — a stand-in phone line in, `yardr reply` out — shows both halves in `yardr mail --with` and in footplate (bead 4) with the cue. Depends on beads 3 and 4.

Bead 1 and bead 2 each stand alone and land in either order; bead 3 needs both. Bead 4 needs only the field bead 3 defines, and bead 5 closes after both sides. Later, and not these beads: Slack letters when yardr-pcxd stamps the same `speaker` by identity, media fetched into the yard, and any mirroring open question 2 might one day allow.
