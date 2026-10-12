# While a bead is at the other yard: the peer as a source

Design yardr-evd6, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr; a peer is another yard this one is linked with by its public key; a bead is one durable piece of work; a source is the outside system a yard receives events from; a thread is a conversation in a source that a crew member takes part in. docs/glossary.md has all of yardr's words. In this document the **asking yard** is the yard whose bead waits in a stage that a peer works, and the **working yard** is the peer that made a bead of its own from it. A bead is **away** from the moment the asking yard sends it until the working yard's return ends the stage.

## 1. The question

mckean, 11 Oct 2026, on yards that work together (the record bead yardr-6yx8): "this one is important and I'd love to move forward on this, but it needs to be done with care." Steps 1 and 2 are built: a bead is sent to a paired yard, worked there, and its outcome and code come back. What is the smallest careful design for step 3: what the two yards say to each other while the bead is away (progress, a question and its answer, a bead sent again), as it was reshaped on 6 Oct 2026: the peer is a source as Linear and GitHub are, and the record of the pair is shown through `yardr link`, `yardr thread show` and `yardr reply` across the link?

The thread the record bead names (`yardr thread show 5307`) could not be read from a builder's session, which has no Linear credential. This design goes by the notes on yardr-6yx8, which quote that thread with dates.

## 2. What exists today

### 2.1. A bead worked by another yard (steps 1 and 2)

docs/peers.md, "Beads from a linked yard", describes all of it. The code:

- **The sending side**, internal/store/peersend.go. A stage is worked by a peer when its route leads to a group whose runner is `peer` (`PeerRunner`; internal/runner/peer.go starts nothing and asks the store to send). `PeerBeadSend` queues one message of kind `bead` and writes the record `peerSent` in the bucket `peer_beads_out`: the peer by its key, this yard's stage, the message's id, when it was delivered, and later the outcome, the notes and the branch the return named. The record is "the bead's last sending": one per bead, overwritten when the bead is sent again.
- **The receiving side**, internal/store/peerbeads.go. `checkBead` decides whether a `bead` message is taken (the refusals of docs/peers.md, "What arrives"), `arriveBead` makes this yard's bead in the intake's depot and flow, held unless the intake says `--auto`, and writes the record `peerBead` in the bucket `peer_beads`: the peer by its key, the sender's handle and stage, the message's id, whether it is still held as it arrived. `releasePeerBead` lets only a user lift that hold (or a crew member with `release=<peer>`, a setting that exists and is off in this yard). `returnPeerBead` queues the `bead-return` when the bead enters a terminal stage, once.
- **The return**, `returnedBead` and `settleSent` in peersend.go: the outcome is applied to the asking yard's bead as `yardr bead done` applies one, by the actor `peer:<name>`, after the notes written at the working yard were added to the bead, each by `<author>@<peer>`. A return for a stay that is over changes nothing and is logged as stale.
- **Code**, internal/store/peergit.go and internal/dispatch/peerfetch.go: the `bead` message says where the work starts (`git.base`, `base_commit`, `remote`), the working yard's `contrib/peer/return-branch` pushes `yard/<its bead>`, and the asking yard's daemon fetches that branch, checks that it is at the commit the return names, and moves its own `yard/<bead>` to it by fast-forward only, before the outcome is applied.
- **What a person sees while the bead is away** is one line of `yardr bead show`: `at north: sent 10-06 14:02, north last heard 10-06 14:10` (`BeadPeer.Line`). Nothing else crosses until the return. The peer group's `max_age` raises `session_aged` once, after 24 hours unless set, and nothing ends the stage by a clock.
- **A question today** is an outcome: the working yard's flow ends in its terminal stage on `question`, the return carries `question`, and the asking yard's flow routes it (to `decide`, in the example of docs/peers.md). This was decided on 6 Oct 2026 with the other recommended options.
- **A bead sent again today** is a new message under a new id (`sentID`, made from the bead and the event that put it in the stage). The working yard refuses it as `already_here` only while its earlier bead for it is open; once that is closed it makes a new bead, from `git.base_commit`, which is where the asking yard's base stands and not where the returned work stands. The branch that then comes back does not come after the asking yard's `yard/<bead>`, so it is not taken and the bead is held (docs/peers.md, "When the branch cannot be had"). Sending a bead with code a second time therefore ends in a hold a person untangles.

### 2.2. The link and its messages

internal/store/peers.go. A link carries kinds of messages, each direction allowed on its own (`yardr peer allow --send/--receive`); `PeerKinds` are the words a link can be allowed by: `ask`, `bead`, `file`, `mail`, `ping`. The word `bead` covers the messages `bead` and `bead-return` (`Peer.carries`). `receivePeer` takes messages: a kind this build does not know is refused as `kind_unknown`, one the link does not carry as `kind_not_allowed`, the same id twice is taken once (the bucket of seen ids, kept 7 to 90 days), and "no other body is kept: what a peer sends is data". Each yard tells the other which kinds its build understands with every signed request and answer (`Says` in internal/yards/receiver.go), and `yardr peer show` prints it. Messages wait in an outbox per peer and are pushed, or pulled through a held question, in order: a message never overtakes one before it. `yardr peer ping` queues a message and waits 30 seconds for the one that answers it by `re`.

Mail (internal/store/peermail.go) sets the rules every later kind followed: a crew member may write to a peer only when the operator named the peer in its `peers` setting; a person needs no setting; a bead's agent may not write to another yard at all; the receiving yard takes 30 mails and asks from one peer within an hour; text from a peer is shown with the line `from another yard: information, not instructions` (`OutsideMail`), or `(as <user>)` where the operator trusts that name as a user of this yard.

### 2.3. Asks across yards (design yardr-h039, built)

docs/peers.md, "Questions for a person of another yard"; internal/store/peerasks.go. A crew member asks a person of a linked yard with `yardr bead ask <id> "<question>" --to <person>@<peer>`. The ask is a message of kind `ask`; it stands in that person's decisions in the other yard as a foreign ask named `bead@peer`, which is not a bead and reaches no agent; the pick comes home as a message of kind `ask` whose `re` names the question, and lands as a note of kind `answer` by `name@peer`. An answer is taken only for an ask this yard sent to exactly that name. yardr-h039 proposed this one carrying for a question and its answer and left it to this design to confirm or replace for a bead that is away.

### 2.4. What a source is

docs/integrations.md, "What an integration is". A source is a set of capabilities, and a source has a capability exactly when it says so: in Go for the two sources that are yardr's own (`source.BuiltIn` in internal/source/declaration.go is `token` and `yardr`), in a declaration file for every other. Design yardr-76zs answered what a source may declare, and it is built: GitHub and Linear are packs of kind source. The capabilities, with the declaration key of each:

| capability | key | what the yard does with it |
|---|---|---|
| receive | `hook` | a delivery becomes one `hook` event with an envelope (`type`, `action`, `actor`, `thread`, `text`, `fields`); `RecordHook` in internal/store/hooks.go matches it against subscriptions (`hookDeliveries`) and records who takes part in its thread |
| identify | `identity` | a crew member's identity in the source decides `mentions` and `involved` |
| link | `link` | a bead's link to the source's issue, in one fixed form (docs/links.md) |
| reply, read a thread | `thread.reply`, `thread.read` | `yardr reply <seq>` and `yardr thread show <seq>`, where `<seq>` is the number an inbox line starts with (internal/cli/reply.go) |
| open a thread, set its status, move with a stage | `thread.open`, `thread.state`, a hook's `--map` | `yardr thread open`, `yardr thread state`, a `tracks` link |
| what its events say of a bead | a hook's `--says` and `--makes` tables | an event reports an outcome for the bead that tracks its thread, or makes a bead |
| pull requests and CI, calls, notify, limits | `events`, `calls`, `notify`, `limits` | not used below |

A subscription has a source, a scope (`all`, `mentions`, `involved`) and `--where` filters over the keys the source's events carry (docs/subscriptions.md). A user is reached only by scope `all`. A crew member takes part in a thread once it answered there or follows it (`yardr thread follow`).

The source `yardr` is this yard's own bead events. The `peer_bead_*` events of steps 1 and 2 are bead events and are heard through it today (`yardr subscribe yardmaster yardr --where kind=peer_bead_expiring`).

## 3. The proposal

One built-in source, `peer`, beside `token` and `yardr`: the yards this one is linked with, heard about the beads this yard has away at them. It has three of the capabilities of section 2.4 and no other: receive, link, and reply with read. It is carried by three new kinds of message on the link, allowed by one new word. Nothing of steps 1 and 2 is expressed anew: the two records, the `bead` and `bead-return` messages, the intake, the hold and the return stay as they are, and the source is added beside them.

Where a choice below is the operator's, the text says which open question of section 5 it is, and describes the option this draft would take.

### 3.1. One new word on the link, three new messages

A link is allowed the word `thread` (open question 1), each direction on its own as for every word:

```sh
yardr peer allow north --send thread --receive thread
```

`thread` joins `PeerKinds`, so each yard says whether its build understands it, `peer allow --send thread` warns for a peer that has not said so, and an older yardr refuses the messages by name as `kind_unknown`. It covers three messages. Each names the `bead` message of the stay it is about in its `re`, which is how both yards find their record of the pair, by the peer's key and that id:

| message | from | body | carried when |
|---|---|---|---|
| `bead-event` | the working yard | `v` 1, `bead` (its handle), `event`, `stage`, `from` (the stage left, for `advanced`), `held`, `held_as`, `at` | the working yard's link sends `thread`, the asking yard's receives it |
| `bead-read` | the asking yard, and the answer from the working yard | the question: `v` 1. The answer, whose `re` is the question's id: `v` 1, `bead`, `stage`, `held`, `held_as`, `notes` (each `author`, `at`, `text`), `notes_left_out` | each yard's link sends and receives `thread` |
| `bead-note` | the asking yard | `v` 1, `from` (the writer's name), `text` | the asking yard's link sends `thread`, the working yard's receives it |

`event` is one of seven words, and `held_as` one of three; both are held to those lists when taken, and `stage` and `from` to the plain characters a stage of another yard is held to today (`peerStage`). A `bead-event` carries no free text and no name of anyone in the working yard (open question 4): the roster of a link says which names a peer may see, and an event is not the place to widen it.

| `event` | when, in the working yard | from its local event |
|---|---|---|
| `arrived` | the bead was made, with `held` | `peer_bead_arrived` |
| `released` | a user lifted the hold it arrived with | `peer_bead_released` |
| `expiring` | it is still held as it arrived and goes back as refused soon | `peer_bead_expiring` |
| `advanced` | it entered a stage that is not terminal | `advanced` |
| `held` | it was held; `held_as` says `arrived`, `note` (section 3.5) or `own` (its agent, a person, three failed sessions) | `held` |
| `unheld` | a hold was lifted | `unheld` |
| `gave_up` | the working yard gave up on it | `gave_up` |

The working yard queues a `bead-event` in the write that logs the local event, for a bead that came from a peer, is not returned, and whose link sends `thread`. The move into a terminal stage sends none: the return says it. A note written at the working yard sends none either: notes are read when someone asks (section 3.4).

No message of the three makes a bead, moves one, ends a stage, lifts a hold or starts an agent. Section 3.7 says for each what it can do.

### 3.2. The source `peer`: receive

The asking yard takes a `bead-event` when its link receives `thread` and the event's `re` names a stay it has open at that peer: a `peerSent` record with that message id and that peer's key, not ended. Anything else is dropped and logged as stale, as a late return is. For an event it takes, in one write:

1. The record learns what the peer said: its handle for its bead, the stage, whether it is held and as what, and when it was said. `yardr bead show` grows from one line to two:

   ```text
   web-d5zc  Add the export
     type task · stage build · open · P2
     at north: sent 10-12 13:40, north last heard 10-12 14:03
     there it is web-0wzb, in the stage new, held by its own (as north said 10-12 14:03)
   ```

2. One event is recorded as a delivery of the source `peer`, through the routing every delivery goes through (`hookDeliveries`, `recordParticipation`), by the actor `peer:<name>`. Its envelope is made by this yard from the fields above:

   | field | value |
   |---|---|
   | `source` | `peer` |
   | `type`, `action` | `bead`, and the `event` word |
   | `thread` | this yard's bead: its id, its title, and the link form of section 3.3 as the URL |
   | `text`, `actor`, `mentions`, `assignee` | none: no words and no name crossed |
   | `fields` | `peer` (the link's name), `bead` (this yard's handle), `depot`, `label` (this yard's bead's labels), `stage` (the other yard's stage) |

   The thread is this yard's bead, not the message, so a bead sent a second time is the same thread and whoever follows it still does.

A subscription to it is a subscription to any source:

```sh
yardr subscribe yardmaster peer --scope all                      # every bead this yard has away
yardr subscribe yardmaster peer --scope all --where peer=north   # those at north
yardr subscribe mckean peer --scope all --where action=held --where action=gave_up
yardr thread follow yardmaster --source peer web-d5zc            # this one bead, with --scope involved
```

The name of the source and its keys are open question 2. Nobody hears the source until they subscribe, as with every source. The source has no identities, since identities are not merged across yards: `mentions` matches nothing, and `involved` is taking part, by having answered in the thread or following it. A user is reached by `--scope all`, as in every source.

The inbox line is this yard's words around the peer's plain fields, with how to read on:

```text
   412 10-12 14:03  peer north: web-d5zc is held there by its own, in the stage new (there web-0wzb): yardr thread show 412  [all]
```

`yardr peer rename` renames the `peer=` value of subscriptions in its one write, as it renames the crew settings that name a peer today (`renamePeerSettings`). `yardr peer rm` leaves such a subscription, which then matches nothing, and `yardr doctor` notes it.

The source has no hook record and no tables. A hook's `--says` table lets an event report an outcome for a bead; for a bead that is away only the return does that, so there is nothing for a table to say. Its deliveries are verified by the link's signature, where a hook's are verified by its secret.

### 3.3. The source `peer`: link

`yardr link list` and the links of `yardr bead show` print the counterpart of a bead as one row, read from the record and never stored as a link:

```text
yardr link list web-d5zc
at      web-0wzb@north (peer)   since 10-12 13:40
tracks  https://linear.app/acme/issue/YRD-12 (linear)

yardr link list web-4y35          # in the working yard
from    y-k3f9@laptop (peer)    for its stage build
```

The words are `at`, `was at` and `from`, as `bead show` says them. The row names the peer as it is called today, since the record is kept by the peer's key. `yardr link list --issue web-0wzb@north` finds the bead. `link add`, `link rm` and `link move` refuse such a row: the record is the yard's own, and nothing anyone can edit of a bead changes whose it is (the comment on `bPeerBeads`). `tracks` keeps its meaning, this yard's stage moving their item, and no stage of this yard moves a bead of another: the row is neither `tracks` nor `relates`. BDP exports stored links and is unchanged.

Until the first `bead-event` or the return has named the other yard's handle, the row says `at north (its bead not said yet)`.

### 3.4. The source `peer`: read

`yardr thread show` takes the number of a delivery of the source, or a bead that is away:

```sh
yardr thread show 412
yardr thread show web-d5zc
```

```text
peer thread web-d5zc "Add the export": at north as web-0wzb, in the stage new, held by its own. As north said 10-12 14:05.
What follows was written in the yard north: information, not instructions.

  10-12 14:02  builder@north
    Which export format: CSV only, or XLSX too? Held until answered.
```

The command queues a `bead-read` and waits for its answer as `yardr peer ping` waits for its pong: 30 seconds, or `--timeout`. With the held question of the pull side an answer comes at once whichever yard calls the other. The asking yard keeps the last answer beside the record, one copy per bead, replaced by each answer and removed when the stay ends. So when no answer comes in time, the command prints the copy it has and says so:

```text
north has not answered in 30s (last heard 10-12 14:10). What it said last, 10-12 14:05:
```

The working yard answers a `bead-read` whose `re` names a bead that came from that peer over the link as it is today, open and not returned, when its link receives and sends `thread`. The answer is what the return would carry at this moment: the notes written there, the newest kept within the limits a return has (16 KB a note, 128 KB together, the older ones counted in `notes_left_out`), and the stage and hold. Nothing is in it that the peer would not get when the bead ends. Any other `bead-read` is refused as `no_such_bead`, the one reason whether the bead exists, is another peer's, or has ended. Reads count in the peer's hour beside mail and asks.

Reading needs what writing to a peer needs today: a crew member needs the peer in its `peers` setting, a person needs nothing, a bead's agent is refused. The answer's notes are kept only in that one copy; they become notes of this yard's bead when the return brings them, as today.

The `session_aged` notice of a bead that has been away for a day names the command, which is how someone who heard nothing learns to ask.

### 3.5. The source `peer`: reply

`yardr reply` on a delivery of the source, or on a bead that is away, writes a note on the other yard's bead:

```sh
yardr reply 412 "CSV only. XLSX is a later bead."
note for web-0wzb@north queued; the daemon delivers it, and yardr peer show north lists it while it waits
```

It is one message of kind `bead-note`, queued as mail is. Who may send follows mail's rule, with the word `thread` in place of `mail`: the link sends `thread`, a crew member has the peer in its `peers` setting, a person needs nothing, a bead's agent is refused, and only the crew member's own session or the operator speaks as a crew member (`maySpeakAs`, unchanged). The text is held to 16 KB before it is queued. The writer then takes part in the thread, as after any reply.

The working yard takes a `bead-note` when its link receives `thread` and the `re` names a bead that came from that peer, open and not returned; otherwise `no_such_bead`. The writer's name is held to mail's characters. Notes count in the peer's hour beside mail and asks (30). What it does, in one write:

1. The text becomes a note of the working yard's bead, by `<name>@<peer>`, written as the notes that came with the bead are (`peerNotes`): each yard stays the only writer of its own bead, and no `noted` event is matched against subscriptions with a peer's text.
2. The bead logs `peer_bead_noted`, by `peer:<name>`, with the writer's name and no text. It is an event of the source `yardr`, heard as `peer_bead_expiring` is today.
3. Unless the intake is `--auto`, the bead is held as it arrived again (open question 3): the record's hold is set, the bead is held, which ends a session that works it, and only a user releases it, by the check that exists (`releasePeerBead`). `bead show` says why:

   ```text
   held: a note came from laptop (10-12 14:09, by yardmaster@laptop) after this bead was released: a user reads it and releases the bead (yardr bead unhold web-0wzb)
   ```

   The reason: the hold a bead arrives with is the wall between a peer's text and an agent that runs with this yard's permissions, and it was decided that a user of this yard lifts it. A note is more of that peer's text. Without this a peer sends a harmless bead, waits for its release, and then sends the text it meant. With `--auto` the operator has said that the other yard's operator may start agents here, and a note lands as the bead did.

A note never lifts a hold, moves a bead or answers an ask. Where the working yard's bead was held by its own agent with a question, the note is the answer's words, and a user of that yard reads it and unholds: one step, which is the step the decided rule asks for.

A note given up (24 tries, or a refusal) is told to its writer by mail from `yard`, as mail that was not delivered is.

### 3.6. A question and its answer, and a bead sent again

**A question.** Three ways exist after this design, and each has its place:

- *The flow's way, unchanged.* The working yard's flow ends on `question` and the asking yard's flow routes that outcome. It costs a round trip of the whole bead and is right when the question changes the task.
- *An ask to a person* (open question 5). This design confirms yardr-h039's carrying and adds no second one: a crew member of the working yard asks with `yardr bead ask <its bead> "<question>" --to <person>@<asking yard>`, and the pick comes back as a note of kind `answer`. It works today for a bead that arrived, since an ask stands on any bead. This design adds one line to the foreign ask's card in the asking yard, where the pair is known: `this is the bead north works for this yard's web-d5zc`, with that bead among the about-beads, so the person sees their own bead and not only a handle of another yard. What yardr-h039 said must not be inherited holds: an ask arrives unheld, because a row in a person's queue starts no work.
- *Words, by `yardr reply`* (section 3.5): for an answer that is no pick, and for context the asking yard wants to add.

A bead's agent in the working yard writes to no other yard, as today. Its question reaches the asking yard as the event `held` with `held_as` `own`, or as `advanced` into the stage that yard's flow has for questions, and is read with `yardr thread show`.

**A bead sent again.** Three changes, the first on the asking yard and the other two on the working yard:

1. *It continues from what came back.* When the asking yard's `yard/<bead>` stands at the commit a return of that bead named and the daemon fetched (`peerSent.Git` with `Fetched`), the next `bead` message for the bead names that commit as `git.base_commit`. The working yard has the commit, since it built it, and its new bead's branch starts there (`checkBase` and `familyOf`, unchanged), so what it returns comes after the asking yard's branch and is taken by fast-forward. When the asking yard's branch has moved past the returned commit, the other yard does not have those commits, and sending them out is step 4: the stage ends as `refused` by `yard`, with a note that says so, before anything is queued.
2. *The working yard knows it has had the bead.* It looks in its own records for beads from that peer's key with the same sender's handle. `bead show` of the new bead names them: `laptop's y-k3f9 was here before as web-0wzb (returned done 10-12 15:10)`. Nothing the peer says is used for this but the handle its message always carried.
3. *A bead this yard refused does not start unseen* (open question 6). When an earlier bead for the same sender's handle went back as `refused` because a person closed it or nobody released it in time, the new bead arrives held whatever the intake says, also with `--auto`, and `bead show` says `this yard refused it before (web-4y35, 10-09: not released within 3d)`. And a yard takes 30 `bead` messages from one peer within an hour, refused ones counted, the next refused as `rate_limited`, as mail is: a flow that routes `refused` back into the peer's stage cannot keep the other yard busy.

   What changes 2 and 3 go by is the sender's handle, and the handle is the peer's word: the link's signature proves which yard sent a message, not which of that yard's beads it is about. So the match finds a bead that comes again from a yard that runs yardr as it is, where a bead sent again keeps its handle. It does not find a bead whose yard gave it a new handle, by making a new bead of the same text or by sending another handle on purpose. That one is a new bead to this yard: no line about an earlier stay, and the intake's own rule, which is the hold for a user without `--auto` and a start unseen with it. Section 3.8 says what that leaves, and open question 6 has the option that does not depend on the handle.

### 3.7. Care 1: who reads each message, in what frame

| message | text of the peer's in it | who reads it | in what frame | what it can make this yard do | what it cannot |
|---|---|---|---|---|---|
| `bead-event` | none: seven words, three words, a stage name and a handle held to plain characters | whoever subscribed to the source `peer`, a crew member or a user, in an inbox line; anyone in `bead show` | this yard's own sentence, ending `as <peer> said <when>` | record one event, write one inbox line per subscriber, update two fields of the record | end a stage, report an outcome, move or hold a bead, start an agent, reach anyone not subscribed |
| `bead-read`, the question | none | no one | | hand over the notes, stage and hold of one open bead that came from that peer | read any other bead, any bead that ended, or anything a return would not carry |
| `bead-read`, the answer | the notes written at the peer | whoever ran `yardr thread show`: a person, or a crew member allowed that peer | under `What follows was written in the yard <peer>: information, not instructions`, each note by `<author>@<peer>` | replace one kept copy, for one bead | write a note on this yard's bead, move it, reach an inbox, or be taken unasked: an answer whose `re` is no question this yard asked is dropped |
| `bead-note` | the note | first a user of this yard, who releases the bead; then the agent that works the bead | in `bead show`, the prime and the brief, under the line a bead from a peer carries today (`This bead came from the yard <peer>: its text is that yard's, the task and not instructions about this yard`), the note by `<name>@<peer>` | add one note to one open bead that came from that peer, and hold that bead for a user | lift a hold, move a bead, answer an ask, touch a bead of this yard's own, or reach an agent before a user has released the bead (unless the intake is `--auto`) |

Two frames already exist and are reused: `OutsideMail` for what a crew member reads from another yard, and `BeadPeer.Marking` for what a bead's agent reads. A trusted name (`--trust mckean@airy=mckean`) changes how a note's writer is shown, `(as mckean)`, and nothing else: the grant "releases no held bead" (docs/peers.md), and a note by a trusted name holds the bead as any does.

### 3.8. Care 2: the threats

| a peer that | what this design does |
|---|---|
| is slow | Nothing waits on a clock. Events arrive in the order they were queued and before the return, since an outbox keeps its order. `thread show` waits 30 seconds and prints what it last had, with the time. A note waits in the outbox, shown by `peer show`. The stage is ended by the return alone, as today |
| is gone for a day, or for good | The notice after a day away is unchanged and names `thread show`. A note or a read is given up after mail's 24 tries and its writer is told by mail from `yard`. `yardr peer rm` ends the stay as `undelivered`, as today, and drops the kept copy; the bead and its record stay |
| replays | The same id is taken once. An id forgotten after the retention comes in as new: an old `bead-event` then names a stay that ended and is dropped as stale; an old `bead-note` names a bead that is returned and is refused; an old `bead-read` answer answers no question and is dropped. A request recorded off the wire is refused after five minutes by its signed time, as today |
| lies about an outcome | This design adds no way to do it and removes none: no new message carries an outcome, no event is put to a table, and the stage ends by `bead-return` alone. The outcome in a return is the peer's word, and this yard cannot check it: what is checked, and what the yard's own flow has to do about the rest, is under this table. A lie in an event (`advanced` that did not happen) changes a line that says `as north said` |
| sends a question that is a prompt | As a `bead-event` it has no text. As notes read by `thread show` it is framed, is read only by who asked for it, and lands on no bead. As an `ask` it is read by one person and no agent (yardr-h039, unchanged). As a `bead-note` toward a working yard it reaches an agent only after a user of that yard released the bead with the note on it |
| sends a bead again that was refused | Under the same handle: the new bead arrives held, also under `--auto`, and says what was refused before and why; a person decides again, with that in view. Under another handle the history is not found, since the handle is the peer's word (section 3.6): without `--auto` the bead arrives held as every bead does, and the user who releases it does not see the line about the refusal; with `--auto` it starts unseen, as a new bead. So under `--auto` this protects against a flow that sends a bead round again and against a peer that runs yardr as it is, and not against a peer that means to hide a retry. That peer gains nothing `--auto` had not given it: the setting says "the operator of that yard may start agents here", and a bead under a new handle is one more bead. What holds whatever the handle is counted by the peer's key: thirty beads an hour, and the intake's `--limit` of beads open at once. Open question 6 has the option that closes this, at a price |
| floods | Notes and reads share the peer's hour with mail and asks (30). Events have an hour of their own, 240, past which they are refused as `rate_limited` and logged once; the peer gives each up, and nothing waits on an event |
| asks to read what is not its | `no_such_bead`, one reason for every case. A relay does not widen it: a yard that sent a bead on answers reads about its own bead and forwards none |
| stops its own work by a note | It can: a note holds the working yard's bead and so ends the session on it. It is the peer's own task that stops, the worktree and branch stay, and the hold is the yard's protection, not the peer's lever on anything else |

**What a paired yard is trusted for when it reports an outcome.** The return is signed, so it is known which yard said it. Whether it is true is not known. `returnedBead` in internal/store/peersend.go holds the outcome to the characters of a stage's name and takes it for the stay it answers; nothing in it, and nothing this yard could add to it, shows that the other yard's flow reached that outcome, that an agent there ran a test, or that a person there decided anything. The commit check of step 2 proves that the branch this yard fetched is the commit the return names, and proves nothing about how that commit came to be. A paired yard is therefore trusted to say how its own work ended, as a group of this yard's own agents is trusted when one of them runs `yardr bead done`. Steps 1 and 2 built it so, and this design does not change it. What bounds a false outcome is this yard's, and all of it exists:

- The outcome moves the bead only where this yard's flow routes it. A stage that does not name the outcome that came keeps the bead, held (`TestPeerBeadOutcomeTheStageDoesNotName`). A peer chooses among the routes the operator wrote out of that stage and can make no other.
- Code comes in by fast-forward of `yard/<bead>` alone, from the depot's own remote, at the named commit (`TestPeerCodeNotWhatTheReturnNames`, `TestPeerCodeNotTaken`). `bead show` says of it `This yard has not tested it`.
- What comes after the peer's stage is this yard's work: the review stage's file and the reviewer's role have the reviewer run the depot's test command on the branch whatever the notes say, and the assembly tests exactly what lands (docs/peers.md, "Whose code it is").

So a false `done` costs what a wrong `done` from one of this yard's own builders costs, where the flow puts a stage worked here between the peer's stage and the landing. Where a flow routes the peer's `done` to a stage that lands or to a terminal stage, nothing of this yard looks at the work but the assembly's test run, and for a bead whose result is words, nothing at all. That is a property of the flow the operator writes, and yardr says nothing about it today. Whether that is enough is open question 7.

### 3.9. Care 3: what steps 1 and 2 keep

No message, record field or rule of steps 1 and 2 changes meaning. `bead` and `bead-return` keep `v` 1; a field is added to no body. The one behaviour that changes is the value of `git.base_commit` for a bead sent again (section 3.6), and the new hold of a bead refused before under `--auto`. Each bead of section 6 is done only when these pass unchanged, as the proof that the rest stands:

- internal/store/peerbeads_test.go: `TestPeerBeadArrivesHeld`, `TestPeerBeadArrivesReadyWithAuto`, `TestPeerBeadRefusals`, `TestPeerIntake`, `TestPeerBeadKinds`, `TestPeerBeadRelease`, `TestPeerBeadReleaseFromACrewPane`, `TestPeerBeadReturn`, `TestPeerBeadReturnWithoutOutcome`, `TestPeerBeadReturnLeavesOutOldNotes`, `TestPeerBeadReturnOverAClosedLink`, `TestPeerBeadRenameAndRemove`, `TestPeerBeadPulled`, `TestPeerRefusalOfBeads`.
- internal/store/peersend_test.go: `TestPeerGroup`, `TestPeerBeadSend`, `TestPeerBeadSendSaysTheType`, `TestPeerBeadTooLargeToSend`, `TestPeerBeadPath`, `TestPeerBeadReturnsThatAreNone`, `TestFlowMapOfAPeerStage`.
- internal/store/peerhold_internal_test.go: `TestPeerBeadNobodyReleasesGoesBack`, `TestPeerBeadReleasedIsLeft`, `TestPeerBeadHeldForEver`, `TestPeerBeadEndedOtherwiseIsLeft`.
- internal/dispatch/peerbeads_test.go: `TestPeerBeadRoundTrip`, `TestPeerBeadDeliveredTwice`, `TestPeerBeadRefused`, `TestPeerBeadUndelivered`, `TestPeerBeadOutcomeTheStageDoesNotName`, `TestPeerBeadTakenBack`, `TestPeerBeadLinkGoesWhileAway`, `TestPeerBeadRename`, `TestPeerBeadSentOn`, `TestPeerBeadLoop`, `TestPeerBeadWaitsPastItsAge`, `TestPeerBeadNobodyReleased`.
- internal/dispatch/peergit_test.go: `TestPeerCodeComesBack`, `TestPeerBeadBaseUnknown`, `TestPeerCodeRemoteDown`, `TestPeerCodeNotWhatTheReturnNames`, `TestPeerCodeNotTaken`, `TestPeerCodeIntoADirDepot`, `TestPeerCodeSentOn`, `TestPeerBaseKeptByARef`.
- internal/store/peergit_test.go `TestTakePeerBranchFastForwardOnly`, internal/runner/peer_test.go `TestPeerRunner`, contrib/peer/return_branch_test.go `TestReturnBranchPushes` and `TestReturnBranchNeverForces`.
- For asks: internal/store/peerasks_test.go `TestPeerAskReceive` and `TestUserAsksForeignOrder`, internal/dispatch/peerasks_test.go `TestPeerAskAnswersComeHome`.

A test of that list that has to be edited for a bead of section 6 is a finding the builder reports in its note, with why.

### 3.10. Care 4: the test plan, before any real yard

1. **In the tests**, with each bead: two stores joined as internal/dispatch/peerbeads_test.go joins them, with a test clock. Every refusal of sections 3.4 and 3.5, every row of 3.8, and the lists of 3.9.
2. **Two isolated yards on one machine**, as the trial of yardr-b35t ran: `contrib/isolated-yard/isolated-yard bare` twice for what starts no agent, listeners on free loopback ports (`127.0.0.1:0`, never 8790 or 8791), every command through each yard's `D/e`, both stopped with `isolated-yard down`. The transcript, each step's output written on the trial's bead:
   - a's bead enters a stage of a `peer` group, arrives held at b, is released by a user, advances, is held by hand with a note. On a: the `bead show` lines after each, one inbox line per event for a subscriber, `link list` on both.
   - a reads with `thread show` while b runs, then with b's server stopped (the kept copy and its sentence), then again after b is back.
   - a replies. On b: the note by `<name>@a`, the bead held for a user, `unhold` refused to a crew member and taken from a user. The same with the intake `--auto`: no hold.
   - b's flow ends, the return comes, a's bead moves; an event of b's queued after that is logged stale on a.
   - a sends the bead again after a return with a branch: b's new bead starts at the returned commit, and what it returns is taken by fast-forward. With a commit added on a's branch first: `refused`, by `yard`, nothing queued.
   - b closes an arrived bead; a sends it again under `--auto`: it arrives held, with the line. Thirty-one beads in an hour by the test's clock: the last `rate_limited`. The same closed bead's text sent as a new bead of a, so under another handle: under `--auto` it starts and has no line about the refusal, which is the limit section 3.8 states, shown so that nobody takes it for more.
   - a link that does not carry `thread` on one side, then on the other: nothing crosses, and steps 1 and 2 run as before. b on a build from before this design: `kind_unknown`, by name.
3. **One run with an agent**, since a note ends at an agent: the working yard made with `isolated-yard up` (the stand-in `claude`, proved first as the README says). A bead arrives, is released, the stand-in starts; a note arrives; `D/agents.log` shows no start between the note and a user's `unhold`, and the brief of the start after it has the marking line above the note.
4. **The pair this yard has with airy** is a separate bead and a separate step, and it is mckean's to allow: on 12 Oct 2026 he said the first real transfer is done with him, after it is built. That bead waits in `decide` until he says so, and starts with what he types on both yards: the link with airy carries `file`, `mail` and `ping` today, so first `bead` with an intake (steps 1 and 2, not yet switched on there), then `thread`.

### 3.11. Care 5: how each bead is undone

Every bead of section 6 can be switched off on a link without a revert: `yardr peer allow <peer> --no-send thread --no-receive thread`, which anyone may run, stops all three messages on that link, and beads cross as in steps 1 and 2. For the three behaviours of bead 5 that the word does not gate, the revert is the undo. Per bead, what a `git revert` of its commit leaves:

| bead | after the revert |
|---|---|
| 1 | The word `thread` is unknown again: a link that names it keeps the word in its record and carries nothing for it; a `bead-event` that waits in an outbox is refused by the other yard as `kind_unknown` and given up. The fields the record learned are ignored by the older code and dropped at the record's next write |
| 2 | The `link list` row and the source are gone. Subscriptions to `peer` stay in the store and match nothing; `yardr unsubscribe <name> peer` removes them. Events already recorded stay in the log until the prune |
| 3 | `thread show` refuses a bead and a `peer` delivery again. The kept copies are then a bucket nothing reads or empties: the bead's note names it, so that whoever reverts can drop it |
| 4 | `reply` refuses a `peer` delivery again. Notes that were written stay notes, by `<name>@<peer>`. A bead held by a note stays held as it arrived, and a user releases it by the check of step 1, which is older than this design |
| 5 | A bead sent again starts from the base again, as today. Beads held as refused before stay held and are released by a user. The count of beads in the hour is ignored |
| 5a | A note of `yardr doctor` and a paragraph: nothing in a store |
| 6, 7 | Documents and a trial: nothing in a store |

No bead migrates a record or changes a message of steps 1 and 2, so no revert needs a step in a store.

## 4. Left out

**Step 4, review elsewhere.** This design leaves room for it in one place: section 3.6 refuses to send a bead again when the asking yard's branch holds commits the other yard does not have, and says that sending them is step 4. Step 4 is then one addition to the `bead` message (the asking yard's branch pushed to the shared remote and named, as `return-branch` names the working yard's) and the working yard's check of it, which mirrors step 2 in the other direction. The thread of a bead under review elsewhere is the thread of this design, unchanged: its events, reads and notes do not ask what kind of work the stage is.

**Following another yard's events in general.** The source `peer` carries events about the beads two yards share and nothing else, so who may hear what is settled by the pair of records and needs no permission of its own. Following a peer's events in general would be the same source with more envelope types beside `bead`, and it would need what this design avoids: a statement by each operator of which of their yard's events a peer may hear. The envelope, the subscription and the inbox line would not change; the grant is the design that is missing.

**An event kind of this yard's own for progress**, heard through the source `yardr` with a default `--on`. That was step 3 before 6 Oct 2026 and was replaced then: `reply` and `thread show` have nothing to hang on there.

**Notes pushed with the events**, in place of reading on demand. It would save the `bead-read` message and make `thread show` work with the peer down. It was weighed on 6 Oct 2026 (the note on yardr-6yx8 of 11:04, the first of the four points) and reading on demand was taken: a peer's words then cross only when someone here asked for them, and the kept copy gives the offline case what it needs.

**A `noted` event across.** A note written at the working yard tells the asking yard nothing by itself. An agent writes many notes, and each would be an inbox line. A hold, a release and a stage change are told.

**Names of the working yard's people in events**, and free text in them (open question 4).

**A hook record and tables for the source**, and with them `--says` lines that would let an event of a peer report an outcome. Only the return ends a stage.

**Stored links to a peer's bead**, `tracks` on them, and a stage of this yard moving a bead of another.

**Events forwarded along a relay.** With a to b to c, a hears of b's bead and b of c's. A note from a lands on b's bead. Trust is per pair, as decided on 6 Oct 2026, and the loop guard is untouched: no new message makes a bead or is passed on.

**A reply from the working yard onto the asking yard's bead.** The working yard writes on its own bead, and the asking yard reads that.

**Taking part without asking.** Nobody takes part in a thread of the source because they moved the bead into the peer's stage. `--scope all`, with `--where`, or `thread follow` says who hears.

**A declaration or a pack for the source.** A declared source describes one service to yardr; there is one link protocol, and it is yardr's own, as `token` is. A pack that wants a crew member to hear it says `crew.<name>.subscribe.peer: all`, which packs can say for any source today.

**The crew member who may release** (`release=<peer>`). It exists, is off in this yard, and is not reopened; a note's hold goes through the same check and gains nothing from this design.

## 5. Open questions

**1. Does step 3 get a word of its own on the link, or ride on `bead`?** Option A, the word `thread`, as proposed: nothing new crosses an existing link until both operators allow it, a yard can tell its progress and take no notes (`--send thread` alone), and step 3 is switched off per link without stopping beads; it costs one more thing to allow, and a link with `bead` and no `thread` that behaves as today, which someone may read as broken. Option B, the three messages go with `bead`, as `bead-return` does: nothing to set, and every link that carries beads carries the rest on the day it is upgraded, with no switch but the revert. I would take A: "with care" is this. The link with airy carries `file`, `mail` and `ping` today (`yardr peer show airy`, 12 Oct 2026), so with A the first transfer there can be beads alone, and the thread a second step he allows.

**2. What is the source called?** Option A, one source `peer`, with the yard as a filter (`--where peer=north`), as proposed: one name joins `token` and `yardr` among those a declared source may not take, a peer may be called anything, and a rename changes a filter's value. Option B, a source per link, called by the peer's name (`yardr subscribe yardmaster north`), which is how the note of 6 Oct 2026 wrote it: shorter to type, and it costs a clash to guard both ways (a peer called `linear`, a source declared under a peer's name), and a rename that moves subscriptions, thread records and the meaning of every older event. I would take A: Linear is one source with many teams, and the filter is the mechanism that exists for that.

**3. What does a note from the peer do to a bead a user has released?** Option A, it holds the bead as it arrived again, unless the intake is `--auto`, as proposed: a peer's text reaches an agent only after a user of this yard read it, and it costs that user one `unhold` per note and ends the session at work. Option B, it lands marked and holds nothing: a conversation flows, and a peer can put text before a running agent that no one of this yard has seen, which is what the hold at arrival exists to prevent. Option C, as A, but a note by a name the operator trusts as a user lands as B: it costs widening what the trust grant does, which docs/peers.md says "releases no held bead". I would take A, and for a pair run by one person `--auto` is the setting that says so.

**4. Do events carry words?** Option A, none, as proposed: an inbox line is this yard's own sentence and a reader runs `thread show` for the words. Option B, the first 500 characters of the hold's reason or the note, as a Linear delivery carries its text: one command less to learn what is asked, and a peer's text in every subscriber's inbox, crew members included, unasked. I would take A.

**5. Is an ask (yardr-h039) the carrying for a question about a bead that is away?** Option A, confirm it, as proposed: a question with options goes as `yardr bead ask --to <person>@<peer>` from the working yard, built and tested already, and this design adds one line to its card; words go by `yardr reply`. It costs two ways to answer, with the rule that a pick is an ask and anything else a note. Option B, replace it for away beads with notes alone: one way, and no options, no pick, and nothing in the person's decisions. I would take A.

**6. A bead that this yard refused and that comes again, with an `--auto` intake: held, refused, or as any?** Option A, it arrives held and says what was refused, as proposed: a person looks once more, and `--auto` no longer means every bead starts unseen. Option B, it is refused outright (`refused_before`) until someone here says otherwise, which needs a command to say so. Option C, nothing special: `--auto` means what it says, and the hour's limit is the only guard. Options A and B find the earlier refusal by the sender's handle, which the peer chooses: they stop a flow's loop and a peer that runs yardr as it is, and a peer that sends the bead under a new handle is past both (sections 3.6 and 3.8). Option D does not go by the handle: when a person of this yard closes a bead that arrived from a peer, that link's intake stops being `--auto`, by the peer's key, and every later bead from that yard arrives held until the operator says `yardr peer intake <peer> … --auto` again; `peer show` says since when and for which bead. It costs that one refusal, perhaps of one bad bead among many good ones, stops a pair that ran unattended until the operator acts, and that A's line about the earlier bead is still worth having beside it. I would take A, with its limit written in docs/peers.md where `--auto` is described: `--auto` already means that the other yard's operator may start agents here, so a retry in disguise is given nothing new, and an operator who refuses a bead from a yard they no longer trust that far has the command for it, `yardr peer intake <peer>` without `--auto`. D is the one to take if a refusal is meant to bind the other yard and not only to inform this one.

**7. Is a paired yard's own word for its outcome enough?** Steps 1 and 2 take it, and section 3.8 says what bounds it. Option A, as it is: the peer is trusted as one of this yard's own groups is, the flow's routes and the stages after the peer's are the check, and docs/peers.md gains a paragraph saying so in those words, with the advice that a peer's `done` should lead to a stage worked in this yard. It costs nothing to build, and a flow that routes a peer's `done` straight to landing has only the assembly's tests between the other yard and the base. Option B, as A, and `yardr doctor` notes a flow in which an outcome of a peer's stage leads to a terminal stage or to one a script works: the operator is told once, in the place they look, and it costs a check that will also name flows where that is meant (a stage whose result is words). Option C, every return is held for a user of this yard before its outcome is applied: nothing a peer says moves a bead unseen, and it costs a person's step for every bead that comes back, which is most of what sending a bead away saves. I would take B, as a bead of its own after bead 5, since it is about steps 1 and 2 and not about what crosses while a bead is away.

## 6. The beads it would become

All in the depot yardr, in the order they land. Each depends on the one before it: 1 to 5 all change `receivePeer` in internal/store/peers.go and the records in internal/store/peersend.go and peerbeads.go, so two at once would conflict. Each passes `sh .yardr/test` and the list of section 3.9 unchanged, updates docs/peers.md for what it built, and names in its note what a revert of it leaves (section 3.11).

**1. A yard tells the yard it works a bead for how the bead stands.** Goal: the word `thread`, the message `bead-event`, and the record and `bead show` line of section 3.2, step 1. Where to look: `PeerKinds`, `knownKind` and `receivePeer` in internal/store/peers.go; `Peer.carries` in internal/store/peerbeads.go; where `peer_bead_arrived`, `peer_bead_released`, `peer_bead_expiring`, `advanced`, `held`, `unheld` and `gave_up` are logged for a bead with a `peerBead` record; `peerSent`, `BeadPeer.Line` and `withPeer` for the second line; `returnedBead` for how a stale message is logged; the allow form and its warning in internal/cli/peers.go. Done when: the seven events of section 3.1 each queue one message, for a bead from a peer that is not returned, over a link that sends `thread`, and none otherwise; the asking yard takes one only for an open stay of that peer, and logs any other as stale; fields outside the lists are refused; 240 an hour, then `rate_limited`; `bead show` and `--json` have the peer's handle, stage, hold and when it was said; a link without the word behaves as before on both sides; a peer from before the word refuses by name.

**2. The source `peer`: its events are deliveries, and the pair is a link row.** Goal: sections 3.2, step 2, and 3.3. Depends on 1 and on open question 2. Where to look: `source.BuiltIn` in internal/source/declaration.go; `RecordHook`, `hookDeliveries` and `recordParticipation` in internal/store for the routing a delivery goes through, and every reader of a `hook` event's `hook` field, which these have none of; `whereKeys` in internal/store/subscriptions.go for the keys; the inbox line of a delivery; `renamePeerSettings` for the rename; `Links` and `IssueLinks` in internal/store/links.go and the link commands for the computed row and its refusals; docs/subscriptions.md and docs/integrations.md (a row of the capability table). Done when: a taken `bead-event` is one delivery of the source with the envelope of section 3.2, heard by a crew member and by a user with `--scope all`, filtered by `peer`, `bead`, `depot`, `label`, `stage` and `action`, and by a follower with `--scope involved`; the inbox line holds no character of the peer's beyond the held fields; a declared source cannot be called `peer`; `peer rename` renames the filter; `link list` prints the row on both yards and `link add`, `rm` and `move` refuse it.

**3. Reading the other yard's bead.** Goal: section 3.4, and the line on a foreign ask's card of section 3.6. Depends on 2. Where to look: `threadShowCmd` and `hookEvent` in internal/cli/reply.go; the ping in internal/cli/peers.go for a command that waits for the message answering its own; `returnPeerBead` and `fitNotes` for what an answer holds and its limits; the hour's count in internal/store/peermail.go; `MailPeers` for who may; the `session_aged` notice of a peer group; the foreign ask's view in internal/store/peerasks.go. Done when: `thread show` on a delivery of the source and on a bead that is away prints the answer under the frame line; it prints the kept copy and says so when no answer comes in time; the working yard answers only for an open bead of that peer and refuses anything else as `no_such_bead`; an answer to no question is dropped; a bead's agent and a crew member without the peer are refused; the copy goes when the stay ends; a foreign ask whose bead is the counterpart of a bead of this yard says so.

**4. Answering on the other yard's bead.** Goal: section 3.5. Depends on 3 and on open question 3. Where to look: `replyCmd` and `maySpeakAs` in internal/cli/reply.go; `MailPeer` in internal/store/peermail.go for the gates, the queueing and the mail from `yard` when a message is given up; `peerNotes` and `releasePeerBead` in internal/store/peerbeads.go; `SetHold` in internal/store/beads.go for what a hold does to a session; `BeadPeer.Marking` and its two callers for the frame. Done when: `reply` on a delivery of the source and on a bead that is away queues one `bead-note`, refused before queueing for each gate of section 3.5; the working yard writes the note by `<name>@<peer>`, logs `peer_bead_noted` without text, and holds the bead for a user unless the intake is `--auto`; a crew member's `unhold` of it is refused and a user's taken; the event `held` with `held_as` `note` goes back; a note for a bead that ended or is another peer's is refused as `no_such_bead`; a note never lifts a hold or moves a bead.

**5. A bead sent again.** Goal: section 3.6, the three changes. Depends on 4 and on open question 6. Where to look: `PeerBeadSend` and `baseOf` in internal/store for the commit a second sending names; `checkBead` and `arriveBead` for the earlier records, the hold and the hour; `BeadPeer` for the lines; docs/peers.md, "When the branch cannot be had", which this makes rare. Done when: a bead sent again after a fetched return starts at the returned commit in the working yard and its next return is taken by fast-forward; with the asking yard's branch moved on, the stage ends as `refused` by `yard` and nothing is queued; the working yard's `bead show` names its earlier beads for the same sender's handle; a bead refused before by a close or by its time arrives held under `--auto`; the 31st bead of an hour is `rate_limited`.

**5a. Doctor notes a flow that lands a peer's work unseen.** Only if open question 7 is answered with its option B; it depends on no other bead of this list and is placed here so that the trial runs with it. Goal: section 3.8, "What a paired yard is trusted for", and the paragraph of docs/peers.md that says it. Where to look: the checks of `yardr doctor` (docs/doctor.md) and one that reads flows; `PeerRunner` and how a stage's route names its group, for which stages a peer works; the flow's routes for where each outcome of such a stage leads, and how a stage is known to be terminal or worked by a script. Done when: doctor prints one note per flow and stage where an outcome of a stage worked by a peer leads to a terminal stage or a script's, naming the outcome and both stages, and none for a flow where every such outcome leads to a stage of agents or people; it is a note and never a fault; docs/peers.md says what a returned outcome is and is not evidence of. A revert leaves no trace in a store.

**6. The trial in two isolated yards.** Goal: section 3.10, steps 2 and 3, with every step's output on this bead. Depends on 5. Where to look: contrib/isolated-yard/README.md; the notes of yardr-b35t for how its trial was run. Done when: the transcript ran as written, on free loopback ports, both yards stopped by pid and removed; what differed from this design is filed as beads, not mended in the trial.

**7. The pair with airy.** Goal: section 3.10, step 4. Depends on 6, and waits in `decide` for mckean: it is run with him. Done when: he allowed `bead` and then `thread` on both yards and set the intake, one bead went from one yard to the other and back with an event heard, a read, a note released by him, and the return; what he said of it is on the record bead yardr-6yx8.
