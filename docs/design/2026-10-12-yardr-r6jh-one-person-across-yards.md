# One person across linked yards: what is for them reaches them where they work, and a question with no bead has a place

Design yardr-r6jh, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr; a peer is another yard this one is linked with by its public key, and the link is that pairing as one yard holds it; a user is a person of a yard, a name and an inbox; the operator is the user who runs the yard; a crew member is one of the yard's always-on agents, and the yardmaster is the one that coordinates; a bead is one durable piece of work; an ask is one question as data on a bead, answered by picking an option, put aside, or declined; footplate is the terminal interface over yardr's socket in which a person picks. docs/glossary.md has all of yardr's words. In this document the **home yard** of an ask is the yard whose bead it stands on, and a **joined name** is a name of a linked yard that the home yard's operator has said is one of its own users (today: `yardr peer allow <peer> --trust <name>@<peer>=<user>`).

## 1. The question

On 12 Oct 2026 the yardmaster of the Mac's yard put four decisions to mckean as one mail with letters, while he worked from the yard airy in footplate. mckean, by mail from airy, 17:14: "so what's interesting is that you didn't use the new decide flow :)". Two gaps were behind it. An ask addressed to `mckean` stands in the Mac's decisions only, and he was looking at airy's, so "the same person has two addresses, and whoever asks has to know where he is sitting". And one of the four questions (whether to rename "depot") belonged to no bead, so it had nowhere to stand as an ask. Asked whether to file both as one design question, mckean, 17:16: "interesting yeah sounds good, that would perhaps go into identity management as whole, but important just hard to do correctly". So the question is not a patch for asks. It is what a person is across linked yards, and at a service's edge (design yardr-8wxe): what joins the names, who may say they are joined, what follows the person (asks, mail, notifications, the release of a bead that arrived) and what must not; and beside it, where a question about no bead stands.

## 2. What exists today

### 2.1. A person is a user of one yard

docs/users.md: a user is "a person as a party of the yard: a name and an inbox", with subscriptions and a `notify` source. Only the operator adds one. Its section "The record" says the record "is where more about a person goes later: who they are in an outside source ... and a user who comes from another yard. Neither is built." `yardr user list` in this yard prints one user, `mckean`, the operator, and under it whom the linked yard exposes: `mayor@airy` (crew) and `mckean@airy` (person, operator).

It was decided on 8 Oct 2026, and recorded as not to be reopened, that identities are not merged across yards (design yardr-h039, section 2; docs/peers.md, "Whose word an answer is"). This design stays inside that: nothing below makes two records one.

### 2.2. yardr already says "this name is that party" in six places

Each is one statement at one boundary, made by whoever relies on it, and each goes one way. None of them is called identity, and together they are what yardr has of it.

| boundary | the statement | who makes it | what proves the name | where |
|---|---|---|---|---|
| the machine | a command's actor is `--actor`, else `$YARDR_ACTOR`, else the login name, and it acts as the user of that name | whoever runs the command | nothing: "yardr does not authenticate anyone" | docs/users.md, "Who is acting" |
| a pane | a command from a crew member's pane, or a bead's session's, is that crew member's or that session's, whatever actor it names | the runner, which records the pane's shell when it starts an agent | the process the call came from | `paneCrew`, `paneSession` and `callerOf` in internal/store/caller.go, used by `mayTell` (internal/store/usernotify.go) and the release check |
| a link, inward | `mckean@airy` speaks as this yard's user `mckean` | this yard's operator, per link and per name | the peer's key signs every message; the name inside is the peer yard's word | `Peer.Trust` and `TrustedAs` in internal/store/peertrust.go; docs/peers.md, "Trusting a name as a person of this yard" |
| a link, the roster | `mckean` is a user of airy that this yard may see and write to | airy's operator (`--expose`) | the same signature | docs/peers.md, "The roster" |
| a source | a crew member is this account in GitHub or Linear | the operator (`yardr crew identity`) | the source's own credential | docs/crew.md; for a user "not built" |
| outside the yard | what lands in a user's inbox is also sent to this address in a source | the user themselves or the operator, never an agent's pane | the env file's address | `notify` and `env_file`, docs/users.md, "Told outside the yard" |

Design yardr-8wxe adds a seventh when it is built: the web view started `--as <user>` takes every request as that user, and the service's edge (Kratos and Oathkeeper) is what says which logged-in account reaches which yard's view.

### 2.3. What the link's trust grants today, and what it does not

This yard holds one such grant: `yardr peer show airy` prints `trusted as: mckean@airy -> mckean`. docs/peers.md says what it does: mail from that name is marked `(as mckean)` where it is read, and so is that name's answer to an ask. And what it does not: "The two names stay two. Nothing of the user's is the other name's: not their inbox, their subscriptions or their notify source"; it "gives none of the operator's powers"; it "releases no held bead and answers no `decide`"; it "proves the link, not the person", so "trust a name of a yard whose operator you would trust with that user's word". The grant is the operator's alone to give, the name must be on the roster the peer last said, and it is dropped, not kept and flagged, when the user is removed or the peer exposes the name no more (`untrust`, `distrusted` in peertrust.go).

The grant goes inward only. It says whose word comes in. Nothing says where a user's questions or mail go out.

`yardr chat` already took a position on that (docs/chat.md, "A name trusted as a user of this yard"): the trusted name has no row, its mail is in the conversation with the user, and "Enter writes to the user in this yard ... the trusted name is how they spoke, not where they are reached."

### 2.4. Asks, in one yard and across two

`yardr bead ask <id> "<question>" --to <user>` stores a note of kind `ask` on a bead (`AddAsk` in internal/store/asks.go); `--to` defaults to the operator. `UserAsks` (internal/store/decisions.go) is a person's queue: every open ask whose `To` equals their name, on this yard's beads, with the foreign asks beside them. footplate reads it over the socket and the web view lists the operator's open asks among what waits (docs/web.md).

Design yardr-h039 is built (docs/peers.md, "Questions for a person of another yard"; internal/store/peerasks.go). `--to mckean@airy` stores the ask and queues one message of kind `ask`; at airy it is a foreign ask in mckean's queue, named `bead@peer`, read by no agent; the pick comes home and lands as a note of kind `answer` by `mckean@airy`. Four things in it matter here:

- **It stands in one queue.** "Who decides is the writer, per ask, and there is no setting for where the person is." That design left out, by name, a standing forward of a user's asks, the home yard's queue listing such an ask as well, and any state pushed from the asking yard after the ask arrived. Its open question 2 gave the cost of "as well": one question in two windows, stale in one direction.
- **An answer is taken only for an ask addressed across.** `takeAnswer` cuts the ask's `To` at `@` and refuses with `no_such_ask` when there is no peer part. An ask to `mckean` cannot be answered from airy, whoever is trusted.
- **A late answer is told its end.** When the home yard refuses an answer as `ask_answered`, `ask_declined` or `no_such_ask`, the person's yard marks its foreign ask with that outcome and it takes no further answer (`outcomeState`, `outcomeWords`). A pick on an ask that only has an older pick is no such case: a newer pick supersedes, by note order.
- **Who may send.** The link must carry `ask` out; a crew member needs the peer in its `peers` setting; a bead's agent may not ask across.

Ask 2600 on this bead went `--to mckean@airy` and was answered there. The four decisions of that afternoon went as mail.

### 2.5. Mail

`yardr mail <user>` fills the user's inbox in this yard; `yardr mail <name>@<peer>` is queued for the other yard (`MailPeer` in internal/store/peermail.go). A crew member answers with `yardr reply <seq>`, which goes to the sender of that mail, so an answer to mail that came from `mckean@airy` goes back to airy. Mail written first to `mckean` stays here. Whether mail is read is one position per user per yard (`mail_read`), and nothing of it crosses.

### 2.6. What tells a person who is looking at neither yard

`notify` sends what lands in a user's inbox to a phone: "Mail to the user ... and the notifications of their subscriptions ... Nothing else, and nothing that is not in the inbox" (docs/users.md). An ask is a note on a bead and not in the inbox; internal/store/pushes.go queues a push at two places, a mail and a notification. So an ask to a user is told on no phone unless the user subscribed to the bead's `noted` events. The starter pack's yardmaster role says the same from the other side: "Nothing nudges a person: they read mail when they look".

### 2.7. Acts, and who may do them

Moving a bead out of a manual stage is checked by `checkMembers` (internal/store/routing.go), which goes by the name the command gives. A bead that arrives from a peer is held, and `releasePeerBead` lets only a user of the yard lift the hold, going by the pane before the name. Design yardr-evd6, approved today and not yet built, takes that hold out with its bead 0: the first stage of the intake's flow is the review, its group's members are who may release, and the member check learns to go by the pane. It says of a trusted name: it "changes how a note's writer is shown ... and nothing else". The operator's commands (`peer allow`, `user add`, `source set`, `crew set`) go by the actor of this yard.

No message a link carries moves a bead, lifts a hold or runs a command in the yard that takes it, with one exception that the receiving operator switches on: a `bead` message makes a bead in the intake's flow.

### 2.8. A question that is about no bead

An ask needs a bead: `yardr bead ask <id> <question>`. The decide stage's file of the starter flow ends: "A decision that is not about one piece of work can be a bead created straight into `decide`, closed with the answer as its reason". That is two commands (`yardr bead create ... --stage decide`, then `yardr bead ask`), and a third to close. `yardr bead create` without `--depot` makes a bead with the prefix `yd`, under no flow, in the stage `new`, which no route leads from (`CreateBead` in internal/store/beads.go: "'new' is only for beads no flow governs"). The yardmaster's role file, in the starter pack and in the pack this yard runs, says "Anything that is {{operator}}'s to decide goes to stage `decide` with the question and the options in the bead", and does not name `yardr bead ask`.

## 3. The proposal

### 3.1. What a person is, in four rules

1. **A person is a user of a yard, once per yard they work in.** `mckean` here and `mckean` at airy are two records, each with its own inbox, subscriptions, notify source and powers. Nothing below changes that.
2. **A join is a statement on one link, by the operator of the yard that relies on it, about one name, for that yard alone.** It is the trust grant that exists. The other yard does not make it and cannot ask for it; all it contributes is its roster, which says the name is a person there. Each yard states its own side: for an ask at airy to reach him at the Mac, airy's operator joins `mckean@mac` to airy's `mckean`. A join does not chain: what the Mac sends to airy is never sent on by airy.
3. **What is said to the person follows them, and what they say back counts as theirs.** A question for them is also put where they work, and their answer from there lands on the bead. That is the whole of what follows.
4. **Nothing that acts follows.** A move, a release, an operator's command, a setting, a secret: each is done in a yard, by an actor of that yard, under that yard's checks. A link carries words to a person and words back. A person who wants to act in the other yard acts there: in its terminal, through its socket forwarded over ssh (design yardr-h039, section 3.1), or through its web view at an edge (design yardr-8wxe).

| of the person | follows across a join? | by what |
|---|---|---|
| an ask addressed to them | yes | section 3.3 |
| their answer, Later or Decline to that ask | yes, and it is marked `(as <user>)` | the answer path of yardr-h039, with one check changed |
| that the ask ended in the home yard | yes | section 3.4, open question 1 |
| mail written first to them | open question 5; this draft says later, as its own step | section 3.6 |
| being told on a phone | the phone is with the person already; the gap is that an ask is not told at all | section 3.7, open question 6 |
| notifications of their subscriptions | no | they are one yard's events, and each yard has its own |
| the inbox, its read positions, the subscriptions, `notify` and its env file | no | the two records stay two |
| being the operator; being a member of a group; releasing a bead that arrived; moving a bead out of `decide` | no | rule 4; section 3.8 |

### 3.2. The join: the grant that exists, read in both directions

Today `mckean@airy -> mckean` says: its word is mckean's. Proposed (open question 2): the same grant also says where mckean is reached, so one statement joins the two names.

The lines a command prints, here and below, are proposed wording: today the last line ends at `mckean`, and `yardr bead ask` answers `ask 2600 on yardr-r6jh`.

```text
$ yardr peer allow airy --trust mckean@airy=mckean
peer airy: send ask, file, mail, ping; receive ask, file, mail, ping; expose mckean, yardmaster
trusted as: mckean@airy -> mckean: its word is mckean's, and an ask for mckean is also sent there
```

Read both ways, the grant authorizes two things it does not authorize today, and whoever gives it gives both. The first is a disclosure: the other yard is sent the text of every question asked of mckean in this yard, with the title and cut body of the bead it stands on, without anyone in this yard choosing that per question. Today a yard whose word is taken as mckean's can write to the yardmaster as mckean and be answered, but it is told only what it asks about and what a writer addresses to it. The second is an answer path: the other yard can answer each of those questions as him, where today it can answer only an ask a writer addressed `--to mckean@airy`. Neither follows from taking a yard's word. Trust as it stands is about what comes in; this adds what goes out, and widens what its word may be given on. Section 3.8 says the same from the side of what must not follow.

The case for one grant all the same is that both statements are about the same judgement of the same operator: whoever runs the other yard can read what is sent there and can write as any name of it. An operator who takes that yard's word as mckean's has already relied on that; sending it his questions relies on it further, in a new direction. Whether that is one decision or two is open question 2, and the paragraphs below are written for the one grant. The grant keeps every rule it has: the operator's alone to give, anyone's to take back, only a name on the peer's roster, dropped with the user or with the roster entry. One rule is added: a question is copied only to a name the roster says is a person, since a crew member has no decisions queue.

`yardr peer show` and `yardr doctor` print the grant with the new half of the sentence, so an operator who gave it before this design reads what it now does. In this yard that is one grant.

### 3.3. An ask for a user is also sent to each joined name

The writer addresses the person, not a place: `--to mckean`, which is the default. In the write that stores the ask, the yard queues one message of kind `ask` for each joined name of that user whose link carries `ask` out. The message is the one yardr-h039 built, unchanged: the bead's handle, title, topic, stage and cut body, the ask whole, the about-beads, addressed to the name as the peer knows it. At airy it is a foreign ask in mckean's queue like any other, so airy needs no change to show it or to answer it, and a peer on a build that knows `ask` takes it today.

```text
$ yardr bead ask yardr-r6jh "Draft it now, or keep it parked?" --context "..." --option "Draft it now: ..." --option "Keep it parked: ..."
ask 2600 on yardr-r6jh for mckean; also queued for mckean@airy
```

The ask stays what it was in the home yard: stored with `To: mckean`, in the Mac's queue, in prime, review, chat and the web view. It now stands in both queues, and he picks in whichever he is looking at.

**The answer.** `takeAnswer` learns one more case. Today it takes an answer only for an ask addressed `name@peer`. It also takes one for an ask addressed to a user of this yard when the link it came over joins the answering name to that user at the moment it arrives. Here trust does gate, where for an ask addressed across it gates nothing: the yard solicited the answer by the join, so the join must stand. Anything else stays `no_such_ask`, the one reason that tells nothing. The note is written by `mckean@airy` and shown `(as mckean)`, as the note on this bead already reads: "mckean@airy (as mckean) picked 1 (Draft it now)". `ResolveAsks` then has the ask answered, and it leaves the Mac's queue.

**Who may send a copy.** The link must carry `ask` out, as for any ask. The writer's own gates are not asked (open question 4): the writer addressed a user of this yard, and it is the operator's statement that sends the copy. So an ask by a crew member without the peer in its `peers` setting is copied, and so is an ask by a bead's agent, which may not itself ask another yard. What crosses is read by one person and by no agent there (docs/peers.md: "It appears in no agent-facing surface").

**A copy that is not delivered.** The link down past its tries, a refusal, the peer's limit of 30 mails and asks an hour: the copy is given up as any message is. The ask is untouched in the home yard. The one told is the person it is for, by mail from `yard` in their inbox here, not the asker, who addressed nobody at the other yard.

**When the grant is given or taken.** Giving it copies nothing that is already open (open question 7); the command says how many open asks for that user stay in this yard alone. Taking it back sends nothing either: an answer that arrives afterwards is refused as `no_such_ask`, and the other yard marks the question "it is no longer asked", by what is built.

**Three yards.** A user joined on two links gets the copy at both. A foreign ask is never copied on, so nothing loops, and trust stays per pair as it was decided on 6 Oct 2026.

### 3.4. The home yard says when a copied ask ended there

Without this, a question he answers at the Mac stays open at airy. If he later picks it there, that pick lands as the newer one and replaces what the asker may already have acted on. This is the staleness yardr-h039 named, and the reason it kept an ask in one queue.

Proposed (open question 1): when a copied ask stops being open in the home yard by an answer that did not come from that peer (a pick, words, or a decline, by hand here or from a third yard), the home yard queues one more message of kind `ask` for each other joined name. It names the bead and the ask's seq, as an answer does, and says `settled: answered` or `settled: declined`. The yard that takes it finds its foreign ask by the peer's key, the bead and the seq (`foreignAskOf`) and marks it with that outcome, by the marking a refused late answer gets today: it moves to the decided, with "it was answered in mac's yard", and takes no further answer. To change a pick after that he picks again in the home yard.

A settle carries no text of anyone's, moves nothing and answers nothing. One that names no foreign ask of that peer is dropped. An older build refuses it as it refuses a malformed answer, the home yard gives it up, and nobody is told: nothing waits on it. Later (put aside) is not told across: a question put aside at the Mac is still a question at airy.

### 3.5. A question that is about no bead

`yardr bead ask --new` makes the bead and the ask in one write. The question is the bead's title; `-b` gives a body.

```text
$ yardr bead ask --new "Rename 'depot'?" --label topic:words \
    --context "The glossary keeps it as the yard's word for a repository." \
    --context "footplate and the web view both print it." \
    --option "Keep depot: nothing changes" \
    --option "Rename it: every command, page and document changes once"
yd-4k2p created; ask 2611 on it for mckean; also queued for mckean@airy
```

Without `--depot` the bead is of no depot and under no flow, in the stage `new`, where nothing routes it: it exists to carry the question (open question 3 has the other place, a depot's `decide` stage). It shows in his decisions under its topic, as every ask does, in both yards. When the answer is on it the asker closes it with what it did with the answer as the reason, as the decide stage's file says. Nothing new is stored and no new kind of bead exists: `--new` is `bead create` and `bead ask` together, refused whole when either part would be.

The other half of that afternoon is the role. The yardmaster's role file gets its "Decisions" line rewritten: a decision that is a person's goes as an ask (`yardr bead ask`), on the bead it is about, or with `--new` when it is about none; addressed to the person by name; and several decisions are several asks, never one mail with letters. The stage `decide` stays for a bead that cannot go on until a person has moved it.

### 3.6. Mail written first

Mail already follows a conversation: `yardr reply` goes to where the person wrote from. What stays behind is mail written first to `mckean`. The whole design has it follow as an ask does: a copy queued for each joined name, shown at airy as mail from `yardmaster@mac`. To be correct it needs the read position to cross as well (a message that says "read there through this mail"), or the Mac counts it unread for ever and tells his phone of mail he read at airy. And it turns round what `yardr chat` says today, "the trusted name is how they spoke, not where they are reached".

This draft places it after asks, as its own step, to be confirmed once asks have followed him for a while (open question 5). The pattern would be the one of sections 3.3 and 3.4, so nothing built for asks blocks it.

### 3.7. An ask is told on the phone

A user with `notify` is told of an ask for them as of mail (open question 6): title `yardmaster asks`, text the question, cut as mail's text is, by the same rules of docs/users.md (20 seconds and still open, one message for a burst, 20 an hour). The yard that puts the ask in the person's queue tells it, for its own asks and for foreign ones. Where two joined yards both have a notify source for the same person, one question is told twice; the person sets `notify` in the yard they want to hear from. This is the one part of the design that reaches a person who is looking at no yard at all, and it is independent of every other part.

### 3.8. What must not follow, and what holds it

| what | what holds it today | what this design adds |
|---|---|---|
| The operator's commands | they go by the actor of this yard, and where it matters by the pane | nothing; no message carries one |
| Membership of a group, and with yardr-evd6's bead 0 the release of a bead that arrived | `checkMembers` compares names of this yard, and no actor of a yard has `@` in its name | nothing; a joined name is no member, and a test says that an answer from a joined name on a bead in a manual stage leaves the bead in its stage |
| Moving a bead out of `decide` | the same check | nothing. An answer is words on the bead; whoever acts on them, a crew member or the person, acts under their own permissions in that yard, as docs/peers.md says of trusted mail |
| The inbox, the read positions, subscriptions, `notify` and its env file | they are fields of one yard's record | nothing is copied but one ask, and with section 3.4 the fact that it ended |
| A secret (design bead yardr-0qs5) | nothing crosses | nothing |
| A second hop | a foreign ask is no bead and no local ask | it is never copied on |
| Another person's questions | the copy is made per ask, from that ask's `To` | a test that an ask for another user of the yard queues nothing for a name joined to the first |

What the join does widen, said plainly: airy's yard now holds the text of every question asked of mckean at the Mac, with the title and cut body of its bead, and airy's yard can answer each as him. Both are new. The disclosure is new outright: until now airy held only the questions a writer chose to address there. The answer path existed for those asks alone and now covers every ask for him, including one whose writer did not think of airy at all. The rule the grant states today is widened to match, and docs/peers.md gets it in those words: join a name of a yard whose operator you would trust with that user's word, and now also with every question put to them here and with the answer to each.

### 3.9. The service's edge

Design yardr-8wxe puts a person at a hosted yard through its web view, started `--as <user>`, behind an edge that knows who is logged in. Under rule 4 that is the way a person acts in a yard they are not sitting in: they act in it, as its user, and the edge is the boundary's statement of who they are. It needs nothing from this design and gives up nothing to it. A customer who also runs a yard of their own and links the two has a join as in section 3.2, made by each yard's operator for its side. Several people behind one edge, each a user of one yard, is that design's open question 4 (a header mapped to a user) and is not taken up here.

### 3.10. The smaller first step

Section 3.3 alone: an ask to a user is also sent to each name a link trusts as that user, and that name's answer is taken. One bead, and a trial in two isolated yards. It changes `AddAsk`, `takeAnswer` and three lines of output, and needs no change at the other yard.

What it leaves, and what he would live with until the rest is built: a question answered at the Mac stays open at airy, and picking it there replaces the answer (section 3.4); a question about no bead still takes three commands; nothing tells his phone of an ask; mail written first stays where it was written. Open question 1 is the choice between this step and the rest.

## 4. Left out

- **A merged identity**: one record for a person across yards, a shared inbox, a person's name that is the same thing everywhere. Decided against on 8 Oct 2026, and every rule above is a statement on a link instead.
- **Where the person is.** No setting for it and no guess from their last command, socket call or acknowledgement. A setting has to be flipped by the person each time they move, and a guess is wrong exactly when it matters, when they have just sat down elsewhere. Sending to every joined name and saying when it ended needs neither (open question 8 keeps the option).
- **A join the other yard, or the person there, can make or ask for.** A yard that could say "I am your mckean" would be taken at its word.
- **A person acting across a link**: releasing, moving, deciding a stage, or running an operator's command in the other yard by a message. Rule 4.
- **The home yard's queue listing asks addressed `--to mckean@airy`.** An ask addressed to a place stays in that place alone, as yardr-h039 decided. After this design the writer who means the person writes `--to mckean`.
- **Later told across**, and the asker's next note bringing a put-aside question back at the other yard: as yardr-h039 left it.
- **Notifications of subscriptions following.** They are a yard's events in bulk; a person who wants a yard's events on their phone sets `notify` there.
- **Users who come from another yard**, the second thing docs/users.md says the record is for: a person of airy made a user of the Mac by the link. A join is between two users that exist.
- **A team behind one edge**, and a login built into yardr.
- **A reach without trust**: being told at a yard whose word is not taken as yours. Open question 2 has it as the option not taken.
- **A new type or flow for question beads**, and a command that lists the decisions in a terminal.

## 5. Open questions

**1. How much is built: the small step, asks whole, or everything?** Option A, the small step of section 3.10: one bead; it costs stale open rows at the other yard, where a late pick replaces the answer. Option B, asks whole: the copy, the answer, the settle of section 3.4, `bead ask --new` and the role's line; four beads and a trial, and questions then follow him without a row left behind. Option C, B and also the phone and mail (sections 3.6 and 3.7): two more beads, and for mail a read position that crosses, which is the part most likely to be got wrong. I would take B, and decide the phone with question 6 and mail with question 5.

**2. Is the join the trust grant, or a second grant beside it?** What is being decided is whether taking a yard's word as a user's also authorizes sending that yard every question put to the user here, and taking its answer to each (section 3.2). Option A, the one grant read both ways, as proposed: nothing new to give. It costs that a grant given for attribution alone becomes, without anyone giving it again, a grant to disclose and to be answered: the one this yard holds starts sending his asks, with their beads' titles and cut bodies, to airy on the day this is built, and `peer show` and doctor saying so afterwards is a notice, not a consent. It also costs that the two cannot be had apart: an operator who wants mail from a yard marked as the user's, but not that yard's operator reading the user's questions, has to take the grant back. Option B, a second word (`--reach mckean@airy=mckean`), the operator's alone to give as trust is, and accepted only for a name already trusted, since an answer from an untrusted name would be nobody's word: an existing grant discloses nothing until the operator says so, and trust without reach stays what it is today. It costs a second list per link, dropped with the first and with the roster entry, one more question in the pairing form, and bead 1 grows by that command and its lines. The difference matters where the other yard is run by someone else, such as a hosted yard of design yardr-8wxe or a colleague's; where both yards are the same person's, as the Mac and airy are, it matters only on the day of the build. I would take A, because every join this design was asked about is between one person's own yards and a second list is a second thing to get wrong; an operator who expects to trust a name of a yard that is not theirs should take B.

**3. Where does a question about no bead stand?** Option A, a bead of no depot under no flow, closed by its asker, as proposed: it is in nobody's line and in no stage's count, and it costs open `yd-` beads when an asker forgets to close. Option B, as A, and the yard closes the bead itself when its only ask is answered with words, declined, or picked with an option other than "You decide", with the answer as the reason: no leftovers, at the cost of a bead that knows it was made by `--new` (one metadata key) and of closing on a pick the person may still change. Option C, `--new` requires `--depot` and creates the bead in that depot's `decide` stage, as the stage file describes: it shows in `yardr review` too, and a question about the yard as a whole has to be filed under some depot. I would take A, with `--depot` and `--stage` accepted for whoever wants C for one question.

**4. Is an ask by a bead's agent, or by a crew member not allowed the peer, copied?** Option A, yes, as proposed: every ask for the person follows, and the rule is the operator's join; it costs that text written by a bead's agent reaches another yard, where one person and no agent reads it. Option B, only asks whose writer could have asked that yard itself: the rules of yardr-h039 stay whole, and the questions of builders, which are the ones that hold work up, stay behind. I would take A.

**5. Does mail written first follow?** Option A, not now; later as its own design step with the read position crossing, as proposed. It costs that the yardmaster's first mail still waits at the Mac, which the phone covers where `notify` is set. Option B, now, as a copy without the read position: quick, and the Mac then counts and pushes mail he has read at airy. Option C, never: mail follows the conversation by `reply`, a question is an ask, and what must reach him anywhere is the phone's. I would take A, and would not take B.

**6. Is an ask told on the phone?** Option A, yes, by the yard that put it in his queue, as section 3.7: questions are what waits for him, and it costs a possible second message where two joined yards both have `notify`. Option B, yes, but a copied ask is told by the home yard alone (the message says it is a copy): no doubles, and a person whose home yard has no `notify` hears nothing of copies. Option C, no: he subscribes to `noted` where he wants it, as today. I would take A.

**7. When the grant is given, are the asks already open sent?** Option A, no, as proposed: the command says how many stay here. Option B, yes, up to the peer's limit of 30 an hour: nothing is left behind, and giving a grant becomes a command that sends a burst of bead text. I would take A; in this yard the grant exists and the question is only what the build's first day does, which is nothing.

**8. To every joined name, or to one?** Option A, every one, as proposed: no notion of where he is; with the settle of section 3.4 a question is open everywhere until it is answered anywhere. Option B, one place at a time, set by him (`yardr user set mckean at=airy`): one queue, no settle to build, and a setting that is wrong whenever he forgets it. I would take A.

## 6. The beads it would become

All in the depot yardr, in the order they would land. Beads 1 and 2 change `AddAsk`, `takeAnswer` and the take path in internal/store/asks.go and peerasks.go, which the chain of design yardr-evd6 also touches (its bead 3 adds a line to the foreign ask's card): they land between two beads of that chain, not beside one. Each passes `sh .yardr/test` and updates docs/peers.md, "Trusting a name as a person of this yard" and "Questions for a person of another yard", for what it built.

**1. An ask for a user is also sent to each name a link trusts as them, and that name's answer is taken.** This is the small step. Goal: section 3.3, and the grant's sentence of 3.2. Where to look: `AddAsk` in internal/store/asks.go (the branch for `outside` builds the `peerAsk` body and calls `queuePeer`); `Peer.Trust`, `TrustedAs` and `Trusted` in internal/store/peertrust.go; the roster's `kind` for the person-only rule; `takeAnswer` in internal/store/peerasks.go, where an ask whose `To` has no `@` is refused; `askUndelivered` for who is told of a copy given up; the `trusted as` line in internal/cli/peers.go, the question `askTrust` puts in the pairing form (internal/cli/pairform.go) and the peers line of `yardr doctor`; the `ask` command's answer in internal/cli/beads.go. Done when: an ask to a user queues one `ask` message for each joined person-name whose link carries `ask` out, and none for a crew name, for another user, or over a link that does not; an ask by a bead's agent and by a crew member without the peer is copied (open question 4); the answer of a joined name lands as a note by `name@peer`, is shown `(as <user>)`, and takes the ask out of the home queue; with the grant taken back before the answer arrives it is refused `no_such_ask`; an answer from a name that is not joined is refused as today; a copy given up is told to the person by mail from `yard`; an answer on a bead in a manual stage leaves the bead there (section 3.8); `peer show` and doctor print the grant's new sentence. Depends on open questions 2 and 4.

**2. The home yard says when a copied ask ended.** Goal: section 3.4. Depends on 1 and on open question 1. Where to look: `AddAnswer` and `answerable` in internal/store/asks.go for where an ask stops being open; `takeAnswer` for the same when the answer came from a peer; `foreignAskOf`, `outcomeState` and `outcomeWords` in internal/store/peerasks.go for the marking; `checkAsk` for how a message of kind `ask` is told apart. Done when: a pick, words or a decline here, or from a third yard, queues one settle for each other joined name the ask was copied to, and Later queues none; the taking yard marks its foreign ask answered or declined with the words that say where, and refuses a later answer to it before anything is queued; a settle for no foreign ask of that peer is dropped; a settle given up tells nobody; a two-store test covers a pick in each yard on one ask, in both orders.

**3. `yardr bead ask --new`, and the yardmaster's role says a decision is an ask.** Goal: section 3.5. Depends on open question 3 and on no other bead. Where to look: the `ask` and `create` commands in internal/cli/beads.go; `CreateBead` in internal/store/beads.go and `AddAsk` for one write; packs/starter/roles/yardmaster.md ("Decisions", and "Users", which says what cannot wait goes to stage `decide`), and every other pack's yardmaster role that has those lines; the decide stage's file of the starter flow. Done when: `bead ask --new "<question>"` makes a bead titled with the question and the ask on it in one write, and makes nothing when the ask would be refused; `--depot`, `--stage`, `--label` and `-b` place and fill the bead as `bead create` does; the command prints the bead and the ask; the role files say what section 3.5 says; the role file a running yard uses is its operator's to update, and the bead's note says so.

**4. An ask is told on the phone.** Only if open question 6 is answered with A or B. Goal: section 3.7. Depends on no other bead. Where to look: internal/store/pushes.go, where a push is queued for a mail and for a notification; `deliverAsk` in internal/store/peerasks.go for a foreign ask's arrival; the table in docs/users.md, "What leaves the machine". Done when: an open ask for a user with `notify` is told once under the rules mail is told by, and not when it was answered within the 20 seconds; a foreign ask is told by the yard that took it; the table has its row.

**5. The trial, in two isolated yards, then the pair with airy.** Goal: prove beads 1 to 3 before he relies on them. Depends on 1, 2 and 3, since its transcript has steps for each: under option A of open question 1 there is no bead 2, and the trial depends on 1 alone and runs without the steps for the settle and for `--new`. Where to look: contrib/isolated-yard/README.md (`isolated-yard bare` twice, listeners on `127.0.0.1:0`, every command through each yard's `D/e`). Done when the transcript ran and each step's output is on the trial's bead: yard a joins `ada@b` to its `ada`; an ask for `ada` at a stands in both queues; picked at b, it is answered at a `(as ada)`; a second ask picked at a is decided at b with the words that say where; a pick at b after that is refused; an ask for another user of a queues nothing; the grant taken back, an answer from b is refused and b's row says it is no longer asked; `bead ask --new` at a stands in b's queue under its topic. The pair with airy is a step of its own after that and waits for mckean: the grant exists there already, so it begins with him reading `yardr peer show airy`.

**Later, each to be confirmed before it is shaped:** mail written first follows, with its read position (open question 5); a user who comes from another yard; the edge's header for a team (design yardr-8wxe, open question 4).
